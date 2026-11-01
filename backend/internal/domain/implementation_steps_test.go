package domain

import (
	"fmt"
	"reflect"
	"testing"
)

func TestPlanStepsOrdersDependenciesAndKeepsCyclesTogether(t *testing.T) {
	files := []PlannedFile{
		{Path: "a-ui/view.ts", DependsOn: []string{"b-api/api.go"}},
		{Path: "b-api/api.go", DependsOn: []string{"z-types/type.go"}},
		{Path: "z-types/type.go"},
		{Path: "tests/api_test.go", DependsOn: []string{"b-api/api.go", "a-ui/view.ts"}},
		{Path: "cycle/a.go", DependsOn: []string{"cycle/b.go", "z-types/type.go"}},
		{Path: "cycle/b.go", DependsOn: []string{"cycle/a.go"}},
	}
	if err := ValidatePlanDependencies(files); err != nil {
		t.Fatal(err)
	}
	steps := PlanSteps(files)
	positions := map[string]int{}
	for i, step := range steps {
		if len(step.Files) > MaxImplementationStepFiles {
			t.Fatal("oversized step")
		}
		for _, p := range step.Files {
			if _, exists := positions[p]; exists {
				t.Fatalf("duplicate %s", p)
			}
			positions[p] = i
		}
	}
	if len(positions) != len(files) || positions["z-types/type.go"] >= positions["b-api/api.go"] || positions["b-api/api.go"] >= positions["a-ui/view.ts"] || positions["a-ui/view.ts"] >= positions["tests/api_test.go"] || positions["cycle/a.go"] != positions["cycle/b.go"] || positions["cycle/a.go"] <= positions["z-types/type.go"] {
		t.Fatalf("bad order: %+v", steps)
	}
	if !reflect.DeepEqual(steps, PlanSteps(files)) {
		t.Fatal("order is nondeterministic")
	}
}

func TestPlanDependenciesRejectUnknownSelfDuplicateAndOversizedCycle(t *testing.T) {
	for _, deps := range [][]string{{"missing.go"}, {"a.go"}, {"b.go", "b.go"}} {
		if err := ValidatePlanDependencies([]PlannedFile{{Path: "a.go", DependsOn: deps}, {Path: "b.go"}}); err == nil {
			t.Fatalf("invalid dependencies accepted: %v", deps)
		}
	}
	files := []PlannedFile{}
	for i := 0; i < 6; i++ {
		files = append(files, PlannedFile{Path: fmt.Sprintf("%d.go", i), DependsOn: []string{fmt.Sprintf("%d.go", (i+1)%6)}})
	}
	if err := ValidatePlanDependencies(files); err == nil || err.Error() != "plan_dependency_group_limit" {
		t.Fatalf("oversized cycle accepted: %v", err)
	}
}

func TestSplitPlanStepPreservesCycle(t *testing.T) {
	files := []PlannedFile{{Path: "a.go", DependsOn: []string{"b.go"}}, {Path: "b.go", DependsOn: []string{"a.go"}}, {Path: "c.go"}}
	if _, ok := SplitPlanStep(files, []string{"a.go", "b.go"}); ok {
		t.Fatal("cycle split")
	}
	parts, ok := SplitPlanStep(files, []string{"a.go", "b.go", "c.go"})
	if !ok || !reflect.DeepEqual(parts, []ImplementationStep{{Files: []string{"a.go", "b.go"}}, {Files: []string{"c.go"}}}) {
		t.Fatalf("wrong split: %+v", parts)
	}
}

func TestAmendmentReordersOnlyUnfinishedDependencies(t *testing.T) {
	i := &Implementation{Plan: &ImplementationPlan{Files: []PlannedFile{{Path: "done.go"}, {Path: "z/types.go", DependsOn: []string{"done.go"}}, {Path: "a/view.ts", DependsOn: []string{"z/types.go"}}}}, Steps: []ImplementationStep{{Files: []string{"done.go"}}, {Files: []string{"z/types.go"}}, {Files: []string{"a/view.ts"}}}, NextStep: 1, PendingFiles: []PendingFile{{Path: "api/api.go", DependsOn: []string{"z/types.go"}}}}
	i.ResolveAmendment(true)
	if len(i.Steps) != 4 || i.Steps[0].Files[0] != "done.go" || i.Steps[1].Files[0] != "z/types.go" || i.Steps[2].Files[0] != "api/api.go" || i.Steps[3].Files[0] != "a/view.ts" || i.NextStep != 1 {
		t.Fatalf("bad amendment order: %+v", i.Steps)
	}
}
