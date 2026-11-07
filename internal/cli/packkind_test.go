package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// kindDecl is acme's declaration with commands, as a pack of kind source
// carries it.
const kindDecl = `name: acme
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
`

// kindPack is a pack of kind source: acme's declaration and the files its
// commands name.
func kindPack(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"pack.yaml":      "name: acme-pack\nkind: source\nsources:\n  acme:\n    declaration: acme.yaml\n",
		"acme.yaml":      kindDecl,
		"acme/normalize": "#!/bin/sh\necho normalize\n",
		"acme/thread":    "#!/bin/sh\necho thread\n",
	}
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// A pack of kind source is checked without --depot, into the yard: the
// plan names each command, its file and the file's sha256, and installs no
// roles or scripts.
func TestPackCheckSourcePackNeedsNoDepot(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	dir := kindPack(t)
	out, err := run(t, a.packCmd(), "check", dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"pack acme-pack (" + dir + "), kind source, into the yard\n",
		"\ndeclared source acme, from acme.yaml\n",
		"  its calls go to https://acme.test: ping (read)\n",
		"  command hook.normalize: acme/normalize, sha256 ",
		"  command thread.reply: acme/thread, sha256 ",
		"  would be added to the yard's sources\n",
		"  missing  env file     no env_file is set for it",
		// The programs the commands name, each once and in path order, after
		// the digest that pins them.
		"\ndigest sha256:",
		"\nruns 2 programs of the pack as you: acme/normalize, acme/thread\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "its roles and scripts would be copied") {
		t.Errorf("a pack with no roles installs none:\n%s", out)
	}

	// A call a command answers (run) brings its program as any command does:
	// the plan names it with its hash, and the file is one of those that run.
	text, err := os.ReadFile(filepath.Join(dir, "acme.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	calls := "calls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n"
	if !strings.Contains(string(text), calls) {
		t.Fatalf("the declaration has no calls to add one to:\n%s", text)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme.yaml"), []byte(strings.Replace(string(text), calls, calls+"  find: { kind: read, run: [acme/find, search] }\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "acme", "find"), []byte("#!/bin/sh\necho find\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, a.packCmd(), "check", dir)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"  its calls go to https://acme.test: ping (read), find (read)\n",
		"  command call.find.run: acme/find, sha256 ",
		"\nruns 3 programs of the pack as you: acme/find, acme/normalize, acme/thread\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the plan with a run call lacks %q:\n%s", want, out)
		}
	}
}

// A pack with no flows, groups or crew is checked without --depot: the plan
// says what its lines would set on the yard's hooks, and each waits for the
// depot, whose flow it is held to once one is named. apply still takes
// --depot for it: an import goes into a depot and is recorded there.
func TestPackCheckYardWidePackNeedsNoDepot(t *testing.T) {
	t.Parallel()
	a := openApp(t, "github")
	out, err := run(t, a.packCmd(), "check", "ci-red")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"pack ci-red (embedded in yardr ",
		", kind git, into no depot (--depot <depot> plans the import)\n",
		"\nwhat the events of source github say of a bead, and make\n  the pack makes conclusion=failure event=workflow_run head_branch=main -> task:new\n",
		"  would be set on hook github, which has none\n",
		"the makes line \"conclusion=failure event=workflow_run head_branch=main -> task:new\" waits for the depot: its stage is held to the flow the depot follows\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack check lacks %q:\n%s", want, out)
		}
	}
	if out, err := run(t, a.packCmd(), "apply", "ci-red", "--yes"); err == nil || !strings.Contains(err.Error(), `required flag(s) "depot" not set`) {
		t.Errorf("apply without --depot: %v\n%s", err, out)
	}
}

// A pack of kind source refuses --depot, and a pack that names a depot's
// own requires it.
func TestPackCheckHoldsDepotToThePack(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	if out, err := run(t, a.packCmd(), "check", kindPack(t), "--depot", "web"); err == nil ||
		!strings.Contains(err.Error(), "pack acme-pack is of kind source: it is imported into the yard, not into a depot (drop --depot)") {
		t.Errorf("with --depot: %v\n%s", err, out)
	}
	if out, err := run(t, a.packCmd(), "check", "../../packs/starter"); err == nil ||
		!strings.Contains(err.Error(), `required flag(s) "depot" not set`) {
		t.Errorf("without --depot: %v\n%s", err, out)
	}
	if out, err := run(t, a.packCmd(), "apply", kindPack(t), "--depot", "web", "--yes"); err == nil ||
		!strings.Contains(err.Error(), "imported into the yard, not into a depot") {
		t.Errorf("apply with --depot: %v\n%s", err, out)
	}
}

// A pack of kind source applies into the yard with no depot named: the
// source is added, its commands' files copied, and pack list shows the
// import into the yard.
func TestPackApplySourcePack(t *testing.T) {
	t.Parallel()
	a := packApp(t)
	requireNonInteractive(t)
	dir := kindPack(t)
	out, err := run(t, a.packCmd(), "apply", dir, "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	sources := filepath.Join(a.cfg.Home, "sources", "acme")
	for _, want := range []string{
		"applied pack acme-pack to the yard: 1 created, 0 changed, 0 unchanged\n",
		"source acme is declared: 1 call under https://acme.test (yardr source show acme)\n",
		"  its commands' files are under " + sources + ", each mode 0700: acme/thread, acme/normalize\n",
		"next, for source acme: no env_file is set for it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	thread := filepath.Join(sources, "acme", "thread")
	if got, err := os.ReadFile(thread); err != nil || string(got) != "#!/bin/sh\necho thread\n" {
		t.Fatalf("the copy = %q, %v", got, err)
	}
	if out, err := run(t, a.packCmd(), "list"); err != nil || !strings.Contains(out, "acme-pack in the yard ") {
		t.Fatalf("pack list: %v\n%s", err, out)
	}
}

// Adding is the operator's: run by anyone else, the import adds no source,
// and copies no file.
func TestPackApplySourcePackByAnotherSessionAddsNoSource(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	a.actor = "web-builder-1"
	dir := kindPack(t)
	const refusal = "adding a source is the operator's (me), and this is web-builder-1"
	out, err := run(t, a.packCmd(), "apply", dir, "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, want := range []string{
		"  not added: " + refusal + "\n",
		"the source acme was not added: " + refusal + ". The rest of the import does not depend on it\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pack apply lacks %q:\n%s", want, out)
		}
	}
	if _, err := a.st.GetSource(context.Background(), "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the source was added: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.cfg.Home, "sources", "acme")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused source's files were copied: %v", err)
	}
}

// A flow pack's lines for a source the yard has nothing of are said once,
// as optional, in the plan and in the output; the import goes through
// without them, and no step calls the source "missing" or "skipped".
func TestPackApplyShowsAnUnusedSourceOnceAsOptional(t *testing.T) {
	t.Parallel()
	a := openApp(t)
	dir := t.TempDir()
	for name, text := range map[string]string{
		"pack.yaml": "name: flow-pack\n" + `flows:
  default:
    stages:
      new: { group: builders }
      merged: { end: true }
groups:
  builders: { agent: roles/builder.md }
crew:
  mate: { role: roles/mate.md }
sources:
  acme:
    as: mate
    hears:
      task: { new: started, merged: done }
`,
		"roles/builder.md": "build\n",
		"roles/mate.md":    "mate\n",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, a.packCmd(), "check", dir, "--depot", "web", "--crew", "mate=boss")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "  optional acme         the yard has no source acme\n") {
		t.Fatalf("the plan has no optional line for the source:\n%s", out)
	}
	if strings.Contains(out, "missing") || strings.Contains(out, "skipped") {
		t.Fatalf("the plan calls the source missing or skipped:\n%s", out)
	}
	requireNonInteractive(t)
	out, err = run(t, a.packCmd(), "apply", dir, "--depot", "web", "--yes", "--crew", "mate=boss", "--bind", "builders=claude")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "  optional acme         the yard has no source acme\n") {
		t.Fatalf("the output has no optional line for the source:\n%s", out)
	}
	if strings.Contains(out, "missing") || strings.Contains(out, "was skipped") {
		t.Fatalf("the output calls the source missing or skipped:\n%s", out)
	}
}
