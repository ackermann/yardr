package store

import (
	"errors"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// migrateNotificationsTakebackHeld remakes every notification of a user who
// is not the operator from what its person is told of the event (schema 33):
// rows written before the hold of old login-takeback sentences kept the
// sentence whole, carrying the tail of the login command. The remake is 31's:
// operator rows untouched, hook rows untouched, pruned-event rows bare. What
// changed is WithoutHeld, which now reads an old unmarked login takeback as
// held exactly as if it were marked, so the remade rows read as the event
// without its sentence. The asker's old rows are remade told like every
// other non-operator row: resolving askers for history would add a bead-notes
// lookup to a migration for an edge case whose failures land on told anyway.
func migrateNotificationsTakebackHeld(tx *bolt.Tx, from uint64) error {
	if from >= 33 {
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
