package hooks_test

import (
	"context"
	"errors"
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
	if err := s.PutDepot(ctx, store.Depot{Name: "web", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"web-1", "web-2"} {
		if _, err := s.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: "web"}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

// No builtin links issues, so the store keeps every link's URL as given:
// a client that is not the CLI, which fixes no URL before it calls,
// lands on the link as it sent it. The CLI names the fixed form (yardr
// link), from the yard's declared sources.
func TestStoreKeepsLinkURLsAsGiven(t *testing.T) {
	s, ctx := socketStore(t)
	for _, c := range []struct {
		kind   string
		source string
		url    string
	}{
		{store.LinkTracks, "github", "https://github.com/acme/webapp/issues/7/showed-links"},
		{store.LinkRelates, "github", "http://GITHUB.com/Acme/WebApp/issues/7?focus=1"},
		{store.LinkRelates, "github", "https://github.com/acme/webapp/discussions/7"},
		{store.LinkRelates, "github", "https://linear.app/acme/issue/YRD-12"},
		{store.LinkRelates, "linear", "https://linear.app/acme/issue/YRD-12/web-view"},
		{store.LinkRelates, "token", "https://tracker.example/Issues/7/A-Title"},
	} {
		head, _ := s.Head(ctx)
		ch, err := s.AddLink(ctx, "web-1", store.NewLink{Kind: c.kind, Source: c.source, URL: c.url}, "me")
		if err != nil || ch.Link.URL != c.url {
			t.Fatalf("AddLink %s %s = %+v, %v; want it kept as given", c.source, c.url, ch, err)
		}
		if now, _ := s.Head(ctx); now == head {
			t.Fatalf("AddLink %s %s logged nothing", c.source, c.url)
		}
	}
	const other = "https://tracker.example/Issues/7/A-Title"
	if ls, err := s.IssueLinks(ctx, other); err != nil || len(ls) != 1 {
		t.Fatalf("IssueLinks(%s) = %+v, %v", other, ls, err)
	}
	// A URL on a host no source has issues on is looked up as given.
	if err := s.RemoveLink(ctx, "web-1", "https://tracker.example/issues/7", "me"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("RemoveLink under another spelling of an unfixed URL: %v; want not found", err)
	}
}
