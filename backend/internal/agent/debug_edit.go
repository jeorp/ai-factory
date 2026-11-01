package agent

import (
	"context"
	"errors"
	"strings"

	"ai-factory/backend/internal/domain"
)

type debugSourceLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

func debugLineContext(source string, first, last int) []debugSourceLine {
	lines := strings.Split(source, "\n")
	out := []debugSourceLine{}
	seen := map[int]bool{}
	for _, anchor := range []int{first, last} {
		for line := max(1, anchor-2); line <= min(len(lines), anchor+2); line++ {
			if !seen[line] {
				out = append(out, debugSourceLine{line, strings.TrimSuffix(lines[line-1], "\r")})
				seen[line] = true
			}
		}
	}
	return out
}

func (run *implementRun) debugEditFailure(ctx context.Context, step []string, originals map[string]string, proposals []ChangeProposal, err error) {
	if debugTrace(ctx) == nil {
		return
	}
	files := []map[string]any{}
	var wrapped *proposalEditError
	var applied *domain.EditApplyError
	errors.As(err, &wrapped)
	errors.As(err, &applied)
	detail := map[string]any{"error_code": implementationErrorCode(err), "step": run.impl.NextStep + 1, "call": run.calls, "phase": "edit", "step_paths": step}
	for index, p := range proposals {
		planned := run.impl.PlannedAction(p.Path)
		files = append(files, map[string]any{"proposal_index": index + 1, "path": p.Path, "planned_action": planned, "submitted_action": p.Action, "source_hash": p.SourceHash, "actual_source_hash": domain.SourceHash(originals[p.Path]), "search_edit_count": len(p.Edits), "line_edit_count": len(p.LineEdits)})
		if wrapped == nil || wrapped.path != p.Path || applied == nil {
			continue
		}
		detail["failed_path"], detail["edit_index"] = p.Path, applied.Index
		source := originals[p.Path]
		if applied.Index > 0 && applied.Index <= len(p.LineEdits) {
			edit := p.LineEdits[applied.Index-1]
			detail["line_edit"] = edit
			detail["source_context"] = debugLineContext(source, edit.StartLine, edit.EndLine)
			lines := strings.Split(source, "\n")
			if edit.StartLine > 0 && edit.StartLine <= len(lines) {
				detail["actual_start"] = strings.TrimSuffix(lines[edit.StartLine-1], "\r")
			}
			if edit.EndLine > 0 && edit.EndLine <= len(lines) {
				detail["actual_end"] = strings.TrimSuffix(lines[edit.EndLine-1], "\r")
			}
		} else if applied.Index > 0 && applied.Index <= len(p.Edits) {
			if applied.Index > 1 {
				if updated, e := domain.ApplyEdits(source, p.Edits[:applied.Index-1]); e == nil {
					source = updated
				}
			}
			edit := p.Edits[applied.Index-1]
			detail["search_edit"], detail["match_count"] = edit, strings.Count(source, edit.Search)
			// Numbered current source includes edits already applied before the failure.
			detail["current_source"] = domain.NumberedSource(source)
		}
	}
	detail["files"] = files
	debugJSON(ctx, "edit_rejected", detail)
}
