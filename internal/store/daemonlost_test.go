package store

import (
	"strings"
	"testing"
	"time"
)

// Recording a lost daemon writes one daemon_lost event and raises a run
// card for yardr up on a new bead: the daemon's last known alive time in
// the event's data, the question naming that time, and the context naming
// the event. A second recording while the loss stands writes nothing, and a
// start ends the state, so the next loss raises a second card.
func TestNoteDaemonLost(t *testing.T) {
	// Sequential: the package's fake clock.
	pinClockFixed(t, askNow)
	y, _ := runYard(t)
	stopped := y.now.Add(-time.Hour)
	bead, wrote, err := y.NoteDaemonLost(y.ctx, stopped)
	if err != nil {
		t.Fatal(err)
	}
	if !wrote || bead == "" {
		t.Fatalf("NoteDaemonLost = %q, %v, want a bead and written", bead, wrote)
	}
	lost, err := y.EventsOfKind(y.ctx, "daemon_lost", 10)
	if err != nil || len(lost) != 1 {
		t.Fatalf("daemon_lost events = %d, %v", len(lost), err)
	}
	if got := lost[0].Data["stopped_at"]; got != stopped.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("the event records stopped_at %v, want %s", got, stopped.UTC().Format(time.RFC3339Nano))
	}
	row, err := y.GetBead(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	when := stopped.Local().Format(daemonTimeLayout)
	if want := "The daemon of this yard was last alive at " + when + ". Start it?"; row.Title != want {
		t.Fatalf("the bead is titled %q, want %q", row.Title, want)
	}
	if row.Type != TypeQuestion {
		t.Fatalf("the bead is a %s, want a question", row.Type)
	}
	notes, err := y.Notes(y.ctx, bead)
	if err != nil {
		t.Fatal(err)
	}
	asks := ResolveAsks(notes, y.now)
	if len(asks) != 1 || asks[0].AskErr != nil {
		t.Fatalf("the bead holds %d asks, %+v", len(asks), asks)
	}
	ask := asks[0].Ask
	if ask.To != "mckean" || ask.Run == nil {
		t.Fatalf("the ask is to %q with run %+v, want mckean and a card", ask.To, ask.Run)
	}
	if len(ask.Run.Argv) != 1 || ask.Run.Argv[0] != "up" {
		t.Fatalf("the card runs %q, want [up]", ask.Run.Argv)
	}
	if joined := strings.Join(ask.Context, "\n"); !strings.Contains(joined, "daemon_lost") ||
		!strings.Contains(joined, "recorded a daemon_lost event at "+y.now.Local().Format(daemonTimeLayout)) {
		t.Fatalf("the card's context %q names no event and its time", ask.Context)
	}
	again, wrote, err := y.NoteDaemonLost(y.ctx, stopped.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if wrote || again != "" {
		t.Fatalf("a second recording = %q, %v, want nothing while the loss stands", again, wrote)
	}
	if lost, err := y.EventsOfKind(y.ctx, "daemon_lost", 10); err != nil || len(lost) != 1 {
		t.Fatalf("daemon_lost events = %d, %v, want the one", len(lost), err)
	}
	if err := y.LogDispatch(y.ctx, "", "daemon_started", "daemon", map[string]any{"to": "new"}); err != nil {
		t.Fatal(err)
	}
	second, wrote, err := y.NoteDaemonLost(y.ctx, stopped.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !wrote || second == "" || second == bead {
		t.Fatalf("the loss after a start = %q, %v, want a second card", second, wrote)
	}
}

// Where the yard has no operator the loss is still written, and no card
// is raised: the event is what the crew displays read.
func TestNoteDaemonLostWithoutOperator(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	y.noOperator(t, "mckean")
	bead, wrote, err := y.NoteDaemonLost(y.ctx, y.now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !wrote || bead != "" {
		t.Fatalf("NoteDaemonLost = %q, %v, want written and no card", bead, wrote)
	}
	if lost, err := y.EventsOfKind(y.ctx, "daemon_lost", 10); err != nil || len(lost) != 1 {
		t.Fatalf("daemon_lost events = %d, %v", len(lost), err)
	}
}

// A daemon_lost event stands until a start comes after it: a start does,
// a replace does, and an older start does not. The reading answers the
// last known alive time the loss recorded, and the event's own time where
// it records none.
// The episode rule, of events alone: open iff a loss stands with no start
// after it, across every order of lost, started and replaced. Since is
// the last known alive time the loss recorded, and the event's own time
// where it records none.
func TestEpisodeOpen(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)
	pass := at.Add(-time.Hour)
	lost := func(seq uint64) *Event {
		return &Event{Seq: seq, At: at, Data: map[string]any{"stopped_at": pass.Format(time.RFC3339Nano)}}
	}
	start := func(seq uint64) *Event { return &Event{Seq: seq, At: at} }
	for _, c := range []struct {
		name                    string
		lost, started, replaced *Event
		want                    bool
	}{
		{"none", nil, nil, nil, false},
		{"start alone", nil, start(1), nil, false},
		{"replace alone", nil, nil, start(1), false},
		{"loss alone", lost(1), nil, nil, true},
		{"start then loss", lost(2), start(1), nil, true},
		{"loss then start", lost(1), start(2), nil, false},
		{"replace then loss", lost(2), nil, start(1), true},
		{"loss then replace", lost(1), nil, start(2), false},
		{"loss between starts", lost(2), start(1), start(3), false},
		{"loss then a start, replace older", lost(2), start(3), start(1), false},
		{"starts then a loss", lost(3), start(1), start(2), true},
	} {
		since, open := episodeOpen(c.lost, c.started, c.replaced)
		if open != c.want {
			t.Errorf("%s: open = %v, want %v", c.name, open, c.want)
		}
		if open && !since.Equal(pass) {
			t.Errorf("%s: since = %s, want the recorded pass", c.name, since)
		}
	}
	if since, open := episodeOpen(&Event{Seq: 1, At: at}, nil, nil); !open || !since.Equal(at) {
		t.Errorf("a loss recording nothing: since %s, open %v, want its own time and open", since, open)
	}
}

// A daemon_lost event stands until a start comes after it: a start does,
// a replace does, and an older start does not. The reading answers the
// last known alive time the loss recorded, and the event's own time where
// it records none.
func TestDaemonLostEpisode(t *testing.T) {
	t.Parallel()
	y, _ := runYard(t)
	if _, lost, err := DaemonLostEpisode(y.ctx, y.Store); err != nil || lost {
		t.Fatalf("with no events lost = %v, %v", lost, err)
	}
	if err := y.LogDispatch(y.ctx, "", "daemon_started", "daemon", map[string]any{"to": "old"}); err != nil {
		t.Fatal(err)
	}
	if _, lost, err := DaemonLostEpisode(y.ctx, y.Store); err != nil || lost {
		t.Fatalf("with a start alone lost = %v, %v", lost, err)
	}
	stopped := y.now.Add(-time.Hour)
	if _, _, err := y.NoteDaemonLost(y.ctx, stopped); err != nil {
		t.Fatal(err)
	}
	at, lost, err := DaemonLostEpisode(y.ctx, y.Store)
	if err != nil || !lost {
		t.Fatalf("with a loss standing lost = %v, %v", lost, err)
	}
	if !at.Equal(stopped) {
		t.Fatalf("the loss reads since %s, want the last known alive time %s", at, stopped)
	}
	if err := y.LogDispatch(y.ctx, "", "daemon_replaced", "daemon", map[string]any{"from": "old", "to": "new"}); err != nil {
		t.Fatal(err)
	}
	if _, lost, err := DaemonLostEpisode(y.ctx, y.Store); err != nil || lost {
		t.Fatalf("with a replace after the loss lost = %v, %v", lost, err)
	}
	if got := UnknownSince(stopped); got != "unknown since "+stopped.Local().Format(daemonTimeLayout) {
		t.Fatalf("UnknownSince = %q", got)
	}
}
