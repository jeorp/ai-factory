package agent

import (
	"context"
	"encoding/json"
	"strings"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// initializeOverview uses the session's original commit. Only the compact result is saved
// in the session; the bounded README excerpt is sent to the model once.
func (r *Runner) initializeOverview(ctx context.Context, in *Input, emit func(Checkpoint) error) error {
	cp := Checkpoint{RepositoryOverview: in.Session.RepositoryOverview, OverviewPending: true, Summary: in.Session.Summary, Sources: append([]domain.Source{}, in.Session.Sources...), Limitations: append([]string{}, in.Session.Limitations...), Metrics: in.Session.Metrics, Progress: "READMEからリポジトリの概要を整理しています"}
	if err := emit(cp); err != nil {
		return err
	}
	content, source, n, err := r.GitHub.Read(ctx, in.GitHubToken, in.Session.Target, github.Read{Kind: "file", Path: "README.md"})
	cp.Metrics.ToolCalls++
	cp.Metrics.FetchedBytes += n
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		truncated := len(content) > 8000
		input, _ := json.Marshal(map[string]any{"overviewTask": "READMEからリポジトリの目的、主要なファイルやディレクトリの配置・役割、技術構成だけを短くsummaryにまとめる。記載がない情報は省く。", "readme": domain.Clip(content, 8000), "truncated": truncated})
		// JSON escaping can expand unusual README text significantly.
		if len(input) > 12000 {
			truncated = true
			input, _ = json.Marshal(map[string]any{"overviewTask": "READMEからリポジトリの目的、主要なファイルやディレクトリの配置・役割、技術構成だけを短くsummaryにまとめる。記載がない情報は省く。", "readme": domain.Clip(content, 1800), "truncated": true})
		}
		decision, usage, aiErr := r.AI.Next(ctx, in.Key, in.Session.Model, string(input))
		cp.Metrics.InputTokens += usage.InputTokens
		cp.Metrics.OutputTokens += usage.OutputTokens
		if len(input) > cp.Metrics.MaxInputBytes {
			cp.Metrics.MaxInputBytes = len(input)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		summary := strings.TrimSpace(decision.Summary)
		if aiErr == nil && summary != "" && !(in.Key != "" && strings.Contains(summary, in.Key)) && !(in.GitHubToken != "" && strings.Contains(summary, in.GitHubToken)) {
			cp.RepositoryOverview = &domain.RepositoryOverview{Text: domain.Clip(summary, 1400), Commit: in.Session.Target.Commit, Truncated: truncated || len(summary) > 1400}
			if !containsSource(cp.Sources, source.URL) {
				cp.Sources = append(cp.Sources, source)
			}
		}
	}
	if cp.RepositoryOverview == nil {
		cp.Limitations = append(cp.Limitations, "READMEの概要を準備できませんでした。必要に応じて調査します。")
	}
	cp.OverviewPending = false
	if err := emit(cp); err != nil {
		return err
	}
	in.Session.RepositoryOverview = cp.RepositoryOverview
	in.Session.OverviewPending = false
	in.Session.Sources = cp.Sources
	in.Session.Limitations = cp.Limitations
	in.Session.Metrics = cp.Metrics
	return nil
}
