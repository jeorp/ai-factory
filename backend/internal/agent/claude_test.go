package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func claudeStream(text, stopReason string) string {
	quoted, _ := json.Marshal(text)
	return "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-haiku-5-5\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":" + string(quoted) + "}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"" + stopReason + "\"},\"usage\":{\"output_tokens\":6}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
}

func claudeClient(t *testing.T, status int, handler func(*http.Request) string) *http.Client {
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		header := make(http.Header)
		header.Set("Content-Type", "text/event-stream")
		if status != 200 {
			header.Set("Content-Type", "application/json")
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(handler(r))), Request: r}, nil
	})}
}

func TestClaudeVertexWireContract(t *testing.T) {
	client := claudeClient(t, 200, func(r *http.Request) string {
		if r.Header.Get("Authorization") != "Bearer ya29.test-token" || r.Header.Get("x-api-key") != "" {
			t.Error("incorrect authentication")
		}
		if r.URL.Path != "/v1/projects/my-project/locations/global/publishers/anthropic/models/claude-haiku-5-5:streamRawPredict" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Fatal("invalid request")
		}
		config := body["output_config"].(map[string]any)
		if config["effort"] != "low" || config["format"].(map[string]any)["schema"] == nil {
			t.Fatalf("missing effort or structured output: %v", config)
		}
		if body["model"] != nil || body["anthropic_version"] == nil {
			t.Fatal("request not shaped for Vertex AI")
		}
		system := body["system"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.Contains(system, "各100文字以内") {
			t.Fatal("intent constraints missing from instructions")
		}
		return claudeStream(`{"intent":{"goal":"構成理解","investigation":"README確認","response":"役割を説明"},"answer":"ok","summary":"summary","read":null}`, "end_turn")
	})
	p := Claude{HTTP: client, Endpoint: "https://example.test/", Vertex: true, Project: "my-project"}
	d, usage, err := p.Next(context.Background(), "ya29.test-token", "vertex/claude-haiku-5-5", "input")
	if err != nil || d.Answer != "ok" || usage.InputTokens != 10 || usage.OutputTokens != 6 {
		t.Fatalf("unexpected result: %+v %+v %v", d, usage, err)
	}
}

func TestClaudeVertexRejectsIncompleteRefusedAndInvalidOutput(t *testing.T) {
	valid := `{"intent":{"goal":"g","investigation":"i","response":"r"},"answer":"ok","summary":"s","read":null}`
	for _, tc := range []struct{ text, stop, want string }{
		{valid, "max_tokens", "claude_incomplete"},
		{"", "refusal", "claude_refusal"},
		{"not json", "end_turn", "claude_invalid_output"},
		{`{"answer":"","summary":"","read":{"kind":"write","path":"","number":0,"detail":"summary"}}`, "end_turn", ""},
	} {
		client := claudeClient(t, 200, func(r *http.Request) string { return claudeStream(tc.text, tc.stop) })
		p := Claude{HTTP: client, Endpoint: "https://example.test/", Vertex: true, Project: "my-project"}
		_, _, err := p.Next(context.Background(), "ya29.test-token", "vertex/claude-haiku-5-5", "input")
		if err == nil || (tc.want != "" && err.Error() != tc.want) {
			t.Fatalf("stop %s: got %v, want %s", tc.stop, err, tc.want)
		}
	}
}

func TestClaudeVertexMapsHTTPErrorsWithoutDetails(t *testing.T) {
	for status, want := range map[int]string{401: "claude_token_invalid", 403: "claude_permission_denied", 404: "claude_model_unavailable", 400: "claude_http_400"} {
		client := claudeClient(t, status, func(r *http.Request) string {
			return fmt.Sprintf(`{"error":{"code":%d,"message":"secret project detail"}}`, status)
		})
		p := Claude{HTTP: client, Endpoint: "https://example.test/", Vertex: true, Project: "my-project"}
		_, _, err := p.Next(context.Background(), "ya29.test-token", "vertex/claude-haiku-5-5", "input")
		if err == nil || err.Error() != want {
			t.Fatalf("status %d: got %v, want %s", status, err, want)
		}
	}
}

func TestClaudeAPIWireContract(t *testing.T) {
	client := claudeClient(t, 200, func(r *http.Request) string {
		if r.Header.Get("X-Api-Key") != "sk-ant-test" || r.Header.Get("Authorization") != "" {
			t.Error("incorrect authentication")
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "claude-haiku-5-5" || body["stream"] != true {
			t.Fatalf("unexpected request: %v", body)
		}
		return claudeStream(`{"intent":{"goal":"g","investigation":"i","response":"r"},"answer":"ok","summary":"s","read":null}`, "end_turn")
	})
	p := Claude{HTTP: client, Endpoint: "https://example.test/"}
	d, _, err := p.Next(context.Background(), "sk-ant-test", "claude-haiku-5-5", "input")
	if err != nil || d.Answer != "ok" {
		t.Fatalf("unexpected result: %+v %v", d, err)
	}
	for status, want := range map[int]string{401: "anthropic_key_invalid", 403: "anthropic_permission_denied", 429: "anthropic_rate_limited", 404: "claude_http_404"} {
		client := claudeClient(t, status, func(r *http.Request) string { return `{"type":"error","error":{"type":"x","message":"secret"}}` })
		p := Claude{HTTP: client, Endpoint: "https://example.test/"}
		if _, _, err := p.Next(context.Background(), "sk-ant-test", "claude-haiku-5-5", "input"); err == nil || err.Error() != want {
			t.Fatalf("status %d: got %v, want %s", status, err, want)
		}
	}
}
