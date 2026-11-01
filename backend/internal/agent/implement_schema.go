package agent

import (
	"encoding/json"
)

const ImplementInputTokens = 60000

func implementWorkflow(input string) bool {
	var body struct {
		Task *struct {
			Mode string `json:"mode"`
		} `json:"implementTask"`
	}
	return json.Unmarshal([]byte(input), &body) == nil && body.Task != nil && (body.Task.Mode == "plan" || body.Task.Mode == "edit" || body.Task.Mode == "verify")
}

func implementInputFits(body []byte) bool {
	return len(body) <= ReviewInputBytes && reviewTokens(string(body))+4000 <= ImplementInputTokens
}

func nullable(v map[string]any) map[string]any {
	return map[string]any{"anyOf": []any{map[string]any{"type": "null"}, v}}
}

func stringList() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
}

func object(props map[string]any) map[string]any {
	required := make([]string, 0, len(props))
	for k := range props {
		required = append(required, k)
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": required, "properties": props}
}

// addImplementationSchema adds the workflow fields to every decision. Outside
// an implementTask only startImplementation may be non-null.
func addImplementationSchema(schema map[string]any) {
	props := schema["properties"].(map[string]any)
	props["startImplementation"] = nullable(object(map[string]any{"request": map[string]any{"type": "string"}, "pullNumber": map[string]any{"type": "integer"}}))
	props["plan"] = map[string]any{"type": "null"}
	props["implementation"] = map[string]any{"type": "null"}
	props["verification"] = map[string]any{"type": "null"}
	props["search"] = map[string]any{"type": "null"}
	schema["required"] = append(schema["required"].([]string), "startImplementation", "plan", "implementation", "verification", "search")
}

func implementRead(kinds []string) map[string]any {
	return object(map[string]any{
		"kind":   map[string]any{"type": "string", "enum": kinds},
		"path":   map[string]any{"type": "string"},
		"number": map[string]any{"type": "integer", "enum": []int{0}},
		"detail": map[string]any{"type": "string", "enum": []string{"summary"}},
	})
}

func implementInstructions(schema map[string]any, task implementTask) string {
	props := schema["properties"].(map[string]any)
	props["startReview"] = map[string]any{"type": "null"}
	props["startImplementation"] = map[string]any{"type": "null"}
	props["review"] = map[string]any{"type": "null"}
	props["plan"] = map[string]any{"type": "null"}
	props["implementation"] = map[string]any{"type": "null"}
	if task.Mode == "verify" {
		props["read"] = map[string]any{"type": "null"}
		if !task.FinalAnswer && task.ReadBudget > 0 {
			props["read"] = nullable(implementRead([]string{"file"}))
			props["search"] = nullable(implementationSearchSchema())
		}
		props["answer"] = map[string]any{"type": "string", "enum": []string{""}}
		props["verification"] = nullable(object(map[string]any{
			"issues": stringList(), "unverified": stringList(),
			"criteria": map[string]any{"type": "array", "items": object(map[string]any{
				"id": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"met", "unmet", "unverified"}}, "evidence": map[string]any{"type": "string"}, "paths": stringList(),
			})},
			"repairs":         map[string]any{"type": "array", "items": object(map[string]any{"path": map[string]any{"type": "string"}, "approach": map[string]any{"type": "string"}})},
			"additionalFiles": pendingFilesSchema(),
		}))
		return implementVerifyPrompt + implementProjectDocumentationRule
	}
	if task.FinalAnswer || task.ReadBudget <= 0 {
		props["read"] = map[string]any{"type": "null"}
	} else if task.Mode == "plan" {
		props["read"] = nullable(implementRead([]string{"repository", "file"}))
	} else {
		props["read"] = nullable(implementRead([]string{"file"}))
	}
	if task.Mode == "plan" {
		if !task.FinalAnswer && task.ReadBudget > 0 {
			props["search"] = nullable(implementationSearchSchema())
		}
		props["plan"] = nullable(object(map[string]any{
			"summary": map[string]any{"type": "string"},
			"files": map[string]any{"type": "array", "items": object(map[string]any{
				"path": map[string]any{"type": "string"}, "action": map[string]any{"type": "string", "enum": []string{"create", "update", "delete"}}, "approach": map[string]any{"type": "string"},
				"dependsOn": stringList(),
			})},
			"outOfScope": stringList(), "risks": stringList(), "tests": stringList(),
			"acceptanceCriteria": map[string]any{"type": "array", "items": object(map[string]any{
				"id": map[string]any{"type": "string"}, "requirement": map[string]any{"type": "string"}, "paths": stringList(), "checks": stringList(), "failureRefs": stringList(),
			})},
		}))
		return implementPlanPrompt + implementProjectDocumentationRule
	}
	props["answer"] = map[string]any{"type": "string", "enum": []string{""}}
	lineEdit := object(map[string]any{"startLine": map[string]any{"type": "integer"}, "endLine": map[string]any{"type": "integer"}, "expectedStart": map[string]any{"type": "string"}, "expectedEnd": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}})
	edit := object(map[string]any{"search": map[string]any{"type": "string"}, "replace": map[string]any{"type": "string"}})
	edits := map[string]any{"type": "array", "items": edit}
	lineEdits := map[string]any{"type": "array", "items": lineEdit}
	if task.EditFormat == lineRangeFormat {
		edits["maxItems"] = 0
	} else {
		lineEdits["maxItems"] = 0
	}
	props["implementation"] = nullable(object(map[string]any{
		"changes":         changeItemsSchema(task.Files, edits, lineEdits),
		"unverified":      stringList(),
		"additionalFiles": pendingFilesSchema(),
	}))
	return implementEditPrompt + implementProjectDocumentationRule
}

// changeItemsSchema binds every step path to its planned action. A shared
// action enum would let a mixed create/update/delete step swap operations
// between paths, so each path gets its own variant. The server still checks
// path, action and uniqueness before applying any edit.
func changeItemsSchema(files []implementFile, edits, lineEdits map[string]any) map[string]any {
	noItems := func(items map[string]any) map[string]any {
		return map[string]any{"type": "array", "items": items["items"], "maxItems": 0}
	}
	variant := func(path map[string]any, actions []string) map[string]any {
		e, l, content := edits, lineEdits, map[string]any{"type": "string"}
		if len(actions) == 1 && actions[0] != "update" {
			// create carries full content only; delete carries nothing.
			e, l = noItems(edits), noItems(lineEdits)
			if actions[0] == "delete" {
				content = map[string]any{"type": "string", "enum": []string{""}}
			}
		}
		return object(map[string]any{
			"path": path, "action": map[string]any{"type": "string", "enum": actions},
			"content": content, "edits": e,
			"sourceHash": map[string]any{"type": "string"}, "lineEdits": l,
		})
	}
	if len(files) == 0 {
		return map[string]any{"type": "array", "items": variant(map[string]any{"type": "string"}, []string{"create", "update", "delete"})}
	}
	variants := make([]any, 0, len(files))
	for _, f := range files {
		variants = append(variants, variant(map[string]any{"type": "string", "enum": []string{f.Path}}, []string{f.Action}))
	}
	if len(variants) == 1 {
		return map[string]any{"type": "array", "items": variants[0]}
	}
	return map[string]any{"type": "array", "items": map[string]any{"anyOf": variants}}
}

func implementationSearchSchema() map[string]any {
	return object(map[string]any{"query": map[string]any{"type": "string"}, "prefix": map[string]any{"type": "string"}, "cursor": map[string]any{"type": "string"}})
}

func pendingFilesSchema() map[string]any {
	return map[string]any{"type": "array", "items": object(map[string]any{
		"path": map[string]any{"type": "string"}, "action": map[string]any{"type": "string", "enum": []string{"create", "update", "delete"}},
		"approach": map[string]any{"type": "string"}, "reason": map[string]any{"type": "string"}, "dependsOn": stringList(),
	})}
}
