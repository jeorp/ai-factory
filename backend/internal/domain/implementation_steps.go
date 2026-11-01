package domain

import (
	"errors"
	"path"
	"sort"
)

func ValidatePlanDependencies(files []PlannedFile) error {
	paths := map[string]bool{}
	for _, f := range files {
		paths[f.Path] = true
	}
	for _, f := range files {
		if len(f.DependsOn) > MaxChangeFiles {
			return errors.New("invalid_plan_dependencies")
		}
		seen := map[string]bool{}
		for _, dep := range f.DependsOn {
			if dep == f.Path || !paths[dep] || seen[dep] {
				return errors.New("invalid_plan_dependencies")
			}
			seen[dep] = true
		}
	}
	for _, group := range planGroups(files) {
		if len(group) > MaxImplementationStepFiles {
			return errors.New("plan_dependency_group_limit")
		}
	}
	return nil
}

// With at most 20 files, transitive closure makes strongly connected groups
// straightforward and deterministic. Unknown dependencies may be completed
// files when scheduling only the unfinished subset; full plans validate them.
func planGroups(files []PlannedFile) [][]int {
	n := len(files)
	indices := map[string]int{}
	for i, f := range files {
		indices[f.Path] = i
	}
	reach := make([][]bool, n)
	for i, f := range files {
		reach[i] = make([]bool, n)
		for _, dep := range f.DependsOn {
			if j, ok := indices[dep]; ok {
				reach[i][j] = true
			}
		}
	}
	for k := 0; k < n; k++ {
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				reach[i][j] = reach[i][j] || reach[i][k] && reach[k][j]
			}
		}
	}
	groups := [][]int{}
	seen := make([]bool, n)
	for i := range files {
		if seen[i] {
			continue
		}
		group := []int{i}
		seen[i] = true
		for j := i + 1; j < n; j++ {
			if reach[i][j] && reach[j][i] {
				group = append(group, j)
				seen[j] = true
			}
		}
		groups = append(groups, group)
	}
	return groups
}

// PlanSteps schedules dependencies before consumers, keeps cycles atomic, and
// packs independent groups in each dependency layer into steps of <=5 files.
// Directory order remains the stable fallback when no dependencies are given.
func PlanSteps(files []PlannedFile) []ImplementationStep {
	return planSteps(files, 0)
}

func planSteps(files []PlannedFile, priorityCount int) []ImplementationStep {
	groups := planGroups(files)
	groupOf := map[string]int{}
	for g, members := range groups {
		for _, i := range members {
			groupOf[files[i].Path] = g
		}
	}
	done := map[int]bool{}
	steps := []ImplementationStep{}
	for len(done) < len(groups) {
		ready := []int{}
		for g, members := range groups {
			if done[g] {
				continue
			}
			blocked := false
			for _, i := range members {
				for _, dep := range files[i].DependsOn {
					if d, ok := groupOf[dep]; ok && d != g && !done[d] {
						blocked = true
					}
				}
			}
			if !blocked {
				ready = append(ready, g)
			}
		}
		if len(ready) == 0 {
			return nil
		} // impossible after collapsing cycles
		// Amendment files are normally needed before the held step. Prefer
		// them when ready, while respecting explicit dependencies on old work.
		priority := []int{}
		for _, g := range ready {
			for _, i := range groups[g] {
				if i < priorityCount {
					priority = append(priority, g)
					break
				}
			}
		}
		if len(priority) > 0 {
			ready = priority
		}
		sort.SliceStable(ready, func(i, j int) bool {
			return path.Dir(files[groups[ready[i]][0]].Path) < path.Dir(files[groups[ready[j]][0]].Path)
		})
		var batch []string
		for _, g := range ready {
			if len(groups[g]) > MaxImplementationStepFiles {
				return nil
			}
			if len(batch)+len(groups[g]) > MaxImplementationStepFiles {
				steps = append(steps, ImplementationStep{Files: batch})
				batch = nil
			}
			for _, i := range groups[g] {
				batch = append(batch, files[i].Path)
			}
			done[g] = true
		}
		if len(batch) > 0 {
			steps = append(steps, ImplementationStep{Files: batch})
		}
	}
	return steps
}

// SplitPlanStep sheds input size without splitting a cyclic dependency group.
func SplitPlanStep(files []PlannedFile, step []string) ([]ImplementationStep, bool) {
	groupOf := map[string]int{}
	for g, members := range planGroups(files) {
		for _, i := range members {
			groupOf[files[i].Path] = g
		}
	}
	best := 0
	for split := 1; split < len(step); split++ {
		left := map[int]bool{}
		for _, p := range step[:split] {
			left[groupOf[p]] = true
		}
		valid := true
		for _, p := range step[split:] {
			if left[groupOf[p]] {
				valid = false
			}
		}
		if valid && (best == 0 || abs(2*split-len(step)) < abs(2*best-len(step))) {
			best = split
		}
	}
	if best == 0 {
		return nil, false
	}
	return []ImplementationStep{{Files: step[:best]}, {Files: step[best:]}}, true
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
