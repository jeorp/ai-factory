package agent

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type Reader interface {
	Read(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error)
}
type Checkpoint struct {
	Target             *domain.Target
	RunKind            string
	ResetReview        bool
	ReviewScope        string
	ActivePullRequest  int
	RepositoryOverview *domain.RepositoryOverview
	OverviewPending    bool
	Review             *domain.Review
	Progress           string
	Summary            string
	Answer             string
	Applied            []string
	Sources            []domain.Source
	Limitations        []string
	Metrics            domain.Metrics
}
type Runner struct {
	AI     Provider
	GitHub Reader
}
type evidence struct {
	Read    github.Read `json:"read"`
	Content string      `json:"content"`
}
type Input struct {
	Session         domain.Session
	Key             string
	GitHubToken     string
	Instructions    <-chan domain.Message
	Logger          *slog.Logger
	ContinueMetrics bool
}

// Run owns only transient evidence. Each emitted checkpoint must be durably saved before continuing.
func (r *Runner) Run(ctx context.Context, in Input, emit func(Checkpoint) error) error {
	if in.Session.OverviewPending {
		if err := r.initializeOverview(ctx, &in, emit); err != nil {
			return err
		}
	}
	originalEmit := emit
	wrappedEmit := func(cp Checkpoint) error {
		cp.RepositoryOverview = in.Session.RepositoryOverview
		cp.OverviewPending = in.Session.OverviewPending
		return originalEmit(cp)
	}
	emit = wrappedEmit

	if in.Session.RunKind == "review" {
		return r.runReview(ctx, in, emit)
	}
	reviewCtx := ctx
	ctx, cancelChat := context.WithTimeout(ctx, 3*time.Minute)
	defer cancelChat()
	start := time.Now()
	logger := in.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cp := Checkpoint{Summary: in.Session.Summary, Metrics: in.Session.Metrics, Sources: append([]domain.Source{}, in.Session.Sources...)}
	cp.Metrics.LastInputTokens = 0
	cp.Metrics.LastOutputTokens = 0
	cp.Metrics.HasLastUsage = false
	ev := []evidence{}
	cache := map[string]string{}
	successfulReads := map[string]bool{}
	files := 0
	calls := 0
	instructions := []domain.Message{}
	var intent IntentPlan
	for _, m := range in.Session.Messages {
		if m.Role == "instruction" {
			instructions = append(instructions, m)
		}
	}
	measure := func() {
		cp.Metrics.DurationMS = time.Since(start).Milliseconds()
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		if int64(mem.HeapAlloc) > cp.Metrics.HeapPeakBytes {
			cp.Metrics.HeapPeakBytes = int64(mem.HeapAlloc)
		}
	}
	for step := 0; step <= domain.MaxToolCalls; step++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		for {
			select {
			case instruction := <-in.Instructions:
				instructions = append(instructions, instruction)
			default:
				goto drained
			}
		}
	drained:
		cp.Applied = nil
		for _, m := range instructions {
			cp.Applied = append(cp.Applied, m.ID)
		}
		cp.Progress = "AIが取得情報を確認しています"
		if step == 0 {
			cp.Progress = "質問の意図と調査方針を整理しています"
		}
		budget := newChatBudget(step, calls, files)
		if budget.FinalAnswerOnly {
			cp.Progress = "取得済みの情報から回答をまとめています"
			logger.Info("chat_final_answer_required", "step", step+1, "reads_used", calls, "files_used", files, "remaining_model_calls", budget.RemainingModelCalls)
		}
		measure()
		if err := emit(cp); err != nil {
			return err
		}
		input := buildContext(in.Session, cp.Summary, instructions, ev, cp.Limitations, intent, &budget)
		if len(input) > domain.MaxInputBytes {
			return errors.New("ai_input_limit")
		}
		if len(input) > cp.Metrics.MaxInputBytes {
			cp.Metrics.MaxInputBytes = len(input)
		}
		callStart := time.Now()
		logger.Info("model_call_started", "step", step+1, "input_bytes", len(input))
		decision, usage, err := r.AI.Next(ctx, in.Key, in.Session.Model, input)
		logger.Info("model_call_finished", "step", step+1, "duration_ms", time.Since(callStart).Milliseconds(), "outcome", outcome(err), "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens)
		if err != nil {
			switch err.Error() {
			case "gemini_answer_too_long", "gemini_invalid_intent", "gemini_invalid_review", "gemini_invalid_review_action":
				logger.Info("model_response_validation_failed", "step", step+1, "reason", err.Error())
			}
		}
		cp.Metrics.InputTokens += usage.InputTokens
		cp.Metrics.OutputTokens += usage.OutputTokens
		cp.Metrics.LastInputTokens += usage.InputTokens
		cp.Metrics.LastOutputTokens += usage.OutputTokens
		if usage.InputTokens != 0 || usage.OutputTokens != 0 {
			cp.Metrics.HasLastUsage = true
			measure()
			if emitErr := emit(cp); emitErr != nil {
				return emitErr
			}
		}
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// A directive accepted during the last model call is applied before a final answer.
		if decision.Read == nil && step < domain.MaxToolCalls {
			select {
			case instruction := <-in.Instructions:
				instructions = append(instructions, instruction)
				continue
			default:
			}
		}
		// Never checkpoint echoed credentials, including in summaries or model-generated paths.
		raw, _ := json.Marshal(decision)
		if (len(in.Key) > 0 && strings.Contains(string(raw), in.Key)) || (len(in.GitHubToken) > 0 && strings.Contains(string(raw), in.GitHubToken)) {
			return errors.New("credential_echo_blocked")
		}
		if validIntent(decision.Intent) {
			intent = decision.Intent
		}
		if decision.StartReview != nil {
			if in.Session.RunKind == "investigation" {
				return errors.New("investigation_review_restart_blocked")
			}
			if !validReviewAction(decision) {
				return errors.New("invalid_review_action")
			}
			// Carry directives consumed during chat routing into the review worker.
			for _, m := range instructions {
				if !in.Session.HasMessage(m.ID) {
					in.Session.Messages = append(in.Session.Messages, m)
				}
			}
			return r.reviewFromChat(reviewCtx, in, cp, *decision.StartReview, emit)
		}
		if decision.Read == nil {
			if strings.TrimSpace(decision.Answer) == "" {
				return errors.New("empty_ai_answer")
			}
			cp.Answer = decision.Answer
			cp.Summary = domain.Clip(decision.Summary, 2048)
			cp.Progress = "回答が完了しました"
			logger.Info("chat_answer_completed", "step", step+1, "reads_used", calls, "files_used", files)
			measure()
			return emit(cp)
		}
		read := *decision.Read
		readLogger := chatReadLogger(logger, step, read, budget)
		if budget.FinalAnswerOnly {
			reason := "model_call_limit"
			if budget.RemainingReads == 0 {
				reason = "read_limit"
			}
			readLogger.Info("github_read_rejected", "reason", reason)
			cp.Answer = chatBudgetFallback(cp)
			cp.Limitations = append(cp.Limitations, "追加取得は実行していません: "+read.Kind+" "+read.Path)
			cp.Progress = "取得済みの範囲で調査を終了しました"
			logger.Info("chat_answer_fallback", "step", step+1, "reason", reason, "reads_used", calls, "files_used", files)
			measure()
			return emit(cp)
		}
		if err = read.Validate(); err != nil {
			readLogger.Info("github_read_rejected", "reason", err.Error())
			return err
		}
		cacheKeyBytes, _ := json.Marshal(read)
		cacheKey := string(cacheKeyBytes)
		content, ok := cache[cacheKey]
		if ok {
			readLogger.Info("github_read_cache_hit")
			cp.Metrics.CacheHits++
			cp.Limitations = append(cp.Limitations, "同じ取得要求が繰り返されたため、保存済みの一時結果を利用しました。")
		} else {
			if read.Kind == "file" {
				if budget.RemainingFiles == 0 {
					readLogger.Info("github_read_rejected", "reason", "file_limit")
					cp.Limitations = append(cp.Limitations, "ファイル取得は最大5件です。追加要求は未取得です: "+read.Path)
					measure()
					if err = emit(cp); err != nil {
						return err
					}
					continue
				}
				files++
			}
			calls++
			cp.Metrics.ToolCalls++
			cp.Progress = "GitHub MCP: " + read.Kind + " " + read.Path
			measure()
			if err = emit(cp); err != nil {
				return err
			}
			var source domain.Source
			var n int
			readStart := time.Now()
			readLogger.Info("github_read_started")
			content, source, n, err = r.GitHub.Read(ctx, in.GitHubToken, in.Session.Target, read)
			readLogger.Info("github_read_finished", "duration_ms", time.Since(readStart).Milliseconds(), "outcome", outcome(err), "bytes", n)
			cp.Metrics.FetchedBytes += n
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				content = "取得できませんでした: " + err.Error()
				cp.Limitations = append(cp.Limitations, content)
			} else {
				successfulReads[cacheKey] = true
				if !containsSource(cp.Sources, source.URL) {
					cp.Sources = append(cp.Sources, source)
					if len(cp.Sources) > 40 {
						cp.Sources = cp.Sources[len(cp.Sources)-40:]
					}
				}
				if len(content) > 8000 {
					content = domain.Clip(content, 8000) + "\n[入力サイズ制限により後半を省略。省略部分は未確認]"
					cp.Limitations = append(cp.Limitations, "取得内容の後半を省略しました: "+source.Label)
				}
			}
			cache[cacheKey] = content
		}
		if read.Kind == "pull_request" && successfulReads[cacheKey] {
			in.Session.ActivePullRequest = read.Number
			cp.ActivePullRequest = read.Number
		}
		// Only two recent bounded observations. Full results are never put into saved history.
		ev = append(ev, evidence{Read: read, Content: content})
		if len(ev) > 2 {
			ev = ev[len(ev)-2:]
		}
		cp.Summary = domain.Clip(decision.Summary, 2048)
		measure()
		if err = emit(cp); err != nil {
			return err
		}
	}
	return errors.New("agent_step_limit")
}
func containsSource(s []domain.Source, url string) bool {
	for _, v := range s {
		if v.URL == url {
			return true
		}
	}
	return false
}
func BuildContext(s domain.Session, summary string, instructions []domain.Message, ev []evidence, limits []string, plans ...IntentPlan) string {
	var intent IntentPlan
	if len(plans) > 0 && validIntent(plans[0]) {
		intent = plans[0]
	}
	return buildContext(s, summary, instructions, ev, limits, intent, nil)
}

func buildContext(s domain.Session, summary string, instructions []domain.Message, ev []evidence, limits []string, intent IntentPlan, budget *chatBudget) string {
	// Keep original user instructions independently of the model's lossy summary.
	var pinned strings.Builder
	for _, m := range instructions {
		pinned.WriteString(m.Text + "\n")
	}
	if pinned.Len() > 4096 {
		return strings.Repeat("x", domain.MaxInputBytes+1)
	}
	history := []domain.Message{}
	historyBudget := 4500
	for i := len(s.Messages) - 1; i >= 0 && len(history) < 6; i-- {
		m := s.Messages[i]
		if m.Role == "instruction" {
			continue
		}
		if len(m.Text) > historyBudget {
			m.Text = domain.Clip(m.Text, historyBudget)
		}
		history = append([]domain.Message{m}, history...)
		historyBudget -= len(m.Text)
		if historyBudget <= 0 {
			break
		}
	}
	limitText := strings.Join(limits, "\n")
	payload := map[string]any{"intent": intent, "target": s.Target, "activePullRequest": activePullRequest(s), "repositoryOverview": s.RepositoryOverview, "summary": domain.Clip(summary, 2048), "instructions": pinned.String(), "recentHistory": history, "evidence": ev, "unverified": domain.Clip(limitText, 2048)}
	if budget != nil {
		payload["chatBudget"] = budget
	}
	body, _ := json.Marshal(payload)
	// Drop oldest evidence to enforce a hard serialized-input cap; explicitly record the omission.
	for len(body) > domain.MaxInputBytes && len(ev) > 0 {
		ev = ev[1:]
		payload["evidence"] = ev
		payload["unverified"] = domain.Clip(limitText, 1024) + " 古い取得情報を入力上限のため省略しました。必要なら残りの取得枠内で再取得してください。"
		body, _ = json.Marshal(payload)
	}
	return string(body)
}

// outcome avoids writing provider errors, which can contain user data or credentials.
func outcome(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "error"
}
