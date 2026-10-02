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
	if err := st.PutRig(ctx, store.Rig{Name: "yard"}, "me"); err != nil {
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
	if err := st.PutRig(ctx, store.Rig{Name: "yard", Path: t.TempDir(), Base: "main"}, "me"); err != nil {
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

// dep add -k parent adds the child's blocks edge to its epic too, and says
// so; dep rm -k parent takes both away.
func TestDepAddParentPairsBlocks(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutRig(ctx, store.Rig{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-e", Title: "epic", Type: store.TypeEpic, Rig: "yard"},
		{ID: "yard-1", Title: "child", Rig: "yard"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.depCmd(), "add", "yard-e", "yard-1", "-k", "parent")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-e -parent-> yard-1\nyard-1 -blocks-> yard-e\n"; out != want {
		t.Fatalf("dep add printed %q, want %q", out, want)
	}
	if bs, _ := st.OpenBlockers(ctx, "yard-e"); len(bs) != 1 || bs[0] != "yard-1" {
		t.Fatalf("epic blocked by %v, want yard-1", bs)
	}
	if _, err := run(t, a.depCmd(), "rm", "yard-e", "yard-1", "-k", "parent"); err != nil {
		t.Fatal(err)
	}
	if ds, _ := st.Deps(ctx, "yard-e"); len(ds) != 0 {
		t.Fatalf("edges left after dep rm -k parent: %+v", ds)
	}
}
