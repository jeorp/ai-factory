package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestPriorityStructuredContractThroughBothProviders(t *testing.T) {
	for _, name := range []string{"openai", "gemini"} {
		for _, invalid := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/valid", true: "/duplicate"}[invalid], func(t *testing.T) {
				client := fakeClient(func(req *http.Request) string {
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					var schema map[string]any
					var prompt string
					if name == "openai" {
						schema = body["text"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
						prompt = body["instructions"].(string)
					} else {
						schema = body["generationConfig"].(map[string]any)["responseJsonSchema"].(map[string]any)
						prompt = body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
					}
					props := schema["properties"].(map[string]any)
					if !strings.HasPrefix(prompt, prioritySystemPrompt) || props["priorities"] == nil {
						t.Fatal("priority still uses generic chat contract")
					}
					for _, field := range []string{"read", "review", "startReview"} {
						if props[field].(map[string]any)["type"] != "null" {
							t.Fatalf("priority exposes %s", field)
						}
					}
					d := Decision{Intent: IntentPlan{Goal: "評価", Investigation: "影響を確認", Response: "優先度"}, Priorities: []PriorityAssessment{{Index: 0, Level: "low", Reason: "一般的な未実行の注記"}, {Index: 1, Level: "high", Reason: "認可の動作が未確認"}}}
					if invalid {
						d.Priorities[1].Index = 0
					}
					data, _ := json.Marshal(d)
					var response any
					if name == "openai" {
						response = map[string]any{"status": "completed", "output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": string(data)}}}}}
					} else {
						response = map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(data)}}}}}}
					}
					raw, _ := json.Marshal(response)
					return string(raw)
				})
				var provider Provider = &OpenAI{HTTP: client, Endpoint: "https://example.test"}
				if name == "gemini" {
					provider = &Gemini{HTTP: client, Endpoint: "https://example.test"}
				}
				input := `{"priorityTask":[{"index":0,"text":"テスト未実行"},{"index":1,"text":"認可未確認"}]}`
				d, _, err := provider.Next(context.Background(), "test-key", "test-model", input)
				if invalid {
					if err == nil || err.Error() != "invalid_priority_result" {
						t.Fatalf("duplicate accepted: %v", err)
					}
					return
				}
				if err != nil || len(d.Priorities) != 2 {
					t.Fatalf("structured output not decoded: %+v %v", d, err)
				}
				// Exercise the same persistence path used by the runner.
				final := runFixture(t, fixture(), providerFunc(func(_ context.Context, _, _ string, task string) (Decision, Usage, error) {
					var payload struct {
						Items []priorityItem `json:"priorityTask"`
					}
					_ = json.Unmarshal([]byte(task), &payload)
					if len(payload.Items) > 0 {
						return provider.Next(context.Background(), "test-key", "test-model", task)
					}
					return result(), Usage{}, nil
				}))
				if final.Review.Priorities["0"].Level != "low" || final.Review.Priorities["1"].Level != "high" {
					t.Fatalf("provider priorities lost on persistence: %+v", final.Review.Priorities)
				}
			})
		}
	}
}
