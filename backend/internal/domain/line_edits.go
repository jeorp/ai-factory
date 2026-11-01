package domain

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
)

// LineEdit addresses an inclusive range in a single, hash-pinned source
// snapshot. Anchors check its first and last non-empty source lines. No fuzzy
// search is performed, and bytes outside the selected range are preserved.
type LineEdit struct {
	StartLine     int    `json:"startLine"`
	EndLine       int    `json:"endLine"`
	ExpectedStart string `json:"expectedStart"`
	ExpectedEnd   string `json:"expectedEnd"`
	Content       string `json:"content"`
}

func SourceHash(content string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(content))) }

func sourceLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// NumberedSource is a display for the model, never an editable source body.
func NumberedSource(content string) string {
	var out strings.Builder
	for i, line := range sourceLines(content) {
		fmt.Fprintf(&out, "%d|%s\n", i+1, strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"))
	}
	return out.String()
}

func ApplyLineEdits(content string, edits []LineEdit) (string, error) {
	if len(edits) == 0 {
		return "", &EditApplyError{Code: "edit_empty"}
	}
	lines := sourceLines(content)
	offsets := make([]int, len(lines)+1)
	for i, line := range lines {
		offsets[i+1] = offsets[i] + len(line)
	}
	type replacement struct {
		start, end, index int
		content           string
	}
	replacements := make([]replacement, 0, len(edits))
	for i, edit := range edits {
		reject := func(code string) (string, error) { return "", &EditApplyError{Code: code, Index: i + 1} }
		if edit.StartLine < 1 || edit.EndLine < edit.StartLine || edit.EndLine > len(lines) {
			return reject("edit_line_range_invalid")
		}
		start, end := lines[edit.StartLine-1], lines[edit.EndLine-1]
		if strings.TrimSpace(edit.ExpectedStart) == "" || strings.TrimSpace(edit.ExpectedEnd) == "" || strings.ContainsAny(edit.ExpectedStart+edit.ExpectedEnd, "\r\n") || strings.TrimSpace(start) != strings.TrimSpace(edit.ExpectedStart) || strings.TrimSpace(end) != strings.TrimSpace(edit.ExpectedEnd) {
			return reject("edit_line_anchor_mismatch")
		}
		text := edit.Content
		if strings.HasSuffix(end, "\n") && text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		// Preserve the newline convention within the replaced source range.
		if strings.HasSuffix(end, "\r\n") {
			text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r\n")
		}
		replacements = append(replacements, replacement{offsets[edit.StartLine-1], offsets[edit.EndLine], i + 1, text})
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start < replacements[j].start })
	for i := 1; i < len(replacements); i++ {
		if replacements[i].start < replacements[i-1].end {
			return "", &EditApplyError{Code: "edit_line_overlap", Index: replacements[i].index}
		}
	}
	// All coordinates refer to the original snapshot, so apply from the end.
	for i := len(replacements) - 1; i >= 0; i-- {
		r := replacements[i]
		content = content[:r.start] + r.content + content[r.end:]
	}
	return content, nil
}
