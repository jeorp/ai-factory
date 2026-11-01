package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

// Exercises plan → approval → changes → diff → publish over the real
// WebSocket with Firestore, using the fixed demo AI and demo GitHub writes.
func TestEmulatorImplementationLifecycle(t *testing.T) {
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
	gh := &github.Client{Demo: true, Cache: &github.FirestoreReadCache{DB: db}}
	storage := &store.Store{DB: db}
	cfg := Config{Local: true, AIProvider: "demo", Project: project, Origin: "http://app.test", Models: []string{"gpt-5-mini"}, CookieKey: []byte(strings.Repeat("a", 32))}
	api := New(cfg, auth, storage, gh, &agent.Runner{AI: agent.Demo{}, GitHub: gh, Writer: gh}, http.DefaultClient)
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()

	uid := "github:test:" + randomID()
	custom, err := auth.CustomToken(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { auth.DeleteUser(ctx, uid) })
	body, _ := json.Marshal(map[string]any{"token": custom, "returnSecureToken": true})
	resp, err := http.Post("http://"+os.Getenv("FIREBASE_AUTH_EMULATOR_HOST")+"/identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken?key=local", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var token struct {
		IDToken string `json:"idToken"`
	}
	json.NewDecoder(resp.Body).Decode(&token)
	resp.Body.Close()
	cookie, err := seal(cfg.CookieKey, "factory_github", credential{UID: uid, Login: "test", Token: "demo-github", Expires: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body any) *http.Response {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, srv.URL+path, bytes.NewReader(raw))
		req.Header.Set("Origin", cfg.Origin)
		req.Header.Set("Authorization", "Bearer "+token.IDToken)
		req.Header.Set("Cookie", "factory_github="+cookie)
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		return resp
	}
	resp = request("POST", "/api/v1/sessions", map[string]string{"target": "demo/ai-factory", "model": "gpt-5-mini"})
	var session domain.Session
	json.NewDecoder(resp.Body).Decode(&session)
	resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("create failed %d", resp.StatusCode)
	}
	t.Cleanup(func() { storage.Delete(ctx, uid, session.ID) })

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/api/v1/sessions/"+session.ID+"/ws", http.Header{"Origin": []string{cfg.Origin}, "Cookie": []string{"factory_github=" + cookie}})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	ws.WriteJSON(command{Type: "auth", Token: token.IDToken})
	type envelope struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	wait := func(predicate func(domain.Session) bool, allowError string) (domain.Session, string) {
		t.Helper()
		ws.SetReadDeadline(time.Now().Add(15 * time.Second))
		for {
			var msg envelope
			if e := ws.ReadJSON(&msg); e != nil {
				t.Fatal(e)
			}
			if msg.Type == "error" {
				var v struct{ Error string }
				json.Unmarshal(msg.Data, &v)
				if v.Error == allowError {
					return domain.Session{}, v.Error
				}
				t.Fatalf("socket error %s", msg.Data)
			}
			if msg.Type == "session" {
				var s domain.Session
				json.Unmarshal(msg.Data, &s)
				if predicate(s) {
					return s, ""
				}
			}
		}
	}
	wait(func(s domain.Session) bool { return s.ID == session.ID }, "")
	// Each instruction is bounded individually; old instructions must not block
	// a newer one after their combined size exceeds the model's pinned budget.
	for _, text := range []string{strings.Repeat("旧", 1000), strings.Repeat("新", 1000)} {
		id := randomID()
		ws.WriteJSON(command{Type: "instruction", ID: id, Text: text})
		wait(func(s domain.Session) bool { return s.HasMessage(id) }, "")
	}
	// The final answer is saved before the run's completion is processed, so
	// a command sent right after it can still see the run in progress.
	sendWhenIdle := func(cmd command, predicate func(domain.Session) bool) domain.Session {
		t.Helper()
		for i := 0; i < 20; i++ {
			ws.WriteJSON(cmd)
			s, e := wait(predicate, "run_in_progress")
			if e == "" {
				return s
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("run never finished")
		return domain.Session{}
	}

	// Approval and publish are refused before a plan exists.
	ws.WriteJSON(command{Type: "implement_publish", Branch: "ai-factory/x", Title: "x"})
	if _, e := wait(func(domain.Session) bool { return false }, "implementation_not_ready"); e == "" {
		t.Fatal("publish accepted without changes")
	}

	ws.WriteJSON(command{Type: "message", ID: randomID(), Text: "READMEに説明を追加して"})
	planned, _ := wait(func(s domain.Session) bool {
		return s.State == "done" && s.Implementation != nil && s.Implementation.Status == "plan_ready"
	}, "")
	if planned.RunKind != "implement" || planned.Implementation.Plan == nil || len(planned.Implementation.Changes) != 0 {
		t.Fatalf("plan: %+v", planned.Implementation)
	}
	raw, _ := json.Marshal(planned)
	if strings.Contains(string(raw), "cacheId") || strings.Contains(string(raw), "CacheID") {
		t.Fatal("cache id exposed to the browser")
	}

	ready := sendWhenIdle(command{Type: "implement_approve"}, func(s domain.Session) bool {
		return s.State == "done" && s.Implementation != nil && s.Implementation.Status == "ready"
	})
	impl := ready.Implementation
	if impl.Verification == nil || len(impl.Verification.Unverified) == 0 {
		t.Fatalf("verification result missing from websocket: %+v", impl)
	}
	if len(impl.Changes) != 1 || impl.Changes[0].Path != "README.md" || !strings.HasPrefix(impl.Branch, "ai-factory/") {
		t.Fatalf("changes: %+v", impl)
	}

	resp = request("GET", "/api/v1/sessions/"+session.ID+"/change?path=README.md", nil)
	var change struct{ Path, Action, Original, Content string }
	json.NewDecoder(resp.Body).Decode(&change)
	resp.Body.Close()
	if resp.StatusCode != 200 || change.Original == "" || !strings.Contains(change.Content, "固定デモで追加") || !strings.HasPrefix(change.Content, strings.SplitN(change.Original, "\n", 2)[0]) {
		t.Fatalf("change diff: %d %+v", resp.StatusCode, change)
	}
	resp = request("GET", "/api/v1/sessions/"+session.ID+"/change?path=other.md", nil)
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("unknown change served: %d", resp.StatusCode)
	}

	// Replanning before publication carries the saved draft through Firestore
	// into a fresh cache/version, then edits that content instead of the base.
	oldVersion, oldContent := impl.Version, change.Content
	revised := sendWhenIdle(command{Type: "implement", ID: randomID(), Text: "追加の説明も入れて"}, func(s domain.Session) bool {
		return s.State == "done" && s.Implementation != nil && s.Implementation.Version > oldVersion && s.Implementation.Status == "plan_ready"
	})
	if !revised.Implementation.Revising || len(revised.Implementation.Changes) != 1 {
		t.Fatalf("draft lost at replan: %+v", revised.Implementation)
	}
	storedRevision, err := storage.Get(ctx, uid, session.ID)
	if err != nil || storedRevision.Implementation.DraftSource != nil || !storedRevision.Implementation.Revising {
		t.Fatalf("revision not durable: %+v %v", storedRevision, err)
	}
	revised = sendWhenIdle(command{Type: "implement_approve"}, func(s domain.Session) bool {
		return s.State == "done" && s.Implementation != nil && s.Implementation.Version > oldVersion && s.Implementation.Status == "ready"
	})
	impl = revised.Implementation
	resp = request("GET", "/api/v1/sessions/"+session.ID+"/change?path=README.md", nil)
	json.NewDecoder(resp.Body).Decode(&change)
	resp.Body.Close()
	if resp.StatusCode != 200 || strings.Count(change.Content, "固定デモで追加") != strings.Count(oldContent, "固定デモで追加")+1 || impl.ChangeRevision != 1 {
		t.Fatalf("revision dropped prior content: %d %+v", resp.StatusCode, change)
	}

	time.Sleep(200 * time.Millisecond)
	for _, bad := range []command{{Type: "implement_publish", Branch: "main", Title: "x"}, {Type: "implement_publish", Branch: impl.Branch, Title: "ghp_abcdefghijklmnopqrstuvwxyz"}} {
		ws.WriteJSON(bad)
		if _, e := wait(func(domain.Session) bool { return false }, ""+map[bool]string{true: "invalid_branch", false: "invalid_pull_request_no_secrets"}[bad.Branch == "main"]); e == "" {
			t.Fatalf("unsafe publish accepted: %+v", bad)
		}
	}
	published := sendWhenIdle(command{Type: "implement_publish", Branch: impl.Branch, Title: "READMEに説明を追加", Body: "AIが作成しました"}, func(s domain.Session) bool {
		return s.State == "done" && s.Implementation != nil && s.Implementation.Status == "published"
	})
	if published.Implementation.PullNumber != 1 || !strings.Contains(published.Messages[len(published.Messages)-1].Text, "Draft PR") {
		t.Fatalf("publish: %+v", published.Implementation)
	}
	stored, err := storage.Get(ctx, uid, session.ID)
	if err != nil || stored.Implementation == nil || stored.Implementation.Status != "published" || stored.Implementation.CacheID == "" || stored.Implementation.Verification == nil {
		t.Fatalf("not persisted: %+v %v", stored, err)
	}
}
