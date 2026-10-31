package agent

import (
	"log/slog"
	"strings"

	"ai-factory/backend/internal/domain"
	"ai-factory/backend/internal/github"
)

// The final model call is reserved for an answer, even if cached reads or
// additional instructions have used steps without consuming GitHub reads.
type chatBudget struct {
	MaxReads            int  `json:"maxReads"`
	RemainingReads      int  `json:"remainingReads"`
	MaxFiles            int  `json:"maxFiles"`
	RemainingFiles      int  `json:"remainingFiles"`
	RemainingModelCalls int  `json:"remainingModelCalls"`
	FinalAnswerOnly     bool `json:"finalAnswerOnly"`
}

func newChatBudget(step, reads, files int) chatBudget {
	b := chatBudget{
		MaxReads: domain.MaxToolCalls, RemainingReads: max(0, domain.MaxToolCalls-reads),
		MaxFiles: domain.MaxFiles, RemainingFiles: max(0, domain.MaxFiles-files),
		RemainingModelCalls: max(0, domain.MaxToolCalls+1-step),
	}
	b.FinalAnswerOnly = b.RemainingReads == 0 || b.RemainingModelCalls <= 1
	return b
}

func chatReadLogger(logger *slog.Logger, step int, read github.Read, budget chatBudget) *slog.Logger {
	return logger.With("step", step+1, "kind", read.Kind, "path", read.Path, "number", read.Number, "detail", read.Detail,
		"remaining_reads", budget.RemainingReads, "remaining_files", budget.RemainingFiles, "remaining_model_calls", budget.RemainingModelCalls)
}

// A provider that ignores the final-answer schema must not trigger another
// read or an unbounded retry. Preserve the last saved investigation summary.
func chatBudgetFallback(cp Checkpoint) string {
	var answer strings.Builder
	answer.WriteString("追加取得の上限に達したため、確認済みの範囲で調査を終了しました。")
	if strings.TrimSpace(cp.Summary) != "" {
		answer.WriteString("\n\nここまでの調査メモ（要約）:\n")
		answer.WriteString(cp.Summary)
	}
	answer.WriteString("\n\n依頼全体の確認は完了していません。追加で確認しようとした対象は未確認です。")
	return answer.String()
}
