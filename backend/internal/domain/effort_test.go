package domain

import "testing"

func TestDefaultEffortIsLowForUnlistedGeminiModels(t *testing.T) {
	for model, want := range map[string]string{
		"gemini-3.5-flash-lite":       "medium",
		"gemini-3-flash-preview":      "medium",
		"gemini-3-pro-preview":        "low",
		"gemini-3.1-flash-lite-image": "minimal",
		"gemini-4-argon":              "low",
		"gemini-3.8-flash":            "low",
		"gpt-5":                       "",
	} {
		if got := DefaultEffort(model, GeminiEfforts(model)); got != want {
			t.Errorf("%s: got %q want %q", model, got, want)
		}
	}
}
