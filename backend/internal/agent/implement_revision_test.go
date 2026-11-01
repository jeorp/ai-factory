package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

// Unlike the old fixture, this cache distinguishes versions, just as production does.
type revisionWriter struct {
	*fakeWriter
	versions map[string]domain.FileChange
}

func newRevisionWriter() *revisionWriter {
	return &revisionWriter{newFakeWriter(), map[string]domain.FileChange{}}
}
func (w *revisionWriter) PutChange(_ context.Context, id string, version int, f domain.FileChange) error {
	if w.failPut {
		return fmt.Errorf("change_store_failed")
	}
	w.versions[fmt.Sprintf("%s|%d|%s", id, version, f.Path)] = f
	return nil
}
func (w *revisionWriter) GetChange(_ context.Context, id string, version int, path string) (domain.FileChange, bool) {
	f, ok := w.versions[fmt.Sprintf("%s|%d|%s", id, version, path)]
	return f, ok
}

func runImplementationFixture(r *Runner, impl *domain.Implementation, emit func(Checkpoint) error) error {
	s := baseSession()
	s.RunKind, s.Implementation = "implement", impl
	return r.Run(context.Background(), Input{Session: s}, emit)
}

func TestRevisionKeepsDraftAndPublishesOriginalActions(t *testing.T) {
	w := newRevisionWriter()
	w.files["src/c.go"] = "base c"
	w.list = []string{"src/a.go", "src/old.go", "src/c.go"}
	old := &domain.Implementation{Request: strings.Repeat("依頼", 680), Status: "ready", BaseBranch: "main", BaseCommit: implBase, CacheID: "old-cache", Version: 2, Plan: &domain.ImplementationPlan{Summary: "original"}}
	for _, f := range []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "draft a"}, {Path: "src/b.go", Action: "create", Content: "draft b"}, {Path: "src/old.go", Action: "delete"}, {Path: "src/c.go", Action: "update", Content: "draft c"}} {
		old.Plan.Files = append(old.Plan.Files, domain.PlannedFile{Path: f.Path, Action: f.Action, Approach: "original"})
		old.Changes = append(old.Changes, domain.ChangeSummary{Path: f.Path, Action: f.Action, Bytes: len(f.Content)})
		_ = w.PutChange(context.Background(), old.CacheID, old.Version, f)
	}
	next := domain.NextImplementation(old, "最新の修正依頼", "new-cache")
	var last Checkpoint
	r := &Runner{Writer: w, AI: implementProviderFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if !strings.HasSuffix(task.Request, "最新の修正依頼") {
			t.Fatal("latest request lost")
		}
		if task.Mode == "plan" {
			if !strings.Contains(input, "draftChanges") {
				t.Fatal("draft missing from plan")
			}
			return Decision{Plan: &domain.ImplementationPlan{Summary: "revision", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "revise"}, {Path: "src/b.go", Action: "create", Approach: "revise"}}}}, Usage{}, nil
		}
		if task.Mode != "edit" || len(task.Files) != 2 {
			t.Fatalf("unexpected task: %+v", task)
		}
		for _, file := range task.Files {
			want := map[string]string{"src/a.go": "draft a", "src/b.go": "draft b"}[file.Path]
			if file.Original != want {
				t.Fatalf("draft was replaced by base: %+v", file)
			}
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "draft a", Replace: "draft a revised"}}}, {Path: "src/b.go", Action: "create", Content: "draft b revised"}}}}, Usage{}, nil
	})}
	save := func(cp Checkpoint) error { last = cp; return nil }
	if err := runImplementationFixture(r, next, save); err != nil {
		t.Fatal(err)
	}
	next = last.Implementation
	if !next.Revising || next.DraftSource != nil || len(next.Plan.Files) != 4 || len(next.Steps) != 1 || len(next.Changes) != 4 || next.Version != 3 {
		t.Fatalf("bad revision plan: %+v", next)
	}
	// Replanning a create as an update is rejected; the base remains unchanged.
	run := &implementRun{impl: next}
	if _, _, err := run.mergeDraftPlan(&domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "src/b.go", Action: "update"}}}); err == nil {
		t.Fatal("action changed relative to original base")
	}
	next.Status = "editing"
	if err := runImplementationFixture(r, next, save); err != nil {
		t.Fatal(err)
	}
	next = last.Implementation
	if next.Status != "ready" {
		t.Fatalf("not ready: %+v", next)
	}
	for _, path := range []string{"src/a.go", "src/b.go", "src/c.go", "src/old.go"} {
		original, _ := w.GetChange(context.Background(), old.CacheID, old.Version, path)
		current, ok := w.GetChange(context.Background(), next.CacheID, next.Version, path)
		if !ok || current.Action != original.Action {
			t.Fatalf("lost change %s", path)
		}
		if path == "src/a.go" || path == "src/b.go" {
			if current.Content != original.Content+" revised" {
				t.Fatalf("revision missing: %+v", current)
			}
		} else if current != original {
			t.Fatalf("untouched draft changed: %+v", current)
		}
	}
	next.Status = "publishing"
	if err := runImplementationFixture(r, next, save); err != nil {
		t.Fatal(err)
	}
	if len(w.published) != 1 || len(w.published[0].Files) != 4 || len(w.published[0].Approved) != 4 || w.published[0].BaseCommit != implBase {
		t.Fatalf("wrong published draft: %+v", w.published)
	}
}

func TestRevisionCopyExpiryFailsBeforeAnyWritesAndKeepsProvenance(t *testing.T) {
	w := newRevisionWriter()
	old := &domain.Implementation{Status: "ready", BaseCommit: implBase, CacheID: "source", Version: 5, Plan: &domain.ImplementationPlan{Summary: "draft", Files: []domain.PlannedFile{{Path: "a.go", Action: "create", Approach: "x"}, {Path: "b.go", Action: "create", Approach: "x"}}}, Changes: []domain.ChangeSummary{{Path: "a.go", Action: "create"}, {Path: "b.go", Action: "create"}}}
	_ = w.PutChange(context.Background(), old.CacheID, old.Version, domain.FileChange{Path: "a.go", Action: "create", Content: "saved"})
	next := domain.NextImplementation(old, "first revision", "pending")
	next = domain.NextImplementation(next, "second revision before copying", "new")
	if next.DraftSource.CacheID != "source" || next.DraftSource.Version != 5 {
		t.Fatalf("lost source: %+v", next.DraftSource)
	}
	raw, _ := json.Marshal(next)
	if strings.Contains(string(raw), "source") || strings.Contains(string(raw), "CacheID") {
		t.Fatalf("cache provenance leaked: %s", raw)
	}
	err := runImplementationFixture(&Runner{Writer: w}, next, func(Checkpoint) error { t.Fatal("checkpoint before all source files validated"); return nil })
	if err == nil || err.Error() != "implementation_changes_expired" || len(w.versions) != 1 {
		t.Fatalf("partial copy or stale fallback: %v %+v", err, w.versions)
	}
}

func TestChatContextIncludesDraftFindingsWithoutCacheIdentifiers(t *testing.T) {
	s := baseSession()
	s.Implementation = &domain.Implementation{Status: "ready", Request: "実装依頼", CacheID: "secret-cache-id", Verification: &domain.ImplementationVerification{Issues: []string{"src/a.goの戻り値と呼び出し元が不一致"}}}
	input := BuildContext(s, "", []domain.Message{{Text: strings.Repeat("旧", 1365)}, {Text: "最新の追加指示"}}, nil, nil)
	if !strings.Contains(input, "implementationDraft") || !strings.Contains(input, "戻り値と呼び出し元") || !strings.Contains(input, "最新の追加指示") || strings.Contains(input, "secret-cache-id") || len(input) > domain.MaxInputBytes {
		t.Fatalf("bad context: %s", input)
	}
}
