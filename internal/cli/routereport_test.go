package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// reportFixture is a yard with recorded events: two herdr groups, one manual
// group, and beads built, sent back, failed and closed through them.
func reportFixture(t *testing.T) store.API {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddUser(ctx, "me", "me"); err != nil {
		t.Fatal(err)
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "me", "me"))
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "opus", Runner: "herdr", Limit: 4}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "muse", Runner: "herdr", Limit: 4}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual"}, "me"))
	for _, r := range []store.Route{
		{Stage: "new", Group: "opus"},
		{Stage: "review", Group: "muse"},
		{Stage: "decide", Group: "people"},
	} {
		_, err := st.AddRoute(ctx, r, "me")
		must(err)
	}
	bead := func(id, title string, labels ...string) {
		t.Helper()
		_, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: title, Depot: "yard", Labels: labels}, "me")
		must(err)
	}
	claim := func(id, group, stage string) string {
		t.Helper()
		s, err := st.Claim(ctx, id, group, stage, "yard")
		must(err)
		return s.ID
	}
	move := func(id, stage string) {
		t.Helper()
		_, err := st.Advance(ctx, id, stage, store.Expect{}, "me")
		must(err)
	}
	close := func(id, reason string) {
		t.Helper()
		_, err := st.CloseBead(ctx, id, reason, store.Expect{}, "me")
		must(err)
	}

	// Built by two groups in new: opus first, sent back from review, then
	// muse. Counts for each.
	bead("yard-b1", "two groups", "difficulty:3", "size:large")
	claim("yard-b1", "opus", "new")
	move("yard-b1", "review")
	move("yard-b1", "new")
	claim("yard-b1", "muse", "new")
	move("yard-b1", "review")
	close("yard-b1", "done")
	// One failed session, then built by the same group.
	bead("yard-b2", "failed once", "difficulty:3")
	s := claim("yard-b2", "opus", "new")
	must(st.EndSession(ctx, s, "failed", "boom"))
	claim("yard-b2", "opus", "new")
	move("yard-b2", "review")
	close("yard-b2", "done")
	// A clean build by the other group.
	bead("yard-b3", "clean", "difficulty:2")
	claim("yard-b3", "muse", "new")
	move("yard-b3", "review")
	close("yard-b3", "done")
	// Never closed: in no row.
	bead("yard-b4", "open", "difficulty:1")
	claim("yard-b4", "opus", "new")
	// Closed without ever a claim: in no row.
	bead("yard-b5", "unclaimed", "difficulty:2")
	close("yard-b5", "done")
	// A manual stage: only --stage decide reports it.
	_, err := st.CreateBead(ctx, store.NewBead{ID: "yard-b6", Title: "decided", Depot: "yard", Stage: "decide", Labels: []string{"difficulty:1"}}, "me")
	must(err)
	claim("yard-b6", "people", "decide")
	move("yard-b6", "new")
	close("yard-b6", "done")
	return st
}

// reportRows runs route report and returns its data rows by group,
// originator and label.
func reportRows(t *testing.T, st store.API, args ...string) (first string, rows map[string][]string) {
	t.Helper()
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.routeReportCmd(), args...)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("route report %v printed %q, want a first line and a header", args, out)
	}
	if h := strings.Fields(lines[1]); !slices.Equal(h, []string{"GROUP", "ORIGINATOR", "LABEL", "BEADS", "SENT", "BACK", "FAILED", "MEDIAN", "BUILD"}) {
		t.Fatalf("route report %v header %q, want the originator beside the group", args, lines[1])
	}
	rows = map[string][]string{}
	for _, l := range lines[2:] {
		if l == "BY HARNESS" {
			break
		}
		f := strings.Fields(l)
		if len(f) < 7 {
			t.Fatalf("route report %v row %q, want 7 fields", args, l)
		}
		// The median is the rest of the line: it reads "under a
		// minute" or "3h 5m" as well as "41m".
		f[6] = strings.Join(f[6:], " ")
		rows[f[0]+"\x00"+f[1]+"\x00"+f[2]] = f[:7]
	}
	return lines[0], rows
}

// reportKinds runs route report --json and returns its BY HARNESS rows by kind.
func reportKinds(t *testing.T, st store.API, args ...string) map[string]routeKindRow {
	t.Helper()
	a := &app{st: st, actor: "me", json: true}
	out, err := run(t, a.routeReportCmd(), args...)
	if err != nil {
		t.Fatal(err)
	}
	var rep routeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json %v = %q: %v", args, out, err)
	}
	rows := map[string]routeKindRow{}
	for _, r := range rep.ByKind {
		rows[r.Kind] = r
	}
	return rows
}

func TestRouteReport(t *testing.T) {
	st := reportFixture(t)
	ctx := context.Background()
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	first, rows := reportRows(t, st)
	if !strings.HasPrefix(first, "events since ") {
		t.Fatalf("first line %q, want it to say from when the log holds events", first)
	}
	// A bead two groups worked in one stage counts for each; the failed
	// session counts for the group that ran it; the open and unclaimed
	// beads count nowhere.
	want := map[string][]string{
		"opus\x00me\x00difficulty:3": {"2", "0.5", "1"},
		"muse\x00me\x00difficulty:3": {"1", "1.0", "0"},
		"muse\x00me\x00difficulty:2": {"1", "0.0", "0"},
		"opus\x00me\x00size:large":   {"1", "1.0", "0"},
		"muse\x00me\x00size:large":   {"1", "1.0", "0"},
		// The open bead, the manual claim and the unclaimed bead are
		// in no default row.
		"opus\x00me\x00difficulty:1":   nil,
		"muse\x00me\x00difficulty:1":   nil,
		"people\x00me\x00difficulty:1": nil,
	}
	for k, w := range want {
		got, ok := rows[k]
		if w == nil {
			if ok {
				t.Errorf("row %q = %v, want no row", k, got)
			}
			continue
		}
		if !ok {
			t.Errorf("no row for %q", k)
			continue
		}
		if got[3] != w[0] || got[4] != w[1] || got[5] != w[2] {
			t.Errorf("row %q = beads %s sent back %s failed %s, want %s %s %s", k, got[3], got[4], got[5], w[0], w[1], w[2])
		}
		if got[6] == "-" {
			t.Errorf("row %q median %q, want a build duration", k, got[6])
		}
	}
	if len(rows) != 5 {
		t.Errorf("%d rows, want 5", len(rows))
	}

	// The report changes nothing.
	if after, err := st.Head(ctx); err != nil || after != head {
		t.Fatalf("head = %d, err = %v, want %d with no new events", after, err, head)
	}
}

// A claim cut short by failure is not a build: the bead a hand moved on
// after its session stalled closes with the failure counted and no build
// sample.
func TestRouteReportFailedClaimHasNoBuild(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddUser(ctx, "me", "me"); err != nil {
		t.Fatal(err)
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "me", "me"))
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "opus", Runner: "herdr", Limit: 4}, "me"))
	_, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "opus"}, "me")
	must(err)
	_, err = st.CreateBead(ctx, store.NewBead{ID: "yard-f1", Title: "stalled then moved by hand", Depot: "yard", Labels: []string{"difficulty:1"}}, "me")
	must(err)
	s, err := st.Claim(ctx, "yard-f1", "opus", "new", "yard")
	must(err)
	// The session stalls: the bead stays claimed, and a member moves it
	// on by hand without another claim.
	must(st.LogDispatch(ctx, "yard-f1", "session_stalled", "yard", map[string]any{"session": s.ID}))
	_, err = st.Advance(ctx, "yard-f1", "review", store.Expect{}, "me")
	must(err)
	_, err = st.CloseBead(ctx, "yard-f1", "done", store.Expect{}, "me")
	must(err)

	_, rows := reportRows(t, st)
	row, ok := rows["opus\x00me\x00difficulty:1"]
	if !ok {
		t.Fatalf("no row for the stalled bead: %v", rows)
	}
	if row[3] != "1" || row[5] != "1" {
		t.Errorf("row = beads %s failed %s, want 1 and 1", row[3], row[5])
	}
	if row[6] != "-" {
		t.Errorf("median build = %q, want no sample: the failed claim is not a build", row[6])
	}
}

func TestRouteReportFlags(t *testing.T) {
	st := reportFixture(t)

	_, rows := reportRows(t, st, "--prefix", "difficulty")
	for k := range rows {
		if !strings.HasPrefix(k, "opus\x00me\x00difficulty:") && !strings.HasPrefix(k, "muse\x00me\x00difficulty:") {
			t.Errorf("row %q with --prefix difficulty", k)
		}
	}
	if len(rows) != 3 {
		t.Errorf("%d rows with --prefix difficulty, want 3", len(rows))
	}

	_, rows = reportRows(t, st, "--prefix", "size")
	// The two beads with no size label have a row of their own per
	// group, under "-"; the failed session counts in its bead's.
	wantSize := map[string][]string{
		"opus\x00me\x00size:large": {"1", "1.0", "0"},
		"muse\x00me\x00size:large": {"1", "1.0", "0"},
		"opus\x00me\x00-":          {"1", "0.0", "1"},
		"muse\x00me\x00-":          {"1", "0.0", "0"},
	}
	if len(rows) != 4 {
		t.Errorf("%d rows with --prefix size, want 4", len(rows))
	}
	for k, w := range wantSize {
		got, ok := rows[k]
		if !ok {
			t.Errorf("no row for %q with --prefix size", k)
			continue
		}
		if got[3] != w[0] || got[4] != w[1] || got[5] != w[2] {
			t.Errorf("row %q = beads %s sent back %s failed %s, want %s %s %s", k, got[3], got[4], got[5], w[0], w[1], w[2])
		}
	}

	// The default stages are the ones a herdr group works; the manual
	// stage reports only when named.
	_, rows = reportRows(t, st, "--stage", "decide")
	if len(rows) != 1 || rows["people\x00me\x00difficulty:1"] == nil {
		t.Errorf("rows for --stage decide = %v, want the one manual claim", rows)
	}
	_, rows = reportRows(t, st, "--stage", "review")
	if len(rows) != 0 {
		t.Errorf("rows for --stage review = %v, want none: nothing was claimed there", rows)
	}

	// --json prints the same rows.
	a := &app{st: st, actor: "me", json: true}
	out, err := run(t, a.routeReportCmd())
	if err != nil {
		t.Fatal(err)
	}
	var rep routeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json = %q: %v", out, err)
	}
	if rep.From == nil || rep.From.IsZero() {
		t.Errorf("--json from = %v, want when the log starts", rep.From)
	}
	if len(rep.Rows) != 5 {
		t.Fatalf("--json has %d rows, want 5", len(rep.Rows))
	}
	_, text := reportRows(t, st)
	for _, r := range rep.Rows {
		f, ok := text[r.Group+"\x00"+r.Originator+"\x00"+r.Label]
		if !ok {
			t.Errorf("--json row %+v has no text row", r)
			continue
		}
		if r.Originator != "me" {
			t.Errorf("--json row %+v names originator %q, want me", r, r.Originator)
		}
		if f[3] != strconv.Itoa(r.Beads) || f[5] != strconv.Itoa(r.Failed) {
			t.Errorf("--json row %+v differs from text row %v", r, f)
		}
		median := "-"
		if r.MedianBuild != nil {
			median = *r.MedianBuild
		}
		if f[6] != median {
			t.Errorf("--json row %+v median differs from text row %v", r, f)
		}
	}
}

// The report attributes closed beads to originators: one row per group,
// originator and label, and --originator reads one person's beads.
func TestRouteReportOriginator(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	must(st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	if _, err := st.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "jo"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "claude"}}, "jo"))
	_, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "jo")
	must(err)
	build := func(id, title, for_ string) {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: title, Depot: "yard", Labels: []string{"difficulty:1"}, For: for_}, "jo")
		must(err)
		_, err = st.Claim(ctx, b.ID, "builders", b.Stage, "jo")
		must(err)
		_, err = st.Advance(ctx, b.ID, "review", store.Expect{}, "jo")
		must(err)
		_, err = st.CloseBead(ctx, b.ID, "done", store.Expect{}, "jo")
		must(err)
	}
	build("yard-j1", "jo's", "")
	build("yard-h1", "hal's", "hal")

	_, rows := reportRows(t, st)
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want one per originator", rows)
	}
	for _, org := range []string{"jo", "hal"} {
		row, ok := rows["builders\x00"+org+"\x00difficulty:1"]
		if !ok {
			t.Errorf("no row for %s: %v", org, rows)
			continue
		}
		if row[3] != "1" {
			t.Errorf("row for %s = beads %s, want 1", org, row[3])
		}
	}

	_, rows = reportRows(t, st, "--originator", "hal")
	if len(rows) != 1 || rows["builders\x00hal\x00difficulty:1"] == nil {
		t.Errorf("rows for --originator hal = %v, want hal's row alone", rows)
	}
	_, rows = reportRows(t, st, "--originator", "mallory")
	if len(rows) != 0 {
		t.Errorf("rows for --originator mallory = %v, want none", rows)
	}

	a := &app{st: st, actor: "jo", json: true}
	out, err := run(t, a.routeReportCmd(), "--originator", "hal")
	if err != nil {
		t.Fatal(err)
	}
	var rep routeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json --originator hal = %q: %v", out, err)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].Originator != "hal" || rep.Rows[0].Beads != 1 {
		t.Errorf("--json rows = %+v, want hal's row alone", rep.Rows)
	}
	if len(rep.ByKind) != 1 || rep.ByKind[0].Kind != "claude" || rep.ByKind[0].Sessions != 1 {
		t.Errorf("--json by_kind = %+v, want only hal's claude session", rep.ByKind)
	}
}

// A bead with no stored originator counts under the operator's, and the
// originator filter keeps one person's beads.
func TestSummarizeRouteEventsOriginator(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	bead := "yard-a"
	evs := []store.Event{
		{Seq: 1, Bead: &bead, Kind: "claimed", Data: map[string]any{"group": "g", "stage": "new", "session": "s1"}, At: t0},
		{Seq: 2, Bead: &bead, Kind: "advanced", Data: map[string]any{"from": "new", "to": "review", "group": "g"}, At: t0.Add(10 * time.Minute)},
	}
	beads := map[string]store.Bead{
		"yard-a": {ID: "yard-a", Status: "closed", Labels: []string{"difficulty:3"}},
	}
	o := routeReportOpts{prefixes: []string{"difficulty:"}, stages: map[string]bool{"new": true}, operator: "jo"}
	rep := summarizeRouteEvents(evs, beads, o)
	if len(rep.Rows) != 1 || rep.Rows[0].Originator != "jo" {
		t.Fatalf("rows = %+v, want the bead under the operator's", rep.Rows)
	}
	o.originator = "jo"
	if rep := summarizeRouteEvents(evs, beads, o); len(rep.Rows) != 1 {
		t.Errorf("rows for originator jo = %+v, want the bead", rep.Rows)
	}
	o.originator = "hal"
	if rep := summarizeRouteEvents(evs, beads, o); len(rep.Rows) != 0 {
		t.Errorf("rows for originator hal = %+v, want none", rep.Rows)
	}
}

func TestSummarizeRouteEvents(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ev := func(seq uint64, at time.Time, bead, kind string, data map[string]any) store.Event {
		return store.Event{Seq: seq, Bead: &bead, Kind: kind, Data: data, At: at}
	}
	claimed := func(seq uint64, at time.Time, bead, group, stage, session string) store.Event {
		return ev(seq, at, bead, "claimed", map[string]any{"group": group, "stage": stage, "session": session})
	}
	advanced := func(seq uint64, at time.Time, bead, from, to string) store.Event {
		return ev(seq, at, bead, "advanced", map[string]any{"from": from, "to": to, "group": "g"})
	}
	beads := map[string]store.Bead{
		"yard-a": {ID: "yard-a", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-b": {ID: "yard-b", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-c": {ID: "yard-c", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-d": {ID: "yard-d", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-e": {ID: "yard-e", Status: "open", Labels: []string{"difficulty:3"}},
		"yard-f": {ID: "yard-f", Status: "closed", Labels: []string{"topic:routing"}},
	}
	stages := map[string]bool{"new": true}
	prefixes := []string{"difficulty:"}
	rep := summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		claimed(3, t0, "yard-b", "g", "new", "s2"),
		advanced(4, t0.Add(30*time.Minute), "yard-b", "new", "review"),
		claimed(5, t0, "yard-c", "g", "new", "s3"),
		advanced(6, t0.Add(20*time.Minute), "yard-c", "new", "review"),
		// Sent back twice, then out again; a move within the stage is
		// neither an exit nor a send-back.
		advanced(7, t0.Add(time.Hour), "yard-a", "review", "new"),
		advanced(8, t0.Add(2*time.Hour), "yard-a", "new", "new"),
		claimed(9, t0.Add(2*time.Hour), "yard-a", "g", "new", "s4"),
		advanced(10, t0.Add(3*time.Hour), "yard-a", "review", "new"),
		// A failure, then the reclaim that built it: only the reclaim
		// has a build.
		claimed(11, t0, "yard-d", "g", "new", "s5"),
		ev(12, t0.Add(time.Minute), "yard-d", "session_failed", map[string]any{"session": "s5"}),
		claimed(13, t0.Add(2*time.Minute), "yard-d", "g", "new", "s6"),
		ev(14, t0.Add(3*time.Minute), "yard-d", "session_died", map[string]any{"session": "sx"}),
		ev(15, t0.Add(3*time.Minute), "yard-d", "session_aborted", map[string]any{"session": "s5"}),
		advanced(16, t0.Add(42*time.Minute), "yard-d", "new", "review"),
		// Counted nowhere: an open bead, a claim in another stage, a
		// failure of an unknown session. The bead with no estimate label
		// has its own row, under "-".
		claimed(17, t0, "yard-e", "g", "new", "s7"),
		claimed(18, t0, "yard-f", "g", "new", "s8"),
		claimed(19, t0, "yard-a", "g", "review", "s9"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages})
	if rep.From == nil || !rep.From.Equal(t0) {
		t.Fatalf("from = %v, want the oldest event at %v", rep.From, t0)
	}
	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %+v, want two", rep.Rows)
	}
	r := rep.Rows[1]
	// Four closed beads claimed in new; yard-a sent back twice over four
	// beads; one failure; builds of 10m, 30m, 20m and 40m.
	if r.Group != "g" || r.Label != "difficulty:3" || r.Beads != 4 || r.Failed != 1 {
		t.Fatalf("row = %+v", r)
	}
	if r.SentBack != 0.5 {
		t.Errorf("sent back = %v, want 0.5", r.SentBack)
	}
	if r.MedianBuild == nil || *r.MedianBuild != "25m" {
		t.Errorf("median build = %v, want 25m", r.MedianBuild)
	}
	if u := rep.Rows[0]; u.Group != "g" || u.Label != "-" || u.Beads != 1 || u.Failed != 0 {
		t.Errorf("unlabelled row = %+v, want the one bead with no estimate label", u)
	}

	// A late failure report cannot undo a completed build, and a failure
	// event attached to another bead cannot fail this claim.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "late"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		ev(3, t0.Add(20*time.Minute), "yard-a", "session_failed", map[string]any{"session": "late"}),
		claimed(4, t0, "yard-b", "g", "new", "wrong-bead"),
		advanced(5, t0.Add(20*time.Minute), "yard-b", "new", "review"),
		ev(6, t0.Add(30*time.Minute), "yard-a", "session_stalled", map[string]any{"session": "wrong-bead"}),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages})
	if len(rep.Rows) != 1 {
		t.Fatalf("late failure report rows = %+v, want one", rep.Rows)
	}
	if r := rep.Rows[0]; r.Beads != 2 || r.Failed != 1 || r.MedianBuild == nil || *r.MedianBuild != "15m" {
		t.Errorf("late/mismatched failure row = %+v, want 2 builds (median 15m) and 1 failure", r)
	}

	// A session the yard saw stuck counts as failed, once however many
	// such events it has: blocked then died is one. Idle is a nudge,
	// held the operator's act, and aborted never started: none counts.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		ev(2, t0.Add(time.Minute), "yard-a", "session_blocked", map[string]any{"session": "s1"}),
		ev(3, t0.Add(2*time.Minute), "yard-a", "session_died", map[string]any{"session": "s1"}),
		claimed(4, t0, "yard-b", "g", "new", "s2"),
		ev(5, t0.Add(time.Minute), "yard-b", "session_stalled", map[string]any{"session": "s2"}),
		claimed(6, t0, "yard-c", "g", "new", "s3"),
		ev(7, t0.Add(time.Minute), "yard-c", "session_prompt_gave_up", map[string]any{"session": "s3"}),
		claimed(8, t0, "yard-d", "g", "new", "s4"),
		ev(9, t0.Add(time.Minute), "yard-d", "session_harness_error", map[string]any{"session": "s4"}),
		claimed(10, t0, "yard-a", "g", "new", "s5"),
		ev(11, t0.Add(time.Minute), "yard-a", "session_idle", map[string]any{"session": "s5"}),
		claimed(12, t0, "yard-b", "g", "new", "s6"),
		ev(13, t0.Add(time.Minute), "yard-b", "held", map[string]any{"session": "s6"}),
		claimed(14, t0, "yard-c", "g", "new", "s7"),
		ev(15, t0.Add(time.Minute), "yard-c", "session_aborted", map[string]any{"session": "s7"}),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages})
	if len(rep.Rows) != 1 {
		t.Fatalf("stuck rows = %+v, want one", rep.Rows)
	}
	if r := rep.Rows[0]; r.Beads != 4 || r.Failed != 4 {
		t.Errorf("stuck row = %+v, want four beads and four failed sessions", r)
	}

	// An even number of builds medians to the mean of the middle two.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		claimed(3, t0, "yard-b", "g", "new", "s2"),
		advanced(4, t0.Add(20*time.Minute), "yard-b", "new", "review"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages})
	if got := rep.Rows[0].MedianBuild; got == nil || *got != "15m" {
		t.Errorf("median of two builds = %v, want 15m", got)
	}

	// A first arrival is no send-back, and a move on after one is none
	// either: only review sending the bead back to new counts.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(time.Minute), "yard-a", "new", "review"),
		advanced(3, t0.Add(2*time.Minute), "yard-a", "review", "new"),
		claimed(4, t0.Add(3*time.Minute), "yard-a", "g", "new", "s2"),
		advanced(5, t0.Add(4*time.Minute), "yard-a", "new", "review"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages})
	r = rep.Rows[0]
	if r.Beads != 1 || r.SentBack != 1 {
		t.Errorf("row after a send-back = %+v, want one bead sent back once", r)
	}

	// No exit: no median, but the bead still counts.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages})
	r = rep.Rows[0]
	if r.Beads != 1 || r.SentBack != 0 || r.MedianBuild != nil {
		t.Errorf("row without an exit = %+v, want one bead and no median", r)
	}

	// --since keeps the recent claim and drops the old one; From still
	// says when the log starts.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		claimed(3, t0.Add(48*time.Hour), "yard-b", "g", "new", "s2"),
		advanced(4, t0.Add(48*time.Hour+20*time.Minute), "yard-b", "new", "review"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages, since: t0.Add(24 * time.Hour)})
	if rep.From == nil || !rep.From.Equal(t0) {
		t.Errorf("from with --since = %v, want the log's oldest event at %v", rep.From, t0)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].Beads != 1 {
		t.Fatalf("rows with --since = %+v, want the one recent bead", rep.Rows)
	}
	if got := rep.Rows[0].MedianBuild; got == nil || *got != "20m" {
		t.Errorf("median with --since = %v, want 20m", got)
	}

	if got := normalizeReportPrefixes([]string{"difficulty", " size: ", ""}); len(got) != 2 || got[0] != "difficulty:" || got[1] != "size:" {
		t.Errorf("normalized prefixes = %q", got)
	}

	// --since filters counted events, but retained earlier events still
	// establish that the recent move was a return from a later stage.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(time.Minute), "yard-a", "new", "review"),
		advanced(3, t0.Add(25*time.Hour), "yard-a", "review", "new"),
		claimed(4, t0.Add(25*time.Hour+time.Minute), "yard-a", "g", "new", "s2"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages, since: t0.Add(24 * time.Hour)})
	if len(rep.Rows) != 1 || rep.Rows[0].SentBack != 1 {
		t.Errorf("recent send-back with earlier context = %+v, want one", rep.Rows)
	}

	// Send-backs belong to the group that worked the returned-to stage;
	// the group in the later stage does not inherit that count.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g1", "new", "s1"),
		ev(2, t0.Add(time.Minute), "yard-a", "advanced", map[string]any{"from": "new", "to": "review", "group": "g1"}),
		claimed(3, t0.Add(2*time.Minute), "yard-a", "g2", "review", "s2"),
		ev(4, t0.Add(3*time.Minute), "yard-a", "advanced", map[string]any{"from": "review", "to": "new", "group": "g2"}),
	}, beads, routeReportOpts{prefixes: prefixes, stages: map[string]bool{"new": true, "review": true}})
	if len(rep.Rows) != 2 || rep.Rows[0].SentBack != 1 || rep.Rows[1].SentBack != 0 {
		t.Errorf("stage-specific send-back rows = %+v, want g1=1 and g2=0", rep.Rows)
	}
	if rep.Rows[1].MedianBuild == nil || *rep.Rows[1].MedianBuild != "1m" {
		t.Errorf("review group build = %v, want 1m", rep.Rows[1].MedianBuild)
	}
	if rep := summarizeRouteEvents(nil, beads, routeReportOpts{prefixes: prefixes, stages: stages}); rep.From != nil || len(rep.Rows) != 0 {
		t.Errorf("empty log = %+v, want no from and no rows", rep)
	}
}

// BY HARNESS counts every claim with a kind, open beads included, with the
// same pairing as the first table: sessions ran, hours summed, median,
// stages sorted. A failed claim counts as a session but has no build;
// one without a kind counts nowhere.
func TestSummarizeRouteEventsByKind(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ev := func(seq uint64, at time.Time, bead, kind string, data map[string]any) store.Event {
		return store.Event{Seq: seq, Bead: &bead, Kind: kind, Data: data, At: at}
	}
	claimedKind := func(seq uint64, at time.Time, bead, group, stage, session, kind string) store.Event {
		data := map[string]any{"group": group, "stage": stage, "session": session}
		if kind != "" {
			data["kind"] = kind
		}
		return ev(seq, at, bead, "claimed", data)
	}
	advancedGroup := func(seq uint64, at time.Time, bead, from, to, group string) store.Event {
		return ev(seq, at, bead, "advanced", map[string]any{"from": from, "to": to, "group": group})
	}
	beads := map[string]store.Bead{
		"yard-a": {ID: "yard-a", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-b": {ID: "yard-b", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-c": {ID: "yard-c", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-d": {ID: "yard-d", Status: "open", Labels: []string{"difficulty:3"}},
		"yard-e": {ID: "yard-e", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-f": {ID: "yard-f", Status: "open", Labels: []string{"difficulty:3"}},
	}
	stages := map[string]bool{"new": true, "review": true}
	rep := summarizeRouteEvents([]store.Event{
		claimedKind(1, t0, "yard-a", "g1", "new", "s1", "muse"),
		advancedGroup(2, t0.Add(10*time.Minute), "yard-a", "new", "review", "g1"),
		claimedKind(3, t0, "yard-b", "g2", "new", "s2", "muse"),
		advancedGroup(4, t0.Add(30*time.Minute), "yard-b", "new", "review", "g2"),
		claimedKind(5, t0, "yard-c", "g2", "review", "s3", "claude"),
		advancedGroup(6, t0.Add(20*time.Minute), "yard-c", "review", "new", "g2"),
		claimedKind(7, t0, "yard-d", "g1", "new", "s4", "claude"),
		claimedKind(8, t0, "yard-e", "g1", "new", "s5", "muse"),
		ev(9, t0.Add(time.Minute), "yard-e", "session_failed", map[string]any{"session": "s5"}),
		claimedKind(10, t0.Add(2*time.Minute), "yard-e", "g1", "new", "s6", "muse"),
		advancedGroup(11, t0.Add(42*time.Minute), "yard-e", "new", "review", "g1"),
		claimedKind(12, t0, "yard-f", "people", "new", "s7", ""),
	}, beads, routeReportOpts{prefixes: []string{"difficulty:"}, stages: stages})
	if len(rep.ByKind) != 2 {
		t.Fatalf("by_kind = %+v, want muse and claude", rep.ByKind)
	}
	muse, claude := rep.ByKind[1], rep.ByKind[0]
	if claude.Kind != "claude" || muse.Kind != "muse" {
		t.Fatalf("by_kind = %+v, want claude then muse", rep.ByKind)
	}
	// Muse ran four sessions in new (one failed) with builds of 10m,
	// 30m and 40m; claude ran two, one open without an exit.
	if muse.Sessions != 4 || len(muse.Stages) != 1 || muse.Stages[0] != "new" {
		t.Errorf("muse = %+v, want 4 sessions in new", muse)
	}
	if muse.Hours == nil || *muse.Hours != "1h 20m" {
		t.Errorf("muse hours = %v, want 1h 20m", muse.Hours)
	}
	if muse.Median == nil || *muse.Median != "30m" {
		t.Errorf("muse median = %v, want 30m", muse.Median)
	}
	if claude.Sessions != 2 || len(claude.Stages) != 2 || claude.Stages[0] != "new" || claude.Stages[1] != "review" {
		t.Errorf("claude = %+v, want 2 sessions in new and review", claude)
	}
	if claude.Hours == nil || *claude.Hours != "20m" || claude.Median == nil || *claude.Median != "20m" {
		t.Errorf("claude = %+v, want 20m and 20m", claude)
	}
	// The first table still counts closed beads per group: g1 built a
	// and e, g2 b and c; the open and kindless claims count nowhere.
	if len(rep.Rows) != 2 {
		t.Fatalf("rows = %+v, want g1 and g2", rep.Rows)
	}

	// --since keeps the recent claim alone in both tables; From still
	// says when the log starts.
	rep = summarizeRouteEvents([]store.Event{
		claimedKind(1, t0, "yard-a", "g1", "new", "s1", "muse"),
		advancedGroup(2, t0.Add(10*time.Minute), "yard-a", "new", "review", "g1"),
		claimedKind(3, t0.Add(48*time.Hour), "yard-b", "g2", "new", "s2", "muse"),
		advancedGroup(4, t0.Add(48*time.Hour+20*time.Minute), "yard-b", "new", "review", "g2"),
	}, beads, routeReportOpts{prefixes: []string{"difficulty:"}, stages: stages, since: t0.Add(24 * time.Hour)})
	if rep.From == nil || !rep.From.Equal(t0) {
		t.Errorf("from with --since = %v, want %v", rep.From, t0)
	}
	if len(rep.ByKind) != 1 || rep.ByKind[0].Sessions != 1 {
		t.Fatalf("by_kind with --since = %+v, want the one recent session", rep.ByKind)
	}
	if got := rep.ByKind[0]; got.Hours == nil || *got.Hours != "20m" || got.Median == nil || *got.Median != "20m" {
		t.Errorf("recent kind row = %+v, want 20m and 20m", got)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].Beads != 1 {
		t.Errorf("rows with --since = %+v, want the one recent bead", rep.Rows)
	}

	// No exit: sessions count, hours and median stay nil.
	rep = summarizeRouteEvents([]store.Event{
		claimedKind(1, t0, "yard-d", "g1", "new", "s4", "claude"),
	}, beads, routeReportOpts{prefixes: []string{"difficulty:"}, stages: stages})
	if len(rep.ByKind) != 1 || rep.ByKind[0].Sessions != 1 || rep.ByKind[0].Hours != nil || rep.ByKind[0].Median != nil {
		t.Errorf("kind row without an exit = %+v, want one session and no times", rep.ByKind)
	}
	if rep := summarizeRouteEvents(nil, beads, routeReportOpts{prefixes: []string{"difficulty:"}, stages: stages}); rep.ByKind == nil || len(rep.ByKind) != 0 {
		t.Errorf("empty log by_kind = %+v, want an empty list", rep.ByKind)
	}
}

// `yardr route report --since 40h` prints BY HARNESS with the kinds the
// yard runs, and its sessions match a hand count over `events --since 40h
// --kind claimed --json`.
func TestRouteReportByKind(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "muse-builders", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "muse"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "claude-builders", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "claude"}}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "people", Runner: "manual"}, "me"))
	for _, r := range []store.Route{
		{Stage: "new", Group: "muse-builders"},
		{Stage: "review", Group: "claude-builders"},
		{Stage: "decide", Group: "people"},
	} {
		_, err := st.AddRoute(ctx, r, "me")
		must(err)
	}
	bead := func(id string, labels ...string) {
		t.Helper()
		_, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: "yard", Labels: labels}, "me")
		must(err)
	}
	claim := func(id, group, stage string) {
		t.Helper()
		_, err := st.Claim(ctx, id, group, stage, "yard")
		must(err)
	}
	move := func(id, stage string) {
		t.Helper()
		_, err := st.Advance(ctx, id, stage, store.Expect{}, "me")
		must(err)
	}
	close := func(id string) {
		t.Helper()
		_, err := st.CloseBead(ctx, id, "done", store.Expect{}, "me")
		must(err)
	}

	bead("yard-b1", "difficulty:1")
	claim("yard-b1", "muse-builders", "new")
	move("yard-b1", "review")
	close("yard-b1")
	bead("yard-b2", "difficulty:1")
	claim("yard-b2", "muse-builders", "new")
	move("yard-b2", "review")
	close("yard-b2")
	// Open in new after a claude review: counts for BY HARNESS, not
	// for the closed-beads table.
	bead("yard-b3", "difficulty:1")
	move("yard-b3", "review")
	claim("yard-b3", "claude-builders", "review")
	move("yard-b3", "new")
	bead("yard-b4", "difficulty:1")
	claim("yard-b4", "claude-builders", "new")
	move("yard-b4", "review")
	close("yard-b4")
	// Manual work in new: closed beads count for its group, but with
	// no kind it is in no harness row.
	bead("yard-b5", "difficulty:1")
	claim("yard-b5", "people", "new")
	move("yard-b5", "review")
	close("yard-b5")

	// The text prints the second table with the kinds this yard runs.
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.routeReportCmd(), "--since", "40h")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"BY HARNESS", "KIND", "muse", "claude", "new, review"} {
		if !strings.Contains(out, want) {
			t.Errorf("route report --since 40h lacks %q:\n%s", want, out)
		}
	}

	kinds := reportKinds(t, st, "--since", "40h")
	if len(kinds) != 2 {
		t.Fatalf("by_kind = %v, want muse and claude", kinds)
	}
	muse, claude := kinds["muse"], kinds["claude"]
	if muse.Sessions != 2 || len(muse.Stages) != 1 || muse.Stages[0] != "new" {
		t.Errorf("muse = %+v, want 2 sessions in new", muse)
	}
	if claude.Sessions != 2 || len(claude.Stages) != 2 {
		t.Errorf("claude = %+v, want 2 sessions in new and review", claude)
	}
	if muse.Hours == nil || muse.Median == nil || claude.Hours == nil || claude.Median == nil {
		t.Errorf("by_kind times = %+v %+v, want measured hours and medians", muse, claude)
	}

	// The sessions match a hand count over the claimed events.
	aj := &app{st: st, actor: "me", json: true}
	out, err = run(t, aj.eventsCmd(), "--since", "40h", "--kind", "claimed")
	if err != nil {
		t.Fatal(err)
	}
	var claimed []store.Event
	if err := json.Unmarshal([]byte(out), &claimed); err != nil {
		t.Fatalf("events --since 40h --kind claimed = %q: %v", out, err)
	}
	counted := map[string]int{}
	for _, e := range claimed {
		kind, _ := e.Data["kind"].(string)
		if kind == "" {
			continue
		}
		counted[kind]++
	}
	for kind, row := range kinds {
		if counted[kind] != row.Sessions {
			t.Errorf("%s sessions = %d, hand count = %d", kind, row.Sessions, counted[kind])
		}
	}
	if len(counted) != len(kinds) {
		t.Errorf("hand count kinds = %v, by_kind = %v", counted, kinds)
	}
}
