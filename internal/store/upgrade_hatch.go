package store

import (
	"context"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// MatchBeadless is why a bead-less yard event of kind, by actor, reaches
// the holder of sub, "" when it does not: the one place the dead-yard
// escape hatch matches what deliverBeadless would. Kind and actor are the
// event's own; a subscription that filters on any other key needs a bead,
// which such an event has none of, and only scope all reaches without one.
func MatchBeadless(sub Subscription, kind, actor string) string {
	if sub.Source != SourceYardr || !beadlessWhere(sub.Where) {
		return ""
	}
	fields := map[string][]string{"kind": kindWords(kind), "actor": {actor}}
	return match(sub, fields, given(false), given(false), given(false))
}

// HatchStore is the pre-install store copy as the dead-yard escape hatch
// reads it: the users and sources as they were before the install, opened
// read-only without migrations. It serves notify.Send.
type HatchStore struct {
	users   map[string]User
	sources map[string]Source
}

// OpenHatchCopy opens the store copy at path read-only, without applying
// migrations: the subscriptions the hatch matches are the yard's as they
// were. A copy that cannot be opened errs.
func OpenHatchCopy(path string) (*HatchStore, error) {
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true, Timeout: time.Second})
	if err != nil {
		return nil, err
	}
	defer db.Close()
	h := &HatchStore{users: map[string]User{}, sources: map[string]Source{}}
	err = db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(bUsers) != nil {
			if err := each(tx, bUsers, "", func(_ []byte, u *User) error {
				h.users[u.Name] = *u
				return nil
			}); err != nil {
				return err
			}
		}
		if tx.Bucket(bSources) != nil {
			// The copy stands in <home>/backups/, so the yard's home,
			// where the copies a declaration's mint runs from are, is two
			// up: a send through a minted source mints as the server would.
			home := filepath.Dir(filepath.Dir(path))
			if err := each(tx, bSources, "", func(_ []byte, src *Source) error {
				src.Mint = src.mintIn(home)
				h.sources[src.Name] = *src
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return h, nil
}

// Users are the copy's users.
func (h *HatchStore) Users() []User {
	out := make([]User, 0, len(h.users))
	for _, u := range h.users {
		out = append(out, u)
	}
	return out
}

// GetUser is the copy's user, for notify.Send.
func (h *HatchStore) GetUser(_ context.Context, name string) (*User, error) {
	u, ok := h.users[name]
	if !ok {
		return nil, ErrNotFound
	}
	return &u, nil
}

// GetSource is the copy's source, for notify.Send.
func (h *HatchStore) GetSource(_ context.Context, name string) (*Source, error) {
	s, ok := h.sources[name]
	if !ok {
		return nil, ErrNotFound
	}
	return &s, nil
}
