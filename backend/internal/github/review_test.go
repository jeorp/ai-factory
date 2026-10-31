package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func TestReviewRESTContractAndBoundedPages(t *testing.T) {
	target := domain.Target{Repository: "o/r", Kind: "pull", Number: 12}
	client := Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.Method != "GET" || r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("unsafe review request")
		}
		switch r.URL.Path {
		case "/repos/o/r":
			return response(`{"full_name":"o/r"}`), nil
		case "/repos/o/r/pulls/12":
			data, _ := json.Marshal(map[string]any{"title": "PR", "changed_files": 2, "base": map[string]string{"sha": strings.Repeat("b", 40)}, "head": map[string]string{"sha": strings.Repeat("a", 40)}})
			return response(string(data)), nil
		case "/repos/o/r/pulls/12/files":
			if r.URL.Query().Get("page") != "2" || r.URL.Query().Get("per_page") != "1" {
				t.Fatal("pagination lost")
			}
			return response(`[{"filename":"new.go","previous_filename":"old.go","status":"renamed","patch":"@@ -1 +1 @@\n-a\n+b"}]`), nil
		default:
			t.Fatalf("unapproved URL: %s", r.URL)
			return nil, nil
		}
	})}}
	pr, _, err := client.PullRequest(context.Background(), "token", target)
	if err != nil || pr.Head.SHA != strings.Repeat("a", 40) {
		t.Fatalf("snapshot not decoded: %+v %v", pr, err)
	}
	files, _, err := client.ReviewFiles(context.Background(), "token", target, 2, 1)
	if err != nil || len(files) != 1 || files[0].PreviousFilename != "old.go" {
		t.Fatal("rename/patch lost")
	}
	client.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/repos/o/r" {
			return response(`{}`), nil
		}
		return response(strings.Repeat("x", domain.MaxFetchBytes+1)), nil
	})
	if _, _, err = client.ReviewFiles(context.Background(), "token", target, 1, 1); err == nil || err.Error() != "github_response_limit_100kib" {
		t.Fatalf("unbounded review response: %v", err)
	}
}
func TestReviewRejectsNonPRAndInvalidSHA(t *testing.T) {
	client := Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return response(`{"head":{"sha":"main"},"base":{"sha":"main"}}`), nil
	})}}
	if _, _, err := client.PullRequest(context.Background(), "token", domain.Target{Kind: "repository"}); err == nil {
		t.Fatal("non PR accepted")
	}
	target := domain.Target{Repository: "o/r", Kind: "pull", Number: 1}
	if _, _, err := client.PullRequest(context.Background(), "token", target); err == nil {
		t.Fatal("unpinned snapshot accepted")
	}
	for _, page := range []int{0, MaxReviewFiles + 1} {
		if _, _, err := client.ReviewFiles(context.Background(), "token", target, page, 1); err == nil {
			t.Fatal("page boundary bypass")
		}
	}
}
