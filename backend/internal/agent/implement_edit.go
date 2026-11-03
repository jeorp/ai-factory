package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ai-factory/backend/internal/domain"
)

// A validated draft has no side effects on stored file contents. Persist it
// before advancing the durable step cursor or requesting an amendment.
type stepDraft struct {
	Changes    []domain.FileChange
	Unverified []string
	Pending    []domain.PendingFile
	Rejected   []string
	Unchanged  []domain.UnchangedFile
}

func (run *implementRun) edit(ctx context.Context) error {
	impl := run.impl
	if impl.Plan == nil || len(impl.Steps) == 0 || !domain.ValidCommit(impl.BaseCommit) {
		return errors.New("implementation_plan_required")
	}
	for impl.NextStep < len(impl.Steps) {
		step := impl.Steps[impl.NextStep].Files
		if err := run.save(ctx, fmt.Sprintf("変更を作成しています（%d/%d）", impl.NextStep+1, len(impl.Steps))); err != nil {
			return err
		}
		files, originals, err := run.loadStepFiles(ctx, step)
		if err != nil {
			return err
		}
		draft, err := run.generateStep(ctx, step, files, originals)
		if err != nil {
			return err
		}
		if draft == nil {
			continue
		} // input limit split the step; retry its first part
		covered, err := run.persistChanges(ctx, draft.Changes)
		if err != nil {
			return err
		}
		run.advanceStep(step, draft, covered)
		if len(draft.Pending) > 0 {
			impl.PendingFiles = draft.Pending
			impl.Status = "amend_ready"
			run.cp.Answer = formatAmendment(impl)
			return run.save(ctx, "計画にないファイルの追加について承認を待っています")
		}
		if err := run.save(ctx, fmt.Sprintf("変更を保存しました（%d/%d）", impl.NextStep, len(impl.Steps))); err != nil {
			return err
		}
	}
	if len(impl.Changes) == 0 {
		return errors.New("implementation_no_changes")
	}
	if impl.ChangeRevision == impl.EditStartRevision {
		impl.Unverified = appendUnique(impl.Unverified, "実際の変更がないため実装を完了できません。")
		if impl.RepairFeedback != "" && impl.Verification != nil {
			impl.Verification.Unverified = appendUnique(impl.Verification.Unverified, "実際の修復が行われませんでした。")
			impl.Status = "verification_blocked"
			return run.save(ctx, "実際の修復がないため公開を止めています")
		}
		if err := run.save(ctx, "実際の変更がないため実装を停止しました"); err != nil {
			return err
		}
		return errors.New("implementation_edit_blocked")
	}
	impl.Status = "verifying"
	return run.verify(ctx)
}

func (run *implementRun) loadStepFiles(ctx context.Context, step []string) ([]implementFile, map[string]string, error) {
	impl := run.impl
	files := []implementFile{}
	originals := map[string]string{}
	for _, p := range step {
		f := implementFile{Path: p, Action: impl.PlannedAction(p)}
		for _, planned := range impl.Plan.Files {
			if planned.Path == p {
				f.Approach = planned.Approach
				if impl.RepairFeedback != "" && impl.Verification != nil {
					for _, repair := range impl.Verification.Repairs {
						if repair.Path == p {
							f.Approach = repair.Approach
							break
						}
					}
				}
			}
		}
		content, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, p)
		if err != nil {
			return nil, nil, err
		}
		if f.Action == "create" && exists {
			return nil, nil, errors.New("change_file_exists")
		}
		if f.Action != "create" && !exists {
			return nil, nil, errors.New("change_file_missing")
		}
		for _, c := range impl.Changes {
			if c.Path == p {
				current, err := run.savedChange(ctx, p)
				if err != nil {
					return nil, nil, err
				}
				content = current.Content
				break
			}
		}
		originals[p] = content
		_, f.PreserveExisting = retainedChange(impl, originals, p)
		if f.Action == "update" || f.Action == "create" && content != "" {
			f.Original = content
		}
		files = append(files, f)
	}
	return files, originals, nil
}

// A nil draft means input limits or a timeout split the step without saving any generated
// files. The caller reloads inputs for the replacement step.
func (run *implementRun) generateStep(ctx context.Context, step []string, files []implementFile, originals map[string]string) (*stepDraft, error) {
	impl := run.impl
	approved := impl.ApprovedPaths()
	related, previous, err := run.stepContext(ctx, step)
	if err != nil {
		return nil, err
	}
	ev := []evidence{}
	if impl.ExpectedHead != "" && impl.PullNumber > 0 {
		var ciErr error
		run.ciEvidence, ciErr = run.collectCIFailureLogs(ctx, impl.PullNumber)
		if err := run.requireCIEvidence(ctx, ciErr); err != nil {
			return nil, err
		}
	}
	reads := 0
	correction := ""
	retries := 0
	invalidResponses := 0
	editFormat := ""
	var editFailure *implementEditFailure
	var pending []domain.PendingFile
	var rejected []string
	for call := 0; call < ImplementStepCalls; call++ {
		final := reads >= ImplementStepReads || call == ImplementStepCalls-1
		task := implementTask{Mode: "edit", Request: impl.Request, BaseBranch: impl.BaseBranch, Plan: impl.Plan, Step: impl.NextStep + 1, TotalSteps: len(impl.Steps), Files: editTaskFiles(files, editFormat), Done: impl.Changes, Declined: impl.Declined, ReadBudget: ImplementStepReads - reads, FinalAnswer: final, Correction: correction, Attempt: call + 1, RetryCount: retries, RepairFeedback: impl.RepairFeedback, EditFormat: editFormat, EditFailure: editFailure}
		d, err := run.call(ctx, map[string]any{"implementTask": task, "summary": domain.Clip(run.cp.Summary, 2048), "evidence": ev, "context": related, "previousChanges": previous})
		// Shed the optional context before splitting the step.
		// The request never reached the model, so shedding does not use up a
		// call; it ends because ev, related and previous only shrink here and
		// reads are bounded.
		if err != nil && err.Error() == "ai_input_limit" && (len(ev) > 0 || len(related) > 0 || len(previous) > 0) {
			run.retry("edit", call+1, &retries, err)
			debugJSON(ctx, "context_reduced", map[string]any{"phase": "edit", "reason": "ai_input_limit", "step": impl.NextStep + 1, "evidence": ev, "related_context": related, "previous_changes": previous})
			switch {
			case len(ev) > 0:
				ev = nil
			case len(related) > 0:
				related = nil
			default:
				previous = nil
			}
			correction += "入力上限のため関連コードの一部を省略しました。"
			call--
			continue
		}
		if err != nil && ctx.Err() == nil && (err.Error() == "ai_input_limit" || err.Error() == "gemini_timeout") && len(step) > 1 {
			parts, ok := domain.SplitPlanStep(impl.Plan.Files, step)
			debugJSON(ctx, "step_split", map[string]any{"step_paths": step, "replacement_steps": parts, "can_split": ok})
			if !ok {
				return nil, err
			}
			run.logger.Info("implement_step_split", "step", impl.NextStep+1, "files", len(step), "error_code", implementationErrorCode(err))
			// Split the step and retry with fewer original files.
			steps := append([]domain.ImplementationStep{}, impl.Steps[:impl.NextStep]...)
			steps = append(steps, parts...)
			impl.Steps = append(steps, impl.Steps[impl.NextStep+1:]...)
			if err.Error() == "gemini_timeout" {
				if saveErr := run.save(ctx, "AIの応答が時間上限に達したため、編集対象を分割して続行します"); saveErr != nil {
					return nil, saveErr
				}
			}
			return nil, nil
		}
		if err != nil {
			message, retry, saveErr := run.retryInvalidResponse(ctx, "edit", call+1, &invalidResponses, &retries, err)
			if saveErr != nil {
				return nil, saveErr
			}
			if retry {
				correction = message
				continue
			}
		}
		if err != nil {
			return nil, err
		}
		// Keep edit feedback across additional reads; only a new rejected edit
		// supersedes it. Reads do not apply or repair the rejected proposal.
		invalidResponses = 0
		if d.Read != nil && !final {
			reads++
			if err := run.read(ctx, *d.Read, &ev, stepEvidenceKept); err != nil {
				return nil, err
			}
			continue
		}
		if d.Implementation == nil {
			run.retry("edit", call+1, &retries, errors.New("invalid_implementation_result"))
			correction = "implementationに変更を返してください。"
			continue
		}
		changes, err := applyProposals(step, impl, originals, d.Implementation.Changes)
		if err != nil {
			run.debugEditFailure(ctx, step, originals, d.Implementation.Changes, err)
		}
		if len(d.Implementation.Changes) == 0 && len(d.Implementation.AdditionalFiles) == 0 && len(d.Implementation.Unverified) > 0 && len(changes) == 0 {
			for _, reason := range d.Implementation.Unverified {
				impl.Unverified = appendUnique(impl.Unverified, domain.Clip(reason, 512))
			}
			if err := run.save(ctx, "変更を作成できなかった理由を保存しました"); err != nil {
				return nil, err
			}
			return nil, errors.New("implementation_edit_blocked")
		}
		if err == nil {
			err = validateNoChangeCandidates(step, impl, originals, d.Implementation)
		}
		if err == nil && (len(changes) > 0 || len(d.Implementation.AdditionalFiles) == 0 && len(d.Implementation.UnchangedFiles) == 0) {
			err = domain.ValidateChangeSet(changes, approved)
		}
		if err != nil {
			run.retry("edit", call+1, &retries, err)
			correction = run.editFailureCorrection(step, err, &editFormat)
			editFailure = editFailureFeedback(originals, d.Implementation.Changes, err)
			if editFormat == lineRangeFormat && call < ImplementStepCalls-1 {
				if err := run.save(ctx, "原文の行範囲を指定する方式で編集を再試行しています"); err != nil {
					return nil, err
				}
			}
			continue
		}
		pending, rejected, err = run.checkAdditions(ctx, d.Implementation.AdditionalFiles)
		if err != nil {
			return nil, err
		}
		if len(changes) == 0 && len(pending) == 0 && len(d.Implementation.UnchangedFiles) == 0 {
			run.retry("edit", call+1, &retries, errors.New("change_set_empty"))
			correction = "有効な変更も追加提案もありません: " + strings.Join(rejected, " / ") + "。変更を返すか、有効な追加ファイルを提案してください。"
			continue
		}
		run.cp.Summary = domain.Clip(d.Summary, 2048)
		return &stepDraft{Changes: changes, Unverified: d.Implementation.Unverified, Pending: pending, Rejected: rejected, Unchanged: d.Implementation.UnchangedFiles}, nil
	}
	return nil, errors.New("implementation_edit_failed")
}

func (run *implementRun) advanceStep(step []string, draft *stepDraft, covered map[string]bool) {
	impl := run.impl
	for _, f := range draft.Unchanged {
		covered[f.Path] = true
		impl.NoChangeCandidates = append(impl.NoChangeCandidates, f)
	}
	// Keep only unfinished files in the current step while an amendment is
	// pending; completed files must not be edited twice on resume.
	remaining := []string{}
	for _, p := range step {
		if !covered[p] {
			remaining = append(remaining, p)
			if len(draft.Pending) == 0 {
				impl.Unverified = append(impl.Unverified, "計画にあるが変更されなかったファイル: "+p)
			}
		}
	}
	holdStep := len(draft.Pending) > 0 && len(remaining) > 0
	if holdStep {
		impl.Steps[impl.NextStep].Files = remaining
	}
	for _, u := range draft.Unverified {
		impl.Unverified = append(impl.Unverified, domain.Clip(u, 512))
	}
	impl.Unverified = append(impl.Unverified, draft.Rejected...)
	if len(impl.Unverified) > 20 {
		impl.Unverified = impl.Unverified[len(impl.Unverified)-20:]
	}
	impl.Summary = run.cp.Summary
	if !holdStep {
		impl.NextStep++
	}
}
