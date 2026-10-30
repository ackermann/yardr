package store_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

func edgeKey(t *testing.T) string {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return store.Public(key)
}

// The edge's address is its origin: https, or http to this machine alone for
// a stand-in, and nothing after the host, since the edge answers at paths of
// its own that a signature covers.
func TestCheckEdgeURL(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"https://my.yardr.dev", "https://127.0.0.1:8443", "http://127.0.0.1:8080", "http://localhost:9", "http://[::1]:80"} {
		if err := store.CheckEdgeURL(ok); err != nil {
			t.Errorf("CheckEdgeURL(%q) = %v, want it taken", ok, err)
		}
	}
	for bad, want := range map[string]string{
		"":                           "a public address",
		"my.yardr.dev":               "a public address",
		"http://my.yardr.dev":        "http is for a stand-in on this machine only",
		"http://192.0.2.1":           "http is for a stand-in on this machine only",
		"ftp://my.yardr.dev":         "ftp:// is not https://",
		"https://u:p@my.yardr.dev":   "no user or password",
		"https://my.yardr.dev?a=b":   "no query or fragment",
		"https://my.yardr.dev#x":     "no query or fragment",
		"https://my.yardr.dev/":      "the edge's origin alone",
		"https://my.yardr.dev/hooks": "the edge's origin alone",
	} {
		if err := store.CheckEdgeURL(bad); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckEdgeURL(%q) = %v, want it refused with %q", bad, err, want)
		}
	}
}

// hook_public_url shares its checks with the edge's address, and keeps its
// own words.
func TestCheckHookPublicURLWords(t *testing.T) {
	t.Parallel()
	for bad, want := range map[string]string{
		"yard.example":                 `hook_public_url "yard.example": a public address, such as https://yard.example/hooks`,
		"https://u:p@yard.example":     `hook_public_url "https://u:p@yard.example": an address with no user or password in it`,
		"https://yard.example/h?a=b":   `hook_public_url "https://yard.example/h?a=b": an address with no query or fragment: the hook's name is added to its path`,
		"http://yard.example/hooks":    `hook_public_url "http://yard.example/hooks": http is for a stand-in on this machine only: a public address is https`,
		"ftp://yard.example/hooks":     `hook_public_url "ftp://yard.example/hooks": ftp:// is not https://`,
		"https://yard.example/a/../b/": `hook_public_url "https://yard.example/a/../b/": its path has no '..' segment`,
	} {
		if err := store.CheckHookPublicURL(bad); err == nil || err.Error() != want {
			t.Errorf("CheckHookPublicURL(%q) = %v, want %q", bad, err, want)
		}
	}
	for _, ok := range []string{"https://yard.example/hooks", "https://yard.example", "http://127.0.0.1:8790/hooks"} {
		if err := store.CheckHookPublicURL(ok); err != nil {
			t.Errorf("CheckHookPublicURL(%q) = %v", ok, err)
		}
	}
}

// The edge setting holds the address, the pinned key, the account's handle,
// this yard's name and what hook_public_url was; it reads back as it was
// written, and nothing that connecting would not have written is read.
func TestEdgeValue(t *testing.T) {
	t.Parallel()
	key := edgeKey(t)
	for _, e := range []store.Edge{
		{URL: "https://my.yardr.dev", Key: key, Handle: "mckean", Name: "airy"},
		{URL: "http://127.0.0.1:8080", Key: key, Handle: "a1-b2", Name: "a1-b2", HookPublicURL: "https://hooks.example.net/hooks"},
	} {
		got, err := store.ParseEdge(e.String())
		if err != nil || got != e {
			t.Errorf("ParseEdge(%s) = %+v, %v; want it back", e, got, err)
		}
		if err := store.CheckEdge(e.String()); err != nil {
			t.Errorf("CheckEdge(%s) = %v", e, err)
		}
	}
	e := store.Edge{URL: "https://my.yardr.dev", Key: key, Handle: "mckean", Name: "airy"}
	if got := e.HooksURL(); got != "https://my.yardr.dev/hooks/mckean/airy" {
		t.Errorf("HooksURL = %q, want the address the edge takes this yard's webhooks at", got)
	}
	// A value written before yards had names holds none, and reads as named
	// as its account.
	old := store.Edge{URL: e.URL, Key: key, Handle: "mckean"}
	if got, err := store.ParseEdge(old.String()); err != nil || got.Name != "mckean" ||
		got.HooksURL() != "https://my.yardr.dev/hooks/mckean/mckean" {
		t.Errorf("ParseEdge(%s) = %+v, %v; want it named as its account", old.String(), got, err)
	}
	// The name a yard proposes is the edge's own rule, one segment of a path
	// in lower case, taken or refused with it.
	for _, ok := range []string{"airy", "a", "a1-b2", strings.Repeat("a", 63)} {
		if err := store.CheckEdgeName(ok); err != nil {
			t.Errorf("CheckEdgeName(%q) = %v, want it taken", ok, err)
		}
	}
	for _, bad := range []string{"", "Airy", "a b", "a.b", "a/b", "../x", strings.Repeat("a", 64)} {
		if err := store.CheckEdgeName(bad); err == nil || !strings.Contains(err.Error(), "lower case letters, digits and '-'") {
			t.Errorf("CheckEdgeName(%q) = %v, want it refused with the rule", bad, err)
		}
	}
	// What hook_public_url is set to while connected is a value that setting
	// takes.
	if err := store.CheckHookPublicURL(e.HooksURL()); err != nil {
		t.Errorf("the edge's hooks address is no hook_public_url: %v", err)
	}

	for name, tc := range map[string]struct{ value, want string }{
		"not JSON":           {`https://my.yardr.dev`, "not what yardr edge connect writes"},
		"JSON of no edge":    {`[]`, "not what yardr edge connect writes"},
		"nothing":            {`{}`, "a public address"},
		"null":               {`null`, "a public address"},
		"junk after":         {e.String() + "x", "not what yardr edge connect writes"},
		"plain http":         {store.Edge{URL: "http://my.yardr.dev", Key: key, Handle: "mckean"}.String(), "http is for a stand-in on this machine only"},
		"a path":             {store.Edge{URL: "https://my.yardr.dev/x", Key: key, Handle: "mckean"}.String(), "the edge's origin alone"},
		"a short key":        {store.Edge{URL: e.URL, Key: key[:10], Handle: "mckean"}.String(), "the edge's key"},
		"no key":             {store.Edge{URL: e.URL, Handle: "mckean"}.String(), "the edge's key"},
		"upper case":         {store.Edge{URL: e.URL, Key: key, Handle: "McKean"}.String(), "lower case letters, digits and '-'"},
		"a path handle":      {store.Edge{URL: e.URL, Key: key, Handle: "../x"}.String(), "lower case letters, digits and '-'"},
		"a dotted handle":    {store.Edge{URL: e.URL, Key: key, Handle: "a.b"}.String(), "lower case letters, digits and '-'"},
		"no handle":          {store.Edge{URL: e.URL, Key: key}.String(), "lower case letters, digits and '-'"},
		"a long handle":      {store.Edge{URL: e.URL, Key: key, Handle: strings.Repeat("a", 64)}.String(), "lower case letters, digits and '-'"},
		"an upper-case name": {store.Edge{URL: e.URL, Key: key, Handle: "mckean", Name: "Airy"}.String(), "lower case letters, digits and '-'"},
		"a dotted name":      {store.Edge{URL: e.URL, Key: key, Handle: "mckean", Name: "a.b"}.String(), "lower case letters, digits and '-'"},
		"a long name":        {store.Edge{URL: e.URL, Key: key, Handle: "mckean", Name: strings.Repeat("a", 64)}.String(), "lower case letters, digits and '-'"},
		"an http address to put back": {store.Edge{URL: e.URL, Key: key, Handle: "mckean", HookPublicURL: "http://hooks.example.net"}.String(),
			"the hook_public_url to put back"},
	} {
		if _, err := store.ParseEdge(tc.value); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: ParseEdge(%s) = %v, want it refused with %q", name, tc.value, err, tc.want)
		}
		if err := store.CheckEdge(tc.value); err == nil {
			t.Errorf("%s: CheckEdge took %s", name, tc.value)
		}
	}

	// The setting's value out of a yard's settings.
	if got, connected, err := store.EdgeOf(map[string]string{}); err != nil || connected || got != (store.Edge{}) {
		t.Errorf("EdgeOf with no setting = %+v, %v, %v", got, connected, err)
	}
	if got, connected, err := store.EdgeOf(map[string]string{store.EdgeSetting: e.String()}); err != nil || !connected || got != e {
		t.Errorf("EdgeOf = %+v, %v, %v; want %+v", got, connected, err, e)
	}
	if _, connected, err := store.EdgeOf(map[string]string{store.EdgeSetting: `{"url":"x"}`}); err == nil || connected {
		t.Errorf("EdgeOf of a value it cannot read = connected %v, %v; want the error", connected, err)
	}
}
