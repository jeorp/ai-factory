package agent

import (
	"context"
	"errors"
)

// Never log provider error messages, paths embedded in errors, or response text.
func implementationErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	for errors.Unwrap(err) != nil {
		err = errors.Unwrap(err)
	}
	switch code := err.Error(); code {
	case "ai_input_limit", "ai_request_limit", "implementation_call_limit", "implementation_edit_failed", "implementation_plan_failed", "implementation_verification_failed", "implementation_changes_expired", "invalid_implementation_result", "credential_echo_blocked",
		"gemini_invalid_output", "gemini_incomplete", "gemini_response_limit", "gemini_answer_too_long", "gemini_invalid_review", "gemini_invalid_review_action", "gemini_unavailable", "gemini_key_invalid", "gemini_api_disabled", "gemini_bad_request", "gemini_precondition",
		"openai_invalid_output", "openai_unavailable", "openai_incomplete", "openai_response_limit", "openai_output_limit",
		"gemini_http_400", "gemini_http_401", "gemini_http_403", "gemini_http_404", "gemini_http_429", "gemini_http_500", "gemini_http_502", "gemini_http_503",
		"openai_http_400", "openai_http_401", "openai_http_403", "openai_http_404", "openai_http_429", "openai_http_500", "openai_http_502", "openai_http_503",
		"branch_moved", "revision_action_changed", "invalid_plan", "plan_file_limit", "invalid_plan_dependencies", "plan_dependency_group_limit", "invalid_implementation_search", "implementation_search_unavailable", "plan_path_mismatch", "github_mcp_read_failed", "github_unavailable", "change_path_duplicate", "invalid_change_action", "invalid_change_path", "protected_change_path", "binary_or_generated_file_excluded",
		"edit_empty", "edit_search_empty", "edit_search_not_found", "edit_search_ambiguous", "change_path_not_in_step", "change_action_not_planned", "create_requires_content", "update_requires_edits", "update_without_change", "change_set_empty", "change_set_size_limit", "change_contains_secret", "change_file_size_limit", "change_store_failed":
		return code
	}
	return "implementation_error"
}

func (run *implementRun) retry(phase string, attempt int, retries *int, err error) {
	*retries = *retries + 1
	run.logger.Info("implement_retry", "phase", phase, "step", run.impl.NextStep+1, "attempt", attempt, "retry_count", *retries, "error_code", implementationErrorCode(err))
}
