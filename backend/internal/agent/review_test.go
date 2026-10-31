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

type reviewFixture struct {
	files     []github.ChangedFile
	head      string
	reads     []string
	filePages []int
	onFiles   func()
}

func (f *reviewFixture) PullRequest(context.Context, string, domain.Target) (github.PullRequest, int, error) {
	pr := github.PullRequest{Title: "Update API and client", Body: "Sample", ChangedFiles: len(f.files)}
	pr.Base.SHA = strings.Repeat("b", 40)
	pr.Head.SHA = f.head
	return pr, 100, nil
}
func (f *reviewFixture) ReviewFiles(_ context.Context, _ string, _ domain.Target, page, perPage int) ([]github.ChangedFile, int, error) {
	if perPage != 1 {
		panic("unbounded patch page")
	}
	f.filePages = append(f.filePages, page)
	if f.onFiles != nil {
		f.onFiles()
	}
	return f.files[page-1 : page], 100, nil
}
func (f *reviewFixture) Read(_ context.Context, _ string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
	if target.Commit != strings.Repeat("a", 40) {
		return "", domain.Source{}, 0, errors.New("unpinned source")
	}
	f.reads = append(f.reads, read.Path)
	return "source line 1\nsource line 2", domain.Source{Label: read.Path, URL: "https://github.com/o/r/blob/" + target.Commit + "/" + read.Path}, 27, nil
}
func reviewSession() domain.Session {
	return domain.Session{Target: domain.Target{Repository: "o/r", Kind: "pull", Number: 42, Commit: strings.Repeat("c", 40)}, RunKind: "review", Messages: []domain.Message{{Role: "user", Text: "Review API compatibility"}}}
}
func fixture() *reviewFixture {
	return &reviewFixture{head: strings.Repeat("a", 40), files: []github.ChangedFile{
		{Filename: "api/server.go", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
		{Filename: "web/client.ts", Status: "modified", Patch: "@@ -1 +1 @@\n-old client\n+new client\n"},
	}}
}
func finding(p string, line int) domain.Finding {
	return domain.Finding{Severity: "P1", Path: p, Line: line, Side: "RIGHT", Title: "Contract mismatch", Problem: "client expects previous field", Impact: "request fails", Evidence: "server and client differ", Suggestion: "align the fields"}
}
func result() Decision {
	return Decision{Summary: "API contract summary", Review: &domain.ReviewResult{Findings: []domain.Finding{}, Unverified: []string{}}}
}
func runFixture(t *testing.T, f *reviewFixture, p Provider) Checkpoint {
	t.Helper()
	var final Checkpoint
	if err := (&Runner{AI: p, GitHub: f}).Run(context.Background(), Input{Session: reviewSession()}, func(cp Checkpoint) error { final = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	return final
}
func TestReviewSplitsLargePatchAndResumesSavedCursor(t *testing.T) {
	f := fixture()
	lines := strings.Repeat("+new content\n", 800)
	f.files[0].Patch = "@@ -0,0 +1,800 @@\n" + lines
	s := reviewSession()
	interrupted := errors.New("save connection lost")
	var saved Checkpoint
	calls := 0
	ai := providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
		if len(input) > domain.MaxInputBytes {
			t.Fatal("oversized input")
		}
		var body struct {
			Task reviewTask `json:"reviewTask"`
		}
		json.Unmarshal([]byte(input), &body)
		if body.Task.Mode == "diff" {
			if len(body.Task.Patch) > 3100 {
				t.Fatal("PR passed in one call")
			}
			if strings.Contains(body.Task.Patch, "new content") && strings.Contains(body.Task.Patch, "new client") {
				t.Fatal("whole PR in context")
			}
		}
		calls++
		return result(), Usage{InputTokens: 10}, nil
	})
	r := Runner{AI: ai, GitHub: f}
	err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error {
		if cp.Progress == "差分の区切りと次の取得位置を保存しました" && cp.Review.Steps[0].Files[0].Offset > 0 {
			saved = cp
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) || calls != 1 {
		t.Fatalf("checkpoint did not stop: %v calls=%d", err, calls)
	}
	offset := saved.Review.Steps[0].Files[0].Offset
	raw, _ := json.Marshal(saved.Review)
	if strings.Contains(string(raw), "new content") {
		t.Fatal("patch persisted")
	}
	s.Review = saved.Review
	s.Metrics = saved.Metrics
	var final Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { final = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if saved.Review.Steps[0].Files[0].Offset != offset {
		t.Fatal("saved snapshot mutated")
	}
	if final.Review.Phase != "done" || !final.Review.Steps[0].Files[0].Done || final.Answer == "" {
		t.Fatalf("did not finish: %+v", final)
	}
	if final.Metrics.InputTokens != calls*10 {
		t.Fatal("usage lost on resume")
	}
}
func TestCrossReviewRequiresTwoDistinctFreshSources(t *testing.T) {
	for _, sources := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(sources), func(t *testing.T) {
			f := fixture()
			final := runFixture(t, f, providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
				var body struct {
					Task     reviewTask `json:"reviewTask"`
					Evidence []evidence `json:"evidence"`
				}
				json.Unmarshal([]byte(input), &body)
				d := result()
				if body.Task.Mode == "cross" {
					if len(body.Evidence) < sources {
						p := "api/server.go"
						if len(body.Evidence) > 0 {
							p = "web/client.ts"
						}
						d.Read = &github.Read{Kind: "file", Path: p}
						d.Review = nil
					} else {
						d.Review.Findings = []domain.Finding{finding("api/server.go", 1)}
					}
				}
				return d, Usage{}, nil
			}))
			want := 0
			if sources == 2 {
				want = 1
			}
			if len(final.Review.Findings) != want {
				t.Fatalf("unsubstantiated cross finding: %+v", final.Review.Findings)
			}
		})
	}
}
func TestReviewRejectsInvalidLocationsAndDeduplicates(t *testing.T) {
	final := runFixture(t, fixture(), providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
		var body struct {
			Task reviewTask `json:"reviewTask"`
		}
		json.Unmarshal([]byte(input), &body)
		d := result()
		if body.Task.Mode == "diff" {
			f := finding(body.Task.Path, 1)
			d.Review.Findings = []domain.Finding{f, f, finding(body.Task.Path, 999), finding("outside.go", 1)}
		}
		return d, Usage{}, nil
	}))
	if len(final.Review.Findings) != 2 || len(final.Review.Unverified) < 3 {
		t.Fatalf("invalid findings accepted: %+v", final.Review)
	}
}
func TestReviewStopsWhenBaseOrHeadChanges(t *testing.T) {
	for _, duringPlanning := range []bool{true, false} {
		t.Run(fmt.Sprint(duringPlanning), func(t *testing.T) {
			f := fixture()
			calls := 0
			if duringPlanning {
				f.onFiles = func() { f.head = strings.Repeat("d", 40) }
			}
			r := Runner{GitHub: f, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				calls++
				f.head = strings.Repeat("d", 40)
				return result(), Usage{}, nil
			})}
			var saved Checkpoint
			err := r.Run(context.Background(), Input{Session: reviewSession()}, func(cp Checkpoint) error { saved = cp; return nil })
			if err == nil || err.Error() != "pr_changed_create_new_review" {
				t.Fatalf("mutable snapshot accepted: %v", err)
			}
			if duringPlanning && calls != 0 {
				t.Fatal("model saw changed PR")
			}
			if saved.Review != nil && len(saved.Review.Findings) > 0 {
				t.Fatal("stale findings saved")
			}
		})
	}
}
func TestReviewCheckpointFailurePreventsNextModelCall(t *testing.T) {
	calls := 0
	stop := errors.New("store failed")
	r := Runner{GitHub: fixture(), AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return result(), Usage{}, nil
	})}
	err := r.Run(context.Background(), Input{Session: reviewSession()}, func(cp Checkpoint) error {
		if cp.Review != nil {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || calls != 0 {
		t.Fatal("continued without durable plan")
	}
}
func TestReviewCredentialEchoAndExcludedFiles(t *testing.T) {
	f := fixture()
	f.files[0].Filename = "image.png"
	f.files[0].Patch = ""
	f.files[1].Patch = ""
	calls := 0
	final := runFixture(t, f, providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return result(), Usage{}, nil
	}))
	if final.Review.Steps[0].State != "partial" || len(final.Review.Unverified) < 3 {
		t.Fatal("excluded files not reported")
	}
	f = fixture()
	r := Runner{GitHub: f, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		d := result()
		d.Summary = "private-key"
		return d, Usage{}, nil
	})}
	err := r.Run(context.Background(), Input{Session: reviewSession(), Key: "private-key"}, func(cp Checkpoint) error {
		raw, _ := json.Marshal(cp)
		if strings.Contains(string(raw), "private-key") {
			t.Fatal("credential saved")
		}
		return nil
	})
	if err == nil || err.Error() != "credential_echo_blocked" {
		t.Fatalf("echo accepted: %v", err)
	}
}
func TestPatchFragmentsPreserveOriginalLineNumbers(t *testing.T) {
	patch := "@@ -100,2 +200,3 @@\n context\n-old\n+new\n+another\n"
	if !completePatch(patch) || completePatch(strings.TrimSuffix(patch, "+another\n")) {
		t.Fatal("incomplete patch detection failed")
	}
	offset := strings.Index(patch, "+new")
	l, r := patchCursor(patch[:offset])
	fragment := fmt.Sprintf("@@ -%d +%d @@\n", l, r) + patch[offset:]
	if !patchLocation(fragment, finding("a", 201)) || patchLocation(fragment, finding("a", 199)) {
		t.Fatal("fragment locations shifted")
	}
}

func TestReviewBudgetYieldsWithResumableCursor(t *testing.T) {
	f := fixture()
	f.files = f.files[:1]
	f.files[0].Patch = "@@ -0,0 +1,6000 @@\n" + strings.Repeat("+source line\n", 6000)
	calls := 0
	r := Runner{GitHub: f, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return result(), Usage{}, nil
	})}
	var saved Checkpoint
	err := r.Run(context.Background(), Input{Session: reviewSession()}, func(cp Checkpoint) error { saved = cp; return nil })
	if err == nil || err.Error() != "review_run_budget_resume" || calls != 20 || saved.Review.Steps[0].Files[0].Offset == 0 || saved.Answer != "" {
		t.Fatalf("budget did not yield safely: %v calls=%d", err, calls)
	}
	s := reviewSession()
	s.Review = saved.Review
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { saved = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if saved.Review.Phase != "done" {
		t.Fatal("budget cursor did not resume")
	}
}
func TestReviewAppliesLateDirectiveBeforeFinalResult(t *testing.T) {
	queue := make(chan domain.Message, 1)
	calls := 0
	saw := false
	r := Runner{GitHub: fixture(), AI: providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
		calls++
		if calls == 1 {
			queue <- domain.Message{ID: "late", Role: "instruction", Text: "Check authorization"}
		} else if strings.Contains(input, "Check authorization") {
			saw = true
		}
		return result(), Usage{}, nil
	})}
	var saved Checkpoint
	if err := r.Run(context.Background(), Input{Session: reviewSession(), Instructions: queue}, func(cp Checkpoint) error { saved = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if !saw || len(saved.Applied) != 1 || saved.Applied[0] != "late" {
		t.Fatal("late directive not applied before result")
	}
}
