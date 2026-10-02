package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// rig check exits 1 for a red gate and 2 when it could not run, and a red
// gate always prints something, whatever --tail says.
func TestRigCheckExitsAndTail(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	put := func(check string) {
		t.Helper()
		if err := st.PutRig(ctx, store.Rig{Name: "r", Path: t.TempDir(), Base: "main", Check: check}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	put("printf 'one\\ntwo\\nthree\\n'; exit 4")
	out, err := run(t, a.rigCheckCmd(), "r", "--tail", "0")
	if exitCode(err) != 1 || !strings.Contains(out, "3 lines of output not shown") || !strings.Contains(out, "FAIL (exit 4)") {
		t.Fatalf("red gate, --tail 0: %q, %v (exit %d)", out, err, exitCode(err))
	}
	if out, _ = run(t, a.rigCheckCmd(), "r", "--tail", "1"); !strings.Contains(out, "three") || strings.Contains(out, "two") {
		t.Fatalf("red gate, --tail 1: %q", out)
	}

	put("exit 1")
	if out, err = run(t, a.rigCheckCmd(), "r"); exitCode(err) != 1 || !strings.Contains(out, "(the check printed nothing)") {
		t.Fatalf("silent red gate: %q, %v", out, err)
	}

	put("sleep 30")
	if out, err = run(t, a.rigCheckCmd(), "r", "--timeout", "200ms"); exitCode(err) != 1 || !strings.Contains(out, "timed out after 200ms") {
		t.Fatalf("timed out gate: %q, %v", out, err)
	}

	put("true")
	if out, err = run(t, a.rigCheckCmd(), "r"); exitCode(err) != 0 || !strings.Contains(out, ": pass") {
		t.Fatalf("green gate: %q, %v", out, err)
	}

	// Could not run: an unknown rig, a base that does not resolve.
	if _, err = run(t, a.rigCheckCmd(), "nope"); exitCode(err) != 2 {
		t.Fatalf("unknown rig: %v (exit %d), want 2", err, exitCode(err))
	}
	if err := st.PutRig(ctx, store.Rig{Name: "broken", Path: t.TempDir(), Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err = run(t, a.rigCheckCmd(), "broken"); exitCode(err) != 2 {
		t.Fatalf("rig that is not a repo: %v (exit %d), want 2", err, exitCode(err))
	}
}
