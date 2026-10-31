package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ai-factory/backend/internal/agent"
	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
	"ai-factory/backend/internal/store"
	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"github.com/gorilla/websocket"
)

type controlledAI struct {
	Gate    chan struct{}
	Started chan struct{}
	Calls   atomic.Int32
}

func (p *controlledAI) Next(ctx context.Context, key, model, input string) (agent.Decision, agent.Usage, error) {
	p.Calls.Add(1)
	select {
	case p.Started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return agent.Decision{}, agent.Usage{}, ctx.Err()
	case <-p.Gate:
	}
	text := "取得内容を確認しました"
	if strings.Contains(input, "認証を優先") {
		text = "認証を優先して確認しました"
	}
	return agent.Decision{Answer: text, Summary: "確認済みの要点"}, agent.Usage{InputTokens: 42}, nil
}
func TestEmulatorSessionLifecycle(t *testing.T) {
	if os.Getenv("RUN_EMULATOR_TESTS") != "1" {
		t.Skip("set RUN_EMULATOR_TESTS=1 and both emulator hosts")
	}
	ctx := context.Background()
	project := "demo-ai-factory"
	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: project})
	if err != nil {
		t.Fatal(err)
	}
	auth, err := app.Auth(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db, err := firestore.NewClient(ctx, project)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ai := &controlledAI{Gate: make(chan struct{}), Started: make(chan struct{}, 5)}
	gh := &github.Client{Demo: true}
	storage := &store.Store{DB: db}
	cfg := Config{Local: true, Demo: false, Project: project, Origin: "http://app.test", Models: []string{"gpt-5-mini"}, CookieKey: []byte(strings.Repeat("a", 32))}
	api := New(cfg, auth, storage, gh, &agent.Runner{AI: ai, GitHub: gh}, http.DefaultClient)
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()
	type identity struct{ uid, token, cookie string }
	user := func() identity {
		t.Helper()
		uid := "github:test:" + randomID()
		custom, e := auth.CustomToken(ctx, uid)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := json.Marshal(map[string]any{"token": custom, "returnSecureToken": true})
		resp, e := http.Post("http://"+os.Getenv("FIREBASE_AUTH_EMULATOR_HOST")+"/identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key=local", "application/json", bytes.NewReader(body))
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		var token struct {
			IDToken string `json:"idToken"`
		}
		if json.NewDecoder(resp.Body).Decode(&token) != nil || token.IDToken == "" {
			t.Fatal("emulator token exchange failed")
		}
		cookie, e := seal(cfg.CookieKey, "factory_github", credential{UID: uid, Login: "test", Token: "demo-github", Expires: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { auth.DeleteUser(ctx, uid) })
		return identity{uid, token.IDToken, "factory_github=" + cookie}
	}
	one, two := user(), user()
	request := func(who identity, method, path string, body any) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
		req.Header.Set("Origin", cfg.Origin)
		req.Header.Set("Authorization", "Bearer "+who.token)
		req.Header.Set("Cookie", who.cookie)
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return resp
	}
	resp := request(one, "POST", "/api/v1/sessions", map[string]string{"target": "demo/ai-factory", "model": "gpt-5-mini"})
	var session domain.Session
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create failed %d", resp.StatusCode)
	}
	t.Cleanup(func() { storage.Delete(ctx, one.uid, session.ID) })
	resp = request(two, "GET", "/api/v1/sessions/"+session.ID, nil)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("cross-user session access allowed")
	}
	dial := func(who identity) *websocket.Conn {
		t.Helper()
		headers := http.Header{"Origin": []string{cfg.Origin}, "Cookie": []string{who.cookie}}
		ws, _, e := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/sessions/"+session.ID+"/ws", headers)
		if e != nil {
			t.Fatal(e)
		}
		ws.WriteJSON(command{Type: "auth", Token: who.token})
		return ws
	}
	type envelope struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	wait := func(ws *websocket.Conn, predicate func(domain.Session) bool) domain.Session {
		t.Helper()
		ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			var msg envelope
			if e := ws.ReadJSON(&msg); e != nil {
				t.Fatal(e)
			}
			if msg.Type == "error" {
				t.Fatalf("socket error %s", msg.Data)
			}
			if msg.Type == "session" {
				var s domain.Session
				json.Unmarshal(msg.Data, &s)
				if predicate(s) {
					return s
				}
			}
		}
	}
	ws := dial(one)
	defer ws.Close()
	initial := wait(ws, func(s domain.Session) bool { return s.ID == session.ID })
	if initial.Messages == nil || initial.Sources == nil || initial.Limitations == nil {
		t.Fatal("empty collections must serialize as arrays")
	}
	duplicate := dial(one)
	var conflict envelope
	duplicate.ReadJSON(&conflict)
	duplicate.Close()
	if conflict.Type != "error" || !strings.Contains(string(conflict.Data), "session_busy") {
		t.Fatal("duplicate execution allowed")
	}
	ws.WriteJSON(command{Type: "message", ID: randomID(), Text: "期限切れキー", APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(-time.Second).UnixMilli()})
	var denied envelope
	ws.ReadJSON(&denied)
	if denied.Type != "error" || !strings.Contains(string(denied.Data), "ai_key_required") || ai.Calls.Load() != 0 {
		t.Fatal("expired AI credentials accepted")
	}
	ws.WriteJSON(command{Type: "message", ID: randomID(), Text: "構成を確認", APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	wait(ws, func(s domain.Session) bool { return s.State == "running" })
	select {
	case <-ai.Started:
	case <-time.After(5 * time.Second):
		t.Fatal("AI did not start")
	}
	ws.WriteJSON(command{Type: "instruction", ID: randomID(), Text: "認証を優先"})
	wait(ws, func(s domain.Session) bool { return len(s.Messages) == 2 })
	close(ai.Gate)
	finished := wait(ws, func(s domain.Session) bool { return s.State == "done" })
	if len(finished.Messages) != 3 || !finished.Messages[1].Applied || !strings.Contains(finished.Messages[2].Text, "認証を優先") {
		t.Fatalf("instruction not applied: %+v", finished.Messages)
	}
	ws.Close()
	// Wait until disconnect cleanup releases the lease, then reconstruct state on a new connection.
	deadline := time.Now().Add(5 * time.Second)
	for {
		saved, e := storage.Get(ctx, one.uid, session.ID)
		if e != nil {
			t.Fatal(e)
		}
		if saved.Lease == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("lease not released")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ws = dial(one)
	restored := wait(ws, func(s domain.Session) bool { return len(s.Messages) == 3 })
	if restored.Summary == "" {
		t.Fatal("checkpoint not restored")
	}
	// A new blocked call is canceled through the socket. No assistant response may be persisted.
	ai.Gate = make(chan struct{})
	before := ai.Calls.Load()
	ws.WriteJSON(command{Type: "message", ID: randomID(), Text: "追加の質問", APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	wait(ws, func(s domain.Session) bool { return s.State == "running" })
	for ai.Calls.Load() == before {
		time.Sleep(time.Millisecond)
	}
	ws.WriteJSON(command{Type: "stop"})
	stopped := wait(ws, func(s domain.Session) bool { return s.State == "stopped" })
	if len(stopped.Messages) != 4 {
		t.Fatal("late AI result persisted after stop")
	}
	ws.Close()
	saved, e := storage.Get(ctx, one.uid, session.ID)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(saved)
	if strings.Contains(string(raw), "APIKey") || strings.Contains(string(raw), "sk-test-private-key-123456") || strings.Contains(string(raw), "demo-github") {
		t.Fatal("credentials persisted")
	}
	t.Log(fmt.Sprintf("verified: user isolation, exclusive lease, directives, cancellation, reconnect (%d model calls)", ai.Calls.Load()))
}
