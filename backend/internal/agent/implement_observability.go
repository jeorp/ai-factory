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
	case "ai_token_budget_exceeded", "implementation_no_change_candidates", "invalid_no_change_candidate", "gemini_timeout", "ai_input_limit", "ai_request_limit", "implementation_call_limit", "implementation_edit_failed", "implementation_edit_blocked", "implementation_ci_forbidden", "implementation_ci_unavailable", "implementation_ci_incomplete", "implementation_ci_pending", "implementation_ci_not_failed", "implementation_verification_incomplete", "verification_read_not_allowed", "invalid_acceptance_criteria", "implementation_plan_failed", "implementation_verification_failed", "implementation_changes_expired", "invalid_implementation_result", "credential_echo_blocked",
		"documentation_call_limit", "invalid_documentation_result",
		"gemini_invalid_output", "gemini_incomplete", "gemini_response_limit", "gemini_answer_too_long", "gemini_invalid_review", "gemini_invalid_review_action", "gemini_unavailable", "gemini_key_invalid", "gemini_api_disabled", "gemini_bad_request", "gemini_precondition",
		"openai_invalid_output", "openai_unavailable", "openai_incomplete", "openai_response_limit", "openai_output_limit", "invalid_priority_result", "read_tool_not_allowed", "invalid_file_path", "invalid_number", "invalid_read_detail",
		"gemini_http_400", "gemini_http_401", "gemini_http_403", "gemini_http_404", "gemini_http_429", "gemini_http_500", "gemini_http_502", "gemini_http_503",
		"claude_invalid_output", "claude_incomplete", "claude_refusal", "claude_response_limit", "claude_answer_too_long", "claude_invalid_review", "claude_invalid_review_action", "claude_unavailable", "claude_token_invalid", "claude_token_expired", "claude_permission_denied", "claude_model_unavailable",
		"anthropic_key_invalid", "anthropic_permission_denied", "anthropic_rate_limited", "claude_http_400", "claude_http_404", "claude_http_429", "claude_http_500", "claude_http_502", "claude_http_503", "claude_http_529",
		"openai_http_400", "openai_http_401", "openai_http_403", "openai_http_404", "openai_http_429", "openai_http_500", "openai_http_502", "openai_http_503",
		"branch_moved", "revision_action_changed", "invalid_plan", "plan_file_limit", "invalid_plan_dependencies", "plan_dependency_group_limit", "invalid_implementation_search", "implementation_search_unavailable", "plan_path_mismatch", "github_http_401", "github_http_403", "github_http_404", "github_response_limit_100kib", "invalid_github_response", "job_log_redirect_invalid", "github_mcp_read_failed", "github_unavailable", "change_path_duplicate", "invalid_change_action", "invalid_change_path", "protected_change_path", "binary_or_generated_file_excluded",
		"edit_line_range_invalid", "edit_line_anchor_mismatch", "edit_line_overlap", "edit_source_mismatch", "update_format_conflict", "edit_empty", "edit_search_empty", "edit_search_not_found", "edit_search_ambiguous", "change_path_not_in_step", "change_action_not_planned", "create_requires_content", "update_requires_edits", "update_without_change", "change_set_empty", "change_set_size_limit", "change_contains_secret", "change_file_size_limit", "change_store_failed":
		return code
	}
	return "implementation_error"
}

func (run *implementRun) retry(phase string, attempt int, retries *int, err error) {
	*retries = *retries + 1
	run.logger.Info("implement_retry", "phase", phase, "step", run.impl.NextStep+1, "attempt", attempt, "retry_count", *retries, "error_code", implementationErrorCode(err))
}
