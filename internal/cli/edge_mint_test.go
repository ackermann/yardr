package cli

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/standin"
	"yardr.dev/yardr/internal/store"
)

// mintAs runs yardr with args and stdin as actor on st, edge calls by hc:
// what it prints, what it says on its error stream, and its error.
func mintAs(t *testing.T, st store.API, hc *http.Client, actor, stdin string, args ...string) (string, string, error) {
	t.Helper()
	a := appOn(st)
	a.edgeHTTP = hc
	var errs bytes.Buffer
	a.stderr = &errs
	root := a.rootCmd()
	root.SetIn(strings.NewReader(stdin))
	out, err := run(t, root, append(args, "--actor", actor)...)
	return out, errs.String(), err
}

// mintScopes is the hosted mint's fixed granted set: literals, not the
// command's mapping, so a mapping that drifts fails here.
var mintScopes = []string{
	"openid",
	"email",
	"https://www.googleapis.com/auth/drive.file",
	"https://www.googleapis.com/auth/documents",
	"https://www.googleapis.com/auth/presentations",
	"https://www.googleapis.com/auth/calendar.events",
	"https://www.googleapis.com/auth/calendar.calendarlist.readonly",
}

// mintRefusal holds a mint-google run to the one refusal there is: it
// fails, prints nothing on stdout and no token on either stream, and
// names the in-process mint and the source call that is the way.
func mintRefusal(t *testing.T, out, errs string, err error) {
	t.Helper()
	if !errors.Is(err, errCheckFailed) {
		t.Fatalf("mint-google = %v, want a refusal", err)
	}
	if code := exitCode(err); code == 0 {
		t.Error("a refused mint-google exits 0")
	}
	for _, want := range []string{"mints hosted Google in-process", "yardr call google-drive"} {
		if !strings.Contains(errs, want) {
			t.Errorf("the refusal names no %q: %q", want, errs)
		}
	}
	if out != "" || strings.Contains(out+errs, "ya29.secret") {
		t.Errorf("a token reached a stream: stdout %q stderr %q", out, errs)
	}
}

// yardr edge mint-google refuses every caller and mints nothing: the
// yard's server mints hosted Google in-process for a source call, so a
// run of the command, by the operator or under an agent's actor against
// a stand-in edge that would answer, prints no token, makes no mint and
// fetches no key beyond the connect's own.
func TestEdgeMintGoogleRefusesEveryCaller(t *testing.T) {
	t.Parallel()
	st, edge, srv := edgeYard(t)
	mustEdge(t, st, srv.Client(), "edge", "connect", srv.URL)
	edge.Behave(func(b *standin.EdgeBehaviour) { b.MintToken, b.MintGranted = "ya29.secret", mintScopes })
	in := `{"inputs":{"GOOGLE_ACCOUNT":"ada@example.com"},"scope":"thread-9"}`
	for _, actor := range []string{"jo", "yardmaster", "builder-1"} {
		out, errs, err := mintAs(t, st, srv.Client(), actor, in, "edge", "mint-google", srv.URL, "google-drive")
		mintRefusal(t, out, errs, err)
	}
	if s := edge.State(); len(s.Mints) != 0 || s.KeyFetches != 1 {
		t.Errorf("the edge took %+v, want no mint and only the connect's key fetch", s)
	}
	// Its help says the same as its refusal.
	a := appOn(st)
	if cmd := a.edgeMintGoogleCmd(); !strings.Contains(cmd.Long, "in-process") || !strings.Contains(cmd.Long, "yardr call") {
		t.Errorf("mint-google's help does not name the in-process mint and the source call:\n%s", cmd.Long)
	}
}

// The refusal comes before anything the command might have checked or
// called: a bad address or source, stdin holding no mint to read, inputs
// naming no account, and a yard with no key to sign with all meet it, and
// no edge is called for any of them.
func TestEdgeMintGoogleRefusesBeforeAnyCall(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		address string // "" is the edge's URL
		source  string
		stdin   string
	}{
		"a bad address":     {address: "https://my.yardr.dev/hooks", source: "google-drive", stdin: ""},
		"an unknown source": {source: "google-mail", stdin: ""},
		"nothing on stdin":  {source: "google-drive", stdin: ""},
		"no JSON object":    {source: "google-drive", stdin: `hello`},
		"no account":        {source: "google-drive", stdin: `{"inputs":{},"scope":"s"}`},
		"no yard key":       {source: "google-drive", stdin: `{"inputs":{"GOOGLE_ACCOUNT":"a@example.com"},"scope":"s"}`},
	} {
		st, edge, srv := edgeYard(t)
		addr := tc.address
		if addr == "" {
			addr = srv.URL
		}
		out, errs, err := mintAs(t, st, srv.Client(), "jo", tc.stdin, "edge", "mint-google", addr, tc.source)
		if !errors.Is(err, errCheckFailed) {
			t.Errorf("%s: = %v, want a refusal", name, err)
			continue
		}
		if !strings.Contains(errs, "mints hosted Google in-process") {
			t.Errorf("%s: stderr = %q, want the in-process refusal", name, errs)
		}
		if out != "" {
			t.Errorf("%s: a refused mint prints %q on stdout, want nothing", name, out)
		}
		if s := edge.State(); s.KeyFetches != 0 || len(s.Mints) != 0 {
			t.Errorf("%s: the edge was called: %+v", name, s)
		}
	}
}
