package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// yardr-8bhk: a herdr group leaves in the bead's worktree who it started
// there, outside the tracked tree, so a yardr command run in it with
// another session's environment can still tell whose it is. A later stage
// replaces the earlier one's, and a subdirectory is the worktree's too.
func TestHerdrStartRecordsTheWorktreesSession(t *testing.T) {
	ctx := context.Background()
	repo := t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	gitT(t, repo, "commit", "-q", "--allow-empty", "-m", "init")
	wt := filepath.Join(t.TempDir(), "yard-13")
	gitT(t, repo, "worktree", "add", "-q", "-b", "yard/yard-13", wt)

	if ws := SessionOf(ctx, wt); ws != nil {
		t.Fatalf("a worktree no session was started in names %+v", ws)
	}
	log := filepath.Join(t.TempDir(), "calls")
	h := &Herdr{Socket: fakeHerdrAPI(t, log), Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	job := dispatch.Job{
		Bead:    store.Bead{ID: "yard-13", Title: "Shed", Stage: "new"},
		Group:   store.Group{Name: "builders"},
		Depot:   &store.Depot{Name: "yard", Path: repo, Base: "main"},
		Session: store.Session{ID: "s1"},
	}
	if _, err := h.Start(ctx, job); err != nil {
		t.Fatal(err)
	}
	want := WorktreeSession{Bead: "yard-13", Session: "s1", Actor: "yard-13-new", Stage: "new", Group: "builders"}
	if ws := SessionOf(ctx, wt); ws == nil || *ws != want {
		t.Fatalf("the worktree names %+v, want %+v", ws, want)
	}

	job.Bead.Stage, job.Group.Name, job.Session.ID = "review", "reviewers", "s2"
	if _, err := h.Start(ctx, job); err != nil {
		t.Fatal(err)
	}
	want = WorktreeSession{Bead: "yard-13", Session: "s2", Actor: "yard-13-review", Stage: "review", Group: "reviewers"}
	sub := filepath.Join(wt, "internal")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	if ws := SessionOf(ctx, sub); ws == nil || *ws != want {
		t.Fatalf("after the next stage the worktree names %+v, want %+v", ws, want)
	}
	// The depot's own checkout and a plain directory are nobody's.
	for _, dir := range []string{repo, t.TempDir()} {
		if ws := SessionOf(ctx, dir); ws != nil {
			t.Fatalf("%s names %+v", dir, ws)
		}
	}
	gitT(t, wt, "clean", "-fdq")
	if st := gitOutT(t, wt, "status", "--porcelain", "--ignored"); st != "" {
		t.Fatalf("the session file left files in the worktree:\n%s", st)
	}
}

// yardr-8bhk: Codex is started with --no-daemon, bead agent and crew member
// alike, so its commands run under its own pane with its own environment;
// no other kind gets the flag, and a group that passes it gets it once.
// yardr-bcx5: and with its check for a release of itself off, whatever the
// operator's own config says, so that no session upgrades Codex at start.
func TestAgentArgsCodexRunsWithoutItsSharedDaemon(t *testing.T) {
	const noUpdate = "--config=check_for_update_on_startup=false"
	cases := []struct {
		name string
		cfg  map[string]any
		bead string
		want string
	}{
		{"codex", map[string]any{"kind": "codex"}, "", "--no-daemon " + noUpdate},
		{"codex with args and a model", map[string]any{"kind": "codex", "args": []any{"--approve-for-me"}, "model": "gpt-6-luna"}, "",
			"--no-daemon " + noUpdate + " --approve-for-me --model gpt-6-luna"},
		{"codex with a bead's model", map[string]any{"kind": "codex", "model": "gpt-6-luna"}, "gpt-6-astra",
			"--no-daemon " + noUpdate + " --model gpt-6-astra"},
		{"codex already told", map[string]any{"kind": "codex", "args": []any{"--no-daemon", "--approve-for-me"}}, "",
			noUpdate + " --no-daemon --approve-for-me"},
		// yardr-bcx5: a group's own -c (the reviewers' reasoning effort) is
		// another setting's, and does not pass for the update check's.
		{"codex with a -c of its own", map[string]any{"kind": "codex", "args": []any{"-c", "model_reasoning_effort=high"}}, "",
			"--no-daemon " + noUpdate + " -c model_reasoning_effort=high"},
		{"claude", map[string]any{"kind": "claude", "args": []any{"--permission-mode", "auto"}}, "", "--permission-mode auto"},
		{"no kind is claude", map[string]any{}, "", ""},
	}
	for _, c := range cases {
		if got := strings.Join(agentArgs(c.cfg, c.bead, "", ""), " "); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// yardr-7wq2: herdr starts the bare kiro-cli, whose flags are chat's, so
// every Kiro agent runs with the chat subcommand first, before the group's
// flags and the model.
func TestAgentArgsKiroChatsFirst(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]any
		bead string
		want string
	}{
		{"kiro", map[string]any{"kind": "kiro"}, "", "chat"},
		{"kiro with trust and a model", map[string]any{"kind": "kiro", "args": []any{"--trust-tools=*"}, "model": "auto"}, "",
			"chat --trust-tools=* --model auto"},
		{"kiro with a bead's model", map[string]any{"kind": "kiro", "model": "auto"}, "claude-opus-5.5",
			"chat --model claude-opus-5.5"},
		{"kiro already chatting", map[string]any{"kind": "kiro", "args": []any{"chat", "--trust-tools=*"}}, "",
			"chat --trust-tools=*"},
	}
	for _, c := range cases {
		if got := strings.Join(agentArgs(c.cfg, c.bead, "", ""), " "); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

// yardr-d46v: a kind's `read only` preset turns its agent's sandbox on, and
// the sandbox denies every unix socket, the yard's with them, so no yardr
// command of the session got through. The preset names the yard's socket
// as {socket} and the runner puts the path there: the one of its own home,
// symlinks resolved, and only that one. The group's args stay as they were
// written, and a runner without a home changes nothing.
//
// Claude Code's preset also denies the session's workdir for writing, and
// its sandbox took "." there for some other directory: a touch in the
// workdir went through. So the preset names it as {workdir}, which the
// runner fills the same way.
func TestAgentArgsNameTheYardsSocket(t *testing.T) {
	home := filepath.Join(t.TempDir(), "yard")
	real := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, home); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(real, "yard.sock")
	// The workdir is reached through a symlink too.
	workdir := filepath.Join(home, "worktree")
	if err := os.Mkdir(workdir, 0o755); err != nil {
		t.Fatal(err)
	}
	realWorkdir := filepath.Join(real, "worktree")

	preset := func(kind string) []any {
		h, _ := agents.Lookup(kind)
		for _, p := range h.Flags.Permissions {
			if p.Label == "read only" {
				args := make([]any, len(p.Args))
				for i, a := range p.Args {
					args[i] = a
				}
				return args
			}
		}
		t.Fatalf("%s has no read only preset", kind)
		return nil
	}
	for _, kind := range []string{"claude", "codex"} {
		written := preset(kind)
		cfg := map[string]any{"kind": kind, "args": written}
		got := agentArgs(cfg, "", home, workdir)
		joined := strings.Join(got, " ")
		if strings.Contains(joined, WorkdirArg) {
			t.Errorf("%s: %q, want no %s", kind, joined, WorkdirArg)
		}
		if strings.Contains(joined, SocketArg) || strings.Count(joined, socket) != 1 || strings.Count(joined, "yard.sock") != 1 {
			t.Errorf("%s: %q, want the socket %s once and no %s", kind, joined, socket, SocketArg)
		}
		if !strings.Contains(fmt.Sprint(written), SocketArg) {
			t.Errorf("%s: the group's own args were changed: %v", kind, written)
		}
		if bare := strings.Join(agentArgs(cfg, "", "", ""), " "); !strings.Contains(bare, SocketArg) {
			t.Errorf("%s with no home: %q, want %s left", kind, bare, SocketArg)
		}
		switch kind {
		case "claude":
			// The settings are still JSON, the yard's socket is the one
			// socket they allow, and the workdir, by its absolute path,
			// the one directory they deny for writing.
			var settings struct {
				Sandbox struct {
					Enabled    bool
					Filesystem struct{ DenyWrite []string }
					Network    struct{ AllowUnixSockets []string }
				}
			}
			if len(got) != 2 || got[0] != "--settings" || json.Unmarshal([]byte(got[1]), &settings) != nil ||
				!settings.Sandbox.Enabled || len(settings.Sandbox.Network.AllowUnixSockets) != 1 ||
				settings.Sandbox.Network.AllowUnixSockets[0] != socket ||
				len(settings.Sandbox.Filesystem.DenyWrite) != 1 || settings.Sandbox.Filesystem.DenyWrite[0] != realWorkdir {
				t.Errorf("claude: %q, want the socket %s allowed and %s denied for writing", got, socket, realWorkdir)
			}
			if bare := strings.Join(agentArgs(cfg, "", home, ""), " "); !strings.Contains(bare, WorkdirArg) {
				t.Errorf("claude with no workdir: %q, want %s left", bare, WorkdirArg)
			}
		case "codex":
			if want := `permissions.yardr_read_only.network.unix_sockets={"` + socket + `"="allow"}`; !strings.Contains(joined, "-c "+want) ||
				!strings.Contains(joined, "--enable network_proxy -c ") {
				t.Errorf("codex: %q, want %s behind the network proxy", joined, want)
			}
		}
	}
	// An argument of a group's own takes it too, whatever the kind.
	if got := agentArgs(map[string]any{"kind": "cursor", "args": []any{"--socket=" + SocketArg, "-v", "--dir", WorkdirArg}}, "", home, workdir); strings.Join(got, " ") != "--socket="+socket+" -v --dir "+realWorkdir {
		t.Errorf("a group's own argument: %q", got)
	}
}
