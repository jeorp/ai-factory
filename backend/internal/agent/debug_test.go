package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"ai-factory/backend/internal/domain"
)

func debugTestContext(buf *bytes.Buffer, enabled bool) context.Context {
	return withAgentDebug(context.Background(), enabled, Input{Key: "active-api-secret", GitHubToken: "active-gh-secret", Session: domain.Session{ID: "debug-session", Model: "gemini-3.5-flash-lite"}, Logger: slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))})
}

func TestDebugLoggingRequiresExplicitRunOptIn(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var logs bytes.Buffer
		ctx := debugTestContext(&logs, enabled)
		r := &Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
			return Decision{Answer: "generated-code active-api-secret"}, Usage{}, nil
		})}
		_, _, err := r.next(ctx, Input{}, "private-source active-api-secret active-gh-secret")
		if err != nil {
			t.Fatal(err)
		}
		if enabled {
			for _, want := range []string{"agent_debug_model_input", "agent_debug_model_output", "private-source", "generated-code", "ai_call_id", "debug-session"} {
				if !strings.Contains(logs.String(), want) {
					t.Fatalf("missing %s", want)
				}
			}
			for _, secret := range []string{"active-api-secret", "active-gh-secret"} {
				if strings.Contains(logs.String(), secret) {
					t.Fatal("credential leaked")
				}
			}
		} else if logs.Len() != 0 {
			t.Fatal("debug payload logged while disabled")
		}
	}
}

func TestDebugCapturesInvalidProviderResponseWithoutCredentials(t *testing.T) {
	var logs bytes.Buffer
	p := &Gemini{Endpoint: "https://example.test", HTTP: fakeClient(func(*http.Request) string {
		return `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"not-json active-api-secret"}]}}]}`
	})}
	r := &Runner{AI: p}
	_, _, err := r.next(debugTestContext(&logs, true), Input{Key: "active-api-secret", Session: domain.Session{Model: "gemini-3.5-flash-lite"}}, "source-code")
	if err == nil || err.Error() != "gemini_invalid_output" {
		t.Fatalf("expected parsing failure: %v", err)
	}
	for _, want := range []string{"agent_debug_provider_request", "agent_debug_provider_response", "not-json", "source-code"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(logs.String(), "active-api-secret") {
		t.Fatal("raw response leaked key")
	}
}

func TestDebugRedactsBeforeTruncation(t *testing.T) {
	var logs bytes.Buffer
	secret := strings.Repeat("z", 100)
	ctx := withAgentDebug(context.Background(), true, Input{Key: secret, Logger: slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))})
	debugText(ctx, "bounded", strings.Repeat("a", maxDebugPayloadBytes-10)+secret+strings.Repeat("b", 100))
	var event struct {
		Payload      string
		Truncated    bool
		PayloadBytes int `json:"payload_bytes"`
	}
	parts := decodeDebugEvents(t, logs.Bytes())
	if len(parts) != 1 {
		t.Fatalf("expected one reassembled event, got %d", len(parts))
	}
	raw, _ := json.Marshal(parts[0])
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if !event.Truncated || len(event.Payload) > maxDebugPayloadBytes || strings.Contains(event.Payload, "zzzz") {
		t.Fatal("clipping exposed partial credential")
	}
	if got := redactDebug(`{"key":"token\\\"value","pem":"-----BEGIN PRIVATE KEY-----private-material-----END PRIVATE KEY-----"}`, []string{`token\"value`}); strings.Contains(got, "private-material") || strings.Contains(got, "token") {
		t.Fatalf("unredacted escaped credential: %s", got)
	}
}

func TestDebugEditFailureShowsActualAnchorAndPlannedAction(t *testing.T) {
	var logs bytes.Buffer
	original := "first\nactual start active-gh-secret\nactual end\nlast\n"
	proposal := ChangeProposal{Path: "a.go", Action: "update", SourceHash: domain.SourceHash(original), LineEdits: []domain.LineEdit{{StartLine: 2, EndLine: 3, ExpectedStart: "wrong start", ExpectedEnd: "actual end", Content: "replacement"}}}
	impl := &domain.Implementation{Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}}}}
	run := &implementRun{impl: impl, calls: 4}
	_, err := applyProposals([]string{"a.go"}, impl, map[string]string{"a.go": original}, []ChangeProposal{proposal})
	if err == nil || implementationErrorCode(err) != "edit_line_anchor_mismatch" {
		t.Fatalf("expected mismatch: %v", err)
	}
	run.debugEditFailure(debugTestContext(&logs, true), []string{"a.go"}, map[string]string{"a.go": original}, []ChangeProposal{proposal}, err)
	var event struct{ Payload string }
	if err := json.Unmarshal(logs.Bytes(), &event); err != nil {
		t.Fatal(err)
	}
	var detail struct {
		ActualStart string            `json:"actual_start"`
		ActualEnd   string            `json:"actual_end"`
		EditIndex   int               `json:"edit_index"`
		Context     []debugSourceLine `json:"source_context"`
		Files       []struct{ Planned, Submitted string }
	}
	if err := json.Unmarshal([]byte(event.Payload), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.ActualStart != "actual start ***REDACTED***" || detail.ActualEnd != "actual end" || detail.EditIndex != 1 || len(detail.Context) == 0 {
		t.Fatalf("bad mismatch details: %+v", detail)
	}
	for _, want := range []string{"wrong start", "planned_action", "submitted_action", "edit_line_anchor_mismatch"} {
		if !strings.Contains(event.Payload, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(logs.String(), "active-gh-secret") {
		t.Fatal("source context leaked token")
	}
	logs.Reset()
	run.debugEditFailure(debugTestContext(&logs, false), []string{"a.go"}, map[string]string{"a.go": original}, []ChangeProposal{proposal}, err)
	if logs.Len() != 0 {
		t.Fatal("edit details logged while disabled")
	}
}

func TestDebugSearchFailureUsesSourceAfterEarlierEdits(t *testing.T) {
	var logs bytes.Buffer
	impl := &domain.Implementation{Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}}}}
	proposal := ChangeProposal{Path: "a.go", Action: "update", Edits: []domain.Edit{{Search: "before", Replace: "after"}, {Search: "missing", Replace: "new"}}}
	originals := map[string]string{"a.go": "before\nkeep\n"}
	_, err := applyProposals([]string{"a.go"}, impl, originals, []ChangeProposal{proposal})
	run := &implementRun{impl: impl}
	run.debugEditFailure(debugTestContext(&logs, true), []string{"a.go"}, originals, []ChangeProposal{proposal}, err)
	var event struct{ Payload string }
	json.Unmarshal(logs.Bytes(), &event)
	var detail struct {
		Current   string `json:"current_source"`
		EditIndex int    `json:"edit_index"`
	}
	json.Unmarshal([]byte(event.Payload), &detail)
	if detail.EditIndex != 2 || !strings.Contains(detail.Current, "1|after") || strings.Contains(detail.Current, "before") {
		t.Fatalf("wrong source snapshot: %+v", detail)
	}
}

func decodeDebugEvents(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	out := []map[string]any{}
	pending := map[float64]map[string]any{}
	for {
		var e map[string]any
		err := decoder.Decode(&e)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		id, _ := e["payload_id"].(float64)
		if e["chunk_index"].(float64) == 1 {
			pending[id] = e
		} else {
			pending[id]["payload"] = pending[id]["payload"].(string) + e["payload"].(string)
		}
		if e["chunk_index"] == e["chunk_count"] {
			out = append(out, pending[id])
			delete(pending, id)
		}
	}
	if len(pending) > 0 {
		t.Fatal("missing log chunks")
	}
	return out
}

func TestDebugChunksPreserveJapaneseAndEscapedText(t *testing.T) {
	var logs bytes.Buffer
	payload := strings.Repeat("日本語\n\x00\"\\", 4000)
	debugText(debugTestContext(&logs, true), "utf8", payload)
	for _, line := range bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n")) {
		if !utf8.Valid(line) || len(line) >= 16*1024 {
			t.Fatalf("unsafe Docker log frame size %d", len(line))
		}
	}
	events := decodeDebugEvents(t, logs.Bytes())
	if len(events) != 1 || events[0]["payload"] != payload {
		t.Fatal("log payload was corrupted during chunking")
	}
}

func TestDebugAttemptUsesServerMetadata(t *testing.T) {
	var logs bytes.Buffer
	ctx := withDebugAttrs(debugTestContext(&logs, true), "attempt", 3, "retry_count", 2)
	r := &Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		return Decision{}, Usage{}, nil
	})}
	task := implementTask{Mode: "edit", Step: 1, Attempt: 3, RetryCount: 2}
	raw, _ := json.Marshal(map[string]any{"implementTask": task})
	if strings.Contains(string(raw), "attempt") {
		t.Fatal("server retry metadata leaked into model input")
	}
	r.next(ctx, Input{}, string(raw))
	for _, event := range decodeDebugEvents(t, logs.Bytes()) {
		if event["attempt"] != float64(3) || event["retry_count"] != float64(2) {
			t.Fatalf("incorrect retry metadata: %+v", event)
		}
	}
}

func TestDebugInvalidUTF8StillMakesProgress(t *testing.T) {
	var logs bytes.Buffer
	payload := "\xff\xfe" + strings.Repeat("日本語\x80\n", 2000)
	debugText(debugTestContext(&logs, true), "invalid_utf8", payload)
	events := decodeDebugEvents(t, logs.Bytes())
	if len(events) != 1 || events[0]["payload"] != strings.ToValidUTF8(payload, "\uFFFD") {
		t.Fatal("invalid UTF-8 log could not be reconstructed")
	}
}
