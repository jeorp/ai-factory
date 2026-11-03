package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func documentationSession(scope string) domain.Session {
	s := baseSession()
	s.RunKind = "documentation"
	s.Documentation = &domain.Documentation{Request: "ログイン機能", Scope: scope, Repository: s.Target.Repository, Commit: s.Target.Commit, Status: "researching"}
	return s
}

func documentationResult() *domain.DocumentationResult {
	return &domain.DocumentationResult{Title: "ログイン機能の実装", Summary: "認証の流れを整理します。", Sections: []domain.DocumentationSection{{Heading: "処理の流れ", Body: "画面からAPIを呼び出します。", Paths: []string{"src/login.go", "src/invented.go"}}}, Unverified: []string{"実行時の挙動は未確認です。"}}
}

func TestDocumentationUsesPinnedEvidenceAndDropsUnreadReferences(t *testing.T) {
	s := documentationSession("feature")
	original := s.Documentation
	calls, reads := 0, 0
	var saved *domain.Documentation
	r := Runner{AI: providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		calls++
		if !documentationWorkflow(input) || !strings.Contains(input, "ログイン機能") {
			t.Fatal("lost documentation task")
		}
		if calls == 1 {
			return Decision{Read: &github.Read{Kind: "file", Path: "src/login.go"}, Summary: "認証の入口を確認"}, Usage{InputTokens: 10}, nil
		}
		return Decision{Document: documentationResult()}, Usage{InputTokens: 20, OutputTokens: 30}, nil
	}), GitHub: readerFunc(func(ctx context.Context, token string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
		reads++
		if target.Commit != original.Commit || target.Repository != original.Repository {
			t.Fatal("documentation target moved")
		}
		return "source", domain.Source{URL: "https://attacker.invalid/untrusted"}, 6, nil
	})}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error {
		if saved != nil && saved.Status != "researching" {
			t.Fatal("prior checkpoint mutated")
		}
		if saved == nil && cp.Documentation != nil {
			saved = cp.Documentation
		}
		last = cp
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	d := last.Documentation
	if reads != 2 || calls != 2 || d.Status != "ready" || len(d.Result.Sections[0].Paths) != 1 || d.Result.Sections[0].Paths[0] != "src/login.go" {
		t.Fatalf("bad result: %+v", d)
	}
	if original.Status != "researching" || len(original.Evidence) != 0 || len(d.Evidence) != 0 {
		t.Fatal("mutated original or retained finished evidence")
	}
	if len(d.Sources) != 2 || !strings.Contains(d.Sources[1].URL, original.Commit+"/src/login.go") || last.Metrics.LastInputTokens != 30 || last.Metrics.LastOutputTokens != 30 || last.Answer == "" {
		t.Fatalf("lost references or usage: %+v", last)
	}
	if !strings.Contains(strings.Join(d.Limitations, " "), "src/invented.go") {
		t.Fatal("missing unread-reference limitation")
	}
}

func TestDocumentationResumesWithoutRepeatingSavedReads(t *testing.T) {
	s := documentationSession("repository")
	reads, calls := 0, 0
	r := Runner{GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		reads++
		return "code", domain.Source{}, 4, nil
	})}
	r.AI = providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		if calls == 1 {
			return Decision{Read: &github.Read{Kind: "file", Path: "src/login.go"}}, Usage{}, nil
		}
		return Decision{}, Usage{}, errors.New("temporary_provider_failure")
	})
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
	if err == nil || last.Documentation == nil || len(last.Documentation.Evidence) != 2 {
		t.Fatalf("lost checkpoint: %v", err)
	}
	s.Documentation = last.Documentation
	s.Target.Commit = strings.Repeat("b", 40)
	r.AI = providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		if strings.Contains(input, strings.Repeat("b", 40)) || !strings.Contains(input, "src/login.go") {
			t.Fatal("lost pinned research")
		}
		return Decision{Document: documentationResult()}, Usage{}, nil
	})
	err = r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil || reads != 2 || last.Documentation.Status != "ready" {
		t.Fatalf("resume failed: reads=%d err=%v", reads, err)
	}
}

func TestDocumentationRejectsWorkflowChangesAndCredentials(t *testing.T) {
	for _, test := range []struct {
		name     string
		decision Decision
		key      string
		want     string
	}{
		{"implementation", Decision{StartImplementation: &ImplementRequest{Request: "write"}}, "", "invalid_documentation_result"},
		{"review", Decision{StartReview: &ReviewRequest{Number: 1}}, "", "invalid_documentation_result"},
		{"mixed", Decision{Read: &github.Read{Kind: "file", Path: "a.go"}, Document: documentationResult()}, "", "invalid_documentation_result"},
		{"credential", Decision{Document: &domain.DocumentationResult{Title: "secret-api-key"}}, "secret-api-key", "credential_echo_blocked"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := documentationSession("feature")
			s.Documentation.Evidence = []domain.DocumentationEvidence{{Kind: "repository", Content: "listing"}}
			r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				return test.decision, Usage{}, nil
			})}
			err := r.Run(context.Background(), Input{Session: s, Key: test.key}, func(cp Checkpoint) error {
				if cp.Answer != "" || cp.Documentation.Result != nil {
					t.Fatal("saved rejected content")
				}
				return nil
			})
			if err == nil || err.Error() != test.want {
				t.Fatalf("got %v, want %s", err, test.want)
			}
		})
	}
}

func TestDocumentationBudgetBoundsRepeatedReads(t *testing.T) {
	s := documentationSession("feature")
	reads, calls := 0, 0
	r := Runner{AI: providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		calls++
		var body struct {
			Task documentationTask `json:"documentationTask"`
		}
		_ = json.Unmarshal([]byte(input), &body)
		if body.Task.FinalAnswerOnly {
			return Decision{Document: documentationResult()}, Usage{}, nil
		}
		return Decision{Read: &github.Read{Kind: "repository"}}, Usage{}, nil
	}), GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		reads++
		return "listing", domain.Source{}, 7, nil
	})}
	err := r.Run(context.Background(), Input{Session: s}, func(Checkpoint) error { return nil })
	if err != nil || calls != DocumentationMaxCalls-DocumentationFinalReserve+1 || reads != 1 {
		t.Fatalf("unbounded work: %v calls=%d reads=%d", err, calls, reads)
	}
}

func TestDocumentationContextBoundsEscapedEvidenceAndKeepsInstructions(t *testing.T) {
	d := documentationSession("feature").Documentation
	for i := 0; i < 20; i++ {
		d.Evidence = append(d.Evidence, domain.DocumentationEvidence{Kind: "file", Path: fmt.Sprintf("file%d.go", i), Content: strings.Repeat("\x00", 12*1024)})
	}
	input, omitted := buildDocumentationContext(d, documentationTask{Scope: "feature", Request: d.Request}, []domain.Message{{Role: "instruction", Text: "エラー処理も説明して"}})
	if len(input) > DocumentationInputBytes || !json.Valid([]byte(input)) || !omitted || !strings.Contains(input, "エラー処理も説明して") || !strings.Contains(input, "omittedExcerpts") {
		t.Fatalf("bad context: %d bytes", len(input))
	}
	if len(d.Evidence[0].Content) != 12*1024 {
		t.Fatal("context building mutated durable research")
	}
}

func TestDocumentationFileLimitReservesFinalGeneration(t *testing.T) {
	s := documentationSession("feature")
	for i := 0; i < DocumentationMaxFiles; i++ {
		s.Documentation.Evidence = append(s.Documentation.Evidence, domain.DocumentationEvidence{Kind: "file", Path: fmt.Sprintf("file%d.go", i), Content: "code"})
	}
	calls := 0
	r := Runner{AI: providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		calls++
		var body struct {
			Task documentationTask `json:"documentationTask"`
		}
		_ = json.Unmarshal([]byte(input), &body)
		if !body.Task.FinalAnswerOnly || body.Task.RemainingFiles != 0 {
			t.Fatal("file limit did not force final generation")
		}
		return Decision{Document: documentationResult()}, Usage{}, nil
	})}
	err := r.Run(context.Background(), Input{Session: s}, func(Checkpoint) error { return nil })
	if err != nil || calls != 1 {
		t.Fatalf("final generation failed: %v", err)
	}
}

func TestDocumentationHonorsSessionTokenBudgetAndTracksPhaseUsage(t *testing.T) {
	s := documentationSession("feature")
	s.TokenBudget = 10
	calls := 0
	r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{Read: &github.Read{Kind: "file", Path: "src/login.go"}}, Usage{InputTokens: 10}, nil
	}), GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		return "code", domain.Source{}, 4, nil
	})}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
	if err == nil || err.Error() != "ai_token_budget_exceeded" || calls != 1 || last.Metrics.Phases["documentation"].Calls != 1 || last.Documentation.Calls != 1 {
		t.Fatalf("documentation bypassed token budget: calls=%d error=%v checkpoint=%+v", calls, err, last)
	}
}

func TestDocumentationCheckpointFailurePreventsWork(t *testing.T) {
	r := Runner{GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		t.Fatal("read after failed save")
		return "", domain.Source{}, 0, nil
	})}
	err := r.Run(context.Background(), Input{Session: documentationSession("feature")}, func(Checkpoint) error { return errors.New("save_failed") })
	if err == nil || err.Error() != "save_failed" {
		t.Fatal(err)
	}
}

func TestDocumentationSchemaDisablesWritesAndFinalReads(t *testing.T) {
	for _, final := range []bool{false, true} {
		schema := object(map[string]any{"answer": map[string]any{"type": "string"}, "read": nullable(implementRead([]string{"file"}))})
		addReviewSchema(schema)
		addImplementationSchema(schema)
		input, _ := json.Marshal(map[string]any{"documentationTask": documentationTask{Scope: "feature", FinalAnswerOnly: final, RemainingReads: 5, RemainingFiles: 4}})
		prompt := decisionInstructions(schema, string(input))
		props := schema["properties"].(map[string]any)
		if prompt != documentationSystemPrompt || props["document"] == nil || props["startImplementation"].(map[string]any)["type"] != "null" || props["startReview"].(map[string]any)["type"] != "null" {
			t.Fatal("wrong workflow schema")
		}
		if final && props["read"].(map[string]any)["type"] != "null" {
			t.Fatal("final schema permits reads")
		}
	}
	if documentationWorkflow(`{"evidence":"documentationTask: feature"}`) {
		t.Fatal("source text selected workflow")
	}
}

func TestDocumentationDemoSupportsBothScopes(t *testing.T) {
	for _, scope := range []string{"feature", "repository"} {
		gh := &github.Client{Demo: true}
		r := Runner{AI: Demo{}, GitHub: gh}
		s := documentationSession(scope)
		s.Target.Repository = "demo/ai-factory"
		s.Documentation.Repository = s.Target.Repository
		var last Checkpoint
		err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
		if err != nil || last.Documentation.Status != "ready" || len(last.Documentation.Result.Sections) != 6 || !strings.Contains(last.Documentation.Result.Title, "固定デモ") {
			t.Fatalf("demo %s failed: %v", scope, err)
		}
	}
}
