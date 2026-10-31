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

func batchFixture(count int) *reviewFixture {
	f := fixture()
	f.files = nil
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("pkg/service%d.go", i/2)
		if i%2 == 1 {
			name = strings.TrimSuffix(name, ".go") + "_test.go"
		}
		f.files = append(f.files, github.ChangedFile{Filename: name, Status: "modified", Patch: "@@ -1 +1 @@\n-old\n+new\n"})
	}
	return f
}

func TestReviewBatchesRelatedFilesAndCrossChecksTogether(t *testing.T) {
	diffCalls, crossCalls := 0, 0
	final := runFixture(t, batchFixture(12), providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
		var body struct {
			Task reviewTask `json:"reviewTask"`
		}
		if err := json.Unmarshal([]byte(input), &body); err != nil {
			t.Fatal(err)
		}
		d := result()
		if body.Task.Mode == "diff" {
			diffCalls++
			want := 10
			if diffCalls == 2 {
				want = 2
			}
			if len(body.Task.Patches) != want || !reviewPatchesFit(body.Task.Patches) || !reviewInputFits([]byte(input)) {
				t.Fatalf("unbounded or fragmented batch: %+v", body.Task)
			}
			for _, patch := range body.Task.Patches {
				d.Review.Findings = append(d.Review.Findings, finding(patch.Path, 1))
			}
		} else if body.Task.Mode == "cross" {
			crossCalls++
			if len(body.Task.Steps) != 2 {
				t.Fatal("cross review did not include both groups")
			}
		}
		return d, Usage{}, nil
	}))
	if diffCalls != 2 || crossCalls != 1 || len(final.Review.Findings) != 12 || final.Review.Phase != "done" {
		t.Fatalf("inefficient/incomplete review: diff=%d cross=%d findings=%d", diffCalls, crossCalls, len(final.Review.Findings))
	}
	for _, file := range reviewFiles(final.Review.Steps) {
		if !file.Done {
			t.Fatalf("file not committed: %s", file.Path)
		}
	}
}

func TestBatchFailureDoesNotCommitAnyFileAndCanResumeLegacyPlan(t *testing.T) {
	f := batchFixture(10)
	var saved Checkpoint
	interrupted := errors.New("connection lost")
	r := Runner{GitHub: f, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) { return result(), Usage{}, nil })}
	err := r.Run(context.Background(), Input{Session: reviewSession()}, func(cp Checkpoint) error {
		saved = cp
		if cp.Progress == "AIの利用量を保存しています" {
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) {
		t.Fatal(err)
	}
	for _, file := range reviewFiles(saved.Review.Steps) {
		if file.Done || file.Offset != 0 {
			t.Fatal("failed batch advanced a cursor")
		}
	}
	// Mimic an existing saved plan with two five-file groups and one completed
	// file. The model must receive only the nine unfinished files in one batch.
	files := saved.Review.Steps[0].Files
	files[0].Done, files[0].Offset = true, len(f.files[0].Patch)
	saved.Review.Steps = []domain.ReviewStep{{Directory: "pkg", Files: files[:5], State: "running"}, {Directory: "pkg", Files: files[5:], State: "pending"}}
	session := reviewSession()
	session.Review = saved.Review
	diffCalls := 0
	r.AI = providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
		var body struct {
			Task reviewTask `json:"reviewTask"`
		}
		json.Unmarshal([]byte(input), &body)
		if body.Task.Mode == "diff" {
			diffCalls++
			if len(body.Task.Patches) != 9 {
				t.Fatalf("legacy plan fragmented: %d", len(body.Task.Patches))
			}
			for _, patch := range body.Task.Patches {
				if patch.Path == f.files[0].Filename {
					t.Fatal("completed file reviewed again")
				}
			}
		}
		return result(), Usage{}, nil
	})
	if err := r.Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { saved = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if saved.Review.Phase != "done" || diffCalls != 1 {
		t.Fatal("legacy cursor did not resume")
	}
}

func TestReviewUsageBudgetStopsAfterSavingAcceptedBatch(t *testing.T) {
	for _, usage := range []Usage{{InputTokens: ReviewRunInputTokens}, {OutputTokens: ReviewRunOutputTokens}} {
		calls := 0
		var saved Checkpoint
		err := (&Runner{GitHub: fixture(), AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
			calls++
			return result(), usage, nil
		})}).Run(context.Background(), Input{Session: reviewSession()}, func(cp Checkpoint) error { saved = cp; return nil })
		if err == nil || err.Error() != "review_run_budget_resume" || calls != 1 || !saved.Review.Steps[0].Files[0].Done || saved.Review.Steps[1].Files[0].Done {
			t.Fatalf("usage budget lost cursor: %v calls=%d", err, calls)
		}
	}
}
