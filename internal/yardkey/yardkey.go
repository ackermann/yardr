// Package yardkey is a yard's key and what is done with it: the ed25519 key
// pair two yards are linked by (docs/peers.md), the invite one operator
// pastes to the other, and the signatures on what the yards then send each
// other. It knows nothing of the store or of HTTP servers, so the store can
// check a signature itself.
//
// A signature proves who sent a request or an answer and that it is whole.
// It hides nothing: over plain http the body is readable by whoever carries
// it.
package yardkey

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
)

// The headers of a request between yards: the sender's public key, by
// which the receiver finds the link, when it was signed (Unix seconds), and
// the signature. An answer carries HeaderSig alone.
const (
	HeaderKey  = "Yardr-Peer-Key"
	HeaderTime = "Yardr-Peer-Time"
	HeaderSig  = "Yardr-Peer-Signature"
)

// MaxSkew is how far a request's timestamp may be from the receiver's
// clock, either way, which is also how long a recorded request can be
// played again.
const MaxSkew = 5 * time.Minute

// ErrSignature is every reason a request or an answer is not the peer's: a
// key that does not parse, a signature that does not verify, a timestamp
// too far off. One error, so a receiver has one thing to answer.
var ErrSignature = errors.New("not signed by the peer")

var enc = base64.RawURLEncoding

// Ensure returns the yard's private key from the file at path, making the
// pair first when there is no file: once, so the key a yard gave out stays
// its key. The file holds the seed, base64, and is read under the rule of
// every file yardr reads a secret from (0600 or stricter, see
// config.ReadSecretFile).
func Ensure(path string) (key ed25519.PrivateKey, made bool, err error) {
	key, err = Load(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, false, err
	}
	_, key, err = ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, false, err
	}
	// O_EXCL: of two commands making the key at once, one makes it and the
	// other reads that one's.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, fs.ErrExist) {
		key, err = Load(path)
		return key, false, err
	} else if err != nil {
		return nil, false, err
	}
	_, werr := f.WriteString(base64.StdEncoding.EncodeToString(key.Seed()) + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		os.Remove(path)
		return nil, false, werr
	}
	return key, true, nil
}

// Load reads the yard's private key; fs.ErrNotExist when the yard has made
// none. Errors name the file, never what it holds.
func Load(path string) (ed25519.PrivateKey, error) {
	b, p, err := config.ReadSecretFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(string(b))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("key file %s does not hold a yard's key", p)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// Public is the public half of key as yards write it.
func Public(key ed25519.PrivateKey) string {
	return enc.EncodeToString(key.Public().(ed25519.PublicKey))
}

// ParsePublic reads a public key as Public writes it.
func ParsePublic(s string) (ed25519.PublicKey, error) {
	b, err := enc.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("%q is no yard's public key", clip(s))
	}
	return ed25519.PublicKey(b), nil
}

// inviteTag starts an invite, so a line that is something else is told
// apart, and a later form from this one.
const inviteTag = "yardr-peer-1"

var inviteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`)

// Invite is what one yard's operator gives another's to be added as a peer:
// what the yard calls itself, its public key, and where it can be reached.
// A yard without a URL cannot be called: it calls.
type Invite struct {
	Name string `json:"name"`
	Key  string `json:"key"`
	URL  string `json:"url,omitempty"`
}

// String is the invite as one line to paste: no spaces, nothing a shell
// reads.
func (i Invite) String() string {
	parts := []string{inviteTag, i.Name, i.Key}
	if i.URL != "" {
		parts = append(parts, i.URL)
	}
	return strings.Join(parts, ",")
}

// ParseInvite reads an invite as String writes it.
func ParseInvite(s string) (Invite, error) {
	parts := strings.SplitN(strings.TrimSpace(s), ",", 4)
	if len(parts) < 3 || parts[0] != inviteTag {
		return Invite{}, fmt.Errorf("%q is no invite: want the line yardr peer invite prints (%s,<name>,<key>[,<url>])", clip(s), inviteTag)
	}
	inv := Invite{Name: parts[1], Key: parts[2]}
	if !inviteName.MatchString(inv.Name) {
		return Invite{}, fmt.Errorf("invite: %q is no yard's name", clip(inv.Name))
	}
	if _, err := ParsePublic(inv.Key); err != nil {
		return Invite{}, fmt.Errorf("invite: %w", err)
	}
	if len(parts) == 4 {
		u, err := CleanURL(parts[3])
		if err != nil {
			return Invite{}, fmt.Errorf("invite: %w", err)
		}
		inv.URL = u
	}
	return inv, nil
}

// CleanURL is where a yard can be reached, as it is kept: http or https, a
// host, and nothing after the path, without a trailing slash. The requests
// go to <url>/peers/messages.
func CleanURL(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.ContainsAny(s, ", \t\r\n") {
		return "", fmt.Errorf("url %q: want http://host:port or https://host, where the yard's server takes webhooks", clip(s))
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// request is what a request's signature covers. to is the receiver's public
// key: a request signed for one peer is then no request to another the
// sender is linked with.
func request(to, method, path, ts string, body []byte) []byte {
	return []byte(strings.Join([]string{"yardr-peer-request-1", to, method, path, ts, bodySum(body)}, "\n"))
}

// response is what an answer's signature covers. The request's signature
// ties it to the one request it answers, so an answer cannot be kept and
// given to a later request.
func response(status int, requestSig string, body []byte) []byte {
	return []byte(strings.Join([]string{"yardr-peer-response-1", strconv.Itoa(status), requestSig, bodySum(body)}, "\n"))
}

func bodySum(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// SignRequest signs a request to the yard whose public key is to: its
// method, its path with the query, the time and the body's sha256. It
// returns the values of HeaderTime and HeaderSig.
func SignRequest(key ed25519.PrivateKey, to, method, path string, at time.Time, body []byte) (ts, sig string) {
	ts = strconv.FormatInt(at.Unix(), 10)
	return ts, enc.EncodeToString(ed25519.Sign(key, request(to, method, path, ts, body)))
}

// VerifyRequest checks that the yard whose public key is from signed this
// request to the yard whose key is to, no more than MaxSkew from now. Every
// failure is ErrSignature, with the reason in its text for the receiver's
// own log.
func VerifyRequest(from, to, method, path, ts, sig string, body []byte, now time.Time) error {
	pub, err := ParsePublic(from)
	if err != nil {
		return fmt.Errorf("%w: bad key", ErrSignature)
	}
	raw, err := enc.DecodeString(sig)
	if err != nil || !ed25519.Verify(pub, request(to, method, path, ts, body), raw) {
		return fmt.Errorf("%w: bad signature", ErrSignature)
	}
	// After the signature: the timestamp is then the sender's own.
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: bad timestamp", ErrSignature)
	}
	if d := now.Sub(time.Unix(sec, 0)); d > MaxSkew || d < -MaxSkew {
		return fmt.Errorf("%w: timestamp %s off", ErrSignature, d.Round(time.Second))
	}
	return nil
}

// SignResponse signs the answer to the request signed requestSig: its
// status and the body's sha256. It returns the value of HeaderSig.
func SignResponse(key ed25519.PrivateKey, status int, requestSig string, body []byte) string {
	return enc.EncodeToString(ed25519.Sign(key, response(status, requestSig, body)))
}

// VerifyResponse checks that the yard whose public key is from gave this
// answer to the request signed requestSig.
func VerifyResponse(from string, status int, requestSig, sig string, body []byte) error {
	pub, err := ParsePublic(from)
	if err != nil {
		return fmt.Errorf("%w: bad key", ErrSignature)
	}
	raw, err := enc.DecodeString(sig)
	if err != nil || requestSig == "" || !ed25519.Verify(pub, response(status, requestSig, body), raw) {
		return fmt.Errorf("%w: bad signature on the answer", ErrSignature)
	}
	return nil
}

// clip keeps text that is not what was expected short in an error.
func clip(s string) string {
	if len(s) > 48 {
		return s[:48] + "..."
	}
	return s
}
