// The yard's teammates as one call reads them and one call files the
// removal of: the standing web_jwt_users mapping joined to its users and
// devices (Teammates), and the run ask that removes one
// (UninviteTeammate). Both are the operator's calls
// (rpc.OperatorCalls): on the socket a read from the yard's machine and a
// filing any asker may make, as AskNew is, and on the listener a device
// of an operator's.
package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

// TeammateDevice is one device of a teammate's row: its name and when its
// token was last resolved, nil for never. Where it was resolved from is no
// device's business, so LastFrom is not told.
type TeammateDevice struct {
	Name     string     `json:"name"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

// Teammate is one row of Teammates: one address of the standing
// web_jwt_users mapping with its user and devices. A user with two mapped
// addresses stands in two rows. It is built explicitly, and never from a
// User whole: the user's EnvFile, cursors, subscriptions and push record
// are no device's business, an operator's included.
type Teammate struct {
	Name      string           `json:"name"`
	Role      string           `json:"role"`
	Email     string           `json:"email"`
	InvitedBy string           `json:"invited_by"`
	InvitedAt time.Time        `json:"invited_at"`
	Devices   []TeammateDevice `json:"devices"`
}

// Teammates lists the yard's teammates: one row per address of the
// standing web_jwt_users mapping, joined to its user and devices, in
// address order. A row with devices is paired; a row without is waiting.
// Whether anyone signed in over the edge's web door is not told: nothing
// at the yard records it. A mapped name with no user record is omitted and
// logged without its address: invite and removal write the two together, so
// an orphan mapping is yard damage, and the mapping is set by hand too.
func (s *Store) Teammates(ctx context.Context) ([]Teammate, error) {
	out := []Teammate{}
	var orphans []string
	err := s.read(func(tx *bolt.Tx) error {
		w := &wtx{Tx: tx, store: s}
		mapping, _, err := w.webJWTUsers()
		if err != nil {
			return fmt.Errorf("%s: %w", WebJWTUsersSetting, err)
		}
		for _, email := range slices.Sorted(maps.Keys(mapping)) {
			u, err := getUser(tx, mapping[email])
			if err != nil {
				if errors.Is(err, ErrNotFound) {
					orphans = append(orphans, mapping[email])
					continue
				}
				return err
			}
			devices := []TeammateDevice{}
			if err := each(tx, bDevices, u.Name+"\x00", func(_ []byte, d *deviceRecord) error {
				devices = append(devices, TeammateDevice{Name: d.Name, LastUsed: d.LastUsed})
				return nil
			}); err != nil {
				return err
			}
			out = append(out, Teammate{
				Name:      u.Name,
				Role:      u.RoleOf(),
				Email:     email,
				InvitedBy: u.CreatedBy,
				InvitedAt: u.CreatedAt,
				Devices:   devices,
			})
		}
		return nil
	})
	if log := s.log.Load(); err == nil && log != nil {
		slices.Sort(orphans)
		for _, name := range slices.Compact(orphans) {
			log.Warn("teammate mapping has no user record", "user", name)
		}
	}
	return out, err
}

// Uninvited is what UninviteTeammate filed: the bead and the ask on it.
type Uninvited struct {
	Bead string `json:"bead"`
	Ask  uint64 `json:"ask"`
}

// UninviteTeammate files the removal of the teammate user: the run ask
// `user uninvite <user>` on a new bead, addressed to the yard's operator,
// as AskNew files it. The card it makes is the same card a
// `yardr bead ask --run "user uninvite <user>"` makes, and approval,
// the edge-first removal and the retry are that card's built path.
//
// It files the ask and no more. The card refuses a user the yard has
// not, and one no mapping names (a plain user, whose removal is user
// rm's), and a refused ask files nothing; a caller files another
// teammate's removal, never their own user's.
func (s *Store) UninviteTeammate(ctx context.Context, user, actor string) (*Uninvited, error) {
	if user != "" && user == actor {
		return nil, fmt.Errorf("user %s is the caller: file the removal of another teammate, not of the user filing it", user)
	}
	var to string
	err := s.read(func(tx *bolt.Tx) error {
		ops, err := operatorsIn(tx)
		if err != nil {
			return err
		}
		setting, err := operatorIn(tx)
		if err != nil {
			return err
		}
		switch {
		case ops.is(setting):
			to = setting
		case !ops.none():
			to = ops[0]
		default:
			// No operator to address: the ask names the caller and
			// the card refuses it with its reason.
			to = actor
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	made, err := s.AskNew(ctx, NewBead{Priority: DefaultPriority}, Ask{
		Question: fmt.Sprintf("Remove %s as a teammate?", user),
		To:       to,
		Context:  []string{"Approval reports the removal to the edge first, then removes the user and the mapping."},
		Run:      &AskRun{Argv: []string{"user uninvite " + user}},
	}, actor)
	if err != nil {
		return nil, err
	}
	return &Uninvited{Bead: made.Bead.ID, Ask: made.Ask}, nil
}
