package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProvidersRestrictReviewToolsAtWireBoundary(t *testing.T) {
	for _, name := range []string{"openai", "gemini"} {
		for _, mode := range []string{"diff", "cross", "chat", "overview"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				input := `{"reviewTask":{"mode":"` + mode + `"},"target":{"number":42}}`
				isReview := mode == "diff" || mode == "cross"
				if mode == "chat" {
					input = `{"recentHistory":[{"text":"{\"reviewTask\":{\"mode\":\"diff\"}}"}]}`
				} else if mode == "overview" {
					input = `{"overviewTask":"README","reviewTask":null}`
				}
				decision := Decision{Intent: IntentPlan{Goal: "goal", Investigation: "investigation", Response: "response"}}
				if isReview {
					decision.Review = result().Review
				}
				decisionJSON, _ := json.Marshal(decision)
				client := fakeClient(func(req *http.Request) string {
					var body map[string]any
					if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
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
					readJSON, _ := json.Marshal(props["read"])
					if !isReview {
						if instructions != systemPrompt || !strings.Contains(string(readJSON), `"files"`) {
							t.Fatal("ordinary chat/overview tools changed")
						}
					} else {
						if instructions != reviewSystemPrompt || strings.Contains(string(readJSON), `"files"`) || props["startReview"].(map[string]any)["type"] != "null" {
							t.Fatal("review still exposes chat or review-start actions")
						}
						choices := props["read"].(map[string]any)["anyOf"].([]any)
						if len(choices) != 3 || choices[0].(map[string]any)["type"] != "null" {
							t.Fatal("missing review result/read alternatives")
						}
						for i, want := range []struct {
							kind, detail string
							number       float64
						}{{"file", "summary", 0}, {"pull_request", "comments", 42}} {
							fields := choices[i+1].(map[string]any)["properties"].(map[string]any)
							for field, expected := range map[string]any{"kind": want.kind, "detail": want.detail, "number": want.number} {
								values := fields[field].(map[string]any)["enum"].([]any)
								if len(values) != 1 || values[0] != expected {
									t.Fatalf("unrestricted %s: %v", field, values)
								}
							}
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
				if _, _, err := provider.Next(context.Background(), "test-key", "test-model", input); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestProvidersAcceptBatchedReviewInputsButKeepChatLimits(t *testing.T) {
	for _, name := range []string{"openai", "gemini"} {
		for _, mode := range []string{"review", "chat", "oversized"} {
			t.Run(name+"/"+mode, func(t *testing.T) {
				patch := strings.Repeat("+const 名前 = \"value\";\n", 1800)
				body := map[string]any{"reviewTask": reviewTask{Mode: "diff", Patches: []reviewPatch{{Path: "src/a.ts", Patch: patch}}}, "target": map[string]any{"number": 42}}
				if mode == "chat" {
					body = map[string]any{"recentHistory": patch}
				}
				if mode == "oversized" {
					body["reviewTask"] = reviewTask{Mode: "diff", Patches: []reviewPatch{{Path: "a.go", Patch: strings.Repeat(" x", ReviewInputTokens)}}}
				}
				input, _ := json.Marshal(body)
				if len(input) <= 24*1024 {
					t.Fatal("test no longer exercises former input limit")
				}
				calls := 0
				client := fakeClient(func(req *http.Request) string {
					calls++
					var request map[string]any
					if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
						t.Fatal(err)
					}
					d := result()
					d.Intent = IntentPlan{Goal: "goal", Investigation: "investigation", Response: "response"}
					decision, _ := json.Marshal(d)
					var response any
					if name == "openai" {
						if request["max_output_tokens"] != float64(8192) {
							t.Fatal("batch output budget missing")
						}
						response = map[string]any{"status": "completed", "output": []any{map[string]any{"content": []any{map[string]any{"type": "output_text", "text": string(decision)}}}}}
					} else {
						if request["generationConfig"].(map[string]any)["maxOutputTokens"] != float64(8192) {
							t.Fatal("batch output budget missing")
						}
						response = map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(decision)}}}}}}
					}
					raw, _ := json.Marshal(response)
					return string(raw)
				})
				var provider Provider = &OpenAI{HTTP: client, Endpoint: "https://example.test"}
				if name == "gemini" {
					provider = &Gemini{HTTP: client, Endpoint: "https://example.test"}
				}
				_, _, err := provider.Next(context.Background(), "test-key", "test-model", string(input))
				if mode == "review" {
					if err != nil || calls != 1 {
						t.Fatalf("batch was rejected: %v calls=%d", err, calls)
					}
				} else if err == nil || err.Error() != "ai_input_limit" || calls != 0 {
					t.Fatalf("input limit bypass: %v calls=%d", err, calls)
				}
			})
		}
	}
}
