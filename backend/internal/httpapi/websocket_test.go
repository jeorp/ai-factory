package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"ai-factory/backend/internal/agent"
	"ai-factory/backend/internal/domain"
)

func TestApplyRunResultSetsSessionState(t *testing.T) {
	cases := []struct {
		name        string
		runKind     string
		err         error
		wasStopping bool
		want        string
	}{
		{"stopped", "chat", context.Canceled, true, "stopped"},
		{"ci wait", "implement", fmt.Errorf("wrapped: %w", agent.ErrCIWaiting), false, "ci_wait"},
		{"review budget", "review", errors.New("review_run_budget_resume"), false, "user_wait"},
		{"review timeout", "review", context.DeadlineExceeded, false, "user_wait"},
		{"chat timeout", "chat", context.DeadlineExceeded, false, "auth_wait"},
		{"canceled", "chat", context.Canceled, false, "auth_wait"},
		{"failed", "chat", errors.New("boom"), false, "failed"},
		{"ok keeps state", "chat", nil, false, "done"},
	}
	for _, c := range cases {
		session := &domain.Session{RunKind: c.runKind, State: "done"}
		applyRunResult(session, c.err, c.wasStopping)
		if session.State != c.want {
			t.Errorf("%s: state = %q, want %q", c.name, session.State, c.want)
		}
		failed := len(session.Limitations) == 1 && session.Limitations[0] == "boom"
		if failed != (c.want == "failed") {
			t.Errorf("%s: limitations = %v", c.name, session.Limitations)
		}
	}
}

func TestRunOutcome(t *testing.T) {
	cases := map[string]error{
		"ok":       nil,
		"canceled": context.Canceled,
		"timeout":  context.DeadlineExceeded,
		"yielded":  errors.New("review_run_budget_resume"),
		"error":    errors.New("boom"),
	}
	for want, err := range cases {
		if got := runOutcome(err); got != want {
			t.Errorf("runOutcome(%v) = %q, want %q", err, got, want)
		}
	}
	if got := runOutcome(agent.ErrCIWaiting); got != "yielded" {
		t.Errorf("runOutcome(ErrCIWaiting) = %q", got)
	}
}
