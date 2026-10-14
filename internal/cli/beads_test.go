package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// run executes c with args, returning its stdout and error.
func run(t *testing.T, c *cobra.Command, args ...string) (string, error) {
	t.Helper()
	// Never nil: with no arguments set, cobra runs the binary's own
	// (os.Args), and what a command does must not depend on how the test
	// binary was started.
	c.SetArgs(append([]string{}, args...))
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
	if err := st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"); err != nil {
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
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard"}, "me")
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
	if _, err := run(t, a.beadCmd(), "create", "y", "--depot", "yard", "--meta", "flow=ghost"); err == nil || !strings.Contains(err.Error(), "pins flow ghost") {
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
	b, err := st.CreateBead(ctx, store.NewBead{Title: "stuck", Depot: "yard", Stage: "decide"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	// The depot's pointer goes; its checkout is no git repository, so its
	// repo flows cannot be read.
	if err := st.SetFlowPointer(ctx, "yard", "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Path: t.TempDir(), Base: "main"}, "me"); err != nil {
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

// dep add -k parent adds the child's blocks edge to its train too, and says
// so; dep rm -k parent takes both away.
func TestDepAddParentPairsBlocks(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-e", Title: "train", Type: store.TypeTrain, Depot: "yard"},
		{ID: "yard-1", Title: "child", Depot: "yard"},
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
		t.Fatalf("train blocked by %v, want yard-1", bs)
	}
	if _, err := run(t, a.depCmd(), "rm", "yard-e", "yard-1", "-k", "parent"); err != nil {
		t.Fatal(err)
	}
	if ds, _ := st.Deps(ctx, "yard-e"); len(ds) != 0 {
		t.Fatalf("edges left after dep rm -k parent: %+v", ds)
	}
}

// dep add --at stages the edge and says so; dep rm --at removes exactly
// that edge, and without it a staged edge stays. The stage must be one of
// the target's flow, and --at is refused for any other kind.
func TestDepAddAtStagesTheEdge(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("backlog -> new\nnew -> review\nreview -> approved\napproved -> merged\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "yard", "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-1", Title: "one", Depot: "yard", Stage: "new"},
		{ID: "yard-2", Title: "two", Depot: "yard", Stage: "new"},
		{ID: "yard-3", Title: "three", Depot: "yard", Stage: "new"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.depCmd(), "add", "yard-1", "yard-2", "--at", "approved")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 -blocks-> yard-2 at approved\n"; out != want {
		t.Fatalf("dep add --at printed %q, want %q", out, want)
	}
	// The plain echo is unchanged.
	out, err = run(t, a.depCmd(), "add", "yard-1", "yard-3")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 -blocks-> yard-3\n"; out != want {
		t.Fatalf("dep add printed %q, want %q", out, want)
	}
	if _, err := run(t, a.depCmd(), "add", "yard-2", "yard-3", "--at", "launch"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("dep add --at launch: err = %v", err)
	}
	if _, err := run(t, a.depCmd(), "add", "yard-2", "yard-3", "-k", "related", "--at", "approved"); err == nil ||
		!strings.Contains(err.Error(), "--at") {
		t.Fatalf("dep add -k related --at: err = %v", err)
	}
	// --json carries the stage.
	j := &app{st: st, actor: "me", json: true}
	out, err = run(t, j.depCmd(), "add", "yard-2", "yard-3", "--at", "approved")
	if err != nil {
		t.Fatal(err)
	}
	var got store.Dep
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.At != "approved" {
		t.Fatalf("dep add --json --at: %q (%v)", out, err)
	}
	// rm without --at leaves a staged edge alone; with it, removes exactly it.
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-2"); err != nil {
		t.Fatal(err)
	}
	staged := func() []store.Dep {
		t.Helper()
		ds, err := st.Deps(ctx, "yard-2")
		if err != nil {
			t.Fatal(err)
		}
		return slices.DeleteFunc(ds, func(d store.Dep) bool { return d.From != "yard-1" })
	}
	if ds := staged(); len(ds) != 1 || ds[0].At != "approved" {
		t.Fatalf("yard-1's edges after dep rm without --at: %+v", ds)
	}
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-2", "--at", "approved"); err != nil {
		t.Fatal(err)
	}
	if ds := staged(); len(ds) != 0 {
		t.Fatalf("yard-1's edges after dep rm --at approved: %+v", ds)
	}
}

// dep add --until stages the edge and says so, alone and with --at; dep rm
// --until removes exactly that edge, and without it a staged edge stays.
// The stage must be one of the blocker's flow, and --until is refused for
// any other kind.
func TestDepAddUntilStagesTheEdge(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow("backlog -> new\nnew -> review\nreview -> approved\napproved -> merged\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yard", "", "yard", "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-1", Title: "one", Depot: "yard", Stage: "new"},
		{ID: "yard-2", Title: "two", Depot: "yard", Stage: "new"},
		{ID: "yard-3", Title: "three", Depot: "yard", Stage: "new"},
		{ID: "yard-4", Title: "four", Depot: "yard", Stage: "new"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.depCmd(), "add", "yard-1", "yard-2", "--until", "review")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 -blocks-> yard-2 until review\n"; out != want {
		t.Fatalf("dep add --until printed %q, want %q", out, want)
	}
	out, err = run(t, a.depCmd(), "add", "yard-1", "yard-3", "--at", "approved", "--until", "review")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-1 -blocks-> yard-3 at approved until review\n"; out != want {
		t.Fatalf("dep add --at --until printed %q, want %q", out, want)
	}
	// The plain echo is unchanged.
	out, err = run(t, a.depCmd(), "add", "yard-2", "yard-3")
	if err != nil {
		t.Fatal(err)
	}
	if want := "yard-2 -blocks-> yard-3\n"; out != want {
		t.Fatalf("dep add printed %q, want %q", out, want)
	}
	if _, err := run(t, a.depCmd(), "add", "yard-2", "yard-4", "--until", "launch"); err == nil ||
		!strings.Contains(err.Error(), "has no such stage") {
		t.Fatalf("dep add --until launch: err = %v", err)
	}
	if _, err := run(t, a.depCmd(), "add", "yard-2", "yard-4", "-k", "related", "--until", "review"); err == nil ||
		!strings.Contains(err.Error(), "--until") {
		t.Fatalf("dep add -k related --until: err = %v", err)
	}
	// --json carries the stage.
	j := &app{st: st, actor: "me", json: true}
	out, err = run(t, j.depCmd(), "add", "yard-2", "yard-4", "--until", "review")
	if err != nil {
		t.Fatal(err)
	}
	var got store.Dep
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Until != "review" {
		t.Fatalf("dep add --json --until: %q (%v)", out, err)
	}
	// rm without --until leaves a staged edge alone; with it, removes exactly it.
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-2"); err != nil {
		t.Fatal(err)
	}
	staged := func() []store.Dep {
		t.Helper()
		ds, err := st.Deps(ctx, "yard-2")
		if err != nil {
			t.Fatal(err)
		}
		return slices.DeleteFunc(ds, func(d store.Dep) bool { return d.From != "yard-1" })
	}
	if ds := staged(); len(ds) != 1 || ds[0].Until != "review" {
		t.Fatalf("yard-1's edges after dep rm without --until: %+v", ds)
	}
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-2", "--until", "review"); err != nil {
		t.Fatal(err)
	}
	if ds := staged(); len(ds) != 0 {
		t.Fatalf("yard-1's edges after dep rm --until review: %+v", ds)
	}
	// rm --at alone leaves an edge that also names until.
	if _, err := run(t, a.depCmd(), "rm", "yard-1", "yard-3", "--at", "approved"); err != nil {
		t.Fatal(err)
	}
	ds, err := st.Deps(ctx, "yard-3")
	if err != nil {
		t.Fatal(err)
	}
	ds = slices.DeleteFunc(ds, func(d store.Dep) bool { return d.From != "yard-1" })
	if len(ds) != 1 || ds[0].At != "approved" || ds[0].Until != "review" {
		t.Fatalf("yard-1's edges after dep rm --at approved: %+v", ds)
	}
}

// bead list, ready and session list show each bead's train in a TRAIN
// column after the bead, and in --json; bead list --train lists one train's
// family; bead show --json still carries the train.
func TestListingsShowTheTrain(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, nb := range []store.NewBead{
		{ID: "yard-e", Title: "train", Type: store.TypeTrain, Depot: "yard"},
		{ID: "yard-1", Title: "child", Depot: "yard"},
		{ID: "yard-2", Title: "loose", Depot: "yard"},
	} {
		if _, err := st.CreateBead(ctx, nb, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddDep(ctx, "yard-e", "yard-1", store.DepParent, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"yard-1", "yard-2"} {
		if _, err := st.Claim(ctx, id, "builders", "new", "yard"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}

	// fields are the whitespace-separated cells of out's row for id.
	fields := func(out, id string) []string {
		for _, l := range strings.Split(out, "\n") {
			if f := strings.Fields(l); len(f) > 1 && slices.Contains(f[:2], id) {
				return f
			}
		}
		t.Fatalf("no row for %s in:\n%s", id, out)
		return nil
	}
	out, err := run(t, a.beadCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	if h := strings.Fields(out); !slices.Equal(h[:3], []string{"ID", "TRAIN", "PRI"}) {
		t.Fatalf("bead list header:\n%s", out)
	}
	if f := fields(out, "yard-1"); f[1] != "yard-e" {
		t.Fatalf("bead list child row %v, want train yard-e second", f)
	}
	if f := fields(out, "yard-2"); f[1] != "P0" {
		t.Fatalf("bead list plain row %v, want an empty train", f)
	}
	out, err = run(t, a.readyCmd())
	if err != nil {
		t.Fatal(err)
	}
	if f := fields(out, "yard-1"); f[1] != "yard-e" {
		t.Fatalf("ready child row %v", f)
	}
	out, err = run(t, a.sessionCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	if h := strings.Fields(out); !slices.Equal(h[:4], []string{"ID", "BEAD", "TRAIN", "STAGE"}) {
		t.Fatalf("session list header:\n%s", out)
	}
	if f := fields(out, "yard-1"); f[2] != "yard-e" {
		t.Fatalf("session list child row %v", f)
	}
	if f := fields(out, "yard-2"); f[2] != "new" {
		t.Fatalf("session list plain row %v, want an empty train", f)
	}

	out, err = run(t, a.beadCmd(), "list", "--train", "yard-e")
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
		ids = append(ids, strings.Fields(l)[0])
	}
	if !slices.Equal(ids, []string{"yard-e", "yard-1"}) {
		t.Fatalf("bead list --train yard-e = %v:\n%s", ids, out)
	}

	a.json = true
	for _, c := range []struct {
		cmd  *cobra.Command
		args []string
	}{{a.beadCmd(), []string{"list"}}, {a.readyCmd(), nil}, {a.sessionCmd(), []string{"list"}}} {
		out, err := run(t, c.cmd, c.args...)
		if err != nil {
			t.Fatal(err)
		}
		var rows []struct{ ID, Bead, Train string }
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("%s: %v\n%s", c.args, err, out)
		}
		got := map[string]string{}
		for _, r := range rows {
			got[r.ID+r.Bead] = r.Train // sessions: ID is the session's, so key by both
		}
		n := 0
		for k, v := range got {
			if strings.HasSuffix(k, "yard-1") || k == "yard-1" {
				n++
				if v != "yard-e" {
					t.Fatalf("%s --json: yard-1 train %q:\n%s", c.args, v, out)
				}
			} else if v != "" {
				t.Fatalf("%s --json: %s has train %q", c.args, k, v)
			}
		}
		if n != 1 {
			t.Fatalf("%s --json: no yard-1 row:\n%s", c.args, out)
		}
	}
	out, err = run(t, a.beadCmd(), "show", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	var shown struct{ ID, Train, Base, Revision string }
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.Train != "yard-e" || shown.Base != "yard/yard-e" || shown.Revision == "" {
		t.Fatalf("bead show --json = %+v, %v:\n%s", shown, err, out)
	}
}

// bead create --discovered-from records where a bead was found: bead show
// says so on both beads, in text and JSON, and bead list --discovered-from
// lists what a bead led to. An unknown origin creates nothing.
func TestBeadDiscoveredFrom(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-1", Title: "the origin", Depot: "yard"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	create := func(title string) string {
		t.Helper()
		out, err := run(t, a.beadCmd(), "create", title, "--discovered-from", "yard-1")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(out)
	}
	open, shut := create("a loose end"), create("a fixed end")
	if _, err := st.CloseBead(ctx, shut, "fixed", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	// Created like any bead: no depot, labels or priority of the origin's.
	if b, _ := st.GetBead(ctx, open); b == nil || b.Depot != nil || b.Priority != 2 {
		t.Fatalf("the found bead: %+v", b)
	}

	out, err := run(t, a.beadCmd(), "show", open)
	if err != nil {
		t.Fatal(err)
	}
	if want := "  discovered from yard-1 the origin\n"; !strings.Contains(out, want) {
		t.Fatalf("bead show of the found bead lacks %q:\n%s", want, out)
	}
	out, err = run(t, a.beadCmd(), "show", "yard-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := "  led to: " + open + " a loose end\n  led to: " + shut + " a fixed end (closed)\n"; !strings.Contains(out, want) {
		t.Fatalf("bead show of the origin lacks %q:\n%s", want, out)
	}

	listed := func(args ...string) []string {
		t.Helper()
		out, err := run(t, a.beadCmd(), append([]string{"list", "--discovered-from", "yard-1"}, args...)...)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, l := range strings.Split(strings.TrimSpace(out), "\n")[1:] {
			ids = append(ids, strings.Fields(l)[0])
		}
		return ids
	}
	if got := listed(); !slices.Equal(got, []string{open}) {
		t.Fatalf("bead list --discovered-from yard-1 = %v, want %s", got, open)
	}
	if got := listed("-a"); !slices.Equal(got, []string{open, shut}) {
		t.Fatalf("bead list -a --discovered-from yard-1 = %v, want both", got)
	}

	before, _ := st.ListBeads(ctx, store.BeadFilter{All: true})
	if _, err := run(t, a.beadCmd(), "create", "an orphan", "--discovered-from", "yard-nope"); err == nil {
		t.Fatal("an unknown origin was accepted")
	}
	if after, _ := st.ListBeads(ctx, store.BeadFilter{All: true}); len(after) != len(before) {
		t.Fatalf("a refused create left a bead: %d, were %d", len(after), len(before))
	}

	a.json = true
	type refs struct {
		ID             string
		DiscoveredFrom []store.BeadRef `json:"discovered_from"`
		LedTo          []store.BeadRef `json:"led_to"`
	}
	show := func(id string) refs {
		t.Helper()
		out, err := run(t, a.beadCmd(), "show", id)
		if err != nil {
			t.Fatal(err)
		}
		var r refs
		if err := json.Unmarshal([]byte(out), &r); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		// Both lists are there, empty when there is nothing to list.
		if r.ID != id || r.DiscoveredFrom == nil || r.LedTo == nil {
			t.Fatalf("bead show --json %s:\n%s", id, out)
		}
		return r
	}
	if r := show(open); !slices.Equal(r.DiscoveredFrom, []store.BeadRef{{ID: "yard-1", Title: "the origin", Status: "open"}}) || len(r.LedTo) != 0 {
		t.Fatalf("the found bead as JSON: %+v", r)
	}
	if r := show("yard-1"); len(r.DiscoveredFrom) != 0 || !slices.Equal(r.LedTo, []store.BeadRef{
		{ID: open, Title: "a loose end", Status: "open"}, {ID: shut, Title: "a fixed end", Status: "closed"}}) {
		t.Fatalf("the origin as JSON: %+v", r)
	}
}

// oldServer is a store whose server is from before discovered-from and
// NewBead.BlockedBy: it knows neither Discovery nor CheckBlockers, and its
// CreateBead would drop the origin and the blockers and create the bead
// without its edges.
type oldServer struct {
	store.API
	created int
}

func (o *oldServer) Discovery(context.Context, string) (*store.Discovery, error) {
	return nil, errors.New("no such method Discovery")
}

func (o *oldServer) CheckBlockers(context.Context, []string) error {
	return errors.New("no such method CheckBlockers")
}

func (o *oldServer) CreateBead(ctx context.Context, nb store.NewBead, actor string) (*store.Bead, error) {
	o.created++
	nb.DiscoveredFrom, nb.BlockedBy = "", nil
	return o.API.CreateBead(ctx, nb, actor)
}

// Against a server from before the flag, bead create --discovered-from
// creates nothing rather than a bead without its edge.
func TestDiscoveredFromRefusesAnOlderServer(t *testing.T) {
	st := &oldServer{API: storetest.New(t)}
	ctx := context.Background()
	if _, err := st.API.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the origin"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.beadCmd(), "create", "found", "--discovered-from", "yd-1"); err == nil || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("create against an older server: %v, want its refusal", err)
	}
	if st.created != 0 {
		t.Fatal("the bead was created without its edge")
	}
	// Without the flag an older server is asked nothing new.
	if _, err := run(t, a.beadCmd(), "create", "plain"); err != nil || st.created != 1 {
		t.Fatalf("a plain create: %v, %d created", err, st.created)
	}
}

// bead create --blocked-by makes the bead with its blockers or not at all:
// an unknown blocker leaves no bead for bead list and ready to show, and
// several blockers are all there when the bead is.
func TestBeadCreateBlockedBy(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	for _, id := range []string{"yd-1", "yd-2"} {
		if _, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: "a blocker"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	a := &app{st: st, actor: "me"}
	head, _ := st.Head(ctx)
	for _, blockers := range []string{"yd-nope", "yd-1,yd-nope", "yd-1,yd-1"} {
		if out, err := run(t, a.beadCmd(), "create", "x", "--blocked-by", blockers); err == nil || !strings.Contains(err.Error(), "blocked by: ") {
			t.Fatalf("create --blocked-by %s: %v\n%s", blockers, err, out)
		}
		for name, listing := range map[string]*cobra.Command{"bead list": a.beadCmd(), "ready": a.readyCmd()} {
			out, err := run(t, listing, strings.Fields(name)[1:]...)
			if err != nil || strings.Contains(out, " x") {
				t.Fatalf("after a refused --blocked-by %s, %s: %v\n%s", blockers, name, err, out)
			}
		}
		if now, _ := st.Head(ctx); now != head {
			t.Fatalf("a refused --blocked-by %s logged %d events", blockers, now-head)
		}
	}
	out, err := run(t, a.beadCmd(), "create", "x", "--id", "yd-3", "--blocked-by", "yd-1", "--blocked-by", "yd-2")
	if err != nil || strings.TrimSpace(out) != "yd-3" {
		t.Fatalf("create with two blockers: %v\n%s", err, out)
	}
	bs, _ := st.OpenBlockers(ctx, "yd-3")
	slices.Sort(bs)
	if !slices.Equal(bs, []string{"yd-1", "yd-2"}) {
		t.Fatalf("yd-3 is blocked by %v", bs)
	}
	if out, err := run(t, a.readyCmd()); err != nil || strings.Contains(out, "yd-3") {
		t.Fatalf("ready lists the blocked bead: %v\n%s", err, out)
	}
}

// bead create refuses an empty title and one of only spaces, and creates
// nothing; bead update refuses to set one the same way (yardr-0fbd).
func TestBeadRefusesAnEmptyTitle(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "titled"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	head, _ := st.Head(ctx)
	for _, title := range []string{"", "   "} {
		if out, err := run(t, a.beadCmd(), "create", title); err == nil || !strings.Contains(err.Error(), "a bead needs a title") {
			t.Fatalf("create %q: %v\n%s", title, err, out)
		}
		if out, err := run(t, a.beadCmd(), "update", b.ID, "--title", title); err == nil || !strings.Contains(err.Error(), "a bead needs a title") {
			t.Fatalf("update --title %q: %v\n%s", title, err, out)
		}
	}
	if now, _ := st.Head(ctx); now != head {
		t.Fatalf("refused titles logged %d events", now-head)
	}
	if got, _ := st.GetBead(ctx, b.ID); got.Title != "titled" || got.Revision != b.Revision {
		t.Fatalf("refused update changed the bead: %+v", got)
	}
}

// Against a server from before NewBead.BlockedBy, bead create --blocked-by
// creates nothing rather than a bead a route could hand out unblocked.
func TestBlockedByRefusesAnOlderServer(t *testing.T) {
	st := &oldServer{API: storetest.New(t)}
	ctx := context.Background()
	if _, err := st.API.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "a blocker"}, "me"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	if _, err := run(t, a.beadCmd(), "create", "blocked", "--blocked-by", "yd-1"); err == nil || !strings.Contains(err.Error(), "no such method") {
		t.Fatalf("create against an older server: %v, want its refusal", err)
	}
	if st.created != 0 {
		t.Fatal("the bead was created without its blockers")
	}
}

// bead show prints the bead's notes after the body, oldest first, each
// with its author and time and the text as written; --json carries them
// under notes, the key the web view's bead page uses. A bead with none
// prints as before. bead note says what it wrote: the bead and the note's
// seq.
func TestBeadShowPrintsNotes(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed", Body: "two coats"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-2", Title: "the fence", Body: "one coat"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNote(ctx, "yd-1", "the hinge is missing", "builder"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddNote(ctx, "yd-1", "found one\nin the shed", "passer"); err != nil {
		t.Fatal(err)
	}
	notes, err := st.Notes(ctx, "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}

	out, err := run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	at := strings.Index(out, "\nNotes:\n")
	if at < 0 || !strings.Contains(out, "two coats") || strings.Index(out, "two coats") > at {
		t.Fatalf("the notes are not after the body:\n%s", out)
	}
	var heads []int
	for _, n := range notes {
		head := fmt.Sprintf("  %s %s (note %d)\n", n.Author, n.At.Local().Format("2006-01-02 15:04:05"), n.Seq)
		headAt := strings.Index(out, head)
		if headAt < 0 {
			t.Fatalf("no %q:\n%s", strings.TrimSpace(head), out)
		}
		if len(heads) > 0 && headAt <= heads[len(heads)-1] {
			t.Fatalf("the note headers are not oldest first:\n%s", out)
		}
		heads = append(heads, headAt)
	}
	// Each text follows its header and precedes the next note's header.
	for i, snippet := range []string{"the hinge is missing", "found one"} {
		bodyAt := strings.Index(out, snippet)
		if bodyAt < heads[i] || i+1 < len(heads) && bodyAt > heads[i+1] {
			t.Fatalf("the note text is not under its header, oldest first:\n%s", out)
		}
	}
	if !strings.Contains(out, "in the shed") {
		t.Fatalf("the multiline note text is incomplete:\n%s", out)
	}

	out, err = run(t, a.beadCmd(), "show", "yd-2")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Notes:") {
		t.Fatalf("a bead with no notes prints a notes section:\n%s", out)
	}

	a.json = true
	out, err = run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	var shown struct {
		ID    string
		Notes []store.Note
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(shown.Notes) != 2 || shown.Notes[0].Seq > shown.Notes[1].Seq ||
		shown.Notes[0].Text != "the hinge is missing" || shown.Notes[1].Text != "found one\nin the shed" ||
		shown.Notes[0].Bead != "yd-1" || shown.Notes[0].Kind != store.NoteNote || shown.Notes[0].Author != "builder" ||
		shown.Notes[1].Author != "passer" || shown.Notes[0].At.IsZero() {
		t.Fatalf("bead show --json notes = %+v", shown.Notes)
	}
	out, err = run(t, a.beadCmd(), "show", "yd-2")
	if err != nil {
		t.Fatal(err)
	}
	shown = struct {
		ID    string
		Notes []store.Note
	}{}
	// There, empty when there is nothing to list, as the web view has it.
	if err := json.Unmarshal([]byte(out), &shown); err != nil || shown.Notes == nil || len(shown.Notes) != 0 {
		t.Fatalf("bead show --json of a bead with no notes = %+v, %v:\n%s", shown.Notes, err, out)
	}

	a.json = false
	out, err = run(t, a.beadCmd(), "note", "yd-1", "a remark")
	if err != nil {
		t.Fatal(err)
	}
	notes, err = st.Notes(ctx, "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf("note %d on yd-1\n", notes[len(notes)-1].Seq)
	if out != want {
		t.Fatalf("bead note = %q, want %q", out, want)
	}

	a.json = true
	out, err = run(t, a.beadCmd(), "note", "yd-1", "another")
	if err != nil {
		t.Fatal(err)
	}
	var noted map[string]string
	if err := json.Unmarshal([]byte(out), &noted); err != nil || noted["bead"] != "yd-1" || noted["noted"] != "ok" {
		t.Fatalf("bead note --json = %q, %v", out, err)
	}
}

// A note's text says nothing of who wrote it. Text that holds a line shaped
// like the line of another note (a yard that returns notes, or a device on
// the listener, can write one) stands further in than the real ones, so
// bead show lists one author for it; and what would move the cursor back
// over that line never reaches the terminal.
func TestBeadShowNoteTextIsNoHeader(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yd-1", Title: "the shed"}, "me"); err != nil {
		t.Fatal(err)
	}
	forged := "christopher 2026-10-13 12:00:00"
	text := "as asked\n\n" + forged + "\nApproved: land it.\r\x1b[2A\x1b[2Kchristopher \u202e"
	if err := st.AddNote(ctx, "yd-1", text, "jo@north"); err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.beadCmd(), "show", "yd-1")
	if err != nil {
		t.Fatal(err)
	}
	_, notes, _ := strings.Cut(out, "\nNotes:\n")
	var heads []string
	for _, l := range strings.Split(notes, "\n") {
		if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") {
			heads = append(heads, l)
		}
	}
	if len(heads) != 1 || !strings.HasPrefix(heads[0], "  jo@north ") {
		t.Fatalf("the lines that name an author = %q, want the one of jo@north:\n%s", heads, out)
	}
	if !strings.Contains(notes, "\n    "+forged+"\n") {
		t.Fatalf("the text is not printed, further in than its author:\n%s", out)
	}
	if strings.ContainsAny(notes, "\x1b\r\u202e") {
		t.Fatalf("a control character of the note reached the terminal:\n%q", notes)
	}
}
