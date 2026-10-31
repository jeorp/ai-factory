package domain

import (
	"strings"
	"testing"
)

func TestApplyEditsRequiresUniqueMatch(t *testing.T) {
	got, err := ApplyEdits("a := 1\nb := 2\n", []Edit{{Search: "b := 2", Replace: "b := 3"}})
	if err != nil || got != "a := 1\nb := 3\n" {
		t.Fatalf("edit not applied: %q %v", got, err)
	}
	for _, edits := range [][]Edit{nil, {{Search: ""}}, {{Search: "missing"}}, {{Search: "x"}}} {
		if _, err := ApplyEdits("x x", edits); err == nil {
			t.Fatalf("unsafe edit accepted: %+v", edits)
		}
	}
}

func TestValidateChangePath(t *testing.T) {
	for _, p := range []string{"", "/etc/passwd", "../a", "a/../b", "a//b", "./a", ".git/config", "src/.git/x", ".github/workflows/ci.yaml", ".GitHub/Workflows/ci.yaml", ".gitmodules", ".github/actions/setup/action.yml", "CODEOWNERS", ".github/CODEOWNERS", "a\\b", "logo.png", "go.lock"} {
		if ValidateChangePath(p) == nil {
			t.Fatalf("path accepted: %q", p)
		}
	}
	for _, p := range []string{"README.md", "backend/internal/a.go", ".github/pull_request_template.md"} {
		if err := ValidateChangePath(p); err != nil {
			t.Fatalf("path rejected: %q %v", p, err)
		}
	}
}

func TestValidateChangeSet(t *testing.T) {
	approved := []string{"a.go", "b.go"}
	ok := []FileChange{{Path: "a.go", Action: "update", Content: "package a\n"}, {Path: "b.go", Action: "delete"}}
	if err := ValidateChangeSet(ok, approved); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]FileChange{
		"empty":        nil,
		"not approved": {{Path: "c.go", Action: "create", Content: "x"}},
		"duplicate":    {{Path: "a.go", Action: "update", Content: "x"}, {Path: "a.go", Action: "delete"}},
		"action":       {{Path: "a.go", Action: "rename"}},
		"delete body":  {{Path: "a.go", Action: "delete", Content: "x"}},
		"secret":       {{Path: "a.go", Action: "update", Content: "key = \"sk-abcdefghijklmnopqrstu\""}},
		"private key":  {{Path: "a.go", Action: "update", Content: "-----BEGIN RSA PRIVATE KEY-----"}},
		"size":         {{Path: "a.go", Action: "update", Content: strings.Repeat("x", MaxChangeFileBytes+1)}},
		"binary":       {{Path: "a.go", Action: "update", Content: "a\x00b"}},
	}
	for name, files := range cases {
		if ValidateChangeSet(files, approved) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	many := make([]FileChange, MaxChangeFiles+1)
	if ValidateChangeSet(many, approved) == nil {
		t.Fatal("file limit not enforced")
	}
}

func TestValidBranchName(t *testing.T) {
	for _, b := range []string{"ai-factory/add-login", "ai-factory/s1/fix-x"} {
		if !ValidBranchName(b) {
			t.Fatalf("rejected %q", b)
		}
	}
	for _, b := range []string{"main", "feature/x", "ai-factory/", "ai-factory/a..b", "ai-factory/x.lock", "ai-factory/Upper", "ai-factory/a b", "ai-factory/a/b/c"} {
		if ValidBranchName(b) {
			t.Fatalf("accepted %q", b)
		}
	}
}

func TestContainsSecretIgnoresOrdinaryWords(t *testing.T) {
	for _, text := range []string{"risk-assessment-score", "desk-layout-component", "--mask-image-gradient", "task-runner-configuration"} {
		if ContainsSecret(text) {
			t.Fatalf("false positive: %q", text)
		}
	}
	for _, text := range []string{"sk-abcdefghijklmnop", "key=sk-abcdefghijklmnop", "\"ghp_abcdefghijklmnopqrst\""} {
		if !ContainsSecret(text) {
			t.Fatalf("missed: %q", text)
		}
	}
}
