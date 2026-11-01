package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestLineEditsUseOriginalCoordinatesAndPreserveOtherBytes(t *testing.T) {
	original := "# 日本語\r\nfirst()\r\n\told()\r\nend()\r\nkeep()"
	got, err := ApplyLineEdits(original, []LineEdit{
		{StartLine: 4, EndLine: 4, ExpectedStart: "end()", ExpectedEnd: "end()", Content: "endChanged()"},
		{StartLine: 2, EndLine: 3, ExpectedStart: "first()", ExpectedEnd: "old()", Content: "first()\n\tnew()\n\textra()"},
	})
	want := "# 日本語\r\nfirst()\r\n\tnew()\r\n\textra()\r\nendChanged()\r\nkeep()"
	if err != nil || got != want {
		t.Fatalf("wrong range application: %q %v", got, err)
	}
	if NumberedSource(original) != "1|# 日本語\n2|first()\n3|\told()\n4|end()\n5|keep()\n" {
		t.Fatalf("incorrect source coordinates: %q", NumberedSource(original))
	}
}

func TestLineEditsRejectWrongAnchorsRangesAndOverlap(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		edits      []LineEdit
	}{
		{"range", "edit_line_range_invalid", []LineEdit{{StartLine: 0, EndLine: 1}}},
		{"past_eof", "edit_line_range_invalid", []LineEdit{{StartLine: 1, EndLine: 10}}},
		{"anchor", "edit_line_anchor_mismatch", []LineEdit{{StartLine: 2, EndLine: 2, ExpectedStart: "wrong", ExpectedEnd: "middle"}}},
		{"blank_anchor", "edit_line_anchor_mismatch", []LineEdit{{StartLine: 2, EndLine: 2}}},
		{"overlap", "edit_line_overlap", []LineEdit{{StartLine: 1, EndLine: 2, ExpectedStart: "start", ExpectedEnd: "middle"}, {StartLine: 2, EndLine: 3, ExpectedStart: "middle", ExpectedEnd: "end"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ApplyLineEdits("start\nmiddle\nend\n", tc.edits)
			var detail *EditApplyError
			if got != "" || !errors.As(err, &detail) || err.Error() != tc.code || detail.Index < 1 {
				t.Fatalf("unsafe edit accepted: %q %v", got, err)
			}
		})
	}
}

func TestLineEditsInsertAndDeleteWithoutJoiningAdjacentLines(t *testing.T) {
	got, err := ApplyLineEdits("head\nremove\ntail", []LineEdit{{StartLine: 2, EndLine: 2, ExpectedStart: "remove", ExpectedEnd: "remove"}})
	if err != nil || got != "head\ntail" {
		t.Fatalf("delete: %q %v", got, err)
	}
	got, err = ApplyLineEdits(got, []LineEdit{{StartLine: 1, EndLine: 1, ExpectedStart: "head", ExpectedEnd: "head", Content: "head\ninsert"}})
	if err != nil || got != "head\ninsert\ntail" {
		t.Fatalf("insert: %q %v", got, err)
	}
}

func TestExactEditFailureIdentifiesIndexWithoutRetainingSource(t *testing.T) {
	_, err := ApplyEdits("private original", []Edit{{Search: "private", Replace: "updated"}, {Search: "missing source", Replace: "new"}})
	var detail *EditApplyError
	if !errors.As(err, &detail) || detail.Index != 2 || detail.Code != "edit_search_not_found" || strings.Contains(err.Error(), "source") {
		t.Fatalf("bad diagnostic: %v", err)
	}
}
