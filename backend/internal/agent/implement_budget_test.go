package agent

import (
	"context"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestReadsLeaveThreeEditAttempts(t *testing.T) {
	w := newFakeWriter()
	s := baseSession()
	s.RunKind = "implement"
	s.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Plan: simplePlan(), Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	calls, reads, edits := 0, 0, 0
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		calls++
		if task.ReadBudget > 0 {
			reads++
			return Decision{Read: &github.Read{Kind: "file", Path: "src/old.go"}}, Usage{}, nil
		}
		edits++
		action := "create"
		if edits == 3 {
			action = "update"
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: action, Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{}, nil
	})
	reader := readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		return "package src", domain.Source{}, 11, nil
	})
	var last Checkpoint
	err := (&Runner{AI: ai, Writer: w, GitHub: reader}).Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil || calls != 5 || reads != 2 || edits != 3 || last.Implementation.Status != "ready" {
		t.Fatalf("calls=%d reads=%d edits=%d status=%s err=%v", calls, reads, edits, last.Implementation.Status, err)
	}
}
