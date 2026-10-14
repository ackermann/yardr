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

// An update of the body of a bead with a live session says the session
// will be told, as a note does; a label patch, and the session's own
// edit, say nothing of the kind.
func TestBeadUpdateTells(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed", Body: "paint it", Stage: "new"}, "me"); err != nil {
		t.Fatal(err)
	}
	sess, err := st.Claim(ctx, "yd-1", "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "yardmaster"}

	out, err := run(t, a.beadCmd(), "update", "yd-1", "--body", "paint it red")
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("(session %s will be told)", sess.ID); !strings.Contains(out, want) {
		t.Fatalf("bead update --body lacks %q:\n%s", want, out)
	}
	notes, _ := st.Notes(ctx, "yd-1")
	if len(notes) != 1 || notes[0].Notify != sess.ID {
		t.Fatalf("Notes = %+v, want one note for session %s", notes, sess.ID)
	}

	out, err = run(t, a.beadCmd(), "update", "yd-1", "--add-label", "paint")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "will be told") {
		t.Fatalf("a label patch says the session will be told:\n%s", out)
	}

	t.Setenv("YARDR_SESSION", sess.ID)
	out, err = run(t, a.beadCmd(), "update", "yd-1", "--body", "paint it green")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "will be told") {
		t.Fatalf("the session's own edit says it will be told:\n%s", out)
	}
	notes, _ = st.Notes(ctx, "yd-1")
	if len(notes) != 1 {
		t.Fatalf("Notes = %d, want 1: the own edit left a note", len(notes))
	}

	a.json = true
	t.Setenv("YARDR_SESSION", "")
	out, err = run(t, a.beadCmd(), "update", "yd-1", "--title", "the red shed")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("bead update --json = %q, %v", out, err)
	}
	if got["notify"] != sess.ID {
		t.Fatalf("bead update --json notify = %v, want %s", got["notify"], sess.ID)
	}
}
