package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type searchWriter struct {
	*fakeWriter
	search func(context.Context, string, string, string, github.CodeSearch) (github.CodeSearchResult, error)
}

func (w searchWriter) SearchFiles(ctx context.Context, token, repo, commit string, query github.CodeSearch) (github.CodeSearchResult, error) {
	return w.search(ctx, token, repo, commit, query)
}

func TestPlanningSearchFindsConsumersAndEditsDefinitionsFirst(t *testing.T) {
	base := newFakeWriter()
	base.files["z-types/model.go"] = "package types\ntype Thing struct { Old int }\n"
	base.files["a-use/use.go"] = "package use\nvar Field = Thing.Old\n"
	searches := 0
	w := searchWriter{fakeWriter: base, search: func(_ context.Context, _, repo, commit string, q github.CodeSearch) (github.CodeSearchResult, error) {
		searches++
		if commit != implBase || q.Query != "Thing" {
			t.Fatalf("wrong search target: %s %+v", commit, q)
		}
		return github.CodeSearchResult{Matches: []github.SearchMatch{{Path: "z-types/model.go", Line: 2, Text: "type Thing struct"}, {Path: "a-use/use.go", Line: 2, Text: "var Field = Thing.Old"}}, ScannedFiles: 2, ScannedBytes: 50}, nil
	}}
	var editOrder []string
	ai := implementProviderFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "plan" {
			if !strings.Contains(input, `"kind":"search"`) {
				return Decision{Search: &github.CodeSearch{Query: "Thing"}}, Usage{}, nil
			}
			if task.ReadBudget != ImplementPlanReads-1 || !strings.Contains(input, "a-use/use.go") {
				t.Fatal("search results/budget missing")
			}
			return Decision{Plan: &domain.ImplementationPlan{Summary: "Rename field", Files: []domain.PlannedFile{{Path: "a-use/use.go", Action: "update", Approach: "利用箇所", DependsOn: []string{"z-types/model.go"}}, {Path: "z-types/model.go", Action: "update", Approach: "定義"}}}}, Usage{}, nil
		}
		if len(task.Files) != 1 {
			t.Fatalf("dependencies were grouped with consumers: %+v", task.Files)
		}
		f := task.Files[0]
		editOrder = append(editOrder, f.Path)
		if f.Path == "a-use/use.go" && !strings.Contains(input, `Thing struct { New int }`) {
			t.Fatal("consumer lacks generated definition")
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: f.Path, Action: "update", Edits: []domain.Edit{{Search: "Old", Replace: "New"}}}}}}, Usage{}, nil
	})
	session := baseSession()
	session.RunKind = "implement"
	session.Implementation = &domain.Implementation{Status: "planning", Request: "rename Thing.Old", CacheID: "0123456789abcdef"}
	var last Checkpoint
	runner := &Runner{AI: ai, Writer: w}
	run := func() {
		t.Helper()
		if err := runner.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
			t.Fatal(err)
		}
		session.Implementation = last.Implementation
	}
	run()
	if searches != 1 || len(last.Implementation.Steps) != 2 || len(last.Implementation.ContextFiles) != 2 {
		t.Fatalf("plan did not use search/dependencies: %+v", last.Implementation)
	}
	session.Implementation.Status = "editing"
	run()
	if strings.Join(editOrder, ",") != "z-types/model.go,a-use/use.go" || last.Implementation.Status != "ready" {
		t.Fatalf("wrong order: %v", editOrder)
	}
}

func TestPendingDependenciesOnRejectedFilesAreRejected(t *testing.T) {
	w := newFakeWriter()
	run := &implementRun{r: &Runner{Writer: w}, impl: &domain.Implementation{BaseCommit: implBase, Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}}}}
	pending, rejected, err := run.checkAdditions(context.Background(), []domain.PendingFile{{Path: "new.go", Action: "create", Approach: "x", Reason: "needed", DependsOn: []string{".github/workflows/ci.yml"}}, {Path: ".github/workflows/ci.yml", Action: "create", Approach: "x", Reason: "needed"}})
	if err != nil || len(pending) != 0 || len(rejected) != 2 {
		t.Fatalf("invalid dependent survived: %+v %v %v", pending, rejected, err)
	}
}

func TestProvidersExposeSearchOnlyDuringPlanning(t *testing.T) {
	for _, name := range []string{"openai", "gemini"} {
		for _, mode := range []string{"plan", "final", "edit", "verify", "chat", "malformed"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				input := `{"implementTask":{"mode":"` + mode + `","readBudget":2}}`
				if mode == "final" {
					input = `{"implementTask":{"mode":"plan","readBudget":0,"finalAnswerOnly":true}}`
				}
				if mode == "malformed" {
					input = `{"implementTask":{"mode":"plan","readBudget":2,"step":"invalid"}}`
				}
				client := fakeClient(func(req *http.Request) string {
					var body map[string]any
					_ = json.NewDecoder(req.Body).Decode(&body)
					var schema map[string]any
					if name == "openai" {
						schema = body["text"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
					} else {
						schema = body["generationConfig"].(map[string]any)["responseJsonSchema"].(map[string]any)
					}
					search := schema["properties"].(map[string]any)["search"].(map[string]any)
					if mode == "plan" {
						if search["anyOf"] == nil {
							t.Fatal("planning search missing")
						}
					} else if search["type"] != "null" {
						t.Fatal("search exposed outside planning")
					}
					d := Decision{Intent: IntentPlan{Goal: "確認", Investigation: "利用箇所", Response: "計画"}, Search: &github.CodeSearch{Query: "Thing"}}
					raw, _ := json.Marshal(d)
					var response any
					if name == "openai" {
						response = map[string]any{"status": "completed", "output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": string(raw)}}}}}
					} else {
						response = map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(raw)}}}}}}
					}
					encoded, _ := json.Marshal(response)
					return string(encoded)
				})
				var p Provider = &OpenAI{HTTP: client, Endpoint: "https://example.test"}
				if name == "gemini" {
					p = &Gemini{HTTP: client, Endpoint: "https://example.test"}
				}
				_, _, err := p.Next(context.Background(), "key", "model", input)
				if mode == "plan" && err != nil || mode != "plan" && err == nil {
					t.Fatalf("search boundary not enforced: %v", err)
				}
			})
		}
	}
}
