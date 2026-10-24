package store

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

// A yard from before users has an operator setting and no user. The store
// adds the operator's when it opens, once; a yard whose operator has a
// crew member's name keeps the crew member and gets no user.
func TestOperatorUserOfAnOlderYard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	older := func(t *testing.T, operator string, crew ...string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "y.db")
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range crew {
			if _, err := s.PutCrew(ctx, CrewMember{Name: r, Runner: "herdr"}, "jo"); err != nil {
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
		path := older(t, "jo", "yardmaster")
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		us, err := s.Users(ctx)
		if err != nil || len(us) != 1 || us[0].Name != "jo" || us[0].CreatedBy != "yardr" {
			t.Fatalf("users = %+v, %v", us, err)
		}
		if err := s.Mail(ctx, "jo", "hello", "yardmaster"); err != nil {
			t.Fatal(err)
		}
		head, _ := s.Head(ctx)
		s.Close(ctx)
		// Opened again, nothing is added or logged twice.
		s = open(t, path)
		if again, _ := s.Head(ctx); again != head {
			t.Fatalf("the second open wrote: head %d, was %d", again, head)
		}
		if box, err := s.UserInbox(ctx, "jo", 0, "jo"); err != nil || box.Unread != 1 {
			t.Fatalf("inbox after reopening = %+v, %v", box, err)
		}
	})
	t.Run("a crew member named as the operator stays", func(t *testing.T) {
		s := open(t, older(t, "yardmaster", "yardmaster"))
		if us, err := s.Users(ctx); err != nil || len(us) != 0 {
			t.Fatalf("users = %+v, %v", us, err)
		}
		// Mail to the name is the crew member's.
		if err := s.Mail(ctx, "yardmaster", "hello", "clerk"); err != nil {
			t.Fatal(err)
		}
		if page, err := s.Inbox(ctx, "yardmaster", 0); err != nil || page.Total != 1 {
			t.Fatalf("the crew member's inbox = %+v, %v", page, err)
		}
		// The setting is kept as it is, and a new name gives the operator
		// their user. Until then the operator is that name, and a setting
		// is changed as it.
		if err := s.SetSetting(ctx, OperatorSetting, "jo", "jo"); err == nil ||
			!strings.Contains(err.Error(), "only the operator (yardmaster) may change a setting; this is jo") {
			t.Fatalf("renaming the operator as jo: %v", err)
		}
		if err := s.SetSetting(ctx, OperatorSetting, "yardmaster", "yardmaster"); err != nil {
			t.Fatalf("setting the operator it has: %v", err)
		}
		if err := s.SetSetting(ctx, OperatorSetting, "jo", "yardmaster"); err != nil {
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

// Mail to a user goes with the log as a crew member's does, and its index
// entry with it.
// A yard from before roles (schema 29) has the operator setting as its one
// operator: opened by this build, the user it names is the first operator
// and every other user a member (migrateOperatorRole).
func TestOperatorRoleOfAnOlderYard(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "y.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "ada", "jo"); err != nil {
		t.Fatal(err)
	}
	// As a build before roles left it: no user holds one.
	err = s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range []string{"jo", "ada"} {
			u, err := getUser(tx, name)
			if err != nil {
				return err
			}
			u.Role = ""
			if err := put(tx, bUsers, name, u); err != nil {
				return err
			}
		}
		return tx.Bucket(bMeta).Put(kSchema, binary.BigEndian.AppendUint64(nil, 29))
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
	defer s.Close(ctx)
	us, err := s.Users(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := Operators(us); len(got) != 1 || got[0] != "jo" {
		t.Fatalf("operators after the migration = %v, want jo", got)
	}
	ada, err := s.GetUser(ctx, "ada")
	if err != nil || ada.IsOperator() || ada.RoleOf() != RoleMember {
		t.Fatalf("ada = %+v, %v; want a member", ada, err)
	}
	if _, err := s.AddUser(ctx, "kim", "ada"); err == nil || !strings.Contains(err.Error(), "only the operator (jo) may add a user; this is ada") {
		t.Fatalf("a user add by the member ada = %v", err)
	}
}

func TestUserMailIsIndexedAsMail(t *testing.T) {
	t.Parallel()
	e := Event{Seq: 7, Kind: "mail", Data: map[string]any{"to": "jo", "to_user": true, "text": "x"}}
	if got := inboxOf(e); len(got) != 1 || got[0] != "jo" {
		t.Fatalf("inboxOf = %v", got)
	}
	if mailTo(e) != "" {
		t.Fatal("mail to a user reads as mail to a crew member")
	}
	e.Data["to_peer"] = "north"
	if got := inboxOf(e); len(got) != 0 {
		t.Fatalf("mail that left for a peer is in an inbox here: %v", got)
	}
}
