package agent

import "ai-factory/backend/internal/domain"

// Keep enough draft context for a short request such as 「指摘を直して」. Code
// and server cache identifiers are never included in ordinary chat context.
func implementationChatContext(impl *domain.Implementation) any {
	if impl == nil {
		return nil
	}
	issues := []string{}
	if impl.Verification != nil {
		for _, issue := range impl.Verification.Issues {
			if len(issues) == 10 {
				break
			}
			issues = append(issues, domain.Clip(issue, 512))
		}
	}
	return struct {
		Status  string                 `json:"status"`
		Request string                 `json:"request"`
		Changes []domain.ChangeSummary `json:"changes"`
		Issues  []string               `json:"issues"`
	}{impl.Status, domain.Clip(impl.Request, 1024), impl.Changes, issues}
}
