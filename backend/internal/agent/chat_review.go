package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"ai-factory/backend/internal/domain"
)

// ReviewRequest selects a workflow, never a GitHub endpoint or repository.
// A zero number refers to the PR most recently retrieved in this conversation.
type ReviewRequest struct {
	Number int `json:"number"`
}

func validReviewAction(d Decision) bool {
	return d.StartReview == nil || (d.StartReview.Number >= 0 && d.StartReview.Number <= 100000000 && d.Read == nil && d.Review == nil && strings.TrimSpace(d.Answer) == "")
}

func activePullRequest(s domain.Session) int {
	if s.ActivePullRequest > 0 {
		return s.ActivePullRequest
	}
	if s.Target.Kind == "pull" {
		return s.Target.Number
	}
	// Sessions created before ActivePullRequest was introduced still have server
	// generated PR sources. Never infer the selection from a model's summary.
	for i := len(s.Sources) - 1; i >= 0; i-- {
		t, err := domain.ParseTarget(s.Sources[i].URL)
		if err == nil && t.Kind == "pull" && strings.EqualFold(t.Repository, s.Target.Repository) {
			return t.Number
		}
	}
	return 0
}

var githubChatURL = regexp.MustCompile(`https://github\.com/[^\s<>"'）)\]、。]+`)

func (r *Runner) reviewFromChat(ctx context.Context, in Input, cp Checkpoint, request ReviewRequest, emit func(Checkpoint) error) error {
	if _, ok := r.GitHub.(reviewReader); !ok {
		return errors.New("pull_request_required")
	}
	number := request.Number
	// User URLs are checked explicitly; the model can only select a PR number
	// inside the repository already authorized for this conversation.
	latest := ""
	for i := len(in.Session.Messages) - 1; i >= 0; i-- {
		if in.Session.Messages[i].Role == "user" {
			latest = in.Session.Messages[i].Text
			break
		}
	}
	explicit := map[int]bool{}
	for _, value := range githubChatURL.FindAllString(latest, -1) {
		target, err := domain.ParseTarget(strings.TrimRight(value, ".,!"))
		if err != nil || target.Kind != "pull" {
			continue
		}
		if !strings.EqualFold(target.Repository, in.Session.Target.Repository) {
			cp.Answer = "この会話の対象は " + in.Session.Target.Repository + " です。対象リポジトリのPR番号かURLを教えてください。"
			cp.Progress = "レビュー対象の確認が必要です"
			return emit(cp)
		}
		explicit[target.Number] = true
	}
	if len(explicit) == 1 {
		for selected := range explicit {
			number = selected
		}
	} else if len(explicit) > 1 && !explicit[number] {
		cp.Answer = "どのPRをレビューするか、対象のPR番号をこのチャットで教えてください。"
		cp.Progress = "レビュー対象の指定を待っています"
		return emit(cp)
	}
	if number == 0 {
		number = activePullRequest(in.Session)
	}
	if number < 1 || number > 100000000 {
		cp.Answer = "レビューするPRを教えてください。PR番号（例: PR #123）かURLを、このチャットに送ってください。"
		cp.Progress = "レビュー対象の指定を待っています"
		return emit(cp)
	}
	target := in.Session.Target
	target.Kind, target.Number = "pull", number
	cp.Target, cp.RunKind, cp.ResetReview, cp.ActivePullRequest = &target, "review", true, number
	cp.Review = nil
	cp.Progress = fmt.Sprintf("会話中のPR #%d の分割レビューを開始します", number)
	if err := emit(cp); err != nil {
		return err
	}
	if in.Logger != nil {
		in.Logger.Info("review_requested", "source", "chat", "pull_number", number)
	}
	in.Session.Target, in.Session.RunKind, in.Session.Review = target, "review", nil
	in.Session.ActivePullRequest = number
	in.Session.Summary, in.Session.Sources, in.Session.Limitations = cp.Summary, cp.Sources, cp.Limitations
	in.Session.Metrics = cp.Metrics
	in.ContinueMetrics = true
	return r.runReview(ctx, in, emit)
}
