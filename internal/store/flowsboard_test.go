package store

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// flowsBoard is the served view of a yard with a depot on a store flow, a
// depot with no flow, and beads travelling with no depot:
//
//	shop  code beads: new 2 (one held), review 1; task beads in backlog 1;
//	      bug beads: triage 1; a pinned bead and a stranded one off the line
//	loose one bead in stage inbox, off the line
//	      two beads with no depot, one travelling and one in backlog
func flowsBoard(t testing.TB, y *arrivalsTest) {
	t.Helper()
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(y.PutDepot(ctx, Depot{Name: "shop", Base: "origin/main"}, "me"))
	looseDir := dirDepot(t, y, "loose")
	fl, err := ParseFlow("backlog -> new\nnew -> review | backlog\nreview -> merged | new\nmerged terminal\n")
	must(err)
	fl.Name = "code"
	_, err = y.PutFlow(ctx, *fl, "me")
	must(err)
	must(y.SetFlowPointer(ctx, "shop", "", "code", "me"))
	bugs, err := ParseFlow("triage -> fixed\nfixed terminal\n")
	must(err)
	bugs.Name = "bugs"
	_, err = y.PutFlow(ctx, *bugs, "me")
	must(err)
	must(y.SetFlowPointer(ctx, "shop", "bug", "bugs", "me"))
	bead := func(id, depot, stage string) *Bead {
		t.Helper()
		nb := NewBead{Title: id, ID: id, Stage: stage}
		if depot != "" {
			nb.Depot = depot
		}
		b, err := y.CreateBead(ctx, nb, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	bead("shop-back", "shop", "backlog")
	held := bead("shop-held", "shop", "new")
	bead("shop-new", "shop", "new")
	bead("shop-review", "shop", "review")
	_, err = y.SetHold(ctx, held.ID, true, Expect{}, "me")
	must(err)
	bug, err := y.CreateBead(ctx, NewBead{Title: "shop-bug", ID: "shop-bug", Type: "bug", Stage: "triage", Depot: "shop"}, "me")
	must(err)
	_ = bug
	// A changed flow's leftover: created where no flow governs, then the
	// flow arrives without its stage. Only a flow read from files can lose
	// a stage: the store's writes refuse it.
	stray, err := y.CreateBead(ctx, NewBead{Title: "shop-stray", ID: "shop-stray", Stage: "limbo", Depot: "loose"}, "me")
	must(err)
	_ = stray
	writeFlow(t, looseDir, "inbox -> done\ndone terminal\n")
	pinned, err := y.CreateBead(ctx, NewBead{Title: "shop-pinned", ID: "shop-pinned", Stage: "new", Depot: "shop",
		Metadata: map[string]any{"flow": "code"}}, "me")
	must(err)
	_ = pinned
	y.at(y.now.Add(time.Hour))
	bead("wild-new", "", "new")
	y.at(y.now.Add(time.Hour))
	bead("wild-back", "", "backlog")
}

// dirDepot makes a directory depot whose flows are read from files, so a
// flow can lose a stage its beads still stand in. It answers the depot's
// directory; writeFlow writes its default flow.
func dirDepot(t testing.TB, y *arrivalsTest, depot string) string {
	t.Helper()
	dir := t.TempDir()
	if err := y.PutDepot(context.Background(), Depot{Name: depot, Path: dir, Kind: DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFlow(t testing.TB, dir, text string) {
	t.Helper()
	flows := filepath.Join(dir, ".yardr", "flows")
	if err := os.MkdirAll(flows, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(flows, "default.flow"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// flowBeads is the bead ids of rows, in order.
func flowBeads(rows []FlowBead) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Bead)
	}
	return out
}

// TestFlowsServesDepotsFlowsStagesBeads pins the served view: the depots
// each with its flows, each flow its stages in travel order with the open
// beads on each, the per-depot Elsewhere, the unassigned rows and the
// travelling totals.
func TestFlowsServesDepotsFlowsStagesBeads(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	flowsBoard(t, y)
	board, err := y.Flows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if board.Travelling != 7 {
		t.Errorf("travelling = %d, want 7 (every open bead outside backlog)", board.Travelling)
	}
	if len(board.Depots) != 2 || board.Depots[0].Name != "loose" || board.Depots[1].Name != "shop" {
		t.Fatalf("depots = %+v, want loose then shop", board.Depots)
	}
	loose, shop := board.Depots[0], board.Depots[1]
	if loose.Kind != DepotDir || shop.Kind != DepotGit {
		t.Errorf("kinds = %q, %q, want dir and git, served as /flows serves them", loose.Kind, shop.Kind)
	}
	if loose.Travelling != 1 || shop.Travelling != 5 {
		t.Errorf("travelling = %d, %d, want 1 and 5", loose.Travelling, shop.Travelling)
	}
	if len(loose.Flows) != 1 || loose.Flows[0].Name != "default" || loose.Flows[0].Source != "repo (directory)" {
		t.Fatalf("loose flows = %+v, want the directory's flow", loose.Flows)
	}
	if got := flowBeads(loose.Elsewhere); !reflect.DeepEqual(got, []string{"shop-stray"}) {
		t.Errorf("loose elsewhere = %v, want the stranded bead", got)
	}
	if len(shop.Flows) != 2 || shop.Flows[0].Name != "code" || shop.Flows[0].Type != "" || shop.Flows[1].Name != "bugs" || shop.Flows[1].Type != "bug" {
		t.Fatalf("shop flows = %+v, want code then bugs", shop.Flows)
	}
	type stage struct {
		name, group, lamp string
		human             bool
		beads             []string
	}
	var have []stage
	for _, s := range shop.Flows[0].Stages {
		have = append(have, stage{s.Stage, s.Group, s.Lamp, s.Human, flowBeads(s.Beads)})
	}
	// The newest change first, ties by id; new is routed to the
	// builders, review to nobody; the hold lights new's lamp.
	want := []stage{
		{name: "backlog", lamp: "none", beads: []string{"shop-back"}},
		{name: "new", group: "builders", lamp: "fault", beads: []string{"shop-held", "shop-new"}},
		{name: "review", lamp: "none", beads: []string{"shop-review"}},
		{name: "merged", lamp: "none"},
	}
	if !reflect.DeepEqual(have, want) {
		t.Errorf("flow code's stages:\n got %+v\nwant %+v", have, want)
	}
	bugs := shop.Flows[1]
	if len(bugs.Stages) != 2 || !reflect.DeepEqual(flowBeads(bugs.Stages[0].Beads), []string{"shop-bug"}) {
		t.Errorf("flow bugs = %+v, want the bug on triage", bugs)
	}
	if got := flowBeads(shop.Elsewhere); !reflect.DeepEqual(got, []string{"shop-pinned"}) {
		t.Errorf("shop elsewhere = %v, want the pinned bead", got)
	}
	if got := flowBeads(board.Unassigned); !reflect.DeepEqual(got, []string{"wild-new"}) {
		t.Errorf("unassigned = %v, want the travelling bead with no depot", got)
	}
	// Every row carries its Arrivals remark: the held bead's is held, by
	// whom, and red, and the rest have none to make.
	held := shop.Flows[0].Stages[1].Beads[0]
	if held.Title != "shop-held" || held.Remark != "held by me" || held.Tone != "red" || held.HeldBy != "me" {
		t.Errorf("the held bead's row = %+v, want the Arrivals held remark and its holder", held)
	}
	if r := shop.Flows[0].Stages[1].Beads[1]; r.Remark != "" || r.Tone != "none" || r.HeldBy != "" {
		t.Errorf("the plain bead's row = %+v, want no remark", r)
	}
}

// TestFlowsHeldBy pins the holder on a flow bead wherever it stands: on a
// stage, off the line in a depot's Elsewhere, and unassigned. A bead held
// with no holder on record reads "held" and carries no held_by.
func TestFlowsHeldBy(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	flowsBoard(t, y)
	ctx := context.Background()
	if _, err := y.SetHold(ctx, "shop-pinned", true, Expect{}, "nell"); err != nil {
		t.Fatal(err)
	}
	y.holdWithoutHolder(t, "wild-new")
	board, err := y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var shop FlowsDepot
	for _, d := range board.Depots {
		if d.Name == "shop" {
			shop = d
		}
	}
	for _, c := range []struct {
		where string
		row   FlowBead
		want  [3]string // remark, tone, held_by
	}{
		{"on a stage", shop.Flows[0].Stages[1].Beads[0], [3]string{"held by me", "red", "me"}},
		{"in Elsewhere", shop.Elsewhere[0], [3]string{"held by nell", "red", "nell"}},
		{"unassigned, no holder on record", board.Unassigned[0], [3]string{"held", "red", ""}},
	} {
		if got := [3]string{c.row.Remark, c.row.Tone, c.row.HeldBy}; got != c.want {
			t.Errorf("%s: %s is %v, want %v", c.where, c.row.Bead, got, c.want)
		}
		raw, err := json.Marshal(c.row)
		if err != nil {
			t.Fatal(err)
		}
		if has := strings.Contains(string(raw), `"held_by"`); has != (c.want[2] != "") {
			t.Errorf("%s: %s served as %s, want held_by present = %v", c.where, c.row.Bead, raw, c.want[2] != "")
		}
	}
}

// TestFlowsLamps pins the fault/turn rule: a hold and a prompt nobody
// answers are faults; a manual group's stage and an open ask to the
// operator are the person's turn; an answered prompt, an ask to anyone
// else and a plain bead light nothing.
func TestFlowsLamps(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "shop", "code", "new", "review")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	bareDir := dirDepot(t, y, "bare")
	y.flowDepot(t, "quiet", "quiet-flow", "new", "done")
	_, err := y.AddUser(ctx, "op", "op")
	must(err)
	must(y.SetSetting(ctx, OperatorSetting, "op", "op"))
	_, err = y.AddUser(ctx, "zed", "op")
	must(err)
	held := y.travel(t, "held", "shop", "new")
	_, err = y.SetHold(ctx, held.ID, true, Expect{}, "me")
	must(err)
	blocked := y.travel(t, "blocked", "shop", "new")
	sess, err := y.Claim(ctx, blocked.ID, "builders", "new", "yard")
	must(err)
	must(y.MarkRunning(ctx, sess.ID, map[string]any{"agent": "blocked-new"}))
	must(y.LogDispatch(ctx, blocked.ID, "session_blocked", "yard", map[string]any{"session": sess.ID}))
	// A prompt asked and answered is no fault: its last word unblocks.
	freed, err := y.CreateBead(ctx, NewBead{Title: "freed", ID: "freed", Stage: "inbox", Depot: "bare"}, "me")
	must(err)
	fsess, err := y.Claim(ctx, freed.ID, "builders", "inbox", "yard")
	must(err)
	must(y.MarkRunning(ctx, fsess.ID, map[string]any{"agent": "freed-new"}))
	must(y.LogDispatch(ctx, freed.ID, "session_blocked", "yard", map[string]any{"session": fsess.ID}))
	must(y.LogDispatch(ctx, freed.ID, "session_unblocked", "yard", map[string]any{"session": fsess.ID}))
	stray, err := y.CreateBead(ctx, NewBead{Title: "stray", ID: "stray", Stage: "limbo", Depot: "bare"}, "me")
	must(err)
	_ = stray
	writeFlow(t, bareDir, "inbox -> done\ndone terminal\n")
	y.travel(t, "parked", "shop", "review")
	_, err = y.AddRoute(ctx, Route{Stage: "review", Group: "deciders"}, "me")
	must(err)
	solo := y.travel(t, "solo", "quiet", "new")
	_, err = y.AddAsk(ctx, solo.ID, Ask{Question: "For zed?", To: "zed", Context: []string{"zed's call."}}, "yard")
	must(err)
	stageLamp := func(board *FlowsBoard, depot, stage string) string {
		t.Helper()
		for _, d := range board.Depots {
			if d.Name != depot {
				continue
			}
			for _, f := range d.Flows {
				for _, s := range f.Stages {
					if s.Stage == stage {
						return s.Lamp
					}
				}
			}
		}
		t.Fatalf("no stage %s on depot %s", stage, depot)
		return ""
	}
	board, err := y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// New holds the held and the blocked: a fault. Review holds the parked
	// bead on its manual route: turn. Bare's inbox holds only the freed
	// bead: none, and the stranded bead stands off the line. Quiet's new
	// holds only the bead asked of zed: none.
	for _, tt := range []struct{ depot, stage, lamp string }{
		{"shop", "new", "fault"},
		{"shop", "review", "turn"},
		{"bare", "inbox", "none"},
		{"quiet", "new", "none"},
	} {
		if got := stageLamp(board, tt.depot, tt.stage); got != tt.lamp {
			t.Errorf("%s %s lamp = %q, want %q", tt.depot, tt.stage, got, tt.lamp)
		}
	}
	if got := flowBeads(board.Depots[0].Elsewhere); !reflect.DeepEqual(got, []string{"stray"}) {
		t.Errorf("bare elsewhere = %v, want the stranded bead", got)
	}
	stranded, err := y.Stranded(ctx, "")
	must(err)
	if len(stranded) != 1 || stranded[0].Bead != "stray" {
		t.Errorf("stranded = %+v, want the stray bead alone", stranded)
	}
	// Asked of the operator, the quiet bead is the person's turn.
	_, err = y.AddAsk(ctx, solo.ID, Ask{Question: "Ship it?", To: "op", Context: []string{"the release is green."}}, "yard")
	must(err)
	board, err = y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := stageLamp(board, "quiet", "new"); got != "turn" {
		t.Errorf("quiet new lamp = %q after the operator's ask, want turn", got)
	}
}

// A dependency-blocked bead lights the same stage fault lamp as a held bead.
func TestFlowsDependencyBlockLightsFault(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "shop", "code", "backlog", "new", "done")
	blocker := y.travel(t, "blocker", "shop", "backlog")
	target := y.travel(t, "target", "shop", "new")
	if err := y.AddDep(ctx, blocker.ID, target.ID, DepBlocks, "", "", "me"); err != nil {
		t.Fatal(err)
	}

	board, err := y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, depot := range board.Depots {
		if depot.Name != "shop" {
			continue
		}
		for _, flow := range depot.Flows {
			for _, stage := range flow.Stages {
				if stage.Stage != "new" {
					continue
				}
				if stage.Lamp != "fault" {
					t.Fatalf("new lamp = %q, want fault", stage.Lamp)
				}
				if len(stage.Beads) != 1 {
					t.Fatalf("new beads = %+v, want target only", stage.Beads)
				}
				got := stage.Beads[0]
				if got.Bead != target.ID || got.Remark != "blocked by "+blocker.ID || got.Tone != "amber" || got.Expected != "—" {
					t.Fatalf("blocked flow row = %+v", got)
				}
				return
			}
		}
	}
	t.Fatal("shop flow has no new stage")
}

// TestFlowsWorking pins the working mark: an agent's session at work, not a
// manual group's claim.
func TestFlowsWorking(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	ctx := context.Background()
	y.flowDepot(t, "shop", "code", "new", "review")
	run := y.travel(t, "run", "shop", "new")
	sess, err := y.Claim(ctx, run.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.MarkRunning(ctx, sess.ID, map[string]any{"agent": "run-new"}); err != nil {
		t.Fatal(err)
	}
	held := y.travel(t, "byhand", "shop", "new")
	if _, err := y.Claim(ctx, held.ID, "deciders", "new", "nell"); err != nil {
		t.Fatal(err)
	}
	idle := y.travel(t, "idle", "shop", "new")
	_ = idle
	board, err := y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	working := map[string]bool{}
	for _, b := range board.Depots[0].Flows[0].Stages[0].Beads {
		working[b.Bead] = b.Working
	}
	want := map[string]bool{"run": true, "byhand": false, "idle": false}
	if !reflect.DeepEqual(working, want) {
		t.Errorf("working = %v, want %v", working, want)
	}
}

// TestFlowsRowsMatchArrivals pins the reuse: every travelling bead's served
// expected, remark, tone and holder are the board's.
func TestFlowsRowsMatchArrivals(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	flowsBoard(t, y)
	ctx := context.Background()
	arrivals, err := y.Arrivals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	board, err := y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	type shown struct{ expected, remark, tone, heldBy string }
	rows := map[string]shown{}
	for _, r := range arrivals.Rows {
		rows[r.Bead] = shown{r.Expected, r.Remark, r.Tone, r.HeldBy}
	}
	served := map[string]FlowBead{}
	collect := func(b FlowBead) { served[b.Bead] = b }
	for _, d := range board.Depots {
		for _, f := range d.Flows {
			for _, s := range f.Stages {
				for _, b := range s.Beads {
					collect(b)
				}
			}
		}
		for _, b := range d.Elsewhere {
			collect(b)
		}
	}
	for _, b := range board.Unassigned {
		collect(b)
	}
	for id, b := range served {
		got, ok := rows[id]
		if !ok {
			// The flows draw a depot's backlog beads on their stage;
			// the board travels without them.
			bead, err := y.GetBead(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if bead.Stage != "backlog" {
				t.Errorf("%s is served in the flows and missing from the board", id)
			}
			continue
		}
		want := shown{b.Expected, b.Remark, b.Tone, b.HeldBy}
		if got != want {
			t.Errorf("%s: flows %+v, board %+v", id, want, got)
		}
	}
	for id := range rows {
		if _, ok := served[id]; !ok {
			t.Errorf("%s is on the board and missing from the flows", id)
		}
	}
	for _, r := range arrivals.Rows {
		if r.Line == "" && r.Bead != "wild-new" {
			t.Errorf("board row %s has no line and is not unassigned", r.Bead)
		}
	}
}

// TestFlowsReadError pins the degradation: a depot whose flows cannot be
// read still shows its beads, off the line, and the other depots their
// flows.
func TestFlowsReadError(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(y.PutDepot(ctx, Depot{Name: "bad", Base: "origin/main"}, "me"))
	y.flowDepot(t, "good", "code", "new", "review")
	_, err := y.CreateBead(ctx, NewBead{Title: "lost", ID: "lost", Stage: "new", Depot: "bad"}, "me")
	must(err)
	y.travel(t, "found", "good", "new")
	// The depot breaks after its beads are made: no base resolves in it.
	must(y.PutDepot(ctx, Depot{Name: "bad", Path: filepath.Join(t.TempDir(), "missing"), Base: "origin/main"}, "me"))
	board, err := y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(board.Depots) != 2 {
		t.Fatalf("%d depots, want 2", len(board.Depots))
	}
	bad := board.Depots[0]
	if bad.Name != "bad" || bad.Error == "" || len(bad.Flows) != 0 {
		t.Fatalf("bad depot = %+v, want the error and no flows", bad)
	}
	if got := flowBeads(bad.Elsewhere); !reflect.DeepEqual(got, []string{"lost"}) {
		t.Errorf("bad elsewhere = %v, want the lost bead", got)
	}
	if got := flowBeads(board.Depots[1].Flows[0].Stages[0].Beads); !reflect.DeepEqual(got, []string{"found"}) {
		t.Errorf("good new = %v, want the found bead", got)
	}
}

// TestFlowsEmpty pins the empty yard: zeros and empty rows, never nulls.
func TestFlowsEmpty(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	board, err := y.Flows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if board.Travelling != 0 || board.Depots == nil || board.Unassigned == nil {
		t.Fatalf("empty board = %+v, want zeros and empty rows", board)
	}
}

// TestFlowsTerminal pins the terminal flag: true exactly where the flow says
// entering the stage closes the bead, on flows with a terminal stage and one
// without, and served on every stage.
func TestFlowsTerminal(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	flowsBoard(t, y)
	y.flowDepot(t, "plain", "plain-flow", "new", "review")
	board, err := y.Flows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	terminal := map[string]bool{}
	for _, d := range board.Depots {
		for _, f := range d.Flows {
			for _, s := range f.Stages {
				terminal[d.Name+"/"+f.Name+"/"+s.Stage] = s.Terminal
				// Served on every stage, not only where it is true.
				raw, err := json.Marshal(s)
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(raw), `"terminal":`) {
					t.Errorf("%s %s %s serves no terminal flag: %s", d.Name, f.Name, s.Stage, raw)
				}
			}
		}
	}
	want := map[string]bool{
		"shop/code/backlog":       false,
		"shop/code/new":           false,
		"shop/code/review":        false,
		"shop/code/merged":        true,
		"shop/bugs/triage":        false,
		"shop/bugs/fixed":         true,
		"loose/default/inbox":     false,
		"loose/default/done":      true,
		"plain/plain-flow/new":    false,
		"plain/plain-flow/review": false,
	}
	if !reflect.DeepEqual(terminal, want) {
		t.Errorf("terminal flags:\n got %v\nwant %v", terminal, want)
	}
}

// TestFlowsSeats pins the seats each stage serves: a group routed at two
// stages serves one shared yard-wide taken count on both; a stage no route
// matches serves neither field, and a paused group's limit stands as 0.
func TestFlowsSeats(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	y.flowDepot(t, "shop", "code", "new", "review", "done", "siding")
	must(y.PutGroup(ctx, Group{Name: "mothballed", Runner: "herdr", Limit: 1}, "me"))
	paused := 0
	_, err := y.SetGroup(ctx, "mothballed", GroupEdit{Limit: &paused}, "me")
	must(err)
	_, err = y.AddRoute(ctx, Route{Stage: "review", Group: "builders"}, "me")
	must(err)
	_, err = y.AddRoute(ctx, Route{Stage: "done", Group: "mothballed"}, "me")
	must(err)
	// Siding is routed nowhere. Builders (limit 10) works new and review,
	// with a live session at each; the mothballed group is paused, so its
	// stage's bead stands unclaimed.
	run := y.travel(t, "run", "shop", "new")
	if _, err := y.Claim(ctx, run.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	again := y.travel(t, "again", "shop", "review")
	if _, err := y.Claim(ctx, again.ID, "builders", "review", "yard"); err != nil {
		t.Fatal(err)
	}
	y.travel(t, "parked", "shop", "done")
	y.travel(t, "adrift", "shop", "siding")
	board, err := y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	stages := map[string]FlowsStage{}
	for _, d := range board.Depots {
		if d.Name != "shop" {
			continue
		}
		for _, f := range d.Flows {
			for _, s := range f.Stages {
				stages[s.Stage] = s
			}
		}
	}
	deref := func(p *int) (int, bool) {
		if p == nil {
			return 0, false
		}
		return *p, true
	}
	for _, st := range []string{"new", "review"} {
		s := stages[st]
		if s.Group != "builders" {
			t.Errorf("%s group = %q, want builders", st, s.Group)
		}
		if got, ok := deref(s.Limit); !ok || got != 10 {
			t.Errorf("%s limit = %d, %v, want 10, served", st, got, ok)
		}
		if got, ok := deref(s.Taken); !ok || got != 2 {
			t.Errorf("%s taken = %d, %v, want 2, the one yard-wide count on both stages", st, got, ok)
		}
	}
	done := stages["done"]
	if done.Group != "mothballed" {
		t.Errorf("done group = %q, want mothballed", done.Group)
	}
	if got, ok := deref(done.Limit); !ok || got != 0 {
		t.Errorf("done limit = %d, %v, want 0, served: paused, not unrouted", got, ok)
	}
	if got, ok := deref(done.Taken); !ok || got != 0 {
		t.Errorf("done taken = %d, %v, want 0, served", got, ok)
	}
	siding := stages["siding"]
	if siding.Group != "" || siding.Limit != nil || siding.Taken != nil {
		t.Errorf("siding serves group %q limit %v taken %v, want all absent", siding.Group, siding.Limit, siding.Taken)
	}
	raw, err := json.Marshal(siding)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"limit":`) || strings.Contains(string(raw), `"taken":`) {
		t.Errorf("siding serves seats in JSON: %s", raw)
	}
	raw, err = json.Marshal(done)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"limit":0`) || !strings.Contains(string(raw), `"taken":0`) {
		t.Errorf("done omits its zero seats from JSON: %s", raw)
	}
}

// TestFlowsWorkerAndStageEntry pins who works a Flows bead and when it
// entered its stage: a claimed bead serves its worker's name and its stage
// entry, an unclaimed bead serves neither, and a move re-serves the new
// entry. A manual group's claim names no worker, but the stay still has
// its clock. The entries are the stays' clocks, pinned by the fake clock
// at creation and at the move.
func TestFlowsWorkerAndStageEntry(t *testing.T) {
	// Sequential: the package's fake clock and process environment (via newArrivalsTest).
	y := newArrivalsTest(t)
	ctx := context.Background()
	if _, err := y.SetGroup(ctx, "builders", GroupEdit{Set: map[string]any{"kind": "claude", "model": "opus"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := y.PutGroup(ctx, Group{Name: "scripts", Runner: "exec", Config: map[string]any{"cmd": "true"}, Limit: 10}, "me"); err != nil {
		t.Fatal(err)
	}
	y.flowDepot(t, "shop", "code", "new", "review")
	entered := y.now
	run := y.travel(t, "run", "shop", "new")
	sess, err := y.Claim(ctx, run.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.MarkRunning(ctx, sess.ID, map[string]any{"agent": "run-new"}); err != nil {
		t.Fatal(err)
	}
	byhand := y.travel(t, "byhand", "shop", "new")
	if _, err := y.Claim(ctx, byhand.ID, "deciders", "new", "nell"); err != nil {
		t.Fatal(err)
	}
	y.travel(t, "idle", "shop", "new")
	// A move ends the session and starts a new stay: re-claimed, the bead
	// serves the new entry.
	moved := y.travel(t, "moved", "shop", "new")
	if _, err := y.Claim(ctx, moved.ID, "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	y.at(y.now.Add(41 * time.Minute))
	remained := y.now
	if _, err := y.Advance(ctx, moved.ID, "review", Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	msess, err := y.Claim(ctx, moved.ID, "builders", "review", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.MarkRunning(ctx, msess.ID, map[string]any{"agent": "moved-review"}); err != nil {
		t.Fatal(err)
	}
	execd := y.travel(t, "execd", "shop", "new")
	esess, err := y.Claim(ctx, execd.ID, "scripts", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.MarkRunning(ctx, esess.ID, map[string]any{"pid": 1234}); err != nil {
		t.Fatal(err)
	}
	// A session from before kinds were recorded has neither: clear what
	// the claim wrote.
	oldie := y.travel(t, "oldie", "shop", "new")
	osess, err := y.Claim(ctx, oldie.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.MarkRunning(ctx, osess.ID, map[string]any{"agent": "oldie-new"}); err != nil {
		t.Fatal(err)
	}
	if err := y.write(func(w *wtx) error {
		var s Session
		if ok, err := get(w.Tx, bSessions, osess.ID, &s); err != nil {
			return err
		} else if !ok {
			return ErrNotFound
		}
		s.Kind, s.Model = "", ""
		return putSession(w.Tx, &s)
	}); err != nil {
		t.Fatal(err)
	}
	// A bead that pins its flow stands off the line, in Elsewhere.
	stray, err := y.CreateBead(ctx, NewBead{Title: "stray", ID: "stray", Stage: "new", Depot: "shop",
		Metadata: map[string]any{"flow": "code"}}, "me")
	if err != nil {
		t.Fatal(err)
	}
	stsess, err := y.Claim(ctx, stray.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.MarkRunning(ctx, stsess.ID, map[string]any{"agent": "stray-new"}); err != nil {
		t.Fatal(err)
	}
	wild := y.travel(t, "wild", "", "new")
	wsess, err := y.Claim(ctx, wild.ID, "builders", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := y.MarkRunning(ctx, wsess.ID, map[string]any{"agent": "wild-new"}); err != nil {
		t.Fatal(err)
	}
	// The served kind and model are what the claim recorded, not the
	// group's current config.
	if _, err := y.SetGroup(ctx, "builders", GroupEdit{Set: map[string]any{"kind": "codex", "model": "o3"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if g, err := y.GetGroup(ctx, "builders"); err != nil || g.Config["kind"] != "codex" || g.Config["model"] != "o3" {
		t.Fatalf("builders config = %+v, %v, want kind codex model o3", g, err)
	}
	board, err := y.Flows(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]FlowBead{}
	for _, d := range board.Depots {
		for _, f := range d.Flows {
			for _, s := range f.Stages {
				for _, b := range s.Beads {
					rows[b.Bead] = b
				}
			}
		}
		for _, b := range d.Elsewhere {
			rows[b.Bead] = b
		}
	}
	for _, b := range board.Unassigned {
		rows[b.Bead] = b
	}
	entry := func(b FlowBead) (time.Time, bool) {
		if b.EnteredStageAt == nil {
			return time.Time{}, false
		}
		return *b.EnteredStageAt, true
	}
	who := func(b FlowBead) [4]string { return [4]string{b.Worker, b.Group, b.Kind, b.Model} }
	if got := rows["run"].Worker; got != "herdr run-new" {
		t.Errorf("run worker = %q, want the session-list word for its session", got)
	}
	if got := who(rows["run"]); got != [4]string{"herdr run-new", "builders", "claude", "opus"} {
		t.Errorf("run who = %v, want the worker, group, kind and model as recorded", got)
	}
	if got, ok := entry(rows["run"]); !ok || !got.Equal(entered) {
		t.Errorf("run entry = %v, %v, want %v, served", got, ok, entered)
	}
	if got := rows["byhand"].Worker; got != "" {
		t.Errorf("byhand worker = %q, want absent: a manual group's claim names no worker", got)
	}
	if got := who(rows["byhand"]); got != [4]string{} {
		t.Errorf("byhand who = %v, want none: a manual group's claim names no worker", got)
	}
	if got, ok := entry(rows["byhand"]); !ok || !got.Equal(entered) {
		t.Errorf("byhand entry = %v, %v, want %v, served: the stay still has its clock", got, ok, entered)
	}
	if got := rows["idle"]; got.Worker != "" || got.EnteredStageAt != nil {
		t.Errorf("idle serves worker %q entry %v, want neither", got.Worker, got.EnteredStageAt)
	}
	if got := who(rows["idle"]); got != [4]string{} {
		t.Errorf("idle who = %v, want none", got)
	}
	if got := rows["moved"].Worker; got != "herdr moved-review" {
		t.Errorf("moved worker = %q, want herdr moved-review", got)
	}
	if got := who(rows["moved"]); got != [4]string{"herdr moved-review", "builders", "claude", "opus"} {
		t.Errorf("moved who = %v, want the recorded kind and model, not the group's current codex/o3", got)
	}
	if got, ok := entry(rows["moved"]); !ok || !got.Equal(remained) {
		t.Errorf("moved entry = %v, %v, want %v, the move's stay, not the creation's", got, ok, remained)
	}
	if got := who(rows["execd"]); got != [4]string{"exec pid 1234", "scripts", "", ""} {
		t.Errorf("execd who = %v, want group alone: an exec claim serves no kind", got)
	}
	if got := who(rows["oldie"]); got != [4]string{"herdr oldie-new", "builders", "", ""} {
		t.Errorf("oldie who = %v, want group alone: a session recorded without kind or model", got)
	}
	if got := who(rows["stray"]); got != [4]string{"herdr stray-new", "builders", "claude", "opus"} {
		t.Errorf("stray who = %v, want the worker, group, kind and model off the line, in Elsewhere", got)
	}
	if got := who(rows["wild"]); got != [4]string{"herdr wild-new", "builders", "claude", "opus"} {
		t.Errorf("wild who = %v, want the worker, group, kind and model with no depot, unassigned", got)
	}
	raw, err := json.Marshal(rows["idle"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"worker":`) || strings.Contains(string(raw), `"entered_stage_at":`) ||
		strings.Contains(string(raw), `"group":`) || strings.Contains(string(raw), `"kind":`) || strings.Contains(string(raw), `"model":`) {
		t.Errorf("idle serves a worker, an entry or who works it in JSON: %s", raw)
	}
	raw, err = json.Marshal(rows["run"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"worker":"herdr run-new"`) || !strings.Contains(string(raw), `"entered_stage_at":"2099-01-01T12:00:00Z"`) ||
		!strings.Contains(string(raw), `"group":"builders"`) || !strings.Contains(string(raw), `"kind":"claude"`) || !strings.Contains(string(raw), `"model":"opus"`) {
		t.Errorf("run omits its worker, entry or who works it from JSON: %s", raw)
	}
	raw, err = json.Marshal(rows["execd"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"group":"scripts"`) || strings.Contains(string(raw), `"kind":`) || strings.Contains(string(raw), `"model":`) {
		t.Errorf("execd serves %s, want group alone", raw)
	}
}
