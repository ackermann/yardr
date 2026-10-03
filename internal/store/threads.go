package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bThreads holds which source threads each resident participates in:
// <resident>\x00<source>\x00<thread id> -> Participation.
var bThreads = []byte("threads")

// bReplies holds every reply a resident set out to post with yardr reply:
// <resident>\x00<key> -> Reply, from Replying (before the post) through
// Replied (after it).
var bReplies = []byte("replies")

func init() { allBuckets = append(allBuckets, bThreads, bReplies) }

// How a resident came to participate in a thread.
const (
	ParticipantActed    = "acted"    // a hook delivery showed it acting there
	ParticipantFollowed = "followed" // yardr thread follow
	ParticipantReplied  = "replied"  // yardr reply
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

// Reply is what a resident posts in a source's thread with yardr reply:
// made by Replying before the post, completed by Replied after it.
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
	Seq  uint64 `json:"seq,omitempty"`
	Hash string `json:"hash,omitempty"`
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
	return s.changeResident(name, func(w *wtx, r *Resident) error {
		if !participates(w.Tx, name, rp.Source, rp.Thread) {
			p := Participation{Resident: name, Source: rp.Source, Thread: rp.Thread, Title: rp.Title, URL: rp.ThreadURL,
				How: ParticipantReplied, Since: now()}
			if err := put(w.Tx, bThreads, threadKey(name, rp.Source, rp.Thread), p); err != nil {
				return err
			}
		}
		if rp.Key != "" {
			rec := Reply{Key: rp.Key, Seq: rp.Seq}
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
		return w.event(r.Journal, "replied", actor, data)
	})
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
