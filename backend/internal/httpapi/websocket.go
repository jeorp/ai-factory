package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"ai-factory/backend/internal/agent"
	"ai-factory/backend/internal/domain"
	"github.com/gorilla/websocket"
)

type command struct {
	TokenBudget     int    `json:"tokenBudget"`
	Effort          string `json:"effort"`
	Type            string `json:"type"`
	ID              string `json:"id"`
	Text            string `json:"text"`
	Token           string `json:"token"`
	APIKey          string `json:"apiKey"`
	KeyExpiresAt    int64  `json:"keyExpiresAt"`
	UnverifiedIndex *int   `json:"unverifiedIndex"`
	Branch          string `json:"branch"`
	PullNumber      int    `json:"pullNumber"`
	Title           string `json:"title"`
	Body            string `json:"body"`
	DocumentScope   string `json:"documentScope"`
}
type update struct {
	Checkpoint agent.Checkpoint
	Ack        chan error
}

func (s *Server) websocket(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != s.Config.Origin || !idPattern.MatchString(r.PathValue("id")) {
		fail(w, 403, "invalid_socket_request")
		return
	}
	c, err := s.credential(r)
	if err != nil {
		fail(w, 401, "login_required")
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(req *http.Request) bool { return req.Header.Get("Origin") == s.Config.Origin }}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(16 * 1024)
	ws.SetReadDeadline(time.Now().Add(10 * time.Second))
	conn := &socketConn{server: s, ws: ws}
	var hello command
	if ws.ReadJSON(&hello) != nil || hello.Type != "auth" {
		conn.problem("authentication_required")
		return
	}
	c, err = s.identity(r.Context(), r, hello.Token)
	hello.Token = ""
	if err != nil {
		conn.problem("login_required")
		return
	}
	conn.cred = c
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		conn.problem("server_busy")
		return
	}
	session, err := s.Store.Acquire(r.Context(), c.UID, r.PathValue("id"), randomID())
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			conn.problem("session_busy")
		} else {
			conn.problem("session_unavailable")
		}
		return
	}
	conn.session = session
	socketCtx, cancelSocket := context.WithDeadline(r.Context(), minTime(c.Expires, time.Now().Add(55*time.Minute)))
	defer cancelSocket()
	conn.ctx = socketCtx
	conn.updates = make(chan update)
	defer conn.release()
	if conn.send("session", session) != nil {
		return
	}
	conn.serve()
}

// maxAIKeyBytes caps API keys. Vertex AI takes a Google OAuth access token,
// which Google allows to grow up to 2048 bytes (for example with service
// account impersonation), so its cap is larger.
func maxAIKeyBytes(provider string) int {
	if provider == "vertex" {
		return 2048
	}
	return 512
}

func validAIKey(key, provider string) bool {
	return len(key) > 0 && len(key) <= maxAIKeyBytes(provider) && !strings.ContainsAny(key, " \t\r\n")
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// implementCommandProblem validates implementation commands before any state changes.
func implementCommandProblem(session *domain.Session, cmd *command) string {
	impl := session.Implementation
	switch cmd.Type {
	case "implement":
		if strings.TrimSpace(cmd.Text) == "" {
			return "implementation_request_required"
		}
		if cmd.PullNumber < 0 || cmd.PullNumber > 100000000 {
			return "invalid_number"
		}
		// A half-written branch must be finished (or retried) before a new cycle.
		if impl != nil && impl.Status == "publishing" && (impl.RefUpdated || impl.RefBranch != "") {
			return "implementation_publish_incomplete"
		}
	case "implement_approve":
		if impl == nil || impl.Status != "plan_ready" || impl.Plan == nil || impl.Clarification != "" {
			return "implementation_plan_required"
		}
	case "implement_amend":
		if impl == nil || impl.Status != "amend_ready" || len(impl.PendingFiles) == 0 || impl.Plan == nil {
			return "implementation_amend_not_pending"
		}
		if cmd.Text != "approve" && cmd.Text != "decline" {
			return "invalid_implementation_amend"
		}
	case "implement_check_ci":
		if impl == nil || impl.Status != "published" || !domain.ValidCommit(impl.Commit) || impl.PullNumber < 1 {
			return "implementation_not_published"
		}
	case "implement_fix_ci":
		if impl == nil || impl.Status != "published" || impl.CI == nil || impl.CI.State != "failed" || impl.CI.Commit != impl.Commit || impl.PullNumber < 1 {
			return "implementation_ci_not_failed"
		}
	case "implement_verify":
		if impl == nil || impl.Status != "verification_blocked" {
			return "implementation_not_ready"
		}
	case "implement_publish":
		if impl == nil || (impl.Status != "ready" && impl.Status != "publishing") || len(impl.Changes) == 0 {
			return "implementation_not_ready"
		}
		if impl.Status == "ready" && !impl.CanPublish() {
			return "implementation_verification_incomplete"
		}
		cmd.Title, cmd.Body, cmd.Branch = strings.TrimSpace(cmd.Title), strings.TrimSpace(cmd.Body), strings.TrimSpace(cmd.Branch)
		if impl.ExpectedHead != "" {
			cmd.Branch = impl.Branch
		}
		if !domain.ValidBranchName(cmd.Branch) {
			return "invalid_branch"
		}
		if cmd.Title == "" || len(cmd.Title) > 256 || len(cmd.Body) > 12000 || !safeText(cmd.Title) || domain.ContainsSecret(cmd.Title+"\n"+cmd.Body) {
			return "invalid_pull_request_no_secrets"
		}
		if impl.Status == "publishing" && ((impl.RefUpdated && cmd.Branch != impl.Branch) || (impl.RefBranch != "" && cmd.Branch != impl.RefBranch)) {
			return "branch_already_written"
		}
	}
	return ""
}

// applyImplementCommand moves the durable implementation state before a run starts.
func applyImplementCommand(session *domain.Session, cmd command) {
	switch cmd.Type {
	case "implement":
		session.RunKind = "implement"
		session.Implementation = agent.StartImplementation(session.Implementation, agent.ImplementRequest{Request: cmd.Text, PullNumber: cmd.PullNumber}, agent.NewCacheID())
	case "implement_approve":
		impl := session.Implementation
		session.RunKind = "implement"
		impl.Status, impl.NextStep = "editing", 0
		if !impl.Revising {
			impl.Changes = []domain.ChangeSummary{}
			impl.Unverified = []string{}
		}
		impl.Verification = nil
	case "implement_amend":
		session.RunKind = "implement"
		session.Implementation.ResolveAmendment(cmd.Text == "approve")
	case "implement_check_ci":
		session.RunKind = "implement"
		session.Implementation.Status = "checking_ci"
	case "implement_verify":
		session.RunKind = "implement"
		session.Implementation.Status = "verifying"
	case "implement_fix_ci":
		session.RunKind = "implement"
		prior := session.Implementation
		session.Implementation = agent.StartCIFix(prior, agent.NewCacheID())
	case "implement_publish":
		impl := session.Implementation
		session.RunKind = "implement"
		if impl.Status == "ready" {
			impl.Commit, impl.RefBranch, impl.RefUpdated = "", "", false
		}
		impl.Status, impl.Branch, impl.Title, impl.Body = "publishing", cmd.Branch, cmd.Title, cmd.Body
	}
}

// applyCheckpoint copies a worker checkpoint into the durable session. A
// final answer is added to the history and settles the session state.
func applyCheckpoint(session *domain.Session, cp agent.Checkpoint) error {
	session.Summary = cp.Summary
	if cp.Target != nil {
		session.Target = *cp.Target
	}
	if cp.RunKind != "" {
		session.RunKind = cp.RunKind
	}
	if cp.ResetReview {
		session.Review = nil
		session.ReviewScope = cp.ReviewScope
	}
	if cp.ActivePullRequest > 0 {
		session.ActivePullRequest = cp.ActivePullRequest
	}
	session.RepositoryOverview = cp.RepositoryOverview
	session.OverviewPending = cp.OverviewPending
	if cp.Review != nil {
		session.Review = cp.Review
		session.Target.Commit = cp.Review.Head
	}
	if cp.Implementation != nil {
		session.Implementation = cp.Implementation
	}
	if cp.Documentation != nil {
		session.Documentation = cp.Documentation
	}
	session.Sources = cp.Sources
	session.Metrics = cp.Metrics
	session.Limitations = cp.Limitations
	session.Normalize()
	if len(session.Limitations) > 20 {
		session.Limitations = session.Limitations[len(session.Limitations)-20:]
	}
	for i := range session.Messages {
		for _, id := range cp.Applied {
			if session.Messages[i].ID == id {
				session.Messages[i].Applied = true
			}
		}
	}
	if cp.Answer == "" {
		return nil
	}
	if err := session.Add("assistant", cp.Answer, randomID()); err != nil {
		return err
	}
	session.State = "done"
	if session.RunKind == "implement" && session.Implementation != nil && session.Implementation.Status == "verification_blocked" {
		session.State = "user_wait"
	}
	if session.RunKind == "investigation" && session.Review != nil && session.Review.PendingInvestigation > 0 {
		if session.Review.Investigations == nil {
			session.Review.Investigations = map[string]string{}
		}
		result := cp.Answer
		if len(result) > 4096 {
			result = domain.Clip(result, 4096) + "\n（以降はチャットの回答を参照してください）"
		}
		session.Review.Investigations[strconv.Itoa(session.Review.PendingInvestigation-1)] = result
		session.Review.PendingInvestigation = 0
	}
	return nil
}

// applyRunResult sets the session state after a worker run returns.
func applyRunResult(session *domain.Session, runErr error, wasStopping bool) {
	if wasStopping {
		session.State = "stopped"
	} else if runErr != nil && runErr.Error() == "ai_token_budget_exceeded" {
		session.State = "user_wait"
		if !slices.Contains(session.Limitations, "ai_token_budget_exceeded") {
			session.Limitations = append(session.Limitations, "ai_token_budget_exceeded")
		}
	} else if errors.Is(runErr, agent.ErrCIWaiting) {
		session.State = "ci_wait"
	} else if runErr != nil && runErr.Error() == "review_run_budget_resume" {
		session.State = "user_wait"
	} else if runErr != nil {
		if errors.Is(runErr, context.DeadlineExceeded) && session.RunKind == "review" {
			session.State = "user_wait"
		} else if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			session.State = "auth_wait"
		} else {
			session.State = "failed"
			session.Limitations = append(session.Limitations, runErr.Error())
		}
	}
}

// runOutcome classifies a worker run's result for logs.
func runOutcome(runErr error) string {
	switch {
	case errors.Is(runErr, context.Canceled):
		return "canceled"
	case errors.Is(runErr, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(runErr, agent.ErrCIWaiting) || (runErr != nil && runErr.Error() == "review_run_budget_resume"):
		return "yielded"
	case runErr != nil:
		return "error"
	}
	return "ok"
}

// carriesMessage reports whether the command adds a user message to the history.
func (c command) carriesMessage() bool {
	return c.Type == "message" || c.Type == "review" || c.Type == "investigate" || c.Type == "implement" || c.Type == "documentation"
}

// startWorker runs the agent in the background. Checkpoints are handed to the
// socket's event loop, which alone mutates the durable session.
func (s *Server) startWorker(runCtx context.Context, input agent.Input, logger *slog.Logger, updates chan<- update, completion chan<- error) {
	go func() {
		defer func() { input.Key = ""; input.GitHubToken = "" }()
		started := time.Now()
		logger.Info("run_started")
		runErr := s.Runner.Run(runCtx, input, func(cp agent.Checkpoint) error {
			ack := make(chan error, 1)
			select {
			case updates <- update{cp, ack}:
			case <-runCtx.Done():
				return runCtx.Err()
			}
			select {
			case e := <-ack:
				return e
			case <-runCtx.Done():
				return runCtx.Err()
			}
		})
		logger.Info("run_finished", "outcome", runOutcome(runErr), "duration_ms", time.Since(started).Milliseconds())
		completion <- runErr
	}()
}

// investigationPrompt asks for a follow-up on one unverified review item.
func investigationPrompt(session *domain.Session, index int) string {
	return fmt.Sprintf("PR #%d のレビューの未確認項目を追加調査してください。対象: %s\n関連コードや差分を必要に応じて確認し、確認できた事実と根拠、問題の有無、引き続き未確認の点を区別してください。確認できないことを推測で確定しないでください。", session.Target.Number, session.Review.Unverified[index])
}
