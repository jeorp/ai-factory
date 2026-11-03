package agent

import (
	"context"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// Writer is the guarded GitHub write side. The model never calls it: the
// runner passes only server-validated change sets.
type Writer interface {
	Authorize(context.Context, string, string) (github.Repository, error)
	Commit(context.Context, string, github.Repository) (string, error)
	RawFile(context.Context, string, string, string, string) (string, bool, error)
	FileList(context.Context, string, string, string) ([]string, bool, error)
	PutChange(context.Context, string, int, domain.FileChange) error
	GetChange(context.Context, string, int, string) (domain.FileChange, bool)
	Publish(context.Context, string, github.PublishRequest, github.PublishState, func(github.PublishState) error) (github.PublishState, error)
	PullRequestOpen(context.Context, string, string, int, string, string) (bool, error)
	PullRequestTarget(context.Context, string, string, int) (github.PullRequestTarget, error)
}

// ImplementRequest is returned by the chat model to start the implementation workflow.
type ImplementRequest struct {
	Request string `json:"request"`
	// PullNumber is an existing AI Factory PR to push the fix to; 0 starts a new PR.
	PullNumber int `json:"pullNumber"`
}

// ChangeProposal is one file change proposed by the model. Updates use
// search/replace edits against the original; Content replaces the whole file
// only for creates or when no edits are given.
type ChangeProposal struct {
	Path       string            `json:"path"`
	Action     string            `json:"action"`
	Content    string            `json:"content"`
	Edits      []domain.Edit     `json:"edits"`
	SourceHash string            `json:"sourceHash"`
	LineEdits  []domain.LineEdit `json:"lineEdits"`
}

type ImplementResult struct {
	Changes    []ChangeProposal `json:"changes"`
	Unverified []string         `json:"unverified"`
	// AdditionalFiles are files outside the plan that the change also needs.
	// They are added only after the user approves.
	AdditionalFiles []domain.PendingFile   `json:"additionalFiles"`
	UnchangedFiles  []domain.UnchangedFile `json:"unchangedFiles"`
}

const (
	ImplementPlanCalls       = 12
	ImplementPlanReads       = 10
	ImplementStepCalls       = 5
	ImplementStepEditReserve = 3 // generation and corrections cannot be consumed by reads
	ImplementStepReads       = ImplementStepCalls - ImplementStepEditReserve
	ImplementMaxCalls        = 100
	ImplementOutputToks      = 16384
	ImplementRunTime         = 30 * time.Minute

	// Context budgets keep the extra code well inside implementInputFits.
	planEvidenceKept   = 5
	stepEvidenceKept   = 3
	maxContextFiles    = 8
	contextFileBytes   = 12000
	contextTotalBytes  = 36000
	previousDiffBytes  = 6000
	previousTotalBytes = 20000
)

type implementFile struct {
	Path             string `json:"path"`
	Action           string `json:"action"`
	Approach         string `json:"approach"`
	Original         string `json:"original,omitempty"`
	NumberedOriginal string `json:"numberedOriginal,omitempty"`
	SourceHash       string `json:"sourceHash,omitempty"`
	PreserveExisting bool   `json:"preserveExisting,omitempty"`
}

type implementTask struct {
	Mode           string                     `json:"mode"`
	Request        string                     `json:"request"`
	BaseBranch     string                     `json:"baseBranch"`
	FollowUp       bool                       `json:"followUp,omitempty"`
	Plan           *domain.ImplementationPlan `json:"plan,omitempty"`
	Step           int                        `json:"step,omitempty"`
	TotalSteps     int                        `json:"totalSteps,omitempty"`
	Files          []implementFile            `json:"files,omitempty"`
	Done           []domain.ChangeSummary     `json:"done,omitempty"`
	ReadBudget     int                        `json:"readBudget"`
	FinalAnswer    bool                       `json:"finalAnswerOnly,omitempty"`
	Correction     string                     `json:"correction,omitempty"`
	Declined       []string                   `json:"declinedFiles,omitempty"`
	Attempt        int                        `json:"-"`
	RetryCount     int                        `json:"-"`
	RepairFeedback string                     `json:"repairFeedback,omitempty"`
	EditFormat     string                     `json:"editFormat,omitempty"`
	EditFailure    *implementEditFailure      `json:"editFailure,omitempty"`
}
