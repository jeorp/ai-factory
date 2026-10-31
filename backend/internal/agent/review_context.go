package agent

import (
	"path"
	"regexp"
	"strconv"
	"strings"

	"ai-factory/backend/internal/domain"
)

// Saved findings stay out of the lossy summary; follow-up chat gets a compact
// index plus the full details of the findings the question refers to.
const (
	reviewFindingIndexBytes  = 3 * 1024
	reviewFindingDetailCount = 3
)

type reviewFindingRef struct {
	Number   int    `json:"number"`
	Severity string `json:"severity"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Title    string `json:"title"`
}

type reviewFindingDetail struct {
	Number     int    `json:"number"`
	Severity   string `json:"severity"`
	Path       string `json:"path"`
	Line       int    `json:"line"`
	Side       string `json:"side,omitempty"`
	Title      string `json:"title"`
	Problem    string `json:"problem"`
	Impact     string `json:"impact"`
	Evidence   string `json:"evidence"`
	Suggestion string `json:"suggestion"`
}

type reviewFindingsContext struct {
	Total   int                   `json:"total"`
	Index   []reviewFindingRef    `json:"index"`
	Details []reviewFindingDetail `json:"details,omitempty"`
	Omitted string                `json:"omitted,omitempty"`
}

var (
	findingNumberPattern  = regexp.MustCompile(`(?:指摘|#|No\.?\s*)\s*(\d{1,3})`)
	findingOrdinalPattern = regexp.MustCompile(`(\d{1,3})\s*(?:件目|番目|つ目)`)
)

func latestUserText(s domain.Session) string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == "user" {
			return s.Messages[i].Text
		}
	}
	return ""
}

// referencedFindings returns 0-based indices the question points at, in order.
func referencedFindings(findings []domain.Finding, question string) []int {
	seen := map[int]bool{}
	refs := []int{}
	add := func(i int) {
		if i >= 0 && i < len(findings) && !seen[i] {
			seen[i] = true
			refs = append(refs, i)
		}
	}
	for _, pattern := range []*regexp.Regexp{findingNumberPattern, findingOrdinalPattern} {
		for _, m := range pattern.FindAllStringSubmatch(question, -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				add(n - 1)
			}
		}
	}
	if strings.Contains(question, "最後") || strings.Contains(question, "最新") {
		add(len(findings) - 1)
	}
	if strings.Contains(question, "最初") {
		add(0)
	}
	for i, f := range findings {
		base := path.Base(f.Path)
		if f.Path != "" && strings.Contains(question, f.Path) || len(base) >= 4 && strings.Contains(question, base) {
			add(i)
		}
	}
	return refs
}

func buildReviewFindingsContext(s domain.Session) *reviewFindingsContext {
	if s.Review == nil || len(s.Review.Findings) == 0 {
		return nil
	}
	findings := s.Review.Findings
	out := &reviewFindingsContext{Total: len(findings), Index: []reviewFindingRef{}}
	refs := referencedFindings(findings, latestUserText(s))
	if len(refs) == 0 && len(findings) <= reviewFindingDetailCount {
		for i := range findings {
			refs = append(refs, i)
		}
	}
	if len(refs) > reviewFindingDetailCount {
		refs = refs[:reviewFindingDetailCount]
	}
	for _, i := range refs {
		f := findings[i]
		out.Details = append(out.Details, reviewFindingDetail{Number: i + 1, Severity: f.Severity, Path: f.Path, Line: f.Line, Side: f.Side, Title: domain.Clip(f.Title, 200), Problem: domain.Clip(f.Problem, 500), Impact: domain.Clip(f.Impact, 300), Evidence: domain.Clip(f.Evidence, 700), Suggestion: domain.Clip(f.Suggestion, 500)})
	}
	used := 0
	for i, f := range findings {
		ref := reviewFindingRef{Number: i + 1, Severity: f.Severity, Path: domain.Clip(f.Path, 200), Line: f.Line, Title: domain.Clip(f.Title, 80)}
		size := len(ref.Severity) + len(ref.Path) + len(ref.Title) + 64
		if used+size > reviewFindingIndexBytes {
			out.Omitted = strconv.Itoa(len(findings)-i) + "件の指摘を一覧から省略しました。番号やファイルパスを指定すると詳細を参照できます。"
			break
		}
		used += size
		out.Index = append(out.Index, ref)
	}
	return out
}
