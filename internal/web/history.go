package web

import (
	"context"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"

	"yardr.dev/yardr/internal/store"
)

// Change is one event of a bead's history: the event as the feed words it,
// and under an update that changed the body, what it changed.
type Change struct {
	Entry
	// Diff is the body before the update and after it as a unified diff,
	// "" for an event that changed no body, and for an update from before
	// events kept what the body was.
	Diff string `json:"diff,omitempty"`
}

// everyEvent asks the store for all of a bead's events it retains.
const everyEvent = math.MaxInt32

// History is a bead's history, the oldest event first: every event of the
// bead the log still has. It is what `yardr bead history` prints and what
// the bead's page shows, so the two say the same. Nothing but the log keeps
// what a bead was, so the history starts where pruning left the log
// (store.Prune).
func History(ctx context.Context, st store.API, bead string) ([]Change, error) {
	b, err := st.GetBead(ctx, bead)
	if err != nil {
		return nil, err
	}
	var evs []store.Event
	// The newest update's diff ends at the body the bead has now, so the
	// bead and its events must be of one moment: read again when the bead
	// changed while its events were read.
	for range 3 {
		if evs, err = st.Events(ctx, b.ID, everyEvent); err != nil {
			return nil, err
		}
		now, err := st.GetBead(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		same := now.Revision == b.Revision
		if b = now; same {
			break
		}
	}
	rd := readerOf(st, evs)
	rd.titles[b.ID], rd.byBead[b.ID] = b.Title, evs
	out := make([]Change, len(evs))
	// From the newest back: an update says what the body was, and what it
	// became is what the next update of the body says it was, or the bead.
	body, known := b.Body, true
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		out[i] = Change{Entry: rd.entry(ctx, e)}
		if _, changed := e.Data["body"]; e.Kind == "updated" && changed {
			was, _ := e.Data["was"].(map[string]any)
			old, ok := was["body"].(string)
			if ok && known {
				out[i].Diff = unified(old, body)
			}
			body, known = old, ok
		}
	}
	return out, nil
}

// updated says what an update changed, a field after the other: "title
// "a" → "b" · priority 2 → 1 · body". A field set to what it already was is
// left out; the body is named alone, its diff being the history's to show.
// An update from before events kept what a field was says what it became.
func updated(data map[string]any) string {
	was, _ := data["was"].(map[string]any)
	word := func(k string, v any) string {
		s := fmt.Sprint(v)
		switch {
		case v == nil || s == "":
			return "none"
		case k == "title":
			return strconv.Quote(s)
		}
		return s
	}
	var parts []string
	for _, k := range []string{"title", "type", "priority", "depot"} {
		v, set := data[k]
		if !set {
			continue
		}
		if old, had := was[k]; !had {
			parts = append(parts, k+" "+word(k, v))
		} else if word(k, old) != word(k, v) {
			parts = append(parts, k+" "+word(k, old)+" → "+word(k, v))
		}
	}
	var labels []string
	for _, l := range list(data["labels_added"]) {
		labels = append(labels, "+"+l)
	}
	for _, l := range list(data["labels_removed"]) {
		labels = append(labels, "-"+l)
	}
	if len(labels) > 0 {
		parts = append(parts, "labels "+strings.Join(labels, " "))
	}
	if meta, _ := data["metadata"].(map[string]any); len(meta) > 0 {
		before, _ := was["metadata"].(map[string]any)
		for _, k := range slices.Sorted(maps.Keys(meta)) {
			if old := before[k]; old == nil {
				parts = append(parts, fmt.Sprintf("metadata %s=%v", k, meta[k]))
			} else if fmt.Sprint(old) != fmt.Sprint(meta[k]) {
				parts = append(parts, fmt.Sprintf("metadata %s %v → %v", k, old, meta[k]))
			}
		}
	}
	if _, body := data["body"]; body {
		parts = append(parts, "body")
	}
	return strings.Join(parts, " · ")
}

// list reads a list of names from an event's data: a []string in an event
// straight from the store, a []any in one that crossed JSON.
func list(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, e := range l {
			out = append(out, fmt.Sprint(e))
		}
		return out
	}
	return nil
}

// diffContext is how many unchanged lines a hunk shows around a change, as
// diff -u does.
const diffContext = 3

// diffCells bounds the table unified compares two texts in. Past it the
// lines that differ are written as all removed and all added: still what
// changed, in more lines.
const diffCells = 4 << 20

// unified is old and new as a unified diff by line, "" when no line
// differs. A final newline is no line of its own.
func unified(old, new string) string {
	a, b := lines(old), lines(new)
	// The lines both begin and end with are left out of the table.
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
	// ops is the whole text, each line kept (' '), removed or added.
	type op struct {
		kind byte
		text string
	}
	var ops []op
	for _, l := range a[:pre] {
		ops = append(ops, op{' ', l})
	}
	if len(ma)*len(mb) > diffCells {
		for _, l := range ma {
			ops = append(ops, op{'-', l})
		}
		for _, l := range mb {
			ops = append(ops, op{'+', l})
		}
	} else {
		// lcs[i][j] is the longest common subsequence of ma[i:] and mb[j:].
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
	// la and lb are the lines of old and new before ops[at].
	at, la, lb := 0, 0, 0
	for at < len(ops) {
		if ops[at].kind == ' ' {
			at, la, lb = at+1, la+1, lb+1
			continue
		}
		// A hunk: from diffContext lines before this change to diffContext
		// after the last change that is within reach of the one before it.
		start := max(at-diffContext, 0)
		end, quiet := at, 0
		for k := at; k < len(ops) && quiet <= 2*diffContext; k++ {
			if ops[k].kind == ' ' {
				quiet++
			} else {
				end, quiet = k, 0
			}
		}
		end = min(end+diffContext, len(ops)-1)
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

// span is a hunk's range of n lines after the first before of a text, as
// diff -u writes it: the first line's number, or with no line the one
// before it.
func span(before, n int) string {
	if n == 0 {
		return fmt.Sprintf("%d,0", before)
	}
	return fmt.Sprintf("%d,%d", before+1, n)
}

// lines are a text's lines; "" has none.
func lines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}
