package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// A note for a bead whose flow does not read cannot ask who works the
// flow's first stage, so nothing says who looks at the other yard's text
// first: the note is written, and the bead is held by the yard with a note
// of its own that says why. A second note finds it held and holds nothing
// again.
func TestPeerBeadNoteOfABeadWhoseFlowIsGone(t *testing.T) {
	t.Parallel()
	y := newHomeYard(t)
	y.threads(t)
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
		return put(w.Tx, bPeerBeads, b.UUID, peerBead{Bead: b.UUID, Key: y.key, Direction: "in", Theirs: "n-1", Stage: "build", Message: "stay-1", At: now(), Arrived: "arrived"})
	}); err != nil {
		t.Fatal(err)
	}
	note := func(id string) {
		t.Helper()
		body, _ := json.Marshal(PeerNoteMessage{V: 1, From: "jo", Text: "CSV only."})
		if got := y.take(t, PeerMessage{ID: id, Kind: PeerBeadNote, Re: "stay-1", Body: body}); got.Refused != "" {
			t.Fatalf("the note %s was refused: %s", id, got.Refused)
		}
	}
	note("n1")
	got, err := y.GetBead(y.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := y.Notes(y.ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != "work" || !got.Hold || got.HeldBy != "yard" || len(notes) != 2 || notes[0].Author != "jo@north" || notes[1].Author != "yard" ||
		!strings.Contains(notes[1].Text, "this bead's flow does not read") || !strings.Contains(notes[1].Text, "yardr bead unhold "+b.ID) {
		t.Fatalf("the bead after a note it has no flow for = %+v, notes %+v", got, notes)
	}
	held := 0
	evs, err := y.Events(y.ctx, b.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == "held" {
			held++
			if e.Actor != "yard" || e.Data["peer"] != "north" {
				t.Fatalf("the hold = %+v", e)
			}
		}
	}
	note("n2")
	evs, err = y.Events(y.ctx, b.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range evs {
		if e.Kind == "held" {
			held--
		}
	}
	if held != 0 {
		t.Fatalf("a second note held the held bead again (%d)", held)
	}
}
