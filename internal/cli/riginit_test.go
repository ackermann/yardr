package cli

import (
	"bytes"
	"context"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/roles"
)

// rig init seeds a Go repo with the roles naming the yard's operator, a gate
// guessed from go.mod and the starter flow; a second run changes nothing.
func TestRigInit(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRig(ctx, store.Rig{Name: "web", Path: dir, Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"operator": "Jo", "mayor": "boss"} {
		if err := st.SetSetting(ctx, k, v, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.rigCmd(), "init", "web")
	if err != nil {
		t.Fatal(err)
	}

	files := []string{".yardr/roles/builder.md", ".yardr/roles/mayor.md", ".yardr/roles/merger.md",
		".yardr/roles/reviewer.md", ".yardr/check", ".yardr/flows/default.flow"}
	for _, f := range files {
		if !strings.Contains(out, f) {
			t.Errorf("output does not name %s:\n%s", f, out)
		}
	}
	for _, s := range []string{"Nothing is committed", "origin/main", "go vet ./... && go test ./..."} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
	for _, name := range []string{"mayor.md", "builder.md", "merger.md", "reviewer.md"} {
		tmpl, _ := roles.FS.ReadFile(name)
		got, err := os.ReadFile(filepath.Join(dir, ".yardr", "roles", name))
		if err != nil || !bytes.Equal(got, fillRole(tmpl, "Jo", "boss")) {
			t.Fatalf("%s: %v\n%s", name, err, got)
		}
		if bytes.Contains(got, []byte("{{")) {
			t.Errorf("%s keeps a placeholder:\n%s", name, got)
		}
	}
	check := filepath.Join(dir, ".yardr", "check")
	gate, _ := os.ReadFile(check)
	if !strings.HasPrefix(string(gate), "#!/bin/sh\n# The merge gate of rig web") ||
		!strings.Contains(string(gate), "\ngo vet ./... && go test ./...\n") {
		t.Fatalf("check:\n%s", gate)
	}
	if fi, _ := os.Stat(check); fi.Mode().Perm()&0o100 == 0 {
		t.Fatalf("check is not executable: %v", fi.Mode())
	}
	body, _ := os.ReadFile(filepath.Join(dir, ".yardr", "flows", "default.flow"))
	f, err := store.ParseFlow(string(body))
	if err != nil {
		t.Fatalf("starter flow: %v\n%s", err, body)
	}
	for _, s := range []string{"backlog", "new", "review", "approved", "merged", "decide"} {
		if !slices.Contains(f.Stages, s) {
			t.Errorf("flow has no stage %s: %v", s, f.Stages)
		}
	}

	// Again, with one file edited and --operator: nothing changes.
	if err := os.WriteFile(check, []byte("make test\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	snap := snapshot(t, dir)
	out, err = run(t, a.rigCmd(), "init", "web", "--operator", "Kim")
	if err != nil || !strings.Contains(out, "nothing written") {
		t.Fatalf("second run: %v\n%s", err, out)
	}
	if again := snapshot(t, dir); !maps.Equal(snap, again) {
		t.Fatal("second run changed files")
	}

	if _, err := run(t, a.rigCmd(), "init", "nope"); exitCode(err) != 3 {
		t.Fatalf("unknown rig: %v", err)
	}
}

// --operator names who the roles bring decisions to; without it or the
// setting, the login user.
func TestRigInitOperator(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	t.Setenv("USER", "kim")
	for name, args := range map[string][]string{"kim": nil, "Ana": {"--operator", "Ana"}} {
		dir := t.TempDir()
		if err := st.PutRig(ctx, store.Rig{Name: "r" + strings.ToLower(name), Path: dir, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, (&app{st: st}).rigCmd(), append([]string{"init", "r" + strings.ToLower(name)}, args...)...); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, ".yardr", "roles", "mayor.md")); !bytes.Contains(b, []byte("bring to "+name)) {
			t.Errorf("mayor.md does not name %s", name)
		}
	}
}

// The mayor the roles tell agents to mail is the yard's: --mayor, else the
// mayor setting yardr init stores, else the one resident of a yard from
// before the setting, else init's default name. A yard with several
// residents and no setting does not guess among them.
func TestRigInitMayor(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	mailTo := func(t *testing.T, dir string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, ".yardr", "roles", "builder.md"))
		if err != nil {
			t.Fatal(err)
		}
		_, after, ok := strings.Cut(string(b), "yardr mail ")
		if !ok {
			t.Fatalf("builder.md does not mail the mayor:\n%s", b)
		}
		return strings.Fields(after)[0]
	}
	initRig := func(t *testing.T, name string, args ...string) string {
		t.Helper()
		dir := t.TempDir()
		if err := st.PutRig(ctx, store.Rig{Name: name, Path: dir, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := run(t, (&app{st: st}).rigCmd(), append([]string{"init", name}, args...)...); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	if got := mailTo(t, initRig(t, "empty")); got != "mayor" {
		t.Errorf("empty yard: mail %s, want mayor", got)
	}
	if _, err := st.PutResident(ctx, store.Resident{Name: "yardmaster", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := mailTo(t, initRig(t, "one")); got != "yardmaster" {
		t.Errorf("one resident: mail %s, want yardmaster", got)
	}
	if _, err := st.PutResident(ctx, store.Resident{Name: "scribe", Runner: "manual"}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := mailTo(t, initRig(t, "two")); got != "mayor" {
		t.Errorf("two residents, no setting: mail %s, want mayor", got)
	}
	if _, err := run(t, (&app{st: st}).settingsCmd(), "set", "mayor", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if got := mailTo(t, initRig(t, "set")); got != "yardmaster" {
		t.Errorf("mayor setting: mail %s, want yardmaster", got)
	}
	if got := mailTo(t, initRig(t, "flag", "--mayor", "boss")); got != "boss" {
		t.Errorf("--mayor: mail %s, want boss", got)
	}
	if _, err := run(t, (&app{st: st}).settingsCmd(), "set", "mayor", "no spaces"); err == nil {
		t.Error("settings set mayor accepted a name a resident cannot have")
	}
}

// The gate is guessed from the repo: npm's placeholder test script is none.
func TestGuessGate(t *testing.T) {
	for _, tc := range []struct {
		files map[string]string
		gate  string
	}{
		{map[string]string{"go.mod": "module x\n"}, "go vet ./... && go test ./..."},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`}, "npm test"},
		{map[string]string{"package.json": `{"scripts":{"test":"vitest"}}`, "pnpm-lock.yaml": ""}, "pnpm test"},
		{map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`}, ""},
		{map[string]string{"package.json": `{}`}, ""},
		{nil, ""},
	} {
		dir := t.TempDir()
		for n, b := range tc.files {
			if err := os.WriteFile(filepath.Join(dir, n), []byte(b), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		gate, script := guessGate(store.Rig{Name: "r", Path: dir, Base: "main"})
		if gate != tc.gate {
			t.Errorf("%v: gate %q, want %q", tc.files, gate, tc.gate)
		}
		if gate == "" && (!strings.Contains(script, "TODO") || !strings.HasSuffix(script, "exit 0\n")) {
			t.Errorf("%v: no-guess script:\n%s", tc.files, script)
		}
	}
}

// snapshot reads every file under dir outside .git, with its mode.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	m := map[string]string{}
	err := filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() && fi.Name() == ".git" {
			return filepath.SkipDir
		}
		if !fi.IsDir() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			m[p] = fi.Mode().String() + "\n" + string(b)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}
