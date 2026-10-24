package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

func askFor(to, question string) store.Ask {
	return store.Ask{
		Question: question,
		To:       to,
		Context:  []string{"The move was tested on a copy and passed."},
		Options: []store.AskOption{
			{Label: "Now", Consequence: "the links move tonight"},
			{Label: "After Friday", Consequence: "the old source stays until then"},
		},
	}
}

// An ask for a user who names a source is queued to be told with the ask,
// as mail is with the mail: once, for the person it is for, and for as long
// as it is open. Answered or put aside, it is not unread any more, and a
// notification of the same note does not make it two.
func TestPushQueuedWithAnAsk(t *testing.T) {
	t.Parallel()
	st, ctx := pushUsers(t)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "Move the links"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	first, err := st.AddAsk(ctx, b.ID, askFor("ada", "Move the links now?"), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	// Bob is told nowhere.
	if _, err := st.AddAsk(ctx, b.ID, askFor("bob", "And the rest?"), "yardmaster"); err != nil {
		t.Fatal(err)
	}
	ps := pushesOf(t, st)
	if len(ps) != 1 || ps[0].User != "ada" || ps[0].Part != store.PushAsk || !ps[0].Unread() || ps[0].At.IsZero() {
		t.Fatalf("pushes = %+v", ps)
	}
	if a := ps[0].Ask; a == nil || a.Bead != b.ID || a.BeadTitle != "Move the links" || a.Ask != first ||
		a.Question != "Move the links now?" || a.Asker != "yardmaster" {
		t.Fatalf("the ask = %+v", ps[0].Ask)
	}

	// With a subscription that makes a notification of the note, the
	// question is still one push, and it is the ask's.
	if err := st.AddSubscription(ctx, "ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll}, "ada"); err != nil {
		t.Fatal(err)
	}
	second, err := st.AddAsk(ctx, b.ID, askFor("ada", "Tell the others?"), "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	box, err := st.UserNotifications(ctx, "ada", 0, "ada")
	if err != nil || len(box.Notifications) != 1 {
		t.Fatalf("the notification of the second ask: %+v, %v", box, err)
	}
	if ps = pushesOf(t, st); len(ps) != 2 || ps[1].Part != store.PushAsk || ps[1].Ask == nil || ps[1].Ask.Ask != second || ps[1].Note != nil {
		t.Fatalf("pushes after the second ask = %+v", ps)
	}
	// Reading the inbox does not answer a question.
	if err := st.AckUserNotifications(ctx, "ada", 1<<40, "ada"); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); !ps[0].Unread() || !ps[1].Unread() {
		t.Fatalf("after the inbox was read: %+v", ps)
	}

	if err := st.AnswerAsk(ctx, b.ID, first, 1, "", "ada", false); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); ps[0].Unread() || ps[0].Ask != nil || !ps[1].Unread() {
		t.Fatalf("after the first was answered: %+v", ps)
	}
	if err := st.DeferAsk(ctx, b.ID, second, "ada"); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); ps[1].Unread() {
		t.Fatalf("after the second was put aside: %+v", ps)
	}
	if err := st.DropPushes(ctx, "ada", []uint64{ps[0].Seq, ps[1].Seq}); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); len(ps) != 0 {
		t.Fatalf("after they were dropped: %+v", ps)
	}
}

// A question another yard asked is told by the yard that took it into the
// person's queue, under the name this yard has for the link, until the
// person deals with it here or the asking yard puts it aside or ends it
// there. A pending push is not unread while the copy stands aside.
func TestPushQueuedWithAForeignAsk(t *testing.T) {
	t.Parallel()
	y := newPeerYard(t, "http://north.test")
	for _, name := range []string{"mckean", "bob"} {
		if _, err := y.api.AddUser(y.ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := y.api.PutSource(y.ctx, "pushover.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := y.api.SetUser(y.ctx, "mckean", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/home/mckean/mckean.env"}, "mckean"); err != nil {
		t.Fatal(err)
	}
	y.allow(t, store.PeerAllow{Receive: []string{"ask"}, Send: []string{"ask"}, Expose: []string{"mckean", "bob"}})
	since := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	for i, to := range []string{"mckean", "bob", "mckean"} {
		id := []string{"a1", "a2", "a3"}[i]
		if got := y.take(t, askMsg(id, validForeignAsk(to, "yardmaster", "yd-links", uint64(3+i), since))); got[0].Refused != "" {
			t.Fatalf("ask %s = %+v", id, got)
		}
	}
	ps := pushesOf(t, y.api)
	if len(ps) != 2 || ps[0].User != "mckean" || ps[0].Part != store.PushAsk || !ps[0].Unread() || !ps[1].Unread() {
		t.Fatalf("pushes = %+v", ps)
	}
	if a := ps[0].Ask; a.Bead != "yd-links@north" || a.Ask != 3 || a.Question != "Move the links now?" || a.Asker != "yardmaster@north" {
		t.Fatalf("the ask = %+v", a)
	}
	if _, err := y.api.RenamePeer(y.ctx, "north", "upnorth", "jo"); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, y.api); ps[0].Ask == nil || ps[0].Ask.Asker != "yardmaster@upnorth" {
		t.Fatalf("after the rename: %+v", ps)
	}
	if _, err := y.api.RenamePeer(y.ctx, "upnorth", "north", "jo"); err != nil {
		t.Fatal(err)
	}
	returnAt := time.Now().UTC().Add(24 * time.Hour)
	body, err := json.Marshal(struct {
		V        int       `json:"v"`
		Bead     string    `json:"bead"`
		Seq      uint64    `json:"seq"`
		Settled  string    `json:"settled"`
		ReturnAt time.Time `json:"return_at"`
	}{V: 1, Bead: "yd-links", Seq: 5, Settled: store.SettledAside, ReturnAt: returnAt})
	if err != nil {
		t.Fatal(err)
	}
	if got := y.take(t, store.PeerMessage{ID: "s1", Kind: store.PeerAsk, Re: "a3", Body: body}); got[0].Refused != "" {
		t.Fatalf("the aside = %+v", got)
	}
	if ps = pushesOf(t, y.api); len(ps) != 2 || !ps[0].Unread() || ps[1].Unread() {
		t.Fatalf("while the foreign ask stands aside: %+v", ps)
	}

	// Picked here, the first is dealt with; the second ends in the yard
	// that asked it.
	if err := y.api.AnswerAsk(y.ctx, "yd-links@north", 3, 1, "", "mckean", false); err != nil {
		t.Fatal(err)
	}
	settle := store.PeerMessage{ID: "s2", Kind: store.PeerAsk, Re: "a3", Body: []byte(`{"v":1,"bead":"yd-links","seq":5,"settled":"answered"}`)}
	if got := y.take(t, settle); got[0].Refused != "" {
		t.Fatalf("the settle = %+v", got)
	}
	if ps = pushesOf(t, y.api); len(ps) != 2 || ps[0].Unread() || ps[1].Unread() {
		t.Fatalf("after both ended: %+v", ps)
	}
}
