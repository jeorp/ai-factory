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

// analyze runs one model invocation over the current patch fragment or a small
// batch of summaries. Extra reads and evidence are scoped to this invocation.
func (run *reviewRun) analyze(ctx context.Context, task reviewTask) (Decision, error) {
	in, review, cp := run.in, run.review, &run.cp
	ev := []evidence{}
	cache := map[string]bool{}
	readCorrection := ""
	for call := 0; call <= domain.MaxFiles; call++ {
		run.drainInstructions()
		if err := run.save(ctx, "AIが"+task.Mode+"の区切りを確認しています"); err != nil {
			return Decision{}, err
		}
		pinned := run.pinnedInstructions()
		focus := domain.Clip(latestUserText(in.Session), 2048)
		payload := map[string]any{"reviewTask": task, "repositoryOverview": in.Session.RepositoryOverview, "target": map[string]any{"repository": in.Session.Target.Repository, "number": in.Session.Target.Number, "base": review.Base, "head": review.Head}, "title": review.Title, "body": review.Body, "scope": review.Scope, "focus": focus, "instructions": pinned, "evidence": ev, "readBudget": domain.MaxFiles - call}
		if readCorrection != "" {
			payload["readCorrection"] = readCorrection
		}
		body, _ := json.Marshal(payload)
		// Reduce optional background before source evidence. Every omission is visible.
		if !reviewInputFits(body) {
			debugJSON(ctx, "context_reduced", map[string]any{"phase": "review_" + task.Mode, "reason": "background_over_input_limit", "original_bytes": len(body), "prior": task.Prior, "focus": focus})
			payload["body"] = ""
			payload["repositoryOverview"] = nil
			payload["focus"] = domain.Clip(focus, 1024)
			task.Prior = domain.Clip(task.Prior, 512)
			payload["reviewTask"] = task
			run.limit("入力上限によりPR本文・レビュー観点・以前の要点の一部を省略しました。")
			body, _ = json.Marshal(payload)
		}
		for capBytes := 16000; !reviewInputFits(body) && len(ev) > 0 && capBytes >= 1000; capBytes -= 1000 {
			for i := range ev {
				if len(ev[i].Content) > capBytes {
					debugJSON(ctx, "context_reduced", map[string]any{"phase": "review_" + task.Mode, "reason": "evidence_over_input_limit", "read": ev[i].Read, "received_bytes": len(ev[i].Content), "clip_limit": capBytes})
					ev[i].Content = domain.Clip(ev[i].Content, capBytes)
					run.limit("入力上限により関連コードの後半を省略しました: " + ev[i].Read.Path)
				}
			}
			payload["evidence"] = ev
			body, _ = json.Marshal(payload)
		}
		if !reviewInputFits(body) {
			return Decision{}, errors.New("ai_input_limit")
		}
		if run.budgetExhausted() {
			return Decision{}, errors.New("review_run_budget_resume")
		}
		run.modelCalls++
		run.logger.Info("model_call_started", "step", run.modelCalls, "review_phase", task.Mode, "input_bytes", len(body), "estimated_input_tokens", reviewTokens(string(body)), "batch_files", len(task.Patches))
		run.recordInputBytes(len(body))
		d, usage, err := run.r.next(ctx, in, string(body), "review_"+task.Mode)
		run.logger.Info("model_call_finished", "step", run.modelCalls, "review_phase", task.Mode, "outcome", outcome(err), "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens)
		if err == nil {
			run.markApplied()
		}
		run.recordUsage(usage)
		if saveErr := run.save(ctx, "AIの利用量を保存しています"); saveErr != nil {
			return Decision{}, saveErr
		}
		if err != nil {
			return Decision{}, err
		}
		if d.StartReview != nil {
			return Decision{}, errors.New("review_already_running")
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
					run.instructions = append(run.instructions, m)
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
				run.limit(fmt.Sprintf("全体照合 %d番目の区切り: 要点の照合のみ。関連する元コードは再取得していません。", review.CrossCursor+1))
			}
			for _, u := range d.Review.Unverified {
				run.limit(u)
			}
			for _, f := range d.Review.Findings {
				run.acceptFinding(ctx, task, ev, f)
			}
			return d, nil
		}
		read := *d.Read
		if err := read.Validate(); err != nil {
			return Decision{}, err
		}
		if call == domain.MaxFiles {
			run.limit("追加取得回数の上限により、この区切りの確認は完了していません。")
			return Decision{Summary: "追加取得の上限に達したため未確認事項が残っています。"}, nil
		}
		if read.Kind != "file" && !(read.Kind == "pull_request" && read.Number == in.Session.Target.Number && read.Detail == "comments") {
			run.logger.Info("review_read_rejected", "error_code", "review_read_not_allowed", "kind", read.Kind, "detail", read.Detail, "pull_number", read.Number)
			if readCorrection != "" {
				return Decision{}, errors.New("review_read_not_allowed")
			}
			readCorrection = fmt.Sprintf("直前の追加取得要求は実行していません。追加取得はkind=file、またはkind=pull_request・number=%d・detail=commentsのみです。PR概要や変更一覧の再取得はできません。必要な関連コードをfileで指定するか、read=nullで現在の区切りのreviewを返してください。", in.Session.Target.Number)
			continue
		}
		keyBytes, _ := json.Marshal(read)
		key := string(keyBytes)
		if cache[key] {
			cp.Metrics.CacheHits++
			run.limit("同じ追加取得が繰り返されたため確認を打ち切りました: " + read.Path)
			return Decision{Summary: "追加取得が繰り返されたため未確認。"}, nil
		}
		cache[key] = true
		target := in.Session.Target
		target.Commit = review.Head
		content, source, n, err := run.r.GitHub.Read(ctx, in.GitHubToken, target, read)
		cp.Metrics.ToolCalls++
		cp.Metrics.FetchedBytes += n
		if ctx.Err() != nil {
			return Decision{}, ctx.Err()
		}
		debugJSON(ctx, "read_result", map[string]any{"workflow": "review", "read": read, "content": content, "received_bytes": len(content), "clip_limit": 24000, "outcome": outcome(err)})
		if err != nil {
			content = "取得できませんでした: " + err.Error()
			run.limit(read.Path + ": " + content)
		} else {
			if len(content) > 24000 {
				content = domain.Clip(content, 24000)
				run.limit("関連コード・コメントの後半は未確認: " + read.Path)
			}
			if !containsSource(cp.Sources, source.URL) && len(cp.Sources) < 100 {
				cp.Sources = append(cp.Sources, source)
			}
		}
		ev = append(ev, evidence{Read: read, Content: content})
		if len(ev) > domain.MaxFiles {
			ev = ev[len(ev)-domain.MaxFiles:]
		}
	}
	return Decision{}, errors.New("review_step_limit")
}

// acceptFinding saves a finding only when its location is backed by the
// patch or by fetched source, and it is not a duplicate.
func (run *reviewRun) acceptFinding(ctx context.Context, task reviewTask, ev []evidence, f domain.Finding) {
	disposition := "accepted"
	if debugTrace(ctx) != nil {
		defer func() {
			reads := []github.Read{}
			for _, e := range ev {
				reads = append(reads, e.Read)
			}
			debugJSON(ctx, "review_finding_decision", map[string]any{"phase": task.Mode, "finding": f, "reason": disposition, "read_sources": reads})
		}()
	}
	review := run.review
	if nonIssueFinding(f) || excludedReviewFinding(review, f.Path) {
		disposition = "non_issue_or_out_of_scope"
		run.logger.Info("review_finding_rejected", "reason", "non_issue_or_out_of_scope", "path", f.Path)
		return
	}
	supported := false
	if task.Mode == "diff" {
		for _, patch := range task.Patches {
			if f.Path == patch.Path && patchLocation(patch.Patch, f) {
				supported = true
			}
		}
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
		disposition = "unsupported_location_or_sources"
		run.limit("根拠の元コード・位置を確認できない指摘を保留しました: " + f.Path + " " + f.Title)
		return
	}
	for _, old := range review.Findings {
		if old.Path == f.Path && old.Line == f.Line && old.Side == f.Side && old.Title == f.Title {
			disposition = "duplicate"
			return
		}
	}
	if len(review.Findings) < 50 {
		review.Findings = append(review.Findings, f)
	} else {
		disposition = "finding_limit"
		run.limit("指摘の保存上限50件に達しました。追加の指摘は保存していません。")
	}
}
