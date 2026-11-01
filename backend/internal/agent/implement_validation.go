package agent

import (
	"encoding/json"
	"strings"

	"ai-factory/backend/internal/domain"
)

func validImplementationDecision(d Decision) bool {
	if d.Search != nil {
		if d.Read != nil || d.Plan != nil || d.Implementation != nil || d.Verification != nil || d.StartImplementation != nil || d.StartReview != nil || d.Review != nil || strings.TrimSpace(d.Answer) != "" || d.Search.Validate() != nil {
			return false
		}
	}
	if v := d.Verification; v != nil {
		if d.Read != nil || d.Implementation != nil || d.Plan != nil || d.StartImplementation != nil || d.StartReview != nil || d.Review != nil || strings.TrimSpace(d.Answer) != "" || len(v.Issues) > 10 || len(v.Unverified) > 10 {
			return false
		}
		if len(v.Repairs) > domain.MaxChangeFiles || !validAdditionalFiles(v.AdditionalFiles) {
			return false
		}
		for _, repair := range v.Repairs {
			if domain.ValidateChangePath(repair.Path) != nil || strings.TrimSpace(repair.Approach) == "" || len(repair.Approach) > 1024 {
				return false
			}
		}
		for _, list := range [][]string{v.Issues, v.Unverified} {
			for _, s := range list {
				if strings.TrimSpace(s) == "" || len(s) > 1024 {
					return false
				}
			}
		}
	}
	if s := d.StartImplementation; s != nil {
		if strings.TrimSpace(s.Request) == "" || len(s.Request) > 4096 || s.PullNumber < 0 || s.PullNumber > 100000000 || d.Read != nil || d.Review != nil || d.StartReview != nil || strings.TrimSpace(d.Answer) != "" {
			return false
		}
	}
	if d.Plan != nil && (d.Read != nil || d.Implementation != nil) {
		return false
	}
	if r := d.Implementation; r != nil {
		if d.Read != nil || len(r.Changes) > domain.MaxImplementationStepFiles || len(r.Unverified) > 10 {
			return false
		}
		if !validAdditionalFiles(r.AdditionalFiles) {
			return false
		}
		for _, c := range r.Changes {
			if len(c.Path) > 512 || len(c.Content) > domain.MaxChangeFileBytes || len(c.Edits) > 40 {
				return false
			}
			for _, e := range c.Edits {
				if len(e.Search) > 32*1024 || len(e.Replace) > 64*1024 {
					return false
				}
			}
		}
		for _, u := range r.Unverified {
			if len(u) > 512 {
				return false
			}
		}
	}
	return true
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
	if !validImplementationDecision(d) {
		return false
	}
	var body struct {
		Task *implementTask `json:"implementTask"`
	}
	parseErr := json.Unmarshal([]byte(input), &body)
	verifying := body.Task != nil && body.Task.Mode == "verify"
	if verifying != (d.Verification != nil) {
		return false
	}
	if d.Search == nil {
		return true
	}
	return parseErr == nil && body.Task != nil && body.Task.Mode == "plan" && body.Task.ReadBudget > 0 && !body.Task.FinalAnswer
}
