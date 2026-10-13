package chat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/rpc"
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

// yard is a store with the user ada and the crew chief and recorder,
// both running and idle.
func yard(t *testing.T) store.API {
	t.Helper()
	return yardOn(t, storetest.New(t))
}

// yardOn is st with the user ada and the crew chief and recorder, both
// running and idle.
func yardOn(t *testing.T, st store.API) store.API {
	t.Helper()
	ctx := context.Background()
	if _, err := st.AddUser(ctx, me, "jo"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chief", "recorder"} {
		if _, err := st.PutCrew(ctx, store.CrewMember{Name: name, Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo"); err != nil {
			t.Fatal(err)
		}
		must(t, st.CrewStarted(ctx, name, map[string]any{"agent": "yard-" + name}))
		must(t, st.CrewStatus(ctx, name, "idle"))
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
	o := Options{Me: me, Select: sel, Secrets: st, Focus: func(_ context.Context, agent string) error {
		v.focused = append(v.focused, agent)
		return nil
	}}
	for _, f := range opt {
		f(&o)
	}
	v.m = New(ctx, st, o, state)
	v.do(v.m.open()) // Init, without the cursor's blinking
	v.do(v.m.watch())
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
	case convoMsg, crewMsg, sentMsg, ackedMsg, focusMsg, tailMsg, notesMsg, notesAckedMsg, linksMsg, asksMsg, gaveMsg:
		// Not refreshMsg: the time to read a pane again is the test's to
		// say, or a watched pane would be read without end.
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

// The crew member named is selected, else the first; tab and shift+tab go
// through the list and around its ends, where the yard's row is, and each
// selection shows that crew member's conversation.
func TestSelection(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	must(t, st.Mail(ctx, "chief", "to the chief", me))
	must(t, st.Mail(ctx, "recorder", "to the recorder", me))

	if got := open(t, st, "recorder", 120, 24).m.Selected(); got != "recorder" {
		t.Fatalf("named recorder, selected %q", got)
	}
	if got := open(t, st, "nobody", 120, 24).m.Selected(); got != "chief" {
		t.Fatalf("named a crew member the yard does not have, selected %q", got)
	}
	v := open(t, st, "chief", 120, 24)
	v.shows("> chief", "to the chief")
	v.hides("to the recorder")
	v.key(tea.KeyTab)
	v.shows("> recorder", "to the recorder", "Write to recorder")
	v.hides("to the chief", "> chief")
	v.key(tea.KeyTab)
	if !v.m.OnYard() || v.m.Selected() != "" {
		t.Fatalf("tab past the last crew member selected %q, the yard's row %v", v.m.Selected(), v.m.OnYard())
	}
	v.key(tea.KeyTab)
	if v.m.Selected() != "chief" || v.m.OnYard() {
		t.Fatalf("tab past the end selected %q, the yard's row %v", v.m.Selected(), v.m.OnYard())
	}
	v.key(tea.KeyShiftTab)
	v.key(tea.KeyShiftTab)
	if v.m.Selected() != "recorder" {
		t.Fatalf("shift+tab before the start selected %q", v.m.Selected())
	}

	// The selected crew member is removed: the first is selected.
	must(t, st.DeleteCrew(ctx, "recorder", "", "jo"))
	v.stream()
	if v.m.Selected() != "chief" {
		t.Fatalf("after the selected crew member was removed, selected %q", v.m.Selected())
	}
	v.hides("recorder")
}

// Mail from the selected crew member appears from the stream and is
// acknowledged as it is shown. Mail from another is counted beside its
// name and stays unread in the store until its conversation is opened,
// and what came after it is not marked read past it.
func TestMailArrives(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	must(t, st.Mail(ctx, me, "waiting for you", "chief")) // before the view opens
	first := head(t, st)
	v := open(t, st, "chief", 120, 24)
	v.shows("waiting for you")
	if v.cursor() != first || v.m.Unread("chief") != 0 {
		t.Fatalf("opening the conversation acknowledged through %d, want %d (unread %d)", v.cursor(), first, v.m.Unread("chief"))
	}

	must(t, st.Mail(ctx, me, "the tests are green", "chief"))
	second := head(t, st)
	v.hides("the tests are green") // nothing reads the store by itself
	v.stream()
	v.shows("the tests are green")
	if v.cursor() != second {
		t.Fatalf("mail in the open conversation acknowledged through %d, want %d", v.cursor(), second)
	}

	must(t, st.Mail(ctx, me, "landed yardr-1a2b", "recorder"))
	other := head(t, st)
	must(t, st.Mail(ctx, me, "and one more", "chief"))
	newest := head(t, st)
	v.stream()
	v.shows("and one more")
	v.hides("landed yardr-1a2b")
	if v.m.Unread("recorder") != 1 || v.m.Unread("chief") != 0 {
		t.Fatalf("unread: recorder %d, chief %d", v.m.Unread("recorder"), v.m.Unread("chief"))
	}
	if !strings.Contains(v.screen(), "recorder    idle            1") {
		t.Fatalf("the list does not count recorder's mail:\n%s", v.screen())
	}
	// The chief's later mail was seen, but recorder's before it was not:
	// the user's one inbox position stays short of it.
	if got := v.cursor(); got >= other {
		t.Fatalf("mail of a conversation not opened was acknowledged: cursor %d, its seq %d", got, other)
	}

	v.key(tea.KeyTab)
	v.shows("landed yardr-1a2b")
	if v.cursor() != newest || v.m.Unread("recorder") != 0 {
		t.Fatalf("after opening it: cursor %d, the newest mail %d, unread %d", v.cursor(), newest, v.m.Unread("recorder"))
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
	if v.m.Unread("recorder") != 0 || v.m.Unread("chief") != 0 {
		t.Fatalf("replayed mail counts as unread again: recorder %d, chief %d", v.m.Unread("recorder"), v.m.Unread("chief"))
	}
}

// Mail acknowledged elsewhere (yardr inbox --ack in another terminal) is
// no longer counted, and mail from someone who is no crew member is named
// with where to read it.
func TestUnreadElsewhere(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	if _, err := st.AddUser(ctx, "grace", "jo"); err != nil {
		t.Fatal(err)
	}
	v := open(t, st, "chief", 80, 24)
	must(t, st.Mail(ctx, me, "landed", "recorder"))
	must(t, st.Mail(ctx, me, "lunch?", "grace"))
	v.stream()
	v.shows("recorder idle (1)", "1 unread from grace, no crew member: yardr inbox")
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
	v := open(t, st, "chief", 120, 30)
	v.shows("No mail between you and chief yet.")
	v.typed("deploys are frozen")
	v.key(tea.KeyEnter)
	v.shows("deploys are frozen", "· sent; chief is idle")
	sent := head(t, st)
	v.stream() // the same mail, now from the stream

	for _, c := range []struct{ status, list, line string }{
		{"working", "chief       working", "· sent; chief is working and reads mail when idle"},
		{"blocked", "chief       blocked", "· chief waits at a prompt in its pane and cannot read mail (ctrl+g goes there)"},
		{"unknown", "chief       running", "· sent"},
		{"idle", "chief       idle", "· sent; chief is idle"},
	} {
		must(t, st.CrewStatus(ctx, "chief", c.status))
		v.stream()
		v.shows(c.list, c.line)
	}

	// A second mail while the first waits: one line for the two.
	v.typed("until Friday")
	v.key(tea.KeyEnter)
	v.stream()
	if n := strings.Count(v.screen(), "· sent; chief is idle"); n != 1 {
		t.Fatalf("two waiting mails have %d lines of where they stand:\n%s", n, v.screen())
	}

	// The chief reads the first: "read" under it, the second still waits.
	must(t, st.AckInbox(ctx, "chief", sent))
	v.stream()
	v.shows("· read", "· sent; chief is idle")
	screen := v.screen()
	if strings.Index(screen, "· read") > strings.Index(screen, "until Friday") {
		t.Fatalf("read is not under the mail that was read:\n%s", screen)
	}
	must(t, st.AckInbox(ctx, "chief", head(t, st)))
	v.stream()
	v.hides("· sent")
	if n := strings.Count(v.screen(), "· read"); n != 1 {
		t.Fatalf("two read mails have %d lines:\n%s", n, v.screen())
	}

	// Its session ends, and a third mail has nobody to read it.
	must(t, st.SetCrewEnabled(ctx, "chief", false, "", "jo"))
	must(t, st.CrewStopped(ctx, "chief", "disabled"))
	v.typed("anyone?")
	v.key(tea.KeyEnter)
	v.stream()
	v.shows("chief       not running", "· chief is not running: yardr crew start chief")

	// Started again and failing: the list and the line say so.
	must(t, st.SetCrewEnabled(ctx, "chief", true, "", "jo"))
	must(t, st.CrewStartFailed(ctx, "chief", "claude: command not found", time.Minute))
	v.stream()
	v.shows("chief       failed to start", "· chief failed to start: yardr crew show chief")

	// A view opened now reads the same from the store.
	again := open(t, st, "chief", 120, 30)
	again.shows("chief       failed to start", "· read", "· chief failed to start: yardr crew show chief")
}

func TestStateOf(t *testing.T) {
	for _, c := range []struct {
		r    store.CrewMember
		want string
	}{
		{store.CrewMember{State: "stopped", Enabled: true}, NotRunning},
		{store.CrewMember{State: "stopped"}, NotRunning},
		{store.CrewMember{State: "stopped", Failures: 2}, NotRunning}, // stopped by hand after failing
		{store.CrewMember{State: "stopped", Enabled: true, Failures: 2}, Failed},
		{store.CrewMember{State: "failed", Enabled: true, Failures: 5}, Failed},
		{store.CrewMember{State: "running", Enabled: true}, Running},
		{store.CrewMember{State: "running", Enabled: true, Status: "unknown"}, Running},
		{store.CrewMember{State: "running", Enabled: true, Status: "working"}, Working},
		{store.CrewMember{State: "running", Enabled: true, Status: "idle"}, Idle},
		{store.CrewMember{State: "running", Enabled: true, Status: "blocked"}, Blocked},
		// A session that runs after failed starts is what it does now.
		{store.CrewMember{State: "running", Enabled: true, Status: "idle", Failures: 1}, Idle},
		// At its login screen, whatever the runtime says the agent does.
		{store.CrewMember{State: "running", Enabled: true, Status: "idle", Handle: map[string]any{dispatch.HarnessLoginKey: "Select login method:"}}, NotLoggedIn},
		{store.CrewMember{State: "running", Enabled: true, Status: "blocked", Handle: map[string]any{dispatch.HarnessLoginKey: "Select login method:"}}, NotLoggedIn},
		// A crew member that is gone or given up is that, mark or none.
		{store.CrewMember{State: "stopped", Enabled: true, Handle: map[string]any{dispatch.HarnessLoginKey: "Select login method:"}}, NotRunning},
		{store.CrewMember{State: "failed", Enabled: true, Failures: 5, Handle: map[string]any{dispatch.HarnessLoginKey: "Select login method:"}}, Failed},
	} {
		if got := StateOf(c.r); got != c.want {
			t.Errorf("%+v: %q, want %q", c.r, got, c.want)
		}
	}
	if got := Standing(store.Letter{}, nil, "yardr"); got != "sent" {
		t.Errorf("mail to a crew member that is gone stands as %q", got)
	}
}

// A crew member at its login screen: the notice and the standing say it is
// not logged in, with the command that logs in.
func TestNotLoggedInStatus(t *testing.T) {
	r := store.CrewMember{Name: "chief", State: "running", Enabled: true, Status: "idle",
		Config: map[string]any{"kind": "claude"},
		Handle: map[string]any{dispatch.HarnessLoginKey: "Select login method:"}}
	if got := Known(r); got != "chief is not logged in: run claude in another terminal and log in there; "+
		"then answer the login in its pane or start it fresh (`yardr crew restart chief`)" {
		t.Errorf("notice: %q", got)
	}
	if got := Standing(store.Letter{}, &r, "yardr"); got != "chief is not logged in and cannot read mail "+
		"(run claude in another terminal and log in there)" {
		t.Errorf("standing: %q", got)
	}
	r.Handle["kind"] = "codex"
	if got := Known(r); !strings.Contains(got, "run codex in another terminal and log in there") {
		t.Errorf("notice names no login command of the kind: %q", got)
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

// At 80 columns the crew are one line on top and the conversation
// has the width; in a wide terminal they are a column. Either way the
// view is the terminal's size, the newest mail is in view and the keys
// are the last line.
func TestLayout(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	long := strings.Repeat("a long line that has to be wrapped at the width of the view ", 4)
	for i := range 30 {
		must(t, st.Mail(ctx, me, fmt.Sprintf("mail %d %s", i, long), "chief"))
	}
	must(t, st.Mail(ctx, me, "landed", "recorder"))

	v := open(t, st, "chief", 80, 24)
	v.fits(80, 24)
	lines := strings.Split(v.screen(), "\n")
	if lines[1] != "yard  [chief idle]  recorder idle (1)" {
		t.Fatalf("the crew's line at 80 columns is %q", lines[1])
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
	v.shows("> chief       idle", "  recorder    idle            1", " │ ", "mail 29")
	if lines := strings.Split(v.screen(), "\n"); lines[len(lines)-1] != Keys {
		t.Fatalf("the last line is %q", lines[len(lines)-1])
	}

	// Narrower than the keys and the crew: cut, not wrapped.
	v.send(tea.WindowSizeMsg{Width: 30, Height: 12})
	v.fits(30, 12)
	v.send(tea.WindowSizeMsg{Width: Wide - 1, Height: 24})
	v.fits(Wide-1, 24)
	if strings.Contains(v.screen(), "│") {
		t.Fatalf("a column below %d columns", Wide)
	}

	// Lower than all its rows, folded and wide: still the terminal's size,
	// under the title.
	for _, size := range [][2]int{{30, 9}, {30, 6}, {120, 5}} {
		v.send(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		v.fits(size[0], size[1])
		v.shows("yardr chat")
	}

	if n := ansi.StringWidth(Keys); n >= 80 {
		t.Fatalf("the keys are %d columns: more than a terminal of 80 shows", n)
	}
}

// rows is the view as a letter for each of its lines: T the title, F the
// crew members' line, R a rule, I a row of the input, K the keys, and a dot
// for a line of the conversation or the line of notices.
func (v *view) rows() string {
	var out strings.Builder
	for _, l := range strings.Split(v.screen(), "\n") {
		switch {
		case strings.HasPrefix(l, "yardr chat"):
			out.WriteByte('T')
		case strings.Contains(l, " │ "):
			out.WriteByte('.')
		case strings.Contains(l, "[chief ") || strings.Contains(l, "[yard]"):
			out.WriteByte('F')
		case strings.HasPrefix(l, "─"):
			out.WriteByte('R')
		case strings.HasPrefix(l, ">"):
			out.WriteByte('I')
		case strings.HasPrefix(l, "enter send") || strings.HasPrefix(l, "tab next"):
			out.WriteByte('K')
		default:
			out.WriteByte('.')
		}
	}
	return out.String()
}

// A terminal lower than the view's rows gives them up in order: the keys,
// the line of notices, the rows of the input down to one, the rules, the
// crew members' line. The view is never taller than the terminal while that
// has two rows, for the title and a line of the conversation.
func TestLowTerminal(t *testing.T) {
	st := yard(t)
	must(t, st.Mail(context.Background(), me, "a word", "chief")) // its text is the conversation's last line

	for _, c := range []struct {
		name  string
		width int
		yard  bool
		rows  []string // by height, from 1
	}{
		{"folded", 30, false, []string{"T.", "T.", "T.I", "TF.I", "TFR.I", "TFR.RI", "TFR.RII", "TFR.RIII", "TFR.RIII.", "TFR.RIII.K", "TFR..RIII.K"}},
		{"wide", 120, false, []string{"T.", "T.", "T.I", "T.RI", "T.RII", "T.RIII", "T.RIII.", "T.RIII.K", "T..RIII.K"}},
		{"folded, the yard's row", 30, true, []string{"T.", "T.", "TF.", "TFR.", "TFR.R", "TFR.R.", "TFR.R.K", "TFR..R.K"}},
		{"wide, the yard's row", 120, true, []string{"T.", "T.", "T.R", "T.R.", "T.R.K", "T..R.K"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := open(t, st, "chief", c.width, 24)
			if c.yard {
				v.key(tea.KeyShiftTab)
			} else {
				// More lines typed than the input keeps rows for.
				for _, line := range []string{"one", "two", "three", "four"} {
					v.typed(line)
					v.key(tea.KeyCtrlJ)
				}
			}
			// Down to one row and up again: a row given up comes back.
			heights := []int{}
			for h := len(c.rows); h >= 1; h-- {
				heights = append(heights, h)
			}
			for h := 2; h <= len(c.rows); h++ {
				heights = append(heights, h)
			}
			for _, h := range heights {
				v.send(tea.WindowSizeMsg{Width: c.width, Height: h})
				if got := v.rows(); got != c.rows[h-1] {
					t.Fatalf("at %d rows the view is %s, want %s:\n%s", h, got, c.rows[h-1], v.screen())
				}
				if h >= 2 {
					v.fits(c.width, h)
				}
				if !c.yard {
					v.shows("  a word")
				}
			}
		})
	}
}

// Many crew members on the folded line: the selected one stays on it.
func TestFoldedLineKeepsTheSelection(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	for i := range 12 {
		_, err := st.PutCrew(ctx, store.CrewMember{Name: fmt.Sprintf("worker-%02d", i), Runner: "herdr"}, "jo")
		must(t, err)
	}
	v := open(t, st, "worker-11", 80, 24)
	v.fits(80, 24)
	v.shows("[worker-11 not running]")
}

// Enter sends the line as mail to the selected crew member; ctrl+j breaks
// the line instead; an empty line sends nothing.
func TestSend(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	v := open(t, st, "recorder", 80, 24)
	v.key(tea.KeyEnter)
	v.typed("   ")
	v.key(tea.KeyEnter)
	if letters, _, _ := st.Conversation(ctx, me, "recorder", 0, 0, 0); len(letters) != 0 {
		t.Fatalf("an empty line was sent: %+v", letters)
	}
	v.m.input.Reset()
	v.typed("first line")
	v.key(tea.KeyCtrlJ)
	v.typed("second line")
	v.key(tea.KeyEnter)
	letters, _, err := st.Conversation(ctx, me, "recorder", 0, 0, 0)
	if err != nil || len(letters) != 1 || letters[0].Text != "first line\nsecond line" || letters[0].From != me {
		t.Fatalf("sent %+v, %v", letters, err)
	}
	if v.m.input.Value() != "" {
		t.Fatalf("the line still holds %q after sending", v.m.input.Value())
	}
	v.shows("first line", "second line", "· sent; recorder is idle")
	if in, _ := st.Inbox(ctx, "chief", 0); in.Total != 0 {
		t.Fatalf("the mail reached the chief too: %+v", in)
	}
}

// A mail that cannot be sent leaves what was typed in the line.
func TestSendFails(t *testing.T) {
	st := yard(t)
	v := open(t, st, "chief", 80, 24)
	must(t, st.DeleteCrew(context.Background(), "chief", "", "jo")) // gone before the view hears of it
	v.typed("are you there?")
	v.key(tea.KeyEnter)
	v.shows("not sent to chief", "are you there?")
	if v.m.input.Value() != "are you there?" {
		t.Fatalf("the line holds %q after a failed send", v.m.input.Value())
	}
}

// The size of a mail is counted before it is reached; past it the rule
// says how many mails the text is sent as, and what would pass the most
// mails a text is sent as is not taken.
func TestSizeLimit(t *testing.T) {
	st := yard(t)
	v := open(t, st, "chief", 80, 24)
	v.typed(strings.Repeat("x", warnAt-1))
	v.hides("bytes")
	v.typed("x")
	v.shows(fmt.Sprintf("%d of %d bytes", warnAt, store.MaxMailBytes))
	v.typed(strings.Repeat("x", store.MaxMailBytes-warnAt))
	v.shows(fmt.Sprintf("%d of %d bytes", store.MaxMailBytes, store.MaxMailBytes))
	v.typed("y")
	v.shows(fmt.Sprintf("%d bytes, sent as 2 mails", store.MaxMailBytes+1))
	v.hides(" of 4096 bytes", "a text is sent as at most")
	v.fits(80, 24)

	// Bytes, not characters: é is two.
	v.m.input.Reset()
	v.typed(strings.Repeat("é", store.MaxMailBytes/2))
	v.shows(fmt.Sprintf("%d of %d bytes", store.MaxMailBytes, store.MaxMailBytes))
	v.typed("é")
	v.shows("sent as 2 mails")

	// The most a text is sent as, and one byte more: refused whole.
	v.m.input.Reset()
	full := strings.Repeat("x", MaxParts*room)
	v.typed(full)
	v.shows(fmt.Sprintf("%d bytes, sent as %d mails", len(full), MaxParts))
	v.typed("more")
	if v.m.input.Value() != full {
		t.Fatalf("the line holds %d bytes, %d mails hold %d", len(v.m.input.Value()), MaxParts, len(full))
	}
	v.shows("a text is sent as at most 16 mails (about 64 KB)")
	v.fits(80, 24)
	v.key(tea.KeyEnter)
	v.hides("not sent", "went to")
	letters, _, err := st.Conversation(context.Background(), me, "chief", 0, 0, 0)
	if err != nil || len(letters) != MaxParts {
		t.Fatalf("%d mails of %d were sent, %v", len(letters), MaxParts, err)
	}
	if last := letters[MaxParts-1].Text; len(last) != store.MaxMailBytes || !strings.HasPrefix(last, "(16/16)\n") {
		t.Fatalf("the last mail has %d bytes and begins %q", len(last), last[:10])
	}
}

// A text of one mail's size is sent as it is, as one mail.
func TestSendOneMail(t *testing.T) {
	st := yard(t)
	v := open(t, st, "chief", 80, 24)
	text := "(1/2) is what a part begins with\n\n" + strings.Repeat("x", store.MaxMailBytes)
	text = text[:store.MaxMailBytes]
	v.typed(text)
	v.key(tea.KeyEnter)
	letters, _, err := st.Conversation(context.Background(), me, "chief", 0, 0, 0)
	if err != nil || len(letters) != 1 || letters[0].Text != text {
		t.Fatalf("a text of one mail's size: %d mails, %v", len(letters), err)
	}
}

// prose is a text of about size bytes: paragraphs of lines of words.
func prose(size int) string {
	var b strings.Builder
	for n := 0; b.Len() < size; n++ {
		switch {
		case n == 0:
		case n%40 == 0:
			b.WriteString("\n\n")
		case n%8 == 0:
			b.WriteString("\n")
		default:
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "word%04d", n)
	}
	return b.String()
}

// A text of two and a half mails is three parts, each within a mail with
// its number, split at the best break there is: a paragraph's end, else a
// line's, else a space, else between two characters.
func TestSplit(t *testing.T) {
	size := 5 * store.MaxMailBytes / 2
	lines := strings.ReplaceAll(prose(size), "\n\n", "\n")
	words := strings.ReplaceAll(lines, "\n", " ")
	for _, c := range []struct{ name, text, sep string }{
		{"paragraphs", prose(size), "\n\n"},
		{"lines", lines, "\n"},
		{"words", words, " "},
		{"two bytes a character", strings.Repeat("é", size/2), ""},
		{"three", "x" + strings.Repeat("€", size/3), ""},
		{"four", "xy" + strings.Repeat("🚂", size/4), ""},
	} {
		parts := split(c.text)
		if len(parts) != 3 {
			t.Fatalf("%s: %d parts", c.name, len(parts))
		}
		var texts []string
		for i, p := range parts {
			mail := p.mail(i, len(parts))
			if len(mail) > store.MaxMailBytes || !strings.HasPrefix(mail, fmt.Sprintf("(%d/3)\n", i+1)) {
				t.Fatalf("%s: part %d has %d bytes and begins %q", c.name, i+1, len(mail), mail[:8])
			}
			if !utf8.ValidString(p.text) {
				t.Fatalf("%s: part %d is cut inside a character", c.name, i+1)
			}
			if p.text != strings.TrimSpace(p.text) || !strings.HasPrefix(c.text[p.at:], p.text) {
				t.Fatalf("%s: part %d is not the text from byte %d on: %q…", c.name, i+1, p.at, p.text[:20])
			}
			texts = append(texts, p.text)
		}
		// Nothing is lost but the breaks themselves, and no part ends at
		// a lesser break than the text has.
		if got := strings.Join(texts, c.sep); got != c.text {
			t.Fatalf("%s: the parts do not make the text again", c.name)
		}
	}

	// A paragraph break that would leave a part nearly empty is out of
	// reach, and a line that is longer than a mail alone is cut at the limit.
	long := strings.Repeat("x", store.MaxMailBytes+100)
	parts := split("short\n\n" + long + "\nlast line")
	if len(parts) != 3 || parts[0].text != "short" || parts[1].text != long[:room] || parts[2].text != long[room:]+"\nlast line" {
		t.Fatalf("%d parts: %q… %d bytes, %d bytes", len(parts), parts[0].text[:5], len(parts[1].text), len(parts[2].text))
	}
	early := "short\n\n" + strings.ReplaceAll(prose(3*store.MaxMailBytes/2), "\n\n", "\n")
	if parts = split(early); len(parts) != 2 || len(parts[0].text) < room-80 {
		t.Fatalf("an early paragraph break was taken: %d parts, the first of %d bytes", len(parts), len(parts[0].text))
	}
}

// A long text is sent as numbered mails in order, and the conversation
// shows them as the mails they are, with one line for where they stand.
func TestSendLong(t *testing.T) {
	st := yard(t)
	v := open(t, st, "chief", 80, 24)
	text := prose(5 * store.MaxMailBytes / 2)
	v.typed("  " + text + "\n")
	v.shows(fmt.Sprintf("%d bytes, sent as 3 mails", len(text)))
	v.key(tea.KeyEnter)
	if v.m.input.Value() != "" {
		t.Fatalf("the line still holds %d bytes after sending", len(v.m.input.Value()))
	}
	letters, _, err := st.Conversation(context.Background(), me, "chief", 0, 0, 0)
	if err != nil || len(letters) != 3 {
		t.Fatalf("%d mails, %v", len(letters), err)
	}
	var texts []string
	for i, l := range letters {
		head, body, _ := strings.Cut(l.Text, "\n")
		if head != fmt.Sprintf("(%d/3)", i+1) {
			t.Fatalf("mail %d begins %q", i+1, head)
		}
		texts = append(texts, body)
	}
	if strings.Join(texts, "\n\n") != text {
		t.Fatal("the mails do not make the text again")
	}
	all := ansi.Strip(strings.Join(v.m.lines(80), "\n"))
	if n := strings.Count(all, "· sent; chief is idle"); n != 1 {
		t.Fatalf("%d lines say where the three mails stand", n)
	}
	for _, mark := range []string{"  (1/3)\n", "  (2/3)\n", "  (3/3)\n"} {
		if !strings.Contains(all, mark) {
			t.Fatalf("the conversation does not show %q", mark)
		}
	}
	v.hides("went to", "bytes")
}

// failing is a store that takes no mail from the nth on.
type failing struct {
	store.API
	n int
}

func (f *failing) Mail(ctx context.Context, to, text, actor string, files ...string) error {
	if f.n--; f.n < 0 {
		return errors.New("the store is away")
	}
	return f.API.Mail(ctx, to, text, actor, files...)
}

// A part that is not sent ends the send: the view says how many went, and
// the text from that part on is in the line again, to be sent as it is.
func TestSendLongFails(t *testing.T) {
	st := &failing{API: yard(t), n: 1}
	ctx := context.Background()
	v := open(t, st, "chief", 80, 24)
	text := prose(5 * store.MaxMailBytes / 2)
	parts := split(text)
	v.typed(text)
	v.key(tea.KeyEnter)
	v.shows("1 of 3 mails went to chief, the rest is in the line: the store is away")
	if got := v.m.input.Value(); got != text[parts[1].at:] {
		t.Fatalf("the line holds %d bytes, parts 2 and 3 are %d", len(got), len(text)-parts[1].at)
	}
	letters, _, err := st.Conversation(ctx, me, "chief", 0, 0, 0)
	if err != nil || len(letters) != 1 || letters[0].Text != "(1/3)\n"+parts[0].text {
		t.Fatalf("%d mails after the failure, %v", len(letters), err)
	}
	if all := ansi.Strip(strings.Join(v.m.lines(80), "\n")); !strings.Contains(all, "  (1/3)\n") {
		t.Fatalf("the conversation does not show the part that went:\n%s", all)
	}

	// Sent again, the rest is a text of its own.
	st.n = 2
	v.key(tea.KeyEnter)
	v.hides("went to")
	letters, _, err = st.Conversation(ctx, me, "chief", 0, 0, 0)
	if err != nil || len(letters) != 3 || letters[1].Text != "(1/2)\n"+parts[1].text || letters[2].Text != "(2/2)\n"+parts[2].text {
		t.Fatalf("%d mails after sending the rest, %v", len(letters), err)
	}

	// None went, and something was typed since: both are in the line.
	st.n = 0
	v.typed(text)
	cmd := v.m.send()
	v.typed("typed since")
	v.do(cmd)
	v.shows("0 of 3 mails went to chief")
	if got := v.m.input.Value(); got != text+"\n\ntyped since" {
		t.Fatalf("the line holds %d bytes after a failed send and a line typed", len(got))
	}
}

// The key for the pane brings it to the front in herdr, names the herdr
// command elsewhere, and says what to do when no session runs.
func TestPaneKey(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	in := open(t, st, "chief", 120, 24, func(o *Options) { o.InHerdr = true })
	in.key(tea.KeyCtrlG)
	if len(in.focused) != 1 || in.focused[0] != "yard-chief" {
		t.Fatalf("focused %v", in.focused)
	}
	in.shows("herdr shows chief now")

	out := open(t, st, "recorder", 120, 24)
	out.key(tea.KeyCtrlG)
	if len(out.focused) != 0 {
		t.Fatalf("outside herdr a pane was focused: %v", out.focused)
	}
	out.shows("recorder's pane: herdr agent attach yard-recorder (in herdr: herdr agent focus yard-recorder)")

	failing := open(t, st, "chief", 120, 24, func(o *Options) {
		o.InHerdr = true
		o.Focus = func(context.Context, string) error {
			return errors.New("herdr agent focus yard-chief: agent_not_found")
		}
	})
	failing.key(tea.KeyCtrlG)
	failing.shows("herdr agent focus yard-chief: agent_not_found")

	must(t, st.CrewStopped(ctx, "chief", "gone"))
	in.stream()
	in.key(tea.KeyCtrlG)
	if len(in.focused) != 1 {
		t.Fatalf("a pane was focused for a crew member that does not run: %v", in.focused)
	}
	in.shows("chief has no pane, it is not running: yardr crew start chief")
}

// A list of the crew read before a status the stream told of does
// not put the older status back.
func TestOlderListKeepsNewerStatus(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	v := open(t, st, "chief", 120, 24)
	before := head(t, st)
	stale, err := st.Crew(ctx)
	must(t, err)
	must(t, st.CrewStatus(ctx, "chief", "working"))
	v.stream()
	v.send(crewMsg{head: before, crew: stale})
	v.shows("chief       working")
	// One read after it is taken as it is.
	must(t, st.CrewStatus(ctx, "chief", "idle"))
	fresh, err := st.Crew(ctx)
	must(t, err)
	v.send(crewMsg{head: head(t, st), crew: fresh})
	v.shows("chief       idle")
}

// While the stream is lost the view says so; when it is back the events
// missed arrive and the notice goes.
func TestStreamLost(t *testing.T) {
	st := yard(t)
	v := open(t, st, "chief", 80, 24)
	v.send(StreamMsg{Up: false})
	v.shows("the yardr server is away")
	v.fits(80, 24)
	must(t, st.Mail(context.Background(), me, "said while you were away", "chief"))
	v.send(StreamMsg{Up: true})
	v.stream()
	v.hides("the yardr server is away")
	v.shows("said while you were away")
}

// The onboarding opens the view on a yardmaster (here: chief) that was asked
// to greet and may not have yet: its empty conversation says it is starting,
// the first mail replaces that when the stream brings it, and no other
// crew member's conversation claims the same.
func TestExpectedFirstMail(t *testing.T) {
	st := yard(t)
	v := open(t, st, "chief", 80, 24, func(o *Options) { o.Expect = "chief" })
	v.shows("chief is starting: it reads its role first, and its first message appears here.", "You can write to it already.")
	v.hides("No mail between you and chief yet.")
	must(t, st.Mail(context.Background(), me, "hello ada, which repository?", "chief"))
	v.stream()
	v.shows("hello ada, which repository?")
	v.hides("chief is starting")
	v.send(tea.KeyMsg{Type: tea.KeyTab})
	v.shows("No mail between you and recorder yet.")
}

// panes stands in for the runtime's panes: what each agent's shows, and
// how often and for how many lines it was read.
type panes struct {
	text  map[string]string
	err   error
	reads int
	lines int
}

func (p *panes) tail(_ context.Context, handle map[string]any, lines int) (string, error) {
	p.reads++
	p.lines = lines
	agent, _ := handle["agent"].(string)
	return p.text[agent], p.err
}

func (p *panes) option(o *Options) { o.Tail = p.tail }

const trust = "sh$ claude\n\n one\n two\n three\n Do you trust the files in this folder?\n\n ❯ 1. Yes, proceed\n   2. No, exit\n\n Enter to confirm\n"

// A selected crew member that is blocked says so above the input: what is
// known, the last lines of its pane and the key that goes there. The
// notice is no mail, takes no line from the screen's size, and goes when
// the crew member goes on or another is selected.
func TestWaitingNotice(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	p := &panes{text: map[string]string{"yard-chief": trust}}
	known := "! chief waits at a prompt in its pane: claude may wait at a login, a question or a system dialog"
	back := "; answer there and come back"

	v := open(t, st, "chief", 160, 40, p.option)
	v.hides("may wait at", "Do you trust", back)
	if p.reads != 0 {
		t.Fatalf("the pane of an idle crew member was read %d times", p.reads)
	}
	must(t, st.CrewStatus(ctx, "chief", "blocked"))
	v.stream()
	v.shows(known, "  │  three", "  │  Do you trust the files in this folder?", "  │  ❯ 1. Yes, proceed", "  │    2. No, exit",
		"  │  Enter to confirm", "ctrl+g names the command that opens its pane"+back)
	// The last six lines that say something, and no more.
	v.hides("sh$ claude", "  │  one")
	if p.lines != paneRows {
		t.Fatalf("the pane was read for %d lines, want %d", p.lines, paneRows)
	}
	v.fits(160, 40)
	// It is right above the input: the rule follows its last line.
	lines := strings.Split(v.screen(), "\n")
	at := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, back) })
	if !strings.HasPrefix(lines[at+1], "──") || !strings.HasPrefix(lines[at+2], "> ") {
		t.Fatalf("the notice is not right above the input:\n%s", v.screen())
	}

	// Mail is sent all the same, and the line under it has the notice's
	// words for where it stands.
	v.typed("deploys are frozen")
	v.key(tea.KeyEnter)
	v.stream()
	v.shows("deploys are frozen", "· chief waits at a prompt in its pane and cannot read mail (ctrl+g goes there)", known)
	if c, _, err := st.Conversation(ctx, me, "chief", 0, 0, 0); err != nil || len(c) != 1 {
		t.Fatalf("the conversation has %d mails, %v: the notice is none, and what was typed is one", len(c), err)
	}

	// Another crew member has none; back on the chief it is there again.
	v.key(tea.KeyTab)
	v.hides("may wait at", "Do you trust", back)
	v.key(tea.KeyShiftTab)
	v.shows(known, "Do you trust the files in this folder?")

	// Folded, and in a terminal too low for all of it, the view keeps its
	// size: the pane's lines give way first, the oldest of them first.
	v.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	v.fits(80, 24)
	v.shows("! chief waits at a prompt in its pane: claude may wait at a login", "  │  Enter to confirm", back)
	v.send(tea.WindowSizeMsg{Width: 80, Height: 16})
	v.fits(80, 16)
	v.shows("! chief waits at a prompt", "  │  Enter to confirm", back, "· chief waits at a prompt")
	v.hides("  │  three")
	v.send(tea.WindowSizeMsg{Width: 30, Height: 12})
	v.fits(30, 12)
	v.send(tea.WindowSizeMsg{Width: 30, Height: 10})
	v.fits(30, 10)
	v.send(tea.WindowSizeMsg{Width: 30, Height: 5})
	v.fits(30, 5)
	v.send(tea.WindowSizeMsg{Width: 160, Height: 40})

	// In herdr the key goes to the pane itself.
	in := open(t, st, "chief", 160, 40, p.option, func(o *Options) { o.InHerdr = true })
	in.shows(known, "ctrl+g opens its pane"+back)

	// The crew member goes on: the notice is gone with the event that says so.
	must(t, st.CrewStatus(ctx, "chief", "idle"))
	v.stream()
	v.hides("may wait at", "Do you trust", back)
	v.shows("· sent; chief is idle")
	v.fits(160, 40)
	// What comes back late for the watch that is over changes nothing.
	v.send(tailMsg{gen: v.m.watched.gen - 1, text: "a late answer"})
	v.hides("a late answer")
}

// A pane that cannot be read leaves the notice without lines of it, and
// so does a view that was given nothing to read panes with.
func TestWaitingNoticeWithoutPane(t *testing.T) {
	st := yard(t)
	must(t, st.CrewStatus(context.Background(), "chief", "blocked"))
	p := &panes{err: errors.New("herdr pane read: pane_not_found"), text: map[string]string{"yard-chief": trust}}
	for name, opt := range map[string]func(*Options){"unreadable": p.option, "no reader": func(*Options) {}} {
		v := open(t, st, "chief", 80, 24, opt)
		v.shows("! chief waits at a prompt in its pane: claude may wait at a login", "; answer there and come back")
		v.hides("│", "Do you trust", "pane_not_found")
		v.fits(80, 24)
		if name == "unreadable" && p.reads == 0 {
			t.Fatal("the pane was not asked for")
		}
	}
	// A pane that shows nothing is the same.
	p.err, p.text = nil, map[string]string{"yard-chief": "\n  \n"}
	v := open(t, st, "chief", 80, 24, p.option)
	v.shows("! chief waits at a prompt")
	v.hides("│")
}

// What a pane holds is shown as text and nothing else: an escape sequence
// in it, a control character, a mark that turns text around, none reaches
// the terminal of the view.
func TestPaneLinesAreInert(t *testing.T) {
	hostile := "\x1b[31mDo you \x1b[1mtrust\x1b[0m this folder?\x1b[2J\x1b[H\n" +
		"\x1b]0;a title\x07after the title\n" +
		"\x1b]52;c;ZXZpbA==\x1b\\after the clipboard\n" +
		"bell\x07 back\x08 tab\there del\x7f csi\u009b31m nul\x00 cr\rend\n" +
		"turned \u202eround\u2066 \xff\xfe\n" +
		"a lone escape \x1b"
	for in, want := range map[string][]string{
		hostile: {"Do you trust this folder?", "after the title", "after the clipboard",
			"bell back tab here del csi31m nul crend", "turned round", "a lone escape"},
		"one\n\n \t \ntwo  \nthree": {"two", "three"},
		"":                          nil,
	} {
		n := paneRows
		if strings.HasPrefix(in, "one") {
			n = 2
		}
		if got := inert(in, n); !slices.Equal(got, want) {
			t.Errorf("inert(%q)\n got %q\nwant %q", in, got, want)
		}
	}

	st := yard(t)
	must(t, st.CrewStatus(context.Background(), "chief", "blocked"))
	p := &panes{text: map[string]string{"yard-chief": hostile}}
	for _, colour := range []termenv.Profile{termenv.Ascii, termenv.TrueColor} {
		profile(t, colour, true)
		v := open(t, st, "chief", 80, 24, p.option)
		v.shows("  │ Do you trust this folder?", "  │ after the title", "  │ turned round")
		raw := v.m.View()
		for _, bad := range []string{"\x1b[31m", "\x1b[2J", "\x1b[H", "\x1b]", "\x07", "\x08", "\x7f", "\x00", "\r", "\t", "\u009b", "\u202e", "\u2066", "a title", "ZXZpbA"} {
			if strings.Contains(raw, bad) {
				t.Fatalf("the view passes %q of the pane on to the terminal:\n%q", bad, raw)
			}
		}
		if colour == termenv.Ascii && strings.Contains(raw, "\x1b") {
			t.Fatalf("an escape in a view without colour:\n%q", raw)
		}
	}
}

// A crew member whose first prompt is still to be delivered waits as a
// blocked one does, whatever its runtime reads it as, and says what the
// onboarding says of such a yardmaster. Once the prompt arrived, which no
// status tells, the notice is gone.
func TestFirstPromptPending(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	r, err := st.PutCrew(ctx, store.CrewMember{Name: "scribe", Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo")
	must(t, err)
	handle := map[string]any{"agent": "yard-scribe", "pane": "p1"}
	must(t, st.CrewStarted(ctx, "scribe", map[string]any{"agent": "yard-scribe", "pane": "p1", store.PendingPrompt: "You are scribe"}))
	p := &panes{text: map[string]string{"yard-scribe": "Select login method:"}}
	known := "! " + NotReady("claude")
	if !strings.Contains(known, "claude was started in the pane but is not ready for its first prompt: it may wait at a login, a question or a system dialog") {
		t.Fatalf("what is known reads %q", known)
	}

	for _, status := range []string{"", "idle", "unknown", "blocked"} {
		if status != "" {
			must(t, st.CrewStatus(ctx, "scribe", status))
		}
		v := open(t, st, "scribe", 200, 30, p.option)
		if status == "blocked" {
			v.shows("! scribe waits at a prompt in its pane: claude may wait at a login")
		} else {
			v.shows(known)
		}
		v.shows("  │ Select login method:", "; answer there and come back")
	}
	// Working on a prompt, it waits for nobody.
	must(t, st.CrewStatus(ctx, "scribe", "working"))
	open(t, st, "scribe", 200, 30, p.option).hides("may wait at", "Select login method:")

	must(t, st.CrewStatus(ctx, "scribe", "idle"))
	v := open(t, st, "scribe", 200, 30, p.option)
	v.typed("are you there?")
	v.key(tea.KeyEnter)
	v.stream()
	v.shows(known, "· scribe waits at a prompt in its pane and cannot read mail (ctrl+g goes there)")

	// The daemon delivers the prompt: the handle no longer holds it, and
	// the event of its arrival is what the view reads the crew for.
	must(t, st.SetCrewHandle(ctx, "scribe", handle))
	must(t, st.LogDispatch(ctx, r.Journal, "session_prompted", "yard", map[string]any{"crew": "scribe"}))
	v.stream()
	v.hides("may wait at", "Select login method:", "waits at a prompt")
	v.shows("· sent; scribe is idle")
}

// While the notice shows, the pane is read again every Options.Refresh:
// one read at a time for the crew member watched, whatever else happens, and
// none for a watch that is over.
func TestPaneIsReadAgain(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	must(t, st.CrewStatus(ctx, "chief", "blocked"))
	p := &panes{text: map[string]string{"yard-chief": "Do you trust this folder?", "yard-recorder": "Select login method:"}}
	v := open(t, st, "chief", 80, 24, p.option, func(o *Options) { o.Refresh = time.Millisecond })
	v.shows("  │ Do you trust this folder?")
	if p.reads != 1 {
		t.Fatalf("the pane was read %d times on opening, want 1", p.reads)
	}
	// Events come and go: the read is the timer's.
	must(t, st.Mail(ctx, me, "landed", "recorder"))
	must(t, st.CrewStatus(ctx, "recorder", "working"))
	v.stream()
	if p.reads != 1 {
		t.Fatalf("the pane was read %d times between two refreshes, want 1", p.reads)
	}
	p.text["yard-chief"] = "Allow claude to run git push?"
	v.send(refreshMsg{gen: v.m.watched.gen})
	v.shows("  │ Allow claude to run git push?")
	v.hides("Do you trust")

	// Another blocked crew member selected: its pane, and the timer of the
	// first reads nothing more.
	old := v.m.watched.gen
	must(t, st.CrewStatus(ctx, "recorder", "blocked"))
	v.stream()
	v.key(tea.KeyTab)
	v.shows("! recorder waits at a prompt", "  │ Select login method:")
	reads := p.reads
	v.send(refreshMsg{gen: old})
	if p.reads != reads {
		t.Fatal("the timer of a watch that is over read a pane")
	}
	// It goes on: nothing is read for it any more.
	must(t, st.CrewStatus(ctx, "recorder", "idle"))
	v.stream()
	v.send(refreshMsg{gen: v.m.watched.gen})
	if p.reads != reads {
		t.Fatal("a pane was read for a crew member that waits no more")
	}
}

// What the view reads and writes in the yard is what docs/api.md promises a
// client that is not yardr, and nothing else: a method added to Store is
// added to rpc.Calls, and so to the doc, or this fails.
func TestStoreIsTheDocumentedAPI(t *testing.T) {
	st := reflect.TypeOf((*Store)(nil)).Elem()
	for i := range st.NumMethod() {
		if name := st.Method(i).Name; rpc.Calls[name] == nil {
			t.Errorf("chat.Store has %s, which docs/api.md does not name (rpc.Calls)", name)
		}
	}
}

// What the view asks of the yard beyond that is documented too, as a call
// that is the operator's alone to make (rpc.OperatorCalls).
func TestSecretStoreIsTheOperatorAPI(t *testing.T) {
	st := reflect.TypeOf((*SecretStore)(nil)).Elem()
	for i := range st.NumMethod() {
		if name := st.Method(i).Name; rpc.OperatorCalls[name] == nil {
			t.Errorf("chat.SecretStore has %s, which docs/api.md does not name as a call for the operator alone (rpc.OperatorCalls)", name)
		}
	}
}

// Mail that carries an image has a line under its text that says where the
// image is, whether the conversation was read from the store or the mail
// came by the stream; mail without one has no such line.
func TestMailWithAnImage(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	shed, err := st.PutBlob(ctx, []byte("\x89PNG\r\n\x1a\n the shed"))
	must(t, err)
	fence, err := st.PutBlob(ctx, []byte("\xff\xd8\xff\xe0 the fence"))
	must(t, err)
	must(t, st.Mail(ctx, me, "the shed, painted", "chief", shed.Hash))
	v := open(t, st, "chief", 200, 24, func(o *Options) { o.Home = "/y" })
	v.shows("the shed, painted\n", "  1 image: /y/blobs/"+shed.Hash)
	must(t, st.Mail(ctx, me, "and nothing to see here", "chief"))
	must(t, st.Mail(ctx, me, "", "chief", shed.Hash, fence.Hash))
	v.stream()
	v.shows("and nothing to see here", "  2 images: /y/blobs/"+shed.Hash+" /y/blobs/"+fence.Hash)
	if n := strings.Count(v.screen(), "image"); n != 2 {
		t.Errorf("the view names images %d times, want 2:\n%s", n, v.screen())
	}
}
