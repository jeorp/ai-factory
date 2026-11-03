package httpapi

import (
	"ai-factory/backend/internal/domain"
	"strings"
	"testing"
)

func TestDocumentationCommandsValidateScopeAndFeature(t *testing.T) {
	for _, test := range []struct{ scope, text, want string }{
		{"feature", "", "documentation_request_required"}, {"other", "login", "invalid_documentation_scope"}, {"feature", "login", ""}, {"repository", "", ""}, {"", "login", ""},
	} {
		cmd := command{Type: "documentation", Text: test.text, DocumentScope: test.scope}
		if got := documentationCommandProblem(&cmd); got != test.want {
			t.Fatalf("scope=%s got %s want %s", test.scope, got, test.want)
		}
		if test.want == "" && (cmd.Text == "" || !cmd.carriesMessage()) {
			t.Fatal("missing durable request")
		}
	}
}

func TestDocumentationCommandPinsCurrentTargetAndResumeKeepsResearch(t *testing.T) {
	s := &domain.Session{Target: domain.Target{Repository: "a/b", Commit: strings.Repeat("a", 40)}}
	applyDocumentationCommand(s, command{Type: "documentation", Text: "ログイン機能", DocumentScope: "feature"})
	if s.RunKind != "documentation" || s.Documentation.Commit != s.Target.Commit || s.Documentation.Repository != s.Target.Repository {
		t.Fatal("missing pinned target")
	}
	d := s.Documentation
	d.Evidence = []domain.DocumentationEvidence{{Kind: "file", Path: "login.go", Content: "code"}}
	applyDocumentationCommand(s, command{Type: "resume"})
	if s.Documentation != d || len(d.Evidence) != 1 {
		t.Fatal("resume discarded research")
	}
}
