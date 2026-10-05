package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A user is a person as a party of the yard: a name and an inbox, as a
// resident has. The operator (the name in the operator setting) is one of
// them. Mail to a user is a mail event like any other, marked to_user and
// indexed in the inbox bucket under the name, so the log's pruning and the
// rebuilt indexes treat it as they treat a resident's. What differs is the
// reading: nothing nudges a person, and nothing of theirs holds the log
// back.
//
// Residents and users share one space of names, since mail to a name must
// reach one party.

// OperatorSetting is the setting naming the yard's operator.
const OperatorSetting = "operator"

var bUsers = []byte("users")

func init() { allBuckets = append(allBuckets, bUsers) }

// User is a person with an inbox. The record is the place for what a
// person is elsewhere (an identity in an outside source, an origin in
// another yard) once there is such a thing; today it is the name and how
// far its mail is read.
type User struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	Cursor    uint64    `json:"cursor"` // mail acknowledged through this event seq
}

// Letter is one mail as a conversation and a user's inbox show it.
type Letter struct {
	Seq  uint64    `json:"seq"`
	At   time.Time `json:"at"`
	From string    `json:"from"`
	To   string    `json:"to"`
	Text string    `json:"text"`
	// Read is whether the receiver acknowledged it (yardr inbox --ack).
	// Mail to a party that is gone was never read as far as the store
	// knows.
	Read bool `json:"read"`
}

// Mailbox is a user's mail: what they have not acknowledged, and the
// latest of what they have.
type Mailbox struct {
	// Mail is the unread, oldest first, then the read, newest first; at
	// most the limit in all.
	Mail   []Letter `json:"mail"`
	Unread int      `json:"unread"` // all unread, shown or not
	Last   uint64   `json:"last"`   // seq of the newest unread
	// From is who the unread are from, in the order they first wrote.
	From []string `json:"from"`
}

// operatorIn is the operator setting, "" when unset.
func operatorIn(tx *bolt.Tx) (string, error) {
	var v string
	_, err := get(tx, bSettings, OperatorSetting, &v)
	return v, err
}

// addUser writes a new user who reads mail from now on: mail an earlier
// user of the name left is not theirs.
func (w *wtx) addUser(name, actor string) (*User, error) {
	u := &User{Name: name, CreatedAt: now(), CreatedBy: actor}
	if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
		u.Cursor = binary.BigEndian.Uint64(k)
	}
	if err := put(w.Tx, bUsers, name, u); err != nil {
		return nil, err
	}
	return u, w.event("", "user_added", actor, map[string]any{"user": name})
}

// ensureOperator gives the operator a user when there is none of the name.
// A resident of the name keeps it and no user is made: yardr doctor says
// which of the two to rename.
func (w *wtx) ensureOperator(actor string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil || operator == "" {
		return err
	}
	if w.Bucket(bUsers).Get([]byte(operator)) != nil || w.Bucket(bResidents).Get([]byte(operator)) != nil {
		return nil
	}
	_, err = w.addUser(operator, actor)
	return err
}

// migrateOperator is ensureOperator for a yard from before users, run when
// the store opens.
func migrateOperator(tx *bolt.Tx) error {
	return (&wtx{Tx: tx}).ensureOperator("yardr")
}

// ValidUserName reports whether a user may have the name: what a caller
// asks before it proposes one (a login name as the operator, say).
func ValidUserName(name string) bool { return checkUserName(name) == nil }

func checkUserName(name string) error {
	if !peerMailName.MatchString(name) {
		return fmt.Errorf("user %q: use letters, digits, '.', '_', '-' or '/' (starting with a letter or digit)", name)
	}
	return nil
}

// AddUser adds the user name. It refuses a name a resident or a user has.
func (s *Store) AddUser(ctx context.Context, name, actor string) (*User, error) {
	if err := checkUserName(name); err != nil {
		return nil, err
	}
	var out *User
	err := s.write(func(w *wtx) error {
		switch {
		case w.Bucket(bUsers).Get([]byte(name)) != nil:
			return fmt.Errorf("%w: there is a user %s already", ErrConflict, name)
		case w.Bucket(bResidents).Get([]byte(name)) != nil:
			return fmt.Errorf("user %s: that is a resident's name, and mail to a name reaches one party: choose another", name)
		}
		var err error
		out, err = w.addUser(name, actor)
		return err
	})
	return out, err
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	out := []User{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bUsers, "", func(_ []byte, u *User) error {
			out = append(out, *u)
			return nil
		})
	})
	return out, err
}

func getUser(tx *bolt.Tx, name string) (*User, error) {
	u := new(User)
	if ok, err := get(tx, bUsers, name, u); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("user %s: %w", name, ErrNotFound)
	}
	return u, nil
}

func (s *Store) GetUser(ctx context.Context, name string) (*User, error) {
	var u *User
	err := s.read(func(tx *bolt.Tx) (err error) {
		u, err = getUser(tx, name)
		return err
	})
	return u, err
}

// DeleteUser removes the user; the mail to and from them stays in the log.
// The operator's user is not removed: the setting names someone else
// first.
func (s *Store) DeleteUser(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		if _, err := getUser(w.Tx, name); err != nil {
			return err
		}
		if operator, err := operatorIn(w.Tx); err != nil {
			return err
		} else if operator == name {
			return fmt.Errorf("user %s is the yard's operator and is not removed; name another operator first (yardr settings set %s <name>)", name, OperatorSetting)
		}
		if err := w.Bucket(bUsers).Delete([]byte(name)); err != nil {
			return err
		}
		return w.event("", "user_removed", actor, map[string]any{"user": name})
	})
}

// toUser reports whether a mail event is for a user, not a resident.
func toUser(e Event) bool {
	u, _ := e.Data["to_user"].(bool)
	return e.Kind == "mail" && u
}

// mailReceiver is whose inbox a mail event of this yard is in, resident or
// user: "" for mail that left for a peer's resident.
func mailReceiver(e Event) string {
	if e.Kind != "mail" {
		return ""
	}
	if _, left := e.Data["to_peer"]; left {
		return ""
	}
	to, _ := e.Data["to"].(string)
	return to
}

// mailKeys are the event keys after seq in name's inbox index, in order.
func mailKeys(tx *bolt.Tx, name string, after uint64) []string {
	var out []string
	c := tx.Bucket(bInbox).Cursor()
	p := []byte(name + "\x00")
	for k, _ := c.Seek([]byte(join(name, seqKey(after+1)))); k != nil && bytes.HasPrefix(k, p); k, _ = c.Next() {
		out = append(out, string(k[len(p):]))
	}
	return out
}

// readers answers whether a mail's receiver acknowledged it, reading each
// resident or user once.
type readers struct {
	tx      *bolt.Tx
	cursors map[string]uint64
}

func (rd *readers) letter(e Event) (Letter, error) {
	to := mailReceiver(e)
	key, bucket := "r\x00"+to, bResidents
	if toUser(e) {
		key, bucket = "u\x00"+to, bUsers
	}
	cursor, ok := rd.cursors[key]
	if !ok {
		// Resident and User both keep it as "cursor".
		var c struct {
			Cursor uint64 `json:"cursor"`
		}
		if _, err := get(rd.tx, bucket, to, &c); err != nil {
			return Letter{}, err
		}
		cursor = c.Cursor
		if rd.cursors == nil {
			rd.cursors = map[string]uint64{}
		}
		rd.cursors[key] = cursor
	}
	text, _ := e.Data["text"].(string)
	return Letter{Seq: e.Seq, At: e.At, From: e.Actor, To: to, Text: text, Read: e.Seq <= cursor}, nil
}

// UserInbox returns the mail to the user name: all that is unread counted,
// and up to limit shown (0 shows all), the unread first.
func (s *Store) UserInbox(ctx context.Context, name string, limit int) (*Mailbox, error) {
	box := &Mailbox{Mail: []Letter{}, From: []string{}}
	err := s.read(func(tx *bolt.Tx) error {
		if _, err := getUser(tx, name); err != nil {
			return err
		}
		rd := &readers{tx: tx}
		var read []Letter
		for _, k := range mailKeys(tx, name, 0) {
			var e Event
			if ok, err := get(tx, bEvents, k, &e); err != nil {
				return err
			} else if !ok || !toUser(e) || mailReceiver(e) != name {
				continue // a resident's of the same name, before or since
			}
			l, err := rd.letter(e)
			if err != nil {
				return err
			}
			if l.Read {
				read = append(read, l)
				continue
			}
			box.Unread++
			box.Last = l.Seq
			if !slices.Contains(box.From, l.From) {
				box.From = append(box.From, l.From)
			}
			if limit <= 0 || len(box.Mail) < limit {
				box.Mail = append(box.Mail, l)
			}
		}
		for i := len(read) - 1; i >= 0 && (limit <= 0 || len(box.Mail) < limit); i-- {
			box.Mail = append(box.Mail, read[i])
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return box, nil
}

// AckUserInbox marks the user's mail read through seq.
func (s *Store) AckUserInbox(ctx context.Context, name string, through uint64) error {
	return s.write(func(w *wtx) error {
		u, err := getUser(w.Tx, name)
		if err != nil {
			return err
		}
		was := u.Cursor
		u.Cursor = max(u.Cursor, through)
		if err := put(w.Tx, bUsers, name, u); err != nil {
			return err
		}
		return w.mailRead(name, was, u.Cursor)
	})
}

// mailRead logs that name, a resident or a user, acknowledged its inbox
// from after was through seq, when mail is in between: the event mail_read
// with the reader and how far. It is what tells a view that follows the
// stream that mail it shows as sent has been read; an acknowledgement
// that passes no mail is not an event.
func (w *wtx) mailRead(name string, was, through uint64) error {
	read := false
	for _, k := range mailKeys(w.Tx, name, was) {
		if binary.BigEndian.Uint64([]byte(k)) > through {
			break
		}
		var e Event
		if ok, err := get(w.Tx, bEvents, k, &e); err != nil {
			return err
		} else if ok && mailReceiver(e) == name {
			read = true
			break
		}
	}
	if !read {
		return nil
	}
	return w.event("", "mail_read", name, map[string]any{"reader": name, "through": through})
}

// Conversation returns the mail between a and b within this yard, both
// directions, oldest first, with whether its receiver has read each; after
// leaves out what is not newer than that seq. Either may be a resident or
// a user. It reads the inbox index of the two and nothing else of the log,
// so asking again with the last seq seen costs what is new.
func (s *Store) Conversation(ctx context.Context, a, b string, after uint64) ([]Letter, error) {
	out := []Letter{}
	err := s.read(func(tx *bolt.Tx) error {
		keys := mailKeys(tx, a, after)
		if b != a {
			keys = append(keys, mailKeys(tx, b, after)...)
			slices.Sort(keys) // big-endian seqs sort as numbers
		}
		rd := &readers{tx: tx}
		for _, k := range keys {
			var e Event
			if ok, err := get(tx, bEvents, k, &e); err != nil {
				return err
			} else if !ok {
				continue
			}
			to := mailReceiver(e)
			if to == "" || !(e.Actor == a && to == b || e.Actor == b && to == a) {
				continue
			}
			l, err := rd.letter(e)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return nil
	})
	return out, err
}
