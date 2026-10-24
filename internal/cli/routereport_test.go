package cli

import (
	"context"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/runner"
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
// originator and label, with the depot between group and originator
// under --by depot. The row holds the fixed fields, then the wait and
// the median build joined as one ("10m 25m"), then the four token
// columns; both spans read "under a minute" or "3h 5m" as well as "41m",
// so the middle is compared whole.
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
	byDepot := strings.Contains(lines[1], "DEPOT")
	want := []string{"GROUP", "ORIGINATOR", "LABEL", "BEADS", "SENT", "BACK", "AT", "LIMIT", "REVIEW", "FIXED", "SEC", "FIXED", "FAILED", "WAIT", "MEDIAN", "BUILD", "TOKENS/BEAD", "TOKENS", "UNCOUNTED", "PENDING"}
	if byDepot {
		want = []string{"GROUP", "DEPOT", "ORIGINATOR", "LABEL", "BEADS", "SENT", "BACK", "AT", "LIMIT", "REVIEW", "FIXED", "SEC", "FIXED", "FAILED", "WAIT", "MEDIAN", "BUILD", "TOKENS/BEAD", "TOKENS", "UNCOUNTED", "PENDING"}
	}
	if h := strings.Fields(lines[1]); !slices.Equal(h, want) {
		t.Fatalf("route report %v header %q, want the wait beside the median build", args, lines[1])
	}
	n := 3
	if byDepot {
		n = 4
	}
	rows = map[string][]string{}
	for _, l := range lines[2:] {
		if l == "BY HARNESS" {
			break
		}
		if strings.HasPrefix(l, "best ") || strings.HasPrefix(l, "now ") {
			continue
		}
		f := strings.Fields(l)
		if len(f) < n+6+1+4 {
			t.Fatalf("route report %v row %q, want the fixed fields, a middle and four token columns", args, l)
		}
		// The six per-bead values sit between the key and the
		// middle; the four token columns close the row.
		row := append(slices.Clone(f[:n+6]), strings.Join(f[n+6:len(f)-4], " "))
		row = append(row, f[len(f)-4:]...)
		rows[strings.Join(f[:n], "\x00")] = row
	}
	return lines[0], rows
}

// reportBest runs route report and returns its best lines, in order.
func reportBest(t *testing.T, st store.API, args ...string) []string {
	t.Helper()
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.routeReportCmd(), args...)
	if err != nil {
		t.Fatal(err)
	}
	var best []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(l, "best ") {
			best = append(best, l)
		}
	}
	return best
}

// reportNow runs route report and returns its now lines, in order.
func reportNow(t *testing.T, st store.API, args ...string) []string {
	t.Helper()
	a := &app{st: st, actor: "me"}
	out, err := run(t, a.routeReportCmd(), args...)
	if err != nil {
		t.Fatal(err)
	}
	var now []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.HasPrefix(l, "now ") {
			now = append(now, l)
		}
	}
	return now
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
	t.Parallel()
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
	// beads count nowhere. No bead has a review note, so both fix columns
	// read 0.0; no bead reached a round limit, so at limit reads 0.
	want := map[string][]string{
		"opus\x00me\x00difficulty:3": {"2", "0.5", "0", "0.0", "0.0", "1"},
		"muse\x00me\x00difficulty:3": {"1", "1.0", "0", "0.0", "0.0", "0"},
		"muse\x00me\x00difficulty:2": {"1", "0.0", "0", "0.0", "0.0", "0"},
		"opus\x00me\x00size:large":   {"1", "1.0", "0", "0.0", "0.0", "0"},
		"muse\x00me\x00size:large":   {"1", "1.0", "0", "0.0", "0.0", "0"},
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
		if got[3] != w[0] || got[4] != w[1] || got[5] != w[2] || got[6] != w[3] || got[7] != w[4] || got[8] != w[5] {
			t.Errorf("row %q = beads %s sent back %s at limit %s review fixed %s sec fixed %s failed %s, want %s %s %s %s %s %s",
				k, got[3], got[4], got[5], got[6], got[7], got[8], w[0], w[1], w[2], w[3], w[4], w[5])
		}
		if got[9] != "under a minute under a minute" {
			t.Errorf("row %q middle %q, want the wait and the build, each under a minute", k, got[9])
		}
	}
	if len(rows) != 5 {
		t.Errorf("%d rows, want 5", len(rows))
	}

	// The report changes nothing.
	if after, err := st.Head(ctx); err != nil || after != head {
		t.Fatalf("head = %d, err = %v, want %d with no new events", after, err, head)
	}
	testRouteReportTokens(t)
}

// A claim cut short by failure is not a build: the bead a hand moved on
// after its session stalled closes with the failure counted and no build
// sample.
func TestRouteReportFailedClaimHasNoBuild(t *testing.T) {
	t.Parallel()
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
	if row[3] != "1" || row[8] != "1" {
		t.Errorf("row = beads %s failed %s, want 1 and 1", row[3], row[8])
	}
	if row[9] != "under a minute -" {
		t.Errorf("middle = %q, want a wait under a minute and no build: the failed claim is not a build", row[9])
	}
}

func TestRouteReportFlags(t *testing.T) {
	t.Parallel()
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
		"opus\x00me\x00size:large": {"1", "1.0", "0", "0.0", "0.0", "0"},
		"muse\x00me\x00size:large": {"1", "1.0", "0", "0.0", "0.0", "0"},
		"opus\x00me\x00-":          {"1", "0.0", "0", "0.0", "0.0", "1"},
		"muse\x00me\x00-":          {"1", "0.0", "0", "0.0", "0.0", "0"},
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
		if got[3] != w[0] || got[4] != w[1] || got[5] != w[2] || got[6] != w[3] || got[7] != w[4] || got[8] != w[5] {
			t.Errorf("row %q = beads %s sent back %s at limit %s review fixed %s sec fixed %s failed %s, want %s %s %s %s %s %s",
				k, got[3], got[4], got[5], got[6], got[7], got[8], w[0], w[1], w[2], w[3], w[4], w[5])
		}
	}

	// The default stages are the ones a herdr group works; the manual
	// stage reports only when named.
	_, rows = reportRows(t, st, "--stage", "decide")
	if len(rows) != 1 || rows["people\x00me\x00difficulty:1"] == nil {
		t.Errorf("rows for --stage decide = %v, want the one manual claim", rows)
	} else if r := rows["people\x00me\x00difficulty:1"]; r[11] != "0" || r[12] != "0" || r[13] != "0" {
		t.Errorf("manual claim token columns = %q, want 0 tokens, 0 uncounted, 0 pending", r[11:])
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
	if !strings.Contains(out, `"at_limit":`) {
		t.Errorf("--json lacks at_limit: %s", out)
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
		if f[3] != strconv.Itoa(r.Beads) || f[5] != strconv.Itoa(r.AtLimit) || f[8] != strconv.Itoa(r.Failed) {
			t.Errorf("--json row %+v differs from text row %v", r, f)
		}
		if f[6] != strconv.FormatFloat(r.ReviewFixed, 'f', 1, 64) || f[7] != strconv.FormatFloat(r.SecFixed, 'f', 1, 64) {
			t.Errorf("--json row %+v fixes differ from text row %v", r, f)
		}
		wait, median := "-", "-"
		if r.MedianWait != nil {
			wait = *r.MedianWait
		}
		if r.MedianBuild != nil {
			median = *r.MedianBuild
		}
		if f[9] != wait+" "+median {
			t.Errorf("--json row %+v wait and median differ from text row %v", r, f)
		}
		if r.Depot != "" {
			t.Errorf("--json row %+v depot = %q, want empty without --by depot", r, r.Depot)
		}
		if r.MedianWait == nil {
			t.Errorf("--json row %+v median_wait is null, want the built beads' wait", r)
		}
	}
	if !strings.Contains(out, `"median_wait":`) || !strings.Contains(out, `"depot":`) || !strings.Contains(out, `"now":`) {
		t.Errorf("--json lacks the wait, depot or now fields: %s", out)
	}
	// --by takes depot alone.
	if _, err := run(t, a.routeReportCmd(), "--by", "group"); err == nil || !strings.Contains(err.Error(), "--by") {
		t.Errorf("route report --by group err = %v, want a --by refusal", err)
	}
}

// The report attributes closed beads to originators: one row per group,
// originator and label, and --originator reads one person's beads.
func TestRouteReportOriginator(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	rep := summarizeRouteEvents(evs, beads, o, nil)
	if len(rep.Rows) != 1 || rep.Rows[0].Originator != "jo" {
		t.Fatalf("rows = %+v, want the bead under the operator's", rep.Rows)
	}
	o.originator = "jo"
	if rep := summarizeRouteEvents(evs, beads, o, nil); len(rep.Rows) != 1 {
		t.Errorf("rows for originator jo = %+v, want the bead", rep.Rows)
	}
	o.originator = "hal"
	if rep := summarizeRouteEvents(evs, beads, o, nil); len(rep.Rows) != 0 {
		t.Errorf("rows for originator hal = %+v, want none", rep.Rows)
	}
}

func TestSummarizeRouteEvents(t *testing.T) {
	t.Parallel()
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
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
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
	// Only the sent-back bead's second visit has an entry in the log,
	// an hour before its reclaim; the claims without one leave no wait
	// sample.
	if r.MedianWait == nil || *r.MedianWait != "1h" {
		t.Errorf("median wait = %v, want 1h", r.MedianWait)
	}
	if u := rep.Rows[0]; u.Group != "g" || u.Label != "-" || u.Beads != 1 || u.Failed != 0 {
		t.Errorf("unlabelled row = %+v, want the one bead with no estimate label", u)
	}
	if u := rep.Rows[0]; u.MedianWait != nil {
		t.Errorf("unlabelled wait = %v, want no sample: the claim has no entry in the log", u.MedianWait)
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
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
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
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
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
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
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
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
	r = rep.Rows[0]
	if r.Beads != 1 || r.SentBack != 1 {
		t.Errorf("row after a send-back = %+v, want one bead sent back once", r)
	}
	if r.MedianWait == nil || *r.MedianWait != "1m" {
		t.Errorf("wait after a send-back = %v, want 1m: the return's entry to its reclaim", r.MedianWait)
	}

	// No exit: no median, but the bead still counts.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
	r = rep.Rows[0]
	if r.Beads != 1 || r.SentBack != 0 || r.MedianBuild != nil {
		t.Errorf("row without an exit = %+v, want one bead and no median", r)
	}
	if r.MedianWait != nil {
		t.Errorf("wait without an entry = %v, want no sample", r.MedianWait)
	}

	// --since keeps the recent claim and drops the old one; From still
	// says when the log starts.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		claimed(3, t0.Add(48*time.Hour), "yard-b", "g", "new", "s2"),
		advanced(4, t0.Add(48*time.Hour+20*time.Minute), "yard-b", "new", "review"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages, since: t0.Add(24 * time.Hour)}, nil)
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
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages, since: t0.Add(24 * time.Hour)}, nil)
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
		ev(5, t0.Add(4*time.Minute), "yard-a", "closed", map[string]any{"reason": "done"}),
	}, beads, routeReportOpts{prefixes: prefixes, stages: map[string]bool{"new": true, "review": true}}, nil)
	if len(rep.Rows) != 2 || rep.Rows[0].SentBack != 1 || rep.Rows[1].SentBack != 0 {
		t.Errorf("stage-specific send-back rows = %+v, want g1=1 and g2=0", rep.Rows)
	}
	if rep.Rows[1].MedianBuild == nil || *rep.Rows[1].MedianBuild != "1m" {
		t.Errorf("review group build = %v, want 1m", rep.Rows[1].MedianBuild)
	}
	// The wait likewise belongs to the stage the group claimed: the
	// review group's minute from the entry to its claim, and the new
	// group's minute from the return to the close.
	if rep.Rows[1].MedianWait == nil || *rep.Rows[1].MedianWait != "1m" {
		t.Errorf("review group wait = %v, want 1m", rep.Rows[1].MedianWait)
	}
	if rep.Rows[0].MedianWait == nil || *rep.Rows[0].MedianWait != "1m" {
		t.Errorf("new group wait = %v, want 1m", rep.Rows[0].MedianWait)
	}
	// AT LIMIT counts the closed beads a round limit sent on from a
	// stage the row's group claimed the bead in. One bead counts once
	// however many redirects it has; an open bead counts nowhere, as
	// does a redirect back to a stage the group never claimed.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		ev(3, t0.Add(20*time.Minute), "yard-a", store.RoundsReached, map[string]any{"stage": "review", "outcome": "changes", "rounds": 3, "to": "new", "then": "decide"}),
		ev(4, t0.Add(30*time.Minute), "yard-a", store.RoundsReached, map[string]any{"stage": "review", "outcome": "changes", "rounds": 3, "to": "new", "then": "decide"}),
		claimed(5, t0, "yard-b", "g", "new", "s2"),
		advanced(6, t0.Add(10*time.Minute), "yard-b", "new", "review"),
		ev(7, t0.Add(20*time.Minute), "yard-b", store.RoundsReached, map[string]any{"stage": "review", "outcome": "changes", "rounds": 3, "to": "review", "then": "decide"}),
		claimed(8, t0, "yard-e", "g", "new", "s3"),
		ev(9, t0.Add(20*time.Minute), "yard-e", store.RoundsReached, map[string]any{"stage": "review", "outcome": "changes", "rounds": 3, "to": "new", "then": "decide"}),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
	if len(rep.Rows) != 1 {
		t.Fatalf("at-limit rows = %+v, want one", rep.Rows)
	}
	if r := rep.Rows[0]; r.Beads != 2 || r.AtLimit != 1 {
		t.Errorf("at-limit row = %+v, want 2 beads with 1 at the limit", r)
	}

	// A redirect older than the window counts nowhere, as the other
	// columns': yard-a is in the row by its recent claim, but its
	// redirect aged out.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		ev(2, t0.Add(time.Minute), "yard-a", store.RoundsReached, map[string]any{"to": "new"}),
		claimed(3, t0.Add(25*time.Hour), "yard-a", "g", "new", "s2"),
		claimed(4, t0.Add(25*time.Hour), "yard-b", "g", "new", "s3"),
		ev(5, t0.Add(25*time.Hour+time.Minute), "yard-b", store.RoundsReached, map[string]any{"to": "new"}),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages, since: t0.Add(24 * time.Hour)}, nil)
	if len(rep.Rows) != 1 {
		t.Fatalf("at-limit rows with --since = %+v, want one", rep.Rows)
	}
	if r := rep.Rows[0]; r.Beads != 2 || r.AtLimit != 1 {
		t.Errorf("at-limit row with --since = %+v, want 2 beads with the recent redirect alone counted", r)
	}

	// WAIT is the time a bead was ready and unclaimed. Held and
	// blocked time is none of it; a failure adds its wait only when a
	// reclaim follows in the same visit.
	wbeads := map[string]store.Bead{}
	for _, id := range []string{"yard-w1", "yard-w2", "yard-w3", "yard-w4", "yard-w5", "yard-w6", "yard-w7", "yard-w8"} {
		wbeads[id] = store.Bead{ID: id, Status: "closed", Labels: []string{"difficulty:1"}}
	}
	wait := func(evs []store.Event, o routeReportOpts) routeReportRow {
		t.Helper()
		rep := summarizeRouteEvents(evs, wbeads, o, nil)
		if len(rep.Rows) != 1 {
			t.Fatalf("wait rows = %+v, want the one bead's row", rep.Rows)
		}
		return rep.Rows[0]
	}
	// Blocked 30 minutes of a 40-minute wait: WAIT is the 10 ready
	// ones. The store logs the gate's reblocked beside the advance
	// that entered the holding stage, and the unblocked when the last
	// blocker closes.
	r = wait([]store.Event{
		advanced(1, t0, "yard-w1", "backlog", "new"),
		ev(2, t0, "yard-w1", "reblocked", map[string]any{"blockers": []string{"yard-z"}}),
		ev(3, t0.Add(30*time.Minute), "yard-w1", "unblocked", map[string]any{"blockers": []string{"yard-z"}}),
		claimed(4, t0.Add(40*time.Minute), "yard-w1", "g", "new", "s1"),
		advanced(5, t0.Add(50*time.Minute), "yard-w1", "new", "review"),
		ev(6, t0.Add(time.Hour), "yard-w1", "closed", map[string]any{"reason": "done"}),
	}, routeReportOpts{prefixes: prefixes, stages: stages})
	if r.MedianWait == nil || *r.MedianWait != "10m" {
		t.Errorf("wait past a block = %v, want 10m", r.MedianWait)
	}
	if r.MedianBuild == nil || *r.MedianBuild != "10m" {
		t.Errorf("build past a block = %v, want 10m", r.MedianBuild)
	}
	// Held for an hour between two ready spells of 5 minutes: WAIT is
	// 10, the held hour none of it.
	r = wait([]store.Event{
		advanced(1, t0, "yard-w2", "backlog", "new"),
		ev(2, t0.Add(5*time.Minute), "yard-w2", "held", nil),
		ev(3, t0.Add(65*time.Minute), "yard-w2", "unheld", nil),
		claimed(4, t0.Add(70*time.Minute), "yard-w2", "g", "new", "s1"),
		advanced(5, t0.Add(80*time.Minute), "yard-w2", "new", "review"),
		ev(6, t0.Add(90*time.Minute), "yard-w2", "closed", map[string]any{"reason": "done"}),
	}, routeReportOpts{prefixes: prefixes, stages: stages})
	if r.MedianWait == nil || *r.MedianWait != "10m" {
		t.Errorf("wait past a hold = %v, want 10m", r.MedianWait)
	}
	// A failed first session, re-claimed 7 minutes later: WAIT adds
	// those 7 to the 10 before the first claim, and only the reclaim
	// builds.
	r = wait([]store.Event{
		advanced(1, t0, "yard-w3", "backlog", "new"),
		claimed(2, t0.Add(10*time.Minute), "yard-w3", "g", "new", "s1"),
		ev(3, t0.Add(12*time.Minute), "yard-w3", "session_failed", map[string]any{"session": "s1"}),
		claimed(4, t0.Add(19*time.Minute), "yard-w3", "g", "new", "s2"),
		advanced(5, t0.Add(30*time.Minute), "yard-w3", "new", "review"),
		ev(6, t0.Add(time.Hour), "yard-w3", "closed", map[string]any{"reason": "done"}),
	}, routeReportOpts{prefixes: prefixes, stages: stages})
	if r.MedianWait == nil || *r.MedianWait != "17m" {
		t.Errorf("wait past a failure = %v, want 17m", r.MedianWait)
	}
	if r.Failed != 1 {
		t.Errorf("failed past a failure = %d, want 1", r.Failed)
	}
	if r.MedianBuild == nil || *r.MedianBuild != "11m" {
		t.Errorf("build past a failure = %v, want 11m", r.MedianBuild)
	}
	// A failure with no reclaim is no wait: the session may have kept
	// working to the hand move, so only the 5 minutes before the first
	// claim count, and nothing builds.
	r = wait([]store.Event{
		advanced(1, t0, "yard-w4", "backlog", "new"),
		claimed(2, t0.Add(5*time.Minute), "yard-w4", "g", "new", "s1"),
		ev(3, t0.Add(6*time.Minute), "yard-w4", "session_stalled", map[string]any{"session": "s1"}),
		advanced(4, t0.Add(20*time.Minute), "yard-w4", "new", "review"),
		ev(5, t0.Add(30*time.Minute), "yard-w4", "closed", map[string]any{"reason": "done"}),
	}, routeReportOpts{prefixes: prefixes, stages: stages})
	if r.MedianWait == nil || *r.MedianWait != "5m" {
		t.Errorf("wait past an unreclaimed failure = %v, want 5m", r.MedianWait)
	}
	if r.Failed != 1 || r.MedianBuild != nil {
		t.Errorf("unreclaimed failure row = %+v, want 1 failed and no build", r)
	}
	// A release ends the session and the bead is ready again: released
	// at 15, re-claimed at 22, the 7 count without a failure.
	r = wait([]store.Event{
		advanced(1, t0, "yard-w5", "backlog", "new"),
		claimed(2, t0.Add(10*time.Minute), "yard-w5", "g", "new", "s1"),
		ev(3, t0.Add(15*time.Minute), "yard-w5", "released", map[string]any{"reason": "route again"}),
		claimed(4, t0.Add(22*time.Minute), "yard-w5", "g", "new", "s2"),
		advanced(5, t0.Add(30*time.Minute), "yard-w5", "new", "review"),
		ev(6, t0.Add(time.Hour), "yard-w5", "closed", map[string]any{"reason": "done"}),
	}, routeReportOpts{prefixes: prefixes, stages: stages})
	if r.MedianWait == nil || *r.MedianWait != "17m" {
		t.Errorf("wait past a release = %v, want 17m", r.MedianWait)
	}
	// --since clips the wait to the window: an hour's wait starting
	// half an hour before it reads 30m.
	r = wait([]store.Event{
		advanced(1, t0, "yard-w6", "backlog", "new"),
		claimed(2, t0.Add(time.Hour), "yard-w6", "g", "new", "s1"),
		advanced(3, t0.Add(70*time.Minute), "yard-w6", "new", "review"),
		ev(4, t0.Add(80*time.Minute), "yard-w6", "closed", map[string]any{"reason": "done"}),
	}, routeReportOpts{prefixes: prefixes, stages: stages, since: t0.Add(30 * time.Minute)})
	if r.MedianWait == nil || *r.MedianWait != "30m" {
		t.Errorf("wait with --since = %v, want 30m", r.MedianWait)
	}
	// Blocked holds across a move: the bead blocked in new is still
	// blocked in review, where no reblocked is logged, until the gate
	// opens there. Only the review wait belongs to the cell, the one
	// stage the group claimed.
	r = wait([]store.Event{
		advanced(1, t0, "yard-w7", "backlog", "new"),
		ev(2, t0.Add(time.Minute), "yard-w7", "reblocked", map[string]any{"blockers": []string{"yard-z"}}),
		advanced(3, t0.Add(10*time.Minute), "yard-w7", "new", "review"),
		ev(4, t0.Add(20*time.Minute), "yard-w7", "unblocked", map[string]any{"blockers": []string{"yard-z"}}),
		claimed(5, t0.Add(25*time.Minute), "yard-w7", "g", "review", "s1"),
		advanced(6, t0.Add(35*time.Minute), "yard-w7", "review", "approved"),
		ev(7, t0.Add(40*time.Minute), "yard-w7", "closed", map[string]any{"reason": "done"}),
	}, routeReportOpts{prefixes: prefixes, stages: map[string]bool{"new": true, "review": true}})
	if r.MedianWait == nil || *r.MedianWait != "5m" {
		t.Errorf("wait past a carried block = %v, want 5m", r.MedianWait)
	}
	// An unblocked with no reblocked in the log is no block: the bead
	// reads as ready from its entry.
	r = wait([]store.Event{
		advanced(1, t0, "yard-w8", "backlog", "new"),
		ev(2, t0.Add(5*time.Minute), "yard-w8", "unblocked", map[string]any{"blockers": []string{"yard-z"}}),
		claimed(3, t0.Add(10*time.Minute), "yard-w8", "g", "new", "s1"),
		advanced(4, t0.Add(20*time.Minute), "yard-w8", "new", "review"),
		ev(5, t0.Add(30*time.Minute), "yard-w8", "closed", map[string]any{"reason": "done"}),
	}, routeReportOpts{prefixes: prefixes, stages: stages})
	if r.MedianWait == nil || *r.MedianWait != "10m" {
		t.Errorf("wait past a lone unblocked = %v, want 10m", r.MedianWait)
	}
	// Reopening starts a new visit in the bead's existing stage. The
	// closed hour is not part of the review group's wait.
	wbeads["yard-w9"] = store.Bead{ID: "yard-w9", Status: "closed", Labels: []string{"difficulty:1"}}
	r = wait([]store.Event{
		ev(1, t0, "yard-w9", "created", map[string]any{"stage": "review"}),
		claimed(2, t0, "yard-w9", "g", "review", "s1"),
		ev(3, t0.Add(10*time.Minute), "yard-w9", "closed", nil),
		ev(4, t0.Add(time.Hour), "yard-w9", "reopened", nil),
		claimed(5, t0.Add(time.Hour+7*time.Minute), "yard-w9", "g", "review", "s2"),
		advanced(6, t0.Add(time.Hour+8*time.Minute), "yard-w9", "review", "approved"),
		ev(7, t0.Add(time.Hour+9*time.Minute), "yard-w9", "closed", nil),
	}, routeReportOpts{prefixes: prefixes, stages: map[string]bool{"review": true}})
	if r.MedianWait == nil || *r.MedianWait != "7m" {
		t.Errorf("wait after reopening = %v, want 7m", r.MedianWait)
	}

	if rep := summarizeRouteEvents(nil, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil); rep.From != nil || len(rep.Rows) != 0 {
		t.Errorf("empty log = %+v, want no from and no rows", rep)
	}
	testSummarizeRouteEventsTokens(t)
}

// BY HARNESS counts every claim with a kind, open beads included, with the
// same pairing as the first table: sessions ran, hours summed, median,
// stages sorted. A failed claim counts as a session but has no build;
// one without a kind counts nowhere.
func TestSummarizeRouteEventsByKind(t *testing.T) {
	t.Parallel()
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
	}, beads, routeReportOpts{prefixes: []string{"difficulty:"}, stages: stages}, nil)
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
	}, beads, routeReportOpts{prefixes: []string{"difficulty:"}, stages: stages, since: t0.Add(24 * time.Hour)}, nil)
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
	}, beads, routeReportOpts{prefixes: []string{"difficulty:"}, stages: stages}, nil)
	if len(rep.ByKind) != 1 || rep.ByKind[0].Sessions != 1 || rep.ByKind[0].Hours != nil || rep.ByKind[0].Median != nil {
		t.Errorf("kind row without an exit = %+v, want one session and no times", rep.ByKind)
	}
	if rep := summarizeRouteEvents(nil, beads, routeReportOpts{prefixes: []string{"difficulty:"}, stages: stages}, nil); rep.ByKind == nil || len(rep.ByKind) != 0 {
		t.Errorf("empty log by_kind = %+v, want an empty list", rep.ByKind)
	}
}

// `yardr route report --since 40h` prints BY HARNESS with the kinds the
// yard runs, and its sessions match a hand count over `events --since 40h
// --kind claimed --json`.
func TestRouteReportByKind(t *testing.T) {
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

// The fix columns count the commits the review stages' notes name new:
// review's and the security review's. A note counts for its stage by its
// author's agent name or by whoever moved the bead out of the stage; a
// quoted builder SHA never counts, and a re-quote across send-backs counts
// once.
func TestSummarizeRouteEventsReviewFixes(t *testing.T) {
	t.Parallel()
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
	advancedBy := func(seq uint64, at time.Time, bead, from, to, actor string) store.Event {
		e := advanced(seq, at, bead, from, to)
		e.Actor = actor
		return e
	}
	note := func(seq uint64, at time.Time, author, text string) store.Note {
		return store.Note{Author: author, Text: text, At: at, Seq: seq}
	}
	agent := runner.AgentName
	prefixes := []string{"difficulty:"}
	stages := map[string]bool{"new": true}
	beads := map[string]store.Bead{
		"yard-a": {ID: "yard-a", Status: "closed", Labels: []string{"difficulty:3"}},
	}
	row := func(rep *routeReport, group string) routeReportRow {
		t.Helper()
		for _, r := range rep.Rows {
			if r.Group == group {
				return r
			}
		}
		t.Fatalf("no row for %s in %+v", group, rep.Rows)
		return routeReportRow{}
	}
	built := []store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
	}

	// One fix named new, beside a quoted builder SHA: 1.0.
	rep := summarizeRouteEvents(built, beads, routeReportOpts{prefixes: prefixes, stages: stages}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it\ntest: sh .yardr/test @ abc1234: pass"),
			note(2, t0.Add(10*time.Minute), agent("yard-a", "review"), "review fix a4df3d30, on top of abc1234"),
		},
	})
	if r := row(rep, "g"); r.Beads != 1 || r.ReviewFixed != 1 || r.SecFixed != 0 {
		t.Errorf("one fix row = %+v, want beads 1 review 1.0 sec 0.0", r)
	}

	// A review note quoting only the builder's SHA fixes nothing, and
	// neither does a bead with no review note at all.
	rep = summarizeRouteEvents(built, beads, routeReportOpts{prefixes: prefixes, stages: stages}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it @ abc1234"),
			note(2, t0.Add(10*time.Minute), agent("yard-a", "review"), "looks good, verified abc1234"),
		},
	})
	if r := row(rep, "g"); r.ReviewFixed != 0 || r.SecFixed != 0 {
		t.Errorf("quote-only row = %+v, want no fixes", r)
	}
	rep = summarizeRouteEvents(built, beads, routeReportOpts{prefixes: prefixes, stages: stages}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it @ abc1234"),
		},
	})
	if r := row(rep, "g"); r.ReviewFixed != 0 || r.SecFixed != 0 {
		t.Errorf("no review note row = %+v, want no fixes", r)
	}
	// Order is by seq, not by the slice: the builder's earlier note still
	// excludes the SHA.
	rep = summarizeRouteEvents(built, beads, routeReportOpts{prefixes: prefixes, stages: stages}, map[string][]store.Note{
		"yard-a": {
			note(2, t0.Add(10*time.Minute), agent("yard-a", "review"), "review fix abc1234"),
			note(1, t0, agent("yard-a", "new"), "built it @ abc1234"),
		},
	})
	if r := row(rep, "g"); r.ReviewFixed != 0 {
		t.Errorf("unordered notes row = %+v, want no fixes", r)
	}

	// A security note naming a fix: SEC FIXED.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		advanced(3, t0.Add(20*time.Minute), "yard-a", "review", "review-security"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it @ abc1234"),
			note(2, t0.Add(20*time.Minute), agent("yard-a", "review-security"), "Fixed in 38667bcd"),
		},
	})
	if r := row(rep, "g"); r.ReviewFixed != 0 || r.SecFixed != 1 {
		t.Errorf("sec fix row = %+v, want review 0.0 sec 1.0", r)
	}

	// Two stays in review sum their fixes; a SHA both name counts once.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		advanced(3, t0.Add(time.Hour), "yard-a", "review", "new"),
		claimed(4, t0.Add(time.Hour), "yard-a", "g", "new", "s2"),
		advanced(5, t0.Add(2*time.Hour), "yard-a", "new", "review"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it @ abc1234"),
			note(2, t0.Add(30*time.Minute), agent("yard-a", "review"), "review fix a4df3d30"),
			note(3, t0.Add(90*time.Minute), agent("yard-a", "review"), "review fix a4df3d30 and 1122334"),
		},
	})
	if r := row(rep, "g"); r.SentBack != 1 || r.ReviewFixed != 2 {
		t.Errorf("two stays row = %+v, want sent back 1.0 review 2.0", r)
	}

	// A reviewer-group row reads 0.0 for its own stage while the builder
	// row shows both counts; security repairs on beads the group passed
	// still show on its row.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "builders", "new", "s1"),
		advanced(2, t0.Add(10*time.Minute), "yard-a", "new", "review"),
		claimed(3, t0.Add(10*time.Minute), "yard-a", "reviewers", "review", "s2"),
		advanced(4, t0.Add(20*time.Minute), "yard-a", "review", "review-security"),
		advanced(5, t0.Add(30*time.Minute), "yard-a", "review-security", "approved"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: map[string]bool{"new": true, "review": true}}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it @ abc1234"),
			note(2, t0.Add(15*time.Minute), agent("yard-a", "review"), "review fix a4df3d30"),
			note(3, t0.Add(25*time.Minute), agent("yard-a", "review-security"), "Fixed in 38667bcd"),
		},
	})
	if r := row(rep, "builders"); r.ReviewFixed != 1 || r.SecFixed != 1 {
		t.Errorf("builder row = %+v, want review 1.0 sec 1.0", r)
	}
	if r := row(rep, "reviewers"); r.Beads != 1 || r.ReviewFixed != 0 || r.SecFixed != 1 {
		t.Errorf("reviewer row = %+v, want beads 1 review 0.0 sec 1.0", r)
	}

	// --since drops a pre-window review note's fixes, while a pre-window
	// builder note still excludes its SHA.
	since := t0.Add(24 * time.Hour)
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0.Add(25*time.Hour), "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(25*time.Hour+10*time.Minute), "yard-a", "new", "review"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages, since: since}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it @ beef111"),
			note(2, t0.Add(time.Hour), agent("yard-a", "review"), "review fix cafe222"),
			note(3, t0.Add(26*time.Hour), agent("yard-a", "review"), "re-review: beef111 stands, review fix dead333"),
		},
	})
	if r := row(rep, "g"); r.Beads != 1 || r.ReviewFixed != 1 {
		t.Errorf("since row = %+v, want beads 1 review 1.0", r)
	}

	// All-letter hex words are prose, not SHAs; an uppercase SHA counts,
	// and a later lowercase quote of it counts once.
	rep = summarizeRouteEvents(built, beads, routeReportOpts{prefixes: prefixes, stages: stages}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it"),
			note(2, t0.Add(10*time.Minute), agent("yard-a", "review"), "traced deadbeef through the defaced path, review fix A4DF3D30"),
			note(3, t0.Add(20*time.Minute), agent("yard-a", "review"), "confirmed a4df3d30"),
		},
	})
	if r := row(rep, "g"); r.ReviewFixed != 1 {
		t.Errorf("prose row = %+v, want review 1.0", r)
	}

	// A note by whoever moved the bead out of review counts, whenever the
	// move was: the actor attribution reads past --since.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1"),
		advanced(2, t0.Add(time.Hour), "yard-a", "new", "review"),
		advancedBy(3, t0.Add(2*time.Hour), "yard-a", "review", "approved", "mckean"),
		claimed(4, t0.Add(25*time.Hour), "yard-a", "g", "new", "s2"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages, since: since}, map[string][]store.Note{
		"yard-a": {
			note(1, t0, agent("yard-a", "new"), "built it"),
			note(2, t0.Add(25*time.Hour), "mckean", "brakeman review, fixed in beef5ec"),
		},
	})
	if r := row(rep, "g"); r.Beads != 1 || r.ReviewFixed != 1 {
		t.Errorf("manual review row = %+v, want beads 1 review 1.0", r)
	}
}

// Token attribution covers counted, partial, missing and pending agent
// sessions, plus crew counts that join no bead claim.
func testSummarizeRouteEventsTokens(t *testing.T) {
	t.Helper()
	t0 := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	ev := func(seq uint64, at time.Time, bead, kind string, data map[string]any) store.Event {
		return store.Event{Seq: seq, Bead: &bead, Kind: kind, Data: data, At: at}
	}
	claimed := func(seq uint64, at time.Time, bead, group, stage, session, kind string) store.Event {
		data := map[string]any{"group": group, "stage": stage, "session": session}
		if kind != "" {
			data["kind"] = kind
		}
		return ev(seq, at, bead, "claimed", data)
	}
	used := func(seq uint64, at time.Time, bead, session string, usage map[string]any) store.Event {
		return ev(seq, at, bead, "session_usage", map[string]any{
			"session": session, "group": "g", "stage": "new", "kind": "muse", "usage": usage})
	}
	crewUsed := func(seq uint64, at time.Time, member string, usage map[string]any) store.Event {
		return store.Event{Seq: seq, Kind: "crew_usage", At: at,
			Data: map[string]any{"member": member, "usage": usage}}
	}
	count := func(fresh, out, read, write int) map[string]any {
		return map[string]any{"input": fresh + read, "fresh_input": fresh,
			"output": out, "cache_read": read, "cache_write": write}
	}
	beads := map[string]store.Bead{
		"yard-a": {ID: "yard-a", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-b": {ID: "yard-b", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-c": {ID: "yard-c", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-d": {ID: "yard-d", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-e": {ID: "yard-e", Status: "closed", Labels: []string{"difficulty:3"}},
		"yard-f": {ID: "yard-f", Status: "closed", Labels: []string{"difficulty:3"}},
	}
	stages := map[string]bool{"new": true}
	prefixes := []string{"difficulty:"}
	partial := count(200, 100, 2000, 20)
	partial["partial"] = true

	rep := summarizeRouteEvents([]store.Event{
		// yard-a: two sessions, one counted and one partial.
		claimed(1, t0, "yard-a", "g", "new", "s1", "muse"),
		claimed(2, t0, "yard-a", "g", "new", "s2", "muse"),
		used(3, t0.Add(time.Hour), "yard-a", "s1", count(100, 50, 1000, 10)),
		used(4, t0.Add(time.Hour), "yard-a", "s2", partial),
		// yard-b: one counted session, in the socket's numbers.
		claimed(5, t0, "yard-b", "g", "new", "s3", "muse"),
		used(6, t0.Add(time.Hour), "yard-b", "s3", map[string]any{
			"input": float64(300), "fresh_input": float64(300), "output": float64(0),
			"cache_read": float64(0), "cache_write": float64(0)}),
		// yard-c: its session came back missing.
		claimed(7, t0, "yard-c", "g", "new", "s4", "muse"),
		used(8, t0.Add(time.Hour), "yard-c", "s4", map[string]any{"missing": "no file"}),
		// yard-d, worked by another group: one counted, one pending.
		claimed(9, t0, "yard-d", "g2", "new", "s5", "codex"),
		claimed(10, t0, "yard-d", "g2", "new", "s6", "codex"),
		used(11, t0.Add(time.Hour), "yard-d", "s5", count(100, 0, 0, 0)),
		// yard-e, worked by both groups: each cell holds its own.
		claimed(12, t0, "yard-e", "g", "new", "s7", "muse"),
		claimed(13, t0, "yard-e", "g2", "new", "s8", "codex"),
		used(14, t0.Add(time.Hour), "yard-e", "s7", count(100, 0, 0, 0)),
		used(15, t0.Add(time.Hour), "yard-e", "s8", count(200, 0, 0, 0)),
		// Manual claims have session ids but no agent usage is due.
		claimed(16, t0, "yard-f", "people", "new", "manual-session", ""),
		// Crew: one counted and one missing for yardmaster, one for clerk.
		crewUsed(17, t0.Add(time.Hour), "yardmaster", count(1000, 500, 9000, 100)),
		crewUsed(18, t0.Add(time.Hour), "yardmaster", map[string]any{"missing": "no session id"}),
		crewUsed(19, t0.Add(time.Hour), "clerk", count(200, 0, 0, 0)),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
	if len(rep.Rows) != 3 {
		t.Fatalf("rows = %+v, want g, g2 and people", rep.Rows)
	}
	g, g2 := rep.Rows[0], rep.Rows[1]
	if g.Group != "g" || g2.Group != "g2" {
		t.Fatalf("rows = %+v, want g then g2", rep.Rows)
	}
	// New tokens leave cache reads out: 160 + 320 + 300 + 100. The
	// partial and the missing session mark the total a lower bound.
	if g.Tokens != 880 || !g.Partial || g.Uncounted != 1 || g.Pending != 0 {
		t.Errorf("g tokens = %d partial %v uncounted %d pending %d, want 880 true 1 0",
			g.Tokens, g.Partial, g.Uncounted, g.Pending)
	}
	if u := g.Usage; u.Input != 3700 || u.FreshInput != 700 || u.Output != 150 || u.CacheRead != 3000 || u.CacheWrite != 30 {
		t.Errorf("g usage = %+v, want the five counts summed over the counted sessions", u)
	}
	// The median leaves out beads with missing sessions: a at 480 (its
	// partial reading still counts), b at 300, e at 100; c is left out.
	if g.TokensPerBead == nil || *g.TokensPerBead != 300 {
		t.Errorf("g tokens per bead = %v, want 300", g.TokensPerBead)
	}
	// g2 holds its own sessions of d and e: 100 + 200, counted only,
	// with the pending session marked; d is no median bead.
	if g2.Tokens != 300 || g2.Partial || g2.Uncounted != 0 || g2.Pending != 1 {
		t.Errorf("g2 tokens = %d partial %v uncounted %d pending %d, want 300 false 0 1",
			g2.Tokens, g2.Partial, g2.Uncounted, g2.Pending)
	}
	if g2.TokensPerBead == nil || *g2.TokensPerBead != 200 {
		t.Errorf("g2 tokens per bead = %v, want 200", g2.TokensPerBead)
	}
	people := rep.Rows[2]
	if people.Group != "people" || people.Tokens != 0 || people.Partial || people.Uncounted != 0 || people.Pending != 0 || people.TokensPerBead != nil {
		t.Errorf("manual row = %+v, want no usage, pending count or median", people)
	}

	if len(rep.ByKind) != 2 {
		t.Fatalf("by_kind = %+v, want muse and codex", rep.ByKind)
	}
	muse, codex := rep.ByKind[1], rep.ByKind[0]
	if muse.Kind != "muse" || codex.Kind != "codex" {
		t.Fatalf("by_kind = %+v, want codex then muse", rep.ByKind)
	}
	if muse.Sessions != 5 || muse.Tokens != 880 || !muse.Partial || muse.Uncounted != 1 || muse.Pending != 0 {
		t.Errorf("muse = %+v, want 5 sessions, 880 tokens, partial, 1 uncounted", muse)
	}
	if codex.Sessions != 3 || codex.Tokens != 300 || codex.Partial || codex.Uncounted != 0 || codex.Pending != 1 {
		t.Errorf("codex = %+v, want 3 sessions, 300 tokens, 1 pending", codex)
	}

	if len(rep.Crew) != 2 {
		t.Fatalf("crew = %+v, want clerk and yardmaster", rep.Crew)
	}
	clerk, master := rep.Crew[0], rep.Crew[1]
	if clerk.Member != "clerk" || master.Member != "yardmaster" {
		t.Fatalf("crew = %+v, want clerk then yardmaster", rep.Crew)
	}
	if clerk.Sessions != 1 || clerk.Tokens != 200 || clerk.Uncounted != 0 {
		t.Errorf("clerk = %+v, want 1 session of 200 tokens", clerk)
	}
	if master.Sessions != 2 || master.Tokens != 1600 || master.Uncounted != 1 {
		t.Errorf("yardmaster = %+v, want 2 sessions, 1600 tokens, 1 uncounted", master)
	}
	if u := master.Usage; u.FreshInput != 1000 || u.Output != 500 || u.CacheRead != 9000 || u.CacheWrite != 100 {
		t.Errorf("yardmaster usage = %+v, want the counted session's counts", u)
	}

	// --since follows the claim: a count whose claim is out of the
	// window counts nowhere, as one with no claim does; crew keeps the
	// time filter alone.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "s1", "muse"),
		used(2, t0.Add(48*time.Hour), "yard-a", "s1", count(100, 0, 0, 0)),
		used(3, t0.Add(48*time.Hour), "yard-b", "sx", count(200, 0, 0, 0)),
		crewUsed(4, t0, "yardmaster", count(1000, 0, 0, 0)),
		crewUsed(5, t0.Add(48*time.Hour), "clerk", count(200, 0, 0, 0)),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages, since: t0.Add(24 * time.Hour)}, nil)
	if len(rep.Rows) != 0 || len(rep.ByKind) != 0 {
		t.Errorf("rows and by_kind with --since = %+v %+v, want none: the claim is out of the window", rep.Rows, rep.ByKind)
	}
	if len(rep.Crew) != 1 || rep.Crew[0].Member != "clerk" || rep.Crew[0].Tokens != 200 {
		t.Errorf("crew with --since = %+v, want clerk's recent session alone", rep.Crew)
	}

	// A claim with no session id names no session: neither counted nor
	// pending, and its bead takes no part in the median.
	rep = summarizeRouteEvents([]store.Event{
		claimed(1, t0, "yard-a", "g", "new", "", "muse"),
	}, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil)
	if len(rep.Rows) != 1 {
		t.Fatalf("rows = %+v, want the bead's row", rep.Rows)
	}
	if r := rep.Rows[0]; r.Tokens != 0 || r.Uncounted != 0 || r.Pending != 0 || r.TokensPerBead != nil {
		t.Errorf("sessionless row = %+v, want no tokens and no median bead", r)
	}
	if len(rep.ByKind) != 1 {
		t.Fatalf("by_kind = %+v, want the kind's row", rep.ByKind)
	}
	if k := rep.ByKind[0]; k.Sessions != 1 || k.Tokens != 0 || k.Pending != 0 {
		t.Errorf("sessionless kind = %+v, want the session and no tokens", k)
	}
	if rep := summarizeRouteEvents(nil, beads, routeReportOpts{prefixes: prefixes, stages: stages}, nil); rep.Crew == nil || len(rep.Crew) != 0 {
		t.Errorf("empty log crew = %+v, want an empty list", rep.Crew)
	}
}

func TestShortTokens(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		n    int
		want string
	}{
		{0, "0"},
		{827, "827"},
		{999, "999"},
		{1000, "1k"},
		{1040, "1k"},
		{1500, "1.5k"},
		{640000, "640k"},
		{999949, "999.9k"},
		{999950, "1M"},
		{1000000, "1M"},
		{1060000, "1.1M"},
		{3100000, "3.1M"},
		{70640000, "70.6M"},
		{181000000, "181M"},
	} {
		if got := shortTokens(tc.n); got != tc.want {
			t.Errorf("shortTokens(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	if got := lowerBound(shortTokens(880), true, 1, 0); got != "≥880" {
		t.Errorf("lower bound total = %q, want ≥880", got)
	}
	if got := lowerBound(shortTokens(300), false, 0, 1); got != "≥300" {
		t.Errorf("pending total = %q, want ≥300", got)
	}
	if got := lowerBound(shortTokens(200), false, 0, 0); got != "200" {
		t.Errorf("complete total = %q, want 200", got)
	}
}

func TestFixSHAs(t *testing.T) {
	t.Parallel()
	sha40 := "0123456789abcdef0123456789abcdef01234567"
	for _, tc := range []struct {
		text string
		want []string
	}{
		{"review fix a4df3d30", []string{"a4df3d30"}},
		{"Fixed in 38667BCD and a4df3d30", []string{"38667bcd", "a4df3d30"}},
		{"a4df3d30 and A4DF3D30", []string{"a4df3d30"}},
		{"traced deadbeef through the defaced path", nil},
		{"short abc12 and 123456 too", nil},
		{"seven with a digit abcdef0 counts", []string{"abcdef0"}},
		{sha40, []string{sha40}},
		{sha40 + "8", nil},
		{"test: sh .yardr/test @ abc1234: pass", []string{"abc1234"}},
	} {
		if got := fixSHAs(tc.text); !slices.Equal(got, tc.want) {
			t.Errorf("fixSHAs(%q) = %q, want %q", tc.text, got, tc.want)
		}
	}
}

// Under the first table, one line per difficulty names the builder with
// the lowest sent back + repaired per bead, across originators and
// depots; reviewer and shaping groups never win, a group needs ten beads
// to win, and a difficulty with no winner says too few beads to compare.
func TestSummarizeRouteEventsBest(t *testing.T) {
	t.Parallel()
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
	note := func(seq uint64, at time.Time, author, text string) store.Note {
		return store.Note{Author: author, Text: text, At: at, Seq: seq}
	}
	agent := runner.AgentName
	beads := map[string]store.Bead{}
	batch := func(prefix, org string, n int, label string) {
		for i := 0; i < n; i++ {
			id := prefix + "-" + strconv.Itoa(i)
			beads[id] = store.Bead{ID: id, Status: "closed", Originator: org, Labels: []string{label}}
		}
	}
	// g1's twelve span two originators, so the win at 12 proves the
	// cells sum across them; g9's nine would win at 0.0 without the
	// floor, and the shapers' twelve clean beads would win without the
	// shaping exclusion.
	batch("yard-g1-me", "", 6, "difficulty:3")
	batch("yard-g1-hal", "hal", 6, "difficulty:3")
	batch("yard-g2", "", 10, "difficulty:3")
	batch("yard-g9", "", 9, "difficulty:3")
	batch("yard-sh", "", 12, "difficulty:3")
	beads["yard-r1"] = store.Bead{ID: "yard-r1", Status: "closed", Labels: []string{"difficulty:3"}}
	beads["yard-c1"] = store.Bead{ID: "yard-c1", Status: "closed", Labels: []string{"difficulty:3"}}
	beads["yard-s1"] = store.Bead{ID: "yard-s1", Status: "closed", Labels: []string{"difficulty:2"}}
	var evs []store.Event
	seq := uint64(0)
	clean := func(bead, group, stage string) {
		seq++
		evs = append(evs, claimed(seq, t0, bead, group, stage, "s"+strconv.FormatUint(seq, 10)))
		seq++
		out := "review"
		if stage != "new" {
			out = "approved"
		}
		evs = append(evs, advanced(seq, t0.Add(10*time.Minute), bead, stage, out))
	}
	sentBack := func(bead, group string, n int) {
		for i := 0; i < n; i++ {
			seq++
			evs = append(evs, claimed(seq, t0, bead, group, "new", "s"+strconv.FormatUint(seq, 10)))
			seq++
			evs = append(evs, advanced(seq, t0.Add(10*time.Minute), bead, "new", "review"))
			seq++
			evs = append(evs, advanced(seq, t0.Add(time.Hour), bead, "review", "new"))
		}
		seq++
		evs = append(evs, claimed(seq, t0, bead, group, "new", "s"+strconv.FormatUint(seq, 10)))
		seq++
		evs = append(evs, advanced(seq, t0.Add(10*time.Minute), bead, "new", "review"))
	}
	// g1: one bead sent back twice, eleven clean: 2 over 12. g2: three
	// beads sent back once each and one fixed twice: 5 over 10.
	sentBack("yard-g1-me-0", "g1", 2)
	for i := 1; i < 6; i++ {
		clean("yard-g1-me-"+strconv.Itoa(i), "g1", "new")
	}
	for i := 0; i < 6; i++ {
		clean("yard-g1-hal-"+strconv.Itoa(i), "g1", "new")
	}
	for i := 0; i < 3; i++ {
		sentBack("yard-g2-"+strconv.Itoa(i), "g2", 1)
	}
	for i := 3; i < 10; i++ {
		clean("yard-g2-"+strconv.Itoa(i), "g2", "new")
	}
	for i := 0; i < 9; i++ {
		clean("yard-g9-"+strconv.Itoa(i), "g9", "new")
	}
	for i := 0; i < 12; i++ {
		clean("yard-sh-"+strconv.Itoa(i), "shapers", "shape")
	}
	// The reviewers' rows read 0.0 but never win: they claimed only in
	// review and in design's critique, which is review work under
	// another name.
	seq++
	evs = append(evs, claimed(seq, t0, "yard-r1", "reviewers", "review", "s"+strconv.FormatUint(seq, 10)))
	seq++
	evs = append(evs, claimed(seq, t0, "yard-c1", "reviewers", "critique", "s"+strconv.FormatUint(seq, 10)))
	seq++
	evs = append(evs, claimed(seq, t0, "yard-s1", "reviewers", "review", "s"+strconv.FormatUint(seq, 10)))
	rep := summarizeRouteEvents(evs, beads, routeReportOpts{
		prefixes: []string{"difficulty:"},
		stages:   map[string]bool{"new": true, "review": true, "critique": true, "shape": true},
		operator: "me",
	}, map[string][]store.Note{
		"yard-g2-9": {
			note(1, t0, agent("yard-g2-9", "new"), "built it"),
			note(2, t0.Add(10*time.Minute), agent("yard-g2-9", "review"), "review fix beef111 and cafe222"),
		},
	})
	if len(rep.best) != 2 {
		t.Fatalf("best = %+v, want the difficulty:2 and difficulty:3 lines", rep.best)
	}
	if b := rep.best[0]; b.label != "difficulty:2" || !b.tooFew {
		t.Errorf("best difficulty:2 = %+v, want too few beads to compare", b)
	}
	if b := rep.best[1]; b.label != "difficulty:3" || b.tooFew || b.group != "g1" || b.perBead != 0.2 || b.beads != 12 {
		t.Errorf("best difficulty:3 = %+v, want g1 at 0.2 over 12 beads", b)
	}
	// The best line is text only: --json carries no best.
	out, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["best"]; ok {
		t.Errorf("--json carries best: %s", out)
	}
}

// The built report counts review and security fixes from the notes, prints
// the best builder per difficulty, and stays silent where only reviewers
// worked.
func TestRouteReportReviewFixed(t *testing.T) {
	t.Parallel()
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
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "herdr", Limit: 4}, "me"))
	for _, r := range []store.Route{
		{Stage: "new", Group: "builders"},
		{Stage: "review", Group: "reviewers"},
		{Stage: "review-security", Group: "reviewers"},
	} {
		_, err := st.AddRoute(ctx, r, "me")
		must(err)
	}
	claim := func(id, group, stage string) {
		t.Helper()
		_, err := st.Claim(ctx, id, group, stage, "me")
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
	noted := func(id, text, author string) {
		t.Helper()
		_, err := st.AddNoted(ctx, id, text, author)
		must(err)
	}
	agent := runner.AgentName

	_, err := st.CreateBead(ctx, store.NewBead{ID: "yard-a", Title: "fixed", Depot: "yard", Labels: []string{"difficulty:3", "size:large"}}, "me")
	must(err)
	claim("yard-a", "builders", "new")
	move("yard-a", "review")
	claim("yard-a", "reviewers", "review")
	move("yard-a", "review-security")
	claim("yard-a", "reviewers", "review-security")
	move("yard-a", "approved")
	close("yard-a")
	noted("yard-a", "built it\ntest: sh .yardr/test @ abc1234: pass", agent("yard-a", "new"))
	noted("yard-a", "review fix a4df3d30, verified abc1234", agent("yard-a", "review"))
	noted("yard-a", "Fixed in 38667bcd", agent("yard-a", "review-security"))

	_, err = st.CreateBead(ctx, store.NewBead{ID: "yard-b", Title: "clean", Depot: "yard", Labels: []string{"difficulty:3"}}, "me")
	must(err)
	claim("yard-b", "builders", "new")
	move("yard-b", "review")
	claim("yard-b", "reviewers", "review")
	move("yard-b", "approved")
	close("yard-b")
	noted("yard-b", "built it @ b1b2b34", agent("yard-b", "new"))
	noted("yard-b", "looks good", agent("yard-b", "review"))

	_, rows := reportRows(t, st)
	want := map[string][]string{
		// beads, sent back, at limit, review fixed, sec fixed, failed
		"builders\x00me\x00difficulty:3":  {"2", "0.0", "0", "0.5", "0.5", "0"},
		"reviewers\x00me\x00difficulty:3": {"2", "0.0", "0", "0.0", "0.5", "0"},
	}
	for k, w := range want {
		got, ok := rows[k]
		if !ok {
			t.Errorf("no row for %q", k)
			continue
		}
		if got[3] != w[0] || got[4] != w[1] || got[5] != w[2] || got[6] != w[3] || got[7] != w[4] || got[8] != w[5] {
			t.Errorf("row %q = beads %s sent back %s at limit %s review fixed %s sec fixed %s failed %s, want %s %s %s %s %s %s",
				k, got[3], got[4], got[5], got[6], got[7], got[8], w[0], w[1], w[2], w[3], w[4], w[5])
		}
	}

	// --json carries the fix columns as review_fixed/sec_fixed.
	a := &app{st: st, actor: "me", json: true}
	out, err := run(t, a.routeReportCmd())
	if err != nil {
		t.Fatal(err)
	}
	var rep routeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json = %q: %v", out, err)
	}
	for _, r := range rep.Rows {
		f, ok := rows[r.Group+"\x00"+r.Originator+"\x00"+r.Label]
		if !ok {
			t.Errorf("--json row %+v has no text row", r)
			continue
		}
		if f[6] != strconv.FormatFloat(r.ReviewFixed, 'f', 1, 64) || f[7] != strconv.FormatFloat(r.SecFixed, 'f', 1, 64) {
			t.Errorf("--json row %+v fixes differ from text row %v", r, f)
		}
	}
	if len(rep.Rows) != len(rows) {
		t.Errorf("--json has %d rows, text has %d", len(rep.Rows), len(rows))
	}

	// Two beads clear no best line: the difficulty says too few beads
	// to compare instead of naming the builder.
	best := reportBest(t, st)
	wantBest := []string{"best difficulty:3: too few beads to compare"}
	if !slices.Equal(best, wantBest) {
		t.Errorf("best = %q, want %q", best, wantBest)
	}

	// Where only reviewers worked, the same: no group is over the
	// floor, so the line says so instead of staying silent.
	best = reportBest(t, st, "--stage", "review")
	if !slices.Equal(best, wantBest) {
		t.Errorf("best for --stage review = %q, want %q", best, wantBest)
	}
	if _, rows := reportRows(t, st, "--stage", "review"); len(rows) == 0 {
		t.Errorf("no rows for --stage review, want the reviewers' rows without a best line")
	}

	// --prefix size keeps the new columns, with the fixes on the large row.
	_, rows = reportRows(t, st, "--prefix", "size")
	wantSize := map[string][]string{
		"builders\x00me\x00size:large":  {"1", "0.0", "0", "1.0", "1.0", "0"},
		"reviewers\x00me\x00size:large": {"1", "0.0", "0", "0.0", "1.0", "0"},
		"builders\x00me\x00-":           {"1", "0.0", "0", "0.0", "0.0", "0"},
		"reviewers\x00me\x00-":          {"1", "0.0", "0", "0.0", "0.0", "0"},
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
		if got[3] != w[0] || got[4] != w[1] || got[5] != w[2] || got[6] != w[3] || got[7] != w[4] || got[8] != w[5] {
			t.Errorf("row %q = beads %s sent back %s at limit %s review fixed %s sec fixed %s failed %s, want %s %s %s %s %s %s",
				k, got[3], got[4], got[5], got[6], got[7], got[8], w[0], w[1], w[2], w[3], w[4], w[5])
		}
	}
}

// --by depot splits one group's row per depot the group's beads are in;
// the best lines still judge the group across depots, and --json carries
// the split.
func TestRouteReportByDepot(t *testing.T) {
	t.Parallel()
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
	must(st.PutDepot(ctx, store.Depot{Name: "web"}, "me"))
	must(st.PutDepot(ctx, store.Depot{Name: "api"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4}, "me"))
	_, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(err)
	for _, depot := range []string{"web", "api"} {
		for i := 1; i <= 5; i++ {
			id := depot + "-b" + strconv.Itoa(i)
			_, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: depot, Labels: []string{"difficulty:1"}}, "me")
			must(err)
			_, err = st.Claim(ctx, id, "builders", "new", "me")
			must(err)
			_, err = st.Advance(ctx, id, "review", store.Expect{}, "me")
			must(err)
			_, err = st.CloseBead(ctx, id, "done", store.Expect{}, "me")
			must(err)
		}
	}

	_, rows := reportRows(t, st)
	if len(rows) != 1 {
		t.Fatalf("rows = %v, want the one builders row", rows)
	}
	row := rows["builders\x00me\x00difficulty:1"]
	if row == nil {
		t.Fatalf("rows = %v, want the builders row", rows)
	}
	if row[3] != "10" || row[9] != "under a minute under a minute" {
		t.Errorf("row = beads %s middle %q, want 10 and the wait and build under a minute", row[3], row[9])
	}
	// Ten beads across two depots win one best line: best judges the
	// group, not the group in a depot.
	wantBest := []string{"best difficulty:1: builders at 0.0 sent back + repaired per bead (10 beads)"}
	if best := reportBest(t, st); !slices.Equal(best, wantBest) {
		t.Errorf("best = %q, want %q", best, wantBest)
	}

	_, split := reportRows(t, st, "--by", "depot")
	if len(split) != 2 {
		t.Fatalf("rows with --by depot = %v, want one per depot", split)
	}
	for _, depot := range []string{"web", "api"} {
		row := split["builders\x00"+depot+"\x00me\x00difficulty:1"]
		if row == nil {
			t.Errorf("no row for %s: %v", depot, split)
			continue
		}
		if row[4] != "5" || row[10] != "under a minute under a minute" {
			t.Errorf("%s row = beads %s middle %q, want 5 and the wait and build under a minute", depot, row[4], row[10])
		}
	}
	if best := reportBest(t, st, "--by", "depot"); !slices.Equal(best, wantBest) {
		t.Errorf("best with --by depot = %q, want %q: the split moves no best line", best, wantBest)
	}

	// --json carries the depot: empty aggregated, named split.
	a := &app{st: st, actor: "me", json: true}
	out, err := run(t, a.routeReportCmd())
	if err != nil {
		t.Fatal(err)
	}
	var rep routeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json = %q: %v", out, err)
	}
	if len(rep.Rows) != 1 || rep.Rows[0].Depot != "" || rep.Rows[0].Beads != 10 {
		t.Errorf("--json rows = %+v, want the one row with no depot", rep.Rows)
	}
	out, err = run(t, a.routeReportCmd(), "--by", "depot")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json --by depot = %q: %v", out, err)
	}
	if len(rep.Rows) != 2 || rep.Rows[0].Depot != "api" || rep.Rows[1].Depot != "web" {
		t.Errorf("--json rows = %+v, want api then web", rep.Rows)
	}
	for _, r := range rep.Rows {
		if r.Beads != 5 || r.MedianWait == nil || r.MedianBuild == nil {
			t.Errorf("--json row %+v, want 5 beads with a wait and a build", r)
		}
	}
}

// Under the best lines, one now line per estimate label in the report
// names the yard default a bead with the label matches today and the
// depots whose own routes set it aside; --json carries the same.
func TestRouteReportNow(t *testing.T) {
	t.Parallel()
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
	must(st.PutDepot(ctx, store.Depot{Name: "web"}, "me"))
	must(st.PutDepot(ctx, store.Depot{Name: "api"}, "me"))
	for _, g := range []string{"heavy", "light", "plain"} {
		must(st.PutGroup(ctx, store.Group{Name: g, Runner: "herdr", Limit: 4}, "me"))
	}
	d1, web, api := "difficulty:1", "web", "api"
	defLabelled, err := st.AddRoute(ctx, store.Route{Stage: "new", Label: &d1, Group: "heavy"}, "me")
	must(err)
	defPlain, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "plain"}, "me")
	must(err)
	_, err = st.AddRoute(ctx, store.Route{Stage: "new", Depot: &web, Label: &d1, Group: "light"}, "me")
	must(err)
	_, err = st.AddRoute(ctx, store.Route{Stage: "new", Depot: &api, Label: &d1, Group: "light"}, "me")
	must(err)
	build := func(id, depot, label string) {
		t.Helper()
		_, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: depot, Labels: []string{label}}, "me")
		must(err)
		_, err = st.Claim(ctx, id, "heavy", "new", "me")
		must(err)
		_, err = st.Advance(ctx, id, "review", store.Expect{}, "me")
		must(err)
		_, err = st.CloseBead(ctx, id, "done", store.Expect{}, "me")
		must(err)
	}
	build("web-n1", "web", "difficulty:1")
	build("web-n2", "web", "difficulty:2")
	build("api-n3", "api", "size:large")

	want := []string{
		"now difficulty:1: default " + defLabelled.ID + " -> heavy (own routes in api, web)",
		"now difficulty:2: default " + defPlain.ID + " -> plain",
		"now size:large: default " + defPlain.ID + " -> plain",
	}
	if now := reportNow(t, st); !slices.Equal(now, want) {
		t.Errorf("now = %q, want %q", now, want)
	}
	// An explicit stage moves no now line: they read current routing,
	// not the window.
	if now := reportNow(t, st, "--stage", "new"); !slices.Equal(now, want) {
		t.Errorf("now with --stage new = %q, want %q", now, want)
	}
	if now := reportNow(t, st, "--stage", "review", "--since", "3650d", "--originator", "nobody"); !slices.Equal(now, want) {
		t.Errorf("now with report filters = %q, want %q: current routing ignores stage, since and originator", now, want)
	}

	a := &app{st: st, actor: "me", json: true}
	out, err := run(t, a.routeReportCmd())
	if err != nil {
		t.Fatal(err)
	}
	var rep routeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json = %q: %v", out, err)
	}
	if len(rep.Now) != 3 {
		t.Fatalf("--json now = %+v, want one line per label", rep.Now)
	}
	n := rep.Now[0]
	if n.Label != "difficulty:1" || n.Route == nil || n.Route.ID != defLabelled.ID ||
		n.Route.Groups != "heavy" || !slices.Equal(n.SetAside, []string{"api", "web"}) {
		t.Errorf("--json now[0] = %+v, want the labelled default set aside in api and web", n)
	}
	n = rep.Now[1]
	if n.Label != "difficulty:2" || n.Route == nil || n.Route.ID != defPlain.ID || len(n.SetAside) != 0 {
		t.Errorf("--json now[1] = %+v, want the plain default with no set-aside", n)
	}

	// Where no default takes the label, the line says so.
	bare := storetest.New(t)
	must(func() error {
		_, err := bare.AddUser(ctx, "me", "me")
		return err
	}())
	must(bare.SetSetting(ctx, store.OperatorSetting, "me", "me"))
	must(bare.PutDepot(ctx, store.Depot{Name: "web"}, "me"))
	must(bare.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "herdr", Limit: 4}, "me"))
	_, err = bare.AddRoute(ctx, store.Route{Stage: "review", Group: "reviewers"}, "me")
	must(err)
	_, err = bare.CreateBead(ctx, store.NewBead{ID: "web-n1", Title: "reviewed", Depot: "web", Stage: "review", Labels: []string{"difficulty:1"}}, "me")
	must(err)
	_, err = bare.Claim(ctx, "web-n1", "reviewers", "review", "me")
	must(err)
	_, err = bare.Advance(ctx, "web-n1", "approved", store.Expect{}, "me")
	must(err)
	_, err = bare.CloseBead(ctx, "web-n1", "done", store.Expect{}, "me")
	must(err)
	if now := reportNow(t, bare); !slices.Equal(now, []string{"now difficulty:1: no default route"}) {
		t.Errorf("now with no default = %q, want the no-default line", now)
	}
	aj := &app{st: bare, actor: "me", json: true}
	out, err = run(t, aj.routeReportCmd())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json = %q: %v", out, err)
	}
	if len(rep.Now) != 1 || rep.Now[0].Label != "difficulty:1" || rep.Now[0].Route != nil {
		t.Errorf("--json now = %+v, want the label with a null route", rep.Now)
	}
}

// routeNowLines matches each label's probe against the defaults alone
// and inverts the set-asides per depot, sorted.
func TestRouteNowLines(t *testing.T) {
	t.Parallel()
	d1, web, api := "difficulty:1", "web", "api"
	routes := []store.Route{
		{ID: "d0", Stage: "new", Group: "plain"},
		{ID: "d1", Stage: "new", Label: &d1, Group: "heavy"},
		{ID: "o1", Stage: "new", Depot: &web, Label: &d1, Group: "light"},
		{ID: "o2", Stage: "new", Depot: &api, Label: &d1, Group: "light"},
	}
	now := routeNowLines(routes, []string{"difficulty:1", "difficulty:2"}, "me")
	if len(now) != 2 {
		t.Fatalf("now = %+v, want two lines", now)
	}
	if now[0].Label != "difficulty:1" || now[0].Route == nil || now[0].Route.ID != "d1" ||
		now[0].Route.Groups != "heavy" || !slices.Equal(now[0].SetAside, []string{"api", "web"}) {
		t.Errorf("now[0] = %+v, want d1 set aside in api and web", now[0])
	}
	if now[1].Label != "difficulty:2" || now[1].Route == nil || now[1].Route.ID != "d0" || len(now[1].SetAside) != 0 {
		t.Errorf("now[1] = %+v, want the plain default with no set-aside", now[1])
	}
	// The probe's depot is none, so own routes never win it: with the
	// plain default gone, difficulty:2 matches nothing.
	now = routeNowLines(routes[1:], []string{"difficulty:2"}, "me")
	if len(now) != 1 || now[0].Route != nil {
		t.Errorf("now without a default = %+v, want a null route", now)
	}
}

// The built report prints token columns from recorded counts: the median
// over beads with no missing or pending sessions, counted-only totals marked
// as lower bounds, and the CREW table and --json details.
func testRouteReportTokens(t *testing.T) {
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
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 4, Config: map[string]any{"kind": "muse"}}, "me"))
	_, err := st.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
	must(err)
	build := func(id string, usage map[string]any) {
		t.Helper()
		_, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: "yard", Labels: []string{"difficulty:1"}}, "me")
		must(err)
		s, err := st.Claim(ctx, id, "builders", "new", "me")
		must(err)
		if usage != nil {
			must(st.RecordUsage(ctx, s.ID, usage))
		}
		_, err = st.Advance(ctx, id, "review", store.Expect{}, "me")
		must(err)
		_, err = st.CloseBead(ctx, id, "done", store.Expect{}, "me")
		must(err)
	}
	count := func(fresh, out, read, write int) map[string]any {
		return map[string]any{"input": fresh + read, "fresh_input": fresh,
			"output": out, "cache_read": read, "cache_write": write}
	}
	build("yard-t1", count(400000, 80000, 9000000, 20000))
	partial := count(100000, 50000, 1000000, 0)
	partial["partial"] = true
	build("yard-t2", partial)
	build("yard-t3", map[string]any{"missing": "no file"})
	build("yard-t4", nil)
	_, err = st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "me")
	must(err)
	must(st.CrewStartedFor(ctx, "yardmaster", "", map[string]any{"agent": "ym-1", "kind": "muse", "agent_session": "a1"}))
	must(st.CrewStoppedFor(ctx, "yardmaster", "", "done"))
	must(st.RecordCrewUsage(ctx, "yardmaster", "", "a1", count(1000, 500, 9000, 100)))
	help, err := run(t, (&app{st: st, actor: "me"}).routeReportCmd(), "--help")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TOKENS/BEAD", "TOKENS", "UNCOUNTED", "PENDING", "BY HARNESS", "CREW", "agent sessions", "fresh input", "cache writes", "output", "WAIT is how long", "--by depot", "DEPOT column", "ten beads at least", "too few beads", "now line"} {
		if !strings.Contains(help, want) {
			t.Errorf("route report help lacks %q:\n%s", want, help)
		}
	}

	a := &app{st: st, actor: "me"}
	out, err := run(t, a.routeReportCmd())
	if err != nil {
		t.Fatal(err)
	}
	// The median over beads with no missing or pending sessions is 325k;
	// the total is
	// counted-only with ≥, one session missing and one pending.
	_, rows := reportRows(t, st)
	row, ok := rows["builders\x00me\x00difficulty:1"]
	if !ok {
		t.Fatalf("no row for the built beads: %v", rows)
	}
	if row[10] != "325k" || row[11] != "≥650k" || row[12] != "1" || row[13] != "1" {
		t.Errorf("token columns = %q, want 325k ≥650k 1 1", row[10:])
	}
	for _, want := range []string{"TOKENS/BEAD", "UNCOUNTED", "PENDING", "CREW", "yardmaster", "1.6k"} {
		if !strings.Contains(out, want) {
			t.Errorf("route report lacks %q:\n%s", want, out)
		}
	}

	aj := &app{st: st, actor: "me", json: true}
	out, err = run(t, aj.routeReportCmd())
	if err != nil {
		t.Fatal(err)
	}
	var rep routeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("route report --json = %q: %v", out, err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("--json rows = %+v, want the one cell", rep.Rows)
	}
	r := rep.Rows[0]
	if r.TokensPerBead == nil || *r.TokensPerBead != 325000 || r.Tokens != 650000 {
		t.Errorf("--json tokens = %v %d, want 325000 650000", r.TokensPerBead, r.Tokens)
	}
	if u := r.Usage; u.Input != 10500000 || u.FreshInput != 500000 || u.Output != 130000 || u.CacheRead != 10000000 || u.CacheWrite != 20000 {
		t.Errorf("--json usage = %+v, want the counts summed over the counted sessions", u)
	}
	if !r.Partial || r.Uncounted != 1 || r.Pending != 1 {
		t.Errorf("--json flags = partial %v uncounted %d pending %d, want true 1 1", r.Partial, r.Uncounted, r.Pending)
	}
	if len(rep.ByKind) != 1 {
		t.Fatalf("--json by_kind = %+v, want the muse row", rep.ByKind)
	}
	if k := rep.ByKind[0]; k.Sessions != 4 || k.Tokens != 650000 || !k.Partial || k.Uncounted != 1 || k.Pending != 1 {
		t.Errorf("--json by_kind = %+v, want 4 sessions, 650000 tokens, partial, 1 uncounted, 1 pending", k)
	}
	if len(rep.Crew) != 1 {
		t.Fatalf("--json crew = %+v, want yardmaster", rep.Crew)
	}
	if c := rep.Crew[0]; c.Member != "yardmaster" || c.Sessions != 1 || c.Tokens != 1600 || c.Uncounted != 0 {
		t.Errorf("--json crew = %+v, want yardmaster's session of 1600 tokens", c)
	}
}
