package httpapi

import (
	"net/http"
	"strconv"

	"ai-factory/backend/internal/domain"
)

func reviewFileAt(review *domain.Review, index int) *domain.ReviewFile {
	if review == nil || index < 0 {
		return nil
	}
	for i := range review.Steps {
		for j := range review.Steps[i].Files {
			f := &review.Steps[i].Files[j]
			if f.Index == index && f.Path != "" {
				return f
			}
		}
	}
	return nil
}

func (s *Server) sessionReviewDiff(w http.ResponseWriter, r *http.Request, c credential) {
	if !idPattern.MatchString(r.PathValue("id")) {
		fail(w, 404, "not_found")
		return
	}
	session, err := s.Store.Get(r.Context(), c.UID, r.PathValue("id"))
	if err != nil {
		storeError(w, err)
		return
	}
	index, err := strconv.Atoi(r.URL.Query().Get("index"))
	if err != nil {
		fail(w, 422, "invalid_review_file")
		return
	}
	f := reviewFileAt(session.Review, index)
	if f == nil {
		fail(w, 404, "review_file_not_found")
		return
	}
	review := session.Review
	file, hit := s.GitHub.GetReviewFile(r.Context(), c.Token, session.Target, review.Base, review.Head, index)
	if !hit {
		check := func() bool {
			pr, _, e := s.GitHub.PullRequest(r.Context(), c.Token, session.Target)
			return e == nil && pr.Base.SHA == review.Base && pr.Head.SHA == review.Head && pr.ChangedFiles == review.TotalFiles
		}
		if !check() {
			fail(w, 409, "review_diff_snapshot_unavailable")
			return
		}
		files, _, e := s.GitHub.ReviewFiles(r.Context(), c.Token, session.Target, index+1, 1)
		if e != nil {
			fail(w, 502, "review_diff_read_failed")
			return
		}
		if len(files) != 1 || files[0].Filename != f.Path || files[0].Status != f.Status || files[0].PreviousFilename != f.PreviousPath || !check() {
			fail(w, 409, "review_diff_snapshot_unavailable")
			return
		}
		file = files[0]
		s.GitHub.PutReviewFile(r.Context(), session.Target, review.Base, review.Head, index, file)
	}
	if file.Filename != f.Path || file.Status != f.Status || file.PreviousFilename != f.PreviousPath {
		fail(w, 409, "review_diff_snapshot_unavailable")
		return
	}
	reply(w, 200, map[string]any{"path": file.Filename, "previousPath": file.PreviousFilename, "status": file.Status, "patch": file.Patch, "base": review.Base, "head": review.Head})
}
