package hooks_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	_ "yardr.dev/yardr/internal/hooks" // registers the fixed form with the store
	"yardr.dev/yardr/internal/rpc"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// socketStore is a store with beads web-1 and web-2, reached through the
// unix-socket client whatever the suite runs as: a client that is not the
// CLI, which fixes no URL before it calls.
func socketStore(t *testing.T) (store.API, context.Context) {
	t.Helper()
	t.Setenv("YARDR_TEST_RPC", "1")
	s := storetest.New(t)
	if _, ok := s.(*rpc.Client); !ok {
		t.Fatalf("storetest.New = %T, want the socket client", s)
	}
	ctx := context.Background()
	if err := s.PutRig(ctx, store.Rig{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"web-1", "web-2"} {
		if _, err := s.CreateBead(ctx, store.NewBead{ID: id, Title: id, Rig: "web"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

// The store brings a link's URL to the issue's fixed form itself, so a
// client that sends another spelling of an issue (a title slug, other case)
// lands on the link the issue has: the second spelling makes no second
// issue, and the one-tracker-per-issue rule holds across spellings.
func TestStoreLinksOneIssueUnderAnySpelling(t *testing.T) {
	s, ctx := socketStore(t)
	const (
		fixed = "https://linear.app/acme/issue/YRD-12"
		slug  = "https://linear.app/acme/issue/YRD-12/web-view-shows-links"
		cased = "http://LINEAR.app/Acme/issue/yrd-12/?focus=1"
	)
	ch, err := s.AddLink(ctx, "web-1", store.NewLink{Kind: store.LinkTracks, Source: "linear", URL: slug, Thread: "issue-12"}, "me")
	if err != nil || ch.Link.URL != fixed {
		t.Fatalf("AddLink under the slug = %+v, %v; want the link at %s", ch, err, fixed)
	}

	// The same bead, the same issue, another spelling: the link it has.
	_, err = s.AddLink(ctx, "web-1", store.NewLink{Kind: store.LinkTracks, Source: "linear", URL: cased}, "me")
	if err == nil || !strings.Contains(err.Error(), "web-1 already tracks "+fixed) {
		t.Fatalf("a second spelling on the same bead: %v; want it refused as the link the bead has", err)
	}
	// Another bead cannot track it under another spelling.
	_, err = s.AddLink(ctx, "web-2", store.NewLink{Kind: store.LinkTracks, Source: "linear", URL: cased}, "me")
	if err == nil || !strings.Contains(err.Error(), fixed+" is tracked by web-1") {
		t.Fatalf("a second tracker under another spelling: %v; want it refused: web-1 tracks the issue", err)
	}
	// Nor at its creation.
	_, err = s.CreateBead(ctx, store.NewBead{ID: "web-3", Title: "three", Rig: "web",
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "linear", URL: cased}}}, "me")
	if err == nil || !strings.Contains(err.Error(), "is tracked by web-1") {
		t.Fatalf("a bead created tracking another spelling: %v; want it refused", err)
	}
	// A relates link under another spelling is a link to the same issue,
	// and takes the thread id the issue's first link came with.
	ch, err = s.AddLink(ctx, "web-2", store.NewLink{Kind: store.LinkRelates, Source: "linear", URL: cased}, "me")
	if err != nil || ch.Link.URL != fixed || ch.Link.Thread != "issue-12" {
		t.Fatalf("relates under another spelling = %+v, %v; want %s with thread issue-12", ch, err, fixed)
	}

	// The issue is found, moved and unlinked under any spelling too.
	ls, err := s.IssueLinks(ctx, cased)
	if err != nil || len(ls) != 2 || ls[0].Bead != "web-1" || ls[1].Bead != "web-2" || ls[0].URL != fixed || ls[1].URL != fixed {
		t.Fatalf("IssueLinks(%s) = %+v, %v; want web-1 and web-2 at %s", cased, ls, err, fixed)
	}
	mv, err := s.MoveLink(ctx, slug, "web-2", "me")
	if err != nil || mv.Link.Bead != "web-2" || mv.Link.URL != fixed || mv.From != "web-1" {
		t.Fatalf("MoveLink(%s) = %+v, %v", slug, mv, err)
	}
	if err := s.RemoveLink(ctx, "web-1", cased, "me"); err != nil {
		t.Fatalf("RemoveLink(%s): %v", cased, err)
	}
	if ls, err := s.Links(ctx, "web-1"); err != nil || len(ls) != 0 {
		t.Fatalf("links of web-1 after the remove = %+v, %v", ls, err)
	}
}

// What is no issue of the link's source is refused by the store, as the
// CLI refuses it: a pull request, a URL on another host. A source that
// links no issues has no fixed form, and its URL is kept as given.
func TestStoreRefusesWhatIsNoIssueOfTheSource(t *testing.T) {
	s, ctx := socketStore(t)
	for _, c := range []struct{ source, url, want string }{
		{"github", "https://github.com/acme/webapp/pull/7", "is a pull request, not an issue"},
		{"linear", "https://github.com/acme/webapp/issues/7", "no linear issue"},
		{"linear", "https://linear.app/acme/project/x", "is no Linear issue"},
	} {
		head, _ := s.Head(ctx)
		_, err := s.AddLink(ctx, "web-1", store.NewLink{Kind: store.LinkTracks, Source: c.source, URL: c.url}, "me")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("AddLink %s %s: %v; want it refused: %s", c.source, c.url, err, c.want)
		}
		if now, _ := s.Head(ctx); now != head {
			t.Fatalf("AddLink %s %s: refused, yet %d events were logged", c.source, c.url, now-head)
		}
	}
	const other = "https://tracker.example/Issues/7/A-Title"
	ch, err := s.AddLink(ctx, "web-1", store.NewLink{Kind: store.LinkTracks, Source: "token", URL: other}, "me")
	if err != nil || ch.Link.URL != other {
		t.Fatalf("AddLink of a source with no fixed form = %+v, %v; want %s kept as given", ch, err, other)
	}
	if ls, err := s.IssueLinks(ctx, other); err != nil || len(ls) != 1 {
		t.Fatalf("IssueLinks(%s) = %+v, %v", other, ls, err)
	}
	// A URL on a host no source has issues on is looked up as given.
	if err := s.RemoveLink(ctx, "web-1", "https://tracker.example/issues/7", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RemoveLink under another spelling of an unfixed URL: %v; want not found", err)
	}
}
