package hooks_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

// sources points both APIs at a stand-in that takes the yardmaster's
// credentials.
func sources(t *testing.T) *standin.Server {
	t.Helper()
	s := &standin.Server{GitHubToken: "gh-yardmaster"}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	old := hooks.GitHubAPI
	hooks.GitHubAPI = srv.URL
	t.Cleanup(func() { hooks.GitHubAPI = old })
	return s
}

// The store checks a map's words without importing this package.
func TestStatesAreTheStores(t *testing.T) {
	if !reflect.DeepEqual(hooks.States(), store.ThreadStates) {
		t.Fatalf("hooks.States %v, store.ThreadStates %v", hooks.States(), store.ThreadStates)
	}
}

// GitHub only closes: an open issue stands at started and a closed one is
// never reopened.
func TestGitHubForward(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		from, state string
		to, stood   string
	}{
		{"open", "done", "closed", "started"},
		{"open", "started", "", "started"},
		{"closed", "done", "", "done"},
		{"closed", "started", "", "done"},
		{"closed (not planned)", "done", "", "other"},
		{"closed (not planned)", "started", "", "other"},
	} {
		t.Run(c.from+" to "+c.state, func(t *testing.T) {
			s := sources(t)
			s.Add(standin.Issue{Source: "github", Key: "acme/webapp#7", Title: "Retries", Status: c.from})
			got, err := hooks.GitHub{}.Forward(ctx, "acme/webapp#7", c.state, "gh-yardmaster")
			if err != nil {
				t.Fatal(err)
			}
			wantStatus, wantSets := c.from, []string(nil)
			if c.to != "" {
				wantStatus, wantSets = c.to, []string{"github set acme/webapp#7 " + c.to}
			}
			if got.Stood != c.stood || got.Changed != (c.to != "") || got.Status != wantStatus || got.Thread.ID != "acme/webapp#7" ||
				s.Status("github", "acme/webapp#7") != wantStatus || !reflect.DeepEqual(s.Sets(), wantSets) {
				t.Fatalf("got %+v, the issue's %q, sets %v; want %q, stood %s, sets %v",
					got, s.Status("github", "acme/webapp#7"), s.Sets(), wantStatus, c.stood, wantSets)
			}
		})
	}
}

// A label is added to an issue or a pull request wherever it stands, by
// Forward and SetState alike; one the thread has, in any case, is left with
// no write, and nothing is opened, closed or removed.
func TestGitHubLabel(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(thread, state string) (hooks.Stated, error){
		"forward": func(thread, state string) (hooks.Stated, error) {
			f, err := hooks.GitHub{}.Forward(ctx, thread, state, "gh-yardmaster")
			return f.Stated, err
		},
		"set": func(thread, state string) (hooks.Stated, error) {
			return hooks.GitHub{}.SetState(ctx, thread, state, "gh-yardmaster")
		},
	}
	for name, call := range calls {
		for _, c := range []struct {
			what, status string
			pull         bool
			has          []string
			word         string
			labels       []string
			labeled      string // "" when the thread had it
		}{
			{"an open issue", "open", false, nil, "label:in-review", []string{"in-review"}, "in-review"},
			{"a pull request", "open", true, nil, "label:in-review", []string{"in-review"}, "in-review"},
			{"a closed issue", "closed (not planned)", false, []string{"bug"}, "label:In Review", []string{"bug", "In Review"}, "In Review"},
			{"one that has it", "open", false, []string{"bug", "in-review"}, "label:in-review", []string{"bug", "in-review"}, ""},
			{"one that has it in another case", "open", true, []string{"In-Review"}, "label:in-review", []string{"In-Review"}, ""},
		} {
			t.Run(name+" "+c.what, func(t *testing.T) {
				s := sources(t)
				s.Add(standin.Issue{Source: "github", Key: "acme/webapp#7", Title: "Retries", Status: c.status, Pull: c.pull, Labels: c.has})
				got, err := call("acme/webapp#7", c.word)
				if err != nil {
					t.Fatal(err)
				}
				status, sets := "labeled "+c.labeled, []string{"github label acme/webapp#7 " + c.labeled}
				if c.labeled == "" {
					status, sets = "labeled "+c.has[len(c.has)-1], nil
				}
				if got.Changed != (c.labeled != "") || got.Status != status || got.Was != "" || got.Thread.ID != "acme/webapp#7" ||
					!reflect.DeepEqual(s.Labels("github", "acme/webapp#7"), c.labels) || !reflect.DeepEqual(s.Sets(), sets) ||
					s.Status("github", "acme/webapp#7") != c.status {
					t.Fatalf("got %+v, labels %v, sets %v, the issue's %q\nwant %q, labels %v, sets %v", got,
						s.Labels("github", "acme/webapp#7"), s.Sets(), s.Status("github", "acme/webapp#7"), status, c.labels, sets)
				}
				// Again: it has the label now, so nothing more is written.
				again, err := call("acme/webapp#7", c.word)
				if err != nil || again.Changed || again.Status != status || !reflect.DeepEqual(s.Sets(), sets) {
					t.Fatalf("again: %+v, %v, sets %v", again, err, s.Sets())
				}
			})
		}
	}
}

// Forward says where a labeled issue stands, as it does of a moved one.
func TestGitHubLabelStood(t *testing.T) {
	s := sources(t)
	s.Add(standin.Issue{Source: "github", Key: "acme/webapp#7", Title: "Retries", Status: "closed"})
	got, err := hooks.GitHub{}.Forward(context.Background(), "acme/webapp#7", "label:shipped", "gh-yardmaster")
	if err != nil || got.Stood != hooks.StateDone || !got.Changed {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// GitHub has a label for a word; a declared source, where yardr sets
// none, has not, and a label without a name is no word anywhere.
func TestLabelPlaces(t *testing.T) {
	d, err := source.Parse("acme.yaml", []byte(acmeThread))
	if err != nil {
		t.Fatal(err)
	}
	for _, word := range []string{"label:in-review", "label:In Review", "label:a:b"} {
		if err := (hooks.GitHub{}).Places(word); err != nil || !hooks.Word(word) {
			t.Errorf("github %q: %v, a word %v", word, err, hooks.Word(word))
		}
		if err := (hooks.Declared{D: d}).Places(word); err == nil || !hooks.Final(err) || !strings.Contains(err.Error(), "acme: yardr sets no label there") {
			t.Errorf("declared %q: %v", word, err)
		}
	}
	for _, word := range []string{"label:", "label: x", "label:x ", "Label:x", "labels:x"} {
		if err := (hooks.GitHub{}).Places(word); err == nil || !strings.Contains(err.Error(), "is no state") || hooks.Word(word) {
			t.Errorf("github %q: %v, a word %v", word, err, hooks.Word(word))
		}
	}
}

// What yardr refuses itself is final; what the source fails at is not.
func TestForwardRefusals(t *testing.T) {
	ctx := context.Background()
	s := sources(t)
	s.Add(standin.Issue{Source: "github", Key: "acme/webapp#7", Title: "Retries", Status: "open"})
	s.Add(standin.Issue{Source: "github", Key: "acme/webapp#8", Title: "A change", Status: "open", Pull: true})
	for _, c := range []struct {
		name  string
		call  func() (hooks.Forwarded, error)
		want  string
		final bool
	}{
		{"github review", func() (hooks.Forwarded, error) {
			return hooks.GitHub{}.Forward(ctx, "acme/webapp#7", "review", "gh-yardmaster")
		}, "no review status", true},
		{"github pull request", func() (hooks.Forwarded, error) {
			return hooks.GitHub{}.Forward(ctx, "acme/webapp#8", "done", "gh-yardmaster")
		}, "is a pull request", true},
		{"github no state", func() (hooks.Forwarded, error) {
			return hooks.GitHub{}.Forward(ctx, "acme/webapp#7", "closed", "gh-yardmaster")
		}, "is no state", true},
		{"github label, bad thread", func() (hooks.Forwarded, error) {
			return hooks.GitHub{}.Forward(ctx, "webapp", "label:in-review", "gh-yardmaster")
		}, "want owner/repo#number", true},
		{"github label, credential", func() (hooks.Forwarded, error) {
			return hooks.GitHub{}.Forward(ctx, "acme/webapp#7", "label:in-review", "other")
		}, "Bad credentials", false},
		{"github label, no issue", func() (hooks.Forwarded, error) {
			return hooks.GitHub{}.Forward(ctx, "acme/webapp#9", "label:in-review", "gh-yardmaster")
		}, "Not Found", false},
		{"github bad thread", func() (hooks.Forwarded, error) { return hooks.GitHub{}.Forward(ctx, "webapp", "done", "gh-yardmaster") }, "want owner/repo#number", true},
		{"github credential", func() (hooks.Forwarded, error) { return hooks.GitHub{}.Forward(ctx, "acme/webapp#7", "done", "other") }, "Bad credentials", false},
		{"github no issue", func() (hooks.Forwarded, error) {
			return hooks.GitHub{}.Forward(ctx, "acme/webapp#9", "done", "gh-yardmaster")
		}, "Not Found", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.call()
			if err == nil || !strings.Contains(err.Error(), c.want) || hooks.Final(err) != c.final {
				t.Fatalf("err %v (final %v), want %q (final %v)", err, hooks.Final(err), c.want, c.final)
			}
		})
	}
	if sets := s.Sets(); len(sets) != 0 {
		t.Fatalf("sets %v, want none", sets)
	}
}

// An issue is named from the fixed form of its URL alone.
func TestIssueThread(t *testing.T) {
	for _, c := range []struct{ source, url, want string }{
		{"github", "https://github.com/acme/webapp/issues/7", "acme/webapp#7"},
		// A pull request a bead tracks is named as an issue is: Forward
		// is what refuses to set its status.
		{"github", "https://github.com/acme/webapp/pull/7", "acme/webapp#7"},
		{"github", "https://github.com/acme/webapp/discussions/7", ""},
		{"github", "https://github.com/acme/webapp/issues/x", ""},
	} {
		f, ok := hooks.ForwarderFor(c.source, nil, "")
		if !ok {
			t.Fatalf("%s has no Forwarder", c.source)
		}
		got, err := f.IssueThread(c.url)
		if got != c.want || (err == nil) != (c.want != "") || (err != nil && !hooks.Final(err)) {
			t.Errorf("%s: %q, %v; want %q", c.url, got, err, c.want)
		}
	}
	if got := hooks.Forwardable(); !reflect.DeepEqual(got, []string{"github"}) {
		t.Fatalf("forwardable %v", got)
	}
	if _, ok := hooks.ForwarderFor("token", nil, ""); ok {
		t.Fatal("token has a Forwarder")
	}
	gh, _ := hooks.ForwarderFor("github", nil, "")
	if gh.Places("review") == nil || gh.Places("done") != nil {
		t.Fatal("places: github has no review")
	}
	// The credential is read from what an env_file sets, by name; with
	// none of the names set the error says so in a way the caller can tell.
	file := map[string]string{"GITHUB_TOKEN": " gh-tok "}
	get := func(name string) string { return file[name] }
	if c, err := gh.CredentialFrom(get); err != nil || c != "gh-tok" {
		t.Fatalf("github credential %q, %v", c, err)
	}
	{
		f := hooks.Forwarder(gh)
		var missing *hooks.MissingCredential
		if _, err := f.CredentialFrom(func(string) string { return "" }); !errors.As(err, &missing) {
			t.Fatalf("nothing set: %v", err)
		}
	}
}
