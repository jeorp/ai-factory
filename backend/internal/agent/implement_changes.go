package agent

import (
	"context"
	"errors"
	"fmt"

	"ai-factory/backend/internal/domain"
)

// savedChange is the single expiry boundary for generated content. Callers
// must not fall back to repository contents when the saved change is gone.
func (run *implementRun) savedChange(ctx context.Context, path string) (domain.FileChange, error) {
	f, ok := run.r.Writer.GetChange(ctx, run.impl.CacheID, run.impl.Version, path)
	if !ok {
		return domain.FileChange{}, errors.New("implementation_changes_expired")
	}
	return f, nil
}

// applyProposals turns model proposals into full file contents. Only files of
// the current step with their approved action are accepted.
func applyProposals(step []string, impl *domain.Implementation, originals map[string]string, proposals []ChangeProposal) ([]domain.FileChange, error) {
	inStep := map[string]bool{}
	for _, p := range step {
		inStep[p] = true
	}
	out := []domain.FileChange{}
	seen := map[string]bool{}
	for _, p := range proposals {
		if !inStep[p.Path] {
			return nil, errors.New("change_path_not_in_step")
		}
		if seen[p.Path] {
			return nil, errors.New("change_path_duplicate")
		}
		seen[p.Path] = true
		if p.Action != impl.PlannedAction(p.Path) {
			return nil, errors.New("change_action_not_planned")
		}
		switch p.Action {
		case "delete":
			out = append(out, domain.FileChange{Path: p.Path, Action: "delete"})
		case "create":
			if p.Content == "" || len(p.Edits) > 0 {
				return nil, errors.New("create_requires_content")
			}
			out = append(out, domain.FileChange{Path: p.Path, Action: "create", Content: p.Content})
		case "update":
			content := p.Content
			if len(p.Edits) > 0 {
				var err error
				if content, err = domain.ApplyEdits(originals[p.Path], p.Edits); err != nil {
					return nil, fmt.Errorf("%s: %w", p.Path, err)
				}
			} else if content == "" {
				return nil, errors.New("update_requires_edits")
			}
			if content == originals[p.Path] {
				return nil, errors.New("update_without_change")
			}
			out = append(out, domain.FileChange{Path: p.Path, Action: "update", Content: content})
		default:
			return nil, errors.New("invalid_change_action")
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
	for _, f := range changes {
		for _, c := range impl.Changes {
			if c.Path == f.Path {
				total -= c.Bytes
			}
		}
		total += len(f.Content)
	}
	if total > domain.MaxChangeSetBytes {
		return nil, errors.New("change_set_size_limit")
	}
	covered := map[string]bool{}
	for _, f := range changes {
		if err := run.r.Writer.PutChange(ctx, impl.CacheID, impl.Version, f); err != nil {
			return nil, err
		}
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
	if len(changes) > 0 {
		impl.ChangeRevision++
	}
	return covered, nil
}
