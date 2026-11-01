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

func TestCannotPublishWhileVerificationIsUnfinished(t *testing.T) {
	session := &domain.Session{Implementation: &domain.Implementation{Status: "verifying", Changes: []domain.ChangeSummary{{Path: "src/a.go", Action: "update"}}}}
	if got := implementCommandProblem(session, &command{Type: "implement_publish"}); got != "implementation_not_ready" {
		t.Fatalf("unfinished verification can publish: %s", got)
	}
}

func TestRevisionApprovalRetainsDraftAndRequiresClarification(t *testing.T) {
	session := &domain.Session{Implementation: &domain.Implementation{Status: "plan_ready", Revising: true, Clarification: "どちらを直しますか", Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}}}, Changes: []domain.ChangeSummary{{Path: "a.go", Action: "update"}}, Unverified: []string{"CIは未実行"}}}
	if got := implementCommandProblem(session, &command{Type: "implement_approve"}); got != "implementation_plan_required" {
		t.Fatalf("clarification can be approved: %s", got)
	}
	session.Implementation.Clarification = ""
	if got := implementCommandProblem(session, &command{Type: "implement_approve"}); got != "" {
		t.Fatalf("revision approval rejected: %s", got)
	}
	applyImplementCommand(session, command{Type: "implement_approve"})
	if len(session.Implementation.Changes) != 1 || len(session.Implementation.Unverified) != 1 || session.Implementation.Status != "editing" {
		t.Fatalf("draft cleared: %+v", session.Implementation)
	}
}
