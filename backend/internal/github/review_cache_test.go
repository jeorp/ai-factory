package github

import (
	"context"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func TestReviewCacheChunkingSnapshotAndAuthorization(t *testing.T) {
	ctx := context.Background()
	cache := memoryReadCache{}
	c := Client{Demo: true, Cache: cache}
	target := domain.Target{Repository: "demo/ai-factory", Kind: "pull", Number: 3}
	base, head := strings.Repeat("b", 40), strings.Repeat("a", 40)
	f := ChangedFile{Filename: "src/日本語.go", Status: "modified", Patch: strings.Repeat("+日本語\n", 7000)}
	c.PutReviewFile(ctx, target, base, head, 0, f)
	if len(cache) < 3 {
		t.Fatal("large patch was not split")
	}
	for _, value := range cache {
		if len(value) > reviewCacheChunkBytes {
			t.Fatal("cache entry exceeds chunk size")
		}
	}
	got, ok := c.GetReviewFile(ctx, "", target, base, head, 0)
	if !ok || got != f {
		t.Fatal("patch failed to round trip")
	}
	if _, ok := c.GetReviewFile(ctx, "", target, base, strings.Repeat("c", 40), 0); ok {
		t.Fatal("different snapshot hit")
	}
	other := target
	other.Repository = "other/repo"
	if _, ok := c.GetReviewFile(ctx, "", other, base, head, 0); ok {
		t.Fatal("authorization bypassed")
	}
	for key := range cache {
		if key != reviewCacheKey(target, base, head, 0, true) {
			delete(cache, key)
			break
		}
	}
	if _, ok := c.GetReviewFile(ctx, "", target, base, head, 0); ok {
		t.Fatal("incomplete entry returned")
	}
}
