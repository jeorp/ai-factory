package domain

import (
	"fmt"
	"strings"
)

// maxDiffCells bounds the line LCS table; larger inputs fall back to trimming
// the common prefix and suffix, which still shows one changed region.
const maxDiffCells = 4_000_000

// CompactDiff returns a unified-style line diff with two lines of context,
// clipped to max bytes. It lets later implementation steps see what earlier
// steps changed without sending whole files.
func CompactDiff(before, after string, max int) string {
	a, b := splitLines(before), splitLines(after)
	ops := lineOps(a, b)
	keep := make([]bool, len(ops))
	for i, op := range ops {
		if op.kind != ' ' {
			for j := i - 2; j <= i+2; j++ {
				if j >= 0 && j < len(ops) {
					keep[j] = true
				}
			}
		}
	}
	var sb strings.Builder
	gap := false
	for i, op := range ops {
		if !keep[i] {
			gap = true
			continue
		}
		if gap && sb.Len() > 0 {
			sb.WriteString("...\n")
		}
		gap = false
		fmt.Fprintf(&sb, "%c%s\n", op.kind, op.text)
	}
	return Clip(sb.String(), max)
}

type lineOp struct {
	kind byte
	text string
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

func lineOps(a, b []string) []lineOp {
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ops := []lineOp{}
	for _, l := range a[:pre] {
		ops = append(ops, lineOp{' ', l})
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if (len(ma)+1)*(len(mb)+1) > maxDiffCells {
		for _, l := range ma {
			ops = append(ops, lineOp{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, lineOp{'+', l})
		}
	} else {
		ops = append(ops, lcsOps(ma, mb)...)
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, lineOp{' ', l})
	}
	return ops
}

func lcsOps(a, b []string) []lineOp {
	n, m := len(a), len(b)
	t := make([][]int32, n+1)
	for i := range t {
		t[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				t[i][j] = t[i+1][j+1] + 1
			} else if t[i+1][j] >= t[i][j+1] {
				t[i][j] = t[i+1][j]
			} else {
				t[i][j] = t[i][j+1]
			}
		}
	}
	ops := []lineOp{}
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && a[i] == b[j]:
			ops = append(ops, lineOp{' ', a[i]})
			i, j = i+1, j+1
		case i < n && (j == m || t[i+1][j] >= t[i][j+1]):
			ops = append(ops, lineOp{'-', a[i]})
			i++
		default:
			ops = append(ops, lineOp{'+', b[j]})
			j++
		}
	}
	return ops
}
