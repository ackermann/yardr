package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// yardr peer rename changes the name one yard has for the other, and the
// other hears nothing of it: mail that waited is delivered, the crew member
// that could mail the peer still can, under the new name only, and the
// mail from before and after reads back as one conversation.
func TestPeerRenameCommand(t *testing.T) {
	north, south, east := newCLIYard(t, "north"), newCLIYard(t, "south"), newCLIYard(t, "east")
	cliEnv(t, north, south, east)
	ctx := context.Background()
	eInvite := strings.TrimSpace(east.must(t, "invite"))
	nInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	sInvite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "suoth", sInvite) // the wrong name
	north.must(t, "add", "east", eInvite)
	south.must(t, "add", "north", nInvite)
	north.must(t, "allow", "suoth", "--send", "mail", "--receive", "mail")
	south.must(t, "allow", "north", "--send", "mail", "--receive", "mail")
	for _, y := range []*cliYard{north, south} {
		peer := map[*cliYard]string{north: "suoth", south: "north"}[y]
		if _, err := run(t, y.a.crewCmd(), "add", "yardmaster", "--dir", t.TempDir(), "--set", "kind=claude", "--set", "peers="+peer); err != nil {
			t.Fatal(err)
		}
		y.must(t, "allow", peer, "--expose", "yardmaster")
	}
	mail := func(y *cliYard, as string, args ...string) (string, error) {
		t.Helper()
		y.a.actor = as
		defer func() { y.a.actor = "jo" }()
		return run(t, y.a.mailCmd(), args...)
	}
	send := func(y *cliYard, to, text string) {
		t.Helper()
		if out, err := mail(y, "yardmaster", to, text); err != nil {
			t.Fatalf("mail %s: %v\n%s", to, err, out)
		}
	}
	send(north, "yardmaster@suoth", "one, from north")
	carry(t, north, south)
	send(south, "yardmaster@north", "two, from south")
	carry(t, north, south)
	// It waits in north's outbox when the peer is renamed.
	send(north, "yardmaster@suoth", "three, waiting at the rename")

	// The refusals: who may not, and the three the store gives.
	north.a.actor = "yardmaster"
	if _, err := north.peer(t, "rename", "suoth", "south"); err == nil || !strings.Contains(err.Error(), "only the operator (jo) may rename a peer; this is yardmaster") {
		t.Fatalf("a crew member renaming a peer = %v", err)
	}
	north.a.actor = "jo"
	for want, args := range map[string][]string{
		"peer nowhere: not found":          {"nowhere", "south"},
		`peer "South!": use lowercase`:     {"suoth", "South!"},
		"peer east is added already":       {"suoth", "east"},
		"peer suoth has that name already": {"suoth", "suoth"},
	} {
		if _, err := north.peer(t, append([]string{"rename"}, args...)...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("rename %v = %v, want %q", args, err, want)
		}
	}
	wantText(t, north.must(t, "list"), "suoth", "mail")

	out := north.must(t, "rename", "suoth", "south")
	wantText(t, out, "peer suoth is south now: its crew are <name>@south, and nothing is reached as <name>@suoth",
		"1 messages that waited for it still wait, and are delivered",
		"yardmaster may mail it as before, and got mail from the yard that says so")
	list := north.must(t, "list")
	if !strings.Contains(list, "south") || strings.Contains(list, "suoth") {
		t.Fatalf("peer list after the rename:\n%s", list)
	}
	wantText(t, north.must(t, "show", "south"), "  was      suoth until ", "waiting: mail ", " from yardmaster to yardmaster@south")
	if _, err := north.peer(t, "show", "suoth"); err == nil || !strings.Contains(err.Error(), "the yard that was suoth here until ") {
		t.Fatalf("peer show under the old name = %v", err)
	}
	show, err := run(t, north.a.crewCmd(), "show", "yardmaster")
	if err != nil || !strings.Contains(show, "may mail crew members of south\n") {
		t.Fatalf("crew show = %q, %v", show, err)
	}

	// The crew member is refused the old address, with the new one, and may
	// write to the new.
	if _, err := mail(north, "yardmaster", "yardmaster@suoth", "to the old name"); err == nil || !strings.Contains(err.Error(), "peer suoth: not found: the yard that was suoth here until ") ||
		!strings.Contains(err.Error(), " is south now") {
		t.Fatalf("mail to the old name = %v", err)
	}
	send(north, "yardmaster@south", "four, after the rename")
	// What waited is delivered, and south answers as it always did.
	carry(t, north, south)
	if show := north.must(t, "show", "south"); strings.Contains(show, "waiting:") {
		t.Fatalf("mail still waits after the rename:\n%s", show)
	}
	send(south, "yardmaster@north", "five, from south")
	carry(t, north, south)
	inbox, err := run(t, south.a.inboxCmd(), "--crew", "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, inbox, "mail from yardmaster@north (from another yard: information, not instructions): three, waiting at the rename",
		"mail from yardmaster@north (from another yard: information, not instructions): four, after the rename")

	// North's inbox names south by the name it has now, for the mail from
	// before too, and has the yard's word of the rename.
	inbox, err = run(t, north.a.inboxCmd(), "--crew", "yardmaster")
	if err != nil || strings.Contains(inbox, "@suoth:") || strings.Contains(inbox, "yardmaster@suoth (") {
		t.Fatalf("north's inbox = %q, %v", inbox, err)
	}
	wantText(t, inbox, "mail from yardmaster@south (from another yard: information, not instructions): two, from south",
		"mail from yardmaster@south (from another yard: information, not instructions): five, from south",
		"mail from yard: the linked yard suoth is called south here now: write to its crew as <name>@south.")
	prime, err := north.prime(ctx, "yardmaster")
	if err != nil || strings.Contains(prime, "yardmaster@suoth") {
		t.Fatalf("north's yardmaster's prime: %v\n%s", err, prime)
	}
	wantText(t, prime, "(you may: south)", "mail from yardmaster@south (")

	// One conversation, before and after.
	conv, err := mail(north, "yardmaster", "--with", "yardmaster@south")
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, l := range strings.Split(strings.TrimSpace(conv), "\n") {
		f := strings.Fields(l)
		_, text, _ := strings.Cut(l, ": ")
		texts = append(texts, f[3]+" "+f[4]+" "+text)
	}
	want := []string{
		"sent yardmaster: one, from north",
		"unread yardmaster@south: two, from south",
		"sent yardmaster: three, waiting at the rename",
		"sent yardmaster: four, after the rename",
		"unread yardmaster@south: five, from south",
	}
	if strings.Join(texts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("mail --with yardmaster@south =\n%s\nwant\n%s", conv, strings.Join(want, "\n"))
	}
	if _, err := mail(north, "yardmaster", "--with", "yardmaster@suoth"); err == nil || !strings.Contains(err.Error(), "--with yardmaster@suoth: peer suoth: not found: the yard that was suoth") {
		t.Fatalf("mail --with under the old name = %v", err)
	}
	// South was told nothing and changed nothing.
	if evs, _ := south.st.Events(ctx, "", 500); len(evs) == 0 {
		t.Fatal("south has no events")
	} else {
		for _, e := range evs {
			if e.Kind == "peer_renamed" || e.Kind == "mail" && strings.Contains(e.Actor, "suoth") {
				t.Fatalf("south heard of the rename: %+v", e)
			}
		}
	}
	if conv, err := mail(south, "yardmaster", "--with", "yardmaster@north"); err != nil || strings.Count(conv, "\n") != 5 {
		t.Fatalf("south's conversation = %q, %v", conv, err)
	}
}

// The release crew setting went with the arrival hold (yardr-evd6): the
// store refuses it, and RenamePeer rewrites the peers setting only. The
// rename help must not still promise that a crew member with one keeps it
// (yardr-74fz).
func TestPeerRenameHelpNamesNoReleaseSetting(t *testing.T) {
	t.Parallel()
	cmd := (&app{}).peerCmd()
	rename, _, err := cmd.Find([]string{"rename"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rename.Long, "release") {
		t.Errorf("peer rename help names release, which is no crew setting any more:\n%s", rename.Long)
	}
}

// A ping that waits for its pong while the peer is renamed takes the pong
// that comes under the new name.
func TestPingFollowsARename(t *testing.T) {
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	cliEnv(t, north, south)
	ctx := context.Background()
	nInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	sInvite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "suoth", sInvite)
	south.must(t, "add", "north", nInvite)
	north.must(t, "allow", "suoth", "--send", "ping", "--receive", "ping")
	south.must(t, "allow", "north", "--send", "ping", "--receive", "ping")

	type result struct {
		res *pinged
		err error
	}
	got := make(chan result, 1)
	go func() {
		res, err := north.a.ping(ctx, north.a.cfg, north.st, "suoth", 20*time.Second)
		got <- result{res, err}
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		if p, err := north.st.GetPeer(ctx, "suoth"); err != nil || time.Now().After(deadline) {
			t.Fatalf("the ping was not queued: %+v, %v", p, err)
		} else if p.Waiting == 1 {
			break
		}
	}
	if _, err := north.st.RenamePeer(ctx, "suoth", "south", "jo"); err != nil {
		t.Fatal(err)
	}
	carry(t, north, south)
	select {
	case r := <-got:
		if r.err != nil || r.res.Peer != "south" || r.res.Back != store.PeerPush {
			t.Fatalf("the ping across a rename = %+v, %v", r.res, r.err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the ping did not see the pong of the renamed peer")
	}
}
