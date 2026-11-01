package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestDebugExplainsReviewFindingDisposition(t *testing.T) {
	var logs bytes.Buffer
	ctx := debugTestContext(&logs, true)
	run := &reviewRun{review: &domain.Review{}, logger: slog.New(slog.NewJSONHandler(&logs, nil))}
	task := reviewTask{Mode: "diff", Patches: []reviewPatch{{Path: "api/server.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"}}}
	f := finding("api/server.go", 1)
	run.acceptFinding(ctx, task, nil, f)
	run.acceptFinding(ctx, task, nil, f)
	run.acceptFinding(ctx, task, nil, finding("missing.go", 999))
	if len(run.review.Findings) != 1 {
		t.Fatal("diagnostics changed finding acceptance")
	}
	for _, want := range []string{"agent_debug_review_finding_decision", "accepted", "duplicate", "unsupported_location_or_sources", "Contract mismatch"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing disposition %s", want)
		}
	}
}

func TestDebugExplainsChatEvidenceAndHistoryOmissions(t *testing.T) {
	var logs bytes.Buffer
	s := baseSession()
	for i := 0; i < 12; i++ {
		s.Messages = append(s.Messages, domain.Message{ID: strings.Repeat("m", i+1), Role: "user", Text: "history"})
	}
	ev := []evidence{{Read: github.Read{Kind: "file", Path: "old.go"}, Content: strings.Repeat("a", 16000)}, {Read: github.Read{Kind: "file", Path: "new.go"}, Content: strings.Repeat("b", 16000)}}
	input := BuildContext(s, "summary", nil, ev, nil)
	debugChatContext(debugTestContext(&logs, true), s, ev, input)
	var event struct{ Payload string }
	if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		Total    int   `json:"history_messages_total"`
		History  []any `json:"history_sent"`
		Received []any `json:"evidence_received"`
		Sent     []any `json:"evidence_sent"`
	}
	if err := json.Unmarshal([]byte(event.Payload), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Total != 13 || len(detail.History) > 6 || len(detail.Received) != 2 || len(detail.Sent) >= 2 {
		t.Fatalf("missing omissions: %+v", detail)
	}
}

func TestDebugExplainsVerificationAndRepairBlocking(t *testing.T) {
	var logs bytes.Buffer
	r := &Runner{Debug: true, Writer: newFakeWriter(), AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "verify" {
			return Decision{Summary: "checked", Verification: &domain.ImplementationVerification{Issues: []string{"incomplete implementation"}, Criteria: []domain.CriterionResult{{ID: "behavior", Status: "unmet", Evidence: "missing branch", Paths: []string{"src/a.go"}}}}}, Usage{}, nil
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{}, nil
	})}
	s := baseSession()
	s.RunKind = "implement"
	s.Implementation = &domain.Implementation{Request: "fix", Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "A", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s, Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil || last.Implementation.Status != "verification_blocked" {
		t.Fatalf("unexpected state: %v %+v", err, last.Implementation)
	}
	for _, want := range []string{"agent_debug_verification_coverage", "observed_paths", "missing branch", "can_publish", "agent_debug_repair_decision", "no_eligible_repairs"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing quality decision %s", want)
		}
	}
}
