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
	dir := t.TempDir()
	asked := filepath.Join(dir, "asked")
	script := filepath.Join(dir, "provider")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"$1\" >>"+asked+"\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	text := "name: quokka\nabout: A stand-in for a provider of limits.\nlimits:\n  command: [" + script + ", \"{provider}\", --json]\n  kinds: { kimi: moonshot, claude: claude }\n"
	if _, err := f.st.PutSource(context.Background(), "quokka.yaml", text, "me"); err != nil {
		t.Fatal(err)
	}
	// The command runs in the yard's home, which the fixture names but does
	// not make.
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	return asked
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
// per kind in use that a source answers for, and unmapped naming the rest,
// beside the head from before the read. JSON whatever the request accepts,
// and empty lists, never null, for a yard with nothing to ask.
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
	if !reflect.DeepEqual(empty, web.Limits{Limits: []web.KindLimits{}, Unmapped: []string{}, Head: head}) {
		t.Errorf("an empty yard: %+v, want empty lists and head %d", empty, head)
	}
	if strings.Contains(r.body, "null") {
		t.Errorf("an empty yard answers %s, want empty lists and not null", r.body)
	}

	asked := limitsSource(t, f, limitsAnswer())
	useKinds(t, f, "claude", "kimi", "codex")
	head, err = f.st.Head(ctx)
	must(t, err)
	for _, headers := range [][]string{nil, {"Accept", "application/json"}, {"Accept", "text/html"}} {
		// No Accept: there is no page to answer with instead.
		list, got := f.listOf("/limits", "limits", headers...)
		if len(list.([]any)) != 2 || got != head {
			t.Errorf("/limits %v: %d entries, head %d, want 2 and %d", headers, len(list.([]any)), got, head)
		}
	}
	var got web.Limits
	f.getJSON("/limits", &got)
	if !reflect.DeepEqual(got.Unmapped, []string{"codex"}) {
		t.Errorf("unmapped = %v, want [codex]: in use and listed by no source", got.Unmapped)
	}
	if len(got.Limits) != 2 || got.Limits[0].Kind != "claude" || got.Limits[1].Kind != "kimi" {
		t.Fatalf("limits = %+v, want the default kind first, then kimi", got.Limits)
	}
	k := got.Limits[1]
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
	// Each kind asked once, by its provider's name: codex is not listed.
	if got := askedFor(t, asked); got != "claude moonshot" {
		t.Errorf("asked = %q", got)
	}
	// A list is read only, as every page.
	if r := f.do("POST", "/limits"); r.status != 405 {
		t.Errorf("POST /limits: status %d", r.status)
	}
}

// /limits asks the source at most once a minute per kind, however often it
// is read: the second read shares the last answer and runs nothing.
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
	t.Cleanup(ts.Close)
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
// per kind.
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
	if len(raw.Limits) != 1 || raw.Limits[0]["kind"] != "kimi" {
		t.Fatalf("limits = %v", raw.Limits)
	}
	entry := raw.Limits[0]
	if _, null := entry["limits"]; null {
		t.Errorf("the failed entry carries a report: %v", entry)
	}
	if _, window := entry["window"]; window {
		t.Errorf("the failed entry carries a window: %v", entry)
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
	limitsSource(t, f, "sleep 5; "+limitsAnswer())
	useKinds(t, f, "", "kimi")
	start := time.Now()
	var got web.Limits
	f.getJSON("/limits", &got)
	if took := time.Since(start); took > 4*time.Second {
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
	t.Cleanup(ts.Close)
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
