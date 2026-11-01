package agent

import (
	"context"
	"errors"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

func (run *implementRun) publish(ctx context.Context) error {
	impl := run.impl
	if !impl.CanPublish() && !impl.RefUpdated && impl.RefBranch == "" {
		return errors.New("implementation_verification_incomplete")
	}
	files := []domain.FileChange{}
	for _, c := range impl.Changes {
		f, err := run.savedChange(ctx, c.Path)
		if err != nil {
			return err
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
	impl.Status = "checking_ci"
	if err := run.save(ctx, "PRを作成しました。GitHub CIの確認に進みます"); err != nil {
		return err
	}
	return run.checkPublishedCI(ctx)
}
