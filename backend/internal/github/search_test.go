package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func searchClient(t *testing.T, files map[string]string) (*Client, *int) {
	t.Helper()
	reads := new(int)
	c := &Client{Cache: memoryReadCache{}}
	c.HTTP = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		reply := func(v string) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/plain"}}, Body: io.NopCloser(strings.NewReader(v))}, nil
		}
		if strings.Contains(r.URL.Path, "/git/trees/") {
			if !strings.Contains(r.URL.Path, baseSHA) {
				t.Fatal("search tree not pinned")
			}
			entries := []map[string]string{}
			for p := range files {
				entries = append(entries, map[string]string{"path": p, "type": "blob"})
			}
			raw, _ := json.Marshal(map[string]any{"tree": entries, "truncated": false})
			return reply(string(raw))
		}
		if strings.Contains(r.URL.Path, "/contents/") {
			*reads = *reads + 1
			if r.URL.Query().Get("ref") != baseSHA {
				t.Fatal("search contents not pinned")
			}
			p := strings.SplitN(r.URL.Path, "/contents/", 2)[1]
			if _, ok := files[p]; !ok {
				t.Fatalf("unexpected path %s", p)
			}
			return reply(files[p])
		}
		return reply(`{"full_name":"owner/repo","default_branch":"main","permissions":{"push":true}}`)
	})}
	return c, reads
}

func TestSearchIsCommitPinnedAndPaginatesWithinFiles(t *testing.T) {
	c, reads := searchClient(t, map[string]string{"src/a.go": strings.Repeat("call Target()\n", 65), "src/b.go": "Target()\n", "elsewhere.go": "Target()", "node_modules/p/x.go": "Target()"})
	q := CodeSearch{Query: "Target", Prefix: "src"}
	seen := map[string]bool{}
	pages := 0
	for {
		result, err := c.SearchFiles(context.Background(), "token", "owner/repo", baseSHA, q)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for _, match := range result.Matches {
			key := fmt.Sprintf("%s:%d", match.Path, match.Line)
			if seen[key] || !strings.HasPrefix(match.Path, "src/") {
				t.Fatalf("duplicate or out-of-scope match: %s", key)
			}
			seen[key] = true
		}
		q.Cursor = result.NextCursor
		if q.Cursor == "" {
			break
		}
		if pages > 5 {
			t.Fatal("pagination did not finish")
		}
	}
	if len(seen) != 66 || pages != 3 || *reads != 2 {
		t.Fatalf("lost matches or cache: matches=%d pages=%d reads=%d", len(seen), pages, *reads)
	}
}

func TestSearchBoundsFileReadsAndMasksSecrets(t *testing.T) {
	files := map[string]string{}
	for i := 0; i < SearchPageFiles+3; i++ {
		files[fmt.Sprintf("src/%03d.go", i)] = "unrelated"
	}
	files["src/000.go"] = "Target ghp_abcdefghijklmnop1234567890\n"
	files["src/001.go"] = "Target\x00binary"
	c, reads := searchClient(t, files)
	result, err := c.SearchFiles(context.Background(), "token", "owner/repo", baseSHA, CodeSearch{Query: "Target"})
	if err != nil || *reads != SearchPageFiles || result.NextCursor == "" || result.SkippedFiles != 1 || len(result.Matches) != 1 || strings.Contains(result.Matches[0].Text, "ghp_") {
		t.Fatalf("bad bounded search: %+v reads=%d err=%v", result, *reads, err)
	}
}

func TestSearchRejectsTraversalAndCursorForOtherQuery(t *testing.T) {
	c, reads := searchClient(t, map[string]string{"a.go": strings.Repeat("Target\n", SearchPageMatches+1)})
	for _, q := range []CodeSearch{{Query: ""}, {Query: "Target", Prefix: "../private"}, {Query: "Target", Cursor: "malformed!"}} {
		if _, err := c.SearchFiles(context.Background(), "token", "owner/repo", baseSHA, q); err == nil {
			t.Fatalf("invalid search accepted: %+v", q)
		}
	}
	if *reads != 0 {
		t.Fatal("invalid search fetched source")
	}
	result, err := c.SearchFiles(context.Background(), "token", "owner/repo", baseSHA, CodeSearch{Query: "Target"})
	if err != nil || result.NextCursor == "" {
		t.Fatalf("fixture: %v", err)
	}
	if _, err := c.SearchFiles(context.Background(), "token", "owner/repo", baseSHA, CodeSearch{Query: "Other", Cursor: result.NextCursor}); err == nil {
		t.Fatal("cursor reused for another query")
	}
}
