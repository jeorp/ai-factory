package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestReviewDropsPositiveAssessmentButKeepsRealBugs(t *testing.T) {
	for _, problem := range []string{"問題なしと判定されるが、権限チェックを迂回できる", "問題なし。ただし権限チェックを迂回できる", "No problem. However, authentication is bypassed"} {
		if nonIssueFinding(domain.Finding{Problem: problem}) {
			t.Fatalf("real issue would be filtered: %s", problem)
		}
	}
	final := runFixture(t, fixture(), providerFunc(func(_ context.Context, _, _ string, input string) (Decision, Usage, error) {
		var body struct {
			Task reviewTask `json:"reviewTask"`
		}
		_ = json.Unmarshal([]byte(input), &body)
		d := result()
		if body.Task.Mode == "diff" {
			good := finding(body.Task.Patches[0].Path, 1)
			good.Problem = "特になし（適切なバリデーションが実装されている）"
			good.Suggestion = "現状のままで問題ない"
			bug := finding(body.Task.Patches[0].Path, 1)
			bug.Problem = "問題なしと判定されるが、権限チェックを迂回できる"
			d.Review.Findings = []domain.Finding{good, bug}
		}
		return d, Usage{}, nil
	}))
	if len(final.Review.Findings) != 2 {
		t.Fatalf("positive assessment counted or real issue dropped: %+v", final.Review.Findings)
	}
	for _, item := range final.Review.Findings {
		if item.Suggestion == "現状のままで問題ない" {
			t.Fatal("non-issue persisted")
		}
	}
}

func TestReviewScopeExcludesDiffAndCrossInputsAndSurvivesResume(t *testing.T) {
	f := fixture()
	f.files[0].Filename, f.files[1].Filename = "backend/server.go", "frontend/client.ts"
	f.files = append(f.files, github.ChangedFile{Filename: "compose.yaml", Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"})
	s := reviewSession()
	s.RunKind = "chat"
	s.Messages = []domain.Message{{Role: "user", Text: "このPRレビューして。バックエンドだけでいいよ"}}
	diffs, cross := 0, 0
	r := Runner{GitHub: f, AI: providerFunc(func(_ context.Context, _, _ string, input string) (Decision, Usage, error) {
		var body struct {
			Task  *reviewTask    `json:"reviewTask"`
			Items []priorityItem `json:"priorityTask"`
		}
		_ = json.Unmarshal([]byte(input), &body)
		if body.Task == nil && len(body.Items) == 0 {
			return Decision{StartReview: &ReviewRequest{Number: 42, Scope: "backend"}}, Usage{}, nil
		}
		if body.Task != nil {
			if strings.Contains(input, "frontend/client.ts") {
				t.Fatal("excluded file sent to AI")
			}
			if body.Task.Mode == "diff" {
				diffs++
			} else {
				cross++
			}
		}
		return result(), Usage{}, nil
	})}
	stop := errors.New("disconnect")
	var saved Checkpoint
	err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error {
		saved = cp
		if cp.ResetReview && cp.ReviewScope != "backend" {
			t.Fatal("scope missing from durable handoff")
		}
		if cp.Progress == "レビュー計画を保存しました" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || saved.Review.Scope != "backend" {
		t.Fatalf("scope not checkpointed: %v", err)
	}
	s.RunKind, s.Review = "review", saved.Review
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { saved = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if diffs != 2 || cross != 1 || !saved.Review.Steps[1].Files[0].Excluded || saved.Review.Steps[1].State != "excluded" {
		t.Fatalf("scope not enforced: calls %d/%d, %+v", diffs, cross, saved.Review)
	}
	for _, page := range f.filePages[len(f.files):] {
		if page == 2 {
			t.Fatal("excluded patch fetched again after planning")
		}
	}
	if excludedReviewFile("backend", "frontend/moved.go", "backend/moved.go") {
		t.Fatal("cross-scope rename excluded")
	}
}

func TestAllExcludedReviewSkipsCrossCall(t *testing.T) {
	f := fixture()
	f.files = f.files[:1]
	f.files[0].Filename = "frontend/app.ts"
	s := reviewSession()
	s.ReviewScope = "backend"
	calls := 0
	r := Runner{GitHub: f, AI: providerFunc(func(_ context.Context, _, _ string, input string) (Decision, Usage, error) {
		calls++
		if strings.Contains(input, `"reviewTask"`) {
			t.Fatal("all excluded review still sent to AI")
		}
		return Decision{}, Usage{}, nil
	})}
	var final Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { final = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || final.Review.Phase != "done" {
		t.Fatalf("unexpected calls/state: %d %+v", calls, final.Review)
	}
}
