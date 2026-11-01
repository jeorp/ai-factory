package domain

import (
	"errors"
	"strings"
)

func ValidateAcceptanceCriteria(criteria []AcceptanceCriterion) error {
	// Empty is accepted for persisted plans from older versions. The verifier
	// blocks them and asks for replanning; new plans require criteria up front.
	if len(criteria) > 20 {
		return errors.New("invalid_acceptance_criteria")
	}
	seen := map[string]bool{}
	for _, c := range criteria {
		if strings.TrimSpace(c.ID) == "" || len(c.ID) > 64 || seen[c.ID] || strings.TrimSpace(c.Requirement) == "" || len(c.Requirement) > 1024 || len(c.Paths) == 0 || len(c.Paths) > 10 || len(c.Checks) == 0 || len(c.Checks) > 10 {
			return errors.New("invalid_acceptance_criteria")
		}
		seen[c.ID] = true
		for _, p := range c.Paths {
			if ValidateChangePath(p) != nil {
				return errors.New("invalid_acceptance_criteria")
			}
		}
		for _, check := range c.Checks {
			if strings.TrimSpace(check) == "" || len(check) > 512 {
				return errors.New("invalid_acceptance_criteria")
			}
		}
		if len(c.FailureRefs) > 200 {
			return errors.New("invalid_acceptance_criteria")
		}
		for _, ref := range c.FailureRefs {
			if len(ref) > 64 || strings.TrimSpace(ref) == "" {
				return errors.New("invalid_acceptance_criteria")
			}
		}
	}
	return nil
}

// CanPublish requires a completed static check. Execution is performed by
// GitHub CI on the resulting commit and is tracked separately.
func (i *Implementation) CanPublish() bool {
	if i == nil || i.Plan == nil || len(i.Plan.AcceptanceCriteria) == 0 || i.Verification == nil || len(i.Verification.Issues) > 0 || len(i.Verification.Unverified) > 0 {
		return false
	}
	results := map[string]CriterionResult{}
	for _, c := range i.Verification.Criteria {
		if _, duplicate := results[c.ID]; duplicate {
			return false
		}
		results[c.ID] = c
	}
	for _, c := range i.Plan.AcceptanceCriteria {
		r, ok := results[c.ID]
		if !ok || r.Status != "met" || strings.TrimSpace(r.Evidence) == "" || len(r.Paths) == 0 {
			return false
		}
	}
	return true
}
