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
		{"missing intent", func(d *Decision) { d.Intent.Goal = "" }, "gemini_invalid_intent"},
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
