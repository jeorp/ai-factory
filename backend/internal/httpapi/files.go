package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// A browser may select a file, but the repository and commit scope stay server-owned.
func fileTarget(session *domain.Session, commit string) (domain.Target, error) {
	target := session.Target
	if commit == "" {
		commit = target.Commit
	}
	allowed := commit == target.Commit
	if review := session.Review; review != nil {
		allowed = allowed || commit == review.Base || commit == review.Head
	}
	for _, source := range session.Sources {
		u, err := url.Parse(source.URL)
		if err == nil && u.Scheme == "https" && u.Host == "github.com" && u.User == nil && strings.HasPrefix(u.Path, "/"+target.Repository+"/blob/"+commit+"/") {
			allowed = true
		}
	}
	if !allowed || !domain.ValidCommit(commit) {
		return target, errors.New("file_commit_not_allowed")
	}
	target.Commit = commit
	return target, nil
}

func (s *Server) sessionFile(w http.ResponseWriter, r *http.Request, c credential) {
	if !idPattern.MatchString(r.PathValue("id")) {
		fail(w, 404, "not_found")
		return
	}
	session, err := s.Store.Get(r.Context(), c.UID, r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	read := github.Read{Kind: "file", Path: r.URL.Query().Get("path")}
	if err = read.Validate(); err != nil || strings.TrimSpace(read.Path) == "" || strings.HasSuffix(read.Path, "/") {
		fail(w, 422, "invalid_file_path")
		return
	}
	target, err := fileTarget(session, r.URL.Query().Get("commit"))
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	// Read checks GitHub authorization before consulting the shared, commit-pinned cache.
	content, source, fetched, err := s.GitHub.Read(r.Context(), c.Token, target, read)
	if err != nil {
		fail(w, 502, "file_read_failed")
		return
	}
	if len(content) > domain.MaxFetchBytes || !utf8.ValidString(content) || strings.ContainsRune(content, '\x00') {
		fail(w, 422, "file_content_excluded")
		return
	}
	reply(w, 200, map[string]any{"repository": target.Repository, "commit": target.Commit, "path": read.Path, "content": content, "url": source.URL, "cached": fetched == 0})
}
