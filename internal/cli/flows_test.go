package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestFlowViews(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "manual"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"mckean", "yardmaster"}}, "me"))
	depot, decision := "yard", "decision"
	_, err := st.AddRoute(ctx, store.Route{Stage: "new", Depot: &depot, Group: "builders"}, "me")
	must(err)
	_, err = st.AddRoute(ctx, store.Route{Stage: "open", Type: &decision, Group: "deciders"}, "me")
	must(err)

	f, err := store.ParseFlow("new -> review\nreview -> merged | new\nmerged terminal")
	must(err)
	f.Name = "code"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "code", "me"))
	d, err := store.ParseFlow("open -> done\ndone terminal")
	must(err)
	d.Name = "decision"
	_, err = st.PutFlow(ctx, *d, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "decision", "decision", "me"))

	for _, nb := range []store.NewBead{
		{Title: "a", Depot: "yard"}, {Title: "b", Depot: "yard"}, {Title: "c", Depot: "yard", Stage: "review"},
		{Title: "q", Depot: "yard", Type: "decision", Stage: "open"},
	} {
		_, err := st.CreateBead(ctx, nb, "me")
		must(err)
	}

	views, err := flowViews(ctx, st, "yard", "")
	must(err)
	if len(views) != 2 || views[0].Type != "" || views[1].Type != "decision" || views[1].Source.String() != "store, type decision" {
		t.Fatalf("views = %+v", views)
	}
	rv := views[0]
	want := []stageView{
		{Stage: "new", Group: "builders", Next: []string{"review"}, Open: 2},
		{Stage: "review", Next: []string{"merged", "new"}, Open: 1},
		{Stage: "merged", Next: []string{}, Terminal: true},
	}
	for i, w := range want {
		g := rv.Stages[i]
		if g.Stage != w.Stage || g.Group != w.Group || !slices.Equal(g.Next, w.Next) || g.Terminal != w.Terminal || g.Open != w.Open {
			t.Errorf("stage %d = %+v, want %+v", i, g, w)
		}
	}
	// review has no route; the terminal stage needs none.
	if len(rv.Warnings) != 1 || rv.Warnings[0] != "stage review has no route: beads there wait for a human" {
		t.Errorf("warnings = %v", rv.Warnings)
	}
	// The decision bead counts under its own flow, not the depot's.
	dv := views[1]
	if dv.Stages[0].Group != "deciders" || dv.Stages[0].Open != 1 || len(dv.Warnings) != 0 {
		t.Errorf("decision view = %+v", dv)
	}
	// Human stages list their members; a manual group without members is
	// still human, open to anyone.
	if !dv.Stages[0].Human || !slices.Equal(dv.Stages[0].Members, []string{"mckean", "yardmaster"}) {
		t.Errorf("deciders stage = %+v", dv.Stages[0])
	}
	if !rv.Stages[0].Human || rv.Stages[0].Members != nil || rv.Stages[1].Human {
		t.Errorf("depot stages = %+v", rv.Stages)
	}

	if _, err := flowViews(ctx, st, "nope", ""); err == nil {
		t.Error("unknown depot accepted")
	}

	// flow list: each named flow with what it governs; unused ones govern nothing.
	unused, err := store.ParseFlow("a -> b")
	must(err)
	unused.Name = "spare"
	_, err = st.PutFlow(ctx, *unused, "me")
	must(err)
	var warned bytes.Buffer
	rows, err := flowList(&warned, ctx, st)
	must(err)
	if warned.String() != "" {
		t.Errorf("flow list warned:\n%s", warned.String())
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.Name] = r.Source + ": " + strings.Join(r.Governs, ", ")
	}
	wantRows := map[string]string{"code": "store: depot yard", "decision": "store: depot yard type decision", "spare": "store: "}
	if !maps.Equal(got, wantRows) {
		t.Errorf("flow list = %v, want %v", got, wantRows)
	}
}

// flow show says a siding under the table, a pair of lines for each stage
// whose manual group is one (yardr-tywp): what it folds by and where the
// carrier goes, then what waits there now and when it folds. A group that
// works two stages gets a pair under each with that stage's own count, a
// stage no edge leaves has the warning in place of the second line, and
// --json carries the same.
func TestFlowShowSiding(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	members := []string{"mckean", "yardmaster"}
	// siding fills at 4 beads or 8h, at 6 points: all three at their defaults
	// but the first, which is stated. pairing works two stages and states all
	// three. lonely states one, and works a stage nothing leaves.
	for name, config := range map[string]map[string]any{
		"siding":  {"fold": 4},
		"pairing": {"fold": 3, "fold_after": "2h", "fold_max": 8},
		"lonely":  {"fold_max": 4},
		"people":  nil,
	} {
		must(st.PutGroup(ctx, store.Group{Name: name, Runner: "manual", Members: members, Limit: 9, Config: config}, "me"))
	}
	depot := "yard"
	for stage, group := range map[string]string{"siding": "siding", "triage": "pairing", "ready": "pairing", "dead": "lonely", "backlog": "people"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: &depot, Group: group}, "me")
		must(err)
	}
	f, err := store.ParseFlow("backlog -> siding | triage | dead\nsiding -> ready | backlog\ntriage -> ready\nready -> new on done | backlog on question\nnew -> merged\nmerged terminal")
	must(err)
	f.Name = "code"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "code", "me"))

	newBead := func(stage string, labels ...string) *store.Bead {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: "part", Depot: "yard", Stage: stage, Labels: labels}, "me")
		must(err)
		return b
	}
	// Three wait in the siding, weighing 1, 1 and 2. The first is held by the
	// siding's manual group, as the dispatcher has a manual group hold the
	// beads of its stage: nothing works it, so a fold would take it. The
	// second is held by a person, which a fold would leave. Two wait in
	// triage, weighing 1 and 2. The ready stage of the same group is empty,
	// and one bead waits in dead.
	_, err = st.Claim(ctx, newBead("siding", "size:small").ID, "siding", "siding", "yard")
	must(err)
	held := newBead("siding", "size:small")
	_, err = st.SetHold(ctx, held.ID, true, store.Expect{}, "me")
	must(err)
	newBead("siding", "size:medium")
	newBead("triage", "size:small")
	last := newBead("triage", "size:medium")
	newBead("dead")
	newBead("backlog")

	a := &app{st: st, actor: "me"}
	views, err := flowViews(ctx, st, "yard", "")
	must(err)
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
	sidings := map[string]*sidingView{}
	for _, sv := range views[0].Stages {
		if sv.Siding != nil {
			sidings[sv.Stage] = sv.Siding
		}
	}
	for stage, want := range map[string]sidingView{
		"siding": {Fold: 4, FoldAfter: "8h", FoldMax: 6, To: "ready", Waiting: 3, Points: 4, Stuck: 1},
		"triage": {Fold: 3, FoldAfter: "2h", FoldMax: 8, To: "ready", Waiting: 2, Points: 3},
		"ready":  {Fold: 3, FoldAfter: "2h", FoldMax: 8, To: "new"},
		"dead":   {Fold: 4, FoldAfter: "8h", FoldMax: 4, Waiting: 1, Points: 2, Warning: "dead has fold=4 but no next stage; nothing will fold here"},
	} {
		got := sidings[stage]
		if got == nil {
			t.Errorf("stage %s is no siding in the view", stage)
			continue
		}
		at, shown := got.FoldsAt, *got
		shown.FoldsAt = nil
		if shown != want {
			t.Errorf("siding %s = %+v, want %+v", stage, shown, want)
		}
		// When it folds: the hours after the latest arrival, none with
		// nothing waiting.
		switch {
		case want.Waiting == 0 && at != nil:
			t.Errorf("siding %s folds at %v with nothing waiting", stage, at)
		case want.Waiting > 0 && at == nil:
			t.Errorf("siding %s has no time it folds at", stage)
		}
	}
	if len(sidings) != 4 {
		t.Errorf("sidings = %v, want siding, triage, ready and dead only", slices.Sorted(maps.Keys(sidings)))
	}
	// The clock runs from the latest bead to arrive.
	if at := sidings["triage"].FoldsAt; at == nil || !at.Equal(last.CreatedAt.Add(2*time.Hour)) {
		t.Errorf("triage folds at %v, want 2h after the last to arrive, %v", at, last.CreatedAt)
	}

	// The text: the pairs stand in the flow's order under the table, before
	// any warning of the flow's own.
	out, err := run(t, a.flowCmd(), "show", "yard")
	must(err)
	var backlog, ready string
	for line := range strings.Lines(out) {
		switch {
		case strings.HasPrefix(line, "backlog"):
			backlog = line
		case strings.HasPrefix(line, "ready"):
			ready = line
		}
	}
	if !strings.Contains(backlog, "(back)") {
		t.Errorf("backlog row = %q, want (back) in its NEXT", backlog)
	}
	if strings.Contains(ready, "(back)") {
		t.Errorf("ready row = %q, want no (back)", ready)
	}
	_, lines, found := strings.Cut(out, "(terminal: closes the bead)\n")
	if !found {
		t.Fatalf("flow show printed no table:\n%s", out)
	}
	clock := func(at *time.Time) string { return limits.Clock(*at, time.Now()) }
	want := "siding: folds at 4 beads, or 8h after the latest arrives; at most 6 points; the carrier goes to ready\n" +
		"        now 3 waiting, 4 points, 1 stuck; folds at 4 or at " + clock(sidings["siding"].FoldsAt) + "\n" +
		"triage: folds at 3 beads, or 2h after the latest arrives; at most 8 points; the carrier goes to ready\n" +
		"        now 2 waiting, 3 points; folds at 3 or at " + clock(sidings["triage"].FoldsAt) + "\n" +
		"dead: folds at 4 beads, or 8h after the latest arrives; at most 4 points\n" +
		"warning: dead has fold=4 but no next stage; nothing will fold here\n" +
		"ready: folds at 3 beads, or 2h after the latest arrives; at most 8 points; the carrier goes to new\n" +
		"       now 0 waiting, 0 points; folds at 3\n" +
		"warning: stage new has no route: beads there wait for a human\n"
	if lines != want {
		t.Errorf("under the table:\n%s\nwant\n%s", lines, want)
	}

	// --json carries the same, and no stage that is no siding has the key.
	var shown []struct {
		Stages []map[string]json.RawMessage `json:"stages"`
	}
	must(json.Unmarshal([]byte(mustAs(t, st, "me", "flow", "show", "yard", "--json")), &shown))
	var withKey []string
	for _, sv := range shown[0].Stages {
		if _, ok := sv["siding"]; ok {
			withKey = append(withKey, strings.Trim(string(sv["stage"]), `"`))
		}
	}
	if !slices.Equal(withKey, []string{"siding", "triage", "dead", "ready"}) {
		t.Errorf("stages with a siding in --json: %v", withKey)
	}
	var siding struct {
		Siding map[string]any `json:"siding"`
	}
	for _, sv := range shown[0].Stages {
		if strings.Trim(string(sv["stage"]), `"`) != "siding" {
			continue
		}
		whole, _ := json.Marshal(sv)
		must(json.Unmarshal(whole, &siding))
	}
	for key, want := range map[string]any{"fold": 4.0, "fold_after": "8h", "fold_max": 6.0, "to": "ready", "waiting": 3.0, "points": 4.0, "stuck": 1.0} {
		if siding.Siding[key] != want {
			t.Errorf("--json siding.%s = %v, want %v", key, siding.Siding[key], want)
		}
	}
	if _, ok := siding.Siding["folds_at"].(string); !ok {
		t.Errorf("--json siding.folds_at = %v, want a time", siding.Siding["folds_at"])
	}
}

// The lines under the table put the time a siding folds at as a person reads
// it on the day of now (the clock alone for today, with its day otherwise),
// say nothing of a time when nothing waits, and count in the singular.
func TestSidingLines(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 23, 7, 0, 0, 0, time.Local)
	at := func(day, hour, minute int) *time.Time {
		t := time.Date(2026, 10, day, hour, minute, 0, 0, time.Local)
		return &t
	}
	rows := []struct {
		name string
		view sidingView
		want []string
	}{
		{"today", sidingView{Fold: 4, FoldAfter: "8h", FoldMax: 6, To: "ready", Waiting: 3, Points: 4, FoldsAt: at(23, 9, 40)}, []string{
			"siding: folds at 4 beads, or 8h after the latest arrives; at most 6 points; the carrier goes to ready",
			"        now 3 waiting, 4 points; folds at 4 or at 09:40"}},
		{"another day", sidingView{Fold: 4, FoldAfter: "1d", FoldMax: 6, To: "ready", Waiting: 1, Points: 1, FoldsAt: at(24, 6, 5)}, []string{
			"siding: folds at 4 beads, or 1d after the latest arrives; at most 6 points; the carrier goes to ready",
			"        now 1 waiting, 1 point; folds at 4 or at Sat 24 Oct 06:05"}},
		{"stuck", sidingView{Fold: 2, FoldAfter: "30m", FoldMax: 2, To: "new", Waiting: 2, Points: 2, Stuck: 2, FoldsAt: at(23, 7, 30)}, []string{
			"siding: folds at 2 beads, or 30m after the latest arrives; at most 2 points; the carrier goes to new",
			"        now 2 waiting, 2 points, 2 stuck; folds at 2 or at 07:30"}},
		{"nothing waiting", sidingView{Fold: 4, FoldAfter: "8h", FoldMax: 6, To: "ready"}, []string{
			"siding: folds at 4 beads, or 8h after the latest arrives; at most 6 points; the carrier goes to ready",
			"        now 0 waiting, 0 points; folds at 4"}},
		{"no next stage", sidingView{Fold: 4, FoldAfter: "8h", FoldMax: 6, Waiting: 1, Points: 2, FoldsAt: at(23, 9, 40),
			Warning: "siding has fold=4 but no next stage; nothing will fold here"}, []string{
			"siding: folds at 4 beads, or 8h after the latest arrives; at most 6 points",
			"warning: siding has fold=4 but no next stage; nothing will fold here"}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := sidingLines("siding", &r.view, now); !slices.Equal(got, r.want) {
				t.Errorf("lines =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(r.want, "\n"))
			}
		})
	}
}

// flow set -f - reads the flow from the command's stdin.
func TestFlowSetStdin(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	a := &app{st: st, actor: "me"}
	c := a.flowCmd()
	c.SetIn(strings.NewReader("a -> b\nb terminal\n"))
	if out, err := run(t, c, "set", "pipe", "-f", "-"); err != nil {
		t.Fatalf("flow set -f -: %v\n%s", err, out)
	}
	f, err := st.GetFlow(ctx, "pipe")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Stages, []string{"a", "b"}) {
		t.Fatalf("flow stages = %+v", f.Stages)
	}
}
