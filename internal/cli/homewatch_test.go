package cli

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// daemonHome is a home with the daemon's lock held, and the context the
// daemon would run under.
func daemonHome(t *testing.T) (*config.Config, context.Context, *syncBuffer) {
	t.Helper()
	home, err := os.MkdirTemp("", "yd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	cfg := &config.Config{Home: home}
	unlock, err := lockDaemon(cfg.DaemonLock(), 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(unlock)
	logged := &syncBuffer{}
	ctx, release, err := whileHomeStands(context.Background(), cfg, 10*time.Millisecond, slog.New(slog.NewTextHandler(logged, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	return cfg, ctx, logged
}

func stopped(t *testing.T, ctx context.Context, logged *syncBuffer, want string) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the daemon was not told to stop")
	}
	// The line is written before the context ends.
	if n := strings.Count(logged.String(), "no longer its own"); n != 1 || !strings.Contains(logged.String(), want) {
		t.Fatalf("want one line saying %q, got:\n%s", want, logged.String())
	}
}

// A daemon whose home was removed under it kept running, like the server
// (yardr-npdr): a detached process no command of the yard could reach.
func TestDaemonStopsWhenItsHomeIsRemoved(t *testing.T) {
	cfg, ctx, logged := daemonHome(t)
	if err := os.RemoveAll(cfg.Home); err != nil {
		t.Fatal(err)
	}
	stopped(t, ctx, logged, "is gone")
}

// The home's name holds another yard now, with a daemon lock of its own.
func TestDaemonStopsWhenItsLockIsAnothers(t *testing.T) {
	cfg, ctx, logged := daemonHome(t)
	// Renamed over, in one step: removed and then written, a check that fell
	// between the two saw no lock and said "is gone" (yardr-5vsd).
	other := cfg.DaemonLock() + ".new"
	if err := os.WriteFile(other, []byte("1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, cfg.DaemonLock()); err != nil {
		t.Fatal(err)
	}
	stopped(t, ctx, logged, "another file")
}

func TestDaemonRunsOnWhileItsHomeStands(t *testing.T) {
	cfg, ctx, logged := daemonHome(t)
	// A second daemon that is refused opens the lock file; nothing is lost.
	if _, err := lockDaemon(cfg.DaemonLock(), 0); err == nil {
		t.Fatal("a second lock was given")
	}
	select {
	case <-ctx.Done():
		t.Fatalf("stopped:\n%s", logged.String())
	case <-time.After(150 * time.Millisecond):
	}
}

// daemonTrial is a `yardr daemon` started on a yard whose yardmaster and
// one bead session run idle in the stand-in for herdr's runner: the home,
// the store, the stand-in, the bead, what the daemon has logged, and the
// texts it typed to its agents. end tells the daemon to stop the way the
// test wants (remove the home, or cancel as a SIGTERM does) and waits for
// it to exit.
type daemonTrial struct {
	home   string
	st     store.API
	panes  *paneRunner
	bead   string
	logged *syncBuffer
	cancel context.CancelFunc
	end    func(func())
	texts  func() []string
}

func daemonUp(t *testing.T) *daemonTrial {
	t.Helper()
	home, st, panes, ids, a, _ := downYard(t,
		map[string]string{"yardmaster": "idle", "conversation": "idle", "busy": "idle"}, "busy")
	ctx := context.Background()
	mustAs(t, st, "Jo", "user", "add", "hal")
	mustAs(t, st, "Jo", "crew", "set", "yardmaster", "for=Jo,hal", "env_for_hal=/tmp/hal.env")
	if err := st.CrewStartedFor(ctx, "yardmaster", "hal", map[string]any{"agent": "conversation", "tab": "w2:t1"}); err != nil {
		t.Fatal(err)
	}
	stub(t, &homeCheck, 10*time.Millisecond)
	stub(t, &daemonRunners, func(string, store.API, *slog.Logger, func(error)) map[string]dispatch.Runner {
		return map[string]dispatch.Runner{"herdr": panes}
	})
	var nudgeMu sync.Mutex
	var nudgeTexts []string
	panes.onNudge = func(agent, text string) {
		nudgeMu.Lock()
		defer nudgeMu.Unlock()
		nudgeTexts = append(nudgeTexts, agent+"\x00"+text)
	}
	texts := func() []string {
		nudgeMu.Lock()
		defer nudgeMu.Unlock()
		return slices.Clone(nudgeTexts)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	app := a(false)
	app.cfg = cfg
	// The daemon logs to os.Stderr: what it says is read back through a
	// pipe, and os.Stderr is itself again before anything is asserted, so
	// a failure is not lost into the pipe.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = old })
	logged := &syncBuffer{}
	read := make(chan struct{})
	go func() { _, _ = io.Copy(logged, r); close(read) }()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	cmd := app.rootCmd()
	cmd.SetArgs([]string{"daemon"})
	cmd.SetContext(ctx)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	done := make(chan error, 1)
	go func() { done <- cmd.Execute() }()
	for deadline := time.Now().Add(10 * time.Second); !strings.Contains(logged.String(), "yardr daemon started"); {
		select {
		case err := <-done:
			t.Fatalf("the daemon ended before it started: %v\n%s", err, logged.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the daemon did not start:\n%s", logged.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	return &daemonTrial{home: home, st: st, panes: panes, bead: ids["busy"], logged: logged, cancel: cancel, texts: texts, end: func(stop func()) {
		t.Helper()
		stop()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("the daemon exited with %v\n%s", err, logged.String())
			}
		case <-time.After(10 * time.Second):
			t.Fatal("the daemon did not exit")
		}
		os.Stderr = old
		w.Close()
		<-read
		r.Close()
	}}
}

// When the daemon's home is removed under it, it closes the herdr sessions
// it is responsible for — the crew member's line and conversation and the
// bead's — with one log line per session, and exits 0. Nothing is asked for
// a handoff and the store is left as it was: there is no yard to write one
// to.
func TestDaemonClosesHerdrSessionsWhenItsHomeIsGone(t *testing.T) {
	y := daemonUp(t)
	y.end(func() {
		if err := os.RemoveAll(y.home); err != nil {
			t.Fatal(err)
		}
	})
	y.panes.mu.Lock()
	stopped := slices.Clone(y.panes.stopped)
	y.panes.mu.Unlock()
	slices.Sort(stopped)
	if !slices.Equal(stopped, []string{"busy", "conversation", "yardmaster"}) {
		t.Fatalf("the daemon closed %v, want the bead's and both of the yardmaster's sessions\n%s", stopped, y.logged.String())
	}
	for _, text := range y.texts() {
		if strings.Contains(text, "handoff") {
			t.Fatalf("the daemon asked for a handoff on the way out: %q", text)
		}
	}
	if n := strings.Count(y.logged.String(), "no longer its own"); n != 1 {
		t.Fatalf("want the one line saying the home is gone, got %d:\n%s", n, y.logged.String())
	}
	if n := strings.Count(y.logged.String(), "closed the herdr session"); n != 3 {
		t.Fatalf("want one line per closed session, got %d:\n%s", n, y.logged.String())
	}
	if out := y.logged.String(); !strings.Contains(out, "crew member yardmaster\"") ||
		!strings.Contains(out, "crew member yardmaster for hal") || !strings.Contains(out, y.bead) {
		t.Fatalf("the lines do not name the line, conversation and bead sessions:\n%s", out)
	}
	ctx := context.Background()
	if r, err := y.st.GetCrew(ctx, "yardmaster"); err != nil || !r.Live() || !r.Session("hal").Live() {
		t.Fatalf("the yardmaster after the daemon left: %+v, %v", r, err)
	}
	sessions, err := y.st.Sessions(ctx, false)
	if err != nil || len(sessions) != 1 || sessions[0].Bead != y.bead {
		t.Fatalf("the bead's session after the daemon left: %+v, %v", sessions, err)
	}
}

// On a SIGTERM the daemon leaves its herdr sessions alone: down handles
// them, or the daemon that starts next takes them over.
func TestDaemonKeepsHerdrSessionsOnStop(t *testing.T) {
	y := daemonUp(t)
	y.end(func() { y.cancel() }) // cancelling the command's context is the SIGTERM
	y.panes.mu.Lock()
	stopped := slices.Clone(y.panes.stopped)
	y.panes.mu.Unlock()
	if len(stopped) != 0 {
		t.Fatalf("a stopped daemon closed %v: it leaves its sessions\n%s", stopped, y.logged.String())
	}
	if out := y.logged.String(); strings.Contains(out, "no longer its own") || strings.Contains(out, "closed the herdr session") {
		t.Fatalf("a stopped daemon wrote a home-gone line:\n%s", out)
	}
}

// A herdr close failure is logged and does not keep the daemon from closing
// the other sessions or exiting 0.
func TestDaemonSkipsHerdrSessionsItCannotClose(t *testing.T) {
	y := daemonUp(t)
	y.panes.mu.Lock()
	y.panes.fail["conversation"] = true
	y.panes.mu.Unlock()
	y.end(func() {
		if err := os.RemoveAll(y.home); err != nil {
			t.Fatal(err)
		}
	})
	y.panes.mu.Lock()
	stopped := slices.Clone(y.panes.stopped)
	y.panes.mu.Unlock()
	slices.Sort(stopped)
	if !slices.Equal(stopped, []string{"busy", "yardmaster"}) {
		t.Fatalf("the daemon closed %v, want it to skip the failed conversation and close the others\n%s", stopped, y.logged.String())
	}
	if out := y.logged.String(); !strings.Contains(out, "could not close the herdr session") ||
		!strings.Contains(out, "crew member yardmaster for hal") || strings.Count(out, "closed the herdr session") != 2 {
		t.Fatalf("the daemon did not log the failed session and the two successful closes:\n%s", out)
	}
}
