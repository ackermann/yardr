// Held deliveries are verified deliveries from an unmapped sender
// under a hook's unknown word, kept on the sender's mapping card
// until the operator answers it (claim.go). They live in their own
// bucket keyed by the card bead's UUID, never in the bead's notes,
// body, events or ordinary bead view. The person-only read for the
// card is IdentityHeldPayloads; every other bead and agent read sees
// only how many wait. Answering delivers them as hook events in arrival
// order (deliverHeld) or deletes them, and closing the card by any other
// way deletes what still waits (closeBead).
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

var bHeld = []byte("held_deliveries")        // <card bead UUID>: []heldDelivery, oldest first
var bHeldSeen = []byte("held_delivery_seen") // dedupe key: card bead UUID while waiting
var bHeldBytes = []byte("held_source_bytes") // source name: payload bytes held on its cards

func init() { allBuckets = append(allBuckets, bHeld, bHeldSeen, bHeldBytes) }

// MaxHeldPerCard bounds how many deliveries one mapping card holds:
// a later delivery while that many wait is dropped, so a stranger
// cannot fill the store from one card.
const MaxHeldPerCard = 5

// MaxHeldBytesPerSource bounds the payload bytes held on one source's
// mapping cards together: a delivery that would cross it is not held,
// so a sender rotating identities cannot fill the store through one
// source while another source's strangers are still held. It is a var
// so a test can run the bound at a size it can fill.
var MaxHeldBytesPerSource int64 = 20 << 20

// HeldDelivery is one held delivery as the receiver kept it: the hook
// it reached, the normalized envelope before any speaker was stamped,
// the raw payload as RecordHook would keep it, and the dedupe keys
// that name it. The envelope is routing's shape (hooks.Envelope.Map),
// held as data so the store needs no hook type.
type HeldDelivery struct {
	Hook     string         `json:"hook"`
	Envelope map[string]any `json:"envelope"`
	Payload  []byte         `json:"payload,omitempty"`
	Dedupe   []string       `json:"dedupe,omitempty"`
	// Source is the source whose stranger sent it, so the bytes held
	// for that source are counted as each card's list is written
	// (putHeld) rather than summed over every card at each delivery.
	Source string `json:"source,omitempty"`
	// At is when the delivery was held, so Prune can age it with the
	// hook payloads (pruneHeld). Zero on a delivery held before At was
	// kept, which any retention then ages out.
	At time.Time `json:"at,omitempty"`
}

// HeldFile is one file a held delivery names, as its envelope names
// it (hooks.FileRef): what the source calls it, with no bytes. Held
// as data so the store needs no hook type, as HeldDelivery.Envelope
// is.
type HeldFile struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// HeldFilesFetch fetches the files of the hook delivery seq a mapping
// card's answer released, by its hook: the delivery was held, not
// kept, when it arrived, so its files are fetched only now that the
// answer kept it, as a kept delivery's are after its answer
// (hooks.Receiver.fetchFiles, which the registered fetch runs).
type HeldFilesFetch func(hook string, seq uint64, files []HeldFile)

// heldFetchBox holds the registered HeldFilesFetch so a write can
// load it atomically while the owning process registers it: a func
// cannot be an atomic.Pointer's type argument itself.
type heldFetchBox struct{ fetch HeldFilesFetch }

// SetHeldFilesFetch tells the store how its owning process fetches a
// released held delivery's files, as SetPackReader tells it how that
// process reads the packs it embeds; nil where no receiver owns the
// store, and a release then keeps its file refs alone, as a delivery
// whose fetch a restart cut short does. The server registers it from
// serveHooks, after its carry and daemon watchers have started
// writing, so it is stored atomically where the other registrations,
// which all happen before those goroutines start, are plain.
func (s *Store) SetHeldFilesFetch(f HeldFilesFetch) {
	if f == nil {
		s.heldFetch.Store(nil)
		return
	}
	s.heldFetch.Store(&heldFetchBox{fetch: f})
}

// heldFileFetch is one delivery a write released that carries files:
// its hook and the event seq the release logged it as.
type heldFileFetch struct {
	hook  string
	seq   uint64
	files []HeldFile
}

// heldFilesOf are the files envelope names, as HeldFiles; none where
// it names none, or names them in no shape a delivery's files come in.
func heldFilesOf(envelope map[string]any) []HeldFile {
	raw, ok := envelope["files"]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var files []HeldFile
	if json.Unmarshal(b, &files) != nil {
		return nil
	}
	return files
}

// heldList is the deliveries waiting on the card bead uuid, oldest
// first; nil where none wait.
func heldList(tx *bolt.Tx, uuid string) ([]HeldDelivery, error) {
	var out []HeldDelivery
	if _, err := get(tx, bHeld, uuid, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// IdentityHeldPayloads reads the raw bodies waiting on one open identity
// card, only for the person it asks. Bead and UserAsks leave the bodies out,
// because agents and the whole queue read those before the person picks.
func (s *Store) IdentityHeldPayloads(ctx context.Context, bead string, askSeq uint64, actor string) ([]string, error) {
	var out []string
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, bead)
		if err != nil {
			return err
		}
		notes, err := beadNotes(tx, row.UUID)
		if err != nil {
			return err
		}
		var target string
		for _, resolved := range ResolveAsks(notes, now()) {
			if resolved.Note.Seq == askSeq && resolved.AskErr == nil && resolved.Ask.Identity != nil && resolved.State == AskOpen {
				target = resolved.Ask.To
				break
			}
		}
		if target == "" {
			return fmt.Errorf("identity ask %d on %s: %w", askSeq, bead, ErrNotFound)
		}
		reader, err := s.mayReadInbox(ctx, tx, target, actor, "read")
		if err != nil {
			return err
		}
		if reader != target {
			return fmt.Errorf("only %s may read held messages on this card", target)
		}
		held, err := heldList(tx, row.UUID)
		if err != nil {
			return err
		}
		out = make([]string, 0, len(held))
		for _, delivery := range held {
			out = append(out, string(delivery.Payload))
		}
		return nil
	})
	return out, err
}

// putHeld stores the deliveries waiting on uuid, dropping the record
// where none wait, and moves each source's held bytes by what the
// card's list gained or lost.
func putHeld(tx *bolt.Tx, uuid string, held []HeldDelivery) error {
	old, err := heldList(tx, uuid)
	if err != nil {
		return err
	}
	delta := map[string]int64{}
	for _, h := range old {
		delta[h.Source] -= int64(len(h.Payload))
	}
	for _, h := range held {
		delta[h.Source] += int64(len(h.Payload))
	}
	for source, d := range delta {
		if d == 0 {
			continue
		}
		used, err := heldBytesForSource(tx, source)
		if err != nil {
			return err
		}
		if used += d; used <= 0 {
			err = tx.Bucket(bHeldBytes).Delete([]byte(source))
		} else {
			err = put(tx, bHeldBytes, source, used)
		}
		if err != nil {
			return err
		}
	}
	seen := tx.Bucket(bHeldSeen)
	for _, h := range old {
		for _, key := range h.Dedupe {
			if string(seen.Get([]byte(key))) == uuid {
				if err := seen.Delete([]byte(key)); err != nil {
					return err
				}
			}
		}
	}
	if len(held) == 0 {
		return tx.Bucket(bHeld).Delete([]byte(uuid))
	}
	for _, h := range held {
		for _, key := range h.Dedupe {
			if owner := seen.Get([]byte(key)); owner != nil && string(owner) != uuid {
				return fmt.Errorf("held delivery dedupe key already waits on card %s", owner)
			}
		}
	}
	if err := put(tx, bHeld, uuid, held); err != nil {
		return err
	}
	for _, h := range held {
		for _, key := range h.Dedupe {
			if err := seen.Put([]byte(key), []byte(uuid)); err != nil {
				return err
			}
		}
	}
	return nil
}

// heldDuplicate reports whether any key already names a kept or waiting
// delivery. A retry must not consume another place on a card's five-message
// limit, and a delivery released as outside must not be held again.
func heldDuplicate(tx *bolt.Tx, dedupe []string) bool {
	kept, waiting := tx.Bucket(bHookSeen), tx.Bucket(bHeldSeen)
	for _, key := range dedupe {
		if key != "" && (kept.Get([]byte(key)) != nil || waiting.Get([]byte(key)) != nil) {
			return true
		}
	}
	return false
}

// heldCount is how many deliveries wait on the card bead uuid.
func heldCount(tx *bolt.Tx, uuid string) (int, error) {
	held, err := heldList(tx, uuid)
	if err != nil {
		return 0, err
	}
	return len(held), nil
}

// syncHeldCount writes how many deliveries wait on uuid into the
// mapping ask that stands on its bead, so every view of the card
// (queue, bead, printed lines) says the same number. The ask note is
// rewritten in place: no new note, no event, and no held body in it.
func (w *wtx) syncHeldCount(uuid string) error {
	count, err := heldCount(w.Tx, uuid)
	if err != nil {
		return err
	}
	notes, err := beadNotes(w.Tx, uuid)
	if err != nil {
		return err
	}
	for _, n := range notes {
		if n.Kind != NoteAsk {
			continue
		}
		ask, err := ParseAsk(n)
		if err != nil || ask.Identity == nil || ask.Identity.Held == count {
			continue
		}
		ask.Identity.Held = count
		text, err := json.Marshal(ask)
		if err != nil {
			return err
		}
		if err := put(w.Tx, bNotes, join(uuid, seqKey(n.Seq)), Note{
			Bead: uuid, Kind: n.Kind, Author: n.Author, Text: string(text),
			At: n.At, Seq: n.Seq, Via: n.Via, Notify: n.Notify,
			Told: n.Told, Acked: n.Acked, Untold: n.Untold, Files: n.Files, Session: n.Session,
		}); err != nil {
			return err
		}
	}
	return nil
}

// deliverHeld turns the deliveries waiting on uuid into hook events
// as if they had arrived now, stamped with speaker and routed as any
// kept delivery is (delivered, the write RecordHook shares), in
// arrival order, and clears what waited. A delivery whose dedupe key
// an earlier release already recorded is skipped, as a redelivery of
// a kept one is. The card's count is synchronized after release.
func (w *wtx) deliverHeld(uuid, speaker string) error {
	held, err := heldList(w.Tx, uuid)
	if err != nil {
		return err
	}
	if len(held) == 0 {
		return w.syncHeldCount(uuid)
	}
	if err := putHeld(w.Tx, uuid, nil); err != nil {
		return err
	}
	seen := w.Bucket(bHookSeen)
	for _, h := range held {
		duplicate := false
		for _, k := range h.Dedupe {
			if seen.Get([]byte(k)) != nil {
				duplicate = true
			}
		}
		if duplicate {
			continue
		}
		data := map[string]any{"hook": h.Hook}
		for k, v := range h.Envelope {
			data[k] = v
		}
		stampHeldSpeaker(data, speaker)
		seq, err := w.delivered("hook:"+h.Hook, data)
		if err != nil {
			return err
		}
		at := now()
		for _, k := range h.Dedupe {
			if err := seen.Put([]byte(k), seenValue(seq, at)); err != nil {
				return err
			}
		}
		var hook Hook
		if ok, err := get(w.Tx, bHooks, h.Hook, &hook); err != nil {
			return err
		} else if ok {
			hook.Accepted = &at
			if err := put(w.Tx, bHooks, h.Hook, hook); err != nil {
				return err
			}
		}
		if err := w.Bucket(bHookPayloads).Put([]byte(seqKey(seq)), h.Payload); err != nil {
			return err
		}
		// Its files are fetched after this write, as a kept delivery's
		// are after its answer: they were the stranger's until now, so
		// nothing of them was fetched while the delivery waited.
		if files := heldFilesOf(h.Envelope); len(files) > 0 {
			w.heldFiles = append(w.heldFiles, heldFileFetch{hook: h.Hook, seq: seq, files: files})
		}
	}
	return w.syncHeldCount(uuid)
}

// stampHeldSpeaker records speaker on the held envelope's fields, as
// the receiver stamps a kept delivery (hooks.stampSpeaker): a fresh
// map where the envelope carried none, and its other fields kept.
func stampHeldSpeaker(data map[string]any, speaker string) {
	fields := map[string]any{}
	switch f := data["fields"].(type) {
	case map[string]any:
		fields = f
	case map[string][]string:
		for k, v := range f {
			fields[k] = v
		}
	}
	fields[SpeakerKey] = []string{speaker}
	data["fields"] = fields
}

// dropHeld deletes what waits on uuid without delivering it: the
// answer was nobody or a decline, or the card closed another way.
func (w *wtx) dropHeld(uuid string) error {
	if err := putHeld(w.Tx, uuid, nil); err != nil {
		return err
	}
	return w.syncHeldCount(uuid)
}

// heldBytesForSource is the payload bytes waiting on source's mapping
// cards together, the sum MaxHeldBytesPerSource bounds. It is read from
// the count putHeld keeps, not summed over the cards: every delivery
// from a stranger asks it, and a sum over every held card in the one
// writer would let a sender rotating identities make each later
// delivery cost a read of everything held.
func heldBytesForSource(tx *bolt.Tx, source string) (int64, error) {
	var used int64
	_, err := get(tx, bHeldBytes, source, &used)
	return used, err
}

// pruneHeld removes up to pruneBatch held deliveries older than
// cutoff, and closes a card that removal left holding nothing, by the
// path that closes any card (closeCard, which drops what a card holds,
// so what still waits past its answer goes with it). A card Prune did
// not empty keeps its younger deliveries and stays open, and a card
// that never held anything is no concern of Prune's however old it is:
// only a card whose last waiting delivery Prune itself removed closes.
// n counts the deliveries removed, cards the cards closed.
func (s *Store) pruneHeld(cutoff time.Time, actor string) (n, cards int, more bool, err error) {
	err = s.write(func(w *wtx) error {
		n, cards, more = 0, 0, false
		type aged struct {
			uuid string
			kept []HeldDelivery
		}
		var dead []aged
		b := w.Bucket(bHeld)
		c := b.Cursor()
		for k, _ := c.First(); k != nil; k, _ = c.Next() {
			held, err := heldList(w.Tx, string(k))
			if err != nil {
				return err
			}
			var kept []HeldDelivery
			removed := 0
			for _, h := range held {
				if h.At.Before(cutoff) {
					removed++
				} else {
					kept = append(kept, h)
				}
			}
			if removed == 0 {
				continue
			}
			if n >= pruneBatch {
				more = true
				break
			}
			n += removed
			dead = append(dead, aged{uuid: string(k), kept: kept})
		}
		var emptied []string
		for _, d := range dead {
			if err := putHeld(w.Tx, d.uuid, d.kept); err != nil {
				return err
			}
			if len(d.kept) == 0 {
				emptied = append(emptied, d.uuid)
			} else if err := w.syncHeldCount(d.uuid); err != nil {
				return err
			}
		}
		for _, uuid := range emptied {
			row, err := w.loadBead(uuid)
			if err != nil {
				return err
			}
			if !row.open() {
				continue
			}
			if err := w.closeCard(row, "its held messages went in a prune", actor); err != nil {
				return err
			}
			cards++
		}
		return nil
	})
	return n, cards, more, err
}

// heldLine is the card's sentence for how many deliveries wait, as
// printed lines and clients say it; "" where none wait.
func heldLine(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return "1 message waits for your answer"
	default:
		return fmt.Sprintf("%d messages wait for your answer", n)
	}
}
