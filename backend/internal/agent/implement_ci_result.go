package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// ErrCIWaiting yields the read-only wait without completing the implementation.
// A connected client can resume it without an AI key or another publication.
var ErrCIWaiting = errors.New("implementation_ci_wait")

func ciFailedConclusion(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "cancelled", "action_required", "startup_failure", "stale":
		return true
	}
	return false
}

func (run *implementRun) checkPublishedCI(ctx context.Context) error {
	impl := run.impl
	deadline := time.Now().Add(run.r.CIWait)
	for {
		result := &domain.ImplementationCI{Commit: impl.Commit, State: "pending", Checks: []domain.ImplementationCheck{}, Unverified: []string{}}
		impl.CI = result
		if err := run.save(ctx, "公開したコミットのGitHub CIを確認しています"); err != nil {
			return err
		}
		if run.r.GitHub == nil {
			result.State = "unverified"
			result.Unverified = []string{"CIの読み取りが利用できません。GitHubで結果を確認してください。"}
		} else {
			target := domain.Target{Repository: run.in.Session.Target.Repository, Commit: impl.Commit, Kind: "repository"}
			run.cp.Metrics.ToolCalls++
			raw, source, n, err := run.r.GitHub.Read(ctx, run.in.GitHubToken, target, github.Read{Kind: "check_runs", Number: impl.PullNumber})
			run.cp.Metrics.FetchedBytes += n
			var snapshot struct {
				Total     int  `json:"total_count"`
				Truncated bool `json:"truncated"`
				Checks    []struct {
					Name       string `json:"name"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
					Head       string `json:"head_sha"`
					JobID      int    `json:"job_id"`
				} `json:"check_runs"`
			}
			if err != nil || json.Unmarshal([]byte(raw), &snapshot) != nil {
				result.State = "unverified"
				result.Unverified = []string{"CIのチェック結果を取得できません。ChecksとActionsの読み取り権限を確認してください。"}
			} else {
				if source.URL != "" && !containsSource(run.cp.Sources, source.URL) {
					run.cp.Sources = append(run.cp.Sources, source)
				}
				passed, pending, failed, unknown := 0, false, false, false
				for _, c := range snapshot.Checks {
					result.Checks = append(result.Checks, domain.ImplementationCheck{Name: c.Name, Status: c.Status, Conclusion: c.Conclusion, JobID: c.JobID})
					if c.Head != impl.Commit {
						unknown = true
						continue
					}
					if c.Status != "completed" {
						pending = true
						continue
					}
					if c.Conclusion == "success" {
						passed++
					} else if ciFailedConclusion(c.Conclusion) {
						failed = true
					} else {
						unknown = true
					}
				}
				switch {
				case snapshot.Truncated || snapshot.Total > len(snapshot.Checks):
					result.State = "unverified"
					result.Unverified = []string{"チェック一覧が省略され、全件を確認できません。"}
				case pending:
					// Wait for all registered jobs, so a repair starts with the
					// complete set of failures rather than the first failed job.
				case failed:
					result.State = "failed"
				case unknown:
					result.State = "unverified"
					result.Unverified = []string{"スキップ・不明な結論、または別コミットのチェックがあります。成功として扱いません。"}
				case !pending && passed > 0:
					result.State = "passed"
				}
			}
		}
		if result.State != "pending" || !time.Now().Before(deadline) {
			if result.State == "pending" && run.r.CIWait > 0 {
				if err := run.save(ctx, "CIの完了を待っています"); err != nil {
					return err
				}
				return ErrCIWaiting
			}
			impl.Status = "published"
			run.cp.Answer = fmt.Sprintf("[CIを確認](https://github.com/%s/commit/%s/checks)", run.in.Session.Target.Repository, impl.Commit)
			return run.save(ctx, "CIの確認が完了しました")
		}
		if err := run.save(ctx, "CIの完了を待っています"); err != nil {
			return err
		}
		timer := time.NewTimer(min(10*time.Second, time.Until(deadline)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// Keep the original request intact even at its byte limit. CI instructions are
// a separate server-owned flag, rather than a prefix that clips requirements.
func StartCIFix(impl *domain.Implementation, cacheID string) *domain.Implementation {
	next := StartImplementation(impl, ImplementRequest{Request: impl.Request, PullNumber: impl.PullNumber}, cacheID)
	next.CIRepair = true
	if impl.Plan != nil {
		next.RequiredCriteria = append([]domain.AcceptanceCriterion{}, impl.Plan.AcceptanceCriteria...)
	}
	return next
}
