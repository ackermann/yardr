package web

// A view that acts as one fixed user (--as) has no gateway to say who is at
// the browser. Loopback is open to every process on the machine and to every
// page the visitor's browser opens, and the Origin header ownOrigin compares
// is a client's own word, so a process that sets it passes. Outside JWT mode
// such a view therefore asks for a secret of its own, one per serve:
//
//   - the token: 256 random bits that the command writes to a file only the
//     operator reads and prints once in a URL (/?token=...);
//   - the cookie the token buys: a GET carrying it is answered with an
//     HttpOnly, SameSite=Strict cookie and a redirect to the same URL without
//     it, so the token does not stay in the address bar or the history;
//   - the CSRF value every form of the view carries, which a write must send
//     back beside the cookie. SameSite does not keep out a page on another
//     port of the same host, the sandbox origin's for one, so the cookie
//     alone does not show that the request was made from one of the view's
//     own pages.
//
// A request without the cookie reads the yard and acts as no one, as a view
// with no --as does, and a write without both proofs is answered 403. JWT
// mode has its own proof of who the visitor is and takes none of this.

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// viewToken is the secret of one view that acts as a user, and the two
// values a browser holds once it has shown it.
type viewToken struct {
	// secret is the token itself, in hex. It is in the file the command
	// writes and the URL it prints, and in no response.
	secret string
	// cookie and csrf are keyed hashes of the secret, each for one purpose:
	// neither gives away the other or the token, which a URL in a history
	// or a scrollback is likelier to leak than a cookie is.
	cookie, csrf string
}

// newViewToken is a fresh token. Every serve has its own, so one that
// stopped leaves no cookie that works on the next.
func newViewToken() *viewToken {
	var b [32]byte
	_, _ = rand.Read(b[:])
	t := &viewToken{secret: hex.EncodeToString(b[:])}
	t.cookie, t.csrf = t.derive("cookie"), t.derive("csrf")
	return t
}

func (t *viewToken) derive(purpose string) string {
	m := hmac.New(sha256.New, []byte(t.secret))
	m.Write([]byte("yardr web " + purpose))
	return hex.EncodeToString(m.Sum(nil))
}

// sameSecret compares in constant time, so the time a refusal takes says
// nothing of how much of the secret was right.
func sameSecret(given, want string) bool {
	return subtle.ConstantTimeCompare([]byte(given), []byte(want)) == 1
}

// tokenHeldKey marks a request whose cookie is the view's.
type tokenHeldKey struct{}

// cookieName is the name the cookie goes by for a request to hostport. A
// browser sends a host's cookies to every port of it, so two views on one
// machine (a second yard, say) would trade one cookie between them if both
// used one name.
func cookieName(hostport string) string {
	if _, port, err := net.SplitHostPort(hostport); err == nil {
		if n, err := strconv.ParseUint(port, 10, 16); err == nil {
			return "yardr_web_" + strconv.FormatUint(n, 10)
		}
	}
	return "yardr_web"
}

// csrfValue is the value a form of the view carries, "" where no token
// guards the view (it takes no write, or it is in JWT mode). It is drawn
// only into the pages of a request that acts, which holds the cookie.
func (s *Server) csrfValue() string {
	if s.token == nil {
		return ""
	}
	return s.token.csrf
}

// admit judges the request by the view's token. It is the request, marked
// when its cookie is the view's, or false once it has answered: the token
// URL with the cookie and a redirect, a write without both proofs with 403.
// Reads go through with or without the cookie, as before; what the cookie
// changes is whom they act as (acting).
func (s *Server) admit(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	t, name := s.token, cookieName(r.Host)
	// Only a GET trades the token: a POST that carries one is no more than
	// a write without the cookie.
	if r.Method == http.MethodGet {
		if given := r.URL.Query().Get("token"); given != "" && sameSecret(given, t.secret) {
			http.SetCookie(w, &http.Cookie{Name: name, Value: t.cookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, withoutToken(r.URL), http.StatusSeeOther)
			return nil, false
		}
	}
	held := false
	if c, err := r.Cookie(name); err == nil && sameSecret(c.Value, t.cookie) {
		held = true
		r = r.WithContext(context.WithValue(r.Context(), tokenHeldKey{}, true))
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return r, true
	}
	// Everything else is a write, or asks for one: a route that takes more
	// later is guarded without being named here.
	if !held {
		http.Error(w, "yardr web takes a write only from a browser that holds its cookie, and this request carries none that is this view's. "+
			"Open the view's token URL in this browser (yardr web open does, on the machine the view runs on), and send it again from the page",
			http.StatusForbidden)
		return nil, false
	}
	given := r.Header.Get("X-CSRF-Token")
	if given == "" {
		given = r.PostFormValue("csrf")
	}
	if !sameSecret(given, t.csrf) {
		http.Error(w, "yardr web takes a write only with the CSRF token of the page it is made from, and this request carries none that is this view's. "+
			"Load the page again and send it from there",
			http.StatusForbidden)
		return nil, false
	}
	return r, true
}

// withoutToken is the URL of u without its token, as a path and a query for
// the redirect: the same page, whose address no longer holds the secret. The
// path starts with one slash and no more: a Location of //other.host/ is a
// URL on that host, and http.Redirect leaves it as it is, so a request for
// //other.host/ is sent to /other.host/, as the mux would send it, and not
// off this view.
func withoutToken(u *url.URL) string {
	q := u.Query()
	q.Del("token")
	to := "/" + strings.TrimLeft(u.EscapedPath(), "/")
	if len(q) > 0 {
		to += "?" + q.Encode()
	}
	return to
}
