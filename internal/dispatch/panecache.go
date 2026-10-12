package dispatch

import (
	"context"
	"sync"
)

// A PaneCache is one pass's snapshots of the panes it reads: the daemon
// takes at most one snapshot of a pane per pass, and every check of the pass
// reads from it. Three reads in a row make herdr scroll the pane a client
// shows, and one read is in the set that was seen quiet (yardr-2zst).
//
// The cache lives on the pass's context (WithPaneCache, which Reconcile
// calls): a runner that reads panes keeps its snapshots there, keyed by
// pane, and forgets one's (Drop) once it types into the pane, so a later
// check looks at what the keys did. A context without one reads panes
// directly, as before.
type PaneCache struct {
	mu    sync.Mutex
	snaps map[string]paneSnap
}

// paneSnap is the styled text of the last lines lines of a pane: it holds
// the plain text too, once its SGR escapes are stripped, so one snapshot
// serves both kinds of reader.
type paneSnap struct {
	styled string
	lines  int
}

type paneCacheKey struct{}

// WithPaneCache returns ctx with an empty PaneCache for one pass.
func WithPaneCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, paneCacheKey{}, &PaneCache{})
}

// PaneCacheFrom is the PaneCache ctx carries for this pass, nil when it
// carries none.
func PaneCacheFrom(ctx context.Context) *PaneCache {
	c, _ := ctx.Value(paneCacheKey{}).(*PaneCache)
	return c
}

// Lookup is the pass's snapshot of pane, if one was taken that covers the
// pane's last lines lines.
func (c *PaneCache) Lookup(pane string, lines int) (styled string, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.snaps[pane]
	if !ok || s.lines < lines {
		return "", false
	}
	return s.styled, true
}

// Store remembers styled, the styled text of the last lines lines of pane,
// as the pass's snapshot of it.
func (c *PaneCache) Store(pane, styled string, lines int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.snaps == nil {
		c.snaps = map[string]paneSnap{}
	}
	c.snaps[pane] = paneSnap{styled: styled, lines: lines}
}

// Drop forgets the pass's snapshot of pane: keys were typed into it, so a
// later check reads the pane again.
func (c *PaneCache) Drop(pane string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.snaps, pane)
}
