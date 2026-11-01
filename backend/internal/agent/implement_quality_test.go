package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestVerificationInvestigatesUnchangedCodeBeforeClaimingCoverage(t *testing.T) {
	w := newFakeWriter()
	w.files["web/use.ts"] = "send({ locale: uiLanguage })"
	w.files["api/session.go"] = "session.Locale = request.Locale"
	s := verificationSession(w, []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "use(session.Locale)"}})
	s.Implementation.Plan.AcceptanceCriteria[0].Paths = []string{"src/a.go", "web/use.ts"}
	calls := 0
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		task := implementTaskOf(t, input)
		if task.ReadBudget < 1 {
			t.Fatal("verification cannot investigate")
		}
		switch calls {
		case 1:
			if !strings.Contains(input, `"action":"context"`) || !strings.Contains(input, "send({ locale: uiLanguage })") {
				t.Fatal("unchanged caller missing")
			}
			return Decision{Verification: &domain.ImplementationVerification{Unverified: []string{"APIでLocaleがSessionへ保存されるか未確認"}}}, Usage{}, nil
		case 2:
			if !strings.Contains(task.Correction, "read/search") {
				t.Fatal("unverified did not trigger investigation")
			}
			return Decision{Read: &github.Read{Kind: "file", Path: "api/session.go", Detail: "summary"}}, Usage{}, nil
		case 3:
			if !strings.Contains(input, "session.Locale = request.Locale") || task.ReadBudget != implementVerifyReads-1 {
				t.Fatal("additional code/budget missing")
			}
			v := passedVerification(task.Plan)
			v.Criteria[0].Paths = append(v.Criteria[0].Paths, "api/session.go")
			return Decision{Summary: "UIからAPI、Session、利用箇所まで確認", Verification: v}, Usage{}, nil
		}
		t.Fatal("unbounded verification")
		return Decision{}, Usage{}, nil
	})}
	var last Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || last.Implementation.Status != "ready" || !last.Implementation.CanPublish() || len(w.published) != 0 {
		t.Fatalf("incomplete check: %+v", last.Implementation)
	}
}

func TestVerificationCannotClearUncertaintyWithoutSuccessfulRead(t *testing.T) {
	for _, readMissing := range []bool{false, true} {
		t.Run(fmt.Sprint(readMissing), func(t *testing.T) {
			w := newFakeWriter()
			s := verificationSession(w, []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "new code"}})
			calls := 0
			r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				calls++
				task := implementTaskOf(t, input)
				if calls == 1 {
					return Decision{Verification: &domain.ImplementationVerification{Unverified: []string{"Localeの伝達を未確認"}}}, Usage{}, nil
				}
				if calls == 2 && readMissing {
					return Decision{Read: &github.Read{Kind: "file", Path: "missing.go", Detail: "summary"}}, Usage{}, nil
				}
				return Decision{Summary: "確認", Verification: passedVerification(task.Plan)}, Usage{}, nil
			})}
			var last Checkpoint
			if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
				t.Fatal(err)
			}
			if last.Implementation.Status != "verification_blocked" || last.Implementation.CanPublish() || !strings.Contains(strings.Join(last.Implementation.Verification.Unverified, ""), "Locale") {
				t.Fatalf("uncertainty disappeared: %+v", last.Implementation)
			}
		})
	}
}

func TestVerificationBlocksMissingUnmetAndInventedEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "unmet", "unverified", "invented"} {
		t.Run(mode, func(t *testing.T) {
			w := newFakeWriter()
			s := verificationSession(w, []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "new code"}})
			r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				v := passedVerification(implementTaskOf(t, input).Plan)
				switch mode {
				case "missing":
					v.Criteria = nil
				case "unmet", "unverified":
					v.Criteria[0].Status = mode
				case "invented":
					v.Criteria[0].Paths = []string{"unread.go"}
				}
				return Decision{Summary: "確認", Verification: v}, Usage{}, nil
			})}
			var last Checkpoint
			if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
				t.Fatal(err)
			}
			if last.Implementation.Status != "verification_blocked" || last.Implementation.CanPublish() || len(w.published) > 0 {
				t.Fatalf("false completion: %+v", last.Implementation)
			}
		})
	}
}

func TestCIFailureCoverageAndPinnedEvidence(t *testing.T) {
	run := &implementRun{ciEvidence: []evidence{{Read: github.Read{Kind: "job_log", Number: 42}, Content: "[failure L10] FAIL TestLocale\n[failure L80] FAIL TestReason"}}}
	p := simplePlan()
	p.AcceptanceCriteria[0].FailureRefs = []string{"42:L10"}
	if run.validateCIPlanCoverage(p) == nil {
		t.Fatal("unmapped CI failure accepted")
	}
	p.AcceptanceCriteria[0].FailureRefs = append(p.AcceptanceCriteria[0].FailureRefs, "42:L80")
	if err := run.validateCIPlanCoverage(p); err != nil {
		t.Fatal(err)
	}
	// Optional evidence has already been evicted, but CI facts still reach the
	// model at every stage through a separate, non-sheddable input field.
	run.r = &Runner{AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		if !strings.Contains(input, "TestLocale") || !strings.Contains(input, "TestReason") || !strings.Contains(input, "ciEvidence") {
			t.Fatal("CI failures evicted")
		}
		return Decision{}, Usage{}, nil
	})}
	run.impl = &domain.Implementation{}
	run.logger = slog.Default()
	if _, err := run.call(context.Background(), map[string]any{"implementTask": implementTask{Mode: "edit"}, "evidence": []evidence{}}); err != nil {
		t.Fatal(err)
	}
}

func TestCollectCIFailuresIncludesEveryJob(t *testing.T) {
	w := newFakeWriter()
	s := baseSession()
	jobs := 0
	r := &Runner{Writer: w, GitHub: readerFunc(func(_ context.Context, _ string, _ domain.Target, read github.Read) (string, domain.Source, int, error) {
		if read.Kind == "check_runs" {
			checks := []map[string]any{}
			for i := 1; i <= 5; i++ {
				checks = append(checks, map[string]any{"job_id": i, "name": fmt.Sprintf("job%d", i), "conclusion": "failure"})
			}
			b, _ := json.Marshal(map[string]any{"check_runs": checks})
			return string(b), domain.Source{}, len(b), nil
		}
		jobs++
		return fmt.Sprintf("[failure L1] FAIL test%d", read.Number), domain.Source{}, 32, nil
	})}
	run := &implementRun{r: r, in: Input{Session: s}, impl: &domain.Implementation{BaseCommit: implBase}, logger: slog.Default()}
	ev, err := run.collectCIFailureLogs(context.Background(), 7)
	if err != nil || jobs != 5 || len(ev) != 6 {
		t.Fatalf("jobs omitted: %d %+v %v", jobs, ev, err)
	}
}

func TestVerificationSearchOverlaysGeneratedAndDeletedFiles(t *testing.T) {
	w := newFakeWriter()
	wrapper := searchWriter{fakeWriter: w, search: func(context.Context, string, string, string, github.CodeSearch) (github.CodeSearchResult, error) {
		return github.CodeSearchResult{Matches: []github.SearchMatch{{Path: "src/a.go", Text: "Thing stale"}, {Path: "src/old.go", Text: "Thing deleted"}, {Path: "web/use.ts", Text: "Thing caller"}}}, nil
	}}
	s := verificationSession(w, []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "Thing current"}, {Path: "src/new.go", Action: "create", Content: "Thing new"}, {Path: "src/old.go", Action: "delete"}})
	run := &implementRun{r: &Runner{Writer: wrapper}, impl: s.Implementation, in: Input{Session: s}, logger: slog.Default(), emit: func(Checkpoint) error { return nil }}
	ev := []evidence{}
	if err := run.search(context.Background(), github.CodeSearch{Query: "Thing"}, &ev); err != nil {
		t.Fatal(err)
	}
	var got github.CodeSearchResult
	if err := json.Unmarshal([]byte(ev[0].Content), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Matches) != 1 || got.Matches[0].Path != "web/use.ts" || len(got.GeneratedMatches) != 2 || strings.Contains(ev[0].Content, "stale") || strings.Contains(ev[0].Content, "deleted") {
		t.Fatalf("stale code search: %+v", got)
	}
}

func TestVerificationReceivesOriginalAssertionsEvenBeyondCompactDiff(t *testing.T) {
	w := newFakeWriter()
	old := strings.Repeat("// unchanged test padding\n", 1000) + "require.NotEmpty(priority.Reason)\n"
	w.files["src/priority_test.go"] = old
	s := verificationSession(w, []domain.FileChange{{Path: "src/priority_test.go", Action: "update", Content: strings.Repeat("// changed test padding\n", 1000)}})
	previous := ""
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		var body struct {
			Task  implementTask      `json:"implementTask"`
			Parts []verificationPart `json:"parts"`
		}
		_ = json.Unmarshal([]byte(input), &body)
		for _, p := range body.Parts {
			if p.Action == "before" {
				previous += p.Content
			}
		}
		return Decision{Summary: "compare assertions", Verification: passedVerification(body.Task.Plan)}, Usage{}, nil
	})}
	if err := r.Run(context.Background(), Input{Session: s}, func(Checkpoint) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if previous != old {
		t.Fatal("original assertion omitted from verification")
	}
}

func TestRevisionRetainsAcceptanceConditions(t *testing.T) {
	prior := simplePlan()
	prior.AcceptanceCriteria[0].ID = "original"
	run := &implementRun{impl: &domain.Implementation{Revising: true, Plan: prior}}
	proposed := simplePlan()
	proposed.AcceptanceCriteria[0].ID = "follow-up"
	merged, _, err := run.mergeDraftPlan(proposed)
	if err != nil || len(merged.AcceptanceCriteria) != 2 || merged.AcceptanceCriteria[1].ID != "original" {
		t.Fatalf("requirements dropped: %+v %v", merged, err)
	}
}
