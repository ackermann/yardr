package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"testing"
)

// newKey makes a yard's key. Moved with the invite and answer tests from
// internal/yardkey (yardr-rxk9); in memory, as this package's own tests
// cannot call Ensure, which would cycle the test binary through yardkey's
// import of this package.
func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// newKeyFile writes a yard's key to path, as Ensure would, and returns it:
// for the tests that set the yard's key file. The format is Ensure's
// (internal/yardkey), pinned by its test.
func newKeyFile(t *testing.T, path string) ed25519.PrivateKey {
	t.Helper()
	_, k, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(k.Seed())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return k
}

// An invite is one line without spaces that reads back as it was written,
// with a URL or without.
func TestInvite(t *testing.T) {
	key := Public(newKey(t))
	for _, inv := range []Invite{
		{Name: "north", Key: key, URL: "http://10.0.0.5:8790"},
		{Name: "north", Key: key, URL: "https://yard.example.com/y"},
		{Name: "mac", Key: key, URL: "https://my.yardr.dev/peers/mckean/mac"},
		{Name: "laptop.2", Key: key},
	} {
		line := inv.String()
		if strings.ContainsAny(line, " \t\n'\"$") {
			t.Errorf("invite %q is not one plain word", line)
		}
		got, err := ParseInvite(" " + line + "\n")
		if err != nil || got != inv {
			t.Errorf("ParseInvite(%q) = %+v, %v", line, got, err)
		}
	}
	if got, err := ParseInvite("yardr-peer-1,n," + key + ",http://h:1/"); err != nil || got.URL != "http://h:1" {
		t.Errorf("a trailing slash: %+v, %v", got, err)
	}
	for _, bad := range []string{
		"", "north", key, "yardr-peer-1,north", "yardr-peer-2,north," + key,
		"yardr-peer-1,north,notakey", "yardr-peer-1,no rth," + key, "yardr-peer-1,," + key,
		"yardr-peer-1,north," + key + ",ftp://h", "yardr-peer-1,north," + key + ",http://",
		"yardr-peer-1,north," + key + ",http://h:1?x=1", "yardr-peer-1,north," + key + ",http://u:p@h:1",
		"yardr-peer-1,north," + key + ",http://h:1,more",
	} {
		if inv, err := ParseInvite(bad); err == nil {
			t.Errorf("ParseInvite(%q) = %+v, want an error", bad, inv)
		}
	}
}

// An answer verifies for the key that signed it and the one request it
// answers.
func TestResponseSignature(t *testing.T) {
	a, b := newKey(t), newKey(t)
	body := []byte(`{"taken":true}`)
	sig := SignResponse(b, 200, "request-sig", body)
	if err := VerifyResponse(Public(b), 200, "request-sig", sig, body); err != nil {
		t.Fatalf("the answer as signed: %v", err)
	}
	for name, err := range map[string]error{
		"a wrong key":     VerifyResponse(Public(a), 200, "request-sig", sig, body),
		"another status":  VerifyResponse(Public(b), 403, "request-sig", sig, body),
		"another request": VerifyResponse(Public(b), 200, "a-later-request", sig, body),
		"a changed body":  VerifyResponse(Public(b), 200, "request-sig", sig, []byte(`{"taken":false}`)),
		"no signature":    VerifyResponse(Public(b), 200, "request-sig", "", body),
		"no request":      VerifyResponse(Public(b), 200, "", SignResponse(b, 200, "", body), body),
	} {
		if !errors.Is(err, ErrSignature) {
			t.Errorf("%s: %v, want ErrSignature", name, err)
		}
	}
}
