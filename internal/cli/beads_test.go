package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// run executes c with args, returning its stdout and error.
func run(t *testing.T, c *cobra.Command, args ...string) (string, error) {
	t.Helper()
	c.SetArgs(args)
	c.SilenceUsage, c.SilenceErrors = true, true
	var err error
	out := capture(t, func() { err = c.Execute() })
	return out, err
}

// bead update refuses a pin naming no flow, rather than store it and fail
// later, when dispatch or a move resolves it.
func TestBeadUpdateRejectsUnknownPin(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutRig(ctx, store.Rig{Name: "yard"}); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("new -> review\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "code"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	if _, err := run(t, a.beadCmd(), "update", b.ID, "--meta", "flow=ghost"); err == nil || !strings.Contains(err.Error(), "pins flow ghost") {
		t.Fatalf("update --meta flow=ghost: %v", err)
	}
	if got, _ := st.GetBead(ctx, b.ID); store.PinnedFlow(got) != "" || got.Revision != b.Revision {
		t.Fatalf("refused pin changed the bead: %+v", got)
	}
	// Create refuses it too.
	if _, err := run(t, a.beadCmd(), "create", "y", "--rig", "yard", "--meta", "flow=ghost"); err == nil || !strings.Contains(err.Error(), "pins flow ghost") {
		t.Fatalf("create --meta flow=ghost: %v", err)
	}
	if _, err := run(t, a.beadCmd(), "update", b.ID, "--meta", "flow=code"); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.GetBead(ctx, b.ID); store.PinnedFlow(got) != "code" {
		t.Fatalf("pin = %q, want code", store.PinnedFlow(got))
	}
}

// show, prime and review still work for a bead whose flow does not
// resolve, and say why next to it.
func TestBrokenFlowStillShown(t *testing.T) {
	st, ctx := reviewYard(t, "")
	b, err := st.CreateBead(ctx, store.NewBead{Title: "stuck", Rig: "yard", Stage: "decide"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// The rig's pointer goes; its checkout is no git repository, so its
	// repo flows cannot be read.
	if err := st.SetFlowPointer(ctx, "yard", "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutRig(ctx, store.Rig{Name: "yard", Path: t.TempDir(), Base: "main"}); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "mckean"}

	out, err := run(t, a.beadCmd(), "show", b.ID)
	if err != nil || !strings.Contains(out, "stuck") || !strings.Contains(out, "flow error:") || !strings.Contains(out, "not a git repository") {
		t.Fatalf("bead show = %q, %v", out, err)
	}
	out, err = run(t, a.primeCmd(), "--bead", b.ID)
	if err != nil || !strings.Contains(out, "flow error:") || !strings.Contains(out, "OUTCOMES: none until the flow is fixed") {
		t.Fatalf("prime = %q, %v", out, err)
	}
	items, err := reviewQueue(ctx, st, "mckean", nil)
	if err != nil || len(items) != 1 || !strings.Contains(items[0].FlowError, "not a git repository") {
		t.Fatalf("review queue = %+v, %v", items, err)
	}
	for _, ac := range items[0].Actions {
		if ac.Kind != actDiff && ac.Kind != actSkip {
			t.Fatalf("broken flow offers %+v", ac)
		}
	}
}
