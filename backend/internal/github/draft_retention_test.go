package github

import (
	"ai-factory/backend/internal/domain"
	"context"
	"testing"
	"time"
)

type ttlCache struct {
	memoryReadCache
	ttl   time.Duration
	keys  []string
	until time.Time
}

func (c *ttlCache) PutWithTTL(ctx context.Context, key, content string, ttl time.Duration) {
	c.ttl = ttl
	c.Put(ctx, key, content)
}
func (c *ttlCache) Extend(_ context.Context, keys []string, until time.Time) error {
	c.keys = keys
	c.until = until
	return nil
}

func TestDraftRetentionDoesNotChangeRepositoryCacheTTL(t *testing.T) {
	cache := &ttlCache{memoryReadCache: memoryReadCache{}}
	c := Client{Cache: cache}
	id := "0123456789abcdef"
	f := domain.FileChange{Path: "a.go", Action: "create", Content: "package a"}
	if err := c.PutChange(context.Background(), id, 3, f); err != nil {
		t.Fatal(err)
	}
	if cache.ttl != 24*time.Hour {
		t.Fatalf("ttl=%v", cache.ttl)
	}
	until := time.Now().Add(24 * time.Hour)
	if err := c.ExtendChanges(context.Background(), id, 3, []string{f.Path}, until); err != nil {
		t.Fatal(err)
	}
	if len(cache.keys) != 1 || cache.keys[0] != changeCacheKey(id, 3, f.Path) || cache.until != until {
		t.Fatal("wrong renewal identity")
	}
	got, ok := c.GetChange(context.Background(), id, 3, f.Path)
	if !ok || got != f {
		t.Fatal("renewal changed content")
	}
	if err := c.ExtendChanges(context.Background(), id, 3, []string{"../secret"}, until); err == nil {
		t.Fatal("invalid path renewed")
	}
}
