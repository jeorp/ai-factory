package agent

import (
	"context"
	"encoding/json"
	"errors"
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
	Progress    string
	Summary     string
	Answer      string
	Applied     []string
	Sources     []domain.Source
	Limitations []string
	Metrics     domain.Metrics
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
	Session      domain.Session
	Key          string
	GitHubToken  string
	Instructions <-chan domain.Message
}

// Run owns only transient evidence. Each emitted checkpoint must be durably saved before continuing.
func (r *Runner) Run(ctx context.Context, in Input, emit func(Checkpoint) error) error {
	start := time.Now()
	cp := Checkpoint{Summary: in.Session.Summary, Metrics: in.Session.Metrics, Sources: append([]domain.Source{}, in.Session.Sources...)}
	ev := []evidence{}
	cache := map[string]string{}
	files := 0
	calls := 0
	instructions := []domain.Message{}
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
		measure()
		if err := emit(cp); err != nil {
			return err
		}
		input := BuildContext(in.Session, cp.Summary, instructions, ev, cp.Limitations)
		if len(input) > domain.MaxInputBytes {
			return errors.New("ai_input_limit")
		}
		if len(input) > cp.Metrics.MaxInputBytes {
			cp.Metrics.MaxInputBytes = len(input)
		}
		decision, usage, err := r.AI.Next(ctx, in.Key, in.Session.Model, input)
		cp.Metrics.InputTokens += usage.InputTokens
		cp.Metrics.OutputTokens += usage.OutputTokens
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
		if decision.Read == nil {
			if strings.TrimSpace(decision.Answer) == "" {
				return errors.New("empty_ai_answer")
			}
			cp.Answer = decision.Answer
			cp.Summary = domain.Clip(decision.Summary, 2048)
			cp.Progress = "回答が完了しました"
			measure()
			return emit(cp)
		}
		if calls >= domain.MaxToolCalls {
			cp.Answer = "取得回数の上限に達しました。確認範囲を絞って再度質問してください。"
			cp.Limitations = append(cp.Limitations, "1回の質問につき最大6回の取得です。")
			measure()
			return emit(cp)
		}
		read := *decision.Read
		if err = read.Validate(); err != nil {
			return err
		}
		cacheKeyBytes, _ := json.Marshal(read)
		cacheKey := string(cacheKeyBytes)
		content, ok := cache[cacheKey]
		if ok {
			cp.Metrics.CacheHits++
			cp.Limitations = append(cp.Limitations, "同じ取得要求が繰り返されたため、保存済みの一時結果を利用しました。")
		} else {
			if read.Kind == "file" {
				files++
				if files > domain.MaxFiles {
					cp.Limitations = append(cp.Limitations, "ファイル取得は最大5件です。")
					cp.Answer = "ファイル取得の上限に達しました。対象を絞って再度質問してください。"
					measure()
					return emit(cp)
				}
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
			content, source, n, err = r.GitHub.Read(ctx, in.GitHubToken, in.Session.Target, read)
			cp.Metrics.FetchedBytes += n
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				content = "取得できませんでした: " + err.Error()
				cp.Limitations = append(cp.Limitations, content)
			} else {
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
func BuildContext(s domain.Session, summary string, instructions []domain.Message, ev []evidence, limits []string) string {
	// Keep original user instructions independently of the model's lossy summary.
	var pinned strings.Builder
	for _, m := range instructions {
		pinned.WriteString(m.Text + "\n")
	}
	if pinned.Len() > 4096 {
		return strings.Repeat("x", domain.MaxInputBytes+1)
	}
	history := []domain.Message{}
	budget := 4500
	for i := len(s.Messages) - 1; i >= 0 && len(history) < 6; i-- {
		m := s.Messages[i]
		if m.Role == "instruction" {
			continue
		}
		if len(m.Text) > budget {
			m.Text = domain.Clip(m.Text, budget)
		}
		history = append([]domain.Message{m}, history...)
		budget -= len(m.Text)
		if budget <= 0 {
			break
		}
	}
	limitText := strings.Join(limits, "\n")
	body, _ := json.Marshal(map[string]any{"target": s.Target, "summary": domain.Clip(summary, 2048), "instructions": pinned.String(), "recentHistory": history, "evidence": ev, "unverified": domain.Clip(limitText, 2048)})
	// Drop oldest evidence to enforce a hard serialized-input cap; explicitly record the omission.
	for len(body) > domain.MaxInputBytes && len(ev) > 0 {
		ev = ev[1:]
		body, _ = json.Marshal(map[string]any{"target": s.Target, "summary": domain.Clip(summary, 2048), "instructions": pinned.String(), "recentHistory": history, "evidence": ev, "unverified": domain.Clip(limitText, 1024) + " 古い取得情報を入力上限のため省略しました。必要なら再取得してください。"})
	}
	return string(body)
}
