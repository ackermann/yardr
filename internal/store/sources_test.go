package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

const sourceFile = `name: pushover
base: https://api.pushover.net/1
credential: { env: PUSHOVER_TOKEN }
calls:
  message:
    kind: update
    params: { user: { type: string }, text: { type: string, max: 1024 } }
    request: { method: POST, path: /messages.json, body: { token: "{credential}", user: "{user}", message: "{text}" } }
notify: { call: message, address: { param: user, env: PUSHOVER_USER }, text: text }
`

// A declared source is kept as its file, under the name the file gives it;
// replacing it keeps where its credential is; each change is an event.
func TestDeclaredSource(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	src, err := st.PutSource(ctx, "pushover.yaml", sourceFile, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if src.Name != "pushover" || src.Text != sourceFile || src.EnvFile != "" || src.AddedBy != "jo" {
		t.Fatalf("saved = %+v", src)
	}
	d, err := src.Declaration()
	if err != nil || len(d.Calls) != 1 || d.Notify == nil || d.Notify.Call != "message" {
		t.Fatalf("declaration = %+v, %v", d, err)
	}
	if _, err := st.SetSource(ctx, "pushover", store.SourceEnvFile, "/home/jo/.yardr/sources/pushover.env", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "pushover", "token", "abc", "jo"); err == nil || !strings.Contains(err.Error(), "token is no setting of a source") {
		t.Fatalf("a setting a source does not have: %v", err)
	}
	changed := strings.Replace(sourceFile, "max: 1024", "max: 512", 1)
	if src, err = st.PutSource(ctx, "pushover.yaml", changed, "jo"); err != nil || src.EnvFile != "/home/jo/.yardr/sources/pushover.env" || src.Text != changed {
		t.Fatalf("replaced = %+v, %v", src, err)
	}
	if all, err := st.Sources(ctx); err != nil || len(all) != 1 || all[0].Name != "pushover" {
		t.Fatalf("Sources = %+v, %v", all, err)
	}
	if got, err := st.GetSource(ctx, "pushover"); err != nil || got.Text != changed {
		t.Fatalf("GetSource = %+v, %v", got, err)
	}
	if _, err := st.GetSource(ctx, "crm"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a source that is not there: %v", err)
	}
	if _, err := st.SetSource(ctx, "crm", store.SourceEnvFile, "/x", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("setting a source that is not there: %v", err)
	}
	if err := st.DeleteSource(ctx, "pushover", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSource(ctx, "pushover", "jo"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removing it twice: %v", err)
	}
	if all, _ := st.Sources(ctx); len(all) != 0 {
		t.Fatalf("Sources after the removal = %+v", all)
	}

	evs, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		if strings.HasPrefix(e.Kind, "source_") {
			kinds = append(kinds, e.Kind)
			if e.Kind == "source_added" && (e.Data["source"] != "pushover" || e.Data["base"] != "https://api.pushover.net/1") {
				t.Fatalf("source_added = %+v", e.Data)
			}
		}
	}
	if got := strings.Join(kinds, " "); got != "source_added source_set source_added source_removed" {
		t.Fatalf("events = %s", got)
	}
	if raw, _ := json.Marshal(evs); strings.Contains(string(raw), "{credential}") || strings.Contains(string(raw), "messages.json") {
		t.Fatalf("an event has the request of a call in it: %s", raw)
	}
}

// What the parser refuses is not kept, and the error names the file as the
// caller named it.
func TestDeclaredSourceRefusedIsNotKept(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	bad := strings.Replace(sourceFile, "path: /messages.json", `path: "/messages/{credential}"`, 1)
	_, err := st.PutSource(ctx, "my/pushover.yaml", bad, "jo")
	if err == nil || !strings.Contains(err.Error(), "my/pushover.yaml:8: calls.message.request.path: {credential} in a path") {
		t.Fatalf("err = %v", err)
	}
	if all, _ := st.Sources(ctx); len(all) != 0 {
		t.Fatalf("a refused file was kept: %+v", all)
	}
}

// Adding, setting and removing a source is the operator's: whoever may
// replace a source's file can send its credential to an address of their
// own. A crew member is refused by its name, and by its pane whatever name
// the command gives.
func TestDeclaredSourceIsTheOperators(t *testing.T) {
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PutSource(ctx, "p.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	moved := strings.Replace(sourceFile, "https://api.pushover.net/1", "https://collector.example.com", 1)
	for actor, want := range map[string]string{
		"yardmaster":     "adding a source is the operator's (jo): a source is where a credential of the yard is sent, and its file may name a command the yard runs; yardmaster is a crew member",
		"yardr-26a0-new": "adding a source is the operator's (jo): a source is where a credential of the yard is sent, and its file may name a command the yard runs; this is yardr-26a0-new",
		"":               "this is nobody",
	} {
		if _, err := api.PutSource(ctx, "p.yaml", moved, actor); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("PutSource as %q = %v", actor, err)
		}
		if _, err := api.SetSource(ctx, "pushover", store.SourceEnvFile, "/tmp/mine.env", actor); err == nil || !strings.Contains(err.Error(), "setting a source's credential is the operator's") {
			t.Errorf("SetSource as %q = %v", actor, err)
		}
		if err := api.DeleteSource(ctx, "pushover", actor); err == nil || !strings.Contains(err.Error(), "removing a source is the operator's") {
			t.Errorf("DeleteSource as %q = %v", actor, err)
		}
	}
	if got, err := api.GetSource(ctx, "pushover"); err != nil || got.Text != sourceFile || got.EnvFile != "" {
		t.Fatalf("a refused change was made: %+v, %v", got, err)
	}

	// From the yardmaster's pane, naming the operator.
	me := os.Getpid()
	ps, err := peer.Describe(me)
	if err != nil {
		t.Skipf("this process cannot be described: %v", err)
	}
	if err := api.CrewStarted(ctx, "yardmaster", map[string]any{"shell_pid": me, "shell_start": ps.Start}); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	if _, err := own.PutSource(pane, "p.yaml", moved, "jo"); err == nil || !strings.Contains(err.Error(), "this is the crew member yardmaster") {
		t.Fatalf("PutSource from the yardmaster's pane as jo = %v", err)
	}
	if _, err := own.SetSource(pane, "pushover", store.SourceEnvFile, "/tmp/mine.env", "jo"); err == nil || !strings.Contains(err.Error(), "this is the crew member yardmaster") {
		t.Fatalf("SetSource from the yardmaster's pane as jo = %v", err)
	}
	if err := own.DeleteSource(pane, "pushover", "jo"); err == nil || !strings.Contains(err.Error(), "this is the crew member yardmaster") {
		t.Fatalf("DeleteSource from the yardmaster's pane as jo = %v", err)
	}
	if _, err := own.PutSource(ctx, "p.yaml", moved, "jo"); err != nil {
		t.Fatalf("the operator, from no agent's pane: %v", err)
	}
}

// A bead's agent is refused from its pane too, though it names the operator.
func TestDeclaredSourceRefusedFromASessionsPane(t *testing.T) {
	_, s := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutDepot(ctx, store.Depot{Name: "yard", Base: "origin/main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "builders", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "a", Depot: "yard", Stage: "new"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, b.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	handle := map[string]any{"pid": me}
	if ps, err := peer.Describe(me); err == nil {
		handle["start"] = ps.Start
	}
	if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, me)
	if _, err := s.PutSource(pane, "p.yaml", sourceFile, "jo"); err == nil || !strings.Contains(err.Error(), "this is the session "+sess.ID+" of a bead's agent") {
		t.Fatalf("PutSource from a session's pane as jo = %v", err)
	}
	// A file that is a command the yard would run, the same: what an agent
	// could add there, the daemon would run as the operator.
	if _, err := s.PutSource(pane, "q.yaml", limitsFile, "jo"); err == nil || !strings.Contains(err.Error(), "this is the session "+sess.ID+" of a bead's agent") ||
		!strings.Contains(err.Error(), "its file may name a command the yard runs") {
		t.Fatalf("PutSource of a limits command from a session's pane as jo = %v", err)
	}
	if all, _ := s.Sources(ctx); len(all) != 0 {
		t.Fatalf("a refused source was kept: %+v", all)
	}
}

const limitsFile = `name: aiquokka
limits:
  command: [/opt/aiquokka, "{provider}", --json]
  kinds: { claude: claude, kimi: kimi }
`

// A source that says limits is kept like any other, with no call and no
// base, and its event names the kinds it answers for. One source answers
// for a harness kind: a second that lists the kind is refused, and names
// the first.
func TestDeclaredSourceWithLimits(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	src, err := st.PutSource(ctx, "aiquokka.yaml", limitsFile, "jo")
	if err != nil {
		t.Fatal(err)
	}
	d, err := src.Declaration()
	if err != nil || d.Limits == nil || len(d.Calls) != 0 || d.Limits.Kinds["kimi"] != "kimi" {
		t.Fatalf("declaration = %+v, %v", d, err)
	}
	evs, err := st.EventsSince(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	last := evs[len(evs)-1]
	if kinds, _ := json.Marshal(last.Data["limits"]); last.Kind != "source_added" || string(kinds) != `["claude","kimi"]` {
		t.Fatalf("event = %s %v", last.Kind, last.Data)
	}
	// The same source again, changed, is itself and no second one.
	if _, err := st.PutSource(ctx, "aiquokka.yaml", strings.Replace(limitsFile, "claude: claude, ", "", 1), "jo"); err != nil {
		t.Fatalf("replacing it: %v", err)
	}
	second := strings.Replace(strings.Replace(limitsFile, "name: aiquokka", "name: other", 1), "claude: claude", "codex: codex", 1)
	_, err = st.PutSource(ctx, "other.yaml", second, "jo")
	if err == nil || !strings.Contains(err.Error(), "source other: the source aiquokka already says the limits of kimi: one source answers for a harness kind") {
		t.Fatalf("a second source for kimi = %v", err)
	}
	if all, _ := st.Sources(ctx); len(all) != 1 {
		t.Fatalf("a refused source was kept: %+v", all)
	}
	// Another kind is another matter.
	if _, err := st.PutSource(ctx, "other.yaml", strings.Replace(second, ", kimi: kimi", "", 1), "jo"); err != nil {
		t.Fatalf("a second source for codex alone: %v", err)
	}
}
