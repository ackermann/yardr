package dispatch_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// pullBead is one bead of TestJobNamesThePullRequestTheBeadTracks: what
// it links to, the pull request its metadata says is its own (own_pr, none
// when empty), and the pull its job names, if any.
type pullBead struct {
	title, typ, kind, url, own string
	pull                       *dispatch.Pull
}

// The job of a bead that tracks a pull request names it, so the runner
// can put the worktree on its head. A bead that tracks an issue, one that
// only relates to a pull request, a train, whose pull request is of its own
// branch, and a bead that opened the pull request itself from its own branch
// (the public line's), whose sessions work on that branch, are started as
// ever.
func TestJobNamesThePullRequestTheBeadTracks(t *testing.T) {
	t.Parallel()
	const pr = "https://github.com/acme/web/pull/7"
	for _, tc := range []struct {
		name   string // the former test name
		source string // the shipped pack, or a declared one
		file   string // the declaration read from here; empty takes text
		text   string
		beads  []pullBead
	}{
		{
			// The pull request's shape is the pack's link to read.
			name:   "TestJobNamesThePullRequestTheBeadTracks",
			source: "github",
			file:   filepath.Join("..", "..", "packs", "github", "github.yaml"),
			beads: []pullBead{
				{"theirs", "", store.LinkTracks, pr, "", &dispatch.Pull{URL: pr, Number: 7}},
				// The pull request it opened from its own branch is its work, not
				// somebody else's; another it tracks, or an own_pr that names a
				// different one, is still read.
				{"its own", "", store.LinkTracks, "https://github.com/acme/web/pull/10", "https://github.com/acme/web/pull/10", nil},
				{"own is another", "", store.LinkTracks, "https://github.com/acme/web/pull/11", pr, &dispatch.Pull{URL: "https://github.com/acme/web/pull/11", Number: 11}},
				{"an issue", "", store.LinkTracks, "https://github.com/acme/web/issues/8", "", nil},
				{"relates", "", store.LinkRelates, pr, "", nil},
				{"no link", "", "", "", "", nil},
				{"train", store.TypeTrain, store.LinkTracks, "https://github.com/acme/web/pull/9", "", nil},
			},
		},
		{
			// A declared source's pull request is named as a github
			// one's is.
			name:   "TestJobNamesTheForgePullRequestTheBeadTracks",
			source: "forge",
			text:   forgeDecl,
			beads: []pullBead{
				{"theirs", "", store.LinkTracks, forgePull, "", &dispatch.Pull{URL: forgePull, Number: 7}},
				{"an issue", "", store.LinkTracks, forgeIssue, "", nil},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, d := setup(t)
			ctx := context.Background()
			text := tc.text
			if tc.file != "" {
				b, err := os.ReadFile(tc.file)
				must(t, err)
				text = string(b)
			}
			_, err := s.PutSource(ctx, tc.source+".yaml", text, "me")
			must(t, err)
			must(t, s.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "fake", Limit: 9}, "me"))
			_, err = s.AddRoute(ctx, store.Route{Stage: "new", Group: "reviewers"}, "me")
			must(t, err)

			bead := func(title, typ, kind, url, own string) string {
				t.Helper()
				nb := store.NewBead{Title: title, Type: typ}
				if own != "" {
					nb.Metadata = map[string]any{"own_pr": own}
				}
				b, err := s.CreateBead(ctx, nb, "me")
				must(t, err)
				if url != "" {
					_, err = s.AddLink(ctx, b.ID, store.NewLink{Kind: kind, Source: tc.source, URL: url}, "me")
					must(t, err)
				}
				return b.ID
			}
			want := map[string]*dispatch.Pull{}
			for _, b := range tc.beads {
				want[bead(b.title, b.typ, b.kind, b.url, b.own)] = b.pull
			}
			for range len(tc.beads) { // one start per pass for the group (starts.go)
				must(t, d.Reconcile(ctx))
			}
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
		})
	}
}
