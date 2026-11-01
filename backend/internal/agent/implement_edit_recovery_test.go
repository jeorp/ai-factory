package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestEditingCarriesCIContextAndPreservesBlockedReason(t *testing.T) {
	w := newFakeWriter()
	impl := &domain.Implementation{Status: "editing", BaseCommit: implBase, ExpectedHead: implBase, PullNumber: 7, Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	r := &Runner{Writer: w, GitHub: readerFunc(func(_ context.Context, _ string, _ domain.Target, read github.Read) (string, domain.Source, int, error) {
		if read.Kind == "check_runs" {
			return `{"check_runs":[{"id":123,"job_id":999,"name":"backend","conclusion":"failure"}]}`, domain.Source{}, 100, nil
		}
		if read.Kind != "job_log" || read.Number != 999 {
			t.Fatalf("used a check ID as job ID: %+v", read)
		}
		return "compile: missing Locale field", domain.Source{}, 33, nil
	}), AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		if !strings.Contains(input, "missing Locale field") {
			t.Fatal("editing lost CI evidence")
		}
		return Decision{Implementation: &ImplementResult{Unverified: []string{"修正する定義が計画に含まれていません"}}}, Usage{}, nil
	})}
	var saved Checkpoint
	err := runImplementationFixture(r, impl, func(cp Checkpoint) error { saved = cp; return nil })
	if err == nil || err.Error() != "implementation_edit_blocked" || len(saved.Implementation.Unverified) != 1 || len(w.cache) != 0 || saved.Implementation.NextStep != 0 || saved.Metrics.ToolCalls != 2 {
		t.Fatalf("blocked reason lost: %v %+v", err, saved)
	}
}

func TestRangeRecoveryStopsBeforeRetryWhenCheckpointFails(t *testing.T) {
	w := newRevisionWriter()
	calls := 0
	impl := &domain.Implementation{Status: "editing", BaseCommit: implBase, Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	r := &Runner{Writer: w, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "not in original", Replace: "wrong"}}}}}}, Usage{}, nil
	})}
	err := runImplementationFixture(r, impl, func(cp Checkpoint) error {
		if strings.Contains(cp.Progress, "行範囲") {
			return errors.New("checkpoint_failed")
		}
		return nil
	})
	if err == nil || err.Error() != "checkpoint_failed" || calls != 1 || len(w.versions) != 0 {
		t.Fatalf("retried without checkpoint: %v calls=%d", err, calls)
	}
}

func TestRangeRecoveryRejectsStaleSnapshotAndMixedFormats(t *testing.T) {
	impl := &domain.Implementation{Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}}}}
	originals := map[string]string{"a.go": "first\nlast\n"}
	valid := ChangeProposal{Path: "a.go", Action: "update", SourceHash: domain.SourceHash(originals["a.go"]), LineEdits: []domain.LineEdit{{StartLine: 1, EndLine: 1, ExpectedStart: "first", ExpectedEnd: "first", Content: "changed"}}}
	stale := valid
	stale.SourceHash = domain.SourceHash("older content")
	mixed := valid
	mixed.Edits = []domain.Edit{{Search: "first", Replace: "changed"}}
	for _, tc := range []struct {
		proposal ChangeProposal
		code     string
	}{{stale, "edit_source_mismatch"}, {mixed, "update_format_conflict"}} {
		if changes, err := applyProposals([]string{"a.go"}, impl, originals, []ChangeProposal{tc.proposal}); err == nil || err.Error() != tc.code || len(changes) != 0 {
			t.Fatalf("bad snapshot accepted: %v %+v", err, changes)
		}
	}
	changes, err := applyProposals([]string{"a.go"}, impl, originals, []ChangeProposal{valid})
	if err != nil || changes[0].Content != "changed\nlast\n" {
		t.Fatalf("valid range rejected: %+v %v", changes, err)
	}
}

func TestMissingSearchSwitchesToHashPinnedRangesThenRechecks(t *testing.T) {
	w := newRevisionWriter()
	original := "package src\n\nfunc A() int { return 1 }\n// untouched 日本語\n"
	w.files["src/a.go"] = original
	impl := &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "Aを修正", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "2を返す"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	calls, checks := 0, 0
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "verify" {
			checks++
			if !strings.Contains(input, "return 2") || !strings.Contains(input, "untouched 日本語") {
				t.Fatal("verification lost corrected code")
			}
			return Decision{Verification: passedVerification(task.Plan), Summary: "確認"}, Usage{}, nil
		}
		calls++
		if calls == 1 {
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "not in original", Replace: "wrong"}}}}}}, Usage{}, nil
		}
		f := task.Files[0]
		if calls != 2 || task.EditFormat != lineRangeFormat || f.Original != "" || f.SourceHash != domain.SourceHash(original) || !strings.Contains(f.NumberedOriginal, "3|func A()") || !strings.Contains(task.Correction, "編集1") || len(w.versions) != 0 {
			t.Fatalf("unsafe/missing recovery: %+v", task)
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: f.Path, Action: "update", SourceHash: f.SourceHash, LineEdits: []domain.LineEdit{{StartLine: 3, EndLine: 3, ExpectedStart: "func A() int { return 1 }", ExpectedEnd: "func A() int { return 1 }", Content: "func A() int { return 2 }"}}}}}}, Usage{}, nil
	})}
	var last Checkpoint
	if err := runImplementationFixture(r, impl, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	f, ok := w.GetChange(context.Background(), impl.CacheID, impl.Version, "src/a.go")
	if !ok || f.Content != strings.Replace(original, "return 1", "return 2", 1) || last.Implementation.Status != "ready" || calls != 2 || checks != 1 || len(w.published) != 0 {
		t.Fatalf("range recovery failed: %+v %+v", f, last)
	}
}
