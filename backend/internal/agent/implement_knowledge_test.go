package agent

import (
	"context"
	"encoding/json"
	"errors"
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

func TestVerificationKnowledgeFromAdditionalEvidenceDoesNotImplyFullCoverage(t *testing.T) {
	for _, name := range []string{"complete read", "clipped read", "missing read", "search match", "empty search", "failed search"} {
		t.Run(name, func(t *testing.T) {
			base := newFakeWriter()
			const path = "lib/check.py"
			if name != "missing read" {
				base.files[path] = "def check(value):\n    return value is None\n"
				if name == "clipped read" {
					base.files[path] += strings.Repeat("# padding\n", 2000)
				}
			}
			writer := searchWriter{fakeWriter: base, search: func(context.Context, string, string, string, github.CodeSearch) (github.CodeSearchResult, error) {
				if name == "failed search" {
					return github.CodeSearchResult{Matches: []github.SearchMatch{{Path: path, Text: "discarded partial result"}}}, errors.New("github_unavailable")
				}
				result := github.CodeSearchResult{}
				if name == "search match" {
					result.Matches = []github.SearchMatch{{Path: path, Line: 1, Text: "def check(value):"}}
				}
				return result, nil
			}}
			session := verificationSession(base, []domain.FileChange{{Path: "docs/usage.md", Action: "create", Content: "Usage"}})
			calls := 0
			ai := providerFunc(func(_ context.Context, _, _, input string) (Decision, Usage, error) {
				calls++
				var body struct {
					Knowledge *verificationKnowledge `json:"verificationKnowledge"`
					Evidence  []evidence             `json:"evidence"`
				}
				if err := json.Unmarshal([]byte(input), &body); err != nil {
					t.Fatal(err)
				}
				if calls == 1 {
					if body.Knowledge != nil {
						t.Fatal("unread repository inventory selected knowledge")
					}
					if strings.Contains(name, "search") {
						return Decision{Search: &github.CodeSearch{Query: "check", Prefix: "lib"}}, Usage{}, nil
					}
					return Decision{Read: &github.Read{Kind: "file", Path: path, Detail: "summary"}}, Usage{}, nil
				}
				wantKnowledge := name == "complete read" || name == "clipped read" || name == "search match"
				if wantKnowledge {
					if body.Knowledge == nil || len(body.Knowledge.Languages) != 1 || body.Knowledge.Languages[0].Language != "python" || !reflect.DeepEqual(body.Knowledge.Languages[0].Paths, []string{path}) {
						t.Fatalf("visible Python evidence lacks knowledge: %+v", body.Knowledge)
					}
				} else if body.Knowledge != nil {
					t.Fatal("unavailable code selected knowledge")
				}
				if name == "clipped read" && (len(body.Evidence) != 1 || !strings.Contains(body.Evidence[0].Content, "全文未確認")) {
					t.Fatal("clipped evidence lost its incomplete marker")
				}
				v := passedVerification(implementTaskOf(t, input).Plan)
				v.Criteria[0].Paths = append(v.Criteria[0].Paths, path)
				return Decision{Summary: "追加コードを確認", Verification: v}, Usage{}, nil
			})
			var last Checkpoint
			if err := (&Runner{AI: ai, Writer: writer}).Run(context.Background(), Input{Session: session}, func(cp Checkpoint) error { last = cp; return nil }); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("unexpected model calls: %d", calls)
			}
			if name == "complete read" {
				if last.Implementation.Status != "ready" {
					t.Fatalf("full read did not establish coverage: %+v", last.Implementation.Verification)
				}
			} else if last.Implementation.Status != "verification_blocked" || last.Implementation.CanPublish() || !strings.Contains(strings.Join(last.Implementation.Verification.Unverified, ""), path) {
				t.Fatal("partial or missing evidence was accepted as full coverage")
			}
		})
	}
}
