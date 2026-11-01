package github

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"ai-factory/backend/internal/domain"
)

const SearchPageFiles = 40
const SearchPageMatches = 30

type CodeSearch struct {
	Query  string `json:"query"`
	Prefix string `json:"prefix"`
	Cursor string `json:"cursor"`
}

func (q CodeSearch) Validate() error {
	if strings.TrimSpace(q.Query) == "" || len(q.Query) > 128 || !utf8.ValidString(q.Query) || len(q.Cursor) > 512 || len(q.Prefix) > 512 {
		return errors.New("invalid_implementation_search")
	}
	for _, r := range q.Query {
		if unicode.IsControl(r) {
			return errors.New("invalid_implementation_search")
		}
	}
	if q.Prefix != "" && domain.ValidateChangePath(strings.TrimSuffix(q.Prefix, "/")+"/search.txt") != nil {
		return errors.New("invalid_implementation_search")
	}
	return nil
}

type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type CodeSearchResult struct {
	Matches       []SearchMatch `json:"matches"`
	ScannedFiles  int           `json:"scannedFiles"`
	ScannedBytes  int           `json:"scannedBytes"`
	SkippedFiles  int           `json:"skippedFiles"`
	ListedFiles   int           `json:"listedFiles"`
	ListTruncated bool          `json:"listTruncated"`
	NextCursor    string        `json:"nextCursor"`
}

type searchCursor struct {
	File int    `json:"file"`
	Line int    `json:"line"`
	Key  string `json:"key"`
}

// SearchFiles performs bounded literal search at an immutable commit. It does
// not use a default-branch search index or allow the model to pick endpoints.
// The cursor continues within a file, so result limits never skip matches.
func (c *Client) SearchFiles(ctx context.Context, token, repository, commit string, query CodeSearch) (CodeSearchResult, error) {
	if err := query.Validate(); err != nil {
		return CodeSearchResult{}, err
	}
	paths, truncated, err := c.FileList(ctx, token, repository, commit)
	if err != nil {
		return CodeSearchResult{}, err
	}
	files := []string{}
	prefix := strings.TrimSuffix(query.Prefix, "/")
	for _, p := range paths {
		if (prefix == "" || p == prefix || strings.HasPrefix(p, prefix+"/")) && domain.ValidateChangePath(p) == nil {
			files = append(files, p)
		}
	}
	hash := sha256.Sum256([]byte(repository + "\x00" + commit + "\x00" + query.Query + "\x00" + prefix))
	key := hex.EncodeToString(hash[:])
	position := searchCursor{Key: key}
	if query.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		if err != nil || json.Unmarshal(raw, &position) != nil || position.Key != key || position.File < 0 || position.File > len(files) || position.Line < 0 || position.Line > domain.MaxChangeFileBytes {
			return CodeSearchResult{}, errors.New("invalid_implementation_search")
		}
	}
	result := CodeSearchResult{Matches: []SearchMatch{}, ListTruncated: truncated, ListedFiles: len(files)}
	for position.File < len(files) && result.ScannedFiles < SearchPageFiles && len(result.Matches) < SearchPageMatches {
		p := files[position.File]
		cacheKey := hashCacheValue("implementation-search-v1\x00" + token + "\x00" + repository + "\x00" + commit + "\x00" + p)
		content, cached := "", false
		if c.Cache != nil {
			content, cached = c.Cache.Get(ctx, cacheKey)
		}
		if !cached {
			var exists bool
			if c.Demo {
				content, exists, err = c.RawFile(ctx, token, repository, commit, p)
			} else {
				content, exists, err = c.rawFile(ctx, token, repository, commit, p)
			}
			if err != nil && err.Error() != "change_not_text" && err.Error() != "change_file_size_limit" {
				return result, err
			}
			if err != nil || !exists {
				result.SkippedFiles++
				position.File++
				position.Line = 0
				result.ScannedFiles++
				continue
			}
			if c.Cache != nil {
				c.Cache.Put(ctx, cacheKey, content)
			}
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}
		result.ScannedFiles++
		result.ScannedBytes += len(content)
		lines := strings.Split(content, "\n")
		for position.Line < len(lines) && len(result.Matches) < SearchPageMatches {
			line := lines[position.Line]
			position.Line++
			if strings.Contains(line, query.Query) {
				result.Matches = append(result.Matches, SearchMatch{Path: p, Line: position.Line, Text: domain.Clip(domain.MaskSecrets(line), 384)})
			}
		}
		if position.Line >= len(lines) {
			position.File++
			position.Line = 0
		}
	}
	if position.File < len(files) {
		raw, _ := json.Marshal(position)
		result.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return result, nil
}
