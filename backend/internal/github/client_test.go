package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-factory/backend/internal/domain"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestSSECompletesWithoutWaitingForStreamToClose(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "event: message\ndata:{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"content\":[]}}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	client := &Client{HTTP: &http.Client{Timeout: time.Second, Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		req, _ := http.NewRequestWithContext(r.Context(), "GET", server.URL, nil)
		return server.Client().Do(req)
	})}}
	if _, err := client.rpc(context.Background(), "token", "tools/call", nil, ""); err != nil {
		t.Fatalf("SSE response waited for EOF: %v", err)
	}
}
func TestReadBoundary(t *testing.T) {
	target := domain.Target{Repository: "owner/repo", Commit: strings.Repeat("a", 40)}
	for _, read := range []Read{{Kind: "create_pull_request"}, {Kind: "file", Path: "../secret"}, {Kind: "file", Path: "/etc/passwd"}, {Kind: "pull_request", Number: 0}, {Kind: "issue", Number: 1, Detail: "delete"}} {
		if _, _, _, err := Tool(target, read); err == nil {
			t.Fatalf("unsafe tool accepted: %+v", read)
		}
	}
	name, args, source, err := Tool(target, Read{Kind: "file", Path: "src/a.go"})
	if err != nil || name != "get_file_contents" || args["owner"] != "owner" || args["ref"] != target.Commit || !strings.Contains(source.URL, target.Commit) {
		t.Fatalf("target not pinned: %s %+v %+v %v", name, args, source, err)
	}
}
func TestMCPProtocolAndResponseLimit(t *testing.T) {
	calls := 0
	client := &Client{HTTP: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host == "api.github.com" {
			return response(`{"full_name":"owner/repo"}`), nil
		}
		if r.URL.String() != "https://api.githubcopilot.com/mcp/readonly" || r.Header.Get("X-MCP-Readonly") != "true" || r.Header.Get("Authorization") != "Bearer github-token" {
			t.Fatal("missing authorization boundary")
		}
		var p struct {
			Method string `json:"method"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&p)
		switch p.Method {
		case "initialize":
			return response(`{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-03-26"}}`), nil
		case "notifications/initialized":
			return &http.Response{StatusCode: 202, Body: io.NopCloser(strings.NewReader(""))}, nil
		case "tools/call":
			if p.Params.Name != "get_file_contents" {
				t.Fatal("unexpected tool")
			}
			return response(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"resource","resource":{"uri":"repo://owner/repo/contents/README.md","mimeType":"text/plain; charset=utf-8","text":"README"}}]}}`), nil
		}
		t.Fatal("unexpected method")
		return nil, nil
	})}}
	target := domain.Target{Repository: "owner/repo", Commit: strings.Repeat("a", 40)}
	v, _, _, err := client.Read(context.Background(), "github-token", target, Read{Kind: "repository"})
	if err != nil || v != "README" || calls != 4 {
		t.Fatalf("MCP failure: %s %v calls=%d", v, err, calls)
	}
	client.HTTP.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		return response(strings.Repeat("x", domain.MaxFetchBytes+1)), nil
	})
	if _, err = client.rpc(context.Background(), "token", "initialize", nil, ""); err == nil {
		t.Fatal("unbounded MCP response")
	}
}
