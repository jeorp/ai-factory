package github

import (
	"bufio"
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

type Client struct {
	HTTP *http.Client
	Demo bool
}
type Repository struct {
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
	HTMLURL       string `json:"html_url"`
}
type User struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
}

func (c *Client) get(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return errors.New("github_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("github_http_%d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024+1))
	if err != nil {
		return err
	}
	if len(b) > 512*1024 {
		return errors.New("github_response_limit")
	}
	return json.Unmarshal(b, out)
}
func (c *Client) User(ctx context.Context, token string) (User, error) {
	if c.Demo {
		return User{ID: 1, Login: "local-developer"}, nil
	}
	var v User
	err := c.get(ctx, token, "/user", &v)
	return v, err
}
func (c *Client) Repositories(ctx context.Context, token string) ([]Repository, error) {
	if c.Demo {
		return []Repository{{FullName: "demo/ai-factory", DefaultBranch: "main", HTMLURL: "https://github.com/demo/ai-factory"}}, nil
	}
	out := []Repository{}
	for page := 1; page <= 10; page++ {
		var v struct {
			Installations []struct {
				ID int64 `json:"id"`
			} `json:"installations"`
		}
		if err := c.get(ctx, token, fmt.Sprintf("/user/installations?per_page=100&page=%d", page), &v); err != nil {
			return nil, err
		}
		for _, i := range v.Installations {
			for rp := 1; rp <= 10; rp++ {
				var r struct {
					Repositories []Repository `json:"repositories"`
				}
				if err := c.get(ctx, token, fmt.Sprintf("/user/installations/%d/repositories?per_page=100&page=%d", i.ID, rp), &r); err != nil {
					return nil, err
				}
				out = append(out, r.Repositories...)
				if len(out) > 1000 {
					return nil, errors.New("repository_list_limit")
				}
				if len(r.Repositories) < 100 {
					break
				}
				if rp == 10 {
					return nil, errors.New("repository_list_limit")
				}
			}
		}
		if len(v.Installations) < 100 {
			return out, nil
		}
	}
	return nil, errors.New("installation_list_limit")
}
func (c *Client) Authorize(ctx context.Context, token, repository string) (Repository, error) {
	// GitHub App user tokens enforce the intersection of user and App access.
	if c.Demo {
		if repository != "demo/ai-factory" {
			return Repository{}, errors.New("repository_forbidden")
		}
		return Repository{FullName: repository, DefaultBranch: "main"}, nil
	}
	var r Repository
	err := c.get(ctx, token, "/repos/"+repository, &r)
	return r, err
}
func (c *Client) Commit(ctx context.Context, token string, r Repository) (string, error) {
	if c.Demo {
		return strings.Repeat("a", 40), nil
	}
	var v struct {
		SHA string `json:"sha"`
	}
	err := c.get(ctx, token, "/repos/"+r.FullName+"/commits/"+url.PathEscape(r.DefaultBranch), &v)
	if err == nil && !domain.ValidCommit(v.SHA) {
		err = errors.New("invalid_commit")
	}
	return v.SHA, err
}

type Read struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	Number int    `json:"number"`
	Detail string `json:"detail"`
}

func (a Read) Validate() error {
	switch a.Kind {
	case "repository", "file", "pull_request", "issue", "pull_requests", "issues":
	default:
		return errors.New("read_tool_not_allowed")
	}
	if strings.Contains(a.Path, "..") || strings.HasPrefix(a.Path, "/") || strings.ContainsAny(a.Path, "\\\x00") || len(a.Path) > 512 {
		return errors.New("invalid_file_path")
	}
	if a.Kind == "file" {
		for _, suffix := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf", ".zip", ".mp4", ".woff", ".woff2", ".exe", ".lock", ".min.js", ".map"} {
			if strings.HasSuffix(strings.ToLower(a.Path), suffix) {
				return errors.New("binary_or_generated_file_excluded")
			}
		}
	}
	if (a.Kind == "pull_request" || a.Kind == "issue") && (a.Number < 1 || a.Number > 100000000) {
		return errors.New("invalid_number")
	}
	if a.Detail != "" && a.Detail != "summary" && a.Detail != "files" && a.Detail != "comments" {
		return errors.New("invalid_read_detail")
	}
	return nil
}
func Tool(t domain.Target, a Read) (string, map[string]any, domain.Source, error) {
	if err := a.Validate(); err != nil {
		return "", nil, domain.Source{}, err
	}
	p := strings.Split(t.Repository, "/")
	if len(p) != 2 || !domain.ValidCommit(t.Commit) {
		return "", nil, domain.Source{}, errors.New("invalid_target")
	}
	args := map[string]any{"owner": p[0], "repo": p[1]}
	name := ""
	link := "https://github.com/" + t.Repository
	label := a.Kind
	switch a.Kind {
	case "repository", "file":
		name = "get_file_contents"
		args["path"] = a.Path
		args["ref"] = t.Commit
		link += "/tree/" + t.Commit
		if a.Kind == "file" {
			link = "https://github.com/" + t.Repository + "/blob/" + t.Commit + "/" + escapePath(a.Path)
		}
		label = a.Path
		if label == "" {
			label = "ファイル一覧"
		}
	case "pull_request":
		name = "pull_request_read"
		args["method"] = "get"
		args["pullNumber"] = a.Number
		if a.Detail == "files" {
			args["method"] = "get_files"
			args["perPage"] = 10
		}
		if a.Detail == "comments" {
			args["method"] = "get_comments"
			args["perPage"] = 10
		}
		link += fmt.Sprintf("/pull/%d", a.Number)
	case "issue":
		name = "issue_read"
		args["method"] = "get"
		args["issue_number"] = a.Number
		if a.Detail == "comments" {
			args["method"] = "get_comments"
			args["perPage"] = 10
		}
		link += fmt.Sprintf("/issues/%d", a.Number)
	case "pull_requests":
		name = "list_pull_requests"
		args["state"] = "open"
		args["perPage"] = 10
		link += "/pulls"
	case "issues":
		name = "list_issues"
		args["state"] = "OPEN"
		args["perPage"] = 10
		link += "/issues"
	}
	return name, args, domain.Source{Label: label, URL: link}, nil
}
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return strings.Join(parts, "/")
}

// RPC only accepts names constructed by Tool. Remote tools are never exposed directly to the model.
func (c *Client) Read(ctx context.Context, token string, t domain.Target, a Read) (string, domain.Source, int, error) {
	name, args, source, err := Tool(t, a)
	if err != nil {
		return "", source, 0, err
	}
	if _, err = c.Authorize(ctx, token, t.Repository); err != nil {
		return "", source, 0, err
	}
	if c.Demo {
		v := `{"name":"AI Factory demo","description":"固定デモのGitHub読み取り結果。実在するリポジトリを参照していません。","files":["README.md","backend/cmd/api/main.go","frontend/web/src/App.vue"],"note":"GitHub MCP → bounded context → OpenAI → WebSocket という構成です。"}`
		if a.Kind == "file" {
			v = "# AI Factory\nGo API と Vue 3 の GitHub 読み取りチャット。"
		}
		return v, source, len(v), nil
	}
	init, err := c.rpc(ctx, token, "initialize", map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "ai-factory", "version": "0.1.0"}}, "")
	if err != nil {
		return "", source, 0, err
	}
	if _, err = c.rpc(ctx, token, "notifications/initialized", map[string]any{}, init.session); err != nil {
		return "", source, 0, err
	}
	defer func() {
		if init.session != "" {
			req, _ := http.NewRequestWithContext(context.WithoutCancel(ctx), "DELETE", "https://api.githubcopilot.com/mcp/readonly", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Mcp-Session-Id", init.session)
			resp, e := c.HTTP.Do(req)
			if e == nil {
				resp.Body.Close()
			}
		}
	}()
	result, err := c.rpc(ctx, token, "tools/call", map[string]any{"name": name, "arguments": args}, init.session)
	if err != nil {
		return "", source, 0, err
	}
	var v struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			Resource *struct {
				Text     *string `json:"text"`
				Blob     string  `json:"blob"`
				MimeType string  `json:"mimeType"`
			} `json:"resource"`
		} `json:"content"`
	}
	if err = json.Unmarshal(result.data, &v); err != nil {
		return "", source, 0, errors.New("invalid_mcp_result")
	}
	if v.IsError {
		return "", source, 0, errors.New("github_mcp_read_failed")
	}
	var b strings.Builder
	for _, item := range v.Content {
		if item.Type == "text" {
			b.WriteString(item.Text)
		} else if item.Type == "resource" && item.Resource != nil && item.Resource.Text != nil && item.Resource.Blob == "" {
			// GitHub file reads return embedded text resources, including empty files.
			mime := strings.Split(item.Resource.MimeType, ";")[0]
			if mime != "" && !strings.HasPrefix(mime, "text/") && mime != "application/json" && mime != "application/javascript" && mime != "application/yaml" && mime != "application/toml" {
				return "", source, result.bytes, errors.New("binary_content_excluded")
			}
			b.WriteString(*item.Resource.Text)
		} else if item.Type == "resource_link" {
			return "", source, result.bytes, errors.New("file_content_not_returned_download_link_only")
		} else {
			return "", source, result.bytes, errors.New("binary_content_excluded")
		}
	}
	return b.String(), source, result.bytes, nil
}

type rpcResult struct {
	data    json.RawMessage
	session string
	bytes   int
}

func (c *Client) rpc(ctx context.Context, token, method string, params any, session string) (rpcResult, error) {
	payload := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !strings.HasPrefix(method, "notifications/") {
		payload["id"] = 1
	}
	b, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.githubcopilot.com/mcp/readonly", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-MCP-Readonly", "true")
	req.Header.Set("MCP-Protocol-Version", "2025-03-26")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return rpcResult{}, errors.New("github_mcp_unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 202 {
		return rpcResult{}, nil
	}
	if resp.StatusCode != 200 {
		return rpcResult{}, fmt.Errorf("github_mcp_http_%d", resp.StatusCode)
	}
	var body []byte
	n := 0
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		reader := bufio.NewReader(io.LimitReader(resp.Body, domain.MaxFetchBytes+1))
		var event []byte
		for {
			line, readErr := reader.ReadBytes('\n')
			n += len(line)
			if n > domain.MaxFetchBytes {
				return rpcResult{}, errors.New("github_mcp_response_limit_100kib")
			}
			line = bytes.TrimRight(line, "\r\n")
			if bytes.HasPrefix(line, []byte("data:")) {
				event = append(event, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
				event = append(event, '\n')
			}
			if len(line) == 0 || readErr != nil {
				var candidate struct {
					ID int `json:"id"`
				}
				if json.Unmarshal(event, &candidate) == nil && candidate.ID == 1 {
					body = event
					break
				}
				event = nil
			}
			if readErr != nil {
				return rpcResult{}, errors.New("github_mcp_stream_incomplete")
			}
		}
	} else {
		body, err = io.ReadAll(io.LimitReader(resp.Body, domain.MaxFetchBytes+1))
		n = len(body)
		if err != nil {
			return rpcResult{}, errors.New("github_mcp_read_failed")
		}
		if n > domain.MaxFetchBytes {
			return rpcResult{}, errors.New("github_mcp_response_limit_100kib")
		}
	}
	var v struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &v) != nil || v.Error != nil || len(v.Result) == 0 {
		return rpcResult{}, errors.New("github_mcp_protocol_error")
	}
	return rpcResult{v.Result, resp.Header.Get("Mcp-Session-Id"), n}, nil
}
