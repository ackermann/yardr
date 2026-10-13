package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// The job of a bead that tracks a GitHub pull request names it, so the
// runner can put the worktree on its head. A bead that tracks an issue, one
// that only relates to a pull request, and a train, whose pull request is of
// its own branch, are started as ever.
func TestJobNamesThePullRequestTheBeadTracks(t *testing.T) {
	s, f, d := setup(t)
	ctx := context.Background()
	// The pull request's shape is the pack's link to read.
	text, err := os.ReadFile(filepath.Join("..", "..", "packs", "github", "github.yaml"))
	must(t, err)
	_, err = s.PutSource(ctx, "github.yaml", string(text), "me")
	must(t, err)
	must(t, s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "fake", Limit: 9}, "me"))
	_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "reviewers"}, "me")
	must(t, err)

	const pr = "https://github.com/acme/web/pull/7"
	bead := func(title, typ, kind, url string) string {
		t.Helper()
		b, err := s.CreateBead(ctx, store.NewBead{Title: title, Type: typ}, "me")
		must(t, err)
		if url != "" {
			_, err = s.AddLink(ctx, b.ID, store.NewLink{Kind: kind, Source: "github", URL: url}, "me")
			must(t, err)
		}
		return b.ID
	}
	tracks := bead("theirs", "", store.LinkTracks, pr)
	want := map[string]*dispatch.Pull{
		tracks: {URL: pr, Number: 7},
		bead("an issue", "", store.LinkTracks, "https://github.com/acme/web/issues/8"):         nil,
		bead("relates", "", store.LinkRelates, pr):                                             nil,
		bead("no link", "", "", ""):                                                            nil,
		bead("train", store.TypeTrain, store.LinkTracks, "https://github.com/acme/web/pull/9"): nil,
	}
	must(t, d.Reconcile(ctx))
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.jobs) != len(want) {
		t.Fatalf("%d jobs started, want %d", len(f.jobs), len(want))
	}
	for _, j := range f.jobs {
		w := want[j.Bead.ID]
		if (j.Pull == nil) != (w == nil) || (w != nil && *j.Pull != *w) {
			t.Errorf("%s (%s): Pull = %+v, want %+v", j.Bead.ID, j.Bead.Title, j.Pull, w)
		}
	}
}
