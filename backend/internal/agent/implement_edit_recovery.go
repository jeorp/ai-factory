package agent

import (
	"errors"
	"fmt"

	"ai-factory/backend/internal/domain"
)

const lineRangeFormat = "line_ranges"

func editTaskFiles(files []implementFile, format string) []implementFile {
	if format != lineRangeFormat {
		return files
	}
	out := append([]implementFile{}, files...)
	for i := range out {
		if out[i].Action != "update" {
			continue
		}
		out[i].SourceHash = domain.SourceHash(out[i].Original)
		out[i].NumberedOriginal = domain.NumberedSource(out[i].Original)
		out[i].Original = "" // do not duplicate the entire source in the request
	}
	return out
}

func (run *implementRun) editFailureCorrection(step []string, err error, format *string) string {
	code := implementationErrorCode(err)
	if code == "edit_search_not_found" || code == "edit_search_ambiguous" {
		*format = lineRangeFormat
	}
	var proposal *proposalEditError
	var edit *domain.EditApplyError
	fileIndex, editIndex, matches := 0, 0, 0
	location := ""
	if errors.As(err, &proposal) {
		for i, path := range step {
			if path == proposal.path {
				fileIndex = i + 1
				break
			}
		}
		location = proposal.path
	}
	if errors.As(err, &edit) {
		editIndex, matches = edit.Index, edit.Matches
	}
	run.logger.Info("implement_edit_rejected", "step", run.impl.NextStep+1, "file_index", fileIndex, "edit_index", editIndex, "match_count", matches, "error_code", code, "next_edit_format", *format)
	correction := fmt.Sprintf("前回の変更は保存していません。適用失敗: %s（%s、編集%d）。", code, location, editIndex)
	if *format == lineRangeFormat {
		return correction + "検索文字列の書き直しはやめ、更新ファイルはnumberedOriginalを参照してlineEditsで返してください。startLine/endLineは原文の行番号、expectedStart/expectedEndはその行の本文（番号と|を除く）です。sourceHashはfilesの値をそのままコピーし、editsは空配列・contentは空文字にします。すべての範囲はこの原文に対する番号で、重ねません。変更不要の部分は保持し、置換する範囲だけのcontentを書いてください。"
	}
	return correction + "searchはoriginalに一意に一致する原文をそのまま指定し、計画どおりのpathとactionだけを返してください。計画外のファイルも変更が必要ならadditionalFilesで提案してください。"
}
