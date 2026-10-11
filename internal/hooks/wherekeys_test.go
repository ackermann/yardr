package hooks_test

import (
	"context"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
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
// store, so nothing but this holds them together: the github adapter set
// head_repo while `yardr subscribe --where head_repo=...` was refused. Every fixture of each adapter is normalized, and the store is
// asked for a subscription on each field it came back with.
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
		"github": {"testdata/github/*.json", func(file string) http.Header {
			return http.Header{"X-Github-Event": {githubEventOf(file)}}
		}, append(slices.Clone(store.WhereKeys), store.GitHubWhereKeys...)},
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
		files, err := filepath.Glob(src.glob)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 0 {
			t.Errorf("%s: no fixture matches %s", source, src.glob)
		}
		set := map[string]string{} // field: the first fixture that set it
		for _, file := range files {
			h := src.header(file)
			if source == "github" && h.Get("X-GitHub-Event") == "" {
				t.Errorf("%s: its name starts with no event of githubEvents: add the event there", file)
				continue
			}
			body, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			e, err := hooks.Adapters[source].Normalize(h, body)
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
			if err := st.AddSubscription(ctx, "filters", sub, "me"); err != nil {
				t.Errorf("the %s adapter sets field %q (%s), which is no where key of %s: %v", source, k, set[k], source, err)
			}
		}
		for _, k := range src.keys {
			if _, ok := set[k]; !ok {
				t.Logf("%s: no fixture sets where key %q", source, k)
			}
		}
	}
}
