package cli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// stubChat stands in for the chat's view, which needs a terminal: it
// records each view opened, as "<selected>, expecting <resident>".
func stubChat(t *testing.T) *[]string {
	t.Helper()
	var opened []string
	stub(t, &runChat, func(_ context.Context, _ store.API, m *chat.Model, _ uint64) error {
		opened = append(opened, m.Selected()+", expecting "+m.Expects())
		return nil
	})
	return &opened
}

// actAsOperator makes the login name the form proposes as the operator
// ($USER) the name commands act as (the OS's): on a person's machine they
// are the same, and the operator is then the user the chat opens for. A
// gate may run with a $USER that is someone else, or none.
func actAsOperator(t *testing.T) {
	t.Helper()
	t.Setenv("USER", config.Actor())
}

// yardr init --start ends in the chat with the mayor where a person sits at
// a terminal, and nowhere else: without a terminal, as JSON, in an agent's
// own pane and with --no-chat it opens nothing, and what it prints names
// yardr chat.
func TestInitStartEndsInTheChatInATerminalOnly(t *testing.T) {
	home := newHome(t)
	actAsOperator(t)
	stubLookPath(t, "claude", "git", "herdr")
	st := storetest.New(t)
	stubStart(t, st, mayorHandle())
	stubPane(t, answeredPane)
	captureStderr(t)
	chats := stubChat(t)
	start := func(args ...string) string {
		t.Helper()
		out, err := run(t, (&app{st: st}).rootCmd(), append([]string{"init", "--harness", "claude", "--start"}, args...)...)
		if err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return out
	}
	names := "Talk to it in any terminal, or take\nthe same steps yourself as a short run of forms:\n\n  yardr --home " + home + " chat\n"

	setInteractive(t, false)
	wantText(t, start(), "mayor is running, in herdr", names)
	if len(*chats) != 0 {
		t.Fatalf("a chat opened without a terminal: %v", *chats)
	}

	setInteractive(t, true)
	var res struct {
		Start *startResult `json:"start"`
	}
	if err := json.Unmarshal([]byte(start("--json")), &res); err != nil || res.Start == nil || res.Start.Chat != "yardr --home "+home+" chat" {
		t.Fatalf("--json: start = %+v, %v", res.Start, err)
	}
	wantText(t, start("--no-chat"), names)
	t.Setenv("YARDR_RESIDENT", "mayor")
	wantText(t, start(), names)
	t.Setenv("YARDR_RESIDENT", "")
	if len(*chats) != 0 {
		t.Fatalf("a chat opened with --json, --no-chat or in an agent's pane: %v", *chats)
	}

	// The mayor was started more than once by now: it is not in the
	// session that was asked to greet, and the view expects no first mail.
	out := start()
	wantText(t, out, names, "You left the chat; mayor keeps running. To come back: yardr --home "+home+" chat\n")
	if !slices.Equal(*chats, []string{"mayor, expecting "}) {
		t.Fatalf("chats %v\n%s", *chats, out)
	}
}

// A mayor that does not come up leaves nobody to talk to: the form opens
// no chat, and says of it what yardr doctor says, the FAULT line of the
// mayor with why its starts fail.
func TestOnboardingWithoutAMayorSaysWhatDoctorSays(t *testing.T) {
	home, f := resumeYard(t, nil)
	actAsOperator(t)
	chats := stubChat(t)
	st := storetest.New(t)
	stub(t, &spawnDaemon, func(*config.Config) (<-chan error, error) {
		for range store.MaxStartFails {
			if err := st.ResidentStartFailed(context.Background(), "mayor", "claude: not logged in", time.Minute); err != nil {
				t.Error(err)
			}
		}
		return nil, nil
	})
	out, err := run(t, (&app{st: st}).rootCmd())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantText(t, out, "mayor could not be started: the daemon gave up after",
		"`yardr --home "+home+" doctor` says of it:",
		"  FAULT  mayor      mayor is not running: the daemon gave up after", "the last with: claude: not logged in. Once that is fixed: yardr --home "+home+" resident start mayor",
		"Once that is set right, run yardr --home "+home+" in this terminal again")
	doc, _ := run(t, (&app{st: st}).rootCmd(), "doctor")
	for _, l := range strings.Split(doc, "\n") {
		if strings.HasPrefix(l, "  FAULT  mayor") && !strings.Contains(out, l) {
			t.Errorf("the form does not say doctor's line %q:\n%s", l, out)
		}
	}
	if len(*chats) != 0 || slices.Contains(keysOf(f), "go") {
		t.Fatalf("chats %v, asked %q after a mayor that is not up", *chats, keysOf(f))
	}
}

// A mayor whose session runs and that never answered its first prompt (a
// harness at a login screen) is not up either: no chat, and doctor's lines
// with the one doctor cannot know, since it reads no pane.
func TestOnboardingWithASilentMayorOpensNoChat(t *testing.T) {
	home := newHome(t)
	actAsOperator(t)
	setInteractive(t, true)
	stubLookPath(t, "claude", "git", "herdr")
	stubAsk(t, (&former{t: t, goes: []string{goMayor}}).ask)
	captureStderr(t)
	chats := stubChat(t)
	st := storetest.New(t)
	stubStart(t, st, mayorHandle())
	stubPane(t, "Not logged in · Run /login")
	stub(t, &mayorWait, 50*time.Millisecond)
	out, err := run(t, (&app{st: st}).rootCmd())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	wantText(t, out, "it has not answered its first prompt within", "  | Not logged in · Run /login",
		"`yardr --home "+home+" doctor` says of it:", "  ok     mayor      mayor is running (herdr agent y-mayor)",
		"  note   mayor      mayor's session runs, and it has not answered its first prompt")
	if len(*chats) != 0 {
		t.Fatalf("a chat opened on a mayor that is not up: %v", *chats)
	}
}

// The operator was given another name than this terminal acts under: the
// mayor writes to the operator, so a chat as anyone else would stay empty.
// None opens; it says who the chat is for and how to get there, and the
// hand-over is the pane, as before.
func TestHandOverAsSomeoneWhoIsNoUser(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web"}, goes: []string{goMayor}}
	home, st, _ := setupYard(t, f)
	focused := stubFocus(t)
	chats := stubChat(t)
	if err := st.ResidentStarted(context.Background(), "mayor", mayorHandle()); err != nil {
		t.Fatal(err)
	}
	out := setup(t, st, "--actor", "eve")
	wantText(t, out, "this terminal acts as eve, who is none: the operator is Jo",
		"yardr --home "+home+" --actor Jo chat", "yardr --home "+home+" --actor Jo user add eve", "mayor is running, in herdr:")
	if len(*chats) != 0 || !slices.Equal(*focused, []string{"y-mayor"}) {
		t.Fatalf("chats %v, focused %v\n%s", *chats, *focused, out)
	}
}

// The view's own failure ends the hand-over with it, and claims no chat.
func TestHandOverReportsAViewThatFails(t *testing.T) {
	f := &former{answers: map[string]any{"name": "web"}, goes: []string{goMayor}}
	_, st, _ := setupYard(t, f)
	mustAs(t, st, "Jo", "user", "add", "me")
	if err := st.ResidentStarted(context.Background(), "mayor", mayorHandle()); err != nil {
		t.Fatal(err)
	}
	stub(t, &runChat, func(context.Context, store.API, *chat.Model, uint64) error { return errors.New("no tty") })
	out, err := run(t, (&app{st: st}).rootCmd(), "setup", "--actor", "me")
	if err == nil || !strings.Contains(err.Error(), "no tty") || strings.Contains(out, "You left the chat") {
		t.Fatalf("%v\n%s", err, out)
	}
}
