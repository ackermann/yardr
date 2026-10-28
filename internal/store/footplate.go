package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bFoot holds what is still to be told the users' phones through the
// relay: <user>\x00<device>\x00<event id>.
var bFoot = []byte("footplate")

func init() { allBuckets = append(allBuckets, bFoot) }

// What a footplate row is for: the two telling kinds the relay takes, and
// a removal, which the yard reports with every device and user removal.
const (
	FootMail     = "mail"
	FootDecision = "decision"
	FootRemove   = "remove"
)

const (
	// FootTries is how often an intent or removal is sent before it is
	// given up, and FootMaxAge how old one may be and still be sent. Both
	// are the notify queue's (PushTries, PushMaxAge): a push about this
	// morning's mail is noise by the afternoon, on either door.
	FootTries  = 8
	FootMaxAge = 6 * time.Hour
	// FootResendAfter is how long a send waits for the relay's ack before
	// the row is sent again: the relay answers every intent, so silence
	// this long is a lost frame or a dead link, and a retry keeps the
	// same event id, which the relay deduplicates.
	FootResendAfter = 30 * time.Second
	// FootRetryBase and FootRetryMax bound the wait after a send the
	// relay asks for again: doubling from the base to the max.
	FootRetryBase = 5 * time.Second
	FootRetryMax  = 5 * time.Minute
)

// FootItem is one intent or removal still to be reported to the relay over
// the yard's link: for each paired device of the user the news is for. It
// is written with the mail, the ask or the removal, in the same
// transaction, so nothing that lands there is missed, and it is gone once
// the relay acknowledged it, refused it for good, or it aged out. Until
// then it survives any restart.
//
// What is stored names only the user, the device, the kind and the
// event's random opaque id, with what finds the mail or ask again when it
// is sent: never a platform or a push token, which the yard never sees,
// and nothing of the mail or question itself.
type FootItem struct {
	// EventID is the event's random opaque id, made when the row is
	// written and kept across every retry, so the relay tells a retry
	// from a new event. It is unrelated to any mail, ask or target id.
	EventID string `json:"event_id"`
	User    string `json:"user"`
	Device  string `json:"device"`
	Kind    string `json:"kind"` // FootMail, FootDecision or FootRemove
	// At is when it became news: when it was queued, or the aside's
	// return for a delayed row. The age bound counts from here.
	At time.Time `json:"at"`
	// Due is when it may be sent: At for an immediate row, the aside's
	// return for a delayed one.
	Due time.Time `json:"due"`
	// Seq is the mail event's seq, for FootMail.
	Seq uint64 `json:"seq,omitempty"`
	// Where an ask stands, as a Push holds it: the bead's UUID and the
	// ask note's seq, or, for a question another yard asked, the key it
	// is kept under (bPeerAsks).
	AskBead string `json:"ask_bead,omitempty"`
	AskSeq  uint64 `json:"ask_seq,omitempty"`
	AskKey  string `json:"ask_key,omitempty"`
	// AsideSeq is the aside answer note's seq a delayed local row waits
	// on: it fires only while that note is still the ask's latest
	// answer. AsideMsg is the same for a locally deferred foreign ask:
	// the answer message's id. RemoteAside marks a delayed row for an
	// aside the asking yard reported (takeSettle), which waits on the
	// return it carried instead. All three are absent on an immediate row.
	AsideSeq    uint64 `json:"aside_seq,omitempty"`
	AsideMsg    string `json:"aside_msg,omitempty"`
	RemoteAside bool   `json:"remote_aside,omitempty"`
	// Attempts counts the sends that failed, RetryAt when the next is
	// due, and LastSent when it was last sent and is waiting for the
	// relay's ack. The relay's refusal text is not stored: it is remote
	// input and has no place in the yard's durable queue.
	Attempts int        `json:"attempts,omitempty"`
	RetryAt  *time.Time `json:"retry_at,omitempty"`
	LastSent *time.Time `json:"last_sent,omitempty"`

	// Mail or Ask is what it is about, as FootDue found it on the user's
	// board: whether the mail is still unread, or the ask still open.
	// Neither is set for one that is no longer there.
	Mail *Letter  `json:"mail,omitempty"`
	Ask  *OpenAsk `json:"ask,omitempty"`
}

// delayed reports whether f waits on an aside's return rather than news
// that already landed.
func (f *FootItem) delayed() bool {
	return f.AsideSeq != 0 || f.AsideMsg != "" || f.RemoteAside
}

// due reports whether f may be sent at the time at: past its due, past a
// retry it waits out, not waiting on a send the relay may still ack, and
// within its age.
func (f *FootItem) due(at time.Time) bool {
	return !at.Before(f.Due) &&
		(f.RetryAt == nil || !at.Before(*f.RetryAt)) &&
		(f.LastSent == nil || at.Sub(*f.LastSent) > FootResendAfter) &&
		at.Sub(f.At) <= FootMaxAge
}

// newFootID is a random opaque event id: 128 bits, hex, unrelated to any
// target the relay could read from it.
func newFootID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// queueFootFor writes one row per device of the user name, each with its
// own event id, filled by fill. A name that is no user of the yard, or
// has no device, queues nothing: a phone that is not paired is never told.
func (w *wtx) queueFootFor(name, kind string, fill func(*FootItem)) error {
	var u User
	if ok, err := get(w.Tx, bUsers, name, &u); err != nil || !ok {
		return err
	}
	at := now()
	for _, key := range keys(w.Tx, bDevices, name+"\x00") {
		var d deviceRecord
		if _, err := get(w.Tx, bDevices, key, &d); err != nil {
			return err
		}
		id, err := newFootID()
		if err != nil {
			return err
		}
		f := FootItem{EventID: id, User: name, Device: d.Name, Kind: kind, At: at, Due: at}
		fill(&f)
		if err := put(w.Tx, bFoot, join(name, d.Name, id), f); err != nil {
			return err
		}
	}
	return nil
}

// queueFootMail writes the intent for the mail event seq to the user name,
// one per device. It is called in the write that puts the mail in the
// inbox (wtx.event), beside queuePush, when the letter is from someone
// else: never the person's own.
func (w *wtx) queueFootMail(name string, seq uint64) error {
	return w.queueFootFor(name, FootMail, func(f *FootItem) { f.Seq = seq })
}

// queueFootAsk writes the intent for an ask for name that this write put
// in their queue: the ask seq on the bead uuid (AddAsk and the yard's own
// cards), or the foreign ask kept under key (deliverAsk). It is called
// beside queueAskPush, at the same places, and asks no notify setting:
// whether a phone is pushed to is whether it stands registered at the
// relay, never what source the user named.
func (w *wtx) queueFootAsk(name, uuid string, seq uint64, key string) error {
	return w.queueFootFor(name, FootDecision, func(f *FootItem) {
		f.AskBead, f.AskSeq, f.AskKey = uuid, seq, key
	})
}

// queueFootAside writes the delayed intent for a local ask the person put
// aside: it becomes news when the aside returns by its 24 hours, unless
// the asker wrote on the bead first, which queues its own intent
// (queueAsideReturns) and leaves this row to drop.
func (w *wtx) queueFootAside(name, uuid string, ask, aside uint64, due time.Time) error {
	return w.queueFootFor(name, FootDecision, func(f *FootItem) {
		f.At, f.Due = due, due
		f.AskBead, f.AskSeq, f.AsideSeq = uuid, ask, aside
	})
}

// queueFootForeignAside writes the delayed intent for a foreign ask
// deferred here: by this yard's clock the aside returns 24 hours after
// the answer message went home. asideMsg is that message's id, which the
// row fires only while it is still the ask's latest answer.
func (w *wtx) queueFootForeignAside(name, key, asideMsg string, due time.Time) error {
	return w.queueFootFor(name, FootDecision, func(f *FootItem) {
		f.At, f.Due = due, due
		f.AskKey, f.AsideMsg = key, asideMsg
	})
}

// queueFootRemoteAside writes the delayed intent for a foreign ask the
// asking yard put aside there: it becomes news at the return the settle
// carried, if the ask is open then.
func (w *wtx) queueFootRemoteAside(name, key string, until time.Time) error {
	return w.queueFootFor(name, FootDecision, func(f *FootItem) {
		f.At, f.Due = until, until
		f.AskKey, f.RemoteAside = key, true
	})
}

// queueFootNoteAside writes the delayed intent for the ask the aside note
// answers: found among notes, which were read before the answer was
// written, by the ask note's seq.
func (w *wtx) queueFootNoteAside(row *beadRow, notes []Note, askSeq uint64, aside *Note) error {
	for _, n := range notes {
		if n.Seq != askSeq || n.Kind != NoteAsk {
			continue
		}
		ask, err := ParseAsk(n)
		if err != nil {
			return nil
		}
		if err := dropFootAsk(w.Tx, ask.To, row.UUID, askSeq, ""); err != nil {
			return err
		}
		return w.queueFootAside(ask.To, row.UUID, askSeq, aside.Seq, aside.At.Add(LaterFor))
	}
	return nil
}

// queueAsideReturns writes the intent for every put-aside ask on the
// bead's row that the note n reopens: the first note of its asker after
// the aside, written before the aside's 24 hours are over. A note written
// past them reopens nothing, since the aside already came back by its
// time, and its telling is the delayed row's. Later notes queue nothing:
// the ask opened once.
func (w *wtx) queueAsideReturns(row *beadRow, n Note) error {
	notes, err := beadNotes(w.Tx, row.UUID)
	if err != nil {
		return err
	}
	for _, a := range notes {
		if a.Kind != NoteAsk || a.Author != n.Author || a.Seq >= n.Seq {
			continue
		}
		ask, err := ParseAsk(a)
		if err != nil {
			continue
		}
		var aside *Note
		var asideAns Answer
		for i, m := range notes {
			if m.Kind != NoteAnswer || m.Seq >= n.Seq {
				continue
			}
			ans, err := ParseAnswer(m)
			if err != nil || ans.Ask != a.Seq {
				continue
			}
			if aside == nil || m.Seq > aside.Seq {
				aside, asideAns = &notes[i], ans
			}
		}
		// As ResolveAsks reads the latest answer: taken back and
		// declined end the ask, and only a plain aside waits.
		if aside == nil || asideAns.TakeBack || asideAns.Decline || !asideAns.Later {
			continue
		}
		// Reopened after it was answered, declined or taken back: the
		// phone counts those as decided, not news.
		decided := false
		for _, m := range notes {
			if m.Kind != NoteAnswer || m.Seq >= aside.Seq {
				continue
			}
			ans, err := ParseAnswer(m)
			if err != nil || ans.Ask != a.Seq {
				continue
			}
			if ans.TakeBack || ans.Decline || !ans.Later {
				decided = true
				break
			}
		}
		if decided {
			continue
		}
		if !n.At.Before(aside.At.Add(LaterFor)) {
			continue
		}
		first := true
		for _, m := range notes {
			if m.Author == n.Author && m.Seq > aside.Seq && m.Seq < n.Seq {
				first = false
				break
			}
		}
		if !first {
			continue
		}
		if err := w.queueFootAsk(ask.To, row.UUID, a.Seq, ""); err != nil {
			return err
		}
	}
	return nil
}

// queueFootRemove writes the removal for the device name of the user: one
// row, replacing any removal still queued for it, so a device removed,
// re-added and removed again reports once, for the latest removal. The
// row carries an event id as an intent does, so the relay's ack matches
// it; the frame names only the user and the device beside it.
func (w *wtx) queueFootRemove(user, name string) error {
	prefix := join(user, name)
	for _, key := range keys(w.Tx, bFoot, prefix+"\x00") {
		var f FootItem
		if _, err := get(w.Tx, bFoot, key, &f); err != nil {
			return err
		}
		if f.Kind == FootRemove {
			if err := w.Bucket(bFoot).Delete([]byte(key)); err != nil {
				return err
			}
		}
	}
	id, err := newFootID()
	if err != nil {
		return err
	}
	at := now()
	return put(w.Tx, bFoot, join(prefix, id), FootItem{EventID: id, User: user, Device: name, Kind: FootRemove, At: at, Due: at})
}

// dropFootAsk forgets what is still queued for the ask the user put aside:
// the ask seq on the bead uuid, or the foreign ask kept under key. The
// immediate rows are stale now, and the delayed rows of an earlier aside
// can never fire once a newer answer stands. Left behind, the immediate
// rows would be news again when the ask reopens, doubling the return's
// telling; the return queues its own rows.
func dropFootAsk(tx *bolt.Tx, user, uuid string, seq uint64, key string) error {
	for _, k := range keys(tx, bFoot, user+"\x00") {
		var f FootItem
		if _, err := get(tx, bFoot, k, &f); err != nil {
			return err
		}
		if f.Kind != FootDecision {
			continue
		}
		match := (key != "" && f.AskKey == key) ||
			(key == "" && f.AskKey == "" && f.AskBead == uuid && f.AskSeq == seq)
		if match {
			if err := tx.Bucket(bFoot).Delete([]byte(k)); err != nil {
				return err
			}
		}
	}
	return nil
}

// dropFootForget removes every row for the device name of the user: its
// intents tell of nothing once the device is gone, and a queued removal
// goes with them where the caller queues a fresh one after.
func dropFootForget(tx *bolt.Tx, user, name string) error {
	for _, key := range keys(tx, bFoot, join(user, name)+"\x00") {
		if err := tx.Bucket(bFoot).Delete([]byte(key)); err != nil {
			return err
		}
	}
	return nil
}

// dropFootUser forgets what was still to be told the phones of the user
// name, who is removed: their removals are queued after, for the devices
// the removal drops.
func dropFootUser(tx *bolt.Tx, name string) error {
	for _, key := range keys(tx, bFoot, name+"\x00") {
		if err := tx.Bucket(bFoot).Delete([]byte(key)); err != nil {
			return err
		}
	}
	return nil
}

// dropFootRemove forgets a removal still queued for the device name of the
// user, who is added again: sent after the fresh registration it would
// delete the row the phone just wrote at the relay.
func dropFootRemove(tx *bolt.Tx, user, name string) error {
	for _, key := range keys(tx, bFoot, join(user, name)+"\x00") {
		var f FootItem
		if _, err := get(tx, bFoot, key, &f); err != nil {
			return err
		}
		if f.Kind == FootRemove {
			if err := tx.Bucket(bFoot).Delete([]byte(key)); err != nil {
				return err
			}
		}
	}
	return nil
}

// fresh resolves what f is about on the user's board and reports whether
// it is still news: a removal always is, a letter still unread is, and an
// ask is while it is open. A delayed row fires only for the return it
// waits on: the ask open, its aside still the latest answer, no note of
// the asker before its due, whose own intent told that return instead,
// and no answer, decline or take-back before the aside, after which the
// phone counts a return as decided, not news.
func (f *FootItem) fresh(tx *bolt.Tx, rd *readers, at time.Time) (bool, error) {
	switch f.Kind {
	case FootRemove:
		return true, nil
	case FootMail:
		var e Event
		if ok, err := get(tx, bEvents, seqKey(f.Seq), &e); err != nil || !ok {
			return false, err
		} else if !toUser(e) || MailReceiver(e) != f.User {
			return false, nil
		}
		l, err := rd.letter(e)
		if err != nil {
			return false, err
		}
		f.Mail = &l
		return !l.Read, nil
	case FootDecision:
		return f.askFresh(tx, at)
	}
	return false, nil
}

// askFresh is fresh for an ask: an immediate row while the ask is open, a
// delayed row for the return it waits on.
func (f *FootItem) askFresh(tx *bolt.Tx, at time.Time) (bool, error) {
	if !f.delayed() {
		ask, err := (&Push{User: f.User, AskBead: f.AskBead, AskSeq: f.AskSeq, AskKey: f.AskKey}).openAsk(tx, at)
		if err != nil {
			return false, err
		}
		f.Ask = ask
		return ask != nil, nil
	}
	if f.AskKey != "" {
		return f.foreignAsideFresh(tx, at)
	}
	notes, err := beadNotes(tx, f.AskBead)
	if err != nil {
		return false, err
	}
	for _, r := range ResolveAsks(notes, at) {
		if r.Note.Seq != f.AskSeq || r.AskErr != nil || r.Ask.To != f.User {
			continue
		}
		if r.State != AskOpen || len(r.Answers) == 0 || r.Answers[len(r.Answers)-1].Note.Seq != f.AsideSeq {
			return false, nil
		}
		// Reopened after it was answered, declined or taken back: the
		// phone counts those as decided, not news. An aside before the
		// aside is no decision: it came back too.
		for _, a := range r.Answers[:len(r.Answers)-1] {
			if a.Answer.TakeBack || a.Answer.Decline || !a.Answer.Later {
				return false, nil
			}
		}
		for _, n := range notes {
			if n.Author == r.Note.Author && n.Seq > f.AsideSeq && n.At.Before(f.Due) {
				return false, nil
			}
		}
		var b beadRow
		if ok, err := get(tx, bBeads, f.AskBead, &b); err != nil || !ok {
			return false, err
		}
		f.Ask = &OpenAsk{Bead: r.Note.Bead, BeadTitle: b.Title, Topic: topicOf(b.Bead), Ask: r.Note.Seq, Question: r.Ask.Question, Asker: r.Note.Author, Since: r.Note.At}
		return true, nil
	}
	return false, nil
}

// foreignAsideFresh is askFresh for a delayed foreign row: the ask open,
// and for one deferred here, the deferral still its latest answer.
func (f *FootItem) foreignAsideFresh(tx *bolt.Tx, at time.Time) (bool, error) {
	var rec foreignAsk
	if ok, err := get(tx, bPeerAsks, f.AskKey, &rec); err != nil || !ok {
		return false, err
	}
	ask, err := (&Push{User: f.User, AskKey: f.AskKey}).openAsk(tx, at)
	if err != nil || ask == nil {
		return false, err
	}
	if f.AsideMsg != "" {
		if len(rec.Answers) == 0 || rec.Answers[len(rec.Answers)-1].Message != f.AsideMsg {
			return false, nil
		}
		for _, a := range rec.Answers[:len(rec.Answers)-1] {
			if a.Answer.TakeBack || a.Answer.Decline || !a.Answer.Later {
				return false, nil
			}
		}
	}
	f.Ask = ask
	return true, nil
}

// FootDue lists what is due to be reported to the relay, oldest first,
// each with the mail or ask it is about as the user's board has it: past
// its due, past a retry it waits out, not waiting on a send the relay may
// still ack, within its age, and still news. It is the server's to ask,
// and no method of the API.
func (s *Store) FootDue(ctx context.Context) ([]FootItem, error) {
	out := []FootItem{}
	at := now()
	err := s.read(func(tx *bolt.Tx) error {
		rd := &readers{tx: tx}
		return each(tx, bFoot, "", func(_ []byte, f *FootItem) error {
			f.Mail, f.Ask = nil, nil
			if !f.due(at) {
				return nil
			}
			fresh, err := f.fresh(tx, rd, at)
			if err != nil {
				return err
			}
			if !fresh {
				return nil
			}
			out = append(out, *f)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(out, func(a, b FootItem) int {
		if a.At.Equal(b.At) {
			return strings.Compare(a.EventID, b.EventID)
		}
		if a.At.Before(b.At) {
			return -1
		}
		return 1
	})
	return out, nil
}

// FootSweep drops what is past telling: what aged out, and what is no
// longer news — a letter read, an ask dealt with, a return told by
// another row. A delayed row is judged by the board at its due: what can
// never fire — decided before the aside, a newer answer, the asker's
// note before its due, an end — drops now, and what waits its return is
// kept. It answers how many went each way, and is the server's to ask,
// and no method of the API.
func (s *Store) FootSweep(ctx context.Context) (expired, stale int, err error) {
	at := now()
	err = s.write(func(w *wtx) error {
		rd := &readers{tx: w.Tx}
		var gone []string
		err := each(w.Tx, bFoot, "", func(k []byte, f *FootItem) error {
			if at.Sub(f.At) > FootMaxAge {
				expired++
				gone = append(gone, string(k))
				return nil
			}
			when := at
			if f.delayed() && f.Due.After(when) {
				when = f.Due
			}
			fresh, err := f.fresh(w.Tx, rd, when)
			if err != nil {
				return err
			}
			if !fresh {
				stale++
				gone = append(gone, string(k))
			}
			return nil
		})
		for _, k := range gone {
			if err == nil {
				err = w.Bucket(bFoot).Delete([]byte(k))
			}
		}
		return err
	})
	return expired, stale, err
}

// footByID is the key of the row with the event id, "" when none stands.
func footByID(tx *bolt.Tx, id string) (string, error) {
	found := ""
	err := each(tx, bFoot, "", func(k []byte, f *FootItem) error {
		if f.EventID == id {
			found = string(k)
		}
		return nil
	})
	return found, err
}

// FootLinkReturned forgets every row's last send: the link is a new one,
// which never saw them, so what waits for an ack goes again with the
// same ids. A retry a row waits out still waits. It is the server's to
// ask, and no method of the API.
func (s *Store) FootLinkReturned(ctx context.Context) error {
	return s.write(func(w *wtx) error {
		var sent []string
		if err := each(w.Tx, bFoot, "", func(k []byte, f *FootItem) error {
			if f.LastSent != nil {
				sent = append(sent, string(k))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range sent {
			var f FootItem
			if _, err := get(w.Tx, bFoot, k, &f); err != nil {
				return err
			}
			f.LastSent = nil
			if err := put(w.Tx, bFoot, k, f); err != nil {
				return err
			}
		}
		return nil
	})
}

// FootSent records that the row with the event id was sent and waits for
// the relay's ack. False when no row stands for it: acked and gone
// already. It is the server's to ask, and no method of the API.
func (s *Store) FootSent(ctx context.Context, id string) (bool, error) {
	found := false
	err := s.write(func(w *wtx) error {
		key, err := footByID(w.Tx, id)
		if err != nil || key == "" {
			return err
		}
		var f FootItem
		if _, err := get(w.Tx, bFoot, key, &f); err != nil {
			return err
		}
		at := now()
		f.LastSent = &at
		found = true
		return put(w.Tx, bFoot, key, f)
	})
	return found, err
}

// FootLanded ends the row with the event id: the relay took it, or refused
// it for good and will never take it. False when no row stands for it. It
// is the server's to ask, and no method of the API.
func (s *Store) FootLanded(ctx context.Context, id string) (bool, error) {
	found := false
	err := s.write(func(w *wtx) error {
		key, err := footByID(w.Tx, id)
		if err != nil || key == "" {
			return err
		}
		found = true
		return w.Bucket(bFoot).Delete([]byte(key))
	})
	return found, err
}

// FootRetry records that the relay asked for another try, after a wait
// that doubles from FootRetryBase up to FootRetryMax. The wait for its
// ack ends with the refusal: the retry waits out RetryAt alone. Past
// FootTries tries the row goes instead, and kept is false; so it is when
// no row stands for it. The refusal text stays out of the store. This is
// the server's to ask, and no method of the API.
func (s *Store) FootRetry(ctx context.Context, id string) (kept bool, err error) {
	err = s.write(func(w *wtx) error {
		key, err := footByID(w.Tx, id)
		if err != nil || key == "" {
			return err
		}
		var f FootItem
		if _, err := get(w.Tx, bFoot, key, &f); err != nil {
			return err
		}
		if f.Attempts+1 >= FootTries {
			return w.Bucket(bFoot).Delete([]byte(key))
		}
		n := f.Attempts + 1
		retry := now().Add(min(FootRetryBase<<min(n-1, 30), FootRetryMax))
		f.Attempts, f.RetryAt, f.LastSent = n, &retry, nil
		kept = true
		return put(w.Tx, bFoot, key, f)
	})
	return kept, err
}
