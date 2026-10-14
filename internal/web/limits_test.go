package web_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// limitsSource stores quokka, a source that runs a stand-in for kimi and
// claude. The stand-in appends its provider argument to the asked file each
// time it runs, then does body. It returns the asked file.
func limitsSource(t *testing.T, f *fixture, body string) string {
	t.Helper()
	return limitsSourceAs(t, f, "quokka", "{ kimi: moonshot, claude: claude }", body)
}

// limitsSourceAs is limitsSource for a source named name with the kinds
// table given, for a yard with more than one source.
func limitsSourceAs(t *testing.T, f *fixture, name, kinds, body string) string {
	t.Helper()
	dir := t.TempDir()
	asked := filepath.Join(dir, "asked")
	script := filepath.Join(dir, "provider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" >>"+asked+"\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	text := "name: " + name + "\nabout: A stand-in for a provider of limits.\nlimits:\n  command: [" + script + ", \"{provider}\", --json]\n  kinds: " + kinds + "\n"
	if _, err := f.st.PutSource(context.Background(), name+".yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	// The command runs in the yard's home, which the fixture names but does
	// not make.
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	return asked
}

// putGroup runs a herdr group named name with cfg on the yard.
func putGroup(t *testing.T, f *fixture, name string, cfg map[string]any) {
	t.Helper()
	must(t, f.st.PutGroup(context.Background(), store.Group{Name: name, Runner: "herdr", Config: cfg}, "me"))
}

// limitsAnswer is a report with a 5h window used up that resets in 42
// minutes and some seconds, and a weekly one that is not.
func limitsAnswer() string {
	in := func(d time.Duration) string { return time.Now().Add(d).UTC().Format(time.RFC3339) }
	return fmt.Sprintf(`echo '{"provider":"Kimi","plan":"pro","windows":[{"label":"5h","used_percent":100,"resets_at":%q,"used":100,"limit":100},{"label":"Weekly","used_percent":38,"resets_at":%q}]}'`,
		in(42*time.Minute+20*time.Second), in(81*time.Hour+20*time.Second))
}

// askedFor are the provider names the stand-in was run for, in order.
func askedFor(t *testing.T, asked string) string {
	t.Helper()
	b, err := os.ReadFile(asked)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(b)), " ")
}

// useKinds runs the yard on kinds: the default first, then one herdr group
// per rest kind.
func useKinds(t *testing.T, f *fixture, def string, rest ...string) {
	t.Helper()
	ctx := context.Background()
	if def != "" {
		must(t, f.st.SetSetting(ctx, "default_kind", def, "me"))
	}
	for i, kind := range rest {
		must(t, f.st.PutGroup(ctx, store.Group{Name: fmt.Sprintf("g%d", i), Runner: "herdr",
			Config: map[string]any{"kind": kind}}, "me"))
	}
}

// /limits answers what `yardr limits` answers with no kind named, one entry
// per provider in use with the burning kinds and users, and unmapped naming
// the kinds in use that resolve to nothing, beside the head from before the
// read, and empty lists, never null, for a yard with nothing to ask. It has
// a page, so only a client that asks for JSON gets it.
func TestLimits(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var empty web.Limits
	r := f.getJSON("/limits", &empty)
	if r.header.Get("Content-Type") != "application/json" || r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("type %q, cache %q", r.header.Get("Content-Type"), r.header.Get("Cache-Control"))
	}
	head, err := f.st.Head(ctx)
	must(t, err)
	if !reflect.DeepEqual(empty, web.Limits{Limits: []web.ProviderLimits{}, Sources: []web.SourceLimits{}, Unmapped: []string{}, Head: head}) {
		t.Errorf("an empty yard: %+v, want empty lists and head %d", empty, head)
	}
	if strings.Contains(r.body, "null") {
		t.Errorf("an empty yard answers %s, want empty lists and not null", r.body)
	}

	asked := limitsSource(t, f, limitsAnswer())
	// A source and nothing in use: still nothing to ask, and nothing asked.
	var bare web.Limits
	f.getJSON("/limits", &bare)
	if len(bare.Limits) != 0 || len(bare.Unmapped) != 0 {
		t.Fatalf("a source with nothing in use: %+v, want empty lists", bare)
	}
	if got := askedFor(t, asked); got != "" {
		t.Fatalf("asked = %q, want nothing asked", got)
	}

	useKinds(t, f, "claude", "kimi", "codex")
	head, err = f.st.Head(ctx)
	must(t, err)
	// Only a client that asks for JSON gets it.
	list, at := f.listOf("/limits", "limits", "Accept", "application/json")
	if len(list.([]any)) != 2 || at != head {
		t.Errorf("/limits: %d entries, head %d, want 2 and %d", len(list.([]any)), at, head)
	}
	// A browser, plain curl and */* get the page.
	for _, headers := range [][]string{nil, {"Accept", "text/html"}, {"Accept", "*/*"}} {
		r := f.get("/limits", headers...)
		if r.status != 200 || !strings.HasPrefix(r.header.Get("Content-Type"), "text/html") {
			t.Errorf("/limits %v: status %d, type %q, want the page", headers, r.status, r.header.Get("Content-Type"))
		}
	}
	var got web.Limits
	f.getJSON("/limits", &got)
	if !reflect.DeepEqual(got.Unmapped, []string{"codex"}) {
		t.Errorf("unmapped = %v, want [codex]: in use and resolving to nothing", got.Unmapped)
	}
	if len(got.Limits) != 2 || got.Limits[0].Provider != "claude" || got.Limits[1].Provider != "moonshot" {
		t.Fatalf("limits = %+v, want the default's provider first, then kimi's", got.Limits)
	}
	if d := got.Limits[0]; !reflect.DeepEqual(d.Kinds, []string{"claude"}) ||
		!reflect.DeepEqual(d.BurnedBy, []string{"the yard's default"}) {
		t.Errorf("claude's row burns %+v for %+v, want [claude] for [the yard's default]", d.Kinds, d.BurnedBy)
	}
	k := got.Limits[1]
	if !reflect.DeepEqual(k.Kinds, []string{"kimi"}) || !reflect.DeepEqual(k.BurnedBy, []string{"group g0"}) {
		t.Errorf("moonshot's row burns %+v for %+v, want [kimi] for [group g0]", k.Kinds, k.BurnedBy)
	}
	if k.Limits == nil || k.Limits.Source != "quokka" || k.Limits.Provider != "Kimi" || k.Limits.Plan != "pro" ||
		len(k.Limits.Windows) != 2 || k.Limits.Windows[0].Label != "5h" || k.Window != "5h" || k.Unknown != "" ||
		time.Since(k.Limits.AskedAt) > time.Minute {
		t.Errorf("kimi's entry: %+v", k)
	}
	if k.ResetsAt == nil || time.Until(*k.ResetsAt) < 40*time.Minute || time.Until(*k.ResetsAt) > 43*time.Minute {
		t.Errorf("kimi's entry resets at %v, want the used-up 5h window's, in about 42m", k.ResetsAt)
	}
	if w := k.Limits.Windows[1]; w.Label != "Weekly" || w.UsedPercent == nil || *w.UsedPercent != 38 {
		t.Errorf("kimi's weekly window: %+v", w)
	}
	// Each provider asked once, by its name: codex resolves to nothing.
	if got := askedFor(t, asked); got != "claude moonshot" {
		t.Errorf("asked = %q", got)
	}
	// A list is read only, as every page.
	if r := f.do("POST", "/limits"); r.status != 405 {
		t.Errorf("POST /limits: status %d", r.status)
	}
}

// selfSource stores machine, a source that answers for itself: the
// stand-in appends a line to the asked file each time it runs, then does
// body. It returns the asked file.
func selfSource(t *testing.T, f *fixture, body string) string {
	t.Helper()
	dir := t.TempDir()
	asked := filepath.Join(dir, "asked")
	script := filepath.Join(dir, "machine")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho ran >>"+asked+"\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	text := "name: machine\nlimits:\n  command: [" + script + "]\n"
	if _, err := f.st.PutSource(context.Background(), "machine.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	return asked
}

// selfAnswer is a report with the load, swap and disk windows, each with
// its target.
func selfAnswer() string {
	return `echo '{"provider":"machine","windows":[` +
		`{"label":"load","used_percent":12.5,"target_percent":80},` +
		`{"label":"swap","used_percent":10,"target_percent":85},` +
		`{"label":"disk","used_percent":40,"target_percent":90}]}'`
}

// /limits answers one entry per source that answers for itself beside the
// providers, whether or not a kind is in use, and the page shows its
// windows with their targets. The minute's cache holds for sources as for
// providers: a second read asks nothing again.
func TestLimitsOfASourceThatAnswersForItself(t *testing.T) {
	f := newFixture(t)
	asked := selfSource(t, f, selfAnswer())
	var got web.Limits
	f.getJSON("/limits", &got)
	if len(got.Limits) != 0 || len(got.Unmapped) != 0 || len(got.Sources) != 1 {
		t.Fatalf("limits = %+v, want the one source and nothing else", got)
	}
	if s := got.Sources[0]; s.Source != "machine" || s.Limits == nil || s.Limits.Provider != "machine" ||
		len(s.Limits.Windows) != 3 || s.Limits.Windows[0].Label != "load" || s.Unknown != "" {
		t.Fatalf("the source's entry = %+v", s)
	}
	if w := got.Sources[0].Limits.Windows[0]; w.UsedPercent == nil || *w.UsedPercent != 12.5 ||
		w.TargetPercent == nil || *w.TargetPercent != 80 {
		t.Fatalf("the load window = %+v", w)
	}
	if got := askedFor(t, asked); got != "ran" {
		t.Fatalf("asked = %q, want the source once, with no kind in use", got)
	}
	// A second read shares the last answer; the page shows the windows.
	var again web.Limits
	f.getJSON("/limits", &again)
	if len(again.Sources) != 1 || again.Sources[0].Limits == nil {
		t.Fatalf("a second read: %+v", again)
	}
	if got := askedFor(t, asked); got != "ran" {
		t.Fatalf("asked = %q, want nothing asked again within the minute", got)
	}
	r := f.get("/limits", "Accept", "text/html")
	if r.status != 200 || !strings.Contains(r.body, "<h2>machine</h2>") ||
		!strings.Contains(r.body, "12.5% used") || !strings.Contains(r.body, "(target 80%)") {
		t.Errorf("the page shows no machine windows:\n%s", r.body)
	}
}

// Providers the yard burns are asked once however many burn them, and each
// row names who: groups, crew members and the yard's default. A provider
// setting is asked as named even where no table lists the kind, and a
// provider no source names is not known, with who burns it.
func TestLimitsByProvider(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	asked := limitsSource(t, f, limitsAnswer())
	putGroup(t, f, "builders", map[string]any{"kind": "kimi"})
	putGroup(t, f, "more", map[string]any{"kind": "kimi"})
	putGroup(t, f, "reviewers", map[string]any{"kind": "codex"})
	var got web.Limits
	f.getJSON("/limits", &got)
	if len(got.Limits) != 1 {
		t.Fatalf("limits = %+v, want the one provider in use a source answers for", got.Limits)
	}
	row := got.Limits[0]
	if row.Provider != "moonshot" || !reflect.DeepEqual(row.Kinds, []string{"kimi"}) ||
		!reflect.DeepEqual(row.BurnedBy, []string{"group builders", "group more"}) ||
		row.Limits == nil || row.Window != "5h" || row.Unknown != "" {
		t.Fatalf("moonshot's row = %+v", row)
	}
	if !reflect.DeepEqual(got.Unmapped, []string{"codex"}) {
		t.Fatalf("unmapped = %v, want [codex]", got.Unmapped)
	}
	if asked := askedFor(t, asked); asked != "moonshot" {
		t.Fatalf("asked = %q, want kimi's provider once for both groups", asked)
	}

	// A crew member and the yard's default burn their providers too, and
	// appear on the row: none burns without its user. Who burns a provider
	// is fresh every read: only the ask stands a minute.
	_, err := f.st.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "me")
	must(t, err)
	must(t, f.st.SetSetting(ctx, store.DefaultKindSetting, "kimi", "me"))
	// Fresh, every read: decoding reuses the rows it has, so a row that
	// loses its report would keep the last read's.
	got = web.Limits{}
	f.getJSON("/limits", &got)
	if len(got.Limits) != 2 || got.Limits[0].Provider != "moonshot" || got.Limits[1].Provider != "claude" {
		t.Fatalf("limits = %+v, want moonshot, then the crew member's provider", got.Limits)
	}
	if row := got.Limits[0]; !reflect.DeepEqual(row.Kinds, []string{"kimi"}) ||
		!reflect.DeepEqual(row.BurnedBy, []string{"the yard's default", "group builders", "group more"}) {
		t.Errorf("moonshot's row burns %+v for %+v", row.Kinds, row.BurnedBy)
	}
	if row := got.Limits[1]; !reflect.DeepEqual(row.Kinds, []string{"claude"}) ||
		!reflect.DeepEqual(row.BurnedBy, []string{"crew member scribe"}) || row.Limits == nil {
		t.Errorf("claude's row = %+v", row)
	}
	if asked := askedFor(t, asked); asked != "moonshot claude" {
		t.Errorf("asked = %q, want the new provider once and no second ask of moonshot", asked)
	}

	// A group with provider=zai whose kind no table lists is asked as zai
	// through the source naming it, beside the rest: one ask per provider.
	askedWombat := limitsSourceAs(t, f, "wombat", "{ grok: zai }", `echo '{"provider":"Z.ai","windows":[{"label":"5h","used_percent":12,"used":12,"limit":100}]}'`)
	putGroup(t, f, "zap", map[string]any{"kind": "pi", "provider": "zai"})
	got = web.Limits{}
	f.getJSON("/limits", &got)
	if len(got.Limits) != 3 || got.Limits[2].Provider != "zai" {
		t.Fatalf("limits = %+v, want zai's row beside the rest", got.Limits)
	}
	if row := got.Limits[2]; !reflect.DeepEqual(row.Kinds, []string{"pi"}) ||
		!reflect.DeepEqual(row.BurnedBy, []string{"group zap"}) ||
		row.Limits == nil || row.Limits.Source != "wombat" || row.Limits.Provider != "Z.ai" || row.Unknown != "" {
		t.Errorf("zai's row = %+v", row)
	}
	if asked := askedFor(t, askedWombat); asked != "zai" {
		t.Errorf("asked wombat = %q, want the setting's provider, once", asked)
	}
	if asked := askedFor(t, asked); asked != "moonshot claude" {
		t.Errorf("asked quokka = %q, want nothing further asked", asked)
	}

	// A provider no source names is not known, with who burns it, and
	// nothing is asked for it.
	putGroup(t, f, "zap", map[string]any{"kind": "pi", "provider": "openai"})
	got = web.Limits{}
	f.getJSON("/limits", &got)
	if len(got.Limits) != 3 || got.Limits[2].Provider != "openai" {
		t.Fatalf("limits = %+v, want the unanswered provider's row beside the rest", got.Limits)
	}
	if row := got.Limits[2]; !reflect.DeepEqual(row.Kinds, []string{"pi"}) ||
		!reflect.DeepEqual(row.BurnedBy, []string{"group zap"}) || row.Limits != nil ||
		!strings.Contains(row.Unknown, "no source of this yard answers for provider openai") {
		t.Errorf("openai's row = %+v", row)
	}
	if asked := askedFor(t, askedWombat); asked != "zai" {
		t.Errorf("asked wombat = %q, want nothing further asked", asked)
	}
	if asked := askedFor(t, asked); asked != "moonshot claude" {
		t.Errorf("asked quokka = %q, want nothing further asked", asked)
	}
	if !reflect.DeepEqual(got.Unmapped, []string{"codex"}) {
		t.Errorf("unmapped = %v, want [codex]: the setting's kind is grouped, not unmapped", got.Unmapped)
	}
}

// The limits page renders the rows `yardr limits` prints: each provider in
// use with who burns it, its windows, how far each is used and when it
// resets, which window an agent waits for, and the kinds that resolve to
// nothing. A re-read costs no extra provider run: the page reads through
// the same handler, which answers from its last ask until the minute is up.
func TestLimitsPage(t *testing.T) {
	f := newFixture(t)
	asked := limitsSource(t, f, limitsAnswer())
	useKinds(t, f, "claude", "kimi", "codex")
	page := squash(f.get("/limits").body)
	for _, want := range []string{
		`<h2>moonshot</h2>`,
		`<h2>claude</h2>`,
		`burned by</dt><dd>the yard&#39;s default (kind claude)</dd>`,
		`burned by</dt><dd>group g0 (kind kimi)</dd>`,
		`<div><dt>source</dt><dd>quokka says (provider Kimi, plan pro)</dd></div>`,
		`100% used (100 of 100)`,
		`38% used`,
		`<span class="held">used up</span>`,
		`waits for</dt><dd>the 5h window, which resets`,
		`Not asked: codex`,
		`in use here, and listed by no source`,
		`<a href="/config" aria-current="page">Config</a>`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("/limits lacks %s", want)
		}
	}
	// Each used-up provider marks its 5h window once; the weekly window is
	// not marked, and neither provider waits on nothing.
	if n := strings.Count(page, "used up"); n != 2 {
		t.Errorf("/limits says used up %d times, want twice (one mark per provider)", n)
	}
	// Another read renders the same page and runs nothing.
	if first, second := f.get("/limits").body, f.get("/limits").body; first != second {
		t.Error("two reads of the page differ")
	}
	if got := askedFor(t, asked); got != "claude moonshot" {
		t.Errorf("asked = %q, want each provider once", got)
	}

	// An empty yard says so.
	if body := newFixture(t).get("/limits").body; !strings.Contains(body, "This yard has no limits to show.") {
		t.Errorf("/limits of an empty yard lacks its line: %s", body)
	}
}

// What a provider's command wrote is shown as text, never as markup;
// JSON carries it as it is.
func TestLimitsEscapes(t *testing.T) {
	f := newFixture(t)
	const evil = `<script>alert(1)</script>`
	limitsSource(t, f, `echo "`+evil+`" >&2; exit 1`)
	useKinds(t, f, "", "kimi")
	page := f.get("/limits").body
	if strings.Contains(page, "<script>alert") {
		t.Error("/limits carries what the command wrote as markup")
	}
	if !strings.Contains(page, `&lt;script&gt;alert(1)&lt;/script&gt;`) {
		t.Error("/limits does not show the reason as text")
	}
	var got web.Limits
	f.getJSON("/limits", &got)
	if len(got.Limits) != 1 || got.Limits[0].Provider != "moonshot" ||
		!strings.Contains(got.Limits[0].Unknown, evil) {
		t.Errorf("JSON changed what was written: %+v", got.Limits)
	}
}

// /limits asks the source at most once a minute per provider, however often
// it is read: the second read shares the last answer and runs nothing.
func TestLimitsCached(t *testing.T) {
	f := newFixture(t)
	asked := limitsSource(t, f, limitsAnswer())
	useKinds(t, f, "", "kimi")
	first := f.get("/limits", "Accept", "application/json")
	second := f.get("/limits", "Accept", "application/json")
	if first.body != second.body {
		t.Errorf("two reads differ:\n%s\n%s", first.body, second.body)
	}
	if got := askedFor(t, asked); got != "moonshot" {
		t.Errorf("asked = %q, want kimi's provider once", got)
	}
}

// Once the last answer is older than the minute, the next read asks again.
func TestLimitsAskedAgain(t *testing.T) {
	st := storetest.New(t)
	home := t.TempDir()
	srv, err := web.NewServer(st, home, nil)
	must(t, err)
	srv.SetLimitsEvery(50 * time.Millisecond)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, ts: ts, home: home}
	asked := limitsSource(t, f, limitsAnswer())
	useKinds(t, f, "", "kimi")
	f.get("/limits", "Accept", "application/json")
	time.Sleep(200 * time.Millisecond)
	f.get("/limits", "Accept", "application/json")
	if got := askedFor(t, asked); got != "moonshot moonshot" {
		t.Errorf("asked = %q, want kimi's provider again after the minute", got)
	}
}

// Concurrent reads share the one run on its way and never start a second
// per provider.
func TestLimitsConcurrent(t *testing.T) {
	f := newFixture(t)
	asked := limitsSource(t, f, "sleep 0.5; "+limitsAnswer())
	useKinds(t, f, "", "kimi")
	const readers = 10
	bodies := make([]string, readers)
	var wg sync.WaitGroup
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := f.get("/limits", "Accept", "application/json")
			if r.status != 200 {
				t.Errorf("status %d: %s", r.status, r.body)
				return
			}
			bodies[i] = r.body
		}()
	}
	wg.Wait()
	for i := range bodies {
		if bodies[i] == "" || bodies[i] != bodies[0] {
			t.Fatalf("reader %d differs: %q", i, bodies[i])
		}
	}
	if got := askedFor(t, asked); got != "moonshot" {
		t.Errorf("asked = %q, want one run shared by %d readers", got, readers)
	}
}

// A failing provider nulls its entry without failing the route, and the
// null stands like any answer: no run per read.
func TestLimitsFailure(t *testing.T) {
	f := newFixture(t)
	asked := limitsSource(t, f, `echo "aiquokka: kimi: not logged in" >&2; exit 1`)
	useKinds(t, f, "", "kimi")
	r := f.get("/limits", "Accept", "application/json")
	if r.status != 200 {
		t.Fatalf("status %d: %s", r.status, r.body)
	}
	var raw struct {
		Limits []map[string]any `json:"limits"`
	}
	must(t, json.Unmarshal([]byte(r.body), &raw))
	if len(raw.Limits) != 1 || raw.Limits[0]["provider"] != "moonshot" {
		t.Fatalf("limits = %v", raw.Limits)
	}
	entry := raw.Limits[0]
	if _, null := entry["limits"]; null {
		t.Errorf("the failed entry carries a report: %v", entry)
	}
	if _, window := entry["window"]; window {
		t.Errorf("the failed entry carries a window: %v", entry)
	}
	if !reflect.DeepEqual(entry["kinds"], []any{"kimi"}) ||
		!reflect.DeepEqual(entry["burned_by"], []any{"group g0"}) {
		t.Errorf("the failed entry names no user: %v", entry)
	}
	reason, _ := entry["limits_unknown"].(string)
	if !strings.Contains(reason, "source quokka: limits command ") ||
		!strings.Contains(reason, "aiquokka: kimi: not logged in") {
		t.Errorf("limits_unknown = %q", reason)
	}
	second := f.get("/limits", "Accept", "application/json")
	if second.body != r.body {
		t.Errorf("the null is not kept:\n%s\n%s", r.body, second.body)
	}
	if got := askedFor(t, asked); got != "moonshot" {
		t.Errorf("asked = %q, want one run", got)
	}
}

// Each run is bounded by the 30-second Timeout: a provider that hangs is a
// null entry, not a hanging route.
func TestLimitsTimeout(t *testing.T) {
	old := limits.Timeout
	limits.Timeout = 100 * time.Millisecond
	defer func() { limits.Timeout = old }()
	f := newFixture(t)
	limitsSource(t, f, "sleep 300; "+limitsAnswer())
	useKinds(t, f, "", "kimi")
	start := time.Now()
	var got web.Limits
	f.getJSON("/limits", &got)
	// Not the provider's sleep, and far from what a busy machine takes to
	// start a script and kill it (yardr-gdes).
	if took := time.Since(start); took > 2*time.Minute {
		t.Errorf("the route took %s for a hanging provider", took)
	}
	if len(got.Limits) != 1 || got.Limits[0].Limits != nil ||
		!strings.Contains(got.Limits[0].Unknown, "no answer within 100ms") {
		t.Errorf("limits = %+v", got.Limits)
	}
}

// One shape, the command's, on loopback and with --unsafe alike: the same
// bytes under a network name as under a loopback one.
func TestLimitsUnsafeSame(t *testing.T) {
	st := storetest.New(t)
	home := t.TempDir()
	srv, err := web.NewServer(st, home, nil)
	must(t, err)
	srv.AllowNetwork("testhost")
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, ts: ts, home: home}
	limitsSource(t, f, limitsAnswer())
	useKinds(t, f, "", "kimi")
	loopback := f.get("/limits", "Accept", "application/json")
	network := f.get("/limits", "Host", "10.0.0.1:8791", "Accept", "application/json")
	if loopback.status != 200 || network.status != 200 {
		t.Fatalf("loopback %d, network %d", loopback.status, network.status)
	}
	if loopback.body != network.body {
		t.Errorf("the network answer differs:\n%s\n%s", loopback.body, network.body)
	}
}
