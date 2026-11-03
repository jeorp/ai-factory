package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-factory/backend/internal/domain"
)

func finalDocumentationSession() domain.Session {
	s := documentationSession("feature")
	s.Documentation.Calls = DocumentationMaxCalls - DocumentationFinalReserve
	s.Documentation.Evidence = []domain.DocumentationEvidence{{Kind: "file", Path: "src/login.go", Content: "code"}}
	return s
}

func TestDocumentationCallLimitSurvivesRepeatedResumes(t *testing.T) {
	s := documentationSession("feature")
	s.Documentation.Calls = DocumentationMaxCalls
	calls := 0
	r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{}, Usage{}, nil
	})}
	for attempt := 0; attempt < 3; attempt++ {
		var last Checkpoint
		err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
		if err == nil || err.Error() != "documentation_call_limit" || calls != 0 || last.Documentation.Calls != DocumentationMaxCalls || !strings.Contains(strings.Join(last.Documentation.Limitations, " "), "documentation_call_limit") {
			t.Fatalf("resume bypassed limit: %v calls=%d", err, calls)
		}
		s.Documentation = last.Documentation
	}
}

func TestDocumentationRetriesInvalidFinalWithinReservedCalls(t *testing.T) {
	for _, invalids := range []int{1, 2} {
		t.Run(string(rune('0'+invalids)), func(t *testing.T) {
			s := finalDocumentationSession()
			calls := 0
			r := Runner{AI: providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
				calls++
				var body struct {
					Task documentationTask `json:"documentationTask"`
				}
				_ = json.Unmarshal([]byte(input), &body)
				if !body.Task.FinalAnswerOnly {
					t.Fatal("final calls not reserved")
				}
				if calls > 1 && (body.Task.Correction != documentationCorrection || strings.Count(input, documentationCorrection) != 1) {
					t.Fatal("correction lost or accumulated")
				}
				if calls <= invalids {
					return Decision{}, Usage{InputTokens: 1}, nil
				}
				return Decision{Document: documentationResult()}, Usage{InputTokens: 1}, nil
			})}
			var last Checkpoint
			err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
			if err != nil || last.Documentation.Status != "ready" || last.Documentation.Calls != s.Documentation.Calls+invalids+1 || calls != invalids+1 {
				t.Fatalf("invalid final not recovered: %v calls=%d", err, calls)
			}
		})
	}
}

func TestDocumentationTransientRecoveryUsesSameBudget(t *testing.T) {
	for _, code := range []string{"gemini_http_503", "openai_http_429", "claude_http_529", "gemini_timeout"} {
		t.Run(code, func(t *testing.T) {
			s := finalDocumentationSession()
			calls, waits := 0, 0
			r := Runner{retryWait: func(context.Context, time.Duration) error { waits++; return nil }, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				calls++
				if calls == 1 {
					return Decision{}, Usage{InputTokens: 2}, errors.New(code)
				}
				return Decision{Document: documentationResult()}, Usage{InputTokens: 3}, nil
			})}
			var last Checkpoint
			err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
			if err != nil || calls != 2 || waits != 1 || last.Documentation.Calls != s.Documentation.Calls+2 || last.Metrics.InputTokens != 5 || last.Metrics.Phases["documentation"].Calls != 2 {
				t.Fatalf("bad recovery/accounting: %v calls=%d", err, calls)
			}
		})
	}
}

func TestDocumentationStopsWhenFinalRetriesExhaustBudget(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		s := finalDocumentationSession()
		calls, waits := 0, 0
		r := Runner{retryWait: func(context.Context, time.Duration) error { waits++; return nil }, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
			calls++
			if invalid {
				return Decision{}, Usage{}, nil
			}
			return Decision{}, Usage{}, errors.New("gemini_http_503")
		})}
		var last Checkpoint
		err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
		if err == nil || err.Error() != "documentation_call_limit" || calls != DocumentationFinalReserve || last.Documentation.Calls != DocumentationMaxCalls {
			t.Fatalf("final retries exceeded budget: %v calls=%d", err, calls)
		}
		if !invalid && waits != 2 {
			t.Fatal("unexpected backoff count")
		}
	}
}

func TestDocumentationKeepsFinalDocumentWhenLastCallReceivesInstructions(t *testing.T) {
	s := finalDocumentationSession()
	s.Documentation.Calls = DocumentationMaxCalls - 1
	instructions := make(chan domain.Message, 2)
	calls := 0
	r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		instructions <- domain.Message{ID: "late", Role: "instruction", Text: "認証エラーも説明して"}
		return Decision{Document: documentationResult()}, Usage{}, nil
	})}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s, Instructions: instructions}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil || calls != 1 || last.Documentation.Status != "ready" || last.Documentation.Calls != DocumentationMaxCalls || strings.Contains(strings.Join(last.Documentation.Limitations, " "), "documentation_call_limit") || !strings.Contains(strings.Join(last.Documentation.Limitations, " "), "指示は反映できませんでした") {
		t.Fatalf("lost final document: %v", err)
	}
	for _, id := range last.Applied {
		if id == "late" {
			t.Fatal("unapplied instruction marked applied")
		}
	}
}

func TestDocumentationLateInstructionsUseRemainingFinalCall(t *testing.T) {
	s := finalDocumentationSession()
	s.Documentation.Calls = DocumentationMaxCalls - 2
	instructions := make(chan domain.Message, 2)
	calls := 0
	r := Runner{AI: providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		calls++
		if calls == 1 {
			instructions <- domain.Message{ID: "late-a", Role: "instruction", Text: "入口を説明して"}
			instructions <- domain.Message{ID: "late-b", Role: "instruction", Text: "保存先も説明して"}
		} else if !strings.Contains(input, "入口を説明して") || !strings.Contains(input, "保存先も説明して") {
			t.Fatal("late instructions not drained")
		}
		return Decision{Document: documentationResult()}, Usage{}, nil
	})}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s, Instructions: instructions}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil || calls != 2 || last.Documentation.Status != "ready" || len(last.Applied) != 2 {
		t.Fatalf("late instructions failed: %v calls=%d", err, calls)
	}
}

func TestDocumentationRecoveryDoesNotRetryAuthenticationOrCancellation(t *testing.T) {
	s := finalDocumentationSession()
	calls := 0
	r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{}, Usage{}, errors.New("gemini_key_invalid")
	})}
	if err := r.Run(context.Background(), Input{Session: s}, func(Checkpoint) error { return nil }); err == nil || err.Error() != "gemini_key_invalid" || calls != 1 {
		t.Fatalf("auth retried: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls = 0
	r.retryWait = func(context.Context, time.Duration) error { cancel(); return nil }
	r.AI = providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{}, Usage{}, errors.New("gemini_http_503")
	})
	if err := r.Run(ctx, Input{Session: s}, func(Checkpoint) error { return nil }); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation retried: %v calls=%d", err, calls)
	}
}

func TestDocumentationSavesCallReservationBeforeProvider(t *testing.T) {
	s := finalDocumentationSession()
	calls := 0
	savedCalls := 0
	r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		if savedCalls != s.Documentation.Calls+1 {
			t.Fatal("call not reserved durably")
		}
		return Decision{Document: documentationResult()}, Usage{}, nil
	})}
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { savedCalls = cp.Documentation.Calls; return nil }); err != nil || calls != 1 {
		t.Fatal(err)
	}
}

func TestDocumentationLastReservationStaysTerminalAfterDisconnect(t *testing.T) {
	s := finalDocumentationSession()
	s.Documentation.Calls = DocumentationMaxCalls - 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := Runner{AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		cancel()
		return Decision{}, Usage{}, ctx.Err()
	})}
	var last Checkpoint
	err := r.Run(ctx, Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil })
	if !errors.Is(err, context.Canceled) || last.Documentation.Calls != DocumentationMaxCalls || !strings.Contains(strings.Join(last.Documentation.Limitations, " "), "documentation_call_limit") {
		t.Fatalf("interrupted final reservation looked resumable: %v", err)
	}
}
