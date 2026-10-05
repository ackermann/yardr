package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// view is a model over a real store, driven without a terminal: keys and
// events go to Update, and what Update asks of the store is run at once.
type view struct {
	t    *testing.T
	st   store.API
	m    *Model
	last uint64 // the stream is handed on through this seq
	// focused are the agents whose panes were asked to the front.
	focused []string
}

const me = "ada"

// yard is a store with the user ada and the residents mayor and refinery,
// both running and idle.
func yard(t *testing.T) store.API {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, me, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"mayor", "refinery"} {
		if _, err := st.PutResident(ctx, store.Resident{Name: name, Runner: "herdr"}, "jo"); err != nil {
			t.Fatal(err)
		}
		must(t, st.ResidentStarted(ctx, name, map[string]any{"agent": "yard-" + name}))
		must(t, st.ResidentStatus(ctx, name, "idle"))
	}
	return st
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// open starts a view of st for ada, sized w by h, with sel selected.
func open(t *testing.T, st store.API, sel string, w, h int, opt ...func(*Options)) *view {
	t.Helper()
	ctx := context.Background()
	state, err := Load(ctx, st, me)
	must(t, err)
	v := &view{t: t, st: st, last: state.Head}
	o := Options{Me: me, Select: sel, Focus: func(_ context.Context, agent string) error {
		v.focused = append(v.focused, agent)
		return nil
	}}
	for _, f := range opt {
		f(&o)
	}
	v.m = New(ctx, st, o, state)
	v.do(v.m.open()) // Init, without the cursor's blinking
	v.send(tea.WindowSizeMsg{Width: w, Height: h})
	return v
}

// send gives the model a message and runs what it asks for.
func (v *view) send(msg tea.Msg) {
	v.t.Helper()
	_, cmd := v.m.Update(msg)
	v.do(cmd)
}

// do runs a command of the model and gives it the answer. Only the
// model's own messages come back in: the input's cursor blinks on a timer.
func (v *view) do(cmd tea.Cmd) {
	v.t.Helper()
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			v.do(c)
		}
	case convoMsg, residentsMsg, sentMsg, ackedMsg, focusMsg:
		v.send(msg)
	}
}

// key presses a key the view acts on.
func (v *view) key(k tea.KeyType) {
	v.t.Helper()
	v.send(tea.KeyMsg{Type: k})
}

// typed types text into the line; what the input asks for (its cursor) is
// not run.
func (v *view) typed(text string) {
	v.m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
}

// stream hands the model the yard's events since the last it got, as
// Follow does.
func (v *view) stream() {
	v.t.Helper()
	evs, err := v.st.EventsSince(context.Background(), v.last, 0)
	must(v.t, err)
	for _, e := range evs {
		v.last = e.Seq
		v.send(EventMsg(e))
	}
}

// screen is the view as text, without its styling.
func (v *view) screen() string { return ansi.Strip(v.m.View()) }

func (v *view) shows(what ...string) {
	v.t.Helper()
	s := v.screen()
	for _, w := range what {
		if !strings.Contains(s, w) {
			v.t.Fatalf("the view does not show %q:\n%s", w, s)
		}
	}
}

func (v *view) hides(what ...string) {
	v.t.Helper()
	s := v.screen()
	for _, w := range what {
		if strings.Contains(s, w) {
			v.t.Fatalf("the view shows %q:\n%s", w, s)
		}
	}
}

// cursor is how far ada's mail is acknowledged in the store.
func (v *view) cursor() uint64 {
	v.t.Helper()
	u, err := v.st.GetUser(context.Background(), me)
	must(v.t, err)
	return u.Cursor
}

func head(t *testing.T, st store.API) uint64 {
	t.Helper()
	h, err := st.Head(context.Background())
	must(t, err)
	return h
}

// The resident named is selected, else the first; tab and shift+tab go
// through the list and around its ends, and each selection shows that
// resident's conversation.
func TestSelection(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	must(t, st.Mail(ctx, "mayor", "to the mayor", me))
	must(t, st.Mail(ctx, "refinery", "to the refinery", me))

	if got := open(t, st, "refinery", 120, 24).m.Selected(); got != "refinery" {
		t.Fatalf("named refinery, selected %q", got)
	}
	if got := open(t, st, "nobody", 120, 24).m.Selected(); got != "mayor" {
		t.Fatalf("named a resident the yard does not have, selected %q", got)
	}
	v := open(t, st, "mayor", 120, 24)
	v.shows("> mayor", "to the mayor")
	v.hides("to the refinery")
	v.key(tea.KeyTab)
	v.shows("> refinery", "to the refinery", "Write to refinery")
	v.hides("to the mayor", "> mayor")
	v.key(tea.KeyTab)
	if v.m.Selected() != "mayor" {
		t.Fatalf("tab past the end selected %q", v.m.Selected())
	}
	v.key(tea.KeyShiftTab)
	if v.m.Selected() != "refinery" {
		t.Fatalf("shift+tab before the start selected %q", v.m.Selected())
	}

	// The selected resident is removed: the first is selected.
	must(t, st.DeleteResident(ctx, "refinery", "", "jo"))
	v.stream()
	if v.m.Selected() != "mayor" {
		t.Fatalf("after the selected resident was removed, selected %q", v.m.Selected())
	}
	v.hides("refinery")
}

// Mail from the selected resident appears from the stream and is
// acknowledged as it is shown. Mail from another is counted beside its
// name and stays unread in the store until its conversation is opened,
// and what came after it is not marked read past it.
func TestMailArrives(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	must(t, st.Mail(ctx, me, "waiting for you", "mayor")) // before the view opens
	first := head(t, st)
	v := open(t, st, "mayor", 120, 24)
	v.shows("waiting for you")
	if v.cursor() != first || v.m.Unread("mayor") != 0 {
		t.Fatalf("opening the conversation acknowledged through %d, want %d (unread %d)", v.cursor(), first, v.m.Unread("mayor"))
	}

	must(t, st.Mail(ctx, me, "the gate is green", "mayor"))
	second := head(t, st)
	v.hides("the gate is green") // nothing reads the store by itself
	v.stream()
	v.shows("the gate is green")
	if v.cursor() != second {
		t.Fatalf("mail in the open conversation acknowledged through %d, want %d", v.cursor(), second)
	}

	must(t, st.Mail(ctx, me, "landed yardr-1a2b", "refinery"))
	other := head(t, st)
	must(t, st.Mail(ctx, me, "and one more", "mayor"))
	newest := head(t, st)
	v.stream()
	v.shows("and one more")
	v.hides("landed yardr-1a2b")
	if v.m.Unread("refinery") != 1 || v.m.Unread("mayor") != 0 {
		t.Fatalf("unread: refinery %d, mayor %d", v.m.Unread("refinery"), v.m.Unread("mayor"))
	}
	if !strings.Contains(v.screen(), "refinery    idle            1") {
		t.Fatalf("the list does not count refinery's mail:\n%s", v.screen())
	}
	// The mayor's later mail was seen, but refinery's before it was not:
	// the user's one inbox position stays short of it.
	if got := v.cursor(); got >= other {
		t.Fatalf("mail of a conversation not opened was acknowledged: cursor %d, its seq %d", got, other)
	}

	v.key(tea.KeyTab)
	v.shows("landed yardr-1a2b")
	if v.cursor() != newest || v.m.Unread("refinery") != 0 {
		t.Fatalf("after opening it: cursor %d, the newest mail %d, unread %d", v.cursor(), newest, v.m.Unread("refinery"))
	}
	box, err := st.UserInbox(ctx, me, 1)
	if err != nil || box.Unread != 0 {
		t.Fatalf("the store has %d unread, %v", box.Unread, err)
	}

	// The stream gives an event twice after a reconnect that overlaps
	// what was read: it is shown once.
	v.last = 0
	v.stream()
	if n := strings.Count(v.screen(), "landed yardr-1a2b"); n != 1 {
		t.Fatalf("a mail the stream gave twice is shown %d times:\n%s", n, v.screen())
	}
	if v.m.Unread("refinery") != 0 || v.m.Unread("mayor") != 0 {
		t.Fatalf("replayed mail counts as unread again: refinery %d, mayor %d", v.m.Unread("refinery"), v.m.Unread("mayor"))
	}
}

// Mail acknowledged elsewhere (yardr inbox --ack in another terminal) is
// no longer counted, and mail from someone who is no resident is named
// with where to read it.
func TestUnreadElsewhere(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "grace", "jo"); err != nil {
		t.Fatal(err)
	}
	v := open(t, st, "mayor", 80, 24)
	must(t, st.Mail(ctx, me, "landed", "refinery"))
	must(t, st.Mail(ctx, me, "lunch?", "grace"))
	v.stream()
	v.shows("refinery idle (1)", "1 unread from grace, no resident: yardr inbox")
	must(t, st.AckUserInbox(ctx, me, head(t, st)))
	v.stream()
	v.hides("(1)", "unread from grace")
}

// Under what the user sent, the view says where it stands, for every
// state of the receiver, and follows the state and the reading from the
// stream.
func TestStanding(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	v := open(t, st, "mayor", 120, 30)
	v.shows("No mail between you and mayor yet.")
	v.typed("deploys are frozen")
	v.key(tea.KeyEnter)
	v.shows("deploys are frozen", "· sent; mayor is idle")
	sent := head(t, st)
	v.stream() // the same mail, now from the stream

	for _, c := range []struct{ status, list, line string }{
		{"working", "mayor       working", "· sent; mayor is working and reads mail when idle"},
		{"blocked", "mayor       blocked", "· mayor waits at a prompt in its pane and cannot read mail (ctrl+g goes there)"},
		{"unknown", "mayor       running", "· sent"},
		{"idle", "mayor       idle", "· sent; mayor is idle"},
	} {
		must(t, st.ResidentStatus(ctx, "mayor", c.status))
		v.stream()
		v.shows(c.list, c.line)
	}

	// A second mail while the first waits: one line for the two.
	v.typed("until Friday")
	v.key(tea.KeyEnter)
	v.stream()
	if n := strings.Count(v.screen(), "· sent; mayor is idle"); n != 1 {
		t.Fatalf("two waiting mails have %d lines of where they stand:\n%s", n, v.screen())
	}

	// The mayor reads the first: "read" under it, the second still waits.
	must(t, st.AckInbox(ctx, "mayor", sent))
	v.stream()
	v.shows("· read", "· sent; mayor is idle")
	screen := v.screen()
	if strings.Index(screen, "· read") > strings.Index(screen, "until Friday") {
		t.Fatalf("read is not under the mail that was read:\n%s", screen)
	}
	must(t, st.AckInbox(ctx, "mayor", head(t, st)))
	v.stream()
	v.hides("· sent")
	if n := strings.Count(v.screen(), "· read"); n != 1 {
		t.Fatalf("two read mails have %d lines:\n%s", n, v.screen())
	}

	// Its session ends, and a third mail has nobody to read it.
	must(t, st.SetResidentEnabled(ctx, "mayor", false, "", "jo"))
	must(t, st.ResidentStopped(ctx, "mayor", "disabled"))
	v.typed("anyone?")
	v.key(tea.KeyEnter)
	v.stream()
	v.shows("mayor       not running", "· mayor is not running: yardr resident start mayor")

	// Started again and failing: the list and the line say so.
	must(t, st.SetResidentEnabled(ctx, "mayor", true, "", "jo"))
	must(t, st.ResidentStartFailed(ctx, "mayor", "claude: command not found", time.Minute))
	v.stream()
	v.shows("mayor       failed to start", "· mayor failed to start: yardr resident show mayor")

	// A view opened now reads the same from the store.
	again := open(t, st, "mayor", 120, 30)
	again.shows("mayor       failed to start", "· read", "· mayor failed to start: yardr resident show mayor")
}

func TestStateOf(t *testing.T) {
	for _, c := range []struct {
		r    store.Resident
		want string
	}{
		{store.Resident{State: "stopped", Enabled: true}, NotRunning},
		{store.Resident{State: "stopped"}, NotRunning},
		{store.Resident{State: "stopped", Failures: 2}, NotRunning}, // stopped by hand after failing
		{store.Resident{State: "stopped", Enabled: true, Failures: 2}, Failed},
		{store.Resident{State: "failed", Enabled: true, Failures: 5}, Failed},
		{store.Resident{State: "running", Enabled: true}, Running},
		{store.Resident{State: "running", Enabled: true, Status: "unknown"}, Running},
		{store.Resident{State: "running", Enabled: true, Status: "working"}, Working},
		{store.Resident{State: "running", Enabled: true, Status: "idle"}, Idle},
		{store.Resident{State: "running", Enabled: true, Status: "blocked"}, Blocked},
		// A session that runs after failed starts is what it does now.
		{store.Resident{State: "running", Enabled: true, Status: "idle", Failures: 1}, Idle},
	} {
		if got := StateOf(c.r); got != c.want {
			t.Errorf("%+v: %q, want %q", c.r, got, c.want)
		}
	}
	if got := Standing(store.Letter{}, nil, "yardr"); got != "sent" {
		t.Errorf("mail to a resident that is gone stands as %q", got)
	}
}

// fits checks that the view is h lines, none wider than w.
func (v *view) fits(w, h int) {
	v.t.Helper()
	lines := strings.Split(v.screen(), "\n")
	if len(lines) != h {
		v.t.Fatalf("the view is %d lines in a terminal of %d:\n%s", len(lines), h, v.screen())
	}
	for _, l := range lines {
		if n := ansi.StringWidth(l); n > w {
			v.t.Fatalf("a line is %d columns in a terminal of %d: %q", n, w, l)
		}
	}
}

// At 80 columns the residents are one line on top and the conversation
// has the width; in a wide terminal they are a column. Either way the
// view is the terminal's size, the newest mail is in view and the keys
// are the last line.
func TestLayout(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	long := strings.Repeat("a long line that has to be wrapped at the width of the view ", 4)
	for i := range 30 {
		must(t, st.Mail(ctx, me, fmt.Sprintf("mail %d %s", i, long), "mayor"))
	}
	must(t, st.Mail(ctx, me, "landed", "refinery"))

	v := open(t, st, "mayor", 80, 24)
	v.fits(80, 24)
	lines := strings.Split(v.screen(), "\n")
	if lines[1] != "[mayor idle]  refinery idle (1)" {
		t.Fatalf("the residents' line at 80 columns is %q", lines[1])
	}
	if strings.Contains(v.screen(), "│") {
		t.Fatalf("a column at 80 columns:\n%s", v.screen())
	}
	if lines[len(lines)-1] != Keys {
		t.Fatalf("the last line is %q", lines[len(lines)-1])
	}
	v.shows("mail 29")
	v.hides("mail 0 ")
	// Scrolled back to the oldest, and forward to the newest again.
	for range 40 {
		v.key(tea.KeyPgUp)
	}
	v.shows("mail 0 ")
	v.hides("mail 29")
	v.fits(80, 24)
	for range 40 {
		v.key(tea.KeyPgDown)
	}
	v.shows("mail 29")

	// The same view made wide: a column, with the selection marked.
	v.send(tea.WindowSizeMsg{Width: 160, Height: 40})
	v.fits(160, 40)
	v.shows("> mayor       idle", "  refinery    idle            1", " │ ", "mail 29")
	if lines := strings.Split(v.screen(), "\n"); lines[len(lines)-1] != Keys {
		t.Fatalf("the last line is %q", lines[len(lines)-1])
	}

	// Narrower than the keys and the residents: cut, not wrapped.
	v.send(tea.WindowSizeMsg{Width: 30, Height: 12})
	v.fits(30, 12)
	v.send(tea.WindowSizeMsg{Width: Wide - 1, Height: 24})
	v.fits(Wide-1, 24)
	if strings.Contains(v.screen(), "│") {
		t.Fatalf("a column below %d columns", Wide)
	}

	if n := ansi.StringWidth(Keys); n >= 80 {
		t.Fatalf("the keys are %d columns: more than a terminal of 80 shows", n)
	}
}

// Many residents on the folded line: the selected one stays on it.
func TestFoldedLineKeepsTheSelection(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	for i := range 12 {
		_, err := st.PutResident(ctx, store.Resident{Name: fmt.Sprintf("worker-%02d", i), Runner: "herdr"}, "jo")
		must(t, err)
	}
	v := open(t, st, "worker-11", 80, 24)
	v.fits(80, 24)
	v.shows("[worker-11 not running]")
}

// Enter sends the line as mail to the selected resident; ctrl+j breaks
// the line instead; an empty line sends nothing.
func TestSend(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	v := open(t, st, "refinery", 80, 24)
	v.key(tea.KeyEnter)
	v.typed("   ")
	v.key(tea.KeyEnter)
	if letters, _ := st.Conversation(ctx, me, "refinery", 0); len(letters) != 0 {
		t.Fatalf("an empty line was sent: %+v", letters)
	}
	v.m.input.Reset()
	v.typed("first line")
	v.key(tea.KeyCtrlJ)
	v.typed("second line")
	v.key(tea.KeyEnter)
	letters, err := st.Conversation(ctx, me, "refinery", 0)
	if err != nil || len(letters) != 1 || letters[0].Text != "first line\nsecond line" || letters[0].From != me {
		t.Fatalf("sent %+v, %v", letters, err)
	}
	if v.m.input.Value() != "" {
		t.Fatalf("the line still holds %q after sending", v.m.input.Value())
	}
	v.shows("first line", "second line", "· sent; refinery is idle")
	if in, _ := st.Inbox(ctx, "mayor", 0); in.Total != 0 {
		t.Fatalf("the mail reached the mayor too: %+v", in)
	}
}

// A mail that cannot be sent leaves what was typed in the line.
func TestSendFails(t *testing.T) {
	st := yard(t)
	v := open(t, st, "mayor", 80, 24)
	must(t, st.DeleteResident(context.Background(), "mayor", "", "jo")) // gone before the view hears of it
	v.typed("are you there?")
	v.key(tea.KeyEnter)
	v.shows("not sent to mayor", "are you there?")
	if v.m.input.Value() != "are you there?" {
		t.Fatalf("the line holds %q after a failed send", v.m.input.Value())
	}
}

// The size of a mail is counted before the limit, and what would pass it
// is not taken, so the store never refuses a mail for its size.
func TestSizeLimit(t *testing.T) {
	st := yard(t)
	v := open(t, st, "mayor", 80, 24)
	v.typed(strings.Repeat("x", warnAt-1))
	v.hides("bytes")
	v.typed("x")
	v.shows(fmt.Sprintf("%d of %d bytes", warnAt, store.MaxMailBytes))
	v.typed(strings.Repeat("x", store.MaxMailBytes-warnAt))
	v.shows(fmt.Sprintf("%d of %d bytes", store.MaxMailBytes, store.MaxMailBytes))
	v.typed("y")
	if n := len(v.m.input.Value()); n != store.MaxMailBytes {
		t.Fatalf("the line holds %d bytes, the limit is %d", n, store.MaxMailBytes)
	}
	v.shows("a mail holds 4096 bytes")
	// Bytes, not characters: é is two.
	v.m.input.Reset()
	v.typed(strings.Repeat("é", store.MaxMailBytes/2))
	v.typed("é")
	if n := len(v.m.input.Value()); n != store.MaxMailBytes {
		t.Fatalf("the line holds %d bytes of é, the limit is %d", n, store.MaxMailBytes)
	}
	v.fits(80, 24)
	v.key(tea.KeyEnter)
	v.hides("not sent")
	letters, err := st.Conversation(context.Background(), me, "mayor", 0)
	if err != nil || len(letters) != 1 || len(letters[0].Text) != store.MaxMailBytes {
		t.Fatalf("a mail of the full size was not sent: %d letters, %v", len(letters), err)
	}
}

// The key for the pane brings it to the front in herdr, names the herdr
// command elsewhere, and says what to do when no session runs.
func TestPaneKey(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	in := open(t, st, "mayor", 120, 24, func(o *Options) { o.InHerdr = true })
	in.key(tea.KeyCtrlG)
	if len(in.focused) != 1 || in.focused[0] != "yard-mayor" {
		t.Fatalf("focused %v", in.focused)
	}
	in.shows("herdr shows mayor now")

	out := open(t, st, "refinery", 120, 24)
	out.key(tea.KeyCtrlG)
	if len(out.focused) != 0 {
		t.Fatalf("outside herdr a pane was focused: %v", out.focused)
	}
	out.shows("refinery's pane: herdr agent attach yard-refinery (in herdr: herdr agent focus yard-refinery)")

	failing := open(t, st, "mayor", 120, 24, func(o *Options) {
		o.InHerdr = true
		o.Focus = func(context.Context, string) error {
			return errors.New("herdr agent focus yard-mayor: agent_not_found")
		}
	})
	failing.key(tea.KeyCtrlG)
	failing.shows("herdr agent focus yard-mayor: agent_not_found")

	must(t, st.ResidentStopped(ctx, "mayor", "gone"))
	in.stream()
	in.key(tea.KeyCtrlG)
	if len(in.focused) != 1 {
		t.Fatalf("a pane was focused for a resident that does not run: %v", in.focused)
	}
	in.shows("mayor has no pane, it is not running: yardr resident start mayor")
}

// A list of the residents read before a status the stream told of does
// not put the older status back.
func TestOlderListKeepsNewerStatus(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	v := open(t, st, "mayor", 120, 24)
	before := head(t, st)
	stale, err := st.Residents(ctx)
	must(t, err)
	must(t, st.ResidentStatus(ctx, "mayor", "working"))
	v.stream()
	v.send(residentsMsg{head: before, residents: stale})
	v.shows("mayor       working")
	// One read after it is taken as it is.
	must(t, st.ResidentStatus(ctx, "mayor", "idle"))
	fresh, err := st.Residents(ctx)
	must(t, err)
	v.send(residentsMsg{head: head(t, st), residents: fresh})
	v.shows("mayor       idle")
}

// dropping is a stream that ends after each batch: the yardr server going
// away between them.
type dropping struct {
	mu      sync.Mutex
	batches [][]store.Event
	asked   []uint64 // the seq each subscription was asked to start after
	refuse  int      // subscriptions to refuse first, as a server that is away does
}

func (d *dropping) Subscribe(_ context.Context, after uint64) (<-chan store.Event, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked = append(d.asked, after)
	if len(d.asked) > 1 && d.refuse > 0 {
		d.refuse--
		return nil, errors.New("no server")
	}
	ch := make(chan store.Event, 8)
	if len(d.batches) > 0 {
		for _, e := range d.batches[0] {
			ch <- e
		}
		d.batches = d.batches[1:]
	}
	close(ch)
	return ch, nil
}

// When the stream drops, Follow says so, subscribes again after the last
// event it handed on, and says when it is back: every event once, in
// order.
func TestFollowResumesAfterTheLastEvent(t *testing.T) {
	ev := func(seq uint64) store.Event { return store.Event{Seq: seq, Kind: "mail"} }
	d := &dropping{batches: [][]store.Event{{ev(8), ev(9)}, {ev(10)}, {}, {ev(11)}}, refuse: 2}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got []string
	done := make(chan struct{})
	go func() {
		defer close(done)
		Follow(ctx, d, 7, time.Millisecond, func(e store.Event) {
			got = append(got, fmt.Sprint(e.Seq))
			if e.Seq == 11 {
				cancel()
			}
		}, func(up bool) { got = append(got, map[bool]string{true: "up", false: "down"}[up]) })
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Follow did not get to the last event")
	}
	if want := "8 9 down up 10 down up down up 11"; strings.Join(got, " ") != want {
		t.Fatalf("followed %q, want %q", strings.Join(got, " "), want)
	}
	// After 7 as asked, then after the last event each time: twice more
	// after 9 while the server was away, and after 10 for a stream that
	// brought nothing.
	if want := "[7 9 9 9 10 10]"; fmt.Sprint(d.asked) != want {
		t.Fatalf("subscribed after %v, want %s", d.asked, want)
	}
}

// While the stream is lost the view says so; when it is back the events
// missed arrive and the notice goes.
func TestStreamLost(t *testing.T) {
	st := yard(t)
	v := open(t, st, "mayor", 80, 24)
	v.send(StreamMsg{Up: false})
	v.shows("the yardr server is away")
	v.fits(80, 24)
	must(t, st.Mail(context.Background(), me, "said while you were away", "mayor"))
	v.send(StreamMsg{Up: true})
	v.stream()
	v.hides("the yardr server is away")
	v.shows("said while you were away")
}

// Follow against the store itself: what was committed before it
// subscribed and what comes after reach the view in order.
func TestFollowTheStore(t *testing.T) {
	st := yard(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	after := head(t, st)
	must(t, st.Mail(ctx, me, "before", "mayor"))
	got := make(chan store.Event, 16)
	go Follow(ctx, st, after, time.Millisecond, func(e store.Event) { got <- e }, func(bool) {})
	must(t, st.Mail(ctx, me, "after", "mayor"))
	for _, want := range []string{"before", "after"} {
		select {
		case e := <-got:
			if e.Data["text"] != want {
				t.Fatalf("followed %v, want %s", e.Data["text"], want)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no event for %q", want)
		}
	}
}
