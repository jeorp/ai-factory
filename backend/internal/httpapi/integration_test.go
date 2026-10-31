package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Fail    atomic.Bool
}

func (p *controlledAI) Next(ctx context.Context, key, model, input string) (agent.Decision, agent.Usage, error) {
	p.Calls.Add(1)
	// Overview preparation must finish before the review plan is saved.
	if strings.Contains(input, `"overviewTask":`) {
		return agent.Decision{Summary: "テスト用リポジトリの概要"}, agent.Usage{InputTokens: 12}, nil
	}
	select {
	case p.Started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return agent.Decision{}, agent.Usage{}, ctx.Err()
	case <-p.Gate:
	}
	if p.Fail.Load() {
		return agent.Decision{}, agent.Usage{}, errors.New("review_read_not_allowed")
	}
	text := "取得内容を確認しました"
	if strings.Contains(input, "認証を優先") {
		text = "認証を優先して確認しました"
	}
	if strings.Contains(input, `"reviewTask":`) {
		return agent.Decision{Summary: "レビュー区切りの要点", Review: &domain.ReviewResult{Findings: []domain.Finding{}, Unverified: []string{"関連コードを確認できませんでした"}}}, agent.Usage{InputTokens: 42}, nil
	}
	var chat struct {
		Active  int              `json:"activePullRequest"`
		History []domain.Message `json:"recentHistory"`
	}
	if json.Unmarshal([]byte(input), &chat) == nil && len(chat.History) > 0 {
		latest := ""
		for i := len(chat.History) - 1; i >= 0; i-- {
			if chat.History[i].Role == "user" {
				latest = chat.History[i].Text
				break
			}
		}
		if latest == "PRを取得して" && chat.Active == 0 {
			return agent.Decision{Read: &github.Read{Kind: "pull_request", Number: 7, Detail: "summary"}}, agent.Usage{InputTokens: 42}, nil
		}
		if latest == "このPRをレビューして" {
			return agent.Decision{StartReview: &agent.ReviewRequest{}}, agent.Usage{InputTokens: 42}, nil
		}
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
	gh := &github.Client{Demo: true, Cache: &github.FirestoreReadCache{DB: db}}
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
	initialID := session.ID
	// The browser uses the same authorized, pinned read cache as the AI.
	filePath := "/api/v1/sessions/" + initialID + "/file?path=src/" + randomID() + ".go"
	for i := 0; i < 2; i++ {
		resp = request(one, "GET", filePath, nil)
		var file struct {
			Repository, Commit, Path, Content string
			Cached                            bool
		}
		err := json.NewDecoder(resp.Body).Decode(&file)
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 || file.Repository != session.Target.Repository || file.Commit != session.Target.Commit || file.Content == "" || file.Cached != (i == 1) {
			t.Fatalf("file cache retrieval %d: %+v status=%d err=%v", i, file, resp.StatusCode, err)
		}
	}
	for _, test := range []struct {
		who    identity
		path   string
		status int
	}{
		{two, filePath, 404},
		{one, filePath + "&commit=" + strings.Repeat("b", 40), 422},
		{one, "/api/v1/sessions/" + initialID + "/file?path=../secret", 422},
		{one, "/api/v1/sessions/" + initialID + "/file?path=image.png", 422},
	} {
		resp = request(test.who, "GET", test.path, nil)
		resp.Body.Close()
		if resp.StatusCode != test.status {
			t.Fatalf("file boundary: status %d, want %d", resp.StatusCode, test.status)
		}
	}
	t.Cleanup(func() { storage.Delete(ctx, one.uid, initialID) })
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
	// A PR run persists a plan, stops, and resumes through the real socket and Firestore.
	three := user()
	resp = request(three, "POST", "/api/v1/sessions", map[string]string{"target": "https://github.com/demo/ai-factory/pull/7", "model": "gpt-5-mini"})
	var prSession domain.Session
	json.NewDecoder(resp.Body).Decode(&prSession)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatal("PR session creation failed")
	}
	t.Cleanup(func() { storage.Delete(ctx, three.uid, prSession.ID) })
	session = prSession
	ws = dial(three)
	defer ws.Close()
	wait(ws, func(s domain.Session) bool { return s.ID == prSession.ID })
	before = ai.Calls.Load()
	ws.WriteJSON(command{Type: "review", ID: randomID(), Text: "PRをレビュー", APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	plan := wait(ws, func(s domain.Session) bool { return s.Review != nil && s.Review.Phase == "diff" })
	if plan.RunKind != "review" || plan.Review.Base == plan.Review.Head || len(plan.Review.Steps) != 2 {
		t.Fatal("review plan not saved")
	}
	for ai.Calls.Load() == before {
		time.Sleep(time.Millisecond)
	}
	ws.WriteJSON(command{Type: "stop"})
	stopped = wait(ws, func(s domain.Session) bool { return s.State == "stopped" })
	if stopped.Review == nil || stopped.Review.Phase != "diff" {
		t.Fatal("stopped review lost its plan")
	}
	close(ai.Gate)
	ws.WriteJSON(command{Type: "resume", APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	reviewed := wait(ws, func(s domain.Session) bool { return s.State == "done" })
	if reviewed.Review == nil || reviewed.Review.Phase != "done" || reviewed.Target.Commit != reviewed.Review.Head || reviewed.Review.CrossCursor != 2 {
		t.Fatal("review did not resume to completion")
	}
	persisted, e := storage.Get(ctx, three.uid, prSession.ID)
	if e != nil || persisted.Review == nil || persisted.Review.Phase != "done" {
		t.Fatalf("review not restored from Firestore: %v", e)
	}
	raw, _ = json.Marshal(persisted)
	if strings.Contains(string(raw), "sk-test-private-key") || strings.Contains(string(raw), "@@ -") {
		t.Fatal("credentials or patches persisted")
	}
	// Retrieve a PR from a repository chat, then review it in the same chat.
	ws.Close()
	four := user()
	resp = request(four, "POST", "/api/v1/sessions", map[string]string{"target": "demo/ai-factory", "model": "gpt-5-mini"})
	var chatSession domain.Session
	json.NewDecoder(resp.Body).Decode(&chatSession)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatal("chat session creation failed")
	}
	t.Cleanup(func() { storage.Delete(ctx, four.uid, chatSession.ID) })
	session = chatSession
	ws = dial(four)
	defer ws.Close()
	wait(ws, func(s domain.Session) bool { return s.ID == chatSession.ID })
	ws.WriteJSON(command{Type: "message", ID: randomID(), Text: "PRを取得して", APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	retrieved := wait(ws, func(s domain.Session) bool { return s.State == "done" })
	if retrieved.Target.Kind != "repository" || retrieved.ActivePullRequest != 7 {
		t.Fatal("retrieved PR was not retained in the repository chat")
	}
	retained, e := storage.Get(ctx, four.uid, chatSession.ID)
	if e != nil || retained.ActivePullRequest != 7 {
		t.Fatal("PR selection was not persisted")
	}
	ws.WriteJSON(command{Type: "message", ID: randomID(), Text: "このPRをレビューして", APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	chatReviewed := wait(ws, func(s domain.Session) bool { return s.State == "done" && s.Review != nil })
	if chatReviewed.ID != chatSession.ID || chatReviewed.RunKind != "review" || chatReviewed.Target.Number != 7 || chatReviewed.Review.Phase != "done" || len(chatReviewed.Messages) != 4 {
		t.Fatalf("chat review handoff failed: %+v", chatReviewed)
	}
	resp = request(four, "GET", "/api/v1/sessions/"+chatSession.ID, nil)
	var restoredReview domain.Session
	err = json.NewDecoder(resp.Body).Decode(&restoredReview)
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || restoredReview.Review == nil || restoredReview.Review.Findings == nil || restoredReview.Review.Unverified == nil || len(restoredReview.Review.Steps) != len(chatReviewed.Review.Steps) {
		t.Fatalf("review reload cannot render empty findings: %+v %v", restoredReview.Review, err)
	}
	index := 0
	if len(chatReviewed.Review.Unverified) == 0 {
		t.Fatal("missing unverified item")
	}
	ws.WriteJSON(command{Type: "investigate", ID: randomID(), UnverifiedIndex: &index, APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	investigated := wait(ws, func(s domain.Session) bool {
		return s.State == "done" && s.RunKind == "investigation" && s.Review != nil && s.Review.Investigations["0"] != ""
	})
	if investigated.Review.PendingInvestigation != 0 || investigated.Review.Unverified[0] != chatReviewed.Review.Unverified[0] {
		t.Fatal("investigation did not preserve the original item")
	}
	persistedInvestigation, e := storage.Get(ctx, four.uid, chatSession.ID)
	if e != nil || persistedInvestigation.Review.Investigations["0"] != investigated.Review.Investigations["0"] {
		t.Fatalf("investigation not persisted: %v", e)
	}
	// A non-provider error must reach the browser even when a review is present.
	ai.Fail.Store(true)
	ws.WriteJSON(command{Type: "message", ID: randomID(), Text: "失敗通知の確認", APIKey: "sk-test-private-key-123456", KeyExpiresAt: time.Now().Add(time.Hour).UnixMilli()})
	failed := wait(ws, func(s domain.Session) bool { return s.State == "failed" })
	if failed.Review == nil || failed.Limitations[len(failed.Limitations)-1] != "review_read_not_allowed" {
		t.Fatal("review failure reason was not retained")
	}
	var failure envelope
	if err := ws.ReadJSON(&failure); err != nil || failure.Type != "error" || !strings.Contains(string(failure.Data), "review_read_not_allowed") {
		t.Fatalf("review failure was silent: %+v %v", failure, err)
	}
	t.Log(fmt.Sprintf("verified: isolation, directives, stop/resume, PR retrieval and full review in one conversation (%d model calls)", ai.Calls.Load()))
}
