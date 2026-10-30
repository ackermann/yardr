package web_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/web"
)

// linesTile is the Lines tile's anchor: the pill it must never carry.
func linesTile(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, `<a class="tile" href="/flows?panel=1"`)
	if at < 0 {
		at = strings.Index(body, `<a class="tile open" href="/flows?panel=1"`)
	}
	if at < 0 {
		t.Fatal("the rail has no Lines tile")
	}
	end := strings.Index(body[at:], `</a>`)
	return body[at : at+end]
}

// countingInbox wraps a store and counts the inbox reads: the shell reads
// the acting user's inbox once per request.
type countingInbox struct {
	store.API
	n *int
}

func (s countingInbox) UserInbox(ctx context.Context, name string, limit int, actor string) (*store.Mailbox, error) {
	*s.n++
	return s.API.UserInbox(ctx, name, limit, actor)
}

// The shell reads the acting user's inbox once per request: the talk page
// passes its own read through for the Crew pill instead of a second scan,
// and a page that reads none costs one.
func TestRailReadsInboxOnce(t *testing.T) {
	t.Parallel()
	for _, path := range []string{"/talk", "/"} {
		st := storetest.New(t)
		var n int
		home := filepath.Join(t.TempDir(), ".depot")
		srv, err := web.NewServer(countingInbox{API: st, n: &n}, home, nil)
		must(t, err)
		token := srv.ActAs("jo")
		ts := httptest.NewServer(srv)
		t.Cleanup(func() { shut(ts) })
		f := &fixture{t: t, st: st, ts: ts, home: home}
		f.signIn(srv, token)
		ctx := context.Background()
		must(t, func() error { _, err := st.AddUser(ctx, "jo", "jo"); return err }())
		must(t, func() error {
			_, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr"}, "jo")
			return err
		}())
		if r := f.get(path); r.status != 200 || strings.Contains(r.body, `class="pill"`) {
			t.Errorf("%s has a Crew pill with no unread mail: %s", path, r.body)
		}
		n = 0
		must(t, st.Mail(ctx, "jo", "check the gate", "yardmaster"))
		r := f.get(path)
		if r.status != 200 {
			t.Fatalf("%s: status %d: %s", path, r.status, r.body)
		}
		if !strings.Contains(r.body, `title="Crew — 1 unread letter" aria-label="Crew — 1 unread letter"`) {
			t.Errorf("%s renders no Crew count from its one inbox read: %s", path, r.body)
		}
		if n != 1 {
			t.Errorf("%s reads the inbox %d times, want once", path, n)
		}
	}
	// Where the view acts as no user the shell reads no inbox at all.
	st := storetest.New(t)
	var n int
	home := filepath.Join(t.TempDir(), ".depot")
	srv, err := web.NewServer(countingInbox{API: st, n: &n}, home, nil)
	must(t, err)
	ts := httptest.NewServer(srv)
	t.Cleanup(func() { shut(ts) })
	f := &fixture{t: t, st: st, ts: ts, home: home}
	if r := f.get("/"); r.status != 200 {
		t.Fatalf("/: status %d: %s", r.status, r.body)
	}
	if n != 0 {
		t.Errorf("/ with no acting user reads the inbox %d times, want none", n)
	}
}
