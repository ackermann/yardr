package telemetry

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
)

// MintID mints a random install id: 16 bytes from crypto/rand, never
// derived from the machine or the yard, so no one can guess or recompute
// it. It is version 4 in shape, like any random UUID.
func MintID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("minting the install id: %w", err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return formatUUID(b), nil
}

// DeriveUUID is the stable UUID of one batch event, from the install id,
// the event name, the day and the event's fixed dimensions, in that
// order. Rebuilding the same event derives the same UUID, so a
// source-day event in a later batch deduplicates against the earlier one.
// It hashes like a version-5 UUID (SHA-1; the use is derivation, not
// security); only stability across rebuilds is promised.
func DeriveUUID(parts ...string) string {
	h := sha1.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	var b [16]byte
	copy(b[:], h.Sum(nil))
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	return formatUUID(b)
}

// formatUUID is b in the canonical 8-4-4-4-12 shape.
func formatUUID(b [16]byte) string {
	s := hex.EncodeToString(b[:])
	return strings.Join([]string{s[0:8], s[8:12], s[12:16], s[16:20], s[20:32]}, "-")
}
