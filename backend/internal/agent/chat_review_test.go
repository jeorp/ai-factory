package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

type chatReviewFixture struct{ *reviewFixture }

func (f *chatReviewFixture) Read(ctx context.Context, token string, target domain.Target, read github.Read) (string, domain.Source, int, error) {
	if read.Kind == "pull_request" {
		return `{"title":"API update"}`, domain.Source{Label: "PR", URL: fmt.Sprintf("https://github.com/%s/pull/%d", target.Repository, read.Number)}, 22, nil
	}
	return f.reviewFixture.Read(ctx, token, target, read)
}

func TestRetrievedPRIsRememberedAndCanBeReviewedInNextChatMessage(t *testing.T) {
	f := &chatReviewFixture{fixture()}
	s := reviewSession()
	s.RunKind = "chat"
	s.Target.Kind = "repository"
	s.Target.Number = 0
	reads := 0
	r := Runner{GitHub: f, AI: providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
		var body struct {
			Active   int         `json:"activePullRequest"`
			Evidence []evidence  `json:"evidence"`
			Task     *reviewTask `json:"reviewTask"`
		}
		if err := json.Unmarshal([]byte(input), &body); err != nil {
			t.Fatal(err)
		}
		if body.Task != nil {
			return result(), Usage{InputTokens: 5}, nil
		}
		if len(body.Evidence) == 0 && reads == 0 {
			reads++
			return Decision{Read: &github.Read{Kind: "pull_request", Number: 42, Detail: "summary"}}, Usage{InputTokens: 7}, nil
		}
		if body.Active != 42 {
			t.Fatalf("retrieved PR lost from next decision: %s", input)
		}
		return Decision{Answer: "PR #42 を取得しました。"}, Usage{InputTokens: 3}, nil
	})}
	var cp Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(c Checkpoint) error { cp = c; return nil }); err != nil {
		t.Fatal(err)
	}
	if cp.ActivePullRequest != 42 {
		t.Fatal("PR selection was not checkpointed")
	}
	s.ActivePullRequest = cp.ActivePullRequest
	s.Sources = cp.Sources
	s.Metrics = cp.Metrics
	s.Messages = append(s.Messages, domain.Message{Role: "user", Text: "このPRをレビューして"})
	routed := false
	r.AI = providerFunc(func(_ context.Context, _ string, _ string, input string) (Decision, Usage, error) {
		if strings.Contains(input, `"priorityTask":`) {
			return Decision{Answer: `[]`}, Usage{}, nil
		}
		if strings.Contains(input, `"reviewTask":`) {
			return result(), Usage{InputTokens: 5}, nil
		}
		if !strings.Contains(input, `"activePullRequest":42`) {
			t.Fatal("selected PR missing on new turn")
		}
		return Decision{StartReview: &ReviewRequest{}}, Usage{InputTokens: 11}, nil
	})
	if err := r.Run(context.Background(), Input{Session: s}, func(c Checkpoint) error {
		if c.RunKind == "review" {
			routed = c.Target != nil && c.Target.Number == 42 && c.ResetReview
		}
		cp = c
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !routed || cp.Review == nil || cp.Review.Phase != "done" || cp.Answer == "" {
		t.Fatal("chat did not launch the full review")
	}
	if cp.Metrics.LastInputTokens != 31 || cp.Metrics.InputTokens != 41 {
		t.Fatalf("routing/review usage lost: %+v", cp.Metrics)
	}
}

func TestActivePRSurvivesLegacySessionsAndOtherReads(t *testing.T) {
	s := reviewSession()
	s.Target.Kind = "repository"
	s.Target.Number = 0
	s.Sources = []domain.Source{{URL: "https://github.com/o/r/pull/41"}, {URL: "https://github.com/other/repo/pull/99"}, {URL: "https://github.com/o/r/blob/" + s.Target.Commit + "/a.go"}}
	if activePullRequest(s) != 41 {
		t.Fatal("legacy PR source not recovered")
	}
	s.ActivePullRequest = 42
	if activePullRequest(s) != 42 {
		t.Fatal("explicit latest read should take precedence")
	}
}

func TestChatReviewMissingTargetAsksInSameConversation(t *testing.T) {
	s := reviewSession()
	s.RunKind = "chat"
	s.Target.Kind = "repository"
	s.Target.Number = 0
	f := fixture()
	r := Runner{GitHub: f, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		return Decision{StartReview: &ReviewRequest{}}, Usage{}, nil
	})}
	var cp Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(c Checkpoint) error { cp = c; return nil }); err != nil {
		t.Fatal(err)
	}
	if cp.RunKind != "" || cp.Target != nil || cp.Review != nil || !strings.Contains(cp.Answer, "このチャット") || len(f.filePages) != 0 {
		t.Fatal("unknown target was guessed or required a new session")
	}
}

func TestChatReviewPinsRepositoryAndHonorsCheckpointFailure(t *testing.T) {
	for _, crossRepo := range []bool{true, false} {
		t.Run(map[bool]string{true: "scope", false: "checkpoint"}[crossRepo], func(t *testing.T) {
			s := reviewSession()
			s.RunKind = "chat"
			s.Target.Kind = "repository"
			s.Target.Number = 0
			s.Messages = []domain.Message{{Role: "user", Text: "PR #42 をレビューして"}}
			if crossRepo {
				s.Messages[0].Text = "https://github.com/other/repo/pull/42 をレビューして"
			}
			f := fixture()
			r := Runner{GitHub: f, AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
				return Decision{StartReview: &ReviewRequest{Number: 42}}, Usage{}, nil
			})}
			var cp Checkpoint
			blocked := errors.New("save failed")
			err := r.Run(context.Background(), Input{Session: s}, func(c Checkpoint) error {
				cp = c
				if c.RunKind == "review" {
					return blocked
				}
				return nil
			})
			if crossRepo {
				if err != nil || cp.Target != nil || !strings.Contains(cp.Answer, "o/r") {
					t.Fatalf("repository changed without authorization: %v %+v", err, cp)
				}
			} else if !errors.Is(err, blocked) {
				t.Fatal("continued after transition save failure")
			}
			if len(f.filePages) != 0 {
				t.Fatal("review started without durable transition")
			}
		})
	}
}

func TestReviewActionsRejectConflictingOrInvalidRequests(t *testing.T) {
	for _, d := range []Decision{{StartReview: &ReviewRequest{Number: -1}}, {StartReview: &ReviewRequest{Number: 100000001}}, {StartReview: &ReviewRequest{Number: 1}, Read: &github.Read{Kind: "repository"}}, {StartReview: &ReviewRequest{Number: 1}, Review: &domain.ReviewResult{}}} {
		if validReviewAction(d) {
			t.Fatalf("invalid action accepted: %+v", d)
		}
	}
}

// PR selection is derived from successful tool observations, never failed reads.
func TestFailedPRReadDoesNotReplaceActivePR(t *testing.T) {
	s := baseSession()
	s.ActivePullRequest = 3
	calls := 0
	r := Runner{GitHub: readerFunc(func(context.Context, string, domain.Target, github.Read) (string, domain.Source, int, error) {
		return "", domain.Source{}, 0, errors.New("forbidden")
	}), AI: providerFunc(func(context.Context, string, string, string) (Decision, Usage, error) {
		calls++
		if calls == 1 {
			return Decision{Read: &github.Read{Kind: "pull_request", Number: 4}}, Usage{}, nil
		}
		return Decision{Answer: "未取得"}, Usage{}, nil
	})}
	var cp Checkpoint
	if err := r.Run(context.Background(), Input{Session: s}, func(c Checkpoint) error { cp = c; return nil }); err != nil {
		t.Fatal(err)
	}
	if cp.ActivePullRequest != 0 {
		t.Fatal("failed read changed the persisted selection")
	}
}
