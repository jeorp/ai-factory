package agent

import (
	"context"
	"encoding/json"
	"strings"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type overviewCache interface {
	GetOverview(context.Context, string, domain.Target) (*domain.RepositoryOverview, bool)
	PutOverview(context.Context, domain.Target, domain.RepositoryOverview)
}

// initializeOverview uses the session's original commit. Only the compact result is saved
// in the session; the bounded README excerpt is sent to the model once.
func (r *Runner) initializeOverview(ctx context.Context, in *Input, emit func(Checkpoint) error) error {
	cp := Checkpoint{RepositoryOverview: in.Session.RepositoryOverview, OverviewPending: true, Summary: in.Session.Summary, Review: in.Session.Review, Sources: append([]domain.Source{}, in.Session.Sources...), Limitations: append([]string{}, in.Session.Limitations...), Metrics: in.Session.Metrics, Progress: "READMEからリポジトリの概要を整理しています"}
	if err := emit(cp); err != nil {
		return err
	}
	if cache, ok := r.GitHub.(overviewCache); ok {
		if overview, hit := cache.GetOverview(ctx, in.GitHubToken, in.Session.Target); hit && !echoes(overview.Text, *in) {
			_, _, source, err := github.Tool(in.Session.Target, github.Read{Kind: "file", Path: "README.md"})
			if err == nil {
				cp.RepositoryOverview = overview
				cp.Metrics.CacheHits++
				if !containsSource(cp.Sources, source.URL) {
					cp.Sources = append(cp.Sources, source)
				}
				return finishOverview(in, cp, emit)
			}
		}
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
		decision, usage, aiErr := r.next(ctx, *in, string(input), "overview")
		addUsage(&cp.Metrics, usage)
		if len(input) > cp.Metrics.MaxInputBytes {
			cp.Metrics.MaxInputBytes = len(input)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if aiErr != nil && aiErr.Error() == "ai_token_budget_exceeded" {
			// Preserve the pending overview and fetched metrics so a higher
			// session budget can retry initialization on the next run.
			if err := emit(cp); err != nil {
				return err
			}
			return aiErr
		}
		summary := strings.TrimSpace(decision.Summary)
		if aiErr == nil && summary != "" && !echoes(summary, *in) {
			cp.RepositoryOverview = &domain.RepositoryOverview{Text: domain.Clip(summary, 1400), Commit: in.Session.Target.Commit, Truncated: truncated || len(summary) > 1400}
			if cache, ok := r.GitHub.(overviewCache); ok {
				cache.PutOverview(ctx, in.Session.Target, *cp.RepositoryOverview)
			}
			if !containsSource(cp.Sources, source.URL) {
				cp.Sources = append(cp.Sources, source)
			}
		}
	}
	if cp.RepositoryOverview == nil {
		cp.Limitations = append(cp.Limitations, "READMEの概要を準備できませんでした。必要に応じて調査します。")
	}
	return finishOverview(in, cp, emit)
}

func finishOverview(in *Input, cp Checkpoint, emit func(Checkpoint) error) error {
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
