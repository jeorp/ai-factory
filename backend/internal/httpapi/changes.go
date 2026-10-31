package httpapi

import (
	"net/http"

	"ai-factory/backend/internal/domain"
)

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
	change, ok := s.GitHub.GetChange(r.Context(), impl.CacheID, impl.Version, p)
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
