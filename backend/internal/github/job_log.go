package github

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"ai-factory/backend/internal/domain"
)

// Match diagnostics throughout the log, including Go, JS, TypeScript, Python,
// compilers and GitHub annotations. Context preserves assertion explanations.
var jobDiagnostic = regexp.MustCompile(`(?i)--- FAIL:|\bFAIL(?:ED|URE)?\b|\berror\b|\bpanic:|\bfatal\b|\bexception\b|\bassertion\w*\b|\bexpected\b|\bundefined:|\bnot defined\b|\bdeclared and not used\b|\bimported and not used\b|\berror TS\d+|[×✕❯]|失敗|エラー`)
var jobANSI = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

// A fixed input budget must never silently drop failures. Keep a compact index
// of ALL matching lines first, then spend the remaining space on their context.
// Overflow and download limits are explicit, so callers cannot claim coverage.
func sanitizeJobLog(raw string) string {
	const maxBytes = 16 * 1024
	raw = domain.MaskSecrets(jobANSI.ReplaceAllString(strings.ToValidUTF8(raw, ""), ""))
	lines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
	hits := []int{}
	for i, line := range lines {
		if jobDiagnostic.MatchString(line) {
			hits = append(hits, i)
		}
	}
	if len(hits) == 0 {
		// Unknown tool output: retain a bounded tail, explicitly without a
		// diagnostic claim. Some tools report only a nonzero exit status.
		start := max(0, len(lines)-120)
		text := strings.Join(lines[start:], "\n")
		if len(text) > maxBytes-256 {
			text = strings.ToValidUTF8(text[len(text)-(maxBytes-256):], "")
		}
		return "[No recognized failure diagnostics; bounded log tail follows]\n" + text
	}
	var index strings.Builder
	kept := 0
	for _, i := range hits {
		line := fmt.Sprintf("[failure L%d] %s\n", i+1, domain.Clip(lines[i], 384))
		if index.Len()+len(line) > 10*1024 {
			break
		}
		index.WriteString(line)
		kept++
	}
	header := fmt.Sprintf("[CI diagnostics: detected=%d included=%d omitted=%d; source lines=%d]\n", len(hits), kept, len(hits)-kept, len(lines))
	if kept < len(hits) {
		header = "[INCOMPLETE: failure index exceeds input budget]\n" + header
	}
	// Give each failure a share before adding more distant context. This
	// prevents the first verbose failure from hiding the remaining failures.
	selected := map[int]string{}
	remaining := maxBytes - len(header) - index.Len() - 64
	for distance := 0; distance <= 5; distance++ {
		for _, i := range hits[:kept] {
			for _, j := range []int{i - distance, i + distance} {
				if j < 0 || j >= len(lines) || selected[j] != "" {
					continue
				}
				line := fmt.Sprintf("L%d: %s\n", j+1, domain.Clip(lines[j], 512))
				if len(line) > remaining {
					continue
				}
				selected[j] = line
				remaining -= len(line)
			}
		}
	}
	positions := make([]int, 0, len(selected))
	for i := range selected {
		positions = append(positions, i)
	}
	sort.Ints(positions)
	var context strings.Builder
	for _, i := range positions {
		context.WriteString(selected[i])
	}
	return header + index.String() + "[Failure context; gaps are omitted]\n" + context.String()
}
