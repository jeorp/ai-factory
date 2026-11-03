package agent

import "ai-factory/backend/internal/domain"

const missingPlanFilePrefix = "計画にあるが変更されなかったファイル: "

type noChangeCandidateError struct{ path, reason string }

func (e *noChangeCandidateError) Error() string { return "invalid_no_change_candidate" }

// An editor can suggest a smaller scope, but cannot complete a requirement or
// discard a saved draft, create or delete by declaring it unnecessary.
func validateNoChangeCandidates(step []string, impl *domain.Implementation, originals map[string]string, result *ImplementResult) error {
	allowed := map[string]bool{}
	changed := map[string]bool{}
	proposed := map[string]bool{}
	for _, p := range step {
		allowed[p] = true
	}
	for _, c := range impl.Changes {
		changed[c.Path] = true
	}
	for _, c := range result.Changes {
		proposed[c.Path] = true
	}
	for _, f := range result.UnchangedFiles {
		_, loaded := originals[f.Path]
		reason := ""
		switch {
		case !allowed[f.Path]:
			reason = "outside_step"
		case changed[f.Path]:
			reason = "saved_change"
		case proposed[f.Path]:
			reason = "conflicting_change"
		case impl.PlannedAction(f.Path) != "update":
			reason = "action_not_update"
		case !loaded:
			reason = "source_not_loaded"
		}
		if reason != "" {
			return &noChangeCandidateError{path: f.Path, reason: reason}
		}
	}
	return nil
}

func mergeNoChangeCandidates(existing, incoming []domain.UnchangedFile) []domain.UnchangedFile {
	indices := map[string]int{}
	out := []domain.UnchangedFile{}
	for _, list := range [][]domain.UnchangedFile{existing, incoming} {
		for _, f := range list {
			if index, ok := indices[f.Path]; ok {
				out[index] = f
			} else {
				indices[f.Path] = len(out)
				out = append(out, f)
			}
		}
	}
	return out
}

func allPlanFilesHaveNoChangeCandidates(impl *domain.Implementation) bool {
	if impl.Plan == nil || len(impl.Plan.Files) == 0 {
		return false
	}
	paths := map[string]bool{}
	for _, f := range impl.NoChangeCandidates {
		paths[f.Path] = true
	}
	for _, f := range impl.Plan.Files {
		if f.Action != "update" || !paths[f.Path] {
			return false
		}
	}
	return true
}

func (run *implementRun) missingPlanFiles() []domain.UnchangedFile {
	changed := map[string]bool{}
	for _, c := range run.impl.Changes {
		changed[c.Path] = true
	}
	var missing []domain.UnchangedFile
	for _, f := range run.impl.Plan.Files {
		if changed[f.Path] {
			continue
		}
		candidate := domain.UnchangedFile{Path: f.Path}
		for _, c := range run.impl.NoChangeCandidates {
			if c.Path == f.Path {
				candidate.Reason = c.Reason
			}
		}
		missing = append(missing, candidate)
	}
	return missing
}

// Replace only the server's missing-file markers with structured candidates.
// Model findings stay intact; text mentioning CI or no-op is never filtered.
func (run *implementRun) verificationEditUnverified() []string {
	markers := map[string]bool{}
	for _, f := range run.missingPlanFiles() {
		markers[missingPlanFilePrefix+f.Path] = true
	}
	out := []string{}
	for _, u := range run.impl.Unverified {
		if !markers[u] {
			out = append(out, u)
		}
	}
	return out
}

// Reconcile only after all requirements and all evidence coverage pass. Keep
// the original action/approach as an audit record, and keep every criterion.
func (run *implementRun) reconcilePlan(result *domain.ImplementationVerification, observed map[string]bool) {
	impl := run.impl
	changed := map[string]bool{}
	for _, c := range impl.Changes {
		changed[c.Path] = true
	}
	confirmed := map[string]string{}
	for _, f := range result.UnchangedFiles {
		if changed[f.Path] || impl.PlannedAction(f.Path) != "update" || !observed[f.Path] {
			result.Unverified = appendUnique(result.Unverified, "変更不要の根拠を確認できません: "+f.Path)
			continue
		}
		confirmed[f.Path] = f.Reason
	}
	for _, f := range run.missingPlanFiles() {
		if confirmed[f.Path] == "" {
			result.Unverified = appendUnique(result.Unverified, missingPlanFilePrefix+f.Path)
		}
	}
	impl.Verification = result
	if !impl.CanPublish() {
		return
	}
	files := []domain.PlannedFile{}
	for _, f := range impl.Plan.Files {
		if reason, ok := confirmed[f.Path]; ok {
			impl.PlanAdjustments = append(impl.PlanAdjustments, domain.PlanAdjustment{File: f, Reason: reason})
			continue
		}
		deps := []string{}
		for _, p := range f.DependsOn {
			if confirmed[p] == "" {
				deps = append(deps, p)
			}
		}
		f.DependsOn = deps
		files = append(files, f)
	}
	impl.Plan.Files = files
	steps := []domain.ImplementationStep{}
	next := 0
	for index, step := range impl.Steps {
		paths := []string{}
		for _, p := range step.Files {
			if confirmed[p] == "" {
				paths = append(paths, p)
			}
		}
		if len(paths) > 0 {
			steps = append(steps, domain.ImplementationStep{Files: paths})
			if index < impl.NextStep {
				next++
			}
		}
	}
	impl.Steps, impl.NextStep = steps, next
	limits := []string{}
	for _, u := range impl.Unverified {
		resolved := false
		for p := range confirmed {
			resolved = resolved || u == missingPlanFilePrefix+p
		}
		if !resolved {
			limits = append(limits, u)
		}
	}
	impl.Unverified = limits
	impl.NoChangeCandidates = nil
	if len(confirmed) > 0 {
		run.logger.Info("implement_plan_reconciled", "unchanged_files", len(confirmed))
	}
}
