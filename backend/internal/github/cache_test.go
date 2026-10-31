package github

import (
	"context"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

type memoryReadCache map[string]string

func (m memoryReadCache) Get(_ context.Context, key string) (string, bool) {
	v, ok := m[key]
	return v, ok
}
func (m memoryReadCache) Put(_ context.Context, key, content string) { m[key] = content }

func TestCommitReadCache(t *testing.T) {
	ctx := context.Background()
	cache := memoryReadCache{}
	c := Client{Demo: true, Cache: cache}
	target := domain.Target{Repository: "demo/ai-factory", Commit: strings.Repeat("a", 40)}
	read := Read{Kind: "file", Path: "README.md"}
	first, source, n, err := c.Read(ctx, "", target, read)
	if err != nil || n == 0 {
		t.Fatalf("first read: %v, %d", err, n)
	}
	second, cachedSource, n, err := c.Read(ctx, "", target, read)
	if err != nil || n != 0 || first != second || source != cachedSource {
		t.Fatalf("cached read: %v, %d", err, n)
	}
	target.Commit = strings.Repeat("b", 40)
	_, _, n, err = c.Read(ctx, "", target, read)
	if err != nil || n == 0 {
		t.Fatalf("different commit: %v, %d", err, n)
	}
	target.Repository = "other/repo"
	if _, _, _, err = c.Read(ctx, "", target, read); err == nil {
		t.Fatal("cache bypassed authorization")
	}
}

func TestMutableReadsNotCached(t *testing.T) {
	cache := memoryReadCache{}
	c := Client{Demo: true, Cache: cache}
	target := domain.Target{Repository: "demo/ai-factory", Commit: strings.Repeat("a", 40)}
	_, _, _, err := c.Read(context.Background(), "", target, Read{Kind: "issue", Number: 1})
	if err != nil || len(cache) != 0 {
		t.Fatalf("mutable read cached: %v", err)
	}
}
