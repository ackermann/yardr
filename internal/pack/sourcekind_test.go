package pack_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// cmdDecl is a declaration with commands, as a pack of kind source carries:
// what the source sends in (hook.normalize), what the yard does in its
// threads (thread.*), and its issues as a link.
const cmdDecl = `name: acme
about: Acme issues.
base: https://acme.test
credential: { env: ACME_TOKEN }
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
hook:
  verify:
    scheme: hmac-sha256
    signed: body
    signature: { header: Acme-Signature, encoding: hex }
    delivery: { header: Acme-Delivery }
  bodies: unique
  normalize: [acme/normalize]
  keys: [team]
thread:
  reply: [acme/thread, reply]
  posted: [acme/thread, posted]
  read: [acme/thread, read]
  place: team
  open: [acme/thread, open]
  opened: [acme/thread, opened]
  state: [acme/thread, state]
  states: [started, review, done]
link:
  host: acme.test
  issue: '^/issue/([0-9]+)'
  fixed: "https://acme.test/issue/{1}"
  thread: "{1}"
`

const sourcePackYAML = `name: acme-pack
kind: source
sources:
  acme:
    declaration: acme.yaml
`

func sourcePackFiles() map[string]string {
	return map[string]string{
		"pack.yaml":      sourcePackYAML,
		"acme.yaml":      cmdDecl,
		"acme/normalize": "#!/bin/sh\necho normalize\n",
		"acme/thread":    "#!/bin/sh\necho thread\n",
	}
}

// A pack of kind source is sources alone: each with a declaration, which
// may name commands, and the files those commands name beside it.
func TestSourcePackLoads(t *testing.T) {
	p := load(t, writePack(t, sourcePackFiles()))
	if p.Kind != pack.KindSource || len(p.Flows) != 0 || len(p.Groups) != 0 || len(p.Crew) != 0 {
		t.Fatalf("pack = %+v", p)
	}
	if len(p.Declared) != 1 || len(p.Sources) != 0 {
		t.Fatalf("declared = %+v, sources = %+v", p.Declared, p.Sources)
	}
	d := p.Declared[0]
	if d.Name != "acme" || d.File != "acme.yaml" || d.Text != cmdDecl {
		t.Fatalf("declared = %+v", d)
	}
	if d.Declaration.Hook == nil || d.Declaration.Thread == nil || d.Declaration.Link == nil {
		t.Fatalf("declaration = %+v", d.Declaration)
	}
	// The commands' files are the source's, copied into the yard: not
	// roles or scripts, so not among the pack's files.
	if len(p.Files()) != 0 {
		t.Fatalf("files = %v", p.Files())
	}
}

// A pack of kind source may bring how its source's identity keys fold,
// with the commands and the link.
func TestSourcePackBringsIdentity(t *testing.T) {
	files := sourcePackFiles()
	files["acme.yaml"] = cmdDecl + "identity:\n  fold: lower\n"
	p := load(t, writePack(t, files))
	if len(p.Declared) != 1 {
		t.Fatalf("declared = %+v", p.Declared)
	}
	if id := p.Declared[0].Declaration.Identity; id == nil || id.Fold != "lower" {
		t.Fatalf("identity = %+v", id)
	}
}

// A pack of kind source may bring what its source sends in about pull
// requests and CI, with the commands that read them back: in a pack for
// a depot the key is refused.
func TestSourcePackBringsEvents(t *testing.T) {
	files := sourcePackFiles()
	files["acme.yaml"] = cmdDecl + "events:\n  runs: [run]\n  head_from: [pull]\n  pr: [acme/pr]\n  ci: [acme/ci]\n"
	files["acme/pr"] = "#!/bin/sh\necho pr\n"
	files["acme/ci"] = "#!/bin/sh\necho ci\n"
	p := load(t, writePack(t, files))
	if len(p.Declared) != 1 {
		t.Fatalf("declared = %+v", p.Declared)
	}
	ev := p.Declared[0].Declaration.Events
	if ev == nil || len(ev.Runs) != 1 || len(ev.HeadFrom) != 1 || len(ev.PR) != 1 || len(ev.CI) != 1 {
		t.Fatalf("events = %+v", ev)
	}
}

func TestSourcePackRefusals(t *testing.T) {
	validFlow := `
flows:
  default:
    stages:
      new: { group: builders }
      merged: { end: true }
groups:
  builders: { agent: roles/builder.md }
`
	cases := []struct {
		name     string
		yaml     string            // appended to sourcePackYAML; "" is as it is
		files    map[string]string // changed in the pack; "" deletes
		key, msg string
		line     int
	}{
		{name: "flows", yaml: validFlow, files: map[string]string{"roles/builder.md": "build\n"},
			key: "flows", line: 7, msg: "carries sources alone, and no flows"},
		{name: "groups", yaml: "\ngroups:\n  builders: { agent: roles/builder.md }\n", files: map[string]string{"roles/builder.md": "build\n"},
			key: "groups", line: 7, msg: "carries sources alone, and no groups"},
		{name: "crew", yaml: "\ncrew:\n  boss: { role: roles/boss.md }\n", files: map[string]string{"roles/boss.md": "boss\n"},
			key: "crew", line: 7, msg: "carries sources alone, and no crew"},
		{name: "a stage map beside the declaration",
			yaml: "    as: yardmaster\n",
			key:  "sources.acme.as", line: 6, msg: "has declaration: <file> and nothing else"},
		{name: "says beside the declaration",
			yaml: "    says: []\n",
			key:  "sources.acme.says", line: 6, msg: "hears, says and makes are the flow pack's to say of it"},
		{name: "limits in the declaration",
			files: map[string]string{"acme.yaml": cmdDecl + "limits:\n  command: [/opt/q, \"{provider}\"]\n  kinds: { kimi: kimi }\n"},
			key:   "limits", line: 30, msg: "a declaration in a pack of kind source has"},
		{name: "a missing command file",
			files: map[string]string{"acme/thread": ""},
			key:   "sources.acme.declaration", line: 5, msg: "names the command file acme/thread, which is not in the pack"},
		{name: "a declaration for a built-in name",
			yaml: "  github:\n    declaration: acme.yaml\n",
			key:  "sources.github", line: 6, msg: "a declaration is for a source of another name"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := sourcePackFiles()
			if c.yaml != "" {
				files["pack.yaml"] += c.yaml
			}
			for name, content := range c.files {
				if content == "" {
					delete(files, name)
				} else {
					files[name] = content
				}
			}
			dir := writePack(t, files)
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("err = %v, want the pack's errors", err)
			}
			// The file is the pack's own, or the declaration's for what
			// that says.
			file := "pack.yaml"
			if c.key == "limits" {
				file = "acme.yaml"
			}
			want := filepath.Join(dir, filepath.FromSlash(file))
			if !slices.ContainsFunc(errs, func(e pack.Error) bool {
				return e.File == want && e.Key == c.key && e.Line == c.line && strings.Contains(e.Msg, c.msg)
			}) {
				t.Fatalf("no error at %s:%d %s with %q:\n%v", file, c.line, c.key, c.msg, errs)
			}
		})
	}
}

// A pack of kind source is planned into a yard with no depot: its sources
// alone. The plan names each command, its file and the file's sha256, and
// asks for the credential's file as for any declared source. A depot is
// refused for it.
func TestSourcePackPlansIntoAYardWithNoDepot(t *testing.T) {
	st := storetest.New(t)
	o := pack.Options{Home: t.TempDir()}
	p := load(t, writePack(t, sourcePackFiles()))
	plan, err := pack.Build(ctx, st, p, o)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Depot != "" || plan.Kind != pack.KindSource || plan.Home != o.Home || len(plan.Questions) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if got := actions(plan, pack.Create); !slices.Equal(got, []string{"source acme"}) {
		t.Fatalf("creates = %v", got)
	}
	d := declared(t, plan, "acme")
	if d.Action != pack.Create || d.Base != "https://acme.test" || !slices.Equal(d.Calls, []string{"ping (read)"}) {
		t.Fatalf("declared = %+v", d)
	}
	normalize := sha256.Sum256([]byte("#!/bin/sh\necho normalize\n"))
	thread := sha256.Sum256([]byte("#!/bin/sh\necho thread\n"))
	want := []pack.CommandPlan{
		{Key: "thread.reply", Program: "acme/thread", SHA256: hex.EncodeToString(thread[:])},
		{Key: "thread.posted", Program: "acme/thread", SHA256: hex.EncodeToString(thread[:])},
		{Key: "thread.read", Program: "acme/thread", SHA256: hex.EncodeToString(thread[:])},
		{Key: "thread.open", Program: "acme/thread", SHA256: hex.EncodeToString(thread[:])},
		{Key: "thread.opened", Program: "acme/thread", SHA256: hex.EncodeToString(thread[:])},
		{Key: "thread.state", Program: "acme/thread", SHA256: hex.EncodeToString(thread[:])},
		{Key: "hook.normalize", Program: "acme/normalize", SHA256: hex.EncodeToString(normalize[:])},
	}
	if !slices.Equal(d.Commands, want) {
		t.Fatalf("commands = %+v, want %+v", d.Commands, want)
	}
	if n := declaredNeed(t, d, store.SourceEnvFile); n.Status != "missing" || !strings.Contains(n.Detail, "ACME_TOKEN") {
		t.Fatalf("env_file = %+v", n)
	}
	if _, err := pack.Build(ctx, st, p, pack.Options{Depot: "web", Home: o.Home}); err == nil ||
		!strings.Contains(err.Error(), "imported into the yard, not into depot web") {
		t.Fatalf("a depot was taken: %v", err)
	}
}

// A pack of kind source applies into a yard with no depot: the source is
// added, its commands' files copied as yardr source add copies them, and
// the import recorded into the yard. A second apply after a script changed
// shows the changed file as a change, and copies it.
func TestSourcePackAppliesIntoAYardWithNoDepot(t *testing.T) {
	st := storetest.New(t)
	home := t.TempDir()
	o := pack.Options{Home: home}
	dir := writePack(t, sourcePackFiles())
	h := head(t, st)

	res, err := pack.Apply(ctx, st, build(t, st, load(t, dir), o), pack.ApplyOptions{Actor: "jo"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Created != 1 || !res.Recorded || len(res.Declared) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got, err := st.GetSource(ctx, "acme"); err != nil || got.Text != cmdDecl {
		t.Fatalf("source = %+v, %v", got, err)
	}
	thread := filepath.Join(home, "sources", "acme", "acme", "thread")
	got, err := os.ReadFile(thread)
	if err != nil || string(got) != "#!/bin/sh\necho thread\n" {
		t.Fatalf("the copy = %q, %v", got, err)
	}
	if fi, err := os.Stat(thread); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("the copy = %v, %v", fi, err)
	}
	recs, err := st.Packs(ctx)
	if err != nil || len(recs) != 1 || recs[0].Pack != "acme-pack" || recs[0].Depot != "" {
		t.Fatalf("records = %+v, %v", recs, err)
	}
	if kinds := kinds(t, st, h); !slices.Equal(kinds, []string{"source_added", "pack_applied"}) {
		t.Fatalf("log = %v", kinds)
	}

	// Nothing changed: the next plan is unchanged.
	plan := build(t, st, load(t, dir), o)
	if got := find(t, plan, "source", "acme"); got.Action != pack.Unchanged {
		t.Fatalf("second plan: %+v", got)
	}

	// The pack's script changed: the plan shows the changed file.
	if err := os.WriteFile(filepath.Join(dir, "acme", "thread"), []byte("#!/bin/sh\necho thread-two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	got2 := find(t, plan, "source", "acme")
	old := sha256.Sum256([]byte("#!/bin/sh\necho thread\n"))
	newer := sha256.Sum256([]byte("#!/bin/sh\necho thread-two\n"))
	want := []pack.Diff{{Field: "file acme/thread", Old: "sha256 " + hex.EncodeToString(old[:])[:12], New: "sha256 " + hex.EncodeToString(newer[:])[:12]}}
	if got2.Action != pack.Change || !reflect.DeepEqual(got2.Changes, want) {
		t.Fatalf("after the script changed: %+v", got2)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "jo"}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(thread); err != nil || string(got) != "#!/bin/sh\necho thread-two\n" {
		t.Fatalf("the copy = %q, %v", got, err)
	}
}

// Adding is the operator's, as for a declared source of a pack for a
// depot: run by anyone else, the import adds no source, and copies no
// file.
func TestSourcePackApplyByACrewMemberAddsNoSource(t *testing.T) {
	st := storetest.New(t)
	if err := st.SetSetting(ctx, store.OperatorSetting, "me", "me"); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	plan := build(t, st, load(t, writePack(t, sourcePackFiles())), pack.Options{Home: home})
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{SourceRefused: map[string]string{"acme": "adding is the operator's"}, Actor: "ann"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetSource(ctx, "acme"); !errors.Is(err, store.ErrNotFound) || len(res.Declared) != 0 {
		t.Fatalf("a refused source was added: %v, %+v", err, res)
	}
	if _, err := os.Stat(filepath.Join(home, "sources", "acme")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused source's files were copied: %v", err)
	}
	if !res.Recorded {
		t.Fatalf("the import was not recorded: %+v", res)
	}
}

// A command file changed since the plan was made refuses the import, as a
// changed role would: what is copied is what was planned.
func TestApplyRefusesACommandFileChangedSinceThePlan(t *testing.T) {
	st := storetest.New(t)
	o := pack.Options{Home: t.TempDir()}
	dir := writePack(t, sourcePackFiles())
	plan := build(t, st, load(t, dir), o)
	if err := os.WriteFile(filepath.Join(dir, "acme", "thread"), []byte("#!/bin/sh\necho other\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "jo"})
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") ||
		!strings.Contains(err.Error(), "changed since the plan was made") {
		t.Fatalf("err = %v, want the import refused", err)
	}
	if _, err := st.GetSource(ctx, "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a refused import added the source: %v", err)
	}
}

// Replacing a declaration that had copied command files with one that has
// none removes the old files, as source add does when its declaration loses
// its commands.
func TestSourcePackReplacementRemovesOldCommandFiles(t *testing.T) {
	st := storetest.New(t)
	o := pack.Options{Home: t.TempDir()}
	dir := writePack(t, sourcePackFiles())
	plan := build(t, st, load(t, dir), o)
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "jo"}); err != nil {
		t.Fatal(err)
	}
	files := filepath.Join(o.Home, "sources", "acme")
	if _, err := os.Stat(files); err != nil {
		t.Fatal(err)
	}

	text := `name: acme
base: https://acme.test
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
`
	if err := os.WriteFile(filepath.Join(dir, "acme.yaml"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	if got := find(t, plan, "source", "acme"); got.Action != pack.Change {
		t.Fatalf("replacement plan = %+v", got)
	}
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "jo"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(files); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old command files remain: %v", err)
	}
}

// Pruning a dropped source removes its commands' files with it, as yardr
// source rm does.
func TestPruneRemovesTheCommandsFiles(t *testing.T) {
	st := storetest.New(t)
	o := pack.Options{Home: t.TempDir()}
	dir := writePack(t, sourcePackFiles())
	plan := build(t, st, load(t, dir), o)
	if _, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Actor: "jo"}); err != nil {
		t.Fatal(err)
	}
	files := filepath.Join(o.Home, "sources", "acme")
	if _, err := os.Stat(files); err != nil {
		t.Fatal(err)
	}
	other := strings.Replace(pushover, "name: pushover", "name: other", 1)
	files2 := map[string]string{
		"pack.yaml":      "name: acme-pack\nkind: source\nsources:\n  other:\n    declaration: other.yaml\n",
		"other.yaml":     other,
		"acme/normalize": "#!/bin/sh\necho normalize\n",
		"acme/thread":    "#!/bin/sh\necho thread\n",
	}
	dir2 := writePack(t, files2)
	plan = build(t, st, load(t, dir2), o)
	if got := find(t, plan, "source", "acme"); got.Action != pack.Dropped {
		t.Fatalf("source = %+v", got)
	}
	res, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Prune: true, Actor: "jo"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Pruned, "source acme") {
		t.Fatalf("pruned = %+v", res)
	}
	if _, err := os.Stat(files); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the files are still there: %v", err)
	}
}

// A pack of kind source without sources is no pack: it declares at least
// one.
func TestSourcePackWithoutSourcesIsRefused(t *testing.T) {
	for name, yaml := range map[string]string{
		"missing": "name: acme-pack\nkind: source\n",
		"empty":   "name: acme-pack\nkind: source\nsources: {}\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := writePack(t, map[string]string{"pack.yaml": yaml})
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) || len(errs) != 1 {
				t.Fatalf("err = %v, want one error", err)
			}
			if e := errs[0]; e.Key != "sources" || !strings.Contains(e.Msg, "declares") {
				t.Fatalf("error = %s", e)
			}
		})
	}
}
