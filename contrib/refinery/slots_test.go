package refinery

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/rigcheck"
)

// The refinery's gate waits for a slot of the machine like any other, and
// asks to go first. With none free within rig check's bound the gate could
// not run (exit 2): the bead is held where it is, never sent back to new as
// a red gate would be. The script's yardr is wrapped here only to give rig
// check a short bound and to see what it was started with.
func TestMergeLocalNoGateSlotHolds(t *testing.T) {
	y := newYard(t)
	main := y.git(y.repo, "rev-parse", "main")
	y.commit(y.wt, "b.txt", "bead\n")
	ran := filepath.Join(y.home, "ran")
	y.gate(`echo run >> ` + ran)
	slots, first := t.TempDir(), filepath.Join(t.TempDir(), "first")
	release, _, err := rigcheck.Slots{Dir: slots, Limit: 1, Wait: time.Second}.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	y.opts = "YARDR_GATE_SLOTS=1 YARDR_GATE_DIR=" + slots
	y.bin = filepath.Join(t.TempDir(), "yardr")
	wrap := "#!/bin/sh\n[ \"$1 $2\" = \"rig check\" ] && { echo \"first=$YARDR_GATE_FIRST\" > " + first + "; exec " + yardrBin +
		" \"$@\" --slot-wait 300ms; }\nexec " + yardrBin + " \"$@\"\n"
	if err := os.WriteFile(y.bin, []byte(wrap), 0o755); err != nil {
		t.Fatal(err)
	}

	y.refinery()

	b := y.bead()
	if !b.Hold || b.Stage != "approved" {
		t.Fatalf("bead %s hold=%v, want held in approved", b.Stage, b.Hold)
	}
	notes := y.notes()
	for _, want := range []string{"the gate could not run", "1 gate is running on this machine; waiting for a slot", "no gate slot within 300ms"} {
		if !strings.Contains(notes, want) {
			t.Errorf("the hold's note does not say %q:\n%s", want, notes)
		}
	}
	if got, _ := os.ReadFile(first); string(got) != "first=1\n" {
		t.Errorf("rig check was started with %q, want first=1", got)
	}
	if n := runs(t, ran); n != 0 {
		t.Errorf("the gate ran %d times without a slot", n)
	}
	if got := y.git(y.repo, "rev-parse", "main"); got != main {
		t.Error("main moved without a gate")
	}
}
