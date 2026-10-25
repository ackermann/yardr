// The server's notice of a dead daemon: one daemon_lost event with a run
// card to bring the yard back up, and the reading of that state every
// crew display shares.
//
// Only the daemon checks crew liveness, so a dead daemon leaves every crew
// session's stored state behind as it was: running, idle, working. The
// server, which the machine's service manager keeps up, watches the
// daemon's last pass and latest start (internal/server). When the newer
// of those is over the threshold old, and no daemon holds the lock, it
// calls NoteDaemonLost: one
// daemon_lost event beside the daemon's own daemon_started, and a run
// ask for yardr up on a new bead.
// A fresh daemon_started (or daemon_replaced) ends the state, so a second
// stop raises a second card while nothing new stands in between.
//
// daemon_lost is the server's own event, as server_replaced is: it is
// written here, in the owning process, and is no DispatchKind, so no
// socket client may pass one off as the yard's own. It is of no attention
// class either: the crew that would hear it is what is down, and the card
// tells the operator through their decisions.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// daemonTimeLayout is the daemon's last known alive time as the yard prints
// it: on the lost card's question and in every crew display that names it.
const daemonTimeLayout = "01-02 15:04"

// UnknownSince is a crew session as the displays list it while the daemon
// is lost: "unknown since 01-02 15:04", of the daemon's last known alive
// time, instead of a stale running or idle.
func UnknownSince(lastAlive time.Time) string {
	return "unknown since " + lastAlive.Local().Format(daemonTimeLayout)
}

// DaemonLostEpisode reads the yard's lost-daemon episode, if one stands:
// open iff the latest daemon_lost is newer than the latest
// daemon_started or daemon_replaced. It answers since, the last known
// alive time the loss recorded, and whether the episode is open. One
// mechanism serves both the server's watch (no second card while one
// stands) and every crew display (unknown since then).
func DaemonLostEpisode(ctx context.Context, st API) (time.Time, bool, error) {
	lost, err := latestEventOfKind(ctx, st, "daemon_lost")
	if err != nil {
		return time.Time{}, false, err
	}
	started, err := latestEventOfKind(ctx, st, "daemon_started")
	if err != nil {
		return time.Time{}, false, err
	}
	replaced, err := latestEventOfKind(ctx, st, "daemon_replaced")
	if err != nil {
		return time.Time{}, false, err
	}
	since, open := episodeOpen(lost, started, replaced)
	return since, open, nil
}

// episodeOpen is the episode rule, of the newest event of each kind: open
// iff a loss stands with no start after it. Since is the last known alive
// time the loss recorded, and the event's own time where it records none.
func episodeOpen(lost, started, replaced *Event) (time.Time, bool) {
	if lost == nil {
		return time.Time{}, false
	}
	for _, start := range []*Event{started, replaced} {
		if start != nil && start.Seq > lost.Seq {
			return time.Time{}, false
		}
	}
	return episodeSince(lost), true
}

// episodeSince is when the open loss dates from: the last known alive
// time it recorded, else its own time.
func episodeSince(lost *Event) time.Time {
	if raw, _ := lost.Data["stopped_at"].(string); raw != "" {
		if at, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return at
		}
		if at, err := time.Parse(time.RFC3339, raw); err == nil {
			return at
		}
	}
	return lost.At
}

// LastDaemonStart is when the daemon last started, of the newest
// daemon_started or daemon_replaced event, false where the log holds
// neither: a yard whose daemon never started.
func LastDaemonStart(ctx context.Context, st API) (time.Time, bool, error) {
	var at time.Time
	var seq uint64
	found := false
	for _, kind := range []string{"daemon_started", "daemon_replaced"} {
		evs, err := st.EventsOfKind(ctx, kind, 1)
		if err != nil {
			return time.Time{}, false, err
		}
		if len(evs) > 0 && (!found || evs[0].Seq > seq) {
			at, seq, found = evs[0].At, evs[0].Seq, true
		}
	}
	return at, found, nil
}

// latestEventOfKind is the newest event of kind, nil where the log holds
// none.
func latestEventOfKind(ctx context.Context, st API, kind string) (*Event, error) {
	evs, err := st.EventsOfKind(ctx, kind, 1)
	if err != nil {
		return nil, err
	}
	if len(evs) == 0 {
		return nil, nil
	}
	return &evs[0], nil
}

// NoteDaemonLost records that the yard's daemon is lost. stoppedAt is the
// latest known time it was alive: its last pass or a newer start. The
// caller invokes this only after the time is over its threshold and the
// daemon lock is free. In one write it records a daemon_lost event and
// raises a run ask for yardr up to the operator on a new question bead.
// It answers the bead of
// the card it raised and whether it wrote: where an episode already
// stands, it writes nothing and answers "", false: one event per loss,
// not one per check. Where the yard has no operator the event is still
// written, and no card is raised.
//
// The server raises it, not a crew member: the crew is what the daemon
// runs, so none of it is there to ask. Like a claim's card it is built
// here, past the writer check a run ask from a pane goes through.
func (s *Store) NoteDaemonLost(ctx context.Context, stoppedAt time.Time) (string, bool, error) {
	when := stoppedAt.Local().Format(daemonTimeLayout)
	question := fmt.Sprintf("The daemon of this yard was last alive at %s. Start it?", when)
	create, err := s.newBead(ctx, NewBead{Title: question, Type: TypeQuestion}, stageAskAuthor)
	if err != nil {
		return "", false, err
	}
	c, up, err := parseRow([]string{"up"})
	if err != nil {
		return "", false, err
	}
	var bead string
	wrote := false
	err = s.write(func(w *wtx) error {
		lost := lastEvent(w.Tx, "daemon_lost", func(Event) bool { return true })
		started := lastEvent(w.Tx, "daemon_started", func(Event) bool { return true })
		replaced := lastEvent(w.Tx, "daemon_replaced", func(Event) bool { return true })
		if _, open := episodeOpen(lost, started, replaced); open {
			return nil
		}
		recorded := now()
		if err := w.event("", "daemon_lost", stageAskAuthor, map[string]any{
			"stopped_at": stoppedAt.UTC().Format(time.RFC3339Nano),
		}); err != nil {
			return err
		}
		wrote = true
		var operator string
		if _, err := get(w.Tx, bSettings, OperatorSetting, &operator); err != nil {
			return err
		}
		if operator == "" {
			return nil
		}
		if _, err := getUser(w.Tx, operator); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil
			}
			return err
		}
		card, err := w.upCard(up, true)
		if err != nil {
			return err
		}
		card.Description = c.description
		row, err := create(w)
		if err != nil {
			return err
		}
		ask := Ask{Question: question, To: operator, Context: []string{
			fmt.Sprintf("The daemon was last alive at %s, and no daemon holds the yard's lock.", when),
			fmt.Sprintf("The server recorded a daemon_lost event at %s; approving runs yardr up, which starts what is not running and leaves the rest alone.", recorded.Local().Format(daemonTimeLayout)),
		}, Run: &card}
		if ask.Weight, err = w.weighAsk(ask); err != nil {
			return err
		}
		text, err := json.Marshal(ask)
		if err != nil {
			return err
		}
		n, err := w.addNoted(row, NoteAsk, string(text), stageAskAuthor)
		if err != nil {
			return err
		}
		if err := w.queueAskPush(operator, row.UUID, n.Seq, ""); err != nil {
			return err
		}
		bead = row.ID
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return bead, wrote, nil
}
