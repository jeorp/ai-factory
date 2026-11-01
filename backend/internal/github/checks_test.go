package github

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

func TestCheckRunsReadUsesPinnedRESTAndResolvesOwnJob(t *testing.T) {
	commit := strings.Repeat("c", 40)
	requests := 0
	c := &Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected request: %s", r.URL)
		}
		if r.URL.Path == "/repos/owner/repo" {
			return response(`{"full_name":"owner/repo"}`), nil
		}
		if r.URL.Path != "/repos/owner/repo/commits/"+commit+"/check-runs" || r.URL.Query().Get("filter") != "latest" {
			t.Fatalf("checks not pinned: %s", r.URL)
		}
		return response(`{"total_count":3,"check_runs":[{"id":123,"name":"backend","conclusion":"failure","details_url":"https://github.com/owner/repo/actions/runs/11/job/999","output":{"text":"compile failed"}},{"id":124,"details_url":"https://github.com/other/repo/actions/runs/11/job/998"}]}`), nil
	})}}
	raw, _, n, err := c.Read(context.Background(), "test-token", domain.Target{Repository: "owner/repo", Commit: commit}, Read{Kind: "check_runs", Number: 7})
	var result struct {
		Runs      []checkRun `json:"check_runs"`
		Truncated bool       `json:"truncated"`
	}
	if err != nil || n == 0 || json.Unmarshal([]byte(raw), &result) != nil || requests != 2 || !result.Truncated || len(result.Runs) != 2 || result.Runs[0].JobID != 999 || result.Runs[1].JobID != 0 {
		t.Fatalf("bad checks result: %s %v requests=%d", raw, err, requests)
	}
}

func TestActionsJobIDRejectsExternalOrMalformedDetails(t *testing.T) {
	for _, raw := range []string{"https://evil.example/owner/repo/actions/runs/11/job/12", "https://github.com/other/repo/actions/runs/11/job/12", "https://github.com/owner/repo/actions/runs/11/job/-1", "https://github.com/owner/repo/actions/runs/11/job/0", "https://github.com/owner/repo/actions/runs/0/job/12", "https://token@github.com/owner/repo/actions/runs/11/job/12", "http://github.com/owner/repo/actions/runs/11/job/12"} {
		if actionsJobID(raw, "owner/repo") != 0 {
			t.Fatalf("untrusted details accepted: %s", raw)
		}
	}
}
