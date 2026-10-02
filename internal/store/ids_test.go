package store

import (
	"context"
	"encoding/binary"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestUUIDv7Layout(t *testing.T) {
	at := time.Date(2026, 10, 2, 15, 51, 0, 500_000_000+250_000, time.UTC) // .500250
	u := uuidAt(at)
	s := formatUUID(u)
	if !isUUID(s) || !isUUID(strings.ToUpper(s)) {
		t.Fatalf("%s is not a canonical UUID", s)
	}
	if v := u[6] >> 4; v != 7 {
		t.Fatalf("version %d", v)
	}
	if u[8]>>6 != 0b10 {
		t.Fatalf("variant bits %02b", u[8]>>6)
	}
	high := binary.BigEndian.Uint64(u[:8])
	if ms := int64(high >> 16); ms != at.UnixMilli() {
		t.Fatalf("timestamp %d, want %d", ms, at.UnixMilli())
	}
	if frac := high & 0xfff; frac != 250*4096/1000 {
		t.Fatalf("sub-millisecond fraction %d", frac)
	}
	if p, ok := parseUUID(s); !ok || p != u {
		t.Fatal("parse does not round-trip")
	}
}

// Keys minted in one store always increase, also within one clock tick,
// when the clock steps back, and across a full sub-millisecond fraction.
func TestUUIDsIncrease(t *testing.T) {
	at := time.Now()
	prev := formatUUID(uuidAt(at))
	for i := 0; i < 5000; i++ { // more than the 4096 fractions of a millisecond
		next := formatUUID(after(prev, uuidAt(at.Add(-time.Duration(i%3)*time.Millisecond))))
		if next <= prev {
			t.Fatalf("step %d: %s does not sort after %s", i, next, prev)
		}
		if u, _ := parseUUID(next); u[6]>>4 != 7 {
			t.Fatalf("step %d: lost the version nibble: %s", i, next)
		}
		prev = next
	}
	later := uuidAt(at.Add(time.Hour))
	if after(prev, later) != later {
		t.Fatal("a later UUID was changed")
	}
}

func TestHandleSuffixUsesRandomBits(t *testing.T) {
	at := time.Now()
	a, b := uuidAt(at), uuidAt(at) // same timestamp, different randomness
	if handleSuffix(a) == handleSuffix(b) {
		t.Fatal("suffix does not depend on the random bits")
	}
	for _, c := range handleSuffix(a) {
		if !strings.ContainsRune(handleAlphabet, c) || strings.ContainsRune("ilou", c) {
			t.Fatalf("suffix %s has %q", handleSuffix(a), c)
		}
	}
}

// A handle is lengthened only when its shorter forms are taken.
func TestMintHandleLengthensOnCollision(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "yard.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close(context.Background())
	u := uuidAt(time.Now())
	suffix := handleSuffix(u)
	err = s.db.Update(func(tx *bolt.Tx) error {
		h, err := mintHandle(tx, "yard", u)
		if err != nil || h != "yard-"+suffix[:4] {
			t.Fatalf("free: %s %v", h, err)
		}
		for k := 4; k <= 6; k++ {
			if err := tx.Bucket(bHandles).Put([]byte("yard-"+suffix[:k]), []byte("taken")); err != nil {
				return err
			}
			h, err := mintHandle(tx, "yard", u)
			if err != nil || h != "yard-"+suffix[:k+1] {
				t.Fatalf("with %d taken: %s %v, want yard-%s", k, h, err, suffix[:k+1])
			}
		}
		// Another prefix does not collide.
		if h, _ := mintHandle(tx, "web", u); h != "web-"+suffix[:4] {
			t.Fatalf("other prefix: %s", h)
		}
		for k := 7; k <= len(suffix); k++ {
			if err := tx.Bucket(bHandles).Put([]byte("yard-"+suffix[:k]), []byte("taken")); err != nil {
				return err
			}
		}
		if _, err := mintHandle(tx, "yard", u); err == nil {
			t.Fatal("minted a handle with every length taken")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
