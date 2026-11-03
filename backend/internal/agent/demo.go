package agent

import (
	"context"
	"encoding/json"
	"strings"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// Local demonstration never calls OpenAI and never needs a real API key.
type Demo struct{}

func (Demo) Next(ctx context.Context, key, model, input string) (Decision, Usage, error) {
	if documentationWorkflow(input) {
		return demoDocumentation(input), Usage{}, nil
	}
	if strings.Contains(input, `"overviewTask":`) {
		return Decision{Summary: "固定デモ: Go APIとVue 3によるGitHub読み取りチャット。"}, Usage{}, nil
	}
	if implementWorkflow(input) {
		return demoImplement(input), Usage{}, nil
	}
	if strings.Contains(input, `"reviewTask":`) {
		return Decision{Summary: "固定デモの差分を確認しました。実際のリポジトリは未確認です。", Review: &domain.ReviewResult{Findings: []domain.Finding{}, Unverified: []string{"固定デモのため、実コードの問題は判定していません。"}}}, Usage{}, nil
	}
	var chat struct {
		Target   domain.Target    `json:"target"`
		Active   int              `json:"activePullRequest"`
		History  []domain.Message `json:"recentHistory"`
		Evidence []evidence       `json:"evidence"`
	}
	if json.Unmarshal([]byte(input), &chat) == nil && len(chat.History) > 0 {
		message := ""
		for i := len(chat.History) - 1; i >= 0; i-- {
			if chat.History[i].Role == "user" {
				message = chat.History[i].Text
				break
			}
		}
		if strings.Contains(message, "実装") || strings.Contains(message, "追加して") || strings.Contains(message, "修正して") {
			return Decision{StartImplementation: &ImplementRequest{Request: message}}, Usage{}, nil
		}
		if strings.Contains(message, "レビュー") {
			return Decision{StartReview: &ReviewRequest{Number: chat.Active}}, Usage{}, nil
		}
		if strings.Contains(strings.ToUpper(message), "PR") && len(chat.Evidence) == 0 {
			return Decision{Read: &github.Read{Kind: "pull_request", Number: 1, Detail: "summary"}}, Usage{}, nil
		}
	}
	if !strings.Contains(input, `"evidence":[{`) {
		return Decision{Read: &github.Read{Kind: "repository", Detail: "summary"}}, Usage{}, nil
	}
	return Decision{Answer: "これは固定デモの回答です。実際のGitHub・OpenAIには接続していません。\n\nサンプルは、Vue 3の画面からWebSocketでGo APIに接続し、GitHub MCPで必要な情報を取得する構成です。取得結果は会話履歴と分けて一時保持し、呼び出し回数と入力サイズに上限を設けています。\n\n実データを確認するには、GitHub AppとOpenAIの設定を行ってください。", Summary: "固定デモの構成を確認。実リポジトリは未確認。"}, Usage{}, nil
}
