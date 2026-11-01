package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-factory/backend/internal/agent"
	"ai-factory/backend/internal/domain"
	"github.com/gorilla/websocket"
)

type command struct {
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
	send := func(kind string, data any) error {
		ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return ws.WriteJSON(map[string]any{"type": kind, "data": data})
	}
	problem := func(message string) { send("error", map[string]string{"error": message}) }
	var hello command
	if ws.ReadJSON(&hello) != nil || hello.Type != "auth" {
		problem("authentication_required")
		return
	}
	c, err = s.identity(r.Context(), r, hello.Token)
	hello.Token = ""
	if err != nil {
		problem("login_required")
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		problem("server_busy")
		return
	}
	session, err := s.Store.Acquire(r.Context(), c.UID, r.PathValue("id"), randomID())
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			problem("session_busy")
		} else {
			problem("session_unavailable")
		}
		return
	}
	socketCtx, cancelSocket := context.WithDeadline(r.Context(), minTime(c.Expires, time.Now().Add(55*time.Minute)))
	defer cancelSocket()
	var cancelRun context.CancelFunc
	var done chan error
	var instructions chan domain.Message
	defer func() {
		if cancelRun != nil {
			cancelRun()
			<-done
			session.State = "auth_wait"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		s.Store.Release(ctx, c.UID, session)
	}()
	if send("session", session) != nil {
		return
	}
	inbound := make(chan command, 8)
	readDone := make(chan struct{})
	ws.SetReadDeadline(time.Now().Add(35 * time.Second))
	ws.SetPongHandler(func(string) error { return ws.SetReadDeadline(time.Now().Add(35 * time.Second)) })
	go func() {
		defer close(readDone)
		for {
			var v command
			if ws.ReadJSON(&v) != nil {
				return
			}
			select {
			case inbound <- v:
			case <-socketCtx.Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	updates := make(chan update)
	save := func() error {
		ctx, cancel := context.WithTimeout(socketCtx, 5*time.Second)
		defer cancel()
		return s.Store.Save(ctx, c.UID, session)
	}
	for {
		select {
		case <-socketCtx.Done():
			problem("connection_expired_reconnect")
			return
		case <-readDone:
			return
		case <-ticker.C:
			if err = save(); err != nil {
				problem("session_lease_lost")
				return
			}
			if err = ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		case event := <-updates:
			cp := event.Checkpoint
			// Stop/cancel takes precedence over any late result from an external request.
			if session.State == "stopping" {
				event.Ack <- context.Canceled
				continue
			}
			err = applyCheckpoint(session, cp)
			if err == nil {
				err = save()
			}
			event.Ack <- err
			if err != nil {
				problem("checkpoint_save_failed")
				return
			}
			if send("session", session) != nil || send("progress", cp.Progress) != nil {
				return
			}
		case runErr := <-done:
			wasStopping := session.State == "stopping"
			cancelRun()
			cancelRun = nil
			done = nil
			instructions = nil
			applyRunResult(session, runErr, wasStopping)
			if err = save(); err != nil {
				problem("checkpoint_save_failed")
				return
			}
			if send("session", session) != nil {
				return
			}
			if runErr != nil && session.State == "failed" {
				problem(runErr.Error())
			}
		case cmd := <-inbound:
			switch cmd.Type {
			case "end":
				if cancelRun != nil {
					cancelRun()
				}
				send("ended", nil)
				return
			case "stop":
				if cancelRun == nil && session.State == "ci_wait" {
					session.State = "stopped"
					if save() != nil || send("session", session) != nil {
						return
					}
				}
				if cancelRun != nil {
					session.State = "stopping"
					cancelRun()
					if save() != nil {
						return
					}
					if send("session", session) != nil {
						return
					}
				}
			case "set_effort":
				if cancelRun != nil {
					problem("effort_change_in_progress")
					continue
				}
				levels := s.effortOptions(session.Model)
				if len(levels) == 0 || !domain.ValidEffort(levels, cmd.Effort) {
					problem("effort_not_allowed")
					continue
				}
				session.Effort = cmd.Effort
				if session.Effort == "" {
					session.Effort = domain.DefaultEffort(session.Model, levels)
				}
				if save() != nil || send("session", session) != nil {
					return
				}
			case "instruction":
				if !idPattern.MatchString(cmd.ID) || !safeText(cmd.Text) {
					problem("invalid_instruction")
					continue
				}
				if session.HasMessage(cmd.ID) {
					send("session", session)
					continue
				}
				if instructions != nil && len(instructions) >= cap(instructions) {
					problem("instruction_queue_full")
					continue
				}
				if err = session.Add("instruction", cmd.Text, cmd.ID); err != nil {
					problem(err.Error())
					continue
				}
				if save() != nil {
					return
				}
				if instructions != nil {
					instructions <- session.Messages[len(session.Messages)-1]
				}
				if send("session", session) != nil {
					return
				}
			case "message", "resume", "review", "investigate", "implement", "implement_approve", "implement_amend", "implement_publish", "implement_check_ci", "implement_fix_ci", "implement_verify":
				if cancelRun != nil {
					problem("run_in_progress")
					continue
				}
				if cmd.Type == "investigate" {
					if session.Review == nil || session.Target.Kind != "pull" || cmd.UnverifiedIndex == nil || *cmd.UnverifiedIndex < 0 || *cmd.UnverifiedIndex >= len(session.Review.Unverified) || !idPattern.MatchString(cmd.ID) {
						problem("invalid_investigation")
						continue
					}
					cmd.Text = investigationPrompt(session, *cmd.UnverifiedIndex)
				}
				if problemText := implementCommandProblem(session, &cmd); problemText != "" {
					problem(problemText)
					continue
				}
				if cmd.Type == "review" && session.Target.Kind != "pull" {
					problem("pull_request_required")
					continue
				}
				if cmd.carriesMessage() && (!idPattern.MatchString(cmd.ID) || !safeText(cmd.Text) || (cmd.APIKey != "" && strings.Contains(cmd.Text, cmd.APIKey))) {
					problem("invalid_message_no_secrets")
					continue
				}
				if cmd.carriesMessage() && session.HasMessage(cmd.ID) {
					send("session", session)
					continue
				}
				// A finished chat or investigation must not hide a review that was
				// interrupted earlier; resume returns to that review's saved cursor.
				resumeReview := cmd.Type == "resume" && session.State == "done" && session.ReviewUnfinished()
				if cmd.Type == "resume" && !resumeReview && ((len(session.Messages) == 0 && session.RunKind != "review") || session.State == "done") {
					problem("nothing_to_resume")
					continue
				}
				runDuration := agent.ReviewRunDuration
				if strings.HasPrefix(cmd.Type, "implement") || (cmd.Type == "resume" && session.RunKind == "implement") {
					runDuration = agent.ImplementRunTime
				}
				deadline := minTime(time.Now().Add(runDuration), time.UnixMilli(cmd.KeyExpiresAt))
				// Publishing writes the saved change set and calls no AI model.
				publishOnly := cmd.Type == "implement_publish" || cmd.Type == "implement_check_ci" || (cmd.Type == "resume" && session.RunKind == "implement" && session.Implementation != nil && (session.Implementation.Status == "publishing" || session.Implementation.Status == "checking_ci"))
				if publishOnly {
					deadline = time.Now().Add(6 * time.Minute)
					cmd.APIKey = ""
				}
				if s.Config.AIProvider != "demo" && !publishOnly && (!validAIKey(cmd.APIKey, s.Config.ModelProviders[session.Model]) || !deadline.After(time.Now())) {
					session.State = "auth_wait"
					save()
					problem("ai_key_required")
					send("session", session)
					continue
				}
				if s.Config.AIProvider == "demo" {
					deadline = time.Now().Add(runDuration)
					cmd.APIKey = ""
				}
				if session.HistoryBytes() > domain.MaxHistoryBytes-24*1024 || len(session.Messages) > 196 {
					problem("history_limit_create_new_session")
					continue
				}
				if cmd.carriesMessage() {
					if err = session.Add("user", cmd.Text, cmd.ID); err != nil {
						problem(err.Error())
						continue
					}
				}
				if cmd.Type == "investigate" {
					session.RunKind = "investigation"
					session.Review.PendingInvestigation = *cmd.UnverifiedIndex + 1
				} else if cmd.Type == "review" {
					session.RunKind = "review"
					session.Review = nil
					session.ReviewScope = ""
				} else if cmd.Type == "message" {
					if session.Review != nil {
						session.Review.PendingInvestigation = 0
					}
					session.RunKind = "chat"
				}
				if resumeReview {
					session.RunKind = "review"
				}
				applyImplementCommand(session, cmd)
				session.State = "accepted"
				if save() != nil {
					return
				}
				if send("session", session) != nil {
					return
				}
				runCtx, cancel := context.WithDeadline(socketCtx, deadline)
				cancelRun = cancel
				done = make(chan error, 1)
				instructions = make(chan domain.Message, 8)
				// Snapshot slices before the worker starts; only this event loop mutates durable state.
				snapshot := *session
				snapshot.Messages = append([]domain.Message{}, session.Messages...)
				snapshot.Sources = append([]domain.Source{}, session.Sources...)
				traceID := randomID()
				logger := slog.Default().With("trace_id", traceID, "session_id", session.ID, "command", cmd.Type, "model", session.Model, "effort", session.Effort)
				input := agent.Input{Session: snapshot, Key: cmd.APIKey, GitHubToken: c.Token, Instructions: instructions, Logger: logger}
				cmd.APIKey = ""
				session.State = "running"
				if save() != nil {
					cancelRun()
					cancelRun = nil
					return
				}
				if send("session", session) != nil {
					cancelRun()
					cancelRun = nil
					return
				}
				if send("trace", map[string]string{"traceId": traceID}) != nil {
					cancelRun()
					cancelRun = nil
					return
				}
				s.startWorker(runCtx, input, logger, updates, done)
			default:
				problem("unknown_command")
			}
		}
	}
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
	return c.Type == "message" || c.Type == "review" || c.Type == "investigate" || c.Type == "implement"
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
