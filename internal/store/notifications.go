package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	bolt "go.etcd.io/bbolt"
)

// A notification is the yard telling a user of an event one of the user's
// subscriptions matched: what a resident gets as a delivery in its inbox,
// written out for a person. It is no mail: nobody sent it, so there is
// nobody to answer, and it is read and acknowledged apart from the mail
// (User.NotificationCursor). Yardr makes its text from the event when the
// event is logged; no model is called and no resident is nudged.
//
// Unlike a delivery to a resident, which is the event itself found through
// the inbox index, a notification is a record of its own: the log is
// pruned without regard to what a person has read, and the text is to say
// what the bead was called when it happened. The event names whom it
// notified ("notified": user -> reason, as "deliveries" does for
// residents), which is what tells a view that follows the stream to read
// the notifications again.

var bNotifications = []byte("notifications") // <user>\x00<seq>: Notification

func init() { allBuckets = append(allBuckets, bNotifications) }

// MaxNotifications is how many notifications a user's inbox keeps: nobody
// acknowledges a stream, so the oldest go as new ones arrive, read or not.
const MaxNotifications = 500

// Notification text is one line of bounded length: an event's own words
// (a close reason, a comment in a source) may be long.
const (
	maxNotificationLine  = 300 // runes of the event's row
	maxNotificationTitle = 120 // runes of the bead's title after it
)

// Notification is one event told to a user.
type Notification struct {
	Seq uint64    `json:"seq"` // of the event it tells of
	At  time.Time `json:"at"`
	// Source is the subscription source it came through: yardr, or a
	// hook's. Kind is the event's kind (closed, advanced, hook, ...).
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"` // why it was delivered, as Reason gives for a resident
	// Bead is the handle and Title the title of the bead it is about, when
	// it is about one; Commit is the commit a closed event names.
	Bead   string `json:"bead,omitempty"`
	Title  string `json:"title,omitempty"`
	Commit string `json:"commit,omitempty"`
	URL    string `json:"url,omitempty"` // of a hook delivery
	// Text is the whole of it in one line: the row a resident's inbox
	// shows for the event, then the bead's title.
	Text string `json:"text"`
	// Read is whether the user acknowledged it (AckUserNotifications).
	Read bool `json:"read"`
}

// NotificationPage is the notifications part of a user's inbox.
type NotificationPage struct {
	// Notifications are oldest first: the unread from the oldest on, at
	// most the limit, and before them the newest read while the limit
	// leaves room.
	Notifications []Notification `json:"notifications"`
	Unread        int            `json:"unread"` // all unread, shown or not
	Last          uint64         `json:"last"`   // seq of the newest unread
}

// Notified is why event e became a notification for user ("" if it did
// not).
func Notified(e Event, user string) string {
	d, _ := e.Data["notified"].(map[string]any)
	reason, _ := d[user].(string)
	return reason
}

// notSaid are the keys of an event's data that say whom it reached, which
// its row leaves out.
var notSaid = []string{"deliveries", "watches_ended", "notified"}

// userSubscribers calls fn for every user that has subscriptions. A user
// is no member of a group, so they are the user's own.
func userSubscribers(tx *bolt.Tx, fn func(u *User, subs []boundSub) error) error {
	return each(tx, bUsers, "", func(_ []byte, u *User) error {
		if len(u.Subscriptions) == 0 {
			return nil
		}
		subs := make([]boundSub, 0, len(u.Subscriptions))
		for _, sub := range u.Subscriptions {
			subs = append(subs, boundSub{Subscription: sub})
		}
		return fn(u, subs)
	})
}

// maySubscribe refuses actor changing what the user name is told: that is
// the user's own to decide, or the operator's for them. A resident
// deciding what a person hears of is what notifications are there to end.
func (w *wtx) maySubscribe(name, actor string) error {
	operator, err := operatorIn(w.Tx)
	if err != nil {
		return err
	}
	if actor == name || (operator != "" && actor == operator) {
		return nil
	}
	who := "the operator"
	if operator != "" && operator != name {
		who = "the operator (" + operator + ")"
	}
	if w.Bucket(bResidents).Get([]byte(actor)) != nil {
		return fmt.Errorf("%s is a resident, and a resident does not decide what a person is told: %s subscribes themselves, or %s does it for them",
			actor, name, who)
	}
	return fmt.Errorf("%s does not decide what %s is told: %s subscribes themselves, or %s does it for them", or(actor, "nobody"), name, name, who)
}

// notify writes the notification n for each of users (user -> reason), and
// drops what is over the bound. n's text is its row with the place of the
// reason left open (reasonSlot).
func (w *wtx) notify(n Notification, users map[string]any) error {
	row := n.Text
	for _, name := range slices.Sorted(maps.Keys(users)) {
		n.Reason, _ = users[name].(string)
		n.Text = n.line(row)
		if err := put(w.Tx, bNotifications, join(name, seqKey(n.Seq)), n); err != nil {
			return err
		}
		ks := keys(w.Tx, bNotifications, name+"\x00")
		for _, k := range ks[:max(0, len(ks)-MaxNotifications)] {
			if err := w.Bucket(bNotifications).Delete([]byte(k)); err != nil {
				return err
			}
		}
	}
	return nil
}

// line is the notification's text made from its row: one line, cut to
// length, with the reason in its place and the bead's title after it.
func (n Notification) line(row string) string {
	out := clip(oneLine(strings.Replace(row, reasonSlot, n.Reason, 1)), maxNotificationLine)
	if n.Title != "" && !strings.Contains(out, "title="+n.Title) {
		out += fmt.Sprintf(" on %q", clip(oneLine(n.Title), maxNotificationTitle))
	}
	// After the cut, so that a long comment does not cost the link to it.
	if n.URL != "" {
		out += "  " + oneLine(n.URL)
	}
	return out
}

// reasonSlot stands in a notification's row for the delivery reason, which
// differs by user, until notify writes it.
const reasonSlot = "\x00"

func oneLine(s string) string {
	var plain strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
			plain.WriteByte(' ')
		} else if !unicode.IsControl(r) {
			plain.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(plain.String()), " ")
}

// clip cuts s to at most n runes, marking the cut.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// commitIn finds a commit in what a closed event carries: a commit of its
// own, else one its reason names (the refinery's "merged <sha>").
var commitIn = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)

// beadNotification is bead event e as a notification, its row in the words
// yardr inbox shows a resident: "<bead> <kind> (<reason>) by <actor>
// <key=value ...>". The title follows it unless the event says it already.
func beadNotification(e *Event, row *beadRow) Notification {
	n := Notification{Seq: e.Seq, At: e.At, Source: SourceYardr, Kind: e.Kind, Bead: row.ID, Title: row.Title}
	data := said(e.Data)
	n.Text = fmt.Sprintf("%s %s (%s) by %s %s", row.ID, e.Kind, reasonSlot, e.Actor, showMap(data))
	if e.Kind == "closed" {
		n.Commit, _ = data["commit"].(string)
		if reason, _ := data["reason"].(string); n.Commit == "" {
			n.Commit = commitIn.FindString(reason)
		}
	}
	return n
}

// hookNotification is the hook delivery data, logged as event seq at at,
// as a notification, its row in the words yardr inbox shows a resident:
// "hook (<reason>) <source> <type> <action> by <who> on "<title>": <text>
// <url>".
func hookNotification(seq uint64, at time.Time, data map[string]any) Notification {
	data = said(data)
	str := func(k string) string { s, _ := data[k].(string); return s }
	n := Notification{Seq: seq, At: at, Source: str("source"), Kind: "hook", URL: str("url")}
	actor, _ := data["actor"].(map[string]any)
	who, _ := actor["name"].(string)
	if who == "" {
		who, _ = actor["id"].(string)
	}
	what := strings.Join(slices.DeleteFunc([]string{str("source"), str("type"), str("action")},
		func(s string) bool { return s == "" }), " ")
	if who != "" {
		what += " by " + who
	}
	thread, _ := data["thread"].(map[string]any)
	if title, _ := thread["title"].(string); title != "" {
		what += " on " + fmt.Sprintf("%q", title)
	}
	n.Text = fmt.Sprintf("hook (%s) %s", reasonSlot, what)
	if t := str("text"); t != "" {
		n.Text += ": " + t
	}
	return n
}

// said is an event's data as a reader of the log decodes it (numbers as
// JSON has them, lists and maps untyped), without what says whom it
// reached: a row is then worded the same whether made here, as the event
// is logged, or from the log afterwards.
func said(data map[string]any) map[string]any {
	out := map[string]any{}
	raw, err := json.Marshal(data)
	if err != nil || json.Unmarshal(raw, &out) != nil {
		out = maps.Clone(data)
	}
	for _, k := range notSaid {
		delete(out, k)
	}
	return out
}

// showMap prints decoded JSON as "k=v ..." in key order, as yardr inbox
// prints an event's data.
func showMap(m map[string]any) string {
	parts := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, k+"="+show(m[k]))
	}
	return strings.Join(parts, " ")
}

func show(v any) string {
	switch x := v.(type) {
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case map[string]any:
		return "{" + showMap(x) + "}"
	case []any:
		parts := make([]string, len(x))
		for i, e := range x {
			parts[i] = show(e)
		}
		return "[" + strings.Join(parts, " ") + "]"
	}
	return fmt.Sprint(v)
}

// hookNotified decides which users a hook delivery reaches and why: user
// -> reason. A user has no identity in a source and takes part in no
// thread there, so only a subscription of scope all reaches them.
func hookNotified(tx *bolt.Tx, data map[string]any) (map[string]any, error) {
	source, _ := data["source"].(string)
	out := map[string]any{}
	err := userSubscribers(tx, func(u *User, subs []boundSub) error {
		for _, sub := range subs {
			if sub.Source == source && sub.Scope == ScopeAll && whereMatches(sub.Where, data) {
				out[u.Name] = ReasonAll
				break
			}
		}
		return nil
	})
	return out, err
}

// UserNotifications returns the notifications of the user name: all that
// is unread counted, and up to limit shown (0 shows all), the unread
// first. They are oldest first (see NotificationPage).
func (s *Store) UserNotifications(ctx context.Context, name string, limit int) (*NotificationPage, error) {
	page := &NotificationPage{Notifications: []Notification{}}
	err := s.read(func(tx *bolt.Tx) error {
		u, err := getUser(tx, name)
		if err != nil {
			return err
		}
		var all []Notification
		first := -1 // the oldest unread
		err = each(tx, bNotifications, name+"\x00", func(_ []byte, n *Notification) error {
			if n.Read = n.Seq <= u.NotificationCursor; !n.Read {
				if first < 0 {
					first = len(all)
				}
				page.Unread++
				page.Last = n.Seq
			}
			all = append(all, *n)
			return nil
		})
		if err != nil {
			return err
		}
		if first < 0 {
			first = len(all)
		}
		from, to := 0, len(all)
		if limit > 0 {
			to = min(to, first+limit)
			from = max(0, to-limit)
		}
		page.Notifications = append(page.Notifications, all[from:to]...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return page, nil
}

// AckUserNotifications marks the user's notifications read through seq.
// The mail is not touched: each part of a user's inbox has its own cursor.
// When it passes a notification it logs notifications_read with the reader
// and how far, as mail_read does for mail, so that a view following the
// stream counts again.
func (s *Store) AckUserNotifications(ctx context.Context, name string, through uint64) error {
	return s.write(func(w *wtx) error {
		u, err := getUser(w.Tx, name)
		if err != nil {
			return err
		}
		was := u.NotificationCursor
		if through <= was {
			return nil
		}
		u.NotificationCursor = through
		if err := put(w.Tx, bUsers, name, u); err != nil {
			return err
		}
		// Whether a notification lies in (was, through].
		p := name + "\x00"
		k, _ := w.Bucket(bNotifications).Cursor().Seek([]byte(p + seqKey(was+1)))
		if k == nil || !strings.HasPrefix(string(k), p) || binary.BigEndian.Uint64(k[len(p):]) > through {
			return nil
		}
		return w.event("", "notifications_read", name, map[string]any{"reader": name, "through": through})
	})
}

// dropNotifications removes every notification of the user name.
func dropNotifications(tx *bolt.Tx, name string) error {
	for _, k := range keys(tx, bNotifications, name+"\x00") {
		if err := tx.Bucket(bNotifications).Delete([]byte(k)); err != nil {
			return err
		}
	}
	return nil
}

// migrateUserCursors gives every user of a store from before notifications
// (schema 3) the second cursor: a user's inbox had one, for all of it, so
// what it had acknowledged is acknowledged in both parts.
func migrateUserCursors(tx *bolt.Tx, from uint64) error {
	if from >= 4 {
		return nil
	}
	var users []*User
	if err := each(tx, bUsers, "", func(_ []byte, u *User) error {
		users = append(users, u)
		return nil
	}); err != nil {
		return err
	}
	for _, u := range users {
		u.NotificationCursor = max(u.NotificationCursor, u.Cursor)
		if err := put(tx, bUsers, u.Name, u); err != nil {
			return err
		}
	}
	return nil
}
