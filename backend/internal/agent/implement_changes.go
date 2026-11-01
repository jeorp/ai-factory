package agent

import (
	"context"
	"errors"
	"fmt"

	"ai-factory/backend/internal/domain"
)

type proposalEditError struct {
	path string
	err  error
}

func (e *proposalEditError) Error() string { return fmt.Sprintf("%s: %v", e.path, e.err) }
func (e *proposalEditError) Unwrap() error { return e.err }

// changeIssue is a pre-apply rejection of one entry in changes. Path is only
// set to a path of the current step and actions only to valid enum values, so
// the issue never echoes free text from the model.
type changeIssue struct {
	Code            string `json:"code"`
	Change          int    `json:"change"`
	Path            string `json:"path,omitempty"`
	ExpectedAction  string `json:"expectedAction,omitempty"`
	SubmittedAction string `json:"submittedAction,omitempty"`
	FirstChange     int    `json:"firstChange,omitempty"`
}

// proposalStructureError lists every structural problem of a response so the
// model can correct them together instead of discovering them one call at a time.
type proposalStructureError struct{ issues []changeIssue }

func (e *proposalStructureError) Error() string { return e.issues[0].Code }

var validChangeActions = map[string]bool{"create": true, "update": true, "delete": true}

// validateProposalStructure checks paths, uniqueness, planned actions and the
// fields each action allows before any edit touches the source, so a source
// mismatch in one entry cannot hide a duplicate or wrong action in another.
func validateProposalStructure(step []string, impl *domain.Implementation, proposals []ChangeProposal) error {
	inStep := map[string]bool{}
	for _, p := range step {
		inStep[p] = true
	}
	first := map[string]int{}
	issues := []changeIssue{}
	for i, p := range proposals {
		issue := changeIssue{Change: i + 1}
		if validChangeActions[p.Action] {
			issue.SubmittedAction = p.Action
		}
		if !inStep[p.Path] {
			issue.Code = "change_path_not_in_step"
			issues = append(issues, issue)
			continue
		}
		issue.Path = p.Path
		if at, ok := first[p.Path]; ok {
			// Keep checking the duplicate's action and fields so a wrong action
			// in it is reported in the same correction, not one call later.
			issues = append(issues, changeIssue{Code: "change_path_duplicate", Change: i + 1, Path: p.Path, SubmittedAction: issue.SubmittedAction, FirstChange: at})
		} else {
			first[p.Path] = i + 1
		}
		planned := impl.PlannedAction(p.Path)
		switch {
		case !validChangeActions[p.Action]:
			issue.Code, issue.ExpectedAction = "invalid_change_action", planned
		case p.Action != planned:
			issue.Code, issue.ExpectedAction = "change_action_not_planned", planned
		case p.Action != "update" && len(p.LineEdits) > 0, p.Action == "update" && len(p.LineEdits) > 0 && (len(p.Edits) > 0 || p.Content != ""):
			issue.Code = "update_format_conflict"
		case p.Action == "create" && (p.Content == "" || len(p.Edits) > 0):
			issue.Code = "create_requires_content"
		case p.Action == "update" && len(p.LineEdits) == 0 && len(p.Edits) == 0 && p.Content == "":
			issue.Code = "update_requires_edits"
		default:
			continue
		}
		issues = append(issues, issue)
	}
	if len(issues) == 0 {
		return nil
	}
	return &proposalStructureError{issues: issues}
}

// savedChange is the single expiry boundary for generated content. Callers
// must not fall back to repository contents when the saved change is gone.
func (run *implementRun) savedChange(ctx context.Context, path string) (domain.FileChange, error) {
	f, ok := run.r.Writer.GetChange(ctx, run.impl.CacheID, run.impl.Version, path)
	if !ok {
		return domain.FileChange{}, errors.New("implementation_changes_expired")
	}
	return f, nil
}

// retainedChange uses the verified contents loaded for this step. Draft
// revisions and repair rounds retain only already saved changes.
func retainedChange(impl *domain.Implementation, originals map[string]string, path string) (domain.FileChange, bool) {
	ciFix := impl.CIRepair && impl.PullNumber > 0 && domain.ValidCommit(impl.ExpectedHead) && impl.BaseCommit == impl.ExpectedHead
	if !impl.Revising && !ciFix && impl.RepairFeedback == "" {
		return domain.FileChange{}, false
	}
	content, ok := originals[path]
	if !ok {
		return domain.FileChange{}, false
	}
	for _, c := range impl.Changes {
		if c.Path == path && c.Action == impl.PlannedAction(path) {
			return domain.FileChange{Path: path, Action: c.Action, Content: content}, true
		}
	}
	return domain.FileChange{}, false
}

// applyProposals turns model proposals into full file contents. Only files of
// the current step with their approved action are accepted. Unedited saved
// changes in a draft revision retain their original base-relative action.
func applyProposals(step []string, impl *domain.Implementation, originals map[string]string, proposals []ChangeProposal) ([]domain.FileChange, error) {
	if err := validateProposalStructure(step, impl, proposals); err != nil {
		return nil, err
	}
	out := []domain.FileChange{}
	seen := map[string]bool{}
	for _, p := range proposals {
		seen[p.Path] = true
		switch p.Action {
		case "delete":
			out = append(out, domain.FileChange{Path: p.Path, Action: "delete"})
		case "create":
			out = append(out, domain.FileChange{Path: p.Path, Action: "create", Content: p.Content})
		case "update":
			content := p.Content
			if len(p.LineEdits) > 0 {
				if p.SourceHash != domain.SourceHash(originals[p.Path]) {
					return nil, &proposalEditError{path: p.Path, err: errors.New("edit_source_mismatch")}
				}
				var err error
				if content, err = domain.ApplyLineEdits(originals[p.Path], p.LineEdits); err != nil {
					return nil, &proposalEditError{path: p.Path, err: err}
				}
			} else if len(p.Edits) > 0 {
				var err error
				if content, err = domain.ApplyEdits(originals[p.Path], p.Edits); err != nil {
					return nil, &proposalEditError{path: p.Path, err: err}
				}
			}
			if content == originals[p.Path] {
				if _, retained := retainedChange(impl, originals, p.Path); !retained {
					return nil, &proposalEditError{path: p.Path, err: errors.New("update_without_change")}
				}
			}
			out = append(out, domain.FileChange{Path: p.Path, Action: "update", Content: content})
		default:
			return nil, errors.New("invalid_change_action")
		}
	}
	for _, path := range step {
		if !seen[path] {
			if f, retained := retainedChange(impl, originals, path); retained {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

// persistChanges validates the running size before writing and updates summaries
// only after each cache write succeeds. The step cursor is advanced separately.
func (run *implementRun) persistChanges(ctx context.Context, changes []domain.FileChange) (map[string]bool, error) {
	impl := run.impl
	// Check the running total now rather than failing only at publish time.
	total := impl.ChangeBytes()
	unchanged := map[string]bool{}
	for _, f := range changes {
		for _, c := range impl.Changes {
			if c.Path == f.Path {
				total -= c.Bytes
				current, err := run.savedChange(ctx, f.Path)
				if err != nil {
					return nil, err
				}
				unchanged[f.Path] = current == f
				break
			}
		}
		total += len(f.Content)
	}
	if total > domain.MaxChangeSetBytes {
		return nil, errors.New("change_set_size_limit")
	}
	covered := map[string]bool{}
	written := false
	for _, f := range changes {
		if unchanged[f.Path] {
			covered[f.Path] = true
			continue
		}
		if err := run.r.Writer.PutChange(ctx, impl.CacheID, impl.Version, f); err != nil {
			return nil, err
		}
		written = true
		covered[f.Path] = true
		summary := domain.ChangeSummary{Path: f.Path, Action: f.Action, Bytes: len(f.Content)}
		replaced := false
		for i := range impl.Changes {
			if impl.Changes[i].Path == f.Path {
				impl.Changes[i], replaced = summary, true
			}
		}
		if !replaced {
			impl.Changes = append(impl.Changes, summary)
		}
	}
	if written {
		impl.ChangeRevision++
	}
	return covered, nil
}
