package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestImplementationReadUsesGeneratedFiles(t *testing.T) {
	for _, action := range []string{"create", "update", "delete", "unchanged", "expired"} {
		t.Run(action, func(t *testing.T) {
			writer := newFakeWriter()
			impl := &domain.Implementation{Status: "editing", BaseCommit: implBase, CacheID: "0123456789abcdef"}
			if action != "unchanged" {
				impl.Changes = []domain.ChangeSummary{{Path: "src/a.go", Action: action}}
				if action != "expired" {
					_ = writer.PutChange(context.Background(), impl.CacheID, 0, domain.FileChange{Path: "src/a.go", Action: action, Content: "new generated definition"})
				}
			}
			reads := 0
			run := &implementRun{r: &Runner{Writer: writer, GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
				reads++
				return "base definition", domain.Source{}, 15, nil
			})}, impl: impl, logger: slog.Default(), emit: func(Checkpoint) error { return nil }}
			var ev []evidence
			err := run.read(context.Background(), github.Read{Kind: "file", Path: "src/a.go", Detail: "summary"}, &ev, 3)
			if action == "expired" {
				if err == nil || err.Error() != "implementation_changes_expired" || reads != 0 {
					t.Fatalf("expired overlay fell back: %v reads=%d", err, reads)
				}
				return
			}
			if err != nil || len(ev) != 1 {
				t.Fatalf("read: %v %+v", err, ev)
			}
			if action == "unchanged" {
				if reads != 1 || ev[0].Content != "base definition" {
					t.Fatal("base fallback missing")
				}
			} else if reads != 0 || (action == "delete" && !strings.Contains(ev[0].Content, "削除済み")) || (action != "delete" && !strings.Contains(ev[0].Content, "new generated definition")) {
				t.Fatalf("wrong overlay: %+v reads=%d", ev, reads)
			}
		})
	}
}

func TestStepContextIncludesGeneratedCreatesUpdatesAndDeletes(t *testing.T) {
	writer := newFakeWriter()
	impl := &domain.Implementation{CacheID: "0123456789abcdef", BaseCommit: implBase, Plan: &domain.ImplementationPlan{}}
	for _, f := range []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "new definition"}, {Path: "lib/new.go", Action: "create", Content: "new helper"}, {Path: "src/old.go", Action: "delete"}} {
		_ = writer.PutChange(context.Background(), impl.CacheID, 0, f)
		impl.Changes = append(impl.Changes, domain.ChangeSummary{Path: f.Path, Action: f.Action})
	}
	run := &implementRun{r: &Runner{Writer: writer}, impl: impl}
	contextFiles, _, err := run.stepContext(context.Background(), []string{"web/use.ts"})
	if err != nil || len(contextFiles) != 3 {
		t.Fatalf("context: %+v %v", contextFiles, err)
	}
	for _, f := range contextFiles {
		if !f.Modified {
			t.Fatalf("generated file marked original: %+v", f)
		}
		if f.Path == "src/a.go" && f.Content != "new definition" {
			t.Fatal("stale definition")
		}
		if f.Path == "lib/new.go" && f.Content != "new helper" {
			t.Fatal("create omitted")
		}
		if f.Path == "src/old.go" && !f.Deleted {
			t.Fatal("delete omitted")
		}
	}
}

func verificationSession(writer *fakeWriter, files []domain.FileChange) domain.Session {
	session := baseSession()
	session.RunKind = "implement"
	impl := &domain.Implementation{Request: "APIと画面を変更", Status: "verifying", BaseCommit: implBase, BaseBranch: "main", CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{Summary: "API変更"}, NextStep: 1, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	for _, f := range files {
		_ = writer.PutChange(context.Background(), impl.CacheID, 0, f)
		impl.Changes = append(impl.Changes, domain.ChangeSummary{Path: f.Path, Action: f.Action, Bytes: len(f.Content)})
		impl.Plan.Files = append(impl.Plan.Files, domain.PlannedFile{Path: f.Path, Action: f.Action, Approach: "変更"})
	}
	if len(files) > 0 {
		impl.Plan.AcceptanceCriteria = testCriteria(files[0].Path)
	}
	session.Implementation = impl
	return session
}

func TestVerificationChecksEveryByteThenCrossChecksAndShowsIssues(t *testing.T) {
	writer := newFakeWriter()
	files := []domain.FileChange{}
	for i := 0; i < 4; i++ {
		files = append(files, domain.FileChange{Path: fmt.Sprintf("src/file%d.ts", i), Action: "create", Content: strings.Repeat("日本語", 1900) + fmt.Sprintf("END%d", i)})
	}
	session := verificationSession(writer, files)
	session.Implementation.Plan.Files = append(session.Implementation.Plan.Files, domain.PlannedFile{Path: "src/missing_test.go", Action: "create", Approach: "テスト"})
	seen := map[string]string{}
	batches, crossCalls := 0, 0
	ai := providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		var body struct {
			Task      implementTask      `json:"implementTask"`
			Stage     string             `json:"stage"`
			Parts     []verificationPart `json:"parts"`
			Summaries []string           `json:"summaries"`
		}
		if err := json.Unmarshal([]byte(input), &body); err != nil {
			t.Fatal(err)
		}
		if body.Task.Mode != "verify" || body.Task.Plan == nil || len(body.Task.Done) != 4 {
			t.Fatalf("missing plan/changes: %+v", body.Task)
		}
		v := &domain.ImplementationVerification{}
		if body.Stage == "cross" {
			crossCalls++
			if len(body.Summaries) != batches || len(body.Parts) != 0 {
				t.Fatal("cross-check lost summaries")
			}
			v.Issues = []string{"src/file0.ts のAPI引数と src/file1.ts の呼び出しが一致しません。"}
		} else {
			batches++
			for _, p := range body.Parts {
				if !utf8.ValidString(p.Content) || p.Offset != len(seen[p.Path]) {
					t.Fatalf("gap in content: %+v", p)
				}
				seen[p.Path] += p.Content
			}
		}
		return Decision{Summary: "API引数と呼び出し契約", Verification: v}, Usage{InputTokens: 11, OutputTokens: 3}, nil
	})
	var last Checkpoint
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if seen[f.Path] != f.Content {
			t.Fatalf("content omitted for %s", f.Path)
		}
	}
	v := last.Implementation.Verification
	if batches < 2 || crossCalls != 1 || last.Implementation.Status != "verification_blocked" || v == nil || len(v.Issues) != 1 {
		t.Fatalf("verification incomplete: batches=%d cross=%d %+v", batches, crossCalls, last.Implementation)
	}
	if !strings.Contains(strings.Join(v.Unverified, "\n"), "src/missing_test.go") || !strings.Contains(last.Implementation.Body, v.Issues[0]) {
		t.Fatal("issues not surfaced")
	}
	if last.Metrics.InputTokens != (batches+crossCalls)*11 {
		t.Fatal("verification usage missing")
	}
}

func TestVerificationFailureCanResumeWithoutEditingAgain(t *testing.T) {
	writer := newFakeWriter()
	session := verificationSession(writer, []domain.FileChange{{Path: "src/new.go", Action: "create", Content: "package src"}})
	calls := 0
	ai := providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode != "verify" {
			t.Fatal("completed edit repeated")
		}
		calls++
		return Decision{}, Usage{}, nil
	})
	var last Checkpoint
	err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil })
	if err == nil || err.Error() != "implementation_verification_failed" || calls != 3 || last.Implementation.Status != "verifying" || last.Implementation.Verification != nil {
		t.Fatalf("invalid verification accepted: calls=%d %v %+v", calls, err, last.Implementation)
	}
	session.Implementation = last.Implementation
	ai = providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		if implementTaskOf(t, input).Mode != "verify" {
			t.Fatal("completed edit repeated on resume")
		}
		return Decision{Summary: "確認", Verification: passedVerification(implementTaskOf(t, input).Plan)}, Usage{}, nil
	})
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil || last.Implementation.Status != "ready" {
		t.Fatalf("resume failed: %v", err)
	}
}

func TestVerificationReadsDeletedSourceAndRefusesExpiredChanges(t *testing.T) {
	writer := newFakeWriter()
	session := verificationSession(writer, []domain.FileChange{{Path: "src/old.go", Action: "delete"}})
	called := false
	ai := providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		called = true
		var body struct {
			Parts []verificationPart `json:"parts"`
		}
		_ = json.Unmarshal([]byte(input), &body)
		if len(body.Parts) != 1 || body.Parts[0].Action != "delete" || body.Parts[0].Content != writer.files["src/old.go"] {
			t.Fatal("deleted source missing from verification")
		}
		return Decision{Summary: "削除の利用箇所を確認", Verification: passedVerification(implementTaskOf(t, input).Plan)}, Usage{}, nil
	})
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(Checkpoint) error { return nil }); err != nil || !called {
		t.Fatalf("delete check failed: %v", err)
	}
	writer.cache = map[string]domain.FileChange{}
	called = false
	err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(Checkpoint) error { return nil })
	if err == nil || err.Error() != "implementation_changes_expired" || called {
		t.Fatalf("expired code was verified: %v", err)
	}
}

func TestImplementationLogsCodesAndRetriesWithoutResponseText(t *testing.T) {
	var logs bytes.Buffer
	writer := newFakeWriter()
	session := verificationSession(writer, []domain.FileChange{{Path: "src/new.go", Action: "create", Content: "package src"}})
	calls := 0
	ai := providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		if calls == 1 {
			return Decision{}, Usage{}, errors.New("invalid_implementation_result")
		}
		return Decision{}, Usage{}, fmt.Errorf("untrusted secret-response sk-private-content: %w", errors.New("gemini_http_429"))
	})
	err := (&Runner{AI: ai, Writer: writer, retryWait: skipRetryWait}).Run(context.Background(), Input{Session: session, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(Checkpoint) error { return nil })
	text := logs.String()
	if err == nil || !strings.Contains(text, `"phase":"verify"`) || !strings.Contains(text, `"error_code":"gemini_http_429"`) || !strings.Contains(text, `"retry_count":1`) || !strings.Contains(text, `"attempt":2`) || strings.Contains(text, "secret-response") || strings.Contains(text, "sk-private-content") {
		t.Fatalf("unsafe or incomplete logs: %s err=%v", text, err)
	}
}

func TestProvidersExposeReadOnlyVerificationSchema(t *testing.T) {
	for _, name := range []string{"openai", "gemini"} {
		t.Run(name, func(t *testing.T) {
			client := fakeClient(func(req *http.Request) string {
				var body map[string]any
				_ = json.NewDecoder(req.Body).Decode(&body)
				var schema map[string]any
				if name == "openai" {
					schema = body["text"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)
				} else {
					schema = body["generationConfig"].(map[string]any)["responseJsonSchema"].(map[string]any)
				}
				props := schema["properties"].(map[string]any)
				for _, field := range []string{"read", "plan", "implementation", "startImplementation", "startReview", "review"} {
					if props[field].(map[string]any)["type"] != "null" {
						t.Fatalf("verify exposes %s", field)
					}
				}
				if props["verification"].(map[string]any)["anyOf"] == nil {
					t.Fatal("verification not required")
				}
				d := Decision{Intent: IntentPlan{Goal: "確認", Investigation: "整合性", Response: "結果"}, Verification: &domain.ImplementationVerification{}}
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
			if _, _, err := p.Next(context.Background(), "key", "model", `{"implementTask":{"mode":"verify"}}`); err != nil {
				t.Fatal(err)
			}
		})
	}
}
