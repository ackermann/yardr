package pairing

import (
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/yardkey"
)

func party(t *testing.T, name, url string) Party {
	t.Helper()
	k, _, err := yardkey.Ensure(t.TempDir() + "/peer.key")
	if err != nil {
		t.Fatal(err)
	}
	return Party{Nonce: NewNonce(), Name: name, Key: yardkey.Public(k), URL: url}
}

// A code is eight characters of an alphabet without look-alikes, and is
// read back however it was typed: lowercase, without the dash, with the
// letters it never has for the digits they look like.
func TestCode(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		c := NewCode()
		if len(c) != 9 || c[4] != '-' || strings.Trim(c, alphabet+"-") != "" {
			t.Fatalf("code %q", c)
		}
		seen[c] = true
		clean, err := CleanCode(c)
		if err != nil || clean != strings.ReplaceAll(c, "-", "") {
			t.Fatalf("CleanCode(%q) = %q, %v", c, clean, err)
		}
	}
	if len(seen) < 200 {
		t.Fatalf("200 codes, %d different", len(seen))
	}
	for typed, want := range map[string]string{
		"k7qm-2xpd":   "K7QM2XPD",
		" K7QM 2XPD ": "K7QM2XPD",
		"OIL0-1234":   "01101234",
	} {
		if got, err := CleanCode(typed); err != nil || got != want {
			t.Errorf("CleanCode(%q) = %q, %v; want %q", typed, got, err, want)
		}
	}
	for _, bad := range []string{"", "K7QM", "K7QM-2XPD-9", "K7QM-2XPU", "K7QM-2XP!"} {
		if got, err := CleanCode(bad); err == nil {
			t.Errorf("CleanCode(%q) = %q, want an error", bad, got)
		}
	}
}

// Two yards that know the code and see the same exchange compute the same
// check and session; a yard with another code is told apart at each
// message; and what is revealed must be what was committed to.
func TestExchange(t *testing.T) {
	defer func(n int) { Stretch = n }(Stretch)
	Stretch = 10
	joiner, host := party(t, "laptop", ""), party(t, "north", "http://10.0.0.5:8790")
	code, _ := CleanCode(NewCode())

	jk := Key(code, Commit(joiner))
	hello := NewHello(jk, joiner)
	hk := Key(code, hello.Commit)
	if knows, err := hello.Check(hk); err != nil || !knows {
		t.Fatalf("hello: %v, %v", knows, err)
	}
	offer := NewOffer(hk, hello.Commit, host)
	if knows, err := offer.Check(jk, hello.Commit); err != nil || !knows {
		t.Fatalf("offer: %v, %v", knows, err)
	}
	mine := Transcript{Commit: hello.Commit, Joiner: joiner, Host: offer.Party}
	theirs, knows, err := NewReveal(jk, mine).Check(hk, host)
	if err != nil || !knows {
		t.Fatalf("reveal: %v, %v", knows, err)
	}
	if mine.Check() != theirs.Check() || mine.Session() != theirs.Session() {
		t.Fatalf("checks %q and %q, sessions %q and %q", mine.Check(), theirs.Check(), mine.Session(), theirs.Session())
	}
	if c := mine.Check(); len(c) != 7 || c[3] != ' ' || strings.Trim(c, "0123456789 ") != "" {
		t.Fatalf("check %q", c)
	}
	if strings.Contains(mine.Session(), code) {
		t.Fatal("the session holds the code")
	}

	// Another code: each message is told apart, and none is an error.
	other := Key("00000000", hello.Commit)
	if knows, err := hello.Check(other); err != nil || knows {
		t.Errorf("hello under another code: %v, %v", knows, err)
	}
	if knows, err := offer.Check(other, hello.Commit); err != nil || knows {
		t.Errorf("offer under another code: %v, %v", knows, err)
	}
	if _, knows, err := NewReveal(jk, mine).Check(other, host); err != nil || knows {
		t.Errorf("reveal under another code: %v, %v", knows, err)
	}
	// The key is the commitment's too: the same code under another
	// commitment makes another key.
	if string(Key(code, Commit(party(t, "laptop", "")))) == string(jk) {
		t.Error("one key for two commitments")
	}

	// A joiner that reveals another key than it committed to.
	swapped := mine
	swapped.Joiner.Key = party(t, "x", "").Key
	if _, knows, err := NewReveal(jk, swapped).Check(hk, host); err == nil || !knows {
		t.Errorf("a reveal of another key = %v, %v; want an error from one who knows the code", knows, err)
	}
	// Any one field changed changes the check and the session.
	for name, change := range map[string]func(*Transcript){
		"host key":     func(x *Transcript) { x.Host.Key = swapped.Joiner.Key },
		"joiner key":   func(x *Transcript) { x.Joiner.Key = swapped.Joiner.Key },
		"host nonce":   func(x *Transcript) { x.Host.Nonce = NewNonce() },
		"joiner nonce": func(x *Transcript) { x.Joiner.Nonce = NewNonce() },
		"host name":    func(x *Transcript) { x.Host.Name = "south" },
		"host url":     func(x *Transcript) { x.Host.URL = "http://10.0.0.6:8790" },
	} {
		x := mine
		change(&x)
		if x.Session() == mine.Session() {
			t.Errorf("%s changed, the session did not", name)
		}
	}

	// What is no message is an error, whoever sent it.
	for name, err := range map[string]error{
		"hello without a commitment": second(Hello{MAC: hello.MAC}.Check(hk)),
		"offer without a URL":        second(NewOffer(hk, hello.Commit, Party{Nonce: host.Nonce, Name: host.Name, Key: host.Key}).Check(jk, hello.Commit)),
		"offer with a bad name":      second(NewOffer(hk, hello.Commit, Party{Nonce: host.Nonce, Name: "a\nb", Key: host.Key, URL: host.URL}).Check(jk, hello.Commit)),
		"offer with a short nonce":   second(NewOffer(hk, hello.Commit, Party{Nonce: "AAAA", Name: host.Name, Key: host.Key, URL: host.URL}).Check(jk, hello.Commit)),
	} {
		if err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func second(_ bool, err error) error { return err }

// The stretch is what a guess at a code costs: at its real count a key
// takes long enough that the 2^40 codes are not tried in ten minutes.
func TestKeyIsStretched(t *testing.T) {
	if Stretch < 600_000 {
		t.Fatalf("Stretch = %d", Stretch)
	}
	start := time.Now()
	Key("K7QM2XPD", NewNonce())
	if d := time.Since(start); d < 5*time.Millisecond {
		t.Fatalf("a key took %s", d)
	}
}
