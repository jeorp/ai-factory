package agent

import (
	"ai-factory/backend/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Gemini uses the Gemini Developer API; the user supplies the API key for each run.
type Gemini struct {
	HTTP     *http.Client
	Endpoint string
}

func (p *Gemini) Next(ctx context.Context, key, model, input string) (Decision, Usage, error) {
	if len(input) > aiInputLimit(input) {
		return Decision{}, Usage{}, errors.New("ai_input_limit")
	}
	if reviewWorkflow(input) && !reviewInputFits([]byte(input)) {
		return Decision{}, Usage{}, errors.New("ai_input_limit")
	}
	read := map[string]any{"type": []string{"object", "null"}, "properties": map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"repository", "file", "pull_request", "issue", "pull_requests", "issues"}},
		"path": map[string]any{"type": "string"}, "number": map[string]any{"type": "integer"}, "detail": map[string]any{"type": "string", "enum": []string{"summary", "files", "comments"}},
	}, "required": []string{"kind", "path", "number", "detail"}, "additionalProperties": false}
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
	config := geminiGenerationConfig(model, maxOutput, schema)
	levels := domain.GeminiEfforts(model)
	if effort := selectedEffort(ctx); effort != "" {
		if !domain.ValidEffort(levels, effort) {
			return Decision{}, Usage{}, errors.New("effort_not_allowed")
		}
		config["thinkingConfig"] = map[string]any{"thinkingLevel": effort}
	}
	body, err := json.Marshal(map[string]any{"systemInstruction": map[string]any{"parts": []any{map[string]string{"text": instructions}}}, "contents": []any{map[string]any{"role": "user", "parts": []any{map[string]string{"text": input}}}}, "generationConfig": config})
	if err != nil || len(body) > aiRequestLimit(input) {
		return Decision{}, Usage{}, errors.New("ai_request_limit")
	}
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":generateContent"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Decision{}, Usage{}, errors.New("gemini_unavailable")
	}
	req.Header.Set("x-goog-api-key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return Decision{}, Usage{}, errors.New("gemini_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Decision{}, Usage{}, geminiError(resp)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(aiResponseLimit(input))+1))
	if err != nil || len(raw) > aiResponseLimit(input) {
		return Decision{}, Usage{}, errors.New("gemini_response_limit")
	}
	var out struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Parts []struct {
					Text    string `json:"text"`
					Thought bool   `json:"thought"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Usage struct {
			Input    int `json:"promptTokenCount"`
			Output   int `json:"candidatesTokenCount"`
			Thoughts int `json:"thoughtsTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(raw, &out) != nil {
		return Decision{}, Usage{}, errors.New("gemini_invalid_output")
	}
	usage := Usage{InputTokens: out.Usage.Input, OutputTokens: out.Usage.Output + out.Usage.Thoughts}
	if len(out.Candidates) != 1 || out.Candidates[0].FinishReason != "STOP" {
		return Decision{}, usage, errors.New("gemini_incomplete")
	}
	var text strings.Builder
	for _, part := range out.Candidates[0].Content.Parts {
		if !part.Thought {
			text.WriteString(part.Text)
		}
	}
	var d Decision
	if json.Unmarshal([]byte(text.String()), &d) != nil {
		return d, usage, errors.New("gemini_invalid_output")
	}
	if len(d.Answer) > 16*1024 || len(d.Summary) > 2048 {
		return d, usage, errors.New("gemini_answer_too_long")
	}
	if !validIntent(d.Intent) {
		d.IntentIssue = "empty_fields"
		if len(d.Intent.Goal) > 1024 || len(d.Intent.Investigation) > 1024 || len(d.Intent.Response) > 1024 {
			d.IntentIssue = "field_length_limit"
		}
		// Intent is optional internal planning metadata, not the decision itself.
		// Discard an unusable hypothesis; the chat runner retains its last valid
		// one, and specialized workflows do not depend on it.
		d.Intent = IntentPlan{}
	}
	if !validReviewResult(d.Review) {
		return d, usage, errors.New("gemini_invalid_review")
	}
	if !validReviewAction(d) {
		return d, usage, errors.New("gemini_invalid_review_action")
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
		return d, usage, newImplementationResultError(issue, d)
	}
	return d, usage, nil
}

// geminiThinkingHeadroom is added to maxOutputTokens because Gemini counts
// thinking tokens toward that limit. Without it, models that think by
// default (Pro and newer Flash models) stop with MAX_TOKENS before answering.
const geminiThinkingHeadroom = 8192

// geminiGenerationConfig uses medium thinking for Gemini 3 and later across
// chat, review and implementation. Gemini 2.x keeps its default thinking
// configuration and the extra output headroom.
func geminiGenerationConfig(model string, answerTokens int, schema map[string]any) map[string]any {
	config := map[string]any{"maxOutputTokens": answerTokens + geminiThinkingHeadroom, "responseMimeType": "application/json", "responseJsonSchema": schema}
	if levels := domain.GeminiEfforts(model); len(levels) > 0 {
		config["thinkingConfig"] = map[string]any{"thinkingLevel": domain.DefaultEffort(levels)}
	}
	return config
}

// Only return fixed error codes. Never expose Google's error message or request data.
func geminiError(resp *http.Response) error {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err == nil {
		var result struct {
			Error struct {
				Status  string `json:"status"`
				Details []struct {
					Reason string `json:"reason"`
					Type   string `json:"@type"`
				} `json:"details"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &result) == nil {
			for _, detail := range result.Error.Details {
				switch detail.Reason {
				case "API_KEY_INVALID":
					return errors.New("gemini_key_invalid")
				case "API_KEY_SERVICE_BLOCKED", "SERVICE_DISABLED":
					return errors.New("gemini_api_disabled")
				}
				if detail.Type == "type.googleapis.com/google.rpc.BadRequest" {
					return errors.New("gemini_bad_request")
				}
			}
			if result.Error.Status == "FAILED_PRECONDITION" {
				return errors.New("gemini_precondition")
			}
		}
	}
	return fmt.Errorf("gemini_http_%d", resp.StatusCode)
}
