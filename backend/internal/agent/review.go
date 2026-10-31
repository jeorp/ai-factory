package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type reviewReader interface {
	PullRequest(context.Context, string, domain.Target) (github.PullRequest, int, error)
	ReviewFiles(context.Context, string, domain.Target, int, int) ([]github.ChangedFile, int, error)
}
type reviewTask struct {
	Mode  string              `json:"mode"`
	Path  string              `json:"path,omitempty"`
	Patch string              `json:"patch,omitempty"`
	Steps []domain.ReviewStep `json:"steps,omitempty"`
	Prior string              `json:"prior"`
}

func (r *Runner) runReview(ctx context.Context, in Input, emit func(Checkpoint) error) error {
	gh, ok := r.GitHub.(reviewReader)
	if !ok || in.Session.Target.Kind != "pull" {
		return errors.New("pull_request_required")
	}
	started := time.Now()
	logger := in.Logger
	if logger == nil {
		logger = slog.Default()
	}
	modelCalls := 0
	cp := Checkpoint{Summary: in.Session.Summary, Sources: append([]domain.Source{}, in.Session.Sources...), Metrics: in.Session.Metrics}
	cp.Metrics.LastInputTokens, cp.Metrics.LastOutputTokens, cp.Metrics.HasLastUsage = 0, 0, false
	// Deep-copy mutable cursors/results: the socket owns its durable snapshot.
	raw, _ := json.Marshal(in.Session.Review)
	var review *domain.Review
	if err := json.Unmarshal(raw, &review); err != nil {
		return err
	}
	cp.Review = review
	instructions := []domain.Message{}
	applied := []string{}
	for _, m := range in.Session.Messages {
		if m.Role == "instruction" {
			instructions = append(instructions, m)
		}
	}
	save := func(progress string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cp.Progress = progress
		cp.Metrics.DurationMS = time.Since(started).Milliseconds()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		if int64(mem.HeapAlloc) > cp.Metrics.HeapPeakBytes {
			cp.Metrics.HeapPeakBytes = int64(mem.HeapAlloc)
		}
		cp.Applied = nil
		// Mark only directives already consumed at a model boundary.
		cp.Applied = append(cp.Applied, applied...)
		cp.Review = review
		if review != nil {
			cp.Limitations = append([]string{}, review.Unverified...)
		}
		b, _ := json.Marshal(cp)
		if len(b) > 450*1024 {
			return errors.New("review_state_limit")
		}
		if echoes(string(b), in) {
			return errors.New("credential_echo_blocked")
		}
		// Checkpoint arguments must not share writable pointers with the next step.
		var frozen Checkpoint
		if err := json.Unmarshal(b, &frozen); err != nil {
			return err
		}
		return emit(frozen)
	}
	fetchPR := func() (github.PullRequest, error) {
		pr, n, err := gh.PullRequest(ctx, in.GitHubToken, in.Session.Target)
		cp.Metrics.ToolCalls++
		cp.Metrics.FetchedBytes += n
		return pr, err
	}
	check := func() error {
		pr, err := fetchPR()
		if err != nil {
			return err
		}
		if pr.Base.SHA != review.Base || pr.Head.SHA != review.Head || pr.ChangedFiles != review.TotalFiles {
			return errors.New("pr_changed_create_new_review")
		}
		return nil
	}
	limit := func(s string) {
		s = domain.Clip(s, 512)
		for _, old := range review.Unverified {
			if old == s {
				return
			}
		}
		if len(review.Unverified) < 250 {
			review.Unverified = append(review.Unverified, s)
		}
	}
	if review == nil {
		if err := save("PRの概要と対象コミットを取得しています"); err != nil {
			return err
		}
		pr, err := fetchPR()
		if err != nil {
			return err
		}
		review = &domain.Review{Title: domain.Clip(pr.Title, 512), Body: domain.Clip(pr.Body, 2048), Base: pr.Base.SHA, Head: pr.Head.SHA, TotalFiles: pr.ChangedFiles, Phase: "planning", Steps: []domain.ReviewStep{}, Findings: []domain.Finding{}, Unverified: []string{"コード・テストの実行による検証は行っていません。"}}
		if len(pr.Body) > 2048 {
			limit("PR本文の後半を入力上限のため省略しました。")
		}
		source := domain.Source{Label: fmt.Sprintf("PR #%d", in.Session.Target.Number), URL: fmt.Sprintf("https://github.com/%s/pull/%d", in.Session.Target.Repository, in.Session.Target.Number)}
		if !containsSource(cp.Sources, source.URL) {
			cp.Sources = append(cp.Sources, source)
		}
		if err := save("PRの変更ファイルからレビュー計画を作成しています"); err != nil {
			return err
		}
	}
	if err := check(); err != nil {
		return err
	}
	// Planning is resumable too: each directory group contains at most five files.
	if review.Phase == "planning" {
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
			files, n, err := gh.ReviewFiles(ctx, in.GitHubToken, in.Session.Target, count+1, 1)
			cp.Metrics.ToolCalls++
			cp.Metrics.FetchedBytes += n
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				// Preserve a placeholder so pagination remains stable and report the gap.
				review.Steps = append(review.Steps, domain.ReviewStep{Directory: "取得失敗", State: "partial", Files: []domain.ReviewFile{{Index: count, Done: true}}})
				limit(fmt.Sprintf("変更ファイル%d: 一覧・差分を取得できませんでした（%s）。", count+1, err.Error()))
			} else {
				if len(files) != 1 {
					return errors.New("pr_file_list_incomplete")
				}
				f := files[0]
				if len(f.Filename) > 512 || len(f.PreviousFilename) > 512 || f.Filename == "" {
					return errors.New("invalid_pr_file")
				}
				dir := path.Dir(f.Filename)
				idx := -1
				for i := range review.Steps {
					if review.Steps[i].Directory == dir && len(review.Steps[i].Files) < 5 {
						idx = i
						break
					}
				}
				if idx < 0 {
					review.Steps = append(review.Steps, domain.ReviewStep{Directory: dir, State: "pending", Files: []domain.ReviewFile{}})
					idx = len(review.Steps) - 1
				}
				review.Steps[idx].Files = append(review.Steps[idx].Files, domain.ReviewFile{Path: f.Filename, PreviousPath: f.PreviousFilename, Status: f.Status, Index: count})
			}
			count++
			if err := check(); err != nil {
				return err
			}
			if err := save(fmt.Sprintf("レビュー計画: %d / %d ファイル", count, max)); err != nil {
				return err
			}
		}
		if review.TotalFiles > max {
			limit(fmt.Sprintf("先頭%dファイルを計画しました。残り%dファイルは規模上限により未確認です。", max, review.TotalFiles-max))
		}
		review.Phase = "diff"
		if err := save("レビュー計画を保存しました"); err != nil {
			return err
		}
	}
	// One invocation receives only the current patch fragment or a small batch of
	// summaries. Extra reads and evidence are scoped to this invocation.
	analyze := func(task reviewTask) (Decision, error) {
		ev := []evidence{}
		cache := map[string]bool{}
		for call := 0; call <= domain.MaxFiles; call++ {
			for {
				select {
				case m, open := <-in.Instructions:
					if !open {
						goto drained
					}
					instructions = append(instructions, m)
				default:
					goto drained
				}
			}
		drained:
			if err := save("AIが" + task.Mode + "の区切りを確認しています"); err != nil {
				return Decision{}, err
			}
			pinned := ""
			for _, m := range instructions {
				pinned += m.Text + "\n"
			}
			focus := ""
			for i := len(in.Session.Messages) - 1; i >= 0; i-- {
				if in.Session.Messages[i].Role == "user" {
					focus = domain.Clip(in.Session.Messages[i].Text, 2048)
					break
				}
			}
			payload := map[string]any{"reviewTask": task, "repositoryOverview": in.Session.RepositoryOverview, "target": map[string]any{"repository": in.Session.Target.Repository, "number": in.Session.Target.Number, "base": review.Base, "head": review.Head}, "title": review.Title, "body": review.Body, "focus": focus, "instructions": pinned, "evidence": ev, "readBudget": domain.MaxFiles - call}
			body, _ := json.Marshal(payload)
			// Reduce optional background before source evidence. Every omission is visible.
			if len(body) > domain.MaxInputBytes {
				payload["body"] = ""
				payload["repositoryOverview"] = nil
				payload["focus"] = domain.Clip(focus, 1024)
				task.Prior = domain.Clip(task.Prior, 512)
				payload["reviewTask"] = task
				limit("入力上限によりPR本文・レビュー観点・以前の要点の一部を省略しました。")
				body, _ = json.Marshal(payload)
			}
			for capBytes := 3000; len(body) > domain.MaxInputBytes && len(ev) > 0 && capBytes >= 500; capBytes -= 500 {
				for i := range ev {
					if len(ev[i].Content) > capBytes {
						ev[i].Content = domain.Clip(ev[i].Content, capBytes)
						limit("入力上限により関連コードの後半を省略しました: " + ev[i].Read.Path)
					}
				}
				payload["evidence"] = ev
				body, _ = json.Marshal(payload)
			}
			if len(body) > domain.MaxInputBytes {
				return Decision{}, errors.New("ai_input_limit")
			}
			if modelCalls >= 20 {
				return Decision{}, errors.New("review_run_budget_resume")
			}
			modelCalls++
			logger.Info("model_call_started", "step", modelCalls, "review_phase", task.Mode, "input_bytes", len(body))
			if len(body) > cp.Metrics.MaxInputBytes {
				cp.Metrics.MaxInputBytes = len(body)
			}
			d, usage, err := r.AI.Next(ctx, in.Key, in.Session.Model, string(body))
			logger.Info("model_call_finished", "step", modelCalls, "review_phase", task.Mode, "outcome", outcome(err), "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens)
			if err == nil {
				applied = nil
				for _, m := range instructions {
					applied = append(applied, m.ID)
				}
			}
			cp.Metrics.InputTokens += usage.InputTokens
			cp.Metrics.OutputTokens += usage.OutputTokens
			cp.Metrics.LastInputTokens += usage.InputTokens
			cp.Metrics.LastOutputTokens += usage.OutputTokens
			if usage.InputTokens != 0 || usage.OutputTokens != 0 {
				cp.Metrics.HasLastUsage = true
			}
			if saveErr := save("AIの利用量を保存しています"); saveErr != nil {
				return Decision{}, saveErr
			}
			if err != nil {
				return Decision{}, err
			}
			b, _ := json.Marshal(d)
			if echoes(string(b), in) {
				return Decision{}, errors.New("credential_echo_blocked")
			}
			if !validReviewResult(d.Review) || len(d.Summary) > 2048 {
				return Decision{}, errors.New("invalid_review_result")
			}
			if d.Read == nil {
				// A newly accepted instruction must be applied before accepting a result.
				select {
				case m, open := <-in.Instructions:
					if open {
						instructions = append(instructions, m)
						if call < domain.MaxFiles {
							continue
						}
						return Decision{}, errors.New("review_instruction_retry")
					}
				default:
				}
				if d.Review == nil {
					return Decision{}, errors.New("missing_review_result")
				}
				if task.Mode == "cross" && len(ev) == 0 {
					limit(fmt.Sprintf("全体照合 %d番目の区切り: 要点の照合のみ。関連する元コードは再取得していません。", review.CrossCursor+1))
				}
				for _, u := range d.Review.Unverified {
					limit(u)
				}
				for _, f := range d.Review.Findings {
					supported := false
					if task.Mode == "diff" && f.Path == task.Path {
						supported = patchLocation(task.Patch, f)
					}
					sourcePaths := map[string]bool{}
					for _, e := range ev {
						if e.Read.Kind == "file" && !strings.HasPrefix(e.Content, "取得できませんでした") {
							sourcePaths[e.Read.Path] = true
							if e.Read.Path == f.Path && (f.Line == 0 || f.Line <= strings.Count(e.Content, "\n")+1) && f.Side == "RIGHT" {
								supported = true
							}
						}
					}
					if task.Mode == "cross" && len(sourcePaths) < 2 {
						supported = false
					}
					if !supported {
						limit("根拠の元コード・位置を確認できない指摘を保留しました: " + f.Path + " " + f.Title)
						continue
					}
					duplicate := false
					for _, old := range review.Findings {
						if old.Path == f.Path && old.Line == f.Line && old.Side == f.Side && old.Title == f.Title {
							duplicate = true
							break
						}
					}
					if !duplicate {
						if len(review.Findings) < 50 {
							review.Findings = append(review.Findings, f)
						} else {
							limit("指摘の保存上限50件に達しました。追加の指摘は保存していません。")
						}
					}
				}
				return d, nil
			}
			read := *d.Read
			if err := read.Validate(); err != nil {
				return Decision{}, err
			}
			if call == domain.MaxFiles {
				limit(task.Path + ": 追加取得回数の上限により、この区切りの確認は完了していません。")
				return Decision{Summary: "追加取得の上限に達したため未確認事項が残っています。"}, nil
			}
			if read.Kind != "file" && !(read.Kind == "pull_request" && read.Number == in.Session.Target.Number && read.Detail == "comments") {
				return Decision{}, errors.New("review_read_not_allowed")
			}
			keyBytes, _ := json.Marshal(read)
			key := string(keyBytes)
			if cache[key] {
				cp.Metrics.CacheHits++
				limit("同じ追加取得が繰り返されたため確認を打ち切りました: " + read.Path)
				return Decision{Summary: "追加取得が繰り返されたため未確認。"}, nil
			}
			cache[key] = true
			target := in.Session.Target
			target.Commit = review.Head
			content, source, n, err := r.GitHub.Read(ctx, in.GitHubToken, target, read)
			cp.Metrics.ToolCalls++
			cp.Metrics.FetchedBytes += n
			if ctx.Err() != nil {
				return Decision{}, ctx.Err()
			}
			if err != nil {
				content = "取得できませんでした: " + err.Error()
				limit(read.Path + ": " + content)
			} else {
				if len(content) > 3500 {
					content = domain.Clip(content, 3500)
					limit("関連コード・コメントの後半は未確認: " + read.Path)
				}
				if !containsSource(cp.Sources, source.URL) && len(cp.Sources) < 100 {
					cp.Sources = append(cp.Sources, source)
				}
			}
			ev = append(ev, evidence{Read: read, Content: content})
			if len(ev) > 2 {
				ev = ev[len(ev)-2:]
			}
		}
		return Decision{}, errors.New("review_step_limit")
	}
	if review.Phase == "diff" {
		for si := range review.Steps {
			step := &review.Steps[si]
			for fi := range step.Files {
				f := &step.Files[fi]
				if f.Done {
					continue
				}
				if step.State != "partial" {
					step.State = "running"
				}
				if err := check(); err != nil {
					return err
				}
				if err := save(fmt.Sprintf("差分レビュー %d / %d: %s", si+1, len(review.Steps), f.Path)); err != nil {
					return err
				}
				files, n, err := gh.ReviewFiles(ctx, in.GitHubToken, in.Session.Target, f.Index+1, 1)
				cp.Metrics.ToolCalls++
				cp.Metrics.FetchedBytes += n
				if ctx.Err() != nil {
					return ctx.Err()
				}
				if err != nil {
					limit(f.Path + ": 差分取得失敗（" + err.Error() + "）。")
					f.Done = true
					step.State = "partial"
					if err := save("取得できなかった範囲を保存しました"); err != nil {
						return err
					}
					continue
				}
				if len(files) != 1 || files[0].Filename != f.Path || files[0].Status != f.Status || files[0].PreviousFilename != f.PreviousPath {
					return errors.New("pr_changed_create_new_review")
				}
				if err := check(); err != nil {
					return err
				}
				patch := files[0].Patch
				if (github.Read{Kind: "file", Path: f.Path}).Validate() != nil || patch == "" {
					limit(f.Path + ": バイナリ・生成ファイル、またはGitHubから差分が返らないため未確認です。")
					f.Done = true
					step.State = "partial"
				} else {
					// GitHub can omit/truncate patches. Hunk counts are checked explicitly.
					if !completePatch(patch) {
						limit(f.Path + ": 差分が不完全なため、返された範囲だけを確認します。")
						step.State = "partial"
					}
					for f.Offset < len(patch) {
						chunk, next := patchChunk(patch, f.Offset)
						if chunk == "" {
							limit(f.Path + ": 1行が差分入力上限を超えるため未確認です。長い行をスキップしました。")
							step.State = "partial"
							f.Offset = next
							continue
						}
						// Prefix the cursor's original line numbers for a fragment inside a hunk.
						left, right := patchCursor(patch[:f.Offset])
						chunk = fmt.Sprintf("@@ -%d +%d @@\n", left, right) + chunk
						d, err := analyze(reviewTask{Mode: "diff", Path: f.Path, Patch: chunk, Prior: step.Summary})
						if err != nil {
							return err
						}
						if err := check(); err != nil {
							return err
						}
						step.Summary = domain.Clip(d.Summary, 512)
						f.Offset = next
						if err := save("差分の区切りと次の取得位置を保存しました"); err != nil {
							return err
						}
					}
					f.Done = true
				}
				if err := save("ファイルの確認状況を保存しました"); err != nil {
					return err
				}
			}
			if step.State != "partial" {
				step.State = "done"
			}
			if err := save("グループの確認結果を保存しました"); err != nil {
				return err
			}
		}
		review.Phase = "cross"
		if err := save("ファイルをまたぐ不整合を確認します"); err != nil {
			return err
		}
	}
	if review.Phase == "cross" {
		for review.CrossCursor < len(review.Steps) {
			end := review.CrossCursor + 1
			if end > len(review.Steps) {
				end = len(review.Steps)
			}
			prior := review.CrossSummary
			for _, f := range review.Findings {
				for _, rf := range review.Steps[review.CrossCursor].Files {
					if rf.Path == f.Path && len(prior) < 3500 {
						prior += "\n" + f.Severity + " " + f.Path + ": " + domain.Clip(f.Problem, 300)
					}
				}
			}
			d, err := analyze(reviewTask{Mode: "cross", Steps: review.Steps[review.CrossCursor:end], Prior: prior})
			if err != nil {
				return err
			}
			if err := check(); err != nil {
				return err
			}
			review.CrossSummary = domain.Clip(d.Summary, 2048)
			review.CrossCursor = end
			if err := save("全体照合の区切りを保存しました"); err != nil {
				return err
			}
		}
		if err := check(); err != nil {
			return err
		}
		review.Phase = "done"
	}
	cp.Summary = "PR #" + strconv.Itoa(in.Session.Target.Number) + " Base=" + review.Base + " Head=" + review.Head + "\n" + domain.Clip(review.CrossSummary, 800)
	for _, f := range review.Findings {
		cp.Summary += fmt.Sprintf("\n%s %s:%d %s", f.Severity, f.Path, f.Line, domain.Clip(f.Problem, 250))
		if len(cp.Summary) > 1800 {
			break
		}
	}
	cp.Summary = domain.Clip(cp.Summary, 2048)
	cp.Answer = fmt.Sprintf("PR #%d のレビューを完了しました。指摘 %d 件。計画・指摘・未確認範囲をレビュー結果に表示しています。対象ヘッド: %s。実行・テストによる検証は行っていません。", in.Session.Target.Number, len(review.Findings), review.Head[:12])
	return save("レビュー結果を保存しました")
}

func echoes(s string, in Input) bool {
	return in.Key != "" && strings.Contains(s, in.Key) || in.GitHubToken != "" && strings.Contains(s, in.GitHubToken)
}

// Split on complete lines; a very long single line is explicitly skipped.
func patchChunk(s string, offset int) (string, int) {
	end := offset + 3000
	if end >= len(s) {
		return s[offset:], len(s)
	}
	cut := strings.LastIndexByte(s[offset:end], '\n')
	if cut < 0 {
		next := strings.IndexByte(s[offset:], '\n')
		if next < 0 {
			return "", len(s)
		}
		return "", offset + next + 1
	}
	return s[offset : offset+cut+1], offset + cut + 1
}
func hunk(line string) (int, int, bool) {
	var l, r int
	parts := strings.Fields(line)
	if len(parts) < 4 || parts[0] != "@@" || !strings.HasPrefix(parts[1], "-") || !strings.HasPrefix(parts[2], "+") {
		return 0, 0, false
	}
	l, e1 := strconv.Atoi(strings.Split(parts[1][1:], ",")[0])
	r, e2 := strconv.Atoi(strings.Split(parts[2][1:], ",")[0])
	return l, r, e1 == nil && e2 == nil
}
func patchCursor(prefix string) (int, int) {
	l, r := 1, 1
	scanner := bufio.NewScanner(strings.NewReader(prefix))
	scanner.Buffer(make([]byte, 1024), domain.MaxFetchBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if a, b, ok := hunk(line); ok {
			l, r = a, b
			continue
		}
		if strings.HasPrefix(line, "-") {
			l++
		} else if strings.HasPrefix(line, "+") {
			r++
		} else if strings.HasPrefix(line, " ") {
			l++
			r++
		}
	}
	return l, r
}
func patchLocation(patch string, f domain.Finding) bool {
	if f.Line == 0 {
		return true
	}
	l, r := 1, 1
	for _, line := range strings.Split(patch, "\n") {
		if a, b, ok := hunk(line); ok {
			l, r = a, b
			continue
		}
		if strings.HasPrefix(line, "-") {
			if f.Side == "LEFT" && f.Line == l {
				return true
			}
			l++
		} else if strings.HasPrefix(line, "+") {
			if f.Side == "RIGHT" && f.Line == r {
				return true
			}
			r++
		} else if strings.HasPrefix(line, " ") {
			if f.Side == "LEFT" && f.Line == l || f.Side == "RIGHT" && f.Line == r {
				return true
			}
			l++
			r++
		}
	}
	return false
}
func completePatch(patch string) bool {
	wantL, wantR, gotL, gotR := 0, 0, 0, 0
	seen := false
	for _, line := range strings.Split(patch, "\n") {
		if _, _, ok := hunk(line); ok {
			if seen && (wantL != gotL || wantR != gotR) {
				return false
			}
			parts := strings.Fields(line)
			parse := func(s string) int {
				v := strings.Split(s, ",")
				if len(v) == 1 {
					return 1
				}
				n, _ := strconv.Atoi(v[1])
				return n
			}
			wantL, wantR = parse(parts[1]), parse(parts[2])
			gotL, gotR = 0, 0
			seen = true
		} else if strings.HasPrefix(line, "-") {
			gotL++
		} else if strings.HasPrefix(line, "+") {
			gotR++
		} else if strings.HasPrefix(line, " ") {
			gotL++
			gotR++
		}
	}
	return seen && wantL == gotL && wantR == gotR
}
