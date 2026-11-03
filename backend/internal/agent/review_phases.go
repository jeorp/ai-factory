package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// plan lists the changed files one at a time and groups them by directory.
func (run *reviewRun) plan(ctx context.Context) error {
	in, review, cp := run.in, run.review, &run.cp
	count := 0
	for _, step := range review.Steps {
		count += len(step.Files)
	}
	max := review.TotalFiles
	if max > github.MaxReviewFiles {
		max = github.MaxReviewFiles
	}
	for count < max {
		// One file per bounded response prevents unrelated large patches from
		// overflowing an entire metadata page. Raw patches are discarded here.
		files, n, err := run.gh.ReviewFiles(ctx, in.GitHubToken, in.Session.Target, count+1, 1)
		cp.Metrics.ToolCalls++
		cp.Metrics.FetchedBytes += n
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			// Preserve a placeholder so pagination remains stable and report the gap.
			review.Steps = append(review.Steps, domain.ReviewStep{Directory: "取得失敗", State: "partial", Files: []domain.ReviewFile{{Index: count, Done: true}}})
			run.limit(fmt.Sprintf("変更ファイル%d: 一覧・差分を取得できませんでした（%s）。", count+1, err.Error()))
		} else {
			if len(files) != 1 {
				return errors.New("pr_file_list_incomplete")
			}
			if err := run.planFile(files[0], count); err != nil {
				return err
			}
		}
		count++
		if err := run.check(ctx); err != nil {
			return err
		}
		if err == nil {
			if cache, ok := run.r.GitHub.(reviewFileCache); ok {
				cache.PutReviewFile(ctx, in.Session.Target, review.Base, review.Head, count-1, files[0])
			}
		}
		if err := run.save(ctx, fmt.Sprintf("レビュー計画: %d / %d ファイル", count, max)); err != nil {
			return err
		}
	}
	if review.TotalFiles > max {
		run.limit(fmt.Sprintf("先頭%dファイルを計画しました。残り%dファイルは規模上限により未確認です。", max, review.TotalFiles-max))
	}
	review.Phase = "diff"
	return run.save(ctx, "レビュー計画を保存しました")
}

// planFile adds one changed file to the step for its directory.
func (run *reviewRun) planFile(f github.ChangedFile, index int) error {
	review := run.review
	if len(f.Filename) > 512 || len(f.PreviousFilename) > 512 || f.Filename == "" {
		return errors.New("invalid_pr_file")
	}
	dir := path.Dir(f.Filename)
	idx := -1
	for i := range review.Steps {
		if review.Steps[i].Directory == dir && len(review.Steps[i].Files) < ReviewMaxFiles {
			idx = i
			break
		}
	}
	if idx < 0 {
		review.Steps = append(review.Steps, domain.ReviewStep{Directory: dir, State: "pending", Files: []domain.ReviewFile{}})
		idx = len(review.Steps) - 1
	}
	step := &review.Steps[idx]
	if step.State == "excluded" {
		step.State = "pending"
	}
	step.Files = append(step.Files, domain.ReviewFile{Path: f.Filename, PreviousPath: f.PreviousFilename, Status: f.Status, Index: index})
	if excludedReviewFile(review.Scope, f.Filename, f.PreviousFilename) {
		file := &step.Files[len(step.Files)-1]
		file.Excluded, file.Done = true, true
		allExcluded := true
		for _, planned := range step.Files {
			allExcluded = allExcluded && planned.Excluded
		}
		if allExcluded {
			step.State = "excluded"
		}
	}
	return nil
}

type pendingPatch struct {
	step, file, next int
	done             bool
}

// diffBatch collects patch fragments sent to the model in one invocation, and
// the file cursors that advance once that invocation succeeds.
type diffBatch struct {
	patches []reviewPatch
	pending []pendingPatch
}

// flush reviews the collected fragments and only then advances their cursors.
func (run *reviewRun) flush(ctx context.Context, b *diffBatch) error {
	review := run.review
	if len(b.patches) == 0 {
		return nil
	}
	prior := ""
	seen := map[int]bool{}
	for _, item := range b.pending {
		if !seen[item.step] {
			prior += domain.Clip(review.Steps[item.step].Summary, 2048) + "\n"
			seen[item.step] = true
		}
	}
	d, err := run.analyze(ctx, reviewTask{Mode: "diff", Patches: b.patches, Prior: domain.Clip(prior, 6000)})
	if err != nil {
		return err
	}
	if err := run.check(ctx); err != nil {
		return err
	}
	for _, item := range b.pending {
		step := &review.Steps[item.step]
		f := &step.Files[item.file]
		f.Offset, f.Done = item.next, item.done
		step.Summary = domain.Clip(d.Summary, 2048)
	}
	for si := range seen {
		markStepDone(&review.Steps[si])
	}
	b.patches, b.pending = nil, nil
	return run.save(ctx, "差分の区切りと次の取得位置を保存しました")
}

// diff reviews each planned file's patch in bounded fragments.
func (run *reviewRun) diff(ctx context.Context) error {
	review := run.review
	batch := &diffBatch{}
	for si := range review.Steps {
		step := &review.Steps[si]
		// Older saved plans have five-file groups: combine consecutive groups
		// in the same directory without changing their saved cursor identities.
		if si > 0 && step.Directory != review.Steps[si-1].Directory {
			if err := run.flush(ctx, batch); err != nil {
				return err
			}
		}
		for fi := range step.Files {
			if step.Files[fi].Done {
				continue
			}
			if err := run.diffFile(ctx, batch, si, fi); err != nil {
				return err
			}
		}
	}
	if err := run.flush(ctx, batch); err != nil {
		return err
	}
	for si := range review.Steps {
		markStepDone(&review.Steps[si])
	}
	review.Phase = "cross"
	return run.save(ctx, "ファイルをまたぐ不整合を確認します")
}

// changedFile returns the planned file and its patch from the cache or GitHub.
func (run *reviewRun) changedFile(ctx context.Context, f *domain.ReviewFile) ([]github.ChangedFile, bool, error) {
	in, review, cp := run.in, run.review, &run.cp
	if cache, ok := run.r.GitHub.(reviewFileCache); ok {
		if cached, hit := cache.GetReviewFile(ctx, in.GitHubToken, in.Session.Target, review.Base, review.Head, f.Index); hit {
			cp.Metrics.CacheHits++
			return []github.ChangedFile{cached}, true, nil
		}
	}
	files, n, err := run.gh.ReviewFiles(ctx, in.GitHubToken, in.Session.Target, f.Index+1, 1)
	cp.Metrics.ToolCalls++
	cp.Metrics.FetchedBytes += n
	return files, false, err
}

// diffFile adds the unreviewed part of one file's patch to the batch,
// flushing whenever the batch is full or the file continues past it.
func (run *reviewRun) diffFile(ctx context.Context, batch *diffBatch, si, fi int) error {
	in, review := run.in, run.review
	step := &review.Steps[si]
	f := &step.Files[fi]
	if run.budgetExhausted() {
		return errors.New("review_run_budget_resume")
	}
	if len(batch.patches) >= ReviewMaxFiles {
		if err := run.flush(ctx, batch); err != nil {
			return err
		}
	}
	if step.State != "partial" {
		step.State = "running"
	}
	if err := run.check(ctx); err != nil {
		return err
	}
	if err := run.save(ctx, fmt.Sprintf("差分レビュー %d / %d: %s", si+1, len(review.Steps), f.Path)); err != nil {
		return err
	}
	files, cacheHit, err := run.changedFile(ctx, f)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		run.limit(f.Path + ": 差分取得失敗（" + err.Error() + "）。")
		f.Done, step.State = true, "partial"
		return run.save(ctx, "取得できなかった範囲を保存しました")
	}
	if len(files) != 1 || files[0].Filename != f.Path || files[0].Status != f.Status || files[0].PreviousFilename != f.PreviousPath {
		return errors.New("pr_changed_create_new_review")
	}
	if err := run.check(ctx); err != nil {
		return err
	}
	if cache, ok := run.r.GitHub.(reviewFileCache); ok && !cacheHit {
		cache.PutReviewFile(ctx, in.Session.Target, review.Base, review.Head, f.Index, files[0])
	}
	patch := files[0].Patch
	if (github.Read{Kind: "file", Path: f.Path}).Validate() != nil || patch == "" {
		run.limit(f.Path + ": バイナリ・生成ファイル、またはGitHubから差分が返らないため未確認です。")
		f.Done, step.State = true, "partial"
		return run.save(ctx, "ファイルの確認状況を保存しました")
	}
	if !completePatch(patch) {
		run.limit(f.Path + ": 差分が不完全なため、返された範囲だけを確認します。")
		step.State = "partial"
	}
	if f.Offset >= len(patch) {
		f.Done = true
		return run.save(ctx, "ファイルの確認状況を保存しました")
	}
	for offset := f.Offset; offset < len(patch); {
		part, next := nextReviewPatch(f.Path, patch, offset, batch.patches)
		if next == offset {
			if err := run.flush(ctx, batch); err != nil {
				return err
			}
			continue
		}
		if part.Patch == "" {
			run.limit(f.Path + ": 1行が差分入力上限を超えるため未確認です。長い行をスキップしました。")
			step.State = "partial"
			f.Offset, f.Done = next, next == len(patch)
			if err := run.save(ctx, "差分入力上限による未確認範囲を保存しました"); err != nil {
				return err
			}
		} else {
			batch.patches = append(batch.patches, part)
			batch.pending = append(batch.pending, pendingPatch{si, fi, next, next == len(patch)})
			if next < len(patch) || len(batch.patches) >= ReviewMaxFiles {
				if err := run.flush(ctx, batch); err != nil {
					return err
				}
			}
		}
		offset = next
	}
	return nil
}

// cross checks consecutive steps' summaries together for inconsistencies
// that span files.
func (run *reviewRun) cross(ctx context.Context) error {
	review := run.review
	for review.CrossCursor < len(review.Steps) {
		end := review.CrossCursor
		for end < len(review.Steps) {
			body, _ := json.Marshal(review.Steps[review.CrossCursor : end+1])
			if len(body) > 128*1024 || reviewTokens(string(body)) > ReviewDiffTokens {
				break
			}
			end++
		}
		if end == review.CrossCursor {
			return errors.New("ai_input_limit")
		}
		prior := review.CrossSummary
		for _, f := range review.Findings {
			for _, rf := range reviewFiles(review.Steps[review.CrossCursor:end]) {
				if rf.Path == f.Path && len(prior) < 3500 {
					prior += "\n" + f.Severity + " " + f.Path + ": " + domain.Clip(f.Problem, 300)
				}
			}
		}
		steps := reviewCrossSteps(review.Steps[review.CrossCursor:end])
		if len(steps) == 0 {
			review.CrossCursor = end
			if err := run.save(ctx, "対象外の全体照合をスキップしました"); err != nil {
				return err
			}
			continue
		}
		d, err := run.analyze(ctx, reviewTask{Mode: "cross", Steps: steps, Prior: prior})
		if err != nil {
			return err
		}
		if err := run.check(ctx); err != nil {
			return err
		}
		review.CrossSummary = domain.Clip(d.Summary, 2048)
		review.CrossCursor = end
		if err := run.save(ctx, "全体照合の区切りを保存しました"); err != nil {
			return err
		}
	}
	if err := run.check(ctx); err != nil {
		return err
	}
	review.Phase = "priority"
	return run.save(ctx, "未確認項目の優先度を確認します")
}

// prioritize assesses the unverified items in batches. It returns a note for
// the final answer when a directive could not be applied.
func (run *reviewRun) prioritize(ctx context.Context) (string, error) {
	review := run.review
	reassessed := map[string]bool{}
	// IDs of the directives included in the latest priority request.
	attempted := map[string]bool{}
	for {
		for review.PriorityCursor < len(review.Unverified) {
			if err := run.prioritizeBatch(ctx, attempted); err != nil {
				return "", err
			}
		}
		// Instructions accepted during the last assessment must not be left
		// unapplied when the review completes: re-assess with them included.
		// A directive that was already sent with a failed request is not a late
		// arrival; re-running every batch would only repeat the failure.
		run.drainInstructions()
		retry := false
		failedDirective := false
		for _, id := range run.unappliedInstructions() {
			switch {
			case !attempted[id] && !reassessed[id]:
				reassessed[id], retry = true, true
			default:
				failedDirective = true
			}
		}
		if retry && len(review.Unverified) > 0 {
			review.PriorityCursor = 0
			if err := run.save(ctx, "追加指示を反映して未確認項目の優先度を再確認します"); err != nil {
				return "", err
			}
			continue
		}
		review.Phase = "done"
		if failedDirective && !retry {
			return "優先度評価に失敗したため、追加指示の一部は未適用です。必要ならチャットで再度指示してください。", nil
		}
		return "", nil
	}
}

// prioritizeBatch assesses up to 15 unverified items. A failed assessment
// keeps the items unprioritized instead of failing the review.
func (run *reviewRun) prioritizeBatch(ctx context.Context, attempted map[string]bool) error {
	in, review := run.in, run.review
	if run.budgetExhausted() {
		return errors.New("review_run_budget_resume")
	}
	run.drainInstructions()
	for _, m := range run.instructions {
		attempted[m.ID] = true
	}
	end := review.PriorityCursor + 15
	if end > len(review.Unverified) {
		end = len(review.Unverified)
	}
	items := make([]priorityItem, 0, end-review.PriorityCursor)
	for i := review.PriorityCursor; i < end; i++ {
		items = append(items, priorityItem{Index: i, Text: review.Unverified[i]})
	}
	payload, _ := json.Marshal(map[string]any{"priorityTask": items, "title": domain.Clip(review.Title, 500), "body": domain.Clip(review.Body, 1500), "summary": domain.Clip(review.CrossSummary, 1500), "instructions": run.pinnedInstructions()})
	if err := run.save(ctx, "未確認項目の優先度を確認しています"); err != nil {
		return err
	}
	run.modelCalls++
	run.logger.Info("model_call_started", "step", run.modelCalls, "review_phase", "priority", "input_bytes", len(payload))
	d, usage, err := run.r.next(ctx, in, string(payload), "review_priority")
	if err != nil && err.Error() == "ai_token_budget_exceeded" {
		return err
	}
	if err == nil && !validPriorityDecision(string(payload), d) {
		err = errors.New("invalid_priority_result")
	}
	if err == nil {
		run.markApplied()
	}
	run.logger.Info("model_call_finished", "step", run.modelCalls, "review_phase", "priority", "outcome", outcome(err), "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens)
	run.recordUsage(usage)
	run.recordInputBytes(len(payload))
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var priorities []PriorityAssessment
	encoded, _ := json.Marshal(d.Priorities)
	if err == nil && !echoes(string(encoded), in) {
		priorities = d.Priorities
	} else {
		run.logger.Warn("review_priority_failed", "error_code", "priority_assessment_unavailable", "outcome", outcome(err), "items", len(items))
	}
	savePriorities(review, items, priorities)
	review.PriorityCursor = end
	return run.save(ctx, "未確認項目の優先度を保存しました")
}
