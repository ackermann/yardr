package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A device's last use is written when it is first used, when it calls from
// another address, and otherwise at most once a minute: a window that asks
// every few seconds is not a write of the store each time. The address is
// text from outside and is kept bounded.
func TestDeviceLastUse(t *testing.T) {
	// Sequential: the package's fake clock.
	at := time.Date(2026, 10, 12, 19, 0, 0, 0, time.UTC)
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	ctx := context.Background()
	if err := s.SetSetting(ctx, OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	d, err := s.AddDevice(ctx, "jo", "phone", "jo")
	if err != nil {
		t.Fatal(err)
	}
	use := func(after time.Duration, from string) (time.Time, string) {
		t.Helper()
		at = at.Add(after)
		got, err := s.ResolveDevice(ctx, d.Token, from)
		if err != nil || got == nil {
			t.Fatalf("ResolveDevice = %+v, %v", got, err)
		}
		list, err := s.Devices(ctx, "jo")
		if err != nil || len(list) != 1 || list[0].LastUsed == nil {
			t.Fatalf("Devices = %+v, %v", list, err)
		}
		return *list[0].LastUsed, list[0].LastFrom
	}
	first, from := use(time.Minute, "100.64.0.7")
	if !first.Equal(at) || from != "100.64.0.7" {
		t.Fatalf("the first use = %s from %s", first, from)
	}
	if got, _ := use(59*time.Second, "100.64.0.7"); !got.Equal(first) {
		t.Fatalf("a use within the minute was written: %s", got)
	}
	if got, _ := use(time.Second, "100.64.0.7"); !got.Equal(at) {
		t.Fatalf("a use after the minute was not written: %s, want %s", got, at)
	}
	if got, from := use(time.Second, "100.64.0.9"); !got.Equal(at) || from != "100.64.0.9" {
		t.Fatalf("a use from another address was not written: %s from %s", got, from)
	}
	if _, from := use(time.Second, " "+strings.Repeat("a", 200)+"\xff "); from != strings.Repeat("a", 64) {
		t.Fatalf("an address from outside is kept as %q", from)
	}
}
