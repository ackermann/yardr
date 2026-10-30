package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

// pruneYard is a store whose clock the test moves, linked with north, which
// may send and be sent pings and mail.
type pruneYard struct {
	*Store
	at  time.Time
	key string
}

// newPruneYard sets the package's fake clock to the yard's now, so its
// callers stay sequential.
func newPruneYard(t *testing.T) *pruneYard {
	t.Helper()
	y := &pruneYard{at: time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.at }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	y.Store = s
	k := newKey(t)
	y.key = Public(k)
	ctx := context.Background()
	if _, err := s.AddPeer(ctx, Peer{Name: "north", Key: y.key, URL: "http://north.test"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AllowPeer(ctx, "north", PeerAllow{Send: PeerKinds, Receive: PeerKinds}, "jo"); err != nil {
		t.Fatal(err)
	}
	return y
}

// take hands in a ping of id as north posted it, and says whether it was
// dropped as one taken before.
func (y *pruneYard) take(t *testing.T, id string) bool {
	t.Helper()
	got, err := y.PeerReceive(context.Background(), "north", y.key, []PeerMessage{{ID: id, Kind: PeerPing, Re: "x", Body: json.RawMessage(`{}`)}})
	if err != nil || len(got) != 1 || got[0].Refused != "" {
		t.Fatalf("PeerReceive(%s) = %+v, %v", id, got, err)
	}
	return got[0].Duplicate
}

func (y *pruneYard) prune(t *testing.T, events time.Duration) *PruneResult {
	t.Helper()
	res, err := y.Prune(context.Background(), Retention{Events: events}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (y *pruneYard) bucket(t *testing.T, b []byte) (ks []string) {
	t.Helper()
	if err := y.read(func(tx *bolt.Tx) error {
		ks = keys(tx, b, "")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return ks
}

const day = 24 * time.Hour

// An id taken from a peer is forgotten by the prune once it is older than
// the retention of events, and never before PeerKeep: the same id is then
// taken again as new. A younger one stays, and is still dropped as the
// duplicate it is.
func TestPruneForgetsSeenPeerIDs(t *testing.T) {
	// Sequential: the package's fake clock (via newPruneYard).
	y := newPruneYard(t)
	if y.take(t, "old") {
		t.Fatal("the first of an id was dropped as a duplicate")
	}
	y.at = y.at.Add(3 * day)
	y.take(t, "young")

	// Six days on, with events kept for an hour: the floor holds.
	y.at = y.at.Add(3 * day)
	if res := y.prune(t, time.Hour); res.Peers != nil || len(y.bucket(t, bPeerSeen)) != 2 {
		t.Fatalf("a prune before PeerKeep removed %+v, seen = %q", res.Peers, y.bucket(t, bPeerSeen))
	}
	// Eight days on: not while events are kept longer, nor while they are
	// kept for good.
	y.at = y.at.Add(2 * day)
	for _, keep := range []Retention{{Events: 90 * day}, {HookPayloads: time.Hour}} {
		res, err := y.Prune(context.Background(), keep, "jo")
		if err != nil || res.Peers != nil || len(y.bucket(t, bPeerSeen)) != 2 {
			t.Fatalf("Prune(%+v) removed %+v, %v", keep, res, err)
		}
	}
	res := y.prune(t, day)
	if len(res.Peers) != 1 || res.Peers["north"] != (PeerPruned{Seen: 1}) {
		t.Fatalf("pruned of peers = %+v, want one seen id of north", res.Peers)
	}
	if !y.take(t, "young") {
		t.Fatal("an id five days old was forgotten: taken twice")
	}
	if y.take(t, "old") {
		t.Fatal("an id past the bound is still remembered after the prune")
	}
	// The prune's event counts by peer, and names no id.
	evs, err := y.Events(context.Background(), "", 50)
	if err != nil {
		t.Fatal(err)
	}
	var pruned *Event
	for i, e := range evs {
		if e.Kind == "store_pruned" {
			pruned = &evs[i]
		}
	}
	if pruned == nil {
		t.Fatal("no store_pruned event")
	}
	raw, _ := json.Marshal(pruned.Data)
	if !strings.Contains(string(raw), `"peers":{"north":{"seen":1}}`) || strings.Contains(string(raw), "old") {
		t.Fatalf("store_pruned = %s", raw)
	}
	// Nothing of the link is left to remove.
	if res := y.prune(t, day); res.Peers != nil {
		t.Fatalf("a second prune removed %+v", res)
	}
}

// Only a prune that removes something of a link says so: one that removes
// seen ids and nothing else still logs store_pruned.
func TestPruneOfPeersAloneIsLogged(t *testing.T) {
	// Sequential: the package's fake clock (via newPruneYard).
	y := newPruneYard(t)
	y.take(t, "old")
	y.at = y.at.Add(8 * day)
	// A running crew member holds the whole log back, so no event goes.
	ctx := context.Background()
	if _, err := y.PutCrew(ctx, CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	y.at = y.at.Add(-9 * day)
	if err := y.CrewStarted(ctx, "yardmaster", nil); err != nil {
		t.Fatal(err)
	}
	y.at = y.at.Add(9 * day)
	res := y.prune(t, day)
	if res.Events != 0 || res.Peers["north"].Seen != 1 {
		t.Fatalf("pruned %+v, want no events and one seen id", res)
	}
	evs, _ := y.Events(ctx, "", 1)
	if len(evs) != 1 || evs[0].Kind != "store_pruned" {
		t.Fatalf("the last event = %+v, want store_pruned", evs)
	}
}

// A message given up goes from the outbox, body and all, once it has been
// given up for longer than the bound. One that waits is never removed by
// its age.
func TestPruneRemovesPeerMessagesGivenUp(t *testing.T) {
	// Sequential: the package's fake clock (via newPruneYard).
	y := newPruneYard(t)
	ctx := context.Background()
	send := func() *PeerOut {
		t.Helper()
		m, err := y.PeerSend(ctx, "north", PeerPing, json.RawMessage(`{"note":"a body to lose"}`), "jo")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	waits, lost, late := send(), send(), send()
	if err := y.PeerSendFailed(ctx, "north", waits.Seq, "connection refused", y.at.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if err := y.PeerSendFailed(ctx, "north", lost.Seq, "connection refused", y.at, true); err != nil {
		t.Fatal(err)
	}
	// Counted from when it was given up, not from when it was written.
	y.at = y.at.Add(3 * day)
	if err := y.PeerSendFailed(ctx, "north", late.Seq, "connection refused", y.at, true); err != nil {
		t.Fatal(err)
	}

	y.at = y.at.Add(3 * day)
	if res := y.prune(t, time.Hour); res.Peers != nil {
		t.Fatalf("a prune before PeerKeep removed %+v", res.Peers)
	}
	if p, _ := y.GetPeer(ctx, "north"); p.Failed != 2 || p.Waiting != 1 {
		t.Fatalf("before the bound: %d failed, %d waiting; want 2, 1", p.Failed, p.Waiting)
	}
	y.at = y.at.Add(2 * day)
	if res := y.prune(t, day); len(res.Peers) != 1 || res.Peers["north"] != (PeerPruned{Failed: 1}) {
		t.Fatalf("pruned of peers = %+v, want one message given up for north", res.Peers)
	}
	if p, _ := y.GetPeer(ctx, "north"); p.Failed != 1 || p.Waiting != 1 {
		t.Fatalf("after the prune: %d failed, %d waiting; want 1, 1", p.Failed, p.Waiting)
	}
	want := []string{join("north", seqKey(waits.Seq)), join("north", seqKey(late.Seq))}
	if got := y.bucket(t, bPeerOut); len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("the outbox holds %q, want %q", got, want)
	}
	// However old, what waits stays.
	y.at = y.at.Add(400 * day)
	y.prune(t, day)
	if out, err := y.PeerOutbox(ctx, "north", 0); err != nil || len(out) != 1 || out[0].ID != waits.ID {
		t.Fatalf("the outbox after 400 days = %+v, %v", out, err)
	}
	if got := y.bucket(t, bPeerOut); len(got) != 1 {
		t.Fatalf("the outbox holds %q", got)
	}
}

// More than one batch of them goes in one prune, counted whole.
func TestPruneForgetsSeenPeerIDsInBatches(t *testing.T) {
	// Sequential: the package's fake clock (via newPruneYard).
	y := newPruneYard(t)
	n := pruneBatch + 10
	err := y.db.Update(func(tx *bolt.Tx) error {
		for i := 0; i < n; i++ {
			if err := tx.Bucket(bPeerSeen).Put([]byte(join("north", seqKey(uint64(i)))), []byte(seqKey(uint64(y.at.Unix())))); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	y.at = y.at.Add(8 * day)
	if res := y.prune(t, day); res.Peers["north"].Seen != n || len(y.bucket(t, bPeerSeen)) != 0 {
		t.Fatalf("pruned %+v, %d left; want %d", res.Peers, len(y.bucket(t, bPeerSeen)), n)
	}
}

// Mail given up reads as failed in its conversation for as long as the
// mail is in the log, also once the prune took it out of the outbox: what
// is not in the outbox is otherwise taken as sent.
func TestMailGivenUpReadsFailedAfterThePrune(t *testing.T) {
	// Sequential: the package's fake clock (via newPruneYard).
	y := newPruneYard(t)
	ctx := context.Background()
	if _, err := y.PutCrew(ctx, CrewMember{Name: "yardmaster", Runner: "herdr", Config: map[string]any{MailPeers: "north"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	// Running, it holds the log back to its start.
	if err := y.CrewStarted(ctx, "yardmaster", nil); err != nil {
		t.Fatal(err)
	}
	y.at = y.at.Add(time.Minute)
	m, err := y.MailPeer(ctx, "clerk", "north", "are you there", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.PeerSendFailed(ctx, "north", m.Seq, "connection refused", y.at, true); err != nil {
		t.Fatal(err)
	}
	y.at = y.at.Add(8 * day)
	if res := y.prune(t, day); res.Peers["north"].Failed != 1 || res.Events != 0 {
		t.Fatalf("pruned %+v, want the mail given up and no event", res)
	}
	ls, _, err := y.Conversation(ctx, "yardmaster", "clerk@north", 0, 0, 0)
	if err != nil || len(ls) != 1 || ls[0].Delivery != MailFailed {
		t.Fatalf("the conversation = %+v, %v; want one letter, failed", ls, err)
	}
}
