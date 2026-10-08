package store

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The roster a peer says is the peer's text: what is kept of it is entries
// with a name mail can be addressed to, each once, of a kind there is, a
// role of one printable line, and no more of them than a link exposes.
func TestPeerSaidRosterIsHeld(t *testing.T) {
	said := &PeerSays{Understands: []string{"mail"}, Roster: []PeerRosterEntry{
		{Name: "yardmaster", Kind: RosterCrew, Role: "Runs the yard\nand \x1b[31mmore", Mail: true},
		{Name: "ada", Kind: RosterUser, Role: strings.Repeat("é", 200)},
		{Name: "yardmaster", Kind: RosterUser, Role: "the same name again"},
		{Name: "root@south", Kind: RosterCrew},
		{Name: "../etc", Kind: RosterCrew},
		{Name: "builders", Kind: "group"},
		{Name: "", Kind: RosterCrew},
	}}
	got := said.clean().Roster
	want := []PeerRosterEntry{
		{Name: "ada", Kind: RosterUser, Role: strings.Repeat("é", 79) + "…"},
		{Name: "yardmaster", Kind: RosterCrew, Role: "Runs the yard", Mail: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kept of the roster: %+v", got)
	}
	var many []PeerRosterEntry
	for i := 0; i < 2*MaxPeerExpose; i++ {
		many = append(many, PeerRosterEntry{Name: fmt.Sprintf("crew-%03d", i), Kind: RosterCrew})
	}
	if got := (&PeerSays{Understands: []string{}, Roster: many}).clean().Roster; len(got) != MaxPeerExpose {
		t.Fatalf("kept %d of %d", len(got), len(many))
	}
	// A yard that says no kinds at all is an older yardr, roster or not.
	if got := (&PeerSays{Roster: many}).clean(); got != nil {
		t.Fatalf("kept of one that names no kinds: %+v", got)
	}
}

// A roster that changed is kept at once, whatever the hour: who is on it
// decides whom this yard's crew write to. One that says none clears it.
func TestPeerSaidRosterChanges(t *testing.T) {
	at := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })

	says := func(roster ...PeerRosterEntry) *PeerSays {
		return &PeerSays{Understands: []string{"mail"}, Yardr: "v1", Roster: roster}
	}
	master := PeerRosterEntry{Name: "yardmaster", Kind: RosterCrew, Role: "Runs the yard"}
	p := &Peer{Name: "north"}
	if !p.said(says(master)) || len(p.Said.Roster) != 1 {
		t.Fatalf("the first roster: %+v", p.Said)
	}
	at = at.Add(time.Minute)
	if p.said(says(master)) {
		t.Fatal("the same roster, a minute on, is saved again")
	}
	withMail := master
	withMail.Mail = true
	if !p.said(says(withMail)) || !p.Said.Roster[0].Mail || !p.Said.At.Equal(at) {
		t.Fatalf("the link takes mail now: %+v", p.Said)
	}
	if !p.said(says(withMail, PeerRosterEntry{Name: "ada", Kind: RosterUser, Role: "user"})) || len(p.Said.Roster) != 2 {
		t.Fatalf("one more exposed: %+v", p.Said)
	}
	if !p.said(says()) || p.Said == nil || p.Said.Roster != nil {
		t.Fatalf("nobody exposed any more: %+v", p.Said)
	}
}
