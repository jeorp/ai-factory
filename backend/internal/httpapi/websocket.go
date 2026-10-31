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
	Type            string `json:"type"`
	ID              string `json:"id"`
	Text            string `json:"text"`
	Token           string `json:"token"`
	APIKey          string `json:"apiKey"`
	KeyExpiresAt    int64  `json:"keyExpiresAt"`
	UnverifiedIndex *int   `json:"unverifiedIndex"`
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
			session.Summary = cp.Summary
			if cp.Target != nil {
				session.Target = *cp.Target
			}
			if cp.RunKind != "" {
				session.RunKind = cp.RunKind
			}
			if cp.ResetReview {
				session.Review = nil
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
			session.Sources = cp.Sources
			session.Metrics = cp.Metrics
			session.Limitations = cp.Limitations
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
			if cp.Answer != "" {
				if err = session.Add("assistant", cp.Answer, randomID()); err == nil {
					session.State = "done"
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
				}
			} else {
				err = nil
			}
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
			if wasStopping {
				session.State = "stopped"
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
			case "instruction":
				if !idPattern.MatchString(cmd.ID) || !safeText(cmd.Text) {
					problem("invalid_instruction")
					continue
				}
				if session.HasMessage(cmd.ID) {
					send("session", session)
					continue
				}
				total := len(cmd.Text)
				for _, m := range session.Messages {
					if m.Role == "instruction" {
						total += len(m.Text)
					}
				}
				if total > 4096 {
					problem("instruction_limit")
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
			case "message", "resume", "review", "investigate":
				if cancelRun != nil {
					problem("run_in_progress")
					continue
				}
				if cmd.Type == "investigate" {
					if session.Review == nil || session.Target.Kind != "pull" || cmd.UnverifiedIndex == nil || *cmd.UnverifiedIndex < 0 || *cmd.UnverifiedIndex >= len(session.Review.Unverified) || !idPattern.MatchString(cmd.ID) {
						problem("invalid_investigation")
						continue
					}
					cmd.Text = fmt.Sprintf("PR #%d のレビューの未確認項目を追加調査してください。対象: %s\n関連コードや差分を必要に応じて確認し、確認できた事実と根拠、問題の有無、引き続き未確認の点を区別してください。確認できないことを推測で確定しないでください。", session.Target.Number, session.Review.Unverified[*cmd.UnverifiedIndex])
				}
				if cmd.Type == "review" && session.Target.Kind != "pull" {
					problem("pull_request_required")
					continue
				}
				if (cmd.Type == "message" || cmd.Type == "review" || cmd.Type == "investigate") && (!idPattern.MatchString(cmd.ID) || !safeText(cmd.Text) || (cmd.APIKey != "" && strings.Contains(cmd.Text, cmd.APIKey))) {
					problem("invalid_message_no_secrets")
					continue
				}
				if (cmd.Type == "message" || cmd.Type == "review" || cmd.Type == "investigate") && session.HasMessage(cmd.ID) {
					send("session", session)
					continue
				}
				if cmd.Type == "resume" && ((len(session.Messages) == 0 && session.RunKind != "review") || session.State == "done") {
					problem("nothing_to_resume")
					continue
				}
				deadline := minTime(time.Now().Add(agent.ReviewRunDuration), time.UnixMilli(cmd.KeyExpiresAt))
				if s.Config.AIProvider != "demo" && (!validAIKey(cmd.APIKey) || !deadline.After(time.Now())) {
					session.State = "auth_wait"
					save()
					problem("ai_key_required")
					send("session", session)
					continue
				}
				if s.Config.AIProvider == "demo" {
					deadline = time.Now().Add(agent.ReviewRunDuration)
					cmd.APIKey = ""
				}
				if session.HistoryBytes() > domain.MaxHistoryBytes-24*1024 || len(session.Messages) > 196 {
					problem("history_limit_create_new_session")
					continue
				}
				if cmd.Type == "message" || cmd.Type == "review" || cmd.Type == "investigate" {
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
				} else if cmd.Type == "message" {
					if session.Review != nil {
						session.Review.PendingInvestigation = 0
					}
					session.RunKind = "chat"
				}
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
				logger := slog.Default().With("trace_id", traceID, "session_id", session.ID, "command", cmd.Type, "model", session.Model)
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
				completion := done
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
					status := "ok"
					if errors.Is(runErr, context.Canceled) {
						status = "canceled"
					} else if errors.Is(runErr, context.DeadlineExceeded) {
						status = "timeout"
					} else if runErr != nil && runErr.Error() == "review_run_budget_resume" {
						status = "yielded"
					} else if runErr != nil {
						status = "error"
					}
					logger.Info("run_finished", "outcome", status, "duration_ms", time.Since(started).Milliseconds())
					completion <- runErr
				}()
			default:
				problem("unknown_command")
			}
		}
	}
}
func validAIKey(key string) bool {
	return len(key) > 0 && len(key) <= 512 && !strings.ContainsAny(key, " \t\r\n")
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
