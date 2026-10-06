package store

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// holdYard is a yard that takes north's beads held, with the store's clock
// in the test's hand: now is when a write happens, and a pass is made at
// whatever time the test says, as the daemon makes one with its own clock.
type holdYard struct {
	*Store
	ctx context.Context
	key string
	now time.Time
	n   int
}

func newHoldYard(t *testing.T, holdFor string) *holdYard {
	t.Helper()
	y := &holdYard{ctx: context.Background(), now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.now }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	y.Store = s
	k, _, err := yardkey.Ensure(filepath.Join(t.TempDir(), "north.key"))
	if err != nil {
		t.Fatal(err)
	}
	y.key = yardkey.Public(k)
	f, err := ParseFlow("arrived -> returned on done\nreturned terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "incoming"
	for _, err := range []error{
		s.PutRig(y.ctx, Rig{Name: "web", Base: "origin/main"}, "jo"),
		second(s.PutFlow(y.ctx, *f, "jo")),
		second(s.AddUser(y.ctx, "jo", "jo")),
		second(s.AddPeer(y.ctx, Peer{Name: "north", Key: y.key}, "jo")),
		second(s.AllowPeer(y.ctx, "north", PeerAllow{Receive: []string{PeerBead}}, "jo")),
		second(s.SetPeerIntake(y.ctx, "north", &PeerIntake{Rig: "web", Flow: "incoming", HoldFor: holdFor}, "jo")),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	return y
}

func second[T any](_ T, err error) error { return err }

// arrive takes a bead of north's and returns this yard's bead of it.
func (y *holdYard) arrive(t *testing.T) *Bead {
	t.Helper()
	y.n++
	body, _ := json.Marshal(PeerBeadMessage{V: PeerBeadVersion, Bead: fmt.Sprintf("web-k3f%d", y.n), Stage: "build", Title: "Add the export"})
	got, err := y.PeerReceive(y.ctx, "north", y.key, []PeerMessage{{ID: fmt.Sprintf("b%d", y.n), Kind: PeerBead, Body: body}})
	if err != nil || got[0].Refused != "" {
		t.Fatalf("a bead with an intake = %+v, %v", got, err)
	}
	bs, err := y.ListBeads(y.ctx, BeadFilter{Label: "from-north"})
	if err != nil || len(bs) == 0 {
		t.Fatalf("beads from north = %+v, %v", bs, err)
	}
	return &bs[len(bs)-1]
}

// pass is the daemon's, at after since the yard's first moment.
func (y *holdYard) pass(t *testing.T, after time.Duration) {
	t.Helper()
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC).Add(after)
	y.now = at
	if err := y.ExpirePeerBeads(y.ctx, at); err != nil {
		t.Fatal(err)
	}
}

func (y *holdYard) events(t *testing.T, id, kind string) []Event {
	t.Helper()
	evs, err := y.Events(y.ctx, id, 500)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// returns are the returns that wait for north.
func (y *holdYard) returns(t *testing.T) []PeerReturnMessage {
	t.Helper()
	out, err := y.PeerOutbox(y.ctx, "north", 16)
	if err != nil {
		t.Fatal(err)
	}
	var rs []PeerReturnMessage
	for _, m := range out {
		var r PeerReturnMessage
		if m.Kind != PeerBeadReturn || json.Unmarshal(m.Body, &r) != nil {
			t.Fatalf("what waits for north = %+v", m)
		}
		rs = append(rs, r)
	}
	return rs
}

// A bead that arrived held and that nobody released for as long as the
// intake says, three days unless it says, is closed by the yard and goes
// back as refused, with a note that says so: once, however many passes
// follow. A day before, peer_bead_expiring is logged on it, once.
func TestPeerBeadNobodyReleasesGoesBack(t *testing.T) {
	y := newHoldYard(t, "")
	b := y.arrive(t)
	if got, _ := y.GetBead(y.ctx, b.ID); got.Peer.Expires == nil || !got.Peer.Expires.Equal(got.Peer.At.Add(3*day)) {
		t.Fatalf("a held bead says it goes back at %v, arrived %v", got.Peer.Expires, got.Peer.At)
	}
	y.pass(t, 2*day-time.Second)
	if ev := y.events(t, b.ID, "peer_bead_expiring"); len(ev) != 0 {
		t.Fatalf("warned more than a day before: %+v", ev)
	}
	for i := 0; i < 3; i++ {
		y.pass(t, 2*day+time.Duration(i)*time.Hour)
	}
	ev := y.events(t, b.ID, "peer_bead_expiring")
	if len(ev) != 1 || ev[0].Actor != "yard" || ev[0].Data["peer"] != "north" || ev[0].Data["hold_for"] != "3d" ||
		ev[0].Data["expires"] != "2026-10-09T12:00:00Z" {
		t.Fatalf("peer_bead_expiring = %+v", ev)
	}
	y.pass(t, 3*day-time.Second)
	if got, _ := y.GetBead(y.ctx, b.ID); got.Status != "open" || len(y.returns(t)) != 0 {
		t.Fatalf("a bead before its time = %+v", got)
	}
	for i := 0; i < 3; i++ {
		y.pass(t, 3*day+time.Duration(i)*day)
	}
	got, _ := y.GetBead(y.ctx, b.ID)
	want := "not released within 3d at the other yard"
	if got.Status != "closed" || got.CloseReason == nil || *got.CloseReason != want || got.Peer.Outcome != OutcomeRefused || got.Peer.Expires != nil {
		t.Fatalf("the bead after its time = %+v, peer %+v", got, got.Peer)
	}
	rs := y.returns(t)
	if len(rs) != 1 || rs[0].Outcome != OutcomeRefused || len(rs[0].Notes) != 1 || rs[0].Notes[0].Text != want || rs[0].Notes[0].Author != "yard" {
		t.Fatalf("what goes back = %+v", rs)
	}
	if c, r, w := y.events(t, b.ID, "closed"), y.events(t, b.ID, "peer_bead_returned"), y.events(t, b.ID, "peer_bead_expiring"); len(c) != 1 || c[0].Actor != "yard" || len(r) != 1 || len(w) != 1 {
		t.Fatalf("closed %+v, returned %+v, expiring %+v", c, r, w)
	}
}

// The note names the yard as it calls itself, when it has a name.
func TestPeerBeadGoesBackNamingTheYard(t *testing.T) {
	y := newHoldYard(t, "12h")
	if err := y.SetSetting(y.ctx, "name", "south", "jo"); err != nil {
		t.Fatal(err)
	}
	y.arrive(t)
	y.pass(t, 12*time.Hour)
	if rs := y.returns(t); len(rs) != 1 || rs[0].Notes[0].Text != "not released within 12h at south" {
		t.Fatalf("what goes back = %+v", rs)
	}
}

// A time shorter than two days warns at its half.
func TestPeerBeadExpiringAtHalfAShortTime(t *testing.T) {
	y := newHoldYard(t, "10h")
	b := y.arrive(t)
	y.pass(t, 5*time.Hour-time.Second)
	if ev := y.events(t, b.ID, "peer_bead_expiring"); len(ev) != 0 {
		t.Fatalf("warned before half the time: %+v", ev)
	}
	y.pass(t, 5*time.Hour)
	y.pass(t, 6*time.Hour)
	if ev := y.events(t, b.ID, "peer_bead_expiring"); len(ev) != 1 || ev[0].Data["hold_for"] != "10h" {
		t.Fatalf("peer_bead_expiring = %+v", ev)
	}
}

// A bead that was released is left, also when it is held again later for
// another reason, and so is one that arrived ready.
func TestPeerBeadReleasedIsLeft(t *testing.T) {
	y := newHoldYard(t, "")
	b := y.arrive(t)
	for _, hold := range []bool{false, true} {
		if _, err := y.SetHold(y.ctx, b.ID, hold, Expect{}, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := y.SetPeerIntake(y.ctx, "north", &PeerIntake{Rig: "web", Flow: "incoming", Auto: true}, "jo"); err != nil {
		t.Fatal(err)
	}
	ready := y.arrive(t)
	y.pass(t, 30*day)
	for _, id := range []string{b.ID, ready.ID} {
		if got, _ := y.GetBead(y.ctx, id); got.Status != "open" || got.Peer.Expires != nil || len(y.events(t, id, "peer_bead_expiring")) != 0 {
			t.Fatalf("a bead nobody was to release = %+v", got)
		}
	}
	if !y.mustGet(t, b.ID).Hold || len(y.returns(t)) != 0 {
		t.Fatalf("returned %+v", y.returns(t))
	}
}

func (y *holdYard) mustGet(t *testing.T, id string) *Bead {
	t.Helper()
	b, err := y.GetBead(y.ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// --hold-for 0: a held bead waits for as long as nobody releases it. Set
// to a time later, that time holds for the bead that waits already, from
// its arrival; an intake removed leaves it the default.
func TestPeerBeadHeldForEver(t *testing.T) {
	y := newHoldYard(t, PeerHoldNever)
	b := y.arrive(t)
	y.pass(t, 300*day)
	if got := y.mustGet(t, b.ID); got.Status != "open" || got.Peer.Expires != nil || len(y.events(t, b.ID, "peer_bead_expiring")) != 0 || len(y.returns(t)) != 0 {
		t.Fatalf("a bead of an intake that holds for ever = %+v", got)
	}
	p, err := y.SetPeerIntake(y.ctx, "north", &PeerIntake{Rig: "web", Flow: "incoming", HoldFor: "9600h"}, "jo")
	if err != nil || p.Intake.HoldFor != "400d" {
		t.Fatalf("an intake set to 9600h = %+v, %v", p.Intake, err)
	}
	y.pass(t, 399*day)
	if got := y.mustGet(t, b.ID); got.Status != "open" || len(y.events(t, b.ID, "peer_bead_expiring")) != 1 {
		t.Fatalf("a day before its new time = %+v", got)
	}
	if _, err := y.SetPeerIntake(y.ctx, "north", nil, "jo"); err != nil {
		t.Fatal(err)
	}
	y.pass(t, 399*day)
	if rs := y.returns(t); len(rs) != 1 || rs[0].Notes[0].Text != "not released within 3d at the other yard" {
		t.Fatalf("a held bead of an intake that was removed = %+v", rs)
	}
}

// A first pass long after the time (a daemon that was down) returns the
// bead, and warns nobody of what has happened.
func TestPeerBeadGoesBackAtALatePass(t *testing.T) {
	y := newHoldYard(t, "")
	b := y.arrive(t)
	y.pass(t, 40*day)
	if got := y.mustGet(t, b.ID); got.Status != "closed" || len(y.returns(t)) != 1 || len(y.events(t, b.ID, "peer_bead_expiring")) != 0 {
		t.Fatalf("after a late pass = %+v", got)
	}
}

// A bead closed by someone, or whose link is removed, is not the pass's.
func TestPeerBeadEndedOtherwiseIsLeft(t *testing.T) {
	y := newHoldYard(t, "")
	closed, kept := y.arrive(t), y.arrive(t)
	if _, err := y.CloseBead(y.ctx, closed.ID, "not ours", Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := y.DeletePeer(y.ctx, "north", "jo"); err != nil {
		t.Fatal(err)
	}
	y.pass(t, 30*day)
	if got := y.mustGet(t, kept.ID); got.Status != "open" || len(y.events(t, closed.ID, "closed")) != 1 {
		t.Fatalf("a bead of a link that is removed = %+v", got)
	}
}

// What an intake holds for is a time as a timer's is, or 0.
func TestPeerIntakeHoldFor(t *testing.T) {
	y := newHoldYard(t, "")
	for _, bad := range []string{"soon", "-1h", "0s", "3"} {
		if _, err := y.SetPeerIntake(y.ctx, "north", &PeerIntake{Rig: "web", Flow: "incoming", HoldFor: bad}, "jo"); err == nil || !strings.Contains(err.Error(), "or 0 for never (unset: 3d)") {
			t.Fatalf("--hold-for %s = %v", bad, err)
		}
	}
	p, err := y.SetPeerIntake(y.ctx, "north", &PeerIntake{Rig: "web", Flow: "incoming", HoldFor: " 72h "}, "jo")
	if err != nil || p.Intake.HoldFor != "3d" || p.Intake.Hold() != 3*day {
		t.Fatalf("72h = %+v, %v", p.Intake, err)
	}
	if ev := y.events(t, "", "peer_intake"); ev[len(ev)-1].Data["hold_for"] != "3d" {
		t.Fatalf("peer_intake = %+v", ev[len(ev)-1])
	}
}
