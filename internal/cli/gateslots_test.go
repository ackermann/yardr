package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/rigcheck"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// gateSlots gives the test slots of its own, with the yard's setting in
// force, and returns their directory. The suite runs with no limit (see
// TestMain), and inside a gate whose mark would make a rig check here take
// no slot.
func gateSlots(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("YARDR_GATE_DIR", dir)
	t.Setenv("YARDR_GATE_SLOTS", "")
	t.Setenv("YARDR_GATE_FIRST", "")
	t.Setenv(rigcheck.SlotVar, "")
	return dir
}

// holdSlot takes a slot as a running gate does, until the test ends.
func holdSlot(t *testing.T, dir string, limit int) {
	t.Helper()
	release, _, err := rigcheck.Slots{Dir: dir, Limit: limit, Wait: time.Second}.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
}

// rig check waits for a slot by the yard's gate_slots, says so on stderr,
// and exits 2 when none comes free within --slot-wait: it could not run,
// which a merge script holds on, where exit 1 would send the bead back.
func TestRigCheckGateSlots(t *testing.T) {
	dir := gateSlots(t)
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	if err := st.PutRig(ctx, store.Rig{Name: "r", Path: t.TempDir(), Base: "main", Check: "echo ran", CheckLight: "echo light ran"}, "me"); err != nil {
		t.Fatal(err)
	}
	// The default is two.
	holdSlot(t, dir, 2)
	if out, err := run(t, a.rigCheckCmd(), "r", "--slot-wait", "100ms"); err != nil || !strings.Contains(out, "ran\n") {
		t.Fatalf("one of two slots taken: %q, %v", out, err)
	}
	holdSlot(t, dir, 2)
	errs := captureStderr(t)
	out, err := run(t, a.rigCheckCmd(), "r", "--slot-wait", "100ms")
	if exitCode(err) != 2 || strings.Contains(out, "ran") || !strings.Contains(err.Error(), "no gate slot within 100ms") {
		t.Fatalf("both slots taken: %q, %v (exit %d), want 2", out, err, exitCode(err))
	}
	if got := errs.String(); got != "yardr: 2 gates are running on this machine; waiting for a slot\n" {
		t.Fatalf("stderr = %q", got)
	}
	// --json keeps stdout to itself while it waits.
	a.json = true
	if out, err = run(t, a.rigCheckCmd(), "r", "--slot-wait", "100ms"); exitCode(err) != 2 || out != "" {
		t.Fatalf("--json, both slots taken: %q, %v", out, err)
	}
	a.json = false
	// The light gate takes none.
	if out, err = run(t, a.rigCheckCmd(), "r", "--light", "--slot-wait", "100ms"); err != nil || !strings.Contains(out, "light ran\n") {
		t.Fatalf("light gate, both slots taken: %q, %v", out, err)
	}

	// The setting is the yard's: three, none, and what it refuses.
	if _, err = run(t, a.settingsCmd(), "set", "gate_slots", "3"); err != nil {
		t.Fatal(err)
	}
	a.json = true
	if out, err = run(t, a.rigCheckCmd(), "r", "--slot-wait", "100ms"); err != nil {
		t.Fatalf("gate_slots 3, two taken: %q, %v", out, err)
	}
	a.json = false
	var res rigcheck.Result
	if err := json.Unmarshal([]byte(out), &res); err != nil || !res.Pass || res.SlotWaited != 0 {
		t.Fatalf("json: %+v, %v\n%s", res, err, out)
	}
	holdSlot(t, dir, 3)
	if _, err = run(t, a.rigCheckCmd(), "r", "--slot-wait", "100ms"); exitCode(err) != 2 {
		t.Fatalf("gate_slots 3, three taken: %v (exit %d), want 2", err, exitCode(err))
	}
	if _, err = run(t, a.settingsCmd(), "set", "gate_slots", "0"); err != nil {
		t.Fatal(err)
	}
	if out, err = run(t, a.rigCheckCmd(), "r", "--slot-wait", "100ms"); err != nil || !strings.Contains(out, "ran\n") {
		t.Fatalf("gate_slots 0: %q, %v", out, err)
	}
	for _, bad := range []string{"two", "1.5"} {
		if _, err = run(t, a.settingsCmd(), "set", "gate_slots", bad); err == nil || !strings.Contains(err.Error(), "0 for no limit") {
			t.Errorf("settings set gate_slots %s: %v", bad, err)
		}
	}
	// The environment, when set, is used instead of the setting.
	t.Setenv("YARDR_GATE_SLOTS", "1")
	if _, err = run(t, a.rigCheckCmd(), "r", "--slot-wait", "100ms"); exitCode(err) != 2 {
		t.Fatalf("YARDR_GATE_SLOTS=1 over gate_slots 0: %v (exit %d), want 2", err, exitCode(err))
	}
	if _, err = run(t, a.rigCheckCmd(), "r", "--slot-wait", "0s"); exitCode(err) != 2 || !strings.Contains(err.Error(), "--slot-wait must be positive") {
		t.Fatalf("--slot-wait 0s: %v", err)
	}

	// rig add --light-slot: the rig's light gate takes a slot, and rig add
	// says so.
	out, err = run(t, a.rigCmd(), "add", "d", t.TempDir(), "--kind", "dir", "--check", "exit 4", "--check-light", "echo light ran", "--light-slot")
	if err != nil || !strings.Contains(out, "light_slot: the light gate takes a gate slot") {
		t.Fatalf("rig add --light-slot: %q, %v", out, err)
	}
	if _, err = run(t, a.rigCheckCmd(), "d", "--light", "--slot-wait", "100ms"); exitCode(err) != 2 {
		t.Fatalf("light gate of a rig with --light-slot, every slot taken: %v (exit %d), want 2", err, exitCode(err))
	}
}

// yardr status counts the machine's gates when the yard has a limit and
// any runs or waits, and has no line for them otherwise.
func TestStatusGates(t *testing.T) {
	dir := gateSlots(t)
	_, _, a := statusYard(t, "abcdef123456")
	if out, err := run(t, a().rootCmd(), "status"); err != nil || strings.Contains(out, "gates") {
		t.Fatalf("no gate runs: %v\n%s", err, out)
	}
	holdSlot(t, dir, 2)
	holdSlot(t, dir, 2)
	waiting := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _, err := rigcheck.Slots{Dir: dir, Limit: 2, Wait: time.Minute}.Acquire(ctx)
		waiting <- err
	}()
	t.Cleanup(func() { cancel(); <-waiting })
	for i := 0; ; i++ {
		if c, _ := rigcheck.Count(dir); c.Waiting == 1 {
			break
		} else if i > 500 {
			t.Fatalf("the third never waited: %+v", c)
		}
		time.Sleep(10 * time.Millisecond)
	}
	out, err := run(t, a().rootCmd(), "status")
	if err != nil || !strings.Contains(out, "\ngates      2 running, 1 waiting (limit 2)\n") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if g := statusJSON(t, a()).Gates; g == nil || g.Running != 2 || g.Waiting != 1 || g.Limit != 2 {
		t.Fatalf("json: %+v", g)
	}
	// No limit, no line: the yard does not go by the slots.
	t.Setenv("YARDR_GATE_SLOTS", "0")
	if out, err := run(t, a().rootCmd(), "status"); err != nil || strings.Contains(out, "gates") {
		t.Fatalf("no limit: %v\n%s", err, out)
	}
}
