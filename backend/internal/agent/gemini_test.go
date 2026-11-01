package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func fakeClient(handler func(*http.Request) string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(handler(r)))}, nil
	})}
}

func TestGeminiWireContract(t *testing.T) {
	client := fakeClient(func(r *http.Request) string {
		if r.Method != http.MethodPost || r.Header.Get("x-goog-api-key") != "AIza-test-secret" {
			t.Error("incorrect authentication")
		}
		if strings.Contains(r.URL.RawQuery, "secret") {
			t.Error("key in URL")
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid request")
		}
		config := body["generationConfig"].(map[string]any)
		if config["responseMimeType"] != "application/json" || config["responseJsonSchema"] == nil {
			t.Fatal("missing structured output")
		}
		instruction := body["systemInstruction"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.Contains(instruction, "各100文字以内") {
			t.Fatal("intent constraints missing from instructions")
		}
		return `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"{\"intent\":{\"goal\":\"構成理解\",\"investigation\":\"README確認\",\"response\":\"役割を説明\"},\"answer\":\"ok\",\"summary\":\"summary\",\"read\":null}"}]}}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"thoughtsTokenCount":2}}`
	})
	p := Gemini{HTTP: client, Endpoint: "https://example.test"}
	d, usage, err := p.Next(context.Background(), "AIza-test-secret", "gemini-2.5-flash", "input")
	if err != nil || d.Answer != "ok" || usage.InputTokens != 10 || usage.OutputTokens != 6 {
		t.Fatalf("unexpected result: %+v %+v %v", d, usage, err)
	}
}

func TestGeminiRejectsIncompleteAndInvalidReads(t *testing.T) {
	for _, body := range []string{`{"candidates":[{"finishReason":"MAX_TOKENS"}]}`, `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"{\"answer\":\"\",\"summary\":\"\",\"read\":{\"kind\":\"write\",\"path\":\"\",\"number\":0,\"detail\":\"summary\"}}"}]}}]}`} {
		client := fakeClient(func(r *http.Request) string { return body })
		p := Gemini{HTTP: client, Endpoint: "https://example.test"}
		_, _, err := p.Next(context.Background(), "AIza-test-secret", "gemini-2.5-flash", "input")
		if err == nil {
			t.Fatal("expected response to be rejected")
		}
	}
}

func TestGeminiReportsValidationFailures(t *testing.T) {
	valid := Decision{Intent: IntentPlan{Goal: "goal", Investigation: "investigation", Response: "response"}}
	tests := []struct {
		name   string
		change func(*Decision)
		want   string
	}{
		{"answer length", func(d *Decision) { d.Answer = strings.Repeat("x", 16*1024+1) }, "gemini_answer_too_long"},
		{"invalid review", func(d *Decision) { d.Review = &domain.ReviewResult{Unverified: make([]string, 11)} }, "gemini_invalid_review"},
		{"conflicting review action", func(d *Decision) { d.StartReview = &ReviewRequest{Number: 42}; d.Answer = "answer" }, "gemini_invalid_review_action"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := valid
			tc.change(&d)
			decision, _ := json.Marshal(d)
			response, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(decision)}}}}}})
			p := Gemini{HTTP: fakeClient(func(*http.Request) string { return string(response) }), Endpoint: "https://example.test"}
			_, _, err := p.Next(context.Background(), "key", "model", "input")
			if err == nil || err.Error() != tc.want {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

func TestGeminiDiscardsInvalidIntentWithoutLosingDecision(t *testing.T) {
	for _, tc := range []struct {
		name   string
		intent IntentPlan
	}{
		{"missing", IntentPlan{}},
		{"blank field", IntentPlan{Goal: " ", Investigation: "調査", Response: "回答"}},
		{"too many UTF-8 bytes", IntentPlan{Goal: strings.Repeat("あ", 342), Investigation: "調査", Response: "回答"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			decision, _ := json.Marshal(Decision{Intent: tc.intent, Summary: "要約", Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update"}}}})
			response, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(decision)}}}}}})
			p := Gemini{HTTP: fakeClient(func(*http.Request) string { return string(response) }), Endpoint: "https://example.test"}
			d, _, err := p.Next(context.Background(), "key", "model", `{"implementTask":{"mode":"edit"}}`)
			if err != nil || d.Intent != (IntentPlan{}) || d.Implementation == nil || len(d.Implementation.Changes) != 1 || d.Summary != "要約" {
				t.Fatalf("decision lost: %+v, %v", d, err)
			}
		})
	}
}

func TestGeminiInvalidIntentDoesNotBypassOtherValidation(t *testing.T) {
	decision, _ := json.Marshal(Decision{Intent: IntentPlan{}, StartReview: &ReviewRequest{Number: 42}, Answer: "conflicting answer"})
	response, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(decision)}}}}}})
	p := Gemini{HTTP: fakeClient(func(*http.Request) string { return string(response) }), Endpoint: "https://example.test"}
	_, _, err := p.Next(context.Background(), "key", "model", "input")
	if err == nil || err.Error() != "gemini_invalid_review_action" {
		t.Fatalf("invalid review action accepted: %v", err)
	}
}

func TestGeminiErrorUsesOnlyAllowlistedCodes(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`{"error":{"message":"secret-data","details":[{"reason":"API_KEY_INVALID"}]}}`, "gemini_key_invalid"},
		{`{"error":{"details":[{"@type":"type.googleapis.com/google.rpc.BadRequest"}]}}`, "gemini_bad_request"},
		{`{"error":{"message":"secret-data","details":[{"reason":"unknown-secret-data"}]}}`, "gemini_http_400"},
	} {
		resp := &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(tc.body))}
		if got := geminiError(resp).Error(); got != tc.want {
			t.Fatalf("got %q, want %q", got, tc.want)
		}
	}
}

func TestGeminiThinkingConfigPerModel(t *testing.T) {
	cases := []struct {
		model string
		level any
	}{
		{"gemini-3.5-flash-lite", "medium"},
		{"gemini-3.8-flash", "medium"},
		{"gemini-3.1-pro-preview", "medium"},
		{"gemini-4-argon", "medium"},
		{"gemini-2.5-pro", nil},
		{"gemini-2.5-flash", nil},
	}
	for _, tc := range cases {
		var config map[string]any
		var path string
		client := fakeClient(func(r *http.Request) string {
			path = r.URL.Path
			var body map[string]any
			if json.NewDecoder(r.Body).Decode(&body) != nil {
				t.Fatal("invalid request")
			}
			config = body["generationConfig"].(map[string]any)
			return `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"{\"intent\":{\"goal\":\"g\",\"investigation\":\"i\",\"response\":\"r\"},\"answer\":\"ok\",\"summary\":\"s\",\"read\":null}"}]}}]}`
		})
		p := Gemini{HTTP: client}
		if _, _, err := p.Next(context.Background(), "AIza-test-secret", tc.model, "input"); err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		if path != "/v1beta/models/"+tc.model+":generateContent" {
			t.Fatalf("%s: unexpected endpoint %s", tc.model, path)
		}
		var level any
		if thinking, ok := config["thinkingConfig"].(map[string]any); ok {
			level = thinking["thinkingLevel"]
		}
		if level != tc.level {
			t.Fatalf("%s: thinkingLevel = %v, want %v", tc.model, level, tc.level)
		}
		if config["maxOutputTokens"].(float64) <= 4096 {
			t.Fatalf("%s: no headroom for thinking tokens: %v", tc.model, config["maxOutputTokens"])
		}
	}
}
