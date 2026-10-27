package runner

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// psNice is the niceness ps says pid runs at: what an operator sees, which
// niceOf is held to.
func psNice(t *testing.T, pid int) int {
	t.Helper()
	out, err := exec.Command("ps", "-o", "nice=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		t.Fatalf("ps -o nice= -p %d: %v", pid, err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("ps -o nice= -p %d said %q", pid, out)
	}
	return n
}

// paneShell is a stand-in for the shell of a herdr pane, run under nice by
// extra as a herdr started at a niceness would have it: a process that has
// started nothing yet, whose pid a test gives the fake herdr as the pane's
// shell. It is handed out once it has run a command of its own. A renice
// that lands in the few milliseconds after a process starts is undone by
// the exec it is finishing (seen on macOS: lost in half the tries 2 ms after
// the start, never after 10), and a wrapper such as nice has not set its
// niceness before then either. yardr asks for the pid of a pane's shell
// long after herdr started it, but a test that starts its own shell and
// uses it at once is not. release lets the shell go on to start a child,
// which says its niceness, and returns that once the shell has ended.
func paneShell(t *testing.T, extra int) (pid int, release func() int) {
	t.Helper()
	dir := t.TempDir()
	out, up := filepath.Join(dir, "child"), filepath.Join(dir, "up")
	argv := []string{"sh", "-c", `echo up >"$1"; read go; sh -c 'ps -o nice= -p $$' >"$0"`, out, up}
	if extra > 0 {
		argv = append([]string{"nice", "-n", strconv.Itoa(extra)}, argv...)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	stdin, err := cmd.StdinPipe()
	mustT(t, err)
	mustT(t, cmd.Start())
	ended := make(chan struct{})
	go func() { _ = cmd.Wait(); close(ended) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-ended })
	waitFile(t, up)
	return cmd.Process.Pid, func() int {
		t.Helper()
		_, err := io.WriteString(stdin, "go\n")
		mustT(t, err)
		<-ended
		n, err := strconv.Atoi(strings.TrimSpace(string(mustRead(t, out))))
		if err != nil {
			t.Fatalf("the shell's child said %q (%v)", mustRead(t, out), err)
		}
		return n
	}
}

// The shell of a pane is set to the niceness the group asks for before the
// agent is started in it, and what the shell starts then carries it
// (yardr-7a36). One that is that nice already is left as it is: a process
// cannot lower its own niceness, and the group asked for no more than that.
// A group without the key asks nothing. Every niceness is told from this
// process's own, which the gate may itself have been started at.
func TestNiceShell(t *testing.T) {
	t.Parallel()
	own := psNice(t, os.Getpid())
	for _, c := range []struct {
		name   string
		extra  int                 // the shell starts this much nicer than this process
		ask    func(shell int) int // what the group asks for, from the niceness the shell starts at
		raised bool                // the shell ends at what is asked, and not where it was
	}{
		{"raised to what is asked", 0, func(shell int) int { return shell + 5 }, true},
		{"nicer than asked already", 15, func(shell int) int { return shell - 5 }, false},
		{"asked for the niceness it has", 0, func(shell int) int { return shell }, false},
		{"no niceness asked", 0, func(int) int { return 0 }, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.raised && own+5 > store.MaxNice {
				t.Skipf("no niceness a group can ask for is nicer than %d", own)
			}
			pid, release := paneShell(t, c.extra)
			// What nice(1) made of the shell's niceness is the system's to
			// say (macOS goes to 20 where Linux stops at 19), so it is read.
			before := psNice(t, pid)
			if got, err := niceOf(pid); err != nil || got != before {
				t.Fatalf("niceOf(%d) = %d, %v; ps says %d", pid, got, err, before)
			}
			want, after := c.ask(before), before
			if c.raised {
				after = want
			}
			if err := niceShell(map[string]any{"shell_pid": pid}, want); err != nil {
				t.Fatal(err)
			}
			if got := psNice(t, pid); got != after {
				t.Errorf("shell at niceness %d after asking for %d, want %d (it was %d)", got, want, after, before)
			}
			if got := release(); got != after {
				t.Errorf("a child of the shell is at niceness %d, want its shell's %d", got, after)
			}
		})
	}

	// A pane whose shell herdr did not name has none to set; a group that
	// asks nothing needs none.
	if err := niceShell(map[string]any{}, 5); err == nil || !strings.Contains(err.Error(), "did not report the pane shell") {
		t.Errorf("niceShell with no shell = %v, want it said that herdr did not report one", err)
	}
	if err := niceShell(map[string]any{}, 0); err != nil {
		t.Errorf("niceShell with no shell and no niceness asked = %v, want nothing", err)
	}
}

// A command is run under nice only when the niceness asked for is above the
// one this process has, which the command would start with, and by the
// difference: nice(1) adds to it. The shell goes to nice by its path.
func TestUnderNiceWrapsByTheDifference(t *testing.T) {
	t.Parallel()
	nice, err := exec.LookPath("nice")
	if err != nil {
		t.Skip("no nice on this machine's PATH")
	}
	own, err := niceOf(os.Getpid())
	mustT(t, err)
	for _, c := range []struct {
		name   string
		want   int
		nicer  int // by how much nice is told to raise it; 0: the command is left alone
		absent bool
	}{
		{"none asked", 0, 0, false},
		{"as nice as this process", own, 0, false},
		{"nicer than this process", own + 3, 3, own+3 > store.MaxNice},
		{"less nice than this process", own - 1, 0, own == 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.absent {
				t.Skipf("no such niceness above %d", own)
			}
			cmd := exec.Command("sh", "-c", "true")
			sh := cmd.Path
			mustT(t, underNice(cmd, c.want))
			if c.nicer == 0 {
				if cmd.Path != sh || strings.Join(cmd.Args, " ") != "sh -c true" {
					t.Errorf("command = %s %v, want it left as it was", cmd.Path, cmd.Args)
				}
				return
			}
			want := []string{"nice", "-n", strconv.Itoa(c.nicer), sh, "-c", "true"}
			if cmd.Path != nice || strings.Join(cmd.Args, "\x00") != strings.Join(want, "\x00") {
				t.Errorf("command = %s %q, want %s %q", cmd.Path, cmd.Args, nice, want)
			}
		})
	}
}

// A group's nice runs its command, and everything the command starts, at that
// niceness from its first instruction (yardr-7a36): the shell and a child of
// it both say so. A group without the key, or with 0, starts as it did. A
// renice of the shell after its start would miss what it forks at once;
// that is why the command is started under nice.
func TestExecRunsAtTheGroupsNiceness(t *testing.T) {
	t.Parallel()
	own := psNice(t, os.Getpid())
	raised := min(own+5, store.MaxNice)
	for _, c := range []struct {
		name string
		cfg  map[string]any
		want int
		skip bool
	}{
		{"asked", map[string]any{"nice": raised}, raised, raised == own},
		{"not set", nil, own, false},
		{"zero", map[string]any{"nice": 0}, own, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.skip {
				t.Skipf("nothing to show at niceness %d", own)
			}
			said := filepath.Join(t.TempDir(), "said")
			cfg := maps.Clone(c.cfg)
			if cfg == nil {
				cfg = map[string]any{}
			}
			cfg["cmd"] = `{ ps -o nice= -p $$; sh -c 'ps -o nice= -p $$'; } >` + said
			h, err := (&Exec{Home: filepath.Join(t.TempDir(), "home"), noReap: true}).Start(context.Background(), dispatch.Job{
				Bead:    store.Bead{ID: "b-1"},
				Session: store.Session{ID: "s-1"},
				Group:   store.Group{Name: "tick", Runner: "exec", Config: cfg},
			})
			mustT(t, err)
			pid, err := pidOf(h)
			mustT(t, err)
			_, _ = syscall.Wait4(pid, nil, 0, nil) // the command has said what it saw
			want := strconv.Itoa(c.want)
			if got := strings.Fields(string(mustRead(t, said))); len(got) != 2 || got[0] != want || got[1] != want {
				t.Errorf("the command and its child were at niceness %v, want %s for both", got, want)
			}
		})
	}
}

// A herdr group's nice reaches the shell of the pane the agent is started in,
// and is there when herdr is asked to start the agent: the agent and all it
// starts descend from that shell (yardr-7a36). The fake herdr says the
// shell's niceness as it is asked to. Start and startDir both end in the
// brief that does this, so one of them stands for both.
func TestHerdrStartNicesTheShellBeforeTheAgent(t *testing.T) {
	t.Parallel()
	own := psNice(t, os.Getpid())
	raised := min(own+5, store.MaxNice)
	for _, c := range []struct {
		name string
		cfg  map[string]any
		want int
		skip bool
	}{
		{"asked", map[string]any{"nice": raised}, raised, raised == own},
		{"not set", nil, own, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if c.skip {
				t.Skipf("nothing to show at niceness %d", own)
			}
			wt := seedEmptyRepo(t)
			log := filepath.Join(t.TempDir(), "calls")
			shell, release := paneShell(t, 0)
			h := &Herdr{YardHome: t.TempDir(), YardBin: "/bin/yardr", Socket: fakeHerdrAPI(t, log), Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+log+`
case "$1 $2" in
  "worktree create") echo '{"result":{"workspace":{"workspace_id":"w1"},"tab":{"tab_id":"t1"},"root_pane":{"pane_id":"p1"},"worktree":{"path":"`+wt+`"}}}' ;;
  "pane process-info") echo '{"result":{"process_info":{"pane_id":"p2","shell_pid":`+fmt.Sprint(shell)+`}}}' ;;
  "agent start") echo the shell is at niceness $(ps -o nice= -p `+fmt.Sprint(shell)+`) >> `+log+`; echo '{"result":{"type":"ok"}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
			job := dispatch.Job{
				Bead:    store.Bead{ID: "yard-13", Stage: "new"},
				Group:   store.Group{Name: "yard-builders", Config: c.cfg},
				Depot:   &store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"},
				Session: store.Session{ID: "s1"},
			}
			if _, err := h.Start(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			calls := strings.Split(string(mustRead(t, log)), "\n")
			start := slices.IndexFunc(calls, func(l string) bool { return strings.HasPrefix(l, "agent start yard-13-new ") })
			if want := fmt.Sprintf("the shell is at niceness %d", c.want); start < 0 || calls[start+1] != want {
				t.Errorf("herdr was not asked to start the agent with its shell at niceness %d:\n%s", c.want, strings.Join(calls, "\n"))
			}
			if got := release(); got != c.want {
				t.Errorf("a child of the shell is at niceness %d, want %d", got, c.want)
			}
		})
	}
}

// A group that asks for a niceness needs nice on the daemon's PATH: without
// it no session of the group can start, and that is the runtime's failure
// and not the bead's (the group backs off), as a missing sh is. A group that
// asks for none looks for no nice.
func TestExecNiceNeedsNice(t *testing.T) {
	// Serial: sets PATH.
	own := psNice(t, os.Getpid()) // before PATH is changed: it runs ps
	if own == store.MaxNice {
		t.Skipf("nothing to show at niceness %d", own)
	}
	sh, err := exec.LookPath("sh")
	mustT(t, err)
	bin := t.TempDir()
	mustT(t, os.Symlink(sh, filepath.Join(bin, "sh")))
	t.Setenv("PATH", bin)
	start := func(cfg map[string]any) error {
		cfg["cmd"], cfg["log"] = "exit 0", filepath.Join(t.TempDir(), "log")
		h, err := (&Exec{noReap: true}).Start(context.Background(), dispatch.Job{
			Bead:    store.Bead{ID: "b-1"},
			Session: store.Session{ID: "s-1"},
			Group:   store.Group{Name: "tick", Runner: "exec", Config: cfg},
		})
		if err == nil {
			pid, perr := pidOf(h)
			mustT(t, perr)
			_, _ = syscall.Wait4(pid, nil, 0, nil)
		}
		return err
	}
	if err := start(map[string]any{"nice": 0}); err != nil {
		t.Errorf("a group that asks for no niceness looked for nice: %v", err)
	}
	if err := start(map[string]any{"nice": own + 1}); err == nil || !dispatch.IsRuntime(err) || !strings.Contains(err.Error(), `"nice"`) {
		t.Errorf("a group that asks for a niceness, with no nice to run its command under, started: %v; want the runtime's failure naming nice", err)
	}
}
