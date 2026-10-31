package httpapi

import (
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func TestFileTargetCommitScope(t *testing.T) {
	base, head, current, earlier := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40), strings.Repeat("d", 40)
	session := &domain.Session{Target: domain.Target{Repository: "owner/repo", Commit: current}, Review: &domain.Review{Base: base, Head: head}, Sources: []domain.Source{{URL: "https://github.com/owner/repo/blob/" + earlier + "/src/file.go"}}}
	for _, commit := range []string{"", base, head, current, earlier} {
		target, err := fileTarget(session, commit)
		if commit == "" {
			commit = current
		}
		if err != nil || target.Commit != commit || target.Repository != session.Target.Repository {
			t.Fatalf("lost pinned target: %+v %v", target, err)
		}
	}
	for _, source := range []string{"https://github.com/other/repo/blob/", "https://github.com/owner/repository/blob/", "https://github.com.evil.test/owner/repo/blob/", "https://github.com@evil.test/owner/repo/blob/", "http://github.com/owner/repo/blob/"} {
		session.Sources = []domain.Source{{URL: source + earlier + "/src/file.go"}}
		if _, err := fileTarget(session, earlier); err == nil {
			t.Fatalf("untrusted source accepted: %s", source)
		}
	}
	for _, commit := range []string{"main", strings.Repeat("e", 40), head + "/../main"} {
		if _, err := fileTarget(session, commit); err == nil {
			t.Fatalf("arbitrary commit accepted: %s", commit)
		}
	}
}
