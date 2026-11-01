package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type verificationPart struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Content string `json:"content"`
	Offset  int    `json:"offset"`
	Bytes   int    `json:"bytes"`
	Diff    string `json:"diff,omitempty"`
}

const implementVerifyReads = 12

func (run *implementRun) verify(ctx context.Context) error {
	impl := run.impl
	if impl.Plan == nil || len(impl.Changes) == 0 {
		return errors.New("implementation_no_changes")
	}
	if impl.ExpectedHead != "" && impl.PullNumber > 0 && len(run.ciEvidence) == 0 {
		var ciErr error
		run.ciEvidence, ciErr = run.collectCIFailureLogs(ctx, impl.PullNumber)
		if err := run.requireCIEvidence(ctx, ciErr); err != nil {
			return err
		}
	}
	if err := run.save(ctx, "変更全体の整合性を確認しています（ビルド・テストは実行しません）"); err != nil {
		return err
	}
	files := []domain.FileChange{}
	for _, c := range impl.Changes {
		f, err := run.savedChange(ctx, c.Path)
		if err != nil {
			return err
		}
		if f.Action != impl.PlannedAction(f.Path) {
			return errors.New("change_action_not_planned")
		}
		files = append(files, f)
	}
	if err := domain.ValidateChangeSet(files, impl.ApprovedPaths()); err != nil {
		return err
	}
	parts := []verificationPart{}
	observed := map[string]bool{}
	serverUnverified := []string{}
	for _, f := range files {
		original := ""
		if f.Action != "create" {
			var exists bool
			var err error
			original, exists, err = run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, f.Path)
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("change_file_missing")
			}
		}
		content := f.Content
		if f.Action == "delete" {
			content = original
		}
		diff := domain.CompactDiff(original, f.Content, 4000)
		// Every byte of generated code (or deleted source) is inspected. Only
		// the supplemental diff is compacted; file bodies are never dropped.
		for offset := 0; ; {
			chunk := domain.Clip(content[offset:], 8000)
			parts = append(parts, verificationPart{Path: f.Path, Action: f.Action, Content: chunk, Offset: offset, Bytes: len(content), Diff: diff})
			offset += len(chunk)
			if offset == len(content) {
				break
			}
			diff = ""
		}
		if f.Action == "update" && (strings.Contains(f.Path, "_test.") || strings.Contains(f.Path, ".test.") || strings.Contains(f.Path, ".spec.") || strings.Contains("/"+f.Path, "/test_")) {
			// A compact diff can hide a removed assertion in a long test file.
			// Supply the complete previous test as comparison evidence as well.
			for offset := 0; ; {
				chunk := domain.Clip(original[offset:], 8000)
				parts = append(parts, verificationPart{Path: f.Path, Action: "before", Content: chunk, Offset: offset, Bytes: len(original)})
				offset += len(chunk)
				if offset == len(original) {
					break
				}
			}
		}
	}
	// Include definitions, callers and tests outside the write plan. Reading a
	// file does not authorize changing it. Criteria pin the end-to-end path.
	candidates := append([]string{}, impl.ContextFiles...)
	for _, c := range impl.Plan.AcceptanceCriteria {
		candidates = append(candidates, c.Paths...)
	}
	_, paths, err := run.fileList(ctx)
	if err != nil {
		return err
	}
	candidates = append(candidates, relatedFiles(impl.Plan, impl.ContextFiles, paths)...)
	loaded := map[string]bool{}
	for _, f := range files {
		loaded[f.Path] = true
	}
	for _, p := range candidates {
		if loaded[p] {
			continue
		}
		loaded[p] = true
		content, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, p)
		if err != nil || !exists {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			serverUnverified = appendUnique(serverUnverified, "関連コードを確認できません: "+p)
			continue
		}
		for offset := 0; ; {
			chunk := domain.Clip(content[offset:], 8000)
			parts = append(parts, verificationPart{Path: p, Action: "context", Content: chunk, Offset: offset, Bytes: len(content)})
			offset += len(chunk)
			if offset == len(content) {
				break
			}
		}
	}
	result := &domain.ImplementationVerification{Issues: []string{}, Unverified: []string{}}
	changed := map[string]bool{}
	for _, f := range files {
		changed[f.Path] = true
	}
	for _, f := range impl.Plan.Files {
		if !changed[f.Path] {
			result.Unverified = append(result.Unverified, "計画にあるが変更されなかったファイル: "+f.Path)
		}
	}
	// Preserve the full path set for the cross-batch review after parts are consumed.
	verificationPaths := make([]string, 0, len(parts))
	for _, part := range parts {
		verificationPaths = append(verificationPaths, part.Path)
	}
	summaries := []string{}
	reads := 0
	evidenceRevision := 0
	readPaths := map[string]bool{}
	ev := []evidence{}
	check := func(stage string, batch []verificationPart) error {
		correction := ""
		retries := 0
		invalid := 0
		unresolved := []string{}
		investigatedAt := -1
		for attempt := 1; attempt <= implementVerifyReads+5; attempt++ {
			if invalid >= 3 {
				return errors.New("implementation_verification_failed")
			}
			task := implementTask{Mode: "verify", Request: impl.Request, Plan: impl.Plan, Done: impl.Changes, Declined: impl.Declined, ReadBudget: implementVerifyReads - reads, FinalAnswer: reads >= implementVerifyReads || invalid >= 2, Step: len(summaries) + 1, Attempt: attempt, RetryCount: retries, Correction: correction}
			payload := map[string]any{"implementTask": task, "stage": stage, "parts": batch, "evidence": ev, "priorVerification": result, "editUnverified": impl.Unverified}
			knowledgePaths := []string{}
			if stage == "cross" {
				knowledgePaths = append(knowledgePaths, verificationPaths...)
			} else {
				for _, part := range batch {
					knowledgePaths = append(knowledgePaths, part.Path)
				}
			}
			for p := range readPaths {
				knowledgePaths = append(knowledgePaths, p)
			}
			if knowledge := knowledgeForVerification(knowledgePaths); knowledge != nil {
				payload["verificationKnowledge"] = knowledge
			}
			if stage == "cross" {
				payload["summaries"] = summaries
			}
			d, err := run.call(ctx, payload)
			if err != nil {
				if implementationErrorCode(err) != "invalid_implementation_result" {
					return err
				}
				invalid++
				run.retry("verify", attempt, &retries, err)
				correction = "verificationにissuesとunverifiedの配列を返してください。他の操作は行いません。"
				continue
			}
			if d.Read != nil || d.Search != nil {
				if task.FinalAnswer || !validImplementationResponse(string(mustJSON(payload)), d) {
					invalid++
					correction = "追加取得の上限です。verificationを返してください。"
					continue
				}
				reads++
				if d.Search != nil {
					if err := run.search(ctx, *d.Search, &ev); err != nil {
						return err
					}
				} else {
					complete, err := run.readVerificationFile(ctx, *d.Read, &ev)
					if err != nil {
						return err
					}
					// Only complete file reads can substantiate a met criterion.
					if complete {
						observed[d.Read.Path] = true
						if !readPaths[d.Read.Path] {
							evidenceRevision++
							readPaths[d.Read.Path] = true
						}
					}
				}
				correction = "追加取得したコードで未確認事項を調べ、根拠と結果をverificationに返してください。"
				continue
			}
			if d.Verification == nil || !validImplementationDecision(d) {
				invalid++
				run.retry("verify", attempt, &retries, errors.New("invalid_implementation_result"))
				correction = "verificationにissuesとunverifiedの配列を返してください。他の操作は行いません。"
				continue
			}
			// Give an unresolved result a chance to investigate rather than
			// silently completing with zero findings. A claim cannot be cleared
			// on the next call unless new evidence was fetched.
			if len(d.Verification.Unverified) > 0 && investigatedAt < 0 && !task.FinalAnswer {
				unresolved = append([]string{}, d.Verification.Unverified...)
				investigatedAt = evidenceRevision
				correction = "中心機能を含む未確認事項を残して完了しないでください。read/searchで根拠を追加取得してください: " + strings.Join(unresolved, " / ")
				continue
			}
			if investigatedAt == evidenceRevision {
				d.Verification.Unverified = appendUnique(d.Verification.Unverified, unresolved...)
			}
			result.Issues = appendUnique(result.Issues, d.Verification.Issues...)
			result.Unverified = appendUnique(result.Unverified, d.Verification.Unverified...)
			if stage == "cross" || len(summaries) == 0 {
				result.Criteria = d.Verification.Criteria
			}
			for _, repair := range d.Verification.Repairs {
				found := false
				for _, prior := range result.Repairs {
					if prior.Path == repair.Path {
						found = true
						break
					}
				}
				if !found {
					result.Repairs = append(result.Repairs, repair)
				}
			}
			for _, file := range d.Verification.AdditionalFiles {
				found := false
				for _, prior := range result.AdditionalFiles {
					if prior.Path == file.Path {
						found = true
						break
					}
				}
				if !found && len(result.AdditionalFiles) < maxPendingFiles {
					result.AdditionalFiles = append(result.AdditionalFiles, file)
				}
			}
			if stage == "parts" {
				if strings.TrimSpace(d.Summary) == "" {
					result.Unverified = appendUnique(result.Unverified, "一部の区切りで契約の要約が得られず、区切り間の確認に制約があります。")
				}
				summaries = append(summaries, domain.Clip(d.Summary, 2048))
			}
			return nil
		}
		return errors.New("implementation_verification_failed")
	}
	for len(parts) > 0 {
		n, size := 0, 0
		for n < len(parts) {
			raw, _ := json.Marshal(parts[n])
			if n > 0 && size+len(raw) > 48000 {
				break
			}
			size += len(raw)
			n++
		}
		if err := check("parts", parts[:n]); err != nil {
			return err
		}
		for _, part := range parts[:n] {
			// All chunks are sent in order and none are discarded.
			if part.Action != "before" && part.Offset+len(part.Content) == part.Bytes {
				observed[part.Path] = true
			}
		}
		parts = parts[n:]
		if err := run.save(ctx, fmt.Sprintf("変更全体の整合性を確認しています（%d区切り確認済み）", len(summaries))); err != nil {
			return err
		}
	}
	if len(summaries) > 1 {
		if err := check("cross", nil); err != nil {
			return err
		}
	}
	result.Unverified = appendUnique(result.Unverified, serverUnverified...)
	if incompleteCIEvidence(run.ciEvidence) {
		result.Unverified = appendUnique(result.Unverified, "CIの全失敗ログを確認できていません。")
	}
	if err := run.validateCIPlanCoverage(impl.Plan); err != nil {
		result.Unverified = appendUnique(result.Unverified, err.Error())
	}
	run.validateCoverage(result, observed)
	impl.Verification = result
	run.logger.Info("implement_verification_completed", "files", len(files), "batches", len(summaries), "issues", len(result.Issues), "unverified", len(result.Unverified))
	if len(result.Issues) > 0 {
		started, err := run.repair(ctx, result)
		if err != nil || started {
			return err
		}
	}
	impl.RepairFeedback = ""
	if !impl.CanPublish() {
		impl.Status = "verification_blocked"
		impl.Body = defaultBody(impl)
		run.cp.Answer = formatChanges(impl)
		return run.save(ctx, "未解決の指摘・受け入れ条件があるため、公開を止めています。追加修正を依頼してください")
	}
	return run.ready(ctx)
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Verification uses the same immutable base and generated overlay as editing.
// A failed or clipped read is never registered as complete coverage.
func (run *implementRun) readVerificationFile(ctx context.Context, read github.Read, ev *[]evidence) (bool, error) {
	if read.Kind != "file" || read.Validate() != nil {
		return false, errors.New("verification_read_not_allowed")
	}
	if err := run.save(ctx, "関連コードを確認しています: "+read.Path); err != nil {
		return false, err
	}
	content, overlaid, err := run.changedContent(ctx, read.Path)
	exists := overlaid
	if err != nil {
		return false, err
	}
	if !overlaid {
		run.cp.Metrics.ToolCalls++
		content, exists, err = run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, run.impl.BaseCommit, read.Path)
		run.cp.Metrics.FetchedBytes += len(content)
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	complete := err == nil && exists && len(content) <= 16000
	if err != nil || !exists {
		content = "取得できませんでした。パス・定義・利用箇所を確認してください。"
	} else if !complete {
		content = domain.Clip(content, 16000) + "\n[入力サイズ制限により後半を省略。全文未確認]"
	}
	*ev = append(*ev, evidence{Read: read, Content: domain.MaskSecrets(content)})
	if len(*ev) > 4 {
		*ev = (*ev)[len(*ev)-4:]
	}
	return complete, nil
}

func (run *implementRun) validateCoverage(result *domain.ImplementationVerification, observed map[string]bool) {
	if len(run.impl.Plan.AcceptanceCriteria) == 0 {
		result.Unverified = appendUnique(result.Unverified, "受け入れ条件がない古い計画です。依頼全体の条件を含めて計画を作り直してください。")
	}
	byID := map[string]domain.CriterionResult{}
	known := map[string]bool{}
	for _, c := range run.impl.Plan.AcceptanceCriteria {
		known[c.ID] = true
	}
	for _, c := range result.Criteria {
		if !known[c.ID] {
			result.Unverified = appendUnique(result.Unverified, "計画にない受け入れ条件の結果です: "+c.ID)
		}
		byID[c.ID] = c
	}
	for _, c := range run.impl.Plan.AcceptanceCriteria {
		r, ok := byID[c.ID]
		if !ok {
			result.Unverified = appendUnique(result.Unverified, "受け入れ条件の確認結果がありません: "+c.ID+" "+c.Requirement)
			continue
		}
		if r.Status == "unmet" {
			result.Issues = appendUnique(result.Issues, "未達の受け入れ条件: "+c.ID+" "+r.Evidence)
			continue
		}
		if r.Status != "met" || len(r.Paths) == 0 {
			result.Unverified = appendUnique(result.Unverified, "未確認の受け入れ条件: "+c.ID+" "+r.Evidence)
			continue
		}
		for _, p := range append(append([]string{}, c.Paths...), r.Paths...) {
			if !observed[p] {
				result.Unverified = appendUnique(result.Unverified, "受け入れ条件 "+c.ID+" の根拠コードを確認できていません: "+p)
			}
		}
	}
}

func appendUnique(dst []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, existing := range dst {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, value)
		}
	}
	return dst
}
