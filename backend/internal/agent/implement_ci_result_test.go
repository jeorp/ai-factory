package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestPublishedCIBelongsToExactCommitAndDoesNotClaimUnknownSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, status, conclusion, head, want string
		total                                int
		truncated                            bool
	}{
		{"passed", "completed", "success", implBase, "passed", 1, false},
		{"failed", "completed", "failure", implBase, "failed", 1, false},
		{"pending", "in_progress", "", implBase, "pending", 1, false},
		{"skipped", "completed", "skipped", implBase, "unverified", 1, false},
		{"stale", "completed", "success", "wrong-head", "unverified", 1, false},
		{"unknown head", "completed", "success", "", "unverified", 1, false},
		{"partial", "completed", "success", implBase, "unverified", 2, true},
		{"no checks", "", "", implBase, "pending", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newFakeWriter()
			impl := &domain.Implementation{Status: "checking_ci", Commit: implBase, PullNumber: 7, PullURL: "https://github.com/a/b/pull/7"}
			reads := 0
			r := &Runner{Writer: w, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				t.Fatal("CI check called a model")
				return Decision{}, Usage{}, nil
			}), GitHub: readerFunc(func(_ context.Context, _ string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
				reads++
				if target.Commit != impl.Commit || read.Kind != "check_runs" || read.Number != 7 {
					t.Fatalf("wrong CI commit: %+v %+v", target, read)
				}
				checks := []map[string]any{}
				if tc.total > 0 {
					checks = append(checks, map[string]any{"name": "test", "status": tc.status, "conclusion": tc.conclusion, "head_sha": tc.head})
				}
				raw, _ := json.Marshal(map[string]any{"total_count": tc.total, "truncated": tc.truncated, "check_runs": checks})
				return string(raw), domain.Source{}, len(raw), nil
			})}
			var last Checkpoint
			if err := runImplementationFixture(r, impl, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
				t.Fatal(err)
			}
			got := last.Implementation
			if reads != 1 || got.Status != "published" || got.CI == nil || got.CI.Commit != impl.Commit || got.CI.State != tc.want || len(w.published) > 0 {
				t.Fatalf("false CI result: %+v", got)
			}
		})
	}
}

func TestCIInterruptedCheckpointResumesWithoutPublishingAgain(t *testing.T) {
	w := newFakeWriter()
	impl := &domain.Implementation{Status: "checking_ci", Commit: implBase, PullNumber: 7}
	reads := 0
	r := &Runner{Writer: w, GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		reads++
		return `{"check_runs":[]}`, domain.Source{}, 0, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := baseSession()
	s.RunKind = "implement"
	s.Implementation = impl
	if err := r.Run(ctx, Input{Session: s}, func(Checkpoint) error { return nil }); err == nil || reads > 0 {
		t.Fatal("canceled CI check continued")
	}
	if err := runImplementationFixture(r, impl, func(Checkpoint) error { return nil }); err != nil || reads != 1 || len(w.published) > 0 {
		t.Fatalf("CI resume republished: %v", err)
	}
}

func TestCIFixPreservesMaximumLengthOriginalRequest(t *testing.T) {
	original := strings.Repeat("a", 4080) + "LAST_REQUIREMENT"
	prior := &domain.Implementation{Status: "published", Request: original, Commit: implBase, PullNumber: 7, Branch: "ai-factory/x", Plan: simplePlan()}
	next := StartCIFix(prior, "next-ci-cache")
	if next.Request != original || !next.CIRepair || len(next.RequiredCriteria) != 1 || next.ExpectedHead != implBase {
		t.Fatalf("CI repair clipped requirements: %+v", next)
	}
}
