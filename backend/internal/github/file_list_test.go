package github

import "testing"

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
