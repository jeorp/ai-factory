package agent

import (
	"context"
	"strings"

	"ai-factory/backend/internal/domain"
)

type tokenBudgetKey struct{}
type tokenBudget struct{ limit, used int }

func withTokenBudget(ctx context.Context, s domain.Session) context.Context {
	return context.WithValue(ctx, tokenBudgetKey{}, &tokenBudget{limit: s.TokenBudget, used: s.Metrics.InputTokens + s.Metrics.OutputTokens})
}

// addUsage records one model call's token usage in both the cumulative and
// the per-run counters. It reports whether the call returned any usage.
func addUsage(m *domain.Metrics, usage Usage) bool {
	if usage.Phase != "" {
		// Checkpoints own immutable snapshots; never mutate their shared map.
		phases := make(map[string]domain.PhaseUsage, len(m.Phases)+1)
		for key, value := range m.Phases {
			phases[key] = value
		}
		p := phases[usage.Phase]
		p.Calls++
		p.InputTokens += usage.InputTokens
		p.OutputTokens += usage.OutputTokens
		p.DurationMS += usage.DurationMS
		phases[usage.Phase] = p
		m.Phases = phases
	}
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
