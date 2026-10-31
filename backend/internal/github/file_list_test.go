package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestFilterFileListSkipsDependenciesAndCaps(t *testing.T) {
	sha := "x"
	tree := []treeEntry{
		{Path: "b.go", Type: "blob"}, {Path: "a", Type: "tree", SHA: &sha}, {Path: "a/x.go", Type: "blob"},
		{Path: "node_modules/p/i.js", Type: "blob"}, {Path: "web/node_modules/p/i.js", Type: "blob"}, {Path: "sub", Type: "commit"},
	}
	paths, truncated, err := filterFileList(tree, false)
	if err != nil || truncated || len(paths) != 2 || paths[0] != "a/x.go" || paths[1] != "b.go" {
		t.Fatalf("unexpected list: %v %v %v", paths, truncated, err)
	}
	many := make([]treeEntry, MaxListedFiles+1)
	for i := range many {
		many[i] = treeEntry{Path: string(rune('a'+i%26)) + "/" + string(rune(i)) + ".go", Type: "blob"}
	}
	if paths, truncated, _ := filterFileList(many, false); !truncated || len(paths) != MaxListedFiles {
		t.Fatalf("cap not applied: %d %v", len(paths), truncated)
	}
}

func TestFilterFileListKeepsShallowPathsWhenCut(t *testing.T) {
	tree := []treeEntry{{Path: "z/main.go", Type: "blob"}, {Path: "README.md", Type: "blob"}}
	for i := 0; i < 2000; i++ {
		tree = append(tree, treeEntry{Path: fmt.Sprintf("a/deep/nested/dir%04d/file.go", i), Type: "blob"})
	}
	paths, truncated, _ := filterFileList(tree, false)
	size := 0
	for _, p := range paths {
		size += len(p) + 1
	}
	if !truncated || size > MaxListedBytes || !sort.StringsAreSorted(paths) {
		t.Fatalf("not cut to budget: %d bytes, truncated=%v", size, truncated)
	}
	if !slices.Contains(paths, "z/main.go") || !slices.Contains(paths, "README.md") {
		t.Fatal("shallow paths dropped in favour of deep ones")
	}
}

func TestFileListReadsTreesLargerThanTheDefaultResponseCap(t *testing.T) {
	entries := []map[string]any{}
	for i := 0; i < 5000; i++ {
		entries = append(entries, map[string]any{"path": fmt.Sprintf("src/f%05d.go", i), "mode": "100644", "type": "blob", "sha": strings.Repeat("a", 40), "size": 100, "url": "https://api.github.com/repos/owner/repo/git/blobs/" + strings.Repeat("a", 40)})
	}
	body, _ := json.Marshal(map[string]any{"tree": entries, "truncated": false})
	if len(body) <= 512*1024 {
		t.Fatalf("fixture too small: %d", len(body))
	}
	c := &Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/git/trees/") {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(body))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"full_name":"owner/repo","default_branch":"main","permissions":{"push":true}}`))}, nil
	})}}
	paths, truncated, err := c.FileList(context.Background(), "token", "owner/repo", strings.Repeat("1", 40))
	if err != nil || !truncated || len(paths) == 0 {
		t.Fatalf("large tree not listed: %d paths, truncated=%v, err=%v", len(paths), truncated, err)
	}
}
