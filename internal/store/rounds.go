package store

import (
	"fmt"
	"slices"
)

// A bead's rounds are how often it has gone round a loop of its flow: the
// times an outcome brought it back to a stage it had been in (a reviewer's
// changes, a failed landing). Bead.Rounds keeps them per stage, and keeps
// them itself: the ETA's visited list stops at ETAVisitedCap stages while a
// flow may have more, so a return to a stage first reached past the cap
// would never count if the list were read for it.
//
// Every arrival gives its stage a key at zero when it has none, the bead's
// creation and a move by a person included, and a key is what makes a later
// outcome into the stage a return. A move by a person never adds a round:
// whoever moves a bead has already looked at it.

// been reports whether the bead has been in stage before, and the rounds
// counted for it. A key says so. A bead from before Rounds has none, so the
// stages its visited list holds stand for what it had reached, with the cap
// that list already had.
func (b *beadRow) been(stage string) (rounds int, ok bool) {
	if n, keyed := b.Rounds[stage]; keyed {
		return n, true
	}
	return 0, slices.Contains(b.Visited, stage)
}

// enter records that the bead is entering stage. A stage it had not been in
// gets its key at zero. One it had been in keeps its count when a person
// moved the bead, and gets one more round when an outcome brought it back
// (byOutcome), a same-stage outcome included. It runs before exitStay, which
// lists the stage as visited: a stage without a key that the list holds
// counts as one the bead has been in, so after it every first arrival would
// read as a return.
func (b *beadRow) enter(stage string, byOutcome bool) {
	rounds, been := b.been(stage)
	if byOutcome && been {
		rounds++
	}
	if b.Rounds == nil {
		b.Rounds = map[string]int{}
	}
	b.Rounds[stage] = rounds
}

// restart sets the count for stage back to zero and keeps its key, as a limit
// does that has sent the bead on instead: the rounds are used, and whoever
// sends the bead back to stage then has granted it a fresh allowance by doing
// so. Nothing else resets a count, a question to a person mid-loop included,
// so a bead cannot go round for ever by asking once a round. The key stays
// because it is what makes a later outcome into the stage a return.
func (b *beadRow) restart(stage string) {
	b.Rounds[stage] = 0
}

// OutcomeNow is one way work in the bead's stage can end, as it stands for
// the bead: the flow's outcome and where reporting it takes this bead now.
// The two differ once the stage a limited outcome leads back to has used its
// rounds.
type OutcomeNow struct {
	Outcome
	// Goes is the stage reporting the outcome takes the bead to: To, or Then
	// once the rounds back to To are used. To stays the flow's edge.
	Goes string `json:"goes"`
	// Used is how many rounds back to To the bead has used, read for a limited
	// outcome only; 0 is left out.
	Used int `json:"used,omitempty"`
	// Round is which round reporting the outcome would be, Used+1, when it
	// takes the bead back to a stage it has been in within the limit; 0 when it
	// is no round: no limit, a stage the bead has not been in, or the limit's
	// redirect.
	Round int `json:"round,omitempty"`
}

// takes reads o as it stands for the bead. It is the one place that decides
// where a limited outcome goes: donePick moves the bead by it and NextStages
// shows it, so what a session is told and what its report does cannot differ.
func (b *beadRow) takes(o Outcome) OutcomeNow {
	n := OutcomeNow{Outcome: o, Goes: o.To}
	if o.Max == 0 {
		return n
	}
	used, been := b.been(o.To)
	n.Used = used
	switch {
	// At least Max, not exactly: two outcomes into one stage may carry
	// different limits and both add to the stage's one count, so the higher
	// limit's edge can raise it past a lower one's, and an equality would let
	// the bead go round past that limit for as long as the count stays above
	// it. Max is 1 or more, so a count that reaches it is one the stage has a
	// key for: the bead has been there.
	case used >= o.Max:
		n.Goes = o.Then
	case been:
		n.Round = used + 1
	}
	return n
}

// RoundNote says how the outcome's limit stands, for the line that names where
// it goes: "round 3 of 3" while the report would take the bead back to a stage
// it has been in within the limit, "the 3 rounds back to new are used" once
// they are, and "" for any other outcome, a first arrival included. quote
// surrounds the stage's name.
func (n OutcomeNow) RoundNote(quote string) string {
	if n.Round > 0 {
		return fmt.Sprintf("round %d of %d", n.Round, n.Max)
	}
	if n.Max == 0 || n.Goes == n.To {
		return ""
	}
	if n.Used == 1 {
		return fmt.Sprintf("the 1 round back to %s%s%s is used", quote, n.To, quote)
	}
	return fmt.Sprintf("the %d rounds back to %s%s%s are used", n.Used, quote, n.To, quote)
}

// RoundsLine says how far into its limit the bead is for the stage a limited
// outcome leads back to, and where the next report of the outcome goes, from
// the stage it is reported in: "new 2 of 3 (the next changes from review goes
// to new)". "" for an outcome with no limit or a stage with no rounds counted:
// a count of zero is nothing to say.
func (n OutcomeNow) RoundsLine(from string) string {
	if n.Max == 0 || n.Used == 0 {
		return ""
	}
	return fmt.Sprintf("%s %d of %d (the next %s from %s goes to %s)", n.To, n.Used, n.Max, n.Name, from, n.Goes)
}
