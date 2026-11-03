package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func skipRetryWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

func TestRetryWaitKeepsBackoffScheduleAndCancellation(t *testing.T) {
	w := newFakeWriter()
	s := recoverySession(w, "verify")
	var delays []time.Duration
	calls := 0
	r := &Runner{Writer: w, retryWait: func(ctx context.Context, delay time.Duration) error { delays = append(delays, delay); return ctx.Err() }, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		if calls < 3 {
			return Decision{}, Usage{}, errors.New("gemini_http_503")
		}
		return recoveryDecision(t, input), Usage{}, nil
	})}
	if err := r.Run(context.Background(), Input{Session: s}, func(Checkpoint) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 2 || delays[0] < time.Second || delays[0] >= 1250*time.Millisecond || delays[1] < 2*time.Second || delays[1] >= 2250*time.Millisecond {
		t.Fatalf("wrong backoff schedule: %v", delays)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if err := (&Runner{}).waitModelRetry(ctx, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("real timer ignored cancellation: %v", err)
	}
	ctx2, stop := context.WithCancel(context.Background())
	r.retryWait = func(context.Context, time.Duration) error { stop(); return nil }
	if err := r.waitModelRetry(ctx2, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("injected wait lost cancellation: %v", err)
	}
}

func TestSourcePathEvidenceDoesNotUsePartialPathMatches(t *testing.T) {
	for _, text := range []string{"src/a.go.old is unverified", "nested/src/a.go is unverified", "prefixsrc/a.go is unverified"} {
		if sourcePathMentioned(text, "src/a.go") {
			t.Fatalf("partial path matched: %s", text)
		}
	}
	if !sourcePathMentioned("`src/a.go`の挙動を未確認", "src/a.go") {
		t.Fatal("explicit source reference missed")
	}
}

func recoverySession(w *fakeWriter, phase string) domain.Session {
	s := verificationSession(w, []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "package src\nfunc A() int { return 2 }\n"}})
	if phase == "plan" {
		s.Implementation.Status = "planning"
		s.Implementation.Plan = nil
		s.Implementation.Changes = nil
	}
	if phase == "edit" {
		s.Implementation.Status = "editing"
		s.Implementation.NextStep = 0
		s.Implementation.Changes = nil
	}
	return s
}

func recoveryDecision(t *testing.T, input string) Decision {
	task := implementTaskOf(t, input)
	switch task.Mode {
	case "plan":
		return Decision{Plan: &domain.ImplementationPlan{Summary: "Fix A", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "return 2"}}, AcceptanceCriteria: testCriteria("src/a.go")}}
	case "edit":
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}}}
	default:
		return Decision{Summary: "checked", Verification: passedVerification(task.Plan)}
	}
}

func TestRecoverTransientModelErrorsInEveryImplementationPhase(t *testing.T) {
	for _, phase := range []string{"plan", "edit", "verify"} {
		for _, code := range []string{"gemini_http_503", "openai_http_429", "claude_unavailable"} {
			t.Run(phase+"/"+code, func(t *testing.T) {
				t.Parallel()
				w := newFakeWriter()
				s := recoverySession(w, phase)
				calls := 0
				r := &Runner{retryWait: skipRetryWait, Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
					calls++
					if calls == 1 {
						return Decision{}, Usage{}, errors.New(code)
					}
					return recoveryDecision(t, input), Usage{}, nil
				})}
				var last Checkpoint
				if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
					t.Fatal(err)
				}
				want := "ready"
				if phase == "plan" {
					want = "plan_ready"
				}
				if last.Implementation.Status != want || calls < 2 || len(w.published) != 0 {
					t.Fatalf("not recovered: calls=%d %+v", calls, last.Implementation)
				}
			})
		}
	}
}

func TestRecoverInvalidVerificationJSONForEveryProvider(t *testing.T) {
	for _, code := range []string{"gemini_invalid_output", "openai_invalid_output", "claude_invalid_output"} {
		w := newFakeWriter()
		s := recoverySession(w, "verify")
		calls := 0
		r := &Runner{retryWait: skipRetryWait, Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
			calls++
			if calls <= 2 {
				return Decision{}, Usage{}, errors.New(code)
			}
			if !strings.Contains(implementTaskOf(t, input).Correction, "verification") {
				t.Fatal("verification correction missing")
			}
			return recoveryDecision(t, input), Usage{}, nil
		})}
		var last Checkpoint
		if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil || calls != 3 || last.Implementation.Status != "ready" {
			t.Fatalf("%s: calls=%d err=%v", code, calls, err)
		}
	}
}

func TestModelRecoveryStopsAtBoundaries(t *testing.T) {
	for _, stop := range []string{"cancel", "checkpoint", "retry-limit", "phase-budget", "global-budget", "auth", "refusal", "expired-cache", "malformed-limit"} {
		t.Run(stop, func(t *testing.T) {
			w := newFakeWriter()
			s := recoverySession(w, "verify")
			calls := 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			code := "gemini_http_503"
			switch stop {
			case "auth":
				code = "gemini_key_invalid"
			case "refusal":
				code = "claude_refusal"
			case "expired-cache":
				code = "implementation_changes_expired"
			case "malformed-limit":
				code = "gemini_invalid_output"
			}
			if stop == "global-budget" || stop == "phase-budget" {
				run := &implementRun{calls: ImplementMaxCalls}
				attempt := 1
				if stop == "phase-budget" {
					run.calls = 0
					attempt = ImplementStepCalls
				}
				n, retries := 0, 0
				retry, err := run.retryTransientModel(ctx, "edit", attempt, ImplementStepCalls, &n, &retries, errors.New(code))
				if retry || err != nil || retries != 0 {
					t.Fatal("retried past budget")
				}
				return
			}
			r := &Runner{retryWait: skipRetryWait, Writer: w, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				calls++
				return Decision{}, Usage{}, errors.New(code)
			})}
			err := r.Run(ctx, Input{Session: s}, func(cp Checkpoint) error {
				if strings.Contains(cp.Progress, "通信エラー") {
					if stop == "cancel" {
						cancel()
					}
					if stop == "checkpoint" {
						return errors.New("checkpoint_failed")
					}
				}
				return nil
			})
			want := 1
			if stop == "retry-limit" || stop == "malformed-limit" {
				want = 3
			}
			if err == nil || calls != want || len(w.published) != 0 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if stop == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if stop == "checkpoint" && err.Error() != "checkpoint_failed" {
				t.Fatal(err)
			}
		})
	}
}

func TestSearchEvidenceCanResolveUncertaintyWithoutFakingCoverage(t *testing.T) {
	for _, scenario := range []string{"fresh", "duplicate", "no-match", "partial-coverage", "unrelated", "explicit-path", "multiple-unknowns"} {
		t.Run(scenario, func(t *testing.T) {
			base := newFakeWriter()
			base.files["src/caller.go"] = "package src\nvar X = A()\n"
			searches := 0
			w := searchWriter{fakeWriter: base, search: func(context.Context, string, string, string, github.CodeSearch) (github.CodeSearchResult, error) {
				searches++
				return github.CodeSearchResult{ScannedBytes: searches * 100, Matches: []github.SearchMatch{{Path: "src/caller.go", Line: 2, Text: "var X = A()"}}}, nil
			}}
			if scenario == "no-match" {
				w.search = func(context.Context, string, string, string, github.CodeSearch) (github.CodeSearchResult, error) {
					return github.CodeSearchResult{}, nil
				}
			}
			s := recoverySession(base, "verify")
			if scenario == "explicit-path" {
				s.Implementation.Plan.AcceptanceCriteria[0].Paths = append(s.Implementation.Plan.AcceptanceCriteria[0].Paths, "src/caller.go")
			}
			calls := 0
			search := Decision{Search: &github.CodeSearch{Query: "X", Prefix: "src/caller.go"}}
			r := &Runner{retryWait: skipRetryWait, Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				calls++
				if scenario == "duplicate" && calls == 1 {
					return search, Usage{}, nil
				}
				n := calls
				if scenario == "duplicate" {
					n--
				}
				if n == 1 {
					v := passedVerification(implementTaskOf(t, input).Plan)
					v.Unverified = []string{"src/caller.go: caller behavior unknown"}
					if scenario == "unrelated" {
						v.Unverified = []string{"required behavior unknown"}
					}
					if scenario == "explicit-path" {
						v.Unverified = []string{"src/a.go: return behavior unknown"}
					}
					if scenario == "multiple-unknowns" {
						v.Unverified = append(v.Unverified, "src/a.go: return behavior unknown")
					}
					return Decision{Verification: v}, Usage{}, nil
				}
				if n == 2 {
					return search, Usage{}, nil
				}
				d := recoveryDecision(t, input)
				if scenario == "partial-coverage" {
					d.Verification.Criteria[0].Paths = append(d.Verification.Criteria[0].Paths, "src/caller.go")
				}
				return d, Usage{}, nil
			})}
			var last Checkpoint
			if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
				t.Fatal(err)
			}
			want := "verification_blocked"
			if scenario == "fresh" {
				want = "ready"
			}
			if last.Implementation.Status != want {
				t.Fatalf("%s %+v", scenario, last.Implementation)
			}
		})
	}
}
