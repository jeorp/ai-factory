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
	// Two CI reads plus the automatic lookup of project documentation.
	if err == nil || err.Error() != "implementation_edit_blocked" || len(saved.Implementation.Unverified) != 1 || len(w.cache) != 0 || saved.Implementation.NextStep != 0 || saved.Metrics.ToolCalls != 3 {
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
		if changes, err := applyProposals([]string{"a.go"}, impl, originals, []ChangeProposal{tc.proposal}); err == nil || implementationErrorCode(err) != tc.code || len(changes) != 0 {
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

func TestAnchorRecoveryKeepsExactSourceAcrossAdditionalRead(t *testing.T) {
	for _, wrongAnchor := range []string{"実装しないでください。", "instructionsは利用者の追加指示です。"} {
		t.Run(wrongAnchor, func(t *testing.T) {
			w := newRevisionWriter()
			original := "package src\nconst rule = `\n実装を開始しないでください。\n`\n"
			w.files["src/a.go"] = original
			impl := &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
			calls, checks, reads := 0, 0, 0
			r := &Runner{Writer: w, GitHub: readerFunc(func(_ context.Context, _ string, _ domain.Target, read github.Read) (string, domain.Source, int, error) {
				reads++
				return original, domain.Source{}, len(original), nil
			}), AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				task := implementTaskOf(t, input)
				if task.Mode == "verify" {
					checks++
					return Decision{Summary: "境界行の修正と既存コードの保持を確認", Verification: passedVerification(task.Plan)}, Usage{}, nil
				}
				calls++
				if len(w.versions) != 0 {
					t.Fatal("rejected or read-only proposal was saved")
				}
				if calls == 1 {
					return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "missing original", Replace: "wrong"}}}}}}, Usage{}, nil
				}
				p := ChangeProposal{Path: "src/a.go", Action: "update", SourceHash: task.Files[0].SourceHash, LineEdits: []domain.LineEdit{{StartLine: 3, EndLine: 3, ExpectedStart: wrongAnchor, ExpectedEnd: wrongAnchor, Content: "実装前に確認してください。"}}}
				if calls == 2 {
					return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{p}}}, Usage{}, nil
				}
				f := task.EditFailure
				if f == nil || f.Code != "edit_line_anchor_mismatch" || f.Path != p.Path || f.SourceHash != domain.SourceHash(original) || f.LineCount == nil || *f.LineCount != 4 || f.EditIndex != 1 || f.StartLine != 3 || f.EndLine != 3 || f.ExpectedStart != "実装を開始しないでください。" || f.ExpectedEnd != f.ExpectedStart || !strings.Contains(task.Correction, "editFailure") {
					t.Fatalf("exact retry evidence missing on call %d: %+v", calls, task)
				}
				if calls == 3 {
					return Decision{Read: &github.Read{Kind: "file", Path: "src/a.go", Detail: "summary"}}, Usage{}, nil
				}
				if calls != 4 {
					t.Fatal("recovery exceeded expected call count")
				}
				p.LineEdits[0].ExpectedStart, p.LineEdits[0].ExpectedEnd = f.ExpectedStart, f.ExpectedEnd
				return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{p}}}, Usage{}, nil
			})}
			var last Checkpoint
			if err := runImplementationFixture(r, impl, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
				t.Fatal(err)
			}
			result, ok := w.GetChange(context.Background(), impl.CacheID, impl.Version, "src/a.go")
			if !ok || result.Content != strings.Replace(original, "実装を開始しないでください。", "実装前に確認してください。", 1) || calls != 4 || reads != 1 || checks != 1 || last.Implementation.Status != "ready" || len(w.published) != 0 {
				t.Fatalf("unsafe or incomplete recovery: calls=%d reads=%d checks=%d result=%+v", calls, reads, checks, result)
			}
		})
	}
}

func TestEditFailureFeedbackUsesValidUnclippedBoundaries(t *testing.T) {
	for _, source := range []string{"first\nlast\n", "first\r\nlast\r\n", "first\nlast"} {
		proposal := ChangeProposal{Path: "a.go", LineEdits: []domain.LineEdit{{StartLine: 1, EndLine: 2}}}
		err := &proposalEditError{path: "a.go", err: &domain.EditApplyError{Code: "edit_line_anchor_mismatch", Index: 1}}
		f := editFailureFeedback(map[string]string{"a.go": source}, []ChangeProposal{proposal}, err)
		if f.LineCount == nil || *f.LineCount != 2 || f.ExpectedStart != "first" || f.ExpectedEnd != "last" || f.SourceHash != domain.SourceHash(source) {
			t.Fatalf("incorrect source coordinates: %+v", f)
		}
	}
	source := "\n" + strings.Repeat("秘", 1366) + "\nlast\n"
	for _, lines := range [][2]int{{1, 2}, {0, 4}, {3, 3}} {
		p := ChangeProposal{Path: "a.go", LineEdits: []domain.LineEdit{{StartLine: lines[0], EndLine: lines[1]}}}
		err := &proposalEditError{path: "a.go", err: &domain.EditApplyError{Code: "edit_line_range_invalid", Index: 1}}
		f := editFailureFeedback(map[string]string{"a.go": source}, []ChangeProposal{p}, err)
		if f.LineCount == nil || *f.LineCount != 3 {
			t.Fatalf("phantom trailing line: %+v", f)
		}
		if lines[0] == 3 {
			if f.ExpectedStart != "last" || f.ExpectedEnd != "last" {
				t.Fatal("valid boundary omitted")
			}
		} else if f.ExpectedStart != "" || f.ExpectedEnd != "" {
			t.Fatal("invalid or clipped boundary suggested")
		}
	}
	p := ChangeProposal{Path: "a.go", Action: "update", SourceHash: "stale", LineEdits: []domain.LineEdit{{StartLine: 1, EndLine: 1}}}
	impl := &domain.Implementation{Plan: &domain.ImplementationPlan{Files: []domain.PlannedFile{{Path: "a.go", Action: "update"}}}}
	_, err := applyProposals([]string{"a.go"}, impl, map[string]string{"a.go": source}, []ChangeProposal{p})
	f := editFailureFeedback(map[string]string{"a.go": source}, []ChangeProposal{p}, err)
	if f == nil || f.Code != "edit_source_mismatch" || f.SourceHash != domain.SourceHash(source) {
		t.Fatal("stale source hash correction missing")
	}
}
