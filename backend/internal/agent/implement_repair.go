package agent

import (
	"context"
	"fmt"
	"strings"

	"ai-factory/backend/internal/domain"
)

const maxImplementationRepairs = 2

// repair schedules recommendations against the existing approved plan. The
// model can recommend paths, but cannot expand approval or change actions.
func (run *implementRun) repair(ctx context.Context, result *domain.ImplementationVerification) (bool, error) {
	impl := run.impl
	if impl.RepairRound >= maxImplementationRepairs {
		debugJSON(ctx, "repair_decision", map[string]any{"reason": "repair_limit", "round": impl.RepairRound, "verification": result})
		result.Unverified = appendUnique(result.Unverified, "自動修正の上限（2回）に達しました。残っている指摘を確認してください。")
		return false, nil
	}
	fixes := map[string]string{}
	for _, repair := range result.Repairs {
		fixes[repair.Path] = repair.Approach
	}
	files := []domain.PlannedFile{}
	for _, planned := range impl.Plan.Files {
		approach, ok := fixes[planned.Path]
		if !ok {
			continue
		}
		delete(fixes, planned.Path)
		if planned.Action == "delete" {
			result.Unverified = appendUnique(result.Unverified, "削除済みファイル自体の自動修正は行いません: "+planned.Path)
			continue
		}
		planned.Approach = approach
		files = append(files, planned)
	}
	for path := range fixes {
		result.Unverified = appendUnique(result.Unverified, "承認範囲外の修正候補は自動変更しません: "+path)
	}
	pending, rejected, err := run.checkAdditions(ctx, result.AdditionalFiles)
	if err != nil {
		return false, err
	}
	result.Unverified = appendUnique(result.Unverified, rejected...)
	if len(files) == 0 && len(pending) == 0 {
		debugJSON(ctx, "repair_decision", map[string]any{"reason": "no_eligible_repairs", "verification": result, "rejected_additions": rejected})
		return false, nil
	}
	debugJSON(ctx, "repair_decision", map[string]any{"reason": "scheduled", "round": impl.RepairRound + 1, "selected_files": files, "pending_files": pending, "rejected_additions": rejected, "verification": result})
	impl.EditStartRevision = impl.ChangeRevision
	impl.RepairRound++
	feedback := append([]string{}, result.Issues...)
	for _, f := range files {
		feedback = append(feedback, f.Path+": "+f.Approach)
	}
	impl.RepairFeedback = domain.Clip(strings.Join(feedback, "\n"), 8192)
	impl.Steps = append(impl.Steps, domain.PlanSteps(files)...)
	run.logger.Info("implement_repair_scheduled", "round", impl.RepairRound, "files", len(files), "pending_files", len(pending))
	if len(pending) > 0 {
		impl.PendingFiles, impl.Status = pending, "amend_ready"
		run.cp.Answer = formatAmendment(impl)
		return true, run.save(ctx, "整合性確認で必要になった追加ファイルの承認を待っています")
	}
	impl.Status = "editing"
	if err := run.save(ctx, fmt.Sprintf("整合性の指摘を修正して再確認します（%d/%d回）", impl.RepairRound, maxImplementationRepairs)); err != nil {
		return true, err
	}
	return true, run.edit(ctx)
}
