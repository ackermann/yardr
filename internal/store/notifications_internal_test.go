package store

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// A store from before notifications has one cursor per user, for all of
// the inbox. Opening it gives the user the second, at the first: what was
// acknowledged stays acknowledged in both parts, and from then on each
// part moves by itself. It happens once.
func TestUserCursorOfAnOlderStore(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "y.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"one", "two", "three"} {
		if err := s.Mail(ctx, "ada", text, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	box, err := s.UserInbox(ctx, "ada", 0)
	if err != nil || box.Unread != 3 {
		t.Fatalf("inbox = %+v, %v", box, err)
	}
	read := box.Mail[1].Seq
	if err := s.AckUserInbox(ctx, "ada", read); err != nil {
		t.Fatal(err)
	}
	// As a build before notifications left it: the user's record with its
	// one cursor, stamped schema 3.
	err = s.db.Update(func(tx *bolt.Tx) error {
		type old struct {
			Name   string `json:"name"`
			Cursor uint64 `json:"cursor"`
		}
		if err := put(tx, bUsers, "ada", old{Name: "ada", Cursor: read}); err != nil {
			return err
		}
		if err := tx.DeleteBucket(bNotifications); err != nil {
			return err
		}
		return tx.Bucket(bMeta).Put(kSchema, binary.BigEndian.AppendUint64(nil, 3))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.GetUser(ctx, "ada")
	if err != nil || u.Cursor != read || u.NotificationCursor != read {
		t.Fatalf("migrated user = %+v, %v; want both cursors at %d", u, err, read)
	}
	if box, _ := s.UserInbox(ctx, "ada", 0); box.Unread != 1 {
		t.Fatalf("mail after the migration: %d unread; want 1", box.Unread)
	}
	// The parts are apart from here on: a notification arrives unread, and
	// acknowledging the mail does not read it.
	if err := s.AddSubscription(ctx, "ada", Subscription{Source: SourceYardr, Scope: ScopeAll}, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateBead(ctx, NewBead{Title: "x"}, "jo"); err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	if err := s.AckUserInbox(ctx, "ada", head); err != nil {
		t.Fatal(err)
	}
	if page, err := s.UserNotifications(ctx, "ada", 0); err != nil || page.Unread != 1 {
		t.Fatalf("notifications = %+v, %v; want one unread", page, err)
	}
	if err := s.Close(ctx); err != nil {
		t.Fatal(err)
	}

	// Stamped 4, it is not migrated again: the mail's cursor has moved on
	// and the notifications' has not followed.
	if s, err = Open(path); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	if u, _ := s.GetUser(ctx, "ada"); u.Cursor != head || u.NotificationCursor != read {
		t.Fatalf("after a second open = %+v; want mail at %d, notifications at %d", u, head, read)
	}
}
