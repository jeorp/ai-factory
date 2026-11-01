package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func repairFixture() (*revisionWriter, *domain.Implementation) {
	w := newRevisionWriter()
	impl := &domain.Implementation{Request: "Aを修正", Status: "verifying", BaseBranch: "main", BaseCommit: implBase, CacheID: "repair-cache", Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "Aを修正", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update", Approach: "Aを変更"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}, NextStep: 1, Changes: []domain.ChangeSummary{{Path: "src/a.go", Action: "update", Bytes: len("draft a")}}}
	_ = w.PutChange(context.Background(), impl.CacheID, impl.Version, domain.FileChange{Path: "src/a.go", Action: "update", Content: "draft a"})
	return w, impl
}

func TestVerificationRepairsCurrentContentThenRechecks(t *testing.T) {
	w, impl := repairFixture()
	impl.Plan.Files = append(impl.Plan.Files, domain.PlannedFile{Path: "src/b.go", Action: "create", Approach: "Bを作成"})
	impl.Changes = append(impl.Changes, domain.ChangeSummary{Path: "src/b.go", Action: "create", Bytes: len("draft b")})
	_ = w.PutChange(context.Background(), impl.CacheID, impl.Version, domain.FileChange{Path: "src/b.go", Action: "create", Content: "draft b"})
	checks, edits := 0, 0
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "verify" {
			checks++
			if checks == 1 {
				return Decision{Summary: "確認", Verification: &domain.ImplementationVerification{Issues: []string{"src/a.goとsrc/b.goの契約が不一致"}, Repairs: []domain.VerificationRepair{{Path: "src/a.go", Approach: "Aの引数を修正"}, {Path: "src/b.go", Approach: "Bの呼び出しを修正"}}}}, Usage{}, nil
			}
			if !strings.Contains(input, "draft a fixed") || !strings.Contains(input, "draft b fixed") {
				t.Fatal("verification used stale contents")
			}
			return Decision{Summary: "整合", Verification: passedVerification(task.Plan)}, Usage{}, nil
		}
		edits++
		if task.Mode != "edit" || !strings.Contains(task.RepairFeedback, "Aの引数を修正") || len(task.Files) != 2 {
			t.Fatalf("missing repair context: %+v", task)
		}
		for _, file := range task.Files {
			if file.Original != map[string]string{"src/a.go": "draft a", "src/b.go": "draft b"}[file.Path] {
				t.Fatalf("base overwrote generated file: %+v", file)
			}
		}
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "draft a", Replace: "draft a fixed"}}}, {Path: "src/b.go", Action: "create", Content: "draft b fixed"}}}}, Usage{}, nil
	})}
	var last Checkpoint
	if err := runImplementationFixture(r, impl, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	got := last.Implementation
	if checks != 2 || edits != 1 || got.Status != "ready" || got.RepairRound != 1 || len(got.Verification.Issues) != 0 || got.RepairFeedback != "" || got.ChangeRevision != 1 || len(w.published) != 0 {
		t.Fatalf("bad repair: %+v checks=%d edits=%d", got, checks, edits)
	}
}

func TestVerificationRepairStopsAtTwoRounds(t *testing.T) {
	w, impl := repairFixture()
	checks, edits := 0, 0
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "verify" {
			checks++
			return Decision{Summary: "確認", Verification: &domain.ImplementationVerification{Issues: []string{"src/a.goの残っている問題"}, Repairs: []domain.VerificationRepair{{Path: "src/a.go", Approach: "修正"}}}}, Usage{}, nil
		}
		edits++
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: task.Files[0].Original, Replace: fmt.Sprintf("repair %d", edits)}}}}}}, Usage{}, nil
	})}
	var last Checkpoint
	if err := runImplementationFixture(r, impl, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	got := last.Implementation
	if got.Status != "verification_blocked" || got.RepairRound != 2 || edits != 2 || checks != 3 || len(got.Verification.Issues) != 1 || !strings.Contains(strings.Join(got.Verification.Unverified, ""), "上限") {
		t.Fatalf("repair loop not bounded: %+v edits=%d checks=%d", got, edits, checks)
	}
}

func TestVerificationAdditionalFilesWaitForApproval(t *testing.T) {
	for _, approve := range []bool{true, false} {
		t.Run(fmt.Sprint(approve), func(t *testing.T) {
			w, impl := repairFixture()
			checks, edits := 0, 0
			r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				task := implementTaskOf(t, input)
				if task.Mode == "verify" {
					checks++
					if checks == 1 || !approve {
						return Decision{Summary: "確認", Verification: &domain.ImplementationVerification{Issues: []string{"src/b.goが必要"}, Repairs: []domain.VerificationRepair{{Path: "outside.go", Approach: "未承認なので変更禁止"}}, AdditionalFiles: []domain.PendingFile{{Path: "src/b.go", Action: "create", Approach: "Bを作成", Reason: "Aから呼ぶ", DependsOn: []string{"src/a.go"}}}}}, Usage{}, nil
					}
					return Decision{Summary: "確認", Verification: passedVerification(task.Plan)}, Usage{}, nil
				}
				edits++
				if !approve || len(task.Files) != 1 || task.Files[0].Path != "src/b.go" {
					t.Fatalf("unapproved edit: %+v", task)
				}
				return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/b.go", Action: "create", Content: "package src\nfunc B() {}\n"}}}}, Usage{}, nil
			})}
			var last Checkpoint
			save := func(cp Checkpoint) error { last = cp; return nil }
			if err := runImplementationFixture(r, impl, save); err != nil {
				t.Fatal(err)
			}
			got := last.Implementation
			if got.Status != "amend_ready" || len(got.PendingFiles) != 1 || edits != 0 || len(w.versions) != 1 || got.NextStep != 1 {
				t.Fatalf("approval bypassed: %+v edits=%d", got, edits)
			}
			got.ResolveAmendment(approve)
			if err := runImplementationFixture(r, got, save); err != nil {
				t.Fatal(err)
			}
			got = last.Implementation
			if (approve && got.Status != "ready" || !approve && got.Status != "verification_blocked") || (approve && (edits != 1 || len(got.Changes) != 2)) || (!approve && (edits != 0 || len(got.Changes) != 1 || len(got.Declined) != 1 || got.RepairRound != 1)) {
				t.Fatalf("bad amendment: %+v", got)
			}
		})
	}
}

func TestScheduledRepairResumesAfterCheckpointFailure(t *testing.T) {
	w, impl := repairFixture()
	edits := 0
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "verify" {
			v := passedVerification(task.Plan)
			if edits == 0 {
				v.Issues = []string{"修正が必要"}
				v.Repairs = []domain.VerificationRepair{{Path: "src/a.go", Approach: "引数を修正"}}
			}
			return Decision{Summary: "確認", Verification: v}, Usage{}, nil
		}
		edits++
		return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "draft a", Replace: "draft a fixed"}}}}}}, Usage{}, nil
	})}
	var saved Checkpoint
	err := runImplementationFixture(r, impl, func(cp Checkpoint) error {
		saved = cp
		if cp.Implementation.Status == "editing" {
			return errors.New("checkpoint_failed")
		}
		return nil
	})
	if err == nil || edits != 0 || saved.Implementation.RepairRound != 1 {
		t.Fatalf("continued after failed checkpoint: %v", err)
	}
	if err := runImplementationFixture(r, saved.Implementation, func(cp Checkpoint) error { saved = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if edits != 1 || saved.Implementation.Status != "ready" || saved.Implementation.RepairRound != 1 {
		t.Fatalf("repair not resumed: %+v", saved.Implementation)
	}
}
