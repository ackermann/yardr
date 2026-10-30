// How a bead answer command reads the ask it answers: the secret, the
// setup, the depot push or the command the ask seq on a bead proposes, as
// the yard stored it, or why the ask is none of that kind.
package cli

import (
	"context"
	"fmt"
	"time"

	"yardr.dev/yardr/internal/store"
)

// askedSecret reads the secret the ask seq on the bead id asks for, as the
// yard stored it.
func askedSecret(ctx context.Context, st store.API, id string, seq uint64) (*store.AskSecret, error) {
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		if r.AskErr != nil {
			return nil, r.AskErr
		}
		if r.Ask.Secret == nil {
			return nil, fmt.Errorf("ask %d asks for no secret: it is answered with yardr bead answer %s %d <n> [words], or --words", seq, id, seq)
		}
		return r.Ask.Secret, nil
	}
	return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
}

// askedSetup reads the setup the ask seq on the bead id proposes, as the
// yard stored it.
func askedSetup(ctx context.Context, st store.API, id string, seq uint64) (*store.AskSetup, error) {
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		if r.AskErr != nil {
			return nil, r.AskErr
		}
		if r.Ask.Setup == nil {
			return nil, fmt.Errorf("ask %d sets up no pack", seq)
		}
		return r.Ask.Setup, nil
	}
	return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
}

// askedDepotPush reads the depot-push the ask seq on the bead id
// proposes, as the yard stored it.
func askedDepotPush(ctx context.Context, st store.API, id string, seq uint64) (*store.AskDepotPush, error) {
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		if r.AskErr != nil {
			return nil, r.AskErr
		}
		if r.Ask.DepotPush == nil {
			return nil, fmt.Errorf("ask %d turns no depot's push on or off", seq)
		}
		return r.Ask.DepotPush, nil
	}
	return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
}

// askedRun reads the command the ask seq on the bead id proposes, as the
// yard stored it.
func askedRun(ctx context.Context, st store.API, id string, seq uint64) (*store.AskRun, error) {
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq != seq {
			continue
		}
		if r.AskErr != nil {
			return nil, r.AskErr
		}
		if r.Ask.Run == nil {
			return nil, fmt.Errorf("ask %d proposes no command", seq)
		}
		return r.Ask.Run, nil
	}
	return nil, fmt.Errorf("bead %s has no ask %d", id, seq)
}
