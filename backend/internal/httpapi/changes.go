package httpapi

import (
	"errors"
	"net/http"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func (s *Server) extendChanges(w http.ResponseWriter, r *http.Request, c credential) {
	if !idPattern.MatchString(r.PathValue("id")) {
		fail(w, 404, "not_found")
		return
	}
	session, err := s.Store.Get(r.Context(), c.UID, r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	i := session.Implementation
	if i == nil || len(i.Changes) == 0 {
		fail(w, 422, "change_not_found")
		return
	}
	if _, err := s.GitHub.Authorize(r.Context(), c.Token, session.Target.Repository); err != nil {
		fail(w, 403, "repository_forbidden")
		return
	}
	id, version := i.CacheID, i.Version
	if i.DraftSource != nil {
		id, version = i.DraftSource.CacheID, i.DraftSource.Version
	}
	paths := make([]string, 0, len(i.Changes))
	for _, f := range i.Changes {
		paths = append(paths, f.Path)
	}
	until := time.Now().Add(github.DraftTTL)
	if session.ExpiresAt.Before(until) {
		until = session.ExpiresAt
	}
	if err := s.GitHub.ExtendChanges(r.Context(), id, version, paths, until); err != nil {
		if errors.Is(err, domain.ErrNotFound) || err.Error() == "implementation_changes_expired" {
			fail(w, 410, "implementation_changes_expired")
		} else {
			fail(w, 503, "change_store_unavailable")
		}
		return
	}
	reply(w, 200, map[string]any{"expiresAt": until})
}

// sessionChange returns one AI-generated file and its original at the base
// commit so the browser can render a diff before the user creates the PR.
func (s *Server) sessionChange(w http.ResponseWriter, r *http.Request, c credential) {
	if !idPattern.MatchString(r.PathValue("id")) {
		fail(w, 404, "not_found")
		return
	}
	session, err := s.Store.Get(r.Context(), c.UID, r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	impl := session.Implementation
	p := r.URL.Query().Get("path")
	if impl == nil || domain.ValidateChangePath(p) != nil {
		fail(w, 422, "invalid_file_path")
		return
	}
	var summary *domain.ChangeSummary
	for i := range impl.Changes {
		if impl.Changes[i].Path == p {
			summary = &impl.Changes[i]
		}
	}
	if summary == nil {
		fail(w, 404, "change_not_found")
		return
	}
	cacheID, version := impl.CacheID, impl.Version
	if impl.DraftSource != nil {
		cacheID, version = impl.DraftSource.CacheID, impl.DraftSource.Version
	}
	change, ok := s.GitHub.GetChange(r.Context(), cacheID, version, p)
	if !ok {
		fail(w, 410, "implementation_changes_expired")
		return
	}
	original := ""
	if change.Action != "create" {
		// RawFile checks the user's current repository access first.
		content, exists, err := s.GitHub.RawFile(r.Context(), c.Token, session.Target.Repository, impl.BaseCommit, p)
		if err != nil || !exists {
			fail(w, 502, "file_read_failed")
			return
		}
		original = content
	} else if _, err := s.GitHub.Authorize(r.Context(), c.Token, session.Target.Repository); err != nil {
		fail(w, 403, "repository_forbidden")
		return
	}
	reply(w, 200, map[string]any{"path": p, "action": change.Action, "baseCommit": impl.BaseCommit, "original": original, "content": change.Content})
}
