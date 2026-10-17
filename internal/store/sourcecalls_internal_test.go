package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/source"
)

// The answer of a call is not noted on a bead that came from another yard,
// whose notes go back to that yard with its return: held by the record of
// the bead's arrival, apart from the refusal of every call of such a
// session, so that it stands when a source is opened to a peer's beads. A
// bead of this yard's own, and one it sent away, take the note.
func TestSourceNoteRefusedOnABeadFromAPeer(t *testing.T) {
	t.Parallel()
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	err = s.write(func(w *wtx) error {
		if err := put(w.Tx, bPeerBeads, "u-in", peerBead{Bead: "u-in", Direction: "in"}); err != nil {
			return err
		}
		return put(w.Tx, bPeerBeads, "u-out", peerBead{Bead: "u-out", Direction: "out"})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.read(func(tx *bolt.Tx) error {
		of := func(bead string) *sourceCaller {
			return &sourceCaller{sess: &Session{ID: "s1", Group: "builders", Bead: bead}}
		}
		bead, why, status, err := of("u-in").noteBead(tx)
		if err != nil || bead != "" || status != CallFromPeer || !strings.Contains(why, "came from another yard, and its notes go back to that yard") {
			t.Errorf("a bead from a peer = %q, %q, %q, %v", bead, why, status, err)
		}
		for _, own := range []string{"u-own", "u-out"} {
			if bead, why, _, err := of(own).noteBead(tx); err != nil || bead != own || why != "" {
				t.Errorf("%s = %q, %q, %v", own, bead, why, err)
			}
		}
		if _, why, status, _ := (&sourceCaller{crew: &CrewMember{Name: "yardmaster"}}).noteBead(tx); status != source.BadParam || !strings.Contains(why, "crew member yardmaster works no bead") {
			t.Errorf("a crew member = %q, %q", why, status)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The key of an update is kept by caller, the user called as, call and the
// values given, and for SourceKeyKept after its last try: the same call
// tried again within that has the key and counts the try, and one tried
// later, or with another value, or by another caller, or as another user,
// has a new one. A plain update's key is the string it has always been.
// Removing the source drops its keys.
func TestSourceUpdateKeyIsKeptForASecondTry(t *testing.T) {
	// Sequential: the package's fake clock.
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	try := func(caller, call string, params map[string]string, as string) *updateKey {
		t.Helper()
		var u *updateKey
		if err := s.write(func(w *wtx) (err error) {
			u, err = w.keyed(updateKeyOf(join("crm", "session", caller), call, params, as))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return u
	}
	note := map[string]string{"id": "7", "text": "called back"}
	first := try("s1", "note", note, "ada")
	at = at.Add(time.Hour)
	if again := try("s1", "note", note, "ada"); again.Key != first.Key || again.Attempts != 2 || len(first.Key) != 36 {
		t.Fatalf("the same call again as ada = %+v, first %+v", again, first)
	}
	for name, other := range map[string]*updateKey{
		"another value":       try("s1", "note", map[string]string{"id": "7", "text": "called back."}, "ada"),
		"another call":        try("s1", "close", note, "ada"),
		"another caller":      try("s2", "note", note, "ada"),
		"another user called": try("s1", "note", note, "bia"),
		"no user called as":   try("s1", "note", note, ""),
		// A value cannot be made to read as a second parameter.
		"a value with the separator in it": try("s1", "note", map[string]string{"id": "7\x00text\x00called back"}, "ada"),
	} {
		if other.Key == first.Key || other.Attempts != 1 {
			t.Errorf("%s has %+v, the first call %+v", name, other, first)
		}
	}
	// A plain update's key is byte for byte the key it was before a call
	// could name a user.
	if got, want := updateKeyOf(join("crm", "session", "s1"), "note", note, ""), "crm\x00session\x00s1\x00cbeb1ea6d3c051996e8bbbba6277bc17b9e1675112574fa90607f8baf5dd482c"; got != want {
		t.Fatalf("a plain update's key = %q, want %q", got, want)
	}
	// The text of a note is not what the key is kept under.
	_ = s.read(func(tx *bolt.Tx) error {
		for _, k := range keys(tx, bSourceKeys, "crm\x00") {
			if strings.Contains(k, "called back") {
				t.Errorf("a key is kept under the call's values: %q", k)
			}
		}
		return nil
	})
	at = at.Add(SourceKeyKept)
	if late := try("s1", "note", note, "ada"); late.Key == first.Key || late.Attempts != 1 {
		t.Fatalf("the same call after %s = %+v, first %+v", SourceKeyKept, late, first)
	}
	err = s.write(func(w *wtx) error {
		if n := len(keys(w.Tx, bSourceKeys, "crm\x00")); n != 1 {
			t.Errorf("%d keys are kept, want the one tried last: the others are older than %s", n, SourceKeyKept)
		}
		if err := dropSourceRecords(w.Tx, "crm"); err != nil {
			return err
		}
		if n := len(keys(w.Tx, bSourceKeys, "")); n != 0 {
			t.Errorf("%d keys are left of a source that was removed", n)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
