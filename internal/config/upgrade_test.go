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

// A failure marker round-trips through its file, and AllTold is only when
// every direct send succeeded.
func TestUpgradeFailedRoundTrip(t *testing.T) {
	home := t.TempDir()
	at := time.Now().Truncate(time.Second).UTC()
	m := UpgradeFailed{Kind: "rolled_back", Revision: "1904417", Stage: "health", Reason: "daemon wedged",
		Yards: []UpgradeYardOutcome{{Home: home, Outcome: "back on 1904410", Restored: true}},
		Beads: []string{"yardr-1"}, Sends: []UpgradeSend{{User: "jo", Source: "pushover", Status: 200, OK: true}}, At: at}
	if err := WriteUpgradeFailed(home, m); err != nil {
		t.Fatal(err)
	}
	got, err := ReadUpgradeFailed(home)
	if err != nil || got.Kind != m.Kind || got.Revision != m.Revision || len(got.Yards) != 1 || len(got.Sends) != 1 || !got.At.Equal(at) {
		t.Fatalf("marker = %+v, %v", got, err)
	}
	if !got.AllTold() {
		t.Fatalf("AllTold with one ok send = false, want true")
	}
	empty := t.TempDir()
	if _, err := ReadUpgradeFailed(empty); !os.IsNotExist(err) {
		t.Fatalf("a home with no marker: %v, want a missing file", err)
	}
	for _, sends := range [][]UpgradeSend{nil, {}, {{User: "jo", OK: false}}, {{User: "jo", OK: true}, {User: "al", OK: false}}} {
		if (UpgradeFailed{Sends: sends}).AllTold() {
			t.Fatalf("AllTold with %+v = true, want false", sends)
		}
	}
}
