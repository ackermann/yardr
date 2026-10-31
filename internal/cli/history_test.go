package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// bead history prints the bead's events, the oldest first, a line each
// (when, who, what), and under an update of the body its diff; --json has
// the same as the bead's page of the web view has it (yardr-pekm).
func TestBeadHistory(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	// The old values a diff is said from are the operator's to read, and
	// the history is read as the command's actor.
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
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
	// As a member: the update stands, but its old values and its diff
	// are the operator's.
	if _, err := st.AddUser(ctx, "sam", "jo"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, (&app{st: st, actor: "sam"}).beadCmd(), "history", b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "updated: title \"the door\" · priority 1 · body") ||
		strings.Contains(out, "a door") || strings.Contains(out, "--- was") {
		t.Errorf("a member's history:\n%s", out)
	}
}

// A line about an update names what the event kept of the fields' old
// values and does not write them out: yardr events stays a line an event.
func TestEventLineNamesWhatWas(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	bead := "yard-1"
	e := store.Event{Seq: 7, Bead: &bead, Kind: "updated", Actor: "jo",
		Data: map[string]any{"body": true, "was": map[string]any{"body": "Hang it.\nOn two hinges.\n"}}}
	var buf bytes.Buffer
	printBeadDelivery(&buf, e, "yardmaster", "")
	out := buf.String()
	if !strings.Contains(out, "by jo body=true was=[body]\n") || strings.Contains(out, "hinges") {
		t.Errorf("inbox line: %q", out)
	}
}

// A red quiet run's inbox delivery names its failing output under its
// line, as mail names the files it carries.
func TestInboxQuietFailedShowsItsFilesLine(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	ref := store.BlobRef{Hash: strings.Repeat("a", 64), Type: "text/plain; charset=utf-8", Bytes: 42, Name: "yardr.dev_yardr_internal_store.log"}
	e := store.Event{Seq: 9, Kind: store.QuietFailed, Actor: "yard",
		At:   time.Date(2026, 10, 21, 9, 14, 0, 0, time.Local),
		Data: map[string]any{"depot": "yard", "commit": "abc123", "files": []store.BlobRef{ref}}}
	var buf bytes.Buffer
	printInboxEvent(&buf, e, "yardmaster", home, nil)
	out := buf.String()
	if !strings.Contains(out, "quiet_failed") {
		t.Fatalf("inbox delivery: %q", out)
	}
	marked := "    " + store.NoteFilesMark + " 1 file: " + store.BlobPath(home, ref.Hash) + " (yardr.dev_yardr_internal_store.log)\n"
	if !strings.Contains(out, marked) {
		t.Errorf("inbox delivery names no files line:\n%s", out)
	}
}

// yardr events takes --since and --kind: --kind keeps only those kinds
// (repeatable), --since those at or after a duration back or an RFC3339
// time, combinable with --bead and -n. --since without -n returns
// everything in the window; otherwise the newest 50.
func TestEventsSinceAndKind(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4}, "me"))
	b, err := st.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard"}, "me")
	must(err)
	_, err = st.Claim(ctx, b.ID, "builders", "new", "yard")
	must(err)
	for i := 0; i < 60; i++ {
		must(st.AddNote(ctx, b.ID, fmt.Sprintf("note %d", i), "me"))
	}
	a := &app{st: st, actor: "me"}
	events := func(args ...string) []store.Event {
		t.Helper()
		aj := &app{st: st, actor: "me", json: true}
		out, err := run(t, aj.eventsCmd(), args...)
		if err != nil {
			t.Fatal(err)
		}
		var evs []store.Event
		if err := json.Unmarshal([]byte(out), &evs); err != nil {
			t.Fatalf("events %v --json = %q: %v", args, out, err)
		}
		return evs
	}

	// --kind claimed lists only claims.
	claimed := events("--kind", "claimed")
	if len(claimed) != 1 || claimed[0].Kind != "claimed" {
		t.Fatalf("--kind claimed = %d events, want the one claim", len(claimed))
	}
	if claimed[0].Seq == 0 || claimed[0].At.IsZero() || claimed[0].Bead == nil {
		t.Errorf("--kind claimed JSON shape = %+v, want seq, at and bead", claimed[0])
	}
	// Repeatable: claims and notes, and nothing else.
	both := events("--since", "2h", "--kind", "claimed", "--kind", "noted")
	if len(both) != 61 {
		t.Fatalf("--kind claimed --kind noted = %d events, want 61", len(both))
	}
	for _, e := range both {
		if e.Kind != "claimed" && e.Kind != "noted" {
			t.Fatalf("--kind claimed --kind noted has %q", e.Kind)
		}
	}
	// Combinable with --bead: the bead's notes alone.
	beadNotes := events("--since", "2h", "--bead", b.ID, "--kind", "noted")
	if len(beadNotes) != 60 {
		t.Fatalf("--bead --kind noted = %d events, want 60", len(beadNotes))
	}
	// --since without -n returns everything in the window: all are
	// recent, so more than the default 50.
	all := events("--since", "2h")
	if len(all) <= 50 {
		t.Fatalf("--since 2h = %d events, want everything (>50)", len(all))
	}
	old := events("--since", "2020-01-01T00:00:00Z")
	if len(old) != len(all) {
		t.Errorf("--since 2020 = %d events, want all %d", len(old), len(all))
	}
	// A future time keeps none.
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	if evs := events("--since", future); len(evs) != 0 {
		t.Errorf("--since future = %d events, want none", len(evs))
	}
	// With -n, the newest of those in the window.
	if evs := events("--since", "2h", "-n", "5"); len(evs) != 5 {
		t.Errorf("--since 2h -n 5 = %d events, want 5", len(evs))
	}
	// Without --since the default is still 50, of the kinds asked for.
	if evs := events("--kind", "noted"); len(evs) != 50 {
		t.Errorf("--kind noted = %d events, want the newest 50", len(evs))
	}
	if evs := events(); len(evs) != 50 {
		t.Errorf("events = %d events, want the newest 50", len(evs))
	}
	// Text lists only claims of the last two hours, oldest first.
	out, err := run(t, a.eventsCmd(), "--since", "2h", "--kind", "claimed")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "\n"); n != 1 || !strings.Contains(out, "claimed") {
		t.Errorf("events --since 2h --kind claimed = %q, want the one claim", out)
	}
	if _, err := run(t, a.eventsCmd(), "--since", "soon"); err == nil || !strings.Contains(err.Error(), "--since") {
		t.Errorf("--since soon err = %v, want a --since error", err)
	}
	if _, err := run(t, a.eventsCmd(), "--bead", "none-zzzz", "--kind", "noted"); err == nil {
		t.Error("an unknown bead with --kind has events")
	}
}
