package domain

// Review stores only the plan, cursors and conclusions. Patches and source code
// live in a separate, expiring cache and are fetched again on a cache miss.
type Review struct {
	Title                string            `json:"title"`
	Body                 string            `json:"body"`
	Base                 string            `json:"base"`
	Head                 string            `json:"head"`
	TotalFiles           int               `json:"totalFiles"`
	Phase                string            `json:"phase"`
	Steps                []ReviewStep      `json:"steps"`
	Findings             []Finding         `json:"findings"`
	Unverified           []string          `json:"unverified"`
	Investigations       map[string]string `json:"investigations,omitempty"`
	PendingInvestigation int               `json:"pendingInvestigation,omitempty"`
	CrossCursor          int               `json:"crossCursor"`
	CrossSummary         string            `json:"crossSummary"`
}
type ReviewStep struct {
	Directory string       `json:"directory"`
	Files     []ReviewFile `json:"files"`
	State     string       `json:"state"`
	Summary   string       `json:"summary"`
}
type ReviewFile struct {
	Path         string `json:"path"`
	PreviousPath string `json:"previousPath"`
	Status       string `json:"status"`
	Index        int    `json:"index"`
	Offset       int    `json:"offset"`
	Done         bool   `json:"done"`
}
type Finding struct {
	Severity   string `json:"severity"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Side       string `json:"side"`
	Title      string `json:"title"`
	Problem    string `json:"problem"`
	Impact     string `json:"impact"`
	Evidence   string `json:"evidence"`
	Suggestion string `json:"suggestion"`
}
type ReviewResult struct {
	Findings   []Finding `json:"findings"`
	Unverified []string  `json:"unverified"`
}
