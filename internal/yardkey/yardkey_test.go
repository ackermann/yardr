package yardkey

import (
	"crypto/ed25519"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	k, _, err := Ensure(filepath.Join(t.TempDir(), "peer.key"))
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// The key pair is made once, in a file of mode 0600, and is the same key
// every time after; a file open to others is refused, not used.
func TestEnsureMakesTheKeyOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peer.key")
	if _, err := Load(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Load with no file = %v", err)
	}
	k, made, err := Ensure(path)
	if err != nil || !made {
		t.Fatalf("Ensure = made %v, %v", made, err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v, %v", st.Mode().Perm(), err)
	}
	again, made, err := Ensure(path)
	if err != nil || made || Public(again) != Public(k) {
		t.Fatalf("second Ensure = made %v, %v, same key %v", made, err, Public(again) == Public(k))
	}
	if pub, err := ParsePublic(Public(k)); err != nil || !pub.Equal(k.Public()) {
		t.Fatalf("ParsePublic(Public) = %v", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, fn := range map[string]func() error{
		"Load":   func() error { _, err := Load(path); return err },
		"Ensure": func() error { _, _, err := Ensure(path); return err },
	} {
		if err := fn(); err == nil || !strings.Contains(err.Error(), "open to others") {
			t.Errorf("%s of a 0644 key file = %v", name, err)
		}
	}
	// It was not replaced by a new key either.
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if kept, err := Load(path); err != nil || Public(kept) != Public(k) {
		t.Fatalf("the key after the refusal: %v", err)
	}

	junk := filepath.Join(t.TempDir(), "junk.key")
	if err := os.WriteFile(junk, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(junk); err == nil || strings.Contains(err.Error(), "not a key\n") {
		t.Fatalf("Load of junk = %v", err)
	}
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

// A request verifies for the key that signed it, the yard it was signed
// for and what it was signed over, within MaxSkew, and for nothing else.
func TestRequestSignature(t *testing.T) {
	a, b, c := newKey(t), newKey(t), newKey(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"id":"m1","kind":"ping","body":{}}`)
	const path = "/peers/messages"
	ts, sig := SignRequest(a, Public(b), "POST", path, now, body)
	verify := func(from, to, method, path, ts, sig string, body []byte, at time.Time) error {
		return VerifyRequest(from, to, method, path, ts, sig, body, at)
	}
	if err := verify(Public(a), Public(b), "POST", path, ts, sig, body, now); err != nil {
		t.Fatalf("the request as signed: %v", err)
	}
	for _, off := range []time.Duration{MaxSkew, -MaxSkew} {
		if err := verify(Public(a), Public(b), "POST", path, ts, sig, body, now.Add(off)); err != nil {
			t.Errorf("%s off: %v", off, err)
		}
	}
	_, other := SignRequest(c, Public(b), "POST", path, now, body)
	for name, err := range map[string]error{
		"a wrong key":        verify(Public(c), Public(b), "POST", path, ts, sig, body, now),
		"another's signing":  verify(Public(a), Public(b), "POST", path, ts, other, body, now),
		"a changed body":     verify(Public(a), Public(b), "POST", path, ts, sig, append([]byte{}, body[1:]...), now),
		"another method":     verify(Public(a), Public(b), "GET", path, ts, sig, body, now),
		"another path":       verify(Public(a), Public(b), "POST", path+"?after=1", ts, sig, body, now),
		"a changed time":     verify(Public(a), Public(b), "POST", path, "1", sig, body, now),
		"another receiver":   verify(Public(a), Public(c), "POST", path, ts, sig, body, now),
		"an old timestamp":   verify(Public(a), Public(b), "POST", path, ts, sig, body, now.Add(MaxSkew+time.Second)),
		"a timestamp ahead":  verify(Public(a), Public(b), "POST", path, ts, sig, body, now.Add(-MaxSkew-time.Second)),
		"no signature":       verify(Public(a), Public(b), "POST", path, ts, "", body, now),
		"a key that is none": verify("x", Public(b), "POST", path, ts, sig, body, now),
	} {
		if !errors.Is(err, ErrSignature) {
			t.Errorf("%s: %v, want ErrSignature", name, err)
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
