package agent

import (
	"context"
	"fmt"
)

type documentationRecovery struct {
	transientErrors  int
	invalidResponses int
	correction       string
}

const documentationCorrection = "前回の応答は形式が不正でした。answerは空文字、使用しない操作はnullです。追加取得するならreadだけ、資料を完成するならread=nullとしてdocumentにtitle・summary・sections・unverifiedを返してください。finalAnswerOnly=trueなら追加取得は禁止です。本文・節数・文字数の制限を守り、readとdocumentを両方nullにしないでください。"

func (r *Runner) recoverDocumentation(ctx context.Context, recovery *documentationRecovery, err error, save func(string) error) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if limit := transientModelRetryLimit(err); limit > 0 && recovery.transientErrors < limit {
		recovery.transientErrors++
		if err := save(fmt.Sprintf("AIとの一時的な通信エラーのため再試行します（%d/%d回）", recovery.transientErrors, limit)); err != nil {
			return false, err
		}
		if err := r.waitModelRetry(ctx, modelRetryDelay(recovery.transientErrors)); err != nil {
			return false, err
		}
		return true, nil
	}
	if invalidDocumentationResponse(err) && recovery.invalidResponses < 2 {
		recovery.invalidResponses++
		// Replace one field rather than accumulating correction text across retries.
		recovery.correction = documentationCorrection
		if err := save(fmt.Sprintf("資料の応答形式を訂正して再試行しています（%d/2回）", recovery.invalidResponses)); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, err
}

func invalidDocumentationResponse(err error) bool {
	if invalidModelResponse(err) {
		return true
	}
	switch implementationErrorCode(err) {
	case "invalid_documentation_result", "gemini_incomplete", "openai_incomplete", "claude_incomplete",
		"gemini_answer_too_long", "openai_output_limit", "claude_answer_too_long",
		"read_tool_not_allowed", "invalid_file_path", "invalid_number", "invalid_read_detail":
		return true
	}
	return false
}
