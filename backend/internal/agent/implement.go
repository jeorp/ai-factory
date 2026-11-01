package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"ai-factory/backend/internal/domain"
)

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
	if v.DraftSource != nil {
		source := *v.DraftSource
		out.DraftSource = &source
	}
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
	r                          *Runner
	in                         Input
	emit                       func(Checkpoint) error
	cp                         Checkpoint
	impl                       *domain.Implementation
	logger                     *slog.Logger
	instructions               []domain.Message
	calls                      int
	ciEvidence                 []evidence // pinned failure evidence, never evicted with optional reads
	projectDocumentation       *projectDocumentation
	projectDocumentationLoaded bool
}

func (r *Runner) runImplementation(ctx context.Context, in Input, emit func(Checkpoint) error) (runErr error) {
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
	defer func() {
		run.logger.Info("implement_run_finished", "phase", impl.Status, "step", impl.NextStep+1, "model_calls", run.calls, "outcome", outcome(runErr), "error_code", implementationErrorCode(runErr))
	}()
	run.cp = Checkpoint{Summary: in.Session.Summary, Sources: append([]domain.Source{}, in.Session.Sources...), Limitations: append([]string{}, in.Session.Limitations...), Metrics: in.Session.Metrics, Implementation: impl}
	// Earlier failed attempts are recorded in logs; do not keep showing those
	// transient failures as current limitations after a fresh resume.
	limits := []string{}
	for _, limit := range run.cp.Limitations {
		switch limit {
		case "invalid_implementation_result", "implementation_edit_failed", "implementation_edit_blocked", "implementation_ci_forbidden", "implementation_ci_unavailable", "implementation_plan_failed", "implementation_verification_failed":
		default:
			limits = append(limits, limit)
		}
	}
	run.cp.Limitations = limits
	if !in.ContinueMetrics {
		run.cp.Metrics.LastInputTokens, run.cp.Metrics.LastOutputTokens, run.cp.Metrics.HasLastUsage = 0, 0, false
	}
	for _, m := range in.Session.Messages {
		if m.Role == "instruction" {
			run.instructions = append(run.instructions, m)
		}
	}
	if err := run.prepareDraft(ctx); err != nil {
		return err
	}
	switch impl.Status {
	case "planning":
		return run.plan(ctx)
	case "editing":
		return run.edit(ctx)
	case "verifying", "verification_blocked":
		impl.Status = "verifying"
		return run.verify(ctx)
	case "publishing":
		return run.publish(ctx)
	case "checking_ci":
		return run.checkPublishedCI(ctx)
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

func (run *implementRun) ready(ctx context.Context) error {
	impl := run.impl
	if !impl.CanPublish() {
		return errors.New("implementation_verification_incomplete")
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
