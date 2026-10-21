package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The JWKS redirect rule lets plain http through to the loopback, for
// tests, and to the hosted router, whose keys the web_jwt_jwks setting
// lets come over http; any other http target is refused, and an https
// fetch is never downgraded, to the router either.
func TestCheckJWKSRedirect(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name string
		to   string
		via  string
		want string // "" allows
	}{
		{"https target", "https://keys.example.net/jwks", "http://127.0.0.1/certs", ""},
		{"loopback target", "http://127.0.0.1:9/jwks", "http://127.0.0.1/certs", ""},
		{"localhost target", "http://localhost:9/jwks", "http://127.0.0.1/certs", ""},
		{"router target", "http://router:8080/jwks.json", "http://router:8080/certs", ""},
		{"router target in another case", "http://Router:8080/jwks.json", "http://router:8080/certs", ""},
		{"another single-label host", "http://gateway/jwks.json", "http://127.0.0.1/certs", "non-loopback"},
		{"a public host", "http://keys.example.net/jwks", "http://127.0.0.1/certs", "non-loopback"},
		{"a subdomain of the router", "http://router.example.net/jwks", "http://127.0.0.1/certs", "non-loopback"},
		{"a downgrade to the loopback", "http://127.0.0.1:9/jwks", "https://keys.example.net/certs", "HTTPS-to-HTTP"},
		{"a downgrade to the router", "http://router:8080/jwks.json", "https://keys.example.net/certs", "HTTPS-to-HTTP"},
	} {
		to, err := url.Parse(c.to)
		if err != nil {
			t.Fatal(err)
		}
		via, err := url.Parse(c.via)
		if err != nil {
			t.Fatal(err)
		}
		err = checkJWKSRedirect(&http.Request{URL: to}, []*http.Request{{URL: via}})
		if c.want == "" && err != nil {
			t.Errorf("%s: refused: %v", c.name, err)
		} else if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%s: = %v, want a refusal naming %q", c.name, err, c.want)
		}
	}
}
