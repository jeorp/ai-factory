package github

import (
	"context"
	"errors"
	"sort"
	"strings"

	"ai-factory/backend/internal/domain"
)

// MaxListedFiles caps the paths given to the model so the list stays a small
// share of the input.
const MaxListedFiles = 3000

// skippedDirs hold dependencies or build output that the model never edits.
var skippedDirs = []string{"node_modules/", "vendor/", "dist/", "build/", ".git/"}

// FileList returns the blob paths of a commit's tree so the model plans with
// real paths instead of guessing them. truncated reports that GitHub or the cap
// cut the list short.
func (c *Client) FileList(ctx context.Context, token, repository, commit string) ([]string, bool, error) {
	if !domain.ValidCommit(commit) {
		return nil, false, errors.New("invalid_commit")
	}
	if c.Demo {
		return []string{"README.md"}, false, nil
	}
	if _, err := c.Authorize(ctx, token, repository); err != nil {
		return nil, false, err
	}
	var v struct {
		Tree      []treeEntry `json:"tree"`
		Truncated bool        `json:"truncated"`
	}
	if err := c.send(ctx, token, "GET", "/repos/"+repository+"/git/trees/"+commit+"?recursive=1", nil, &v); err != nil {
		return nil, false, err
	}
	return filterFileList(v.Tree, v.Truncated)
}

func filterFileList(tree []treeEntry, truncated bool) ([]string, bool, error) {
	paths := []string{}
	for _, e := range tree {
		if e.Type != "blob" {
			continue
		}
		skip := false
		for _, d := range skippedDirs {
			if strings.HasPrefix(e.Path, d) || strings.Contains(e.Path, "/"+d) {
				skip = true
				break
			}
		}
		if !skip {
			paths = append(paths, e.Path)
		}
	}
	sort.Strings(paths)
	if len(paths) > MaxListedFiles {
		paths, truncated = paths[:MaxListedFiles], true
	}
	return paths, truncated, nil
}
