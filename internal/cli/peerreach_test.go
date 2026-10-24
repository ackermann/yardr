package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// yardr peer allow --reach <name>@<peer>=<user> is a second grant beside
// trust: from then on an ask for the user is also queued for that name,
// and the command says so; peer allow, peer show and doctor say in words
// what is sent. The person picks at the other yard, and home the answer
// stands on the bead as that user's word and the ask is out of their
// queue. --no-reach takes it back, which anyone may, and an answer that
// arrives afterwards is refused and marked there as no longer asked.
func TestAskFollowsReach(t *testing.T) {
	t.Parallel()
	north, south := newCLIYard(t, "north"), newCLIYard(t, "south")
	ctx := context.Background()
	north.a.env = testEnv(t, north.home)
	nInvite := strings.TrimSpace(north.must(t, "invite", "--url", north.srv.URL))
	south.a.env = testEnv(t, south.home)
	sInvite := strings.TrimSpace(south.must(t, "invite", "--url", south.srv.URL))
	north.must(t, "add", "south", sInvite)
	south.must(t, "add", "up-north", nInvite)
	if _, err := south.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := north.st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "jo"); err != nil {
		t.Fatal(err)
	}
	north.must(t, "allow", "south", "--send", "ask,mail", "--receive", "ask", "--expose", "jo,yardmaster")
	south.must(t, "allow", "up-north", "--send", "ask", "--receive", "ask,mail")
	north.a.actor = "jo"
	if _, err := run(t, north.a.mailCmd(), "yardmaster@south", "hello"); err != nil {
		t.Fatal(err)
	}
	carry(t, north, south)
	b, err := south.st.CreateBead(ctx, store.NewBead{Title: "the work"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	ask := func(as string, more ...string) string {
		t.Helper()
		south.a.actor = as
		defer func() { south.a.actor = "jo" }()
		out, err := run(t, south.a.beadCmd(), append([]string{"ask", b.ID, "Move the links now?",
			"--context", "64 links still point at the old source.",
			"--option", "Move them now: all 64 links move tonight"}, more...)...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// In this order: the last refusal is reached only by a name the link
	// trusts, so the trust before it is given first.
	for _, c := range []struct{ args, want string }{
		{"--reach jo@up-north=jo", "peer up-north: the link does not trust jo@up-north as jo, and reach is given only to a name whose word is that user's already (yardr peer allow up-north --trust jo@up-north=jo first)"},
		{"--reach jo@elsewhere=jo", "--reach jo@elsewhere: that is a name of elsewhere, and this changes the link with up-north"},
		{"--reach jo@up-north", "--reach jo@up-north: say whose asks are sent to the name, <name>@up-north=<user of this yard>"},
		{"--no-reach jo", "--no-reach jo: name the sender as mail does, <name>@up-north"},
		{"--trust yardmaster@up-north=jo", ""},
		{"--reach yardmaster@up-north=jo", "peer up-north: yardmaster is no person on the roster it last said, and only a person has decisions to be asked (yardr peer show up-north lists whom it exposes)"},
	} {
		_, err := south.peer(t, append([]string{"allow", "up-north"}, strings.Fields(c.args)...)...)
		if c.want == "" {
			if err != nil {
				t.Fatalf("allow %s = %v", c.args, err)
			}
		} else if err == nil || err.Error() != c.want {
			t.Errorf("allow %s = %v, want %q", c.args, err, c.want)
		}
	}
	south.must(t, "allow", "up-north", "--trust", "yardmaster@up-north=jo")
	south.must(t, "allow", "up-north", "--trust", "jo@up-north=jo")
	south.a.actor = "yardmaster"
	if _, err := south.peer(t, "allow", "up-north", "--reach", "jo@up-north=jo"); err == nil ||
		err.Error() != "only the operator (jo) may send a user's asks to a name of a peer; this is yardmaster" {
		t.Fatalf("a crew member giving reach = %v", err)
	}
	south.a.actor = "jo"
	// The grant reports zero too when it has no earlier asks to copy.
	wantText(t, south.must(t, "allow", "up-north", "--reach", "jo@up-north=jo"),
		"0 open asks for jo stay in this yard alone: reach sends what is asked from now on\n")
	south.must(t, "allow", "up-north", "--no-reach", "jo@up-north")

	// Trust alone sends nothing, and says nothing of it.
	if out := ask("yardmaster"); strings.Contains(out, "also queued") {
		t.Fatalf("an ask under trust alone:\n%s", out)
	}
	if show := south.must(t, "show", "up-north"); strings.Contains(show, "reach:") {
		t.Fatalf("a link that gives nobody reach:\n%s", show)
	}

	const grant = "reach: jo@up-north -> jo: every ask for jo is also sent there, the ask whole, with its bead's handle, title, topic, stage, labels and cut body, and the handle, title and stage of each bead it is about, and the answer from there is taken as jo's"
	wantText(t, south.must(t, "allow", "up-north", "--reach", "jo@up-north=jo"),
		"trusted as: jo@up-north -> jo, yardmaster@up-north -> jo\n", grant+"\n",
		"1 open ask for jo stays in this yard alone: reach sends what is asked from now on\n")
	wantText(t, south.must(t, "show", "up-north"), "\n  "+grant+"\n")
	part, err := peerPart(ctx, south.a.cfg, south.st)
	if err != nil || !strings.Contains(part.Detail, "; "+grant+")") || strings.Contains(strings.Join(part.Notes, "\n"), "sends the asks") {
		t.Fatalf("doctor's peers = %+v, %v", part, err)
	}
	if queued, _ := south.st.PeerOutbox(ctx, "up-north", 0); len(queued) != 0 {
		t.Fatalf("giving reach queued %+v", queued)
	}

	// A bead's agent asks the operator, by default: the copy goes too.
	out := ask("yard-10-new")
	wantText(t, out, "ask 2 · open · to jo · asked by yard-10-new", "ask 2 on "+b.ID+" for jo; also queued for jo@up-north\n")
	j := &app{st: south.st, actor: "yardmaster", json: true, cfg: south.a.cfg, env: south.a.env}
	js, err := run(t, j.beadCmd(), "ask", b.ID, "And the mirrors?", "--context", "They follow the links.")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Ask  uint64
		To   string
		Also []string
	}
	if err := json.Unmarshal([]byte(js), &got); err != nil || got.Ask != 3 || got.To != "jo" || len(got.Also) != 1 || got.Also[0] != "jo@up-north" {
		t.Fatalf("ask --json = %s, %v", js, err)
	}
	carry(t, south, north)

	// It stands in both queues, and is picked where the person is.
	ref := b.ID + "@south"
	q, err := north.st.UserAsks(ctx, "jo", "jo")
	if err != nil || len(q.Open) != 2 || q.Open[0].Bead != ref || q.Open[0].Asker != "yard-10-new@south" {
		t.Fatalf("north's queue = %+v, %v", q, err)
	}
	if q, err := south.st.UserAsks(ctx, "jo", "jo"); err != nil || len(q.Open) != 3 {
		t.Fatalf("south's queue = %+v, %v", q, err)
	}
	if _, err := run(t, north.a.beadCmd(), "answer", ref, "2", "1", "tonight"); err != nil {
		t.Fatal(err)
	}
	carry(t, north, south)
	prime, err := run(t, south.a.primeCmd(), "--bead", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantText(t, prime, "jo@up-north (as jo) picked 1 (Move them now): tonight")
	if q, err := south.st.UserAsks(ctx, "jo", "jo"); err != nil || len(q.Open) != 2 || q.Open[0].Ask != 1 || q.Open[1].Ask != 3 {
		t.Fatalf("south's queue after the pick = %+v, %v", q, err)
	}
	notes, err := south.st.Notes(ctx, b.ID)
	if err != nil || notes[len(notes)-1].Kind != store.NoteAnswer || notes[len(notes)-1].Author != "jo@up-north" {
		t.Fatalf("the answer's note = %+v, %v", notes[len(notes)-1], err)
	}

	// Taken back by anyone: the next ask stays here, and the answer to
	// one that was copied is refused and marked where it was given.
	south.a.actor = "yardmaster"
	if out := south.must(t, "allow", "up-north", "--no-reach", "jo@up-north"); strings.Contains(out, "reach:") || !strings.Contains(out, "trusted as: jo@up-north -> jo") {
		t.Fatalf("after --no-reach: %s", out)
	}
	south.a.actor = "jo"
	if out := ask("yardmaster"); strings.Contains(out, "also queued") {
		t.Fatalf("an ask after --no-reach:\n%s", out)
	}
	if _, err := run(t, north.a.beadCmd(), "answer", ref, "3", "--words", "they follow"); err != nil {
		t.Fatal(err)
	}
	carry(t, north, south)
	carry(t, north, south)
	for _, r := range store.ResolveAsks(mustNotes(t, south.st, b.ID), time.Now()) {
		if r.Note.Seq == 3 && r.State != store.AskOpen {
			t.Fatalf("ask 3 after reach was taken back: %v", r.State)
		}
	}
	if _, err := run(t, north.a.beadCmd(), "answer", ref, "3", "--words", "again"); err == nil ||
		!strings.Contains(err.Error(), "it is no longer asked in south's yard") {
		t.Fatalf("a second answer to what is asked no more = %v", err)
	}

	// A question about no bead follows reach as any ask does: the bead
	// made for it is what the other yard is shown.
	south.must(t, "allow", "up-north", "--reach", "jo@up-north=jo")
	south.a.actor = "yardmaster"
	out, err = run(t, south.a.beadCmd(), "ask", "--new", "Rename 'depot'?", "--label", "topic:words",
		"--context", "The glossary keeps it.", "--option", "Keep depot: nothing changes")
	south.a.actor = "jo"
	if err != nil {
		t.Fatal(err)
	}
	made, err := south.st.ListBeads(ctx, store.BeadFilter{Label: "topic:words"})
	if err != nil || len(made) != 1 {
		t.Fatalf("the bead of ask --new = %+v, %v", made, err)
	}
	asked := mustNotes(t, south.st, made[0].ID)
	if len(asked) != 1 {
		t.Fatalf("the notes of the bead of ask --new = %+v", asked)
	}
	wantText(t, out, fmt.Sprintf("%s created; ask %d on it for jo; also queued for jo@up-north\n", made[0].ID, asked[0].Seq))
	carry(t, south, north)
	q, err = north.st.UserAsks(ctx, "jo", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(q.Open, func(o store.OpenAsk) bool { return o.Bead == made[0].ID+"@south" }); i < 0 ||
		q.Open[i].BeadTitle != "Rename 'depot'?" || q.Open[i].Topic != "topic:words" {
		t.Fatalf("north's queue after ask --new = %+v", q.Open)
	}
}

func mustNotes(t *testing.T, st store.API, id string) []store.Note {
	t.Helper()
	notes, err := st.Notes(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return notes
}

// The store drops a grant of reach with the trust it stands on and with
// the roster's person. doctor says of one that is left all the same what
// it stands on no more, and how it is taken off.
func TestDoctorReachOnNothing(t *testing.T) {
	t.Parallel()
	p := store.Peer{Name: "north", Send: []string{store.PeerAsk},
		Trust: map[string]string{"ada": "jo", "bo": "jo", "cy": "ada"},
		Reach: map[string]string{"ada": "jo", "bo": "jo", "cy": "jo", "di": "jo"},
		Said: &store.PeerSaid{At: time.Now(), PeerSays: store.PeerSays{Roster: []store.PeerRosterEntry{
			{Name: "ada", Kind: store.RosterUser}, {Name: "bo", Kind: store.RosterCrew}, {Name: "cy", Kind: store.RosterUser}}}}}
	var got []string
	for _, n := range trustNotes(p, []string{"jo", "ada"}, "yardr") {
		if strings.Contains(n, "sends the asks") {
			got = append(got, n)
		}
	}
	want := []string{
		"the link with peer north sends the asks for jo to bo@north, and north exposes no person called bo: yardr peer allow north --no-reach bo@north",
		"the link with peer north sends the asks for jo to cy@north, and the link does not trust cy@north as jo: yardr peer allow north --no-reach cy@north",
		"the link with peer north sends the asks for jo to di@north, and the link does not trust di@north as jo: yardr peer allow north --no-reach di@north",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("trustNotes =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// A link that carries no ask out says that nothing is sent.
	p.Send = nil
	if lines := reachLines(p); len(lines) != 4 || !strings.HasSuffix(lines[0], " (nothing is sent while the link carries no ask out: yardr peer allow north --send ask)") {
		t.Fatalf("reachLines = %q", lines)
	}
}
