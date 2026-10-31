package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
)

var (
	baseSHA   = strings.Repeat("1", 40)
	rootTree  = strings.Repeat("2", 40)
	srcTree   = strings.Repeat("3", 40)
	newCommit = strings.Repeat("4", 40)
)

type fakeGitHub struct {
	t        *testing.T
	push     bool
	refs     map[string]string
	pulls    []map[string]any
	calls    []string
	treeBody map[string]any
	failOn   string
}

func newFake(t *testing.T) *fakeGitHub {
	return &fakeGitHub{t: t, push: true, refs: map[string]string{"main": baseSHA}}
}

func (f *fakeGitHub) client() *Client {
	return &Client{HTTP: &http.Client{Transport: roundTrip(f.serve)}}
}

func (f *fakeGitHub) serve(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer token" {
		f.t.Fatalf("unexpected request %s", r.URL)
	}
	route := r.Method + " " + strings.TrimPrefix(r.URL.Path, "/repos/owner/repo")
	f.calls = append(f.calls, route)
	if route == f.failOn {
		return &http.Response{StatusCode: 502, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	var in map[string]any
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&in)
	}
	reply := func(status int, v any) (*http.Response, error) {
		b, _ := json.Marshal(v)
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
	}
	switch {
	case route == "GET ":
		return reply(200, map[string]any{"permissions": map[string]bool{"push": f.push}})
	case route == "GET /git/commits/"+baseSHA || route == "GET /git/commits/"+newCommit:
		return reply(200, map[string]any{"tree": map[string]string{"sha": rootTree}})
	case route == "GET /git/trees/"+rootTree:
		return reply(200, map[string]any{"tree": []map[string]string{
			{"path": "README.md", "mode": "100644", "type": "blob", "sha": strings.Repeat("5", 40)},
			{"path": "run.sh", "mode": "100755", "type": "blob", "sha": strings.Repeat("6", 40)},
			{"path": "link", "mode": "120000", "type": "blob", "sha": strings.Repeat("7", 40)},
			{"path": "vendor", "mode": "160000", "type": "commit", "sha": strings.Repeat("8", 40)},
			{"path": "src", "mode": "040000", "type": "tree", "sha": srcTree},
		}})
	case route == "GET /git/trees/"+srcTree:
		return reply(200, map[string]any{"tree": []map[string]string{{"path": "a.go", "mode": "100644", "type": "blob", "sha": strings.Repeat("9", 40)}}})
	case route == "POST /git/blobs":
		return reply(201, map[string]string{"sha": strings.Repeat("a", 40)})
	case route == "POST /git/trees":
		f.treeBody = in
		return reply(201, map[string]string{"sha": strings.Repeat("b", 40)})
	case route == "POST /git/commits":
		return reply(201, map[string]string{"sha": newCommit})
	case strings.HasPrefix(route, "GET /git/ref/heads/"):
		sha, ok := f.refs[strings.TrimPrefix(route, "GET /git/ref/heads/")]
		if !ok {
			return reply(404, map[string]string{})
		}
		return reply(200, map[string]any{"object": map[string]string{"sha": sha}})
	case route == "POST /git/refs":
		name := strings.TrimPrefix(in["ref"].(string), "refs/heads/")
		if _, ok := f.refs[name]; ok {
			return reply(422, map[string]string{})
		}
		f.refs[name] = in["sha"].(string)
		return reply(201, map[string]string{})
	case strings.HasPrefix(route, "PATCH /git/refs/heads/"):
		if in["force"] != false {
			f.t.Fatal("force update requested")
		}
		f.refs[strings.TrimPrefix(route, "PATCH /git/refs/heads/")] = in["sha"].(string)
		return reply(200, map[string]string{})
	case route == "GET /pulls":
		if r.URL.Query().Get("state") != "open" {
			f.t.Fatal("existing PR lookup must be limited to open PRs")
		}
		return reply(200, f.pulls)
	case route == "POST /pulls":
		if in["draft"] != true || in["base"] != "main" {
			f.t.Fatalf("pull request not draft against base: %+v", in)
		}
		p := map[string]any{"number": len(f.pulls) + 7, "html_url": fmt.Sprintf("https://github.com/owner/repo/pull/%d", len(f.pulls)+7), "head": map[string]string{"sha": newCommit}}
		f.pulls = append(f.pulls, p)
		return reply(201, p)
	}
	f.t.Fatalf("unexpected route %s", route)
	return nil, nil
}

func request() PublishRequest {
	return PublishRequest{
		Repository: "owner/repo", BaseBranch: "main", BaseCommit: baseSHA, Branch: "ai-factory/add-a",
		Title: "Add a", Body: "AIが作成したPRです。", Message: "Add a",
		Files: []domain.FileChange{
			{Path: "src/a.go", Action: "update", Content: "package src\n"},
			{Path: "src/b.go", Action: "create", Content: "package src\n"},
			{Path: "run.sh", Action: "update", Content: "#!/bin/sh\n"},
			{Path: "README.md", Action: "delete"},
		},
		Approved: []string{"src/a.go", "src/b.go", "run.sh", "README.md"},
	}
}

func TestPublishCreatesBranchAndDraftPR(t *testing.T) {
	f := newFake(t)
	saves := 0
	state, err := f.client().Publish(context.Background(), "token", request(), PublishState{}, func(PublishState) error { saves++; return nil })
	if err != nil || state.Commit != newCommit || !state.RefUpdated || state.PullNumber != 7 || saves != 4 {
		t.Fatalf("publish failed: %+v %v saves=%d", state, err, saves)
	}
	if f.refs["ai-factory/add-a"] != newCommit || f.refs["main"] != baseSHA {
		t.Fatalf("refs: %+v", f.refs)
	}
	entries := f.treeBody["tree"].([]any)
	modes := map[string]any{}
	for _, e := range entries {
		m := e.(map[string]any)
		modes[m["path"].(string)] = m["mode"]
		if m["path"] == "README.md" && m["sha"] != nil {
			t.Fatal("delete must send a null sha")
		}
	}
	if modes["run.sh"] != "100755" || modes["src/b.go"] != "100644" || f.treeBody["base_tree"] != rootTree {
		t.Fatalf("tree: %+v", f.treeBody)
	}
}

func TestPublishResumesWithoutDuplicates(t *testing.T) {
	f := newFake(t)
	f.failOn = "POST /pulls"
	var saved PublishState
	save := func(s PublishState) error { saved = s; return nil }
	if _, err := f.client().Publish(context.Background(), "token", request(), PublishState{}, save); err == nil {
		t.Fatal("expected failure")
	}
	f.failOn, f.calls = "", nil
	state, err := f.client().Publish(context.Background(), "token", request(), saved, save)
	if err != nil || state.PullNumber != 7 {
		t.Fatalf("resume failed: %+v %v", state, err)
	}
	for _, c := range f.calls {
		if c == "POST /git/commits" || c == "POST /git/refs" {
			t.Fatalf("repeated write on resume: %v", f.calls)
		}
	}
	// A third attempt after the PR exists but before its state was saved reuses it.
	f.calls = nil
	state, err = f.client().Publish(context.Background(), "token", request(), PublishState{Commit: newCommit, RefUpdated: true}, save)
	if err != nil || state.PullNumber != 7 || len(f.pulls) != 1 {
		t.Fatalf("duplicate PR: %+v %v %d", state, err, len(f.pulls))
	}
}

func TestPublishRefusals(t *testing.T) {
	cases := map[string]func(*fakeGitHub, *PublishRequest){
		"no push":        func(f *fakeGitHub, r *PublishRequest) { f.push = false },
		"branch exists":  func(f *fakeGitHub, r *PublishRequest) { f.refs["ai-factory/add-a"] = baseSHA },
		"default branch": func(f *fakeGitHub, r *PublishRequest) { r.Branch = "main" },
		"symlink": func(f *fakeGitHub, r *PublishRequest) {
			r.Files = []domain.FileChange{{Path: "link", Action: "update", Content: "x"}}
			r.Approved = []string{"link"}
		},
		"submodule": func(f *fakeGitHub, r *PublishRequest) {
			r.Files = []domain.FileChange{{Path: "vendor/x.go", Action: "create", Content: "x"}}
			r.Approved = []string{"vendor/x.go"}
		},
		"create existing": func(f *fakeGitHub, r *PublishRequest) {
			r.Files = []domain.FileChange{{Path: "src/a.go", Action: "create", Content: "x"}}
			r.Approved = []string{"src/a.go"}
		},
		"update missing": func(f *fakeGitHub, r *PublishRequest) {
			r.Files = []domain.FileChange{{Path: "src/z.go", Action: "update", Content: "x"}}
			r.Approved = []string{"src/z.go"}
		},
		"unapproved":     func(f *fakeGitHub, r *PublishRequest) { r.Approved = r.Approved[:1] },
		"secret in body": func(f *fakeGitHub, r *PublishRequest) { r.Body = "ghp_abcdefghijklmnopqrstuvwxyz" },
	}
	for name, mutate := range cases {
		f := newFake(t)
		r := request()
		mutate(f, &r)
		if _, err := f.client().Publish(context.Background(), "token", r, PublishState{}, func(PublishState) error { return nil }); err == nil {
			t.Fatalf("%s accepted", name)
		}
		if f.refs["main"] != baseSHA {
			t.Fatalf("%s moved the base branch", name)
		}
	}
}

func TestPublishFollowUpCommit(t *testing.T) {
	f := newFake(t)
	f.refs["ai-factory/add-a"] = baseSHA
	r := request()
	r.ExpectedHead = baseSHA
	state, err := f.client().Publish(context.Background(), "token", r, PublishState{}, func(PublishState) error { return nil })
	if err != nil || f.refs["ai-factory/add-a"] != newCommit || state.PullNumber != 0 {
		t.Fatalf("follow-up failed: %+v %v", state, err)
	}
	for _, c := range f.calls {
		if strings.Contains(c, "/pulls") {
			t.Fatal("follow-up must not create a PR")
		}
	}
	// Someone else pushed to the branch: stop instead of overwriting.
	f = newFake(t)
	f.refs["ai-factory/add-a"] = strings.Repeat("c", 40)
	_, err = f.client().Publish(context.Background(), "token", r, PublishState{}, func(PublishState) error { return nil })
	if err == nil || err.Error() != "branch_moved" {
		t.Fatalf("moved branch not detected: %v", err)
	}
}

func TestPublishStopsWhenSaveFails(t *testing.T) {
	f := newFake(t)
	_, err := f.client().Publish(context.Background(), "token", request(), PublishState{}, func(PublishState) error { return errors.New("store") })
	if err == nil {
		t.Fatal("save failure ignored")
	}
	for _, c := range f.calls {
		if c == "POST /git/refs" {
			t.Fatal("continued writing after save failure")
		}
	}
}

func TestPublishIgnoresOtherPRsOnSameBranchName(t *testing.T) {
	f := newFake(t)
	// An open PR on this branch whose head is not our commit is not reused.
	f.pulls = []map[string]any{{"number": 3, "html_url": "https://github.com/owner/repo/pull/3", "head": map[string]string{"sha": strings.Repeat("e", 40)}}}
	_, err := f.client().Publish(context.Background(), "token", request(), PublishState{}, func(PublishState) error { return nil })
	if err == nil || err.Error() != "pull_request_conflict" {
		t.Fatalf("foreign PR reused: %v", err)
	}
}

func TestPublishRecordsBranchBeforeCreatingRef(t *testing.T) {
	f := newFake(t)
	var states []PublishState
	if _, err := f.client().Publish(context.Background(), "token", request(), PublishState{}, func(s PublishState) error { states = append(states, s); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(states) < 2 || states[1].RefBranch != "ai-factory/add-a" || states[1].RefUpdated {
		t.Fatalf("branch not recorded before ref creation: %+v", states)
	}
}
