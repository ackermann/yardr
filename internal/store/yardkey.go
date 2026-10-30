package store

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"yardr.dev/yardr/internal/config"
)

// The yard's key as the store keeps and checks it: the key file, what a
// yard calls itself to another, invites, and the answers it verifies.
// Moved from internal/yardkey (yardr-rxk9), where making the key and the
// signing of requests, links and frames stay, over these.

var enc = base64.RawURLEncoding

// ErrSignature is every reason a request or an answer is not the peer's: a
// key that does not parse, a signature that does not verify, a timestamp
// too far off. One error, so a receiver has one thing to answer.
var ErrSignature = errors.New("not signed by the peer")

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
		return nil, fmt.Errorf("%q is no yard's public key", clipErr(s))
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
		return Invite{}, fmt.Errorf("%q is no invite: want the line yardr peer invite prints (%s,<name>,<key>[,<url>])", clipErr(s), inviteTag)
	}
	inv := Invite{Name: parts[1], Key: parts[2]}
	if !inviteName.MatchString(inv.Name) {
		return Invite{}, fmt.Errorf("invite: %q is no yard's name", clipErr(inv.Name))
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
		return "", fmt.Errorf("url %q: want http://host:port or https://host, where the yard's server takes webhooks", clipErr(s))
	}
	return strings.TrimRight(u.String(), "/"), nil
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

// SignResponse signs the answer to the request signed requestSig: its
// status and the body's sha256. It returns the value of the signature
// header (yardkey.HeaderSig).
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

// clipErr keeps text that is not what was expected short in an error.
func clipErr(s string) string {
	if len(s) > 48 {
		return s[:48] + "..."
	}
	return s
}
