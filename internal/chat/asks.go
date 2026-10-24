package chat

import (
	"cmp"
	"context"
	"slices"
	"time"

	"yardr.dev/yardr/internal/store"
)

// AskStore is what reading a user's asks takes of the yard. It is not part
// of Store: those calls are the socket API a program that is not yardr may
// rely on (docs/api.md), and asks join them with the socket bead. Until
// then the command passes this read in as Options.Asks.
type AskStore interface {
	ListBeads(ctx context.Context, f store.BeadFilter) ([]store.Bead, error)
	Notes(ctx context.Context, id string) ([]store.Note, error)
}

// AsksFor loads the asks addressed to the user me on the yard's open
// beads, oldest first, each with its answers and its state.
func AsksFor(ctx context.Context, st AskStore, me string) ([]store.ResolvedAsk, error) {
	beads, err := st.ListBeads(ctx, store.BeadFilter{Viewer: me})
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var out []store.ResolvedAsk
	for _, b := range beads {
		notes, err := st.Notes(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		for _, r := range store.ResolveAsks(notes, now) {
			if r.AskErr == nil && r.Ask.To == me {
				out = append(out, r)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b store.ResolvedAsk) int { return cmp.Compare(a.Note.Seq, b.Note.Seq) })
	return out, nil
}
