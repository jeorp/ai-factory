package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"ai-factory/backend/internal/domain"
)

const MaxReviewFiles = 200

type PullRequest struct {
	Title        string `json:"title"`
	Body         string `json:"body"`
	ChangedFiles int    `json:"changed_files"`
	Base         struct {
		SHA string `json:"sha"`
	} `json:"base"`
	Head struct {
		SHA string `json:"sha"`
	} `json:"head"`
}
type ChangedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Patch            string `json:"patch"`
	Changes          int    `json:"changes"`
}

// The REST adapter supplies typed, paginated PR metadata/patches. Related source
// and comments still use the read-only MCP adapter. Never follow response URLs.
func (c *Client) reviewGet(ctx context.Context, token, path string, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com"+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return 0, errors.New("github_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("github_http_%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, domain.MaxFetchBytes+1))
	if err != nil {
		return len(raw), errors.New("github_read_failed")
	}
	if len(raw) > domain.MaxFetchBytes {
		return len(raw), errors.New("github_response_limit_100kib")
	}
	if json.Unmarshal(raw, out) != nil {
		return len(raw), errors.New("invalid_github_response")
	}
	return len(raw), nil
}
func (c *Client) PullRequest(ctx context.Context, token string, t domain.Target) (PullRequest, int, error) {
	var pr PullRequest
	if t.Kind != "pull" || t.Number < 1 {
		return pr, 0, errors.New("pull_request_required")
	}
	if _, err := c.Authorize(ctx, token, t.Repository); err != nil {
		return pr, 0, err
	}
	if c.Demo {
		pr.Title = "固定デモ: APIと画面の更新"
		pr.Body = "サンプル差分を分割して確認します。実際のGitHubを参照していません。"
		pr.Base.SHA, pr.Head.SHA, pr.ChangedFiles = strings.Repeat("b", 40), strings.Repeat("a", 40), 2
		return pr, 0, nil
	}
	n, err := c.reviewGet(ctx, token, fmt.Sprintf("/repos/%s/pulls/%d", t.Repository, t.Number), &pr)
	if err == nil && (!domain.ValidCommit(pr.Base.SHA) || !domain.ValidCommit(pr.Head.SHA) || pr.ChangedFiles < 0) {
		err = errors.New("invalid_pr_snapshot")
	}
	return pr, n, err
}
func (c *Client) ReviewFiles(ctx context.Context, token string, t domain.Target, page, perPage int) ([]ChangedFile, int, error) {
	if t.Kind != "pull" || t.Number < 1 || page < 1 || page > MaxReviewFiles || (perPage != 1 && perPage != 10) {
		return nil, 0, errors.New("invalid_review_page")
	}
	if _, err := c.Authorize(ctx, token, t.Repository); err != nil {
		return nil, 0, err
	}
	if c.Demo {
		files := []ChangedFile{
			{Filename: "backend/api.go", Status: "modified", Changes: 2, Patch: "@@ -1,2 +1,2 @@\n-package api\n+package api // demo\n // 固定デモ"},
			{Filename: "frontend/client.ts", Status: "modified", Changes: 2, Patch: "@@ -1 +1 @@\n-export const version = 1;\n+export const version = 2;"},
		}
		start := (page - 1) * perPage
		if start >= len(files) {
			return []ChangedFile{}, 0, nil
		}
		end := start + perPage
		if end > len(files) {
			end = len(files)
		}
		return files[start:end], 0, nil
	}
	var files []ChangedFile
	n, err := c.reviewGet(ctx, token, fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=%d&page=%d", t.Repository, t.Number, perPage, page), &files)
	return files, n, err
}
