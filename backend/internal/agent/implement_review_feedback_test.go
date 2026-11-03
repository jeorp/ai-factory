package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func TestInvalidNoChangeCandidateReceivesActionableCorrection(t *testing.T) {
	w := newFakeWriter()
	s := recoverySession(w, "edit")
	w.cache = map[string]domain.FileChange{}
	s.Implementation.Plan.Files = append(s.Implementation.Plan.Files, domain.PlannedFile{Path: "src/new.go", Action: "create", Approach: "add helper"})
	s.Implementation.Steps = domain.PlanSteps(s.Implementation.Plan.Files)
	calls := 0
	r := &Runner{Writer: w, AI: implementProviderFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		task := implementTaskOf(t, input)
		result := &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}
		if calls == 1 {
			result.UnchangedFiles = []domain.UnchangedFile{{Path: "src/new.go", Reason: "not needed"}}
		} else {
			if len(w.cache) != 0 || len(task.Done) != 0 {
				t.Fatal("rejected response partially saved")
			}
			if !strings.Contains(task.Correction, `path="src/new.go"`) || !strings.Contains(task.Correction, "create/delete") || !strings.Contains(task.Correction, "有効なchangesは維持") {
				t.Fatalf("unhelpful correction: %s", task.Correction)
			}
			result.Changes = append(result.Changes, ChangeProposal{Path: "src/new.go", Action: "create", Content: "package src\nfunc Helper() {}\n"})
		}
		return Decision{Implementation: result}, Usage{}, nil
	})}
	var last Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil || calls != 2 || last.Implementation.Status != "ready" || len(last.Implementation.Changes) != 2 {
		t.Fatalf("calls=%d err=%v impl=%+v", calls, err, last.Implementation)
	}
}

func TestNoChangeCandidatesDeduplicateByPathOnReplay(t *testing.T) {
	impl := &domain.Implementation{NoChangeCandidates: []domain.UnchangedFile{{Path: "src/a.go", Reason: "old"}, {Path: "src/a.go", Reason: "duplicate"}}}
	run := &implementRun{impl: impl}
	draft := &stepDraft{Unchanged: []domain.UnchangedFile{{Path: "src/a.go", Reason: "latest evidence"}}}
	for range 2 {
		run.advanceStep([]string{"src/a.go"}, draft, map[string]bool{})
	}
	if len(impl.NoChangeCandidates) != 1 || impl.NoChangeCandidates[0].Reason != "latest evidence" {
		t.Fatalf("duplicate candidates: %+v", impl.NoChangeCandidates)
	}
}

func TestDeferredChecksAreServerOwned(t *testing.T) {
	w := newFakeWriter()
	s := recoverySession(w, "verify")
	s.Implementation.Plan.Tests = []string{"go test ./..."}
	calls := 0
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		d := recoveryDecision(t, input)
		if calls == 1 {
			d.Verification.DeferredChecks = []string{"Required API language propagation has not been implemented"}
		}
		// Use the same validation boundary as real providers.
		if issue := implementationResponseIssue(input, d); issue != "" {
			return d, Usage{}, &implementationResultError{reason: issue}
		}
		return d, Usage{}, nil
	})}
	var last Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil || calls != 2 || last.Implementation.Status != "ready" {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	checks := last.Implementation.Verification.DeferredChecks
	if len(checks) != 1 || checks[0] != "go test ./..." {
		t.Fatalf("model modified execution checks: %v", checks)
	}
	// Only schema fields may be returned by a model. Existing saved results can
	// still carry this server-owned property to the UI and PR formatter.
	schema := object(map[string]any{"read": nullable(implementRead([]string{"file"}))})
	implementInstructions(schema, implementTask{Mode: "verify"})
	encoded, _ := json.Marshal(schema)
	if strings.Contains(string(encoded), "deferredChecks") {
		t.Fatal("server field exposed in model response schema")
	}
}

func TestPlanAdjustmentReasonsStayOnOnePRBodyLine(t *testing.T) {
	w := newFakeWriter()
	s := noChangeFixture(w)
	s.Implementation.PlanAdjustments = []domain.PlanAdjustment{{File: domain.PlannedFile{Path: "src/old.go", Action: "update"}, Reason: "No prompts.\n\n## injected heading\r\n- injected list"}}
	body := defaultBody(s.Implementation)
	if strings.Contains(body, "\n## injected") || strings.Contains(body, "\n- injected") || !strings.Contains(body, "No prompts. ## injected heading - injected list") {
		t.Fatal("reason altered PR structure")
	}
}

func TestDeferredModelClaimsCannotPassVerification(t *testing.T) {
	w := newFakeWriter()
	s := recoverySession(w, "verify")
	calls := 0
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		d := recoveryDecision(t, input)
		d.Verification.DeferredChecks = []string{"required API language propagation is missing"}
		return d, Usage{}, &implementationResultError{reason: implementationResponseIssue(input, d)}
	})}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
	if err == nil || err.Error() != "implementation_verification_failed" || calls != 3 || last.Implementation.CanPublish() || len(w.published) != 0 {
		t.Fatalf("unsupported model claims passed: err=%v calls=%d", err, calls)
	}
}
