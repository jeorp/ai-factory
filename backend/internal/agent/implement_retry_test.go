package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestImplementationCorrectsInvalidGeminiResponse(t *testing.T) {
	for _, phase := range []string{"plan", "edit"} {
		t.Run(phase, func(t *testing.T) {
			w := newFakeWriter()
			impl := &domain.Implementation{Request: "Aの戻り値を修正", BaseCommit: implBase, BaseBranch: "main", CacheID: "0123456789abcdef", Status: "planning"}
			if phase == "edit" {
				impl.Status = "editing"
				impl.Plan = &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "Aを修正", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "戻り値を2にする"}}}
				impl.Steps = domain.PlanSteps(impl.Plan.Files)
			}
			calls := 0
			p := &Gemini{Endpoint: "https://example.test", HTTP: fakeClient(func(req *http.Request) string {
				var body struct {
					Contents []struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"contents"`
				}
				_ = json.NewDecoder(req.Body).Decode(&body)
				task := implementTaskOf(t, body.Contents[0].Parts[0].Text)
				calls++
				if calls > 1 && task.Correction == "" {
					t.Fatal("invalid response was retried without correction")
				}
				d := Decision{Intent: IntentPlan{Goal: "修正", Investigation: "コード確認", Response: "計画"}}
				if calls <= 2 {
					if len(w.cache) != 0 {
						t.Fatal("invalid model output wrote a change")
					}
					d.Search = &github.CodeSearch{Query: "private-code-fragment"}
					d.Answer = "sensitive-model-response"
				} else if phase == "plan" {
					d.Plan = &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "Aを修正", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "戻り値を2にする"}}}
				} else {
					d.Implementation = &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}
				}
				raw, _ := json.Marshal(d)
				response, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"finishReason": "STOP", "content": map[string]any{"parts": []any{map[string]any{"text": string(raw)}}}}}})
				return string(response)
			})}
			var logs bytes.Buffer
			r := &Runner{Writer: w, AI: implementProviderFunc(p.Next)}
			s := baseSession()
			s.RunKind, s.Implementation = "implement", impl
			var last Checkpoint
			err := r.Run(context.Background(), Input{Session: s, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(cp Checkpoint) error { last = cp; return nil })
			if err != nil {
				t.Fatal(err)
			}
			if calls != 3 || (phase == "plan" && last.Implementation.Status != "plan_ready") || (phase == "edit" && last.Implementation.Status != "ready") {
				t.Fatalf("not recovered: calls=%d %+v", calls, last.Implementation)
			}
			if !strings.Contains(logs.String(), `"reason":"search_action_conflict"`) || !strings.Contains(logs.String(), `"retry_count":2`) || strings.Contains(logs.String(), "sensitive-model-response") || strings.Contains(logs.String(), "private-code-fragment") {
				t.Fatalf("unsafe or insufficient diagnostics: %s", logs.String())
			}
		})
	}
}

func TestInvalidImplementationRetriesAreBoundedAndOtherErrorsStop(t *testing.T) {
	for _, code := range []string{"invalid_implementation_result", "gemini_invalid_output", "gemini_http_401"} {
		t.Run(code, func(t *testing.T) {
			calls := 0
			w := newFakeWriter()
			r := &Runner{Writer: w, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				calls++
				return Decision{}, Usage{}, errors.New(code)
			})}
			impl := &domain.Implementation{Status: "planning", BaseCommit: implBase, CacheID: "0123456789abcdef"}
			var saved Checkpoint
			err := runImplementationFixture(r, impl, func(cp Checkpoint) error { saved = cp; return nil })
			want := 3
			if code == "gemini_http_401" {
				want = 1
			}
			if err == nil || err.Error() != code || calls != want || len(w.cache) != 0 || saved.Implementation.Status != "planning" {
				t.Fatalf("bad retry boundary: calls=%d err=%v %+v", calls, err, saved)
			}
		})
	}
}

func TestInvalidImplementationDoesNotRetryAfterCheckpointFailure(t *testing.T) {
	calls := 0
	r := &Runner{Writer: newFakeWriter(), AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{}, Usage{}, errors.New("invalid_implementation_result")
	})}
	err := runImplementationFixture(r, &domain.Implementation{Status: "planning", BaseCommit: implBase}, func(cp Checkpoint) error {
		if strings.Contains(cp.Progress, "再試行") {
			return errors.New("checkpoint_failed")
		}
		return nil
	})
	if err == nil || err.Error() != "checkpoint_failed" || calls != 1 {
		t.Fatalf("retried without saving: %v calls=%d", err, calls)
	}
}
