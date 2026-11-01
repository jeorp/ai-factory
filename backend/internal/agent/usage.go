package agent

import (
	"strings"

	"ai-factory/backend/internal/domain"
)

// addUsage records one model call's token usage in both the cumulative and
// the per-run counters. It reports whether the call returned any usage.
func addUsage(m *domain.Metrics, usage Usage) bool {
	m.InputTokens += usage.InputTokens
	m.OutputTokens += usage.OutputTokens
	m.LastInputTokens += usage.InputTokens
	m.LastOutputTokens += usage.OutputTokens
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return false
	}
	m.HasLastUsage = true
	return true
}

// echoes reports whether s contains the run's AI key or GitHub token.
func echoes(s string, in Input) bool {
	return in.Key != "" && strings.Contains(s, in.Key) || in.GitHubToken != "" && strings.Contains(s, in.GitHubToken)
}
