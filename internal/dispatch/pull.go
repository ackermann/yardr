package dispatch

import (
	"context"
	"strconv"
	"strings"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// metaOwnPR is the metadata key whose value is the address of a pull request
// the bead itself opened from its own branch (the public line's open-pr
// script writes it, docs/line.md). Such a pull request is the bead's work
// published, not somebody else's change, so the bead is not put on its head.
const metaOwnPR = "own_pr"

// Pull is a pull request a bead tracks: somebody else's change, which the
// bead's sessions read and do not write.
type Pull struct {
	URL    string // as the bead's link has it
	Number int
	// Head is the commit the session's worktree was put on, the pull
	// request's head when the session started. The runner sets it; it is
	// empty where there is no worktree.
	Head string
}

// pullOf is the pull request among a bead's links: the one it tracks, of a
// source that has pull requests. An issue it tracks is none, and neither is
// a link that only relates, nor the pull request the bead's metadata names
// as its own (metaOwnPR): a session on the bead stands on its branch, where
// its commits are the ones the pull request is updated from, instead of
// being told that a commit made on the pull request's head lands nowhere.
func (d *Dispatcher) pullOf(ctx context.Context, links []store.Link, meta map[string]any) *Pull {
	own, _ := meta[metaOwnPR].(string)
	for _, l := range links {
		if l.Kind != store.LinkTracks || (own != "" && l.URL == own) {
			continue
		}
		p, ok := hooks.PullerFor(l.Source, d.declOf(ctx, l.Source))
		if !ok {
			continue
		}
		thread := p.PullOf(l.URL)
		if thread == "" {
			continue
		}
		_, num, _ := strings.Cut(thread, "#")
		if n, err := strconv.Atoi(num); err == nil {
			return &Pull{URL: l.URL, Number: n}
		}
	}
	return nil
}
