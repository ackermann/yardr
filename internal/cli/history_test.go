package cli

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// bead history prints the bead's events, the oldest first, a line each
// (when, who, what), and under an update of the body its diff; --json has
// the same as the bead's page of the web view has it (yardr-pekm).
func TestBeadHistory(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "a door", Body: "Hang it.\n"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "jo"}
	if _, err := run(t, a.beadCmd(), "update", b.ID, "--title", "the door", "--body", "Hang it.\n- on two hinges\n", "-p", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, a.beadCmd(), "note", b.ID, "hung\ntoday"); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.beadCmd(), "history", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The time is this machine's: every line begins with one.
	when := regexp.MustCompile(`(?m)^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d  `)
	want := `me             created
jo             updated: title "a door" → "the door" · priority 0 → 1 · body
    --- was
    +++ is
    @@ -1,1 +1,2 @@
     Hang it.
    +- on two hinges
jo             noted: hung today
`
	if got := when.ReplaceAllString(out, ""); got != want {
		t.Errorf("history:\n%s\nwant\n%s", got, want)
	}
	if n := len(when.FindAllString(out, -1)); n != 3 {
		t.Errorf("%d lines begin with a time, want 3:\n%s", n, out)
	}

	a.json = true
	out, err = run(t, a.beadCmd(), "history", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []web.Change
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(got) != 3 || got[1].Kind != "updated" || got[1].Actor != "jo" || got[1].Bead != b.ID ||
		got[1].Diff != "--- was\n+++ is\n@@ -1,1 +1,2 @@\n Hang it.\n+- on two hinges\n" || got[2].Note != "hung\ntoday" {
		t.Errorf("history --json: %+v", got)
	}
	a.json = false
	if _, err := run(t, a.beadCmd(), "history", "none-zzzz"); err == nil {
		t.Error("an unknown bead has a history")
	}
}

// A line about an update names what the event kept of the fields' old
// values and does not write them out: yardr events stays a line an event.
func TestEventLineNamesWhatWas(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "a door", Body: "Hang it.\nOn two hinges.\n"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	body := "Hung."
	if _, err := st.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &body}, store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "jo"}
	out, err := run(t, a.eventsCmd(), "--bead", b.ID, "-n", "1")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "body=true was=[body]") || strings.Contains(out, "hinges") || strings.Count(out, "\n") != 1 {
		t.Errorf("events: %q", out)
	}
	// The event itself has the old body: --json is the log as it is.
	a.json = true
	if out, err = run(t, a.eventsCmd(), "--bead", b.ID, "-n", "1"); err != nil || !strings.Contains(out, `"body": "Hang it.\nOn two hinges.\n"`) {
		t.Errorf("events --json: %v: %s", err, out)
	}
}

// A crew member's inbox line about an update is one line too.
func TestInboxLineNamesWhatWas(t *testing.T) {
	bead := "yard-1"
	e := store.Event{Seq: 7, Bead: &bead, Kind: "updated", Actor: "jo",
		Data: map[string]any{"body": true, "was": map[string]any{"body": "Hang it.\nOn two hinges.\n"}}}
	out := capture(t, func() { printBeadDelivery(e, "yardmaster") })
	if !strings.Contains(out, "by jo body=true was=[body]\n") || strings.Contains(out, "hinges") {
		t.Errorf("inbox line: %q", out)
	}
}
