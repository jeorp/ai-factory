package github

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"ai-factory/backend/internal/domain"
)

// Review patches are stored as bounded, content-addressed pieces. A manifest is
// published last; missing or expired pieces turn the whole entry into a miss.
const reviewCacheChunkBytes = 16 * 1024
const reviewCacheMaxChunks = 64

type reviewCacheManifest struct {
	Chunks []string `json:"chunks"`
	Digest string   `json:"digest"`
}

func hashCacheValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
func reviewCacheKey(t domain.Target, base, head string, index int, demo bool) string {
	data, _ := json.Marshal([]any{"review-patch-v1", demo, t.Repository, t.Number, base, head, index})
	return hashCacheValue(string(data))
}

func (c *Client) GetReviewFile(ctx context.Context, token string, t domain.Target, base, head string, index int) (ChangedFile, bool) {
	var f ChangedFile
	if c.Cache == nil || t.Kind != "pull" || t.Number < 1 || index < 0 || index >= MaxReviewFiles || !domain.ValidCommit(base) || !domain.ValidCommit(head) {
		return f, false
	}
	if _, err := c.Authorize(ctx, token, t.Repository); err != nil {
		return f, false
	}
	key := reviewCacheKey(t, base, head, index, c.Demo)
	raw, ok := c.Cache.Get(ctx, key)
	if !ok {
		return f, false
	}
	var manifest reviewCacheManifest
	if json.Unmarshal([]byte(raw), &manifest) != nil || len(manifest.Chunks) == 0 || len(manifest.Chunks) > reviewCacheMaxChunks {
		return f, false
	}
	var b strings.Builder
	for _, id := range manifest.Chunks {
		if len(id) != 64 {
			return f, false
		}
		part, ok := c.Cache.Get(ctx, hashCacheValue("review-chunk-v1:"+id))
		if !ok || len(part) > reviewCacheChunkBytes || hashCacheValue(part) != id {
			return f, false
		}
		b.WriteString(part)
	}
	value := b.String()
	if hashCacheValue(value) != manifest.Digest || json.Unmarshal([]byte(value), &f) != nil || f.Filename == "" {
		return ChangedFile{}, false
	}
	return f, true
}

// PutReviewFile is called only after the caller has rechecked the PR snapshot.
func (c *Client) PutReviewFile(ctx context.Context, t domain.Target, base, head string, index int, f ChangedFile) {
	if c.Cache == nil || t.Kind != "pull" || t.Number < 1 || index < 0 || index >= MaxReviewFiles || !domain.ValidCommit(base) || !domain.ValidCommit(head) || f.Filename == "" {
		return
	}
	value, err := json.Marshal(f)
	if err != nil || len(value) > reviewCacheChunkBytes*reviewCacheMaxChunks {
		return
	}
	s := string(value)
	manifest := reviewCacheManifest{Digest: hashCacheValue(s)}
	for len(s) > 0 {
		end := len(s)
		if end > reviewCacheChunkBytes {
			end = reviewCacheChunkBytes
			for end > 0 && !utf8.RuneStart(s[end]) {
				end--
			}
		}
		if end == 0 {
			return
		}
		part := s[:end]
		id := hashCacheValue(part)
		c.Cache.Put(ctx, hashCacheValue("review-chunk-v1:"+id), part)
		manifest.Chunks = append(manifest.Chunks, id)
		s = s[end:]
	}
	raw, err := json.Marshal(manifest)
	if err == nil {
		c.Cache.Put(ctx, reviewCacheKey(t, base, head, index, c.Demo), string(raw))
	}
}
