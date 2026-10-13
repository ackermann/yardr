package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A note on a bead with a live session says the session will be told; one
// by the session itself says nothing of the kind. The session acks what it
// read, and bead show says how each note stands with its session, so the
// one who wrote it sees whether it was read.
func TestBeadNoteAck(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed", Stage: "new"}, "me"); err != nil {
		t.Fatal(err)
	}
	sess, err := st.Claim(ctx, "yd-1", "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "yardmaster"}

	out, err := run(t, a.beadCmd(), "note", "yd-1", "narrower: only the shed")
	if err != nil {
		t.Fatal(err)
	}
	notes, _ := st.Notes(ctx, "yd-1")
	seq := notes[len(notes)-1].Seq
	if want := fmt.Sprintf("note %d on yd-1 (session %s will be told)\n", seq, sess.ID); out != want {
		t.Fatalf("bead note = %q, want %q", out, want)
	}
	out, err = run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("(note %d), session %s will be told", seq, sess.ID); !strings.Contains(out, want) {
		t.Fatalf("bead show lacks %q:\n%s", want, out)
	}

	// As another session, and with text, the ack is refused.
	t.Setenv("YARDR_SESSION", "another")
	if _, err := run(t, a.beadCmd(), "note", "yd-1", "--ack", fmt.Sprint(seq)); err == nil ||
		!strings.Contains(err.Error(), "was told to session "+sess.ID) {
		t.Fatalf("another session's ack: %v", err)
	}
	if _, err := run(t, a.beadCmd(), "note", "yd-1", "words", "--ack", fmt.Sprint(seq)); err == nil ||
		!strings.Contains(err.Error(), "takes the bead and no text") {
		t.Fatalf("an ack with text: %v", err)
	}
	if _, err := run(t, a.beadCmd(), "note", "yd-1", "--ack", "999"); err == nil {
		t.Fatal("acked a note the bead has not")
	}

	// As the told session the ack lands, and bead show says the note was read.
	t.Setenv("YARDR_SESSION", sess.ID)
	out, err = run(t, a.beadCmd(), "note", "yd-1", "--ack", fmt.Sprint(seq))
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("note %d on yd-1 acked\n", seq); out != want {
		t.Fatalf("bead note --ack = %q, want %q", out, want)
	}
	out, err = run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("(note %d), read by session %s", seq, sess.ID); !strings.Contains(out, want) {
		t.Fatalf("bead show lacks %q:\n%s", want, out)
	}

	// The session's own note tells nothing, and says nothing of telling.
	out, err = run(t, a.beadCmd(), "note", "yd-1", "on it")
	if err != nil {
		t.Fatal(err)
	}
	notes, _ = st.Notes(ctx, "yd-1")
	own := notes[len(notes)-1].Seq
	if want := fmt.Sprintf("note %d on yd-1\n", own); out != want {
		t.Fatalf("the session's own note = %q, want %q", out, want)
	}

	a.json = true
	out, err = run(t, a.beadCmd(), "note", "yd-1", "--ack", fmt.Sprint(seq))
	if err != nil {
		t.Fatal(err)
	}
	var acked map[string]any
	if err := json.Unmarshal([]byte(out), &acked); err != nil || acked["bead"] != "yd-1" || acked["acked"] != "ok" {
		t.Fatalf("bead note --ack --json = %q, %v", out, err)
	}
}
