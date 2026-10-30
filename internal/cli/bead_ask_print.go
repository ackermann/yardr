// How a bead ask and answer command confirms what it put or answered: the
// ask printed as prime shows it, numbered text with its state, or the
// question with its state as this yard knows it for a bead of another
// yard, which stands on no bead here.
package cli

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"yardr.dev/yardr/internal/store"
)

// printAsk prints the ask seq of the bead id on w, as the confirmation of
// the command that put or answered it. warn is where it says that the
// yard's links could not be read (see trustMarks).
func printAsk(w, warn io.Writer, ctx context.Context, st store.API, id string, seq uint64) {
	if _, _, foreign := strings.Cut(id, "@"); foreign {
		printForeignAsk(w, ctx, st, id, seq)
		return
	}
	notes, err := st.Notes(ctx, id)
	if err != nil {
		fmt.Fprintf(w, "ask %d on %s\n", seq, id)
		return
	}
	as := trustMarks(warn, ctx, st)
	if as == nil {
		as = map[string]string{}
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			fmt.Fprintln(w, strings.Join(r.LinesMarked(as), "\n"))
			return
		}
	}
	fmt.Fprintf(w, "ask %d on %s\n", seq, id)
}

// printForeignAsk prints the ask seq of a bead@peer ref as the answer's
// confirmation: the question with its state as this yard knows it. The
// question stands on no bead of this yard, so there is no numbered text
// for it here; the card is in the decisions UI. The question, To and
// asker are the other yard's writer words, so each stands folded to one
// line, as the local card's one-line slots do, and none starts a line of
// its own.
func printForeignAsk(w io.Writer, ctx context.Context, st store.API, id string, seq uint64) {
	v, err := st.Bead(ctx, id)
	if err != nil {
		fmt.Fprintf(w, "ask %d on %s\n", seq, id)
		return
	}
	for _, a := range v.Asks {
		if a.Ask == seq {
			fmt.Fprintf(w, "ask %d · %s · to %s · asked by %s\n%s\n",
				a.Ask, a.State, store.OneLine(a.To), store.OneLine(a.Asker), store.OneLine(a.Question))
			return
		}
	}
	fmt.Fprintf(w, "ask %d on %s\n", seq, id)
}

// askState is the ask's state as words, "" when the bead cannot be read
// now.
func askState(ctx context.Context, st store.API, id string, seq uint64) string {
	if _, _, foreign := strings.Cut(id, "@"); foreign {
		v, err := st.Bead(ctx, id)
		if err != nil {
			return ""
		}
		for _, a := range v.Asks {
			if a.Ask == seq {
				return a.State
			}
		}
		return ""
	}
	notes, err := st.Notes(ctx, id)
	if err != nil {
		return ""
	}
	for _, r := range store.ResolveAsks(notes, time.Now()) {
		if r.Note.Seq == seq {
			return r.State.String()
		}
	}
	return ""
}
