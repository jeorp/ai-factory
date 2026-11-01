package httpapi

import (
	"testing"

	"ai-factory/backend/internal/domain"
)

func TestImplementAmendCommand(t *testing.T) {
	session := &domain.Session{Implementation: &domain.Implementation{Status: "editing", Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}}}}}
	if got := implementCommandProblem(session, &command{Type: "implement_amend", Text: "approve"}); got != "implementation_amend_not_pending" {
		t.Fatalf("amend without pending files: %s", got)
	}
	session.Implementation.Status = "amend_ready"
	session.Implementation.PendingFiles = []domain.PendingFile{{Path: "b.go", Action: "update", Approach: "x", Reason: "y"}}
	if got := implementCommandProblem(session, &command{Type: "implement_amend", Text: "maybe"}); got != "invalid_implementation_amend" {
		t.Fatalf("unknown answer accepted: %s", got)
	}
	cmd := command{Type: "implement_amend", Text: "approve"}
	if got := implementCommandProblem(session, &cmd); got != "" {
		t.Fatalf("valid amend rejected: %s", got)
	}
	applyImplementCommand(session, cmd)
	impl := session.Implementation
	if impl.Status != "editing" || len(impl.Plan.Files) != 2 || len(impl.Steps) != 1 || impl.PendingFiles != nil {
		t.Fatalf("amend not applied: %+v", impl)
	}
}
