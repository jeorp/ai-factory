package agent

import (
	"math/rand/v2"
	"time"
)

// Only transient failures of read-only model requests are retryable.
func transientModelRetryLimit(err error) int {
	switch implementationErrorCode(err) {
	case "gemini_timeout":
		return 1
	case "gemini_unavailable", "openai_unavailable", "claude_unavailable",
		"gemini_http_429", "gemini_http_500", "gemini_http_502", "gemini_http_503",
		"openai_http_429", "openai_http_500", "openai_http_502", "openai_http_503",
		"anthropic_rate_limited", "claude_http_429", "claude_http_500", "claude_http_502", "claude_http_503", "claude_http_529":
		return 2
	}
	return 0
}

func modelRetryDelay(attempt int) time.Duration {
	return time.Second*time.Duration(1<<(attempt-1)) + time.Duration(rand.IntN(250))*time.Millisecond
}
