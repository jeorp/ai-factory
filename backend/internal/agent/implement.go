package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// Writer is the guarded GitHub write side. The model never calls it: the
// runner passes only server-validated change sets.
type Writer interface {
	Authorize(context.Context, string, string) (github.Repository, error)
	Commit(context.Context, string, github.Repository) (string, error)
	RawFile(context.Context, string, string, string, string) (string, bool, error)
	FileList(context.Context, string, string, string) ([]string, bool, error)
	PutChange(context.Context, string, int, domain.FileChange) error
	GetChange(context.Context, string, int, string) (domain.FileChange, bool)
	Publish(context.Context, string, github.PublishRequest, github.PublishState, func(github.PublishState) error) (github.PublishState, error)
	PullRequestOpen(context.Context, string, string, int, string, string) (bool, error)
	PullRequestTarget(context.Context, string, string, int) (github.PullRequestTarget, error)
}

// ImplementRequest is returned by the chat model to start the implementation workflow.
type ImplementRequest struct {
	Request string `json:"request"`
	// PullNumber is an existing AI Factory PR to push the fix to; 0 starts a new PR.
	PullNumber int `json:"pullNumber"`
}

// ChangeProposal is one file change proposed by the model. Updates use
// search/replace edits against the original; Content replaces the whole file
// only for creates or when no edits are given.
type ChangeProposal struct {
	Path    string        `json:"path"`
	Action  string        `json:"action"`
	Content string        `json:"content"`
	Edits   []domain.Edit `json:"edits"`
}

type ImplementResult struct {
	Changes    []ChangeProposal `json:"changes"`
	Unverified []string         `json:"unverified"`
	// AdditionalFiles are files outside the plan that the change also needs.
	// They are added only after the user approves.
	AdditionalFiles []domain.PendingFile `json:"additionalFiles"`
}

const (
	ImplementPlanCalls  = 12
	ImplementPlanReads  = 10
	ImplementStepCalls  = 5
	ImplementStepReads  = 3
	ImplementMaxCalls   = 100
	ImplementOutputToks = 16384
	ImplementRunTime    = 30 * time.Minute

	// Context budgets keep the extra code well inside implementInputFits.
	planEvidenceKept   = 5
	stepEvidenceKept   = 3
	maxContextFiles    = 8
	contextFileBytes   = 12000
	contextTotalBytes  = 36000
	previousDiffBytes  = 6000
	previousTotalBytes = 20000
)

type implementFile struct {
	Path     string `json:"path"`
	Action   string `json:"action"`
	Approach string `json:"approach"`
	Original string `json:"original,omitempty"`
}

type implementTask struct {
	Mode        string                     `json:"mode"`
	Request     string                     `json:"request"`
	BaseBranch  string                     `json:"baseBranch"`
	FollowUp    bool                       `json:"followUp,omitempty"`
	Plan        *domain.ImplementationPlan `json:"plan,omitempty"`
	Step        int                        `json:"step,omitempty"`
	TotalSteps  int                        `json:"totalSteps,omitempty"`
	Files       []implementFile            `json:"files,omitempty"`
	Done        []domain.ChangeSummary     `json:"done,omitempty"`
	ReadBudget  int                        `json:"readBudget"`
	FinalAnswer bool                       `json:"finalAnswerOnly,omitempty"`
	Correction  string                     `json:"correction,omitempty"`
	Declined    []string                   `json:"declinedFiles,omitempty"`
}

// NewCacheID returns an unguessable key for one implementation's change cache.
func NewCacheID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func cloneImplementation(v *domain.Implementation) (*domain.Implementation, error) {
	if v == nil {
		return nil, errors.New("implementation_missing")
	}
	raw, _ := json.Marshal(v)
	var out domain.Implementation
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	// CacheID is hidden from JSON so that it never reaches the browser.
	out.CacheID = v.CacheID
	return &out, nil
}

func (r *Runner) implementFromChat(ctx context.Context, in Input, cp Checkpoint, req ImplementRequest, emit func(Checkpoint) error) error {
	if strings.TrimSpace(req.Request) == "" {
		cp.Answer = "どのような変更を実装するか、このチャットで具体的に教えてください。"
		return emit(cp)
	}
	impl := StartImplementation(in.Session.Implementation, req, NewCacheID())
	cp.RunKind, cp.Implementation = "implement", impl
	cp.Progress = "実装の計画を作成します"
	if err := emit(cp); err != nil {
		return err
	}
	in.Session.RunKind, in.Session.Implementation = "implement", impl
	in.Session.Summary, in.Session.Sources, in.Session.Limitations, in.Session.Metrics = cp.Summary, cp.Sources, cp.Limitations, cp.Metrics
	in.ContinueMetrics = true
	return r.runImplementation(ctx, in, emit)
}

// StartImplementation begins a new implementation cycle. With a PullNumber for
// a PR other than the one this session is already following, the cycle starts
// fresh and the plan step attaches it to that PR's branch.
func StartImplementation(prev *domain.Implementation, req ImplementRequest, cacheID string) *domain.Implementation {
	impl := domain.NextImplementation(prev, req.Request, cacheID)
	if req.PullNumber > 0 && req.PullNumber != impl.PullNumber {
		impl = domain.NextImplementation(nil, req.Request, cacheID)
		if prev != nil {
			impl.Version = prev.Version + 1
		}
		impl.PullNumber, impl.FixTarget = req.PullNumber, true
	}
	return impl
}

type implementRun struct {
	r            *Runner
	in           Input
	emit         func(Checkpoint) error
	cp           Checkpoint
	impl         *domain.Implementation
	logger       *slog.Logger
	instructions []domain.Message
	calls        int
}

func (r *Runner) runImplementation(ctx context.Context, in Input, emit func(Checkpoint) error) error {
	if r.Writer == nil {
		return errors.New("implementation_unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, ImplementRunTime)
	defer cancel()
	impl, err := cloneImplementation(in.Session.Implementation)
	if err != nil {
		return err
	}
	run := &implementRun{r: r, in: in, emit: emit, impl: impl, logger: in.Logger}
	if run.logger == nil {
		run.logger = slog.Default()
	}
	run.cp = Checkpoint{Summary: in.Session.Summary, Sources: append([]domain.Source{}, in.Session.Sources...), Limitations: append([]string{}, in.Session.Limitations...), Metrics: in.Session.Metrics, Implementation: impl}
	if !in.ContinueMetrics {
		run.cp.Metrics.LastInputTokens, run.cp.Metrics.LastOutputTokens, run.cp.Metrics.HasLastUsage = 0, 0, false
	}
	for _, m := range in.Session.Messages {
		if m.Role == "instruction" {
			run.instructions = append(run.instructions, m)
		}
	}
	switch impl.Status {
	case "planning":
		return run.plan(ctx)
	case "editing":
		return run.edit(ctx)
	case "publishing":
		return run.publish(ctx)
	}
	return errors.New("implementation_nothing_to_run")
}

func (run *implementRun) save(ctx context.Context, progress string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	run.cp.Progress = progress
	// The socket keeps the checkpoint after the ack while this run keeps
	// mutating run.impl, so hand over a copy.
	snapshot, err := cloneImplementation(run.impl)
	if err != nil {
		return err
	}
	run.cp.Implementation = snapshot
	return run.emit(run.cp)
}

func (run *implementRun) drain() {
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

func (run *implementRun) pinned() string {
	var b strings.Builder
	for _, m := range run.instructions {
		b.WriteString(m.Text + "\n")
	}
	return domain.Clip(b.String(), 4096)
}

func (run *implementRun) target() domain.Target {
	return domain.Target{Repository: run.in.Session.Target.Repository, Commit: run.impl.BaseCommit, Kind: "repository"}
}

// call sends one bounded model request and records usage. Instructions sent
// with the request are marked applied in the next checkpoint.
func (run *implementRun) call(ctx context.Context, payload map[string]any) (Decision, error) {
	if run.calls >= ImplementMaxCalls {
		return Decision{}, errors.New("implementation_call_limit")
	}
	run.calls++
	run.drain()
	payload["instructions"] = run.pinned()
	payload["target"] = run.target()
	payload["repositoryOverview"] = run.in.Session.RepositoryOverview
	body, _ := json.Marshal(payload)
	if !implementInputFits(body) {
		return Decision{}, errors.New("ai_input_limit")
	}
	run.cp.Applied = nil
	for _, m := range run.instructions {
		run.cp.Applied = append(run.cp.Applied, m.ID)
	}
	if len(body) > run.cp.Metrics.MaxInputBytes {
		run.cp.Metrics.MaxInputBytes = len(body)
	}
	started := time.Now()
	d, usage, err := run.r.AI.Next(ctx, run.in.Key, run.in.Session.Model, string(body))
	run.logger.Info("implement_model_call", "duration_ms", time.Since(started).Milliseconds(), "outcome", outcome(err), "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens)
	run.cp.Metrics.InputTokens += usage.InputTokens
	run.cp.Metrics.OutputTokens += usage.OutputTokens
	run.cp.Metrics.LastInputTokens += usage.InputTokens
	run.cp.Metrics.LastOutputTokens += usage.OutputTokens
	if usage.InputTokens != 0 || usage.OutputTokens != 0 {
		run.cp.Metrics.HasLastUsage = true
	}
	if err != nil {
		return d, err
	}
	raw, _ := json.Marshal(d)
	if (len(run.in.Key) > 0 && strings.Contains(string(raw), run.in.Key)) || (len(run.in.GitHubToken) > 0 && strings.Contains(string(raw), run.in.GitHubToken)) {
		return d, errors.New("credential_echo_blocked")
	}
	return d, nil
}

// read performs a model-requested read pinned to the base commit.
func (run *implementRun) read(ctx context.Context, read github.Read, ev *[]evidence, keep int) error {
	if read.Kind != "file" && read.Kind != "repository" {
		*ev = append(*ev, evidence{Read: read, Content: "この区切りではファイルとディレクトリだけ取得できます。"})
		return nil
	}
	if err := read.Validate(); err != nil {
		return err
	}
	if err := run.save(ctx, "GitHub: "+read.Kind+" "+read.Path); err != nil {
		return err
	}
	run.cp.Metrics.ToolCalls++
	content, source, n, err := run.r.GitHub.Read(ctx, run.in.GitHubToken, run.target(), read)
	run.cp.Metrics.FetchedBytes += n
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		content = "取得できませんでした: " + err.Error()
	} else {
		if !containsSource(run.cp.Sources, source.URL) {
			run.cp.Sources = append(run.cp.Sources, source)
			if len(run.cp.Sources) > 40 {
				run.cp.Sources = run.cp.Sources[len(run.cp.Sources)-40:]
			}
		}
		if len(content) > 16000 {
			content = domain.Clip(content, 16000) + "\n[入力サイズ制限により後半を省略]"
		}
	}
	*ev = append(*ev, evidence{Read: read, Content: content})
	if len(*ev) > keep {
		*ev = (*ev)[len(*ev)-keep:]
	}
	return nil
}

func (run *implementRun) plan(ctx context.Context) error {
	impl := run.impl
	if impl.PullNumber > 0 && (impl.Branch == "" || impl.FixTarget) {
		// A fix request for an existing PR: build on its current head and push
		// to its branch. Only open ai-factory/ branches in this repository qualify.
		// The head is re-read on every plan so that a named PR is never replaced
		// by a new one; a closed PR stops with an error instead.
		pr, err := run.r.Writer.PullRequestTarget(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.PullNumber)
		if err != nil {
			return err
		}
		impl.BaseBranch, impl.BaseCommit, impl.ExpectedHead = pr.Base, pr.Head, pr.Head
		impl.Branch, impl.PullURL, impl.Title = pr.Branch, pr.URL, pr.Title
		if err := run.save(ctx, fmt.Sprintf("PR #%d のブランチに修正を追加します", impl.PullNumber)); err != nil {
			return err
		}
	}
	if impl.ExpectedHead != "" {
		open, err := run.r.Writer.PullRequestOpen(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.PullNumber, impl.Branch, impl.ExpectedHead)
		if err != nil {
			return err
		}
		if !open {
			// Only a merged or closed PR starts over on a new branch. If the PR is
			// still open and someone pushed to it, build on its new head instead.
			pr, err := run.r.Writer.PullRequestTarget(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.PullNumber)
			switch {
			case err == nil && pr.Branch == impl.Branch:
				impl.BaseCommit, impl.ExpectedHead = pr.Head, pr.Head
				impl.Unverified = append(impl.Unverified, fmt.Sprintf("PR #%d のブランチが更新されていたため、最新のコミットを基準にしました。", impl.PullNumber))
			case err == nil || err.Error() == "pull_request_closed" || err.Error() == "pull_request_not_found":
				previous := impl.PullNumber
				impl.DetachFollowUp()
				impl.Unverified = append(impl.Unverified, fmt.Sprintf("PR #%d はマージ・クローズ済みのため、最新のデフォルトブランチから新しいブランチで作成します。", previous))
			default:
				return err
			}
		}
	}
	if impl.BaseCommit == "" {
		repo, err := run.r.Writer.Authorize(ctx, run.in.GitHubToken, run.in.Session.Target.Repository)
		if err != nil {
			return err
		}
		sha, err := run.r.Writer.Commit(ctx, run.in.GitHubToken, repo)
		if err != nil {
			return err
		}
		impl.BaseBranch, impl.BaseCommit = repo.DefaultBranch, sha
	}
	if err := run.save(ctx, "依頼内容と関連コードを確認して実装計画を作成しています"); err != nil {
		return err
	}
	files, allPaths, err := run.fileList(ctx)
	if err != nil {
		return err
	}
	impl.ContextFiles = nil
	ev := []evidence{}
	if impl.ExpectedHead != "" && impl.PullNumber > 0 {
		ciEv := run.collectCIFailureLogs(ctx, impl.PullNumber)
		if len(ciEv) > 0 {
			ev = append(ev, ciEv...)
		}
	}
	reads := 0
	correction := ""
	gapReviewed := false
	for call := 0; call < ImplementPlanCalls; call++ {
		final := reads >= ImplementPlanReads || call == ImplementPlanCalls-1
		task := implementTask{Mode: "plan", Request: impl.Request, BaseBranch: impl.BaseBranch, FollowUp: impl.ExpectedHead != "", ReadBudget: ImplementPlanReads - reads, FinalAnswer: final, Correction: correction}
		d, err := run.call(ctx, map[string]any{"implementTask": task, "summary": domain.Clip(run.cp.Summary, 2048), "evidence": ev, "repositoryFiles": files})
		if err != nil && err.Error() == "ai_input_limit" && len(ev) > 0 {
			ev = ev[1:]
			correction = "入力上限のため古い取得結果を省略しました。"
			// The request never reached the model, so it does not use up a call.
			call--
			continue
		}
		if err != nil {
			return err
		}
		correction = ""
		if d.Read != nil && !final {
			reads++
			if err := run.read(ctx, *d.Read, &ev, planEvidenceKept); err != nil {
				return err
			}
			if d.Read.Kind == "file" {
				impl.ContextFiles = addContextFile(impl.ContextFiles, d.Read.Path)
			}
			run.cp.Summary = domain.Clip(d.Summary, 2048)
			continue
		}
		if d.Plan == nil {
			if strings.TrimSpace(d.Answer) != "" {
				// The model needs the user to clarify before planning.
				impl.Status = "plan_ready"
				impl.Plan = nil
				run.cp.Answer = d.Answer
				return run.save(ctx, "実装の前に確認が必要です")
			}
			correction = "planを返してください。"
			continue
		}
		if err := domain.ValidatePlan(d.Plan); err != nil {
			correction = "計画が無効です: " + err.Error() + "。変更対象は20ファイル以内、.github/workflows・バイナリ・ロックファイルは対象外です。"
			continue
		}
		problems, err := run.checkPlanPaths(ctx, d.Plan)
		if err != nil {
			return err
		}
		if len(problems) > 0 {
			// The model sometimes plans an update for a path it never read. Catch
			// it here so the user only approves plans that can be applied.
			run.logger.Info("implement_plan_path_mismatch", "count", len(problems))
			correction = "計画のパスがベースコミットと一致しません: " + strings.Join(problems, " / ") + "。変更・削除は実在するファイルだけ、追加は存在しないパスだけにしてください。パスが不明ならrepositoryまたはfileで確認してください。"
			continue
		}
		if !gapReviewed && call < ImplementPlanCalls-1 {
			// Ask the model once to reconsider files it likely missed: tests next
			// to planned files and files it read while planning.
			gapReviewed = true
			if related := relatedFiles(d.Plan, impl.ContextFiles, allPaths); len(related) > 0 {
				run.logger.Info("implement_plan_gap_review", "count", len(related))
				correction = "計画の見直し: 次のファイルは計画にありませんが、関連する可能性があります: " + strings.Join(related, "、") + "。変更した定義・関数・型の利用箇所やテストで変更が必要なものは計画に加えてください。必要なら読んで確認できます。不要なら同じ計画をそのまま返してください。"
				continue
			}
		}
		impl.Plan = d.Plan
		impl.Steps = domain.PlanSteps(d.Plan.Files)
		impl.NextStep = 0
		impl.Status = "plan_ready"
		impl.Summary = domain.Clip(d.Summary, 2048)
		run.cp.Summary = impl.Summary
		run.cp.Answer = formatPlan(impl)
		return run.save(ctx, "実装計画を作成しました。承認を待っています")
	}
	return errors.New("implementation_plan_failed")
}

// checkPlanPaths confirms that planned updates and deletes exist at the base
// commit and planned creates do not. Problems are returned for the model to fix;
// an error is returned only when GitHub itself could not be read.
func (run *implementRun) checkPlanPaths(ctx context.Context, plan *domain.ImplementationPlan) ([]string, error) {
	problems := []string{}
	for _, f := range plan.Files {
		_, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, run.impl.BaseCommit, f.Path)
		if err != nil {
			switch err.Error() {
			case "change_file_size_limit", "change_not_text", "invalid_change_path", "protected_change_path", "binary_or_generated_file_excluded":
				problems = append(problems, f.Path+" は変更できません（"+err.Error()+"）")
				continue
			}
			return nil, err
		}
		switch {
		case f.Action == "create" && exists:
			problems = append(problems, f.Path+" は既に存在します（updateにしてください）")
		case f.Action != "create" && !exists:
			problems = append(problems, f.Path+" は存在しません")
		}
	}
	return problems, nil
}

func (run *implementRun) collectCIFailureLogs(ctx context.Context, pullNumber int) []evidence {
	if run.r.GitHub == nil {
		return nil
	}
	checkRead := github.Read{Kind: "check_runs", Number: pullNumber}
	raw, source, _, err := run.r.GitHub.Read(ctx, run.in.GitHubToken, run.target(), checkRead)
	if err != nil {
		run.logger.Info("implement_ci_check_runs_failed", "error", err.Error())
		return nil
	}
	var checksResult struct {
		CheckRuns []struct {
			ID         int64  `json:"id"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"check_runs"`
	}
	var out []evidence
	out = append(out, evidence{Read: checkRead, Content: domain.MaskSecrets(domain.Clip(raw, 8000))})
	if !containsSource(run.cp.Sources, source.URL) {
		run.cp.Sources = append(run.cp.Sources, source)
	}
	if json.Unmarshal([]byte(raw), &checksResult) != nil {
		return out
	}
	failedJobs := 0
	for _, cr := range checksResult.CheckRuns {
		if cr.Conclusion == "failure" || cr.Conclusion == "timed_out" {
			failedJobs++
			if failedJobs > 3 {
				break
			}
			jobRead := github.Read{Kind: "job_log", Number: int(cr.ID)}
			logContent, jobSource, _, jobErr := run.r.GitHub.Read(ctx, run.in.GitHubToken, run.target(), jobRead)
			if jobErr == nil {
				out = append(out, evidence{Read: jobRead, Content: fmt.Sprintf("Job %s (#%d) 失敗ログ末尾:\n%s", cr.Name, cr.ID, logContent)})
				if !containsSource(run.cp.Sources, jobSource.URL) {
					run.cp.Sources = append(run.cp.Sources, jobSource)
				}
			}
		}
	}
	return out
}

func (run *implementRun) edit(ctx context.Context) error {
	impl := run.impl
	if impl.Plan == nil || len(impl.Steps) == 0 || !domain.ValidCommit(impl.BaseCommit) {
		return errors.New("implementation_plan_required")
	}
	approved := impl.ApprovedPaths()
	for impl.NextStep < len(impl.Steps) {
		step := impl.Steps[impl.NextStep].Files
		if err := run.save(ctx, fmt.Sprintf("変更を作成しています（%d/%d）", impl.NextStep+1, len(impl.Steps))); err != nil {
			return err
		}
		files := []implementFile{}
		originals := map[string]string{}
		for _, p := range step {
			f := implementFile{Path: p, Action: impl.PlannedAction(p)}
			for _, planned := range impl.Plan.Files {
				if planned.Path == p {
					f.Approach = planned.Approach
				}
			}
			content, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, p)
			if err != nil {
				return err
			}
			if f.Action == "create" && exists {
				return fmt.Errorf("change_file_exists")
			}
			if f.Action != "create" && !exists {
				return fmt.Errorf("change_file_missing")
			}
			originals[p] = content
			if f.Action == "update" {
				f.Original = content
			}
			files = append(files, f)
		}
		related, previous, err := run.stepContext(ctx, step)
		if err != nil {
			return err
		}
		ev := []evidence{}
		reads := 0
		correction := ""
		var applied []domain.FileChange
		var result *ImplementResult
		var pending []domain.PendingFile
		var rejected []string
		split := false
		for call := 0; call < ImplementStepCalls && result == nil; call++ {
			final := reads >= ImplementStepReads || call == ImplementStepCalls-1
			task := implementTask{Mode: "edit", Request: impl.Request, BaseBranch: impl.BaseBranch, Plan: impl.Plan, Step: impl.NextStep + 1, TotalSteps: len(impl.Steps), Files: files, Done: impl.Changes, Declined: impl.Declined, ReadBudget: ImplementStepReads - reads, FinalAnswer: final, Correction: correction}
			d, err := run.call(ctx, map[string]any{"implementTask": task, "summary": domain.Clip(run.cp.Summary, 2048), "evidence": ev, "context": related, "previousChanges": previous})
			// Shed the optional context before splitting the step.
			// The request never reached the model, so shedding does not use up a
			// call; it ends because ev, related and previous only shrink here and
			// reads are bounded.
			if err != nil && err.Error() == "ai_input_limit" && (len(ev) > 0 || len(related) > 0 || len(previous) > 0) {
				switch {
				case len(ev) > 0:
					ev = nil
				case len(related) > 0:
					related = nil
				default:
					previous = nil
				}
				correction = "入力上限のため関連コードの一部を省略しました。"
				call--
				continue
			}
			if err != nil && err.Error() == "ai_input_limit" && len(step) > 1 {
				// Split the step and retry with fewer original files.
				half := len(step) / 2
				steps := append([]domain.ImplementationStep{}, impl.Steps[:impl.NextStep]...)
				steps = append(steps, domain.ImplementationStep{Files: step[:half]}, domain.ImplementationStep{Files: step[half:]})
				impl.Steps = append(steps, impl.Steps[impl.NextStep+1:]...)
				split = true
				break
			}
			if err != nil {
				return err
			}
			correction = ""
			if d.Read != nil && !final {
				reads++
				if err := run.read(ctx, *d.Read, &ev, stepEvidenceKept); err != nil {
					return err
				}
				continue
			}
			if d.Implementation == nil {
				correction = "implementationに変更を返してください。"
				continue
			}
			changes, err := applyProposals(step, impl, originals, d.Implementation.Changes)
			if err == nil && (len(changes) > 0 || len(d.Implementation.AdditionalFiles) == 0) {
				err = domain.ValidateChangeSet(changes, approved)
			}
			if err != nil {
				correction = "前回の変更は適用できませんでした: " + err.Error() + "。searchは元ファイル内で一意に一致する原文をそのまま指定し、計画どおりのpathとactionだけを返してください。計画外のファイルも変更が必要ならchangesに入れず、additionalFilesで提案してください。"
				continue
			}
			pending, rejected, err = run.checkAdditions(ctx, d.Implementation.AdditionalFiles)
			if err != nil {
				return err
			}
			if len(changes) == 0 && len(pending) == 0 {
				correction = "有効な変更も追加提案もありません: " + strings.Join(rejected, " / ") + "。変更を返すか、有効な追加ファイルを提案してください。"
				continue
			}
			applied, result = changes, d.Implementation
			run.cp.Summary = domain.Clip(d.Summary, 2048)
		}
		if split {
			continue
		}
		if result == nil {
			return errors.New("implementation_edit_failed")
		}
		// Check the running total now rather than failing only at publish time.
		total := impl.ChangeBytes()
		for _, f := range applied {
			for _, c := range impl.Changes {
				if c.Path == f.Path {
					total -= c.Bytes
				}
			}
			total += len(f.Content)
		}
		if total > domain.MaxChangeSetBytes {
			return errors.New("change_set_size_limit")
		}
		covered := map[string]bool{}
		for _, f := range applied {
			if err := run.r.Writer.PutChange(ctx, impl.CacheID, impl.Version, f); err != nil {
				return err
			}
			covered[f.Path] = true
			summary := domain.ChangeSummary{Path: f.Path, Action: f.Action, Bytes: len(f.Content)}
			replaced := false
			for i := range impl.Changes {
				if impl.Changes[i].Path == f.Path {
					impl.Changes[i], replaced = summary, true
				}
			}
			if !replaced {
				impl.Changes = append(impl.Changes, summary)
			}
		}
		// Keep only unfinished files in the current step while an amendment is
		// pending; completed files must not be edited twice on resume.
		remaining := []string{}
		for _, p := range step {
			if !covered[p] {
				remaining = append(remaining, p)
				if len(pending) == 0 {
					impl.Unverified = append(impl.Unverified, "計画にあるが変更されなかったファイル: "+p)
				}
			}
		}
		holdStep := len(pending) > 0 && len(remaining) > 0
		if holdStep {
			impl.Steps[impl.NextStep].Files = remaining
		}
		for _, u := range result.Unverified {
			impl.Unverified = append(impl.Unverified, domain.Clip(u, 512))
		}
		impl.Unverified = append(impl.Unverified, rejected...)
		if len(impl.Unverified) > 20 {
			impl.Unverified = impl.Unverified[len(impl.Unverified)-20:]
		}
		impl.Summary = run.cp.Summary
		if !holdStep {
			impl.NextStep++
		}
		if len(pending) > 0 {
			impl.PendingFiles = pending
			impl.Status = "amend_ready"
			run.cp.Answer = formatAmendment(impl)
			return run.save(ctx, "計画にないファイルの追加について承認を待っています")
		}
		if err := run.save(ctx, fmt.Sprintf("変更を保存しました（%d/%d）", impl.NextStep, len(impl.Steps))); err != nil {
			return err
		}
	}
	if len(impl.Changes) == 0 {
		return errors.New("implementation_no_changes")
	}
	impl.Status = "ready"
	if impl.Branch == "" {
		impl.Branch = defaultBranchName(run.in.Session.ID, impl.Version)
	}
	if impl.Title == "" {
		impl.Title = defaultTitle(impl)
	}
	impl.Body = defaultBody(impl)
	run.cp.Answer = formatChanges(impl)
	return run.save(ctx, "変更を作成しました。差分を確認してPRを作成してください")
}

// maxPendingFiles bounds one amendment request.
const maxPendingFiles = 5

// checkAdditions validates files the model asked to add to the plan. Accepted
// ones wait for the user's approval; the rest are reported as unverified.
func (run *implementRun) checkAdditions(ctx context.Context, files []domain.PendingFile) ([]domain.PendingFile, []string, error) {
	impl := run.impl
	planned := map[string]bool{}
	for _, f := range impl.Plan.Files {
		planned[f.Path] = true
	}
	declined := map[string]bool{}
	for _, p := range impl.Declined {
		declined[p] = true
	}
	pending := []domain.PendingFile{}
	rejected := []string{}
	reject := func(f domain.PendingFile, why string) {
		rejected = append(rejected, domain.Clip("追加の提案を受け付けられませんでした: "+f.Path+"（"+why+"）", 512))
	}
	for _, f := range files {
		f.Path, f.Approach, f.Reason = strings.TrimSpace(f.Path), domain.Clip(strings.TrimSpace(f.Approach), 1024), domain.Clip(strings.TrimSpace(f.Reason), 512)
		switch {
		case domain.ValidateChangePath(f.Path) != nil:
			reject(f, "変更できないパス")
			continue
		case planned[f.Path]:
			continue // already in the plan; nothing to approve
		case declined[f.Path]:
			reject(f, "利用者が見送り済み")
			continue
		case f.Action != "create" && f.Action != "update" && f.Action != "delete":
			reject(f, "不明な操作")
			continue
		case f.Reason == "" || f.Approach == "":
			reject(f, "理由または方針がない")
			continue
		case len(pending) >= maxPendingFiles || len(impl.Plan.Files)+len(pending) >= domain.MaxChangeFiles:
			reject(f, "ファイル数の上限")
			continue
		}
		_, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, f.Path)
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			switch err.Error() {
			case "change_file_size_limit", "change_not_text", "invalid_change_path", "protected_change_path", "binary_or_generated_file_excluded":
				reject(f, err.Error())
				continue
			}
			return nil, nil, err
		}
		if (f.Action == "create") == exists {
			reject(f, "ベースコミットでの有無が操作と合わない")
			continue
		}
		planned[f.Path] = true
		pending = append(pending, f)
	}
	return pending, rejected, nil
}

// relatedFiles lists files the plan may have missed: test files next to
// planned files, and files the model read while planning but left out.
func relatedFiles(plan *domain.ImplementationPlan, read []string, all []string) []string {
	planned := map[string]bool{}
	for _, f := range plan.Files {
		planned[f.Path] = true
	}
	exists := map[string]bool{}
	for _, p := range all {
		exists[p] = true
	}
	out := []string{}
	add := func(p string) {
		if planned[p] || domain.ValidateChangePath(p) != nil || (len(all) > 0 && !exists[p]) {
			return
		}
		planned[p] = true
		out = append(out, p)
	}
	for _, f := range plan.Files {
		if f.Action == "create" {
			continue
		}
		dir, base := path.Split(f.Path)
		ext := path.Ext(base)
		stem := strings.TrimSuffix(base, ext)
		for _, c := range []string{stem + "_test" + ext, stem + ".test" + ext, stem + ".spec" + ext, stem + ".test.ts", stem + ".spec.ts"} {
			if len(all) > 0 {
				add(dir + c)
			}
		}
	}
	for _, p := range read {
		add(p)
	}
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

type contextFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

type previousChange struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Diff   string `json:"diff"`
}

// fileList gives the model the real paths of the base commit, newline separated.
// The list is a planning aid: when it cannot be read the plan goes ahead with
// repository reads only, and the plan path check still applies.
func (run *implementRun) fileList(ctx context.Context) (string, []string, error) {
	paths, truncated, err := run.r.Writer.FileList(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, run.impl.BaseCommit)
	if err != nil {
		if ctx.Err() != nil {
			return "", nil, ctx.Err()
		}
		run.logger.Info("implement_file_list_unavailable", "error", err.Error())
		return "[ファイル一覧を取得できませんでした。repositoryで確認してください]", nil, nil
	}
	// Cut only at a line break so no partial path reaches the model.
	var list strings.Builder
	for _, p := range paths {
		if list.Len()+len(p)+1 > github.MaxListedBytes {
			truncated = true
			break
		}
		list.WriteString(p + "\n")
	}
	if truncated {
		list.WriteString("[一覧の一部（深い階層）を省略しました。必要ならrepositoryで確認してください]")
	}
	return strings.TrimSuffix(list.String(), "\n"), paths, nil
}

func addContextFile(files []string, p string) []string {
	if domain.ValidateChangePath(p) != nil {
		return files
	}
	out := []string{}
	for _, f := range files {
		if f != p {
			out = append(out, f)
		}
	}
	out = append(out, p)
	if len(out) > maxContextFiles {
		out = out[len(out)-maxContextFiles:]
	}
	return out
}

// stepContext collects what a step needs beyond its own files: diffs of files
// changed by earlier steps, so names and signatures stay consistent, and the
// code read while planning, so definitions are not guessed.
func (run *implementRun) stepContext(ctx context.Context, step []string) ([]contextFile, []previousChange, error) {
	impl := run.impl
	inStep := map[string]bool{}
	for _, p := range step {
		inStep[p] = true
	}
	changed := map[string]bool{}
	previous := []previousChange{}
	total := 0
	for _, c := range impl.Changes {
		changed[c.Path] = true
		if inStep[c.Path] || total >= previousTotalBytes {
			continue
		}
		f, ok := run.r.Writer.GetChange(ctx, impl.CacheID, impl.Version, c.Path)
		if !ok {
			continue
		}
		diff := "（削除）"
		if c.Action != "delete" {
			original := ""
			if c.Action == "update" {
				content, _, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, c.Path)
				if err != nil {
					if ctx.Err() != nil {
						return nil, nil, ctx.Err()
					}
					continue
				}
				original = content
			}
			diff = domain.CompactDiff(original, f.Content, min(previousDiffBytes, previousTotalBytes-total))
		}
		total += len(diff)
		previous = append(previous, previousChange{Path: c.Path, Action: c.Action, Diff: diff})
	}
	candidates := append([]string{}, impl.ContextFiles...)
	if impl.Plan != nil {
		for _, f := range impl.Plan.Files {
			if f.Action == "update" {
				candidates = append(candidates, f.Path)
			}
		}
	}
	related := []contextFile{}
	seen := map[string]bool{}
	total = 0
	for _, p := range candidates {
		if inStep[p] || changed[p] || seen[p] || total >= contextTotalBytes {
			continue
		}
		seen[p] = true
		content, exists, err := run.r.Writer.RawFile(ctx, run.in.GitHubToken, run.in.Session.Target.Repository, impl.BaseCommit, p)
		if err != nil || !exists {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			continue // directories, binaries and oversized files are simply left out
		}
		limit := min(contextFileBytes, contextTotalBytes-total)
		if len(content) > limit {
			content = domain.Clip(content, limit) + "\n[以降省略]"
		}
		total += len(content)
		related = append(related, contextFile{Path: p, Content: content})
	}
	return related, previous, nil
}

// applyProposals turns model proposals into full file contents. Only files of
// the current step with their approved action are accepted.
func applyProposals(step []string, impl *domain.Implementation, originals map[string]string, proposals []ChangeProposal) ([]domain.FileChange, error) {
	inStep := map[string]bool{}
	for _, p := range step {
		inStep[p] = true
	}
	out := []domain.FileChange{}
	seen := map[string]bool{}
	for _, p := range proposals {
		if !inStep[p.Path] {
			return nil, errors.New("change_path_not_in_step")
		}
		if seen[p.Path] {
			return nil, errors.New("change_path_duplicate")
		}
		seen[p.Path] = true
		if p.Action != impl.PlannedAction(p.Path) {
			return nil, errors.New("change_action_not_planned")
		}
		switch p.Action {
		case "delete":
			out = append(out, domain.FileChange{Path: p.Path, Action: "delete"})
		case "create":
			if p.Content == "" || len(p.Edits) > 0 {
				return nil, errors.New("create_requires_content")
			}
			out = append(out, domain.FileChange{Path: p.Path, Action: "create", Content: p.Content})
		case "update":
			content := p.Content
			if len(p.Edits) > 0 {
				var err error
				if content, err = domain.ApplyEdits(originals[p.Path], p.Edits); err != nil {
					return nil, fmt.Errorf("%s: %w", p.Path, err)
				}
			} else if content == "" {
				return nil, errors.New("update_requires_edits")
			}
			if content == originals[p.Path] {
				return nil, errors.New("update_without_change")
			}
			out = append(out, domain.FileChange{Path: p.Path, Action: "update", Content: content})
		default:
			return nil, errors.New("invalid_change_action")
		}
	}
	return out, nil
}

func (run *implementRun) publish(ctx context.Context) error {
	impl := run.impl
	files := []domain.FileChange{}
	for _, c := range impl.Changes {
		f, ok := run.r.Writer.GetChange(ctx, impl.CacheID, impl.Version, c.Path)
		if !ok {
			return errors.New("implementation_changes_expired")
		}
		files = append(files, f)
	}
	if err := run.save(ctx, "GitHubにブランチとPRを作成しています"); err != nil {
		return err
	}
	req := github.PublishRequest{
		Repository: run.in.Session.Target.Repository, BaseBranch: impl.BaseBranch, BaseCommit: impl.BaseCommit,
		Branch: impl.Branch, Title: impl.Title, Body: impl.Body, Message: impl.Title,
		Files: files, Approved: impl.ApprovedPaths(), ExpectedHead: impl.ExpectedHead,
	}
	state := github.PublishState{Commit: impl.Commit, RefBranch: impl.RefBranch, RefUpdated: impl.RefUpdated, PullNumber: impl.PullNumber, PullURL: impl.PullURL}
	if impl.ExpectedHead != "" {
		// A follow-up adds a commit; the PR fields belong to the earlier cycle.
		state.PullNumber, state.PullURL = 0, ""
	}
	final, err := run.r.Writer.Publish(ctx, run.in.GitHubToken, req, state, func(s github.PublishState) error {
		impl.Commit, impl.RefBranch, impl.RefUpdated = s.Commit, s.RefBranch, s.RefUpdated
		if s.PullNumber > 0 {
			impl.PullNumber, impl.PullURL = s.PullNumber, s.PullURL
		}
		return run.save(ctx, "GitHubへの書き込みを記録しました")
	})
	if err != nil {
		return err
	}
	impl.Commit, impl.RefUpdated = final.Commit, final.RefUpdated
	if final.PullNumber > 0 {
		impl.PullNumber, impl.PullURL = final.PullNumber, final.PullURL
	}
	impl.Status = "published"
	if impl.ExpectedHead != "" {
		run.cp.Answer = fmt.Sprintf("ブランチ %s に追加の変更をコミットしました（%s）。\n%s", impl.Branch, impl.Commit[:7], impl.PullURL)
	} else {
		run.cp.Answer = fmt.Sprintf("Draft PRを作成しました: %s\n\nブランチ: %s\nCIの結果を確認してください。アプリ内ではコードやテストを実行していません。", impl.PullURL, impl.Branch)
	}
	return run.save(ctx, "PRを作成しました")
}

func defaultBranchName(sessionID string, version int) string {
	id := strings.ToLower(sessionID)
	clean := strings.Builder{}
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			clean.WriteRune(c)
		}
		if clean.Len() >= 12 {
			break
		}
	}
	if clean.Len() == 0 {
		clean.WriteString("change")
	}
	return fmt.Sprintf("ai-factory/%s-%d", clean.String(), version+1)
}

func defaultTitle(impl *domain.Implementation) string {
	title := strings.TrimSpace(strings.SplitN(impl.Plan.Summary, "\n", 2)[0])
	if title == "" {
		title = strings.TrimSpace(strings.SplitN(impl.Request, "\n", 2)[0])
	}
	return domain.Clip(title, 120)
}

func defaultBody(impl *domain.Implementation) string {
	var b strings.Builder
	b.WriteString("このPRはAIファクトリーのAIが作成しました。内容を確認してからマージしてください。\n\n## 依頼\n\n")
	b.WriteString(domain.Clip(impl.Request, 2000))
	b.WriteString("\n\n## 変更内容\n\n")
	b.WriteString(impl.Plan.Summary + "\n\n")
	for _, f := range impl.Plan.Files {
		b.WriteString(fmt.Sprintf("- `%s`（%s）: %s\n", f.Path, actionLabel(f.Action), f.Approach))
	}
	b.WriteString("\n## 検証\n\nアプリ内ではコード・ビルド・テストを実行していません。このリポジトリのCIの結果を確認してください。\n")
	if len(impl.Plan.Tests) > 0 {
		b.WriteString("\n確認してほしい点:\n")
		for _, t := range impl.Plan.Tests {
			b.WriteString("- " + t + "\n")
		}
	}
	if len(impl.Unverified) > 0 {
		b.WriteString("\n## 未確認事項\n\n")
		for _, u := range impl.Unverified {
			b.WriteString("- " + u + "\n")
		}
	}
	return domain.Clip(b.String(), 60000)
}

func actionLabel(a string) string {
	switch a {
	case "create":
		return "追加"
	case "delete":
		return "削除"
	}
	return "変更"
}

func formatAmendment(impl *domain.Implementation) string {
	var b strings.Builder
	b.WriteString("作業の途中で、計画にないファイルも変更が必要だと分かりました。追加してよければ承認してください。見送ると、これらのファイルは変更せずに残りの作業を続けます。\n\n【追加したいファイル】\n")
	for _, f := range impl.PendingFiles {
		b.WriteString(fmt.Sprintf("- %s（%s）: %s\n  理由: %s\n", f.Path, actionLabel(f.Action), f.Approach, f.Reason))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func formatPlan(impl *domain.Implementation) string {
	p := impl.Plan
	var b strings.Builder
	base := "ベース: " + impl.BaseBranch + " " + impl.BaseCommit[:7]
	if impl.ExpectedHead != "" && impl.PullNumber > 0 {
		base = fmt.Sprintf("PR #%d のブランチ %s の %s に追加", impl.PullNumber, impl.Branch, impl.BaseCommit[:7])
	}
	b.WriteString("実装計画を作成しました（" + base + "）。\n\n" + p.Summary + "\n\n【変更するファイル】\n")
	for _, f := range p.Files {
		b.WriteString(fmt.Sprintf("- %s（%s）: %s\n", f.Path, actionLabel(f.Action), f.Approach))
	}
	list := func(title string, items []string) {
		if len(items) == 0 {
			return
		}
		b.WriteString("\n【" + title + "】\n")
		for _, v := range items {
			b.WriteString("- " + v + "\n")
		}
	}
	list("対象外", p.OutOfScope)
	list("リスク", p.Risks)
	list("確認すべきテスト", p.Tests)
	b.WriteString("\n計画を承認すると変更を作成します。修正したい場合はチャットで追加の依頼を送ってください。")
	return b.String()
}

func formatChanges(impl *domain.Implementation) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%dファイルの変更を作成しました。差分を確認し、ブランチ名・PRタイトル・本文を確認してからPRを作成してください。\n\n", len(impl.Changes)))
	for _, c := range impl.Changes {
		b.WriteString(fmt.Sprintf("- %s（%s）\n", c.Path, actionLabel(c.Action)))
	}
	if len(impl.Unverified) > 0 {
		b.WriteString("\n【未確認事項】\n")
		for _, u := range impl.Unverified {
			b.WriteString("- " + u + "\n")
		}
	}
	b.WriteString("\nコードやテストは実行していません。作成後のCIで確認してください。")
	return b.String()
}
