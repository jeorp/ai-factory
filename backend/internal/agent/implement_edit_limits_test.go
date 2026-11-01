package agent

import (
	"ai-factory/backend/internal/domain"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestEditSchemaEnforcesFormat(t *testing.T) {
	for _, format := range []string{"", lineRangeFormat} {
		task := implementTask{Mode: "edit", EditFormat: format}
		schema := map[string]any{"properties": map[string]any{}}
		implementInstructions(schema, task)
		impl := schema["properties"].(map[string]any)["implementation"].(map[string]any)["anyOf"].([]any)[1].(map[string]any)
		change := impl["properties"].(map[string]any)["changes"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		inactive := "lineEdits"
		c := ChangeProposal{LineEdits: []domain.LineEdit{{}}}
		if format == lineRangeFormat {
			inactive = "edits"
			c = ChangeProposal{Edits: []domain.Edit{{}}}
		}
		if change[inactive].(map[string]any)["maxItems"] != 0 {
			t.Fatal("inactive format allowed")
		}
		body, _ := json.Marshal(map[string]any{"implementTask": task})
		if issue := implementationResponseIssue(string(body), Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{c}}}); issue != "edit_format_mismatch" {
			t.Fatalf("wrong format accepted: %s", issue)
		}
	}
}

func TestMissingSearchThenOversizedAnchorRecovers(t *testing.T) {
	w := newRevisionWriter()
	original := "package src\n\nfunc A() int { return 1 }\n// untouched 日本語\n"
	w.files["src/a.go"] = original
	impl := &domain.Implementation{Status: "editing", BaseBranch: "main", BaseCommit: implBase, CacheID: "0123456789abcdef", Plan: &domain.ImplementationPlan{AcceptanceCriteria: testCriteria("src/a.go"), Summary: "Aを修正", Files: []domain.PlannedFile{{Path: "src/a.go", Action: "update"}}}, Steps: []domain.ImplementationStep{{Files: []string{"src/a.go"}}}}
	calls, checks := 0, 0
	var logs bytes.Buffer
	r := &Runner{Writer: w, AI: providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		task := implementTaskOf(t, input)
		if task.Mode == "verify" {
			checks++
			return Decision{Verification: passedVerification(impl.Plan), Summary: "確認済み"}, Usage{}, nil
		}
		calls++
		if len(w.versions) != 0 {
			t.Fatal("failed edit persisted")
		}
		if calls == 1 {
			return Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", Edits: []domain.Edit{{Search: "missing", Replace: "wrong"}}}}}}, Usage{}, nil
		}
		if task.EditFormat != lineRangeFormat {
			t.Fatal("lost recovery format")
		}
		anchor := "func A() int { return 1 }"
		if calls == 2 {
			anchor = strings.Repeat("秘", 1366)
		}
		if calls == 3 && (!strings.Contains(task.Correction, "expectedStart") || !strings.Contains(task.Correction, "4098") || !strings.Contains(task.Correction, "4096") || !strings.Contains(task.Correction, "原文1行")) {
			t.Fatalf("unhelpful correction: %s", task.Correction)
		}
		d := Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{{Path: "src/a.go", Action: "update", SourceHash: task.Files[0].SourceHash, LineEdits: []domain.LineEdit{{StartLine: 3, EndLine: 3, ExpectedStart: anchor, ExpectedEnd: "func A() int { return 1 }", Content: "func A() int { return 2 }"}}}}}}
		if issue := implementationResponseIssue(input, d); issue != "" {
			return d, Usage{}, newImplementationResultError(issue, d)
		}
		return d, Usage{}, nil
	})}
	s := baseSession()
	s.RunKind, s.Implementation = "implement", impl
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s, Logger: slog.New(slog.NewJSONHandler(&logs, nil))}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil || calls != 3 || checks != 1 || last.Implementation.Status != "ready" {
		t.Fatalf("failed recovery: %v calls=%d checks=%d impl=%+v", err, calls, checks, last.Implementation)
	}
	f, ok := w.GetChange(context.Background(), impl.CacheID, impl.Version, "src/a.go")
	if !ok || f.Content != strings.Replace(original, "return 1", "return 2", 1) || len(w.published) != 0 {
		t.Fatal("wrong saved result")
	}
	for _, want := range []string{`"field":"expectedStart"`, `"actual_bytes":4098`, `"limit_bytes":4096`, `"file_index":1`, `"edit_index":1`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("missing diagnostic %s", want)
		}
	}
	if strings.Contains(logs.String(), "秘") {
		t.Fatal("logs leaked model text")
	}
}

func TestEditTextLimitBoundaries(t *testing.T) {
	for _, tc := range []struct {
		field string
		limit int
	}{{"expectedStart", 4096}, {"expectedEnd", 4096}, {"lineEdits.content", 65536}, {"search", 32768}, {"replace", 65536}} {
		t.Run(tc.field, func(t *testing.T) {
			for _, extra := range []int{0, 1} {
				text := strings.Repeat("x", tc.limit+extra)
				c := ChangeProposal{Path: "src/a.go", Action: "update"}
				switch tc.field {
				case "expectedStart":
					c.LineEdits = []domain.LineEdit{{ExpectedStart: text}}
				case "expectedEnd":
					c.LineEdits = []domain.LineEdit{{ExpectedEnd: text}}
				case "lineEdits.content":
					c.LineEdits = []domain.LineEdit{{Content: text}}
				case "search":
					c.Edits = []domain.Edit{{Search: text}}
				case "replace":
					c.Edits = []domain.Edit{{Replace: text}}
				}
				d := Decision{Implementation: &ImplementResult{Changes: []ChangeProposal{c}}}
				issue := implementationDecisionIssue(d)
				if extra == 0 {
					if issue != "" {
						t.Fatalf("boundary rejected: %s", issue)
					}
					continue
				}
				e := newImplementationResultError(issue, d)
				if issue != "edit_text_limit" || e.limit == nil || e.limit.Field != tc.field || e.limit.Bytes != tc.limit+1 || e.limit.Limit != tc.limit {
					t.Fatalf("bad diagnostic: %+v", e)
				}
			}
		})
	}
}
