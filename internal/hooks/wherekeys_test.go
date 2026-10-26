package hooks_test

import (
	"context"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// githubEvents are the X-GitHub-Event values the fixtures under
// testdata/github are named after: a fixture is <event>_<what>.json, or
// <event>.json.
var githubEvents = []string{"check_suite", "issue_comment", "issues", "ping", "pull_request", "pull_request_review", "push", "workflow_run"}

// githubEventOf is the event a github fixture is a delivery of: the longest
// event its name starts with (pull_request_review_approved is a review, not
// a pull_request).
func githubEventOf(file string) string {
	name, event := strings.TrimSuffix(filepath.Base(file), ".json"), ""
	for _, e := range githubEvents {
		if (name == e || strings.HasPrefix(name, e+"_")) && len(e) > len(event) {
			event = e
		}
	}
	return event
}

// A field an adapter sets is one a subscription may filter on. The field
// names are literals in the adapters and the where keys a list in the
// store, so nothing but this holds them together. Every fixture of each
// builtin adapter is normalized, and the store is asked for a subscription
// on each field it came back with. token's sender-chosen fields are the
// exception: its fixtures carry none, its subscriptions keep the common
// keys, and only its makes lines filter on any key but the routing names
// (store.SourceToken, store.TokenReservedFields).
//
// The other way round is only said (go test -v): a where key no fixture
// sets may be an event the fixtures do not cover, or a key of another
// source, since the hook sources share store.WhereKeys.
func TestAdapterFieldsAreWhereKeys(t *testing.T) {
	sources := map[string]struct {
		glob   string
		header func(file string) http.Header
		keys   []string // what the store lets the source filter on, for the note
	}{
		"token": {"testdata/token_*.json", func(string) http.Header { return nil }, store.WhereKeys},
	}
	st, _ := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "filters", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, source := range hooks.Sources() {
		src, ok := sources[source]
		if !ok {
			t.Errorf("%s adapter has no fixture entry", source)
			continue
		}
		checkFields(t, st, ctx, source, src.glob, src.header, hooks.Adapters[source], src.keys)
	}
}

// A field the pack's normalize sets is one a subscription to the declared
// source may filter on: its hook.keys beside the store's. The field names
// are literals in the script and the keys a list in the declaration, so
// nothing but this holds them together: the adapter once set head_repo
// while `yardr subscribe --where head_repo=...` was refused.
func TestGitHubPackFieldsAreWhereKeys(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's normalize is written in it")
	}
	dir := filepath.Join("..", "..", "packs", "github")
	text, err := os.ReadFile(filepath.Join(dir, "github.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "github.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := d.CopyCommandFiles(dir, home); err != nil {
		t.Fatal(err)
	}
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	st, _ := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "filters", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutSource(ctx, "github.yaml", string(text), "me"); err != nil {
		t.Fatal(err)
	}
	header := func(file string) http.Header {
		return http.Header{"X-Github-Event": {githubEventOf(file)}}
	}
	checkFields(t, st, ctx, "github", "testdata/github/*.json", header,
		hooks.Declared{D: d, Home: home}, append(slices.Clone(store.WhereKeys), d.Hook.Keys...))
}

// checkFields normalizes every fixture the glob matches through ad and
// asks the store for a subscription on each field one came back with.
// keys are what the store lets the source filter on, for the note.
func checkFields(t *testing.T, st store.API, ctx context.Context, source, glob string, header func(string) http.Header, ad hooks.Adapter, keys []string) {
	t.Helper()
	files, err := filepath.Glob(glob)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Errorf("%s: no fixture matches %s", source, glob)
	}
	set := map[string]string{} // field: the first fixture that set it
	for _, file := range files {
		h := header(file)
		if source == "github" && h.Get("X-GitHub-Event") == "" {
			t.Errorf("%s: its name starts with no event of githubEvents: add the event there", file)
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		e, err := ad.Normalize(h, body)
		if err != nil {
			t.Errorf("%s: %v", file, err)
			continue
		}
		// type and action are the envelope's own, read from there by a
		// filter whatever Fields holds.
		for k, v := range map[string]string{"type": e.Type, "action": e.Action} {
			if _, seen := set[k]; v != "" && !seen {
				set[k] = file
			}
		}
		for k := range e.Fields {
			if _, seen := set[k]; !seen {
				set[k] = file
			}
		}
	}
	for _, k := range slices.Sorted(maps.Keys(set)) {
		sub := store.Subscription{Source: source, Scope: store.ScopeAll, Where: map[string]string{k: "x"}}
		if err := st.AddSubscription(ctx, "filters", sub, "filters"); err != nil {
			t.Errorf("the %s adapter sets field %q (%s), which is no where key of %s: %v", source, k, set[k], source, err)
		}
	}
	for _, k := range keys {
		if _, ok := set[k]; !ok {
			t.Logf("%s: no fixture sets where key %q", source, k)
		}
	}
}
