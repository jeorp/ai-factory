package agent

import (
	"ai-factory/backend/internal/domain"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSavePrioritiesPreservesUnassessedItems(t *testing.T) {
	r := &domain.Review{Unverified: []string{"a", "b", "c", "d"}}
	items := []priorityItem{{0, "a"}, {1, "b"}, {2, "c"}, {3, "d"}}
	savePriorities(r, items, `[{"index":0,"level":"high","reason":"重要な機能が未確認"},{"index":1,"level":"low","reason":"影響が限定的"},{"index":1,"level":"high","reason":"重複"},{"index":2,"level":"bad","reason":"不正"}]`)
	if r.Priorities["0"].Level != "high" || r.Priorities["0"].Reason != "重要な機能が未確認" {
		t.Fatalf("valid priority lost: %+v", r.Priorities)
	}
	for _, index := range []string{"1", "2", "3"} {
		if r.Priorities[index].Level != "unknown" {
			t.Fatalf("invalid or missing priority accepted: %+v", r.Priorities)
		}
	}
	if len(r.Unverified) != 4 {
		t.Fatal("unverified items were removed")
	}
}

func TestReviewPrioritizesBeforeCompletionAndResumes(t *testing.T) {
	f := fixture()
	s := reviewSession()
	r := Runner{GitHub: f, AI: providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
		if strings.Contains(input, `"priorityTask":`) {
			return Decision{Answer: `[{"index":0,"level":"high","reason":"影響が大きい"}]`}, Usage{InputTokens: 3}, nil
		}
		d := result()
		d.Review.Unverified = []string{"重要な条件を確認できない"}
		return d, Usage{}, nil
	})}
	var saved Checkpoint
	interrupted := errors.New("interrupted")
	err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error {
		if cp.Progress == "未確認項目の優先度を確認しています" {
			saved = cp
			return interrupted
		}
		return nil
	})
	if !errors.Is(err, interrupted) || saved.Review.Phase != "priority" || saved.Review.PriorityCursor != 0 {
		t.Fatalf("priority phase not checkpointed: %v %+v", err, saved.Review)
	}
	s.Review = saved.Review
	s.Metrics = saved.Metrics
	var final Checkpoint
	if err = r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { final = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if final.Review.Phase != "done" || final.Review.Priorities["0"].Level != "high" || final.Review.PriorityCursor != len(final.Review.Unverified) {
		t.Fatalf("priority was not persisted: %+v", final.Review)
	}
}
