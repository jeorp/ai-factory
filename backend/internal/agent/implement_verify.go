package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ai-factory/backend/internal/domain"
)

type verificationPart struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Content string `json:"content"`
	Offset  int    `json:"offset"`
	Bytes   int    `json:"bytes"`
	Diff    string `json:"diff,omitempty"`
}

func (run *implementRun) verify(ctx context.Context) error {
	impl := run.impl
	if impl.Plan == nil || len(impl.Changes) == 0 {
		return errors.New("implementation_no_changes")
	}
	if err := run.save(ctx, "変更全体の整合性を確認しています（ビルド・テストは実行しません）"); err != nil {
		return err
	}
	files := []domain.FileChange{}
	for _, c := range impl.Changes {
		f, err := run.savedChange(ctx, c.Path)
		if err != nil {
			return err
		}
		if f.Action != impl.PlannedAction(f.Path) {
			return errors.New("change_action_not_planned")
		}
		files = append(files, f)
	}
	if err := domain.ValidateChangeSet(files, impl.ApprovedPaths()); err != nil {
		return err
	}
	parts := []verificationPart{}
	for _, f := range files {
		original := ""
		if f.Action != "create" {
			var exists bool
			var err error
			original, exists, err = run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, f.Path)
			if err != nil {
				return err
			}
			if !exists {
				return errors.New("change_file_missing")
			}
		}
		content := f.Content
		if f.Action == "delete" {
			content = original
		}
		diff := domain.CompactDiff(original, f.Content, 4000)
		// Every byte of generated code (or deleted source) is inspected. Only
		// the supplemental diff is compacted; file bodies are never dropped.
		for offset := 0; ; {
			chunk := domain.Clip(content[offset:], 8000)
			parts = append(parts, verificationPart{Path: f.Path, Action: f.Action, Content: chunk, Offset: offset, Bytes: len(content), Diff: diff})
			offset += len(chunk)
			if offset == len(content) {
				break
			}
			diff = ""
		}
	}
	result := &domain.ImplementationVerification{Issues: []string{}, Unverified: []string{}}
	changed := map[string]bool{}
	for _, f := range files {
		changed[f.Path] = true
	}
	for _, f := range impl.Plan.Files {
		if !changed[f.Path] {
			result.Unverified = append(result.Unverified, "計画にあるが変更されなかったファイル: "+f.Path)
		}
	}
	summaries := []string{}
	check := func(stage string, batch []verificationPart) error {
		correction := ""
		retries := 0
		for attempt := 1; attempt <= 3; attempt++ {
			task := implementTask{Mode: "verify", Request: impl.Request, Plan: impl.Plan, Done: impl.Changes, Declined: impl.Declined, FinalAnswer: true, Step: len(summaries) + 1, Attempt: attempt, RetryCount: retries, Correction: correction}
			payload := map[string]any{"implementTask": task, "stage": stage, "parts": batch}
			if stage == "cross" {
				payload["summaries"] = summaries
			}
			d, err := run.call(ctx, payload)
			if err != nil {
				if implementationErrorCode(err) != "invalid_implementation_result" {
					return err
				}
				run.retry("verify", attempt, &retries, err)
				correction = "verificationにissuesとunverifiedの配列を返してください。他の操作は行いません。"
				continue
			}
			if d.Verification == nil || !validImplementationDecision(d) {
				run.retry("verify", attempt, &retries, errors.New("invalid_implementation_result"))
				correction = "verificationにissuesとunverifiedの配列を返してください。他の操作は行いません。"
				continue
			}
			result.Issues = appendUnique(result.Issues, d.Verification.Issues...)
			result.Unverified = appendUnique(result.Unverified, d.Verification.Unverified...)
			for _, repair := range d.Verification.Repairs {
				found := false
				for _, prior := range result.Repairs {
					if prior.Path == repair.Path {
						found = true
						break
					}
				}
				if !found {
					result.Repairs = append(result.Repairs, repair)
				}
			}
			for _, file := range d.Verification.AdditionalFiles {
				found := false
				for _, prior := range result.AdditionalFiles {
					if prior.Path == file.Path {
						found = true
						break
					}
				}
				if !found && len(result.AdditionalFiles) < maxPendingFiles {
					result.AdditionalFiles = append(result.AdditionalFiles, file)
				}
			}
			if stage == "parts" {
				if strings.TrimSpace(d.Summary) == "" {
					result.Unverified = appendUnique(result.Unverified, "一部の区切りで契約の要約が得られず、区切り間の確認に制約があります。")
				}
				summaries = append(summaries, domain.Clip(d.Summary, 2048))
			}
			return nil
		}
		return errors.New("implementation_verification_failed")
	}
	for len(parts) > 0 {
		n, size := 0, 0
		for n < len(parts) {
			raw, _ := json.Marshal(parts[n])
			if n > 0 && size+len(raw) > 48000 {
				break
			}
			size += len(raw)
			n++
		}
		if err := check("parts", parts[:n]); err != nil {
			return err
		}
		parts = parts[n:]
		if err := run.save(ctx, fmt.Sprintf("変更全体の整合性を確認しています（%d区切り確認済み）", len(summaries))); err != nil {
			return err
		}
	}
	if len(summaries) > 1 {
		if err := check("cross", nil); err != nil {
			return err
		}
	}
	impl.Verification = result
	run.logger.Info("implement_verification_completed", "files", len(files), "batches", len(summaries), "issues", len(result.Issues), "unverified", len(result.Unverified))
	if len(result.Issues) > 0 {
		started, err := run.repair(ctx, result)
		if err != nil || started {
			return err
		}
	}
	impl.RepairFeedback = ""
	return run.ready(ctx)
}

func appendUnique(dst []string, values ...string) []string {
	for _, value := range values {
		found := false
		for _, existing := range dst {
			if existing == value {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, value)
		}
	}
	return dst
}
