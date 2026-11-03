package agent

import (
	"context"
	"errors"
	"strings"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type IntentPlan struct {
	Goal          string `json:"goal"`
	Investigation string `json:"investigation"`
	Response      string `json:"response"`
}

type Decision struct {
	IntentIssue string               `json:"-"`
	Intent      IntentPlan           `json:"intent"`
	Answer      string               `json:"answer"`
	Summary     string               `json:"summary"`
	Read        *github.Read         `json:"read"`
	Search      *github.CodeSearch   `json:"search"`
	Review      *domain.ReviewResult `json:"review"`
	StartReview *ReviewRequest       `json:"startReview"`
	Priorities  []PriorityAssessment `json:"priorities,omitempty"`
	// Implementation workflow.
	StartImplementation *ImplementRequest                  `json:"startImplementation"`
	Plan                *domain.ImplementationPlan         `json:"plan"`
	Implementation      *ImplementResult                   `json:"implementation"`
	Verification        *domain.ImplementationVerification `json:"verification"`
}
type Usage struct {
	Phase        string `json:"-"`
	DurationMS   int64  `json:"-"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
}
type Provider interface {
	Next(context.Context, string, string, string) (Decision, Usage, error)
}

// intentLengthRule asks providers whose structured output cannot bound string
// lengths to keep the intent fields short.
const intentLengthRule = "\nintentのgoal・investigation・responseはそれぞれ空にせず、各100文字以内で簡潔に記載してください。"

// checkDecisionInput rejects inputs that exceed the request limits before any
// provider is called.
func checkDecisionInput(input string) error {
	if len(input) > aiInputLimit(input) {
		return errors.New("ai_input_limit")
	}
	if reviewWorkflow(input) && !reviewInputFits([]byte(input)) {
		return errors.New("ai_input_limit")
	}
	return nil
}

// readObjectSchema is the single typed GitHub read a decision may request. A
// read decision is interpreted locally: the AI cannot select endpoints,
// credentials, repositories or write tools.
func readObjectSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"kind", "path", "number", "detail"}, "properties": map[string]any{
		"kind": map[string]any{"type": "string", "enum": []string{"repository", "file", "pull_request", "issue", "pull_requests", "issues"}}, "path": map[string]any{"type": "string"}, "number": map[string]any{"type": "integer"}, "detail": map[string]any{"type": "string", "enum": []string{"summary", "files", "comments"}},
	}}
}

// nullableReadSchema expresses an optional read as anyOf null or the object,
// which OpenAI and Claude structured output accept.
func nullableReadSchema() map[string]any {
	return map[string]any{"anyOf": []any{map[string]any{"type": "null"}, readObjectSchema()}}
}

// decisionSchema builds the full structured-output schema for one decision,
// including the review and implementation workflow fields.
func decisionSchema(read map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"intent", "answer", "summary", "read"}, "properties": map[string]any{
		"intent": intentSchema(), "answer": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}, "read": read,
	}}
	addReviewSchema(schema)
	addImplementationSchema(schema)
	return schema
}

// decisionOutputTokens is the answer token budget for the workflow in input.
func decisionOutputTokens(input string) int {
	if implementWorkflow(input) {
		return ImplementOutputToks
	}
	if reviewWorkflow(input) {
		return 8192
	}
	return 4096
}

// answerTooLong reports whether the answer or summary exceeds the stored limits.
func answerTooLong(d Decision) bool {
	return len(d.Answer) > 16*1024 || len(d.Summary) > 2048
}

// checkDecision validates a decoded decision for providers that tolerate an
// unusable intent (Gemini and Claude). Errors use the provider's prefix so
// existing error codes stay unchanged.
func checkDecision(input string, d *Decision, prefix string) error {
	if answerTooLong(*d) {
		return errors.New(prefix + "_answer_too_long")
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
		return errors.New(prefix + "_invalid_review")
	}
	if !validReviewAction(*d) {
		return errors.New(prefix + "_invalid_review_action")
	}
	return checkDecisionWorkflow(input, *d)
}

// checkDecisionWorkflow applies the checks every provider shares: the read
// request, priority results and the implementation response contract.
func checkDecisionWorkflow(input string, d Decision) error {
	if d.Read != nil {
		if err := d.Read.Validate(); err != nil {
			return err
		}
	}
	if !validPriorityDecision(input, d) {
		return errors.New("invalid_priority_result")
	}
	if issue := implementationResponseIssue(input, d); issue != "" {
		return newImplementationResultError(issue, d)
	}
	return nil
}

func intentSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": false, "required": []string{"goal", "investigation", "response"}, "properties": map[string]any{"goal": map[string]any{"type": "string"}, "investigation": map[string]any{"type": "string"}, "response": map[string]any{"type": "string"}}}
}
func validIntent(p IntentPlan) bool {
	return strings.TrimSpace(p.Goal) != "" && strings.TrimSpace(p.Investigation) != "" && strings.TrimSpace(p.Response) != "" && len(p.Goal) <= 1024 && len(p.Investigation) <= 1024 && len(p.Response) <= 1024
}
