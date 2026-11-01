package domain

import "strings"

// GeminiEfforts returns supported thinking levels for Gemini 3 and later.
// Older Gemini models use their existing default configuration.
func GeminiEfforts(model string) []string {
	if !strings.HasPrefix(model, "gemini-") || strings.HasPrefix(model, "gemini-1") || strings.HasPrefix(model, "gemini-2") {
		return nil
	}
	if model == "gemini-3-pro-preview" {
		return []string{"low", "high"}
	}
	if model == "gemini-3.1-flash-lite-image" {
		return []string{"minimal", "high"}
	}
	if strings.Contains(model, "flash-lite") || model == "gemini-3-flash-preview" || model == "gemini-3.6-flash" {
		return []string{"minimal", "low", "medium", "high"}
	}
	return []string{"low", "medium", "high"}
}

func DefaultEffort(levels []string) string {
	for _, level := range levels {
		if level == "medium" {
			return level
		}
	}
	if len(levels) > 0 {
		return levels[0]
	}
	return ""
}

func ValidEffort(levels []string, effort string) bool {
	if effort == "" {
		return true
	}
	for _, level := range levels {
		if effort == level {
			return true
		}
	}
	return false
}
