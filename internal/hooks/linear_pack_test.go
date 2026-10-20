package hooks_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/source"
)

// The core sends a declared call's credential in the header the
// declaration's credential names and nowhere else a call has, so a
// declaration with no header reached Linear unauthenticated through yardr
// call linear viewer and yardr source check linear. Linear takes an OAuth
// access token as a Bearer token. The thread script builds its own header
// (a personal key bare, anything else as Bearer), which
// TestLinearPackAuthHeader holds.
func TestLinearPackDeclaredCallSendsTheCredential(t *testing.T) {
	dir := filepath.Join("..", "..", "packs", "linear")
	text, err := os.ReadFile(filepath.Join(dir, "linear.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := source.Parse(filepath.Join(dir, "linear.yaml"), text)
	if err != nil {
		t.Fatal(err)
	}
	if c := d.Credential; c == nil || c.Env != "LINEAR_API_KEY" || c.Header != "Authorization: Bearer {credential}" {
		t.Errorf("credential = %+v", d.Credential)
	}
	if d.Check != "viewer" {
		t.Fatalf("check = %q, want the viewer call", d.Check)
	}

	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"viewer":{"id":"u-yardr","name":"Yardr"}}}`)
	}))
	defer srv.Close()
	d.Base = srv.URL
	const cred = "lin_oauth_test"
	res, err := source.Do(context.Background(), d, d.Check, nil, cred)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK() || !strings.Contains(string(res.Answer), "u-yardr") {
		t.Errorf("the check call was answered %d with %s", res.Status, res.Answer)
	}
	if auth != "Bearer "+cred {
		t.Errorf("the check call was sent with Authorization %q, want %q", auth, "Bearer "+cred)
	}
}
