package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The folders Codex's config trusts: a table per folder with trust_level
// "trusted", in either kind of string, and nothing else.
func TestCodexTrusted(t *testing.T) {
	config := `model = "gpt-5"
[projects."/a/b"]
trust_level = "trusted"

[projects.'/c d/e/']   # a literal string
trust_level = 'trusted' # so is this

[projects."/no"]
trust_level = "untrusted"

[projects."/left"]
[tui]
trust_level = "trusted"

[ projects . "/q\"uote" ]
trust_level="trusted"

[projects."/commented"]
# trust_level = "trusted"
`
	want := []string{"/a/b", "/c d/e", `/q"uote`}
	if got := codexTrusted(config); !slices.Equal(got, want) {
		t.Fatalf("trusted = %q, want %q", got, want)
	}
	if got := codexTrusted(""); len(got) != 0 {
		t.Fatalf("of no config: %q", got)
	}
}

// Doctor notes a depot that a route sends to a group of kind codex while
// Codex's config does not trust its root, with the lines that trust it, and
// says nothing of one that is trusted or that no codex group works
// (yardr-ecsn; footplate-rn6b sat on "Trust this folder?").
func TestDoctorCodexTrust(t *testing.T) {
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers, "codex": toolAnswers})
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := run(t, (&app{st: st, actor: "me"}).rootCmd(), "init", "--harness", "claude"); err != nil {
		t.Fatal(err)
	}
	web := gitDepot(t, st)
	api := store.Depot{Name: "api", Path: t.TempDir(), Base: "main", Kind: store.DepotGit}
	if err := st.PutDepot(ctx, api, "me"); err != nil {
		t.Fatal(err)
	}
	for name, kind := range map[string]string{"builders": "codex", "reviewers": "claude"} {
		if err := st.PutGroup(ctx, store.Group{Name: name, Runner: "herdr", Config: map[string]any{"kind": kind}}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	route := func(stage string, depot *string, group string) {
		t.Helper()
		if _, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: depot, Group: group}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	route("new", &web.Name, "builders")
	route("new", &api.Name, "reviewers")

	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	file := filepath.Join(codexHome, "config.toml")
	root := func(d store.Depot) string {
		t.Helper()
		p, err := filepath.EvalSymlinks(d.Path)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	write := func(config string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	notes := func() []string {
		t.Helper()
		rep, _ := doctorJSON(t, st)
		for _, f := range rep.Faults {
			if strings.Contains(f, "Codex") {
				t.Errorf("an untrusted depot is a fault: %s", f)
			}
		}
		return rep.State.CodexTrust
	}
	line := func(d store.Depot) string {
		return "note   codex       depot " + d.Name + " (" + d.Path + ") is not trusted by Codex, and group builders runs on it"
	}
	add := func(d store.Depot) string {
		return "Add to " + file + " the two lines `[projects." + strconv.Quote(root(d)) + "]` and `trust_level = \"trusted\"`"
	}

	// No config at all: the depot the codex group works, and not the other.
	out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor")
	wantText(t, out, line(web), add(web))
	if strings.Contains(out, "depot api (") {
		t.Errorf("a depot no codex group works is noted:\n%s", out)
	}
	if got := notes(); len(got) != 1 {
		t.Fatalf("notes = %q", got)
	}

	// Another folder trusted, and this one named as not: still noted.
	write("[projects.\"/elsewhere\"]\ntrust_level = \"trusted\"\n\n[projects." + strconv.Quote(root(web)) + "]\ntrust_level = \"untrusted\"\n")
	if got := notes(); len(got) != 1 || !strings.Contains(got[0], "depot web (") {
		t.Fatalf("with another folder trusted: notes = %q", got)
	}

	// Trusted: nothing.
	write("[projects." + strconv.Quote(root(web)) + "]\ntrust_level = \"trusted\"\n")
	if got := notes(); len(got) != 0 {
		t.Fatalf("trusted: notes = %q", got)
	}
	if out, _ := run(t, (&app{st: st, actor: "me"}).rootCmd(), "doctor"); strings.Contains(out, "codex       depot") {
		t.Errorf("a trusted depot is noted:\n%s", out)
	}

	// A route that names no depot sends every depot's beads to the group.
	route("review", nil, "builders")
	got := notes()
	if len(got) != 1 || !strings.Contains(got[0], "depot api (") || !strings.Contains(got[0], add(api)) {
		t.Fatalf("with a route for every depot: notes = %q", got)
	}

	// A config that cannot be read is said, once, and no depot is called
	// untrusted on it.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := notes(); len(got) != 1 || !strings.HasPrefix(got[0], "whether Codex trusts the depots its groups work is not known: ") {
		t.Fatalf("unreadable config: notes = %q", got)
	}

	// No codex group: Codex's config is not looked at.
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := notes(); len(got) != 0 {
		t.Fatalf("no codex group: notes = %q", got)
	}
}
