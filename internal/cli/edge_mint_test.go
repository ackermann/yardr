package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/yardkey"
)

// mintAs runs yardr with args and stdin as jo on st, edge calls by hc: what
// it prints, what it says on its error stream, and its error.
func mintAs(t *testing.T, st store.API, hc *http.Client, stdin string, args ...string) (string, string, error) {
	t.Helper()
	a := appOn(st)
	a.edgeHTTP = hc
	var errs bytes.Buffer
	a.stderr = &errs
	root := a.rootCmd()
	root.SetIn(strings.NewReader(stdin))
	out, err := run(t, root, append(args, "--actor", "jo")...)
	return out, errs.String(), err
}

// mintScopes is the hosted mint's fixed granted set, as the design's 3.2 and
// 3.6 draw it: literals, not the command's mapping, so a mapping that drifts
// fails here.
var mintScopes = []string{
	"openid",
	"email",
	"https://www.googleapis.com/auth/drive.file",
	"https://www.googleapis.com/auth/documents",
	"https://www.googleapis.com/auth/presentations",
	"https://www.googleapis.com/auth/calendar.events",
	"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
}

// mintKey makes a key for the yard st's runs are on, to sign mints with.
func mintKey(t *testing.T, st store.API) {
	t.Helper()
	if _, _, err := yardkey.Ensure(config.At(yardEnv(t, st)["YARDR_HOME"]).PeerKey()); err != nil {
		t.Fatal(err)
	}
}

// yardr edge mint-google mints a Google access token through the edge: it
// reads {inputs, scope} on stdin, calls signed with the yard's key, and
// prints {token, expires_at} with the granted scopes checked and stripped.
// A yard whose edge setting names the edge's URL mints by the key it
// pinned, and fetches nothing.
func TestEdgeMintGoogle(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.MintToken, b.MintGranted = "ya29.access", mintScopes })
	out, errs, err := mintAs(t, st, srv.Client(), `{"inputs":{"GOOGLE_ACCOUNT":"ada@example.com"},"scope":"thread-9"}`,
		"edge", "mint-google", srv.URL, "google-drive")
	if err != nil {
		t.Fatalf("mint-google: %v\n%s%s", err, out, errs)
	}
	if errs != "" {
		t.Errorf("a mint that answers says nothing on stderr: %q", errs)
	}
	var got struct {
		Token     string `json:"token"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Token != "ya29.access" || got.ExpiresAt == "" {
		t.Fatalf("mint-google printed %q, want {token, expires_at}", out)
	}
	if strings.Contains(out, "granted_scopes") {
		t.Errorf("the granted scopes reach the mint's answer: %q", out)
	}
	expires, err := time.Parse(time.RFC3339, got.ExpiresAt)
	if err != nil || time.Until(expires) < 59*time.Minute {
		t.Errorf("expires_at = %q (%v), want about an hour out", got.ExpiresAt, err)
	}
	yard, ok := yardKeyOf(t, st)
	if !ok {
		t.Fatal("the yard has no key")
	}
	mints := edge.State().Mints
	if len(mints) != 1 || mints[0].Key != yard || mints[0].Account != "ada@example.com" || mints[0].Time == "" || mints[0].Sig == "" {
		t.Errorf("the edge took %+v, want the one call as this yard for ada@example.com", mints)
	}
	if n := edge.State().KeyFetches; n != 1 {
		t.Errorf("the edge's key was fetched %d times, want the connect's fetch alone: a pinned key is never fetched again", n)
	}
}

// A yard with no edge setting naming the edge fetches its key first, as a
// connect would, and mints by it.
func TestEdgeMintGoogleFetchesTheKeyItHasNotPinned(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mintKey(t, st)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.MintToken, b.MintGranted = "ya29.access", mintScopes })
	out, errs, err := mintAs(t, st, srv.Client(), `{"inputs":{"GOOGLE_ACCOUNT":"ada@example.com"},"scope":""}`,
		"edge", "mint-google", srv.URL, "google-calendar")
	if err != nil {
		t.Fatalf("mint-google: %v\n%s%s", err, out, errs)
	}
	if !strings.Contains(out, "ya29.access") {
		t.Errorf("mint-google printed %q, want the token", out)
	}
	if s := edge.State(); s.KeyFetches != 1 || len(s.Mints) != 1 {
		t.Errorf("the edge took %+v, want the key fetched once and the one call", s)
	}
}

// An answer whose granted scopes miss the named source's scope refuses,
// naming the source, the missing scope and where to reconnect, and its
// token reaches neither stream; the same answer mints for a granted source.
func TestEdgeMintGoogleRefusesAnUngrantedSource(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	edge.Behave(func(b *standin.EdgeBehaviour) {
		b.MintToken, b.MintGranted = "ya29.secret", []string{"openid", "email", "https://www.googleapis.com/auth/documents"}
	})
	in := `{"inputs":{"GOOGLE_ACCOUNT":"ada@example.com"},"scope":"s"}`
	out, errs, err := mintAs(t, st, srv.Client(), in, "edge", "mint-google", srv.URL, "google-drive")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("mint-google for an ungranted source = %v, want a refusal", err)
	}
	for _, want := range []string{"google-drive", "https://www.googleapis.com/auth/drive.file", srv.URL + "/google"} {
		if !strings.Contains(errs, want) {
			t.Errorf("the refusal names no %q: %q", want, errs)
		}
	}
	if out != "" || strings.Contains(out+errs, "ya29.secret") {
		t.Errorf("the refused token reached a stream: stdout %q stderr %q", out, errs)
	}
	// The same partial answer mints for the source it granted.
	out, errs, err = mintAs(t, st, srv.Client(), in, "edge", "mint-google", srv.URL, "google-docs")
	if err != nil || !strings.Contains(out, "ya29.secret") {
		t.Errorf("mint-google for the granted source = %q, %v (%s)", out, err, errs)
	}
}

// An answer signed by another key, and one signed by nothing, are refused,
// and their token reaches neither stream.
func TestEdgeMintGoogleBelievesOnlyThePinnedKeysSignature(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.MintToken, b.MintGranted = "ya29.secret", mintScopes })
	_, other, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	in := `{"inputs":{"GOOGLE_ACCOUNT":"ada@example.com"},"scope":"s"}`
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Signer = other })
	out, errs, err := mintAs(t, st, srv.Client(), in, "edge", "mint-google", srv.URL, "google-drive")
	if !errors.Is(err, errCheckFailed) || !strings.Contains(errs, "not signed with the key this yard pinned") {
		t.Errorf("mint-google, answered by another key = %v (%q), want it refused as unsigned", err, errs)
	}
	if out != "" || strings.Contains(out+errs, "ya29.secret") {
		t.Errorf("the unsigned token reached a stream: stdout %q stderr %q", out, errs)
	}
	edge.Behave(func(b *standin.EdgeBehaviour) { b.Signer, b.MintUnsigned = b.Served, true })
	out, errs, err = mintAs(t, st, srv.Client(), in, "edge", "mint-google", srv.URL, "google-drive")
	if !errors.Is(err, errCheckFailed) || !strings.Contains(errs, "not signed with the key this yard pinned") {
		t.Errorf("mint-google, answered with no signature = %v (%q), want it refused as unsigned", err, errs)
	}
	if out != "" || strings.Contains(out+errs, "ya29.secret") {
		t.Errorf("the unsigned token reached a stream: stdout %q stderr %q", out, errs)
	}
}

// A signed refusal's words reach stderr whole, and the run fails.
func TestEdgeMintGoogleRepeatsASignedRefusal(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	words := "Google is not connected for this account: connect it at " + srv.URL + "/google, or set the source's mint_command back to a local mint"
	edge.Behave(func(b *standin.EdgeBehaviour) { b.MintRefuse, b.MintReason = http.StatusNotFound, words })
	out, errs, err := mintAs(t, st, srv.Client(), `{"inputs":{"GOOGLE_ACCOUNT":"ada@example.com"},"scope":"s"}`,
		"edge", "mint-google", srv.URL, "google-drive")
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("mint-google, refused = %v, want a refusal", err)
	}
	if !strings.Contains(errs, words) {
		t.Errorf("stderr holds %q, want the refusal's words whole", errs)
	}
	if out != "" {
		t.Errorf("a refused mint prints %q on stdout, want nothing", out)
	}
	if code := exitCode(err); code == 0 {
		t.Error("a refused mint exits 0")
	}
}

// What the command can refuse itself it refuses before any call: a bad
// address or source, stdin holding no mint to read, inputs naming no
// account, and a yard with no key to sign with.
func TestEdgeMintGoogleRefusesBeforeAnyCall(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		address string // "" is the edge's URL
		source  string
		stdin   string
		key     bool // the yard has a key to sign with
		want    []string
	}{
		"a bad address": {
			address: "https://my.yardr.dev/hooks", source: "google-drive", stdin: "",
			key:  true,
			want: []string{"the edge's origin alone"},
		},
		"an unknown source": {
			source: "google-mail", stdin: "",
			key:  true,
			want: []string{"google-mail", "google-drive", "google-docs", "google-slides", "google-calendar"},
		},
		"nothing on stdin": {
			source: "google-drive", stdin: "",
			key:  true,
			want: []string{"got nothing"},
		},
		"no JSON object": {
			source: "google-drive", stdin: `hello`,
			key:  true,
			want: []string{"no JSON object"},
		},
		"no scope": {
			source: "google-drive", stdin: `{"inputs":{"GOOGLE_ACCOUNT":"a@example.com"}}`,
			key:  true,
			want: []string{"got no scope"},
		},
		"no account": {
			source: "google-drive", stdin: `{"inputs":{},"scope":"s"}`,
			key:  true,
			want: []string{"name no GOOGLE_ACCOUNT"},
		},
		"no yard key": {
			source: "google-drive", stdin: `{"inputs":{"GOOGLE_ACCOUNT":"a@example.com"},"scope":"s"}`,
			key:  false,
			want: []string{"has no key", "connect"},
		},
	} {
		st, edge, srv := edgeYard(t)
		if tc.key {
			mintKey(t, st)
		}
		addr := tc.address
		if addr == "" {
			addr = srv.URL
		}
		out, errs, err := mintAs(t, st, srv.Client(), tc.stdin, "edge", "mint-google", addr, tc.source)
		if !errors.Is(err, errCheckFailed) {
			t.Errorf("%s: = %v, want a refusal", name, err)
			continue
		}
		for _, want := range tc.want {
			if !strings.Contains(errs, want) {
				t.Errorf("%s: stderr = %q, want %q", name, errs, want)
			}
		}
		if out != "" {
			t.Errorf("%s: a refused mint prints %q on stdout, want nothing", name, out)
		}
		if s := edge.State(); s.KeyFetches != 0 || len(s.Mints) != 0 {
			t.Errorf("%s: the edge was called: %+v", name, s)
		}
	}
}
