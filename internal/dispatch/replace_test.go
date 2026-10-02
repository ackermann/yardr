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
	b.ReadRevision = func(p string) (string, error) {
		data, err := os.ReadFile(p)
		return string(data), err
	}
	b.Exec = func(_ string, _, env []string) error { return exec(env) }
	return b, path
}

func installRev(t *testing.T, path, rev string) {
	t.Helper()
	must(t, os.WriteFile(path+".tmp", []byte(rev), 0o755))
	must(t, os.Rename(path+".tmp", path))
}

func TestDaemonReplacesItselfAfterAPass(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	must(t, s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}))
	_, _ = s.AddRoute(ctx, store.Route{Stage: "new", Group: "g"})
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
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "YARDR_REPLACED_FROM=") {
			t.Fatal("YARDR_REPLACED_FROM left set; agents the daemon starts would inherit it")
		}
	}
}
