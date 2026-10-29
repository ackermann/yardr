package store

import (
	"time"
)

// FlowsBoard is what the yard serves for the flows view: its depots as
// lines, each with its flows, each flow its stages in travel order with
// the open beads on each and its lamp, and the beads travelling with no
// line beside them. It is what footplate's timetable draws: the yard
// computes the view once so the readers draw without computing (design
// footplate-zj5f, section 3.4). The depots, flows, stages and beads are
// the web's /flows page as served; the lamps are its panel's; each bead's
// expected arrival, remark and tone are the Arrivals row's, reused, not
// recomputed.
type FlowsBoard struct {
	// Travelling counts the yard's open beads outside the backlog: the
	// depots' totals plus the unassigned count. Complete, not capped.
	Travelling int          `json:"travelling"`
	Depots     []FlowsDepot `json:"depots"`
	// Unassigned are the open beads outside the backlog with no depot,
	// the newest change first, each row shaped as a flow bead.
	Unassigned []FlowBead `json:"unassigned"`
}

// FlowsDepot is one depot as the flows view shows it: its flows, and the
// open beads in no stage of them.
type FlowsDepot struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Travelling counts the depot's open beads outside the backlog.
	Travelling int         `json:"travelling"`
	Flows      []FlowsFlow `json:"flows"`
	// Elsewhere are the depot's open beads in no stage of the flows
	// above: a bead that pins a flow of its own, one a changed flow left
	// behind, and every bead of a depot with no flow. The newest change
	// first, as on /flows.
	Elsewhere []FlowBead `json:"elsewhere"`
	// Error is why the depot's flows could not be read; its beads are
	// then all in Elsewhere, as on /flows.
	Error string `json:"error,omitempty"`
}

// FlowsFlow is one flow of a depot: the depot's own (Type "") or a bead
// type's.
type FlowsFlow struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Source is where the flow comes from, as `yardr flow show` says it:
	// "store", "repo@0f415ee".
	Source string       `json:"source"`
	Stages []FlowsStage `json:"stages"`
	// Landings are the flow's beads that entered merged in the trailing
	// 24 hours, newest first, at most 30 of them: what landed under the
	// flow's landed station. Empty where nothing did.
	Landings []FlowLanding `json:"landings"`
}

// FlowLanding is one bead that landed under a flow: the bead, its title
// as it stands now, and when it entered merged.
type FlowLanding struct {
	Bead     string    `json:"bead"`
	Title    string    `json:"title"`
	LandedAt time.Time `json:"landed_at"`
}

// FlowsStage is one stage of a flow and the open beads in it.
type FlowsStage struct {
	Stage string `json:"stage"`
	// Group is the group a bead of the flow's type would be routed to in
	// this stage, "" when no route matches. Human says it is a manual
	// group.
	Group string `json:"group"`
	Human bool   `json:"human"`
	// Lamp is the stage's wait light: none while a bead is worked with
	// no fault of its own, fault where a session waits on a prompt or a
	// bead lacks what its route needs, turn where only a held bead or a
	// person's turn does, none otherwise. The panel's two lamps, served.
	Lamp string `json:"lamp"`
	// Terminal is a stage that closes the bead entering it, as the web's
	// stage says: a reader draws the buffer stop from this, never from the
	// last place.
	Terminal bool `json:"terminal"`
	// Limit is the routed group's seat limit, 0 pausing the group; Taken
	// is how many of the group's seats live sessions hold across the whole
	// yard, the same number on every stage routed to the group. Both are
	// absent where no route matches, so a reader can tell an unrouted
	// stage from a paused one.
	Limit *int       `json:"limit,omitempty"`
	Taken *int       `json:"taken,omitempty"`
	Beads []FlowBead `json:"beads"`
}

// FlowBead is one bead of the flows view: the bead with its Arrivals row.
type FlowBead struct {
	Bead     string `json:"bead"`
	Title    string `json:"title"`
	Expected string `json:"expected"`
	Remark   string `json:"remark"`
	Tone     string `json:"tone"`
	// HeldBy is who holds a held bead, as the Arrivals row says it;
	// absent where the bead is not held and where the holder is not known.
	HeldBy string `json:"held_by,omitempty"`
	// Working is a bead an agent's or a command's session has claimed; a
	// manual group's claim is a person's turn and no work in progress, as
	// on Home and the line.
	Working bool `json:"working"`
	// Worker is who works the bead, as `yardr session list` shows the
	// claiming session: the runner with the agent's name or pid. Absent
	// where no session claimed the bead, and on a manual group's claim,
	// which names no worker.
	Worker string `json:"worker,omitempty"`
	// Group is the group working the bead, as the claiming session
	// recorded it at claim. Absent wherever Worker is absent, so a reader
	// can tell a worked bead from either field.
	Group string `json:"group,omitempty"`
	// Kind and Model are the harness kind and model the claiming session
	// started with, as it recorded them at claim and served unchanged: a
	// route or group change after the claim does not move them. Absent
	// where Worker is, and where the session recorded none: an exec or
	// peer claim serves no kind, and no model where the group named none.
	Kind  string `json:"kind,omitempty"`
	Model string `json:"model,omitempty"`
	// EnteredStageAt is when the bead entered its stage, so a reader can
	// say how long it has stood there. Absent where no session claimed
	// the bead, and where the stay has no clock.
	EnteredStageAt *time.Time `json:"entered_stage_at,omitempty"`
}
