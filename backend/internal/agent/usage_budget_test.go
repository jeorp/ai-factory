package agent

import (
	"ai-factory/backend/internal/domain"
	"context"
	"testing"
)

func TestTokenBudgetStopsNextCallAndSurvivesResume(t *testing.T) {
	calls := 0
	r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{Answer: "done"}, Usage{InputTokens: 8, OutputTokens: 4}, nil
	})}
	s := baseSession()
	s.TokenBudget = 10
	ctx := withTokenBudget(context.Background(), s)
	_, usage, err := r.next(ctx, Input{Session: s}, `{"implementTask":{"mode":"edit"}}`, "edit")
	if err != nil || usage.Phase != "edit" {
		t.Fatalf("first: %+v %v", usage, err)
	}
	addUsage(&s.Metrics, usage)
	for _, c := range []context.Context{ctx, withTokenBudget(context.Background(), s)} {
		if _, _, err := r.next(c, Input{Session: s}, `{}`, "chat"); err == nil || err.Error() != "ai_token_budget_exceeded" {
			t.Fatalf("budget bypassed: %v", err)
		}
	}
	if calls != 1 {
		t.Fatalf("provider called %d times", calls)
	}
	s.TokenBudget = 20
	if _, _, err := r.next(withTokenBudget(context.Background(), s), Input{Session: s}, `{}`, "chat"); err != nil {
		t.Fatal(err)
	}
}

func TestPhaseUsageSnapshotsStayImmutable(t *testing.T) {
	m := domain.Metrics{}
	addUsage(&m, Usage{InputTokens: 2, Phase: "edit", DurationMS: 7})
	previous := m
	addUsage(&m, Usage{InputTokens: 3, OutputTokens: 1, Phase: "edit", DurationMS: 8})
	if previous.Phases["edit"].Calls != 1 || previous.Phases["edit"].InputTokens != 2 {
		t.Fatal("saved map mutated")
	}
	p := m.Phases["edit"]
	if p.Calls != 2 || p.InputTokens != 5 || p.OutputTokens != 1 || p.DurationMS != 15 {
		t.Fatalf("bad phase: %+v", p)
	}
}

func TestImplementationBudgetKeepsDraftAndUsageAcrossResume(t *testing.T) {
	s := baseSession()
	s.RunKind = "implement"
	s.TokenBudget = 10
	s.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Plan: simplePlan(), Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	calls := 0
	r := Runner{Writer: newFakeWriter(), AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		task := implementTaskOf(t, input)
		if task.Mode == "verify" {
			return Decision{Summary: "checked", Verification: passedVerification(task.Plan)}, Usage{InputTokens: 1}, nil
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{InputTokens: 8, OutputTokens: 4}, nil
	})}
	run := func() error {
		return r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { s.Implementation = cp.Implementation; s.Metrics = cp.Metrics; return nil })
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := run(); err == nil || err.Error() != "ai_token_budget_exceeded" {
			t.Fatalf("budget bypassed: %v", err)
		}
		if calls != 1 || len(s.Implementation.Changes) != 1 || s.Metrics.InputTokens+s.Metrics.OutputTokens != 12 {
			t.Fatalf("draft or usage lost: %+v %+v calls=%d", s.Implementation, s.Metrics, calls)
		}
	}
	s.TokenBudget = 30
	if err := run(); err != nil || s.Implementation.Status != "ready" || calls != 2 {
		t.Fatalf("cannot resume verification: %v %+v calls=%d", err, s.Implementation, calls)
	}
}
