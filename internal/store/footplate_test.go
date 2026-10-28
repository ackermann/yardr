package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// footYard is a store with the operator jo, the user ada with the devices
// phone and tablet, the user bob with none, the crew member yardmaster,
// and the clock in the test's hand. Its callers stay sequential.
type footYard struct {
	*Store
	ctx context.Context
	now time.Time
}

func newFootYard(t *testing.T) *footYard {
	t.Helper()
	y := &footYard{ctx: context.Background(), now: time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)}
	clock = func() time.Time { return y.now }
	t.Cleanup(func() { clock = time.Now })
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	y.Store = s
	if err := s.SetSetting(y.ctx, OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ada", "bob"} {
		if _, err := s.AddUser(y.ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.PutCrew(y.ctx, CrewMember{Name: "yardmaster", Runner: "fake"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"phone", "tablet"} {
		if _, err := s.AddDevice(y.ctx, "ada", name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	return y
}

func (y *footYard) due(t *testing.T) []FootItem {
	t.Helper()
	rows, err := y.FootDue(y.ctx)
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// footKinds is the rows as kind user/device, in order.
func footKinds(rows []FootItem) []string {
	var out []string
	for _, f := range rows {
		out = append(out, f.Kind+" "+f.User+"/"+f.Device)
	}
	return out
}

func (y *footYard) bead(t *testing.T, title string) string {
	t.Helper()
	b, err := y.CreateBead(y.ctx, NewBead{Title: title}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return b.ID
}

func (y *footYard) ask(t *testing.T, bead, to, question string) uint64 {
	t.Helper()
	seq, err := y.AddAsk(y.ctx, bead, Ask{
		Question: question,
		To:       to,
		Context:  []string{"The move was tested on a copy and passed."},
		Options: []AskOption{
			{Label: "Now", Consequence: "the links move tonight"},
			{Label: "After Friday", Consequence: "the old source stays until then"},
		},
	}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return seq
}

// An intent is queued per device of the user a letter is for, when it is
// from someone else: never the person's own, never for a user with no
// device, and never for a crew member. Each carries a random opaque id,
// and no row holds a platform or a push token. Read, the rows drop.
func TestFootQueuedWithMail(t *testing.T) {
	y := newFootYard(t)
	if err := y.Mail(y.ctx, "ada", "the tests are green", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	y.now = y.now.Add(time.Second)
	if err := y.Mail(y.ctx, "ada", "a note to myself", "ada"); err != nil {
		t.Fatal(err)
	}
	if err := y.Mail(y.ctx, "bob", "for bob", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if err := y.Mail(y.ctx, "yardmaster", "for the crew", "ada"); err != nil {
		t.Fatal(err)
	}
	rows := y.due(t)
	got := footKinds(rows)
	slices.Sort(got)
	if len(rows) != 2 || strings.Join(got, "|") != "mail ada/phone|mail ada/tablet" {
		t.Fatalf("due = %v", got)
	}
	seen := map[string]bool{}
	for _, f := range rows {
		if len(f.EventID) != 32 {
			t.Fatalf("event id %q is not 128 bits hex", f.EventID)
		}
		if _, err := hex.DecodeString(f.EventID); err != nil {
			t.Fatalf("event id %q is not hex: %v", f.EventID, err)
		}
		if seen[f.EventID] {
			t.Fatalf("event id %q twice", f.EventID)
		}
		seen[f.EventID] = true
		if f.Mail == nil || f.Mail.From != "yardmaster" || f.Mail.Read {
			t.Fatalf("the mail = %+v", f.Mail)
		}
		if f.At.IsZero() || f.Due != f.At {
			t.Fatalf("at/due = %s/%s", f.At, f.Due)
		}
	}
	raw, _ := json.Marshal(rows)
	if strings.Contains(string(raw), "token") || strings.Contains(string(raw), "platform") {
		t.Fatalf("rows hold a token or platform: %s", raw)
	}

	// Read, the rows are past telling.
	var through uint64
	for _, f := range rows {
		through = max(through, f.Seq)
	}
	if err := y.AckUserInbox(y.ctx, "ada", through, "ada"); err != nil {
		t.Fatal(err)
	}
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due after the mail was read = %+v", rows)
	}
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 0 || stale != 2 {
		t.Fatalf("sweep = %d expired, %d stale, %v", expired, stale, err)
	}

	// Oldest first: the earlier letter's rows come before the later's.
	if err := y.Mail(y.ctx, "ada", "first", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	y.now = y.now.Add(time.Second)
	if err := y.Mail(y.ctx, "ada", "second", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	rows = y.due(t)
	if len(rows) != 4 || rows[0].Mail.Text != "first" || rows[1].Mail.Text != "first" ||
		rows[2].Mail.Text != "second" || rows[3].Mail.Text != "second" {
		t.Fatalf("due is not oldest first: %+v", rows)
	}
}

// An intent is queued per device of the user an ask is for, for as long as
// it is open. Answered or declined, the rows drop.
func TestFootQueuedWithAsk(t *testing.T) {
	y := newFootYard(t)
	bead := y.bead(t, "Move the links")
	first := y.ask(t, bead, "ada", "Move the links now?")
	y.ask(t, bead, "bob", "And the rest?")
	rows := y.due(t)
	got := footKinds(rows)
	slices.Sort(got)
	if len(rows) != 2 || strings.Join(got, "|") != "decision ada/phone|decision ada/tablet" {
		t.Fatalf("due = %v", got)
	}
	if a := rows[0].Ask; a == nil || a.Bead != bead || a.Ask != first || a.Question != "Move the links now?" || a.Asker != "yardmaster" {
		t.Fatalf("the ask = %+v", rows[0].Ask)
	}
	if err := y.AnswerAsk(y.ctx, bead, first, 1, "", "ada", false); err != nil {
		t.Fatal(err)
	}
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due after the answer = %+v", rows)
	}
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 0 || stale != 2 {
		t.Fatalf("sweep = %d expired, %d stale, %v", expired, stale, err)
	}

	second := y.ask(t, bead, "ada", "Tell the others?")
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due after the second ask = %+v", rows)
	}
	if err := y.DeclineAsk(y.ctx, bead, second, "not now", "ada"); err != nil {
		t.Fatal(err)
	}
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due after the decline = %+v", rows)
	}
}

// A put-aside ask reopened is reported as new: by its 24 hours, or by the
// asker's first note on the bead before them. A note past the 24 hours
// reopens nothing, and a second note tells nothing twice. An aside
// before the aside is no decision: it came back too.
func TestFootAsideReturn(t *testing.T) {
	y := newFootYard(t)

	// By its 24 hours.
	bead := y.bead(t, "Move the links")
	ask := y.ask(t, bead, "ada", "Move the links now?")
	if err := y.DeferAsk(y.ctx, bead, ask, "ada"); err != nil {
		t.Fatal(err)
	}
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due while aside = %+v", rows)
	}
	// The aside dropped the ask's rows with it; only the delayed wait.
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 0 || stale != 0 {
		t.Fatalf("sweep while aside = %d expired, %d stale, %v", expired, stale, err)
	}
	y.now = y.now.Add(25 * time.Hour)
	rows := y.due(t)
	if len(rows) != 2 || rows[0].Ask == nil || rows[0].Ask.Ask != ask {
		t.Fatalf("due at the return = %+v", rows)
	}
	for _, f := range rows {
		if found, err := y.FootLanded(y.ctx, f.EventID); err != nil || !found {
			t.Fatalf("land %s = %v, %v", f.EventID, found, err)
		}
	}

	// By the asker's first note before them.
	bead = y.bead(t, "Tell the others")
	ask = y.ask(t, bead, "ada", "Tell the others?")
	if err := y.DeferAsk(y.ctx, bead, ask, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := y.AddNote(y.ctx, bead, "the others are told", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	rows = y.due(t)
	if len(rows) != 2 || rows[0].Ask == nil || rows[0].Ask.Ask != ask {
		t.Fatalf("due at the asker's note = %+v", rows)
	}
	// The asker's note told the return: the delayed rows can never fire,
	// and drop now.
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 0 || stale != 2 {
		t.Fatalf("sweep after the asker's note = %d expired, %d stale, %v", expired, stale, err)
	}
	// A second note tells nothing twice.
	if err := y.AddNote(y.ctx, bead, "once more", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due after the second note = %+v", rows)
	}
	for _, f := range rows {
		if _, err := y.FootLanded(y.ctx, f.EventID); err != nil {
			t.Fatal(err)
		}
	}

	// A note past the 24 hours reopens nothing: the aside already came
	// back by its time, and its telling is the delayed row's.
	bead = y.bead(t, "After the hours")
	ask = y.ask(t, bead, "ada", "After the hours?")
	if err := y.DeferAsk(y.ctx, bead, ask, "ada"); err != nil {
		t.Fatal(err)
	}
	y.now = y.now.Add(25 * time.Hour)
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due at the return = %+v", rows)
	}
	if err := y.AddNote(y.ctx, bead, "too late", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due after the late note = %+v", rows)
	}
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 0 || stale != 0 {
		t.Fatalf("sweep after the late note = %d expired, %d stale, %v", expired, stale, err)
	}
	for _, f := range y.due(t) {
		if _, err := y.FootLanded(y.ctx, f.EventID); err != nil {
			t.Fatal(err)
		}
	}

	// Aside, returned, and aside again: the first aside is no decision,
	// and the second return is told.
	bead = y.bead(t, "Twice aside")
	ask = y.ask(t, bead, "ada", "Twice aside?")
	if err := y.DeferAsk(y.ctx, bead, ask, "ada"); err != nil {
		t.Fatal(err)
	}
	if err := y.AddNote(y.ctx, bead, "back it comes", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	for _, f := range y.due(t) {
		if _, err := y.FootLanded(y.ctx, f.EventID); err != nil {
			t.Fatal(err)
		}
	}
	// Aside again: the aside drops the first aside's rows with it.
	if err := y.DeferAsk(y.ctx, bead, ask, "ada"); err != nil {
		t.Fatal(err)
	}
	y.now = y.now.Add(25 * time.Hour)
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due at the second return = %+v", rows)
	}
}

// A return after the ask was answered, declined or taken back is decided,
// not news: no note of the asker reopens it, and no delayed row fires for
// it. The store's own rules refuse such an aside, so the notes and the
// row are written by hand.
func TestFootReturnAfterDecided(t *testing.T) {
	y := newFootYard(t)
	write := func(fn func(w *wtx) error) {
		t.Helper()
		if err := y.write(fn); err != nil {
			t.Fatal(err)
		}
	}
	note := func(uuid string, seq uint64, kind, author string, v any, at time.Time) {
		t.Helper()
		text, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		write(func(w *wtx) error {
			return put(w.Tx, bNotes, join(uuid, seqKey(seq)), Note{Bead: uuid, Kind: kind, Author: author, Text: string(text), At: at, Seq: seq})
		})
	}
	const uuid = "bead-for-decided"
	write(func(w *wtx) error {
		return put(w.Tx, bBeads, uuid, beadRow{Bead: Bead{UUID: uuid, ID: "test-1"}})
	})
	askText := Ask{Question: "Move them?", To: "ada", Context: []string{"They wait to be moved."}}
	note(uuid, 1, NoteAsk, "yardmaster", askText, y.now.Add(-26*time.Hour))
	note(uuid, 2, NoteAnswer, "ada", Answer{Ask: 1, Option: 1}, y.now.Add(-25*time.Hour))
	note(uuid, 3, NoteAnswer, "ada", Answer{Ask: 1, Later: true}, y.now.Add(-25*time.Hour))
	write(func(w *wtx) error {
		return w.queueFootAside("ada", uuid, 1, 3, y.now.Add(-time.Hour))
	})
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due for a return after an answer = %+v", rows)
	}
	// The asker's note reopens nothing either.
	n := Note{Bead: uuid, Kind: NoteNote, Author: "yardmaster", Text: "back?", At: y.now, Seq: 4}
	note(uuid, 4, NoteNote, "yardmaster", "back?", y.now)
	write(func(w *wtx) error {
		return w.queueAsideReturns(&beadRow{UUID: uuid}, n)
	})
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 0 || stale != 2 {
		t.Fatalf("sweep = %d expired, %d stale, %v", expired, stale, err)
	}
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due after the asker's note = %+v", rows)
	}
}

// A question another yard asked is queued with its delivery, and its
// returns are told as a local ask's: a deferral here by its 24 hours,
// and an aside there at the return its settle carried. A decision before
// the deferral, and a newer answer after it, leave the row to drop.
func TestFootForeignQueue(t *testing.T) {
	y := newFootYard(t)
	write := func(fn func(w *wtx) error) {
		t.Helper()
		if err := y.write(fn); err != nil {
			t.Fatal(err)
		}
	}
	const key = "k1\x00a1"
	rec := func() foreignAsk {
		return foreignAsk{
			Message: "a1", Key: "k1", To: "ada", From: "ym", Bead: "yd-links",
			Title: "Move the links", Seq: 3, Since: y.now.Add(-48 * time.Hour),
			Ask: Ask{Question: "Move the links now?", To: "ada", Context: []string{"They wait to be moved."}},
		}
	}
	write(func(w *wtx) error {
		if err := put(w.Tx, bPeers, "north", Peer{Key: "k1", Name: "north"}); err != nil {
			return err
		}
		return put(w.Tx, bPeerAsks, key, rec())
	})
	write(func(w *wtx) error {
		return w.queueFootAsk("ada", "", 0, key)
	})
	rows := y.due(t)
	if len(rows) != 2 || rows[0].Ask == nil || rows[0].Ask.Bead != "yd-links@north" || rows[0].Ask.Asker != "ym@north" {
		t.Fatalf("due at the delivery = %+v", rows)
	}
	for _, f := range rows {
		if _, err := y.FootLanded(y.ctx, f.EventID); err != nil {
			t.Fatal(err)
		}
	}

	// Deferred here: told at its 24 hours.
	at := y.now.Add(-25 * time.Hour)
	write(func(w *wtx) error {
		r := rec()
		r.Answers = []foreignAnswer{{Message: "m1", Answer: Answer{Ask: 3, Later: true}, By: "ada", At: at}}
		if err := put(w.Tx, bPeerAsks, key, r); err != nil {
			return err
		}
		return w.queueFootForeignAside("ada", key, "m1", at.Add(24*time.Hour))
	})
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due at the deferral's return = %+v", rows)
	}
	for _, f := range y.due(t) {
		if _, err := y.FootLanded(y.ctx, f.EventID); err != nil {
			t.Fatal(err)
		}
	}

	// A pick before the deferral, and a newer answer after it, are no
	// return: both rows drop.
	write(func(w *wtx) error {
		r := rec()
		r.Answers = []foreignAnswer{
			{Message: "m0", Answer: Answer{Ask: 3, Option: 1}, By: "ada", At: at},
			{Message: "m1", Answer: Answer{Ask: 3, Later: true}, By: "ada", At: at.Add(time.Hour)},
		}
		return put(w.Tx, bPeerAsks, key, r)
	})
	write(func(w *wtx) error {
		return w.queueFootForeignAside("ada", key, "m1", at.Add(25*time.Hour))
	})
	write(func(w *wtx) error {
		r := rec()
		r.Answers = []foreignAnswer{
			{Message: "m1", Answer: Answer{Ask: 3, Later: true}, By: "ada", At: at},
			{Message: "m2", Answer: Answer{Ask: 3, Later: true}, By: "ada", At: at.Add(time.Hour)},
		}
		if err := put(w.Tx, bPeerAsks, key, r); err != nil {
			return err
		}
		return w.queueFootForeignAside("ada", key, "m1", at.Add(24*time.Hour))
	})
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due for no return = %+v", rows)
	}
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 0 || stale != 4 {
		t.Fatalf("sweep = %d expired, %d stale, %v", expired, stale, err)
	}

	// Put aside there: told at the return the settle carried, and not
	// before it.
	past := y.now.Add(-time.Hour)
	write(func(w *wtx) error {
		r := rec()
		r.Answers = nil
		r.AsideAt, r.AsideUntil = &at, &past
		if err := put(w.Tx, bPeerAsks, key, r); err != nil {
			return err
		}
		return w.queueFootRemoteAside("ada", key, past)
	})
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due at the remote return = %+v", rows)
	}
	for _, f := range y.due(t) {
		if _, err := y.FootLanded(y.ctx, f.EventID); err != nil {
			t.Fatal(err)
		}
	}
	future := y.now.Add(time.Hour)
	write(func(w *wtx) error {
		r := rec()
		r.AsideAt, r.AsideUntil = &at, &future
		if err := put(w.Tx, bPeerAsks, key, r); err != nil {
			return err
		}
		return w.queueFootRemoteAside("ada", key, future)
	})
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due before the remote return = %+v", rows)
	}
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 0 || stale != 0 {
		t.Fatalf("sweep before the remote return = %d expired, %d stale, %v", expired, stale, err)
	}
}

// A row past its six hours drops untold. A failed send is tried again
// after a wait that doubles, and past eight tries the row goes. A send
// waits for its ack before it goes again, unless the link returned, and
// an answer for no row is nothing.
func TestFootSweepAndRetry(t *testing.T) {
	y := newFootYard(t)
	if err := y.Mail(y.ctx, "ada", "the tests are green", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	rows := y.due(t)
	if len(rows) != 2 {
		t.Fatalf("due = %+v", rows)
	}
	first, second := rows[0].EventID, rows[1].EventID

	kept, err := y.FootRetry(y.ctx, first)
	if err != nil || !kept {
		t.Fatalf("retry = %v, %v", kept, err)
	}
	if rows := y.due(t); len(rows) != 1 || rows[0].EventID != second {
		t.Fatalf("due while the first waits out its retry = %+v", rows)
	}
	y.now = y.now.Add(6 * time.Second)
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due after the retry waits out = %+v", rows)
	}

	if found, err := y.FootSent(y.ctx, first); err != nil || !found {
		t.Fatalf("sent = %v, %v", found, err)
	}
	if rows := y.due(t); len(rows) != 1 || rows[0].EventID != second {
		t.Fatalf("due while the first waits for its ack = %+v", rows)
	}
	y.now = y.now.Add(31 * time.Second)
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due after the ack waits out = %+v", rows)
	}

	for i := 0; i < 6; i++ {
		if kept, err := y.FootRetry(y.ctx, first); err != nil || !kept {
			t.Fatalf("retry %d = %v, %v", i+2, kept, err)
		}
	}
	if kept, err := y.FootRetry(y.ctx, first); err != nil || kept {
		t.Fatalf("the eighth retry = %v, %v", kept, err)
	}
	if rows := y.due(t); len(rows) != 1 || rows[0].EventID != second {
		t.Fatalf("due after the first gave up = %+v", rows)
	}

	if found, err := y.FootLanded(y.ctx, "no such event"); err != nil || found {
		t.Fatalf("landed unknown = %v, %v", found, err)
	}
	if kept, err := y.FootRetry(y.ctx, "no such event"); err != nil || kept {
		t.Fatalf("retry unknown = %v, %v", kept, err)
	}
	if found, err := y.FootSent(y.ctx, "no such event"); err != nil || found {
		t.Fatalf("sent unknown = %v, %v", found, err)
	}

	y.now = y.now.Add(7 * time.Hour)
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due past the age = %+v", rows)
	}
	if expired, stale, err := y.FootSweep(y.ctx); err != nil || expired != 1 || stale != 0 {
		t.Fatalf("sweep past the age = %d expired, %d stale, %v", expired, stale, err)
	}

	// A new link never saw what waits for an ack: it goes again.
	if err := y.Mail(y.ctx, "ada", "again", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	rows = y.due(t)
	if len(rows) != 2 {
		t.Fatalf("due = %+v", rows)
	}
	for _, f := range rows {
		if _, err := y.FootSent(y.ctx, f.EventID); err != nil {
			t.Fatal(err)
		}
	}
	if rows := y.due(t); len(rows) != 0 {
		t.Fatalf("due while waiting for acks = %+v", rows)
	}
	if err := y.FootLinkReturned(y.ctx); err != nil {
		t.Fatal(err)
	}
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due after the link returned = %+v", rows)
	}

	// A refusal ends the wait for the ack: the retry waits out RetryAt
	// alone.
	for _, f := range y.due(t) {
		if _, err := y.FootLanded(y.ctx, f.EventID); err != nil {
			t.Fatal(err)
		}
	}
	if err := y.Mail(y.ctx, "ada", "once more", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	rows = y.due(t)
	if len(rows) != 2 {
		t.Fatalf("due = %+v", rows)
	}
	if _, err := y.FootSent(y.ctx, rows[0].EventID); err != nil {
		t.Fatal(err)
	}
	if kept, err := y.FootRetry(y.ctx, rows[0].EventID); err != nil || !kept {
		t.Fatalf("retry = %v, %v", kept, err)
	}
	y.now = y.now.Add(6 * time.Second)
	if rows := y.due(t); len(rows) != 2 {
		t.Fatalf("due after the retry waits out = %+v", rows)
	}
}
