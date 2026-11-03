package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func noChangeFixture(w *fakeWriter) domain.Session {
	s := verificationSession(w, []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "package src\nfunc A() int { return 2 }\n"}})
	s.Implementation.Plan.Files = append(s.Implementation.Plan.Files, domain.PlannedFile{Path: "src/old.go", Action: "update", Approach: "Translate prompts", DependsOn: []string{"src/a.go"}})
	s.Implementation.Plan.Files[0].DependsOn = []string{"src/old.go"}
	s.Implementation.Plan.AcceptanceCriteria[0].Paths = append(s.Implementation.Plan.AcceptanceCriteria[0].Paths, "src/old.go")
	s.Implementation.Steps = []domain.ImplementationStep{{Files: []string{"src/a.go", "src/old.go"}}}
	s.Implementation.Unverified = []string{missingPlanFilePrefix + "src/old.go"}
	return s
}

func TestReconcilePlanRequiresIndependentCodeAndCriteriaVerification(t *testing.T) {
	for _, scenario := range []string{"confirmed", "no-confirmation", "real-unknown", "unmet", "missing-code", "create", "delete", "changed-file", "out-of-plan"} {
		t.Run(scenario, func(t *testing.T) {
			w := newFakeWriter()
			s := noChangeFixture(w)
			switch scenario {
			case "missing-code":
				delete(w.files, "src/old.go")
			case "create", "delete":
				s.Implementation.Plan.Files[1].Action = scenario
			}
			before, _ := json.Marshal(s.Implementation.Plan.AcceptanceCriteria)
			calls := 0
			r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				calls++
				task := implementTaskOf(t, input)
				if !strings.Contains(input, `"noChangeCandidates"`) {
					t.Fatal("reconciliation candidates missing")
				}
				v := passedVerification(task.Plan)
				v.UnchangedFiles = []domain.UnchangedFile{{Path: "src/old.go", Reason: "File contains only a package declaration, no prompts to translate."}}
				v.DeferredChecks = []string{"go test ./..."}
				switch scenario {
				case "no-confirmation":
					v.UnchangedFiles = nil
				case "real-unknown":
					v.Unverified = []string{"CIという名前のフィールドの伝播を確認できない"}
				case "unmet":
					v.Criteria[0].Status = "unmet"
				case "changed-file":
					v.UnchangedFiles[0].Path = "src/a.go"
				case "out-of-plan":
					v.UnchangedFiles[0].Path = "src/not-approved.go"
				}
				return Decision{Summary: "checked", Verification: v}, Usage{}, nil
			})}
			var last Checkpoint
			if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
				t.Fatal(err)
			}
			got := last.Implementation
			after, _ := json.Marshal(got.Plan.AcceptanceCriteria)
			if string(before) != string(after) {
				t.Fatal("requirements changed")
			}
			if scenario == "confirmed" {
				if got.Status != "ready" || len(got.Plan.Files) != 1 || len(got.PlanAdjustments) != 1 || len(got.Plan.Files[0].DependsOn) != 0 || len(got.Unverified) != 0 || len(got.Steps[0].Files) != 1 {
					t.Fatalf("not reconciled: %+v", got)
				}
				if !strings.Contains(defaultBody(got), "no prompts to translate") || !strings.Contains(defaultBody(got), "公開後のCIで確認") {
					t.Fatal("audit or deferred check missing from PR")
				}
				// Re-run from a serialized checkpoint: the new effective plan and
				// its audit must survive without reintroducing the removed file.
				s.Implementation = got
				s.Implementation.Status = "verifying"
				r.AI = providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
					return Decision{Summary: "checked", Verification: passedVerification(implementTaskOf(t, input).Plan)}, Usage{}, nil
				})
				if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil || last.Implementation.Status != "ready" || len(last.Implementation.PlanAdjustments) != 1 {
					t.Fatalf("resume lost reconciliation: %v", err)
				}
			} else if got.Status != "verification_blocked" || len(got.Plan.Files) != 2 || len(got.PlanAdjustments) != 0 {
				t.Fatalf("unsafe narrowing: %+v", got)
			}
			if len(w.published) != 0 {
				t.Fatal("published without user review")
			}
		})
	}
}

func TestEditorNoChangeProposalIsProvisional(t *testing.T) {
	w := newFakeWriter()
	s := noChangeFixture(w)
	s.Implementation.Status = "editing"
	s.Implementation.Changes = nil
	s.Implementation.NextStep = 0
	s.Implementation.Unverified = nil
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "edit" {
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "return 1", Replace: "return 2"}}}}, UnchangedFiles: []domain.UnchangedFile{{Path: "src/old.go", Reason: "No prompts here"}}}}, Usage{}, nil
		}
		if len(task.Plan.Files) != 2 {
			t.Fatal("editor removed a file without verification")
		}
		// A verifier which finds a genuine gap must block even when the editor
		// has already proposed removing the file from scope.
		v := passedVerification(task.Plan)
		v.Unverified = []string{"required behavior is not established"}
		return Decision{Summary: "needs review", Verification: v}, Usage{}, nil
	})}
	var last Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if last.Implementation.Status != "verification_blocked" || len(last.Implementation.Plan.Files) != 2 || len(last.Implementation.NoChangeCandidates) != 1 || len(last.Implementation.Changes) != 1 {
		t.Fatalf("proposal bypassed verifier: %+v", last.Implementation)
	}
}

func TestNoChangeCandidatesCannotDiscardDraftsOrOtherActions(t *testing.T) {
	for _, scenario := range []string{"valid", "create", "delete", "outside-step", "already-changed", "conflict", "not-read"} {
		w := newFakeWriter()
		s := noChangeFixture(w)
		impl := s.Implementation
		result := &ImplementResult{UnchangedFiles: []domain.UnchangedFile{{Path: "src/old.go", Reason: "no prompt definitions"}}}
		originals := map[string]string{"src/old.go": w.files["src/old.go"]}
		step := []string{"src/old.go"}
		switch scenario {
		case "create", "delete":
			impl.Plan.Files[1].Action = scenario
		case "outside-step":
			step = []string{"src/a.go"}
		case "already-changed":
			impl.Changes = append(impl.Changes, domain.ChangeSummary{Path: "src/old.go", Action: "update"})
		case "conflict":
			result.Changes = []ChangeProposal{{Path: "src/old.go", Action: "update"}}
		case "not-read":
			delete(originals, "src/old.go")
		}
		err := validateNoChangeCandidates(step, impl, originals, result)
		if (err == nil) != (scenario == "valid") {
			t.Fatalf("%s: %v", scenario, err)
		}
	}
}

func TestNoChangeModelFieldsAreBounded(t *testing.T) {
	for _, files := range [][]domain.UnchangedFile{
		{{Path: "src/a.go", Reason: ""}},
		{{Path: "../outside", Reason: "not needed"}},
		{{Path: "src/a.go", Reason: "a"}, {Path: "src/a.go", Reason: "b"}},
		{{Path: "src/a.go", Reason: strings.Repeat("x", 1025)}},
	} {
		if validImplementationDecision(Decision{Implementation: &ImplementResult{UnchangedFiles: files}}) || validImplementationDecision(Decision{Verification: &domain.ImplementationVerification{UnchangedFiles: files}}) {
			t.Fatal("invalid no-change claim accepted")
		}
	}
	if validImplementationDecision(Decision{Verification: &domain.ImplementationVerification{DeferredChecks: []string{strings.Repeat("x", 1025)}}}) {
		t.Fatal("unbounded deferred check")
	}
}

func TestAllUnchangedFilesDoNotCreateAnEmptyPullRequest(t *testing.T) {
	w := newFakeWriter()
	s := noChangeFixture(w)
	s.Implementation.Status = "editing"
	s.Implementation.NextStep = 0
	s.Implementation.Changes = nil
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode != "edit" {
			t.Fatal("empty changes reached verification")
		}
		return Decision{Implementation: &ImplementResult{UnchangedFiles: []domain.UnchangedFile{{Path: "src/a.go", Reason: "already satisfied"}, {Path: "src/old.go", Reason: "no prompts"}}}}, Usage{}, nil
	})}
	err := r.Run(context.Background(), Input{Session: s}, func(Checkpoint) error { return nil })
	if err == nil || err.Error() != "implementation_no_changes" || len(w.published) != 0 {
		t.Fatalf("empty PR allowed: %v", err)
	}
}
