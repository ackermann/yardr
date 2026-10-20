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
	"sort"
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

// ValidName reports whether s is a name a yard can give itself to another:
// what an invite carries, and a pairing.
func ValidName(s string) bool { return inviteName.MatchString(s) }

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

// The link (design yardr-hosted-050n, 3.2) is the WebSocket a yard that
// connects to an edge keeps open to it. The signed upgrade that opens it can
// be played again for MaxSkew, so the socket is the yard's link only once
// each end has signed both nonces, the yard's own from its upgrade and the
// edge's from its challenge, under a label of the link's own: such a
// signature is no request's and no answer's, and theirs are not it. The two
// nonces together are the link's session, and every request the edge sends
// down it is signed over the session, so a frame recorded from one link is
// refused on the next.
//
// The edge defines these bytes (yardr-hosted, router/internal/yardkey) and
// this is their copy: the tests pin the vectors the router's tests pin, so
// a change on either side that the other has not made is seen.
const (
	linkLabel  = "yardr edge link v1"
	frameLabel = "yardr edge request v1"
)

// NonceSize is the length of each end's nonce on the link.
const NonceSize = 32

// link is what both ends of a link sign: the two nonces, the yard's first.
func link(yardNonce, edgeNonce []byte) []byte {
	return []byte(strings.Join([]string{linkLabel, enc.EncodeToString(yardNonce), enc.EncodeToString(edgeNonce)}, "\n"))
}

// SignLink signs the two nonces of one link: the edge's challenge, and the
// yard's hello that answers it.
func SignLink(key ed25519.PrivateKey, yardNonce, edgeNonce []byte) string {
	return enc.EncodeToString(ed25519.Sign(key, link(yardNonce, edgeNonce)))
}

// VerifyLink checks that the party whose public key is from signed these
// two nonces. A nonce of any other length is refused: each end draws
// NonceSize random bytes, and a short one would be a guessable session.
func VerifyLink(from string, yardNonce, edgeNonce []byte, sig string) error {
	pub, err := ParsePublic(from)
	if err != nil {
		return fmt.Errorf("%w: bad key", ErrSignature)
	}
	if len(yardNonce) != NonceSize || len(edgeNonce) != NonceSize {
		return fmt.Errorf("%w: a nonce is not %d bytes", ErrSignature, NonceSize)
	}
	raw, err := enc.DecodeString(sig)
	if err != nil || !ed25519.Verify(pub, link(yardNonce, edgeNonce), raw) {
		return fmt.Errorf("%w: bad signature on the link", ErrSignature)
	}
	return nil
}

// EncodeNonce writes a nonce as it travels: in the upgrade's header and in
// the challenge. DecodeNonce reads one, and refuses any other length.
func EncodeNonce(n []byte) string { return enc.EncodeToString(n) }

func DecodeNonce(s string) ([]byte, error) {
	b, err := enc.Strict().DecodeString(s)
	if err != nil || len(b) != NonceSize {
		return nil, fmt.Errorf("%q is not a link nonce", clip(s))
	}
	return b, nil
}

// Session is a link's session id: its two nonces, the yard's first.
func Session(yardNonce, edgeNonce []byte) string {
	return enc.EncodeToString(yardNonce) + "." + enc.EncodeToString(edgeNonce)
}

// Frame is what the edge's signature on a request frame covers: the link's
// session and the frame's place in it, the request as the sender made it
// (its headers by their digest, its body by its sha256), when the edge
// received it, and whether it waited in the edge's queue, which chooses the
// clock the yard checks the sender's timestamp against.
type Frame struct {
	Session  string
	Seq      uint64
	ID       string
	Method   string
	Path     string
	Header   map[string][]string
	Body     []byte
	Received string
	Queued   bool
}

// frame is the bytes a request frame's signature covers. None of the
// fields it joins holds a newline (VerifyFrame refuses one that does), so
// no two frames sign as the same text.
func frame(f Frame) []byte {
	return []byte(strings.Join([]string{
		frameLabel, f.Session, strconv.FormatUint(f.Seq, 10), f.ID, f.Method, f.Path,
		HeaderDigest(f.Header), bodySum(f.Body), f.Received, strconv.FormatBool(f.Queued),
	}, "\n"))
}

// SignFrame signs a request frame. The edge signs every one it sends; a
// yard signs none, and a stand-in edge does.
func SignFrame(key ed25519.PrivateKey, f Frame) string {
	return enc.EncodeToString(ed25519.Sign(key, frame(f)))
}

// VerifyFrame checks that the party whose public key is from signed this
// request frame.
func VerifyFrame(from string, f Frame, sig string) error {
	pub, err := ParsePublic(from)
	if err != nil {
		return fmt.Errorf("%w: bad key", ErrSignature)
	}
	for _, v := range []string{f.Session, f.ID, f.Method, f.Path, f.Received} {
		if strings.ContainsAny(v, "\r\n") {
			return fmt.Errorf("%w: a field holds a line break", ErrSignature)
		}
	}
	// HTTP allows neither in a header, and either would let two header
	// maps digest alike.
	seenHeaders := make(map[string]string, len(f.Header))
	for name, values := range f.Header {
		if strings.ContainsAny(name, ":\r\n") {
			return fmt.Errorf("%w: a header name holds a colon or a line break", ErrSignature)
		}
		lower := strings.ToLower(name)
		if prior, ok := seenHeaders[lower]; ok && prior != name {
			return fmt.Errorf("%w: header names %q and %q differ only in case", ErrSignature, prior, name)
		}
		seenHeaders[lower] = name
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n") {
				return fmt.Errorf("%w: a header value holds a line break", ErrSignature)
			}
		}
	}
	raw, err := enc.DecodeString(sig)
	if err != nil || !ed25519.Verify(pub, frame(f), raw) {
		return fmt.Errorf("%w: bad signature on the frame", ErrSignature)
	}
	return nil
}

// HeaderDigest is the sha256, hex, of a header map's canonical form: names
// lowercased and sorted, one line name:value for each value, a name's
// values in the order they arrived, the lines joined by newlines. It is
// what makes the headers a frame carries as trustworthy as its body, since
// they hold what the yard verifies a delivery with. A map whose keys differ
// only in case (Go's own never does) lists them by the key as written, so
// the digest is the same however the map is ranged.
func HeaderDigest(h map[string][]string) string {
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := strings.ToLower(names[i]), strings.ToLower(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	var lines []string
	for _, name := range names {
		lower := strings.ToLower(name)
		for _, v := range h[name] {
			lines = append(lines, lower+":"+v)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// clip keeps text that is not what was expected short in an error.
func clip(s string) string {
	if len(s) > 48 {
		return s[:48] + "..."
	}
	return s
}
