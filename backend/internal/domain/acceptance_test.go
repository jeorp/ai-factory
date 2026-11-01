package domain

import "testing"

func TestAcceptanceCriteriaRequireObservableChecksAndUniqueIDs(t *testing.T) {
	c := AcceptanceCriterion{ID: "locale", Requirement: "UI language reaches AI", Paths: []string{"web/ui.ts", "api/session.go"}, Checks: []string{"test both languages"}}
	if err := ValidateAcceptanceCriteria([]AcceptanceCriterion{c}); err != nil {
		t.Fatal(err)
	}
	for _, criteria := range [][]AcceptanceCriterion{{c, c}, {{ID: "locale", Requirement: "language", Paths: []string{"a.go"}}}, {{ID: "locale", Requirement: "language", Paths: []string{"../secret"}, Checks: []string{"test"}}}} {
		if ValidateAcceptanceCriteria(criteria) == nil {
			t.Fatal("invalid criterion accepted")
		}
	}
}

func TestCannotPublishWithZeroIssuesAndUnverifiedRequirement(t *testing.T) {
	i := &Implementation{Plan: &ImplementationPlan{AcceptanceCriteria: []AcceptanceCriterion{{ID: "locale"}}}, Verification: &ImplementationVerification{Criteria: []CriterionResult{{ID: "locale", Status: "met", Evidence: "UI passes locale to API", Paths: []string{"web/ui.ts"}}}}}
	if !i.CanPublish() {
		t.Fatal("complete verification rejected")
	}
	i.Verification.Unverified = []string{"Session保存を未確認"}
	if i.CanPublish() {
		t.Fatal("zero findings masked uncertainty")
	}
	i.Verification.Unverified = nil
	i.Verification.Criteria = nil
	if i.CanPublish() {
		t.Fatal("missing criterion accepted")
	}
}
