// Package pairing is what two yards say to each other to become peers from
// a short code one operator reads to the other (docs/peers.md, "Pairing"):
// the code, what is computed from it, and the check both operators compare.
// It knows nothing of the store or of HTTP: both sides, and a test that
// stands between them, compute the same things from here.
//
// The yard that shows the code is the host, the one that calls it the
// joiner. Three messages cross before anyone is asked anything:
//
//	joiner -> host   Hello:  a commitment to the joiner's nonce, name and key
//	host -> joiner   Offer:  the host's nonce, name, key and URL
//	joiner -> host   Reveal: the joiner's nonce, name and key
//
// Each carries an HMAC under a key stretched from the code (Key), so a yard
// that does not know the code gets no further than a counted wrong try.
//
// The code is short, and a short secret used as a key can be guessed by
// whoever sees one HMAC made with it and has the time. So the code is not
// what the link rests on. The check is: six digits from a hash of
// everything that crossed (Transcript.Check), which each operator sees and
// compares with the other's. Someone in the middle who puts a key of their
// own in either direction has to make two hashes agree in those digits, and
// has no say in them: the joiner is bound to its nonce by the commitment
// before it sees the host's, and the host has sent its nonce before the
// joiner's is revealed. One try in a million, and a pairing takes one try.
// That is the commit-then-reveal exchange of a short authentication string
// (Vaudenay 2005; ZRTP and Bluetooth's numeric comparison use it), made of
// SHA-256, HMAC and random nonces and nothing else.
//
// What then proves that each yard holds the private key it gave is not
// here: the confirmations that follow are signed as every request between
// yards is (internal/yardkey).
package pairing

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

// TTL is how long a code is good for, from when it is shown. ConfirmTTL is
// how long the two operators then have to compare the check, from the call.
const (
	TTL        = 10 * time.Minute
	ConfirmTTL = 5 * time.Minute
)

// MaxTries is how many wrong codes an open pairing takes before it ends.
const MaxTries = 5

// The paths of the three requests, under the peers' listener. Confirm is
// signed with the joiner's key, as a request between peers is.
const (
	PathHello   = "/peers/pair/hello"
	PathReveal  = "/peers/pair/reveal"
	PathConfirm = "/peers/pair/confirm"
)

// alphabet is what a code is written in: digits and capitals without I, L,
// O and U, so that none reads or sounds like another.
const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// codeLen characters of 5 bits each: 40 bits, which is what MaxTries is
// counted against.
const codeLen = 8

// Stretch is the PBKDF2 round count of Key. It makes one guess at a code
// cost what a guess at a password costs, so a code seen in transit is not
// guessed within the minutes it is good for. Both sides must use the same:
// it is a variable only so that tests, which make many keys, can lower it.
var Stretch = 600_000

var enc = base64.RawURLEncoding

// NewCode makes a code, as it is shown: XXXX-XXXX.
func NewCode() string {
	b := make([]byte, codeLen)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[b[i]&31]
	}
	return string(b[:codeLen/2]) + "-" + string(b[codeLen/2:])
}

// CleanCode is a code as it was typed, in the one form both sides compute
// from: no dash or space, capitals, and the letters a code never has read
// as the digits they look like.
func CleanCode(s string) (string, error) {
	s = strings.NewReplacer("-", "", " ", "", "I", "1", "L", "1", "O", "0").Replace(strings.ToUpper(strings.TrimSpace(s)))
	if len(s) != codeLen || strings.Trim(s, alphabet) != "" {
		return "", errors.New("that is no pairing code: want the eight characters yardr peer pair shows on the other yard, such as K7QM-2XPD")
	}
	return s, nil
}

// NewNonce is 32 random bytes, as they cross.
func NewNonce() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return enc.EncodeToString(b)
}

// Party is what one yard says of itself: what an invite carries, and its
// nonce. Only the host has a URL.
type Party struct {
	Nonce string `json:"nonce"`
	Name  string `json:"name"`
	Key   string `json:"key"`
	URL   string `json:"url,omitempty"`
}

// Valid refuses a party that is not one, before anything is computed from
// it: every field is then of a few plain characters and has no newline,
// which is what the hashes below separate their fields by.
func (p Party) Valid() error {
	if !isHash(p.Nonce) {
		return errors.New("pairing: the nonce is not 32 bytes")
	}
	if !yardkey.ValidName(p.Name) {
		return errors.New("pairing: that is no yard's name")
	}
	if _, err := yardkey.ParsePublic(p.Key); err != nil {
		return errors.New("pairing: that is no yard's public key")
	}
	if p.URL != "" {
		if u, err := yardkey.CleanURL(p.URL); err != nil || u != p.URL {
			return errors.New("pairing: that is no yard's URL")
		}
	}
	return nil
}

// isHash reports whether s is 32 bytes as they cross: a nonce, a
// commitment, an HMAC.
func isHash(s string) bool {
	b, err := enc.DecodeString(s)
	return err == nil && len(b) == sha256.Size
}

func sum(fields ...string) []byte {
	h := sha256.Sum256([]byte(strings.Join(fields, "\n")))
	return h[:]
}

func mac(key []byte, fields ...string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(strings.Join(fields, "\n")))
	return enc.EncodeToString(m.Sum(nil))
}

// Commit is the joiner's commitment to what it reveals later: a hash that
// binds it to its nonce, name and key and, the nonce being random, tells
// nothing of them.
func Commit(joiner Party) string {
	return enc.EncodeToString(sum("yardr-pair-commit-1", joiner.Nonce, joiner.Name, joiner.Key))
}

// Key stretches the code into the key of a pairing's HMACs, salted with the
// joiner's commitment: a key is then good for one call, and nothing worked
// out for one pairing helps with another.
func Key(code, commit string) []byte {
	k, err := pbkdf2.Key(sha256.New, code, []byte("yardr-pair-key-1\n"+commit), Stretch, sha256.Size)
	if err != nil {
		panic(err) // only for parameters no caller here gives
	}
	return k
}

// Hello is the joiner's first message.
type Hello struct {
	Commit string `json:"commit"`
	MAC    string `json:"mac"`
}

// NewHello is the first message of a joiner that knows the code.
func NewHello(key []byte, joiner Party) Hello {
	c := Commit(joiner)
	return Hello{Commit: c, MAC: mac(key, "yardr-pair-hello-1", c)}
}

// Valid refuses what is no first message, before a key is made for it.
func (h Hello) Valid() error {
	if !isHash(h.Commit) || !isHash(h.MAC) {
		return errors.New("pairing: that is no first message")
	}
	return nil
}

// Check reports that h is well formed (an error when not) and whether
// whoever made it knows the code.
func (h Hello) Check(key []byte) (knows bool, err error) {
	if err := h.Valid(); err != nil {
		return false, err
	}
	return hmac.Equal([]byte(h.MAC), []byte(mac(key, "yardr-pair-hello-1", h.Commit))), nil
}

// Offer is the host's answer: itself, and the proof that it knows the code
// too.
type Offer struct {
	Party
	MAC string `json:"mac"`
}

func offerMAC(key []byte, commit string, host Party) string {
	return mac(key, "yardr-pair-offer-1", commit, host.Nonce, host.Name, host.Key, host.URL)
}

// NewOffer is the host's answer to the hello that committed to commit.
func NewOffer(key []byte, commit string, host Party) Offer {
	return Offer{Party: host, MAC: offerMAC(key, commit, host)}
}

// Check is Hello.Check for the host's answer.
func (o Offer) Check(key []byte, commit string) (knows bool, err error) {
	if err := o.Party.Valid(); err != nil {
		return false, err
	}
	if o.URL == "" || !isHash(o.MAC) {
		return false, errors.New("pairing: that is no answer to a first message")
	}
	return hmac.Equal([]byte(o.MAC), []byte(offerMAC(key, commit, o.Party))), nil
}

// Transcript is everything that crossed, which both sides have once the
// joiner has revealed itself. The check and the session are computed from
// all of it, so two yards that did not see the same have not the same.
type Transcript struct {
	Commit string `json:"commit"`
	Joiner Party  `json:"joiner"`
	Host   Party  `json:"host"`
}

func (t Transcript) fields(tag string) []string {
	return []string{tag, t.Commit,
		t.Joiner.Nonce, t.Joiner.Name, t.Joiner.Key,
		t.Host.Nonce, t.Host.Name, t.Host.Key, t.Host.URL}
}

// Reveal is the joiner's second message: what it committed to.
type Reveal struct {
	Commit string `json:"commit"`
	Party
	MAC string `json:"mac"`
}

// NewReveal opens the joiner's commitment.
func NewReveal(key []byte, t Transcript) Reveal {
	return Reveal{Commit: t.Commit, Party: t.Joiner, MAC: mac(key, t.fields("yardr-pair-reveal-1")...)}
}

// Check reports that r is well formed and opens the commitment the host
// answered (an error when not), and whether whoever made it knows the code.
// It returns the transcript, now whole.
func (r Reveal) Check(key []byte, host Party) (t Transcript, knows bool, err error) {
	if err := r.Party.Valid(); err != nil {
		return t, false, err
	}
	if r.URL != "" || !isHash(r.MAC) || !isHash(r.Commit) {
		return t, false, errors.New("pairing: that is no second message")
	}
	t = Transcript{Commit: r.Commit, Joiner: r.Party, Host: host}
	if !hmac.Equal([]byte(r.MAC), []byte(mac(key, t.fields("yardr-pair-reveal-1")...))) {
		return t, false, nil
	}
	if Commit(r.Party) != r.Commit {
		return t, true, errors.New("pairing: what was revealed is not what was committed to")
	}
	return t, true, nil
}

// Session names a pairing between the two yards once the exchange is done:
// the confirmations that follow say which pairing they confirm by it. It
// holds nothing of the code.
func (t Transcript) Session() string {
	return hex.EncodeToString(sum(t.fields("yardr-pair-session-1")...))
}

// Check is the six digits both operators compare, as they are shown:
// "482 913". Both yards compute it from the transcript, keys and nonces
// included, so it is the same on both only when both saw the same.
func (t Transcript) Check() string {
	n := binary.BigEndian.Uint64(sum(t.fields("yardr-pair-check-1")...)) % 1_000_000
	return fmt.Sprintf("%03d %03d", n/1000, n%1000)
}

// Answer is the joiner's word on the check, which crosses signed with its
// key: its operator confirmed it, or declined. The joiner sends it again to
// ask what the host's operator said.
type Answer struct {
	Session string `json:"session"`
	Confirm bool   `json:"confirm"`
}

// The states of an Outcome.
const (
	Waiting   = "waiting"   // the host's operator has not answered yet
	Confirmed = "confirmed" // both confirmed, and the host has saved the joiner
	Ended     = "ended"     // nothing was saved; Reason says why
)

// Outcome is the host's signed answer to an Answer.
type Outcome struct {
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// The reasons a pairing ends without a link, as events and answers name
// them.
const (
	WrongCode    = "wrong_code"    // MaxTries wrong codes; also the answer to one
	Declined     = "declined"      // this yard's operator said no to the check
	PeerDeclined = "peer_declined" // the other yard's did
	TimedOut     = "timed_out"     // the code, or the time to confirm, ran out
	Closed       = "closed"        // the host's operator ended it
	AlreadyPeer  = "already_peer"  // the name or the key is a peer's already
	BadName      = "bad_name"      // the other yard's name is none a peer can have here
	Own          = "own_key"       // the two yards are one
	NoPairing    = "no_pairing"    // an answer only: no pairing is open
	Used         = "used"          // an answer only: the code was used by another call
	Superseded   = "superseded"    // an answer only: a later call took this one's place
)

// Refused is the host's word that a call is not taken, with one of the
// reasons above.
type Refused struct{ Reason string }

func (r *Refused) Error() string { return "pairing refused: " + r.Reason }
