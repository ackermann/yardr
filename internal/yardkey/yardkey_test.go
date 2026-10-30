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

	"yardr.dev/yardr/internal/store"
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
	if _, err := store.Load(path); !errors.Is(err, fs.ErrNotExist) {
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
	if err != nil || made || store.Public(again) != store.Public(k) {
		t.Fatalf("second Ensure = made %v, %v, same key %v", made, err, store.Public(again) == store.Public(k))
	}
	if pub, err := store.ParsePublic(store.Public(k)); err != nil || !pub.Equal(k.Public()) {
		t.Fatalf("ParsePublic(Public) = %v", err)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, fn := range map[string]func() error{
		"Load":   func() error { _, err := store.Load(path); return err },
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
	if kept, err := store.Load(path); err != nil || store.Public(kept) != store.Public(k) {
		t.Fatalf("the key after the refusal: %v", err)
	}

	junk := filepath.Join(t.TempDir(), "junk.key")
	if err := os.WriteFile(junk, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(junk); err == nil || strings.Contains(err.Error(), "not a key\n") {
		t.Fatalf("Load of junk = %v", err)
	}
}

// A request verifies for the key that signed it, the yard it was signed
// for and what it was signed over, within MaxSkew, and for nothing else.
func TestRequestSignature(t *testing.T) {
	a, b, c := newKey(t), newKey(t), newKey(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	body := []byte(`{"id":"m1","kind":"ping","body":{}}`)
	const path = "/peers/messages"
	ts, sig := SignRequest(a, store.Public(b), "POST", path, now, body)
	verify := func(from, to, method, path, ts, sig string, body []byte, at time.Time) error {
		return VerifyRequest(from, to, method, path, ts, sig, body, at)
	}
	if err := verify(store.Public(a), store.Public(b), "POST", path, ts, sig, body, now); err != nil {
		t.Fatalf("the request as signed: %v", err)
	}
	for _, off := range []time.Duration{MaxSkew, -MaxSkew} {
		if err := verify(store.Public(a), store.Public(b), "POST", path, ts, sig, body, now.Add(off)); err != nil {
			t.Errorf("%s off: %v", off, err)
		}
	}
	_, other := SignRequest(c, store.Public(b), "POST", path, now, body)
	for name, err := range map[string]error{
		"a wrong key":        verify(store.Public(c), store.Public(b), "POST", path, ts, sig, body, now),
		"another's signing":  verify(store.Public(a), store.Public(b), "POST", path, ts, other, body, now),
		"a changed body":     verify(store.Public(a), store.Public(b), "POST", path, ts, sig, append([]byte{}, body[1:]...), now),
		"another method":     verify(store.Public(a), store.Public(b), "GET", path, ts, sig, body, now),
		"another path":       verify(store.Public(a), store.Public(b), "POST", path+"?after=1", ts, sig, body, now),
		"a changed time":     verify(store.Public(a), store.Public(b), "POST", path, "1", sig, body, now),
		"another receiver":   verify(store.Public(a), store.Public(c), "POST", path, ts, sig, body, now),
		"an old timestamp":   verify(store.Public(a), store.Public(b), "POST", path, ts, sig, body, now.Add(MaxSkew+time.Second)),
		"a timestamp ahead":  verify(store.Public(a), store.Public(b), "POST", path, ts, sig, body, now.Add(-MaxSkew-time.Second)),
		"no signature":       verify(store.Public(a), store.Public(b), "POST", path, ts, "", body, now),
		"a key that is none": verify("x", store.Public(b), "POST", path, ts, sig, body, now),
	} {
		if !errors.Is(err, store.ErrSignature) {
			t.Errorf("%s: %v, want ErrSignature", name, err)
		}
	}
}
