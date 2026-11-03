package agent

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func TestEditingTimeoutSplitsAndFinishesWithoutPublishing(t *testing.T) {
	w := newFakeWriter()
	w.files["src/b.go"] = "package src\nfunc B() int { return 1 }\n"
	impl := &domain.Implementation{Status: "editing", Request: "update", BaseCommit: implBase, BaseBranch: "main", CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{
		Summary: "update", AcceptanceCriteria: testCriteria("src/a.go"),
		Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}, {Path: "src/b.go", Action: "update"}},
	}}
	impl.Steps = domain.PlanSteps(impl.Plan.Files)
	calls := 0
	splitSaved := false
	var last Checkpoint
	var logs bytes.Buffer
	r := &Runner{Writer: w, AI: implementProviderFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		task := implementTaskOf(t, input)
		if calls == 1 {
			if len(task.Files) != 2 {
				t.Fatal("fixture did not batch files")
			}
			return Decision{}, Usage{}, errors.New("gemini_timeout")
		}
		if !splitSaved || len(task.Files) != 1 {
			t.Fatal("split not saved before retry")
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: task.Files[0].Path, Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{}, nil
	})}
	s := baseSession()
	s.RunKind, s.Implementation = "implement", impl
	err := r.Run(context.Background(), Input{Session: s, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(cp Checkpoint) error {
		last = cp
		if strings.Contains(cp.Progress, "分割") {
			splitSaved = true
		}
		return nil
	})
	if err != nil || calls != 3 || last.Implementation.Status != "ready" || len(last.Implementation.Changes) != 2 || len(w.published) != 0 {
		t.Fatalf("err=%v calls=%d impl=%+v", err, calls, last.Implementation)
	}
	if !strings.Contains(logs.String(), `"msg":"implement_step_split"`) || !strings.Contains(logs.String(), `"error_code":"gemini_timeout"`) {
		t.Fatal("split reason missing")
	}
}

func TestEditingTimeoutStopsForAtomicOrSingleFileStep(t *testing.T) {
	for _, cycle := range []bool{false, true} {
		w := newFakeWriter()
		files := []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}
		if cycle {
			files[0].DependsOn = []string{"src/old.go"}
			files = append(files, domain.PlannedFile{Path: "src/old.go", Action: "update", DependsOn: []string{"src/a.go"}})
		}
		impl := &domain.Implementation{Status: "editing", BaseCommit: implBase, BaseBranch: "main", CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{Summary: "update", Files: files, AcceptanceCriteria: testCriteria("src/a.go")}}
		impl.Steps = domain.PlanSteps(files)
		calls := 0
		r := &Runner{Writer: w, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
			calls++
			return Decision{}, Usage{}, errors.New("gemini_timeout")
		})}
		err := runImplementationFixture(r, impl, func(Checkpoint) error { return nil })
		if err == nil || err.Error() != "gemini_timeout" || calls != 2 || len(w.cache) != 0 {
			t.Fatalf("cycle=%t calls=%d err=%v", cycle, calls, err)
		}
	}
}

func TestEditingTimeoutDoesNotContinueAfterCancelOrSaveFailure(t *testing.T) {
	for _, stop := range []string{"cancel", "checkpoint"} {
		t.Run(stop, func(t *testing.T) {
			w := newFakeWriter()
			impl := &domain.Implementation{Status: "editing", BaseCommit: implBase, BaseBranch: "main", CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{Summary: "update", AcceptanceCriteria: testCriteria("src/a.go"), Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}, {Path: "src/old.go", Action: "update"}}}}
			impl.Steps = domain.PlanSteps(impl.Plan.Files)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			r := &Runner{Writer: w, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				calls++
				if stop == "cancel" {
					cancel()
				}
				return Decision{}, Usage{}, errors.New("gemini_timeout")
			})}
			s := baseSession()
			s.RunKind, s.Implementation = "implement", impl
			err := r.Run(ctx, Input{Session: s}, func(cp Checkpoint) error {
				if stop == "checkpoint" && strings.Contains(cp.Progress, "分割") {
					return errors.New("checkpoint_failed")
				}
				return nil
			})
			if err == nil || calls != 1 || len(w.cache) != 0 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
			if stop == "checkpoint" && err.Error() != "checkpoint_failed" {
				t.Fatal(err)
			}
		})
	}
}
