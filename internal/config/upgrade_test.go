package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A pass stamp and an upgrade gate round-trip through their files, and a
// home with neither errs as a missing file does.
func TestPassStampAndUpgradeGateRoundTrip(t *testing.T) {
	home := t.TempDir()
	at := time.Now().Truncate(time.Second).UTC()
	if err := WritePassStamp(home, PassStamp{Build: "1904417", At: at}); err != nil {
		t.Fatal(err)
	}
	s, err := ReadPassStamp(home)
	if err != nil || s.Build != "1904417" || !s.At.Equal(at) {
		t.Fatalf("stamp = %+v, %v; want build 1904417 at %v", s, err, at)
	}
	if err := WriteUpgradeGate(home, UpgradeGate{PID: 4242, Start: 99, Head: 7}); err != nil {
		t.Fatal(err)
	}
	g, err := ReadUpgradeGate(home)
	if err != nil || g != (UpgradeGate{PID: 4242, Start: 99, Head: 7}) {
		t.Fatalf("gate = %+v, %v", g, err)
	}
	// Written through a temporary file renamed into place: no temp file
	// is left behind.
	names, err := filepath.Glob(filepath.Join(home, ".*.tmp-*"))
	if err != nil || len(names) != 0 {
		t.Fatalf("temp files left in the home: %v, %v", names, err)
	}

	empty := t.TempDir()
	if _, err := ReadPassStamp(empty); !os.IsNotExist(err) {
		t.Fatalf("a home with no stamp: %v, want a missing file", err)
	}
	if _, err := ReadUpgradeGate(empty); !os.IsNotExist(err) {
		t.Fatalf("a home with no gate: %v, want a missing file", err)
	}
}

// A gate that is no JSON, or names no process, errs as a bad one.
func TestBadUpgradeGate(t *testing.T) {
	home := t.TempDir()
	path := At(home).UpgradeGate()
	for _, body := range []string{"not json", `{"pid":0,"start":1,"head":2}`} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if g, err := ReadUpgradeGate(home); err == nil {
			t.Fatalf("gate %q read as %+v, want an error", body, g)
		}
	}
}
