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

// bThreads holds which source threads each crew member participates in:
// <crew member>\x00<source>\x00<thread id> -> Participation.
var bThreads = []byte("threads")

// bReplies holds every post a crew member set out to make in a source, a
// reply (yardr reply) or a thread of its own (yardr thread open):
// <crew member>\x00<key> -> Reply, from Replying or Opening (before the post)
// through Replied or Opened (after it).
var bReplies = []byte("replies")

func init() { allBuckets = append(allBuckets, bThreads, bReplies) }

// How a crew member came to participate in a thread.
const (
	ParticipantActed    = "acted"    // a hook delivery showed it acting there
	ParticipantFollowed = "followed" // yardr thread follow
	ParticipantReplied  = "replied"  // yardr reply
	ParticipantOpened   = "opened"   // yardr thread open
)

// Participation is a crew member taking part in a source's thread (in Linear,
// an issue), so that an involved subscription hears of what others do
// there afterwards.
type Participation struct {
	CrewMember string    `json:"crew"`
	Source     string    `json:"source"`
	Thread     string    `json:"thread"`
	Title      string    `json:"title,omitempty"`
	URL        string    `json:"url,omitempty"`
	How        string    `json:"how"`
	Since      time.Time `json:"since"`
}

func threadKey(member, source, thread string) string {
	return member + "\x00" + source + "\x00" + thread
}

// participates reports whether the crew member takes part in the thread.
func participates(tx *bolt.Tx, member, source, thread string) bool {
	return thread != "" && tx.Bucket(bThreads).Get([]byte(threadKey(member, source, thread))) != nil
}

// recordParticipation marks every crew member that is the actor of a hook
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
	return each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		id, known := r.Identities[source]
		if !known || !id.is(actorID) || participates(tx, r.Name, source, threadID) {
			return nil
		}
		return put(tx, bThreads, threadKey(r.Name, source, threadID), Participation{
			CrewMember: r.Name, Source: source, Thread: threadID, Title: title, URL: url,
			How: ParticipantActed, Since: now(),
		})
	})
}

// FollowThread makes the crew member a participant in a thread by hand, as
// if it had acted there. Anyone but the crew member and the operator is
// refused (maySpeakAs), as actor.
func (s *Store) FollowThread(ctx context.Context, name, source, thread, actor string) error {
	if source == "" || thread == "" {
		return fmt.Errorf("follow: source and thread are required")
	}
	return s.changeCrew(name, func(w *wtx, r *CrewMember) error {
		if err := maySpeakAs(ctx, w.Tx, name, actor); err != nil {
			return err
		}
		thread, err := peerThread(w.Tx, source, thread)
		if err != nil {
			return err
		}
		if participates(w.Tx, name, source, thread) {
			return nil
		}
		p := Participation{CrewMember: name, Source: source, Thread: thread, How: ParticipantFollowed, Since: now()}
		if err := put(w.Tx, bThreads, threadKey(name, source, thread), p); err != nil {
			return err
		}
		return nil
	})
}

// Reply is what a crew member posts in a source: a comment in a thread with
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
	// a source takes it as the comment's own id or as a marker in the
	// body. Either way an attempt whose outcome was lost (a timeout after
	// the source had taken the post) is found by it, not posted again.
	Key string `json:"key,omitempty"`
	// Seq is the delivery answered and Hash identifies the answer (its
	// conversation and body), so a retry of the same reply gets the same
	// key and a different reply to the same delivery a new one.
	// A thread the crew member opened answers no delivery: its Seq is 0, In is
	// where it was opened (a team, a repository) and Hash identifies the
	// source, the place, the title and the body.
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

func replyKey(member, key string) string { return member + "\x00" + key }

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

// Replying is the crew member about to answer delivery seq with body, under
// the conversation to ("" for the delivery's own). It returns the Reply
// for exactly that answer: a fresh one, with a key chosen now, or the one
// an earlier attempt made, with Attempts counted up. The record is written
// before anything is posted, so a reply the source took but the caller
// never heard back about is still known by its key. It is where anyone but
// the crew member and the operator is refused (maySpeakAs), as actor.
func (s *Store) Replying(ctx context.Context, name string, seq uint64, to, body, actor string) (*Reply, error) {
	if seq == 0 || body == "" {
		return nil, fmt.Errorf("replying: the delivery's seq and a body are required")
	}
	hash := replyHash(to, body)
	var out *Reply
	err := s.changeCrew(name, func(w *wtx, r *CrewMember) error {
		if err := maySpeakAs(ctx, w.Tx, name, actor); err != nil {
			return err
		}
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

// Replied records that the crew member answered in a source's thread: the
// reply made by Replying (rp.Key) is marked posted with the source's id
// of the comment and its URL, a `replied` event goes on the crew member's
// journal, and it participates in the thread from now on. The source's
// webhook echoes the reply later, but the crew member takes part as soon as
// it has spoken. A thread it already takes part in keeps how and since it
// joined. Without a key (a caller that did not go through Replying) only
// the event and the participation are recorded. Anyone but the crew member
// and the operator is refused (maySpeakAs), as actor, as in Replying.
func (s *Store) Replied(ctx context.Context, name string, rp Reply, actor string) error {
	if rp.Source == "" || rp.Thread == "" {
		return fmt.Errorf("replied: source and thread are required")
	}
	return s.posted(ctx, name, rp, ParticipantReplied, "replied", actor)
}

// posted completes the record of a post (rp.Key), makes the crew member a
// participant in its thread (how, unless it already takes part) and logs
// the event kind on its journal.
func (s *Store) posted(ctx context.Context, name string, rp Reply, how, kind, actor string) error {
	return s.changeCrew(name, func(w *wtx, r *CrewMember) error {
		if err := maySpeakAs(ctx, w.Tx, name, actor); err != nil {
			return err
		}
		if !participates(w.Tx, name, rp.Source, rp.Thread) {
			p := Participation{CrewMember: name, Source: rp.Source, Thread: rp.Thread, Title: rp.Title, URL: rp.ThreadURL,
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
		data := map[string]any{"crew": name, "source": rp.Source, "thread": rp.Thread, "url": rp.URL}
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

// OpenThreads is the crew member setting (a key of its config) naming the
// sources it may open threads in: "acme,forge", or a list. Unset, it
// may open none.
const OpenThreads = "open_threads"

// MayOpen lists the sources the crew member may open threads in, sorted.
func (r CrewMember) MayOpen() []string { return r.sources(OpenThreads) }

// sources reads a setting that names sources: "acme,forge", or a list.
// Sorted, each once.
func (r CrewMember) sources(setting string) []string {
	var out []string
	switch v := r.Config[setting].(type) {
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

// AllowOpening adds sources to those the crew member may open threads in
// (OpenThreads) and keeps the ones it has: what an import of pack does on
// the operator's yes. It returns the sources that are new to the crew member,
// sorted, and logs them as open_threads_granted on its journal, with the
// pack; when the crew member may open threads in all of them already, nothing
// is written. A source that is new to it is the operator's to grant
// (operatorOnly), as in PutCrew.
func (s *Store) AllowOpening(ctx context.Context, name string, sources []string, pack, actor string) ([]string, error) {
	var added []string
	err := s.write(func(w *wtx) error {
		added = nil
		var r CrewMember
		ok, err := get(w.Tx, bCrew, name, &r)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("crew member %s: %w", name, ErrNotFound)
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
		if err := w.operatorOnly(ctx, actor, fmt.Sprintf("let %s open threads in %s", name, strings.Join(added, ", ")), Askable); err != nil {
			return err
		}
		may = append(may, added...)
		slices.Sort(may)
		if r.Config == nil {
			r.Config = map[string]any{}
		}
		// As --set open_threads=a,b writes it.
		r.Config[OpenThreads] = strings.Join(may, ",")
		r.Revision = newRevision()
		if err := put(w.Tx, bCrew, name, r); err != nil {
			return err
		}
		return w.event(r.Journal, "open_threads_granted", actor, map[string]any{"crew": name, "sources": added, "pack": pack})
	})
	return added, err
}

// openHash identifies one thread to open: where, and what it says.
func openHash(source, in, title, body string) string {
	sum := sha256.Sum256([]byte("open\x00" + source + "\x00" + in + "\x00" + title + "\x00" + body))
	return hex.EncodeToString(sum[:])
}

// Opening is the crew member about to open a thread in source, in the place
// in (a team, a repository), with title and body. Like
// Replying, it returns the record for exactly that thread, a fresh one
// with a key chosen now or the one an earlier attempt made, written before
// anything is posted. A crew member whose definition does not allow the
// source (OpenThreads) is refused here, whoever asks, and so is anyone but
// the crew member and the operator (maySpeakAs), as actor.
func (s *Store) Opening(ctx context.Context, name, source, in, title, body, actor string) (*Reply, error) {
	if source == "" || in == "" || title == "" {
		return nil, fmt.Errorf("opening: a source, where to open the thread and a title are required")
	}
	hash := openHash(source, in, title, body)
	var out *Reply
	err := s.changeCrew(name, func(w *wtx, r *CrewMember) error {
		if err := maySpeakAs(ctx, w.Tx, name, actor); err != nil {
			return err
		}
		if !slices.Contains(r.MayOpen(), source) {
			return fmt.Errorf("crew member %s may not open threads in %s: its definition does not allow it. The operator allows it with yardr crew set %s %s=%s",
				name, source, name, OpenThreads, strings.Join(append(r.MayOpen(), source), ","))
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

// Opened records that the crew member opened a thread: the record made by
// Opening (rp.Key) is marked posted with the thread's id and URL, a
// `thread_opened` event goes on the crew member's journal, and the thread is
// the crew member's from now on: it participates (how opened) before the
// source's webhook has said anything. Anyone but the crew member and the
// operator is refused (maySpeakAs), as actor, as in Opening.
func (s *Store) Opened(ctx context.Context, name string, rp Reply, actor string) error {
	if rp.Source == "" || rp.Thread == "" || rp.Key == "" {
		return fmt.Errorf("opened: source, thread and the key Opening chose are required")
	}
	return s.posted(ctx, name, rp, ParticipantOpened, "thread_opened", actor)
}

// SetState is the crew member setting (a key of its config) naming the sources
// it may set the status of a thread in: "acme,forge", or a list. Unset,
// it may set none.
const SetState = "set_state"

// MaySetState lists the sources the crew member may set the status of a thread
// in, sorted.
func (r CrewMember) MaySetState() []string { return r.sources(SetState) }

// ThreadState is a crew member setting the status of a source's thread: the
// word it asked for (started, review, done, or label:<name>) and the source's
// name of the status the thread was put in.
type ThreadState struct {
	Source string `json:"source"`
	Thread string `json:"thread"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
	State  string `json:"state"`
	Status string `json:"status"`
	Was    string `json:"was,omitempty"`
	// Instead is the word acted on when the source has no status for the
	// one asked (see hooks.Stated).
	Instead string `json:"instead,omitempty"`
}

// mayState is the crew member's participation in the thread it is about to set
// the status of, or the refusal: a crew member whose definition does not allow
// the source (SetState) is refused, whoever asks, and so is one that takes
// no part in the thread.
func mayState(tx *bolt.Tx, r *CrewMember, source, thread string) (*Participation, error) {
	if source == "" || thread == "" {
		return nil, fmt.Errorf("thread state: source and thread are required")
	}
	if !slices.Contains(r.MaySetState(), source) {
		return nil, fmt.Errorf("crew member %s may not set the status of threads in %s: its definition does not allow it. The operator allows it with yardr crew set %s %s=%s",
			r.Name, source, r.Name, SetState, strings.Join(append(r.MaySetState(), source), ","))
	}
	var p Participation
	ok, err := get(tx, bThreads, threadKey(r.Name, source, thread), &p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("crew member %s takes no part in %s thread %s, and sets the status only of a thread it takes part in (yardr thread follow %s --source %s %s makes it a participant)",
			r.Name, source, thread, r.Name, source, thread)
	}
	return &p, nil
}

// SettingState is the crew member about to set the status of a thread: its
// participation in the thread, or why it may not (mayState). It is asked
// before the source is called and writes nothing. Anyone but the crew
// member and the operator is refused (maySpeakAs), as actor.
func (s *Store) SettingState(ctx context.Context, name, source, thread, actor string) (*Participation, error) {
	var out *Participation
	err := s.read(func(tx *bolt.Tx) error {
		var r CrewMember
		ok, err := get(tx, bCrew, name, &r)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("crew member %s: %w", name, ErrNotFound)
		}
		if err := maySpeakAs(ctx, tx, name, actor); err != nil {
			return err
		}
		out, err = mayState(tx, &r, source, thread)
		return err
	})
	return out, err
}

// StateSet records that the crew member set the status of a thread: a
// `thread_state` event on its journal with the source, the thread, the word
// asked and the status set. What SettingState refuses is refused here too,
// so nothing is on record that the crew member was not allowed.
func (s *Store) StateSet(ctx context.Context, name string, ts ThreadState, actor string) error {
	if ts.State == "" || ts.Status == "" {
		return fmt.Errorf("thread state: the state asked for and the status set are required")
	}
	return s.changeCrew(name, func(w *wtx, r *CrewMember) error {
		if err := maySpeakAs(ctx, w.Tx, name, actor); err != nil {
			return err
		}
		if _, err := mayState(w.Tx, r, ts.Source, ts.Thread); err != nil {
			return err
		}
		data := map[string]any{"crew": name, "source": ts.Source, "thread": ts.Thread, "state": ts.State, "status": ts.Status}
		for k, v := range map[string]string{"title": ts.Title, "url": ts.URL, "was": ts.Was, "instead": ts.Instead} {
			if v != "" {
				data[k] = v
			}
		}
		return w.event(r.Journal, "thread_state", actor, data)
	})
}

// UnfollowThread ends the crew member's participation in a thread, however it
// began. Acting there again makes it a participant again. Anyone but the
// crew member and the operator is refused (maySpeakAs), as actor.
func (s *Store) UnfollowThread(ctx context.Context, name, source, thread, actor string) error {
	return s.changeCrew(name, func(w *wtx, r *CrewMember) error {
		if err := maySpeakAs(ctx, w.Tx, name, actor); err != nil {
			return err
		}
		thread, err := peerThread(w.Tx, source, thread)
		if err != nil {
			return err
		}
		key := []byte(threadKey(name, source, thread))
		if w.Bucket(bThreads).Get(key) == nil {
			return fmt.Errorf("crew member %s does not participate in %s thread %s: %w", name, source, thread, ErrNotFound)
		}
		if err := w.Bucket(bThreads).Delete(key); err != nil {
			return err
		}
		return nil
	})
}

// Threads lists the threads the crew member participates in, by source and
// thread id.
func (s *Store) Threads(ctx context.Context, name string) ([]Participation, error) {
	out := []Participation{}
	err := s.read(func(tx *bolt.Tx) error {
		if tx.Bucket(bCrew).Get([]byte(name)) == nil {
			return fmt.Errorf("crew member %s: %w", name, ErrNotFound)
		}
		return each(tx, bThreads, name+"\x00", func(_ []byte, p *Participation) error {
			out = append(out, *p)
			return nil
		})
	})
	return out, err
}

// dropThreads forgets a deleted crew member's participation and replies, so
// a crew member later created under the name starts with none.
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
