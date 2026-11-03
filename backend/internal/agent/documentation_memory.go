package agent

import (
	"encoding/json"
	"strings"

	"ai-factory/backend/internal/domain"
)

const DocumentationEvidenceBytes = 64 * 1024

func rememberDocumentationSummary(doc *domain.Documentation, summary string) {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return
	}
	// Preserve the note for the evidence just inspected independently of later notes.
	if n := len(doc.Evidence); n > 0 && !doc.Evidence[n-1].Failed {
		doc.Evidence[n-1].Summary = domain.Clip(summary, 768)
	}
	notes := append(strings.Split(doc.Notes, "\n\n"), domain.Clip(summary, 1024))
	for len(strings.Join(notes, "\n\n")) > 4096 && len(notes) > 1 {
		notes = notes[1:]
	}
	doc.Notes = strings.TrimSpace(strings.Join(notes, "\n\n"))
}

// Bound serialized evidence including escaping and notes before every durable save.
// Old excerpts go first; per-file notes go last. This also bounds older checkpoints.
func boundDocumentationEvidence(doc *domain.Documentation) bool {
	omitted := false
	for {
		body, _ := json.Marshal(doc.Evidence)
		if len(body) <= DocumentationEvidenceBytes {
			return omitted
		}
		trimmed := false
		for i := range doc.Evidence {
			if doc.Evidence[i].Content != "" {
				doc.Evidence[i].Content = ""
				trimmed = true
				break
			}
		}
		if !trimmed {
			for i := range doc.Evidence {
				if doc.Evidence[i].Summary != "" {
					doc.Evidence[i].Summary = ""
					trimmed = true
					break
				}
			}
		}
		if !trimmed {
			return omitted
		} // Metadata of at most 20 validated reads fits the cap.
		omitted = true
	}
}
