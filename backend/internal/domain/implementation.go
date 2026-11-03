package domain

import (
	"errors"
	"strings"
)

// Implementation is the durable state of an AI implementation and PR creation
// run (specification section 13). Changed file contents are never stored here;
// they live in an expiring cache addressed by CacheID.
type Implementation struct {
	Request            string                      `json:"request"`
	BaseBranch         string                      `json:"baseBranch"`
	BaseCommit         string                      `json:"baseCommit"`
	Status             string                      `json:"status"` // planning, plan_ready, editing, amend_ready, verifying, verification_blocked, ready, publishing, checking_ci, published
	Plan               *ImplementationPlan         `json:"plan,omitempty"`
	NoChangeCandidates []UnchangedFile             `json:"noChangeCandidates,omitempty"`
	PlanAdjustments    []PlanAdjustment            `json:"planAdjustments,omitempty"`
	Steps              []ImplementationStep        `json:"steps,omitempty"`
	NextStep           int                         `json:"nextStep"`
	Changes            []ChangeSummary             `json:"changes"`
	Unverified         []string                    `json:"unverified"`
	Summary            string                      `json:"summary"`
	Verification       *ImplementationVerification `json:"verification,omitempty"`
	CI                 *ImplementationCI           `json:"ci,omitempty"`
	CIRepair           bool                        `json:"ciRepair,omitempty"`
	RequiredCriteria   []AcceptanceCriterion       `json:"requiredCriteria,omitempty"`
	Clarification      string                      `json:"clarification,omitempty"`
	Revising           bool                        `json:"revising,omitempty"`
	DraftSource        *ImplementationDraftSource  `json:"-"`
	RepairRound        int                         `json:"repairRound,omitempty"`
	RepairFeedback     string                      `json:"repairFeedback,omitempty"`
	ChangeRevision     int                         `json:"changeRevision,omitempty"`
	EditStartRevision  int                         `json:"editStartRevision,omitempty"`
	ContextFiles       []string                    `json:"contextFiles,omitempty"` // files read while planning, given to every step
	PendingFiles       []PendingFile               `json:"pendingFiles,omitempty"` // files the AI asked to add mid-way, awaiting approval
	Declined           []string                    `json:"declined,omitempty"`     // additions the user turned down; not proposed again
	FixTarget          bool                        `json:"fixTarget,omitempty"`    // the user named PullNumber; never fall back to a new PR
	CacheID            string                      `json:"-"`
	Version            int                         `json:"version"`
	Branch             string                      `json:"branch,omitempty"`
	Title              string                      `json:"title,omitempty"`
	Body               string                      `json:"body,omitempty"`
	Commit             string                      `json:"commit,omitempty"`
	RefBranch          string                      `json:"refBranch,omitempty"`
	RefUpdated         bool                        `json:"refUpdated,omitempty"`
	PullNumber         int                         `json:"pullNumber,omitempty"`
	PullURL            string                      `json:"pullUrl,omitempty"`
	ExpectedHead       string                      `json:"expectedHead,omitempty"`
}

// GitHub CI is execution evidence for one exact published commit. A Draft PR
// being created is not the same as its checks passing.
type ImplementationCI struct {
	Commit     string                `json:"commit"`
	State      string                `json:"state"` // pending, passed, failed, unverified
	Checks     []ImplementationCheck `json:"checks"`
	Unverified []string              `json:"unverified"`
}

type ImplementationCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	JobID      int    `json:"jobId"`
}

// ImplementationVerification is an AI consistency check, not a build/test result.
type ImplementationVerification struct {
	Issues          []string             `json:"issues"`
	Unverified      []string             `json:"unverified"`
	Criteria        []CriterionResult    `json:"criteria,omitempty"`
	Repairs         []VerificationRepair `json:"repairs,omitempty"`
	AdditionalFiles []PendingFile        `json:"additionalFiles,omitempty"`
	UnchangedFiles  []UnchangedFile      `json:"unchangedFiles,omitempty"`
	DeferredChecks  []string             `json:"deferredChecks,omitempty"` // server-owned, copied from the approved plan's Tests
}

// Candidates do not change approval. Only a completed static verification can
// remove an unchanged update from the effective plan, preserving its history.
type UnchangedFile struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type PlanAdjustment struct {
	File   PlannedFile `json:"file"`
	Reason string      `json:"reason"`
}

type VerificationRepair struct {
	Path     string `json:"path"`
	Approach string `json:"approach"`
}

// DraftSource is server-only provenance for copying saved changes into a new
// revision. Firestore persists it; JSON never exposes cache identifiers.
type ImplementationDraftSource struct {
	CacheID string
	Version int
}

type ImplementationPlan struct {
	Summary            string                `json:"summary"`
	Files              []PlannedFile         `json:"files"`
	OutOfScope         []string              `json:"outOfScope"`
	Risks              []string              `json:"risks"`
	Tests              []string              `json:"tests"`
	AcceptanceCriteria []AcceptanceCriterion `json:"acceptanceCriteria"`
}

// Criteria are approved with the plan and are checked against code again after
// generation. They describe observable outcomes, not merely a list of edits.
type AcceptanceCriterion struct {
	ID          string   `json:"id"`
	Requirement string   `json:"requirement"`
	Paths       []string `json:"paths"`
	Checks      []string `json:"checks"`
	FailureRefs []string `json:"failureRefs"`
}

type CriterionResult struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"` // met, unmet, unverified (static verification only)
	Evidence string   `json:"evidence"`
	Paths    []string `json:"paths"`
}

type PlannedFile struct {
	Path      string   `json:"path"`
	Action    string   `json:"action"`
	Approach  string   `json:"approach"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// PendingFile is a file the AI found it must also change while editing. It is
// added to the plan only after the user approves.
type PendingFile struct {
	Path      string   `json:"path"`
	Action    string   `json:"action"`
	Approach  string   `json:"approach"`
	Reason    string   `json:"reason"`
	DependsOn []string `json:"dependsOn,omitempty"`
}

// ResolveAmendment adds the pending files to the plan when approved, or records
// them as left out. Either way editing continues with the remaining steps.
func (i *Implementation) ResolveAmendment(approve bool) {
	if approve && i.Plan != nil {
		added := []PlannedFile{}
		for _, f := range i.PendingFiles {
			added = append(added, PlannedFile{Path: f.Path, Action: f.Action, Approach: f.Approach, DependsOn: f.DependsOn})
		}
		i.Plan.Files = append(i.Plan.Files, added...)
		// Reorder unfinished work with the new dependency graph. Completed
		// files remain untouched and satisfy dependencies in remaining work.
		remaining := map[string]bool{}
		for _, step := range i.Steps[i.NextStep:] {
			for _, p := range step.Files {
				remaining[p] = true
			}
		}
		unfinished := append([]PlannedFile{}, added...)
		for _, f := range i.Plan.Files[:len(i.Plan.Files)-len(added)] {
			if remaining[f.Path] {
				unfinished = append(unfinished, f)
			}
		}
		steps := append([]ImplementationStep{}, i.Steps[:i.NextStep]...)
		i.Steps = append(steps, planSteps(unfinished, len(added))...)
	} else {
		for _, f := range i.PendingFiles {
			i.Unverified = append(i.Unverified, "追加を見送ったファイル: "+f.Path+"（"+f.Reason+"）")
			i.Declined = append(i.Declined, f.Path)
		}
	}
	i.PendingFiles = nil
	i.Status = "editing"
}

// ImplementationStep is a group of planned files. Firestore cannot store nested arrays.
type ImplementationStep struct {
	Files []string `json:"files"`
}

type ChangeSummary struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Bytes  int    `json:"bytes"`
}

const MaxImplementationStepFiles = 5

// ValidatePlan checks a model-proposed plan before it is shown for approval.
func ValidatePlan(p *ImplementationPlan) error {
	if p == nil || strings.TrimSpace(p.Summary) == "" || len(p.Summary) > 2048 {
		return errors.New("invalid_plan")
	}
	if len(p.Files) == 0 || len(p.Files) > MaxChangeFiles {
		return errors.New("plan_file_limit")
	}
	seen := map[string]bool{}
	for _, f := range p.Files {
		if err := ValidateChangePath(f.Path); err != nil {
			return err
		}
		if seen[f.Path] {
			return errors.New("change_path_duplicate")
		}
		seen[f.Path] = true
		if f.Action != "create" && f.Action != "update" && f.Action != "delete" {
			return errors.New("invalid_change_action")
		}
		if strings.TrimSpace(f.Approach) == "" || len(f.Approach) > 1024 {
			return errors.New("invalid_plan")
		}
	}
	if err := ValidatePlanDependencies(p.Files); err != nil {
		return err
	}
	if err := ValidateAcceptanceCriteria(p.AcceptanceCriteria); err != nil {
		return err
	}
	for _, list := range [][]string{p.OutOfScope, p.Risks, p.Tests} {
		if len(list) > 10 {
			return errors.New("invalid_plan")
		}
		for _, v := range list {
			if len(v) > 512 {
				return errors.New("invalid_plan")
			}
		}
	}
	return nil
}

// ApprovedPaths lists the files the user approved in the plan.
func (i *Implementation) ApprovedPaths() []string {
	if i == nil || i.Plan == nil {
		return nil
	}
	out := make([]string, 0, len(i.Plan.Files))
	for _, f := range i.Plan.Files {
		out = append(out, f.Path)
	}
	return out
}

// PlannedAction returns the approved action for a path.
func (i *Implementation) PlannedAction(p string) string {
	if i == nil || i.Plan == nil {
		return ""
	}
	for _, f := range i.Plan.Files {
		if f.Path == p {
			return f.Action
		}
	}
	return ""
}

// NextImplementation starts a new planning cycle. After a PR was published the
// cycle continues on the same AI branch from its last commit; otherwise a
// pending plan is replaced; generated changes are inherited into an isolated
// cache and the newest request is preserved within the accumulated request.
func NextImplementation(prev *Implementation, request, cacheID string) *Implementation {
	request = Clip(strings.TrimSpace(request), 4096)
	next := &Implementation{Request: request, Status: "planning", CacheID: cacheID, Changes: []ChangeSummary{}, Unverified: []string{}}
	if prev == nil {
		return next
	}
	next.Version = prev.Version + 1
	published := prev.Status == "published" || prev.Status == "checking_ci"
	if published && prev.Branch != "" && ValidCommit(prev.Commit) {
		next.BaseBranch, next.BaseCommit = prev.BaseBranch, prev.Commit
		next.Branch, next.PullNumber, next.PullURL = prev.Branch, prev.PullNumber, prev.PullURL
		next.ExpectedHead = prev.Commit
		next.Title = prev.Title
		return next
	}
	if prev.Request != "" && !published {
		next.Request = AppendRequest(prev.Request, request, 4096)
		next.CIRepair = prev.CIRepair
		next.RequiredCriteria = append([]AcceptanceCriterion{}, prev.RequiredCriteria...)
	}
	if !published && prev.Status != "publishing" && prev.Plan != nil && len(prev.Changes) > 0 && ValidCommit(prev.BaseCommit) {
		next.Revising = true
		next.BaseBranch, next.BaseCommit = prev.BaseBranch, prev.BaseCommit
		next.Branch, next.Title = prev.Branch, prev.Title
		next.PullNumber, next.PullURL, next.ExpectedHead = prev.PullNumber, prev.PullURL, prev.ExpectedHead
		next.Plan = prev.Plan
		next.Changes = append([]ChangeSummary{}, prev.Changes...)
		next.Unverified = append([]string{}, prev.Unverified...)
		next.ContextFiles = append([]string{}, prev.ContextFiles...)
		next.Declined = append([]string{}, prev.Declined...)
		next.Verification = prev.Verification
		next.PlanAdjustments = append([]PlanAdjustment{}, prev.PlanAdjustments...)
		next.DraftSource = &ImplementationDraftSource{CacheID: prev.CacheID, Version: prev.Version}
		if prev.DraftSource != nil {
			source := *prev.DraftSource
			next.DraftSource = &source
		}
	}
	return next
}

// DetachFollowUp turns a follow-up whose PR was merged, closed or moved into a
// fresh cycle on a new branch from the current default branch.
func (i *Implementation) DetachFollowUp() {
	i.ExpectedHead, i.BaseBranch, i.BaseCommit = "", "", ""
	i.Branch, i.Title, i.PullNumber, i.PullURL = "", "", 0, ""
}

// ChangeBytes is the total size of the generated contents.
func (i *Implementation) ChangeBytes() int {
	n := 0
	for _, c := range i.Changes {
		n += c.Bytes
	}
	return n
}
