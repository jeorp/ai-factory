package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	moved     string   // a newer head someone pushed to the PR branch
	list      []string // overrides the listed paths when set
	listErr   error
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
	if w.listErr != nil {
		return nil, false, w.listErr
	}
	if w.list != nil {
		return w.list, false, nil
	}
	paths := []string{}
	for p := range w.files {
		paths = append(paths, p)
	}
	return paths, false, nil
}
func (w *fakeWriter) PullRequestTarget(ctx context.Context, token, repo string, number int) (github.PullRequestTarget, error) {
	if w.closed {
		return github.PullRequestTarget{}, errors.New("pull_request_closed")
	}
	if w.moved != "" {
		return github.PullRequestTarget{Branch: "ai-factory/x-1", Head: w.moved, Base: "main"}, nil
	}
	if number != 7 {
		return github.PullRequestTarget{}, errors.New("pull_request_not_ai_branch")
	}
	return github.PullRequestTarget{Branch: "ai-factory/fix-7", Head: implBase, Base: "main", URL: "https://github.com/a/b/pull/7", Title: "Seven"}, nil
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

// Existing edit fixtures acknowledge the extra, read-only verification phase.
// Verification-specific tests use providerFunc to exercise its actual contract.
type implementProviderFunc providerFunc

func (f implementProviderFunc) Next(ctx context.Context, key, model, input string) (Decision, Usage, error) {
	var body struct {
		Task implementTask `json:"implementTask"`
	}
	_ = json.Unmarshal([]byte(input), &body)
	if body.Task.Mode == "verify" {
		return Decision{Summary: "確認済み", Verification: passedVerification(body.Task.Plan)}, Usage{}, nil
	}
	d, u, err := providerFunc(f).Next(ctx, key, model, input)
	if d.Plan != nil && len(d.Plan.AcceptanceCriteria) == 0 && len(d.Plan.Files) > 0 {
		d.Plan.AcceptanceCriteria = testCriteria(d.Plan.Files[0].Path)
	}
	return d, u, err
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
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		switch task.Mode {
		case "":
			return Decision{StartImplementation: &ImplementRequest{Request: "Aを2を返すように変更し、old.goを削除してb.goを追加"}}, Usage{InputTokens: 1}, nil
		case "plan":
			if !strings.Contains(input, `"evidence":[{`) {
				return Decision{Read: &github.Read{Kind: "file", Path: "src/a.go", Detail: "summary"}}, Usage{}, nil
			}
			return Decision{Summary: "a.goを確認", Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "Aの戻り値を変更\n詳細", Files: []domain.PlannedFile{
				{Path: "src/a.go", Action: "update", Approach: "1を2に"}, {Path: "src/b.go", Action: "create", Approach: "Bを追加"}, {Path: "src/old.go", Action: "delete", Approach: "不要"},
			}, Tests: []string{"go test"}}}, Usage{}, nil
		case "edit":
			editCalls++
			if len(task.Files) != 3 || (task.Files[0].Original == "" && task.Files[0].NumberedOriginal == "") {
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
				{Path: "src/a.go", Action: "update", SourceHash: task.Files[0].SourceHash, LineEdits: []domain.LineEdit{{StartLine: 3, EndLine: 3, ExpectedStart: "func A() int { return 1 }", ExpectedEnd: "func A() int { return 1 }", Content: "func A() int { return 2 }"}}},
				{Path: "src/b.go", Action: "create", Content: "package src\n\nfunc B() {}\n"},
				{Path: "src/old.go", Action: "delete"},
			}, Unverified: []string{"CIでテストを確認"}}}, Usage{}, nil
		}
		t.Fatalf("unexpected task %+v", task)
		return Decision{}, Usage{}, nil
	})
	reader := readerFunc(func(ctx context.Context, token string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
		if read.Kind == "check_runs" && target.Commit == strings.Repeat("d", 40) {
			return `{"check_runs":[]}`, domain.Source{}, 0, nil
		}
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
		if cp.Implementation != nil && cp.Implementation.Status == "checking_ci" && cp.Answer != "" {
			t.Fatal("CI polling emitted a premature/repeated final answer")
		}
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

func TestFollowUpIncorporatesCIFailureLogs(t *testing.T) {
	writer := newFakeWriter()
	var planEvidenceSeen string
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "plan" {
			planEvidenceSeen = input
			return Decision{Plan: simplePlan()}, Usage{}, nil
		}
		return Decision{}, Usage{}, errors.New("unexpected mode")
	})
	reader := readerFunc(func(ctx context.Context, token string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
		if read.Kind == "check_runs" {
			return `{"check_runs":[{"id":42,"job_id":42,"name":"test-job","conclusion":"failure"}]}`, domain.Source{Label: "checks", URL: "https://github.com/a/b/pull/9/checks"}, 50, nil
		}
		if read.Kind == "job_log" && read.Number == 42 {
			return "FAIL: TestSomethingFailed", domain.Source{Label: "job", URL: "https://github.com/a/b/actions/jobs/42"}, 30, nil
		}
		return "", domain.Source{}, 0, errors.New("unknown read")
	})
	runner := &Runner{AI: ai, GitHub: reader, Writer: writer}
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{
		Status:       "planning",
		Request:      "テスト失敗を修正",
		BaseBranch:   "main",
		BaseCommit:   implBase,
		ExpectedHead: implBase,
		PullNumber:   9,
		Branch:       "ai-factory/test-1",
		CacheID:      "0123456789abcdef",
	}
	err := runner.Run(context.Background(), Input{Session: session, GitHubToken: "gh"}, func(Checkpoint) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(planEvidenceSeen, "TestSomethingFailed") || !strings.Contains(planEvidenceSeen, "check_runs") {
		t.Fatalf("CI failure log not found in plan evidence: %s", planEvidenceSeen)
	}
}

func TestImplementationRejectsUnplannedChanges(t *testing.T) {
	impl := &domain.Implementation{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}, {Path: "b.go", Action: "create"}}}}
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
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/old.go", Action: "delete"}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Steps: []domain.ImplementationStep{{Files: []string{"src/old.go"}}}, Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "x", Files: []domain.PlannedFile{{Path: "src/old.go", Action: "delete", Approach: "x"}}}}
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
						if !strings.Contains(string(raw), `"startImplementation":{"anyOf"`) || props["implementation"].(map[string]any)["type"] != "null" || !strings.HasPrefix(instructions, systemPrompt) {
							t.Fatal("chat cannot start implementation or can emit changes")
						}
					case "plan":
						if !strings.HasPrefix(instructions, implementPlanPrompt) || props["plan"].(map[string]any)["anyOf"] == nil || props["implementation"].(map[string]any)["type"] != "null" || props["startImplementation"].(map[string]any)["type"] != "null" {
							t.Fatal("plan contract")
						}
					case "edit":
						if !strings.HasPrefix(instructions, implementEditPrompt) || props["implementation"].(map[string]any)["anyOf"] == nil || props["plan"].(map[string]any)["type"] != "null" || props["read"].(map[string]any)["type"] != "null" {
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
	return !w.closed && w.moved == "", nil
}

func TestFollowUpOnClosedPRStartsNewBranch(t *testing.T) {
	writer := newFakeWriter()
	writer.closed = true
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		if task := implementTaskOf(t, input); task.FollowUp {
			t.Fatal("closed PR still treated as follow-up")
		}
		return Decision{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}}, Usage{}, nil
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
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
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
	session.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Steps: steps, Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: files}}
	err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(Checkpoint) error { return nil })
	if err == nil || err.Error() != "change_set_size_limit" || len(writer.cache) != 3 {
		t.Fatalf("total size not enforced during editing: %v stored=%d", err, len(writer.cache))
	}
}

func TestPlanWithMissingPathIsCorrectedBeforeApproval(t *testing.T) {
	writer := newFakeWriter()
	corrections := []string{}
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Correction != "" {
			corrections = append(corrections, task.Correction)
			return Decision{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}}, Usage{}, nil
		}
		// A guessed path that does not exist at the base commit, and a create
		// for a file that already exists.
		return Decision{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "frontend/src/App.vue", Action: "update", Approach: "x"}, {Path: "src/old.go", Action: "create", Approach: "x"}}}}, Usage{}, nil
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
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		switch task.Mode {
		case "plan":
			if !strings.Contains(input, `"repositoryFiles":"`) || !strings.Contains(input, "src/types.go") {
				t.Fatal("plan input lacks the repository file list")
			}
			if !strings.Contains(input, `"evidence":[{`) {
				return Decision{Read: &github.Read{Kind: "file", Path: "src/types.go", Detail: "summary"}}, Usage{}, nil
			}
			return Decision{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}, {Path: "lib/b.go", Action: "create", Approach: "y"}}}}, Usage{}, nil
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

func planOnly(t *testing.T, writer *fakeWriter, ai Provider) *domain.Implementation {
	t.Helper()
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "planning", Request: "r", CacheID: "0123456789abcdef"}
	reader := readerFunc(func(ctx context.Context, token string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
		return "package src", domain.Source{Label: read.Path, URL: "https://github.com/a/b/blob/" + implBase + "/" + read.Path}, 11, nil
	})
	var last Checkpoint
	if err := (&Runner{AI: ai, GitHub: reader, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	return last.Implementation
}

func simplePlan() *domain.ImplementationPlan {
	return &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}
}

func TestPlanGoesOnWhenFileListCannotBeRead(t *testing.T) {
	writer := newFakeWriter()
	writer.listErr = errors.New("github_response_limit")
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		if !strings.Contains(input, "ファイル一覧を取得できませんでした") {
			t.Fatal("plan input does not say the list is missing")
		}
		return Decision{Plan: simplePlan()}, Usage{}, nil
	})
	if impl := planOnly(t, writer, ai); impl.Status != "plan_ready" || impl.Plan == nil {
		t.Fatalf("plan not made without the file list: %+v", impl)
	}
}

func TestFileListIsCutAtLineBreaks(t *testing.T) {
	writer := newFakeWriter()
	for i := 0; i < 2000; i++ {
		writer.list = append(writer.list, fmt.Sprintf("pkg/dir%04d/some_long_file_name.go", i))
	}
	known := map[string]bool{}
	for _, p := range writer.list {
		known[p] = true
	}
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		var body struct {
			Files string `json:"repositoryFiles"`
		}
		if err := json.Unmarshal([]byte(input), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Files) > github.MaxListedBytes+200 {
			t.Fatalf("list too long: %d", len(body.Files))
		}
		lines := strings.Split(body.Files, "\n")
		for _, l := range lines[:len(lines)-1] {
			if !known[l] {
				t.Fatalf("partial or unknown path in list: %q", l)
			}
		}
		if !strings.Contains(lines[len(lines)-1], "省略") {
			t.Fatalf("truncation not marked: %q", lines[len(lines)-1])
		}
		return Decision{Plan: simplePlan()}, Usage{}, nil
	})
	planOnly(t, writer, ai)
}

func TestPlanInputLimitRetriesKeepReadBudget(t *testing.T) {
	writer := newFakeWriter()
	reads := 0
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		var body struct {
			Evidence []json.RawMessage `json:"evidence"`
		}
		json.Unmarshal([]byte(input), &body)
		if len(body.Evidence) >= 2 {
			// Only one earlier read fits, as with a few large files.
			return Decision{}, Usage{}, errors.New("ai_input_limit")
		}
		if task := implementTaskOf(t, input); !task.FinalAnswer {
			reads++
			return Decision{Read: &github.Read{Kind: "file", Path: fmt.Sprintf("src/f%d.go", reads), Detail: "summary"}}, Usage{}, nil
		}
		return Decision{Plan: simplePlan()}, Usage{}, nil
	})
	impl := planOnly(t, writer, ai)
	if reads != ImplementPlanReads || impl.Status != "plan_ready" {
		t.Fatalf("reads %d, status %s", reads, impl.Status)
	}
}

func TestEditInputLimitRetriesKeepCallBudget(t *testing.T) {
	writer := newFakeWriter()
	writer.files["src/ctx.go"] = "package src\n\ntype C struct{}\n"
	modelCalls := 0
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		if strings.Contains(input, `"context":[{`) {
			return Decision{}, Usage{}, errors.New("ai_input_limit")
		}
		modelCalls++
		search := "return 9" // does not match: the model needs every call
		if modelCalls == ImplementStepCalls {
			search = "return 1"
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: search, Replace: "return 2"}}}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	plan := simplePlan()
	session.Implementation = &domain.Implementation{Status: "editing", Request: "r", CacheID: "0123456789abcdef", BaseBranch: "main", BaseCommit: implBase, Plan: plan, Steps: domain.PlanSteps(plan.Files), ContextFiles: []string{"src/ctx.go"}}
	var last Checkpoint
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if modelCalls != ImplementStepCalls || len(last.Implementation.Changes) != 1 {
		t.Fatalf("model calls %d, changes %+v", modelCalls, last.Implementation.Changes)
	}
}

func TestFixRequestForExistingPRPushesToItsBranch(t *testing.T) {
	writer := newFakeWriter()
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		switch task.Mode {
		case "plan":
			if !task.FollowUp {
				t.Fatal("fix request not treated as a follow-up")
			}
			return Decision{Plan: &domain.ImplementationPlan{AcceptanceCriteria: []domain.AcceptanceCriterion{{ID: "return", Requirement: "A returns 2", Paths: []string{"src/a.go"}, Checks: []string{"test return value"}, FailureRefs: []string{"42:L1"}}}, Summary: "fix", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}}, Usage{}, nil
		case "edit":
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{}, nil
		}
		return Decision{}, Usage{}, errors.New("unexpected mode")
	})
	reader := readerFunc(func(ctx context.Context, token string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
		if read.Kind == "job_log" {
			return "[failure L1] FAIL A expected 2 got 1", domain.Source{}, 32, nil
		}
		if target.Commit == implBase {
			return `{"check_runs":[{"name":"backend","status":"completed","conclusion":"failure","job_id":42}]}`, domain.Source{}, 64, nil
		}
		return `{"check_runs":[{"name":"backend","status":"completed","conclusion":"success","head_sha":"` + target.Commit + `"}]}`, domain.Source{}, 64, nil
	})
	runner := &Runner{AI: ai, GitHub: reader, Writer: writer}
	session := baseSession()
	session.RunKind = "implement"
	// The session previously published a different PR; the fix targets #7.
	prev := &domain.Implementation{Status: "published", BaseBranch: "main", BaseCommit: implBase, Commit: strings.Repeat("f", 40), Branch: "ai-factory/other-1", PullNumber: 3, Version: 2}
	session.Implementation = StartImplementation(prev, ImplementRequest{Request: "CIの失敗を直して", PullNumber: 7}, "0123456789abcdef")
	var last Checkpoint
	run := func() {
		t.Helper()
		if err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
			t.Fatal(err)
		}
		session.Implementation = last.Implementation
	}
	run()
	impl := last.Implementation
	if impl.Branch != "ai-factory/fix-7" || impl.ExpectedHead != implBase || impl.PullNumber != 7 || impl.Version != 3 || impl.Status != "plan_ready" {
		t.Fatalf("not attached to PR #7: %+v", impl)
	}
	impl.Status = "editing"
	run()
	session.Implementation.Status = "publishing"
	run()
	if len(writer.published) != 1 || writer.published[0].Branch != "ai-factory/fix-7" || writer.published[0].ExpectedHead != implBase {
		t.Fatalf("fix not pushed to the PR branch: %+v", writer.published)
	}
	// Branches AI Factory did not create are refused.
	session.Implementation = StartImplementation(nil, ImplementRequest{Request: "x", PullNumber: 8}, "0123456789abcdef")
	if err := runner.Run(context.Background(), Input{Session: session}, func(Checkpoint) error { return nil }); err == nil || err.Error() != "pull_request_not_ai_branch" {
		t.Fatalf("foreign branch accepted: %v", err)
	}
}

func TestFollowUpOnMovedPRBuildsOnNewHead(t *testing.T) {
	writer := newFakeWriter()
	writer.moved = implBase
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		return Decision{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	prev := &domain.Implementation{Status: "published", BaseBranch: "main", BaseCommit: strings.Repeat("9", 40), Commit: strings.Repeat("f", 40), Branch: "ai-factory/x-1", PullNumber: 4, Request: "r"}
	session.Implementation = domain.NextImplementation(prev, "追加", "0123456789abcdef")
	var last Checkpoint
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	impl := last.Implementation
	if impl.PullNumber != 4 || impl.Branch != "ai-factory/x-1" || impl.ExpectedHead != implBase || impl.BaseCommit != implBase {
		t.Fatalf("open PR with a moved head must not become a new PR: %+v", impl)
	}
	if !strings.Contains(last.Answer, "PR #4 のブランチ ai-factory/x-1") {
		t.Fatalf("plan message should name the target branch: %s", last.Answer)
	}
}

func TestRelatedFilesSuggestsTestsAndReadFiles(t *testing.T) {
	plan := &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "pkg/a.go", Action: "update"}, {Path: "web/src/lib/x.ts", Action: "update"}, {Path: "pkg/new.go", Action: "create"}}}
	all := []string{"pkg/a.go", "pkg/a_test.go", "web/src/lib/x.ts", "web/src/lib/x.test.ts", "pkg/new_test.go", "pkg/types.go"}
	got := relatedFiles(plan, []string{"pkg/types.go", "pkg/a.go", "missing.go"}, all)
	want := []string{"pkg/a_test.go", "web/src/lib/x.test.ts", "pkg/types.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPlanGapReviewAsksOnceAboutRelatedFiles(t *testing.T) {
	writer := newFakeWriter()
	writer.files["src/a_test.go"] = "package src\n"
	corrections := []string{}
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Correction != "" {
			corrections = append(corrections, task.Correction)
			return Decision{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}, {Path: "src/a_test.go", Action: "update", Approach: "test"}}}}, Usage{}, nil
		}
		return Decision{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "planning", Request: "r", CacheID: "0123456789abcdef"}
	var last Checkpoint
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if len(corrections) != 1 || !strings.Contains(corrections[0], "src/a_test.go") {
		t.Fatalf("gap review not requested once: %q", corrections)
	}
	if files := last.Implementation.Plan.Files; len(files) != 2 || files[1].Path != "src/a_test.go" {
		t.Fatalf("reviewed plan not kept: %+v", files)
	}
}

func TestEditCanAskToAddAFileAndContinueAfterApproval(t *testing.T) {
	writer := newFakeWriter()
	writer.files["lib/use.go"] = "package lib\n\nvar X = 1\n"
	proposals := 0
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		switch task.Files[0].Path {
		case "src/a.go":
			proposals++
			return Decision{Implementation: &ImplementResult{
				Changes:         []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return X()"}}}},
				AdditionalFiles: []domain.PendingFile{{Path: "lib/use.go", Action: "update", Approach: "Xを関数にする", Reason: "a.goがX()を呼ぶため"}, {Path: ".github/workflows/ci.yml", Action: "update", Approach: "x", Reason: "y"}},
			}}, Usage{}, nil
		case "lib/use.go":
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "lib/use.go", Action: "update", Edits: []domain.Edit{{Search: "var X = 1", Replace: "func X() int { return 1 }"}}}}}}, Usage{}, nil
		}
		return Decision{}, Usage{}, errors.New("unexpected step")
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}, Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}, Changes: []domain.ChangeSummary{}}
	runner := &Runner{AI: ai, Writer: writer}
	var last Checkpoint
	run := func() {
		t.Helper()
		if err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
			t.Fatal(err)
		}
		session.Implementation = last.Implementation
	}
	run()
	impl := last.Implementation
	if impl.Status != "amend_ready" || len(impl.PendingFiles) != 1 || impl.PendingFiles[0].Path != "lib/use.go" || impl.NextStep != 1 || len(impl.Changes) != 1 {
		t.Fatalf("amendment not requested: %+v", impl)
	}
	if !strings.Contains(strings.Join(impl.Unverified, "\n"), ".github/workflows/ci.yml") || !strings.Contains(last.Answer, "a.goがX()を呼ぶため") {
		t.Fatalf("protected path not reported or reason not shown: %v %s", impl.Unverified, last.Answer)
	}
	session.Implementation.ResolveAmendment(true)
	run()
	impl = last.Implementation
	if impl.Status != "ready" || len(impl.Changes) != 2 || len(impl.Plan.Files) != 2 || proposals != 1 {
		t.Fatalf("approved file not edited: %+v", impl)
	}
}

func TestDeclinedAdditionIsNotProposedAgain(t *testing.T) {
	writer := newFakeWriter()
	writer.files["lib/use.go"] = "package lib\n"
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		// The step first needs another file and changes nothing yet.
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{}, AdditionalFiles: []domain.PendingFile{{Path: "lib/use.go", Action: "update", Approach: "x", Reason: "y"}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}, Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}, Changes: []domain.ChangeSummary{}}
	runner := &Runner{AI: ai, Writer: writer}
	var last Checkpoint
	if err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if last.Implementation.Status != "amend_ready" || last.Implementation.NextStep != 0 {
		t.Fatalf("step should wait for the decision: %+v", last.Implementation)
	}
	session.Implementation = last.Implementation
	session.Implementation.ResolveAmendment(false)
	err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil })
	if err == nil || err.Error() != "implementation_edit_failed" || last.Implementation.Status == "amend_ready" {
		t.Fatalf("declined file asked again or run did not finish: %v %+v", err, last.Implementation)
	}
	if !strings.Contains(strings.Join(last.Implementation.Unverified, "\n"), "追加を見送ったファイル") {
		t.Fatalf("decline not recorded: %v", last.Implementation.Unverified)
	}
}

func TestAmendmentEditsDependenciesBeforeRemainingFiles(t *testing.T) {
	writer := newFakeWriter()
	writer.files["src/b.go"] = "package src\nvar B = 1\n"
	writer.files["lib/use.go"] = "package lib\nvar X = 1\n"
	var order []string
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		var paths []string
		for _, f := range task.Files {
			paths = append(paths, f.Path)
		}
		order = append(order, strings.Join(paths, ","))
		switch strings.Join(paths, ",") {
		case "src/a.go,src/b.go":
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}, AdditionalFiles: []domain.PendingFile{{Path: "lib/use.go", Action: "update", Approach: "change X", Reason: "needed by b"}}}}, Usage{}, nil
		case "lib/use.go":
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "lib/use.go", Action: "update", Edits: []domain.Edit{{Search: "var X = 1", Replace: "var X = 2"}}}}}}, Usage{}, nil
		case "src/b.go":
			if len(task.Done) != 2 {
				t.Fatalf("dependency missing from prior changes: %+v", task.Done)
			}
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/b.go", Action: "update", Edits: []domain.Edit{{Search: "var B = 1", Replace: "var B = 2"}}}}}}, Usage{}, nil
		}
		return Decision{}, Usage{}, errors.New("unexpected step")
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Steps: []domain.ImplementationStep{{Files: []string{"src/a.go", "src/b.go"}}}, Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}, {Path: "src/b.go", Action: "update", Approach: "x"}}}}
	runner := &Runner{AI: ai, Writer: writer}
	var last Checkpoint
	run := func() {
		t.Helper()
		if err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
			t.Fatal(err)
		}
		session.Implementation = last.Implementation
	}
	run()
	if last.Implementation.Status != "amend_ready" || strings.Join(last.Implementation.Steps[0].Files, ",") != "src/b.go" {
		t.Fatalf("unfinished step not retained: %+v", last.Implementation)
	}
	session.Implementation.ResolveAmendment(true)
	run()
	if got := strings.Join(order, " | "); got != "src/a.go,src/b.go | lib/use.go | src/b.go" {
		t.Fatalf("wrong order: %s", got)
	}
	if len(last.Implementation.Changes) != 3 || last.Implementation.Status != "ready" {
		t.Fatalf("incomplete: %+v", last.Implementation)
	}
}

func TestInvalidAdditionWithoutChangesRetriesStep(t *testing.T) {
	writer := newFakeWriter()
	calls := 0
	ai := implementProviderFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		calls++
		if calls == 1 {
			return Decision{Implementation: &ImplementResult{AdditionalFiles: []domain.PendingFile{{Path: ".github/workflows/ci.yml", Action: "update", Approach: "x", Reason: "y"}}}}, Usage{}, nil
		}
		if !strings.Contains(task.Correction, ".github/workflows/ci.yml") {
			t.Fatalf("missing correction: %s", task.Correction)
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}, Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "s", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "x"}}}}
	var last Checkpoint
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || last.Implementation.Status != "ready" || len(last.Implementation.Changes) != 1 {
		t.Fatalf("step was not retried: calls=%d impl=%+v", calls, last.Implementation)
	}
}

// Ordinary workflow fixtures have one observable condition. Tests dedicated to
// coverage use raw providers to exercise missing/unmet/unverified results.
func testCriteria(path string) []domain.AcceptanceCriterion {
	return []domain.AcceptanceCriterion{{ID: "behavior", Requirement: "requested behavior", Paths: []string{path}, Checks: []string{"check the requested behavior"}}}
}
func passedVerification(plan *domain.ImplementationPlan) *domain.ImplementationVerification {
	v := &domain.ImplementationVerification{Issues: []string{}, Unverified: []string{}}
	if plan != nil {
		for _, c := range plan.AcceptanceCriteria {
			v.Criteria = append(v.Criteria, domain.CriterionResult{ID: c.ID, Status: "met", Evidence: "fixture code implements requested behavior", Paths: c.Paths})
		}
	}
	return v
}
