package store

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// The flows the siding tests follow. sidingFlow is web's and api's: a
// siding that waits for a person at ready. sidingBug is the flow web's
// bugs follow, with its own siding and its own next stage. sidingNone
// names a siding with no way out.
const (
	sidingFlow = `
backlog -> siding | new
siding -> ready | backlog
ready -> new | backlog
new -> merged
merged terminal
`
	sidingBug = `
backlog -> siding | new
siding -> triage | backlog
triage -> new | backlog
new -> merged
merged terminal
`
	sidingNone = `
backlog -> siding | new
new -> merged
merged terminal
`
)

// sidingYard is a store with depots web and api on sidingFlow, web's bugs
// on sidingBug, depot odd on sidingNone, a manual siding group of mckean's
// folding at fold or after, an exec group, and a yard default route sending
// the siding stage to the siding group. The clock is the test's, so its
// callers stay sequential.
type sidingYard struct {
	*Store
	t  *testing.T
	at time.Time
}

func newSidingYard(t *testing.T, fold int, after string) *sidingYard {
	t.Helper()
	y := &sidingYard{t: t, at: time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.at }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	y.Store = s
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Setting the operator makes the user, so mckean needs no AddUser.
	must(s.SetSetting(ctx, OperatorSetting, "mckean", "mckean"))
	for _, d := range []Depot{{Name: "web", Base: "main"}, {Name: "api", Base: "main"}, {Name: "odd", Base: "main"}} {
		must(s.PutDepot(ctx, d, "me"))
	}
	for name, text := range map[string]string{"siding.f": sidingFlow, "sidingbug.f": sidingBug, "sidingnone.f": sidingNone} {
		f, err := ParseFlow(text)
		must(err)
		f.Name = name
		_, err = s.PutFlow(ctx, *f, "me")
		must(err)
	}
	must(s.SetFlowPointer(ctx, "web", "", "siding.f", "me"))
	must(s.SetFlowPointer(ctx, "api", "", "siding.f", "me"))
	must(s.SetFlowPointer(ctx, "web", "bug", "sidingbug.f", "me"))
	must(s.SetFlowPointer(ctx, "odd", "", "sidingnone.f", "me"))
	must(s.PutGroup(ctx, Group{Name: "siding", Runner: "manual", Members: []string{"mckean"},
		Config: map[string]any{"fold": fold, "fold_after": after}}, "me"))
	must(s.PutGroup(ctx, Group{Name: "script", Runner: "exec", Limit: 9, Config: map[string]any{"cmd": "true"}}, "me"))
	_, err = s.AddRoute(ctx, Route{Stage: "siding", Group: "siding"}, "me")
	must(err)
	return y
}

// bead makes an open bead of depot's in stage.
func (y *sidingYard) bead(title, depot, stage string) *Bead {
	y.t.Helper()
	b, err := y.CreateBead(context.Background(), NewBead{Title: title, Depot: depot, Stage: stage}, "me")
	if err != nil {
		y.t.Fatal(err)
	}
	return b
}

// lists are the pass's lists, as the dispatcher reads them.
func (y *sidingYard) lists() ([]Bead, []Route) {
	y.t.Helper()
	ctx := context.Background()
	beads, err := y.ListBeads(ctx, BeadFilter{})
	if err != nil {
		y.t.Fatal(err)
	}
	routes, err := y.Routes(ctx)
	if err != nil {
		y.t.Fatal(err)
	}
	return beads, routes
}

// pass runs FoldSidings at the clock's now, over fresh lists.
func (y *sidingYard) pass() *SidingPass {
	y.t.Helper()
	beads, routes := y.lists()
	pass, err := y.FoldSidings(context.Background(), y.at, beads, routes)
	if err != nil {
		y.t.Fatal(err)
	}
	return pass
}

// tick moves the clock by d and runs the pass then.
func (y *sidingYard) tick(d time.Duration) *SidingPass {
	y.t.Helper()
	y.at = y.at.Add(d)
	return y.pass()
}

// eventsOf are bead's events of kind, oldest first.
func eventsOf(t *testing.T, s *Store, bead, kind string) []Event {
	t.Helper()
	evs, err := s.Events(context.Background(), bead, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, e := range evs {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// A siding folds when its open beads reach fold: the carrier goes to the
// siding's first next stage, the members close folded into it, and one
// folded event says members, left, from, to, trigger and points. A bead
// the siding's own manual group holds folds with the rest: its claim is a
// person's turn, not a session at work.
func TestSidingFoldsWhenFull(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	a, b := y.bead("first", "web", "siding"), y.bead("second", "web", "siding")
	if _, err := y.Claim(ctx, b.ID, "siding", b.Stage, "yard"); err != nil {
		t.Fatal(err)
	}
	pass := y.pass()
	if len(pass.Folded) != 1 {
		t.Fatalf("folded %d sidings, want one: %+v", len(pass.Folded), pass.Folded)
	}
	f := pass.Folded[0]
	if f.Carrier == "" || f.To != "ready" || f.Trigger != "count" || f.Advanced || f.Flow != "siding.f" || f.Depot != "web" {
		t.Errorf("fold = %+v, want a count fold of web's siding to ready", f)
	}
	if !reflect.DeepEqual(f.Members, []string{a.ID, b.ID}) || len(f.Left) != 0 {
		t.Errorf("members = %v left = %v, want [%s %s] and none", f.Members, f.Left, a.ID, b.ID)
	}
	carrier, err := y.GetBead(ctx, f.Carrier)
	if err != nil {
		t.Fatal(err)
	}
	if carrier.Stage != "ready" || carrier.Status != "open" {
		t.Errorf("carrier is %s in %s, want open in ready", carrier.Status, carrier.Stage)
	}
	for _, m := range []*Bead{a, b} {
		got, err := y.GetBead(ctx, m.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != "closed" {
			t.Errorf("%s is %s, want closed", m.ID, got.Status)
		}
	}
	folded := eventsOf(t, y.Store, f.Carrier, "folded")
	if len(folded) != 1 {
		t.Fatalf("%d folded events on the carrier, want one", len(folded))
	}
	want := map[string]any{"members": []any{a.ID, b.ID}, "left": []any{}, "from": "siding",
		"to": "ready", "trigger": "count", "points": float64(4)}
	if !reflect.DeepEqual(folded[0].Data, want) {
		t.Errorf("folded event data = %v, want %v", folded[0].Data, want)
	}
	if folded[0].Actor != "yard" {
		t.Errorf("folded by %s, want yard", folded[0].Actor)
	}
}

// Ineligible beads count toward fold but stay: two eligible beads and a
// held one fill a siding of three, and the fold names the held one in
// left.
func TestSidingCountsIneligibleBeads(t *testing.T) {
	y := newSidingYard(t, 3, "8h")
	ctx := context.Background()
	a, b := y.bead("first", "web", "siding"), y.bead("second", "web", "siding")
	h := y.bead("held", "web", "siding")
	if _, err := y.SetHold(ctx, h.ID, true, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	pass := y.pass()
	if len(pass.Folded) != 1 {
		t.Fatalf("folded %d sidings, want one: %+v", len(pass.Folded), pass.Folded)
	}
	f := pass.Folded[0]
	if !reflect.DeepEqual(f.Members, []string{a.ID, b.ID}) || !reflect.DeepEqual(f.Left, []string{h.ID}) {
		t.Errorf("members = %v left = %v, want [%s %s] and [%s]", f.Members, f.Left, a.ID, b.ID, h.ID)
	}
	if got, err := y.GetBead(ctx, h.ID); err != nil || got.Status != "open" || got.Stage != "siding" {
		t.Errorf("held bead = %+v, %v; want open in siding", got, err)
	}
	folded := eventsOf(t, y.Store, f.Carrier, "folded")
	if len(folded) != 1 || !reflect.DeepEqual(folded[0].Data["left"], []any{h.ID}) {
		t.Errorf("folded left = %v, want [%s]", folded[0].Data["left"], h.ID)
	}
}

// Past fold_after since the latest bead entered, eligible or not, the
// eligible ones fold. The pass says when the earliest siding that can fold
// is due, and a new arrival moves that.
func TestSidingFoldsWhenDue(t *testing.T) {
	y := newSidingYard(t, 4, "8h")
	a, b := y.bead("first", "web", "siding"), y.bead("second", "web", "siding")
	if pass := y.pass(); len(pass.Folded) != 0 {
		t.Fatalf("folded before anything was due: %+v", pass.Folded)
	} else if pass.Next == nil || !pass.Next.Equal(y.at.Add(8*time.Hour)) {
		t.Fatalf("next = %v, want %v", pass.Next, y.at.Add(8*time.Hour))
	}
	pass := y.tick(9 * time.Hour)
	if len(pass.Folded) != 1 {
		t.Fatalf("folded %d sidings past the wait, want one", len(pass.Folded))
	}
	f := pass.Folded[0]
	if f.Trigger != "wait" || !reflect.DeepEqual(f.Members, []string{a.ID, b.ID}) {
		t.Errorf("fold = %+v, want a wait fold of [%s %s]", f, a.ID, b.ID)
	}

	// The latest arrival starts the clock again, and the due moves with it.
	c, d := y.bead("third", "web", "siding"), y.bead("fourth", "web", "siding")
	due := y.at.Add(8 * time.Hour)
	if pass := y.tick(7 * time.Hour); len(pass.Folded) != 0 {
		t.Fatalf("folded an hour before the due: %+v", pass.Folded)
	} else if pass.Next == nil || !pass.Next.Equal(due) {
		t.Fatalf("next = %v, want %v", pass.Next, due)
	}
	e := y.bead("fifth", "web", "siding")
	if pass := y.pass(); len(pass.Folded) != 0 {
		t.Fatalf("folded on the arrival: %+v", pass.Folded)
	} else if pass.Next == nil || !pass.Next.Equal(y.at.Add(8*time.Hour)) {
		t.Fatalf("next = %v, want %v", pass.Next, y.at.Add(8*time.Hour))
	}
	pass = y.tick(8*time.Hour + time.Minute)
	if len(pass.Folded) != 1 {
		t.Fatalf("folded %d sidings past the moved due, want one", len(pass.Folded))
	}
	if got := pass.Folded[0].Members; !reflect.DeepEqual(got, []string{c.ID, d.ID, e.ID}) {
		t.Errorf("members = %v, want the three in the order made", got)
	}

	// A bead that cannot move starts the clock as any bead does, and stays
	// when it fires.
	sixth := y.bead("sixth", "web", "siding")
	y.at = y.at.Add(time.Hour)
	h := y.bead("held", "web", "siding")
	if _, err := y.SetHold(context.Background(), h.ID, true, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	due = y.at.Add(8 * time.Hour)
	y.at = y.at.Add(7 * time.Hour)
	if pass := y.pass(); len(pass.Folded) != 0 {
		t.Fatalf("folded before the held bead's due: %+v", pass.Folded)
	} else if pass.Next == nil || !pass.Next.Equal(due) {
		t.Fatalf("next = %v, want %v", pass.Next, due)
	}
	pass = y.tick(61 * time.Minute)
	if len(pass.Folded) != 1 {
		t.Fatalf("folded %d sidings past the held bead's due, want one", len(pass.Folded))
	}
	got := pass.Folded[0]
	if !got.Advanced || got.Carrier != sixth.ID || !reflect.DeepEqual(got.Left, []string{h.ID}) {
		t.Errorf("fold = %+v, want %s advanced with %s left", got, sixth.ID, h.ID)
	}
}

// A ready siding with one eligible bead advances it, not folded: no
// one-part carrier. The move is the yard's own, past a group the yard is
// no member of, and its event says by siding.
func TestSidingAdvancesOneEligibleBead(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	a := y.bead("only", "web", "siding")
	h := y.bead("held", "web", "siding")
	if _, err := y.SetHold(ctx, h.ID, true, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	names := func() []string {
		beads, err := y.ListBeads(ctx, BeadFilter{})
		if err != nil {
			t.Fatal(err)
		}
		ids := make([]string, len(beads))
		for i, b := range beads {
			ids[i] = b.ID
		}
		return ids
	}
	before := names()
	pass := y.pass()
	if len(pass.Folded) != 1 || !pass.Folded[0].Advanced {
		t.Fatalf("folded %+v, want one advance", pass.Folded)
	}
	f := pass.Folded[0]
	if f.Carrier != a.ID || len(f.Members) != 0 || !reflect.DeepEqual(f.Left, []string{h.ID}) || f.To != "ready" {
		t.Errorf("fold = %+v, want %s advanced to ready with %s left", f, a.ID, h.ID)
	}
	if got := names(); !reflect.DeepEqual(got, before) {
		t.Errorf("open beads = %v, want no carrier: %v", got, before)
	}
	if got, err := y.GetBead(ctx, a.ID); err != nil || got.Stage != "ready" || got.Status != "open" {
		t.Errorf("advanced bead = %+v, %v; want open in ready", got, err)
	}
	advanced := eventsOf(t, y.Store, a.ID, "advanced")
	if len(advanced) != 1 {
		t.Fatalf("%d advanced events, want one", len(advanced))
	}
	if advanced[0].Data["by"] != "siding" || advanced[0].Data["from"] != "siding" || advanced[0].Data["to"] != "ready" {
		t.Errorf("advanced event data = %v, want by siding from siding to ready", advanced[0].Data)
	}
	if advanced[0].Actor != "yard" {
		t.Errorf("advanced by %s, want yard", advanced[0].Actor)
	}
}

// Held, blocked, claimed and asked beads stay in the siding and are named
// in folded's left, oldest first: a hold at another stage binds the fold
// as one here does, and so does a train's hold and another yard's.
func TestSidingLeavesIneligibleBeads(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	a, b := y.bead("first", "web", "siding"), y.bead("second", "web", "siding")
	h := y.bead("held", "web", "siding")
	if _, err := y.SetHold(ctx, h.ID, true, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	blocker, err := y.CreateBead(ctx, NewBead{Title: "blocker", Depot: "web", Stage: "backlog"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	p := y.bead("blocked", "web", "siding")
	if err := y.AddDep(ctx, blocker.ID, p.ID, DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	heldElsewhere := y.bead("held at new", "web", "siding")
	if err := y.AddDep(ctx, blocker.ID, heldElsewhere.ID, DepBlocks, "new", "", "me"); err != nil {
		t.Fatal(err)
	}
	c := y.bead("claimed", "web", "siding")
	if _, err := y.Claim(ctx, c.ID, "script", c.Stage, "yard"); err != nil {
		t.Fatal(err)
	}
	k := y.bead("asked", "web", "siding")
	if _, err := y.AddAsk(ctx, k.ID, Ask{Question: "Which one?", To: "mckean", Context: []string{"it is open"}}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	train, err := y.CreateBead(ctx, NewBead{Title: "train", Type: TypeTrain, Depot: "web", Stage: "backlog"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	wagon := y.bead("wagon", "web", "siding")
	if err := y.AddDep(ctx, train.ID, wagon.ID, DepParent, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	lone, err := y.CreateBead(ctx, NewBead{Title: "lone train", Type: TypeTrain, Depot: "web", Stage: "siding"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	away := y.bead("away", "web", "siding")
	if err := y.write(func(w *wtx) error {
		row, err := w.loadBead(away.ID)
		if err != nil {
			return err
		}
		return put(w.Tx, bPeerBeads, row.UUID, peerBead{Bead: row.UUID, Direction: "in", Theirs: "north-1", Was: "north"})
	}); err != nil {
		t.Fatal(err)
	}
	pass := y.pass()
	if len(pass.Folded) != 1 {
		t.Fatalf("folded %d sidings, want one: %+v", len(pass.Folded), pass.Folded)
	}
	f := pass.Folded[0]
	wantLeft := []string{h.ID, p.ID, heldElsewhere.ID, c.ID, k.ID, wagon.ID, lone.ID, away.ID}
	if !reflect.DeepEqual(f.Members, []string{a.ID, b.ID}) || !reflect.DeepEqual(f.Left, wantLeft) {
		t.Errorf("members = %v left = %v, want [%s %s] and %v", f.Members, f.Left, a.ID, b.ID, wantLeft)
	}
	for _, id := range wantLeft {
		if got, err := y.GetBead(ctx, id); err != nil || got.Status != "open" || got.Stage != "siding" {
			t.Errorf("%s = %+v, %v; want open in siding", id, got, err)
		}
	}
}

// Two depots on one yard default route fold apart: each siding folds its
// own beads into its own depot's carrier.
func TestSidingsFoldApartByDepot(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	a, b := y.bead("web one", "web", "siding"), y.bead("web two", "web", "siding")
	c, d := y.bead("api one", "api", "siding"), y.bead("api two", "api", "siding")
	pass := y.pass()
	if len(pass.Folded) != 2 {
		t.Fatalf("folded %d sidings, want two: %+v", len(pass.Folded), pass.Folded)
	}
	// Keys sort by depot, so api's fold comes first.
	first, second := pass.Folded[0], pass.Folded[1]
	if first.Depot != "api" || !reflect.DeepEqual(first.Members, []string{c.ID, d.ID}) {
		t.Errorf("first fold = %+v, want api's [%s %s]", first, c.ID, d.ID)
	}
	if second.Depot != "web" || !reflect.DeepEqual(second.Members, []string{a.ID, b.ID}) {
		t.Errorf("second fold = %+v, want web's [%s %s]", second, a.ID, b.ID)
	}
	for _, f := range pass.Folded {
		carrier, err := y.GetBead(ctx, f.Carrier)
		if err != nil {
			t.Fatal(err)
		}
		if carrier.Depot == nil || *carrier.Depot != f.Depot || carrier.Stage != "ready" {
			t.Errorf("carrier %+v is not open in %s's ready", carrier, f.Depot)
		}
	}
}

// Two flows of one depot that both name the siding stage fold apart, each
// into its own flow's next stage.
func TestSidingsFoldApartByFlow(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	a, b := y.bead("task one", "web", "siding"), y.bead("task two", "web", "siding")
	mkbug := func(title string) *Bead {
		t.Helper()
		bead, err := y.CreateBead(ctx, NewBead{Title: title, Type: "bug", Depot: "web", Stage: "siding"}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return bead
	}
	c, d := mkbug("bug one"), mkbug("bug two")
	pass := y.pass()
	if len(pass.Folded) != 2 {
		t.Fatalf("folded %d sidings, want two: %+v", len(pass.Folded), pass.Folded)
	}
	byFlow := map[string]FoldedSiding{}
	for _, f := range pass.Folded {
		byFlow[f.Flow] = f
	}
	task, bug := byFlow["siding.f"], byFlow["sidingbug.f"]
	if !reflect.DeepEqual(task.Members, []string{a.ID, b.ID}) || task.To != "ready" {
		t.Errorf("task fold = %+v, want [%s %s] to ready", task, a.ID, b.ID)
	}
	if !reflect.DeepEqual(bug.Members, []string{c.ID, d.ID}) || bug.To != "triage" {
		t.Errorf("bug fold = %+v, want [%s %s] to triage", bug, c.ID, d.ID)
	}
	for _, f := range pass.Folded {
		if carrier, err := y.GetBead(ctx, f.Carrier); err != nil || carrier.Stage != f.To {
			t.Errorf("carrier of %+v = %+v, %v; want it in %s", f, carrier, err, f.To)
		}
	}
}

// The write folds what is there when it runs, not what the lists held: a
// member that arrived after them is folded too, and one that left is not.
func TestSidingFoldsWhatIsThere(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	a, b := y.bead("first", "web", "siding"), y.bead("second", "web", "siding")
	c := y.bead("third", "web", "siding")
	beads, routes := y.lists()
	d := y.bead("fourth", "web", "siding")
	if _, err := y.Advance(ctx, b.ID, "backlog", Expect{}, "mckean"); err != nil {
		t.Fatal(err)
	}
	pass, err := y.FoldSidings(ctx, y.at, beads, routes)
	if err != nil {
		t.Fatal(err)
	}
	if len(pass.Folded) != 1 {
		t.Fatalf("folded %d sidings, want one: %+v", len(pass.Folded), pass.Folded)
	}
	if got := pass.Folded[0].Members; !reflect.DeepEqual(got, []string{a.ID, c.ID, d.ID}) {
		t.Errorf("members = %v, want [%s %s %s]", got, a.ID, c.ID, d.ID)
	}
	if got, err := y.GetBead(ctx, b.ID); err != nil || got.Status != "open" || got.Stage != "backlog" {
		t.Errorf("moved bead = %+v, %v; want open in backlog", got, err)
	}
}

// A bead whose depot's repository cannot be read is left out of the pass,
// and folded on a later one, once it reads again.
func TestSidingSkipsAnUnreadableRepository(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	good := t.TempDir()
	p := filepath.Join(good, RepoFlowDir, "default.flow")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(sidingFlow), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := y.PutDepot(ctx, Depot{Name: "repo", Kind: DepotDir, Path: good}, "me"); err != nil {
		t.Fatal(err)
	}
	a, b := y.bead("first", "repo", "siding"), y.bead("second", "repo", "siding")
	// The flows entry is a file, not a directory: the listing fails.
	bad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bad, ".yardr"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, RepoFlowDir), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	depot, err := y.GetDepot(ctx, "repo")
	if err != nil {
		t.Fatal(err)
	}
	depot.Path = bad
	if err := y.PutDepot(ctx, *depot, "mckean"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.RepoFlows(ctx, "repo"); err == nil {
		t.Fatal("the broken repository reads")
	}
	if pass := y.pass(); len(pass.Folded) != 0 {
		t.Fatalf("folded through an unreadable repository: %+v", pass.Folded)
	}
	for _, m := range []*Bead{a, b} {
		if got, err := y.GetBead(ctx, m.ID); err != nil || got.Status != "open" || got.Stage != "siding" {
			t.Errorf("%s = %+v, %v; want open in siding", m.ID, got, err)
		}
	}
	depot.Path = good
	if err := y.PutDepot(ctx, *depot, "mckean"); err != nil {
		t.Fatal(err)
	}
	pass := y.pass()
	if len(pass.Folded) != 1 {
		t.Fatalf("folded %d sidings once the repository reads, want one", len(pass.Folded))
	}
	if got := pass.Folded[0].Members; len(got) != 2 {
		t.Errorf("members = %v, want the two", got)
	}
}

// A ready siding whose stage has no next stage folds nothing and arms
// nothing, and logs one fold_refused with reason next on its latest member,
// with the siding, the sorted members, their points and fold_max. The next
// pass over the same set logs none; a new arrival logs one again.
func TestSidingRefusesWithoutNext(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	a := y.bead("first", "odd", "siding")
	y.at = y.at.Add(time.Minute)
	b := y.bead("second", "odd", "siding")
	pass := y.pass()
	if len(pass.Folded) != 0 || pass.Next != nil {
		t.Fatalf("pass = %+v, want nothing folded and no due", pass)
	}
	if len(pass.Refused) != 1 {
		t.Fatalf("refused %d sidings, want one: %+v", len(pass.Refused), pass.Refused)
	}
	r := pass.Refused[0]
	if r.Depot != "odd" || r.Flow != "sidingnone.f" || r.Stage != "siding" || r.Reason != "next" {
		t.Errorf("refusal = %+v, want odd's siding refused for next", r)
	}
	if !reflect.DeepEqual(r.Members, []string{a.ID, b.ID}) {
		t.Errorf("members = %v, want [%s %s] oldest first", r.Members, a.ID, b.ID)
	}
	for _, m := range []*Bead{a, b} {
		if got, err := y.GetBead(ctx, m.ID); err != nil || got.Status != "open" || got.Stage != "siding" {
			t.Errorf("%s = %+v, %v; want open in siding", m.ID, got, err)
		}
	}
	if evs, err := y.EventsOfKind(ctx, "folded", 100); err != nil || len(evs) != 0 {
		t.Fatalf("folded events = %d, %v; want none", len(evs), err)
	}
	refused := eventsOf(t, y.Store, b.ID, FoldRefused)
	if len(refused) != 1 {
		t.Fatalf("%d fold_refused events on the latest member %s, want one", len(refused), b.ID)
	}
	members := []string{a.ID, b.ID}
	slices.Sort(members)
	want := map[string]any{"depot": "odd", "flow": "sidingnone.f", "stage": "siding",
		"members": []any{members[0], members[1]}, "points": float64(4), "fold_max": float64(6),
		"reason": "next", "unreadable": []any{members[0], members[1]}}
	if !reflect.DeepEqual(refused[0].Data, want) {
		t.Errorf("fold_refused data = %v, want %v", refused[0].Data, want)
	}
	if refused[0].Actor != "yard" {
		t.Errorf("fold_refused by %s, want yard", refused[0].Actor)
	}
	if other := eventsOf(t, y.Store, a.ID, FoldRefused); len(other) != 0 {
		t.Errorf("%d fold_refused events on the older member, want none", len(other))
	}

	// The same set tells once: the next pass logs none, a new arrival logs
	// one again.
	if pass := y.pass(); len(pass.Refused) != 0 {
		t.Fatalf("refused the same set again: %+v", pass.Refused)
	}
	if refused := eventsOf(t, y.Store, b.ID, FoldRefused); len(refused) != 1 {
		t.Fatalf("%d fold_refused events after a second pass, want one", len(refused))
	}
	y.at = y.at.Add(time.Minute)
	c := y.bead("third", "odd", "siding")
	if pass := y.pass(); len(pass.Refused) != 1 {
		t.Fatalf("refused %d sidings after an arrival, want one: %+v", len(pass.Refused), pass.Refused)
	}
	if refused := eventsOf(t, y.Store, c.ID, FoldRefused); len(refused) != 1 {
		t.Fatalf("%d fold_refused events on the arrival, want one", len(refused))
	}

	// A siding with no next stage still wakes at fold_after, when it becomes
	// ready and can tell the yardmaster that the flow has nowhere to go.
	timed := newSidingYard(t, 4, "8h")
	only := timed.bead("only", "odd", "siding")
	due := timed.at.Add(8 * time.Hour)
	if pass := timed.pass(); len(pass.Refused) != 0 || pass.Next == nil || !pass.Next.Equal(due) {
		t.Fatalf("early pass = %+v, want no refusal and a wake at %v", pass, due)
	}
	pass = timed.tick(9 * time.Hour)
	if len(pass.Folded) != 0 || pass.Next != nil || len(pass.Refused) != 1 || pass.Refused[0].Reason != "next" {
		t.Fatalf("due pass = %+v, want one next-stage refusal and no fold or next wake", pass)
	}
	if got := eventsOf(t, timed.Store, only.ID, FoldRefused); len(got) != 1 {
		t.Fatalf("%d fold_refused events after fold_after, want one", len(got))
	}
}

// A ready siding whose eligible beads weigh more than fold_max changes
// nothing and logs one fold_refused with reason ceiling on its latest
// member, with the siding, the sorted members and the beads whose estimate
// is unreadable. The next pass over the same set logs none; a new arrival,
// an older member leaving, and an older member becoming ineligible log one
// again, the latest staying: the set is compared, not the latest member. A
// bead with more than one size label counts the largest, one with an
// unknown label or none counts 2, and each is named as unreadable.
func TestSidingRefusesOverCeiling(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	mk := func(title string, labels ...string) *Bead {
		t.Helper()
		y.at = y.at.Add(time.Minute)
		b, err := y.CreateBead(ctx, NewBead{Title: title, Depot: "web", Stage: "siding", Labels: labels}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	a := mk("first", "size:large")
	b := mk("second", "size:small", "size:large")
	c := mk("third", "size:huge")
	d := mk("fourth")
	sorted := func(ids ...string) []any {
		slices.Sort(ids)
		var out []any
		for _, id := range ids {
			out = append(out, id)
		}
		return out
	}
	refusals := func() []Event {
		t.Helper()
		evs, err := y.EventsOfKind(ctx, FoldRefused, 100)
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	pass := y.pass()
	if len(pass.Folded) != 0 || pass.Next != nil {
		t.Fatalf("pass = %+v, want nothing folded and no due", pass)
	}
	if len(pass.Refused) != 1 {
		t.Fatalf("refused %d sidings, want one: %+v", len(pass.Refused), pass.Refused)
	}
	r := pass.Refused[0]
	if r.Depot != "web" || r.Flow != "siding.f" || r.Stage != "siding" || r.Reason != "ceiling" {
		t.Errorf("refusal = %+v, want web's siding refused for ceiling", r)
	}
	if !reflect.DeepEqual(r.Members, []string{a.ID, b.ID, c.ID, d.ID}) {
		t.Errorf("members = %v, want the four oldest first", r.Members)
	}
	for _, m := range []*Bead{a, b, c, d} {
		if got, err := y.GetBead(ctx, m.ID); err != nil || got.Status != "open" || got.Stage != "siding" {
			t.Errorf("%s = %+v, %v; want open in siding", m.ID, got, err)
		}
	}
	if evs := refusals(); len(evs) != 1 {
		t.Fatalf("%d fold_refused events, want one", len(evs))
	}
	got := eventsOf(t, y.Store, d.ID, FoldRefused)
	if len(got) != 1 {
		t.Fatalf("%d fold_refused events on the latest member %s, want one", len(got), d.ID)
	}
	want := map[string]any{"depot": "web", "flow": "siding.f", "stage": "siding",
		"members": sorted(a.ID, b.ID, c.ID, d.ID), "points": float64(12), "fold_max": float64(6),
		"reason": "ceiling", "unreadable": sorted(b.ID, c.ID, d.ID)}
	if !reflect.DeepEqual(got[0].Data, want) {
		t.Errorf("fold_refused data = %v, want %v", got[0].Data, want)
	}
	if got[0].Actor != "yard" {
		t.Errorf("fold_refused by %s, want yard", got[0].Actor)
	}

	// The same set tells once.
	if pass := y.pass(); len(pass.Refused) != 0 {
		t.Fatalf("refused the same set again: %+v", pass.Refused)
	}
	if evs := refusals(); len(evs) != 1 {
		t.Fatalf("%d fold_refused events after a second pass, want one", len(evs))
	}

	// A new arrival tells again, on itself as the latest.
	e := mk("fifth", "size:large")
	if pass := y.pass(); len(pass.Refused) != 1 {
		t.Fatalf("refused %d sidings after an arrival, want one: %+v", len(pass.Refused), pass.Refused)
	}
	if evs := refusals(); len(evs) != 2 {
		t.Fatalf("%d fold_refused events after an arrival, want two", len(evs))
	}
	got = eventsOf(t, y.Store, e.ID, FoldRefused)
	if len(got) != 1 || got[0].Data["points"] != float64(16) ||
		!reflect.DeepEqual(got[0].Data["members"], sorted(a.ID, b.ID, c.ID, d.ID, e.ID)) {
		t.Fatalf("arrival's fold_refused = %+v, want 16 points over the five", got)
	}

	// An older member leaving tells again, the latest staying.
	if _, err := y.Advance(ctx, a.ID, "backlog", Expect{}, "mckean"); err != nil {
		t.Fatal(err)
	}
	if pass := y.pass(); len(pass.Refused) != 1 {
		t.Fatalf("refused %d sidings after a member left, want one: %+v", len(pass.Refused), pass.Refused)
	}
	if evs := refusals(); len(evs) != 3 {
		t.Fatalf("%d fold_refused events after a member left, want three", len(evs))
	}
	got = eventsOf(t, y.Store, e.ID, FoldRefused)
	if len(got) != 2 || got[1].Data["points"] != float64(12) ||
		!reflect.DeepEqual(got[1].Data["members"], sorted(b.ID, c.ID, d.ID, e.ID)) ||
		!reflect.DeepEqual(got[1].Data["unreadable"], sorted(b.ID, c.ID, d.ID)) {
		t.Fatalf("second fold_refused on %s = %+v, want 12 points over the four left", e.ID, got)
	}

	// An older member becoming ineligible tells again too: eligibility is
	// part of the set, and the latest stays.
	if _, err := y.SetHold(ctx, b.ID, true, Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if pass := y.pass(); len(pass.Refused) != 1 {
		t.Fatalf("refused %d sidings after a member held, want one: %+v", len(pass.Refused), pass.Refused)
	}
	if evs := refusals(); len(evs) != 4 {
		t.Fatalf("%d fold_refused events after a member held, want four", len(evs))
	}
	got = eventsOf(t, y.Store, e.ID, FoldRefused)
	if len(got) != 3 || got[2].Data["points"] != float64(8) ||
		!reflect.DeepEqual(got[2].Data["members"], sorted(c.ID, d.ID, e.ID)) ||
		!reflect.DeepEqual(got[2].Data["unreadable"], sorted(c.ID, d.ID)) {
		t.Fatalf("third fold_refused on %s = %+v, want 8 points over the three eligible", e.ID, got)
	}
	if got, err := y.GetBead(ctx, b.ID); err != nil || got.Status != "open" || got.Stage != "siding" {
		t.Errorf("held bead = %+v, %v; want open in siding", got, err)
	}

	// The remembered refusal must survive more than EventsOfKind's default
	// page of unrelated siding refusals.
	for i := 0; i < 51; i++ {
		if err := y.LogEvent(ctx, "", FoldRefused, "yard", map[string]any{
			"depot": "other", "flow": "siding.f", "stage": "siding",
			"members": []string{"elsewhere"}, "reason": "ceiling",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if pass := y.pass(); len(pass.Refused) != 0 {
		t.Fatalf("refused the same set after unrelated refusals: %+v", pass.Refused)
	}
	if got := eventsOf(t, y.Store, e.ID, FoldRefused); len(got) != 3 {
		t.Fatalf("%d fold_refused events on unchanged set after unrelated refusals, want three", len(got))
	}
}

// A ready siding with no eligible bead does nothing and logs nothing: no
// fold, no refusal, no due. Its beads wait until one becomes eligible.
func TestSidingSkipsWithoutEligible(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	c, d := y.bead("third", "web", "siding"), y.bead("fourth", "web", "siding")
	for _, m := range []*Bead{c, d} {
		if _, err := y.SetHold(ctx, m.ID, true, Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if pass := y.pass(); len(pass.Folded) != 0 || len(pass.Refused) != 0 || pass.Next != nil {
		t.Fatalf("pass = %+v, want nothing folded, refused or due", pass)
	}
	for _, kind := range []string{"folded", FoldRefused} {
		if evs, err := y.EventsOfKind(ctx, kind, 100); err != nil || len(evs) != 0 {
			t.Errorf("%s events = %d, %v; want none", kind, len(evs), err)
		}
	}
}

// A yard with no siding has no pass: nothing folded, nothing due.
func TestFoldSidingsNeedsNoSiding(t *testing.T) {
	y := newSidingYard(t, 2, "8h")
	ctx := context.Background()
	if _, err := y.DeleteGroup(ctx, "siding", "mckean"); err != nil {
		t.Fatal(err)
	}
	y.bead("lone", "web", "siding")
	if pass := y.pass(); len(pass.Folded) != 0 || pass.Next != nil {
		t.Fatalf("pass = %+v, want nothing folded and no due", pass)
	}
}
