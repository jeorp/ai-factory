package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func TestDocumentationRetainsPerFileNotesAfterLaterSummaries(t *testing.T) {
	d := documentationSession("feature").Documentation
	d.Evidence = append(d.Evidence, domain.DocumentationEvidence{Kind: "file", Path: "entry.go", Content: "entry code"})
	rememberDocumentationSummary(d, "EntryHandler validates the request and calls SaveSession.")
	d.Evidence = append(d.Evidence, domain.DocumentationEvidence{Kind: "file", Path: "storage.go", Content: "storage code"})
	rememberDocumentationSummary(d, "SaveSession writes the token.")
	if !strings.Contains(d.Notes, "EntryHandler") || !strings.Contains(d.Notes, "SaveSession writes") || !strings.Contains(d.Evidence[0].Summary, "EntryHandler") {
		t.Fatal("earlier note overwritten")
	}
	d.Evidence[0].Content = ""
	input, omitted := buildDocumentationContext(d, documentationTask{Scope: "feature"}, nil)
	if !omitted || !strings.Contains(input, "EntryHandler") || !strings.Contains(input, "entry.go") {
		t.Fatal("omitted excerpt lost its per-file note")
	}
}

func TestDocumentationBoundsEscapedStoredEvidence(t *testing.T) {
	d := documentationSession("feature").Documentation
	for i := 0; i < DocumentationMaxReads; i++ {
		d.Evidence = append(d.Evidence, domain.DocumentationEvidence{Kind: "file", Path: strings.Repeat("\x01", 500) + fmt.Sprintf("%d.go", i), Content: strings.Repeat("\x00", 12*1024), Summary: strings.Repeat("\x00", 768)})
	}
	if !boundDocumentationEvidence(d) {
		t.Fatal("did not compact stored evidence")
	}
	body, _ := json.Marshal(d.Evidence)
	if len(body) > DocumentationEvidenceBytes || len(d.Evidence) != DocumentationMaxReads {
		t.Fatalf("storage bound failed: %d", len(body))
	}
}

func TestDocumentationCompactsOldResearchBeforeSavingAndKeepsNotes(t *testing.T) {
	s := documentationSession("feature")
	for i := 0; i < DocumentationMaxFiles; i++ {
		s.Documentation.Evidence = append(s.Documentation.Evidence, domain.DocumentationEvidence{Kind: "file", Path: fmt.Sprintf("file%d.go", i), Content: strings.Repeat("x", 12*1024), Summary: fmt.Sprintf("function%d calls storage", i)})
	}
	r := Runner{AI: providerFunc(func(ctx context.Context, key, model, input string) (Decision, Usage, error) {
		if !strings.Contains(input, "function0 calls storage") {
			t.Fatal("lost first-file summary during compaction")
		}
		return Decision{Document: documentationResult()}, Usage{}, nil
	})}
	var last Checkpoint
	err := r.Run(context.Background(), Input{Session: s}, func(cp Checkpoint) error {
		last = cp
		body, _ := json.Marshal(cp.Documentation.Evidence)
		if len(body) > DocumentationEvidenceBytes {
			t.Fatal("oversized evidence checkpoint")
		}
		return nil
	})
	if err != nil || !strings.Contains(strings.Join(last.Documentation.Limitations, " "), "保存上限") {
		t.Fatalf("missing compaction limitation: %v", err)
	}
}
