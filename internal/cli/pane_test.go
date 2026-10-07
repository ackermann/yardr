package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/huh"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// loginPane is what a harness that is not logged in shows: no first prompt,
// and nothing after one.
const loginPane = "Not logged in · Run /login"

// blocked is a yard whose yardmaster's harness waits at a login: its session
// runs, its first prompt is pending, its pane shows loginPane.
type blocked struct {
	t  *testing.T
	st store.API
	f  *former
	// attached and focused are the agents herdr was asked to attach to and
	// to bring to the front, and chats the views opened.
	attached, focused, chats *[]string
	// logsIn: the person logs in while in the pane, and the daemon then
	// delivers the first prompt.
	logsIn bool
}

// blockedYardmaster is a terminal outside herdr in which the daemon, once
// started, leaves the yardmaster at a login. The form's questions are
// answered by the former it returns: panes are the answers to "open the
// yardmaster's pane?".
func blockedYardmaster(t *testing.T, panes ...string) *blocked {
	t.Helper()
	newHome(t)
	actAsOperator(t)
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	stubServices(t, "darwin")
	stubWeb(t)
	captureStderr(t)
	t.Setenv("HERDR_ENV", "")
	stub(t, &pollEvery, 0)
	b := &blocked{t: t, st: storetest.New(t), f: &former{t: t, panes: panes, goes: []string{goYardmaster}}}
	stubAsk(t, b.f.ask)
	handle := yardmasterHandle()
	handle[store.PendingPrompt] = "You are the yardr crew member \"yardmaster\"."
	stubStart(t, b.st, handle)
	pane := loginPane
	stub(t, &paneTail, func(context.Context, map[string]any, int) (string, error) { return pane, nil })
	b.chats = stubChat(t)
	var attached, focused []string
	b.attached, b.focused = &attached, &focused
	visit := func(to *[]string) func(context.Context, string) error {
		return func(_ context.Context, agent string) error {
			*to = append(*to, agent)
			if b.logsIn {
				pane = answeredPane
				if err := b.st.SetCrewHandle(context.Background(), "yardmaster", yardmasterHandle()); err != nil {
					t.Error(err)
				}
			}
			return nil
		}
	}
	stub(t, &attachAgent, visit(&attached))
	stub(t, &focusAgent, visit(&focused))
	return b
}

// start runs yardr init --start with a wait a test can sit out.
func (b *blocked) start(args ...string) (string, error) {
	b.t.Helper()
	return run(b.t, (&app{st: b.st}).rootCmd(), append([]string{"init", "--harness", "claude", "--start", "--wait", "30ms"}, args...)...)
}

// asked is how often the pane was offered.
func (b *blocked) asked() int {
	n := 0
	for _, k := range keysOf(b.f) {
		if k == "pane" {
			n++
		}
	}
	return n
}

// A yardmaster whose harness waits at a login is offered as its pane: the start
// says what is known and shows the pane, "Open the yardmaster's pane" attaches
// to it in this terminal after saying how to come back, and once the person
// is back and the yardmaster is up the chat opens, as after any start
// (yardr-qm3e; before, the start ended on what is known and how to go on).
func TestInitStartOffersTheYardmastersPane(t *testing.T) {
	b := blockedYardmaster(t, paneOpen)
	b.logsIn = true
	out, err := b.start()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantText(t, out, "yardmaster's session is running, but it has not answered its first prompt within 30ms.",
		"The last lines of its pane:\n\n  | "+loginPane+"\n",
		"Opening yardmaster's pane here. To come back: ctrl+b q (press ctrl+b, release, then q).\n"+
			"Leaving that way stops nothing: yardmaster keeps running, and this goes on.\n",
		"\nBack from yardmaster's pane.\nWaiting for yardmaster to answer its first prompt (up to 30ms).\n",
		"yardmaster is running, in herdr:", "herdr agent attach y-yardmaster",
		"You left the chat; yardmaster keeps running. To come back: "+here("yardr chat")+"\n")
	if b.asked() != 1 || !slices.Equal(*b.attached, []string{"y-yardmaster"}) || len(*b.focused) != 0 ||
		!slices.Equal(*b.chats, []string{"yardmaster, expecting yardmaster"}) {
		t.Fatalf("asked %d times, attached %v, focused %v, chats %v\n%s", b.asked(), *b.attached, *b.focused, *b.chats, out)
	}
	// A yardmaster that came up is no problem to explain, and the daemon is
	// named once.
	for _, s := range []string{"What is known:", "How to go on:"} {
		if strings.Contains(out, s) {
			t.Errorf("output has %q after the yardmaster came up:\n%s", s, out)
		}
	}
	if n := strings.Count(out, "The daemon is started"); n != 1 {
		t.Errorf("the daemon is named %d times:\n%s", n, out)
	}
}

// Back from the pane with the yardmaster still not up, it says so once, with
// the pane as it is, and asks again; "Not now" ends as the start without a
// question does, naming yardr init --start as the way back.
func TestInitStartAsksAgainWhileTheYardmasterIsBlocked(t *testing.T) {
	b := blockedYardmaster(t, paneOpen, paneOpen)
	out, err := b.start()
	if err == nil || err.Error() != "yardmaster is not up" {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if b.asked() != 3 || !slices.Equal(*b.attached, []string{"y-yardmaster", "y-yardmaster"}) || len(*b.chats) != 0 {
		t.Fatalf("asked %d times, attached %v, chats %v\n%s", b.asked(), *b.attached, *b.chats, out)
	}
	wantText(t, out, "What is known:\n  - claude was started in the pane but is not ready for its first prompt",
		"How to go on:", "this waits for it again and prints where it is: "+here("yardr init --start"))
	// The problem and the pane: before the first question and after each
	// return, and not again under the last answer.
	for _, s := range []string{"has not answered its first prompt within 30ms.", "The last lines of its pane:"} {
		if n := strings.Count(out, s); n != 3 {
			t.Errorf("%q is said %d times, want 3:\n%s", s, n, out)
		}
	}
	if n := strings.Count(out, "The daemon is started"); n != 1 {
		t.Errorf("the daemon is named %d times:\n%s", n, out)
	}
}

// "Not now", and a form left with ctrl+c, attach to nothing: the text is
// the one a start that asks nothing prints.
func TestInitStartNotNow(t *testing.T) {
	b := blockedYardmaster(t)
	out, err := b.start()
	if err == nil || b.asked() != 1 || len(*b.attached)+len(*b.focused)+len(*b.chats) != 0 {
		t.Fatalf("err %v, asked %d times, attached %v, focused %v, chats %v\n%s", err, b.asked(), *b.attached, *b.focused, *b.chats, out)
	}
	// Without a terminal nothing is asked: the same text.
	setInteractive(t, false)
	plain, err := b.start()
	if err == nil || b.asked() != 1 {
		t.Fatalf("err %v, asked %d times\n%s", err, b.asked(), plain)
	}
	from := "\nyardmaster's session is running"
	// The question stood under a blank line of its own.
	out = strings.Replace(out, "\n\n\nWhat is known:", "\n\nWhat is known:", 1)
	if i, j := strings.Index(out, from), strings.Index(plain, from); i < 0 || j < 0 || out[i:] != plain[j:] {
		t.Errorf("\"Not now\" ends other than a start that asks nothing.\n--- not now:\n%s\n--- no terminal:\n%s", out, plain)
	}

	setInteractive(t, true)
	b.f.abort = func(f huh.Field) bool { return f.GetKey() == "pane" }
	out, err = b.start()
	if err == nil || len(*b.attached) != 0 || !strings.Contains(out, "How to go on:") {
		t.Fatalf("ctrl+c at the question: err %v, attached %v\n%s", err, *b.attached, out)
	}
}

// Where no chat would open (no terminal, --json, --no-chat, an agent's own
// pane) nothing is asked and nothing attached: the text is as before, and
// names the attach command with the keys that leave it.
func TestInitStartAsksOnlyWhereTheChatOpens(t *testing.T) {
	b := blockedYardmaster(t, paneOpen, paneOpen, paneOpen, paneOpen)
	attach := "herdr agent attach y-yardmaster from any other terminal (ctrl+b q leaves it again, and stops nothing)"
	setInteractive(t, false)
	out, err := b.start()
	if err == nil {
		t.Fatalf("a blocked yardmaster is up\n%s", out)
	}
	wantText(t, out, "has not answered its first prompt within 30ms.", "  | "+loginPane, attach)

	setInteractive(t, true)
	if out, err = b.start("--no-chat"); err == nil {
		t.Fatalf("--no-chat: a blocked yardmaster is up\n%s", out)
	}
	wantText(t, out, attach)
	t.Setenv("YARDR_SESSION", "s1")
	if out, err = b.start(); err == nil {
		t.Fatalf("an agent's session: a blocked yardmaster is up\n%s", out)
	}
	wantText(t, out, attach)
	t.Setenv("YARDR_SESSION", "")
	out, err = run(t, (&app{st: b.st, json: true}).rootCmd(), "init", "--harness", "claude", "--start", "--wait", "30ms", "--json")
	if err == nil || !strings.Contains(out, `"running": false`) || !strings.Contains(out, "herdr agent attach y-yardmaster") {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	if b.asked() != 0 || len(*b.attached)+len(*b.focused)+len(*b.chats) != 0 {
		t.Fatalf("asked %d times, attached %v, focused %v, chats %v", b.asked(), *b.attached, *b.focused, *b.chats)
	}
}

// In a pane of herdr there is nothing to attach: herdr shows the yardmaster's
// pane, as yardr chat's key for it does, the text says how to come back,
// and this run waits behind it.
func TestInitStartInHerdrShowsThePane(t *testing.T) {
	b := blockedYardmaster(t, paneOpen)
	b.logsIn = true
	t.Setenv("HERDR_ENV", "1")
	out, err := b.start()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantText(t, out, "herdr shows yardmaster's pane now. To come back to this one: ctrl+b g opens herdr's list of panes.\n"+
		"This pane waits for yardmaster meanwhile, and opens the chat once it is up.\n",
		"Waiting for yardmaster to answer its first prompt (up to 30ms).\n", "You left the chat; yardmaster keeps running.")
	if !slices.Equal(*b.focused, []string{"y-yardmaster"}) || len(*b.attached) != 0 || len(*b.chats) != 1 {
		t.Fatalf("focused %v, attached %v, chats %v\n%s", *b.focused, *b.attached, *b.chats, out)
	}
}

// A pane herdr does not open ends the question: it says why, and how to go
// on, and asks no second time.
func TestInitStartWhenThePaneDoesNotOpen(t *testing.T) {
	b := blockedYardmaster(t, paneOpen, paneOpen)
	stub(t, &attachAgent, func(context.Context, string) error {
		return errors.New("herdr agent attach y-yardmaster: exit status 1")
	})
	out, err := b.start()
	if err == nil || b.asked() != 1 || len(*b.chats) != 0 {
		t.Fatalf("err %v, asked %d times, chats %v\n%s", err, b.asked(), *b.chats, out)
	}
	wantText(t, out, "yardmaster's pane could not be opened: herdr agent attach y-yardmaster: exit status 1\n", "How to go on:")
}

// A yardmaster without a session is nobody's pane to open: a start that failed
// asks nothing, whatever the terminal.
func TestInitStartOffersNoPaneOfAYardmasterThatDidNotStart(t *testing.T) {
	b := blockedYardmaster(t, paneOpen)
	stubStart(t, b.st, nil)
	out, err := b.start()
	if err == nil || b.asked() != 0 || len(*b.attached) != 0 {
		t.Fatalf("err %v, asked %d times, attached %v\n%s", err, b.asked(), *b.attached, out)
	}
	wantText(t, out, "yardmaster did not start within 30ms.")
}

// The onboarding's form goes the same way, since it starts the yard as
// yardr init --start does: the pane, and once the yardmaster is up the rest of
// the form, which ends in the chat. "Not now" ends in what doctor says.
func TestOnboardingOffersTheYardmastersPane(t *testing.T) {
	b := blockedYardmaster(t, paneOpen)
	b.logsIn = true
	stub(t, &yardmasterWait, 30*time.Millisecond)
	out, err := run(t, (&app{st: b.st}).rootCmd())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantText(t, out, "  | "+loginPane, "Opening yardmaster's pane here. To come back: ctrl+b q", "Back from yardmaster's pane.",
		"yardmaster is running, in herdr:", "You left the chat; yardmaster keeps running.")
	if b.asked() != 1 || !slices.Equal(*b.attached, []string{"y-yardmaster"}) || !slices.Equal(*b.chats, []string{"yardmaster, expecting yardmaster"}) ||
		!slices.Contains(keysOf(b.f), "go") {
		t.Fatalf("asked %d times (%v), attached %v, chats %v\n%s", b.asked(), keysOf(b.f), *b.attached, *b.chats, out)
	}

	later := blockedYardmaster(t)
	stub(t, &yardmasterWait, 30*time.Millisecond)
	out, err = run(t, (&app{st: later.st}).rootCmd())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantText(t, out, "How to go on:", "doctor` says of it:", "in this terminal again: it goes on from here.")
	if later.asked() != 1 || len(*later.attached)+len(*later.chats) != 0 || slices.Contains(keysOf(later.f), "go") {
		t.Fatalf("asked %d times (%v), attached %v, chats %v\n%s", later.asked(), keysOf(later.f), *later.attached, *later.chats, out)
	}
}

// yardr setup's "Talk to the yardmaster" finds the same yardmaster the same
// way: the pane, and the chat once it is up.
func TestSetupTalkToTheYardmasterOffersItsPane(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web"}, goes: []string{goYardmaster}, panes: []string{paneOpen, paneOpen}}
	home, st, _ := setupYard(t, f)
	mustAs(t, st, "Jo", "user", "add", "me")
	chats := stubChat(t)
	captureStderr(t)
	t.Setenv("HERDR_ENV", "")
	stub(t, &pollEvery, 0)
	stub(t, &yardmasterWait, 30*time.Millisecond)
	if err := st.CrewStarted(context.Background(), "yardmaster", yardmasterHandle()); err != nil {
		t.Fatal(err)
	}
	pane, visits := loginPane, 0
	stub(t, &paneTail, func(context.Context, map[string]any, int) (string, error) { return pane, nil })
	stub(t, &attachAgent, func(_ context.Context, agent string) error {
		// The second visit is the one that logs in.
		if visits++; agent == "y-yardmaster" && visits == 2 {
			pane = answeredPane
		}
		return nil
	})
	out := setup(t, st, "--actor", "me")
	wantText(t, out, "yardmaster has not answered its first prompt, so there is nobody to talk to yet.", "  | "+loginPane,
		"Opening yardmaster's pane here. To come back: ctrl+b q", "has not answered its first prompt within 30ms.",
		"You left the chat; yardmaster keeps running. To come back: yardr --home "+home+" chat\n")
	if visits != 2 || !slices.Equal(*chats, []string{"yardmaster, expecting yardmaster"}) {
		t.Fatalf("%d visits, chats %v\n%s", visits, *chats, out)
	}
	if strings.Contains(out, "doctor` says of it:") {
		t.Errorf("doctor is quoted of a yardmaster that came up:\n%s", out)
	}
}
