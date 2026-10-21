package store_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// paneShell is a herdr tab's shell handle for this test process, as MarkRunning
// takes it: the pane's shell is the test itself (see expect_test.go), running
// as agent. Each test has its own yard, so only the pane under test is on
// record.
func paneShell(t *testing.T, agent string) map[string]any {
	t.Helper()
	me := os.Getpid()
	h := map[string]any{"agent": agent, "shell_pid": me}
	if st, err := peer.Describe(me); err == nil {
		h["shell_start"] = st.Start
	}
	return h
}

// lastEventActor is the actor of the latest event of kind on the bead.
func lastEventActor(t *testing.T, s store.API, ctx context.Context, bead, kind string) string {
	t.Helper()
	evs, err := s.Events(ctx, bead, 50)
	if err != nil {
		t.Fatal(err)
	}
	got := ""
	for _, e := range evs {
		if e.Kind == kind {
			got = e.Actor
		}
	}
	if got == "" {
		t.Fatalf("no %s event on %s in %+v", kind, bead, evs)
	}
	return got
}

// lastKindActor is the actor of the latest yard event of kind.
func lastKindActor(t *testing.T, s store.API, ctx context.Context, kind string) string {
	t.Helper()
	evs, err := s.EventsOfKind(ctx, kind, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		t.Fatalf("no %s event", kind)
	}
	return evs[len(evs)-1].Actor
}

// moveYardClaim is a yard with the outcome flow, a herdr builders group
// and an operator, and a bead in new claimed by a session with no pane on
// record yet.
func moveYardClaim(t *testing.T) (store.API, context.Context, *store.Bead, *store.Session, string) {
	t.Helper()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "op")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	return s, ctx, b, sess, b.ID + "-new"
}

// paneMoveYard is a moveYardClaim whose session runs in this test
// process's pane, as actor.
func paneMoveYard(t *testing.T) (store.API, context.Context, *store.Bead, *store.Session, string) {
	t.Helper()
	s, ctx, b, sess, actor := moveYardClaim(t)
	if err := s.MarkRunning(ctx, sess.ID, paneShell(t, actor)); err != nil {
		t.Fatal(err)
	}
	return s, ctx, b, sess, actor
}

// A move from a session's or a crew member's pane is recorded as that
// session's actor or that crew member, whatever --actor says: an agent
// cannot move a bead as the operator by naming them. The move's event
// and its note name the pane's actor. From no pane the actor stands as
// given.
func TestMoveFromAPaneIsThatPanesActor(t *testing.T) {
	t.Parallel()
	t.Run("done", func(t *testing.T) {
		s, ctx, b, sess, actor := paneMoveYard(t)
		pane := store.WithCaller(ctx, os.Getpid())
		got, err := s.Done(pane, b.ID, "", "words", store.Expect{Session: sess.ID}, "op")
		if err != nil || got.Stage != "review" {
			t.Fatalf("the pane's own done = %+v, %v", got, err)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "advanced"); a != actor {
			t.Fatalf("advanced event actor = %q, want the pane's actor %q", a, actor)
		}
		notes, _ := s.Notes(ctx, b.ID)
		if len(notes) == 0 || notes[len(notes)-1].Author != actor {
			t.Fatalf("move note = %+v, want author %q", notes, actor)
		}
	})

	t.Run("advance", func(t *testing.T) {
		s, ctx, b, sess, actor := paneMoveYard(t)
		pane := store.WithCaller(ctx, os.Getpid())
		if _, err := s.AdvanceNote(pane, b.ID, "review", "note", store.Expect{Session: sess.ID}, "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "advanced"); a != actor {
			t.Fatalf("advanced event actor = %q, want %q", a, actor)
		}
		notes, _ := s.Notes(ctx, b.ID)
		if len(notes) == 0 || notes[len(notes)-1].Author != actor {
			t.Fatalf("move note = %+v, want author %q", notes, actor)
		}
	})

	t.Run("close", func(t *testing.T) {
		s, ctx, b, sess, actor := paneMoveYard(t)
		pane := store.WithCaller(ctx, os.Getpid())
		if _, err := s.CloseBead(pane, b.ID, "reason", store.Expect{Session: sess.ID}, "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "closed"); a != actor {
			t.Fatalf("closed event actor = %q, want %q", a, actor)
		}
	})

	t.Run("reopen hold release", func(t *testing.T) {
		s, ctx, a, asess, actor := moveYardClaim(t)
		// Another bead no session owns, closed from no pane: the pane
		// moves it as anyone's would be, recorded as its own actor. Made
		// and closed before the pane is on record: over the socket every
		// call of this process is the pane's after that, and an agent
		// files no bead into new and closes no other bead.
		b, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CloseBead(ctx, b.ID, "done", store.Expect{}, "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(ctx, asess.ID, paneShell(t, actor)); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if _, err := s.Reopen(pane, b.ID, store.Expect{}, "op"); err != nil {
			t.Fatal(err)
		}
		if x := lastEventActor(t, s, ctx, b.ID, "reopened"); x != actor {
			t.Fatalf("reopened event actor = %q, want %q", x, actor)
		}
		if _, err := s.SetHold(pane, b.ID, true, store.Expect{}, "op"); err != nil {
			t.Fatal(err)
		}
		if x := lastEventActor(t, s, ctx, b.ID, "held"); x != actor {
			t.Fatalf("held event actor = %q, want %q", x, actor)
		}
		// Lifting a hold is for a person or a crew member, not a bead's
		// agent (see SetHold). The bead stays held by the pane that held it.
		if _, err := s.SetHold(pane, b.ID, false, store.Expect{}, "op"); err == nil || !strings.Contains(err.Error(), "may hold a bead but not lift a hold") {
			t.Fatalf("unhold from a session's pane = %v, want it refused", err)
		}
		if got, err := s.GetBead(ctx, b.ID); err != nil || !got.Hold || got.HeldBy != actor {
			t.Fatalf("bead after the refused unhold = %+v, %v: want it held by %q", got, err, actor)
		}
		// A release that changes nothing logs nothing, so the pane
		// releases its own bead, ending its session.
		if _, err := s.Release(pane, a.ID, "again", store.Expect{Session: asess.ID}, "op"); err != nil {
			t.Fatal(err)
		}
		if x := lastEventActor(t, s, ctx, a.ID, "released"); x != actor {
			t.Fatalf("released event actor = %q, want %q", x, actor)
		}
	})

	t.Run("crew pane", func(t *testing.T) {
		s, ctx := flowDepot(t)
		putFlow(t, s, "yard", "", outcomeFlow)
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		h := map[string]any{"shell_pid": os.Getpid()}
		if st, err := peer.Describe(os.Getpid()); err == nil {
			h["shell_start"] = st.Start
		}
		if err := s.CrewStarted(ctx, "yardmaster", h); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if _, err := s.AdvanceNote(pane, b.ID, "review", "note", store.Expect{}, "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "advanced"); a != "yardmaster" {
			t.Fatalf("advanced event actor = %q, want yardmaster", a)
		}
		notes, _ := s.Notes(ctx, b.ID)
		if len(notes) == 0 || notes[len(notes)-1].Author != "yardmaster" {
			t.Fatalf("move note = %+v, want author yardmaster", notes)
		}
		// A crew member may take away what a bead's agent may not. The hold
		// it lifts and the edge it removes are recorded as its own writes.
		other, err := s.CreateBead(ctx, store.NewBead{Title: "b", Depot: "yard", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetHold(pane, other.ID, true, store.Expect{}, "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.SetHold(pane, other.ID, false, store.Expect{}, "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastEventActor(t, s, ctx, other.ID, "unheld"); a != "yardmaster" {
			t.Fatalf("unheld event actor = %q, want yardmaster", a)
		}
		if err := s.AddDep(pane, b.ID, other.ID, store.DepBlocks, "", "", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveDep(pane, b.ID, other.ID, store.DepBlocks, "", "", "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastEventActor(t, s, ctx, other.ID, "dep_removed"); a != "yardmaster" {
			t.Fatalf("dep_removed event actor = %q, want yardmaster", a)
		}
	})

	t.Run("no pane", func(t *testing.T) {
		s, ctx, b, sess, _ := moveYardClaim(t)
		// No pane on record anywhere: a person's terminal keeps the
		// actor as given. Over the socket the server sees this test
		// process as the caller, so a pane of it would resolve.
		got, err := s.Done(ctx, b.ID, "", "words", store.Expect{Session: sess.ID}, "op")
		if err != nil || got.Stage != "review" {
			t.Fatalf("done from no pane = %+v, %v", got, err)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "advanced"); a != "op" {
			t.Fatalf("advanced event actor = %q, want op", a)
		}
		notes, _ := s.Notes(ctx, b.ID)
		if len(notes) == 0 || notes[len(notes)-1].Author != "op" {
			t.Fatalf("move note = %+v, want author op", notes)
		}
	})
}

// A bead written from a session's pane is recorded as that session's
// actor, whatever --actor says: who created it, who changed it, who
// linked and edged it. From no pane the actor stands as given.
func TestBeadWriteFromAPaneIsThatPanesActor(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, withPane bool) (store.API, context.Context, *store.Bead, *store.Session, string) {
		t.Helper()
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		actor := b.ID + "-new"
		if withPane {
			if err := s.MarkRunning(ctx, sess.ID, paneShell(t, actor)); err != nil {
				t.Fatal(err)
			}
		}
		return s, ctx, b, sess, actor
	}

	t.Run("create update dep link", func(t *testing.T) {
		s, ctx, a, sess, actor := setup(t, true)
		pane := store.WithCaller(ctx, os.Getpid())
		made, err := s.CreateBead(pane, store.NewBead{Title: "b", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		if made.CreatedBy == nil || *made.CreatedBy != actor {
			t.Fatalf("CreatedBy = %+v, want %q", made.CreatedBy, actor)
		}
		if x := lastEventActor(t, s, ctx, made.ID, "created"); x != actor {
			t.Fatalf("created event actor = %q, want %q", x, actor)
		}
		title := "a, renamed"
		if _, err := s.UpdateBead(pane, a.ID, store.BeadPatch{Title: &title}, store.Expect{Session: sess.ID}, "op"); err != nil {
			t.Fatal(err)
		}
		if x := lastEventActor(t, s, ctx, a.ID, "updated"); x != actor {
			t.Fatalf("updated event actor = %q, want %q", x, actor)
		}
		if err := s.AddDep(pane, a.ID, made.ID, store.DepBlocks, "", "", "op"); err != nil {
			t.Fatal(err)
		}
		if x := lastEventActor(t, s, ctx, made.ID, "dep_added"); x != actor {
			t.Fatalf("dep_added event actor = %q, want %q", x, actor)
		}
		nl := store.NewLink{Kind: store.LinkTracks, Source: "linear", URL: "https://linear.app/acme/issue/YRD-1"}
		if _, err := s.AddLink(pane, a.ID, nl, "op"); err != nil {
			t.Fatal(err)
		}
		if x := lastEventActor(t, s, ctx, a.ID, "linked"); x != actor {
			t.Fatalf("linked event actor = %q, want %q", x, actor)
		}
		if err := s.RemoveLink(pane, a.ID, nl.URL, "op"); err != nil {
			t.Fatal(err)
		}
		if x := lastEventActor(t, s, ctx, a.ID, "unlinked"); x != actor {
			t.Fatalf("unlinked event actor = %q, want %q", x, actor)
		}
		// A bead's agent never removes a blocks edge (see RemoveDep). It does
		// remove an edge that only records something, as the pane's actor.
		if err := s.RemoveDep(pane, a.ID, made.ID, store.DepBlocks, "", "", "op"); err == nil || !strings.Contains(err.Error(), "may add edges but not remove a blocks edge") {
			t.Fatalf("RemoveDep of a blocks edge from a session's pane = %v, want it refused", err)
		}
		if err := s.AddDep(pane, a.ID, made.ID, "related", "", "", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.RemoveDep(pane, a.ID, made.ID, "related", "", "", "op"); err != nil {
			t.Fatal(err)
		}
		if x := lastEventActor(t, s, ctx, made.ID, "dep_removed"); x != actor {
			t.Fatalf("dep_removed event actor = %q, want %q", x, actor)
		}
	})

	t.Run("no pane", func(t *testing.T) {
		s, ctx, _, _, _ := setup(t, false)
		made, err := s.CreateBead(ctx, store.NewBead{Title: "b", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		if made.CreatedBy == nil || *made.CreatedBy != "op" {
			t.Fatalf("CreatedBy = %+v, want op", made.CreatedBy)
		}
	})
}

// An ack from a session's pane is recorded as that session's actor,
// whatever --actor says. From no pane the actor stands as given.
func TestAckFromAPaneIsThatPanesActor(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, withPane bool) (store.API, context.Context, *store.Bead, *store.Session, string) {
		t.Helper()
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		actor := b.ID + "-new"
		if withPane {
			if err := s.MarkRunning(ctx, sess.ID, paneShell(t, actor)); err != nil {
				t.Fatal(err)
			}
		}
		return s, ctx, b, sess, actor
	}

	t.Run("pane", func(t *testing.T) {
		s, ctx, b, sess, actor := setup(t, true)
		n, err := s.AddNotedAs(ctx, b.ID, "words", "op", "")
		if err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if _, err := s.AckNote(pane, b.ID, n.Seq, sess.ID, "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "note_acked"); a != actor {
			t.Fatalf("note_acked event actor = %q, want %q", a, actor)
		}
	})

	t.Run("no pane", func(t *testing.T) {
		s, ctx, b, sess, _ := setup(t, false)
		n, err := s.AddNotedAs(ctx, b.ID, "words", "op", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AckNote(ctx, b.ID, n.Seq, sess.ID, "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastEventActor(t, s, ctx, b.ID, "note_acked"); a != "op" {
			t.Fatalf("note_acked event actor = %q, want op", a)
		}
	})
}

// An ask from a session's pane is asked as that session's actor,
// whatever --actor says: the ask note's author and the yard's own
// "leave it to" name it. From no pane the actor stands as given.
func TestAskFromAPaneIsThatPanesActor(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, withPane bool) (store.API, context.Context, *store.Bead, string) {
		t.Helper()
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		actor := b.ID + "-new"
		if withPane {
			if err := s.MarkRunning(ctx, sess.ID, paneShell(t, actor)); err != nil {
				t.Fatal(err)
			}
		}
		return s, ctx, b, actor
	}
	ask := store.Ask{
		Question: "Which way?",
		To:       "op",
		Context:  []string{"two ways out"},
		Options:  []store.AskOption{{Label: "left", Consequence: "goes left"}},
	}

	t.Run("pane", func(t *testing.T) {
		s, ctx, b, actor := setup(t, true)
		pane := store.WithCaller(ctx, os.Getpid())
		seq, err := s.AddAsk(pane, b.ID, ask, "op")
		if err != nil {
			t.Fatal(err)
		}
		notes, _ := s.Notes(ctx, b.ID)
		var found *store.Note
		for i := range notes {
			if notes[i].Seq == seq {
				found = &notes[i]
			}
		}
		if found == nil || found.Author != actor {
			t.Fatalf("ask note = %+v, want author %q", found, actor)
		}
		var parsed store.Ask
		if err := json.Unmarshal([]byte(found.Text), &parsed); err != nil {
			t.Fatal(err)
		}
		last := parsed.Options[len(parsed.Options)-1]
		if last.Label != store.YouDecide || last.Consequence != "leave it to "+actor {
			t.Fatalf("YouDecide option = %+v, want it left to %q", last, actor)
		}
	})

	t.Run("ask new", func(t *testing.T) {
		s, ctx, _, actor := setup(t, true)
		pane := store.WithCaller(ctx, os.Getpid())
		made, err := s.AskNew(pane, store.NewBead{}, ask, "op")
		if err != nil {
			t.Fatal(err)
		}
		if made.Bead.CreatedBy == nil || *made.Bead.CreatedBy != actor {
			t.Fatalf("CreatedBy = %+v, want %q", made.Bead.CreatedBy, actor)
		}
		notes, _ := s.Notes(ctx, made.Bead.ID)
		if len(notes) == 0 || notes[0].Author != actor {
			t.Fatalf("ask note = %+v, want author %q", notes, actor)
		}
	})

	t.Run("no pane", func(t *testing.T) {
		s, ctx, b, _ := setup(t, false)
		seq, err := s.AddAsk(ctx, b.ID, ask, "op")
		if err != nil {
			t.Fatal(err)
		}
		notes, _ := s.Notes(ctx, b.ID)
		var found *store.Note
		for i := range notes {
			if notes[i].Seq == seq {
				found = &notes[i]
			}
		}
		if found == nil || found.Author != "op" {
			t.Fatalf("ask note = %+v, want author op", found)
		}
	})
}

// Mail from a bead session's pane is recorded as that session's actor and
// names the session's bead as from_bead: the actor is no inbox, so the bead
// is what a reply to the mail is written on (yardr reply). It names it for a
// session that has ended since, whose agent is still open in its pane. The
// bead is in the mail's data and not on the event, whose bead's history and
// subscribers are not the mail's readers. Mail from a crew member's pane and
// from no pane names none.
func TestMailFromAPaneNamesItsBead(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, runner string, withPane bool) (store.API, context.Context, *store.Bead, *store.Session, string) {
		t.Helper()
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		group := store.Group{Name: "builders", Runner: runner}
		if runner == "exec" {
			group.Config = map[string]any{"cmd": "true"}
		}
		if err := s.PutGroup(ctx, group, "op"); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"yardmaster", "clerk"} {
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr"}, "op"); err != nil {
				t.Fatal(err)
			}
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		actor := b.ID + "-new"
		if runner == "exec" {
			actor = "builders"
		}
		if withPane {
			handle := paneShell(t, actor)
			if runner == "exec" {
				// An exec handle names the script's pid,
				// without an agent: the calling process
				// itself, which over the socket is the peer
				// the server sees, not a made-up pid.
				handle = map[string]any{"pid": os.Getpid()}
			}
			if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
				t.Fatal(err)
			}
		}
		return s, ctx, b, sess, actor
	}
	lastMail := func(t *testing.T, s store.API, ctx context.Context) store.Event {
		t.Helper()
		evs, err := s.EventsOfKind(ctx, "mail", 50)
		if err != nil || len(evs) == 0 {
			t.Fatalf("mail events = %+v, %v", evs, err)
		}
		return evs[len(evs)-1]
	}

	t.Run("session", func(t *testing.T) {
		s, ctx, b, _, actor := setup(t, "herdr", true)
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.Mail(pane, "yardmaster", "may I narrow it?", "op"); err != nil {
			t.Fatal(err)
		}
		e := lastMail(t, s, ctx)
		if e.Actor != actor || e.Data["from_bead"] != b.ID || e.Data["to"] != "yardmaster" {
			t.Fatalf("mail from the session's pane = %+v, want actor %q, from_bead %q, to yardmaster", e, actor, b.ID)
		}
		if e.Bead != nil {
			t.Fatalf("the mail event is on bead %s: its history would show the mail", *e.Bead)
		}
		evs, err := s.Events(ctx, b.ID, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range evs {
			if ev.Kind == "mail" {
				t.Fatalf("the bead's history holds the mail: %+v", ev)
			}
		}
	})

	t.Run("exec session", func(t *testing.T) {
		s, ctx, b, _, actor := setup(t, "exec", true)
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.Mail(pane, "yardmaster", "the script needs a narrower limit", "op"); err != nil {
			t.Fatal(err)
		}
		e := lastMail(t, s, ctx)
		if e.Actor != actor || e.Data["from_bead"] != b.ID {
			t.Fatalf("mail from an exec session's pane = %+v, want actor %q, from_bead %q", e, actor, b.ID)
		}
	})

	t.Run("ended session", func(t *testing.T) {
		if !peer.HaveStat() {
			t.Skip("this OS reports no process start time")
		}
		s, ctx, b, sess, actor := setup(t, "herdr", true)
		if err := s.EndSession(ctx, sess.ID, "died", "x"); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.Mail(pane, "yardmaster", "I am still here", "op"); err != nil {
			t.Fatal(err)
		}
		if e := lastMail(t, s, ctx); e.Actor != actor || e.Data["from_bead"] != b.ID {
			t.Fatalf("mail from an ended session's open pane = %+v, want actor %q, from_bead %q", e, actor, b.ID)
		}
	})

	t.Run("crew pane", func(t *testing.T) {
		s, ctx, _, _, _ := setup(t, "herdr", false)
		if err := s.CrewStarted(ctx, "yardmaster", paneShell(t, "yardmaster")); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.Mail(pane, "clerk", "the shed is painted", "op"); err != nil {
			t.Fatal(err)
		}
		e := lastMail(t, s, ctx)
		if _, named := e.Data["from_bead"]; e.Actor != "yardmaster" || named {
			t.Fatalf("mail from a crew member's pane = %+v, want actor yardmaster and no from_bead", e)
		}
	})

	t.Run("no pane", func(t *testing.T) {
		s, ctx, _, _, _ := setup(t, "herdr", false)
		if err := s.Mail(ctx, "yardmaster", "from a terminal", "op"); err != nil {
			t.Fatal(err)
		}
		e := lastMail(t, s, ctx)
		if _, named := e.Data["from_bead"]; e.Actor != "op" || named {
			t.Fatalf("mail from no pane = %+v, want actor op and no from_bead", e)
		}
	})
}

// An answer from a session's pane is recorded as that session's actor,
// whatever --actor says, and only the pane's actor's own asks are its
// to take back: one of the operator's is refused as the pane's. From
// no pane the actor stands as given.
func TestAnswerFromAPaneIsThatPanesActor(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, withPane bool) (store.API, context.Context, *store.Bead, *store.Session, string) {
		t.Helper()
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		actor := b.ID + "-new"
		if withPane {
			if err := s.MarkRunning(ctx, sess.ID, paneShell(t, actor)); err != nil {
				t.Fatal(err)
			}
		}
		return s, ctx, b, sess, actor
	}
	words := store.Ask{Question: "What tone?", To: "op", Context: []string{"the greeting"}}

	t.Run("pane", func(t *testing.T) {
		s, ctx, b, _, actor := setup(t, true)
		seq, err := s.AddAsk(ctx, b.ID, words, "op")
		if err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.AddAnswer(pane, b.ID, store.Answer{Ask: seq, Words: "warm"}, "op"); err != nil {
			t.Fatal(err)
		}
		notes, _ := s.Notes(ctx, b.ID)
		if len(notes) == 0 || notes[len(notes)-1].Author != actor {
			t.Fatalf("answer note = %+v, want author %q", notes, actor)
		}
	})

	t.Run("take back", func(t *testing.T) {
		s, ctx, b, sess, actor := setup(t, false)
		// The operator's ask lands before the pane is on record, so
		// it is theirs in both modes; over the socket the server
		// would otherwise resolve the plain call to the pane.
		theirs, err := s.AddAsk(ctx, b.ID, words, "op")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(ctx, sess.ID, paneShell(t, actor)); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		own, err := s.AddAsk(pane, b.ID, words, "op")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.TakeBackAsk(pane, b.ID, own, "never mind", "op"); err != nil {
			t.Fatalf("taking back the pane's own ask: %v", err)
		}
		notes, _ := s.Notes(ctx, b.ID)
		if len(notes) == 0 || notes[len(notes)-1].Author != actor {
			t.Fatalf("take-back note = %+v, want author %q", notes, actor)
		}
		err = s.TakeBackAsk(pane, b.ID, theirs, "mine now", "op")
		if err == nil || !strings.Contains(err.Error(), "this is "+actor) {
			t.Fatalf("taking back the operator's ask from the pane = %v, want a refusal as %q", err, actor)
		}
	})

	t.Run("no pane", func(t *testing.T) {
		s, ctx, b, _, _ := setup(t, false)
		seq, err := s.AddAsk(ctx, b.ID, words, "op")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: seq, Words: "warm"}, "op"); err != nil {
			t.Fatal(err)
		}
		notes, _ := s.Notes(ctx, b.ID)
		if len(notes) == 0 || notes[len(notes)-1].Author != "op" {
			t.Fatalf("answer note = %+v, want author op", notes)
		}
	})
}

// A journal entry or mail from a session's pane is recorded as that
// session's actor, whatever --actor says; from a crew member's pane as
// that crew member. From no pane the actor stands as given.
func TestCrewWriteFromAPaneIsThatPanesActor(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, withPane bool) (store.API, context.Context, string) {
		t.Helper()
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		actor := b.ID + "-new"
		if withPane {
			if err := s.MarkRunning(ctx, sess.ID, paneShell(t, actor)); err != nil {
				t.Fatal(err)
			}
		}
		return s, ctx, actor
	}

	t.Run("pane", func(t *testing.T) {
		s, ctx, actor := setup(t, true)
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.Journal(pane, "yardmaster", "journal", "an entry", "op"); err != nil {
			t.Fatal(err)
		}
		r, err := s.GetCrew(ctx, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		entries, _ := s.Notes(ctx, r.Journal)
		if len(entries) == 0 || entries[len(entries)-1].Author != actor {
			t.Fatalf("journal entry = %+v, want author %q", entries, actor)
		}
		if err := s.Mail(pane, "yardmaster", "hi", "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastKindActor(t, s, ctx, "mail"); a != actor {
			t.Fatalf("mail event actor = %q, want %q", a, actor)
		}
	})

	t.Run("crew pane", func(t *testing.T) {
		s := storetest.New(t)
		ctx := context.Background()
		if err := s.SetSetting(ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		h := map[string]any{"shell_pid": os.Getpid()}
		if st, err := peer.Describe(os.Getpid()); err == nil {
			h["shell_start"] = st.Start
		}
		if err := s.CrewStarted(ctx, "yardmaster", h); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.Mail(pane, "yardmaster", "hi", "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastKindActor(t, s, ctx, "mail"); a != "yardmaster" {
			t.Fatalf("mail event actor = %q, want yardmaster", a)
		}
	})

	t.Run("no pane", func(t *testing.T) {
		s, ctx, _ := setup(t, false)
		if err := s.Mail(ctx, "yardmaster", "hi", "op"); err != nil {
			t.Fatal(err)
		}
		if a := lastKindActor(t, s, ctx, "mail"); a != "op" {
			t.Fatalf("mail event actor = %q, want op", a)
		}
	})
}

// Mail to another yard from a session's pane may not go as a name the
// pane does not bear out: asked as the operator, it is refused as the
// pane's actor. From the crew member's own pane it goes as that member,
// whatever --actor says. A raw send that the link refuses logs the
// pane's actor, and a dispatch report names it too.
func TestPeerBoundWriteFromAPaneIsThatPanesActor(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T, withPane bool) (*peerYard, string) {
		t.Helper()
		y := newPeerYard(t, "http://north.test")
		y.member(t, "yardmaster", "north")
		if err := y.api.SetSetting(y.ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		if err := y.api.PutGroup(y.ctx, store.Group{Name: "builders", Runner: "herdr"}, "op"); err != nil {
			t.Fatal(err)
		}
		b, err := y.api.CreateBead(y.ctx, store.NewBead{Title: "a", Stage: "new"}, "op")
		if err != nil {
			t.Fatal(err)
		}
		sess, err := y.api.Claim(y.ctx, b.ID, "builders", "new", "yard")
		if err != nil {
			t.Fatal(err)
		}
		actor := b.ID + "-new"
		if withPane {
			if err := y.api.MarkRunning(y.ctx, sess.ID, paneShell(t, actor)); err != nil {
				t.Fatal(err)
			}
		}
		return y, actor
	}

	t.Run("mail forged from a session pane is refused", func(t *testing.T) {
		y, actor := setup(t, true)
		pane := store.WithCaller(y.ctx, os.Getpid())
		_, err := y.api.MailPeer(pane, "yardmaster", "north", "hello", "op")
		if err == nil || !strings.Contains(err.Error(), "this is "+actor) {
			t.Fatalf("mail to another yard as the operator from the pane = %v, want a refusal as %q", err, actor)
		}
	})

	t.Run("mail from a crew pane goes as the member", func(t *testing.T) {
		y := newPeerYard(t, "http://north.test")
		y.member(t, "yardmaster", "north")
		y.allow(t, store.PeerAllow{Send: []string{"mail"}})
		if err := y.api.SetSetting(y.ctx, store.OperatorSetting, "op", "op"); err != nil {
			t.Fatal(err)
		}
		h := map[string]any{"shell_pid": os.Getpid()}
		if st, err := peer.Describe(os.Getpid()); err == nil {
			h["shell_start"] = st.Start
		}
		if err := y.api.CrewStarted(y.ctx, "yardmaster", h); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(y.ctx, os.Getpid())
		out, err := y.api.MailPeer(pane, "yardmaster", "north", "hello", "op")
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(out.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["from"] != "yardmaster" {
			t.Fatalf("mail to another yard as the operator from the crew pane = %+v, want from yardmaster", body)
		}
		if a := lastKindActor(t, y.api, y.ctx, "mail"); a != "yardmaster" {
			t.Fatalf("mail event actor = %q, want yardmaster", a)
		}
	})

	t.Run("raw send refused logs the pane", func(t *testing.T) {
		y, actor := setup(t, true)
		pane := store.WithCaller(y.ctx, os.Getpid())
		// The link carries nothing: the refusal is logged with who sent.
		_, err := y.api.PeerSend(pane, "north", "note", json.RawMessage("{}"), "op")
		if err == nil || !strings.Contains(err.Error(), "does not carry note out") {
			t.Fatalf("PeerSend over a link that carries none = %v", err)
		}
		if a := lastKindActor(t, y.api, y.ctx, "peer_message_refused"); a != actor {
			t.Fatalf("peer_message_refused event actor = %q, want %q", a, actor)
		}
	})

	t.Run("dispatch report", func(t *testing.T) {
		y, actor := setup(t, true)
		pane := store.WithCaller(y.ctx, os.Getpid())
		if err := y.api.LogDispatch(pane, "", "session_idle", "yard", map[string]any{}); err != nil {
			t.Fatal(err)
		}
		if a := lastKindActor(t, y.api, y.ctx, "session_idle"); a != actor {
			t.Fatalf("session_idle event actor = %q, want %q", a, actor)
		}
		// No pane on record anywhere: over the socket the server
		// sees this test process as the caller.
		plain, _ := setup(t, false)
		if err := plain.api.LogDispatch(plain.ctx, "", "session_idle", "yard", map[string]any{}); err != nil {
			t.Fatal(err)
		}
		if a := lastKindActor(t, plain.api, plain.ctx, "session_idle"); a != "yard" {
			t.Fatalf("session_idle event actor from no pane = %q, want yard", a)
		}
	})
}

// Peer notes and reads also carry the pane's actor into peer-bound state:
// a note says who wrote it, and a failed read is reported to who asked.
func TestPeerNoteAndReadFromACrewPaneUseTheCrewActor(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) (*peerYard, *store.Bead, context.Context) {
		t.Helper()
		y, _ := newSendingYard(t)
		y.allow(t, store.PeerAllow{
			Send:    []string{store.PeerBead, store.PeerThread},
			Receive: []string{store.PeerThread},
		})
		b, _ := y.send(t, store.NewBead{Title: "a"})
		y.member(t, "yardmaster", "north")
		h := map[string]any{"shell_pid": os.Getpid()}
		if st, err := peer.Describe(os.Getpid()); err == nil {
			h["shell_start"] = st.Start
		}
		if err := y.api.CrewStarted(y.ctx, "yardmaster", h); err != nil {
			t.Fatal(err)
		}
		return y, b, store.WithCaller(y.ctx, os.Getpid())
	}

	t.Run("peer note", func(t *testing.T) {
		y, b, pane := setup(t)
		noted, err := y.api.PeerNote(pane, b.ID, "A note from the pane.", "op")
		if err != nil {
			t.Fatal(err)
		}
		out, err := y.api.PeerOutbox(y.ctx, "north", 0)
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			From string `json:"from"`
		}
		for _, m := range out {
			if m.ID == noted.ID {
				if err := json.Unmarshal(m.Body, &body); err != nil {
					t.Fatal(err)
				}
			}
		}
		if body.From != "yardmaster" {
			t.Fatalf("peer note sender = %q, want yardmaster", body.From)
		}
		if a := lastEventActor(t, y.api, y.ctx, b.ID, "peer_bead_noted"); a != "yardmaster" {
			t.Fatalf("peer_bead_noted actor = %q, want yardmaster", a)
		}
	})

	t.Run("failed peer read", func(t *testing.T) {
		y, b, pane := setup(t)
		read, err := y.api.PeerRead(pane, b.ID, "op")
		if err != nil {
			t.Fatal(err)
		}
		out, err := y.api.PeerOutbox(y.ctx, "north", 0)
		if err != nil {
			t.Fatal(err)
		}
		var seq uint64
		for _, m := range out {
			if m.ID == read.ID && m.Kind == store.PeerBeadRead {
				seq = m.Seq
			}
		}
		if seq == 0 {
			t.Fatalf("peer read %q is not queued: %+v", read.ID, out)
		}
		if err := y.api.PeerRefused(y.ctx, "north", seq, "peer north: refused by the peer"); err != nil {
			t.Fatal(err)
		}
		inbox := y.inbox(t, "yardmaster")
		if len(inbox) != 1 || inbox[0].Data["to"] != "yardmaster" {
			t.Fatalf("failed-read mail = %+v, want a message to yardmaster", inbox)
		}
	})
}

// The setup-path writes stay anyone's, and from a session's or a crew
// member's pane are recorded as the pane's actor, whatever --actor says:
// a crew member setting up the yard is not refused, and cannot pass its
// writes as the operator by naming them.
func TestSetupWritesFromAPaneAreThatPanesActor(t *testing.T) {
	t.Parallel()
	t.Run("crew pane", func(t *testing.T) {
		api, _ := operatorYard(t)
		ctx := context.Background()
		// The grant first, as the operator, from no pane: the pane
		// revokes it. No call is made, so the base is a dummy.
		if _, err := api.PutSource(ctx, "crm.yaml", strings.Replace(crmCalls, "BASE", "http://127.0.0.1:9", 1), "jo"); err != nil {
			t.Fatal(err)
		}
		if _, err := api.GrantSource(ctx, "crm", store.GrantRead, store.Grantee{Crew: "yardmaster"}, "", "jo"); err != nil {
			t.Fatal(err)
		}
		f, err := store.ParseFlow(outcomeFlow)
		if err != nil {
			t.Fatal(err)
		}
		f.Name = "f1"
		// The pane goes on record after the setup: over the socket
		// every call carries this process as its caller.
		me := os.Getpid()
		handle := map[string]any{"shell_pid": me}
		if ps, err := peer.Describe(me); err == nil {
			handle["shell_start"] = ps.Start
		}
		if err := api.CrewStarted(ctx, "yardmaster", handle); err != nil {
			t.Fatal(err)
		}
		pane := store.WithCaller(ctx, me)
		as := func(kind string) {
			t.Helper()
			if a := lastKindActor(t, api, ctx, kind); a != "yardmaster" {
				t.Fatalf("%s event actor = %q, want the pane's actor yardmaster", kind, a)
			}
		}
		if err := api.PutDepot(pane, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
			t.Fatalf("PutDepot from the pane as the operator: %v", err)
		}
		as("depot_updated")
		if _, err := api.PutFlow(pane, *f, "jo"); err != nil {
			t.Fatalf("PutFlow from the pane as the operator: %v", err)
		}
		as("flow_set")
		if err := api.SetFlowPointer(pane, "yard", "", "f1", "jo"); err != nil {
			t.Fatalf("SetFlowPointer from the pane as the operator: %v", err)
		}
		as("flow_pointer_set")
		if err := api.PutGroup(pane, store.Group{Name: "builders", Runner: "herdr"}, "jo"); err != nil {
			t.Fatalf("PutGroup from the pane as the operator: %v", err)
		}
		as("group_set")
		limit := 3
		if _, err := api.SetGroup(pane, "builders", store.GroupEdit{Limit: &limit}, "jo"); err != nil {
			t.Fatalf("SetGroup from the pane as the operator: %v", err)
		}
		as("group_set")
		if _, err := api.AddRoute(pane, store.Route{Stage: "new", Group: "builders"}, "jo"); err != nil {
			t.Fatalf("AddRoute from the pane as the operator: %v", err)
		}
		as("route_added")
		if _, err := api.RecordPack(pane, store.PackImport{Pack: "starter", Depot: "yard"}, true, "jo"); err != nil {
			t.Fatalf("RecordPack from the pane as the operator: %v", err)
		}
		as("pack_applied")
		if recs, _ := api.Packs(ctx); len(recs) != 1 || recs[0].By != "yardmaster" {
			t.Fatalf("pack record = %+v, want it by yardmaster", recs)
		}
		if g, err := api.RevokeSource(pane, "crm", store.GrantRead, store.Grantee{Crew: "yardmaster"}, "", "jo"); err != nil {
			t.Fatalf("RevokeSource from the pane as the operator: %v", err)
		} else if len(g.Calls) != 0 {
			t.Fatalf("grant after the revoke = %+v", g)
		}
		as("source_revoked")
		if err := api.SetCrewEnabled(pane, "clerk", false, "", "jo"); err != nil {
			t.Fatalf("disabling a crew member from the pane as the operator: %v", err)
		}
		as("crew_disabled")
	})

	t.Run("session pane", func(t *testing.T) {
		s, ctx, _, _, actor := paneMoveYard(t)
		pane := store.WithCaller(ctx, os.Getpid())
		if err := s.PutGroup(pane, store.Group{Name: "reviewers", Runner: "herdr"}, "op"); err != nil {
			t.Fatalf("PutGroup from the pane as the operator: %v", err)
		}
		if a := lastKindActor(t, s, ctx, "group_set"); a != actor {
			t.Fatalf("group_set event actor = %q, want the pane's actor %q", a, actor)
		}
		if err := s.PutDepot(pane, store.Depot{Name: "web", Base: "origin/main"}, "op"); err != nil {
			t.Fatalf("PutDepot from the pane as the operator: %v", err)
		}
		if a := lastKindActor(t, s, ctx, "depot_updated"); a != actor {
			t.Fatalf("depot_updated event actor = %q, want %q", a, actor)
		}
	})
}
