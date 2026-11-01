package agent

import (
	"context"
	"errors"

	"ai-factory/backend/internal/domain"
)

func (run *implementRun) prepareDraft(ctx context.Context) error {
	impl := run.impl
	if impl.DraftSource == nil {
		return nil
	}
	source := impl.DraftSource
	files := []domain.FileChange{}
	for _, c := range impl.Changes {
		f, ok := run.r.Writer.GetChange(ctx, source.CacheID, source.Version, c.Path)
		if !ok {
			return errors.New("implementation_changes_expired")
		}
		if f.Path != c.Path || f.Action != impl.PlannedAction(c.Path) {
			return errors.New("change_action_not_planned")
		}
		files = append(files, f)
	}
	if err := domain.ValidateChangeSet(files, impl.ApprovedPaths()); err != nil {
		return err
	}
	for _, f := range files {
		if err := run.r.Writer.PutChange(ctx, impl.CacheID, impl.Version, f); err != nil {
			return err
		}
	}
	impl.DraftSource = nil
	return run.save(ctx, "作成済みの変更を引き継いで追加修正を計画します")
}

// mergeDraftPlan retains prior approved changes while scheduling only requested
// edits and previously unfinished files. Actions remain relative to BaseCommit.
func (run *implementRun) mergeDraftPlan(proposed *domain.ImplementationPlan) (*domain.ImplementationPlan, []domain.PlannedFile, error) {
	work := append([]domain.PlannedFile{}, proposed.Files...)
	if !run.impl.Revising || run.impl.Plan == nil {
		return proposed, work, nil
	}
	merged := *proposed
	merged.Files = append([]domain.PlannedFile{}, proposed.Files...)
	requested := map[string]bool{}
	old := map[string]domain.PlannedFile{}
	changed := map[string]bool{}
	for _, f := range run.impl.Plan.Files {
		old[f.Path] = f
	}
	for _, c := range run.impl.Changes {
		changed[c.Path] = true
	}
	for _, f := range proposed.Files {
		if prior, ok := old[f.Path]; ok && prior.Action != f.Action {
			return nil, nil, errors.New("revision_action_changed")
		}
		requested[f.Path] = true
	}
	for _, f := range run.impl.Plan.Files {
		if requested[f.Path] {
			continue
		}
		merged.Files = append(merged.Files, f)
		if !changed[f.Path] {
			work = append(work, f)
		}
	}
	return &merged, work, nil
}
