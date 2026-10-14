package dispatch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// fakeBinary watches a "binary" whose contents are its revision; exec
// stands in for syscall.Exec.
func fakeBinary(t *testing.T, rev string, exec func(env []string) error) (*version.Binary, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "yardr")
	installRev(t, path, rev)
	b, err := version.Watch(path, rev)
	if err != nil {
		t.Fatal(err)
	}
	b.ReadBuild = func(p string) (version.Build, error) {
		data, err := os.ReadFile(p)
		return version.Build{Revision: string(data)}, err
	}
	b.Exec = func(_ string, _, env []string) error { return exec(env) }
	return b, path
}

var installed = time.Now()

// installRev installs a build a second after the last one, so mtime
// orders them: only a newer build replaces.
func installRev(t *testing.T, path, rev string) {
	t.Helper()
	must(t, os.WriteFile(path+".tmp", []byte(rev), 0o755))
	installed = installed.Add(time.Second)
	must(t, os.Chtimes(path+".tmp", installed, installed))
	must(t, os.Rename(path+".tmp", path))
}

func TestDaemonReplacesItselfAfterAPass(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")

	var env []string
	var startsAtExec []string
	bin, path := fakeBinary(t, "old", func(e []string) error {
		env, startsAtExec = e, f.starts()
		return nil
	})
	d.Binary = bin
	d.Interval = time.Hour
	installRev(t, path, "new")

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not replace itself")
	}
	if !slices.Equal(startsAtExec, []string{b.ID}) {
		t.Fatalf("starts at exec = %v; the pass must finish first", startsAtExec)
	}
	if !slices.Contains(env, "YARDR_REPLACED_FROM=old") {
		t.Fatal("exec env does not say which build it replaced")
	}
}

func TestDaemonChecksItsBinaryBetweenPasses(t *testing.T) {
	_, _, d := setup(t)
	execd := make(chan struct{})
	bin, path := fakeBinary(t, "old", func([]string) error { close(execd); return nil })
	d.Binary = bin
	d.Interval = time.Hour
	d.CheckBinary = 20 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- d.Run(context.Background()) }()
	time.Sleep(100 * time.Millisecond)

	installRev(t, path, "new")
	select {
	case <-execd:
	case <-time.After(5 * time.Second):
		t.Fatal("an idle daemon never noticed the new build")
	}
	must(t, <-done)
}

func TestDaemonUnchangedOrFailedExecRunsOn(t *testing.T) {
	_, _, d := setup(t)
	var calls atomic.Int32
	bin, path := fakeBinary(t, "old", func([]string) error {
		calls.Add(1)
		return errors.New("exec format error")
	})
	d.Binary = bin
	d.Interval = 10 * time.Millisecond
	d.CheckBinary = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatal("exec'd an unchanged binary")
	}
	installRev(t, path, "new")
	time.Sleep(200 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("daemon stopped after a failed exec: %v", err)
	default:
	}
	if calls.Load() != 1 {
		t.Fatalf("exec calls = %d; a binary that failed must not be retried every pass", calls.Load())
	}
	cancel()
	must(t, <-done)
}

func TestDaemonSkipsAnOlderBuildAndLogsIt(t *testing.T) {
	s, _, d := setup(t)
	var calls atomic.Int32
	bin, path := fakeBinary(t, "new", func([]string) error { calls.Add(1); return nil })
	d.Binary = bin
	d.Interval = 10 * time.Millisecond
	d.CheckBinary = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	head, err := s.Head(ctx)
	must(t, err)

	installRev(t, path, "old")
	past := time.Now().Add(-time.Hour)
	must(t, os.Chtimes(path, past, past))
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	must(t, <-done)
	if calls.Load() != 0 {
		t.Fatal("the daemon exec'd an older build")
	}
	evs, err := s.EventsSince(context.Background(), head, 0)
	must(t, err)
	var skipped []store.Event
	for _, e := range evs {
		if e.Kind == "replace_skipped" {
			skipped = append(skipped, e)
		}
	}
	if len(skipped) != 1 || skipped[0].Data["process"] != "daemon" || skipped[0].Data["candidate"] != "old" {
		t.Fatalf("replace_skipped events = %+v; want one, from the daemon", skipped)
	}
}

func TestReplacedDaemonLogsIt(t *testing.T) {
	s, _, d := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bin, _ := fakeBinary(t, "new", func([]string) error { return nil })
	d.Binary = bin
	d.Interval = time.Hour
	t.Setenv("YARDR_REPLACED_FROM", "old")
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	var got []store.Event
	for deadline := time.Now().Add(3 * time.Second); len(got) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		evs, err := s.EventsSince(ctx, 0, 0)
		must(t, err)
		for _, e := range evs {
			if e.Kind == "daemon_replaced" {
				got = append(got, e)
			}
		}
	}
	cancel()
	must(t, <-done)
	if len(got) != 1 || got[0].Data["from"] != "old" || got[0].Data["to"] != "new" {
		t.Fatalf("daemon_replaced events = %+v", got)
	}
	if started := eventsOf(t, s, "daemon_started"); len(started) != 0 {
		t.Fatalf("daemon_started events after a replace = %+v, want none", started)
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "YARDR_REPLACED_FROM=") {
			t.Fatal("YARDR_REPLACED_FROM left set; agents the daemon starts would inherit it")
		}
	}
}

// A fresh start logs daemon_started with the build it runs, and no
// daemon_replaced.
func TestFreshDaemonLogsStarted(t *testing.T) {
	s, _, d := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bin, _ := fakeBinary(t, "new", func([]string) error { return nil })
	d.Binary = bin
	d.Interval = time.Hour
	t.Setenv("YARDR_REPLACED_FROM", "")
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	var got []store.Event
	for deadline := time.Now().Add(3 * time.Second); len(got) == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		got = eventsOf(t, s, "daemon_started")
	}
	cancel()
	must(t, <-done)
	if len(got) != 1 || got[0].Data["to"] != "new" {
		t.Fatalf("daemon_started events = %+v", got)
	}
	if replaced := eventsOf(t, s, "daemon_replaced"); len(replaced) != 0 {
		t.Fatalf("daemon_replaced events on a fresh start = %+v, want none", replaced)
	}
}

// slowRunner is a fakeRunner whose Start waits to be released, so a test
// can stop the daemon while a pass is running.
type slowRunner struct {
	*fakeRunner
	started, release chan struct{}
}

func (r *slowRunner) Start(ctx context.Context, j dispatch.Job) (map[string]any, error) {
	close(r.started)
	<-r.release
	return r.fakeRunner.Start(ctx, j)
}

// A daemon told to stop while a pass runs stops once the pass is over; it
// does not exec the new build installed meanwhile.
func TestDaemonStoppedDuringAPassDoesNotReplaceItself(t *testing.T) {
	s, f, d := setup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}, "me"))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"}, "me")
	_, _ = s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	slow := &slowRunner{fakeRunner: f, started: make(chan struct{}), release: make(chan struct{})}
	d.Runners = map[string]dispatch.Runner{"fake": slow}
	var execd atomic.Bool
	bin, path := fakeBinary(t, "old", func([]string) error { execd.Store(true); return nil })
	d.Binary = bin
	d.Interval = time.Hour
	installRev(t, path, "new")

	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	<-slow.started
	cancel()
	close(slow.release)
	select {
	case err := <-done:
		must(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
	if execd.Load() {
		t.Fatal("a daemon told to stop exec'd the new build instead")
	}
}
