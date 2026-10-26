package dispatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// pushSource is a source that tells a person as Pushover does: the yard's
// token and the person's key in the body, with Pushover's bounds.
const pushSource = `name: push
base: BASE
credential: { env: PUSH_TOKEN }
calls:
  message:
    kind: update
    params:
      user:  { type: string, pattern: "^u-[a-z0-9]{6}$" }
      title: { type: string, max: 250 }
      text:  { type: string, max: 1024 }
    request:
      method: POST
      path: /messages.json
      body: { token: "{credential}", user: "{user}", title: "{title}", message: "{text}" }
notify:
  call: message
  address: { param: user, env: PUSH_USER }
  title: title
  text: text
`

const (
	pushToken   = "tok-41c9-of-the-yard"
	pushAddress = "u-9f2k4x"

	pushFlow = `
backlog -> new
new     -> decide on question | review on done
decide  -> new
review terminal
`
)

// pushYard is a yard with a stand-in for a service that takes messages,
// the source push that calls it, and the user ada, who is told through it.
type pushYard struct {
	api    store.API
	d      *dispatch.Dispatcher
	srv    *httptest.Server
	status atomic.Int64 // what the stand-in answers
	mu     sync.Mutex
	got    []map[string]string // the bodies it was sent
	now    time.Time
	log    bytes.Buffer
	env    string // ada's env_file
	home   string // the yard's home, the store's and the daemon's
	// secrets are what, beside the source's token and ada's address, no
	// event or line of the log may hold: a test that sets up another
	// source adds its own.
	secrets []string
}

// pushFuse is how long a test yard lets one send from a pass take: a fuse
// against a send that hangs, not a window its start must fit in. A pass sends
// synchronously, so there is no effect to wait on. Under -race at load the
// start of the image command, a script written a moment before, took longer
// than the daemon's 10 s: the send was cut, the words went alone, and the test
// failed on a file the command never lived to write (yardr-tfmr).
const pushFuse = 2 * time.Minute

func newPushYard(t *testing.T) *pushYard {
	t.Helper()
	ctx := context.Background()
	// The store stamps a push with the time it has; the daemon's clock
	// starts there and is moved by the test.
	home := t.TempDir()
	y := &pushYard{api: storetest.NewIn(t, home), now: time.Now(), home: home}
	y.status.Store(200)
	y.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]string
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("the body is no JSON: %s", raw)
		}
		y.mu.Lock()
		y.got = append(y.got, body)
		y.mu.Unlock()
		status := int(y.status.Load())
		if body["message"] == "" {
			// Pushover refuses a push without a message, and for good.
			status = http.StatusBadRequest
		}
		w.WriteHeader(status)
		io.WriteString(w, `{"status":1}`)
	}))
	t.Cleanup(y.srv.Close)
	file := func(name, text string) string {
		f := filepath.Join(t.TempDir(), name)
		must(t, os.WriteFile(f, []byte(text), 0o600))
		return f
	}
	y.env = file("ada.env", "PUSH_USER="+pushAddress+"\n")
	must(t, y.api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	_, err := y.api.AddUser(ctx, "ada", "jo")
	must(t, err)
	_, err = y.api.PutSource(ctx, "push.yaml", strings.Replace(pushSource, "BASE", y.srv.URL, 1), "jo")
	must(t, err)
	_, err = y.api.SetSource(ctx, "push", store.SourceEnvFile, file("push.env", "PUSH_TOKEN="+pushToken+"\n"), "jo")
	must(t, err)
	_, err = y.api.SetUser(ctx, "ada", map[string]string{store.UserNotify: "push", store.UserEnvFile: y.env}, "ada")
	must(t, err)
	_, err = y.api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "fake"}, "jo")
	must(t, err)
	y.d = &dispatch.Dispatcher{Store: y.api, Home: home, Runners: map[string]dispatch.Runner{"fake": &fakeRunner{dead: map[string]bool{}}},
		Log: slog.New(slog.NewTextHandler(&y.log, nil)), PushTimeout: pushFuse}
	dispatch.SetClock(y.d, func() time.Time { return y.now })
	t.Cleanup(func() { y.noSecrets(t) })
	return y
}

func (y *pushYard) mail(t *testing.T, from, text string) {
	t.Helper()
	must(t, y.api.Mail(context.Background(), "ada", text, from))
}

// pass is one pass of the daemon after d has gone by.
func (y *pushYard) pass(t *testing.T, d time.Duration) {
	t.Helper()
	y.now = y.now.Add(d)
	must(t, y.d.Reconcile(context.Background()))
}

// sent is what the stand-in was sent, as "title | text".
func (y *pushYard) sent() []string {
	y.mu.Lock()
	defer y.mu.Unlock()
	out := []string{}
	for _, b := range y.got {
		out = append(out, b["title"]+" | "+b["message"])
	}
	return out
}

func (y *pushYard) queued(t *testing.T) []store.Push {
	t.Helper()
	ps, err := y.api.Pushes(context.Background())
	must(t, err)
	return ps
}

// failed are the push_failed events, each as its data in JSON.
func (y *pushYard) failed(t *testing.T) []string {
	t.Helper()
	evs, err := y.api.EventsSince(context.Background(), 0, 1000)
	must(t, err)
	out := []string{}
	for _, e := range evs {
		if e.Kind == "push_failed" {
			raw, _ := json.Marshal(e.Data)
			out = append(out, string(raw))
		}
	}
	return out
}

func (y *pushYard) last(t *testing.T) *store.PushResult {
	t.Helper()
	u, err := y.api.GetUser(context.Background(), "ada")
	must(t, err)
	if u.LastPush == nil {
		t.Fatal("ada's record has no last push")
	}
	return u.LastPush
}

// noSecrets fails if the source's token or ada's address is in an event or
// in a line the daemon logged. Every test of a pushYard ends with it.
func (y *pushYard) noSecrets(t *testing.T) {
	t.Helper()
	evs, err := y.api.EventsSince(context.Background(), 0, 1000)
	must(t, err)
	raw, _ := json.Marshal(evs)
	for what, text := range map[string]string{"the events": string(raw), "the daemon's log": y.log.String()} {
		for _, secret := range append([]string{pushToken, pushAddress}, y.secrets...) {
			if strings.Contains(text, secret) {
				t.Errorf("%s hold %q:\n%s", what, secret, text)
			}
		}
	}
}

// Mail to a user, a timer the user set for themselves and a notification
// of a bead entering decide each reach the source once, after the grace,
// with who, what and the start of the text, and with the token and the
// address the files hold. A mail of files alone has no text, and the
// service refuses a push without one for good: it is told as
// "(with files)", and the push ends told.
func TestPushTellsOfMailATimerAndABeadInDecide(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	ctx := context.Background()

	y.mail(t, "yardmaster", "the tests are green\n"+strings.Repeat("x", 300))
	y.pass(t, store.PushGrace-time.Second)
	want(t, "sent within the grace", y.sent(), []string{})
	y.pass(t, 2*time.Second)
	y.pass(t, time.Minute)
	want(t, "the mail", y.sent(), []string{"mail from yardmaster | the tests are green\n" + strings.Repeat("x", 179) + "…"})
	if got := y.got[0]; got["token"] != pushToken || got["user"] != pushAddress {
		t.Fatalf("the stand-in got %v", got)
	}
	if l := y.last(t); !l.OK() || l.Status != 200 || l.Source != "push" || l.Count != 1 || l.Tries != 1 {
		t.Fatalf("last push = %+v", l)
	}
	want(t, "left in the queue", len(y.queued(t)), 0)

	// A timer ada set for herself is mail from herself, and is told.
	_, err := y.api.AddTimer(ctx, store.NewTimer{Reason: "look at the shed", Due: time.Now().Add(time.Hour)}, "ada")
	must(t, err)
	y.pass(t, 2*time.Hour)
	y.pass(t, time.Minute)
	if got := y.sent(); len(got) != 2 || !strings.HasPrefix(got[1], "timer from ada | timer: look at the shed") {
		t.Fatalf("after the timer: %q", got)
	}

	// So is her own mail to herself; only an event of hers that a
	// subscription matched is not (the store makes no notification of it).
	must(t, y.api.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"))
	flow, err := store.ParseFlow(pushFlow)
	must(t, err)
	flow.Name = "yard"
	_, err = y.api.PutFlow(ctx, *flow, "jo")
	must(t, err)
	must(t, y.api.SetFlowPointer(ctx, "yard", "", "yard", "jo"))
	must(t, y.api.AddSubscription(ctx, "ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"stage": "decide"}}, "ada"))
	for _, id := range []string{"yard-1", "yard-2"} {
		_, err = y.api.CreateBead(ctx, store.NewBead{ID: id, Title: "Which way round", Depot: "yard", Stage: "new"}, "jo")
		must(t, err)
	}
	_, err = y.api.Advance(ctx, "yard-1", "decide", store.Expect{}, "builder")
	must(t, err)
	_, err = y.api.Advance(ctx, "yard-2", "decide", store.Expect{}, "ada")
	must(t, err)
	y.pass(t, 3*time.Hour)
	y.pass(t, time.Minute)
	got := y.sent()
	if len(got) != 3 || !strings.HasPrefix(got[2], "yard-1 advanced | yard-1 advanced (all) by builder ") ||
		!strings.Contains(got[2], "to=decide") || !strings.HasSuffix(got[2], `on "Which way round"`) {
		t.Fatalf("after the bead entered decide: %q", got)
	}

	// A mail of one file and no words, through the plain call (this source
	// has no picture command). The clock is hours ahead of the store's,
	// which stamps the mail now: it starts again from there.
	must(t, y.api.Mail(ctx, "ada", "", "yardmaster", y.putBlob(t, pushPNG, "shot.png")))
	y.now = time.Now()
	y.pass(t, store.PushGrace+time.Second)
	want(t, "the mail of a file alone", y.sent()[3:], []string{"mail from yardmaster | (with files)"})
	if l := y.last(t); !l.OK() || l.Status != 200 {
		t.Fatalf("last push = %+v", l)
	}
	want(t, "left in the queue", len(y.queued(t)), 0)
	want(t, "push_failed events", y.failed(t), []string{})
}

// What the user acknowledged within the grace is not told, mail or
// notification, and leaves nothing in the queue.
func TestPushNotForWhatWasRead(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	ctx := context.Background()
	y.mail(t, "yardmaster", "one")
	y.mail(t, "yardmaster", "two")
	box, err := y.api.UserInbox(ctx, "ada", 0, "ada")
	must(t, err)
	must(t, y.api.AckUserInbox(ctx, "ada", box.Mail[0].Seq, "ada"))
	y.pass(t, store.PushGrace+time.Second)
	want(t, "told of the unread one alone", y.sent(), []string{"mail from yardmaster | two"})

	y.mail(t, "yardmaster", "three")
	must(t, y.api.AckUserInbox(ctx, "ada", 1<<40, "ada"))
	y.pass(t, time.Hour)
	want(t, "told of what was read", len(y.sent()), 1)
	want(t, "left in the queue", len(y.queued(t)), 0)
	want(t, "push_failed events", y.failed(t), []string{})
}

// An ask for the user is told as mail is: once, after the grace, with who
// asks and the question, and nothing of its context or options. One that
// was answered within the grace is not told. With mail in the same burst it
// is one message, which does not call the place the inbox.
func TestPushTellsOfAnAsk(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	ctx := context.Background()
	b, err := y.api.CreateBead(ctx, store.NewBead{Title: "Move the links"}, "yardmaster")
	must(t, err)
	ask := func(question string) uint64 {
		t.Helper()
		seq, err := y.api.AddAsk(ctx, b.ID, store.Ask{Question: question, To: "ada",
			Context: []string{"The secret of the context."},
			Options: []store.AskOption{{Label: "Now", Consequence: "they move tonight"}}}, "yardmaster")
		must(t, err)
		return seq
	}

	ask("Move the links now?\n" + strings.Repeat("x", 300))
	y.pass(t, store.PushGrace-time.Second)
	want(t, "sent within the grace", y.sent(), []string{})
	y.pass(t, 2*time.Second)
	y.pass(t, time.Hour)
	want(t, "the ask", y.sent(), []string{"yardmaster asks | Move the links now?\n" + strings.Repeat("x", 179) + "…"})
	want(t, "left in the queue", len(y.queued(t)), 0)

	// Answered within the 20 seconds: not told.
	seq := ask("And the rest?")
	must(t, y.api.AnswerAsk(ctx, b.ID, seq, 1, "", "ada", false))
	y.pass(t, time.Hour)
	want(t, "told of an ask that was answered", len(y.sent()), 1)
	want(t, "left in the queue", len(y.queued(t)), 0)

	y.mail(t, "yardmaster", "see the question")
	ask("Tell the others?")
	y.pass(t, time.Hour)
	if got := y.sent(); len(got) != 2 || got[1] != "2 new in yardr | mail from yardmaster, yardmaster asks" {
		t.Fatalf("mail and an ask: %q", got)
	}
	want(t, "push_failed events", y.failed(t), []string{})
}

// Five in a burst are one message, which counts them and names each kind
// once; so is everything that waited while the daemon was away.
func TestPushBurstIsOneMessage(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	for i := range 4 {
		y.mail(t, "yardmaster", fmt.Sprint("mail ", i))
	}
	y.mail(t, "jo", "and one from jo")
	y.settle(t)
	want(t, "a burst", y.sent(), []string{"5 new in your yardr inbox | mail from yardmaster (4), mail from jo"})
	if l := y.last(t); l.Count != 5 {
		t.Fatalf("last push = %+v", l)
	}

	// A message does not go while more is still coming: the first is past
	// its grace here, the second is not. The store stamps a mail with the time
	// it is made, to the microsecond, so the clock is set from the two stamps,
	// with the second made after the first's, and not from how long the test
	// took to get here.
	y.mail(t, "yardmaster", "first")
	stamp := y.queued(t)[0].At
	until(t, "the clock to pass the first mail's stamp", func() bool { return time.Now().Truncate(time.Microsecond).After(stamp) })
	y.mail(t, "yardmaster", "second")
	ps := y.queued(t)
	y.now = ps[0].At.Add(store.PushGrace)
	y.pass(t, 0)
	want(t, "sent while the burst was coming", len(y.sent()), 1)
	y.settle(t)
	if got := y.sent(); len(got) != 2 || got[1] != "2 new in your yardr inbox | mail from yardmaster (2)" {
		t.Fatalf("after the burst: %q", got)
	}
	t.Run("mail image", testPushTellsMailImage)
	t.Run("done bead image", testPushTellsDoneBeadScreenshot)
	t.Run("first image across batch", testPushFirstImageAcrossBatch)
	t.Run("pushover pack pictures", testPushoverPackPictures)
}

// A message waits for the grace of its newest item, and no longer than
// three graces after its oldest: mail that keeps coming does not hold it
// back for good.
func TestPushReady(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ newest, want time.Duration }{
		{0, store.PushGrace},
		{5 * time.Second, store.PushGrace + 5*time.Second},
		{2 * store.PushGrace, 3 * store.PushGrace},
		{10 * store.PushGrace, 3 * store.PushGrace},
	} {
		if got := dispatch.PushReady(t0, t0.Add(tc.newest)).Sub(t0); got != tc.want {
			t.Errorf("the newest %s after the oldest: ready after %s, want %s", tc.newest, got, tc.want)
		}
	}
}

// A source that answers 500 is asked again after the backoff, and the
// message goes once; one that answers 400 is not asked again, and
// push_failed says so without the address, the token or the text. A send
// is held to the Dispatcher's PushTimeout: one whose bound ran out before
// the call asks nobody and is tried again after the backoff, and a
// Dispatcher with none set holds it to 10 s.
func TestPushTriedAgainOn500AndNotOn400(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	y.status.Store(500)
	y.mail(t, "yardmaster", "the tests are green")
	y.pass(t, store.PushGrace+time.Second)
	want(t, "asked", len(y.sent()), 1)
	if l := y.last(t); l.Reason != store.PushRetry || l.Status != 500 || l.RetryAt == nil {
		t.Fatalf("last push = %+v", l)
	}
	y.pass(t, time.Second) // within the backoff
	want(t, "asked within the backoff", len(y.sent()), 1)
	// Mail that comes meanwhile waits with it.
	y.mail(t, "yardmaster", "and the review is in")
	y.pass(t, time.Second)
	want(t, "asked for new mail within the backoff", len(y.sent()), 1)

	y.status.Store(200)
	y.pass(t, time.Minute)
	y.pass(t, time.Minute)
	want(t, "asked after the backoff", y.sent()[1:], []string{"2 new in your yardr inbox | mail from yardmaster (2)"})
	if l := y.last(t); !l.OK() || l.Tries != 2 || l.Count != 2 {
		t.Fatalf("last push = %+v", l)
	}
	want(t, "push_failed events", y.failed(t), []string{})

	y.status.Store(400)
	y.mail(t, "yardmaster", "a third")
	y.pass(t, 2*time.Hour)
	y.pass(t, 2*time.Hour)
	want(t, "asked after a 400", len(y.sent()), 3)
	want(t, "left in the queue", len(y.queued(t)), 0)
	fails := y.failed(t)
	if len(fails) != 1 || !strings.Contains(fails[0], `"reason":"refused"`) || !strings.Contains(fails[0], `"status":400`) ||
		!strings.Contains(fails[0], `"user":"ada"`) || !strings.Contains(fails[0], `"source":"push"`) || strings.Contains(fails[0], "third") {
		t.Fatalf("push_failed = %s", fails)
	}
	if l := y.last(t); l.Reason != store.PushRefused || l.Status != 400 {
		t.Fatalf("last push = %+v", l)
	}

	// A negative bound has run out before the call is made, on any machine.
	y.status.Store(200)
	y.d.PushTimeout = -time.Second
	y.mail(t, "yardmaster", "a fourth")
	y.now = time.Now()
	y.pass(t, store.PushGrace+time.Second)
	want(t, "asked under a bound that ran out", len(y.sent()), 3)
	if l := y.last(t); l.Reason != store.PushRetry || l.RetryAt == nil {
		t.Fatalf("last push = %+v", l)
	}
	y.d.PushTimeout = 0
	y.pass(t, time.Minute)
	want(t, "asked after the backoff", y.sent()[3:], []string{"mail from yardmaster | a fourth"})
	want(t, "the bound with none set", y.d.PushTimeout, 10*time.Second)
}

// With the source down the inbox has everything at once, the push is tried
// with a growing wait, and given up after PushTries with one event; one
// that waited longer than PushMaxAge is not sent at all.
func TestPushWhileTheSourceIsDown(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	ctx := context.Background()
	y.srv.Close()
	y.mail(t, "yardmaster", "one")
	y.mail(t, "yardmaster", "two")
	box, err := y.api.UserInbox(ctx, "ada", 0, "ada")
	must(t, err)
	want(t, "unread in the inbox", box.Unread, 2)

	y.pass(t, store.PushGrace+time.Second)
	ps := y.queued(t)
	if len(ps) != 2 || ps[0].Attempts != 1 || ps[0].RetryAt == nil || !strings.Contains(ps[0].Error, "no answer") {
		t.Fatalf("after the first try: %+v", ps)
	}
	first := ps[0].RetryAt.Sub(y.now)
	y.pass(t, first+time.Second)
	if ps = y.queued(t); ps[0].Attempts != 2 || ps[0].RetryAt.Sub(y.now) != 2*first {
		t.Fatalf("after the second try: %+v (the first wait was %s)", ps, first)
	}
	want(t, "push_failed while it is tried", y.failed(t), []string{})
	for range dispatch.PushTries - 2 {
		y.pass(t, y.queued(t)[0].RetryAt.Sub(y.now)+time.Second)
	}
	want(t, "left in the queue", len(y.queued(t)), 0)
	fails := y.failed(t)
	if len(fails) != 1 || !strings.Contains(fails[0], `"reason":"gave_up"`) || !strings.Contains(fails[0], fmt.Sprintf(`"tries":%d`, dispatch.PushTries)) ||
		!strings.Contains(fails[0], "gave up after") {
		t.Fatalf("push_failed = %s", fails)
	}
	if box, err = y.api.UserInbox(ctx, "ada", 0, "ada"); err != nil || box.Unread != 2 {
		t.Fatalf("the inbox afterwards: %+v, %v", box, err)
	}

	y.mail(t, "yardmaster", "three")
	y.now = time.Now()
	y.pass(t, dispatch.PushMaxAge+time.Minute)
	want(t, "left in the queue", len(y.queued(t)), 0)
	if fails = y.failed(t); len(fails) != 2 || !strings.Contains(fails[1], `"reason":"expired"`) {
		t.Fatalf("push_failed = %s", fails)
	}
}

// A user is sent at most PushesPerHour messages in an hour. The last of
// them says so; what comes after waits, and goes as one when the hour has
// room.
func TestPushBoundOnAnHour(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	for i := range store.PushesPerHour {
		y.mail(t, "yardmaster", fmt.Sprint("mail ", i))
		y.now = time.Now()
		y.pass(t, store.PushGrace+time.Second)
	}
	got := y.sent()
	want(t, "sent within the bound", len(got), store.PushesPerHour)
	if last := got[len(got)-1]; !strings.Contains(last, "the last: what comes next waits in yardr inbox.\nmail 19") {
		t.Fatalf("the message that reached the bound: %q", last)
	}
	if strings.Contains(got[len(got)-2], "waits in yardr inbox") {
		t.Fatalf("the one before it: %q", got[len(got)-2])
	}
	y.mail(t, "yardmaster", "over")
	y.mail(t, "yardmaster", "and over")
	y.now = time.Now()
	y.pass(t, store.PushGrace+time.Second)
	y.pass(t, 30*time.Minute)
	want(t, "sent over the bound", len(y.sent()), store.PushesPerHour)
	want(t, "waiting", len(y.queued(t)), 2)
	y.pass(t, 31*time.Minute)
	want(t, "sent when the hour had room", y.sent()[store.PushesPerHour:], []string{"2 new in your yardr inbox | mail from yardmaster (2)"})
}

// A user whose env file is gone is not told, and nothing is tried: one
// push_failed for the cause, not one for every mail that meets it, and the
// record says what is wrong. The inbox is filled as ever.
func TestPushToAUserWhoIsNotSetUp(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	must(t, os.Remove(y.env))
	y.mail(t, "yardmaster", "one")
	y.pass(t, store.PushGrace+time.Second)
	y.mail(t, "yardmaster", "two")
	y.now = time.Now()
	y.pass(t, store.PushGrace+time.Second)
	want(t, "asked", len(y.sent()), 0)
	want(t, "left in the queue", len(y.queued(t)), 0)
	fails := y.failed(t)
	if len(fails) != 1 || !strings.Contains(fails[0], `"reason":"not_set_up"`) || !strings.Contains(fails[0], "user ada") {
		t.Fatalf("push_failed = %s", fails)
	}
	if l := y.last(t); l.Reason != store.PushNotSetUp || l.Tries != 0 {
		t.Fatalf("last push = %+v", l)
	}
	if n := strings.Count(y.log.String(), "user not told"); n != 1 {
		t.Fatalf("the daemon said it %d times:\n%s", n, y.log.String())
	}
	box, err := y.api.UserInbox(context.Background(), "ada", 0, "ada")
	if err != nil || box.Unread != 2 {
		t.Fatalf("the inbox: %+v, %v", box, err)
	}
}

// withImage gives the push source an image command: a script that records
// what it was handed and answers 200. It returns the directory it records
// in. The script is a stand-in (scripttest), since the send runs it at once and
// the first start of an executable file written a moment before takes seconds
// on a loaded macOS (see scripttest).
func (y *pushYard) withImage(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "ishow")
	show := "#!/bin/sh\n{ cat; echo; } >> " + dir + "/image.stdin\nprintf '%s' '{\"status\":200}'\n"
	scripttest.Write(t, script, show)
	text := strings.Replace(pushSource, "BASE", y.srv.URL, 1)
	text = strings.Replace(text, "  text: text\n", "  text: text\n  image: ["+script+"]\n", 1)
	_, err := y.api.PutSource(context.Background(), "push.yaml", text, "jo")
	must(t, err)
	return dir
}

// imageSends are the objects the image command was handed, oldest first.
func imageSends(t *testing.T, dir string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "image.stdin"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("the command was handed %q: %v", line, err)
		}
		out = append(out, v)
	}
	return out
}

// putBlob keeps data as a blob and answers the file a mail or a note
// carries it with.
func (y *pushYard) putBlob(t *testing.T, data []byte, name string) store.BlobFile {
	t.Helper()
	ref, err := y.api.PutBlob(context.Background(), data)
	must(t, err)
	return store.BlobFile{Hash: ref.Hash, Name: name}
}

var pushPNG = []byte("\x89PNG\r\n\x1a\nthe shed from the lane")

// A mail with a picture is told with it: the image command is handed the
// address, the words and the picture's path, whose bytes are the mail's.
// A mail whose picture the yard no longer holds is told without: the call
// tells, and the command runs no more. So is one whose picture the source
// refuses: the words are told at once, not tried again with the picture
// until they are given up, and the daemon's log says why.
func testPushTellsMailImage(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	dir := y.withImage(t)
	ctx := context.Background()

	shot := y.putBlob(t, pushPNG, "shot.png")
	must(t, y.api.Mail(ctx, "ada", "the shed lands Monday", "yardmaster", shot))
	y.settle(t)
	sends := imageSends(t, dir)
	if len(sends) != 1 || sends[0]["address"] != pushAddress || sends[0]["title"] != "mail from yardmaster" ||
		sends[0]["text"] != "the shed lands Monday" {
		t.Fatalf("the command was handed %v", sends)
	}
	path, _ := sends[0]["image"].(string)
	if path != store.BlobPath(y.home, shot.Hash) {
		t.Fatalf("the picture's path = %q", path)
	}
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, pushPNG) {
		t.Fatalf("the picture holds %q, %v", got, err)
	}
	want(t, "asked the call", len(y.sent()), 0)
	want(t, "left in the queue", len(y.queued(t)), 0)

	// The picture gone: the call tells, unadorned.
	gone := y.putBlob(t, pushPNG, "gone.png")
	must(t, y.api.Mail(ctx, "ada", "another shed", "yardmaster", gone))
	must(t, os.Remove(store.BlobPath(y.home, gone.Hash)))
	// A push settles wherever this test sends (y.settle): the store stamps a
	// mail with the time it is made, not the daemon's clock, and a send that
	// ran the command can take longer than the grace on a loaded machine.
	y.settle(t)
	want(t, "told without the picture", y.sent(), []string{"mail from yardmaster | another shed"})
	if sends := imageSends(t, dir); len(sends) != 1 {
		t.Fatalf("the command ran %d times", len(sends))
	}
	want(t, "push_failed events", y.failed(t), []string{})

	// The source refuses the picture: the call tells all the same, in the
	// same pass, with nothing left to try again.
	scripttest.Write(t, filepath.Join(dir, "ishow"), "#!/bin/sh\necho 'PHOTO_INVALID_DIMENSIONS' >&2\nexit 1\n")
	tall := y.putBlob(t, append(bytes.Clone(pushPNG), "-tall"...), "tall.png")
	must(t, y.api.Mail(ctx, "ada", "a long page", "yardmaster", tall))
	y.settle(t)
	want(t, "told without the refused picture", y.sent(),
		[]string{"mail from yardmaster | another shed", "mail from yardmaster | a long page"})
	want(t, "left in the queue", len(y.queued(t)), 0)
	want(t, "push_failed events", y.failed(t), []string{})
	if l := y.last(t); !l.OK() || l.Tries != 1 {
		t.Fatalf("last push = %+v", l)
	}
	if logged := y.log.String(); !strings.Contains(logged, "user told without the picture") || !strings.Contains(logged, "PHOTO_INVALID_DIMENSIONS") {
		t.Fatalf("the daemon's log: %s", logged)
	}
}

// A notification of a done bead is told with the newest note on that bead
// with a picture: notes without one, and files that are no picture, are
// passed over.
func testPushTellsDoneBeadScreenshot(t *testing.T) {
	t.Parallel()
	y := newPushYard(t)
	dir := y.withImage(t)
	ctx := context.Background()
	must(t, y.api.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"))
	flow, err := store.ParseFlow(pushFlow)
	must(t, err)
	flow.Name = "yard"
	_, err = y.api.PutFlow(ctx, *flow, "jo")
	must(t, err)
	must(t, y.api.SetFlowPointer(ctx, "yard", "", "yard", "jo"))
	must(t, y.api.AddSubscription(ctx, "ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
		Where: map[string]string{"state": store.StateDone}}, "ada"))
	_, err = y.api.CreateBead(ctx, store.NewBead{ID: "yard-1", Title: "The shed", Depot: "yard", Stage: "new"}, "jo")
	must(t, err)
	must(t, y.api.AddNote(ctx, "yard-1", "the plans", "builder", y.putBlob(t, []byte("plans"), "plans.txt")))
	must(t, y.api.AddNote(ctx, "yard-1", "the old shed", "builder", y.putBlob(t, pushPNG, "old.png")))
	must(t, y.api.AddNote(ctx, "yard-1", "landed", "builder"))
	_, err = y.api.CloseBead(ctx, "yard-1", "landed", store.Expect{}, "builder")
	must(t, err)

	y.settle(t)
	sends := imageSends(t, dir)
	if len(sends) != 1 || sends[0]["title"] != "yard-1 closed" {
		t.Fatalf("the command was handed %v", sends)
	}
	path, _ := sends[0]["image"].(string)
	if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, pushPNG) {
		t.Fatalf("the picture holds %q, %v", got, err)
	}
	want(t, "asked the call", len(y.sent()), 0)
	want(t, "push_failed events", y.failed(t), []string{})
}

// Across a batch the first picture in order goes, whether a mail's or a
// done bead's; the rest stay in the yard.
func testPushFirstImageAcrossBatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	setup := func(t *testing.T, mailFirst bool) (*pushYard, string, store.BlobFile, store.BlobFile) {
		t.Helper()
		y := newPushYard(t)
		dir := y.withImage(t)
		must(t, y.api.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "jo"))
		flow, err := store.ParseFlow(pushFlow)
		must(t, err)
		flow.Name = "yard"
		_, err = y.api.PutFlow(ctx, *flow, "jo")
		must(t, err)
		must(t, y.api.SetFlowPointer(ctx, "yard", "", "yard", "jo"))
		must(t, y.api.AddSubscription(ctx, "ada", store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll,
			Where: map[string]string{"state": store.StateDone}}, "ada"))
		_, err = y.api.CreateBead(ctx, store.NewBead{ID: "yard-1", Title: "The shed", Depot: "yard", Stage: "new"}, "jo")
		must(t, err)
		mailShot := y.putBlob(t, append(bytes.Clone(pushPNG), "-mail"...), "mail.png")
		noteShot := y.putBlob(t, append(bytes.Clone(pushPNG), "-note"...), "note.png")
		mail := func() { must(t, y.api.Mail(ctx, "ada", "look", "yardmaster", mailShot)) }
		done := func() {
			must(t, y.api.AddNote(ctx, "yard-1", "shot", "builder", noteShot))
			_, err = y.api.CloseBead(ctx, "yard-1", "landed", store.Expect{}, "builder")
			must(t, err)
		}
		if mailFirst {
			mail()
			done()
		} else {
			done()
			mail()
		}
		return y, dir, mailShot, noteShot
	}

	y, dir, mailShot, _ := setup(t, true)
	y.settle(t)
	sends := imageSends(t, dir)
	if len(sends) != 1 || sends[0]["image"] != store.BlobPath(y.home, mailShot.Hash) {
		t.Fatalf("the mail first: %v", sends)
	}

	y, dir, _, noteShot := setup(t, false)
	y.settle(t)
	sends = imageSends(t, dir)
	if len(sends) != 1 || sends[0]["image"] != store.BlobPath(y.home, noteShot.Hash) {
		t.Fatalf("the done bead first: %v", sends)
	}
}
