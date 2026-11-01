package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestCIFixStopsBeforeModelWhenGitHubRefusesEvidence(t *testing.T) {
	w := newFakeWriter()
	impl := &domain.Implementation{Request: "PR#7のCIの失敗理由を確認して修正する", Status: "editing", BaseCommit: implBase, ExpectedHead: implBase, PullNumber: 7, Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	r := &Runner{Writer: w, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		t.Fatal("model generated code without CI evidence")
		return Decision{}, Usage{}, nil
	}), GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		return "", domain.Source{}, 0, errors.New("github_http_403")
	})}
	var saved Checkpoint
	err := runImplementationFixture(r, impl, func(cp Checkpoint) error { saved = cp; return nil })
	if err == nil || err.Error() != "implementation_ci_forbidden" || saved.Implementation.NextStep != 0 || len(w.cache) != 0 || len(saved.Implementation.Unverified) != 1 || !strings.Contains(saved.Implementation.Unverified[0], "Read-only") {
		t.Fatalf("unsafe CI fix: %v %+v", err, saved)
	}
}

func TestCIRequirementDoesNotBlockUnrelatedPRChanges(t *testing.T) {
	for request, want := range map[string]bool{"CIの失敗を修正": true, "Fix failing CI": true, "テスト失敗を直して": true, "READMEにCIの説明を追加": false, "エラー画面のデザインを変更": false} {
		if implementationNeedsCI(request) != want {
			t.Fatalf("incorrect CI requirement: %q", request)
		}
	}
}
