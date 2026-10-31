package agent

import (
	"context"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestOverviewSavedOnceAndIncludedInContext(t *testing.T) {
	s := baseSession()
	s.OverviewPending = true
	reads, calls := 0, 0
	r := Runner{GitHub: readerFunc(func(_ context.Context, _ string, _ domain.Target, read github.Read) (string, domain.Source, int, error) {
		reads++
		if read.Path != "README.md" {
			t.Fatalf("unexpected read: %+v", read)
		}
		return "backend: API\n" + strings.Repeat("x", 9000), domain.Source{URL: "https://github.com/a/b/blob/sha/README.md"}, 9000, nil
	}), AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		if strings.Contains(input, `"overviewTask"`) {
			if len(input) > 9000 {
				t.Fatal("README input is not bounded")
			}
			return Decision{Summary: "backend: API"}, Usage{InputTokens: 10}, nil
		}
		if !strings.Contains(input, "backend: API") || !strings.Contains(input, s.Target.Commit) {
			t.Fatal("overview missing from prompt")
		}
		return Decision{Answer: "done"}, Usage{}, nil
	})}
	var saved Checkpoint
	emit := func(cp Checkpoint) error { saved = cp; return nil }
	if err := r.Run(context.Background(), Input{Session: s}, emit); err != nil {
		t.Fatal(err)
	}
	if saved.RepositoryOverview == nil || !saved.RepositoryOverview.Truncated || saved.RepositoryOverview.Text != "backend: API" || saved.OverviewPending {
		t.Fatalf("bad overview: %+v", saved)
	}
	s.RepositoryOverview, s.OverviewPending = saved.RepositoryOverview, saved.OverviewPending
	if err := r.Run(context.Background(), Input{Session: s}, emit); err != nil {
		t.Fatal(err)
	}
	if reads != 1 || calls != 3 {
		t.Fatalf("overview was not reused: reads=%d calls=%d", reads, calls)
	}
}
