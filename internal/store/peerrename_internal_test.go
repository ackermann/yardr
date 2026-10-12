package store

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A store from before a link could be renamed (schema 4) has peers that do
// not say since when they are: Open reads it from the log, so the mail of
// an earlier yard that had the name is not the peer's.
func TestMigratePeerSince(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Two yards, one after the other, were north; the first sent mail.
	first := Peer{Name: "north", Key: "a8tUnLBerkUE5hhF7C4ejE-GeV12GYuXpfNf1Jw1grY"}
	second := Peer{Name: "north", Key: "YeV1ykmaiuXGs72Ruw0uce6T4hthRT_I9rmgrcNr5ls"}
	if _, err := s.AddPeer(ctx, first, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "north", PeerAllow{Receive: []string{PeerMail}, Expose: []string{"yardmaster"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	old := PeerMessage{ID: "m1", Kind: PeerMail, Body: []byte(`{"to":"yardmaster","from":"clerk","text":"from the first north"}`)}
	if got, err := s.PeerReceive(ctx, "north", first.Key, []PeerMessage{old}); err != nil || got[0].Refused != "" {
		t.Fatalf("PeerReceive = %+v, %v", got, err)
	}
	if err := s.DeletePeer(ctx, "north", "jo"); err != nil {
		t.Fatal(err)
	}
	added, err := s.AddPeer(ctx, second, "jo")
	if err != nil {
		t.Fatal(err)
	}
	// As a build before the rename left it: a record without since.
	err = s.db.Update(func(tx *bolt.Tx) error {
		type was struct {
			Name    string    `json:"name"`
			Key     string    `json:"key"`
			Send    []string  `json:"send"`
			Receive []string  `json:"receive"`
			AddedAt time.Time `json:"added_at"`
		}
		if err := put(tx, bPeers, "north", was{Name: "north", Key: second.Key, Send: []string{}, Receive: []string{}, AddedAt: added.AddedAt}); err != nil {
			return err
		}
		return tx.Bucket(bMeta).Put(kSchema, binary.BigEndian.AppendUint64(nil, 4))
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
	p, err := s.GetPeer(ctx, "north")
	if err != nil || p.Since == 0 || p.Since != added.Since || p.Key != second.Key {
		t.Fatalf("the migrated peer = %+v, %v; want since %d", p, err, added.Since)
	}
	if ls, _, err := s.Conversation(ctx, "yardmaster", "clerk@north", 0, 0, 0); err != nil || len(ls) != 0 {
		t.Fatalf("the second north's conversation = %+v, %v; want none of the first's", ls, err)
	}
	// Opened again, nothing is migrated twice.
	if err := s.db.View(func(tx *bolt.Tx) error { return migratePeerSince(tx, stampedSchema(tx)) }); err != nil {
		t.Fatal(err)
	}
}
