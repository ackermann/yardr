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
// bead the log still has, as viewer is told them (store.EventFor). It is
// what `yardr bead history` prints and what the bead's page shows, so the
// two say the same. Nothing but the log keeps what a bead was, so the
// history starts where pruning left the log (store.Prune).
func History(ctx context.Context, st store.API, bead, viewer string) ([]Change, error) {
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
	operator := false
	if viewer != "" {
		if u, err := st.GetUser(ctx, viewer); err == nil && u.IsOperator() {
			operator = true
		}
	}
	// An event the viewer is told nothing of stands in no history.
	kept := make([]store.Event, 0, len(evs))
	for _, e := range evs {
		if told, ok := store.EventFor(viewer, operator, e); ok {
			kept = append(kept, told)
		}
	}
	evs = kept
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

// unified is old and new as a unified diff by line, rendered as the store
// renders it so a card and a history show a change one way.
func unified(old, new string) string { return store.Unified(old, new) }
