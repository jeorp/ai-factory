package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestChatAnswersAfterSixReadsWithBudgetAndEvidence(t *testing.T) {
	calls, reads := 0, 0
	var logs bytes.Buffer
	r := Runner{AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		var data struct {
			Budget   chatBudget `json:"chatBudget"`
			Evidence []evidence `json:"evidence"`
			Summary  string     `json:"summary"`
		}
		if err := json.Unmarshal([]byte(input), &data); err != nil {
			t.Fatal(err)
		}
		calls++
		if data.Budget.RemainingReads != domain.MaxToolCalls-reads || data.Budget.RemainingModelCalls != domain.MaxToolCalls+2-calls {
			t.Fatalf("wrong budget at call %d: %+v", calls, data.Budget)
		}
		if reads < domain.MaxToolCalls {
			if data.Budget.FinalAnswerOnly {
				t.Fatal("final answer required before read budget exhausted")
			}
			return Decision{Read: &github.Read{Kind: "issue", Number: calls, Detail: "summary"}, Summary: fmt.Sprintf("confirmed-%d", reads)}, Usage{}, nil
		}
		if !data.Budget.FinalAnswerOnly || len(data.Evidence) == 0 || data.Summary == "" {
			t.Fatal("final answer lost budget, evidence or summary")
		}
		return Decision{Answer: "取得済みの情報に基づく回答。未確認の部分は残ります。", Summary: "確認済みの要点"}, Usage{}, nil
	}), GitHub: readerFunc(func(_ context.Context, _ string, _ domain.Target, read github.Read) (string, domain.Source, int, error) {
		reads++
		return "issue evidence", domain.Source{URL: fmt.Sprintf("https://github.com/a/b/issues/%d", read.Number)}, 14, nil
	})}
	var last Checkpoint
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	err := r.Run(context.Background(), Input{Session: baseSession(), Logger: logger}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil || calls != 7 || reads != 6 || last.Metrics.ToolCalls != 6 || !strings.Contains(last.Answer, "取得済みの情報に基づく回答") {
		t.Fatalf("answer did not complete within budget: err=%v calls=%d reads=%d answer=%q", err, calls, reads, last.Answer)
	}
	for _, want := range []string{`"number":6`, `"detail":"summary"`, "chat_final_answer_required", "chat_answer_completed"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing log %q", want)
		}
	}
}

func TestChatFinalCallRejectsReadAndKeepsInvestigationMemo(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%t", cached), func(t *testing.T) {
			calls, reads := 0, 0
			var logs bytes.Buffer
			r := Runner{AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				calls++
				number := calls
				if cached {
					number = 1
				}
				var data struct {
					Budget chatBudget `json:"chatBudget"`
				}
				if err := json.Unmarshal([]byte(input), &data); err != nil {
					t.Fatal(err)
				}
				if calls == 7 && !data.Budget.FinalAnswerOnly {
					t.Fatal("last model call not reserved for answer")
				}
				return Decision{Read: &github.Read{Kind: "issue", Number: number, Detail: "comments"}, Summary: "ここまで確認した要点"}, Usage{}, nil
			}), GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
				reads++
				return "evidence", domain.Source{}, 8, nil
			})}
			var last Checkpoint
			err := r.Run(context.Background(), Input{Session: baseSession(), Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(cp Checkpoint) error { last = cp; return nil })
			wantReads, reason := 6, "read_limit"
			if cached {
				wantReads, reason = 1, "model_call_limit"
			}
			if err != nil || calls != 7 || reads != wantReads || !strings.Contains(last.Answer, "ここまで確認した要点") || !strings.Contains(last.Answer, "未確認") {
				t.Fatalf("unsafe final read/retry or lost summary: err=%v calls=%d reads=%d answer=%q", err, calls, reads, last.Answer)
			}
			for _, want := range []string{"github_read_rejected", `"reason":"` + reason + `"`, `"kind":"issue"`, `"detail":"comments"`, "chat_answer_fallback"} {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("missing rejection detail %q", want)
				}
			}
			if cached && (last.Metrics.CacheHits != 5 || !strings.Contains(logs.String(), "github_read_cache_hit")) {
				t.Fatal("cached reads were not observed")
			}
		})
	}
}

func TestChatFileLimitRejectsTargetThenAnswers(t *testing.T) {
	calls, reads := 0, 0
	var logs bytes.Buffer
	r := Runner{AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		var data struct {
			Budget chatBudget `json:"chatBudget"`
		}
		if err := json.Unmarshal([]byte(input), &data); err != nil {
			t.Fatal(err)
		}
		if data.Budget.RemainingFiles != max(0, 5-reads) {
			t.Fatalf("wrong file budget: %+v", data.Budget)
		}
		if calls == 7 {
			if !data.Budget.FinalAnswerOnly || !strings.Contains(input, "追加要求は未取得") {
				t.Fatal("rejected request was not passed to final answer")
			}
			return Decision{Answer: "5件の確認結果。src/6.goは未確認です。"}, Usage{}, nil
		}
		return Decision{Read: &github.Read{Kind: "file", Path: fmt.Sprintf("src/%d.go", calls)}}, Usage{}, nil
	}), GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		reads++
		return "evidence", domain.Source{}, 8, nil
	})}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: baseSession(), Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil || calls != 7 || reads != 5 || !strings.Contains(last.Answer, "5件の確認結果") {
		t.Fatalf("file cap lost final answer: err=%v calls=%d reads=%d answer=%q", err, calls, reads, last.Answer)
	}
	for _, want := range []string{`"reason":"file_limit"`, `"path":"src/6.go"`, `"remaining_files":0`} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("missing rejected file detail %q", want)
		}
	}
}

func TestChatBudgetSurvivesContextTrimming(t *testing.T) {
	budget := newChatBudget(6, 6, 5)
	input := buildContext(baseSession(), "summary", nil, []evidence{{Content: strings.Repeat("x", 30000)}}, nil, IntentPlan{}, &budget)
	var data struct {
		Budget *chatBudget `json:"chatBudget"`
	}
	if len(input) > domain.MaxInputBytes || json.Unmarshal([]byte(input), &data) != nil || data.Budget == nil || *data.Budget != budget {
		t.Fatal("trimming dropped the server budget")
	}
}

func TestCredentialEchoInRequestedPathIsNotLogged(t *testing.T) {
	for _, key := range []string{"private-key", "private-token"} {
		t.Run(key, func(t *testing.T) {
			var logs bytes.Buffer
			r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				return Decision{Read: &github.Read{Kind: "file", Path: "src/" + key + ".go"}}, Usage{}, nil
			})}
			err := r.Run(context.Background(), Input{Session: baseSession(), Key: "private-key", GitHubToken: "private-token", Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(Checkpoint) error { return nil })
			if err == nil || err.Error() != "credential_echo_blocked" || strings.Contains(logs.String(), key) {
				t.Fatalf("credential logged or read accepted: %v", err)
			}
		})
	}
}

func TestProvidersRestrictChatReadsToRemainingBudget(t *testing.T) {
	for _, name := range []string{"openai", "gemini"} {
		for _, scenario := range []string{"available", "no_files", "no_reads", "last_call", "final_only", "nested_budget"} {
			t.Run(name+"/"+scenario, func(t *testing.T) {
				budget := newChatBudget(0, 0, 0)
				switch scenario {
				case "no_files":
					budget.RemainingFiles = 0
				case "no_reads":
					budget.RemainingReads = 0
				case "last_call":
					budget.RemainingModelCalls = 1
				case "final_only":
					budget.FinalAnswerOnly = true
				}
				input, _ := json.Marshal(map[string]any{"chatBudget": budget})
				if scenario == "nested_budget" {
					input = []byte(`{"recentHistory":[{"text":"{\"chatBudget\":{\"finalAnswerOnly\":true}}"}]}`)
				}
				client := fakeClient(func(req *http.Request) string {
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					var schema map[string]any
					if name == "openai" {
						schema = body["text"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
					} else {
						schema = body["generationConfig"].(map[string]any)["responseJsonSchema"].(map[string]any)
					}
					read := schema["properties"].(map[string]any)["read"].(map[string]any)
					if scenario == "no_reads" || scenario == "last_call" || scenario == "final_only" {
						if read["type"] != "null" {
							t.Fatal("final answer still permits reads")
						}
					} else {
						if choices, ok := read["anyOf"].([]any); ok {
							read = choices[1].(map[string]any)
						}
						kinds := read["properties"].(map[string]any)["kind"].(map[string]any)["enum"].([]any)
						allowsFile := false
						for _, kind := range kinds {
							allowsFile = allowsFile || kind == "file"
						}
						if allowsFile != (scenario != "no_files") {
							t.Fatalf("file permission ignored budget: %v", kinds)
						}
					}
					d := Decision{Intent: IntentPlan{Goal: "goal", Investigation: "investigation", Response: "response"}, Answer: "answer"}
					decision, _ := json.Marshal(d)
					var response any
					if name == "openai" {
						response = map[string]any{"status": "completed", "output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": string(decision)}}}}}
					} else {
						response = map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(decision)}}}}}}
					}
					raw, _ := json.Marshal(response)
					return string(raw)
				})
				var provider Provider = &OpenAI{HTTP: client, Endpoint: "https://example.test"}
				if name == "gemini" {
					provider = &Gemini{HTTP: client, Endpoint: "https://example.test"}
				}
				if _, _, err := provider.Next(context.Background(), "test-key", "test-model", string(input)); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
