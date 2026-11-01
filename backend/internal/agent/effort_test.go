package agent

import (
	"ai-factory/backend/internal/domain"
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestGeminiSelectedEffortWire(t *testing.T) {
	for _, level := range []string{"minimal", "low", "medium", "high"} {
		t.Run(level, func(t *testing.T) {
			calls := 0
			p := &Gemini{Endpoint: "https://example.test", HTTP: fakeClient(func(r *http.Request) string {
				calls++
				var body struct {
					Config struct {
						Thinking struct {
							Level string `json:"thinkingLevel"`
						} `json:"thinkingConfig"`
					} `json:"generationConfig"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				if body.Config.Thinking.Level != level {
					t.Fatalf("got %s, want %s", body.Config.Thinking.Level, level)
				}
				return `{"candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"{\"intent\":{\"goal\":\"g\",\"investigation\":\"i\",\"response\":\"r\"},\"answer\":\"ok\",\"summary\":\"s\",\"read\":null}"}]}}]}`
			})}
			if _, _, err := p.Next(withEffort(context.Background(), level), "key", "gemini-3.5-flash-lite", "input"); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatal("missing call")
			}
		})
	}
	p := &Gemini{HTTP: fakeClient(func(*http.Request) string { t.Fatal("invalid effort reached provider"); return "" })}
	if _, _, err := p.Next(withEffort(context.Background(), "minimal"), "key", "gemini-3.1-pro-preview", "input"); err == nil || err.Error() != "effort_not_allowed" {
		t.Fatalf("invalid value accepted: %v", err)
	}
}

func TestRunnerCarriesEffortAcrossWorkflows(t *testing.T) {
	for _, mode := range []string{"chat", "review", "implement"} {
		t.Run(mode, func(t *testing.T) {
			s := baseSession()
			s.Effort = "high"
			r := &Runner{}
			if mode == "review" {
				s = reviewSession()
				s.Effort = "high"
				r.GitHub = fixture()
			}
			if mode == "implement" {
				s.RunKind = "implement"
				s.Implementation = &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "A", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
				r.Writer = newFakeWriter()
			}
			calls := 0
			r.AI = providerFunc(func(ctx context.Context, _, _, input string) (Decision, Usage, error) {
				calls++
				if selectedEffort(ctx) != "high" {
					t.Fatal("lost session effort")
				}
				if mode == "review" {
					return result(), Usage{}, nil
				}
				if mode == "implement" {
					task := implementTaskOf(t, input)
					if task.Mode == "verify" {
						return Decision{Summary: "checked", Verification: passedVerification(s.Implementation.Plan)}, Usage{}, nil
					}
					return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}, Usage{}, nil
				}
				return Decision{Answer: "ok"}, Usage{}, nil
			})
			if err := r.Run(context.Background(), Input{Session: s}, func(Checkpoint) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if calls == 0 {
				t.Fatal("no calls")
			}
		})
	}
}
