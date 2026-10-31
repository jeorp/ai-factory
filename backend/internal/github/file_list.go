package github

import (
	"context"
	"errors"
	"sort"
	"strings"

	"ai-factory/backend/internal/domain"
)

// MaxListedFiles and MaxListedBytes cap the paths given to the model so the
// list stays a small share of the input. MaxListedBytes counts one newline per
// path.
const (
	MaxListedFiles = 3000
	MaxListedBytes = 30000
	// treeResponseBytes covers GitHub's own 7 MB cap on recursive trees, so a
	// large repository is listed (and cut here) instead of failing the read.
	treeResponseBytes = 8 * 1024 * 1024
)

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
	if err := c.sendLimit(ctx, token, "GET", "/repos/"+repository+"/git/trees/"+commit+"?recursive=1", nil, &v, treeResponseBytes); err != nil {
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
	// Keep shallow paths first so every top-level directory stays visible when
	// the list is cut, then present the kept paths in path order.
	sort.Slice(paths, func(i, j int) bool {
		di, dj := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})
	kept, size := 0, 0
	for _, p := range paths {
		if kept == MaxListedFiles || size+len(p)+1 > MaxListedBytes {
			truncated = true
			break
		}
		kept++
		size += len(p) + 1
	}
	paths = paths[:kept]
	sort.Strings(paths)
	return paths, truncated, nil
}
