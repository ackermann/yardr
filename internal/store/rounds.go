package store

import "slices"

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
