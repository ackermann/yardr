package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bPushes holds what is still to be told outside the yard: <user>\x00<seq>.
var bPushes = []byte("pushes")

func init() { allBuckets = append(allBuckets, bPushes) }

// What a push is for: the parts of a user's inbox, and an ask for the
// user, which is in no inbox but in their queue (UserAsks).
const (
	PushMail         = "mail"
	PushNotification = "notification"
	PushAsk          = "ask"
)

const (
	// PushGrace is how long what landed in a user's inbox waits before it
	// is told outside the yard. What the user acknowledged meanwhile (a
	// conversation open in yardr chat, yardr inbox --ack <seq>) is not told, and
	// neither is an ask they answered, declined or put aside meanwhile.
	PushGrace = 20 * time.Second
	// PushesPerHour is the most messages one user is sent in an hour. What
	// is over it waits in the queue, and goes as one message when the hour
	// has room again.
	PushesPerHour = 20
)

// MaxNotificationText is the most runes a notification's text has before
// the URL of a hook delivery: the event's row, then ` on "<title>"`
// (Notification.line). A title with a quote in it is a few runes longer
// written out, so whoever sizes by this cuts to it.
const MaxNotificationText = maxNotificationLine + len(` on ""`) + maxNotificationTitle

// Push is something in a user's inbox, or an ask in their queue, that is
// still to be told outside the yard, through the source the user names
// (User.Notify). It is written with the mail, the notification or the ask,
// in the same transaction, so nothing that lands there is missed and
// nothing is told twice, and the inbox never waits for a source: the call
// is made afterwards by the daemon. The record is gone once PushTried has
// said how the send ended, or DropPushes that there was nothing to send.
// Until then it survives any restart.
//
// What is stored is the part and the event's seq, where an ask stands, and
// how the tries went. The mail, notification or ask itself is read when the
// pushes are listed.
type Push struct {
	User string `json:"user"`
	Seq  uint64 `json:"seq"`  // of the mail event, of the event the notification tells of, or of the ask's noted event
	Part string `json:"part"` // PushMail, PushNotification or PushAsk
	// Where an ask stands: the bead's UUID and the ask note's seq, or, for
	// a question another yard asked, the key it is kept under (bPeerAsks).
	AskBead string `json:"ask_bead,omitempty"`
	AskSeq  uint64 `json:"ask_seq,omitempty"`
	AskKey  string `json:"ask_key,omitempty"`
	// At is when it landed in the inbox.
	At time.Time `json:"at"`
	// Attempts counts the sends that failed, Error is what the last one
	// came to, and RetryAt when the next is due.
	Attempts int        `json:"attempts,omitempty"`
	Error    string     `json:"error,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`

	// Mail or Note is what it is about, as Pushes found it in the inbox,
	// with whether the user has acknowledged it. Neither is set for one
	// that is no longer there (the log was pruned, the notifications went
	// over their bound): there is nothing left to tell of.
	Mail *Letter       `json:"mail,omitempty"`
	Note *Notification `json:"note,omitempty"`
	// Ask is the ask it is about, as the user's queue has it (UserAsks),
	// while it is open. It is not set for one that is answered, declined,
	// put aside or ended in the yard that asked, nor for one that is gone.
	Ask *OpenAsk `json:"ask,omitempty"`
}

// Unread reports whether p is still in the user's inbox and not
// acknowledged, or an ask that is still open: what is told outside the
// yard.
func (p *Push) Unread() bool {
	return (p.Mail != nil && !p.Mail.Read) || (p.Note != nil && !p.Note.Read) || p.Ask != nil
}

// How a send ended, as PushResult.Reason and the push_failed event name
// it. A send the source took has none.
const (
	PushRetry    = "trying_again" // it failed and is tried again: no event
	PushNotSetUp = "not_set_up"   // the user, their env file or the source is not set up to tell them
	PushRefused  = "refused"      // the source answered that it will not take it
	PushGaveUp   = "gave_up"      // it failed as often as it is tried
	PushExpired  = "expired"      // it waited longer than a push is worth
)

// PushResult is how a send to a user ended: what the user's record keeps of
// the last one (User.LastPush) for yardr user show and yardr doctor. It
// never holds the address, a credential, or anything of what was sent.
type PushResult struct {
	At     time.Time `json:"at"`
	Source string    `json:"source,omitempty"`
	// Reason is "" for a message the source took, else one of the Push*
	// reasons.
	Reason string `json:"reason,omitempty"`
	// Status is what the source answered, 0 when it did not.
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	// Count is how many items of the inbox the message was for, and Tries
	// how many sends it took or had failed.
	Count int `json:"count,omitempty"`
	Tries int `json:"tries,omitempty"`
	// RetryAt is when it is tried again (PushRetry).
	RetryAt *time.Time `json:"retry_at,omitempty"`
}

// OK reports whether the source took the message.
func (r *PushResult) OK() bool { return r.Reason == "" }

// queuePush writes the push for what the event seq put in the inbox of the
// user name, when a source tells that user outside the yard. It is called
// in the write that fills the inbox, at its two places: a mail event to a
// user (wtx.event) and a notification (wtx.notify).
func (w *wtx) queuePush(name, part string, seq uint64) error {
	return w.queue(Push{User: name, Seq: seq, Part: part})
}

// queueAskPush writes the push for an ask for name that this write put in
// their queue, when name is a user a source tells outside the yard: the
// ask seq on the bead uuid (AddAsk), or the foreign ask kept under key
// (deliverAsk). So the yard that puts an ask in a person's queue is the one
// that tells it, whether the question is its own or another yard's.
//
// It is called right after the ask's noted event was logged, and takes
// that event's seq. Where a subscription of the user's made a notification
// of the same event, its push has the same key and this one replaces it:
// the question is told once, as an ask, and for as long as it is open.
func (w *wtx) queueAskPush(name, uuid string, seq uint64, key string) error {
	return w.queue(Push{User: name, Seq: w.Bucket(bEvents).Sequence(), Part: PushAsk, AskBead: uuid, AskSeq: seq, AskKey: key})
}

// queue writes p when a source tells its user outside the yard, and of
// that part (User.Tells).
func (w *wtx) queue(p Push) error {
	var u User
	if ok, err := get(w.Tx, bUsers, p.User, &u); err != nil || !ok || u.Notify == "" || !u.Tells(p.Part) {
		return err
	}
	p.At = now()
	return put(w.Tx, bPushes, join(p.User, seqKey(p.Seq)), p)
}

// openAsk is the ask p is about as a row of its user's queue, nil when it
// is not open any more or not there: read as UserAsks reads it, so what is
// told is what the queue shows.
func (p *Push) openAsk(tx *bolt.Tx, at time.Time) (*OpenAsk, error) {
	if p.AskKey != "" {
		var rec foreignAsk
		if ok, err := get(tx, bPeerAsks, p.AskKey, &rec); err != nil || !ok || rec.To != p.User || rec.Outcome != "" {
			return nil, err
		}
		peer, err := peerByKey(tx, rec.Key)
		if err != nil || peer == nil {
			return nil, err
		}
		row := rec.row(peer)
		if rec.resolved(row.Bead, row.Asker, at).State != AskOpen {
			return nil, nil
		}
		return &row, nil
	}
	var b beadRow
	if ok, err := get(tx, bBeads, p.AskBead, &b); err != nil || !ok {
		return nil, err
	}
	notes, err := beadNotes(tx, p.AskBead)
	if err != nil {
		return nil, err
	}
	for _, r := range ResolveAsks(notes, at) {
		if r.Note.Seq == p.AskSeq && r.AskErr == nil && r.Ask.To == p.User && r.State == AskOpen {
			return &OpenAsk{
				Bead: b.ID, BeadTitle: b.Title, Topic: topicOf(b.Bead),
				Ask: r.Note.Seq, Question: r.Ask.Question,
				Asker: r.Note.Author, Since: r.Note.At,
			}, nil
		}
	}
	return nil, nil
}

// dropPushes forgets what was still to be told the user name.
func dropPushes(tx *bolt.Tx, name string) error {
	for _, k := range keys(tx, bPushes, name+"\x00") {
		if err := tx.Bucket(bPushes).Delete([]byte(k)); err != nil {
			return err
		}
	}
	return nil
}

// dropUntold forgets what was still to be told the user u of a part they
// are not told of (User.Tells).
func dropUntold(tx *bolt.Tx, u *User) error {
	var gone [][]byte
	err := each(tx, bPushes, u.Name+"\x00", func(k []byte, p *Push) error {
		if !u.Tells(p.Part) {
			gone = append(gone, slices.Clone(k))
		}
		return nil
	})
	for _, k := range gone {
		if err == nil {
			err = tx.Bucket(bPushes).Delete(k)
		}
	}
	return err
}

// Pushes lists what is still to be told outside the yard, by user and then
// oldest first, each with the mail or notification it is about and whether
// the user has acknowledged it, or the ask it is about while that is open.
func (s *Store) Pushes(ctx context.Context) ([]Push, error) {
	out := []Push{}
	at := now()
	err := s.read(func(tx *bolt.Tx) error {
		users := map[string]*User{}
		rd := &readers{tx: tx}
		return each(tx, bPushes, "", func(_ []byte, p *Push) error {
			p.Mail, p.Note, p.Ask = nil, nil, nil
			u, ok := users[p.User]
			if !ok {
				u = new(User)
				if found, err := get(tx, bUsers, p.User, u); err != nil {
					return err
				} else if !found {
					u = nil
				}
				users[p.User] = u
			}
			if u == nil {
				return nil // DeleteUser took its pushes along: none is left behind
			}
			switch p.Part {
			case PushMail:
				var e Event
				if found, err := get(tx, bEvents, seqKey(p.Seq), &e); err != nil {
					return err
				} else if found && toUser(e) && MailReceiver(e) == p.User {
					l, err := rd.letter(e)
					if err != nil {
						return err
					}
					p.Mail = &l
				}
			case PushNotification:
				var n Notification
				if found, err := get(tx, bNotifications, join(p.User, seqKey(p.Seq)), &n); err != nil {
					return err
				} else if found {
					n.Read = n.Seq <= u.NotificationCursor
					p.Note = &n
				}
			case PushAsk:
				ask, err := p.openAsk(tx, at)
				if err != nil {
					return err
				}
				p.Ask = ask
			}
			out = append(out, *p)
			return nil
		})
	})
	return out, err
}

// changePushes reads the user and those of their pushes seqs names that are
// still there for fn, and writes the user back. keep is whether the pushes
// stay, as fn changed them, or go.
func (s *Store) changePushes(name string, seqs []uint64, keep bool, fn func(w *wtx, u *User, ps []*Push) error) error {
	return s.write(func(w *wtx) error {
		u, err := getUser(w.Tx, name)
		if err != nil {
			return err
		}
		var ps []*Push
		for _, seq := range seqs {
			p := new(Push)
			if ok, err := get(w.Tx, bPushes, join(name, seqKey(seq)), p); err != nil {
				return err
			} else if ok {
				ps = append(ps, p)
			}
		}
		if err := fn(w, u, ps); err != nil {
			return err
		}
		for _, p := range ps {
			key := join(name, seqKey(p.Seq))
			if keep {
				err = put(w.Tx, bPushes, key, p)
			} else {
				err = w.Bucket(bPushes).Delete([]byte(key))
			}
			if err != nil {
				return err
			}
		}
		return put(w.Tx, bUsers, name, u)
	})
}

// DropPushes ends the user's pushes seqs without a word: the user
// acknowledged what they were about before it was told, or dealt with the
// ask, or it is no longer in the inbox.
func (s *Store) DropPushes(ctx context.Context, name string, seqs []uint64) error {
	return s.changePushes(name, seqs, false, func(*wtx, *User, []*Push) error { return nil })
}

// PushTried records how a send for the user's pushes seqs ended, and keeps
// it as the last result on the user's record.
//
// A message the source took (res.OK) ends them, and its time counts
// towards PushesPerHour. No event says so: the inbox has what was told, and
// the record when. PushRetry leaves them for another try at res.RetryAt,
// as IssueMoveFailed does, also without an event. Any other reason ends
// them and logs push_failed, about no bead: the user, the source, the
// reason, the status and the last error, the seqs it was for and the tries.
// Never the address, a credential, or the title and text. One cause that
// stands (PushNotSetUp with the same words as the last result) is logged
// once, not with every mail that meets it.
func (s *Store) PushTried(ctx context.Context, name string, seqs []uint64, res PushResult) error {
	if !slices.Contains([]string{"", PushRetry, PushNotSetUp, PushRefused, PushGaveUp, PushExpired}, res.Reason) {
		return fmt.Errorf("push to %s: %q is no way a send ends", name, res.Reason)
	}
	if res.Reason == PushRetry && res.RetryAt == nil {
		return fmt.Errorf("push to %s: when it is tried again is required", name)
	}
	res.At = res.At.UTC()
	return s.changePushes(name, seqs, res.Reason == PushRetry, func(w *wtx, u *User, ps []*Push) error {
		if len(ps) == 0 {
			return nil // dropped meanwhile: the user is told nowhere now
		}
		was := u.LastPush
		res.Count = len(ps)
		u.LastPush = &res
		switch res.Reason {
		case "":
			u.Pushed = append(u.Pushed, res.At)
			u.Pushed = u.Pushed[max(0, len(u.Pushed)-PushesPerHour):]
			return nil
		case PushRetry:
			at := res.RetryAt.UTC()
			res.RetryAt = &at
			for _, p := range ps {
				p.Attempts, p.Error, p.RetryAt = p.Attempts+1, res.Error, &at
			}
			return nil
		case PushNotSetUp:
			if was != nil && was.Reason == PushNotSetUp && was.Error == res.Error {
				return nil
			}
		}
		data := map[string]any{"user": name, "reason": res.Reason, "tries": res.Tries}
		told := make([]uint64, len(ps))
		for i, p := range ps {
			told[i] = p.Seq
		}
		data["seqs"] = told
		if res.Source != "" {
			data["source"] = res.Source
		}
		if res.Status != 0 {
			data["status"] = res.Status
		}
		if res.Error != "" {
			data["error"] = res.Error
		}
		return w.event("", "push_failed", "yard", data)
	})
}

// Said is the notification's text without the URL of a hook delivery,
// which line puts after it: what is told of it where no link is sent.
func (n Notification) Said() string {
	if n.URL == "" {
		return n.Text
	}
	return strings.TrimSuffix(n.Text, "  "+oneLine(n.URL))
}
