package pack

import (
	"net/http"
	"strings"
	"testing"
)

// A file of a pack may move within the host it is served from, and the
// client follows it; it does not follow one to another host or port, down
// from https to plain http at any hop, or past ten redirects. This is the
// rule alone: TestURLFSErrors drives the same refusal through the client,
// and https to http is here because a test server's certificate is not one
// the client trusts.
func TestPackRedirect(t *testing.T) {
	tenHops := make([]string, maxRedirects)
	for i := range tenHops {
		tenHops[i] = "https://packs.example/hop"
	}
	for _, c := range []struct {
		name string
		via  []string // the URLs asked so far, the first the pack's own
		to   string
		want string // what the refusal says, "" for a redirect that is followed
	}{
		{"a path of the same host", []string{"https://packs.example/a/pack.yaml"}, "https://packs.example/b/pack.yaml", ""},
		{"the same host in another case", []string{"https://packs.example/a"}, "https://PACKS.example/b", ""},
		{"a stand-in going up to https", []string{"http://127.0.0.1:8080/a"}, "https://127.0.0.1:8080/a", ""},
		{"another host", []string{"https://packs.example/a"}, "https://cdn.example/a?token=secret", "redirected to cdn.example, which is not the host the pack is served from"},
		{"another port of the host", []string{"https://packs.example/a"}, "https://packs.example:8443/a", "redirected to packs.example:8443, which is not the host the pack is served from"},
		{"https down to http", []string{"https://packs.example/a"}, "http://packs.example/a", "redirected from https to plain http"},
		{"a later hop down to http", []string{"https://packs.example/a", "https://packs.example/b"}, "http://packs.example/c", "redirected from https to plain http"},
		{"too many", tenHops, "https://packs.example/hop", "stopped after 10 redirects"},
	} {
		t.Run(c.name, func(t *testing.T) {
			get := func(u string) *http.Request {
				t.Helper()
				r, err := http.NewRequest(http.MethodGet, u, nil)
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			var via []*http.Request
			for _, u := range c.via {
				via = append(via, get(u))
			}
			err := packRedirect(get(c.to), via)
			switch {
			case c.want == "" && err != nil:
				t.Errorf("a redirect to %s was refused: %v", c.to, err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Errorf("a redirect to %s: %v, want %q", c.to, err, c.want)
			case err != nil && strings.Contains(err.Error(), "secret"):
				t.Errorf("the refusal repeats the redirect's query: %v", err)
			}
		})
	}
}
