package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type OpenAI struct {
	HTTP     *http.Client
	Endpoint string
}

func (p *OpenAI) Next(ctx context.Context, key, model, input string) (Decision, Usage, error) {
	if err := checkDecisionInput(input); err != nil {
		return Decision{}, Usage{}, err
	}
	schema := decisionSchema(nullableReadSchema())
	instructions := decisionInstructions(schema, input)
	maxOutput := decisionOutputTokens(input)
	body, _ := json.Marshal(map[string]any{"model": model, "store": false, "max_output_tokens": maxOutput, "reasoning": map[string]string{"effort": "minimal"}, "instructions": instructions, "input": input, "text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "github_read_decision", "strict": true, "schema": schema}}})
	if len(body) > aiRequestLimit(input) {
		return Decision{}, Usage{}, errors.New("ai_request_limit")
	}
	debugText(ctx, "provider_request", string(body))
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/responses"
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return Decision{}, Usage{}, errors.New("openai_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Decision{}, Usage{}, fmt.Errorf("openai_http_%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(aiResponseLimit(input))+1))
	if err != nil || len(raw) > aiResponseLimit(input) {
		return Decision{}, Usage{}, errors.New("openai_response_limit")
	}
	debugText(ctx, "provider_response", string(raw))
	var out struct {
		Status string `json:"status"`
		Usage  Usage  `json:"usage"`
		Output []struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Status != "completed" {
		return Decision{}, out.Usage, errors.New("openai_incomplete")
	}
	var text strings.Builder
	for _, o := range out.Output {
		for _, c := range o.Content {
			if c.Type == "output_text" {
				text.WriteString(c.Text)
			}
		}
	}
	var d Decision
	if json.Unmarshal([]byte(text.String()), &d) != nil {
		return d, out.Usage, errors.New("openai_invalid_output")
	}
	if answerTooLong(d) || !validIntent(d.Intent) || !validReviewResult(d.Review) || !validReviewAction(d) {
		return d, out.Usage, errors.New("openai_output_limit")
	}
	if err := checkDecisionWorkflow(input, d); err != nil {
		return d, out.Usage, err
	}
	return d, out.Usage, nil
}
