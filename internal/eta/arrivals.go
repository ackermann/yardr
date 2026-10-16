// The arrivals board as the yard serves it: the open beads still
// travelling, up to Limit rows in arrival order, with the expected
// arrival as the yard's printed range and the remark (design
// footplate-8v5t, section 3).
//
// Arrivals is pure over its inputs — the beads with their stay starts,
// running holds, faults, waits and estimates — so the socket serves the
// same rows the board draws without recomputing them elsewhere. The
// order and the remark are signalbox src/board.ts, ported so the two
// cannot drift: running sessions first, then by the low end of the
// expected arrival in minutes, then by stage age and id, capped; the
// remark held, stalled, waiting for a person, late, in that order. No
// via and no badge: the row carries neither.
package eta

import (
	"cmp"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ArrivalLimit is how many rows the board holds: the top of what is in
// progress. The same cap as signalbox src/board.ts LIMIT; arrivals_test
// pins the two equal by value.
const ArrivalLimit = 15

// NoArrival is no expected arrival: the yard serves none yet, or the
// bead is below its sample floor. The same dash as signalbox NO_TIME.
const NoArrival = "—"

// An ArrivalBead is one travelling bead as the arrivals view reads it:
// what its row says, read where it is shown.
type ArrivalBead struct {
	ID    string
	Title string
	Stage string
	// Depot is the line, "" when the bead has none.
	Depot string
	// StayStart is when its current stay in its stage started.
	StayStart time.Time
	// Running says a live session holds the bead now.
	Running bool
	Hold    bool
	// Fault says its latest ended session came to no good end, and no
	// live session holds it now: the board's red block.
	Fault bool
	// WaitsFor is who it waits for at its people's stage, "" when it
	// waits for nobody: the members routed there, else the group, else
	// "a person".
	WaitsFor string
	// Low and High are the expected arrival as the yard printed it, ""
	// where it serves none.
	Low  string
	High string
	// Late says it runs past its usual time.
	Late bool
}

// An ArrivalRow is one row of the board: everything it shows of one
// bead, as text. The remark is blank when there is none; its tone is
// the board's red block or its yellow, "none" with no remark.
type ArrivalRow struct {
	Bead     string `json:"bead"`
	Title    string `json:"title"`
	Platform string `json:"platform"`
	Line     string `json:"line"`
	Expected string `json:"expected"`
	Remark   string `json:"remark"`
	Tone     string `json:"tone"`
}

// An ArrivalBoard is what the yard serves for the board: the rows beside
// the platforms in the order the beads travel, so a reader groups the
// rows by the served list without knowing the flows. Every platform a
// row stands on is in the list.
type ArrivalBoard struct {
	Rows      []ArrivalRow `json:"rows"`
	Platforms []string     `json:"platforms"`
}

// Arrivals answers the board's rows for beads: running sessions first,
// then by the low end in minutes, then by stage age and id, capped at
// ArrivalLimit. A low that reads as no span — "under a minute" among
// it — sorts with the beads the yard serves no ETA for, as the board
// does.
func Arrivals(beads []ArrivalBead) []ArrivalRow {
	aged := slices.Clone(beads)
	slices.SortStableFunc(aged, func(a, b ArrivalBead) int {
		if a.Running != b.Running {
			if a.Running {
				return -1
			}
			return 1
		}
		x, xok := arrivalMinutes(a.Low)
		y, yok := arrivalMinutes(b.Low)
		if xok != yok {
			if xok {
				return -1
			}
			return 1
		}
		if xok && x != y {
			return cmp.Compare(x, y)
		}
		if c := a.StayStart.Compare(b.StayStart); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	if len(aged) > ArrivalLimit {
		aged = aged[:ArrivalLimit]
	}
	out := make([]ArrivalRow, 0, len(aged))
	for _, b := range aged {
		out = append(out, ArrivalRowFor(b))
	}
	return out
}

// ArrivalRowFor answers the board's row for one bead: the expected arrival
// as the yard prints it, with the remark and its tone. Arrivals maps it
// over the capped board; the flows view maps it over every bead it draws,
// so the two can never disagree about one.
func ArrivalRowFor(b ArrivalBead) ArrivalRow {
	remark, tone := arrivalRemark(b)
	return ArrivalRow{
		Bead: b.ID, Title: b.Title, Platform: b.Stage, Line: b.Depot,
		Expected: arrivalTime(b), Remark: remark, Tone: tone,
	}
}

// arrivalTime is the row's time: the yard's range, one span when both
// ends agree, and no time where the yard serves none.
func arrivalTime(b ArrivalBead) string {
	if b.Low == "" {
		return NoArrival
	}
	if b.Low == b.High {
		return b.Low
	}
	return b.Low + "–" + b.High
}

// arrivalRemark is the row's remark: held, stalled, waiting for a
// person, late, in that order, else blank. A hold and a session that
// came to no good end are the board's red block; waiting and late are
// its yellow.
func arrivalRemark(b ArrivalBead) (remark, tone string) {
	if b.Hold {
		return "held", "red"
	}
	if b.Fault {
		return "stalled", "red"
	}
	if b.WaitsFor != "" {
		return "waits for " + b.WaitsFor, "amber"
	}
	if b.Late {
		return "late", "amber"
	}
	return "", "none"
}

// arrivalSpan is one part of a printed span: a number and its unit.
var arrivalSpan = regexp.MustCompile(`(?i)(\d+)\s*([dhms])`)

// arrivalMinutes is a span the yard prints ("2h", "30m", "2h 5m") in
// minutes: what the rows are ordered by. False for what does not read
// as a span — "under a minute" and "soon" among it.
func arrivalMinutes(span string) (float64, bool) {
	parts := arrivalSpan.FindAllStringSubmatch(span, -1)
	if len(parts) == 0 {
		return 0, false
	}
	long := map[string]float64{"d": 24 * 60, "h": 60, "m": 1, "s": 1.0 / 60}
	var out float64
	for _, p := range parts {
		n, err := strconv.ParseFloat(p[1], 64)
		if err != nil {
			return 0, false
		}
		out += n * long[strings.ToLower(p[2])]
	}
	return out, true
}
