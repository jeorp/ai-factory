package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestProvidersExposeAndDecodeChatReviewAction(t *testing.T) {
	for _, name := range []string{"openai", "gemini"} {
		t.Run(name, func(t *testing.T) {
			decision := Decision{Intent: IntentPlan{Goal: "PRをレビュー", Investigation: "分割して確認", Response: "指摘を提示"}, StartReview: &ReviewRequest{Number: 42, Scope: "backend"}}
			decisionJSON, _ := json.Marshal(decision)
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
				if schema["properties"].(map[string]any)["startReview"] == nil {
					t.Fatal("chat review action missing from provider schema")
				}
				required := false
				for _, field := range schema["required"].([]any) {
					if field == "startReview" {
						required = true
					}
				}
				if !required {
					t.Fatal("action missing from strict output contract")
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
			d, _, err := provider.Next(context.Background(), "test-key", "test-model", "input")
			if err != nil || d.StartReview == nil || d.StartReview.Number != 42 || d.StartReview.Scope != "backend" {
				t.Fatalf("review action was not decoded: %+v %v", d, err)
			}
		})
	}
}
