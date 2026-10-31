package store

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
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

// A device's call must answer while the yard's one writer is held, as a
// daemon pass holds it writing back to back: the list read is a snapshot
// and answers at once, and resolving the device's token answers from its
// read too, leaving the last-use stamp — a write — to a later call
// (yardr-wpgc: resolves timed out at the resolver's 5 s behind slow
// passes). No test drove a resolve against a held write before, so this
// is its own test rather than a case of TestDeviceLastUse above.
func TestDeviceResolveWhileWriteHeld(t *testing.T) {
	t.Parallel()
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
	held := make(chan struct{})
	release := make(chan struct{})
	// Once each: a batched write may run its function again, and a failed
	// assertion below must still let the held write go, through cleanup.
	var heldOnce, releaseOnce sync.Once
	releaseWrite := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseWrite)
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- s.write(func(w *wtx) error {
			heldOnce.Do(func() { close(held) })
			<-release
			return nil
		})
	}()
	<-held

	// The bead's bound: a device's call answers within 2 s while a pass
	// phase is held. The write stays held past it, until released below.
	within := func(name string, call func() error) {
		t.Helper()
		done := make(chan error, 1)
		go func() { done <- call() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("%s while a write is held: %v", name, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s did not answer in 2 s while a write is held", name)
		}
	}
	var got *Device
	within("ResolveDevice", func() error {
		var err error
		got, err = s.ResolveDevice(ctx, d.Token, "100.64.0.7")
		return err
	})
	if got == nil || got.User != "jo" || got.Name != "phone" {
		t.Fatalf("ResolveDevice while a write is held = %+v", got)
	}
	if got.LastUsed != nil {
		t.Fatalf("the last use was stamped while a write is held: %+v", got)
	}
	within("ListBeads", func() error {
		_, err := s.ListBeads(ctx, BeadFilter{})
		return err
	})

	releaseWrite()
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	// The stamp the held write deferred is made by the next call.
	got, err = s.ResolveDevice(ctx, d.Token, "100.64.0.7")
	if err != nil || got == nil || got.LastUsed == nil || got.LastFrom != "100.64.0.7" {
		t.Fatalf("ResolveDevice after the write = %+v, %v", got, err)
	}
}
