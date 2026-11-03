package agent

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const maxInvalidImplementationResponses = 3

func invalidModelResponse(err error) bool {
	switch implementationErrorCode(err) {
	case "invalid_implementation_result", "gemini_invalid_output", "openai_invalid_output", "claude_invalid_output":
		return true
	}
	return false
}

// Retry only side-effect-free model requests. GitHub writes, authorization,
// exhausted budgets and user cancellation never enter this recovery path.
func (run *implementRun) retryTransientModel(ctx context.Context, phase string, attempt, maxAttempts int, consecutive, retries *int, err error) (bool, error) {
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	limit := transientModelRetryLimit(err)
	if limit == 0 {
		return false, nil
	}
	if *consecutive >= limit || attempt >= maxAttempts || run.calls >= ImplementMaxCalls {
		return false, nil
	}
	*consecutive = *consecutive + 1
	run.retry(phase, attempt, retries, err)
	message := fmt.Sprintf("AIとの一時的な通信エラーのため再試行します（%d/%d回）", *consecutive, limit)
	if implementationErrorCode(err) == "gemini_timeout" {
		message = "Geminiが時間切れになったため1回だけ再試行します。追加の応答待ちが発生します。全体の時間上限内で続行します。"
	}
	if err := run.save(ctx, message); err != nil {
		return false, err
	}
	if err := run.r.waitModelRetry(ctx, modelRetryDelay(*consecutive)); err != nil {
		return false, err
	}
	return true, nil
}

func (r *Runner) waitModelRetry(ctx context.Context, delay time.Duration) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r.retryWait != nil {
		if err := r.retryWait(ctx, delay); err != nil {
			return err
		}
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

func implementationResultReason(err error) string {
	var invalid *implementationResultError
	if errors.As(err, &invalid) {
		switch invalid.reason {
		case "search_action_conflict", "invalid_search", "verification_shape", "verification_file_limit", "verification_repair_invalid", "verification_text_limit", "start_implementation_shape", "plan_action_conflict", "edit_shape", "additional_files_shape", "change_shape", "edit_text_limit", "edit_format_mismatch", "unverified_text_limit", "verification_phase_mismatch", "search_not_allowed":
			return invalid.reason
		}
	}
	return "unknown"
}

// Invalid responses have no side effects. Retry with a correction, inside both
// the phase's call budget and a smaller consecutive-invalid-response budget.
func (run *implementRun) retryInvalidResponse(ctx context.Context, phase string, attempt int, invalids, retries *int, err error) (string, bool, error) {
	if !invalidModelResponse(err) {
		return "", false, nil
	}
	*invalids = *invalids + 1
	run.retry(phase, attempt, retries, err)
	if *invalids >= maxInvalidImplementationResponses || (phase == "plan" && attempt >= ImplementPlanCalls) || (phase == "edit" && attempt >= ImplementStepCalls) {
		return "", false, nil
	}
	correction := "前回の応答は形式が不正でした。使用しない操作はすべてnullにし、一度に一つの操作だけを返してください。"
	if phase == "plan" {
		correction += "readで取得する、searchで検索する、planで計画を返す、answerで利用者に質問する、のどれか一つです。read・search・planを返す場合はanswerを空にしてください。"
	} else {
		correction += "追加取得する場合はreadだけを返しimplementationはnullです。変更を返す場合はimplementationだけを返しreadはnull、answerは常に空にしてください。"
	}
	if implementationResultReason(err) == "edit_format_mismatch" {
		correction += "implementTask.editFormatに従ってください。line_rangesならedits=[]、それ以外はlineEdits=[]です。"
	}
	var invalid *implementationResultError
	if errors.As(err, &invalid) && invalid.limit != nil {
		v := invalid.limit
		correction = fmt.Sprintf("前回の変更は保存していません。changesの%d番目のファイル、編集%dの%sは%dバイトで、上限%dバイトを超えています。", v.FileIndex, v.EditIndex, v.Field, v.Bytes, v.Limit)
		switch v.Field {
		case "expectedStart", "expectedEnd":
			correction += "expectedStart/expectedEndは指定した境界の原文1行だけです。複数行や関数全体を入れず、行番号と|を除いてコピーしてください。境界の原文自体が上限を超える場合は、上限以内の非空行を境界に選んでください。"
		default:
			correction += "無関係な部分の再掲を避け、編集範囲を小さな複数の編集に分けてください。searchは原文に一意に一致させ、lineEditsは固定原文の行番号を使い範囲を重ねないでください。"
		}
		correction += "implementTask.editFormatを維持し、line_rangesならedits=[]、それ以外はlineEdits=[]にしてください。"
	}
	correction += "検証理由: " + implementationResultReason(err)
	if err := run.save(ctx, fmt.Sprintf("AIの応答形式を訂正して再試行しています（%d/%d回）", *invalids, maxInvalidImplementationResponses-1)); err != nil {
		return "", false, err
	}
	return correction, true, nil
}
