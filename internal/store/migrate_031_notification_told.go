package store

import (
	"errors"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// migrateNotificationsTold remakes every notification of a user who is not
// the operator from what its person is told of the event (schema 31): rows
// written before the hold kept the whole event as key=value pairs, so a
// worktree, a command or error words stayed readable to a lost token. An
// operator's rows are left whole, as the writer leaves them, and hook rows
// (Source is the hook's, not yardr's) are left too: the writer tells those
// whole to every user they reach. A row whose event is still kept is remade
// as the writer remakes it (toldNotification): bead rows from the current
// bead row, whose title may have changed since, and beads gone since in the
// yard form. A row whose event was pruned is remade bare, from the row's
// Kind, Bead, Reason and Title alone, with no event data at all.
func migrateNotificationsTold(tx *bolt.Tx, from uint64) error {
	if from >= 31 {
		return nil
	}
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	for _, k := range keys(tx, bNotifications, "") {
		user, _, _ := strings.Cut(k, "\x00")
		if ops.is(user) {
			continue
		}
		var n Notification
		if ok, err := get(tx, bNotifications, k, &n); err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if n.Source != SourceYardr {
			continue
		}
		var e Event
		if ok, err := get(tx, bEvents, seqKey(n.Seq), &e); err != nil {
			return err
		} else if !ok {
			n.Text = bareNotificationText(n)
			if err := put(tx, bNotifications, k, n); err != nil {
				return err
			}
			continue
		}
		var remake func(*Event) Notification
		var row *beadRow
		if n.Bead != "" {
			r, err := loadBead(tx, n.Bead)
			if err == nil {
				row = r
				remake = func(held *Event) Notification { return beadNotification(held, row) }
			} else if errors.Is(err, ErrNotFound) || errors.Is(err, ErrAmbiguous) {
				remake = yardNotification
			} else {
				return err
			}
		} else {
			remake = yardNotification
		}
		told := toldNotification(&e, n, remake)
		told.Reason = n.Reason
		told.Text = told.line(told.Text)
		n.Text = told.Text
		if row != nil {
			n.Title = told.Title
			n.Commit = told.Commit
		}
		if err := put(tx, bNotifications, k, n); err != nil {
			return err
		}
	}
	return nil
}

// bareNotificationText is a notification whose event was pruned as the
// migration remakes it: the bead, the kind and the delivery reason, then
// the bead's title, and nothing of the event. The actor and every key=value
// pair are gone with the log, so they cannot be told in part.
func bareNotificationText(n Notification) string {
	var row strings.Builder
	if n.Bead != "" {
		row.WriteString(n.Bead + " ")
	}
	row.WriteString(n.Kind + " (" + n.Reason + ")")
	out := clip(oneLine(row.String()), maxNotificationLine)
	if n.Title != "" {
		out += fmt.Sprintf(" on %q", clip(oneLine(n.Title), maxNotificationTitle))
	}
	return out
}
