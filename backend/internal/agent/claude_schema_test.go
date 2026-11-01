package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestClaudeOutputSchemaDropsUnsupportedArrayLimits(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{
		"edits":      map[string]any{"type": "array", "maxItems": 0, "items": map[string]any{"type": "string"}},
		"priorities": map[string]any{"type": "array", "minItems": 3, "maxItems": 3},
		"one":        map[string]any{"type": "array", "minItems": 1},
		"changes":    map[string]any{"type": "array", "items": map[string]any{"anyOf": []any{map[string]any{"type": "array", "maxItems": 0}}}},
	}}
	before, _ := json.Marshal(schema)
	got, _ := json.Marshal(claudeOutputSchema(schema))
	if strings.Contains(string(got), "maxItems") || strings.Contains(string(got), `"minItems":3`) || !strings.Contains(string(got), `"minItems":1`) {
		t.Fatalf("unexpected Claude schema: %s", got)
	}
	if after, _ := json.Marshal(schema); string(after) != string(before) {
		t.Fatal("original schema was modified; the instructions must keep the full limits")
	}
}
