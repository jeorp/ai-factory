package agent

import (
	"encoding/json"
	"strings"

	"ai-factory/backend/internal/domain"
)

// implementationResultError carries fixed classifications and numeric metadata, never model text.
type implementationResultError struct {
	reason string
	limit  *editLimitIssue
}

type editLimitIssue struct {
	FileIndex int
	EditIndex int
	Field     string
	Bytes     int
	Limit     int
}

func editTextLimit(d Decision) *editLimitIssue {
	if d.Implementation == nil {
		return nil
	}
	for i, c := range d.Implementation.Changes {
		for j, e := range c.LineEdits {
			for _, v := range []struct {
				field       string
				size, limit int
			}{
				{"expectedStart", len(e.ExpectedStart), 4096}, {"expectedEnd", len(e.ExpectedEnd), 4096}, {"lineEdits.content", len(e.Content), 64 * 1024},
			} {
				if v.size > v.limit {
					return &editLimitIssue{i + 1, j + 1, v.field, v.size, v.limit}
				}
			}
		}
		for j, e := range c.Edits {
			for _, v := range []struct {
				field       string
				size, limit int
			}{
				{"search", len(e.Search), 32 * 1024}, {"replace", len(e.Replace), 64 * 1024},
			} {
				if v.size > v.limit {
					return &editLimitIssue{i + 1, j + 1, v.field, v.size, v.limit}
				}
			}
		}
	}
	return nil
}

func newImplementationResultError(reason string, d Decision) *implementationResultError {
	e := &implementationResultError{reason: reason}
	if reason == "edit_text_limit" {
		e.limit = editTextLimit(d)
	}
	return e
}

func (e *implementationResultError) Error() string { return "invalid_implementation_result" }

func validImplementationDecision(d Decision) bool { return implementationDecisionIssue(d) == "" }

func implementationDecisionIssue(d Decision) string {
	if d.Search != nil {
		if d.Read != nil || d.Plan != nil || d.Implementation != nil || d.Verification != nil || d.StartImplementation != nil || d.StartReview != nil || d.Review != nil || strings.TrimSpace(d.Answer) != "" {
			return "search_action_conflict"
		}
		if d.Search.Validate() != nil {
			return "invalid_search"
		}
	}
	if v := d.Verification; v != nil {
		if d.Read != nil || d.Implementation != nil || d.Plan != nil || d.StartImplementation != nil || d.StartReview != nil || d.Review != nil || strings.TrimSpace(d.Answer) != "" || len(v.Issues) > 10 || len(v.Unverified) > 10 {
			return "verification_shape"
		}
		if len(v.Repairs) > domain.MaxChangeFiles || !validAdditionalFiles(v.AdditionalFiles) {
			return "verification_file_limit"
		}
		for _, repair := range v.Repairs {
			if domain.ValidateChangePath(repair.Path) != nil || strings.TrimSpace(repair.Approach) == "" || len(repair.Approach) > 1024 {
				return "verification_repair_invalid"
			}
		}
		for _, list := range [][]string{v.Issues, v.Unverified} {
			for _, s := range list {
				if strings.TrimSpace(s) == "" || len(s) > 1024 {
					return "verification_text_limit"
				}
			}
		}
		if len(v.Criteria) > 20 {
			return "verification_criteria_invalid"
		}
		seen := map[string]bool{}
		for _, c := range v.Criteria {
			if c.ID == "" || len(c.ID) > 64 || seen[c.ID] || (c.Status != "met" && c.Status != "unmet" && c.Status != "unverified") || strings.TrimSpace(c.Evidence) == "" || len(c.Evidence) > 2048 || len(c.Paths) > 10 {
				return "verification_criteria_invalid"
			}
			seen[c.ID] = true
			for _, p := range c.Paths {
				if domain.ValidateChangePath(p) != nil {
					return "verification_criteria_invalid"
				}
			}
		}
	}
	if s := d.StartImplementation; s != nil {
		if strings.TrimSpace(s.Request) == "" || len(s.Request) > 4096 || s.PullNumber < 0 || s.PullNumber > 100000000 || d.Read != nil || d.Review != nil || d.StartReview != nil || strings.TrimSpace(d.Answer) != "" {
			return "start_implementation_shape"
		}
	}
	if d.Plan != nil && (d.Read != nil || d.Implementation != nil) {
		return "plan_action_conflict"
	}
	if r := d.Implementation; r != nil {
		if d.Read != nil || len(r.Changes) > domain.MaxImplementationStepFiles || len(r.Unverified) > 10 {
			return "edit_shape"
		}
		if !validAdditionalFiles(r.AdditionalFiles) {
			return "additional_files_shape"
		}
		for _, c := range r.Changes {
			if len(c.Path) > 512 || len(c.Content) > domain.MaxChangeFileBytes || len(c.Edits) > 40 || len(c.LineEdits) > 40 || len(c.SourceHash) > 64 {
				return "change_shape"
			}
		}
		if editTextLimit(d) != nil {
			return "edit_text_limit"
		}
		for _, u := range r.Unverified {
			if len(u) > 512 {
				return "unverified_text_limit"
			}
		}
	}
	return ""
}

func validAdditionalFiles(files []domain.PendingFile) bool {
	if len(files) > 10 {
		return false
	}
	for _, f := range files {
		if len(f.Path) > 512 || len(f.Approach) > 2048 || len(f.Reason) > 2048 || len(f.DependsOn) > domain.MaxChangeFiles {
			return false
		}
		for _, p := range f.DependsOn {
			if len(p) > 512 {
				return false
			}
		}
	}
	return true
}

// validImplementationResponse applies the same action boundary for both
// providers. Transport decoding and provider-specific errors stay separate.
func validImplementationResponse(input string, d Decision) bool {
	return implementationResponseIssue(input, d) == ""
}

func implementationResponseIssue(input string, d Decision) string {
	if issue := implementationDecisionIssue(d); issue != "" {
		return issue
	}
	var body struct {
		Task *implementTask `json:"implementTask"`
	}
	parseErr := json.Unmarshal([]byte(input), &body)
	if body.Task != nil && body.Task.Mode == "edit" && d.Implementation != nil {
		for _, c := range d.Implementation.Changes {
			if (body.Task.EditFormat == lineRangeFormat && len(c.Edits) > 0) || (body.Task.EditFormat != lineRangeFormat && len(c.LineEdits) > 0) {
				return "edit_format_mismatch"
			}
		}
	}
	verifying := body.Task != nil && body.Task.Mode == "verify"
	reading := verifying && (d.Read != nil || d.Search != nil)
	if verifying != (d.Verification != nil || reading) {
		return "verification_phase_mismatch"
	}
	if reading && (parseErr != nil || body.Task.FinalAnswer || body.Task.ReadBudget <= 0 || (d.Read != nil && (d.Read.Kind != "file" || d.Read.Validate() != nil))) {
		return "verification_read_not_allowed"
	}
	if d.Search == nil {
		return ""
	}
	if parseErr != nil || body.Task == nil || (body.Task.Mode != "plan" && body.Task.Mode != "verify") || body.Task.ReadBudget <= 0 || body.Task.FinalAnswer {
		return "search_not_allowed"
	}
	return ""
}
