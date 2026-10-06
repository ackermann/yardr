package rigcheck

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A gate run for a bead is in the yard's home while it runs, with what it
// is, and gone when it has ended.
func TestGatesAreKnownWhileTheyRun(t *testing.T) {
	home := t.TempDir()
	if gates, err := Gates(home); err != nil || len(gates) != 0 {
		t.Fatalf("a home with no gates: %v, %v", gates, err)
	}
	since := time.Now().Add(-6 * time.Minute).Truncate(time.Second)
	full, err := MarkRunning(home, Gate{Bead: "y-1", Depot: "y", Since: since, Dir: "/w/y-1"})
	if err != nil {
		t.Fatal(err)
	}
	light, err := MarkRunning(home, Gate{Bead: "y-2", Depot: "y", Light: true, Since: since.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	gates, err := Gates(home)
	if err != nil || len(gates) != 2 {
		t.Fatalf("gates = %+v, %v", gates, err)
	}
	if g := gates[0]; g.Bead != "y-1" || g.Depot != "y" || g.Light || g.Waiting || !g.Since.Equal(since) || g.Dir != "/w/y-1" || g.Pid != os.Getpid() {
		t.Fatalf("the full gate: %+v", g)
	}
	now := since.Add(6*time.Minute + 20*time.Second)
	if got := gates[0].Show(now); got != "gating 6m (full)" {
		t.Fatalf("Show = %q", got)
	}
	if got := gates[1].Show(now); got != "gating 5m (light)" {
		t.Fatalf("Show = %q", got)
	}

	// Waiting for a slot, and then not: the file is written over, a shorter
	// value over a longer one too.
	full.SetWaiting(true)
	if g, ok := GateOf(mustGates(t, home), "y-1"); !ok || !g.Waiting || g.Show(now) != "waiting 6m (full)" {
		t.Fatalf("waiting: %+v, %v", g, ok)
	}
	full.SetWaiting(false)
	if g, ok := GateOf(mustGates(t, home), "y-1"); !ok || g.Waiting {
		t.Fatalf("no longer waiting: %+v, %v", g, ok)
	}

	full.End()
	if gates := mustGates(t, home); len(gates) != 1 || gates[0].Bead != "y-2" {
		t.Fatalf("after the full gate ended: %+v", gates)
	}
	light.End()
	if entries, _ := os.ReadDir(filepath.Join(home, RunningDir)); len(mustGates(t, home)) != 0 || len(entries) != 0 {
		t.Fatalf("after both ended: %v", entries)
	}
}

// A gate the build before this one started, when a depot was a rig, is
// still running when the yard is upgraded: its file names the depot as that
// build did.
func TestAGateOfTheBuildBeforeNamesItsDepot(t *testing.T) {
	home := t.TempDir()
	run, err := MarkRunning(home, Gate{Bead: "y-1", Depot: "y", Since: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	defer run.End()
	entries, err := os.ReadDir(filepath.Join(home, RunningDir))
	if err != nil || len(entries) != 1 {
		t.Fatalf("the gate's file: %v, %v", entries, err)
	}
	old := `{"bead":"y-1","rig":"y","since":"2026-10-06T12:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, RunningDir, entries[0].Name()), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	if gates := mustGates(t, home); len(gates) != 1 || gates[0].Bead != "y-1" || gates[0].Depot != "y" {
		t.Fatalf("gates = %+v, want the one of depot y", gates)
	}
}

func mustGates(t *testing.T, home string) []Gate {
	t.Helper()
	gates, err := Gates(home)
	if err != nil {
		t.Fatal(err)
	}
	return gates
}

// A depot check that was killed removed nothing, and holds nothing: its file
// is no gate, and whoever reads the directory removes it.
func TestAGateWhoseRunIsGoneIsNoGate(t *testing.T) {
	home := t.TempDir()
	r, err := MarkRunning(home, Gate{Bead: "y-1", Depot: "y", Since: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// What the kernel does when the process ends: the lock goes, the file
	// stays.
	r.f.Close()
	if gates := mustGates(t, home); len(gates) != 0 {
		t.Fatalf("gates = %+v", gates)
	}
	if entries, _ := os.ReadDir(filepath.Join(home, RunningDir)); len(entries) != 0 {
		t.Fatalf("the file was left: %v", entries)
	}
}

// Nothing is recorded without a bead or a home, and the nil record takes
// every call.
func TestNoBeadNoRecord(t *testing.T) {
	home := t.TempDir()
	for _, r := range []struct {
		home string
		g    Gate
	}{{home, Gate{Depot: "y"}}, {"", Gate{Bead: "y-1"}}} {
		got, err := MarkRunning(r.home, r.g)
		if got != nil || err != nil {
			t.Fatalf("MarkRunning(%q, %+v) = %v, %v", r.home, r.g, got, err)
		}
		got.SetWaiting(true)
		got.End()
	}
	if _, err := os.Stat(filepath.Join(home, RunningDir)); !os.IsNotExist(err) {
		t.Fatalf("the directory was made: %v", err)
	}
}

// Of two gates for one bead the one that runs is shown before the one that
// waits for a slot.
func TestGateOfPrefersTheOneRunning(t *testing.T) {
	t0 := time.Now()
	gates := []Gate{{Bead: "a", Waiting: true, Since: t0}, {Bead: "b", Since: t0}, {Bead: "a", Light: true, Since: t0.Add(time.Second)}}
	if g, ok := GateOf(gates, "a"); !ok || g.Waiting || !g.Light {
		t.Fatalf("GateOf = %+v, %v", g, ok)
	}
	if _, ok := GateOf(gates, "c"); ok {
		t.Fatal("a gate for a bead with none")
	}
}

// A run that waits for a slot says so, and says when it has one.
func TestSlotsTellWaiting(t *testing.T) {
	dir := t.TempDir()
	release := take(t, dir, 1)
	told := make(chan bool, 2)
	done := make(chan error, 1)
	go func() {
		rel, _, err := Slots{Dir: dir, Limit: 1, Wait: time.Minute, Waiting: func(w bool) { told <- w }}.Acquire(context.Background())
		if err == nil {
			rel()
		}
		done <- err
	}()
	if w := <-told; !w {
		t.Fatal("told it has a slot before it waited")
	}
	release()
	if w := <-told; w {
		t.Fatal("told it waits when it got the slot")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
