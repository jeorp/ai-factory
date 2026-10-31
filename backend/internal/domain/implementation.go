package domain

import (
	"errors"
	"path"
	"sort"
	"strings"
)

// Implementation is the durable state of an AI implementation and PR creation
// run (specification section 13). Changed file contents are never stored here;
// they live in an expiring cache addressed by CacheID.
type Implementation struct {
	Request      string               `json:"request"`
	BaseBranch   string               `json:"baseBranch"`
	BaseCommit   string               `json:"baseCommit"`
	Status       string               `json:"status"` // planning, plan_ready, editing, ready, publishing, published
	Plan         *ImplementationPlan  `json:"plan,omitempty"`
	Steps        []ImplementationStep `json:"steps,omitempty"`
	NextStep     int                  `json:"nextStep"`
	Changes      []ChangeSummary      `json:"changes"`
	Unverified   []string             `json:"unverified"`
	Summary      string               `json:"summary"`
	CacheID      string               `json:"-"`
	Version      int                  `json:"version"`
	Branch       string               `json:"branch,omitempty"`
	Title        string               `json:"title,omitempty"`
	Body         string               `json:"body,omitempty"`
	Commit       string               `json:"commit,omitempty"`
	RefBranch    string               `json:"refBranch,omitempty"`
	RefUpdated   bool                 `json:"refUpdated,omitempty"`
	PullNumber   int                  `json:"pullNumber,omitempty"`
	PullURL      string               `json:"pullUrl,omitempty"`
	ExpectedHead string               `json:"expectedHead,omitempty"`
}

type ImplementationPlan struct {
	Summary    string        `json:"summary"`
	Files      []PlannedFile `json:"files"`
	OutOfScope []string      `json:"outOfScope"`
	Risks      []string      `json:"risks"`
	Tests      []string      `json:"tests"`
}

type PlannedFile struct {
	Path     string `json:"path"`
	Action   string `json:"action"`
	Approach string `json:"approach"`
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

// PlanSteps groups planned files by directory, at most five per step.
func PlanSteps(files []PlannedFile) []ImplementationStep {
	byDir := map[string][]string{}
	dirs := []string{}
	for _, f := range files {
		d := path.Dir(f.Path)
		if _, ok := byDir[d]; !ok {
			dirs = append(dirs, d)
		}
		byDir[d] = append(byDir[d], f.Path)
	}
	sort.Strings(dirs)
	steps := []ImplementationStep{}
	var cur []string
	for _, d := range dirs {
		for _, p := range byDir[d] {
			if len(cur) == MaxImplementationStepFiles {
				steps = append(steps, ImplementationStep{Files: cur})
				cur = nil
			}
			cur = append(cur, p)
		}
	}
	if len(cur) > 0 {
		steps = append(steps, ImplementationStep{Files: cur})
	}
	return steps
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
// pending plan or change set is replaced and the request is accumulated.
func NextImplementation(prev *Implementation, request, cacheID string) *Implementation {
	request = Clip(strings.TrimSpace(request), 4096)
	next := &Implementation{Request: request, Status: "planning", CacheID: cacheID, Changes: []ChangeSummary{}, Unverified: []string{}}
	if prev == nil {
		return next
	}
	next.Version = prev.Version + 1
	if prev.Status == "published" && prev.Branch != "" && ValidCommit(prev.Commit) {
		next.BaseBranch, next.BaseCommit = prev.BaseBranch, prev.Commit
		next.Branch, next.PullNumber, next.PullURL = prev.Branch, prev.PullNumber, prev.PullURL
		next.ExpectedHead = prev.Commit
		next.Title = prev.Title
		return next
	}
	if prev.Request != "" && prev.Status != "published" {
		next.Request = Clip(prev.Request+"\n\n追加の依頼: "+request, 4096)
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
