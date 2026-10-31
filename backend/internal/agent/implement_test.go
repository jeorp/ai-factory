package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

var implBase = strings.Repeat("c", 40)

type fakeWriter struct {
	files     map[string]string
	cache     map[string]domain.FileChange
	published []github.PublishRequest
	failPut   bool
	closed    bool
}

func newFakeWriter() *fakeWriter {
	return &fakeWriter{files: map[string]string{"src/a.go": "package src\n\nfunc A() int { return 1 }\n", "src/old.go": "package src\n"}, cache: map[string]domain.FileChange{}}
}
func (w *fakeWriter) Authorize(ctx context.Context, token, repo string) (github.Repository, error) {
	return github.Repository{FullName: repo, DefaultBranch: "main"}, nil
}
func (w *fakeWriter) Commit(ctx context.Context, token string, r github.Repository) (string, error) {
	return implBase, nil
}
func (w *fakeWriter) RawFile(ctx context.Context, token, repo, commit, p string) (string, bool, error) {
	if commit != implBase {
		return "", false, errors.New("unexpected_commit")
	}
	v, ok := w.files[p]
	return v, ok, nil
}
func (w *fakeWriter) FileList(ctx context.Context, token, repo, commit string) ([]string, bool, error) {
	paths := []string{}
	for p := range w.files {
		paths = append(paths, p)
	}
	return paths, false, nil
}
func (w *fakeWriter) PutChange(ctx context.Context, id string, version int, f domain.FileChange) error {
	if w.failPut {
		return errors.New("change_store_failed")
	}
	w.cache[id+"|"+f.Path] = f
	return nil
}
func (w *fakeWriter) GetChange(ctx context.Context, id string, version int, p string) (domain.FileChange, bool) {
	f, ok := w.cache[id+"|"+p]
	return f, ok
}
func (w *fakeWriter) Publish(ctx context.Context, token string, r github.PublishRequest, s github.PublishState, save func(github.PublishState) error) (github.PublishState, error) {
	w.published = append(w.published, r)
	s = github.PublishState{Commit: strings.Repeat("d", 40), RefUpdated: true, PullNumber: 9, PullURL: "https://github.com/a/b/pull/9"}
	return s, save(s)
}

func implementTaskOf(t *testing.T, input string) implementTask {
	t.Helper()
	var body struct {
		Task *implementTask `json:"implementTask"`
	}
	if json.Unmarshal([]byte(input), &body) != nil || body.Task == nil {
		return implementTask{}
	}
	return *body.Task
}

func TestImplementationFlowFromChatToDraftPR(t *testing.T) {
	writer := newFakeWriter()
	editCalls := 0
	reads := []github.Read{}
	ai := providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		switch task.Mode {
		case "":
			return Decision{StartImplementation: &ImplementRequest{Request: "Aを2を返すように変更し、old.goを削除してb.goを追加"}}, Usage{InputTokens: 1}, nil
		case "plan":
			if !strings.Contains(input, `"evidence":[{`) {
				return Decision{Read: &github.Read{Kind: "file", Path: "src/a.go", Detail: "summary"}}, Usage{}, nil
			}
			return Decision{Summary: "a.goを確認", Plan: &domain.ImplementationPlan{Summary: "Aの戻り値を変更\n詳細", Files: []domain.PlannedFile{
				{Path: "src/a.go", Action: "update", Approach: "1を2に"}, {Path: "src/b.go", Action: "create", Approach: "Bを追加"}, {Path: "src/old.go", Action: "delete", Approach: "不要"},
			}, Tests: []string{"go test"}}}, Usage{}, nil
		case "edit":
			editCalls++
			if len(task.Files) != 3 || task.Files[0].Original == "" {
				t.Fatalf("step files: %+v", task.Files)
			}
			if editCalls == 1 {
				// Ambiguous/missing search text must be corrected, not applied.
				return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 3", Replace: "return 2"}}}}}}, Usage{}, nil
			}
			if task.Correction == "" {
				t.Fatal("correction not sent after failed edit")
			}
			return Decision{Summary: "変更済み", Implementation: &ImplementResult{Changes: []ChangeProposal{
				{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}},
				{Path: "src/b.go", Action: "create", Content: "package src\n\nfunc B() {}\n"},
				{Path: "src/old.go", Action: "delete"},
			}, Unverified: []string{"CIでテストを確認"}}}, Usage{}, nil
		}
		t.Fatalf("unexpected task %+v", task)
		return Decision{}, Usage{}, nil
	})
	reader := readerFunc(func(ctx context.Context, token string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
		if target.Commit != implBase {
			t.Fatalf("read not pinned to base: %+v", target)
		}
		reads = append(reads, read)
		return "package src", domain.Source{Label: read.Path, URL: "https://github.com/a/b/blob/" + implBase + "/" + read.Path}, 12, nil
	})
	runner := &Runner{AI: ai, GitHub: reader, Writer: writer}
	session := baseSession()
	session.ID = "Session_ABC123"
	var last Checkpoint
	save := func(cp Checkpoint) error {
		last = cp
		if cp.Implementation != nil {
			session.Implementation = cp.Implementation
		}
		if cp.RunKind != "" {
			session.RunKind = cp.RunKind
		}
		return nil
	}
	if err := runner.Run(context.Background(), Input{Session: session, GitHubToken: "gh"}, save); err != nil {
		t.Fatal(err)
	}
	impl := session.Implementation
	if session.RunKind != "implement" || impl.Status != "plan_ready" || impl.BaseCommit != implBase || impl.BaseBranch != "main" || len(impl.Steps) != 1 || len(reads) != 1 || !strings.Contains(last.Answer, "src/a.go") {
		t.Fatalf("plan not ready: %+v %+v", impl, last)
	}
	if len(writer.cache) != 0 {
		t.Fatal("changes created before approval")
	}

	impl.Status = "editing"
	if err := runner.Run(context.Background(), Input{Session: session, GitHubToken: "gh"}, save); err != nil {
		t.Fatal(err)
	}
	impl = session.Implementation
	if impl.Status != "ready" || len(impl.Changes) != 3 || editCalls != 2 || !strings.HasPrefix(impl.Branch, "ai-factory/") || impl.Title != "Aの戻り値を変更" {
		t.Fatalf("changes not ready: %+v", impl)
	}
	if got := writer.cache[impl.CacheID+"|src/a.go"].Content; !strings.Contains(got, "return 2") || strings.Contains(got, "return 1") {
		t.Fatalf("edit not applied: %q", got)
	}
	if !strings.Contains(impl.Body, "AIファクトリー") || !strings.Contains(impl.Body, "CIでテストを確認") {
		t.Fatalf("body: %s", impl.Body)
	}

	impl.Status = "publishing"
	if err := runner.Run(context.Background(), Input{Session: session, GitHubToken: "gh"}, save); err != nil {
		t.Fatal(err)
	}
	impl = session.Implementation
	if impl.Status != "published" || impl.PullNumber != 9 || len(writer.published) != 1 || !strings.Contains(last.Answer, "pull/9") {
		t.Fatalf("not published: %+v", impl)
	}
	req := writer.published[0]
	if req.BaseCommit != implBase || req.BaseBranch != "main" || len(req.Files) != 3 || len(req.Approved) != 3 || req.ExpectedHead != "" {
		t.Fatalf("publish request: %+v", req)
	}

	// A follow-up continues on the same branch from the published commit.
	next := domain.NextImplementation(impl, "追加で修正", "cache-2-xxxxxxxxxxxx")
	if next.ExpectedHead != impl.Commit || next.BaseCommit != impl.Commit || next.Branch != impl.Branch || next.PullNumber != 9 || next.Version != impl.Version+1 {
		t.Fatalf("follow-up: %+v", next)
	}
}

func TestImplementationRejectsUnplannedChanges(t *testing.T) {
	impl := &domain.Implementation{Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}, {Path: "b.go", Action: "create"}}}}
	originals := map[string]string{"a.go": "x := 1\n"}
	cases := [][]ChangeProposal{
		{{Path: "c.go", Action: "create", Content: "x"}},
		{{Path: "a.go", Action: "delete"}},
		{{Path: "b.go", Action: "create"}},
		{{Path: "a.go", Action: "update"}},
		{{Path: "a.go", Action: "update", Edits: []domain.Edit{{Search: "x := 1", Replace: "x := 1"}}}},
		{{Path: "a.go", Action: "update", Edits: []domain.Edit{{Search: "y", Replace: "z"}}}},
		{{Path: "a.go", Action: "update", Content: "y"}, {Path: "a.go", Action: "update", Content: "z"}},
	}
	for i, c := range cases {
		if _, err := applyProposals([]string{"a.go", "b.go"}, impl, originals, c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestImplementationStopsWhenChangeCannotBeStored(t *testing.T) {
	writer := newFakeWriter()
	writer.failPut = true
	ai := providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/old.go", Action: "delete"}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Steps: []domain.ImplementationStep{{Files: []string{"src/old.go"}}}, Plan: &domain.ImplementationPlan{Summary: "x", Files: []domain.PlannedFile{{Path: "src/old.go", Action: "delete", Approach: "x"}}}}
	var last Checkpoint
	err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil })
	if err == nil || (last.Implementation != nil && last.Implementation.NextStep != 0) {
		t.Fatalf("step advanced without stored changes: %v", err)
	}
}

func TestProvidersExposeImplementationContract(t *testing.T) {
	for _, name := range []string{"openai", "gemini"} {
		for _, mode := range []string{"chat", "plan", "edit"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				input := "input"
				decision := Decision{Intent: IntentPlan{Goal: "g", Investigation: "i", Response: "r"}}
				switch mode {
				case "chat":
					decision.StartImplementation = &ImplementRequest{Request: "ログイン画面を追加"}
				case "plan":
					input = `{"implementTask":{"mode":"plan","readBudget":2}}`
					decision.Plan = &domain.ImplementationPlan{Summary: "s", Files: []domain.PlannedFile{{Path: "a.go", Action: "update", Approach: "x"}}}
				case "edit":
					input = `{"implementTask":{"mode":"edit","readBudget":0}}`
					decision.Implementation = &ImplementResult{Changes: []ChangeProposal{{Path: "a.go", Action: "update", Edits: []domain.Edit{{Search: "a", Replace: "b"}}}}}
				}
				decisionJSON, _ := json.Marshal(decision)
				client := fakeClient(func(req *http.Request) string {
					var body map[string]any
					json.NewDecoder(req.Body).Decode(&body)
					var schema map[string]any
					var instructions string
					if name == "openai" {
						schema = body["text"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
						instructions = body["instructions"].(string)
					} else {
						schema = body["generationConfig"].(map[string]any)["responseJsonSchema"].(map[string]any)
						instructions = body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
					}
					props := schema["properties"].(map[string]any)
					required := map[string]bool{}
					for _, r := range schema["required"].([]any) {
						required[r.(string)] = true
					}
					for p := range props {
						if !required[p] {
							t.Fatalf("%s not required in strict schema", p)
						}
					}
					raw, _ := json.Marshal(props)
					switch mode {
					case "chat":
						if !strings.Contains(string(raw), `"startImplementation":{"anyOf"`) || props["implementation"].(map[string]any)["type"] != "null" || instructions != systemPrompt {
							t.Fatal("chat cannot start implementation or can emit changes")
						}
					case "plan":
						if instructions != implementPlanPrompt || props["plan"].(map[string]any)["anyOf"] == nil || props["implementation"].(map[string]any)["type"] != "null" || props["startImplementation"].(map[string]any)["type"] != "null" {
							t.Fatal("plan contract")
						}
					case "edit":
						if instructions != implementEditPrompt || props["implementation"].(map[string]any)["anyOf"] == nil || props["plan"].(map[string]any)["type"] != "null" || props["read"].(map[string]any)["type"] != "null" {
							t.Fatal("edit contract")
						}
					}
					var response any
					if name == "openai" {
						response = map[string]any{"status": "completed", "output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": string(decisionJSON)}}}}}
					} else {
						response = map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(decisionJSON)}}}}}}
					}
					data, _ := json.Marshal(response)
					return string(data)
				})
				var provider Provider = &OpenAI{HTTP: client, Endpoint: "https://example.test"}
				if name == "gemini" {
					provider = &Gemini{HTTP: client, Endpoint: "https://example.test"}
				}
				d, _, err := provider.Next(context.Background(), "test-key", "test-model", input)
				if err != nil || (mode == "chat" && d.StartImplementation == nil) || (mode == "plan" && d.Plan == nil) || (mode == "edit" && d.Implementation == nil) {
					t.Fatalf("not decoded: %+v %v", d, err)
				}
			})
		}
	}
}

func (w *fakeWriter) PullRequestOpen(ctx context.Context, token, repo string, number int, branch, head string) (bool, error) {
	return !w.closed, nil
}

func TestFollowUpOnClosedPRStartsNewBranch(t *testing.T) {
	writer := newFakeWriter()
	writer.closed = true
	ai := providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		if task := implementTaskOf(t, input); task.FollowUp {
			t.Fatal("closed PR still treated as follow-up")
		}
		return Decision{Plan: &domain.ImplementationPlan{Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	prev := &domain.Implementation{Status: "published", BaseBranch: "main", BaseCommit: implBase, Commit: strings.Repeat("f", 40), Branch: "ai-factory/x-1", PullNumber: 4, PullURL: "https://github.com/a/b/pull/4", Request: "r"}
	session.Implementation = domain.NextImplementation(prev, "追加", "0123456789abcdef")
	var last Checkpoint
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	impl := last.Implementation
	if impl.ExpectedHead != "" || impl.Branch != "" || impl.PullNumber != 0 || impl.BaseCommit != implBase || len(impl.Unverified) != 1 {
		t.Fatalf("follow-up not detached: %+v", impl)
	}
}

func TestImplementationStopsWhenTotalSizeExceeded(t *testing.T) {
	writer := newFakeWriter()
	big := strings.Repeat("x", 60*1024)
	ai := providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		changes := []ChangeProposal{}
		for _, f := range task.Files {
			changes = append(changes, ChangeProposal{Path: f.Path, Action: "create", Content: big})
		}
		return Decision{Implementation: &ImplementResult{Changes: changes}}, Usage{}, nil
	})
	files := []domain.PlannedFile{}
	steps := []domain.ImplementationStep{}
	for _, dir := range []string{"a", "b", "c", "d"} {
		files = append(files, domain.PlannedFile{Path: dir + "/n.go", Action: "create", Approach: "x"})
		steps = append(steps, domain.ImplementationStep{Files: []string{dir + "/n.go"}})
	}
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Steps: steps, Plan: &domain.ImplementationPlan{Summary: "s", Files: files}}
	err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(Checkpoint) error { return nil })
	if err == nil || err.Error() != "change_set_size_limit" || len(writer.cache) != 3 {
		t.Fatalf("total size not enforced during editing: %v stored=%d", err, len(writer.cache))
	}
}

func TestPlanWithMissingPathIsCorrectedBeforeApproval(t *testing.T) {
	writer := newFakeWriter()
	corrections := []string{}
	ai := providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Correction != "" {
			corrections = append(corrections, task.Correction)
			return Decision{Plan: &domain.ImplementationPlan{Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}}, Usage{}, nil
		}
		// A guessed path that does not exist at the base commit, and a create
		// for a file that already exists.
		return Decision{Plan: &domain.ImplementationPlan{Summary: "s", Files: []domain.PlannedFile{{Path: "frontend/src/App.vue", Action: "update", Approach: "x"}, {Path: "src/old.go", Action: "create", Approach: "x"}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "planning", Request: "r", CacheID: "0123456789abcdef"}
	var last Checkpoint
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(corrections) != 1 || !strings.Contains(corrections[0], "frontend/src/App.vue は存在しません") || !strings.Contains(corrections[0], "src/old.go は既に存在します") {
		t.Fatalf("missing path not reported to the model: %q", corrections)
	}
	impl := last.Implementation
	if impl.Status != "plan_ready" || impl.Plan == nil || len(impl.Plan.Files) != 1 || impl.Plan.Files[0].Path != "src/a.go" {
		t.Fatalf("corrected plan not kept: %+v", impl)
	}
}

func TestImplementationPassesFileListContextAndEarlierDiffs(t *testing.T) {
	writer := newFakeWriter()
	writer.files["src/types.go"] = "package src\n\ntype T struct{}\n"
	var stepInputs []string
	ai := providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		switch task.Mode {
		case "plan":
			if !strings.Contains(input, `"repositoryFiles":"`) || !strings.Contains(input, "src/types.go") {
				t.Fatal("plan input lacks the repository file list")
			}
			if !strings.Contains(input, `"evidence":[{`) {
				return Decision{Read: &github.Read{Kind: "file", Path: "src/types.go", Detail: "summary"}}, Usage{}, nil
			}
			return Decision{Plan: &domain.ImplementationPlan{Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}, {Path: "lib/b.go", Action: "create", Approach: "y"}}}}, Usage{}, nil
		case "edit":
			stepInputs = append(stepInputs, input)
			if task.Files[0].Path == "src/a.go" {
				return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return Two()"}}}}}}, Usage{}, nil
			}
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "lib/b.go", Action: "create", Content: "package src\n\nfunc Two() int { return 2 }\n"}}}}, Usage{}, nil
		}
		return Decision{}, Usage{}, errors.New("unexpected mode")
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "planning", Request: "r", CacheID: "0123456789abcdef"}
	reader := readerFunc(func(ctx context.Context, token string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
		return "type T", domain.Source{Label: read.Path, URL: "https://github.com/a/b/blob/" + implBase + "/" + read.Path}, 6, nil
	})
	runner := &Runner{AI: ai, GitHub: reader, Writer: writer}
	var last Checkpoint
	if err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	impl := last.Implementation
	if len(impl.ContextFiles) != 1 || impl.ContextFiles[0] != "src/types.go" {
		t.Fatalf("plan reads not recorded: %v", impl.ContextFiles)
	}
	// Approve and edit in two steps, as a larger plan would be split.
	impl.Status = "editing"
	impl.Steps = []domain.ImplementationStep{{Files: []string{"src/a.go"}}, {Files: []string{"lib/b.go"}}}
	session.Implementation = impl
	if err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(stepInputs) != 2 {
		t.Fatalf("expected two edit steps, got %d", len(stepInputs))
	}
	if !strings.Contains(stepInputs[0], `"path":"src/types.go","content":"package src`) {
		t.Fatal("first step lacks planning context")
	}
	if !strings.Contains(stepInputs[1], `"previousChanges":[{"path":"src/a.go","action":"update","diff":"`) || !strings.Contains(stepInputs[1], `+func A() int { return Two() }`) {
		t.Fatalf("second step lacks the earlier diff: %s", stepInputs[1])
	}
}
