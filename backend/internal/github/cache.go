package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"ai-factory/backend/internal/domain"
	"cloud.google.com/go/firestore"
)

// ReadCache stores only successful reads of content fixed to a commit.
type ReadCache interface {
	Get(context.Context, string) (string, bool)
	Put(context.Context, string, string)
}

type FirestoreReadCache struct{ DB *firestore.Client }
type cacheEntry struct {
	Content   string
	ExpiresAt time.Time
}

func readCacheKey(t domain.Target, a Read, demo bool) string {
	// Only fields used by get_file_contents affect the result.
	data, _ := json.Marshal([]any{demo, t.Repository, t.Commit, a.Path})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (c *FirestoreReadCache) Get(ctx context.Context, key string) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	snap, err := c.DB.Collection("githubReadCache").Doc(key).Get(ctx)
	if err != nil {
		return "", false
	}
	var entry cacheEntry
	if snap.DataTo(&entry) != nil || !time.Now().Before(entry.ExpiresAt) {
		return "", false
	}
	return entry.Content, true
}

func (c *FirestoreReadCache) Put(ctx context.Context, key, content string) {
	// Firestore has a document size limit; a failed cache write must not fail the read.
	if len(content) > domain.MaxFetchBytes {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = c.DB.Collection("githubReadCache").Doc(key).Set(ctx, cacheEntry{Content: content, ExpiresAt: time.Now().Add(time.Hour)})
}

// GetOverview checks current repository access before returning a shared summary.
func (c *Client) GetOverview(ctx context.Context, token string, target domain.Target) (*domain.RepositoryOverview, bool) {
	if c.Cache == nil || !domain.ValidCommit(target.Commit) {
		return nil, false
	}
	if _, err := c.Authorize(ctx, token, target.Repository); err != nil {
		return nil, false
	}
	key := readCacheKey(target, Read{Path: "\x00overview-v1"}, c.Demo)
	value, ok := c.Cache.Get(ctx, key)
	if !ok {
		return nil, false
	}
	var overview domain.RepositoryOverview
	if json.Unmarshal([]byte(value), &overview) != nil || overview.Commit != target.Commit || overview.Text == "" || len(overview.Text) > 1400 {
		return nil, false
	}
	return &overview, true
}

// PutOverview is called only after a successful authorized README read and summary.
func (c *Client) PutOverview(ctx context.Context, target domain.Target, overview domain.RepositoryOverview) {
	if c.Cache == nil {
		return
	}
	value, err := json.Marshal(overview)
	if err != nil {
		return
	}
	c.Cache.Put(ctx, readCacheKey(target, Read{Path: "\x00overview-v1"}, c.Demo), string(value))
}
