package store_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A hook's table is read in order and the first line an event matches
// counts. A line matches as a subscription's filter does: every key must
// hold, a key with several values holds for any, and stage is the stage of
// the bead the event is about.
func TestHookSaidIsTheFirstLineThatMatches(t *testing.T) {
	var says []store.Say
	for _, l := range []string{
		"event=pull_request action=closed merged=true -> merged: PR merged",
		"event=pull_request action=closed -> question",
		"event=workflow_run event=check_suite conclusion=failure conclusion=timed_out stage=review -> changes",
		"event=ci stage=in-pr stage=land -> done",
	} {
		s, err := store.ParseSay(l)
		if err != nil {
			t.Fatal(err)
		}
		says = append(says, s)
	}
	h := store.Hook{Name: "gh", Source: "github", Says: says}
	env := func(typ, action string, fields map[string]any) map[string]any {
		return map[string]any{"source": "github", "type": typ, "action": action, "fields": fields}
	}
	for _, c := range []struct {
		name  string
		data  map[string]any
		stage string
		want  string // the outcome, "" for no line
	}{
		{"the first of two that match", env("pull_request", "closed", map[string]any{"event": []any{"pull_request"}, "action": []any{"closed"}, "merged": []any{"true"}}), "open", "merged"},
		{"the second, where the first does not", env("pull_request", "closed", map[string]any{"event": []any{"pull_request"}, "action": []any{"closed"}, "merged": []any{"false"}}), "open", "question"},
		{"any value of a key", env("check_suite", "completed", map[string]any{"event": []any{"check_suite"}, "conclusion": []any{"timed_out"}}), "review", "changes"},
		{"another stage", env("check_suite", "completed", map[string]any{"event": []any{"check_suite"}, "conclusion": []any{"timed_out"}}), "in-pr", ""},
		{"another value", env("workflow_run", "completed", map[string]any{"event": []any{"workflow_run"}, "conclusion": []any{"success"}}), "review", ""},
		{"the envelope's type, and fields set in process", map[string]any{"type": "ci", "fields": map[string][]string{"event": {"ci"}}}, "land", "done"},
		{"no fields", map[string]any{"type": "push"}, "review", ""},
	} {
		got := ""
		if line := h.Said(c.data, c.stage, "task"); line != nil {
			got = line.Outcome
		}
		if got != c.want {
			t.Errorf("%s: outcome %q, want %q", c.name, got, c.want)
		}
	}
	if (store.Hook{}).Said(env("pull_request", "closed", nil), "open", "task") != nil {
		t.Error("a hook without a table said something")
	}
}

// A line with type matches only beads of that type: one table serves a
// train and a pr bead with different outcomes for the same event. The
// event's own type is named with event.
func TestHookSaidFiltersOnBeadType(t *testing.T) {
	var says []store.Say
	for _, l := range []string{
		"event=pull_request action=closed merged=false type=train -> question",
		"event=pull_request action=closed merged=false type=pr -> dropped",
		"event=pull_request action=closed merged=false -> ignored",
	} {
		s, err := store.ParseSay(l)
		if err != nil {
			t.Fatal(err)
		}
		says = append(says, s)
	}
	h := store.Hook{Name: "gh", Source: "github", Says: says}
	data := map[string]any{"source": "github", "type": "pull_request", "action": "closed",
		"fields": map[string]any{"event": []any{"pull_request"}, "action": []any{"closed"}, "merged": []any{"false"}}}
	for _, c := range []struct {
		beadType, want string
	}{
		{"train", "question"},
		{"pr", "dropped"},
		{"task", "ignored"},
	} {
		got := ""
		if line := h.Said(data, "open", c.beadType); line != nil {
			got = line.Outcome
		}
		if got != c.want {
			t.Errorf("bead type %s: outcome %q, want %q", c.beadType, got, c.want)
		}
	}
	// A line with several types holds for any of them.
	s, err := store.ParseSay("event=pull_request type=train type=pr -> moved")
	if err != nil {
		t.Fatal(err)
	}
	h = store.Hook{Name: "gh", Source: "github", Says: []store.Say{s}}
	for _, c := range []struct {
		beadType, want string
	}{
		{"train", "moved"},
		{"pr", "moved"},
		{"task", ""},
	} {
		got := ""
		if line := h.Said(data, "open", c.beadType); line != nil {
			got = line.Outcome
		}
		if got != c.want {
			t.Errorf("bead type %s: outcome %q, want %q", c.beadType, got, c.want)
		}
	}
}

// A line is written as it is typed and read back the same, note and all.
func TestParseSay(t *testing.T) {
	s, err := store.ParseSay("event=pull_request  merged=false action=closed ->  question : PR closed: reopen, or drop it? ")
	if err != nil {
		t.Fatal(err)
	}
	const want = "action=closed event=pull_request merged=false -> question: PR closed: reopen, or drop it?"
	if s.String() != want || s.Outcome != "question" || s.Note != "PR closed: reopen, or drop it?" {
		t.Fatalf("line = %q (%+v), want %q", s, s, want)
	}
	again, err := store.ParseSay(s.String())
	if err != nil || !store.SameSays([]store.Say{s}, []store.Say{again}) {
		t.Fatalf("read back %+v, %v", again, err)
	}
	for line, want := range map[string]string{
		"event=pull_request merged": "want <key>=<value> ... -> <outcome>",
		"event -> merged":           `"event": want key=value`,
		"=x -> merged":              `"=x": want key=value`,
	} {
		if _, err := store.ParseSay(line); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want %q", line, err, want)
		}
	}
}

// The store refuses a table that could not be acted on, whoever sets it:
// a line with no filter, a key the source's envelope has not, and an
// outcome that is no outcome's name. It keeps the lines in order and logs
// them with the hook.
func TestPutHookChecksItsTable(t *testing.T) {
	st := storetest.New(t)
	declareGitHubKeys(t, st, "me")
	ctx := context.Background()
	put := func(source string, says ...store.Say) error {
		_, err := st.PutHook(ctx, store.Hook{Name: "h", Source: source, SecretFile: "/nonexistent/s", Says: says}, "me")
		return err
	}
	for name, c := range map[string]struct {
		source string
		say    store.Say
		want   string
	}{
		"no filter":                   {"github", store.Say{Outcome: "merged"}, "no filter"},
		"a key of another source":     {"github", store.Say{Where: map[string]string{"kind": "held"}, Outcome: "merged"}, `cannot filter github on "kind"`},
		"a github key on linear":      {"linear", store.Say{Where: map[string]string{"merged": "true"}, Outcome: "merged"}, `cannot filter linear on "merged"`},
		"an empty value":              {"github", store.Say{Where: map[string]string{"event": ""}, Outcome: "merged"}, "empty value for event"},
		"a bead type that is no name": {"github", store.Say{Where: map[string]string{"event": "push", "type": "My Type"}, Outcome: "merged"}, `bead type "My Type"`},
		"no outcome":                  {"github", store.Say{Where: map[string]string{"event": "push"}}, `outcome ""`},
		"an outcome that is no name":  {"github", store.Say{Where: map[string]string{"event": "push"}, Outcome: "all done"}, `outcome "all done"`},
	} {
		if err := put(c.source, c.say); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	if hs, _ := st.Hooks(ctx); len(hs) != 0 {
		t.Fatalf("refused hooks were saved: %+v", hs)
	}
	lines := []store.Say{
		{Where: map[string]string{"event": "pull_request", "stage": "approved"}, Outcome: "done", Note: "PR merged"},
		{Where: map[string]string{"event": "deploy"}, Outcome: "changes"},
	}
	if err := put("github", lines...); err != nil {
		t.Fatal(err)
	}
	h, err := st.GetHook(ctx, "h")
	if err != nil || !store.SameSays(h.Says, lines) {
		t.Fatalf("stored %+v, %v", h, err)
	}
	evs, err := st.Events(ctx, "", 5)
	if err != nil {
		t.Fatal(err)
	}
	logged := false
	for _, e := range evs {
		if e.Kind == "hook_added" {
			got, _ := e.Data["says"].([]any)
			logged = len(got) == 2 && got[0] == "event=pull_request stage=approved -> done: PR merged"
		}
	}
	if !logged {
		t.Fatalf("hook_added without the table: %+v", evs)
	}
	// The order is part of the table: the same lines turned round are another.
	turned := []store.Say{lines[1], lines[0]}
	if store.SameSays(lines, turned) || !slices.Equal(store.SayLines(turned), []string{"event=deploy -> changes", "event=pull_request stage=approved -> done: PR merged"}) {
		t.Fatalf("turned = %v", store.SayLines(turned))
	}
}

// A hook whose table reads CI as state (an event=ci line) of a source
// is refused without what the read runs with: the source's ci command,
// and the crew member it acts as.
func TestPutHookChecksItsCIReads(t *testing.T) {
	const hook = `
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Forge-Signature, encoding: hex }
    delivery: { header: Forge-Delivery }
  bodies: repeat
  normalize: [normalize]
  keys: [event, conclusion]
`
	decl := func(events string) string {
		return "name: forge\nbase: https://forge.test\ncredential: { env: FORGE_TOKEN }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n" + hook + events
	}
	line, err := store.ParseSay("event=ci conclusion=success stage=in-pr -> done")
	if err != nil {
		t.Fatal(err)
	}
	setup := func(t *testing.T, events string) store.API {
		t.Helper()
		st := storetest.New(t)
		if _, err := st.PutSource(context.Background(), "forge.yaml", decl(events), "me"); err != nil {
			t.Fatal(err)
		}
		return st
	}
	put := func(st store.API, as string) error {
		_, err := st.PutHook(context.Background(), store.Hook{Name: "fg", Source: "forge",
			SecretFile: "/nonexistent/s", As: as, Says: []store.Say{line}}, "me")
		return err
	}
	// Without the command, whatever the hook acts as.
	st := setup(t, "events:\n  runs: [workflow_run]\n  head_from: [pull_request]\n")
	if err := put(st, ""); err == nil || !strings.Contains(err.Error(), "source forge's ci command, and it declares none (events.ci in the declaration)") {
		t.Errorf("no ci command: %v", err)
	}
	// With the command but no crew member to read as.
	st = setup(t, "events:\n  runs: [workflow_run]\n  head_from: [pull_request]\n  ci: [forge/ci]\n")
	if err := put(st, ""); err == nil || !strings.Contains(err.Error(), "acts as none (--as)") {
		t.Errorf("no crew member: %v", err)
	}
	// With both, the hook is saved.
	moveYardmaster(t, st, "forge")
	if err := st.SetIdentity(context.Background(), "yardmaster", "forge", store.Identity{ID: "u-yardmaster"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutHook(context.Background(), store.Hook{Name: "fg", Source: "forge",
		SecretFile: "/nonexistent/s", As: "yardmaster", Says: []store.Say{line}}, "me"); err != nil {
		t.Errorf("with both: %v", err)
	}
	// A hook of a source the yard does not declare takes no line that
	// filters on a declared key: its events carry the store's fields
	// alone. The refusal names the first unknown key in sorted order,
	// every time: the line filters on conclusion and event, and the
	// check walks the filter's keys sorted.
	st = storetest.New(t)
	for range 32 {
		if _, err := st.PutHook(context.Background(), store.Hook{Name: "gh", Source: "github",
			SecretFile: "/nonexistent/s", Says: []store.Say{line}}, "me"); err == nil ||
			!strings.Contains(err.Error(), `cannot filter github on "conclusion"`) {
			t.Fatalf("undeclared: %v", err)
		}
	}
}

// Unrouted asks the flow before an event does: the stage a line names must
// have its outcome, and a line that names none needs one stage that has.
func TestSayUnrouted(t *testing.T) {
	stages := map[string][]string{"review": {"done", "changes"}, "in-pr": {"done", "merged"}, "decide": nil}
	for line, want := range map[string]string{
		"event=x -> merged":                          "",
		"event=x stage=in-pr -> merged":              "",
		"event=x stage=in-pr stage=review -> done":   "",
		"event=x -> landed":                          "no stage has an outcome landed",
		"event=x stage=review -> merged":             "stage review has no outcome merged (it has: done, changes)",
		"event=x stage=in-pr stage=review -> merged": "stage review has no outcome merged",
		"event=x stage=decide -> done":               "stage decide has no outcome done (it has: none)",
		"event=x stage=land -> done":                 "no stage land (stages: decide, in-pr, review)",
	} {
		s, err := store.ParseSay(line)
		if err != nil {
			t.Fatal(err)
		}
		err = s.Unrouted(stages)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%q: %v, want %q", line, err, want)
		}
	}
}
