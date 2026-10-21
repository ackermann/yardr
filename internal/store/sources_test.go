package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
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
// replacing it keeps where its credential is; each change but the removal
// is an event.
func TestDeclaredSource(t *testing.T) {
	t.Parallel()
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
	if _, err := st.SetSource(ctx, "pushover", store.SourceMintCommand, "/home/jo/bin/pushover-mint --as jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "pushover", "token", "abc", "jo"); err == nil || !strings.Contains(err.Error(), "token is no setting of a source") {
		t.Fatalf("a setting a source does not have: %v", err)
	}
	changed := strings.Replace(sourceFile, "max: 1024", "max: 512", 1)
	if src, err = st.PutSource(ctx, "pushover.yaml", changed, "jo"); err != nil || src.EnvFile != "/home/jo/.yardr/sources/pushover.env" ||
		src.MintCommand != "/home/jo/bin/pushover-mint --as jo" || src.Text != changed {
		t.Fatalf("replaced = %+v, %v", src, err)
	}
	// Taken away again, as the file is.
	if src, err = st.SetSource(ctx, "pushover", store.SourceMintCommand, "", "jo"); err != nil || src.MintCommand != "" {
		t.Fatalf("unsetting the mint command: %+v, %v", src, err)
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
	if got := strings.Join(kinds, " "); got != "source_added source_set source_set source_added source_set" {
		t.Fatalf("events = %s", got)
	}
	if raw, _ := json.Marshal(evs); strings.Contains(string(raw), "{credential}") || strings.Contains(string(raw), "messages.json") {
		t.Fatalf("an event has the request of a call in it: %s", raw)
	}
}

// What the parser refuses is not kept, and the error names the file as the
// caller named it.
func TestDeclaredSourceRefusedIsNotKept(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
		if _, err := api.KeySource(ctx, "pushover", map[string]string{"PUSHOVER_TOKEN": "s3cret"}, actor); err == nil || !strings.Contains(err.Error(), "setting a source's credential is the operator's (jo)") || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("KeySource as %q = %v", actor, err)
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
	if _, err := own.KeySource(pane, "pushover", map[string]string{"PUSHOVER_TOKEN": "s3cret"}, "jo"); err == nil || !strings.Contains(err.Error(), "setting a source's credential is the operator's (jo)") || !strings.Contains(err.Error(), "this is the crew member yardmaster") {
		t.Fatalf("KeySource from the yardmaster's pane as jo = %v", err)
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
	t.Parallel()
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
	if _, err := s.KeySource(pane, "pushover", map[string]string{"PUSHOVER_TOKEN": "s3cret"}, "jo"); err == nil || !strings.Contains(err.Error(), "setting a source's credential is the operator's (jo)") ||
		!strings.Contains(err.Error(), "this is the session "+sess.ID+" of a bead's agent") {
		t.Fatalf("KeySource from a session's pane as jo = %v", err)
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
	t.Parallel()
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

// A declaration whose hear.subscribe names a key no subscription to the
// source takes is not kept: a claim approval would fail every claim
// subscribing the hearer with it.
func TestDeclaredSourceWithBadClaimFilterIsNotKept(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	file := `name: chime
base: https://chime.example.com
credential: { env: CHIME_TOKEN }
calls:
  message:
    kind: update
    params: { chat: { type: string }, text: { type: string } }
    request: { method: POST, path: /send, body: { chat: "{chat}", text: "{text}" } }
notify:
  call: message
  address: { param: chat, env: CHIME_CHAT }
  text: text
hook:
  verify: { scheme: secret, signature: { header: Chime-Secret } }
  bodies: unique
  normalize: [normalize]
  keys: [chat]
hear:
  field: chat
  address: { env: CHIME_CHAT }
  from: operator
  subscribe: { colour: red }
`
	_, err := st.PutSource(ctx, "chime.yaml", file, "jo")
	if err == nil || !strings.Contains(err.Error(), `hear.subscribe names "colour", and a subscription to chime filters on`) {
		t.Fatalf("err = %v", err)
	}
	if all, _ := st.Sources(ctx); len(all) != 0 {
		t.Fatalf("a refused file was kept: %+v", all)
	}
	// The filter beside a valid key is kept.
	if _, err := st.PutSource(ctx, "chime.yaml", strings.Replace(file, "colour: red", "type: message", 1), "jo"); err != nil {
		t.Fatalf("a valid filter: %v", err)
	}
}

// crmKeyFile names a credential of two variables, one for the updates.
const crmKeyFile = `name: crm
base: https://crm.example.com/api
credential: { env: CRM_TOKEN, update_env: CRM_UPDATE_TOKEN, header: "Authorization: Bearer {credential}" }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`

// sourceSets are the source_set events of the store, as JSON.
func sourceSets(t *testing.T, st store.API) []string {
	t.Helper()
	evs, err := st.EventsSince(context.Background(), 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.Kind == "source_set" {
			raw, _ := json.Marshal(e.Data)
			out = append(out, string(raw))
		}
	}
	return out
}

// A credential given to a source with no env_file is written to
// <home>/sources/<name>.env, mode 0600, and that file is set as SetSource
// sets one, with its one event. Given again it is written to the file that
// is set, with the other lines kept and nothing more in the store. The
// value is in the file and in nothing the store keeps or says.
func TestKeySource(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "yard.db")
	// Closed by the test itself, which reads the store's file at its end.
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	t.Cleanup(func() {
		if !closed {
			st.Close(context.Background())
		}
	})
	ctx := context.Background()
	if _, err := st.PutSource(ctx, "crm.yaml", crmKeyFile, "jo"); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(filepath.Dir(path), "sources", "crm.env")
	keyed, err := st.KeySource(ctx, "crm", map[string]string{"CRM_TOKEN": "s3cret-read"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if keyed.File != file || keyed.Source.EnvFile != file || !slices.Equal(keyed.Given, []string{"CRM_TOKEN"}) {
		t.Fatalf("keyed = %+v (source %+v), want the file %s", keyed, keyed.Source, file)
	}
	// One of the two is not the whole credential, and that is said without
	// failing the giving.
	if keyed.State != "" || keyed.Missing != "env_file "+file+" does not set CRM_UPDATE_TOKEN" {
		t.Fatalf("state = %q, missing = %q", keyed.State, keyed.Missing)
	}
	if st, err := os.Stat(file); err != nil || st.Mode() != 0o600 {
		t.Fatalf("the file: %v, %v", st, err)
	}
	if b, _ := os.ReadFile(file); string(b) != "CRM_TOKEN=s3cret-read\n" {
		t.Fatalf("the file holds %q", b)
	}
	if got, err := st.GetSource(ctx, "crm"); err != nil || got.EnvFile != file {
		t.Fatalf("the source = %+v, %v", got, err)
	}
	want := []string{`{"env_file":"` + file + `","source":"crm"}`}
	if got := sourceSets(t, st); !slices.Equal(got, want) {
		t.Fatalf("source_set events = %v, want %v", got, want)
	}

	// A person's own lines in the file are kept, and the file that is set
	// takes the second giving: nothing in the store changes.
	if err := os.WriteFile(file, []byte("# rotated in May\nCRM_TOKEN=s3cret-read\n\nMINE=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	keyed, err = st.KeySource(ctx, "crm", map[string]string{"CRM_UPDATE_TOKEN": "s3cret-write", "CRM_TOKEN": "s3cret-read-2"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if want := "CRM_TOKEN is set in " + file + "; for an update, CRM_UPDATE_TOKEN is set in " + file; keyed.State != want || keyed.Missing != "" {
		t.Fatalf("state = %q, missing = %q, want %q", keyed.State, keyed.Missing, want)
	}
	if b, _ := os.ReadFile(file); string(b) != "# rotated in May\nCRM_TOKEN=s3cret-read-2\n\nMINE=1\nCRM_UPDATE_TOKEN=s3cret-write\n" {
		t.Fatalf("the file holds %q", b)
	}
	if got := sourceSets(t, st); !slices.Equal(got, want) {
		t.Fatalf("source_set events after the second giving = %v", got)
	}

	// Where the value is: the env file, and nowhere in the events, in what
	// the call answered or in the store's file.
	evs, err := st.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	said, _ := json.Marshal(map[string]any{"events": evs, "answer": keyed})
	closed = true
	if err := st.Close(ctx); err != nil {
		t.Fatal(err)
	}
	db, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, kept := range [][]byte{said, db} {
		if bytes.Contains(kept, []byte("s3cret")) {
			t.Fatalf("a value is kept outside the env file: %.200s", kept)
		}
	}
}

// What is refused leaves the env file as it was, to the byte, no second
// file and nothing in the store; no refusal repeats a value.
func TestKeySourceRefuses(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "yard.db")
	st := storetest.Open(t, path)
	ctx := context.Background()
	for _, f := range []string{crmKeyFile, "name: bare\nbase: https://bare.example.com\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"} {
		if _, err := st.PutSource(ctx, "s.yaml", f, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(filepath.Dir(path), "sources", "crm.env")
	const body = "# by hand\nCRM_TOKEN=old-s3cret\n"
	write := func(mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(file, mode); err != nil {
			t.Fatal(err)
		}
	}
	for name, c := range map[string]struct {
		source string
		values map[string]string
		mode   os.FileMode
		want   string
	}{
		"a name the declaration does not have": {"crm", map[string]string{"CRM_TOKEN": "new-s3cret", "PATH": "new-s3cret"}, 0o600, "source crm: PATH is no variable of its credential (its declaration names CRM_TOKEN and CRM_UPDATE_TOKEN)"},
		"an empty value":                       {"crm", map[string]string{"CRM_TOKEN": ""}, 0o600, "the value for CRM_TOKEN is empty"},
		"a line break":                         {"crm", map[string]string{"CRM_TOKEN": "new-s3cret\nCRM_UPDATE_TOKEN=x"}, 0o600, "the value for CRM_TOKEN has a line break in it"},
		"no value":                             {"crm", nil, 0o600, "source crm: no value was given"},
		"a file open to others":                {"crm", map[string]string{"CRM_TOKEN": "new-s3cret"}, 0o644, "env_file " + file + " is open to others (mode 0644); chmod 600 it"},
		"a source with no credential":          {"bare", map[string]string{"CRM_TOKEN": "new-s3cret"}, 0o600, "source bare: its declaration names no credential"},
		"a source the yard does not have":      {"nope", map[string]string{"CRM_TOKEN": "new-s3cret"}, 0o600, "source nope: not found"},
	} {
		write(c.mode)
		_, err := st.KeySource(ctx, c.source, c.values, "jo")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
			continue
		}
		var half *store.KeyNotRecorded
		if strings.Contains(err.Error(), "s3cret") || errors.As(err, &half) {
			t.Errorf("%s: the error shows a value, or says the file was written: %v", name, err)
		}
		if b, _ := os.ReadFile(file); string(b) != body {
			t.Errorf("%s: the env file changed: %q", name, b)
		}
		if _, err := os.Lstat(config.EnvFileNew(file)); !os.IsNotExist(err) {
			t.Errorf("%s: a second file was left: %v", name, err)
		}
		if src, err := st.GetSource(ctx, "crm"); err != nil || src.EnvFile != "" {
			t.Errorf("%s: the source = %+v, %v", name, src, err)
		}
		if evs := sourceSets(t, st); len(evs) != 0 {
			t.Errorf("%s: events = %v", name, evs)
		}
	}
}

// An env_file that is a symbolic link is refused, and the file it points
// to is left alone: a link would pass the rule for a file elsewhere, and
// the rename would replace the link, not the file.
func TestKeySourceRefusesALink(t *testing.T) {
	t.Parallel()
	st := storetest.Open(t, filepath.Join(t.TempDir(), "yard.db"))
	ctx := context.Background()
	if _, err := st.PutSource(ctx, "crm.yaml", crmKeyFile, "jo"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	elsewhere, link := filepath.Join(dir, "elsewhere.env"), filepath.Join(dir, "crm.env")
	if err := os.WriteFile(elsewhere, []byte("CRM_TOKEN=old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "crm", store.SourceEnvFile, link, "jo"); err != nil {
		t.Fatal(err)
	}
	_, err := st.KeySource(ctx, "crm", map[string]string{"CRM_TOKEN": "new-s3cret"}, "jo")
	if err == nil || !strings.Contains(err.Error(), "env_file "+link+" is a symbolic link, and the yard writes through none") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(elsewhere); string(b) != "CRM_TOKEN=old\n" {
		t.Fatalf("the file the link points to holds %q", b)
	}
	if got, err := os.Readlink(link); err != nil || got != elsewhere {
		t.Fatalf("the link = %q, %v", got, err)
	}
}
