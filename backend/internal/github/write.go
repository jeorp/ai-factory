package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"ai-factory/backend/internal/domain"
)

// PublishRequest describes one user-approved write. The server, never the model,
// fills Repository, BaseBranch, BaseCommit and ExpectedHead.
type PublishRequest struct {
	Repository string
	BaseBranch string
	BaseCommit string
	Branch     string
	Title      string
	Body       string
	Message    string
	Files      []domain.FileChange
	Approved   []string
	// ExpectedHead is set for a follow-up commit to a branch this session
	// created earlier. The branch must still point at it.
	ExpectedHead string
}

// PublishState is persisted after each completed write so that a retry only
// performs the remaining steps and never duplicates a commit, branch or PR.
type PublishState struct {
	Commit     string `json:"commit,omitempty"`
	RefUpdated bool   `json:"refUpdated,omitempty"`
	PullNumber int    `json:"pullNumber,omitempty"`
	PullURL    string `json:"pullUrl,omitempty"`
}

func (r PublishRequest) validate() error {
	p := strings.Split(r.Repository, "/")
	if len(p) != 2 || p[0] == "" || p[1] == "" {
		return errors.New("invalid_target")
	}
	if !domain.ValidCommit(r.BaseCommit) || r.BaseBranch == "" || strings.ContainsAny(r.BaseBranch, " ~^:?*[\\\x00") {
		return errors.New("invalid_base")
	}
	if !domain.ValidBranchName(r.Branch) || r.Branch == r.BaseBranch {
		return errors.New("invalid_branch")
	}
	if r.ExpectedHead != "" && !domain.ValidCommit(r.ExpectedHead) {
		return errors.New("invalid_expected_head")
	}
	if strings.TrimSpace(r.Message) == "" || len(r.Message) > 4096 {
		return errors.New("invalid_commit_message")
	}
	if r.ExpectedHead == "" && (strings.TrimSpace(r.Title) == "" || len(r.Title) > 256 || len(r.Body) > 65536) {
		return errors.New("invalid_pull_request")
	}
	if domain.ContainsSecret(r.Title + "\n" + r.Body + "\n" + r.Message) {
		return errors.New("change_contains_secret")
	}
	return domain.ValidateChangeSet(r.Files, r.Approved)
}

type apiError struct{ Status int }

func (e apiError) Error() string { return fmt.Sprintf("github_http_%d", e.Status) }

func (c *Client) send(ctx context.Context, token, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.github.com"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("github_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError{resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024+1))
	if err != nil {
		return err
	}
	if len(b) > 512*1024 {
		return errors.New("github_response_limit")
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

type treeEntry struct {
	Path string  `json:"path"`
	Mode string  `json:"mode"`
	Type string  `json:"type"`
	SHA  *string `json:"sha"`
}

// entry walks the parent's tree one directory at a time, which also rejects
// symlinks and submodules along the path.
func (c *Client) entry(ctx context.Context, token, repo, root, p string, cache map[string][]treeEntry) (treeEntry, bool, error) {
	parts := strings.Split(p, "/")
	sha := root
	for i, name := range parts {
		entries, ok := cache[sha]
		if !ok {
			var v struct {
				Tree      []treeEntry `json:"tree"`
				Truncated bool        `json:"truncated"`
			}
			if err := c.send(ctx, token, "GET", "/repos/"+repo+"/git/trees/"+sha, nil, &v); err != nil {
				return treeEntry{}, false, err
			}
			if v.Truncated {
				return treeEntry{}, false, errors.New("tree_too_large")
			}
			entries = v.Tree
			cache[sha] = entries
		}
		var found *treeEntry
		for j := range entries {
			if entries[j].Path == name {
				found = &entries[j]
				break
			}
		}
		if found == nil {
			return treeEntry{}, false, nil
		}
		if found.Mode == "120000" || found.Type == "commit" {
			return treeEntry{}, false, errors.New("protected_change_path")
		}
		if i < len(parts)-1 {
			if found.Type != "tree" || found.SHA == nil {
				return treeEntry{}, false, errors.New("invalid_change_path")
			}
			sha = *found.SHA
			continue
		}
		return *found, true, nil
	}
	return treeEntry{}, false, nil
}

func (c *Client) createCommit(ctx context.Context, token string, r PublishRequest, parent string) (string, error) {
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.send(ctx, token, "GET", "/repos/"+r.Repository+"/git/commits/"+parent, nil, &commit); err != nil {
		return "", err
	}
	if !domain.ValidCommit(commit.Tree.SHA) {
		return "", errors.New("invalid_tree")
	}
	cache := map[string][]treeEntry{}
	tree := []treeEntry{}
	for _, f := range r.Files {
		existing, exists, err := c.entry(ctx, token, r.Repository, commit.Tree.SHA, f.Path, cache)
		if err != nil {
			return "", err
		}
		if exists && existing.Type != "blob" {
			return "", errors.New("invalid_change_path")
		}
		switch {
		case f.Action == "create" && exists:
			return "", errors.New("change_file_exists")
		case f.Action != "create" && !exists:
			return "", errors.New("change_file_missing")
		}
		mode := "100644"
		if exists {
			mode = existing.Mode
		}
		if f.Action == "delete" {
			tree = append(tree, treeEntry{Path: f.Path, Mode: mode, Type: "blob"})
			continue
		}
		var blob struct {
			SHA string `json:"sha"`
		}
		if err := c.send(ctx, token, "POST", "/repos/"+r.Repository+"/git/blobs", map[string]string{"content": f.Content, "encoding": "utf-8"}, &blob); err != nil {
			return "", err
		}
		sha := blob.SHA
		tree = append(tree, treeEntry{Path: f.Path, Mode: mode, Type: "blob", SHA: &sha})
	}
	var newTree struct {
		SHA string `json:"sha"`
	}
	if err := c.send(ctx, token, "POST", "/repos/"+r.Repository+"/git/trees", map[string]any{"base_tree": commit.Tree.SHA, "tree": tree}, &newTree); err != nil {
		return "", err
	}
	var created struct {
		SHA string `json:"sha"`
	}
	if err := c.send(ctx, token, "POST", "/repos/"+r.Repository+"/git/commits", map[string]any{"message": r.Message, "tree": newTree.SHA, "parents": []string{parent}}, &created); err != nil {
		return "", err
	}
	if !domain.ValidCommit(created.SHA) {
		return "", errors.New("invalid_commit")
	}
	return created.SHA, nil
}

func (c *Client) branchHead(ctx context.Context, token, repo, branch string) (string, bool, error) {
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	err := c.send(ctx, token, "GET", "/repos/"+repo+"/git/ref/heads/"+escapePath(branch), nil, &ref)
	var api apiError
	if errors.As(err, &api) && api.Status == 404 {
		return "", false, nil
	}
	return ref.Object.SHA, err == nil, err
}

// Publish writes an approved change set to a new ai-factory/ branch and opens a
// draft PR, or adds a commit to a branch this session created earlier. save is
// called after each completed step; a retry with the saved state resumes there.
func (c *Client) Publish(ctx context.Context, token string, r PublishRequest, state PublishState, save func(PublishState) error) (PublishState, error) {
	if err := r.validate(); err != nil {
		return state, err
	}
	if c.Demo {
		state = PublishState{Commit: strings.Repeat("b", 40), RefUpdated: true}
		if r.ExpectedHead == "" {
			state.PullNumber, state.PullURL = 1, "https://github.com/"+r.Repository+"/pull/1"
		}
		return state, save(state)
	}
	var repo struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
	}
	if err := c.send(ctx, token, "GET", "/repos/"+r.Repository, nil, &repo); err != nil {
		return state, err
	}
	if !repo.Permissions.Push {
		return state, errors.New("repository_write_forbidden")
	}
	parent := r.BaseCommit
	if r.ExpectedHead != "" {
		parent = r.ExpectedHead
	}
	if state.Commit == "" {
		sha, err := c.createCommit(ctx, token, r, parent)
		if err != nil {
			return state, err
		}
		state.Commit = sha
		if err := save(state); err != nil {
			return state, err
		}
	}
	if !state.RefUpdated {
		head, exists, err := c.branchHead(ctx, token, r.Repository, r.Branch)
		if err != nil {
			return state, err
		}
		switch {
		case exists && head == state.Commit:
			// A previous attempt updated the ref before its state was saved.
		case r.ExpectedHead == "":
			if exists {
				return state, errors.New("branch_exists")
			}
			if err := c.send(ctx, token, "POST", "/repos/"+r.Repository+"/git/refs", map[string]string{"ref": "refs/heads/" + r.Branch, "sha": state.Commit}, nil); err != nil {
				return state, err
			}
		default:
			if !exists || head != r.ExpectedHead {
				return state, errors.New("branch_moved")
			}
			// force:false makes GitHub reject anything but a fast-forward.
			if err := c.send(ctx, token, "PATCH", "/repos/"+r.Repository+"/git/refs/heads/"+escapePath(r.Branch), map[string]any{"sha": state.Commit, "force": false}, nil); err != nil {
				return state, err
			}
		}
		state.RefUpdated = true
		if err := save(state); err != nil {
			return state, err
		}
	}
	if r.ExpectedHead != "" || state.PullNumber != 0 {
		return state, nil
	}
	type pull struct {
		Number  int    `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	owner := strings.Split(r.Repository, "/")[0]
	var existing []pull
	if err := c.send(ctx, token, "GET", "/repos/"+r.Repository+"/pulls?state=all&head="+url.QueryEscape(owner+":"+r.Branch), nil, &existing); err != nil {
		return state, err
	}
	var p pull
	if len(existing) > 0 {
		p = existing[0]
	} else if err := c.send(ctx, token, "POST", "/repos/"+r.Repository+"/pulls", map[string]any{"title": r.Title, "body": r.Body, "head": r.Branch, "base": r.BaseBranch, "draft": true}, &p); err != nil {
		return state, err
	}
	if p.Number < 1 {
		return state, errors.New("invalid_pull_request")
	}
	state.PullNumber, state.PullURL = p.Number, p.HTMLURL
	return state, save(state)
}
