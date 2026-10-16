// Package udiff renders two texts as one unified diff by line, as
// diff -u does: "--- was" / "+++ is", one hunk to a change with three
// lines round it. It is the rendering a history and a run-ask card
// share, so a change is shown the same way wherever it is approved.
package udiff

import (
	"fmt"
	"strings"
)

// Context is how many unchanged lines a hunk shows around a change.
const Context = 3

// cells bounds the table Unified compares two texts in. Past it the
// lines that differ are written as all removed and all added: still
// what changed, in more lines.
const cells = 4 << 20

// Unified is old and new as a unified diff by line, "" when no line
// differs. A final newline is no line of its own.
func Unified(old, new string) string {
	a, b := lines(old), lines(new)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	ma, mb := a[pre:len(a)-suf], b[pre:len(b)-suf]
	if len(ma) == 0 && len(mb) == 0 {
		return ""
	}
	type op struct {
		kind byte
		text string
	}
	var ops []op
	for _, l := range a[:pre] {
		ops = append(ops, op{' ', l})
	}
	if len(ma)*len(mb) > cells {
		for _, l := range ma {
			ops = append(ops, op{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, op{'+', l})
		}
	} else {
		lcs := make([][]int32, len(ma)+1)
		for i := range lcs {
			lcs[i] = make([]int32, len(mb)+1)
		}
		for i := len(ma) - 1; i >= 0; i-- {
			for j := len(mb) - 1; j >= 0; j-- {
				if ma[i] == mb[j] {
					lcs[i][j] = lcs[i+1][j+1] + 1
				} else {
					lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(ma) || j < len(mb) {
			switch {
			case i < len(ma) && j < len(mb) && ma[i] == mb[j]:
				ops = append(ops, op{' ', ma[i]})
				i, j = i+1, j+1
			case i < len(ma) && (j == len(mb) || lcs[i+1][j] >= lcs[i][j+1]):
				ops = append(ops, op{'-', ma[i]})
				i++
			default:
				ops = append(ops, op{'+', mb[j]})
				j++
			}
		}
	}
	for _, l := range a[len(a)-suf:] {
		ops = append(ops, op{' ', l})
	}

	var out strings.Builder
	out.WriteString("--- was\n+++ is\n")
	at, la, lb := 0, 0, 0
	for at < len(ops) {
		if ops[at].kind == ' ' {
			at, la, lb = at+1, la+1, lb+1
			continue
		}
		start := max(at-Context, 0)
		end, quiet := at, 0
		for k := at; k < len(ops) && quiet <= 2*Context; k++ {
			if ops[k].kind == ' ' {
				quiet++
			} else {
				end, quiet = k, 0
			}
		}
		end = min(end+Context, len(ops)-1)
		sa, sb := la-(at-start), lb-(at-start)
		na, nb := 0, 0
		var hunk strings.Builder
		for _, o := range ops[start : end+1] {
			if o.kind != '+' {
				na++
			}
			if o.kind != '-' {
				nb++
			}
			hunk.WriteByte(o.kind)
			hunk.WriteString(o.text)
			hunk.WriteByte('\n')
		}
		fmt.Fprintf(&out, "@@ -%s +%s @@\n%s", span(sa, na), span(sb, nb), hunk.String())
		la, lb, at = sa+na, sb+nb, end+1
	}
	return out.String()
}

// Counts are a diff's insertions and deletions, by line.
func Counts(diff string) (ins, del int) {
	lines := strings.Split(diff, "\n")
	// The first two lines are the file headers. Skip by position instead
	// of by prefix: a changed source line can itself start with "++" or
	// "--" and still counts as one insertion or deletion.
	for i, l := range lines {
		if i < 2 {
			continue
		}
		switch {
		case strings.HasPrefix(l, "+"):
			ins++
		case strings.HasPrefix(l, "-"):
			del++
		}
	}
	return ins, del
}

func span(before, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", before)
	}
	return fmt.Sprintf("%d,%d", before+1, n)
}

func lines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
