package store_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// originatorYard is a yard whose operator is jo, with the user hal, the
// crew member yardmaster, the depot yard, a herdr group builders and a
// manual group humans.
func originatorYard(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	if _, err := s.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "humans", Runner: "manual"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

// createdOriginator is the originator the bead's created event carries.
func createdOriginator(t *testing.T, s store.API, ctx context.Context, b *store.Bead) string {
	t.Helper()
	evs, err := s.Events(ctx, b.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == "created" {
			o, _ := e.Data["originator"].(string)
			return o
		}
	}
	t.Fatalf("no created event among %+v", evs)
	return ""
}

// A bead created by a user names them; one created with --for names them,
// whoever files it outside a bead agent's pane; a crew member's, a hook's
// and any other non-user actor's create is the operator's. The created
// event carries the originator.
func TestOriginatorAtCreate(t *testing.T) {
	s, ctx := originatorYard(t)
	for _, c := range []struct {
		name  string
		for_  string
		actor string
		want  string
	}{
		{"a user is their own", "", "hal", "hal"},
		{"the operator is their own", "", "jo", "jo"},
		{"for names another", "hal", "jo", "hal"},
		{"for by a crew member", "hal", "yardmaster", "hal"},
		{"for by an unknown actor", "hal", "nobody", "hal"},
		{"a crew member's is the operator's", "", "yardmaster", "jo"},
		{"a hook's is the operator's", "", "hook:linear", "jo"},
		{"an unknown actor's is the operator's", "", "nobody", "jo"},
		{"a login name's is the operator's", "", "kloni", "jo"},
	} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: c.name, Depot: "yard", For: c.for_}, c.actor)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if b.Originator != c.want {
			t.Errorf("%s: originator = %q, want %q", c.name, b.Originator, c.want)
		}
		if got := createdOriginator(t, s, ctx, b); got != c.want {
			t.Errorf("%s: created event carries %q, want %q", c.name, got, c.want)
		}
	}
}

// --for for a user that does not exist is refused, and creates nothing;
// a crew member's name is no user's.
func TestForUnknownUserRefused(t *testing.T) {
	s, ctx := originatorYard(t)
	head, _ := s.Head(ctx)
	for _, user := range []string{"mallory", "yardmaster"} {
		_, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", For: user}, "jo")
		if err == nil || !strings.Contains(err.Error(), "--for: user "+user+": not found") {
			t.Errorf("--for %s: err = %v", user, err)
		}
	}
	if bs, _ := s.ListBeads(ctx, store.BeadFilter{All: true}); len(bs) != 0 {
		t.Fatalf("refused creates left %+v", bs)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("refused creates logged %d events", now-head)
	}
}

// A bead filed while working another inherits the origin's originator,
// whatever actor files it; an explicit --for wins outside an agent's pane.
func TestDiscoveredFromInheritsOriginator(t *testing.T) {
	s, ctx := originatorYard(t)
	hals, err := s.CreateBead(ctx, store.NewBead{Title: "hal's", Depot: "yard"}, "hal")
	if err != nil {
		t.Fatal(err)
	}
	found, err := s.CreateBead(ctx, store.NewBead{Title: "from hal's", Depot: "yard", DiscoveredFrom: hals.ID}, "jo")
	if err != nil || found.Originator != "hal" {
		t.Fatalf("a follow-up filed by jo = %+v, %v; want hal's", found, err)
	}
	won, err := s.CreateBead(ctx, store.NewBead{Title: "for jo", Depot: "yard", DiscoveredFrom: hals.ID, For: "jo"}, "hal")
	if err != nil || won.Originator != "jo" {
		t.Fatalf("an explicit --for over inheritance = %+v, %v; want jo's", won, err)
	}
	jos, err := s.CreateBead(ctx, store.NewBead{Title: "jo's", Depot: "yard"}, "nobody")
	if err != nil || jos.Originator != "jo" {
		t.Fatalf("an unknown actor's bead = %+v, %v; want jo's", jos, err)
	}
	fromJos, err := s.CreateBead(ctx, store.NewBead{Title: "from jo's", Depot: "yard", DiscoveredFrom: jos.ID}, "hal")
	if err != nil || fromJos.Originator != "jo" {
		t.Fatalf("a follow-up filed by hal = %+v, %v; want jo's", fromJos, err)
	}
	if got := createdOriginator(t, s, ctx, found); got != "hal" {
		t.Fatalf("the follow-up's created event carries %q, want hal", got)
	}
}

// --for from a bead agent's pane is refused, whatever actor the command
// names, and creates nothing; without it the bead takes the pane bead's
// originator, or the origin's with --discovered-from.
func TestForRefusedFromBeadPane(t *testing.T) {
	s, ctx := originatorYard(t)
	a, err := s.CreateBead(ctx, store.NewBead{Title: "hal's work", Depot: "yard", Stage: "new"}, "hal")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, a.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	// Before the pane is recorded: over the socket the server reads every
	// call of this process as the pane's, plain context or not.
	jos, err := s.CreateBead(ctx, store.NewBead{Title: "jo's", Depot: "yard"}, "jo")
	if err != nil || jos.Originator != "jo" {
		t.Fatalf("jo's bead = %+v, %v", jos, err)
	}
	// This test process is the shell of the agent's tab.
	me := os.Getpid()
	handle := map[string]any{"agent": "yard-a-new", "shell_pid": me}
	if st, err := peer.Describe(me); err == nil {
		handle["shell_start"] = st.Start
	}
	if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	head, _ := s.Head(ctx)
	for _, actor := range []string{"jo", "hal", sess.ID} {
		_, err := s.CreateBead(pane, store.NewBead{Title: "x", Depot: "yard", For: "jo"}, actor)
		want := "--for jo: a bead's agent files for its own bead's originator alone (this is the pane of session " + sess.ID + ", whatever actor the command names)"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("--for from the pane as %s = %v, want %q", actor, err, want)
		}
		if err := s.CheckFor(pane, "jo"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckFor from the pane as %s = %v, want %q", actor, err, want)
		}
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatalf("the refusals logged %d events", now-head)
	}
	// Filed by another user from the pane, the bead is still the pane
	// bead's person's: the pane's rule, not the actor's.
	filed, err := s.CreateBead(pane, store.NewBead{Title: "from the pane", Depot: "yard"}, "jo")
	if err != nil || filed.Originator != "hal" {
		t.Fatalf("a pane create by jo = %+v, %v; want hal's", filed, err)
	}
	// With --discovered-from, the origin's is the only way another
	// person's name arrives; --for beside it is still refused.
	fromJos, err := s.CreateBead(pane, store.NewBead{Title: "from jo's", Depot: "yard", DiscoveredFrom: jos.ID}, "hal")
	if err != nil || fromJos.Originator != "jo" {
		t.Fatalf("a pane create from jo's = %+v, %v; want jo's", fromJos, err)
	}
	if _, err := s.CreateBead(pane, store.NewBead{Title: "x", Depot: "yard", DiscoveredFrom: jos.ID, For: "jo"}, "hal"); err == nil {
		t.Fatal("--for beside --discovered-from from the pane was allowed")
	}
}

// A manual pane is a person at work, not an agent's: --for is allowed
// from it, and the actor's rule reads the create.
func TestForAllowedFromManualPane(t *testing.T) {
	s, ctx := originatorYard(t)
	a, err := s.CreateBead(ctx, store.NewBead{Title: "jo's work", Depot: "yard", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, a.ID, "humans", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	handle := map[string]any{"agent": "yard-a-new", "shell_pid": me}
	if st, err := peer.Describe(me); err == nil {
		handle["shell_start"] = st.Start
	}
	if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	for_, err := s.CreateBead(pane, store.NewBead{Title: "for hal", Depot: "yard", For: "hal"}, "jo")
	if err != nil || for_.Originator != "hal" {
		t.Fatalf("--for from a manual pane = %+v, %v; want hal's", for_, err)
	}
	// The pane bead is jo's; the actor hal names the bead.
	filed, err := s.CreateBead(pane, store.NewBead{Title: "hal's", Depot: "yard"}, "hal")
	if err != nil || filed.Originator != "hal" {
		t.Fatalf("a manual pane create by hal = %+v, %v; want hal's", filed, err)
	}
}

// A question bead takes its originator by the same rules; it matters only
// to the record.
func TestQuestionBeadOriginator(t *testing.T) {
	s, ctx := originatorYard(t)
	asked, err := s.AskNew(ctx, store.NewBead{Depot: "yard", For: "hal"}, store.Ask{Question: "Which?", To: "jo", Context: []string{"the budget"}}, "jo")
	if err != nil {
		t.Fatalf("a question asked --for hal: %v", err)
	}
	if asked.Bead.Originator != "hal" {
		t.Fatalf("a question asked --for hal = %+v; want hal's", asked.Bead)
	}
	plain, err := s.AskNew(ctx, store.NewBead{Depot: "yard"}, store.Ask{Question: "Which?", To: "jo", Context: []string{"the budget"}}, "hal")
	if err != nil {
		t.Fatalf("a question asked by hal: %v", err)
	}
	if plain.Bead.Originator != "hal" {
		t.Fatalf("a question asked by hal = %+v; want hal's", plain.Bead)
	}
}

// Only the operator changes a bead's originator, to a user that must
// exist, and the change logs an event with from, to and by. Setting what
// it already is logs nothing; anyone else is refused, for that too.
func TestBeadUpdateOriginator(t *testing.T) {
	s, ctx := originatorYard(t)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	head, _ := s.Head(ctx)
	hal, mallory, jo := "hal", "mallory", "jo"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Originator: &hal}, store.Expect{}, "hal"); err == nil ||
		!strings.Contains(err.Error(), "only the operator (jo) may change a bead's originator; this is hal") {
		t.Fatalf("hal's change: %v", err)
	}
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Originator: &mallory}, store.Expect{}, "jo"); err == nil ||
		!strings.Contains(err.Error(), "--originator: user mallory: not found") {
		t.Fatalf("jo's change to mallory: %v", err)
	}
	if cur, _ := s.GetBead(ctx, b.ID); cur.Revision != b.Revision || cur.Originator != "jo" {
		t.Fatalf("a refused change moved the bead: %+v", cur)
	}
	got, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Originator: &hal}, store.Expect{}, "jo")
	if err != nil || got.Originator != "hal" {
		t.Fatalf("jo's change to hal = %+v, %v", got, err)
	}
	evs, err := s.Events(ctx, b.ID, 10)
	if err != nil || len(evs) != 2 || evs[1].Kind != "originator_changed" || evs[1].Actor != "jo" ||
		evs[1].Data["from"] != "jo" || evs[1].Data["to"] != "hal" {
		t.Fatalf("events after the change = %+v, %v", evs, err)
	}
	// Setting what it already is logs nothing; another actor is refused
	// even for that.
	head, _ = s.Head(ctx)
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Originator: &hal}, store.Expect{}, "jo"); err != nil {
		t.Fatalf("setting what it is: %v", err)
	}
	if now, _ := s.Head(ctx); now != head {
		t.Fatal("setting what it is logged an event")
	}
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Originator: &hal}, store.Expect{}, "hal"); err == nil {
		t.Fatal("hal set what the originator already is")
	}
	// Beside other fields the originator's change is its own event, first.
	title := "hal's now"
	if _, err := s.UpdateBead(ctx, b.ID, store.BeadPatch{Title: &title, Originator: &jo}, store.Expect{Revision: got.Revision}, "jo"); err != nil {
		t.Fatal(err)
	}
	evs, _ = s.Events(ctx, b.ID, 10)
	if len(evs) != 4 || evs[2].Kind != "originator_changed" || evs[3].Kind != "updated" {
		t.Fatalf("events after a combined change = %+v", evs)
	}
}

// A bead that arrives over the peer link is the operator's: nothing of
// the other yard's person crosses.
func TestPeerBeadArrivesOperators(t *testing.T) {
	y := newBeadYard(t)
	if err := y.api.SetSetting(y.ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	y.take(t, beadMsg("m1", "north-1", nil))
	if b := y.arrived(t); b.Originator != "jo" {
		t.Fatalf("the arrived bead's originator = %q, want jo", b.Originator)
	}
}
