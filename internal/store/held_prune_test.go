package store

import (
	"bytes"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// heldCardOf is the open mapping card waiting for sender, as the
// operator's queue holds it.
func heldCardOf(t *testing.T, y *askYard, sender string) OpenAsk {
	t.Helper()
	q := mappingQueue(t, y)
	for _, o := range q.Open {
		if o.Identity != nil && o.Identity.Sender == sender {
			return o
		}
	}
	t.Fatalf("no open mapping card for %s among %v", sender, q.Open)
	return OpenAsk{}
}

// holdFor holds one delivery from sender on its mapping card, with the
// dedupe key key.
func holdFor(t *testing.T, y *askYard, sender, body, key string) {
	t.Helper()
	env := map[string]any{"fields": map[string]any{"sender": []string{sender}}}
	if _, err := y.HoldIdentity(y.ctx, "talk", "talk", sender, "C1", env, []byte(body), []string{key}); err != nil {
		t.Fatal(err)
	}
}

// Prune ages held deliveries with the hook payloads: one past the
// retention goes, the card it leaves holding nothing closes and is
// counted, a younger delivery on the same terms stays, and a card that
// never held anything survives however old it is.
func TestPruneHeldDeliveries(t *testing.T) {
	y := mappingYard(t)
	holdFor(t, y, "OLD1", "old body", "old-1")
	holdFor(t, y, "PART", "part old", "part-1")
	if _, err := y.ClaimIdentity(y.ctx, "talk", "NEVER", "C1", ""); err != nil {
		t.Fatal(err)
	}
	oldCard := heldCardOf(t, y, "OLD1")
	partCard := heldCardOf(t, y, "PART")
	neverCard := heldCardOf(t, y, "NEVER")

	y.now = y.now.Add(20 * 24 * time.Hour)
	holdFor(t, y, "PART", "part young", "part-2")
	holdFor(t, y, "YOUNG1", "young body", "young-1")
	youngCard := heldCardOf(t, y, "YOUNG1")
	if got := heldCardOf(t, y, "PART"); got.Identity.Held != 2 {
		t.Fatalf("the part card holds %+v, want 2 waiting", got.Identity)
	}

	y.now = y.now.Add(11 * 24 * time.Hour) // 31 days after the first holds
	res, err := y.Prune(y.ctx, Retention{HookPayloads: 30 * 24 * time.Hour}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	if res.Held != 2 || res.HeldCards != 1 {
		t.Fatalf("pruned %+v, want 2 held deliveries and 1 card closed", res)
	}
	// The emptied card closed; its ask left the queue.
	wantCardClosed(t, y, oldCard.Bead, "its held messages went in a prune")
	// The card with a younger delivery stays open holding that one.
	if got := heldCardOf(t, y, "PART"); got.Bead != partCard.Bead || got.Identity.Held != 1 {
		t.Fatalf("the part card after prune = %+v, want the same card holding 1", got)
	}
	// The young card is untouched.
	if got := heldCardOf(t, y, "YOUNG1"); got.Bead != youngCard.Bead || got.Identity.Held != 1 {
		t.Fatalf("the young card after prune = %+v, want it holding 1", got)
	}
	// The card that never held anything survives, open.
	if b, err := y.GetBead(y.ctx, neverCard.Bead); err != nil || b.Status != "open" {
		t.Fatalf("the never-held card = %+v, %v, want open", b, err)
	}
	// A second prune on the same terms removes nothing more.
	res, err = y.Prune(y.ctx, Retention{HookPayloads: 30 * 24 * time.Hour}, "mckean")
	if err != nil || res.Held != 0 || res.HeldCards != 0 {
		t.Fatalf("second prune = %+v, %v, want nothing held removed", res, err)
	}
	if b, err := y.GetBead(y.ctx, neverCard.Bead); err != nil || b.Status != "open" {
		t.Fatalf("the never-held card after both prunes = %+v, %v, want open", b, err)
	}
}

// heldBytes is what the store counts as held for source.
func heldBytes(t *testing.T, y *askYard, source string) int64 {
	t.Helper()
	var used int64
	if err := y.read(func(tx *bolt.Tx) (err error) {
		used, err = heldBytesForSource(tx, source)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return used
}

// The held bytes a source's strangers use are counted as each card's
// list is written, so the bound is read without visiting every card:
// a sender rotating identities must not make each later delivery cost
// a read of all that is held. The count follows holds, a nobody pick
// and a prune, and stands where a card's notes are not read at all.
func TestHeldBytesCountedPerWrite(t *testing.T) {
	y := mappingYard(t)
	holdFor(t, y, "A1", "aaaa", "a-1")
	holdFor(t, y, "B1", "bbbbbb", "b-1")
	if got := heldBytes(t, y, "talk"); got != 10 {
		t.Fatalf("held bytes after two holds = %d, want 10", got)
	}
	card := heldCardOf(t, y, "A1")
	if err := y.AnswerAsk(y.ctx, card.Bead, card.Ask, pickFor(t, y, card.Bead, MappingNobody), "", "mckean", false); err != nil {
		t.Fatal(err)
	}
	if got := heldBytes(t, y, "talk"); got != 6 {
		t.Fatalf("held bytes after a nobody pick = %d, want 6", got)
	}
	y.now = y.now.Add(31 * 24 * time.Hour)
	if _, err := y.Prune(y.ctx, Retention{HookPayloads: 30 * 24 * time.Hour}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if got := heldBytes(t, y, "talk"); got != 0 {
		t.Fatalf("held bytes after the prune = %d, want 0", got)
	}

	holdFor(t, y, "C1", "cc", "c-1")
	c := heldCardOf(t, y, "C1")
	if err := y.db.Update(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, c.Bead)
		if err != nil {
			return err
		}
		cur := tx.Bucket(bNotes).Cursor()
		prefix := []byte(join(row.UUID, ""))
		for k, _ := cur.Seek(prefix); k != nil && bytes.HasPrefix(k, prefix); k, _ = cur.Seek(prefix) {
			if err := cur.Delete(); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := heldBytes(t, y, "talk"); got != 2 {
		t.Fatalf("held bytes with the card's notes unread = %d, want 2", got)
	}
}
