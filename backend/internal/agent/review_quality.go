package agent

import (
	"strings"

	"ai-factory/backend/internal/domain"
)

func validReviewScope(scope string) bool {
	return scope == "" || scope == "all" || scope == "backend" || scope == "frontend"
}

// Shared configuration and documentation remain in scope. Related code may
// still be read as evidence, but its unrelated changes are not review tasks.
func excludedReviewFile(scope, name, previous string) bool {
	prefix := ""
	if scope == "backend" {
		prefix = "frontend/"
	} else if scope == "frontend" {
		prefix = "backend/"
	}
	return prefix != "" && strings.HasPrefix(name, prefix) && (previous == "" || strings.HasPrefix(previous, prefix))
}

// Catch explicit positive assessments without treating phrases such as
// "問題なしと判定されるが…" as proof that a real finding is harmless.
func nonIssueFinding(f domain.Finding) bool {
	problem := strings.TrimRight(strings.ToLower(strings.TrimSpace(f.Problem)), "。.!！ \n")
	for _, qualifier := range []string{"ただし", "しかし", "が、", "but ", "however"} {
		if strings.Contains(problem, qualifier) {
			return false
		}
	}
	for _, phrase := range []string{"特になし", "問題なし", "問題はありません", "不具合はありません", "no issues", "no issue", "no problems", "no problem", "none"} {
		if problem == phrase {
			return true
		}
		for _, pair := range [][2]string{{"（", "）"}, {"(", ")"}} {
			if strings.HasPrefix(problem, phrase+pair[0]) && strings.HasSuffix(problem, pair[1]) {
				return true
			}
		}
	}
	return false
}

func excludedReviewFinding(review *domain.Review, name string) bool {
	previous := ""
	for _, step := range review.Steps {
		for _, file := range step.Files {
			if file.Path == name {
				previous = file.PreviousPath
			}
		}
	}
	return excludedReviewFile(review.Scope, name, previous)
}

func reviewCrossSteps(steps []domain.ReviewStep) []domain.ReviewStep {
	var selected []domain.ReviewStep
	for _, step := range steps {
		copy := step
		copy.Files = nil
		for _, file := range step.Files {
			if !file.Excluded {
				copy.Files = append(copy.Files, file)
			}
		}
		if len(copy.Files) > 0 {
			selected = append(selected, copy)
		}
	}
	return selected
}
