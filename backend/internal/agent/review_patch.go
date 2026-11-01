package agent

import (
	"bufio"
	"strconv"
	"strings"

	"ai-factory/backend/internal/domain"
)

func hunk(line string) (int, int, bool) {
	var l, r int
	parts := strings.Fields(line)
	if len(parts) < 4 || parts[0] != "@@" || !strings.HasPrefix(parts[1], "-") || !strings.HasPrefix(parts[2], "+") {
		return 0, 0, false
	}
	l, e1 := strconv.Atoi(strings.Split(parts[1][1:], ",")[0])
	r, e2 := strconv.Atoi(strings.Split(parts[2][1:], ",")[0])
	return l, r, e1 == nil && e2 == nil
}
func patchCursor(prefix string) (int, int) {
	l, r := 1, 1
	scanner := bufio.NewScanner(strings.NewReader(prefix))
	scanner.Buffer(make([]byte, 1024), domain.MaxFetchBytes)
	for scanner.Scan() {
		line := scanner.Text()
		if a, b, ok := hunk(line); ok {
			l, r = a, b
			continue
		}
		if strings.HasPrefix(line, "-") {
			l++
		} else if strings.HasPrefix(line, "+") {
			r++
		} else if strings.HasPrefix(line, " ") {
			l++
			r++
		}
	}
	return l, r
}
func patchLocation(patch string, f domain.Finding) bool {
	if f.Line == 0 {
		return true
	}
	l, r := 1, 1
	for _, line := range strings.Split(patch, "\n") {
		if a, b, ok := hunk(line); ok {
			l, r = a, b
			continue
		}
		if strings.HasPrefix(line, "-") {
			if f.Side == "LEFT" && f.Line == l {
				return true
			}
			l++
		} else if strings.HasPrefix(line, "+") {
			if f.Side == "RIGHT" && f.Line == r {
				return true
			}
			r++
		} else if strings.HasPrefix(line, " ") {
			if f.Side == "LEFT" && f.Line == l || f.Side == "RIGHT" && f.Line == r {
				return true
			}
			l++
			r++
		}
	}
	return false
}
func completePatch(patch string) bool {
	wantL, wantR, gotL, gotR := 0, 0, 0, 0
	seen := false
	for _, line := range strings.Split(patch, "\n") {
		if _, _, ok := hunk(line); ok {
			if seen && (wantL != gotL || wantR != gotR) {
				return false
			}
			parts := strings.Fields(line)
			parse := func(s string) int {
				v := strings.Split(s, ",")
				if len(v) == 1 {
					return 1
				}
				n, _ := strconv.Atoi(v[1])
				return n
			}
			wantL, wantR = parse(parts[1]), parse(parts[2])
			gotL, gotR = 0, 0
			seen = true
		} else if strings.HasPrefix(line, "-") {
			gotL++
		} else if strings.HasPrefix(line, "+") {
			gotR++
		} else if strings.HasPrefix(line, " ") {
			gotL++
			gotR++
		}
	}
	return seen && wantL == gotL && wantR == gotR
}
