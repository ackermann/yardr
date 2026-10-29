package rpc

import (
	"context"
	"slices"

	"yardr.dev/yardr/internal/eta"
	"yardr.dev/yardr/internal/store"
)

// bead is Bead as a device is told it: the view the store gives, but for
// the file of a setup card's typed account, the candidates of the card, each
// a file of the yard's with the values it holds, and for the file the account
// of an approved setup is set in, which is the candidate's when the approval
// took one: only a device of the operator is told them (isOperator, as
// deviceTold tells the operator's words). To any other device the card reads
// as one with none and naming no file
// (store.SetupAsked.WithoutCandidatesAndFile) and what the setup printed says
// the account is set and not where, or that it failed and no more, since the
// words of a failure name the file too (store.SetupAsked.WithoutAccountFile),
// and in a yard without an operator it does to every device. A run ask is told
// as its asker typed it, and not as the yard wrote it up from its own records:
// what the command changes and takes back, the files it carries, the plan of a
// pack, and what it printed when approved, which name the files and the
// settings it is about (withoutRunWords). Where the typed words themselves
// carry a value or a path, argv and command are told to the operator alone,
// and the ask reads as its question and the command's help line. The socket,
// the terminal and the
// agents read the whole card and result from the store, as they do the
// operator's words. UserAsks holds the same back from the same devices
// (userAsks), since the user an ask was put to need not be the operator now.
// The yard's own words about a failed fetch or login are held likewise: a
// note by the yard that a peer's branch could not be taken, or that a note
// from another yard met a flow that does not read, reads as what happened,
// that the bead is held, and that why is an operator's to read, and the
// words of a login ask taken back when the login ended read as that the
// login ended and that why is an operator's to read
// (store.HeldYardNote, store.HeldYardWords). Any other note and any other
// words read as they are.
type userBeadView struct {
	*store.BeadView
	ETA  *eta.ETA  `json:"eta,omitempty"`
	Late *eta.Late `json:"late,omitempty"`
}

func (u *UserServer) bead(ctx context.Context, id, viewer string) (*userBeadView, error) {
	v, err := u.st.Bead(ctx, id, viewer)
	if err != nil {
		return nil, err
	}
	view := &userBeadView{BeadView: v}
	// Bead's device response kept these fields before estimates moved out
	// of the store. A failed estimate never hid the bead then, so it still
	// leaves this view intact.
	if est, err := u.beadEstimate(ctx, u.st, id, u.now()); err == nil && est != nil {
		view.ETA, view.Late = est.ETA, est.Late
	}
	cards := slices.ContainsFunc(v.Asks, func(a store.BeadAsk) bool {
		return a.Run != nil || a.Setup != nil || a.DepotPush != nil
	})
	yard := slices.ContainsFunc(v.Notes, func(n store.BeadNote) bool {
		_, ok := store.HeldYardNote(n.Author, n.Text)
		return ok
	}) || slices.ContainsFunc(v.Asks, func(a store.BeadAsk) bool {
		_, ok := store.HeldYardWords(a.By, a.Words)
		return ok
	})
	if !cards && !yard {
		return view, nil
	}
	if u.isOperator(ctx, userOf(ctx)) {
		return view, nil
	}
	for i := range v.Notes {
		if held, ok := store.HeldYardNote(v.Notes[i].Author, v.Notes[i].Text); ok {
			v.Notes[i].Text = held
		}
	}
	for i := range v.Asks {
		a := &v.Asks[i]
		a.Setup, a.Ran = withoutAccount(a.Setup, a.Ran)
		a.Run, a.Ran = withoutRunWords(a.Run, a.Ran)
		a.DepotPush, a.Ran = withoutDepotPush(a.DepotPush, a.Ran)
		if held, ok := store.HeldYardWords(a.By, a.Words); ok {
			a.Words = held
		}
	}
	return view, nil
}
