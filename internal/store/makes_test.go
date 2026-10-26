package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A line of what a hook's events make is written as it is typed and read
// back the same, with its depot when it names one.
func TestParseMake(t *testing.T) {
	t.Parallel()
	m, err := store.ParseMake("event=issues  action=reopened action=opened ->  issue : triage @ web ")
	if err != nil {
		t.Fatal(err)
	}
	const want = "action=opened action=reopened event=issues -> issue:triage@web"
	if m.String() != want || m.Type != "issue" || m.Stage != "triage" || m.Depot != "web" {
		t.Fatalf("line = %q (%+v), want %q", m, m, want)
	}
	again, err := store.ParseMake(m.String())
	if err != nil || !store.SameMakes([]store.Make{m}, []store.Make{again}) {
		t.Fatalf("read back %+v, %v", again, err)
	}
	if m, err := store.ParseMake("event=issues -> issue:triage"); err != nil || m.Depot != "" || m.String() != "event=issues -> issue:triage" {
		t.Fatalf("without a depot: %+v, %v", m, err)
	}
	for line, want := range map[string]string{
		"event=issues issue:triage":     "want <key>=<value> ... -> <type>:<stage>[@<depot>]",
		"event=issues -> issue":         "want <key>=<value> ... -> <type>:<stage>[@<depot>]",
		"event=issues -> :triage":       "want <key>=<value> ... -> <type>:<stage>[@<depot>]",
		"event=issues -> issue:triage@": "want <key>=<value> ... -> <type>:<stage>[@<depot>]",
		"event -> issue:triage":         `"event": want key=value`,
	} {
		if _, err := store.ParseMake(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", line, err, want)
		}
	}
	if err := m.Unstaged([]string{"triage", "new"}); err != nil {
		t.Errorf("a stage the flow has: %v", err)
	}
	if err := m.Unstaged([]string{"backlog", "new"}); err == nil || !strings.Contains(err.Error(), "no stage triage a bead can be made in (stages: backlog, new)") {
		t.Errorf("a stage the flow has not: %v", err)
	}
}

// The store refuses a table of makes that could not be acted on, whoever
// sets it, keeps the lines in order and logs them with the hook. The first
// line an event matches is the one that counts.
func TestPutHookChecksWhatItMakes(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	declareGitHubKeys(t, st, "me")
	ctx := context.Background()
	put := func(source string, makes ...store.Make) error {
		_, err := st.PutHook(ctx, store.Hook{Name: "h", Source: source, SecretFile: "/nonexistent/s", Makes: makes}, "me")
		return err
	}
	opened := map[string]string{"event": "issues"}
	for name, c := range map[string]struct {
		source string
		make   store.Make
		want   string
	}{
		"no filter":                 {"github", store.Make{Type: "issue", Stage: "triage"}, "no filter"},
		"a key of another source":   {"linear", store.Make{Where: map[string]string{"merged": "true"}, Type: "issue", Stage: "triage"}, `cannot filter linear on "merged"`},
		"the bead's stage":          {"github", store.Make{Where: map[string]string{"stage": "new"}, Type: "issue", Stage: "triage"}, `cannot filter github on "stage"`},
		"the bead's stage on token": {"token", store.Make{Where: map[string]string{"event": "box", "stage": "new"}, Type: "box", Stage: "requested"}, "stage=new names the bead's stage"},
		"a routing name on token":   {"token", store.Make{Where: map[string]string{"event": "box", "train": "yard-1"}, Type: "box", Stage: "requested"}, `cannot filter token on "train"`},
		"the bead's type":           {"github", store.Make{Where: map[string]string{"event": "issues", "type": "issue"}, Type: "issue", Stage: "triage"}, "redundant with the bead the line makes"},
		"an empty value":            {"github", store.Make{Where: map[string]string{"event": ""}, Type: "issue", Stage: "triage"}, "empty value for event"},
		"no type":                   {"github", store.Make{Where: opened, Stage: "triage"}, `bead type ""`},
		"a type with no flow":       {"github", store.Make{Where: opened, Type: store.TypeMemory, Stage: "memory"}, "follows no flow"},
		"no stage":                  {"github", store.Make{Where: opened, Type: "issue"}, `stage ""`},
		"a depot that is no name":   {"github", store.Make{Where: opened, Type: "issue", Stage: "triage", Depot: "My Depot"}, `depot "My Depot"`},
	} {
		if err := put(c.source, c.make); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if hs, _ := st.Hooks(ctx); len(hs) != 0 {
		t.Fatalf("refused hooks were saved: %+v", hs)
	}
	lines := []store.Make{
		{Where: map[string]string{"event": "issues", "action": "opened"}, Type: "issue", Stage: "triage"},
		{Where: map[string]string{"event": "issues"}, Type: "task", Stage: "backlog", Depot: "web"},
	}
	if err := put("github", lines...); err != nil {
		t.Fatal(err)
	}
	h, err := st.GetHook(ctx, "h")
	if err != nil || !store.SameMakes(h.Makes, lines) || store.SameMakes(lines, []store.Make{lines[1], lines[0]}) {
		t.Fatalf("stored %+v, %v", h, err)
	}
	evs, err := st.Events(ctx, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(evs, func(e store.Event) bool {
		got, _ := e.Data["makes"].([]any)
		return e.Kind == "hook_added" && len(got) == 2 && got[0] == "action=opened event=issues -> issue:triage"
	}) {
		t.Fatalf("hook_added without the table: %+v", evs)
	}
	env := func(action string) map[string]any {
		return map[string]any{"type": "issues", "action": action, "fields": map[string]any{"event": []any{"issues"}}}
	}
	if m := h.Made(env("opened")); m == nil || m.Type != "issue" {
		t.Fatalf("an opened issue makes %+v, want the first line's issue", m)
	}
	if m := h.Made(env("edited")); m == nil || m.Type != "task" {
		t.Fatalf("an edited issue makes %+v, want the second line's task", m)
	}
	if m := h.Made(map[string]any{"type": "push", "fields": map[string]any{"event": []any{"push"}}}); m != nil {
		t.Fatalf("a push makes %+v", m)
	}
	// A token hook's makes lines filter on the delivery's sender-chosen
	// fields, so any key but the routing names holds there.
	tok, err := store.ParseMake("event=box verb=create -> box:requested@yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := put("token", tok); err != nil {
		t.Fatalf("a token makes line on a sender-chosen key: %v", err)
	}
}

// A bead made for a thread (Untracked) is made only when no open bead
// tracks it, in the one write: an open tracker refuses it as stale and
// nothing is created, and a closed one hands the link on and keeps a
// relates link.
func TestUntrackedBeadIsMadeOncePerThread(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	const issue = "https://github.com/acme/web/issues/5"
	nb := store.NewBead{Title: "from outside", Untracked: true,
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: issue}}}
	first, err := st.CreateBead(ctx, nb, "hook:gh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, nb, "hook:gh"); !errors.Is(err, store.ErrConflict) || !strings.Contains(err.Error(), "is tracked by "+first.ID) {
		t.Fatalf("a second bead for the thread: %v, want it refused as stale", err)
	}
	if all, _ := st.ListBeads(ctx, store.BeadFilter{}); len(all) != 1 {
		t.Fatalf("%d beads after a refused make, want one", len(all))
	}
	if _, err := st.CloseBead(ctx, first.ID, "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	second, err := st.CreateBead(ctx, nb, "hook:gh")
	if err != nil {
		t.Fatal(err)
	}
	links, err := st.IssueLinks(ctx, issue)
	if err != nil || len(links) != 2 || links[0].Bead != second.ID || links[0].Kind != store.LinkTracks ||
		links[1].Bead != first.ID || links[1].Kind != store.LinkRelates {
		t.Fatalf("links to the thread: %+v, %v; want %s tracking and %s relating", links, err, second.ID, first.ID)
	}
	// Without it a closed tracker stands in the way, as it always did.
	nb.Untracked = false
	if _, err := st.CloseBead(ctx, second.ID, "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, nb, "me"); err == nil || !strings.Contains(err.Error(), "is tracked by "+second.ID) {
		t.Fatalf("a bead made by hand for a thread a closed bead tracks: %v", err)
	}
}
