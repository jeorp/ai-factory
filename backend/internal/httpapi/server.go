package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"ai-factory/backend/internal/agent"
	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
	"ai-factory/backend/internal/store"
	"firebase.google.com/go/v4/auth"
)

type Server struct {
	Config Config
	Auth   *auth.Client
	Store  *store.Store
	GitHub *github.Client
	Runner *agent.Runner
	HTTP   *http.Client
	slots  chan struct{}
}

func New(c Config, a *auth.Client, db *store.Store, gh *github.Client, runner *agent.Runner, h *http.Client) *Server {
	return &Server{c, a, db, gh, runner, h, make(chan struct{}, 4)}
}
func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
func fail(w http.ResponseWriter, code int, err string) {
	reply(w, code, map[string]string{"error": err})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(&struct{}{}) != io.EOF {
		fail(w, 400, "invalid_json")
		return false
	}
	return true
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{16,80}$`)
var keyPattern = regexp.MustCompile(`(?:AIza|sk-|github_pat_|gh[opusr]_)[a-zA-Z0-9_-]{12,}`)

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	if dir := os.Getenv("WEB_DIR"); dir != "" {
		files := http.FileServer(http.Dir(dir))
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" && r.Method != "HEAD" {
				fail(w, 405, "method_not_allowed")
				return
			}
			files.ServeHTTP(w, r)
		})
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]bool{"ok": true}) })
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		reply(w, 200, map[string]any{"demo": s.Config.Demo, "aiProvider": s.Config.AIProvider, "models": s.Config.Models, "installUrl": "https://github.com/apps/" + s.Config.AppSlug + "/installations/new", "reviewPolicy": map[string]any{"maxFiles": agent.ReviewMaxFiles, "diffTokens": agent.ReviewDiffTokens, "inputTokens": agent.ReviewInputTokens, "inputBytes": agent.ReviewInputBytes, "maxCalls": agent.ReviewMaxCalls, "maxMinutes": int(agent.ReviewRunDuration.Minutes()), "runInputTokens": agent.ReviewRunInputTokens, "runOutputTokens": agent.ReviewRunOutputTokens}})
	})
	mux.HandleFunc("GET /auth/github", s.login)
	mux.HandleFunc("GET /auth/callback", s.callback)
	mux.HandleFunc("POST /auth/complete", s.complete)
	mux.HandleFunc("POST /auth/logout", func(w http.ResponseWriter, r *http.Request) {
		s.cookie(w, "factory_github", nil, -1)
		reply(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/v1/sessions/{id}/ws", s.websocket)
	mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		c, err := s.identity(ctx, r, "")
		if err != nil {
			fail(w, 401, "login_required")
			return
		}
		route := http.NewServeMux()
		route.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) { reply(w, 200, map[string]string{"login": c.Login}) })
		route.HandleFunc("GET /api/v1/repositories", func(w http.ResponseWriter, r *http.Request) {
			v, err := s.GitHub.Repositories(ctx, c.Token)
			if err != nil {
				fail(w, 502, err.Error())
				return
			}
			reply(w, 200, v)
		})
		route.HandleFunc("GET /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) {
			v, err := s.Store.List(ctx, c.UID)
			if err != nil {
				fail(w, 503, "store_unavailable")
				return
			}
			reply(w, 200, v)
		})
		route.HandleFunc("POST /api/v1/sessions", func(w http.ResponseWriter, r *http.Request) { s.create(w, r, c) })
		route.HandleFunc("GET /api/v1/sessions/{id}/file", func(w http.ResponseWriter, r *http.Request) { s.sessionFile(w, r, c) })
		route.HandleFunc("GET /api/v1/sessions/{id}/change", func(w http.ResponseWriter, r *http.Request) { s.sessionChange(w, r, c) })
		route.HandleFunc("GET /api/v1/sessions/{id}/review-diff", func(w http.ResponseWriter, r *http.Request) { s.sessionReviewDiff(w, r, c) })
		route.HandleFunc("GET /api/v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
			if !idPattern.MatchString(r.PathValue("id")) {
				fail(w, 404, "not_found")
				return
			}
			v, err := s.Store.Get(ctx, c.UID, r.PathValue("id"))
			if err != nil {
				storeError(w, err)
				return
			}
			reply(w, 200, v)
		})
		route.HandleFunc("DELETE /api/v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
			if !idPattern.MatchString(r.PathValue("id")) {
				fail(w, 404, "not_found")
				return
			}
			if err := s.Store.Delete(ctx, c.UID, r.PathValue("id")); err != nil {
				storeError(w, err)
				return
			}
			reply(w, 200, map[string]bool{"ok": true})
		})
		route.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		origin := r.Header.Get("Origin")
		if origin != "" && origin != s.Config.Origin {
			fail(w, 403, "origin_forbidden")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && origin != s.Config.Origin {
			fail(w, 403, "origin_required")
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func storeError(w http.ResponseWriter, err error) {
	if errors.Is(err, domain.ErrNotFound) {
		fail(w, 404, "not_found")
	} else if errors.Is(err, domain.ErrConflict) {
		fail(w, 409, "session_busy")
	} else {
		fail(w, 503, "store_unavailable")
	}
}
func (s *Server) create(w http.ResponseWriter, r *http.Request, c credential) {
	var body struct {
		Target string `json:"target"`
		Model  string `json:"model"`
	}
	if !decode(w, r, &body) {
		return
	}
	target, err := domain.ParseTarget(body.Target)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	allowed := false
	for _, m := range s.Config.Models {
		allowed = allowed || m == body.Model
	}
	if !allowed {
		fail(w, 422, "model_not_allowed")
		return
	}
	repo, err := s.GitHub.Authorize(r.Context(), c.Token, target.Repository)
	if err != nil {
		fail(w, 403, "repository_forbidden")
		return
	}
	target.Commit, err = s.GitHub.Commit(r.Context(), c.Token, repo)
	if err != nil {
		fail(w, 502, "commit_lookup_failed")
		return
	}
	v := domain.Session{OverviewPending: true, ID: randomID(), Title: target.Repository, Target: target, Model: body.Model, State: "auth_wait", Messages: []domain.Message{}, Sources: []domain.Source{}, Limitations: []string{}, UpdatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour)}
	if target.Kind != "repository" {
		v.Title = body.Target
	}
	if err = s.Store.Create(r.Context(), c.UID, &v); err != nil {
		fail(w, 503, "store_unavailable")
		return
	}
	reply(w, 201, v)
}
func safeText(text string) bool {
	return strings.TrimSpace(text) != "" && len(text) <= 4096 && !keyPattern.MatchString(text)
}
