package store

import (
	"context"
	"path/filepath"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// A yard from before users has an operator setting and no user. The store
// adds the operator's when it opens, once; a yard whose operator has a
// resident's name keeps the resident and gets no user.
func TestOperatorUserOfAnOlderYard(t *testing.T) {
	ctx := context.Background()
	older := func(t *testing.T, operator string, residents ...string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "y.db")
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range residents {
			if _, err := s.PutResident(ctx, Resident{Name: r, Runner: "herdr"}, "jo"); err != nil {
				t.Fatal(err)
			}
		}
		// As a build before users left it: the setting, and no user.
		err = s.db.Update(func(tx *bolt.Tx) error {
			if err := put(tx, bSettings, OperatorSetting, operator); err != nil {
				return err
			}
			return tx.DeleteBucket(bUsers)
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Close(ctx); err != nil {
			t.Fatal(err)
		}
		return path
	}
	open := func(t *testing.T, path string) *Store {
		t.Helper()
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { s.Close(ctx) })
		return s
	}

	t.Run("gets the user", func(t *testing.T) {
		path := older(t, "jo", "mayor")
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		us, err := s.Users(ctx)
		if err != nil || len(us) != 1 || us[0].Name != "jo" || us[0].CreatedBy != "yardr" {
			t.Fatalf("users = %+v, %v", us, err)
		}
		if err := s.Mail(ctx, "jo", "hello", "mayor"); err != nil {
			t.Fatal(err)
		}
		head, _ := s.Head(ctx)
		s.Close(ctx)
		// Opened again, nothing is added or logged twice.
		s = open(t, path)
		if again, _ := s.Head(ctx); again != head {
			t.Fatalf("the second open wrote: head %d, was %d", again, head)
		}
		if box, err := s.UserInbox(ctx, "jo", 0); err != nil || box.Unread != 1 {
			t.Fatalf("inbox after reopening = %+v, %v", box, err)
		}
	})
	t.Run("a resident named as the operator stays", func(t *testing.T) {
		s := open(t, older(t, "mayor", "mayor"))
		if us, err := s.Users(ctx); err != nil || len(us) != 0 {
			t.Fatalf("users = %+v, %v", us, err)
		}
		// Mail to the name is the resident's.
		if err := s.Mail(ctx, "mayor", "hello", "clerk"); err != nil {
			t.Fatal(err)
		}
		if page, err := s.Inbox(ctx, "mayor", 0); err != nil || page.Total != 1 {
			t.Fatalf("the resident's inbox = %+v, %v", page, err)
		}
		// The setting is kept as it is, and a new name gives the operator
		// their user.
		if err := s.SetSetting(ctx, OperatorSetting, "mayor", "jo"); err != nil {
			t.Fatalf("setting the operator it has: %v", err)
		}
		if err := s.SetSetting(ctx, OperatorSetting, "jo", "jo"); err != nil {
			t.Fatal(err)
		}
		if us, _ := s.Users(ctx); len(us) != 1 || us[0].Name != "jo" {
			t.Fatalf("users after the rename = %+v", us)
		}
	})
	t.Run("no operator, no user", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "y.db")
		s := open(t, path)
		if us, _ := s.Users(ctx); len(us) != 0 {
			t.Fatalf("users = %+v", us)
		}
	})
}

// Mail to a user goes with the log as a resident's does, and its index
// entry with it.
func TestUserMailIsIndexedAsMail(t *testing.T) {
	e := Event{Seq: 7, Kind: "mail", Data: map[string]any{"to": "jo", "to_user": true, "text": "x"}}
	if got := inboxOf(e); len(got) != 1 || got[0] != "jo" {
		t.Fatalf("inboxOf = %v", got)
	}
	if mailTo(e) != "" {
		t.Fatal("mail to a user reads as mail to a resident")
	}
	e.Data["to_peer"] = "north"
	if got := inboxOf(e); len(got) != 0 {
		t.Fatalf("mail that left for a peer is in an inbox here: %v", got)
	}
}
