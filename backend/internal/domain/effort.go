package domain

import "strings"

// GeminiEfforts returns supported thinking levels for Gemini 3 and later.
// Older Gemini models use their existing default configuration.
func GeminiEfforts(model string) []string {
	levels, _ := geminiEfforts(model)
	return levels
}

// geminiEfforts also reports whether the model's levels are explicitly known.
// Unlisted Gemini 3+ models are assumed to accept low/medium/high, but only
// low is known to work across all of them.
func geminiEfforts(model string) ([]string, bool) {
	if !strings.HasPrefix(model, "gemini-") || strings.HasPrefix(model, "gemini-1") || strings.HasPrefix(model, "gemini-2") {
		return nil, false
	}
	if model == "gemini-3-pro-preview" {
		return []string{"low", "high"}, true
	}
	if model == "gemini-3.1-flash-lite-image" {
		return []string{"minimal", "high"}, true
	}
	if strings.Contains(model, "flash-lite") || model == "gemini-3-flash-preview" || model == "gemini-3.6-flash" {
		return []string{"minimal", "low", "medium", "high"}, true
	}
	return []string{"low", "medium", "high"}, false
}

// DefaultEffort picks medium for models whose levels are known. Unlisted
// Gemini models keep the previous fixed default, low, because medium is not
// supported by every Gemini 3+ model and an unsupported level fails every call.
func DefaultEffort(model string, levels []string) string {
	if len(levels) == 0 {
		return ""
	}
	if _, known := geminiEfforts(model); !known && strings.HasPrefix(model, "gemini-") && containsEffort(levels, "low") {
		return "low"
	}
	if containsEffort(levels, "medium") {
		return "medium"
	}
	return levels[0]
}

func containsEffort(levels []string, effort string) bool {
	for _, level := range levels {
		if level == effort {
			return true
		}
	}
	return false
}

func ValidEffort(levels []string, effort string) bool {
	return effort == "" || containsEffort(levels, effort)
}
