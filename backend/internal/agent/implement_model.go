package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ai-factory/backend/internal/domain"
)

func (run *implementRun) drain() {
	for {
		select {
		case m, open := <-run.in.Instructions:
			if !open {
				return
			}
			run.instructions = append(run.instructions, m)
		default:
			return
		}
	}
}

func (run *implementRun) pinned() string {
	return domain.LatestInstructions(run.instructions, 4096)
}

func (run *implementRun) target() domain.Target {
	return domain.Target{Repository: run.in.Session.Target.Repository, Commit: run.impl.BaseCommit, Kind: "repository"}
}

// call sends one bounded model request and records usage. Instructions sent
// with the request are marked applied in the next checkpoint.
func (run *implementRun) call(ctx context.Context, payload map[string]any) (Decision, error) {
	if run.calls >= ImplementMaxCalls {
		return Decision{}, errors.New("implementation_call_limit")
	}
	run.drain()
	payload["instructions"] = run.pinned()
	payload["target"] = run.target()
	payload["repositoryOverview"] = run.in.Session.RepositoryOverview
	body, _ := json.Marshal(payload)
	if !implementInputFits(body) {
		return Decision{}, errors.New("ai_input_limit")
	}
	run.calls++
	run.cp.Applied = nil
	for _, m := range run.instructions {
		run.cp.Applied = append(run.cp.Applied, m.ID)
	}
	if len(body) > run.cp.Metrics.MaxInputBytes {
		run.cp.Metrics.MaxInputBytes = len(body)
	}
	started := time.Now()
	task, _ := payload["implementTask"].(implementTask)
	log := run.logger.With("phase", task.Mode, "step", task.Step, "total_steps", task.TotalSteps, "call", run.calls, "attempt", max(1, task.Attempt), "retry_count", task.RetryCount)
	if stage, _ := payload["stage"].(string); task.Mode == "verify" && (stage == "parts" || stage == "cross") {
		log = log.With("verification_stage", stage)
	}
	log.Info("implement_model_call_started", "input_bytes", len(body))
	d, usage, err := run.r.AI.Next(ctx, run.in.Key, run.in.Session.Model, string(body))
	log.Info("implement_model_call", "duration_ms", time.Since(started).Milliseconds(), "outcome", outcome(err), "error_code", implementationErrorCode(err), "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens)
	if d.IntentIssue == "empty_fields" || d.IntentIssue == "field_length_limit" {
		log.Info("implement_intent_discarded", "reason", d.IntentIssue)
	}
	run.cp.Metrics.InputTokens += usage.InputTokens
	run.cp.Metrics.OutputTokens += usage.OutputTokens
	run.cp.Metrics.LastInputTokens += usage.InputTokens
	run.cp.Metrics.LastOutputTokens += usage.OutputTokens
	if usage.InputTokens != 0 || usage.OutputTokens != 0 {
		run.cp.Metrics.HasLastUsage = true
	}
	if err != nil {
		return d, err
	}
	raw, _ := json.Marshal(d)
	if (len(run.in.Key) > 0 && strings.Contains(string(raw), run.in.Key)) || (len(run.in.GitHubToken) > 0 && strings.Contains(string(raw), run.in.GitHubToken)) {
		return d, errors.New("credential_echo_blocked")
	}
	return d, nil
}
