package agent

import (
	"context"
	"errors"
	"log/slog"
	"strings"
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

func TestInputSheddingDoesNotConsumeEditRetries(t *testing.T) {
	w := newFakeWriter()
	original := strings.Repeat("x~", 27000) + "\nreturn 1\n"
	w.files["src/a.go"] = original
	contextPaths := []string{"src/ctx1.go", "src/ctx2.go", "src/ctx3.go"}
	for _, path := range contextPaths {
		w.files[path] = strings.Repeat("c ", 3000)
	}
	impl := &domain.Implementation{BaseCommit: implBase, Plan: simplePlan(), Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}, ContextFiles: contextPaths}
	calls := 0
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		task := implementTaskOf(t, input)
		if strings.Count(task.Correction, "入力上限のため関連コードの一部を省略しました。") != 1 || strings.Contains(input, `"path":"src/ctx`) {
			t.Fatal("context was not shed or omission feedback was duplicated")
		}
		action := "create" // force one real correction after the initial call
		if calls == 2 {
			action = "update"
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: action, Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{InputTokens: 1}, nil
	})}
	run := &implementRun{r: r, in: Input{Session: baseSession()}, impl: impl, logger: slog.Default(), emit: func(Checkpoint) error { return nil }}
	draft, err := run.generateStep(context.Background(), []string{"src/a.go"}, []implementFile{{Path: "src/a.go", Action: "update", Original: original}}, map[string]string{"src/a.go": original})
	if err != nil || draft == nil || calls != 2 || run.calls != 2 {
		t.Fatalf("input shedding consumed model attempts: err=%v calls=%d model_calls=%d", err, calls, run.calls)
	}
	if run.cp.Metrics.Phases["edit"].Calls != 1 || run.cp.Metrics.Phases["edit_retry"].Calls != 1 {
		t.Fatalf("input shedding polluted retry usage: %+v", run.cp.Metrics.Phases)
	}
}

func TestUsageCheckpointKeepsImplementationProgress(t *testing.T) {
	for _, modelErr := range []error{nil, errors.New("gemini_timeout")} {
		t.Run(outcome(modelErr), func(t *testing.T) {
			const progress = "AIの応答形式を訂正して再試行しています（1/2回）"
			saved := 0
			run := &implementRun{
				r: &Runner{Writer: newFakeWriter(), AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
					return Decision{}, Usage{InputTokens: 3}, modelErr
				})},
				in: Input{Session: baseSession()}, impl: &domain.Implementation{BaseCommit: implBase}, logger: slog.Default(),
				cp: Checkpoint{Progress: progress},
				emit: func(cp Checkpoint) error {
					saved++
					if cp.Progress != progress || cp.Metrics.InputTokens != 3 || cp.Metrics.Phases["edit"].Calls != 1 {
						t.Fatalf("usage checkpoint overwrote progress or lost usage: %+v", cp)
					}
					return nil
				},
			}
			_, err := run.call(context.Background(), map[string]any{"implementTask": implementTask{Mode: "edit"}})
			if !errors.Is(err, modelErr) || saved != 1 {
				t.Fatalf("usage not persisted before returning: err=%v saves=%d", err, saved)
			}
		})
	}
}
