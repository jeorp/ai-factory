package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type documentationWriter struct {
	*fakeWriter
	reads int
	err   error
}

func (w *documentationWriter) RawFile(ctx context.Context, token, repo, commit, path string) (string, bool, error) {
	if path == "README.md" {
		w.reads++
		if w.err != nil {
			return "", false, w.err
		}
	}
	return w.fakeWriter.RawFile(ctx, token, repo, commit, path)
}

func TestProjectDocumentationReachesEveryPhaseWithoutEviction(t *testing.T) {
	w := &documentationWriter{fakeWriter: newFakeWriter()}
	w.files["README.md"] = "Omitted page sizes retain the existing default."
	phases := map[string]int{}
	ai := providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		var body struct {
			Task          implementTask         `json:"implementTask"`
			Documentation *projectDocumentation `json:"projectDocumentation"`
		}
		if err := json.Unmarshal([]byte(input), &body); err != nil {
			t.Fatal(err)
		}
		if body.Documentation == nil || body.Documentation.Content != w.files["README.md"] || body.Documentation.Unavailable || body.Documentation.Truncated {
			t.Fatalf("documented contract missing in %s", body.Task.Mode)
		}
		phases[body.Task.Mode]++
		switch body.Task.Mode {
		case "plan":
			// More optional reads than the evidence window must not evict
			// the documented contract or require the model to discover it.
			if phases["plan"] <= planEvidenceKept+1 {
				return Decision{Read: &github.Read{Kind: "file", Path: "src/a.go", Detail: "summary"}}, Usage{}, nil
			}
			return Decision{Plan: &domain.ImplementationPlan{Summary: "Update A", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "Return the new value"}}, AcceptanceCriteria: testCriteria("src/a.go")}}, Usage{}, nil
		case "edit":
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{}, nil
		case "verify":
			return Decision{Summary: "Documented contract checked", Verification: passedVerification(body.Task.Plan)}, Usage{}, nil
		}
		t.Fatalf("unexpected phase %q", body.Task.Mode)
		return Decision{}, Usage{}, nil
	})
	reader := readerFunc(func(_ context.Context, _ string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
		if target.Commit != implBase {
			t.Fatal("read not pinned to implementation base")
		}
		return w.files[read.Path], domain.Source{}, len(w.files[read.Path]), nil
	})
	runner := &Runner{AI: ai, Writer: w, GitHub: reader}
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = StartImplementation(nil, ImplementRequest{Request: "Update A"}, NewCacheID())
	var last Checkpoint
	for _, status := range []string{"planning", "editing"} {
		session.Implementation.Status = status
		if err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
			t.Fatal(err)
		}
		session.Implementation = last.Implementation
	}
	if last.Implementation.Status != "ready" || w.reads != 2 || phases["edit"] != 1 || phases["verify"] == 0 {
		t.Fatalf("unexpected lifecycle: status=%s reads=%d phases=%v", last.Implementation.Status, w.reads, phases)
	}
	if len(last.Implementation.Plan.Files) != 1 || last.Implementation.Plan.Files[0].Path != "src/a.go" {
		t.Fatal("read-only documentation expanded the write plan")
	}
	url := "https://github.com/a/b/blob/" + implBase + "/README.md"
	if !containsSource(last.Sources, url) {
		t.Fatal("pinned documentation source not recorded")
	}
}

func TestProjectDocumentationMissingUnavailableAndBounded(t *testing.T) {
	for _, name := range []string{"absent", "unavailable", "bounded", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			w := &documentationWriter{fakeWriter: newFakeWriter()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch name {
			case "unavailable":
				w.err = errors.New("private upstream error must not enter model context")
			case "bounded":
				w.files["README.md"] = "model-key work-token github_pat_0123456789abcdef " + strings.Repeat("既存の契約", 3000)
			case "cancelled":
				cancel()
			}
			session := baseSession()
			run := &implementRun{r: &Runner{Writer: w}, in: Input{Session: session, Key: "model-key", GitHubToken: "work-token"}, impl: &domain.Implementation{BaseCommit: implBase}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			err := run.loadProjectDocumentation(ctx)
			if name == "cancelled" {
				if !errors.Is(err, context.Canceled) || w.reads != 0 {
					t.Fatal("cancelled run fetched documentation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := run.loadProjectDocumentation(ctx); err != nil || w.reads != 1 {
				t.Fatal("documentation was refetched on each model call")
			}
			doc := run.projectDocumentation
			switch name {
			case "absent":
				if doc != nil {
					t.Fatal("missing README treated as a required file")
				}
			case "unavailable":
				if doc == nil || !doc.Unavailable || doc.Content != "" {
					t.Fatal("unavailable documentation hidden or raw error exposed")
				}
			case "bounded":
				if doc == nil || !doc.Truncated || len(doc.Content) > projectDocumentationBytes || !utf8.ValidString(doc.Content) {
					t.Fatal("documentation limit or truncation metadata missing")
				}
				for _, secret := range []string{"model-key", "work-token", "github_pat_0123456789abcdef"} {
					if strings.Contains(doc.Content, secret) {
						t.Fatal("credential exposed in documentation")
					}
				}
			}
		})
	}
}

func TestDocumentationUsesSavedReadmeOverlay(t *testing.T) {
	w := newRevisionWriter()
	impl := &domain.Implementation{CacheID: "docs", Version: 1, Changes: []domain.ChangeSummary{{Path: "README.md", Action: "update"}}}
	if err := w.PutChange(context.Background(), impl.CacheID, impl.Version, domain.FileChange{Path: "README.md", Action: "update", Content: "new contract"}); err != nil {
		t.Fatal(err)
	}
	run := &implementRun{r: &Runner{Writer: w}, impl: impl, projectDocumentation: &projectDocumentation{Path: "README.md", Content: "old contract"}}
	doc, err := run.currentProjectDocumentation(context.Background())
	if err != nil || !doc.Modified || !strings.Contains(doc.Content, "new contract") || strings.Contains(doc.Content, "old contract") {
		t.Fatalf("wrong documentation: %+v %v", doc, err)
	}
}

func TestDocumentationIsShedWhenInputExceedsLimit(t *testing.T) {
	w := newFakeWriter()
	w.files["README.md"] = strings.Repeat("documentation ", 600)
	called := false
	run := &implementRun{r: &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		called = true
		var body struct {
			Documentation projectDocumentation `json:"projectDocumentation"`
		}
		if err := json.Unmarshal([]byte(input), &body); err != nil {
			t.Fatal(err)
		}
		if !body.Documentation.Unavailable || body.Documentation.Content != "" {
			t.Fatalf("documentation was not shed: %+v", body.Documentation)
		}
		return Decision{}, Usage{}, nil
	})}, in: Input{Session: baseSession()}, impl: &domain.Implementation{BaseCommit: implBase}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Search for the largest request that fits with omission metadata. A
	// space-separated input avoids pathological tokenization of a single word.
	low, high := 0, ReviewInputBytes/2
	for low < high {
		mid := (low + high + 1) / 2
		candidate := strings.Repeat("x ", mid)
		bare, _ := json.Marshal(map[string]any{"filler": candidate, "instructions": "", "target": run.target(), "repositoryOverview": run.in.Session.RepositoryOverview, "projectDocumentation": &projectDocumentation{Path: "README.md", Unavailable: true}})
		if implementInputFits(bare) {
			low = mid
		} else {
			high = mid - 1
		}
	}
	filler := strings.Repeat("x ", low)
	full, _ := json.Marshal(map[string]any{"filler": filler, "instructions": "", "target": run.target(), "repositoryOverview": run.in.Session.RepositoryOverview, "projectDocumentation": &projectDocumentation{Path: "README.md", Content: w.files["README.md"]}})
	if implementInputFits(full) {
		t.Fatal("could not construct boundary input")
	}

	if _, err := run.call(context.Background(), map[string]any{"filler": filler}); err != nil || !called {
		t.Fatalf("optional documentation prevented call: %v", err)
	}
}
