package assembly

// A gate ended by a signal from outside (depot check exits 128+N) is not a
// red gate: no builder can fix a signal. merge-local and integrate-train run
// it once more on the same commit, and hold the bead if a signal ends it
// again. Seen as exit 143 after every package had printed ok (yardr-z0bw).

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
)

// gate makes check depot r's gate.
func (y *yard) gate(check string) {
	y.t.Helper()
	st := rpc.NewClient((&config.Config{Home: y.home}).Socket())
	if err := st.PutDepot(context.Background(), store.Depot{Name: "r", Path: y.repo, Base: "main", Check: check}, "test"); err != nil {
		y.t.Fatal(err)
	}
}

// runs is how many times a gate that appends a line to file has run.
func runs(t *testing.T, file string) int {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(b), "\n")
}

// The first run is killed by SIGTERM, the second passes: the bead lands as
// usual, and a note keeps the first run on record with its output.
func TestMergeLocalGateEndedBySignalIsRunAgain(t *testing.T) {
	y := newYard(t)
	tip := y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.gate(`echo run >> ` + ran + `; [ "$(wc -l < ` + ran + `)" -gt 1 ] || { echo "ok every package"; kill -TERM $$; }`)

	y.assembly()

	if b := y.bead(); b.Stage != "merged" || b.Status != "closed" || b.Hold {
		t.Fatalf("bead %s/%s hold=%v, want merged and closed", b.Stage, b.Status, b.Hold)
	}
	if main := y.git(y.repo, "rev-parse", "main"); main != tip {
		t.Fatalf("main %s, want the gated commit %s", main, tip)
	}
	if n := runs(t, ran); n != 2 {
		t.Fatalf("the gate ran %d times, want 2", n)
	}
	notes := y.notes()
	for _, want := range []string{"at " + tip[:7] + " was ended by signal 15 (exit 143), not by a failing check",
		"running it once more on the same commit", "ok every package", "merged " + tip[:7]} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
}

// Ended by a signal both times (here the way a shell reports a killed
// command of its script, exit 143): held where it is with both outputs,
// never sent back to new, and not run a third time.
func TestMergeLocalGateEndedBySignalTwiceHolds(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.gate(`echo run >> ` + ran + `; echo "output of run $(wc -l < ` + ran + ` | tr -d ' ')"; exit 143`)

	y.assembly()

	b := y.bead()
	if !b.Hold || b.Stage != "approved" || b.Status != "open" {
		t.Fatalf("bead %s/%s hold=%v, want held in approved", b.Stage, b.Status, b.Hold)
	}
	if n := runs(t, ran); n != 2 {
		t.Fatalf("the gate ran %d times, want 2", n)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Fatal("main moved on a gate ended by a signal")
	}
	notes := y.notes()
	for _, want := range []string{"was ended by signal 15 (exit 143)", "and again by signal 15 (exit 143) when run once more",
		"First run:\n", "output of run 1", "Second run:\n", "output of run 2"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "the gate failed") {
		t.Fatalf("a gate ended by a signal was reported as red:\n%s", notes)
	}
}

// The second run decides as any run does: red goes back to new, with the
// first run's note still on the bead.
func TestMergeLocalGateEndedBySignalThenRed(t *testing.T) {
	y := newYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.gate(`echo run >> ` + ran + `; [ "$(wc -l < ` + ran + `)" -gt 1 ] || exit 143; echo "really red"; exit 1`)

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	notes := y.notes()
	for _, want := range []string{"was ended by signal 15 (exit 143)", "the gate failed", "really red"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
}

// depot check's own kill at its deadline is not a signal from outside: it
// exits 1, so the gate is red, goes back to new and is not run again. The
// script's yardr is wrapped here only to give depot check a short deadline.
func TestMergeLocalGateDeadlineIsRed(t *testing.T) {
	y := newYard(t)
	y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.gate(`echo run >> ` + ran + `; sleep 30`)
	y.bin = filepath.Join(t.TempDir(), "yardr")
	wrap := "#!/bin/sh\n[ \"$1 $2\" = \"depot check\" ] && exec " + yardrBin + " \"$@\" --timeout 300ms\nexec " + yardrBin + " \"$@\"\n"
	if err := os.WriteFile(y.bin, []byte(wrap), 0o755); err != nil {
		t.Fatal(err)
	}

	y.assembly()

	if b := y.bead(); b.Stage != "new" || b.Hold {
		t.Fatalf("bead %s hold=%v, want back in new", b.Stage, b.Hold)
	}
	if n := runs(t, ran); n != 1 {
		t.Fatalf("the gate ran %d times, want 1", n)
	}
	notes := y.notes()
	if !strings.Contains(notes, "the gate failed") || !strings.Contains(notes, "ran past its deadline and was killed") ||
		strings.Contains(notes, "ended by signal") {
		t.Fatalf("the deadline is not reported as a red gate:\n%s", notes)
	}
}

// trainSignalYard is newTrainYard with both children landed, and a gate that
// from then on is ended by SIGTERM: the first times times, 0 for always.
func trainSignalYard(t *testing.T, times int) (y *yard, ran string) {
	t.Helper()
	y = newTrainYard(t, "true")
	y.exec("merge-local", "assembly", "r-1")
	y.exec("merge-local", "assembly", "r-2")
	ran = filepath.Join(y.home, "ran")
	check := `echo run >> ` + ran + `; echo "output of run $(wc -l < ` + ran + ` | tr -d ' ')"; `
	if times > 0 {
		check += `[ "$(wc -l < ` + ran + `)" -gt ` + string(rune('0'+times)) + ` ] || `
	}
	y.gate(check + `kill -TERM $$`)
	return y, ran
}

// integrate-train runs the gate the same way: ended by a signal once, the
// train goes to review on the second run, with the first on record.
func TestTrainGateEndedBySignalIsRunAgain(t *testing.T) {
	y, ran := trainSignalYard(t, 1)

	y.exec("integrate-train", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if e.Stage != "review" || e.Status != "open" || e.Hold {
		t.Fatalf("train %s/%s hold=%v, want review", e.Stage, e.Status, e.Hold)
	}
	if n := runs(t, ran); n != 2 {
		t.Fatalf("the gate ran %d times, want 2", n)
	}
	notes := y.yardr("prime", "--bead", "r-e")
	for _, want := range []string{"was ended by signal 15 (exit 143), not by a failing check", "output of run 1", "gate passed"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
}

// Ended by a signal twice, the train is held in open with both outputs, and
// no child is added to fix a gate that was never red.
func TestTrainGateEndedBySignalTwiceHolds(t *testing.T) {
	y, ran := trainSignalYard(t, 0)

	y.exec("integrate-train", "yard-integrators", "r-e")

	e := y.beadOf("r-e")
	if !e.Hold || e.Stage != "open" || e.Status != "open" {
		t.Fatalf("train %s/%s hold=%v, want held in open", e.Stage, e.Status, e.Hold)
	}
	if n := runs(t, ran); n != 2 {
		t.Fatalf("the gate ran %d times, want 2", n)
	}
	if f := y.familyOf("r-e"); len(f.Children) != 2 {
		t.Fatalf("train children %v, want only r-1 and r-2", f.Children)
	}
	notes := y.yardr("prime", "--bead", "r-e")
	for _, want := range []string{"and again by signal 15 (exit 143) when run once more", "First run:\n", "output of run 1", "Second run:\n", "output of run 2"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes lack %q:\n%s", want, notes)
		}
	}
}
