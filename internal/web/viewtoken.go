package web

// A view that acts as one fixed user (--as) has no gateway to say who is at
// the browser. Loopback is open to every process on the machine and to every
// page the visitor's browser opens, and the Origin header ownOrigin compares
// is a client's own word, so a process that sets it passes. Outside JWT mode
// such a view therefore asks for a secret of its own, one per user it acts
// as, that outlives the view until the operator revokes it:
//
//   - the token: 256 random bits that the first serve writes to a file only
//     the operator reads, which later serves read again, and that a serve
//     prints once, in a URL (/?token=...), to a terminal: a log is read by
//     more than the operator;
//   - the cookie the token buys: a GET carrying it is answered with an
//     HttpOnly, SameSite=Strict cookie and a redirect to the same URL without
//     it, so the token does not stay in the address bar or the history. It
//     lasts web_cookie_days from then, which the view checks itself, and no
//     request makes it last longer;
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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// viewToken is the secret of one view that acts as a user, kept in a file
// of the home's (config.WebToken), and how long a cookie it bought lasts.
type viewToken struct {
	// file holds the token itself, in hex. It is in the file, the view's
	// record, and the URL the command prints to a terminal, and in no
	// response. The view reads it again on every request, so a token that
	// yardr web revoke replaced stops working on a view that runs, with no
	// restart; and one it cannot read admits no browser.
	file string
	// maxAge is how long a cookie lasts from when the token bought it
	// (web_cookie_days), and now the clock it is measured by.
	maxAge time.Duration
	now    func() time.Time
}

// tokenKeys are the values a browser holds once it has shown the token:
// keyed hashes of the secret, each for one purpose, so that neither gives
// away the other or the token, which a URL in a history or a scrollback is
// likelier to leak than a cookie is.
type tokenKeys struct {
	secret, csrf string
}

// keys reads the token from its file. An error is a file that is gone or
// holds no token, and the caller admits no one on it.
func (t *viewToken) keys() (tokenKeys, error) {
	secret, err := ReadViewToken(t.file)
	if err != nil {
		return tokenKeys{}, err
	}
	return tokenKeys{secret: secret, csrf: derive(secret, "csrf")}, nil
}

// cookie is the cookie's value for a browser that showed the token at
// issued: the time, and a keyed hash of the secret and that time. The time
// is in the value, and in the hash, so that the view itself refuses a cookie
// past its age, whatever the browser that holds it keeps, and nobody makes
// an old one young without the secret.
func (k tokenKeys) cookie(issued time.Time) string {
	at := strconv.FormatInt(issued.Unix(), 10)
	return at + "." + derive(k.secret, "cookie "+at)
}

// held reports whether value is a cookie this token bought no longer than
// maxAge before now. One from the future is no cookie of this view's
// either: the clock it was issued by is this one.
func (k tokenKeys) held(value string, now time.Time, maxAge time.Duration) bool {
	at, _, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	unix, err := strconv.ParseInt(at, 10, 64)
	if err != nil {
		return false
	}
	issued := time.Unix(unix, 0)
	if !sameSecret(value, k.cookie(issued)) {
		return false
	}
	return !issued.After(now.Add(time.Minute)) && now.Sub(issued) <= maxAge
}

func derive(secret, purpose string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("yardr web " + purpose))
	return hex.EncodeToString(m.Sum(nil))
}

// ReadViewToken is the token in file: 256 bits in hex, and nothing else.
// It makes none: a reader of a view's record takes the token the view takes.
// A file that others than its owner may read is refused, as peer.key is:
// whoever else read it may hold the token.
func ReadViewToken(file string) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return "", fmt.Errorf("%s has mode %v, and a web view's token is its owner's alone: yardr web revoke writes a new one of mode 0600", file, perm)
	}
	b, err := io.ReadAll(io.LimitReader(f, 128))
	if err != nil {
		return "", err
	}
	secret := strings.TrimSpace(string(b))
	if len(secret) != 64 {
		return "", fmt.Errorf("%s holds no web view token: yardr web revoke writes a new one", file)
	}
	if _, err := hex.DecodeString(secret); err != nil {
		return "", fmt.Errorf("%s holds no web view token: yardr web revoke writes a new one", file)
	}
	return secret, nil
}

// LoadViewToken is the token in file, made there first when there is none:
// the first serve of a view that acts as its user makes it, and every later
// one takes it, so a browser stays logged in across the view's restarts. Of
// two serves that start at once, one makes it and the other takes that one.
func LoadViewToken(file string) (string, error) {
	if secret, err := ReadViewToken(file); !errors.Is(err, fs.ErrNotExist) {
		return secret, err
	}
	return writeViewToken(file, false)
}

// RevokeViewToken writes a new token to file in place of the one there, if
// any, and returns it: every cookie and token URL of the old one stops
// working, on a view that runs too, which reads the file on every request.
func RevokeViewToken(file string) (string, error) { return writeViewToken(file, true) }

// writeViewToken writes a fresh token to file, in a directory only its owner
// reads, through a file of mode 0600 that is complete before it has the
// name. With replace false a file that is there already stays, and its
// token is the one returned.
func writeViewToken(file string, replace bool) (string, error) {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	// MkdirAll leaves a directory that was there as it was: this one holds
	// what logs a browser in, so it is the owner's alone whoever made it.
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	secret := hex.EncodeToString(b[:])
	f, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_, err = f.WriteString(secret + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if replace {
		return secret, os.Rename(f.Name(), file)
	}
	// A link fails where the name is taken, where a rename would replace
	// it: the serve that lost the race takes the winner's token.
	if err := os.Link(f.Name(), file); errors.Is(err, fs.ErrExist) {
		return ReadViewToken(file)
	} else if err != nil {
		return "", err
	}
	return secret, nil
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
// guards the view (it takes no write, or it is in JWT mode) or the token's
// file cannot be read, when no write is taken either. It is drawn only into
// the pages of a request that acts, which holds the cookie.
func (s *Server) csrfValue() string {
	if s.token == nil {
		return ""
	}
	k, err := s.token.keys()
	if err != nil {
		return ""
	}
	return k.csrf
}

// admit judges the request by the view's token. It is the request, marked
// when its cookie is the view's, or false once it has answered: the token
// URL with the cookie and a redirect, a write without both proofs with 403.
// Reads go through with or without the cookie, as before; what the cookie
// changes is whom they act as (acting).
func (s *Server) admit(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	t, name := s.token, cookieName(r.Host)
	// The token as its file holds it now: a revoke since the last request
	// is seen on this one. A file that cannot be read admits no browser,
	// and the request goes on as one that holds no cookie.
	k, kerr := t.keys()
	if kerr != nil {
		s.log.Warn("the web view admits no browser: its token cannot be read", "err", kerr)
	}
	now := t.now()
	// Only a GET trades the token: a POST that carries one is no more than
	// a write without the cookie.
	if r.Method == http.MethodGet && kerr == nil {
		if given := r.URL.Query().Get("token"); given != "" && sameSecret(given, k.secret) {
			// The cookie lasts maxAge from now, and only a token URL sets
			// one: a request that carries it does not make it last longer.
			http.SetCookie(w, &http.Cookie{Name: name, Value: k.cookie(now), Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode,
				MaxAge: int(t.maxAge / time.Second), Expires: now.Add(t.maxAge)})
			w.Header().Set("Cache-Control", "no-store")
			http.Redirect(w, r, withoutToken(r.URL), http.StatusSeeOther)
			return nil, false
		}
	}
	held := false
	if c, err := r.Cookie(name); err == nil && kerr == nil && k.held(c.Value, now, t.maxAge) {
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
	if !sameSecret(given, k.csrf) {
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
