package store

import (
	"context"
	"crypto/subtle"
	"fmt"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/pairing"
)

// A device is enrolled by a code as two yards are paired (pairing.go,
// docs/users.md "Enrolled by a code"): the operator opens a pairing for one
// device of one user, the yard shows a code of the kind a pairing of yards
// has, and the device that calls with it is answered with its token. It is
// that pairing's code, its ten minutes, its five wrong tries and its one
// use, and like it an open one is in the server's memory and nowhere else.
//
// What it has not is the exchange of keys and the check two operators
// compare: a device has no key to be put in place of, and what it is given
// is a bearer token, which whoever reads the connection reads on every call
// after this one. The connection is what a device rests on (docs/api.md,
// "Over the network"), so the code crosses on it as the token does.

// DeviceNotAdded is why a device pairing ended when the code was right and
// the device could not be added: its user was removed, or a device of its
// name added, while the pairing was open. Its other reasons are pairing's.
const DeviceNotAdded = "not_added"

// DevicePairing is the yard's device pairing as its operator sees it: one
// of PairOpened, PairDone and PairEnded. It never holds the code.
type DevicePairing struct {
	ID      string    `json:"id"`
	User    string    `json:"user"`
	Device  string    `json:"device"`
	State   string    `json:"state"`
	Expires time.Time `json:"expires"`
	// Tries counts the wrong codes.
	Tries int `json:"wrong_codes,omitempty"`
	// Reason is why it ended without a device, and Detail what there is to
	// say beyond it.
	Reason string `json:"reason,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// DevicePairingCode is a device pairing just opened, with the code the
// device calls with.
type DevicePairingCode struct {
	DevicePairing
	Code string `json:"code"`
}

// devicePairings holds the yard's device pairing. Its lock is taken before
// any of the store's, never after.
type devicePairings struct {
	mu  sync.Mutex
	cur *devicePairing
	// clock is the time a pairing runs out by when a test set one
	// (SetDevicePairClock).
	clock func() time.Time
}

// devicePairing is the one device pairing a yard has open, or had last.
type devicePairing struct {
	DevicePairing
	actor string
	// code is the code in the form both sides compute from, until it is
	// used or the pairing ends.
	code string
}

func (p *devicePairing) event(extra map[string]any) map[string]any {
	data := map[string]any{"pairing": p.ID, "user": p.User, "device": p.Device}
	if p.Tries > 0 {
		data["wrong_codes"] = p.Tries
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

// SetDevicePairClock replaces the clock a device pairing runs out by
// (tests).
func (s *Store) SetDevicePairClock(now func() time.Time) {
	s.devPair.mu.Lock()
	s.devPair.clock = now
	s.devPair.mu.Unlock()
}

// devicePairNow is the time, with s.devPair.mu held.
func (s *Store) devicePairNow() time.Time {
	if s.devPair.clock != nil {
		return s.devPair.clock()
	}
	return now()
}

// endDevicePairing ends the pairing without a device, for reason, and logs
// it.
func (s *Store) endDevicePairing(ctx context.Context, p *devicePairing, reason, detail, actor string) {
	p.State, p.Reason, p.Detail, p.code = PairEnded, reason, detail, ""
	// The pairing has ended whether or not the event is written.
	_ = s.LogEvent(ctx, "", "device_pair_refused", actor, p.event(map[string]any{"reason": reason}))
}

// devicePairing is the yard's device pairing as it is now, with
// s.devPair.mu held: one whose time ran out is ended here, by whoever looks
// first.
func (s *Store) devicePairing(ctx context.Context) *devicePairing {
	p := s.devPair.cur
	if p != nil && p.State == PairOpened && !s.devicePairNow().Before(p.Expires) {
		s.endDevicePairing(ctx, p, pairing.TimedOut, "", "yard")
	}
	return p
}

// DevicePairOpen opens a pairing for the device name of the user and
// returns its code, good for pairing.TTL and for one device. A yard has one
// open at a time. It is asked of whoever AddDevice is, and refuses what
// that would: the device is added when the code is used, as by the actor
// who opened the pairing.
func (s *Store) DevicePairOpen(ctx context.Context, user, name, actor string) (*DevicePairingCode, error) {
	if err := checkDeviceName(name); err != nil {
		return nil, err
	}
	err := s.read(func(tx *bolt.Tx) error {
		if err := s.may(ctx, tx, actor, ActAddDevice, Object{Kind: KindUser, Name: user}); err != nil {
			return err
		}
		return deviceFree(tx, user, name)
	})
	if err != nil {
		return nil, err
	}
	s.devPair.mu.Lock()
	defer s.devPair.mu.Unlock()
	if p := s.devicePairing(ctx); p != nil && p.State == PairOpened {
		return nil, fmt.Errorf("a pairing is open already, for the device %s of %s until %s: a yard has one at a time (yardr user device pair --end ends it)",
			p.Device, p.User, p.Expires.Local().Format("15:04:05"))
	}
	code := pairing.NewCode()
	clean, err := pairing.CleanCode(code)
	if err != nil {
		return nil, err
	}
	p := &devicePairing{DevicePairing: DevicePairing{ID: newPeerMessageID(), User: user, Device: name, State: PairOpened,
		Expires: s.devicePairNow().Add(pairing.TTL)}, actor: actor, code: clean}
	if err := s.LogEvent(ctx, "", "device_pair_opened", actor, p.event(map[string]any{"expires": p.Expires})); err != nil {
		return nil, err
	}
	s.devPair.cur = p
	return &DevicePairingCode{DevicePairing: p.DevicePairing, Code: code}, nil
}

// DevicePairClose ends the device pairing the yard has open and answers it
// as it ended: nothing is added, and the code is good for nothing. The
// operator ends one, as the operator opens one.
func (s *Store) DevicePairClose(ctx context.Context, actor string) (*DevicePairing, error) {
	if err := s.mayRead(ctx, actor, ActEndDevicePairing, Object{Kind: KindDevice}); err != nil {
		return nil, err
	}
	s.devPair.mu.Lock()
	defer s.devPair.mu.Unlock()
	p := s.devicePairing(ctx)
	if p == nil || p.State != PairOpened {
		return nil, fmt.Errorf("an open device pairing: %w (one lives in the server's memory, and ends with it)", ErrNotFound)
	}
	s.endDevicePairing(ctx, p, pairing.Closed, "", actor)
	out := p.DevicePairing
	return &out, nil
}

// DevicePairOpened reports whether a device pairing is open now: whether a
// code can be redeemed at all. Like DevicePairRedeem it is the server's to
// ask, for a request it was sent, and no method of the API.
func (s *Store) DevicePairOpened(ctx context.Context) bool {
	s.devPair.mu.Lock()
	defer s.devPair.mu.Unlock()
	p := s.devicePairing(ctx)
	return p != nil && p.State == PairOpened
}

// DevicePairRedeem takes the code a device calls with and answers the
// device the pairing was opened for, added now, with its token: the one
// time the token is anywhere. A wrong code is refused and counted, and
// pairing.MaxTries of them end the pairing; the right one is used with
// this call, whatever becomes of adding the device. What is no code at all
// is an error that is no *pairing.Refused, and no try. With no pairing open
// the refusal is pairing.NoPairing, whatever the last one ended for.
func (s *Store) DevicePairRedeem(ctx context.Context, code string) (*NewDevice, error) {
	s.devPair.mu.Lock()
	defer s.devPair.mu.Unlock()
	p := s.devicePairing(ctx)
	if p == nil || p.State != PairOpened {
		return nil, &pairing.Refused{Reason: pairing.NoPairing}
	}
	clean, err := pairing.CleanCode(code)
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(clean), []byte(p.code)) != 1 {
		p.Tries++
		if p.Tries >= pairing.MaxTries {
			s.endDevicePairing(ctx, p, pairing.WrongCode, "", "yard")
		}
		return nil, &pairing.Refused{Reason: pairing.WrongCode}
	}
	p.code = ""
	// Made before the write, which may run twice (write).
	out, err := newDevice()
	if err == nil {
		err = s.write(func(w *wtx) error {
			return putDevice(w, out, p.User, p.Device, p.actor, map[string]any{"pairing": p.ID})
		})
	}
	if err != nil {
		s.endDevicePairing(ctx, p, DeviceNotAdded, err.Error(), "yard")
		return nil, &pairing.Refused{Reason: DeviceNotAdded}
	}
	p.State = PairDone
	return out, nil
}
