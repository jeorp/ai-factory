package agent

import (
	"context"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func preservationFixture(t *testing.T) (*revisionWriter, *domain.Implementation) {
	t.Helper()
	w := newRevisionWriter()
	w.files["src/c.go"] = "base c"
	w.list = []string{"src/a.go", "src/c.go", "src/old.go"}
	old := &domain.Implementation{Request: "original request", Status: "ready", BaseBranch: "main", BaseCommit: implBase, CacheID: "original-draft", Version: 2, Plan: &domain.ImplementationPlan{Summary: "original", AcceptanceCriteria: testCriteria("src/a.go")}}
	for _, f := range []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "saved a"}, {Path: "src/b.go", Action: "create", Content: "saved b"}, {Path: "src/c.go", Action: "update", Content: "saved c"}, {Path: "src/old.go", Action: "delete"}} {
		old.Plan.Files = append(old.Plan.Files, domain.PlannedFile{Path: f.Path, Action: f.Action, Approach: "original"})
		old.Changes = append(old.Changes, domain.ChangeSummary{Path: f.Path, Action: f.Action, Bytes: len(f.Content)})
		if err := w.PutChange(context.Background(), old.CacheID, old.Version, f); err != nil {
			t.Fatal(err)
		}
	}
	return w, old
}

func TestRevisionPreservesUnchangedFilesWhileEditingNewFile(t *testing.T) {
	for _, explicit := range []bool{true, false} {
		name := "omitted"
		if explicit {
			name = "explicit-noop"
		}
		t.Run(name, func(t *testing.T) {
			w, old := preservationFixture(t)
			next := domain.NextImplementation(old, "append to b and keep everything else", "revised-draft")
			calls := 0
			r := &Runner{Writer: w, AI: implementProviderFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				task := implementTaskOf(t, input)
				if task.Mode == "plan" {
					return Decision{Plan: &domain.ImplementationPlan{Summary: "append to b", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "retain"}, {Path: "src/b.go", Action: "create", Approach: "append"}}}}, Usage{}, nil
				}
				calls++
				for _, f := range task.Files {
					if !f.PreserveExisting {
						t.Fatalf("saved draft not identified to model: %+v", f)
					}
					if f.Path == "src/a.go" && f.Original != "saved a" || f.Path == "src/b.go" && f.Original != "saved b" {
						t.Fatalf("lost saved content: %+v", f)
					}
				}
				changes := []ChangeProposal{{Path: "src/b.go", Action: "create", Content: "saved b\nappended"}}
				if explicit {
					changes = append(changes, ChangeProposal{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "saved a", Replace: "saved a"}}})
				}
				return Decision{Implementation: &ImplementResult{Changes: changes}}, Usage{}, nil
			})}
			var last Checkpoint
			save := func(cp Checkpoint) error { last = cp; return nil }
			if err := runImplementationFixture(r, next, save); err != nil {
				t.Fatal(err)
			}
			next = last.Implementation
			next.Status = "editing"
			if err := runImplementationFixture(r, next, save); err != nil {
				t.Fatal(err)
			}
			next = last.Implementation
			if next.Status != "ready" || calls != 1 || len(next.Unverified) != 0 || len(next.Changes) != 4 {
				t.Fatalf("preservation retried or blocked: calls=%d %+v", calls, next)
			}
			for _, summary := range old.Changes {
				before, _ := w.GetChange(context.Background(), old.CacheID, old.Version, summary.Path)
				after, ok := w.GetChange(context.Background(), next.CacheID, next.Version, summary.Path)
				if summary.Path == "src/b.go" {
					before.Content += "\nappended"
				}
				if !ok || before != after {
					t.Fatalf("lost draft change %s: %+v", summary.Path, after)
				}
			}
			next.Status = "publishing"
			if err := runImplementationFixture(r, next, save); err != nil {
				t.Fatal(err)
			}
			if len(w.published) != 1 || len(w.published[0].Files) != 4 || w.published[0].BaseCommit != implBase {
				t.Fatalf("published partial draft: %+v", w.published)
			}
		})
	}
}

func TestRevisionCanKeepEntireSavedStepWithoutWritingOrChangingRevision(t *testing.T) {
	w, old := preservationFixture(t)
	next := domain.NextImplementation(old, "retain the current draft", "retained-draft")
	next.Status, next.DraftSource, next.ChangeRevision = "editing", nil, 7
	next.Steps = []domain.ImplementationStep{{Files: []string{"src/a.go", "src/b.go", "src/old.go"}}}
	for _, c := range old.Changes {
		f, _ := w.GetChange(context.Background(), old.CacheID, old.Version, c.Path)
		_ = w.PutChange(context.Background(), next.CacheID, next.Version, f)
	}
	w.failPut = true // Retaining cached files must not rewrite them.
	calls := 0
	r := &Runner{Writer: w, AI: implementProviderFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{Implementation: &ImplementResult{}}, Usage{}, nil
	})}
	var last Checkpoint
	if err := runImplementationFixture(r, next, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || last.Implementation.Status != "ready" || last.Implementation.ChangeRevision != 7 || len(last.Implementation.Unverified) != 0 {
		t.Fatalf("retention changed state or retried: calls=%d %+v", calls, last.Implementation)
	}
}

func TestUnchangedBaseFileIsStillRejectedDuringRevision(t *testing.T) {
	impl := &domain.Implementation{Revising: true, Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}}}}
	_, err := applyProposals([]string{"a.go"}, impl, map[string]string{"a.go": "base"}, []ChangeProposal{{Path: "a.go", Action: "update", Content: "base"}})
	if err == nil || !strings.Contains(err.Error(), "update_without_change") {
		t.Fatalf("unmodified base treated as a saved draft: %v", err)
	}
}

func TestRevisionRetentionStillBlocksMissingNewFiles(t *testing.T) {
	w, old := preservationFixture(t)
	next := domain.NextImplementation(old, "also create new.go", "incomplete-draft")
	next.Status, next.DraftSource = "editing", nil
	next.Plan = &domain.ImplementationPlan{Summary: "new file", Files: append(append([]domain.PlannedFile{}, old.Plan.Files...), domain.PlannedFile{Path: "src/new.go", Action: "create", Approach: "new behavior"}), AcceptanceCriteria: testCriteria("src/a.go")}
	next.Steps = []domain.ImplementationStep{{Files: []string{"src/a.go", "src/new.go"}}}
	for _, c := range old.Changes {
		f, _ := w.GetChange(context.Background(), old.CacheID, old.Version, c.Path)
		_ = w.PutChange(context.Background(), next.CacheID, next.Version, f)
	}
	r := &Runner{Writer: w, AI: implementProviderFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		return Decision{Implementation: &ImplementResult{}}, Usage{}, nil
	})}
	var last Checkpoint
	if err := runImplementationFixture(r, next, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if last.Implementation.Status != "verification_blocked" || !strings.Contains(strings.Join(last.Implementation.Unverified, " "), "src/new.go") {
		t.Fatalf("missing new work became publishable: %+v", last.Implementation)
	}
}

func TestRevisionRetentionDoesNotRecreateExpiredChanges(t *testing.T) {
	w, old := preservationFixture(t)
	next := domain.NextImplementation(old, "retain current work", "expired-draft")
	next.Status, next.DraftSource = "editing", nil
	next.Steps = []domain.ImplementationStep{{Files: []string{"src/a.go"}}}
	calls := 0
	r := &Runner{Writer: w, AI: implementProviderFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{Implementation: &ImplementResult{}}, Usage{}, nil
	})}
	err := runImplementationFixture(r, next, func(Checkpoint) error { return nil })
	if err == nil || err.Error() != "implementation_changes_expired" || calls != 0 || len(w.versions) != len(old.Changes) {
		t.Fatalf("expired draft fell back to repository: %v calls=%d", err, calls)
	}
}

func TestEditSchemaLimitsProposalsToCurrentStep(t *testing.T) {
	schema := map[string]any{"properties": map[string]any{}}
	task := implementTask{Mode: "edit", Files: []implementFile{{Path: "engine.go", Action: "update"}}, Done: []domain.ChangeSummary{{Path: "summary.go", Action: "create"}}, Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "engine.go", Action: "update"}, {Path: "summary.go", Action: "create"}}}}
	implementInstructions(schema, task)
	props := schema["properties"].(map[string]any)["implementation"].(map[string]any)["anyOf"].([]any)[1].(map[string]any)["properties"].(map[string]any)
	path := props["changes"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)["path"].(map[string]any)
	paths := path["enum"].([]string)
	if len(paths) != 1 || paths[0] != "engine.go" {
		t.Fatalf("previous changes permitted in current step: %+v", path)
	}
}
