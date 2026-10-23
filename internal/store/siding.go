package store

import (
	"context"
	"time"

	bolt "go.etcd.io/bbolt"
)

// What a siding folds by where its group leaves a number unstated: about
// four parts, which is what the shaper bundles by hand; eight hours since the
// latest arrival, so that with each arrival counting toward the four no bead
// waits more than about a day; and six points, six small beads or three
// mediums, which one session can build and one review hold.
const (
	SidingFold  = 4
	SidingAfter = 8 * time.Hour
	SidingMax   = 6
)

// Siding is what a siding group folds by. A siding is not a kind of stage or
// of bead but a stage that a manual group works with these set: beads wait
// there, and leave folded into one.
type Siding struct {
	// Fold is how many beads waiting in a stage of the group fill it.
	Fold int
	// After is how long after the latest bead entered the stage the beads
	// waiting are folded anyway.
	After time.Duration
	// Max is the most points one fold may carry.
	Max int
}

// SidingOf reads g as a siding. A manual group is one when its config states
// any of fold, fold_after and fold_max; what it leaves out takes the number
// above. Stating any is enough because a key stated alone would otherwise be
// stored and ignored, which is what ConfigKeys is there to stop. It is the one
// reader of those keys: bead fold and flow show ask it, and so must whatever
// folds a siding by itself, so that none of them means something of its own
// by a siding. A value stored as something its key does not hold
// (ConfigProblems refuses one where it is written) reads as unstated.
func SidingOf(g Group) (Siding, bool) {
	if g.Runner != "manual" {
		return Siding{}, false
	}
	s := Siding{Fold: SidingFold, After: SidingAfter, Max: SidingMax}
	_, fold := g.Config["fold"]
	_, after := g.Config["fold_after"]
	_, most := g.Config["fold_max"]
	if !fold && !after && !most {
		return Siding{}, false
	}
	if n, ok := wholeCount(g.Config["fold"]); ok {
		s.Fold = n
	}
	if text, _ := g.Config["fold_after"].(string); text != "" {
		if d, err := ParseSpan(text); err == nil {
			s.After = d
		}
	}
	if n, ok := wholeCount(g.Config["fold_max"]); ok {
		s.Max = n
	}
	return s, true
}

// SidingBead is a bead waiting in a siding, as the siding reads it.
type SidingBead struct {
	Bead string `json:"bead"`
	// Entered is when the bead entered its stage. The latest of a siding's
	// beads starts the clock its fold_after counts from.
	Entered time.Time `json:"entered"`
	// Points is what the bead adds to a fold, by its size: label.
	Points int `json:"points"`
	// Stuck says why a fold would leave the bead where it is: "held" (a
	// person's word that it waits), "claimed" (a session works it), "blocked"
	// (an open bead blocks it, and it would build before its blocker) or
	// "asked" (a question to a person stands open on it, which a fold would
	// strand). Empty for a bead a fold takes.
	Stuck string `json:"stuck,omitempty"`
}

// SidingBeads reads the beads ids, in that order, as a siding reads the beads
// that wait in it: when each entered its stage, what it weighs and whether a
// fold would take it. The shape of a siding (its stage, its group, which
// beads are in it) is the caller's to know; this is what only the store
// knows, so that the definition of a bead a fold leaves is one, wherever it
// is asked.
//
// A bead a manual group holds is not "claimed": a manual group claims the
// beads of its stage up to its limit, and its claim is a person's turn with
// nothing running (Session.Manual), so counting it would leave every bead of
// a siding where it is.
func (s *Store) SidingBeads(ctx context.Context, ids []string) ([]SidingBead, error) {
	out := []SidingBead{}
	err := s.withUntilRepos(ctx, func(look *untilLooks) error {
		out = []SidingBead{}
		return s.read(func(tx *bolt.Tx) error {
			for _, id := range ids {
				row, err := loadBead(tx, id)
				if err != nil {
					return err
				}
				stuck, err := stuckBead(tx, row, look)
				if err != nil {
					return err
				}
				out = append(out, SidingBead{Bead: row.ID, Entered: row.StayStart, Points: sizePoints(row.Labels), Stuck: stuck})
			}
			return nil
		})
	})
	return out, err
}

// stuckBead is why a fold would leave row where it is, in the order
// SidingBead lists the reasons, or "" for a bead it takes. An ask the stage
// itself put (Ask.Stage) is not a reason: closing the bead takes it back.
func stuckBead(tx *bolt.Tx, row *beadRow, look *untilLooks) (string, error) {
	if row.Hold {
		return "held", nil
	}
	if row.Session != nil {
		var sess Session
		// A session that is not there reads as the zero one, which is no
		// manual group's: it is taken to work the bead, since a fold that
		// ended it would end work it cannot see.
		if _, err := get(tx, bSessions, *row.Session, &sess); err != nil {
			return "", err
		}
		if !sess.Manual() {
			return "claimed", nil
		}
	}
	if len(openBlockers(tx, &row.Bead, look)) > 0 {
		return "blocked", nil
	}
	notes, err := beadNotes(tx, row.UUID)
	if err != nil {
		return "", err
	}
	for _, r := range ResolveAsks(notes, now()) {
		if r.AskErr == nil && r.Ask.Stage == "" && (r.State == AskOpen || r.State == AskAside) {
			return "asked", nil
		}
	}
	return "", nil
}
