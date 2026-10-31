package domain

import (
	"strings"
	"testing"
)

func TestCompactDiffShowsChangedLinesWithContext(t *testing.T) {
	before := "a\nb\nc\nd\ne\nf\ng\nh\ni\n"
	after := "a\nb\nc\nD\ne\nf\ng\nh\ni\nj\n"
	got := CompactDiff(before, after, 1000)
	want := " b\n c\n-d\n+D\n e\n f\n...\n h\n i\n+j\n"
	if got != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", got, want)
	}
	if d := CompactDiff("", "x\ny\n", 1000); d != "+x\n+y\n" {
		t.Fatalf("create diff: %q", d)
	}
	if d := CompactDiff(strings.Repeat("x\n", 5000), strings.Repeat("y\n", 5000), 100); len(d) > 100+len("…") || !strings.HasPrefix(d, "-x") {
		t.Fatalf("large diff not bounded: %d", len(d))
	}
}
