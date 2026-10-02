package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func head(t *testing.T, s store.API) uint64 {
	t.Helper()
	h, err := s.Head(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// The inbox reads the resident's own index entries, not the log: with
// 100k events of which 10 are for it, it decodes those 10.
func TestInboxReadsOnlyTheResidentsEntries(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", On: []string{"gave_up"}}, "me"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := own.BulkEvents(10000, "noise", nil); err != nil {
			t.Fatal(err)
		}
		if i%5 == 4 { // two through its --on kinds
			if err := own.LogEvent(ctx, "", "gave_up", "yard", nil); err != nil {
				t.Fatal(err)
			}
		} else if err := s.Mail(ctx, "mayor", "hello", "me"); err != nil {
			t.Fatal(err)
		}
	}
	// Mail to someone else and the mayor's own actions stay out.
	if _, err := s.PutResident(ctx, store.Resident{Name: "other", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "other", "not yours", "me"); err != nil {
		t.Fatal(err)
	}
	before := own.InboxReads()
	page, err := s.Inbox(ctx, "mayor", 3)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 10 || len(page.Events) != 3 {
		t.Fatalf("inbox total %d, %d events; want 10, 3", page.Total, len(page.Events))
	}
	if read := own.InboxReads() - before; read != 10 {
		t.Fatalf("inbox decoded %d events, want 10", read)
	}
	if page.Events[0].Kind != "mail" || page.Events[0].Seq >= page.Events[1].Seq {
		t.Fatalf("inbox not in seq order: %+v", page.Events)
	}
}

// A store from before the indexes gets them built on open.
func TestInboxIndexBuiltForOldStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "mayor", "kept across the upgrade", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.DropIndexes(); err != nil {
		t.Fatal(err)
	}
	s.Close(ctx)
	s = storetest.Open(t, path)
	page, err := s.Inbox(ctx, "mayor", 0)
	if err != nil || page.Total != 1 || page.Events[0].Data["text"] != "kept across the upgrade" {
		t.Fatalf("inbox after reopen = %+v %v", page, err)
	}
}

// The versions bucket is no longer written, and one an older yardr left is
// dropped on open.
func TestVersionsBucketDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("versions"))
		if err != nil {
			return err
		}
		return b.Put([]byte("bead/x"), []byte("{}"))
	}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := storetest.Open(t, path)
	if s.HasBucket("versions") {
		t.Fatal("versions bucket kept")
	}
	ctx := context.Background()
	a, err := s.CreateBead(ctx, store.NewBead{Title: "a"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateBead(ctx, store.NewBead{Title: "b"}, "me")
	if err := s.AddDep(ctx, a.ID, b.ID, "blocks", "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDep(ctx, a.ID, b.ID, "blocks", "me"); err != nil {
		t.Fatal(err)
	}
	if s.HasBucket("versions") {
		t.Fatal("writes recreated the versions bucket")
	}
}

var keep90 = store.Retention{Events: 90 * 24 * time.Hour}

func TestPruneRemovesOldEventsButTheHead(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddNote(ctx, b.ID, "old news", "me"); err != nil {
		t.Fatal(err)
	}
	if err := own.BulkEvents(2500, "noise", nil); err != nil {
		t.Fatal(err)
	}
	h := head(t, s)
	if err := own.Backdate(1, h, time.Now().Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(ctx, keep90, "me")
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != int(h-1) || res.HeldBy != "" {
		t.Fatalf("pruned %+v, want %d events", res, h-1)
	}
	evs, err := s.EventsSince(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The old head, then the store_pruned event.
	if len(evs) != 2 || evs[0].Seq != h || evs[1].Kind != "store_pruned" {
		t.Fatalf("left %+v", evs)
	}
	if n := own.IndexEntries(1); n != 0 {
		t.Fatalf("%d index entries left for a pruned event", n)
	}
	// The bead and its note are state, not log: they stay.
	if notes, err := s.Notes(ctx, b.ID); err != nil || len(notes) != 1 {
		t.Fatalf("notes = %+v %v", notes, err)
	}
	if evs, err := s.Events(ctx, b.ID, 0); err != nil || len(evs) != 0 {
		t.Fatalf("bead events = %+v %v", evs, err)
	}
	// The old head goes once it is no longer the head; then nothing old
	// enough is left, and a prune removes nothing and logs nothing.
	if res, err := s.Prune(ctx, keep90, "me"); err != nil || res.Events != 1 {
		t.Fatalf("second prune = %+v %v", res, err)
	}
	h = head(t, s)
	if res, err := s.Prune(ctx, keep90, "me"); err != nil || res.Events != 0 {
		t.Fatalf("third prune = %+v %v", res, err)
	}
	if head(t, s) != h {
		t.Fatal("a prune that removed nothing logged an event")
	}
}

// An event a resident has not read stays, however old, and so does all
// after it; a resident with nothing unread holds nothing back.
func TestPruneKeepsWhatAResidentHasNotRead(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := own.BulkEvents(100, "noise", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Mail(ctx, "mayor", "read me", "me"); err != nil {
		t.Fatal(err)
	}
	mail := head(t, s)
	if err := own.BulkEvents(100, "noise", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.AckInbox(ctx, "mayor", mail-1); err != nil {
		t.Fatal(err)
	}
	if err := own.Backdate(1, head(t, s), time.Now().Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(ctx, keep90, "me")
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != int(mail-1) || !strings.Contains(res.HeldBy, "resident mayor") {
		t.Fatalf("pruned %+v, want %d events held by the mayor", res, mail-1)
	}
	page, err := s.Inbox(ctx, "mayor", 0)
	if err != nil || page.Total != 1 || page.Events[0].Data["text"] != "read me" {
		t.Fatalf("inbox = %+v %v", page, err)
	}
	// Once read, it may go.
	if err := s.AckInbox(ctx, "mayor", page.Last); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Prune(ctx, keep90, "me"); err != nil || res.Events != 101 {
		t.Fatalf("after ack pruned %+v %v, want 101", res, err)
	}
}

// Nothing since the oldest live session began goes.
func TestPruneKeepsWhatALiveSessionSpans(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	if err := s.PutGroup(ctx, store.Group{Name: "g", Runner: "fake"}); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := own.BulkEvents(50, "noise", nil); err != nil {
		t.Fatal(err)
	}
	old := head(t, s)
	sess, err := s.Claim(ctx, b.ID, "g", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	if err := own.BulkEvents(50, "noise", nil); err != nil {
		t.Fatal(err)
	}
	h := head(t, s)
	long := time.Now().Add(-200 * 24 * time.Hour)
	started := time.Now().Add(-150 * 24 * time.Hour)
	if err := own.Backdate(1, old, long); err != nil {
		t.Fatal(err)
	}
	if err := own.Backdate(old+1, h, started.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := own.BackdateSession(sess.ID, started); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(ctx, keep90, "me")
	if err != nil {
		t.Fatal(err)
	}
	if res.Events != int(old) || !strings.Contains(res.HeldBy, "session "+sess.ID) {
		t.Fatalf("pruned %+v, want %d held by session %s", res, old, sess.ID)
	}
	// Once it ends, the rest is past retention too.
	if err := s.EndSession(ctx, sess.ID, "aborted", ""); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Prune(ctx, keep90, "me"); err != nil || res.Events == 0 || res.HeldBy != "" {
		t.Fatalf("after end pruned %+v %v", res, err)
	}
}

// Webhook bodies go after their own retention; the event stays.
func TestPruneHookPayloads(t *testing.T) {
	s, own := storetest.NewOwned(t)
	ctx := context.Background()
	seq, _, err := own.RecordHook(ctx, "linear", map[string]any{"source": "linear", "type": "comment"}, []byte(`{"big":1}`), []string{"d1"})
	if err != nil {
		t.Fatal(err)
	}
	young, _, err := own.RecordHook(ctx, "linear", map[string]any{"source": "linear", "type": "comment"}, []byte(`{"new":1}`), []string{"d2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := own.Backdate(seq, seq, time.Now().Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	res, err := s.Prune(ctx, store.Retention{HookPayloads: 30 * 24 * time.Hour}, "me")
	if err != nil || res.HookPayloads != 1 || res.Events != 0 {
		t.Fatalf("prune = %+v %v", res, err)
	}
	if _, err := s.HookPayload(ctx, seq); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old payload: %v", err)
	}
	if _, err := s.HookPayload(ctx, young); err != nil {
		t.Fatalf("young payload: %v", err)
	}
	if evs, err := s.EventsSince(ctx, seq-1, 1); err != nil || len(evs) != 1 || evs[0].Kind != "hook" {
		t.Fatalf("hook event = %+v %v", evs, err)
	}
	// With the event pruned, its dedupe key goes too: a redelivery is new.
	if err := own.Backdate(seq, seq, time.Now().Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if res, err := s.Prune(ctx, keep90, "me"); err != nil || res.Events != 1 {
		t.Fatalf("prune events = %+v %v", res, err)
	}
	if _, dup, err := own.RecordHook(ctx, "linear", map[string]any{"source": "linear"}, nil, []string{"d1"}); err != nil || dup {
		t.Fatalf("redelivery after prune: dup=%v %v", dup, err)
	}
	if _, dup, err := own.RecordHook(ctx, "linear", map[string]any{"source": "linear"}, nil, []string{"d2"}); err != nil || !dup {
		t.Fatalf("redelivery of a kept delivery: dup=%v %v", dup, err)
	}
}

// Compact shrinks a pruned file, keeps its contents, and refuses while the
// store is open.
func TestCompact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yard.db")
	s, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b, err := s.CreateBead(ctx, store.NewBead{Title: "survivor"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.BulkEvents(20000, "noise", map[string]any{"pad": strings.Repeat("x", 200)}); err != nil {
		t.Fatal(err)
	}
	h := head(t, s)
	if err := s.Backdate(1, h, time.Now().Add(-100*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Prune(ctx, keep90, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Compact(path); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("compact of an open store: %v", err)
	}
	s.Close(ctx)
	res, err := store.Compact(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.After*4 > res.Before {
		t.Fatalf("compacted %d -> %d bytes", res.Before, res.After)
	}
	if _, err := os.Stat(path + ".compact"); !os.IsNotExist(err) {
		t.Fatalf("temporary file left: %v", err)
	}
	s = storetest.Open(t, path)
	if got, err := s.GetBead(ctx, b.ID); err != nil || got.Title != "survivor" {
		t.Fatalf("bead after compact = %+v %v", got, err)
	}
	if hd := head(t, s); hd <= h {
		t.Fatalf("head after compact %d, want past %d", hd, h)
	}
	// Sequences carry on from where they were.
	b2, err := s.CreateBead(ctx, store.NewBead{Title: "after"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if evs, _ := s.Events(ctx, b2.ID, 1); len(evs) != 1 || evs[0].Seq <= h {
		t.Fatalf("new event seq %+v, want past %d", evs, h)
	}
}

// A backlog longer than a page arrives whole, in order.
func TestSubscribePagesTheBacklog(t *testing.T) {
	s, own := storetest.NewOwned(t)
	if err := own.BulkEvents(2500, "noise", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := s.Subscribe(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := uint64(11)
	for e := range ch {
		if e.Seq != want {
			t.Fatalf("got seq %d, want %d", e.Seq, want)
		}
		if want == 2500 {
			return
		}
		want++
	}
	t.Fatalf("stream ended at %d", want)
}
