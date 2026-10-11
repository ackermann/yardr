package pack_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// pushover is the declaration docs/pushover.md gives, which the cases
// below each change in one place.
const pushover = `name: pushover
about: A push to a person's phone through Pushover.
base: https://api.pushover.net/1
credential:
  env: PUSHOVER_TOKEN
calls:
  message:
    kind: update
    about: One push to one person
    params:
      user:  { type: string, pattern: "^[A-Za-z0-9]{30}$" }
      title: { type: string, max: 250 }
      text:  { type: string, max: 1024 }
      link:  { type: string, max: 512, optional: true }
    request:
      method: POST
      path: /messages.json
      body: { token: "{credential}", user: "{user}", title: "{title}", message: "{text}", url: "{link}" }
notify:
  call: message
  address: { param: user, env: PUSHOVER_USER }
  title: title
  text: text
  link: link
`

// smallDeclared is small with a declared source.
const smallDeclared = small + `sources:
  pushover:
    declaration: sources/pushover.yaml
`

func declaredFiles(yaml, declaration string) map[string]string {
	files := smallFiles(yaml)
	files["sources/pushover.yaml"] = declaration
	return files
}

func declared(t *testing.T, p *pack.Plan, name string) pack.DeclaredPlan {
	t.Helper()
	i := slices.IndexFunc(p.Declared, func(d pack.DeclaredPlan) bool { return d.Source == name })
	if i < 0 {
		t.Fatalf("the plan has no declared source %s: %+v", name, p.Declared)
	}
	return p.Declared[i]
}

func declaredNeed(t *testing.T, d pack.DeclaredPlan, what string) pack.Need {
	t.Helper()
	i := slices.IndexFunc(d.Needs, func(n pack.Need) bool { return n.What == what })
	if i < 0 {
		t.Fatalf("the declared source %s says nothing of %s: %+v", d.Source, what, d.Needs)
	}
	return d.Needs[i]
}

// A pack names a declaration file under a source yardr has no adapter for,
// and the file is read as yardr source add reads it.
func TestPackReadsADeclaredSource(t *testing.T) {
	p := load(t, writePack(t, declaredFiles(smallDeclared, pushover)))
	if len(p.Declared) != 1 || len(p.Sources) != 0 {
		t.Fatalf("declared = %+v, sources = %+v", p.Declared, p.Sources)
	}
	d := p.Declared[0]
	if d.Name != "pushover" || d.File != "sources/pushover.yaml" || d.Text != pushover {
		t.Fatalf("declared = %+v", d)
	}
	if d.Declaration == nil || d.Declaration.Notify == nil || d.Declaration.Notify.AddressEnv != "PUSHOVER_USER" {
		t.Fatalf("declaration = %+v", d.Declaration)
	}
	// The declaration goes into the store, not under the yard's home.
	if slices.Contains(p.Files(), "sources/pushover.yaml") {
		t.Fatalf("files = %v", p.Files())
	}
}

// What is wrong with a declared source is named by the file it is in: the
// pack's own for what the pack says, the declaration's for what that says.
func TestDeclaredSourceRefusals(t *testing.T) {
	cases := []struct {
		name        string
		yaml        string // of the pack; "" is smallDeclared
		declaration string // "" is pushover; "-" is no file
		file, key   string
		line        int
		msg         string
	}{
		{name: "a limits key", declaration: strings.Replace(pushover, "calls:\n", "limits:\n  hour: 60\ncalls:\n", 1),
			file: "sources/pushover.yaml", key: "limits", line: 6, msg: "a declaration in a pack has name, about, base, credential, check, idempotency, calls, notify and no other key"},
		// The key the reader has since learned: a command the yard would
		// run. A pack brings none, whether the file has calls or is that
		// command alone.
		{name: "a limits command", declaration: pushover + "limits:\n  command: [/opt/q, \"{provider}\"]\n  kinds: { kimi: kimi }\n",
			file: "sources/pushover.yaml", key: "limits", line: 25, msg: "a pack brings no command the yard runs"},
		{name: "a limits command alone", declaration: "name: pushover\nlimits:\n  command: [/opt/q, \"{provider}\"]\n  kinds: { kimi: kimi }\n",
			file: "sources/pushover.yaml", key: "limits", line: 2, msg: "a pack brings no command the yard runs"},
		// The keys the reader has since learned: the commands by which the
		// yard speaks in a source's threads, and the source's issues as a
		// bead's link. A pack brings neither, until it is added to the list.
		{name: "a thread key", declaration: pushover + "thread:\n  reply: [linear/thread, reply]\n  place: team\n",
			file: "sources/pushover.yaml", key: "thread", line: 25, msg: "a pack brings no command the yard runs"},
		{name: "a link key", declaration: pushover + "link:\n  host: linear.app\n  issue: '^/x'\n  fixed: y\n  thread: z\n",
			file: "sources/pushover.yaml", key: "link", line: 25, msg: "a pack brings no command the yard runs"},
		{name: "a key nobody knows", declaration: pushover + "events: {}\n",
			file: "sources/pushover.yaml", key: "events", line: 25, msg: "and no other key"},
		{name: "what yardr source add refuses", declaration: strings.Replace(pushover, "https://api.pushover.net/1", "http://api.pushover.net/1", 1),
			file: "sources/pushover.yaml", key: "base", line: 3, msg: "http:// is for a stand-in on this machine only"},
		// What the file says of its updates is refused as yardr source add
		// refuses it: a value where the name of a variable goes.
		{name: "a value for the name of the update's credential", declaration: strings.Replace(pushover, "  env: PUSHOVER_TOKEN\n", "  env: PUSHOVER_TOKEN\n  update_env: \"s3cr3t value\"\n", 1),
			file: "sources/pushover.yaml", key: "credential.update_env", line: 6, msg: "is no name of a variable"},
		{name: "a key over the credential's header", declaration: strings.Replace(pushover, "  env: PUSHOVER_TOKEN\n", "  env: PUSHOVER_TOKEN\n  header: \"X-Token: {credential}\"\nidempotency: x-token\n", 1),
			file: "sources/pushover.yaml", key: "idempotency", line: 7, msg: "is the header the credential is sent in"},
		{name: "another name in the file", declaration: strings.Replace(pushover, "name: pushover", "name: push", 1),
			file: "pack.yaml", key: "sources.pushover.declaration", line: 24, msg: "sources/pushover.yaml declares the source push, and the pack names it pushover"},
		{name: "no file", declaration: "-",
			file: "pack.yaml", key: "sources.pushover.declaration", line: 24, msg: "declaration sources/pushover.yaml is missing"},
		{name: "a file outside the pack", yaml: strings.Replace(smallDeclared, "sources/pushover.yaml", "../pushover.yaml", 1),
			file: "pack.yaml", key: "sources.pushover.declaration", line: 24, msg: "leaves the pack's directory"},
		{name: "a stage map beside it", yaml: smallDeclared + "    as: yardmaster\n",
			file: "pack.yaml", key: "sources.pushover.as", line: 25, msg: "a declared source has declaration: <file> and nothing else"},
		{name: "neither an adapter nor a declaration", yaml: small + "sources:\n  pushover:\n    as: yardmaster\n",
			file: "pack.yaml", key: "sources.pushover", line: 23, msg: "no adapter for source pushover"},
		{name: "a declaration for a source yardr has built in", yaml: small + "sources:\n  linear:\n    declaration: sources/pushover.yaml\n",
			file: "pack.yaml", key: "sources.linear.declaration", line: 24, msg: "unknown key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := declaredFiles(or(c.yaml, smallDeclared), or(c.declaration, pushover))
			if c.declaration == "-" {
				delete(files, "sources/pushover.yaml")
			}
			dir := writePack(t, files)
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("err = %v, want the pack's errors", err)
			}
			want := filepath.Join(dir, filepath.FromSlash(c.file))
			if !slices.ContainsFunc(errs, func(e pack.Error) bool {
				return e.File == want && e.Key == c.key && e.Line == c.line && strings.Contains(e.Msg, c.msg)
			}) {
				t.Fatalf("no error at %s:%d %s with %q:\n%v", c.file, c.line, c.key, c.msg, errs)
			}
			// One mistake is one line: the reader's own "unknown key" does
			// not follow the pack's refusal of the same key.
			if n := len(errs); c.name != "a declaration for a source yardr has built in" && n != 1 {
				t.Fatalf("%d errors for one mistake:\n%v", n, errs)
			}
		})
	}
}

func or(s, otherwise string) string {
	if s == "" {
		return otherwise
	}
	return s
}

// An import adds the declaration to the yard's sources and nothing else of
// it: the credential's file and the user who is told stay to be named, and
// the plan gives the command for each. A second import changes nothing.
func TestApplyAddsADeclaredSource(t *testing.T) {
	st, o := yard(t)
	dir := writePack(t, declaredFiles(smallDeclared, pushover))

	plan := build(t, st, load(t, dir), o)
	d := declared(t, plan, "pushover")
	if d.Action != pack.Create || d.Base != "https://api.pushover.net/1" || !slices.Equal(d.Calls, []string{"message (update)"}) {
		t.Fatalf("declared = %+v", d)
	}
	if n := declaredNeed(t, d, store.SourceEnvFile); n.Status != "missing" || !strings.Contains(n.Detail, "yardr source set pushover env_file=<a file of mode 0600 that sets PUSHOVER_TOKEN>") {
		t.Fatalf("env_file = %+v", n)
	}
	if n := declaredNeed(t, d, store.UserNotify); n.Status != "missing" || !strings.Contains(n.Detail, "yardr user set <user> notify=pushover env_file=<a file of mode 0600 that sets PUSHOVER_USER>") {
		t.Fatalf("notify = %+v", n)
	}
	if _, err := st.GetSource(ctx, "pushover"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a plan wrote the source: %v", err)
	}

	res := apply(t, st, plan, nil)
	if !slices.Equal(res.Declared, []string{"pushover"}) {
		t.Fatalf("result = %+v", res)
	}
	src, err := st.GetSource(ctx, "pushover")
	if err != nil || src.Text != pushover || src.EnvFile != "" {
		t.Fatalf("source = %+v, %v", src, err)
	}
	if rec := record(t, st); !slices.ContainsFunc(rec.Objects, func(po store.PackObject) bool { return po.Kind == "source" && po.Name == "pushover" }) {
		t.Fatalf("the source is not on record: %+v", rec.Objects)
	}

	plan = build(t, st, load(t, dir), o)
	if got := find(t, plan, "source", "pushover"); got.Action != pack.Unchanged {
		t.Fatalf("a second import would %s the source", got.Action)
	}
	if c, ch := actions(plan, pack.Create), actions(plan, pack.Change); len(c)+len(ch) != 0 {
		t.Fatalf("a second import would create %v and change %v", c, ch)
	}
}

// A declaration the yard has under the pack's name is replaced, and the
// plan shows where its calls would go instead: a source is where a
// credential of the yard is sent. Its env_file stays, and so does the user
// who names it, which the plan then says.
func TestApplyReplacesADeclaredSourceAndThePlanShowsWhatChanges(t *testing.T) {
	st, o := yard(t)
	if _, err := st.PutSource(ctx, "pushover.yaml", strings.Replace(pushover, "https://api.pushover.net/1", "https://push.example.com/1", 1), "me"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "pushover.env")
	if err := os.WriteFile(env, []byte("PUSHOVER_TOKEN=secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "pushover", store.SourceEnvFile, env, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddUser(ctx, "ada", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/home/ada/ada.env"}, "ada"); err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(pushover, "      path: /messages.json", "      path: /messages", 1)
	changed = strings.Replace(changed, "calls:\n", "calls:\n  limits: { kind: read, request: { method: GET, path: /apps/limits.json } }\n", 1)
	plan := build(t, st, load(t, writePack(t, declaredFiles(smallDeclared, changed))), o)

	got := find(t, plan, "source", "pushover")
	want := []pack.Diff{
		{Field: "base", Old: "https://push.example.com/1", New: "https://api.pushover.net/1"},
		{Field: "call limits", Old: "", New: "(read) GET /apps/limits.json"},
		{Field: "call message", Old: "(update) POST /messages.json", New: "(update) POST /messages"},
	}
	if got.Action != pack.Change || !slices.Equal(got.Changes, want) {
		t.Fatalf("source = %s %+v, want a change of %+v", got.Action, got.Changes, want)
	}
	d := declared(t, plan, "pushover")
	if n := declaredNeed(t, d, store.SourceEnvFile); n.Status != "ok" || strings.Contains(n.Detail, "secret-token") {
		t.Fatalf("env_file = %+v", n)
	}
	if n := declaredNeed(t, d, store.UserNotify); n.Status != "ok" || n.Detail != "tells ada" {
		t.Fatalf("notify = %+v", n)
	}

	apply(t, st, plan, nil)
	src, err := st.GetSource(ctx, "pushover")
	if err != nil || src.Text != changed || src.EnvFile != env {
		t.Fatalf("source = %+v, %v", src, err)
	}
}

// Another text that declares the same is a change too, and said to be one
// of the text alone.
func TestADeclarationThatOnlyReadsDifferentlyIsSaidSo(t *testing.T) {
	st, o := yard(t)
	if _, err := st.PutSource(ctx, "pushover.yaml", "# as the operator added it\n"+pushover, "me"); err != nil {
		t.Fatal(err)
	}
	plan := build(t, st, load(t, writePack(t, declaredFiles(smallDeclared, pushover))), o)
	got := find(t, plan, "source", "pushover")
	if got.Action != pack.Change || len(got.Changes) != 1 || got.Changes[0].Field != "declaration" {
		t.Fatalf("source = %s %+v", got.Action, got.Changes)
	}
}

// Adding a source is the operator's. Run by anyone else the source is
// neither added nor recorded, and the rest of the pack is imported; the
// operator's next apply adds it.
func TestDeclaredSourceRefusedLeavesTheRestOfTheImport(t *testing.T) {
	st, o := yard(t)
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	p := load(t, writePack(t, declaredFiles(smallDeclared, pushover)))

	plan := build(t, st, p, o)
	a := pack.Answers{}
	for _, q := range plan.Questions {
		a[q.ID] = map[string]string{"kind": "claude", "dir": t.TempDir()}[q.Field]
	}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, SourceRefused: map[string]string{"pushover": "not the operator"}, Actor: "builder"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSource(ctx, "pushover"); !errors.Is(err, store.ErrNotFound) || len(res.Declared) != 0 {
		t.Fatalf("a refused source was added: %v, %+v", err, res)
	}
	if _, err := st.GetGroup(ctx, "web-builders"); err != nil || !res.Recorded {
		t.Fatalf("the rest was not imported: %v, %+v", err, res)
	}
	if rec := record(t, st); slices.ContainsFunc(rec.Objects, func(po store.PackObject) bool { return po.Kind == "source" }) {
		t.Fatalf("a refused source is on record: %+v", rec.Objects)
	}

	// Without the refusal the store holds the same line, and says whose it is.
	plan = build(t, st, p, o)
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: a, Actor: "builder"}); err == nil || !strings.Contains(err.Error(), "adding a source is the operator's (me)") {
		t.Fatalf("err = %v, want the store's refusal", err)
	}

	plan = build(t, st, p, o)
	if got := actions(plan, pack.Create); !slices.Equal(got, []string{"source pushover"}) {
		t.Fatalf("the next plan would create %v", got)
	}
	apply(t, st, plan, nil)
	if _, err := st.GetSource(ctx, "pushover"); err != nil {
		t.Fatal(err)
	}
}

// A pack that drops its declaration leaves the source in the yard unless
// told to prune. A prune removes it, but not while a user is told through
// it, and never one whose declaration was added by hand since.
func TestDroppedDeclaredSourceIsPrunedOnlyWhenAsked(t *testing.T) {
	st, o := yard(t)
	dir := writePack(t, declaredFiles(smallDeclared, pushover))
	apply(t, st, build(t, st, load(t, dir), o), nil)
	if _, err := st.AddUser(ctx, "ada", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/home/ada/ada.env"}, "ada"); err != nil {
		t.Fatal(err)
	}
	edit(t, dir, "sources:\n  pushover:\n    declaration: sources/pushover.yaml\n", "")
	prune := func() *pack.Result {
		t.Helper()
		res, err := pack.Apply(ctx, st, build(t, st, load(t, dir), o), pack.ApplyOptions{Answers: pack.Answers{}, Prune: true, Actor: "me"})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	plan := build(t, st, load(t, dir), o)
	got := find(t, plan, "source", "pushover")
	if got.Action != pack.Dropped || !strings.Contains(got.Held, "user ada is told through it") {
		t.Fatalf("source = %s, held %q", got.Action, got.Held)
	}
	if res := prune(); !slices.Contains(res.Left, "source pushover") {
		t.Fatalf("a held source was pruned: %+v", res)
	}
	if _, err := st.GetSource(ctx, "pushover"); err != nil {
		t.Fatal(err)
	}

	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: ""}, "ada"); err != nil {
		t.Fatal(err)
	}
	res := apply(t, st, build(t, st, load(t, dir), o), nil)
	if !slices.Contains(res.Left, "source pushover") {
		t.Fatalf("without --prune the source is left: %+v", res)
	}
	if res := prune(); !slices.Contains(res.Pruned, "source pushover") {
		t.Fatalf("pruned = %+v", res)
	}
	if _, err := st.GetSource(ctx, "pushover"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the source is still there: %v", err)
	}
}

func TestADeclaredSourceAddedByHandSinceIsTheYards(t *testing.T) {
	st, o := yard(t)
	dir := writePack(t, declaredFiles(smallDeclared, pushover))
	apply(t, st, build(t, st, load(t, dir), o), nil)
	if _, err := st.PutSource(ctx, "pushover.yaml", "# mine now\n"+pushover, "me"); err != nil {
		t.Fatal(err)
	}
	edit(t, dir, "sources:\n  pushover:\n    declaration: sources/pushover.yaml\n", "")
	plan := build(t, st, load(t, dir), o)
	if slices.ContainsFunc(plan.Objects, func(o pack.Object) bool { return o.Kind == "source" }) {
		t.Fatalf("the yard's own source is in the plan: %v", actions(plan, pack.Dropped))
	}
}

// A pack's declaration may name a second credential for its updates and the
// header an update's key goes in. The plan asks for an env_file that sets
// both variables, and shows either as a change of a source the yard has.
func TestADeclaredSourceWithACredentialForItsUpdates(t *testing.T) {
	st, o := yard(t)
	if _, err := st.PutSource(ctx, "pushover.yaml", pushover, "me"); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(t.TempDir(), "pushover.env")
	if err := os.WriteFile(env, []byte("PUSHOVER_TOKEN=secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, "pushover", store.SourceEnvFile, env, "me"); err != nil {
		t.Fatal(err)
	}
	two := strings.Replace(pushover, "  env: PUSHOVER_TOKEN\n", "  env: PUSHOVER_TOKEN\n  update_env: PUSHOVER_SEND_TOKEN\nidempotency: Idempotency-Key\n", 1)
	plan := build(t, st, load(t, writePack(t, declaredFiles(smallDeclared, two))), o)
	want := []pack.Diff{
		{Field: "credential", Old: "PUSHOVER_TOKEN", New: "PUSHOVER_TOKEN, for an update PUSHOVER_SEND_TOKEN"},
		{Field: "idempotency", Old: "", New: "Idempotency-Key"},
	}
	if got := find(t, plan, "source", "pushover"); got.Action != pack.Change || !slices.Equal(got.Changes, want) {
		t.Fatalf("source = %s %+v, want a change of %+v", got.Action, got.Changes, want)
	}
	d := declared(t, plan, "pushover")
	if n := declaredNeed(t, d, store.SourceEnvFile); n.Status != "missing" || !strings.Contains(n.Detail, "does not set PUSHOVER_SEND_TOKEN") {
		t.Fatalf("env_file, with the first variable alone = %+v", n)
	}
	if err := os.WriteFile(env, []byte("PUSHOVER_TOKEN=secret-token\nPUSHOVER_SEND_TOKEN=secret-send\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	d = declared(t, build(t, st, load(t, writePack(t, declaredFiles(smallDeclared, two))), o), "pushover")
	if n := declaredNeed(t, d, store.SourceEnvFile); n.Status != "ok" || !strings.Contains(n.Detail, "for an update, PUSHOVER_SEND_TOKEN is set in") || strings.Contains(n.Detail, "secret") {
		t.Fatalf("env_file, with both = %+v", n)
	}
}
