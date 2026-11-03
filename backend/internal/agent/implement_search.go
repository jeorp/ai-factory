package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type implementationSearcher interface {
	SearchFiles(context.Context, string, string, string, github.CodeSearch) (github.CodeSearchResult, error)
}

// A newly fetched code snippet can resolve a question, but never establishes
// complete-file coverage. Deduplicate the code itself, excluding page metadata.
func recordSearchEvidence(ev evidence, seen map[[32]byte]bool) []string {
	if len(ev.codePaths) == 0 {
		return nil
	}
	var result github.CodeSearchResult
	if json.Unmarshal([]byte(ev.Content), &result) != nil {
		return nil
	}
	fresh := []string{}
	for _, matches := range [][]github.SearchMatch{result.Matches, result.GeneratedMatches} {
		for _, match := range matches {
			raw, _ := json.Marshal(match)
			hash := sha256.Sum256(raw)
			if !seen[hash] {
				seen[hash] = true
				if domain.ValidateChangePath(match.Path) == nil {
					fresh = appendUnique(fresh, match.Path)
				}
			}
		}
	}
	return fresh
}

// An uncertainty with an explicit source path needs evidence for that path.
// Otherwise require evidence in the approved plan or acceptance criteria.
func hasRelevantNewEvidence(unknown string, plan *domain.ImplementationPlan, fresh map[string]bool, repositoryPaths, verifiedReads []string) bool {
	allowed := map[string]bool{}
	known := append([]string{}, repositoryPaths...)
	for _, f := range plan.Files {
		allowed[f.Path] = true
		known = append(known, f.Path)
	}
	for _, c := range plan.AcceptanceCriteria {
		for _, p := range c.Paths {
			allowed[p] = true
			known = append(known, p)
		}
	}
	for p := range fresh {
		known = append(known, p)
	}
	// Preserve discovery through complete file reads when the final criterion
	// result cites them as evidence. Snippets cannot establish this coverage.
	for _, p := range verifiedReads {
		allowed[p] = true
		known = append(known, p)
	}
	explicit := map[string]bool{}
	for _, p := range known {
		if sourcePathMentioned(unknown, p) {
			explicit[p] = true
		}
	}
	if len(explicit) > 0 {
		allowed = explicit
	}
	for p := range fresh {
		if allowed[p] {
			return true
		}
	}
	return false
}

func sourcePathMentioned(text, path string) bool {
	if path == "" {
		return false
	}
	pathByte := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("_./-", rune(c))
	}
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], path)
		if index < 0 {
			return false
		}
		index += offset
		end := index + len(path)
		if (index == 0 || !pathByte(text[index-1])) && (end == len(text) || !pathByte(text[end])) {
			return true
		}
		offset = end
	}
	return false
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
	run.logger.Info("implement_search", "phase", run.impl.Status, "duration_ms", time.Since(started).Milliseconds(), "scanned_files", result.ScannedFiles, "scanned_bytes", result.ScannedBytes, "matches", len(result.Matches), "skipped_files", result.SkippedFiles, "has_more", result.NextCursor != "", "list_truncated", result.ListTruncated, "outcome", outcome(err), "error_code", implementationErrorCode(err))
	content := ""
	codePaths := []string{}
	if err != nil {
		content = "検索できませんでした: " + implementationErrorCode(err)
	} else {
		// Search the base for unchanged callers, but never show stale matches
		// from generated/deleted files during verification.
		if run.impl.Status == "verifying" || run.impl.Status == "verification_blocked" {
			changed := map[string]bool{}
			for _, c := range run.impl.Changes {
				changed[c.Path] = true
			}
			matches := result.Matches[:0]
			for _, m := range result.Matches {
				if !changed[m.Path] {
					matches = append(matches, m)
				}
			}
			result.Matches = matches
			for _, c := range run.impl.Changes {
				if c.Action == "delete" || (query.Prefix != "" && !strings.HasPrefix(c.Path, strings.TrimSuffix(query.Prefix, "/")+"/") && c.Path != query.Prefix) {
					continue
				}
				f, err := run.savedChange(ctx, c.Path)
				if err != nil {
					return err
				}
				for i, line := range strings.Split(f.Content, "\n") {
					if strings.Contains(line, query.Query) {
						if len(result.GeneratedMatches) >= github.SearchPageMatches {
							result.GeneratedTruncated = true
							break
						}
						result.GeneratedMatches = append(result.GeneratedMatches, github.SearchMatch{Path: c.Path, Line: i + 1, Text: domain.Clip(domain.MaskSecrets(line), 384)})
					}
				}
			}
		}
		debugJSON(ctx, "search_result", map[string]any{"phase": run.impl.Status, "query": query, "result": result})
		raw, _ := json.Marshal(result)
		content = string(raw)
		for _, match := range result.Matches {
			codePaths = append(codePaths, match.Path)
			run.impl.ContextFiles = addContextFile(run.impl.ContextFiles, match.Path)
		}
		for _, match := range result.GeneratedMatches {
			codePaths = append(codePaths, match.Path)
		}
	}
	*ev = append(*ev, evidence{Read: github.Read{Kind: "search", Path: query.Prefix}, Content: content, codePaths: codePaths})
	if len(*ev) > planEvidenceKept {
		*ev = (*ev)[len(*ev)-planEvidenceKept:]
	}
	return nil
}
