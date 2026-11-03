package domain

import "time"

// Documentation keeps its own commit so later chat/review runs cannot move its references.
type Documentation struct {
	Request     string               `json:"request"`
	Scope       string               `json:"scope"`
	Repository  string               `json:"repository"`
	Commit      string               `json:"commit"`
	Status      string               `json:"status"`
	Result      *DocumentationResult `json:"result,omitempty"`
	Sources     []Source             `json:"sources"`
	Limitations []string             `json:"limitations"`
	CreatedAt   time.Time            `json:"createdAt"`
	// Bounded research checkpoints are persisted in Firestore, but not sent to the browser.
	Evidence []DocumentationEvidence `json:"-"`
	Notes    string                  `json:"-"`
	Calls    int                     `json:"-"`
}

type DocumentationEvidence struct {
	Kind    string `json:"kind"`
	Path    string `json:"path"`
	Content string `json:"content"`
	Failed  bool   `json:"failed"`
}

type DocumentationResult struct {
	Title      string                 `json:"title"`
	Summary    string                 `json:"summary"`
	Sections   []DocumentationSection `json:"sections"`
	Unverified []string               `json:"unverified"`
}

type DocumentationSection struct {
	Heading string   `json:"heading"`
	Body    string   `json:"body"`
	Paths   []string `json:"paths"`
}
