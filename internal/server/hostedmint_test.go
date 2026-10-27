package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/yardkey"
)

// The server's hosted minter makes the one signed call through the
// edge by the key the yard pinned, checks the granted scopes before
// the token is held, and hands back the token and its expiry alone: an
// ungranted source is refused with its token in no error, and nothing
// is fetched again for it.
func TestHostedMintFor(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	st := storetest.NewIn(t, home)
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	edge, err := standin.NewEdge()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(edge)
	t.Cleanup(srv.Close)
	granted := []string{"openid", "email", "https://www.googleapis.com/auth/drive.file",
		"https://www.googleapis.com/auth/documents", "https://www.googleapis.com/auth/presentations",
		"https://www.googleapis.com/auth/calendar.events", "https://www.googleapis.com/auth/calendar.calendarlist.readonly"}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.MintToken, b.MintGranted = "ya29.hosted", granted })
	if _, _, err := yardkey.Ensure(config.At(home).PeerKey()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, store.EdgeSetting, store.Edge{URL: srv.URL, Key: edge.PublicKey(), Handle: "mckean", Name: "mckean"}.String(), "jo"); err != nil {
		t.Fatal(err)
	}
	mint := hostedMintFor(st, config.At(home))
	token, expires, err := mint(ctx, srv.URL, "google-drive", "ada@example.com")
	if err != nil || token != "ya29.hosted" || time.Until(expires) < 59*time.Minute {
		t.Fatalf("mint = %q, %v, %v", token, expires, err)
	}
	if s := edge.State(); len(s.Mints) != 1 || s.Mints[0].Account != "ada@example.com" || s.KeyFetches != 0 {
		t.Errorf("the edge took %+v, want the one mint by the pinned key", s)
	}
	if _, _, err := mint(ctx, srv.URL, "unlisted-source", "ada@example.com"); err == nil ||
		!strings.Contains(err.Error(), "not served by the hosted Google mint") {
		t.Fatalf("an unknown hosted source = %v", err)
	}
	if s := edge.State(); len(s.Mints) != 1 {
		t.Errorf("an unknown source reached the edge: %+v", s)
	}
	edge.Behave(func(b *standin.EdgeBehaviour) {
		b.MintGranted = []string{"openid", "email", "https://www.googleapis.com/auth/documents"}
	})
	if _, _, err := mint(ctx, srv.URL, "google-drive", "ada@example.com"); err == nil ||
		!strings.Contains(err.Error(), "google-drive needs https://www.googleapis.com/auth/drive.file") ||
		!strings.Contains(err.Error(), "missing from the Google connection's grant") ||
		strings.Contains(err.Error(), "ya29.hosted") {
		t.Fatalf("an ungranted mint = %v", err)
	}
}
