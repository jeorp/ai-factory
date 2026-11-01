package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"ai-factory/backend/internal/github"
)

type implementationSearcher interface {
	SearchFiles(context.Context, string, string, string, github.CodeSearch) (github.CodeSearchResult, error)
}

func (run *implementRun) search(ctx context.Context, query github.CodeSearch, ev *[]evidence) error {
	if err := query.Validate(); err != nil {
		return err
	}
	if err := run.save(ctx, "関連する定義・利用箇所を検索しています"); err != nil {
		return err
	}
	searcher, ok := run.r.Writer.(implementationSearcher)
	if !ok {
		return errors.New("implementation_search_unavailable")
	}
	run.cp.Metrics.ToolCalls++
	started := time.Now()
	result, err := searcher.SearchFiles(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, run.impl.BaseCommit, query)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	run.cp.Metrics.FetchedBytes += result.ScannedBytes
	run.logger.Info("implement_search", "phase", "plan", "duration_ms", time.Since(started).Milliseconds(), "scanned_files", result.ScannedFiles, "scanned_bytes", result.ScannedBytes, "matches", len(result.Matches), "skipped_files", result.SkippedFiles, "has_more", result.NextCursor != "", "list_truncated", result.ListTruncated, "outcome", outcome(err), "error_code", implementationErrorCode(err))
	content := ""
	if err != nil {
		content = "検索できませんでした: " + implementationErrorCode(err)
	} else {
		raw, _ := json.Marshal(result)
		content = string(raw)
		for _, match := range result.Matches {
			run.impl.ContextFiles = addContextFile(run.impl.ContextFiles, match.Path)
		}
	}
	*ev = append(*ev, evidence{Read: github.Read{Kind: "search", Path: query.Prefix}, Content: content})
	if len(*ev) > planEvidenceKept {
		*ev = (*ev)[len(*ev)-planEvidenceKept:]
	}
	return nil
}
