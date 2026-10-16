package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// pushUsers is a yard with the operator jo, the user ada, who is told
// through the source pushover, and the user bob, who is told nowhere.
func pushUsers(t *testing.T) (store.API, context.Context) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ada", "bob"} {
		if _, err := st.AddUser(ctx, name, "jo"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.PutSource(ctx, "pushover.yaml", sourceFile, "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: "pushover", store.UserEnvFile: "/home/ada/ada.env"}, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "fake"}, "jo"); err != nil {
		t.Fatal(err)
	}
	return st, ctx
}

func pushesOf(t *testing.T, st store.API) []store.Push {
	t.Helper()
	ps, err := st.Pushes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

// A push is queued with what lands in the inbox of a user who names a
// source: mail, the user's own mail too, and a notification. Nothing is
// queued for a user who names none, nor for a crew member. The list has
// what each is about and whether it was read.
func TestPushQueuedWithTheInbox(t *testing.T) {
	t.Parallel()
	st, ctx := pushUsers(t)
	for _, m := range [][3]string{{"ada", "the tests are green", "yardmaster"}, {"ada", "a note to myself", "ada"},
		{"bob", "for bob", "yardmaster"}, {"yardmaster", "for the crew", "ada"}} {
		if err := st.Mail(ctx, m[0], m[1], m[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ada", "bob"} {
		if err := st.AddSubscription(ctx, name, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll}, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-1", Title: "Which way round", Depot: "yard"}, "jo"); err != nil {
		t.Fatal(err)
	}
	// What ada did herself is no notification, and so no push.
	if _, err := st.CreateBead(ctx, store.NewBead{ID: "yard-2", Title: "Hers", Depot: "yard"}, "ada"); err != nil {
		t.Fatal(err)
	}
	ps := pushesOf(t, st)
	var got []string
	for _, p := range ps {
		if p.User != "ada" || !p.Unread() || p.At.IsZero() {
			t.Fatalf("push = %+v", p)
		}
		switch {
		case p.Mail != nil:
			got = append(got, p.Part+": "+p.Mail.From+": "+p.Mail.Text)
		case p.Note != nil:
			got = append(got, p.Part+": "+p.Note.Bead+" "+p.Note.Kind)
		}
	}
	if want := "mail: yardmaster: the tests are green|mail: ada: a note to myself|notification: yard-1 created"; strings.Join(got, "|") != want {
		t.Fatalf("pushes = %q, want %q", got, want)
	}

	// Read is by the part's own cursor.
	if err := st.AckUserInbox(ctx, "ada", ps[0].Seq); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); ps[0].Unread() || !ps[1].Unread() || !ps[2].Unread() {
		t.Fatalf("after the first mail was read: %+v", ps)
	}
	if err := st.AckUserNotifications(ctx, "ada", 1<<40); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); !ps[1].Unread() || ps[2].Unread() {
		t.Fatalf("after the notifications were read: %+v", ps)
	}
	if err := st.DropPushes(ctx, "ada", []uint64{ps[0].Seq, ps[2].Seq, 999}); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); len(ps) != 1 || ps[0].Mail.Text != "a note to myself" {
		t.Fatalf("after the read ones were dropped: %+v", ps)
	}

	// A user who is told nowhere any more has nothing waiting to be told,
	// and neither has one who is gone.
	if _, err := st.SetUser(ctx, "ada", map[string]string{store.UserNotify: ""}, "ada"); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); len(ps) != 0 {
		t.Fatalf("after notify= : %+v", ps)
	}
	if _, err := st.SetUser(ctx, "bob", map[string]string{store.UserNotify: "pushover"}, "bob"); err != nil {
		t.Fatal(err)
	}
	if err := st.Mail(ctx, "bob", "now bob is told", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); len(ps) != 1 || ps[0].User != "bob" {
		t.Fatalf("mail to bob, who names a source now: %+v", ps)
	}
	if err := st.DeleteUser(ctx, "bob", "jo"); err != nil {
		t.Fatal(err)
	}
	if ps = pushesOf(t, st); len(ps) != 0 {
		t.Fatalf("after bob was removed: %+v", ps)
	}
}

// PushTried keeps how a send ended on the user's record. A try that is
// made again stays, counted; a message that was taken ends the pushes and
// counts towards the hour; a failure for good ends them with push_failed,
// and a cause that stands is logged once.
func TestPushTried(t *testing.T) {
	t.Parallel()
	st, ctx := pushUsers(t)
	for _, text := range []string{"one", "two", "three", "four"} {
		if err := st.Mail(ctx, "ada", text, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	ps := pushesOf(t, st)
	seqs := func(i ...int) []uint64 {
		out := []uint64{}
		for _, n := range i {
			out = append(out, ps[n].Seq)
		}
		return out
	}
	at := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	retry := at.Add(30 * time.Second)

	if err := st.PushTried(ctx, "ada", seqs(0), store.PushResult{Reason: "lost"}); err == nil || !strings.Contains(err.Error(), "no way a send ends") {
		t.Fatalf("a reason there is none of: %v", err)
	}
	if err := st.PushTried(ctx, "ada", seqs(0), store.PushResult{Reason: store.PushRetry}); err == nil {
		t.Fatal("a retry without its time was taken")
	}
	if err := st.PushTried(ctx, "nobody", seqs(0), store.PushResult{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a user that is not there: %v", err)
	}

	res := store.PushResult{At: at, Source: "pushover", Reason: store.PushRetry, Status: 500, Error: "pushover answered 500", Tries: 1, RetryAt: &retry}
	if err := st.PushTried(ctx, "ada", seqs(0, 1), res); err != nil {
		t.Fatal(err)
	}
	now := pushesOf(t, st)
	if len(now) != 4 || now[0].Attempts != 1 || now[1].Error != "pushover answered 500" || !now[0].RetryAt.Equal(retry) || now[2].Attempts != 0 {
		t.Fatalf("after a failed try: %+v", now)
	}
	u, _ := st.GetUser(ctx, "ada")
	if l := u.LastPush; l == nil || l.Reason != store.PushRetry || l.Count != 2 || l.Status != 500 || !l.RetryAt.Equal(retry) || len(u.Pushed) != 0 {
		t.Fatalf("the record after a failed try: %+v", u)
	}

	if err := st.PushTried(ctx, "ada", seqs(0, 1), store.PushResult{At: at, Source: "pushover", Status: 200, Tries: 2}); err != nil {
		t.Fatal(err)
	}
	u, _ = st.GetUser(ctx, "ada")
	if l := u.LastPush; !l.OK() || l.Count != 2 || l.Tries != 2 || len(u.Pushed) != 1 || !u.Pushed[0].Equal(at) || len(pushesOf(t, st)) != 2 {
		t.Fatalf("the record after a message was taken: %+v", u)
	}
	// The record keeps the times the bound is counted from, and no more.
	for i := range store.PushesPerHour + 5 {
		if err := st.Mail(ctx, "ada", "more", "yardmaster"); err != nil {
			t.Fatal(err)
		}
		all := pushesOf(t, st)
		if err := st.PushTried(ctx, "ada", []uint64{all[len(all)-1].Seq}, store.PushResult{At: at.Add(time.Duration(i+1) * time.Minute), Source: "pushover", Status: 200}); err != nil {
			t.Fatal(err)
		}
	}
	if u, _ = st.GetUser(ctx, "ada"); len(u.Pushed) != store.PushesPerHour || !u.Pushed[0].Equal(at.Add(6*time.Minute)) {
		t.Fatalf("the times kept: %v", u.Pushed)
	}
	// Reading the inbox rewrites the record, and keeps them.
	if err := st.AckUserNotifications(ctx, "ada", 1<<40); err != nil {
		t.Fatal(err)
	}
	if u, _ = st.GetUser(ctx, "ada"); u.LastPush == nil || len(u.Pushed) != store.PushesPerHour {
		t.Fatalf("after an acknowledgement: %+v", u)
	}

	gone := "user ada: env_file /home/ada/ada.env is not there"
	for _, i := range []int{2, 3} {
		if err := st.PushTried(ctx, "ada", seqs(i), store.PushResult{At: at, Source: "pushover", Reason: store.PushNotSetUp, Error: gone}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Mail(ctx, "ada", "five", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	five := pushesOf(t, st)
	if len(five) != 1 {
		t.Fatalf("left: %+v", five)
	}
	if err := st.PushTried(ctx, "ada", []uint64{five[0].Seq}, store.PushResult{At: at, Source: "pushover", Reason: store.PushRefused, Status: 400, Error: "pushover answered 400", Tries: 1}); err != nil {
		t.Fatal(err)
	}
	evs, err := st.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var failed []map[string]any
	for _, e := range evs {
		if e.Kind == "push_failed" {
			if e.Bead != nil || e.Actor != "yard" {
				t.Fatalf("push_failed = %+v", e)
			}
			failed = append(failed, e.Data)
		}
	}
	raw, _ := json.Marshal(failed)
	want := `[{"error":"` + gone + `","reason":"not_set_up","seqs":[` + jsonSeq(ps[2].Seq) + `],"source":"pushover","tries":0,"user":"ada"},` +
		`{"error":"pushover answered 400","reason":"refused","seqs":[` + jsonSeq(five[0].Seq) + `],"source":"pushover","status":400,"tries":1,"user":"ada"}]`
	if string(raw) != want {
		t.Fatalf("push_failed events =\n%s\nwant\n%s", raw, want)
	}
}

func jsonSeq(n uint64) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

// What is told of a notification where no link is sent is its text
// without the URL a hook delivery's ends with.
func TestNotificationSaid(t *testing.T) {
	t.Parallel()
	n := store.Notification{Text: `hook (all) linear Issue create by Jane on "Web view": first words  https://linear.app/acme/issue/YRD-1`,
		URL: "https://linear.app/acme/issue/YRD-1"}
	if got := n.Said(); got != `hook (all) linear Issue create by Jane on "Web view": first words` {
		t.Fatalf("Said = %q", got)
	}
	n = store.Notification{Text: "yard-1 closed (all) by jo"}
	if got := n.Said(); got != n.Text {
		t.Fatalf("Said = %q", got)
	}
}

// parts are the parts the pushes are for, in their order.
func parts(ps []store.Push) string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = p.Part
	}
	return strings.Join(out, " ")
}

// A user says which parts they are told of outside the yard. Of the others
// nothing is queued, and what waited of one they are told of no more is
// dropped. A name that is no part is refused with the three, and naming
// none is all of them again.
func TestUserTellsWhichPartsArePushed(t *testing.T) {
	t.Parallel()
	st, ctx := pushUsers(t)
	if err := st.AddSubscription(ctx, "ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll}, "ada"); err != nil {
		t.Fatal(err)
	}
	// One of each part lands with ada: a mail, a bead's notification, and
	// an ask, whose note is a notification too.
	each := func(title string) {
		t.Helper()
		if err := st.Mail(ctx, "ada", "about "+title, "yardmaster"); err != nil {
			t.Fatal(err)
		}
		b, err := st.CreateBead(ctx, store.NewBead{Title: title}, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddAsk(ctx, b.ID, askFor("ada", title+"?"), "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	set := func(v string) *store.User {
		t.Helper()
		u, err := st.SetUser(ctx, "ada", map[string]string{store.UserTell: v}, "ada")
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	clear := func() {
		t.Helper()
		var seqs []uint64
		for _, p := range pushesOf(t, st) {
			seqs = append(seqs, p.Seq)
		}
		if err := st.DropPushes(ctx, "ada", seqs); err != nil {
			t.Fatal(err)
		}
	}

	// Unset, as before there was the setting: all three, the ask in place
	// of its note's notification.
	each("First")
	if got := parts(pushesOf(t, st)); got != "mail notification ask" {
		t.Fatalf("with no tell: %q", got)
	}
	clear()

	if u := set("notifications"); strings.Join(u.Tell, ",") != "notifications" {
		t.Fatalf("tell=notifications: %+v", u)
	}
	each("Second")
	ps := pushesOf(t, st)
	if got := parts(ps); got != "notification notification" || ps[0].Note == nil || ps[1].Note == nil {
		t.Fatalf("with tell=notifications: %q %+v", got, ps)
	}

	_, err := st.SetUser(ctx, "ada", map[string]string{store.UserTell: "mail,beads"}, "ada")
	if err == nil || !strings.Contains(err.Error(), `user ada: "beads" is no part a user is told of outside the yard`) ||
		!strings.Contains(err.Error(), "notifications, mail, asks") {
		t.Fatalf("a part that is none: %v", err)
	}
	if u, _ := st.GetUser(ctx, "ada"); strings.Join(u.Tell, ",") != "notifications" || len(pushesOf(t, st)) != 2 {
		t.Fatalf("a refused tell changed something: %+v", u)
	}

	// Kept in the parts' own order, each once; the notifications that
	// waited are not told any more.
	if u := set(" asks, mail,asks"); strings.Join(u.Tell, ",") != "mail,asks" {
		t.Fatalf("tell=asks,mail: %+v", u)
	}
	if ps = pushesOf(t, st); len(ps) != 0 {
		t.Fatalf("what waited of the notifications: %+v", ps)
	}
	each("Third")
	if got := parts(pushesOf(t, st)); got != "mail ask" {
		t.Fatalf("with tell=mail,asks: %q", got)
	}
	clear()

	if u := set(""); u.Tell != nil {
		t.Fatalf("tell= : %+v", u)
	}
	each("Fourth")
	if got := parts(pushesOf(t, st)); got != "mail notification ask" {
		t.Fatalf("with tell= : %q", got)
	}

	evs, err := st.EventsSince(ctx, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, e := range evs {
		if e.Kind == "user_set" && e.Data[store.UserTell] != nil {
			said = append(said, e.Data[store.UserTell].(string))
		}
	}
	if strings.Join(said, "|") != "notifications|mail,asks|" {
		t.Fatalf("user_set events = %q", said)
	}
}
