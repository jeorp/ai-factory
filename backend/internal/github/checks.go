package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"ai-factory/backend/internal/domain"
)

type checkRun struct {
	ID         int64  `json:"id"`
	JobID      int    `json:"job_id,omitempty"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
	DetailsURL string `json:"details_url"`
	Output     struct {
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Text    string `json:"text"`
	} `json:"output"`
}

// checkRuns uses REST after Read has authorized the repository. The ref comes
// from the server-pinned implementation, never from a model or response URL.
func (c *Client) checkRuns(ctx context.Context, token string, target domain.Target) (string, int, error) {
	var result struct {
		Total     int        `json:"total_count"`
		Runs      []checkRun `json:"check_runs"`
		Truncated bool       `json:"truncated"`
	}
	result.Runs = []checkRun{}
	n := 0
	if !c.Demo {
		var err error
		n, err = c.reviewGet(ctx, token, fmt.Sprintf("/repos/%s/commits/%s/check-runs?per_page=20&filter=latest", target.Repository, target.Commit), &result)
		if err != nil {
			return "", n, err
		}
	}
	result.Truncated = result.Total > len(result.Runs)
	for i := range result.Runs {
		r := &result.Runs[i]
		r.JobID = actionsJobID(r.DetailsURL, target.Repository)
		r.Output.Title = domain.MaskSecrets(domain.Clip(r.Output.Title, 512))
		r.Output.Summary = domain.MaskSecrets(domain.Clip(r.Output.Summary, 2048))
		r.Output.Text = domain.MaskSecrets(domain.Clip(r.Output.Text, 4096))
	}
	raw, _ := json.Marshal(result)
	return domain.MaskSecrets(string(raw)), n, nil
}

// Extract only a job ID in this repository's GitHub Actions page; never follow
// the check's details URL, which arbitrary check providers can control.
func actionsJobID(raw, repository string) int {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil {
		return 0
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 7 || parts[0]+"/"+parts[1] != repository || parts[2] != "actions" || parts[3] != "runs" || parts[5] != "job" {
		return 0
	}
	run, err := strconv.ParseUint(parts[4], 10, 64)
	if err != nil || run == 0 {
		return 0
	}
	id, err := strconv.Atoi(parts[6])
	if err != nil || id < 1 {
		return 0
	}
	return id
}
