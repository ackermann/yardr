package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// An ask addressed to a person of a linked yard is stored and queued for
// that yard: the command prints the ask as numbered text and says it is
// queued. It is refused before storing when the link does not carry ask
// out, when a crew asker lacks the peer in its peers setting, and when
// the asker is a bead's agent.
func TestBeadAskAcrossYards(t *testing.T) {
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	t.Setenv("YARDR_HOME", south.home)
	invite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", invite)
	ctx := context.Background()
	b, err := north.st.CreateBead(ctx, store.NewBead{Title: "the work"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := north.st.PutCrew(ctx, store.CrewMember{Name: "clerk", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	args := func(to string) []string {
		return []string{"ask", b.ID, "Move the links now?", "--to", to,
			"--context", "64 links still point at the old source.",
			"--option", "Move them now: all 64 links move tonight"}
	}
	notes := func() int {
		t.Helper()
		ns, err := north.st.Notes(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		return len(ns)
	}

	// The link carries nothing yet.
	if out, err := run(t, north.a.beadCmd(), args("mckean@south")...); err == nil ||
		!strings.Contains(err.Error(), "yardr peer allow south --send ask") {
		t.Fatalf("an ask over a link that carries none = %v\n%s", err, out)
	}
	if n := notes(); n != 0 {
		t.Fatalf("a refused ask stored %d notes", n)
	}
	north.must(t, "allow", "south", "--send", "ask")

	// A crew member without the peer in its setting, and a bead's agent.
	north.a.actor = "clerk"
	if out, err := run(t, north.a.beadCmd(), args("mckean@south")...); err == nil ||
		!strings.Contains(err.Error(), "crew member clerk may not ask mckean@south") {
		t.Fatalf("an ask from a crew member without peers = %v\n%s", err, out)
	}
	north.a.actor = "yard-10-new"
	if out, err := run(t, north.a.beadCmd(), args("mckean@south")...); err == nil ||
		!strings.Contains(err.Error(), "may ask another yard; this is yard-10-new") {
		t.Fatalf("an ask across as a bead's agent = %v\n%s", err, out)
	}
	if n := notes(); n != 0 {
		t.Fatalf("a refused ask stored %d notes", n)
	}

	// The operator asks: the ask is printed, and queued for the peer.
	north.a.actor = "jo"
	out, err := run(t, north.a.beadCmd(), args("mckean@south")...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ask 1 · open", "to mckean@south", "asked by jo",
		"Move the links now?",
		"ask for mckean@south queued; the daemon delivers it, and yardr peer show south lists it while it waits",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ask printed no %q:\n%s", want, out)
		}
	}
	queued, err := north.st.PeerOutbox(ctx, "south", 0)
	if err != nil || len(queued) != 1 || queued[0].Kind != "ask" {
		t.Fatalf("the outbox = %+v, %v", queued, err)
	}
	if show := north.must(t, "show", "south"); !strings.Contains(show, "waiting: ask ") || !strings.Contains(show, "from jo to mckean@south") {
		t.Errorf("peer show lists no waiting ask from jo to mckean@south:\n%s", show)
	}

	// --json for a script: the bead, the ask and the whole address.
	j := &app{st: north.st, actor: "jo", json: true, cfg: north.a.cfg}
	out, err = run(t, j.beadCmd(), args("ada@south")...)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Bead string
		Ask  uint64
		To   string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Bead != b.ID || got.Ask == 0 || got.To != "ada@south" {
		t.Fatalf("ask --json = %s, %v", out, err)
	}
}

// yardr peer allow --send ask warns as for other kinds when the peer has
// not said it understands ask, and allows all the same.
func TestPeerAllowSendAskWarns(t *testing.T) {
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	t.Setenv("YARDR_HOME", south.home)
	invite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", invite)
	ctx := context.Background()
	p, err := north.st.GetPeer(ctx, "south")
	if err != nil {
		t.Fatal(err)
	}
	if err := north.own.PeerHeard(ctx, "south", p.Key, &store.PeerSays{Understands: []string{"ping", "mail"}, Yardr: "v1.2.3"}); err != nil {
		t.Fatal(err)
	}
	errs := captureStderr(t)
	out := north.must(t, "allow", "south", "--send", "ask")
	wantText(t, out, "peer south: send ask; receive nothing")
	wantText(t, errs.String(), "warning: south's yardr (v1.2.3) said at ", " that it understands mail, ping, and not ask: an ask sent to it is refused there until it is upgraded")
	if strings.Count(errs.String(), "warning:") != 1 {
		t.Fatalf("ask warned never or twice:\n%s", errs)
	}
}
