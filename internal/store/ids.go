package store

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A bead has two names. Its UUID (a UUIDv7) is its identity: the beads
// bucket is keyed by it, and edges, notes, sessions and events refer to it,
// so ids need no single-writer counter. Its handle (yard-k3f9) is the short
// name people, branches, worktrees and herdr labels use; it is chosen once
// and never changes. Everything the store returns names beads by handle.
//
// Because the UUID's leading bits are its creation time, iterating the
// beads bucket yields creation order. Handles are random and carry no order.

// ErrAmbiguous means a handle prefix matches more than one bead.
var ErrAmbiguous = errors.New("ambiguous bead prefix")

var bHandles = []byte("handles") // handle -> uuid

// MinPrefix is how many characters after "<prefix>-" a handle prefix needs
// before it is matched against handles; exact handles always resolve.
const MinPrefix = 4

// handleAlphabet is Crockford's base32 in lowercase: no i, l, o or u, so a
// handle survives being read aloud or retyped.
const handleAlphabet = "0123456789abcdefghjkmnpqrstvwxyz"

// uuidAt returns a UUIDv7 for t: 48 bits of Unix milliseconds, the
// sub-millisecond fraction in rand_a (RFC 9562 method 3, so beads created
// within one millisecond still sort by time), and 62 random bits.
func uuidAt(t time.Time) [16]byte {
	var u [16]byte
	if _, err := rand.Read(u[8:]); err != nil {
		panic(err)
	}
	binary.BigEndian.PutUint64(u[:8], uuidHigh(t))
	u[8] = 0x80 | u[8]&0x3f // variant 10
	return u
}

// uuidHigh is the time-ordered first half of a UUIDv7: milliseconds,
// version 7, then the millisecond fraction in 12 bits.
func uuidHigh(t time.Time) uint64 {
	ns := t.UnixNano()
	ms, frac := uint64(ns/1e6), uint64(ns%1e6)*4096/1e6
	return ms<<16 | 0x7<<12 | frac
}

// after returns u, or if u does not sort after prev, u with its time
// advanced to the smallest step past prev, so keys minted in one store are
// strictly increasing even when the clock stalls or steps back.
func after(prev string, u [16]byte) [16]byte {
	p, ok := parseUUID(prev)
	if !ok {
		return u
	}
	ph, uh := binary.BigEndian.Uint64(p[:8]), binary.BigEndian.Uint64(u[:8])
	if uh > ph {
		return u
	}
	if ph&0xfff == 0xfff { // fraction full: next millisecond, fraction 0
		uh = (ph>>16+1)<<16 | 0x7<<12
	} else {
		uh = ph + 1
	}
	binary.BigEndian.PutUint64(u[:8], uh)
	return u
}

func formatUUID(u [16]byte) string {
	h := hex.EncodeToString(u[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// parseUUID accepts the canonical lowercase form only, as stored.
func parseUUID(s string) ([16]byte, bool) {
	var u [16]byte
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, false
	}
	h := s[:8] + s[9:13] + s[14:18] + s[19:23] + s[24:]
	if strings.ToLower(h) != h {
		return u, false
	}
	if _, err := hex.Decode(u[:], []byte(h)); err != nil {
		return u, false
	}
	return u, true
}

func isUUID(s string) bool {
	_, ok := parseUUID(strings.ToLower(s))
	return ok
}

// handleSuffix spells the UUID's 62 random bits (not its prefix, which is
// the timestamp) in base32: up to 12 characters, of which a handle uses as
// few as stay unique.
func handleSuffix(u [16]byte) string {
	bits := binary.BigEndian.Uint64(u[8:]) << 2 // drop the variant
	out := make([]byte, 12)
	for i := range out {
		out[i] = handleAlphabet[bits>>59]
		bits <<= 5
	}
	return string(out)
}

// mintHandle picks <prefix>-<suffix> with the shortest suffix (at least
// MinPrefix characters) no other bead holds. Handles are only lengthened
// here, at mint time; once stored they never change.
func mintHandle(tx *bolt.Tx, prefix string, u [16]byte) (string, error) {
	suffix := handleSuffix(u)
	for k := MinPrefix; k <= len(suffix); k++ {
		h := prefix + "-" + suffix[:k]
		if tx.Bucket(bHandles).Get([]byte(h)) == nil {
			return h, nil
		}
	}
	return "", fmt.Errorf("no free handle for %s under prefix %s", formatUUID(u), prefix)
}

// resolve finds the UUID of the bead ref names: its UUID, its handle, or a
// unique prefix of its handle with at least MinPrefix characters after the
// last '-'.
func resolve(tx *bolt.Tx, ref string) (string, error) {
	if u := strings.ToLower(ref); isUUID(u) && tx.Bucket(bBeads).Get([]byte(u)) != nil {
		return u, nil
	}
	if u := tx.Bucket(bHandles).Get([]byte(ref)); u != nil {
		return string(u), nil
	}
	notFound := fmt.Errorf("bead %s: %w", ref, ErrNotFound)
	dash := strings.LastIndexByte(ref, '-')
	if dash < 0 {
		return "", notFound
	}
	const show = 5
	var found, uuids []string
	c := tx.Bucket(bHandles).Cursor()
	p := []byte(ref)
	for k, v := c.Seek(p); k != nil && strings.HasPrefix(string(k), ref) && len(found) <= show; k, v = c.Next() {
		found, uuids = append(found, string(k)), append(uuids, string(v))
	}
	switch {
	case len(found) == 0:
		return "", notFound
	case len(ref)-dash-1 < MinPrefix:
		return "", fmt.Errorf("bead %s: give at least %d characters after %q, or the whole handle",
			ref, MinPrefix, ref[:dash+1])
	case len(found) == 1:
		return uuids[0], nil
	}
	list := strings.Join(found[:min(len(found), show)], ", ")
	if len(found) > show {
		list += ", ..."
	}
	return "", fmt.Errorf("bead %s matches %s: %w", ref, list, ErrAmbiguous)
}

// handleCache maps UUIDs to handles for one read, so translating many records
// that name the same beads decodes each bead once.
type handleCache struct {
	tx   *bolt.Tx
	seen map[string]string
}

func handlesIn(tx *bolt.Tx) *handleCache { return &handleCache{tx: tx, seen: map[string]string{}} }

// of returns the handle of the bead with UUID u, or u when there is none.
func (h *handleCache) of(u string) string {
	if v, ok := h.seen[u]; ok {
		return v
	}
	v := u
	var row beadRow
	if ok, _ := get(h.tx, bBeads, u, &row); ok {
		v = row.ID
	}
	h.seen[u] = v
	return v
}
