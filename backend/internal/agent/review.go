package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"strconv"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type reviewReader interface {
	PullRequest(context.Context, string, domain.Target) (github.PullRequest, int, error)
	ReviewFiles(context.Context, string, domain.Target, int, int) ([]github.ChangedFile, int, error)
}
type reviewFileCache interface {
	GetReviewFile(context.Context, string, domain.Target, string, string, int) (github.ChangedFile, bool)
	PutReviewFile(context.Context, domain.Target, string, string, int, github.ChangedFile)
}
type reviewTask struct {
	Mode    string              `json:"mode"`
	Patches []reviewPatch       `json:"patches,omitempty"`
	Steps   []domain.ReviewStep `json:"steps,omitempty"`
	Prior   string              `json:"prior"`
}

// reviewRun holds the state of one resumable PR review invocation. Each phase
// (planning, diff, cross, priority) advances the saved cursor in review and
// checkpoints after every bounded unit of work.
type reviewRun struct {
	r       *Runner
	gh      reviewReader
	in      Input
	emit    func(Checkpoint) error
	logger  *slog.Logger
	started time.Time
	cp      Checkpoint
	review  *domain.Review

	instructions []domain.Message
	// IDs of the directives already consumed at a model boundary.
	applied []string

	modelCalls      int
	runInputTokens  int
	runOutputTokens int
}

func (r *Runner) runReview(ctx context.Context, in Input, emit func(Checkpoint) error) error {
	ctx, cancel := context.WithTimeout(ctx, ReviewRunDuration)
	defer cancel()
	gh, ok := r.GitHub.(reviewReader)
	if !ok || in.Session.Target.Kind != "pull" {
		return errors.New("pull_request_required")
	}
	run := &reviewRun{r: r, gh: gh, in: in, emit: emit, logger: in.Logger, started: time.Now()}
	if run.logger == nil {
		run.logger = slog.Default()
	}
	run.cp = Checkpoint{Summary: in.Session.Summary, Sources: append([]domain.Source{}, in.Session.Sources...), Metrics: in.Session.Metrics}
	if !in.ContinueMetrics {
		run.cp.Metrics.LastInputTokens, run.cp.Metrics.LastOutputTokens, run.cp.Metrics.HasLastUsage = 0, 0, false
	}
	// Deep-copy mutable cursors/results: the socket owns its durable snapshot.
	raw, _ := json.Marshal(in.Session.Review)
	if err := json.Unmarshal(raw, &run.review); err != nil {
		return err
	}
	run.cp.Review = run.review
	run.instructions = []domain.Message{}
	run.applied = []string{}
	for _, m := range in.Session.Messages {
		if m.Role == "instruction" {
			run.instructions = append(run.instructions, m)
		}
	}
	if run.review == nil {
		if err := run.start(ctx); err != nil {
			return err
		}
	}
	if err := run.check(ctx); err != nil {
		return err
	}
	// Planning is resumable too; related implementation/tests share a directory.
	if run.review.Phase == "planning" {
		if err := run.plan(ctx); err != nil {
			return err
		}
	}
	if run.review.Phase == "diff" {
		if err := run.diff(ctx); err != nil {
			return err
		}
	}
	if run.review.Phase == "cross" {
		if err := run.cross(ctx); err != nil {
			return err
		}
	}
	unappliedNote := ""
	if run.review.Phase == "priority" {
		note, err := run.prioritize(ctx)
		if err != nil {
			return err
		}
		unappliedNote = note
	}
	return run.finish(ctx, unappliedNote)
}

// start fetches the PR and creates a fresh review in the planning phase.
func (run *reviewRun) start(ctx context.Context) error {
	in := run.in
	if !validReviewScope(in.Session.ReviewScope) {
		return errors.New("invalid_review_scope")
	}
	if err := run.save(ctx, "PRの概要と対象コミットを取得しています"); err != nil {
		return err
	}
	pr, err := run.fetchPR(ctx)
	if err != nil {
		return err
	}
	review := &domain.Review{Title: domain.Clip(pr.Title, 512), Body: domain.Clip(pr.Body, 2048), Base: pr.Base.SHA, Head: pr.Head.SHA, TotalFiles: pr.ChangedFiles, Phase: "planning", Steps: []domain.ReviewStep{}, Findings: []domain.Finding{}, Unverified: []string{"コード・テストの実行による検証は行っていません。"}}
	review.Scope = in.Session.ReviewScope
	for i := len(in.Session.Messages) - 1; i >= 0; i-- {
		if in.Session.Messages[i].Role == "user" {
			review.RequestMessageID = in.Session.Messages[i].ID
			break
		}
	}
	run.review = review
	if len(pr.Body) > 2048 {
		run.limit("PR本文の後半を入力上限のため省略しました。")
	}
	source := domain.Source{Label: fmt.Sprintf("PR #%d", in.Session.Target.Number), URL: fmt.Sprintf("https://github.com/%s/pull/%d", in.Session.Target.Repository, in.Session.Target.Number)}
	if !containsSource(run.cp.Sources, source.URL) {
		run.cp.Sources = append(run.cp.Sources, source)
	}
	return run.save(ctx, "PRの変更ファイルからレビュー計画を作成しています")
}

// finish writes the final summary and answer once every phase is done.
func (run *reviewRun) finish(ctx context.Context, unappliedNote string) error {
	review, cp := run.review, &run.cp
	cp.Summary = "PR #" + strconv.Itoa(run.in.Session.Target.Number) + " Base=" + review.Base + " Head=" + review.Head + "\n" + domain.Clip(review.CrossSummary, 800)
	for _, f := range review.Findings {
		cp.Summary += fmt.Sprintf("\n%s %s:%d %s", f.Severity, f.Path, f.Line, domain.Clip(f.Problem, 250))
		if len(cp.Summary) > 1800 {
			break
		}
	}
	cp.Summary = domain.Clip(cp.Summary, 2048)
	cp.Answer = fmt.Sprintf("PR #%d のレビューを完了しました。指摘 %d 件。計画・指摘・未確認範囲をレビュー結果に表示しています。対象ヘッド: %s。実行・テストによる検証は行っていません。", run.in.Session.Target.Number, len(review.Findings), review.Head[:12])
	if unappliedNote != "" {
		cp.Answer += "\n" + unappliedNote
	}
	return run.save(ctx, "レビュー結果を保存しました")
}

func (run *reviewRun) budgetExhausted() bool {
	return run.modelCalls >= ReviewMaxCalls || run.runInputTokens >= ReviewRunInputTokens || run.runOutputTokens >= ReviewRunOutputTokens
}

// recordUsage adds one model call's usage to the run budget and the metrics.
func (run *reviewRun) recordUsage(usage Usage) {
	run.runInputTokens += usage.InputTokens
	run.runOutputTokens += usage.OutputTokens
	addUsage(&run.cp.Metrics, usage)
}

func (run *reviewRun) recordInputBytes(n int) {
	if n > run.cp.Metrics.MaxInputBytes {
		run.cp.Metrics.MaxInputBytes = n
	}
}

// drainInstructions takes every directive queued so far without blocking.
func (run *reviewRun) drainInstructions() {
	for {
		select {
		case m, open := <-run.in.Instructions:
			if !open {
				return
			}
			run.instructions = append(run.instructions, m)
		default:
			return
		}
	}
}

func (run *reviewRun) pinnedInstructions() string {
	return domain.LatestInstructions(run.instructions, 4096)
}

func (run *reviewRun) markApplied() {
	run.applied = nil
	for _, m := range run.instructions {
		run.applied = append(run.applied, m.ID)
	}
}

func (run *reviewRun) unappliedInstructions() []string {
	done := map[string]bool{}
	for _, id := range run.applied {
		done[id] = true
	}
	pending := []string{}
	for _, m := range run.instructions {
		if !m.Applied && !done[m.ID] {
			pending = append(pending, m.ID)
		}
	}
	return pending
}

func (run *reviewRun) save(ctx context.Context, progress string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cp := &run.cp
	cp.Progress = progress
	cp.Metrics.DurationMS = time.Since(run.started).Milliseconds()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	if int64(mem.HeapAlloc) > cp.Metrics.HeapPeakBytes {
		cp.Metrics.HeapPeakBytes = int64(mem.HeapAlloc)
	}
	cp.Applied = nil
	// Mark only directives already consumed at a model boundary.
	cp.Applied = append(cp.Applied, run.applied...)
	cp.Review = run.review
	if run.review != nil {
		cp.Limitations = append([]string{}, run.review.Unverified...)
	}
	b, _ := json.Marshal(cp)
	if len(b) > 450*1024 {
		return errors.New("review_state_limit")
	}
	if echoes(string(b), run.in) {
		return errors.New("credential_echo_blocked")
	}
	// Checkpoint arguments must not share writable pointers with the next step.
	var frozen Checkpoint
	if err := json.Unmarshal(b, &frozen); err != nil {
		return err
	}
	return run.emit(frozen)
}

func (run *reviewRun) fetchPR(ctx context.Context) (github.PullRequest, error) {
	pr, n, err := run.gh.PullRequest(ctx, run.in.GitHubToken, run.in.Session.Target)
	run.cp.Metrics.ToolCalls++
	run.cp.Metrics.FetchedBytes += n
	return pr, err
}

// check stops the review when the PR moved since the plan was made.
func (run *reviewRun) check(ctx context.Context) error {
	pr, err := run.fetchPR(ctx)
	if err != nil {
		return err
	}
	if pr.Base.SHA != run.review.Base || pr.Head.SHA != run.review.Head || pr.ChangedFiles != run.review.TotalFiles {
		return errors.New("pr_changed_create_new_review")
	}
	return nil
}

// limit records an unverified range once, up to the saved cap.
func (run *reviewRun) limit(s string) {
	s = domain.Clip(s, 512)
	for _, old := range run.review.Unverified {
		if old == s {
			return
		}
	}
	if len(run.review.Unverified) < 250 {
		run.review.Unverified = append(run.review.Unverified, s)
	}
}

// markStepDone completes a step whose files are all done, unless it is
// partial or excluded.
func markStepDone(step *domain.ReviewStep) {
	complete := true
	for _, f := range step.Files {
		complete = complete && f.Done
	}
	if complete && step.State != "partial" && step.State != "excluded" {
		step.State = "done"
	}
}
