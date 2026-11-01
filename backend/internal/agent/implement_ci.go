package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"ai-factory/backend/internal/domain"
)

var ciSubject = regexp.MustCompile(`(?i)\bCI\b|workflow|ワークフロー|ビルド|テスト|チェック`)
var ciFailure = regexp.MustCompile(`(?i)failure|failed|failing|broken|失敗|不具合|エラー|落ち`)
var ciFailureLine = regexp.MustCompile(`\[failure L([0-9]+)\]`)

func (run *implementRun) validateCIPlanCoverage(plan *domain.ImplementationPlan) error {
	covered := map[string]bool{}
	for _, c := range plan.AcceptanceCriteria {
		for _, ref := range c.FailureRefs {
			covered[ref] = true
		}
	}
	for _, ev := range run.ciEvidence {
		if ev.Read.Kind != "job_log" {
			continue
		}
		for _, m := range ciFailureLine.FindAllStringSubmatch(ev.Content, -1) {
			ref := fmt.Sprintf("%d:L%s", ev.Read.Number, m[1])
			if !covered[ref] {
				return fmt.Errorf("invalid_ci_coverage: failureRefsに%sの修正方針がありません", ref)
			}
		}
	}
	return nil
}

func implementationNeedsCI(request string) bool {
	return ciSubject.MatchString(request) && ciFailure.MatchString(request)
}

func ciReadError(err error) error {
	if implementationErrorCode(err) == "github_http_403" {
		return errors.New("implementation_ci_forbidden")
	}
	if err != nil && err.Error() == "implementation_ci_incomplete" {
		return err
	}
	return errors.New("implementation_ci_unavailable")
}

func incompleteCIEvidence(ev []evidence) bool {
	for _, item := range ev {
		if strings.Contains(item.Content, "[INCOMPLETE:") {
			return true
		}
	}
	return false
}

// A CI-fix request must not generate speculative changes without its evidence.
// Other changes on a PR can still proceed without CI access.
func (run *implementRun) requireCIEvidence(ctx context.Context, err error) error {
	if err == nil || !(run.impl.CIRepair || implementationNeedsCI(run.impl.Request)) {
		return nil
	}
	reason := "CIの失敗情報を取得できません。対象PRのチェック結果と失敗ログを確認してください。"
	if err.Error() == "implementation_ci_incomplete" {
		reason = "CIログやチェック一覧が入力上限を超え、全失敗を確認できません。ログ・対象ジョブを絞って再実行してください。"
	}
	if err.Error() == "implementation_ci_pending" {
		reason = "CIに実行中のチェックがあります。全ジョブが完了してから、失敗ログを取得して修正を再開してください。"
	}
	if err.Error() == "implementation_ci_not_failed" {
		reason = "現在のコミットにCI失敗が見つかりません。対象PR・コミットとCI結果を再確認してください。"
	}
	if err.Error() == "implementation_ci_forbidden" {
		reason = "GitHub AppがCI情報の読み取りを403で拒否されました。ChecksとActionsのRead-only権限、インストール先の権限更新を確認し、GitHubでログインし直してください。"
	}
	run.impl.Unverified = appendUnique(run.impl.Unverified, reason)
	if saveErr := run.save(ctx, "CIの根拠を取得できないため、変更を作成せずに中断しました"); saveErr != nil {
		return saveErr
	}
	return err
}
