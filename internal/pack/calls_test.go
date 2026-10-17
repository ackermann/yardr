package pack_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

// crm is the declaration of the design's worked example: two read calls
// and an update.
const crm = `name: crm
about: The customer system.
base: https://crm.example.com/api/v2
credential:
  env: CRM_TOKEN
  header: "Authorization: Bearer {credential}"
check: ping
calls:
  ping: { kind: read, request: { method: GET, path: /me } }
  customer:
    kind: read
    params: { id: { type: string, pattern: "^[0-9]{1,12}$" } }
    request: { method: GET, path: "/customers/{id}" }
  note:
    kind: update
    params: { id: { type: string }, text: { type: string, max: 2000 } }
    request: { method: POST, path: "/customers/{id}/notes", body: { text: "{text}" } }
`

// smallCalls is small with the crm declared, and its builders, its
// script group and its yardmaster each asking for calls of it.
var smallCalls = strings.NewReplacer(
	"  builders: { agent: roles/builder.md, limit: 2 }\n", "  builders: { agent: roles/builder.md, limit: 2, calls: { crm: [read] } }\n",
	"  assembly: { run: scripts/land }\n", "  assembly: { run: scripts/land, calls: { crm: [note] } }\n",
	"    role: roles/yardmaster.md\n", "    role: roles/yardmaster.md\n    calls: { crm: [customer, note] }\n",
).Replace(small) + "sources:\n  crm:\n    declaration: sources/crm.yaml\n"

func callsFiles(yaml, declaration string) map[string]string {
	files := smallFiles(yaml)
	if declaration != "" {
		files["sources/crm.yaml"] = declaration
	}
	return files
}

// asked is what the plan says of the calls of source the pack asks for its
// group or crew member named name.
func asked(t *testing.T, p *pack.Plan, name, source string) pack.Calls {
	t.Helper()
	i := slices.IndexFunc(p.Calls, func(c pack.Calls) bool { return c.Pack == name && c.Source == source })
	if i < 0 {
		t.Fatalf("the plan asks for no call of %s for %s: %+v", source, name, p.Calls)
	}
	return p.Calls[i]
}

// credential sets the file the source's credential is read from.
func credential(t *testing.T, st store.API, source, text string) {
	t.Helper()
	f := filepath.Join(t.TempDir(), "source.env")
	if err := os.WriteFile(f, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSource(ctx, source, store.SourceEnvFile, f, "me"); err != nil {
		t.Fatal(err)
	}
}

func applyCalls(t *testing.T, st store.API, p *pack.Plan, answers map[string]map[string]bool) *pack.Result {
	t.Helper()
	a := pack.Answers{}
	for _, q := range p.Questions {
		a[q.ID] = map[string]string{"kind": "claude", "dir": t.TempDir()}[q.Field]
	}
	res, err := pack.Apply(ctx, st, p, pack.ApplyOptions{Answers: a, Calls: answers, Actor: "me"})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// granted is what to may call of the crm, as the store has it.
func granted(t *testing.T, st store.API, to store.Grantee) string {
	t.Helper()
	grants, err := st.SourceGrants(ctx, "crm")
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range grants {
		if g.Grantee == to {
			return strings.Join(g.Calls, ",")
		}
	}
	return ""
}

// A group and a crew member each ask for calls of a declared source, by
// read or by a call's name, in the file's order.
func TestPackReadsTheCallsItAsksFor(t *testing.T) {
	p := load(t, writePack(t, callsFiles(smallCalls, crm)))
	want := map[string][]pack.CallsAsked{
		"builders": {{Source: "crm", Calls: []string{"read"}}},
		"assembly": {{Source: "crm", Calls: []string{"note"}}},
		"people":   nil,
	}
	for _, g := range p.Groups {
		if len(g.Calls) != len(want[g.Name]) || (len(g.Calls) > 0 && (g.Calls[0].Source != "crm" || !slices.Equal(g.Calls[0].Calls, want[g.Name][0].Calls))) {
			t.Errorf("group %s asks for %+v, want %+v", g.Name, g.Calls, want[g.Name])
		}
	}
	if c := p.Crew[0].Calls; len(c) != 1 || c[0].Source != "crm" || !slices.Equal(c[0].Calls, []string{"customer", "note"}) {
		t.Errorf("the yardmaster asks for %+v", c)
	}
}

// What a pack may not ask: a call its own declaration lacks, a call of a
// source yardr has built in, a call for a people group, nothing at all.
func TestCallsRefusals(t *testing.T) {
	cases := []struct {
		name, old, new string
		key            string
		line           int
		msg            string
	}{
		{name: "a call the declaration lacks", old: "calls: { crm: [read] }", new: "calls: { crm: [read, export] }",
			key: "groups.builders.calls.crm", line: 10, msg: "sources/crm.yaml declares no call export (it has: ping, customer, note)"},
		{name: "a source yardr has built in", old: "calls: { crm: [read] }", new: "calls: { token: [read] }",
			key: "groups.builders.calls.token", line: 10, msg: "token is a source yardr has built in"},
		{name: "a people group", old: "people:   { people: true }", new: "people:   { people: true, calls: { crm: [read] } }",
			key: "groups.people.calls", line: 12, msg: "a people group makes no call"},
		{name: "nothing", old: "calls: { crm: [customer, note] }", new: "calls: { crm: [] }",
			key: "crew.yardmaster.calls.crm", line: 16, msg: "empty: name read or a call"},
		{name: "a call twice", old: "calls: { crm: [customer, note] }", new: "calls: { crm: [note, note] }",
			key: "crew.yardmaster.calls.crm", line: 16, msg: "note is listed twice"},
		{name: "no mapping", old: "calls: { crm: [customer, note] }", new: "calls: [crm]",
			key: "crew.yardmaster.calls", line: 16, msg: "want a mapping"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !strings.Contains(smallCalls, c.old) {
				t.Fatalf("the pack has no %q", c.old)
			}
			dir := writePack(t, callsFiles(strings.Replace(smallCalls, c.old, c.new, 1), crm))
			_, err := pack.LoadDir(dir)
			var errs pack.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("err = %v, want the pack's errors", err)
			}
			if len(errs) != 1 || errs[0].File != filepath.Join(dir, "pack.yaml") || errs[0].Key != c.key || errs[0].Line != c.line || !strings.Contains(errs[0].Msg, c.msg) {
				t.Fatalf("want one error at pack.yaml:%d %s with %q, got:\n%v", c.line, c.key, c.msg, errs)
			}
		})
	}
}

// In a yard that has not the source, the import adds it and asks nothing:
// nobody can make a call of a source without a credential. Once the operator
// has set the file, the next import asks about each call, grants what was
// answered yes, and records a no. The one after asks nothing; a grant that
// was revoked since is asked about again.
func TestPlanAsksAboutCallsOnceTheSourceHasItsCredential(t *testing.T) {
	st, o := yard(t)
	dir := writePack(t, callsFiles(smallCalls, crm))
	builders, assembly, boss := store.Grantee{Group: "web-builders"}, store.Grantee{Group: "web-assembly"}, store.Grantee{Crew: "yardmaster"}

	plan := build(t, st, load(t, dir), o)
	if len(plan.Calls) != 3 {
		t.Fatalf("calls = %+v", plan.Calls)
	}
	for _, c := range plan.Calls {
		if len(c.Asks)+len(c.Has)+len(c.Declined) != 0 || !slices.Equal(c.Lacks, c.Wants) || len(c.Missing) != 1 || c.Missing[0].What != store.SourceEnvFile ||
			!strings.Contains(c.Missing[0].Detail, "yardr source key crm gives CRM_TOKEN, or yardr source set crm env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> \"<question>\" --secret crm --context \"<where to get it>\", then apply the pack again") {
			t.Fatalf("before the credential: %+v", c)
		}
	}
	if c := asked(t, plan, "assembly", "crm"); c.Group != "web-assembly" || !slices.Equal(c.Updates, []string{"note"}) || c.Words(c.Wants) != "note (update)" {
		t.Fatalf("assembly = %+v", c)
	}
	// An answer to what was not asked is refused, and nothing is written.
	_, err := pack.Apply(ctx, st, plan, pack.ApplyOptions{Answers: pack.Answers{}, Calls: map[string]map[string]bool{"group web-builders": {"crm:read": true}}, Actor: "me"})
	if err == nil || !strings.Contains(err.Error(), "the plan does not ask whether group web-builders may call crm:read") {
		t.Fatalf("err = %v", err)
	}
	if res := applyCalls(t, st, plan, nil); len(res.Calls) != 0 || len(record(t, st).Calls) != 0 {
		t.Fatalf("the first import granted %+v and recorded %+v", res.Calls, record(t, st).Calls)
	}

	credential(t, st, "crm", "CRM_TOKEN=not-a-real-one\n")
	plan = build(t, st, load(t, dir), o)
	for name, want := range map[string][]string{"builders": {"read"}, "assembly": {"note"}, "yardmaster": {"customer", "note"}} {
		if c := asked(t, plan, name, "crm"); !slices.Equal(c.Asks, want) || len(c.Lacks)+len(c.Missing)+len(c.Has)+len(c.Declined) != 0 {
			t.Fatalf("%s, with the credential: %+v", name, c)
		}
	}
	if granted(t, st, builders) != "" {
		t.Fatal("a plan granted a call")
	}
	res := applyCalls(t, st, plan, map[string]map[string]bool{
		"group web-builders":     {"crm:read": true},
		"group web-assembly":     {"crm:note": false},
		"crew member yardmaster": {"crm:customer": true, "crm:note": false},
	})
	if len(res.Calls) != 2 || res.Calls[0].Grantee != builders || !slices.Equal(res.Calls[0].Calls, []string{"read"}) ||
		res.Calls[1].Grantee != boss || !slices.Equal(res.Calls[1].Calls, []string{"customer"}) || !res.Wrote() {
		t.Fatalf("granted = %+v", res.Calls)
	}
	if b, a, y := granted(t, st, builders), granted(t, st, assembly), granted(t, st, boss); b != "read" || a != "" || y != "customer" {
		t.Fatalf("grants: builders %q, assembly %q, yardmaster %q", b, a, y)
	}
	rec := record(t, st).Calls
	if !rec["group web-builders"]["crm:read"] || rec["group web-assembly"]["crm:note"] || len(rec["group web-assembly"]) != 1 ||
		!rec["crew member yardmaster"]["crm:customer"] || len(rec["crew member yardmaster"]) != 2 {
		t.Fatalf("record = %+v", rec)
	}

	// The second import after the answers asks nothing and changes nothing.
	plan = build(t, st, load(t, dir), o)
	if c := asked(t, plan, "builders", "crm"); !slices.Equal(c.Has, []string{"read"}) || len(c.Asks) != 0 {
		t.Fatalf("builders = %+v", c)
	}
	if c := asked(t, plan, "assembly", "crm"); !slices.Equal(c.Declined, []string{"note"}) || len(c.Asks) != 0 || !c.Grantable("note") {
		t.Fatalf("assembly = %+v", c)
	}
	if c := asked(t, plan, "yardmaster", "crm"); !slices.Equal(c.Has, []string{"customer"}) || !slices.Equal(c.Declined, []string{"note"}) || len(c.Asks) != 0 {
		t.Fatalf("yardmaster = %+v", c)
	}
	before := head(t, st)
	if res := applyCalls(t, st, plan, nil); res.Wrote() || res.Recorded {
		t.Fatalf("the second import wrote: %+v", res)
	}
	if got := kinds(t, st, before); !slices.Equal(got, []string{"pack_unchanged"}) {
		t.Fatalf("the second import logged %v", got)
	}

	// A no is answered after all; a yes that was taken back is asked again,
	// never given back unasked.
	if _, err := st.RevokeSource(ctx, "crm", "read", builders, "", "me"); err != nil {
		t.Fatal(err)
	}
	plan = build(t, st, load(t, dir), o)
	if c := asked(t, plan, "builders", "crm"); !slices.Equal(c.Asks, []string{"read"}) || len(c.Has) != 0 {
		t.Fatalf("after the revoke: %+v", c)
	}
	applyCalls(t, st, plan, map[string]map[string]bool{"group web-assembly": {"crm:note": true}})
	if b, a := granted(t, st, builders), granted(t, st, assembly); b != "" || a != "note" {
		t.Fatalf("grants: builders %q, assembly %q", b, a)
	}
	if rec := record(t, st).Calls; len(rec["group web-builders"]) != 0 || !rec["group web-assembly"]["crm:note"] {
		t.Fatalf("record = %+v", rec)
	}
}

// A grant of read covers the name of a read call: nothing is asked for a
// call the grantee makes already.
func TestACallCoveredByReadIsHad(t *testing.T) {
	st, o := yard(t)
	dir := writePack(t, callsFiles(smallCalls, crm))
	applyCalls(t, st, build(t, st, load(t, dir), o), nil)
	credential(t, st, "crm", "CRM_TOKEN=not-a-real-one\n")
	if _, err := st.GrantSource(ctx, "crm", "read", store.Grantee{Crew: "yardmaster"}, "", "me"); err != nil {
		t.Fatal(err)
	}
	c := asked(t, build(t, st, load(t, dir), o), "yardmaster", "crm")
	if !slices.Equal(c.Has, []string{"customer"}) || !slices.Equal(c.Asks, []string{"note"}) {
		t.Fatalf("yardmaster = %+v", c)
	}
}

// A pack may ask for calls of a source it does not declare: the yard's.
// Nothing is asked while the yard has no such source, or the source no such
// call.
func TestCallsOfASourceThePackDoesNotDeclare(t *testing.T) {
	st, o := yard(t)
	yaml := strings.Replace(smallCalls, "sources:\n  crm:\n    declaration: sources/crm.yaml\n", "", 1)
	yaml = strings.Replace(yaml, "calls: { crm: [note] }", "calls: { crm: [export] }", 1)
	dir := writePack(t, callsFiles(yaml, ""))

	plan := build(t, st, load(t, dir), o)
	if c := asked(t, plan, "builders", "crm"); !slices.Equal(c.Lacks, []string{"read"}) || len(c.Missing) != 1 || c.Missing[0].What != "source" ||
		!strings.Contains(c.Missing[0].Detail, "the yard has no source crm, and the pack declares none") {
		t.Fatalf("without the source: %+v", c)
	}
	// A source the binary carries a pack for names it.
	linYaml := strings.ReplaceAll(yaml, "crm: [export]", "linear: [ping]")
	linPlan := build(t, st, load(t, writePack(t, callsFiles(linYaml, ""))), o)
	if c := asked(t, linPlan, "assembly", "linear"); len(c.Missing) != 1 || c.Missing[0].What != "source" ||
		!strings.Contains(c.Missing[0].Detail, "the yard has no source linear, and the pack declares none: yardr pack apply linear brings it") {
		t.Fatalf("without the packed source: %+v", c)
	}
	// So does one whose Go adapter is still compiled in: the pack may
	// be asked of github, and a yard without it is told what brings it.
	ghYaml := strings.ReplaceAll(yaml, "crm: [export]", "github: [ping]")
	ghPlan := build(t, st, load(t, writePack(t, callsFiles(ghYaml, ""))), o)
	if c := asked(t, ghPlan, "assembly", "github"); len(c.Missing) != 1 || c.Missing[0].What != "source" ||
		!strings.Contains(c.Missing[0].Detail, "the yard has no source github, and the pack declares none: yardr pack apply github brings it") {
		t.Fatalf("without the packed source: %+v", c)
	}
	applyCalls(t, st, plan, nil)

	if _, err := st.PutSource(ctx, "crm.yaml", crm, "me"); err != nil {
		t.Fatal(err)
	}
	credential(t, st, "crm", "CRM_TOKEN=not-a-real-one\n")
	plan = build(t, st, load(t, dir), o)
	if c := asked(t, plan, "builders", "crm"); !slices.Equal(c.Asks, []string{"read"}) || len(c.Missing) != 0 {
		t.Fatalf("with the source: %+v", c)
	}
	if c := asked(t, plan, "assembly", "crm"); !slices.Equal(c.Lacks, []string{"export"}) || len(c.Asks) != 0 || len(c.Missing) != 1 ||
		c.Missing[0].Detail != "the source crm has no call export (it has: ping, customer, note)" {
		t.Fatalf("a call the yard's source lacks: %+v", c)
	}
	if c := asked(t, plan, "yardmaster", "crm"); !slices.Equal(c.Asks, []string{"customer", "note"}) || !slices.Equal(c.Updates, []string{"note"}) {
		t.Fatalf("yardmaster = %+v", c)
	}
}

// A declaration that replaces the yard's and adds a read call widens what
// a grant of read reaches, with nobody asked: the plan says who gets which
// call that way. An added update reaches nobody.
func TestAReplacedDeclarationShowsWhoReadNowReaches(t *testing.T) {
	st, o := yard(t)
	dir := writePack(t, callsFiles(smallCalls, crm))
	applyCalls(t, st, build(t, st, load(t, dir), o), nil)
	for _, to := range []store.Grantee{{Group: "web-builders"}, {Crew: "yardmaster"}} {
		what := "read"
		if to.Crew != "" {
			what = "customer"
		}
		if _, err := st.GrantSource(ctx, "crm", what, to, "", "me"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.AddUser(ctx, "ada", "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GrantSource(ctx, "crm", "read", store.Grantee{Crew: "yardmaster"}, "ada", "ada"); err != nil {
		t.Fatal(err)
	}
	if d := declared(t, build(t, st, load(t, dir), o), "crm"); d.Reaches != nil {
		t.Fatalf("an unchanged declaration reaches %+v", d.Reaches)
	}

	wider := crm + "  orders: { kind: read, request: { method: GET, path: /orders } }\n  close: { kind: update, request: { method: POST, path: /close } }\n"
	d := declared(t, build(t, st, load(t, writePack(t, callsFiles(smallCalls, wider))), o), "crm")
	if d.Action != pack.Change || d.Reaches == nil || !slices.Equal(d.Reaches.Calls, []string{"orders"}) ||
		!slices.Equal(d.Reaches.Who, []pack.ReachHolder{
			{Grantee: store.Grantee{Crew: "yardmaster"}, As: "ada"},
			{Grantee: store.Grantee{Group: "web-builders"}},
		}) {
		t.Fatalf("declared = %+v, reaches %+v", d, d.Reaches)
	}

	// With nobody holding read, a new read call reaches nobody.
	if _, err := st.RevokeSource(ctx, "crm", "read", store.Grantee{Group: "web-builders"}, "", "me"); err != nil {
		t.Fatal(err)
	}
	if d := declared(t, build(t, st, load(t, writePack(t, callsFiles(smallCalls, wider))), o), "crm"); d.Reaches != nil {
		t.Fatalf("reaches %+v with no grant of read", d.Reaches)
	}
}
