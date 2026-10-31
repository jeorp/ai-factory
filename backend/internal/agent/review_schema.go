package agent

import "ai-factory/backend/internal/domain"

func addReviewSchema(schema map[string]any) {
	props := map[string]any{}
	names := []string{"severity", "path", "line", "side", "title", "problem", "impact", "evidence", "suggestion"}
	for _, name := range names {
		props[name] = map[string]any{"type": "string"}
	}
	props["line"] = map[string]any{"type": "integer"}
	props["severity"] = map[string]any{"type": "string", "enum": []string{"P0", "P1", "P2", "P3"}}
	props["side"] = map[string]any{"type": "string", "enum": []string{"RIGHT", "LEFT"}}
	result := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"findings", "unverified"}, "properties": map[string]any{
		"findings":   map[string]any{"type": "array", "items": map[string]any{"type": "object", "additionalProperties": false, "required": names, "properties": props}},
		"unverified": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}}
	schema["properties"].(map[string]any)["review"] = map[string]any{"anyOf": []any{map[string]any{"type": "null"}, result}}
	schema["required"] = append(schema["required"].([]string), "review")
}
func validReviewResult(r *domain.ReviewResult) bool {
	if r == nil {
		return true
	}
	if len(r.Findings) > 5 || len(r.Unverified) > 10 {
		return false
	}
	for _, f := range r.Findings {
		if f.Severity != "P0" && f.Severity != "P1" && f.Severity != "P2" && f.Severity != "P3" {
			return false
		}
		if f.Side != "RIGHT" && f.Side != "LEFT" {
			return false
		}
		if f.Line < 0 || f.Line > 10000000 || f.Path == "" || len(f.Path) > 512 || f.Title == "" || len(f.Title) > 200 || f.Problem == "" || len(f.Problem) > 800 || f.Impact == "" || len(f.Impact) > 500 || f.Evidence == "" || len(f.Evidence) > 800 || len(f.Suggestion) > 500 {
			return false
		}
	}
	for _, u := range r.Unverified {
		if len(u) > 512 {
			return false
		}
	}
	return true
}
