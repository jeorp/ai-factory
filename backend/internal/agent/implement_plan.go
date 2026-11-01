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

func (run *implementRun) plan(ctx context.Context) error {
	impl := run.impl
	if impl.Revising && impl.ExpectedHead != "" {
		open, err := run.r.Writer.PullRequestOpen(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.PullNumber, impl.Branch, impl.ExpectedHead)
		if err != nil {
			return err
		}
		if !open {
			return errors.New("branch_moved")
		}
	}
	if !impl.Revising && impl.PullNumber > 0 && (impl.Branch == "" || impl.FixTarget) {
		// A fix request for an existing PR: build on its current head and push
		// to its branch. Only open ai-factory/ branches in this repository qualify.
		// The head is re-read on every plan so that a named PR is never replaced
		// by a new one; a closed PR stops with an error instead.
		pr, err := run.r.Writer.PullRequestTarget(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.PullNumber)
		if err != nil {
			return err
		}
		impl.BaseBranch, impl.BaseCommit, impl.ExpectedHead = pr.Base, pr.Head, pr.Head
		impl.Branch, impl.PullURL, impl.Title = pr.Branch, pr.URL, pr.Title
		if err := run.save(ctx, fmt.Sprintf("PR #%d のブランチに修正を追加します", impl.PullNumber)); err != nil {
			return err
		}
	}
	if !impl.Revising && impl.ExpectedHead != "" {
		open, err := run.r.Writer.PullRequestOpen(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.PullNumber, impl.Branch, impl.ExpectedHead)
		if err != nil {
			return err
		}
		if !open {
			// Only a merged or closed PR starts over on a new branch. If the PR is
			// still open and someone pushed to it, build on its new head instead.
			pr, err := run.r.Writer.PullRequestTarget(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.PullNumber)
			switch {
			case err == nil && pr.Branch == impl.Branch:
				impl.BaseCommit, impl.ExpectedHead = pr.Head, pr.Head
				impl.Unverified = append(impl.Unverified, fmt.Sprintf("PR #%d のブランチが更新されていたため、最新のコミットを基準にしました。", impl.PullNumber))
			case err == nil || err.Error() == "pull_request_closed" || err.Error() == "pull_request_not_found":
				previous := impl.PullNumber
				impl.DetachFollowUp()
				impl.Unverified = append(impl.Unverified, fmt.Sprintf("PR #%d はマージ・クローズ済みのため、最新のデフォルトブランチから新しいブランチで作成します。", previous))
			default:
				return err
			}
		}
	}
	if impl.BaseCommit == "" {
		repo, err := run.r.Writer.Authorize(ctx, run.in.GitHubToken, run.in.Session.Target.Repository)
		if err != nil {
			return err
		}
		sha, err := run.r.Writer.Commit(ctx, run.in.GitHubToken, repo)
		if err != nil {
			return err
		}
		impl.BaseBranch, impl.BaseCommit = repo.DefaultBranch, sha
	}
	if err := run.save(ctx, "依頼内容と関連コードを確認して実装計画を作成しています"); err != nil {
		return err
	}
	files, allPaths, err := run.fileList(ctx)
	if err != nil {
		return err
	}
	if !impl.Revising {
		impl.ContextFiles = nil
	}
	ev := []evidence{}
	if impl.ExpectedHead != "" && impl.PullNumber > 0 {
		ciEv, ciErr := run.collectCIFailureLogs(ctx, impl.PullNumber)
		if err := run.requireCIEvidence(ctx, ciErr); err != nil {
			return err
		}
		if len(ciEv) > 0 {
			run.ciEvidence = ciEv
		}
	}
	reads := 0
	correction := ""
	retries := 0
	invalidResponses := 0
	gapReviewed := false
	for call := 0; call < ImplementPlanCalls; call++ {
		final := reads >= ImplementPlanReads || call == ImplementPlanCalls-1
		task := implementTask{Mode: "plan", Request: impl.Request, BaseBranch: impl.BaseBranch, FollowUp: impl.ExpectedHead != "", ReadBudget: ImplementPlanReads - reads, FinalAnswer: final, Correction: correction, Attempt: call + 1, RetryCount: retries}
		payload := map[string]any{"implementTask": task, "summary": domain.Clip(run.cp.Summary, 2048), "evidence": ev, "repositoryFiles": files}
		if impl.Revising {
			payload["draftPlan"], payload["draftChanges"], payload["draftVerification"] = impl.Plan, impl.Changes, impl.Verification
		}
		d, err := run.call(ctx, payload)
		if err != nil && err.Error() == "ai_input_limit" && len(ev) > 0 {
			run.retry("plan", call+1, &retries, err)
			debugJSON(ctx, "context_reduced", map[string]any{"phase": "plan", "reason": "ai_input_limit", "discarded_evidence": ev[0]})
			ev = ev[1:]
			correction = "入力上限のため古い取得結果を省略しました。"
			// The request never reached the model, so it does not use up a call.
			call--
			continue
		}
		if err != nil {
			message, retry, saveErr := run.retryInvalidResponse(ctx, "plan", call+1, &invalidResponses, &retries, err)
			if saveErr != nil {
				return saveErr
			}
			if retry {
				correction = message
				continue
			}
		}
		if err != nil {
			return err
		}
		correction = ""
		invalidResponses = 0
		if d.Search != nil && !final {
			reads++
			if err := run.search(ctx, *d.Search, &ev); err != nil {
				return err
			}
			run.cp.Summary = domain.Clip(d.Summary, 2048)
			continue
		}
		if d.Read != nil && !final {
			reads++
			if err := run.read(ctx, *d.Read, &ev, planEvidenceKept); err != nil {
				return err
			}
			if d.Read.Kind == "file" {
				impl.ContextFiles = addContextFile(impl.ContextFiles, d.Read.Path)
			}
			run.cp.Summary = domain.Clip(d.Summary, 2048)
			continue
		}
		if d.Plan == nil {
			if strings.TrimSpace(d.Answer) != "" {
				// The model needs the user to clarify before planning.
				impl.Status = "plan_ready"
				if impl.Revising {
					impl.Clarification = d.Answer
				} else {
					impl.Plan = nil
				}
				run.cp.Answer = d.Answer
				return run.save(ctx, "実装の前に確認が必要です")
			}
			correction = "planを返してください。"
			run.retry("plan", call+1, &retries, errors.New("invalid_plan"))
			continue
		}
		plan, work, err := run.mergeDraftPlan(d.Plan)
		if err == nil {
			known := map[string]int{}
			for index, c := range plan.AcceptanceCriteria {
				known[c.ID] = index
			}
			for _, c := range impl.RequiredCriteria {
				if index, ok := known[c.ID]; ok {
					// A CI-only repair cannot redefine the original behavior to
					// make its tests easier to pass.
					plan.AcceptanceCriteria[index].Requirement = c.Requirement
				} else {
					plan.AcceptanceCriteria = append(plan.AcceptanceCriteria, c)
				}
			}
		}
		if err == nil {
			err = domain.ValidatePlan(plan)
		}
		if err == nil && len(plan.AcceptanceCriteria) == 0 {
			err = errors.New("invalid_acceptance_criteria")
		}
		if err == nil {
			err = run.validateCIPlanCoverage(plan)
		}
		if err != nil {
			run.retry("plan", call+1, &retries, err)
			debugJSON(ctx, "plan_rejected", map[string]any{"reason": err.Error(), "plan": plan, "required_criteria": impl.RequiredCriteria})
			correction = "計画が無効です: " + err.Error() + "。acceptanceCriteriaに依頼全体を分解したid・requirement・paths・checksを必ず含めてください。変更対象は20ファイル以内、.github/workflows・バイナリ・ロックファイルは対象外です。dependsOnは計画内の別ファイルだけを重複なく指定し、循環する変更は5ファイル以内にしてください。作成済みファイルのactionはdraftPlanのものを維持してください。"
			continue
		}
		problems, err := run.checkPlanPaths(ctx, plan)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			debugJSON(ctx, "plan_rejected", map[string]any{"reason": "plan_path_mismatch", "problems": problems, "plan": plan})
			run.retry("plan", call+1, &retries, errors.New("plan_path_mismatch"))
			// The model sometimes plans an update for a path it never read. Catch
			// it here so the user only approves plans that can be applied.
			run.logger.Info("implement_plan_path_mismatch", "count", len(problems))
			correction = "計画のパスがベースコミットと一致しません: " + strings.Join(problems, " / ") + "。変更・削除は実在するファイルだけ、追加は存在しないパスだけにしてください。パスが不明ならrepositoryまたはfileで確認してください。"
			continue
		}
		if !gapReviewed && call < ImplementPlanCalls-1 {
			// Ask the model once to reconsider files it likely missed: tests next
			// to planned files and files it read while planning.
			gapReviewed = true
			if related := relatedFiles(plan, impl.ContextFiles, allPaths); len(related) > 0 {
				debugJSON(ctx, "plan_gap_review", map[string]any{"related_paths": related, "plan": plan})
				run.logger.Info("implement_plan_gap_review", "count", len(related))
				correction = "計画の見直し: 次のファイルは計画にありませんが、関連する可能性があります: " + strings.Join(related, "、") + "。変更した定義・関数・型の利用箇所やテストで変更が必要なものは計画に加えてください。必要なら読んで確認できます。不要なら同じ計画をそのまま返してください。"
				continue
			}
		}
		impl.Plan = plan
		impl.Steps = domain.PlanSteps(work)
		debugJSON(ctx, "plan_accepted", map[string]any{"plan": plan, "steps": impl.Steps, "required_criteria": impl.RequiredCriteria})
		impl.NextStep = 0
		impl.Status = "plan_ready"
		impl.Summary = domain.Clip(d.Summary, 2048)
		run.cp.Summary = impl.Summary
		run.cp.Answer = formatPlan(impl)
		return run.save(ctx, "実装計画を作成しました。承認を待っています")
	}
	return errors.New("implementation_plan_failed")
}

// checkPlanPaths confirms that planned updates and deletes exist at the base
// commit and planned creates do not. Problems are returned for the model to fix;
// an error is returned only when GitHub itself could not be read.
func (run *implementRun) checkPlanPaths(ctx context.Context, plan *domain.ImplementationPlan) ([]string, error) {
	problems := []string{}
	for _, f := range plan.Files {
		_, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, run.impl.BaseCommit, f.Path)
		if err != nil {
			switch err.Error() {
			case "change_file_size_limit", "change_not_text", "invalid_change_path", "protected_change_path", "binary_or_generated_file_excluded":
				problems = append(problems, f.Path+" は変更できません（"+err.Error()+"）")
				continue
			}
			return nil, err
		}
		switch {
		case f.Action == "create" && exists:
			problems = append(problems, f.Path+" は既に存在します（updateにしてください）")
		case f.Action != "create" && !exists:
			problems = append(problems, f.Path+" は存在しません")
		}
	}
	return problems, nil
}

func (run *implementRun) collectCIFailureLogs(ctx context.Context, pullNumber int) ([]evidence, error) {
	if run.r.GitHub == nil {
		return nil, ciReadError(nil)
	}
	checkRead := github.Read{Kind: "check_runs", Number: pullNumber}
	run.cp.Metrics.ToolCalls++
	raw, source, fetched, err := run.r.GitHub.Read(ctx, run.in.GitHubToken, run.target(), checkRead)
	run.cp.Metrics.FetchedBytes += fetched
	if err != nil {
		run.logger.Info("implement_ci_check_runs_failed", "error_code", implementationErrorCode(err))
		return []evidence{{Read: checkRead, Content: "CIのチェック結果を取得できませんでした。原因は未確認です。根拠のない修正をしないでください。"}}, ciReadError(err)
	}
	var checksResult struct {
		Truncated bool `json:"truncated"`
		CheckRuns []struct {
			ID         int64  `json:"id"`
			JobID      int    `json:"job_id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			Output     struct {
				Summary string `json:"summary"`
				Text    string `json:"text"`
			} `json:"output"`
		} `json:"check_runs"`
	}
	var out []evidence
	out = append(out, evidence{Read: checkRead, Content: domain.MaskSecrets(raw)})
	if !containsSource(run.cp.Sources, source.URL) {
		run.cp.Sources = append(run.cp.Sources, source)
	}
	if json.Unmarshal([]byte(raw), &checksResult) != nil {
		return out, ciReadError(nil)
	}
	if checksResult.Truncated {
		return out, errors.New("implementation_ci_incomplete")
	}
	failedJobs := 0
	hasFailureEvidence := false
	var logErr error
	pendingChecks := false
	for _, cr := range checksResult.CheckRuns {
		if cr.Status != "" && cr.Status != "completed" {
			pendingChecks = true
		}
		if ciFailedConclusion(cr.Conclusion) {
			failedJobs++
			hasFailureEvidence = hasFailureEvidence || strings.TrimSpace(cr.Output.Summary+cr.Output.Text) != ""
			if cr.JobID < 1 {
				continue
			}
			jobRead := github.Read{Kind: "job_log", Number: cr.JobID}
			run.cp.Metrics.ToolCalls++
			logContent, jobSource, fetched, jobErr := run.r.GitHub.Read(ctx, run.in.GitHubToken, run.target(), jobRead)
			run.cp.Metrics.FetchedBytes += fetched
			if jobErr == nil {
				hasFailureEvidence = hasFailureEvidence || strings.TrimSpace(logContent) != ""
				out = append(out, evidence{Read: jobRead, Content: fmt.Sprintf("Job %s (#%d) 失敗項目と周辺行:\n%s", cr.Name, cr.JobID, domain.MaskSecrets(logContent))})
				if strings.Contains(logContent, "[INCOMPLETE:") {
					logErr = errors.New("implementation_ci_incomplete")
				}
				if !containsSource(run.cp.Sources, jobSource.URL) {
					run.cp.Sources = append(run.cp.Sources, jobSource)
				}
			} else {
				logErr = jobErr
				run.logger.Info("implement_ci_job_log_failed", "error_code", implementationErrorCode(jobErr))
				out = append(out, evidence{Read: jobRead, Content: "CIの失敗ジョブのログを取得できませんでした。原因は未確認です。"})
			}
		}
	}
	if pendingChecks {
		return out, errors.New("implementation_ci_pending")
	}
	if failedJobs == 0 && (run.impl.CIRepair || implementationNeedsCI(run.impl.Request)) {
		return out, errors.New("implementation_ci_not_failed")
	}
	bytes := 0
	for _, item := range out {
		bytes += len(item.Content)
	}
	if bytes > 48000 {
		return nil, errors.New("implementation_ci_incomplete")
	}
	if logErr != nil {
		return out, ciReadError(logErr)
	}
	if failedJobs > 0 && !hasFailureEvidence {
		return out, ciReadError(logErr)
	}
	return out, nil
}
