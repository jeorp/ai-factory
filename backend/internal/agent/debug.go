package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"

	"ai-factory/backend/internal/domain"
)

const maxDebugPayloadBytes = 512 * 1024

type agentDebugKey struct{}
type agentDebugTrace struct {
	logger  *slog.Logger
	secrets []string
}

var debugCallSequence atomic.Uint64
var debugPrivateKey = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----`)

func withAgentDebug(ctx context.Context, enabled bool, in Input) context.Context {
	var trace *agentDebugTrace
	if enabled {
		logger := in.Logger
		if logger == nil {
			logger = slog.Default()
		}
		trace = &agentDebugTrace{logger: logger.With("session_id", in.Session.ID, "model", in.Session.Model), secrets: []string{in.Key, in.GitHubToken}}
	}
	return context.WithValue(ctx, agentDebugKey{}, trace)
}

func debugTrace(ctx context.Context) *agentDebugTrace {
	trace, _ := ctx.Value(agentDebugKey{}).(*agentDebugTrace)
	if trace == nil || !trace.logger.Enabled(ctx, slog.LevelDebug) {
		return nil
	}
	return trace
}

func redactDebug(text string, secrets []string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		text = strings.ReplaceAll(text, secret, "***REDACTED***")
		encoded, _ := json.Marshal(secret)
		text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "***REDACTED***")
	}
	text = debugPrivateKey.ReplaceAllString(text, "***MASKED_PRIVATE_KEY***")
	return domain.MaskSecrets(text)
}

// Debug payloads deliberately include source and user text, but never headers
// or the Input containing credentials. Redact before clipping so a boundary
// cannot expose a partial known credential. Normal logs remain metadata-only.
func debugText(ctx context.Context, event, payload string) {
	trace := debugTrace(ctx)
	if trace == nil {
		return
	}
	safe := redactDebug(payload, trace.secrets)
	clipped := domain.Clip(safe, maxDebugPayloadBytes)
	trace.logger.DebugContext(ctx, "agent_debug_"+event, "payload", clipped, "payload_bytes", len(payload), "logged_bytes", len(clipped), "truncated", len(clipped) < len(safe))
}

func debugJSON(ctx context.Context, event string, value any) {
	if debugTrace(ctx) == nil {
		return
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return
	}
	debugText(ctx, event, string(raw))
}

func (r *Runner) next(ctx context.Context, in Input, input string) (Decision, Usage, error) {
	if trace := debugTrace(ctx); trace != nil {
		callTrace := *trace
		callTrace.logger = trace.logger.With("ai_call_id", debugCallSequence.Add(1))
		var body struct {
			Task *implementTask `json:"implementTask"`
		}
		if json.Unmarshal([]byte(input), &body) == nil && body.Task != nil {
			callTrace.logger = callTrace.logger.With("phase", body.Task.Mode, "step", body.Task.Step, "attempt", body.Task.Attempt)
		}
		ctx = context.WithValue(ctx, agentDebugKey{}, &callTrace)
	}
	debugText(ctx, "model_input", input)
	d, usage, err := r.AI.Next(ctx, in.Key, in.Session.Model, input)
	debugJSON(ctx, "model_output", d)
	reason := ""
	if err != nil {
		reason = implementationResultReason(err)
	}
	debugJSON(ctx, "model_result", map[string]any{"outcome": outcome(err), "error_code": implementationErrorCode(err), "rejection_reason": reason, "usage": usage})
	return d, usage, err
}

// Explain what survived chat context compaction without changing its budget.
func debugChatContext(ctx context.Context, session domain.Session, received []evidence, input string) {
	if debugTrace(ctx) == nil {
		return
	}
	var sent struct {
		History  []domain.Message `json:"recentHistory"`
		Evidence []evidence       `json:"evidence"`
		Findings json.RawMessage  `json:"reviewFindings"`
		Draft    json.RawMessage  `json:"implementationDraft"`
	}
	if json.Unmarshal([]byte(input), &sent) != nil {
		return
	}
	sizes := func(items []evidence) []map[string]any {
		out := []map[string]any{}
		for _, e := range items {
			out = append(out, map[string]any{"read": e.Read, "bytes": len(e.Content)})
		}
		return out
	}
	history := []map[string]any{}
	for _, m := range sent.History {
		history = append(history, map[string]any{"id": m.ID, "role": m.Role, "bytes": len(m.Text)})
	}
	debugJSON(ctx, "chat_context", map[string]any{"history_messages_total": len(session.Messages), "history_sent": history, "evidence_received": sizes(received), "evidence_sent": sizes(sent.Evidence), "review_findings_included": len(sent.Findings) > 0, "implementation_draft_included": len(sent.Draft) > 0, "serialized_bytes": len(input)})
}
