package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A device is one program of one user on one machine or phone that calls
// the yard from outside it: a window that is not on the yard's machine. It
// is let in by a token the yard made, and every call it makes is made as its
// user. The store has the device's name, whose it is, when it was added and
// when and from where it was last used, and the SHA-256 of the token: never
// the token, which is shown once, when the device is added.
//
// Whoever adds a device decides who may speak as a person, so adding one is
// the operator's and is refused from an agent's pane (AddDevice). A token
// does not end by itself: it is refused once its device is removed.

var (
	bDevices = []byte("devices") // user NUL device -> deviceRecord
	// bDeviceTokens finds a device by what its token hashes to, so that
	// resolving one, right or wrong, costs a hash and a lookup.
	bDeviceTokens = []byte("device_tokens") // hex SHA-256 -> deviceRef
)

func init() { allBuckets = append(allBuckets, bDevices, bDeviceTokens) }

// DeviceTokenPrefix is how a device token begins: what tells one from any
// other credential before anything is looked up.
const DeviceTokenPrefix = "yardr-dev-"

// DeviceUnusedAfter is how long a device goes unused before yardr doctor
// notes it. Nothing ends then: a token is refused only once its device is
// removed.
const DeviceUnusedAfter = 30 * 24 * time.Hour

// deviceUseEvery is how often a device's last use is written while it
// calls from one address: a use is a write of the store, and a window
// that calls every few seconds is not one each time.
const deviceUseEvery = time.Minute

// Device is a device of a user, as it is listed.
type Device struct {
	User      string    `json:"user"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	// LastUsed is when its token was last resolved, nil for never, and
	// LastFrom the address that call came from, as whoever resolved it
	// gave it.
	LastUsed *time.Time `json:"last_used,omitempty"`
	LastFrom string     `json:"last_from,omitempty"`
}

// Unused reports whether the device has not been used for DeviceUnusedAfter
// at the time at: since its last use, or since it was added when it never
// was.
func (d Device) Unused(at time.Time) bool {
	last := d.CreatedAt
	if d.LastUsed != nil {
		last = *d.LastUsed
	}
	return at.Sub(last) >= DeviceUnusedAfter
}

// NewDevice is a device as AddDevice answers it: the one time its token is
// anywhere but on the device.
type NewDevice struct {
	Device
	Token string `json:"token"`
}

// deviceRecord is a device as the store keeps it.
type deviceRecord struct {
	Device
	TokenSHA256 string `json:"token_sha256"` // hex
}

type deviceRef struct {
	User string `json:"user"`
	Name string `json:"name"`
}

// tokenHash is what the store keeps of a device token.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// agentPane says whose pane a call came from when that is an agent's: the
// session of a bead's agent, or a crew member; "" for a call from neither.
// It goes by the store's own records of the process that called, not by the
// actor the call names: what must hold against a caller that names someone
// else asks this first (mayTell, AddDevice).
func agentPane(ctx context.Context, tx *bolt.Tx) (string, error) {
	sess, err := paneSession(tx, callerOf(ctx))
	if err != nil {
		return "", err
	}
	if sess != nil && !sess.Manual() {
		return "the session " + sess.ID + " of a bead's agent", nil
	}
	r, err := paneCrew(tx, callerOf(ctx))
	if err != nil || r == nil {
		return "", err
	}
	return "the crew member " + r.Name, nil
}

func checkDeviceName(name string) error {
	// The characters a user's name has: the name is printed in tables and
	// events, and comes back as part of a command (yardr user device rm).
	if !peerMailName.MatchString(name) {
		return fmt.Errorf("device %q: use letters, digits, '.', '_', '-' or '/' (starting with a letter or digit)", name)
	}
	return nil
}

// AddDevice adds the device name of the user and answers it with its token,
// 32 random bytes that are in no later answer: the store keeps their
// SHA-256. A second device of the name for the same user is refused. Like
// adding a user it is the operator's (operatorOnly), and it is refused from
// the pane of an agent whatever actor the call names, as a change of where
// a user is told is (mayTell): a device speaks as its user.
func (s *Store) AddDevice(ctx context.Context, user, name, actor string) (*NewDevice, error) {
	if err := checkDeviceName(name); err != nil {
		return nil, err
	}
	// Made before the write, which may run twice (write).
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	out := &NewDevice{Token: DeviceTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)}
	hash := tokenHash(out.Token)
	err := s.write(func(w *wtx) error {
		if who, err := agentPane(ctx, w.Tx); err != nil {
			return err
		} else if who != "" {
			return fmt.Errorf("a device is added by the operator, outside an agent's pane: whoever adds one decides who may speak as %s; this is %s", user, who)
		}
		if err := w.operatorOnly(ctx, actor, "add a device"); err != nil {
			return err
		}
		if _, err := getUser(w.Tx, user); err != nil {
			return err
		}
		key := join(user, name)
		if w.Bucket(bDevices).Get([]byte(key)) != nil {
			return fmt.Errorf("%w: %s has a device %s already; remove it first (yardr user device rm %s %s) or choose another name", ErrConflict, user, name, user, name)
		}
		out.Device = Device{User: user, Name: name, CreatedAt: now(), CreatedBy: actor}
		if err := put(w.Tx, bDevices, key, deviceRecord{Device: out.Device, TokenSHA256: hash}); err != nil {
			return err
		}
		if err := put(w.Tx, bDeviceTokens, hash, deviceRef{User: user, Name: name}); err != nil {
			return err
		}
		return w.event("", "device_added", actor, map[string]any{"user": user, "device": name})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Devices lists the devices of the user, or of every user for "", by user
// and then by name.
func (s *Store) Devices(ctx context.Context, user string) ([]Device, error) {
	out := []Device{}
	err := s.read(func(tx *bolt.Tx) error {
		prefix := ""
		if user != "" {
			if _, err := getUser(tx, user); err != nil {
				return err
			}
			prefix = user + "\x00"
		}
		return each(tx, bDevices, prefix, func(_ []byte, d *deviceRecord) error {
			out = append(out, d.Device)
			return nil
		})
	})
	return out, err
}

// RemoveDevice removes the device name of the user: its token is refused
// from then on. The operator removes one, and so does the user whose it is:
// taking a way in away lets nobody speak as anyone.
func (s *Store) RemoveDevice(ctx context.Context, user, name, actor string) (*Device, error) {
	var out *Device
	err := s.write(func(w *wtx) error {
		if actor != user {
			operator, this, err := notOperator(ctx, w.Tx, actor)
			if err != nil {
				return err
			}
			if this != "" {
				return fmt.Errorf("only %s or %s may remove a device of %s; this is %s", user, operator, user, this)
			}
		}
		key := join(user, name)
		var d deviceRecord
		if ok, err := get(w.Tx, bDevices, key, &d); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("device %s of %s: %w", name, user, ErrNotFound)
		}
		if err := dropDevice(w.Tx, key, &d); err != nil {
			return err
		}
		out = &d.Device
		return w.event("", "device_removed", actor, map[string]any{"user": user, "device": name})
	})
	return out, err
}

func dropDevice(tx *bolt.Tx, key string, d *deviceRecord) error {
	if err := tx.Bucket(bDeviceTokens).Delete([]byte(d.TokenSHA256)); err != nil {
		return err
	}
	return tx.Bucket(bDevices).Delete([]byte(key))
}

// dropDevices removes the devices of the user name, who is removed: whoever
// has the name next has none, and no token speaks as them. data, the event
// of the removal, names them.
func dropDevices(tx *bolt.Tx, name string, data map[string]any) error {
	var names []string
	for _, key := range keys(tx, bDevices, name+"\x00") {
		var d deviceRecord
		if _, err := get(tx, bDevices, key, &d); err != nil {
			return err
		}
		if err := dropDevice(tx, key, &d); err != nil {
			return err
		}
		names = append(names, d.Name)
	}
	if len(names) > 0 {
		data["devices"] = names
	}
	return nil
}

// ResolveDevice answers the device a token is of, and so the user a call
// that carries it is made as; nil for a token of no device, whatever is
// wrong with it. from is the address the call came from, as the caller
// knows it; with the time it is kept as the device's last use, written at
// most once in deviceUseEvery while the address stays the same.
//
// It is no method of the API: the server resolves a token for a request it
// was sent, and nothing on the socket has one to ask about.
func (s *Store) ResolveDevice(ctx context.Context, token, from string) (*Device, error) {
	if !strings.HasPrefix(token, DeviceTokenPrefix) {
		return nil, nil
	}
	hash := tokenHash(token)
	from = truncateFrom(from)
	var d *deviceRecord
	find := func(tx *bolt.Tx) error {
		d = nil
		var ref deviceRef
		if ok, err := get(tx, bDeviceTokens, hash, &ref); err != nil || !ok {
			return err
		}
		rec := new(deviceRecord)
		if ok, err := get(tx, bDevices, join(ref.User, ref.Name), rec); err != nil || !ok {
			return err
		}
		// The lookup found the record by the hash; the record says whether
		// it is this token's, compared in constant time.
		if subtle.ConstantTimeCompare([]byte(rec.TokenSHA256), []byte(hash)) != 1 {
			return nil
		}
		d = rec
		return nil
	}
	if err := s.read(find); err != nil || d == nil {
		return nil, err
	}
	at := now()
	if d.LastUsed != nil && d.LastFrom == from && at.Sub(*d.LastUsed) < deviceUseEvery {
		return &d.Device, nil
	}
	err := s.write(func(w *wtx) error {
		// Read again: the device may have been removed since.
		if err := find(w.Tx); err != nil || d == nil {
			return err
		}
		d.LastUsed, d.LastFrom = &at, from
		return put(w.Tx, bDevices, join(d.User, d.Name), d)
	})
	if err != nil || d == nil {
		return nil, err
	}
	return &d.Device, nil
}

// truncateFrom bounds an address that is kept: it is text from outside.
func truncateFrom(from string) string {
	const most = 64
	from = strings.ToValidUTF8(strings.TrimSpace(from), "")
	if len(from) > most {
		from = strings.ToValidUTF8(from[:most], "")
	}
	return from
}
