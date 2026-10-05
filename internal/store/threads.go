package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bThreads holds which source threads each resident participates in:
// <resident>\x00<source>\x00<thread id> -> Participation.
var bThreads = []byte("threads")

// bReplies holds every post a resident set out to make in a source, a
// reply (yardr reply) or a thread of its own (yardr thread open):
// <resident>\x00<key> -> Reply, from Replying or Opening (before the post)
// through Replied or Opened (after it).
var bReplies = []byte("replies")

func init() { allBuckets = append(allBuckets, bThreads, bReplies) }

// How a resident came to participate in a thread.
const (
	ParticipantActed    = "acted"    // a hook delivery showed it acting there
	ParticipantFollowed = "followed" // yardr thread follow
	ParticipantReplied  = "replied"  // yardr reply
	ParticipantOpened   = "opened"   // yardr thread open
)

// Participation is a resident taking part in a source's thread (in Linear,
// an issue), so that an involved subscription hears of what others do
// there afterwards.
type Participation struct {
	Resident string    `json:"resident"`
	Source   string    `json:"source"`
	Thread   string    `json:"thread"`
	Title    string    `json:"title,omitempty"`
	URL      string    `json:"url,omitempty"`
	How      string    `json:"how"`
	Since    time.Time `json:"since"`
}

func threadKey(resident, source, thread string) string {
	return resident + "\x00" + source + "\x00" + thread
}

// participates reports whether the resident takes part in the thread.
func participates(tx *bolt.Tx, resident, source, thread string) bool {
	return thread != "" && tx.Bucket(bThreads).Get([]byte(threadKey(resident, source, thread))) != nil
}

// recordParticipation marks every resident that is the actor of a hook
// delivery as a participant in its thread. Its own action is the evidence,
// so no event is logged beyond the delivery itself; a thread it already
// takes part in keeps how and since it joined.
func recordParticipation(tx *bolt.Tx, data map[string]any) error {
	source, _ := data["source"].(string)
	actor, _ := data["actor"].(map[string]any)
	actorID, _ := actor["id"].(string)
	thread, _ := data["thread"].(map[string]any)
	threadID, _ := thread["id"].(string)
	if actorID == "" || threadID == "" {
		return nil
	}
	title, _ := thread["title"].(string)
	url, _ := thread["url"].(string)
	return each(tx, bResidents, "", func(_ []byte, r *Resident) error {
		id, known := r.Identities[source]
		if !known || !id.is(actorID) || participates(tx, r.Name, source, threadID) {
			return nil
		}
		return put(tx, bThreads, threadKey(r.Name, source, threadID), Participation{
			Resident: r.Name, Source: source, Thread: threadID, Title: title, URL: url,
			How: ParticipantActed, Since: now(),
		})
	})
}

// FollowThread makes the resident a participant in a thread by hand, as
// if it had acted there.
func (s *Store) FollowThread(ctx context.Context, name, source, thread, actor string) error {
	if source == "" || thread == "" {
		return fmt.Errorf("follow: source and thread are required")
	}
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if participates(w.Tx, name, source, thread) {
			return nil
		}
		p := Participation{Resident: name, Source: source, Thread: thread, How: ParticipantFollowed, Since: now()}
		if err := put(w.Tx, bThreads, threadKey(name, source, thread), p); err != nil {
			return err
		}
		return w.event(r.Journal, "thread_followed", actor, map[string]any{"resident": name, "source": source, "thread": thread})
	})
}

// Reply is what a resident posts in a source: a comment in a thread with
// yardr reply (made by Replying before the post, completed by Replied
// after it), or a thread of its own with yardr thread open (Opening, then
// Opened).
type Reply struct {
	Source    string `json:"source,omitempty"`
	Thread    string `json:"thread,omitempty"`
	Title     string `json:"title,omitempty"`
	ThreadURL string `json:"thread_url,omitempty"`
	URL       string `json:"url,omitempty"` // of the reply
	// Key is the id chosen for the reply before it is posted, a UUID v4:
	// Linear takes it as the comment's own id, GitHub gets it as a marker
	// in the body. Either way an attempt whose outcome was lost (a timeout
	// after the source had taken the post) is found by it, not posted
	// again.
	Key string `json:"key,omitempty"`
	// Seq is the delivery answered and Hash identifies the answer (its
	// conversation and body), so a retry of the same reply gets the same
	// key and a different reply to the same delivery a new one.
	// A thread the resident opened answers no delivery: its Seq is 0, In is
	// where it was opened (a Linear team, a GitHub repository) and Hash
	// identifies the source, the place, the title and the body.
	Seq  uint64 `json:"seq,omitempty"`
	Hash string `json:"hash,omitempty"`
	In   string `json:"in,omitempty"`
	// Attempts counts the Replying calls: more than one means an earlier
	// attempt did not get as far as Replied, so the source may hold the
	// comment already.
	Attempts int `json:"attempts,omitempty"`
	// Message is the source's id of the comment once it is posted, and
	// Posted says it is: a retry then has nothing left to do.
	Message string    `json:"message,omitempty"`
	Posted  bool      `json:"posted,omitempty"`
	At      time.Time `json:"at"` // Replying first, then the post
}

func replyKey(resident, key string) string { return resident + "\x00" + key }

// replyHash identifies one answer to a delivery: the conversation it goes
// under and its body.
func replyHash(to, body string) string {
	sum := sha256.Sum256([]byte(to + "\x00" + body))
	return hex.EncodeToString(sum[:])
}

// newReplyKey is a fresh UUID v4 (RFC 9562), the form Linear takes as a
// client-chosen comment id.
func newReplyKey() string {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		panic(err)
	}
	u[6] = 0x40 | u[6]&0x0f // version 4
	u[8] = 0x80 | u[8]&0x3f // variant 10
	h := hex.EncodeToString(u[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// Replying is the resident about to answer delivery seq with body, under
// the conversation to ("" for the delivery's own). It returns the Reply
// for exactly that answer: a fresh one, with a key chosen now, or the one
// an earlier attempt made, with Attempts counted up. The record is written
// before anything is posted, so a reply the source took but the caller
// never heard back about is still known by its key.
func (s *Store) Replying(ctx context.Context, name string, seq uint64, to, body string) (*Reply, error) {
	if seq == 0 || body == "" {
		return nil, fmt.Errorf("replying: the delivery's seq and a body are required")
	}
	hash := replyHash(to, body)
	var out *Reply
	err := s.changeResident(name, func(w *wtx, r *Resident) error {
		out = nil
		err := each(w.Tx, bReplies, name+"\x00", func(_ []byte, rp *Reply) error {
			if rp.Seq == seq && rp.Hash == hash {
				out = rp
			}
			return nil
		})
		if err != nil {
			return err
		}
		if out == nil {
			out = &Reply{Key: newReplyKey(), Seq: seq, Hash: hash, At: now()}
		}
		out.Attempts++
		return put(w.Tx, bReplies, replyKey(name, out.Key), out)
	})
	return out, err
}

// Replied records that the resident answered in a source's thread: the
// reply made by Replying (rp.Key) is marked posted with the source's id
// of the comment and its URL, a `replied` event goes on the resident's
// journal, and it participates in the thread from now on. The source's
// webhook echoes the reply later, but the resident takes part as soon as
// it has spoken. A thread it already takes part in keeps how and since it
// joined. Without a key (a caller that did not go through Replying) only
// the event and the participation are recorded.
func (s *Store) Replied(ctx context.Context, name string, rp Reply, actor string) error {
	if rp.Source == "" || rp.Thread == "" {
		return fmt.Errorf("replied: source and thread are required")
	}
	return s.posted(name, rp, ParticipantReplied, "replied", actor)
}

// posted completes the record of a post (rp.Key), makes the resident a
// participant in its thread (how, unless it already takes part) and logs
// the event kind on its journal.
func (s *Store) posted(name string, rp Reply, how, kind, actor string) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if !participates(w.Tx, name, rp.Source, rp.Thread) {
			p := Participation{Resident: name, Source: rp.Source, Thread: rp.Thread, Title: rp.Title, URL: rp.ThreadURL,
				How: how, Since: now()}
			if err := put(w.Tx, bThreads, threadKey(name, rp.Source, rp.Thread), p); err != nil {
				return err
			}
		}
		if rp.Key != "" {
			rec := Reply{Key: rp.Key, Seq: rp.Seq, In: rp.In}
			if raw := w.Bucket(bReplies).Get([]byte(replyKey(name, rp.Key))); raw != nil {
				if err := json.Unmarshal(raw, &rec); err != nil {
					return err
				}
			}
			rec.Source, rec.Thread, rec.Title, rec.ThreadURL = rp.Source, rp.Thread, rp.Title, rp.ThreadURL
			rec.URL, rec.Message, rec.Posted, rec.At = rp.URL, rp.Message, true, now()
			if err := put(w.Tx, bReplies, replyKey(name, rp.Key), rec); err != nil {
				return err
			}
		}
		data := map[string]any{"resident": name, "source": rp.Source, "thread": rp.Thread, "url": rp.URL}
		if rp.Title != "" {
			data["title"] = rp.Title
		}
		if rp.Message != "" {
			data["message"] = rp.Message
		}
		if rp.Seq != 0 {
			data["seq"] = rp.Seq
		}
		if rp.In != "" {
			data["in"] = rp.In
		}
		return w.event(r.Journal, kind, actor, data)
	})
}

// OpenThreads is the resident setting (a key of its config) naming the
// sources it may open threads in: "linear,github", or a list. Unset, it
// may open none.
const OpenThreads = "open_threads"

// MayOpen lists the sources the resident may open threads in, sorted.
func (r Resident) MayOpen() []string {
	var out []string
	switch v := r.Config[OpenThreads].(type) {
	case string:
		out = strings.Split(v, ",")
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case []string: // a definition not yet through JSON
		out = slices.Clone(v)
	}
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	out = slices.DeleteFunc(out, func(s string) bool { return s == "" })
	slices.Sort(out)
	return slices.Compact(out)
}

// AllowOpening adds sources to those the resident may open threads in
// (OpenThreads) and keeps the ones it has: what an import of pack does on
// the operator's yes. It returns the sources that are new to the resident,
// sorted, and logs them as open_threads_granted on its journal, with the
// pack; when the resident may open threads in all of them already, nothing
// is written. Who may grant is the caller's to check, as with PutResident.
func (s *Store) AllowOpening(ctx context.Context, name string, sources []string, pack, actor string) ([]string, error) {
	var added []string
	err := s.write(func(w *wtx) error {
		added = nil
		var r Resident
		ok, err := get(w.Tx, bResidents, name, &r)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("resident %s: %w", name, ErrNotFound)
		}
		may := r.MayOpen()
		for _, src := range sources {
			if src = strings.TrimSpace(src); src == "" || strings.Contains(src, ",") {
				return fmt.Errorf("allow opening: %q is no source", src)
			}
			if !slices.Contains(may, src) && !slices.Contains(added, src) {
				added = append(added, src)
			}
		}
		if len(added) == 0 {
			return nil
		}
		slices.Sort(added)
		may = append(may, added...)
		slices.Sort(may)
		if r.Config == nil {
			r.Config = map[string]any{}
		}
		// As --set open_threads=a,b writes it.
		r.Config[OpenThreads] = strings.Join(may, ",")
		r.Revision = newRevision()
		if err := put(w.Tx, bResidents, name, r); err != nil {
			return err
		}
		return w.event(r.Journal, "open_threads_granted", actor, map[string]any{"resident": name, "sources": added, "pack": pack})
	})
	return added, err
}

// openHash identifies one thread to open: where, and what it says.
func openHash(source, in, title, body string) string {
	sum := sha256.Sum256([]byte("open\x00" + source + "\x00" + in + "\x00" + title + "\x00" + body))
	return hex.EncodeToString(sum[:])
}

// Opening is the resident about to open a thread in source, in the place
// in (a Linear team, a GitHub repository), with title and body. Like
// Replying, it returns the record for exactly that thread, a fresh one
// with a key chosen now or the one an earlier attempt made, written before
// anything is posted. A resident whose definition does not allow the
// source (OpenThreads) is refused here, whoever asks.
func (s *Store) Opening(ctx context.Context, name, source, in, title, body string) (*Reply, error) {
	if source == "" || in == "" || title == "" {
		return nil, fmt.Errorf("opening: a source, where to open the thread and a title are required")
	}
	hash := openHash(source, in, title, body)
	var out *Reply
	err := s.changeResident(name, func(w *wtx, r *Resident) error {
		if !slices.Contains(r.MayOpen(), source) {
			return fmt.Errorf("resident %s may not open threads in %s: its definition does not allow it. The operator allows it with --set %s=%s on yardr resident add %s (add replaces the definition, so give the rest of it too)",
				name, source, OpenThreads, strings.Join(append(r.MayOpen(), source), ","), name)
		}
		out = nil
		err := each(w.Tx, bReplies, name+"\x00", func(_ []byte, rp *Reply) error {
			if rp.Seq == 0 && rp.Hash == hash {
				out = rp
			}
			return nil
		})
		if err != nil {
			return err
		}
		if out == nil {
			out = &Reply{Key: newReplyKey(), Source: source, In: in, Title: title, Hash: hash, At: now()}
		}
		out.Attempts++
		return put(w.Tx, bReplies, replyKey(name, out.Key), out)
	})
	return out, err
}

// Opened records that the resident opened a thread: the record made by
// Opening (rp.Key) is marked posted with the thread's id and URL, a
// `thread_opened` event goes on the resident's journal, and the thread is
// the resident's from now on: it participates (how opened) before the
// source's webhook has said anything.
func (s *Store) Opened(ctx context.Context, name string, rp Reply, actor string) error {
	if rp.Source == "" || rp.Thread == "" || rp.Key == "" {
		return fmt.Errorf("opened: source, thread and the key Opening chose are required")
	}
	return s.posted(name, rp, ParticipantOpened, "thread_opened", actor)
}

// UnfollowThread ends the resident's participation in a thread, however it
// began. Acting there again makes it a participant again.
func (s *Store) UnfollowThread(ctx context.Context, name, source, thread, actor string) error {
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		key := []byte(threadKey(name, source, thread))
		if w.Bucket(bThreads).Get(key) == nil {
			return fmt.Errorf("resident %s does not participate in %s thread %s: %w", name, source, thread, ErrNotFound)
		}
		if err := w.Bucket(bThreads).Delete(key); err != nil {
			return err
		}
		return w.event(r.Journal, "thread_unfollowed", actor, map[string]any{"resident": name, "source": source, "thread": thread})
	})
}

// Threads lists the threads the resident participates in, by source and
// thread id.
func (s *Store) Threads(ctx context.Context, name string) ([]Participation, error) {
	out := []Participation{}
	err := s.read(func(tx *bolt.Tx) error {
		if tx.Bucket(bResidents).Get([]byte(name)) == nil {
			return fmt.Errorf("resident %s: %w", name, ErrNotFound)
		}
		return each(tx, bThreads, name+"\x00", func(_ []byte, p *Participation) error {
			out = append(out, *p)
			return nil
		})
	})
	return out, err
}

// dropThreads forgets a deleted resident's participation and replies, so
// a resident later created under the name starts with none.
func dropThreads(tx *bolt.Tx, name string) error {
	for _, bucket := range [][]byte{bThreads, bReplies} {
		b := tx.Bucket(bucket)
		for _, k := range keys(tx, bucket, name+"\x00") {
			if err := b.Delete([]byte(k)); err != nil {
				return err
			}
		}
	}
	return nil
}
