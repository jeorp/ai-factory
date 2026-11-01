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
	if len(input) > aiInputLimit(input) {
		return Decision{}, Usage{}, errors.New("ai_input_limit")
	}
	if reviewWorkflow(input) && !reviewInputFits([]byte(input)) {
		return Decision{}, Usage{}, errors.New("ai_input_limit")
	}
	read := map[string]any{"anyOf": []any{map[string]any{"type": "null"}, map[string]any{"type": "object", "properties": map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"repository", "file", "pull_request", "issue", "pull_requests", "issues"}},
		"path": map[string]any{"type": "string"}, "number": map[string]any{"type": "integer"}, "detail": map[string]any{"type": "string", "enum": []string{"summary", "files", "comments"}},
	}, "required": []string{"kind", "path", "number", "detail"}, "additionalProperties": false}}}
	schema := map[string]any{"type": "object", "properties": map[string]any{"intent": intentSchema(), "answer": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}, "read": read}, "required": []string{"intent", "answer", "summary", "read"}, "additionalProperties": false}
	addReviewSchema(schema)
	addImplementationSchema(schema)
	instructions := decisionInstructions(schema, input) + "\nintentのgoal・investigation・responseはそれぞれ空にせず、各100文字以内で簡潔に記載してください。"
	maxOutput := 4096
	if reviewWorkflow(input) {
		maxOutput = 8192
	}
	if implementWorkflow(input) {
		maxOutput = ImplementOutputToks
	}
	params := anthropic.MessageNewParams{
		Model:        anthropic.Model(strings.TrimPrefix(model, VertexModelPrefix)),
		MaxTokens:    int64(maxOutput + claudeThinkingHeadroom),
		System:       []anthropic.TextBlockParam{{Text: instructions}},
		Messages:     []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(input))},
		OutputConfig: anthropic.OutputConfigParam{Effort: anthropic.OutputConfigEffortLow, Format: anthropic.JSONOutputFormatParam{Schema: schema}},
	}
	if body, err := json.Marshal(params); err != nil || len(body) > aiRequestLimit(input) {
		return Decision{}, Usage{}, errors.New("ai_request_limit")
	}
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
	if len(d.Answer) > 16*1024 || len(d.Summary) > 2048 {
		return d, usage, errors.New("claude_answer_too_long")
	}
	if !validIntent(d.Intent) {
		d.IntentIssue = "empty_fields"
		if len(d.Intent.Goal) > 1024 || len(d.Intent.Investigation) > 1024 || len(d.Intent.Response) > 1024 {
			d.IntentIssue = "field_length_limit"
		}
		// Intent is optional planning metadata; see the Gemini provider.
		d.Intent = IntentPlan{}
	}
	if !validReviewResult(d.Review) {
		return d, usage, errors.New("claude_invalid_review")
	}
	if !validReviewAction(d) {
		return d, usage, errors.New("claude_invalid_review_action")
	}
	if d.Read != nil {
		if err := d.Read.Validate(); err != nil {
			return d, usage, err
		}
	}
	if !validPriorityDecision(input, d) {
		return d, usage, errors.New("invalid_priority_result")
	}
	if issue := implementationResponseIssue(input, d); issue != "" {
		return d, usage, &implementationResultError{reason: issue}
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
