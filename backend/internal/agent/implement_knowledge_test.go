package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func TestVerificationKnowledgeSelection(t *testing.T) {
	cases := []struct {
		name  string
		paths []string
		want  map[string][]string
	}{
		{"documentation only", []string{"README.md", "docs/go.md", "data.json", "lib/main.rs"}, nil},
		{"deduplicated Go", []string{"z.go", "a.go", "z.go"}, map[string][]string{"go": {"a.go", "z.go"}}},
		{"JS and TS variants", []string{"a.jsx", "b.tsx", "c.mjs", "d.cjs", "e.mts", "f.cts", "g.d.ts", "h.js", "i.ts"}, map[string][]string{"javascript-typescript": {"a.jsx", "b.tsx", "c.mjs", "d.cjs", "e.mts", "f.cts", "g.d.ts", "h.js", "i.ts"}}},
		{"Python", []string{"app.py", "typing.pyi"}, map[string][]string{"python": {"app.py", "typing.pyi"}}},
		{"mixed", []string{"use.ts", "README.md", "model.go", "script.py"}, map[string][]string{"go": {"model.go"}, "javascript-typescript": {"use.ts"}, "python": {"script.py"}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			knowledge := knowledgeForVerification(c.paths)
			if c.want == nil {
				if knowledge != nil {
					t.Fatalf("unexpected knowledge: %+v", knowledge)
				}
				return
			}
			if knowledge == nil || knowledge.Version == "" || !strings.Contains(knowledge.Common, "ERROR-PATH") {
				t.Fatalf("missing shared guidance: %+v", knowledge)
			}
			got := map[string][]string{}
			for _, lang := range knowledge.Languages {
				if lang.Guidance == "" || !strings.Contains(lang.Guidance, "https://") {
					t.Fatal("registered knowledge lacks content or sources")
				}
				got[lang.Language] = lang.Paths
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
			raw, _ := json.Marshal(knowledge)
			if len(raw) > 10000 {
				t.Fatalf("knowledge adds excessive input: %d bytes", len(raw))
			}
		})
	}
}

func TestVerificationKnowledgeStableAcrossPathOrder(t *testing.T) {
	first := knowledgeForVerification([]string{"use.ts", "script.py", "main.go"})
	second := knowledgeForVerification([]string{"main.go", "use.ts", "main.go", "script.py"})
	if !reflect.DeepEqual(first, second) {
		t.Fatal("knowledge depends on input order or duplicate paths")
	}
}

func TestVerificationUsesKnowledgeInBatchesCrossCheckAndAdditionalReads(t *testing.T) {
	writer := newFakeWriter()
	writer.files["lib/check.py"] = "def check(value):\n    return value is None\n"
	files := []domain.FileChange{{Path: "src/a.go", Action: "update", Content: "package fixture\nfunc A() int { return 2 }\n"}}
	for _, name := range []string{"web/one.ts", "web/two.ts", "web/three.ts", "web/four.ts"} {
		files = append(files, domain.FileChange{Path: name, Action: "create", Content: strings.Repeat("// 日本語のコメント\n", 1500)})
	}
	session := verificationSession(writer, files)
	calls, partBatches, crossCalls := 0, 0, 0
	readRequested, readAvailable := false, false
	ai := providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		var body struct {
			Task      implementTask          `json:"implementTask"`
			Stage     string                 `json:"stage"`
			Parts     []verificationPart     `json:"parts"`
			Knowledge *verificationKnowledge `json:"verificationKnowledge"`
		}
		if err := json.Unmarshal([]byte(input), &body); err != nil {
			t.Fatal(err)
		}
		if body.Task.Mode != "verify" || body.Knowledge == nil {
			t.Fatal("knowledge absent from verification")
		}
		if !strings.Contains(body.Knowledge.Common, "ERROR-PATH") {
			t.Fatal("shared review method missing")
		}
		langs := map[string]languageVerificationKnowledge{}
		for _, lang := range body.Knowledge.Languages {
			langs[lang.Language] = lang
		}
		if !readRequested {
			if _, ok := langs["python"]; ok {
				t.Fatal("unread language included from repository inventory")
			}
			readRequested = true
			return Decision{Read: &github.Read{Kind: "file", Path: "lib/check.py", Detail: "summary"}}, Usage{}, nil
		}
		if _, ok := langs["python"]; !ok || !reflect.DeepEqual(langs["python"].Paths, []string{"lib/check.py"}) {
			t.Fatal("additional file read did not select Python guidance")
		}
		readAvailable = true
		if body.Stage == "cross" {
			crossCalls++
			if len(body.Parts) != 0 || len(langs) != 3 || langs["go"].Guidance == "" || langs["javascript-typescript"].Guidance == "" {
				t.Fatal("cross-batch review lost language knowledge")
			}
		} else {
			partBatches++
			hasGo, hasTS := false, false
			for _, p := range body.Parts {
				hasGo = hasGo || strings.HasSuffix(p.Path, ".go")
				hasTS = hasTS || strings.HasSuffix(p.Path, ".ts")
			}
			_, goIncluded := langs["go"]
			_, tsIncluded := langs["javascript-typescript"]
			if goIncluded != hasGo || tsIncluded != hasTS {
				t.Fatal("batch includes unrelated language guidance")
			}
		}
		return Decision{Summary: "関数と呼び出しの整合性を確認", Verification: passedVerification(body.Task.Plan)}, Usage{}, nil
	})
	var last Checkpoint
	err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if partBatches < 2 || crossCalls != 1 || !readAvailable || calls != partBatches+crossCalls+1 || last.Implementation.Status != "ready" || len(writer.published) != 0 {
		t.Fatalf("verification flow changed: calls=%d parts=%d cross=%d status=%s", calls, partBatches, crossCalls, last.Implementation.Status)
	}
}

func TestDocumentationVerificationOmitsLanguageKnowledge(t *testing.T) {
	writer := newFakeWriter()
	session := verificationSession(writer, []domain.FileChange{{Path: "docs/usage.md", Action: "create", Content: "Usage"}})
	calls := 0
	ai := providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
		calls++
		var body map[string]json.RawMessage
		if err := json.Unmarshal([]byte(input), &body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["verificationKnowledge"]; exists {
			t.Fatal("documentation-only verification received language guidance")
		}
		return Decision{Summary: "文書の変更を確認", Verification: passedVerification(implementTaskOf(t, input).Plan)}, Usage{}, nil
	})
	var last Checkpoint
	if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || last.Implementation.Status != "ready" {
		t.Fatalf("documentation flow changed: calls=%d status=%s verification=%+v", calls, last.Implementation.Status, last.Implementation.Verification)
	}
}
