package httpapi

import (
	"ai-factory/backend/internal/domain"
	"testing"
)

func TestReviewFileAtRestrictsSelectionToPlannedFiles(t *testing.T) {
	review := &domain.Review{Steps: []domain.ReviewStep{{Files: []domain.ReviewFile{{Index: 2, Path: "src/a.go"}, {Index: 3}}}}}
	if f := reviewFileAt(review, 2); f == nil || f.Path != "src/a.go" {
		t.Fatal("planned file not found")
	}
	for _, index := range []int{-1, 0, 3, 200} {
		if reviewFileAt(review, index) != nil {
			t.Fatalf("unexpected file at %d", index)
		}
	}
	if reviewFileAt(nil, 2) != nil {
		t.Fatal("missing review accepted")
	}
}
