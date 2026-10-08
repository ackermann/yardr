package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A user is a person as a party of the yard: a name and an inbox, as a
// crew member has. The operator (the name in the operator setting) is one of
// them. Mail to a user is a mail event like any other, marked to_user and
// indexed in the inbox bucket under the name, so the log's pruning and the
// rebuilt indexes treat it as they treat a crew member's. What differs is the
// reading: nothing nudges a person, and nothing of theirs holds the log
// back.
//
// Crew members and users share one space of names, since mail to a name must
// reach one party.

// OperatorSetting is the setting naming the yard's operator.
const OperatorSetting = "operator"

var bUsers = []byte("users")

func init() { allBuckets = append(allBuckets, bUsers) }

// User is a person with an inbox. The record is the place for what a
// person is elsewhere (an identity in an outside source, an origin in
// another yard) once there is such a thing; today it is the name, what
// the user subscribes to, how far each part of its inbox is read, and
// which source tells them outside the yard.
type User struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	Cursor    uint64    `json:"cursor"` // mail acknowledged through this event seq
	// NotificationCursor is the same for the notifications: the two parts
	// of the inbox are acknowledged apart.
	NotificationCursor uint64 `json:"notification_cursor"`
	// Subscriptions make matching events notifications in the inbox (see
	// Notification). The user and the operator change them.
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
	// Notify is the source that tells the user outside the yard, a declared
	// one with a notify key or an adapter that can; "" for none. EnvFile is
	// the path of the file that holds the user's own address in it (and
	// whatever else of theirs the source needs), read at each send under
	// the rule of every env_file (config.ReadEnvFile). The store has the
	// path and never what is in the file. See SetUser.
	Notify  string `json:"notify,omitempty"`
	EnvFile string `json:"env_file,omitempty"`
	// LastPush is how the last send through that source ended, and Pushed
	// when the last messages it took were sent, the newest PushesPerHour of
	// them: what the bound on an hour's messages is counted from. See Push.
	LastPush *PushResult `json:"last_push,omitempty"`
	Pushed   []time.Time `json:"pushed,omitempty"`
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
	// Timer is the id of the timer the yard sent it for, "" for mail its
	// sender wrote: the text then begins "timer:".
	Timer string `json:"timer,omitempty"`
	// Delivery is set on mail to a crew member of another yard, whose reading
	// of it this yard never learns of: MailQueued while it waits here for
	// the peer, MailSent once the peer has it, MailFailed when it was
	// given up.
	Delivery string `json:"delivery,omitempty"`
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

// notOperator says whether actor acts as anyone but the operator: this is
// then how a refusal names them, and operator how it names the operator.
// It is the one place the rule is read, inside the write it guards, so
// every client of the store is held to it. The actor is the caller's word
// (YARDR_ACTOR, --actor): every agent runs as one OS user, so this is a
// line against mistakes, not a wall, and moves when yardr hosts more than
// one person. What must hold against a caller that names someone else asks
// the store's own records first (maySource, mayTell). A yard that names no
// operator has nobody to hold it to, and takes the change from anyone who
// is no agent of its own, as maySource reads it: no crew member, and no
// call from the pane of a bead's session or of a crew member.
func notOperator(ctx context.Context, tx *bolt.Tx, actor string) (operator, this string, err error) {
	name, err := operatorIn(tx)
	switch {
	case err != nil:
		return "", "", err
	case name != "" && actor == name:
		return "", "", nil
	case name != "":
		return "the operator (" + name + ")", or(actor, "nobody"), nil
	case tx.Bucket(bCrew).Get([]byte(actor)) != nil:
		return "the operator", "the crew member " + actor, nil
	}
	if sess, err := paneSession(tx, callerOf(ctx)); err != nil {
		return "", "", err
	} else if sess != nil && !sess.Manual() {
		return "the operator", "the session " + sess.ID + " of a bead's agent", nil
	}
	if r, err := paneCrew(tx, callerOf(ctx)); err != nil {
		return "", "", err
	} else if r != nil {
		return "the operator", "the crew member " + r.Name, nil
	}
	return "", "", nil
}

// operatorOnly refuses what only the operator may do (does, as in "add a
// peer") when actor is anyone else (notOperator), in the same words
// whatever is asked and whoever asks.
func operatorOnly(ctx context.Context, tx *bolt.Tx, actor, does string) error {
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return fmt.Errorf("only %s may %s; this is %s", operator, does, this)
}

func (w *wtx) operatorOnly(ctx context.Context, actor, does string) error {
	return operatorOnly(ctx, w.Tx, actor, does)
}

// operatorOnly is the rule for a change that is no write of the database
// (a pairing, which lives in the server's memory).
func (s *Store) operatorOnly(ctx context.Context, actor, does string) error {
	return s.read(func(tx *bolt.Tx) error { return operatorOnly(ctx, tx, actor, does) })
}

// maySpeakAs refuses to speak as the crew member name to anyone but the
// crew member itself and the operator: a bead's agent, or another crew
// member, would otherwise speak in its name, with its credential or the
// operator's. The crew member is known by the actor its session names, or
// by the pane yardr started it in (paneCrew) when a command there names
// another. Like operatorOnly it is a line, not a wall.
func maySpeakAs(ctx context.Context, tx *bolt.Tx, name, actor string) error {
	if actor == name {
		return nil
	}
	if r, err := paneCrew(tx, callerOf(ctx)); err != nil {
		return err
	} else if r != nil && r.Name == name {
		return nil
	}
	operator, this, err := notOperator(ctx, tx, actor)
	if err != nil || this == "" {
		return err
	}
	return fmt.Errorf("only %s's own session or %s may speak as %s; this is %s", name, operator, name, this)
}

// addUser writes a new user who reads mail from now on: mail an earlier
// user of the name left is not theirs, nor are its notifications.
func (w *wtx) addUser(name, actor string) (*User, error) {
	u := &User{Name: name, CreatedAt: now(), CreatedBy: actor}
	if k, _ := w.Bucket(bEvents).Cursor().Last(); k != nil {
		u.Cursor = binary.BigEndian.Uint64(k)
		u.NotificationCursor = u.Cursor
	}
	if err := dropNotifications(w.Tx, name); err != nil {
		return nil, err
	}
	if err := put(w.Tx, bUsers, name, u); err != nil {
		return nil, err
	}
	return u, w.event("", "user_added", actor, map[string]any{"user": name})
}

// ensureOperator gives the operator a user when there is none of the name.
// A crew member of the name keeps it and no user is made: yardr doctor says
// which of the two to rename.
func (w *wtx) ensureOperator(actor string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil || operator == "" {
		return err
	}
	if w.Bucket(bUsers).Get([]byte(operator)) != nil || w.Bucket(bCrew).Get([]byte(operator)) != nil {
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

// AddUser adds the user name. It refuses a name a crew member or a user has.
// Who is a party of the yard is the operator's to say (operatorOnly): a
// crew member's session adding one would make itself a second voice.
func (s *Store) AddUser(ctx context.Context, name, actor string) (*User, error) {
	if err := checkUserName(name); err != nil {
		return nil, err
	}
	var out *User
	err := s.write(func(w *wtx) error {
		if err := w.operatorOnly(ctx, actor, "add a user"); err != nil {
			return err
		}
		switch {
		case w.Bucket(bUsers).Get([]byte(name)) != nil:
			return fmt.Errorf("%w: there is a user %s already", ErrConflict, name)
		case w.Bucket(bCrew).Get([]byte(name)) != nil:
			return fmt.Errorf("user %s: that is a crew member's name, and mail to a name reaches one party: choose another", name)
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

// DeleteUser removes the user, with their subscriptions and notifications
// and what was still to be told them outside the yard; the mail to and from
// them stays in the log.
// The operator's user is not removed: the setting names someone else
// first. Like adding one, it is the operator's to do (operatorOnly).
func (s *Store) DeleteUser(ctx context.Context, name, actor string) error {
	return s.write(func(w *wtx) error {
		if err := w.operatorOnly(ctx, actor, "remove a user"); err != nil {
			return err
		}
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
		if err := dropNotifications(w.Tx, name); err != nil {
			return err
		}
		if err := dropPushes(w.Tx, name); err != nil {
			return err
		}
		data := map[string]any{"user": name}
		if err := unexpose(w.Tx, name, data); err != nil {
			return err
		}
		return w.event("", "user_removed", actor, data)
	})
}

// toUser reports whether a mail event is for a user, not a crew member.
func toUser(e Event) bool {
	u, _ := e.Data["to_user"].(bool)
	return e.Kind == "mail" && u
}

// mailReceiver is whose inbox a mail event of this yard is in, crew member or
// user: "" for mail that left for a peer's crew member.
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
// crew member or user once.
type readers struct {
	tx      *bolt.Tx
	cursors map[string]uint64
}

func (rd *readers) letter(e Event) (Letter, error) {
	to := mailReceiver(e)
	key, bucket := "r\x00"+to, bCrew
	if toUser(e) {
		key, bucket = "u\x00"+to, bUsers
	}
	cursor, ok := rd.cursors[key]
	if !ok {
		// CrewMember and User both keep it as "cursor".
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
	return Letter{Seq: e.Seq, At: e.At, From: e.Actor, To: to, Text: text, Read: e.Seq <= cursor, Timer: timerOf(e)}, nil
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
		var evs []Event
		for _, k := range mailKeys(tx, name, 0) {
			var e Event
			if ok, err := get(tx, bEvents, k, &e); err != nil {
				return err
			} else if ok && toUser(e) && mailReceiver(e) == name {
				evs = append(evs, e)
			}
		}
		// A linked yard may have been renamed since it wrote one of these.
		// Give the user the same current sender address as crew inboxes do,
		// so chat can match the mail to the linked yard's row.
		if err := peersToday(tx, evs); err != nil {
			return err
		}
		var read []Letter
		for _, e := range evs {
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

// mailRead logs that name, a crew member or a user, acknowledged its inbox
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
// leaves out what is not newer than that seq. Either may be a crew member or
// a user. It reads the inbox index of the two and nothing else of the log,
// so asking again with the last seq seen costs what is new.
//
// b may also be <crew member>@<peer>, a crew member of a linked yard: see
// peerConversation.
func (s *Store) Conversation(ctx context.Context, a, b string, after uint64) ([]Letter, error) {
	out := []Letter{}
	err := s.read(func(tx *bolt.Tx) (err error) {
		if strings.Contains(b, "@") {
			out, err = peerConversation(tx, a, b, after)
			return err
		}
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
