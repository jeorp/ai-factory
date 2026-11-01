package agent

import (
	"context"
	"errors"
	"fmt"
)

const maxInvalidImplementationResponses = 3

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
	switch implementationErrorCode(err) {
	case "invalid_implementation_result", "gemini_invalid_output", "openai_invalid_output", "claude_invalid_output":
	default:
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
