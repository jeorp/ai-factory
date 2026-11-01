package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/vertex"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Claude calls the Claude API with the user's Anthropic API key, or, with
// Vertex set, Claude on Vertex AI in the operator's GCP project. On Vertex AI
// the user supplies a short-lived Google OAuth access token for each run
// (gcloud auth print-access-token), so the server holds no Google credentials.
type Claude struct {
	HTTP     *http.Client
	Endpoint string
	Vertex   bool
	Project  string
	Region   string
}

// VertexModelPrefix marks Vertex AI models in the model allowlist so the same
// Claude model can be offered through both the Claude API and Vertex AI.
const VertexModelPrefix = "vertex/"

// claudeThinkingHeadroom is added to max_tokens because thinking tokens count
// toward that limit on current Claude models, where thinking is on by default.
const claudeThinkingHeadroom = 8192

func (p *Claude) Next(ctx context.Context, key, model, input string) (Decision, Usage, error) {
	if err := checkDecisionInput(input); err != nil {
		return Decision{}, Usage{}, err
	}
	schema := decisionSchema(nullableReadSchema())
	instructions := decisionInstructions(schema, input) + intentLengthRule
	maxOutput := decisionOutputTokens(input)
	params := anthropic.MessageNewParams{
		Model:        anthropic.Model(strings.TrimPrefix(model, VertexModelPrefix)),
		MaxTokens:    int64(maxOutput + claudeThinkingHeadroom),
		System:       []anthropic.TextBlockParam{{Text: instructions}},
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(input))},
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortLow, Format: anthropic.JSONOutputFormatParam{Schema: claudeOutputSchema(schema)}},
	}
	if body, err := json.Marshal(params); err != nil || len(body) > aiRequestLimit(input) {
		return Decision{}, Usage{}, errors.New("ai_request_limit")
	}
	debugJSON(ctx, "provider_request", params)
	opts := []option.RequestOption{}
	if p.HTTP != nil {
		opts = append(opts, option.WithHTTPClient(p.HTTP))
	}
	if p.Vertex {
		region := p.Region
		if region == "" {
			region = "global"
		}
		creds := &google.Credentials{ProjectID: p.Project, TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: key, TokenType: "Bearer"})}
		opts = append(opts, vertex.WithCredentials(ctx, region, p.Project, creds))
	} else {
		// Only the user's key is used; environment credentials never apply.
		opts = append(opts, option.WithoutEnvironmentDefaults(), option.WithAPIKey(key))
	}
	if p.Endpoint != "" {
		opts = append(opts, option.WithBaseURL(p.Endpoint))
	}
	client := anthropic.NewClient(opts...)
	// Streaming keeps long implementation answers clear of HTTP timeouts.
	stream := client.Messages.NewStreaming(ctx, params)
	message := anthropic.Message{}
	for stream.Next() {
		if err := message.Accumulate(stream.Current()); err != nil {
			return Decision{}, Usage{}, errors.New("claude_invalid_output")
		}
	}
	debugJSON(ctx, "provider_response", message)
	usage := Usage{InputTokens: int(message.Usage.InputTokens + message.Usage.CacheReadInputTokens + message.Usage.CacheCreationInputTokens), OutputTokens: int(message.Usage.OutputTokens)}
	if err := stream.Err(); err != nil {
		return Decision{}, usage, claudeError(err, p.Vertex)
	}
	switch message.StopReason {
	case anthropic.StopReasonEndTurn:
	case anthropic.StopReasonRefusal:
		return Decision{}, usage, errors.New("claude_refusal")
	default:
		return Decision{}, usage, errors.New("claude_incomplete")
	}
	var text strings.Builder
	for _, block := range message.Content {
		if b, ok := block.AsAny().(anthropic.TextBlock); ok {
			text.WriteString(b.Text)
		}
	}
	if text.Len() > aiResponseLimit(input) {
		return Decision{}, usage, errors.New("claude_response_limit")
	}
	var d Decision
	if json.Unmarshal([]byte(text.String()), &d) != nil {
		return d, usage, errors.New("claude_invalid_output")
	}
	if err := checkDecision(input, &d, "claude"); err != nil {
		return d, usage, err
	}
	return d, usage, nil
}

// Only return fixed error codes. Never expose Google's or Anthropic's error message.
func claudeError(err error, onVertex bool) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apierr *anthropic.Error
	if errors.As(err, &apierr) {
		if !onVertex {
			slog.Info("claude_api_error", "http_status", apierr.StatusCode)
			switch apierr.StatusCode {
			case http.StatusUnauthorized:
				return errors.New("anthropic_key_invalid")
			case http.StatusForbidden:
				return errors.New("anthropic_permission_denied")
			case http.StatusTooManyRequests:
				return errors.New("anthropic_rate_limited")
			}
			return fmt.Errorf("claude_http_%d", apierr.StatusCode)
		}
		// Google's status and reason are fixed enums (no request data), so they
		// are safe to log and explain auth failures that share one HTTP status.
		var body struct {
			Error struct {
				Status  string `json:"status"`
				Details []struct {
					Reason string `json:"reason"`
				} `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal([]byte(apierr.RawJSON()), &body)
		reason := ""
		if len(body.Error.Details) > 0 {
			reason = body.Error.Details[0].Reason
		}
		slog.Info("claude_vertex_error", "http_status", apierr.StatusCode, "google_status", body.Error.Status, "reason", reason)
		if reason == "ACCESS_TOKEN_EXPIRED" {
			return errors.New("claude_token_expired")
		}
		switch apierr.StatusCode {
		case http.StatusUnauthorized:
			return errors.New("claude_token_invalid")
		case http.StatusForbidden:
			return errors.New("claude_permission_denied")
		case http.StatusNotFound:
			return errors.New("claude_model_unavailable")
		}
		return fmt.Errorf("claude_http_%d", apierr.StatusCode)
	}
	return errors.New("claude_unavailable")
}

// claudeOutputSchema drops array size limits that Claude structured output
// does not accept (maxItems, and minItems other than 0 or 1). The full schema
// stays in the instructions, and the server validates the same limits after
// the response, so only the decoding constraint is relaxed.
func claudeOutputSchema(v any) map[string]any {
	out, _ := stripArrayLimits(v).(map[string]any)
	return out
}

func stripArrayLimits(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, item := range t {
			if k == "maxItems" {
				continue
			}
			if k == "minItems" {
				if n, ok := item.(int); ok && n > 1 {
					continue
				}
			}
			out[k] = stripArrayLimits(item)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, item := range t {
			out[i] = stripArrayLimits(item)
		}
		return out
	default:
		return v
	}
}
