package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTargetBoundary(t *testing.T) {
	for _, value := range []string{"team/repo", "https://github.com/team/repo", "https://github.com/team/repo/pull/42", "https://github.com/team/repo/issues/7"} {
		if _, err := ParseTarget(value); err != nil {
			t.Errorf("valid target %q: %v", value, err)
		}
	}
	for _, value := range []string{"https://evil.test/team/repo", "https://github.com.evil.test/a/b", "https://user@github.com/a/b", "https://github.com/a/b/pull/0", "https://github.com/a/b/pull/-1", "https://github.com/a/b/pull/9999999999999999999999", "https://github.com/a/b?token=secret", "a/../b", "a/b/actions", "https://github.com/a/b/pull/"} {
		if _, err := ParseTarget(value); err == nil {
			t.Errorf("unsafe target accepted %q", value)
		}
	}
}
func TestClipAndHistoryLimit(t *testing.T) {
	if !utf8.ValidString(Clip("日本語", 5)) {
		t.Fatal("invalid UTF-8")
	}
	s := Session{Locale: "ja"}
	if err := s.Add("user", strings.Repeat("a", MaxHistoryBytes), "first"); err != nil {
		t.Fatal(err)
	}
	if s.Add("user", "more", "second") == nil {
		t.Fatal("unbounded history")
	}
}

func TestReviewUnfinishedIgnoresLaterRunKinds(t *testing.T) {
	s := Session{Target: Target{Kind: "pull", Number: 1}, RunKind: "investigation", State: "done", Review: &Review{Phase: "diff"}}
	if !s.ReviewUnfinished() {
		t.Fatal("interrupted review hidden by investigation")
	}
	s.Review.Phase = "done"
	if s.ReviewUnfinished() {
		t.Fatal("completed review reported unfinished")
	}
	s.Review = nil
	if s.ReviewUnfinished() {
		t.Fatal("missing review reported unfinished")
	}
}
