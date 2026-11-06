package runner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// yardr-8f5k: a sandbox that writes nowhere refuses the file a shell makes
// for a here-document, so a session whose args name {tmpdir} (the codex
// `read only` preset) gets a directory of its own. It is made beside the
// worktrees before the session's pane, private to the yard's user, named in
// the pane's environment (TMPDIR for programs, TMPPREFIX for zsh) and in the
// agent's arguments, kept on the handle, and removed with the tab when the
// session is stopped. A session whose args do not name it gets none.
func TestHerdrStartMakesTheTmpdirItsArgsNameAndStopRemovesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	variants := []struct {
		name     string
		paneCall string // the request that opens the pane, which carries its environment
		start    func(t *testing.T, h *Herdr, cfg map[string]any) (map[string]any, error)
	}{
		{"a bead in a worktree", "tab.create", func(t *testing.T, h *Herdr, cfg map[string]any) (map[string]any, error) {
			return h.Start(ctx, dispatch.Job{
				Bead:    store.Bead{ID: "yard-13", Stage: "review"},
				Group:   store.Group{Name: "g", Config: cfg},
				Depot:   &store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"},
				Session: store.Session{ID: "s1"},
			})
		}},
		{"a bead of a dir depot", "tab.create", func(t *testing.T, h *Herdr, cfg map[string]any) (map[string]any, error) {
			return h.Start(ctx, dispatch.Job{
				Bead:    store.Bead{ID: "notes-2", Stage: "review"},
				Group:   store.Group{Name: "g", Config: cfg},
				Depot:   &store.Depot{Name: "notes", Path: t.TempDir(), Kind: store.DepotDir},
				Session: store.Session{ID: "s2"},
			})
		}},
		{"a crew member", "workspace.create", func(t *testing.T, h *Herdr, cfg map[string]any) (map[string]any, error) {
			return h.StartCrew(ctx, store.CrewMember{Name: "yardmaster", Dir: t.TempDir(), Config: cfg}, "")
		}},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			h, calls, _ := envHerdr(t, seedEmptyRepo(t))
			h.YardHome, h.WorktreeRoot = t.TempDir(), t.TempDir()
			root, err := filepath.EvalSymlinks(h.WorktreeRoot)
			if err != nil {
				t.Fatal(err)
			}

			// No placeholder in the args: no directory, no variable.
			handle, err := v.start(t, h, map[string]any{"args": []any{"--verbose"}})
			if err != nil {
				t.Fatal(err)
			}
			env := envOf(apiCall(t, readCalls(t, calls), v.paneCall))
			if left, _ := os.ReadDir(root); len(left) != 0 || handle["tmpdir"] != nil || env["TMPDIR"] != "" || env["TMPPREFIX"] != "" {
				t.Errorf("args without %s: handle %v, env %v, %v in the root; want no directory and no variable", TmpdirArg, handle, env, left)
			}

			handle, err = v.start(t, h, map[string]any{"args": []any{"--tmp", TmpdirArg}})
			if err != nil {
				t.Fatal(err)
			}
			tmp, _ := handle["tmpdir"].(string)
			if filepath.Dir(tmp) != root || !strings.HasPrefix(filepath.Base(tmp), tmpdirPrefix) {
				t.Fatalf("tmpdir = %q, want a %s directory in %s", tmp, tmpdirPrefix, root)
			}
			if fi, err := os.Stat(tmp); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
				t.Errorf("%s: %v, %v; want a directory only its owner can use", tmp, fi, err)
			}
			log := readCalls(t, calls)
			if env := envOf(apiCall(t, log, v.paneCall)); env["TMPDIR"] != tmp || env["TMPPREFIX"] != filepath.Join(tmp, "zsh") {
				t.Errorf("%s env has TMPDIR %q and TMPPREFIX %q, want %s and %s", v.paneCall, env["TMPDIR"], env["TMPPREFIX"], tmp, filepath.Join(tmp, "zsh"))
			}
			if cli := cliCalls(log); !strings.Contains(cli, " --tmp "+tmp) || strings.Contains(cli, TmpdirArg) {
				t.Errorf("the agent was not started with the directory in its args:\n%s", cli)
			}

			// A reviewer writes into it while the session runs; Stop takes
			// the tab and then the directory, and can be run again.
			if err := os.WriteFile(filepath.Join(tmp, "zsh1A2b3C"), []byte("note"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := h.Stop(ctx, handle); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(tmp); !os.IsNotExist(err) {
				t.Errorf("after Stop: %v, want the directory gone", err)
			}
			if tab, _ := handle["tab"].(string); !strings.Contains(readCalls(t, calls), "tab close "+tab+"\n") {
				t.Errorf("Stop did not close the tab %q:\n%s", tab, readCalls(t, calls))
			}
			if err := h.Stop(ctx, handle); err != nil {
				t.Errorf("Stop again: %v", err)
			}
			if _, err := os.Stat(root); err != nil {
				t.Errorf("the root went with it: %v", err)
			}
		})
	}
}

// The path goes into a string of the agent's setting (TOML for Codex's
// profile), where a quote ends the string and the rest of the path is a
// setting of its own, so a path that could end it is refused, blocked on
// the operator's configuration, and nothing is left behind. A space is no
// trouble.
func TestSessionTmpRefusesAPathItCannotQuote(t *testing.T) {
	t.Parallel()
	cfg := map[string]any{"args": []any{TmpdirArg}}
	for _, name := range []string{`a"b`, `a\b`, "a\nb"} {
		root := filepath.Join(t.TempDir(), name)
		handle := map[string]any{}
		tmp, err := (&Herdr{WorktreeRoot: root}).sessionTmp(cfg, handle)
		if tmp != "" || !dispatch.IsBlocked(err) || len(handle) != 0 {
			t.Errorf("a root named %q: %q, %v, handle %v; want it blocked", name, tmp, err, handle)
		}
		if left, _ := os.ReadDir(root); len(left) != 0 {
			t.Errorf("a root named %q: left %v behind", name, left)
		}
	}
	handle := map[string]any{}
	tmp, err := (&Herdr{WorktreeRoot: filepath.Join(t.TempDir(), "a b")}).sessionTmp(cfg, handle)
	if err != nil || tmp == "" || handle["tmpdir"] != tmp {
		t.Errorf("a root named \"a b\": %q, %v, handle %v; want a directory", tmp, err, handle)
	}
	// The group's own root wins over the runner's.
	own := t.TempDir()
	if tmp, err := (&Herdr{WorktreeRoot: t.TempDir()}).sessionTmp(map[string]any{"args": []any{TmpdirArg}, "worktrees": own}, map[string]any{}); err != nil || filepath.Dir(tmp) != mustEval(t, own) {
		t.Errorf("a group with its own worktrees root: %q, %v; want a directory in %s", tmp, err, own)
	}
}

// The handle is the store's, and the delete is recursive: Stop removes
// only what sessionTmp made, a clean absolute path whose last element
// begins .tmp-, and a link of that name only itself.
func TestStopRemovesOnlyWhatSessionTmpMade(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, filepath.Join(base, ".tmp-rel"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		made string // the directory that exists
		path string // what the handle names
		gone bool
	}{
		{name: "its own", made: ".tmp-own", path: filepath.Join(base, ".tmp-own"), gone: true},
		{name: "not its name", made: "work", path: filepath.Join(base, "work")},
		{name: "a path that climbs", made: "climbed", path: filepath.Join(base, ".tmp-x", "..", "climbed")},
		{name: "a relative path", made: ".tmp-rel", path: rel},
	} {
		dir := filepath.Join(base, c.made)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := (&Herdr{}).Stop(context.Background(), map[string]any{"tmpdir": c.path}); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
		if _, err := os.Stat(dir); os.IsNotExist(err) != c.gone {
			t.Errorf("%s: %v, want the directory gone=%v", c.name, err, c.gone)
		}
	}

	target := filepath.Join(base, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, ".tmp-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := (&Herdr{}).Stop(context.Background(), map[string]any{"tmpdir": link}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf("the link: %v, want it gone", err)
	}
	if _, err := os.Stat(filepath.Join(target, "f")); err != nil {
		t.Errorf("the link's target lost its file: %v", err)
	}
}

func mustEval(t *testing.T, path string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
