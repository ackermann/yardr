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
	t.Parallel()
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
	a := testApp(t, st, newHomeDir(t))
	a.actor = "yardmaster"

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
	yardEnv(t, st)["YARDR_SESSION"] = "another"
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
	yardEnv(t, st)["YARDR_SESSION"] = sess.ID
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

	// A note neither delivery took says so, with why, instead of "will be
	// told".
	yardEnv(t, st)["YARDR_SESSION"] = ""
	_, err = run(t, a.beadCmd(), "note", "yd-1", "wider: the fence too")
	if err != nil {
		t.Fatal(err)
	}
	notes, _ = st.Notes(ctx, "yd-1")
	lost := notes[len(notes)-1].Seq
	if err := st.MarkNoteUntold(ctx, "yd-1", lost, sess.ID, "no agent in the handle"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("(note %d), session %s could not be told: no agent in the handle", lost, sess.ID); !strings.Contains(out, want) {
		t.Fatalf("bead show lacks %q:\n%s", want, out)
	}

	yardEnv(t, st)["YARDR_SESSION"] = sess.ID
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

// yardr bead done from the session working the bead is refused while a
// note to it is unread, and the refusal shows the note; after the ack the
// same done passes. From outside any session the same done is not refused.
func TestBeadDoneRefusedWhileNoteUnread(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("new -> review on done\nreview -> approved on done\napproved -> merged\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "yard", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "g", Runner: "fake", Limit: 2}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"yd-1", "yd-2"} {
		if _, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: "yard", Stage: "new"}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Claim(ctx, id, "g", "new", "yard"); err != nil {
			t.Fatal(err)
		}
	}
	a := testApp(t, st, newHomeDir(t))
	a.actor = "yardmaster"
	for _, id := range []string{"yd-1", "yd-2"} {
		if _, err := run(t, a.beadCmd(), "note", id, "narrower: only the shed"); err != nil {
			t.Fatal(err)
		}
	}

	// Outside any session the done is not refused.
	out, err := run(t, a.beadCmd(), "done", "yd-2")
	if err != nil {
		t.Fatalf("operator's done: %v", err)
	}
	if out != "yd-2 → review\n" {
		t.Fatalf("operator's done printed %q", out)
	}

	// From the session it is refused, and the refusal shows the note.
	sess, err := st.GetBead(ctx, "yd-1")
	if err != nil || sess.Session == nil {
		t.Fatal("yd-1 has no session")
	}
	yardEnv(t, st)["YARDR_SESSION"] = *sess.Session
	notes, _ := st.Notes(ctx, "yd-1")
	seq := notes[len(notes)-1].Seq
	_, err = run(t, a.beadCmd(), "done", "yd-1")
	if err == nil {
		t.Fatal("the session's done with an unread note passed")
	}
	for _, want := range []string{
		fmt.Sprintf("note %d", seq), "yardmaster", "narrower: only the shed",
		"yardr bead note yd-1 --ack", "try again",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not mention %q", err, want)
		}
	}

	if _, err := run(t, a.beadCmd(), "note", "yd-1", "--ack", fmt.Sprint(seq)); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.beadCmd(), "done", "yd-1")
	if err != nil {
		t.Fatalf("done after ack: %v", err)
	}
	if out != "yd-1 → review\n" {
		t.Fatalf("done after ack printed %q", out)
	}
}
