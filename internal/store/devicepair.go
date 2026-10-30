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

// DevicePairingState is the yard's open device pairing as it is now, with
// the code as it was shown when it was opened and the QR string a phone
// scans in place of typing (QR).
type DevicePairingState struct {
	DevicePairingCode
	// QR is what a QR code of the pairing holds: the address the device
	// reaches the yard at, then '#', then the code, and '#' and the yard
	// beside them where the address is the edge's. It is "" while the
	// yard's edge setting names no edge, and the code is then given where
	// it is asked for.
	QR string `json:"qr"`
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

// wrongDevicePairCode counts one wrong try against p, with s.devPair.mu
// held, and answers the refusal for it: pairing.MaxTries of them end the
// pairing. It is the one place a try is counted, for a code that is not the
// code and for a presenter that is not the pairing's user alike.
func (s *Store) wrongDevicePairCode(ctx context.Context, p *devicePairing) error {
	p.Tries++
	if p.Tries >= pairing.MaxTries {
		s.endDevicePairing(ctx, p, pairing.WrongCode, "", "yard")
	}
	return &pairing.Refused{Reason: pairing.WrongCode}
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
	return s.openDevicePairing(ctx, user, name, actor, func(tx *bolt.Tx) error {
		return s.may(ctx, tx, actor, ActAddDevice, Object{Kind: KindUser, Name: user})
	}, func(p *devicePairing) error {
		if p.User != user {
			return fmt.Errorf("a pairing is already open for %s, try again in a few minutes", p.User)
		}
		return fmt.Errorf("a pairing is open already, for the device %s of %s until %s: a yard has one at a time (yardr user device pair --end ends it)",
			p.Device, p.User, p.Expires.Local().Format("15:04:05"))
	})
}

// DevicePairOpenSelf opens a pairing for the device name of the user
// themselves, as the web view's pairing page does for the user its signed-in
// session names. It is DevicePairOpen with the leave-taking left out: no may
// call stands in front of it, and the user is the actor of its events, so the
// web page — which passes its signed-in user and no name from the request —
// opens a pairing for that user's own devices and for no one else's. The
// pairing is the yard's one, as the operator's is: while it lives the
// operator's opening is refused too, and the other way round.
func (s *Store) DevicePairOpenSelf(ctx context.Context, user, name string) (*DevicePairingCode, error) {
	return s.openDevicePairing(ctx, user, name, user, nil, func(p *devicePairing) error {
		if p.User == user {
			return fmt.Errorf("a pairing is open already, for your device %s until %s: a yard has one at a time (end it to open another)",
				p.Device, p.Expires.Local().Format("15:04:05"))
		}
		return fmt.Errorf("a pairing is already open for %s, try again in a few minutes", p.User)
	})
}

// openDevicePairing is DevicePairOpen and DevicePairOpenSelf: check, inside
// the read, is the leave the opening takes (nil for one's own), and busy
// words the refusal when the yard's one pairing already lives. A refusal
// over another user's pairing names the holder's yard user, and nothing of
// the device: the yard's users already know each other, and one sentence
// serves the terminal, the page and the relay alike.
func (s *Store) openDevicePairing(ctx context.Context, user, name, actor string, check func(tx *bolt.Tx) error, busy func(p *devicePairing) error) (*DevicePairingCode, error) {
	if err := checkDeviceName(name); err != nil {
		return nil, err
	}
	err := s.read(func(tx *bolt.Tx) error {
		if check != nil {
			if err := check(tx); err != nil {
				return err
			}
		}
		return deviceFree(tx, user, name)
	})
	if err != nil {
		return nil, err
	}
	s.devPair.mu.Lock()
	defer s.devPair.mu.Unlock()
	if p := s.devicePairing(ctx); p != nil && p.State == PairOpened {
		return nil, busy(p)
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

// DevicePairState answers the yard's open device pairing with its code,
// as it was shown when it was opened, and the QR string that holds the
// address, the code and the yard, so a window reopened shows the same
// pairing again. The address is the edge setting's, and the yard its
// handle/name; with no edge setting the QR string is empty, and the code
// is given where it is asked for. With no pairing open it is no pairing,
// in the same words as DevicePairCodeSelf's. It is the operator's read
// (rpc.OperatorCalls): on the socket an operator-class call, and never
// served to a device, which would read the pairing's whole credential
// here. A device token never opens a pairing.
func (s *Store) DevicePairState(ctx context.Context) (*DevicePairingState, error) {
	s.devPair.mu.Lock()
	p := s.devicePairing(ctx)
	if p == nil || p.State != PairOpened {
		s.devPair.mu.Unlock()
		return nil, fmt.Errorf("an open device pairing: %w (one lives in the server's memory, and ends with it)", ErrNotFound)
	}
	out := DevicePairingState{DevicePairingCode: DevicePairingCode{DevicePairing: p.DevicePairing, Code: displayCode(p.code)}}
	s.devPair.mu.Unlock()
	all, err := s.Settings(ctx)
	if err != nil {
		return nil, err
	}
	if e, connected, err := EdgeOf(all); err == nil && connected {
		out.QR = e.URL + "#" + out.Code + "#" + e.HandleName()
	}
	return &out, nil
}

// DevicePairCloseSelf ends the open pairing of the user themselves, as the
// web view's pairing page does for the user its signed-in session names.
// A pairing of another user's is no pairing to them, in the same words as
// none at all, so nothing of it is learned by asking.
func (s *Store) DevicePairCloseSelf(ctx context.Context, user string) (*DevicePairing, error) {
	s.devPair.mu.Lock()
	defer s.devPair.mu.Unlock()
	p := s.devicePairing(ctx)
	if p == nil || p.State != PairOpened || p.User != user {
		return nil, fmt.Errorf("an open device pairing: %w (one lives in the server's memory, and ends with it)", ErrNotFound)
	}
	s.endDevicePairing(ctx, p, pairing.Closed, "", user)
	out := p.DevicePairing
	return &out, nil
}

// DevicePairCodeSelf answers the user's own open pairing with its code, as
// it was shown when it was opened, so the web view's pairing page shows the
// same QR again on a reload. Another user's pairing, and none, are both no
// pairing to them. The code is the one in the server's memory: it is gone
// when the pairing ends, and this answers none then.
func (s *Store) DevicePairCodeSelf(ctx context.Context, user string) (*DevicePairingCode, error) {
	s.devPair.mu.Lock()
	defer s.devPair.mu.Unlock()
	p := s.devicePairing(ctx)
	if p == nil || p.State != PairOpened || p.User != user {
		return nil, fmt.Errorf("an open device pairing: %w (one lives in the server's memory, and ends with it)", ErrNotFound)
	}
	return &DevicePairingCode{DevicePairing: p.DevicePairing, Code: displayCode(p.code)}, nil
}

// displayCode is a pairing's stored code as it is shown: XXXX-XXXX (the
// stored form has no dash, as pairing.CleanCode leaves it).
func displayCode(clean string) string {
	if len(clean) <= 4 {
		return clean
	}
	return clean[:4] + "-" + clean[4:]
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
//
// Holding the code is the whole credential here, as it is on the yard's own
// listener; a redemption that names who presents it is DevicePairRedeemBy,
// or DevicePairRedeemUnmapped where the address maps to no user.
func (s *Store) DevicePairRedeem(ctx context.Context, code string) (*NewDevice, error) {
	return s.redeemDevicePairing(ctx, code, nil, nil)
}

// DevicePairRedeemBy is DevicePairRedeem for a redemption that names who
// presents it, as the edge does for one it forwards from a signed-in
// session: presenter is the yard user the edge's word maps to. The code is
// still required, and only the user the pairing was opened for may redeem
// it so, so naming a presenter can only narrow who redeems and never widen
// it. Any other presenter is refused as a wrong code is, with the same
// refusal and the same count, and the code is neither looked at nor used:
// the one it was opened for can still redeem it. Nobody, "", is never the
// one a pairing was opened for, so it is always refused here; a single
// address that maps to nobody is DevicePairRedeemUnmapped's, not this.
func (s *Store) DevicePairRedeemBy(ctx context.Context, code, presenter string) (*NewDevice, error) {
	return s.redeemDevicePairing(ctx, code, &presenter, nil)
}

// DevicePairRedeemUnmapped is DevicePairRedeemBy for a redemption whose
// presenter is one address that maps to no user in web_jwt_users: mapped
// holds the users the mapping names, as the caller read it. Where the
// pairing's user is one of them the presenter is refused as a wrong code
// is, exactly as DevicePairRedeemBy refuses; where neither is named the
// code redeems as with no presenter at all. Through the edge an unmapped
// presenter of a yard with members can only be its owner, since the edge
// has already resolved the session to the owner or an active member and an
// invite maps every active member: an owner pairing for an unmapped
// operator user is the ordinary case this redeems. It rests on that, and
// the code is still required: where an operator removes a teammate's line
// while the edge still holds them active, or before the edge records a
// removal, that holder redeems the owner's pairing with its code.
func (s *Store) DevicePairRedeemUnmapped(ctx context.Context, code string, mapped map[string]bool) (*NewDevice, error) {
	nobody := ""
	return s.redeemDevicePairing(ctx, code, &nobody, mapped)
}

// redeemDevicePairing is every redemption: presenter is nil for one that
// names none, else the user it names. A pairing's user is never "", so
// nobody is never the one it was opened for. mapped is nil but for an
// unmapped address, where it holds the users the mapping names: nobody
// with nil mapping, a header sent twice or empty and an unreadable
// mapping, is always refused, while nobody with a mapping is refused only
// when the pairing's user is one it names. A body that is no code at all
// is no try, whoever presents it. Past that the presenter is judged before
// the code, so a presenter that may not redeem is refused the same whether
// or not it holds the right code, and the code is not even compared for
// it.
func (s *Store) redeemDevicePairing(ctx context.Context, code string, presenter *string, mapped map[string]bool) (*NewDevice, error) {
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
	if presenter != nil {
		switch {
		case *presenter != "" && *presenter != p.User:
			return nil, s.wrongDevicePairCode(ctx, p)
		case *presenter == "" && (mapped == nil || mapped[p.User]):
			if mapped != nil {
				if log := s.log.Load(); log != nil {
					log.Warn("a pairing's presenter maps to no user", "setting", WebJWTUsersSetting)
				}
			}
			return nil, s.wrongDevicePairCode(ctx, p)
		}
	}
	if subtle.ConstantTimeCompare([]byte(clean), []byte(p.code)) != 1 {
		return nil, s.wrongDevicePairCode(ctx, p)
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
