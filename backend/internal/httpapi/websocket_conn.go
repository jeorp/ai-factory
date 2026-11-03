package httpapi

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"ai-factory/backend/internal/agent"
	"ai-factory/backend/internal/domain"
	"github.com/gorilla/websocket"
)

// socketConn is the state of one authenticated WebSocket connection. Only its
// event loop (serve) mutates the session; the agent worker reports through
// updates and done.
type socketConn struct {
	server  *Server
	ws      *websocket.Conn
	cred    credential
	session *domain.Session
	ctx     context.Context
	updates chan update

	// Set while an agent run is in progress.
	cancelRun    context.CancelFunc
	done         chan error
	instructions chan domain.Message
}

func (sc *socketConn) send(kind string, data any) error {
	sc.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return sc.ws.WriteJSON(map[string]any{"type": kind, "data": data})
}

func (sc *socketConn) problem(message string) { sc.send("error", map[string]string{"error": message}) }

func (sc *socketConn) save() error {
	ctx, cancel := context.WithTimeout(sc.ctx, 5*time.Second)
	defer cancel()
	return sc.server.Store.Save(ctx, sc.cred.UID, sc.session)
}

// release stops any running agent and returns the session lease.
func (sc *socketConn) release() {
	if sc.cancelRun != nil {
		sc.cancelRun()
		<-sc.done
		sc.session.State = "auth_wait"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sc.server.Store.Release(ctx, sc.cred.UID, sc.session)
}

// serve runs the connection's event loop until the socket closes or a handler
// asks to end it.
func (sc *socketConn) serve() {
	inbound := make(chan command, 8)
	readDone := make(chan struct{})
	sc.ws.SetReadDeadline(time.Now().Add(35 * time.Second))
	sc.ws.SetPongHandler(func(string) error { return sc.ws.SetReadDeadline(time.Now().Add(35 * time.Second)) })
	go func() {
		defer close(readDone)
		for {
			var v command
			if sc.ws.ReadJSON(&v) != nil {
				return
			}
			select {
			case inbound <- v:
			case <-sc.ctx.Done():
				return
			}
		}
	}()
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		keep := true
		select {
		case <-sc.ctx.Done():
			sc.problem("connection_expired_reconnect")
			return
		case <-readDone:
			return
		case <-ticker.C:
			keep = sc.heartbeat()
		case event := <-sc.updates:
			keep = sc.applyUpdate(event)
		case runErr := <-sc.done:
			keep = sc.finishRun(runErr)
		case cmd := <-inbound:
			keep = sc.handle(cmd)
		}
		if !keep {
			return
		}
	}
}

// The handlers below return false when the connection must close.

func (sc *socketConn) heartbeat() bool {
	if sc.save() != nil {
		sc.problem("session_lease_lost")
		return false
	}
	return sc.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) == nil
}

func (sc *socketConn) applyUpdate(event update) bool {
	cp := event.Checkpoint
	// Stop/cancel takes precedence over any late result from an external request.
	if sc.session.State == "stopping" {
		event.Ack <- context.Canceled
		return true
	}
	err := applyCheckpoint(sc.session, cp)
	if err == nil {
		err = sc.save()
	}
	event.Ack <- err
	if err != nil {
		sc.problem("checkpoint_save_failed")
		return false
	}
	return sc.send("session", sc.session) == nil && sc.send("progress", cp.Progress) == nil
}

func (sc *socketConn) finishRun(runErr error) bool {
	wasStopping := sc.session.State == "stopping"
	sc.cancelRun()
	sc.cancelRun = nil
	sc.done = nil
	sc.instructions = nil
	applyRunResult(sc.session, runErr, wasStopping)
	if sc.save() != nil {
		sc.problem("checkpoint_save_failed")
		return false
	}
	if sc.send("session", sc.session) != nil {
		return false
	}
	if runErr != nil && sc.session.State == "failed" {
		sc.problem(runErr.Error())
	}
	return true
}

func (sc *socketConn) handle(cmd command) bool {
	switch cmd.Type {
	case "end":
		if sc.cancelRun != nil {
			sc.cancelRun()
		}
		sc.send("ended", nil)
		return false
	case "stop":
		return sc.stop()
	case "set_effort":
		return sc.setEffort(cmd)
	case "set_budget":
		if sc.cancelRun != nil {
			sc.problem("run_in_progress")
			return true
		}
		if cmd.TokenBudget < 0 || cmd.TokenBudget > 100000000 {
			sc.problem("invalid_token_budget")
			return true
		}
		sc.session.TokenBudget = cmd.TokenBudget
		limits := []string{}
		for _, limit := range sc.session.Limitations {
			if limit != "ai_token_budget_exceeded" {
				limits = append(limits, limit)
			}
		}
		sc.session.Limitations = limits
		return sc.save() == nil && sc.send("session", sc.session) == nil
	case "instruction":
		return sc.addInstruction(cmd)
	case "message", "resume", "review", "investigate", "implement", "implement_approve", "implement_amend", "implement_publish", "implement_check_ci", "implement_fix_ci", "implement_verify":
		return sc.startRun(cmd)
	default:
		sc.problem("unknown_command")
		return true
	}
}

func (sc *socketConn) stop() bool {
	session := sc.session
	if sc.cancelRun == nil && session.State == "ci_wait" {
		session.State = "stopped"
		if sc.save() != nil || sc.send("session", session) != nil {
			return false
		}
	}
	if sc.cancelRun != nil {
		session.State = "stopping"
		sc.cancelRun()
		if sc.save() != nil {
			return false
		}
		if sc.send("session", session) != nil {
			return false
		}
	}
	return true
}

func (sc *socketConn) setEffort(cmd command) bool {
	session := sc.session
	if sc.cancelRun != nil {
		sc.problem("effort_change_in_progress")
		return true
	}
	levels := sc.server.effortOptions(session.Model)
	if len(levels) == 0 || !domain.ValidEffort(levels, cmd.Effort) {
		sc.problem("effort_not_allowed")
		return true
	}
	session.Effort = cmd.Effort
	if session.Effort == "" {
		session.Effort = domain.DefaultEffort(session.Model, levels)
	}
	return sc.save() == nil && sc.send("session", session) == nil
}

// addInstruction records an additional instruction and, during a run, hands
// it to the worker.
func (sc *socketConn) addInstruction(cmd command) bool {
	session := sc.session
	if !idPattern.MatchString(cmd.ID) || !safeText(cmd.Text) {
		sc.problem("invalid_instruction")
		return true
	}
	if session.HasMessage(cmd.ID) {
		sc.send("session", session)
		return true
	}
	if sc.instructions != nil && len(sc.instructions) >= cap(sc.instructions) {
		sc.problem("instruction_queue_full")
		return true
	}
	if err := session.Add("instruction", cmd.Text, cmd.ID); err != nil {
		sc.problem(err.Error())
		return true
	}
	if sc.save() != nil {
		return false
	}
	if sc.instructions != nil {
		sc.instructions <- session.Messages[len(session.Messages)-1]
	}
	return sc.send("session", session) == nil
}

// startRun validates a command that runs the agent, records it on the session
// and starts the worker.
func (sc *socketConn) startRun(cmd command) bool {
	s, session := sc.server, sc.session
	if sc.cancelRun != nil {
		sc.problem("run_in_progress")
		return true
	}
	if cmd.Type == "investigate" {
		if session.Review == nil || session.Target.Kind != "pull" || cmd.UnverifiedIndex == nil || *cmd.UnverifiedIndex < 0 || *cmd.UnverifiedIndex >= len(session.Review.Unverified) || !idPattern.MatchString(cmd.ID) {
			sc.problem("invalid_investigation")
			return true
		}
		cmd.Text = investigationPrompt(session, *cmd.UnverifiedIndex)
	}
	if problemText := implementCommandProblem(session, &cmd); problemText != "" {
		sc.problem(problemText)
		return true
	}
	if cmd.Type == "review" && session.Target.Kind != "pull" {
		sc.problem("pull_request_required")
		return true
	}
	if cmd.carriesMessage() && (!idPattern.MatchString(cmd.ID) || !safeText(cmd.Text) || (cmd.APIKey != "" && strings.Contains(cmd.Text, cmd.APIKey))) {
		sc.problem("invalid_message_no_secrets")
		return true
	}
	if cmd.carriesMessage() && session.HasMessage(cmd.ID) {
		sc.send("session", session)
		return true
	}
	// A finished chat or investigation must not hide a review that was
	// interrupted earlier; resume returns to that review's saved cursor.
	resumeReview := cmd.Type == "resume" && session.State == "done" && session.ReviewUnfinished()
	if cmd.Type == "resume" && !resumeReview && ((len(session.Messages) == 0 && session.RunKind != "review") || session.State == "done") {
		sc.problem("nothing_to_resume")
		return true
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
		sc.save()
		sc.problem("ai_key_required")
		sc.send("session", session)
		return true
	}
	if s.Config.AIProvider == "demo" {
		deadline = time.Now().Add(runDuration)
		cmd.APIKey = ""
	}
	if session.HistoryBytes() > domain.MaxHistoryBytes-24*1024 || len(session.Messages) > 196 {
		sc.problem("history_limit_create_new_session")
		return true
	}
	if cmd.carriesMessage() {
		if err := session.Add("user", cmd.Text, cmd.ID); err != nil {
			sc.problem(err.Error())
			return true
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
	if sc.save() != nil {
		return false
	}
	if sc.send("session", session) != nil {
		return false
	}
	runCtx, cancel := context.WithDeadline(sc.ctx, deadline)
	sc.cancelRun = cancel
	sc.done = make(chan error, 1)
	sc.instructions = make(chan domain.Message, 8)
	// Snapshot slices before the worker starts; only this event loop mutates durable state.
	snapshot := *session
	snapshot.Messages = append([]domain.Message{}, session.Messages...)
	snapshot.Sources = append([]domain.Source{}, session.Sources...)
	traceID := randomID()
	logger := slog.Default().With("trace_id", traceID, "session_id", session.ID, "command", cmd.Type, "model", session.Model, "effort", session.Effort)
	input := agent.Input{Session: snapshot, Key: cmd.APIKey, GitHubToken: sc.cred.Token, Instructions: sc.instructions, Logger: logger}
	cmd.APIKey = ""
	session.State = "running"
	if sc.save() != nil || sc.send("session", session) != nil || sc.send("trace", map[string]string{"traceId": traceID}) != nil {
		sc.cancelRun()
		sc.cancelRun = nil
		return false
	}
	s.startWorker(runCtx, input, logger, sc.updates, sc.done)
	return true
}
