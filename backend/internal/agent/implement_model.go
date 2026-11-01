package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	if err := run.loadProjectDocumentation(ctx); err != nil {
		return Decision{}, err
	}
	run.drain()
	payload["instructions"] = run.pinned()
	payload["target"] = run.target()
	payload["repositoryOverview"] = run.in.Session.RepositoryOverview
	doc, err := run.currentProjectDocumentation(ctx)
	if err != nil {
		return Decision{}, err
	}
	if doc != nil {
		payload["projectDocumentation"] = doc
	}
	if run.impl.CIRepair {
		payload["ciRepair"] = true
		payload["requiredCriteria"] = run.impl.RequiredCriteria
	}
	if len(run.ciEvidence) > 0 {
		payload["ciEvidence"] = run.ciEvidence
	}
	body, _ := json.Marshal(payload)
	if !implementInputFits(body) && doc != nil {
		// Keep an explicit indication that the documentation was omitted.
		payload["projectDocumentation"] = &projectDocumentation{Path: doc.Path, Unavailable: true}
		body, _ = json.Marshal(payload)
	}
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
	if implementationErrorCode(err) == "invalid_implementation_result" {
		log.Info("implement_response_rejected", "reason", implementationResultReason(err), "has_read", d.Read != nil, "has_search", d.Search != nil, "has_plan", d.Plan != nil, "has_implementation", d.Implementation != nil, "has_verification", d.Verification != nil, "has_start_implementation", d.StartImplementation != nil, "has_start_review", d.StartReview != nil, "has_review", d.Review != nil, "answer_bytes", len(d.Answer), "summary_bytes", len(d.Summary))
	}
	if d.IntentIssue == "empty_fields" || d.IntentIssue == "field_length_limit" {
		log.Info("implement_intent_discarded", "reason", d.IntentIssue)
	}
	addUsage(&run.cp.Metrics, usage)
	if err != nil {
		return d, err
	}
	raw, _ := json.Marshal(d)
	if echoes(string(raw), run.in) {
		return d, errors.New("credential_echo_blocked")
	}
	return d, nil
}
