package store

import (
	"maps"
	"slices"
)

// clone is b sharing nothing with it but its Peer, which a row of openBeads
// never has: a caller is free to change what a listing handed it.
func (b *Bead) clone() Bead {
	c := *b
	c.Depot, c.Group, c.Session = copyOf(b.Depot), copyOf(b.Group), copyOf(b.Session)
	c.CloseReason, c.CreatedBy, c.ClosedAt = copyOf(b.CloseReason), copyOf(b.CreatedBy), copyOf(b.ClosedAt)
	c.Labels = slices.Clone(b.Labels)
	c.Rounds = maps.Clone(b.Rounds)
	c.Visited = slices.Clone(b.Visited)
	if b.Metadata != nil {
		c.Metadata = cloneJSON(b.Metadata).(map[string]any)
	}
	return c
}

// cloneJSON copies a value as encoding/json decodes one into an any: the
// maps and slices are new, and the rest cannot be changed in place.
func cloneJSON(v any) any {
	switch v := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, e := range v {
			m[k] = cloneJSON(e)
		}
		return m
	case []any:
		l := make([]any, len(v))
		for i, e := range v {
			l[i] = cloneJSON(e)
		}
		return l
	}
	return v
}
