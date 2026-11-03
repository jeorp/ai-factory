package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"ai-factory/backend/internal/domain"
)

// RawFile reads the exact bytes of a text file at a commit through REST. The
// MCP read is not used here because edits must apply to the unmodified file.
// exists=false means the path is absent at that commit.
func (c *Client) RawFile(ctx context.Context, token, repository, commit, p string) (string, bool, error) {
	if !domain.ValidCommit(commit) {
		return "", false, errors.New("invalid_commit")
	}
	if err := domain.ValidateChangePath(p); err != nil {
		return "", false, err
	}
	if c.Demo {
		if p == "README.md" {
			return "# AI Factory\nGo API と Vue 3 の GitHub 読み取りチャット。\n", true, nil
		}
		return "", false, nil
	}
	if _, err := c.Authorize(ctx, token, repository); err != nil {
		return "", false, err
	}
	return c.rawFile(ctx, token, repository, commit, p)
}

// rawFile is used only after authorization and path/commit validation.
func (c *Client) rawFile(ctx context.Context, token, repository, commit, p string) (string, bool, error) {
	req, err := newAPIRequest(ctx, token, http.MethodGet, "/repos/"+repository+"/contents/"+escapePath(p)+"?ref="+url.QueryEscape(commit), nil, "application/vnd.github.raw+json")
	if err != nil {
		return "", false, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", false, errors.New("github_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, apiError{resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, domain.MaxChangeFileBytes+1))
	if err != nil {
		return "", false, err
	}
	if len(b) > domain.MaxChangeFileBytes {
		return "", true, errors.New("change_file_size_limit")
	}
	// A directory listing comes back as JSON array; refuse to treat it as a file.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") && len(b) > 0 && b[0] == '[' {
		return "", true, errors.New("invalid_change_path")
	}
	if !utf8.Valid(b) || strings.ContainsRune(string(b), 0) {
		return "", true, errors.New("change_not_text")
	}
	return string(b), true, nil
}

func changeCacheKey(cacheID string, version int, p string) string {
	data, _ := json.Marshal([]any{"impl-change-v1", cacheID, version, p})
	return hashCacheValue(string(data))
}

const DraftTTL = 24 * time.Hour

// PutChange keeps generated files longer than ordinary repository reads.
func (c *Client) PutChange(ctx context.Context, cacheID string, version int, f domain.FileChange) error {
	if c.Cache == nil || len(cacheID) < 16 {
		return errors.New("change_store_unavailable")
	}
	raw, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if len(raw) > domain.MaxFetchBytes {
		return errors.New("change_file_size_limit")
	}
	key := changeCacheKey(cacheID, version, f.Path)
	if cache, ok := c.Cache.(interface {
		PutWithTTL(context.Context, string, string, time.Duration)
	}); ok {
		cache.PutWithTTL(ctx, key, string(raw), DraftTTL)
	} else {
		c.Cache.Put(ctx, key, string(raw))
	}
	if got, ok := c.Cache.Get(ctx, key); !ok || got != string(raw) {
		return errors.New("change_store_failed")
	}
	return nil
}

func (c *Client) ExtendChanges(ctx context.Context, cacheID string, version int, paths []string, until time.Time) error {
	cache, ok := c.Cache.(interface {
		Extend(context.Context, []string, time.Time) error
	})
	if !ok || len(cacheID) < 16 || len(paths) == 0 || len(paths) > domain.MaxChangeFiles {
		return errors.New("change_store_unavailable")
	}
	keys := make([]string, 0, len(paths))
	for _, p := range paths {
		if domain.ValidateChangePath(p) != nil {
			return errors.New("invalid_change_path")
		}
		keys = append(keys, changeCacheKey(cacheID, version, p))
	}
	return cache.Extend(ctx, keys, until)
}

// GetChange returns a generated file, or ok=false when it expired.
func (c *Client) GetChange(ctx context.Context, cacheID string, version int, p string) (domain.FileChange, bool) {
	var f domain.FileChange
	if c.Cache == nil || len(cacheID) < 16 {
		return f, false
	}
	raw, ok := c.Cache.Get(ctx, changeCacheKey(cacheID, version, p))
	if !ok || json.Unmarshal([]byte(raw), &f) != nil || f.Path != p {
		return domain.FileChange{}, false
	}
	return f, true
}
