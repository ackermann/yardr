package store

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"testing"
	"time"
)

// Within one priority the dispatcher takes the bead that entered its stage
// first, not the one created first: a young bead waits behind no older
// bead that reached the stage later.
func TestDispatchableOrdersByStageEntry(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	ctx := context.Background()
	t0 := y.now
	a := y.at(t0).create(t, "a", "new")
	b := y.at(t0.Add(time.Minute)).create(t, "b", "new")
	y.at(t0.Add(time.Hour)).advance(t, b.ID, "review")
	y.at(t0.Add(2*time.Hour)).advance(t, a.ID, "review")
	got, err := y.Dispatchable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.Bead)
	}
	if !slices.Equal(ids, []string{b.ID, a.ID}) {
		t.Fatalf("dispatchable = %v, want [%s %s]", ids, b.ID, a.ID)
	}
}

// arrivalFlow is a flow for beads from another yard, as peerbeads_test's
// incoming: a first stage a person or a builder works, and two ways to end.
const arrivalFlow = "arrived -> build | returned on refused\nbuild -> returned on done | returned on failed | gave-up\nreturned terminal\ngave-up terminal\n"

// A bead that arrives over the peer link queues by its arrival: it carries
// the stay clock as a created bead does, so it sorts after the beads that
// were already waiting, not as a zero time ahead of everything. It carries
// the key of its first stage in Rounds as a created bead does, too.
func TestPeerBeadArrivesQueuedByArrival(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newETATest).
	y := newETATest(t)
	ctx := context.Background()
	t0 := y.now
	k := newKey(t)
	key := Public(k)
	if _, err := y.AddPeer(ctx, Peer{Name: "north", Key: key, URL: "http://north.test", Calls: "the-north"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutDepot(ctx, Depot{Name: "web", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	f, err := ParseFlow(arrivalFlow)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "incoming"
	if _, err := y.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.AllowPeer(ctx, "north", PeerAllow{Receive: []string{"bead"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.SetPeerIntake(ctx, "north", &PeerIntake{Depot: "web", Flow: "incoming"}, "jo"); err != nil {
		t.Fatal(err)
	}
	local, err := y.CreateBead(ctx, NewBead{Title: "local", Stage: "arrived", Priority: 1}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]int{"arrived": 0}; !maps.Equal(local.Rounds, want) {
		t.Fatalf("the bead created carries rounds %v, want %v", local.Rounds, want)
	}
	y.at(t0.Add(time.Hour))
	body, _ := json.Marshal(map[string]any{"v": 1, "yardr": "v0.9.1", "bead": "web-k3f9", "stage": "build",
		"title": "Add the export", "body": "As a CSV.", "type": "feature", "priority": 1,
		"outcomes": []string{"done", "failed", "refused"}})
	taken, err := y.PeerReceive(ctx, "north", key, []PeerMessage{{ID: "b1", Kind: PeerBead, Body: body}})
	if err != nil {
		t.Fatal(err)
	}
	if taken[0].Refused != "" || taken[0].Duplicate {
		t.Fatalf("a bead with an intake = %+v", taken)
	}
	bs, err := y.ListBeads(ctx, BeadFilter{All: true, Depot: "web"})
	if err != nil || len(bs) != 1 {
		t.Fatalf("beads in web = %+v, %v; want one", bs, err)
	}
	row := y.row(t, bs[0].ID)
	if row.StayStart.IsZero() || !row.StayStart.Equal(row.CreatedAt) || !slices.Equal(row.Visited, []string{"arrived"}) {
		t.Fatalf("the bead that arrived carries stay=%v created=%v visited=%v", row.StayStart, row.CreatedAt, row.Visited)
	}
	if want := map[string]int{"arrived": 0}; !maps.Equal(row.Rounds, want) {
		t.Fatalf("the bead that arrived carries rounds %v, want %v", row.Rounds, want)
	}
	got, err := y.Dispatchable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, r := range got {
		ids = append(ids, r.Bead)
	}
	if !slices.Equal(ids, []string{local.ID, bs[0].ID}) {
		t.Fatalf("dispatchable = %v, want [%s %s]", ids, local.ID, bs[0].ID)
	}
}
