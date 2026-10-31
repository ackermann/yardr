package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The yard's own words about a failed fetch or login are the three the
// devices are not told whole: the note of a peer's branch that could not be
// taken, the note of a note from another yard for a bead whose flow does not
// read, and the words of a login ask taken back when the login ended. Each
// is held from a device that is no operator's (HeldYardNote, HeldYardWords),
// as what happened with why an operator's to read, and reads whole anywhere
// else. A person's words, and any other of the yard's, are held nothing.
func TestHeldYardWordsOfAFailedFetchOrLogin(t *testing.T) {
	t.Parallel()
	y := newHomeYard(t)
	y.threads(t)

	// A peer's branch that could not be taken, for a reason naming the
	// worktree it is checked out in, as TakePeerBranch says it.
	fetch, err := y.CreateBead(y.ctx, NewBead{Title: "From north, with code"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	at := y.now
	if err := y.write(func(w *wtx) error {
		return put(w.Tx, bPeerSent, fetch.UUID, peerSent{Bead: fetch.UUID, Was: "north",
			Stage: "work", Message: "stay-1", At: y.now, EndedAt: &at, Outcome: "done",
			Git: &PeerGit{Branch: "yard/yb-1", Commit: "abc1234", Remote: "origin"}})
	}); err != nil {
		t.Fatal(err)
	}
	const worktree = "/tmp/yardr-test/wt"
	reason := "this yard's yard/yb-1 is checked out in " + worktree + " and could not be moved to abc1234: exit status 1"
	if err := y.PeerFetchFailed(y.ctx, fetch.ID, "stay-1", reason, y.now.Add(time.Hour), true); err != nil {
		t.Fatal(err)
	}
	notes, err := y.Notes(y.ctx, fetch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].Author != "yard" || !strings.Contains(notes[0].Text, "could not take the branch (1 try)") {
		t.Fatalf("the note of the failed fetch = %+v", notes)
	}
	held, ok := HeldYardNote(notes[0].Author, notes[0].Text)
	if !ok || held != "A peer returned this bead with a branch this yard could not take, so the bead is held; why is an operator's to read." {
		t.Errorf("the failed fetch is told %q, held: %v", held, ok)
	}
	if strings.Contains(held, worktree) {
		t.Errorf("the failed fetch is told the worktree: %q", held)
	}
	fetchText := notes[0].Text

	// A note from another yard for a bead whose flow is gone.
	if err := y.PutDepot(y.ctx, Depot{Name: "web", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	f, err := ParseFlow("arrived -> work on done\nwork -> returned on done\nreturned terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "gone"
	if _, err := y.PutFlow(y.ctx, *f, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := y.CreateBead(y.ctx, NewBead{Title: "From north", Depot: "web", Stage: "work", Metadata: map[string]any{"flow": "gone"}}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.write(func(w *wtx) error {
		if err := w.Bucket(bFlows).Delete([]byte("gone")); err != nil {
			return err
		}
		return put(w.Tx, bPeerBeads, b.UUID, peerBead{Bead: b.UUID, Key: y.key, Direction: "in", Theirs: "n-1", Stage: "build", Message: "stay-2", At: now(), Arrived: "arrived"})
	}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(PeerNoteMessage{V: 1, From: "jo", Text: "CSV only."})
	if got := y.take(t, PeerMessage{ID: "n1", Kind: PeerBeadNote, Re: "stay-2", Body: body}); got.Refused != "" {
		t.Fatalf("the note was refused: %s", got.Refused)
	}
	notes, err = y.Notes(y.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 || notes[1].Author != "yard" || !strings.Contains(notes[1].Text, "this bead's flow does not read") {
		t.Fatalf("the notes of the unreadable flow = %+v", notes)
	}
	held, ok = HeldYardNote(notes[1].Author, notes[1].Text)
	if !ok || held != "A note came from another yard and this bead's flow does not read, so the bead is held; why is an operator's to read." {
		t.Errorf("the unreadable flow is told %q, held: %v", held, ok)
	}

	// A login ask taken back when the login ended, with the tail of the
	// login command's output, as the dispatcher's endLead writes it.
	login, err := y.CreateBead(y.ctx, NewBead{Title: "Crew journal"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	seq, err := y.AddAsk(y.ctx, login.ID, Ask{Question: "code is not logged in: how should the yard log in its harness?", To: "mckean",
		Context: []string{"The crew's harness sits at its login screen.", "Nothing starts until the yard leads its login."}}, "yard")
	if err != nil {
		t.Fatal(err)
	}
	const loginFile = "/tmp/yardr-test/login"
	if err := y.TakeBackAsk(y.ctx, login.ID, seq, LoginEndedPrefix+"harness login: exit status 1: cannot write "+loginFile+": permission denied", "yard"); err != nil {
		t.Fatal(err)
	}
	v, err := y.Bead(y.ctx, login.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Asks) != 1 || v.Asks[0].By != "yard" || !strings.HasPrefix(v.Asks[0].Words, LoginEndedPrefix) {
		t.Fatalf("the bead's login ask = %+v", v.Asks)
	}
	held, ok = HeldYardWords(v.Asks[0].By, v.Asks[0].Words)
	if !ok || held != "the login ended; why is an operator's to read." {
		t.Errorf("the login's end is told %q, held: %v", held, ok)
	}
	if strings.Contains(held, loginFile) {
		t.Errorf("the login's end is told the file: %q", held)
	}

	// Held nothing: a person's telling of the same failure, any other of
	// the yard's notes, and words that are no login's end.
	if held, ok := HeldYardNote("mckean", fetchText); ok {
		t.Errorf("a person's telling of the same failure is told %q", held)
	}
	if held, ok := HeldYardNote("yard", "The yard north returned its branch yard/n-1 at abc1234, which is yard/yb-2 here now."); ok {
		t.Errorf("another of the yard's notes is told %q", held)
	}
	if held, ok := HeldYardWords("mckean", LoginEndedPrefix+"the harness said no"); ok {
		t.Errorf("a person's words are told %q", held)
	}
	if held, ok := HeldYardWords("yard", "the design went back to draft"); ok {
		t.Errorf("words that are no login's end are told %q", held)
	}
}
