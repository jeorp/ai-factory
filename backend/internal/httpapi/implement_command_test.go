package httpapi

import (
	"strings"
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

func TestPublishRejectsReadyStateWithoutAcceptanceCoverage(t *testing.T) {
	session := &domain.Session{Implementation: &domain.Implementation{Status: "ready", Changes: []domain.ChangeSummary{{Path: "a.go", Action: "update"}}, Verification: &domain.ImplementationVerification{}}}
	if got := implementCommandProblem(session, &command{Type: "implement_publish", Branch: "ai-factory/x", Title: "x"}); got != "implementation_verification_incomplete" {
		t.Fatalf("forged ready bypassed coverage: %s", got)
	}
}

func TestCIFeedbackCommandsKeepPublishedCommitAndOriginalRequest(t *testing.T) {
	session := &domain.Session{Implementation: &domain.Implementation{Status: "published", Request: "UI言語を保存し全AI呼び出しへ渡す", Commit: strings.Repeat("d", 40), BaseCommit: strings.Repeat("c", 40), Branch: "ai-factory/x", PullNumber: 7}}
	check := command{Type: "implement_check_ci"}
	if got := implementCommandProblem(session, &check); got != "" {
		t.Fatal(got)
	}
	applyImplementCommand(session, check)
	if session.Implementation.Status != "checking_ci" || session.Implementation.Commit != strings.Repeat("d", 40) {
		t.Fatal("CI check reset publication")
	}
	impl := session.Implementation
	impl.Status = "published"
	fix := command{Type: "implement_fix_ci"}
	if implementCommandProblem(session, &fix) == "" {
		t.Fatal("CI repair accepted without failure")
	}
	impl.CI = &domain.ImplementationCI{Commit: impl.Commit, State: "failed"}
	if got := implementCommandProblem(session, &fix); got != "" {
		t.Fatal(got)
	}
	applyImplementCommand(session, fix)
	next := session.Implementation
	if next.Status != "planning" || next.ExpectedHead != impl.Commit || next.PullNumber != 7 || next.Branch != impl.Branch || !strings.Contains(next.Request, "UI言語を保存し全AI呼び出しへ渡す") {
		t.Fatalf("CI fix lost request/head: %+v", next)
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
