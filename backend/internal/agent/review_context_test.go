package agent

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func manyFindingsSession(n int, question string) domain.Session {
	s := domain.Session{Target: domain.Target{Repository: "o/r", Kind: "pull", Number: 1}, Review: &domain.Review{Phase: "done"}}
	for i := 0; i < n; i++ {
		s.Review.Findings = append(s.Review.Findings, domain.Finding{Severity: "medium", Path: fmt.Sprintf("backend/internal/pkg%02d/file%02d.go", i, i), Line: i + 1, Title: fmt.Sprintf("題名%d", i+1), Problem: fmt.Sprintf("問題%d", i+1), Evidence: fmt.Sprintf("根拠%d", i+1), Suggestion: fmt.Sprintf("修正案%d", i+1)})
	}
	s.Messages = []domain.Message{{ID: "q", Role: "user", Text: question}}
	return s
}

func decodeFindings(t *testing.T, input string) reviewFindingsContext {
	t.Helper()
	var body struct {
		Findings reviewFindingsContext `json:"reviewFindings"`
	}
	if err := json.Unmarshal([]byte(input), &body); err != nil {
		t.Fatal(err)
	}
	return body.Findings
}

func TestChatContextIncludesReferencedSavedFinding(t *testing.T) {
	s := manyFindingsSession(20, "最後の指摘の根拠と修正案を説明して")
	input := BuildContext(s, "", nil, nil, nil)
	got := decodeFindings(t, input)
	if got.Total != 20 || len(got.Details) != 1 || got.Details[0].Number != 20 || got.Details[0].Path != "backend/internal/pkg19/file19.go" || got.Details[0].Evidence != "根拠20" || got.Details[0].Suggestion != "修正案20" {
		t.Fatalf("last finding missing from context: %+v", got.Details)
	}
	for _, q := range []string{"指摘7について", "7件目を詳しく", "file06.go の件"} {
		got = decodeFindings(t, BuildContext(manyFindingsSession(20, q), "", nil, nil, nil))
		if len(got.Details) != 1 || got.Details[0].Number != 7 {
			t.Fatalf("%q did not resolve finding 7: %+v", q, got.Details)
		}
	}
}

func TestChatContextKeepsFindingsWithinInputCap(t *testing.T) {
	s := manyFindingsSession(50, "最後の指摘")
	for i := range s.Review.Findings {
		s.Review.Findings[i].Evidence = strings.Repeat("e", 4000)
	}
	input := BuildContext(s, "", nil, []evidence{{Content: strings.Repeat("x", 15000)}}, nil)
	if len(input) > domain.MaxInputBytes {
		t.Fatalf("input exceeds cap: %d", len(input))
	}
	got := decodeFindings(t, input)
	if len(got.Index) == 0 || got.Omitted == "" {
		t.Fatalf("index should be bounded with an explicit omission: %+v", got)
	}
}
