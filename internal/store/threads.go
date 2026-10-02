package store

import (
	"context"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
)

// bThreads holds which source threads each resident participates in:
// <resident>\x00<source>\x00<thread id> -> Participation.
var bThreads = []byte("threads")

func init() { allBuckets = append(allBuckets, bThreads) }

// How a resident came to participate in a thread.
const (
	ParticipantActed    = "acted"    // a hook delivery showed it acting there
	ParticipantFollowed = "followed" // yardr thread follow
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

// dropThreads forgets a deleted resident's participation, so a resident
// later created under the name starts with none.
func dropThreads(tx *bolt.Tx, name string) error {
	b := tx.Bucket(bThreads)
	for _, k := range keys(tx, bThreads, name+"\x00") {
		if err := b.Delete([]byte(k)); err != nil {
			return err
		}
	}
	return nil
}
