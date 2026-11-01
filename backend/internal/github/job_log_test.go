package github

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestJobLogKeepsAllFailuresBeforeLongSuccessfulTail(t *testing.T) {
	failures := []string{"--- FAIL: TestSessionLocale", "--- FAIL: TestOriginalForCreate", "--- FAIL: TestPriorityReason", "main.go:12:2: imported and not used: fmt"}
	reasons := []string{"Session.Locale undefined", "original missing for src/b.go", "priority reason must not be empty", "remove unused import without weakening assertions"}
	var raw strings.Builder
	for i := 0; i < 787; i++ {
		if i%140 == 10 && i < 500 {
			n := i / 140
			raw.WriteString(failures[n] + "\n    " + reasons[n] + "\n")
		} else {
			fmt.Fprintf(&raw, "PASS step %d\n", i)
		}
	}
	got := sanitizeJobLog(raw.String())
	for i, f := range failures {
		if !strings.Contains(got, f) || !strings.Contains(got, reasons[i]) {
			t.Fatalf("lost failure/context %d: %s", i, got)
		}
	}
	if !strings.Contains(got, "omitted=0") || len(got) > 16*1024 {
		t.Fatalf("invalid evidence budget: %s", got)
	}
}

func TestJobLogOverflowIsExplicitAndUnicodeAndSecretsAreSafe(t *testing.T) {
	raw := strings.Repeat("\x1b[31mFAIL 日本語 token=ghp_"+strings.Repeat("x", 20)+"\x1b[0m\n", 1000)
	got := sanitizeJobLog(raw)
	if !strings.Contains(got, "[INCOMPLETE:") || !strings.Contains(got, "detected=1000") || len(got) > 16*1024 || !utf8.ValidString(got) || strings.Contains(got, "ghp_") || strings.Contains(got, "\x1b") {
		t.Fatalf("unsafe or silent truncation: %s", got)
	}
}
