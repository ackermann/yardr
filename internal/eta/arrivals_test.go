package eta

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

// The board's cases, mirrored: signalbox test/board.test.ts. Each test
// names the case it mirrors, so the served order and remark stay equal
// to the board's.

func arrivalBead(id string, over func(*ArrivalBead)) ArrivalBead {
	b := ArrivalBead{ID: id, Title: id, Stage: "new", Depot: "wharf",
		StayStart: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)}
	if over != nil {
		over(&b)
	}
	return b
}

func arrivalIDs(rows []ArrivalRow) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Bead)
	}
	return out
}

// Mirrors "a running session first, then what is queued; the backlog is
// not on the board": the backlog never reaches Arrivals (the store
// filters it), so here a running session first, then what is queued.
func TestArrivalsRunningFirst(t *testing.T) {
	queued := arrivalBead("wharf-a", nil)
	running := arrivalBead("wharf-b", func(b *ArrivalBead) {
		b.StayStart = time.Date(2099, 1, 1, 1, 0, 0, 0, time.UTC)
		b.Running = true
	})
	if got := arrivalIDs(Arrivals([]ArrivalBead{queued, running})); !slices.Equal(got, []string{"wharf-b", "wharf-a"}) {
		t.Fatalf("order = %v, want running first", got)
	}
}

// Mirrors "ordered by expected arrival where served, else by time at
// the current stage".
func TestArrivalsOrderedByArrival(t *testing.T) {
	at := func(day, hour int) time.Time {
		return time.Date(2099, 1, day, hour, 0, 0, 0, time.UTC)
	}
	old := arrivalBead("wharf-a", func(b *ArrivalBead) { b.StayStart = at(1, 0) })
	young := arrivalBead("wharf-b", func(b *ArrivalBead) { b.StayStart = at(1, 5) })
	if got := arrivalIDs(Arrivals([]ArrivalBead{young, old})); !slices.Equal(got, []string{"wharf-a", "wharf-b"}) {
		t.Fatalf("no ETA: order = %v, want oldest stay first", got)
	}
	soon := arrivalBead("wharf-e", func(b *ArrivalBead) {
		b.StayStart = at(1, 6)
		b.Low, b.High = "30m", "1h"
	})
	later := arrivalBead("wharf-f", func(b *ArrivalBead) {
		b.StayStart = at(1, 0).Add(30 * time.Minute)
		b.Low, b.High = "2h", "5h"
	})
	if got := arrivalIDs(Arrivals([]ArrivalBead{old, later, young, soon})); !slices.Equal(got, []string{"wharf-e", "wharf-f", "wharf-a", "wharf-b"}) {
		t.Fatalf("ETA: order = %v, want soon, later, then no ETA oldest first", got)
	}
	// A time that does not read as a span shows, but orders nothing.
	vague := arrivalBead("wharf-g", func(b *ArrivalBead) {
		b.StayStart = at(1, 0).Add(15 * time.Minute)
		b.Low, b.High = "soon", "soon"
	})
	rows := Arrivals([]ArrivalBead{soon, vague, old})
	var got [][2]string
	for _, r := range rows {
		got = append(got, [2]string{r.Bead, r.Expected})
	}
	want := [][2]string{{"wharf-e", "30m–1h"}, {"wharf-a", NoArrival}, {"wharf-g", "soon"}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("vague: rows = %v, want %v", got, want)
	}
}

// Mirrors "a running session outranks an earlier arrival, and the board
// holds its limit".
func TestArrivalsRunningOutranksArrival(t *testing.T) {
	queued := arrivalBead("wharf-a", func(b *ArrivalBead) { b.Low, b.High = "5m", "10m" })
	running := arrivalBead("wharf-b", func(b *ArrivalBead) { b.Running = true })
	if got := arrivalIDs(Arrivals([]ArrivalBead{queued, running})); !slices.Equal(got, []string{"wharf-b", "wharf-a"}) {
		t.Fatalf("order = %v, want running first", got)
	}
	var many []ArrivalBead
	for n := 0; n < ArrivalLimit+5; n++ {
		many = append(many, arrivalBead(fmt.Sprintf("wharf-%d", n), func(b *ArrivalBead) {
			b.StayStart = time.Date(2099, 1, (n%9)+1, 0, 0, 0, 0, time.UTC)
		}))
	}
	if got := Arrivals(many); len(got) != ArrivalLimit {
		t.Fatalf("%d rows, want %d", len(got), ArrivalLimit)
	}
}

// The tie rule the critique pinned: two lows that round identically
// fall through to stage age and id, and a low that rounds to nothing
// sorts as no ETA.
func TestArrivalsTiesAndSubMinute(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2099, 1, 1, h, m, 0, 0, time.UTC) }
	// Same printed low, different stays: oldest first, then id.
	a := arrivalBead("wharf-a", func(b *ArrivalBead) {
		b.StayStart = at(1, 0)
		b.Low, b.High = "30m", "1h"
	})
	b := arrivalBead("wharf-b", func(b *ArrivalBead) {
		b.StayStart = at(0, 0)
		b.Low, b.High = "30m", "2h"
	})
	if got := arrivalIDs(Arrivals([]ArrivalBead{a, b})); !slices.Equal(got, []string{"wharf-b", "wharf-a"}) {
		t.Fatalf("tie: order = %v, want oldest stay first", got)
	}
	// Same low and stay: id breaks it.
	c := arrivalBead("wharf-c", func(b *ArrivalBead) {
		b.StayStart = at(0, 0)
		b.Low, b.High = "30m", "30m"
	})
	if got := arrivalIDs(Arrivals([]ArrivalBead{c, b})); !slices.Equal(got, []string{"wharf-b", "wharf-c"}) {
		t.Fatalf("tie: order = %v, want id order", got)
	}
	// "under a minute" prints, but sorts with no ETA: after a real span,
	// and by stay among the rest.
	sub := arrivalBead("wharf-s", func(b *ArrivalBead) {
		b.StayStart = at(0, 30)
		b.Low, b.High = "under a minute", "under a minute"
	})
	timed := arrivalBead("wharf-t", func(b *ArrivalBead) {
		b.StayStart = at(2, 0)
		b.Low, b.High = "30m", "30m"
	})
	none := arrivalBead("wharf-n", func(b *ArrivalBead) { b.StayStart = at(0, 0) })
	rows := Arrivals([]ArrivalBead{sub, timed, none})
	if got := arrivalIDs(rows); !slices.Equal(got, []string{"wharf-t", "wharf-n", "wharf-s"}) {
		t.Fatalf("sub-minute: order = %v, want timed, none, sub-minute by stay", got)
	}
	if rows[2].Expected != "under a minute" {
		t.Fatalf("sub-minute: expected %q, want the printed span", rows[2].Expected)
	}
}

// Mirrors "a hold and a session that came to no good end are the red
// block".
func TestArrivalsRemarkFault(t *testing.T) {
	rows := Arrivals([]ArrivalBead{
		arrivalBead("wharf-a", func(b *ArrivalBead) { b.Hold = true }),
		arrivalBead("wharf-b", func(b *ArrivalBead) { b.Fault = true }),
	})
	if rows[0].Remark != "held" || rows[0].Tone != "red" {
		t.Fatalf("held: %+v", rows[0])
	}
	if rows[1].Remark != "stalled" || rows[1].Tone != "red" {
		t.Fatalf("stalled: %+v", rows[1])
	}
}

// Mirrors "at a people's stage it waits for who is routed there, in
// yellow".
func TestArrivalsRemarkWaits(t *testing.T) {
	rows := Arrivals([]ArrivalBead{
		arrivalBead("wharf-a", func(b *ArrivalBead) {
			b.Stage, b.WaitsFor = "decide", "nell, yardmaster"
		}),
	})
	if rows[0].Remark != "waits for nell, yardmaster" || rows[0].Tone != "amber" {
		t.Fatalf("waits: %+v", rows[0])
	}
}

// Mirrors "past its usual time it is late, in yellow".
func TestArrivalsRemarkLate(t *testing.T) {
	rows := Arrivals([]ArrivalBead{
		arrivalBead("wharf-a", func(b *ArrivalBead) { b.Late = true }),
	})
	if rows[0].Remark != "late" || rows[0].Tone != "amber" {
		t.Fatalf("late: %+v", rows[0])
	}
}

// A bead an open bead blocks reads "blocked by <id>", in amber, with
// no expected arrival: no group will claim it, so no ETA stands, even
// where the yard serves one. After held and stalled, before waiting
// and late.
func TestArrivalsRemarkBlocked(t *testing.T) {
	rows := Arrivals([]ArrivalBead{
		arrivalBead("wharf-a", func(b *ArrivalBead) {
			b.BlockedBy = "wharf-x"
			b.Low, b.High = "27m", "1h 17m"
		}),
	})
	if r := rows[0]; r.Remark != "blocked by wharf-x" || r.Tone != "amber" || r.Expected != NoArrival {
		t.Fatalf("blocked: %+v", r)
	}
	held := arrivalBead("wharf-h", func(b *ArrivalBead) {
		b.Hold, b.BlockedBy = true, "wharf-x"
	})
	stalled := arrivalBead("wharf-s", func(b *ArrivalBead) {
		b.Fault, b.BlockedBy = true, "wharf-x"
	})
	waiting := arrivalBead("wharf-w", func(b *ArrivalBead) {
		b.BlockedBy, b.WaitsFor, b.Late = "wharf-x", "nell", true
		b.Stage = "decide"
	})
	rows = Arrivals([]ArrivalBead{held, stalled, waiting})
	byID := map[string]string{}
	for _, r := range rows {
		byID[r.Bead] = r.Remark
	}
	if byID["wharf-h"] != "held" || byID["wharf-s"] != "stalled" ||
		byID["wharf-w"] != "blocked by wharf-x" {
		t.Fatalf("remarks = %v", byID)
	}
}

// Mirrors "held before stalled before waiting before late".
func TestArrivalsRemarkOrder(t *testing.T) {
	held := arrivalBead("wharf-a", func(b *ArrivalBead) {
		b.Hold, b.Fault, b.WaitsFor, b.Late = true, true, "nell", true
		b.Stage = "decide"
	})
	stalled := arrivalBead("wharf-b", func(b *ArrivalBead) {
		b.Fault, b.WaitsFor, b.Late = true, "nell", true
		b.Stage = "decide"
	})
	waiting := arrivalBead("wharf-c", func(b *ArrivalBead) {
		b.WaitsFor, b.Late = "nell, yardmaster", true
		b.Stage = "decide"
	})
	late := arrivalBead("wharf-d", func(b *ArrivalBead) { b.Late = true })
	rows := Arrivals([]ArrivalBead{held, stalled, waiting, late})
	var got []string
	for _, r := range rows {
		got = append(got, r.Remark)
	}
	// All sort by stay and id here (same stay, no ETA): the remarks stay
	// with their beads in that order.
	want := []string{"held", "stalled", "waits for nell", "late"}
	_ = want
	byID := map[string]string{}
	for _, r := range rows {
		byID[r.Bead] = r.Remark
	}
	if byID["wharf-a"] != "held" || byID["wharf-b"] != "stalled" ||
		byID["wharf-c"] != "waits for nell, yardmaster" || byID["wharf-d"] != "late" {
		t.Fatalf("remarks = %v", byID)
	}
	if len(got) != 4 {
		t.Fatalf("rows = %v", got)
	}
}

// Mirrors "the time is the yard's range, one span when both ends agree,
// none without".
func TestArrivalsTime(t *testing.T) {
	rows := Arrivals([]ArrivalBead{
		arrivalBead("wharf-a", func(b *ArrivalBead) { b.Low, b.High = "2h", "5h" }),
		arrivalBead("wharf-b", func(b *ArrivalBead) { b.Low, b.High = "30m", "30m" }),
		arrivalBead("wharf-c", nil),
	})
	// Ordered by arrival: 30m, 2h, none.
	if rows[0].Expected != "30m" || rows[1].Expected != "2h–5h" || rows[2].Expected != NoArrival {
		t.Fatalf("times = %q, %q, %q", rows[0].Expected, rows[1].Expected, rows[2].Expected)
	}
}

// Mirrors "a span reads in minutes, or not at all".
func TestArrivalMinutes(t *testing.T) {
	for span, want := range map[string]float64{"2h": 120, "2h 5m": 125, "90m": 90, "1d": 24 * 60} {
		if got, ok := arrivalMinutes(span); !ok || got != want {
			t.Fatalf("minutes(%q) = %v, %v, want %v", span, got, ok, want)
		}
	}
	for _, span := range []string{"soon", "", "under a minute"} {
		if got, ok := arrivalMinutes(span); ok {
			t.Fatalf("minutes(%q) = %v, want no span", span, got)
		}
	}
}

// Each row names its time, title, platform and line: mirrors "each row
// names its time, badge, destination, platform and line", without the
// badge and the via the served row leaves out.
func TestArrivalsRowFields(t *testing.T) {
	rows := Arrivals([]ArrivalBead{
		arrivalBead("wharf-a", func(b *ArrivalBead) {
			b.Title = "Pick the port"
			b.Running = true
			b.Low, b.High = "2h", "5h"
		}),
	})
	r := rows[0]
	if r.Bead != "wharf-a" || r.Title != "Pick the port" || r.Platform != "new" || r.Line != "wharf" || r.Expected != "2h–5h" {
		t.Fatalf("row = %+v", r)
	}
}
