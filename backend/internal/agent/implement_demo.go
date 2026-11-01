package agent

import (
	"encoding/json"
	"strings"

	"ai-factory/backend/internal/domain"
)

// demoImplement plans and applies a fixed README change without calling an AI.
func demoImplement(input string) Decision {
	var body struct {
		Task implementTask `json:"implementTask"`
	}
	json.Unmarshal([]byte(input), &body)
	intent := IntentPlan{Goal: "デモの実装", Investigation: "固定の変更", Response: "差分を提示"}
	if body.Task.Mode == "verify" {
		return Decision{Intent: intent, Summary: "固定デモの整合性確認", Verification: &domain.ImplementationVerification{Issues: []string{}, Unverified: []string{"固定デモのため実際の整合性は確認していません。"}}}
	}
	if body.Task.Mode == "plan" {
		return Decision{Intent: intent, Summary: "固定デモの計画", Plan: &domain.ImplementationPlan{
			AcceptanceCriteria: []domain.AcceptanceCriterion{{ID: "demo", Requirement: "READMEに固定デモの説明を追加", Paths: []string{"README.md"}, Checks: []string{"追加行と既存の本文を確認"}}},
			Summary:            "READMEにデモの説明を追加\n固定デモの変更です。実際のAIは呼び出していません。",
			Files:              []domain.PlannedFile{{Path: "README.md", Action: "update", Approach: "先頭の見出しの後にデモの説明を1行追加する"}},
			Tests:              []string{"README の表示を確認する"},
		}}
	}
	result := &ImplementResult{Unverified: []string{"固定デモのため、依頼内容は反映していません。"}}
	for _, f := range body.Task.Files {
		first := strings.SplitN(f.Original, "\n", 2)[0]
		if f.Action == "update" && first != "" && strings.Count(f.Original, first) == 1 {
			result.Changes = append(result.Changes, ChangeProposal{Path: f.Path, Action: "update", Edits: []domain.Edit{{Search: first, Replace: first + "\n\nこの行はAIファクトリーの固定デモで追加しました。"}}})
		}
	}
	return Decision{Intent: intent, Summary: "固定デモの変更を作成", Implementation: result}
}
