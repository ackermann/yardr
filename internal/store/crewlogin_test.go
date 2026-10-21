package store_test

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// crewLogins are the crew_login events after seq at, as the stream has them.
func crewLogins(t *testing.T, s store.API, at uint64) []store.Event {
	t.Helper()
	var out []store.Event
	for _, e := range kindsAfter(t, s, at) {
		if e.Kind == "crew_login" {
			out = append(out, e)
		}
	}
	return out
}

// handleWith is a copy of the handle h with the keys of kv set.
func handleWith(h, kv map[string]any) map[string]any {
	out := maps.Clone(h)
	maps.Copy(out, kv)
	return out
}

// A session sitting at its harness's login screen, and the yard leading
// that login, are two facts of Crew and GetCrew (login_screen, login_led),
// and a client that follows the stream is told when either changes: one
// crew_login for the write of the handle that changes it. The daemon
// writes the handle on many passes for other reasons (a prompt waiting, an
// error told of), and a write that leaves both facts as they were tells of
// nothing.
func TestCrewLoginIsToldOnChange(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	base := map[string]any{"agent": "ym", "pane": "p1"}
	if err := s.CrewStarted(ctx, "yardmaster", base); err != nil {
		t.Fatal(err)
	}
	at, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := func(h map[string]any) {
		t.Helper()
		if err := s.SetCrewHandle(ctx, "yardmaster", h); err != nil {
			t.Fatal(err)
		}
	}
	// read is the two facts as Crew says them and as GetCrew says them.
	read := func() (screen, led bool) {
		t.Helper()
		one, err := s.GetCrew(ctx, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		rows, err := s.Crew(ctx)
		if err != nil || len(rows) != 1 {
			t.Fatalf("Crew = %+v, %v", rows, err)
		}
		if rows[0].LoginScreen != one.LoginScreen || rows[0].LoginLed != one.LoginLed {
			t.Errorf("Crew says %v and %v, GetCrew %v and %v", rows[0].LoginScreen, rows[0].LoginLed, one.LoginScreen, one.LoginLed)
		}
		return one.LoginScreen, one.LoginLed
	}
	// told checks that the writes since the last check raised exactly
	// want, each as the two facts stood after it.
	told := func(what string, want ...[2]bool) {
		t.Helper()
		var got [][2]bool
		for _, e := range crewLogins(t, s, at) {
			screen, _ := e.Data["login_screen"].(bool)
			led, _ := e.Data["login_led"].(bool)
			got = append(got, [2]bool{screen, led})
			if e.Data["crew"] != "yardmaster" || e.Data["user"] != nil || e.Bead != nil || e.Actor != "yard" {
				t.Errorf("%s: event %+v, want the line session of yardmaster, on no bead", what, e)
			}
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d crew_login events %v, want %d %v", what, len(got), got, len(want), want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: event %d says login_screen and login_led %v, want %v", what, i+1, got[i], want[i])
			}
		}
		if at, err = s.Head(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// Writes that say nothing of a login tell nothing.
	set(handleWith(base, map[string]any{store.PendingPrompt: "first prompt"}))
	set(handleWith(base, map[string]any{"harness_error": "rate_limited"}))
	set(base)
	told("handle writes with no login")
	if screen, led := read(); screen || led {
		t.Errorf("a session at no login screen reads %v and %v", screen, led)
	}

	// It sits down at the screen: told once, whatever else the handle holds.
	atScreen := handleWith(base, map[string]any{store.HarnessLoginKey: "Select login method:", store.PendingPrompt: "first prompt"})
	set(atScreen)
	told("sitting down at the login screen", [2]bool{true, false})
	if screen, led := read(); !screen || led {
		t.Errorf("at the login screen it reads %v and %v, want true and false", screen, led)
	}
	// Writes that leave both facts as they were tell nothing: the same
	// again, another line of the screen, another key.
	set(atScreen)
	set(handleWith(atScreen, map[string]any{store.HarnessLoginKey: "Paste code here if prompted>"}))
	set(handleWith(atScreen, map[string]any{"harness_error": "not_logged_in"}))
	told("writes at the login screen that change neither fact")

	// The yard leads the login: told once, and not again as the ask goes
	// on from step to step.
	led := handleWith(atScreen, map[string]any{store.HarnessLoginAskKey: map[string]any{"ask": 4, "step": "pick"}})
	set(led)
	told("the yard leading the login", [2]bool{true, true})
	if screen, led := read(); !screen || !led {
		t.Errorf("with the yard leading it reads %v and %v, want true and true", screen, led)
	}
	set(handleWith(led, map[string]any{store.HarnessLoginAskKey: map[string]any{"ask": 5, "step": "input"}}))
	told("the led login going on to its next step")

	// It leaves the screen with the lead still marked, and then the lead
	// ends: told each time.
	set(handleWith(base, map[string]any{store.HarnessLoginAskKey: map[string]any{"ask": 5, "step": "input"}}))
	told("leaving the login screen", [2]bool{false, true})
	set(base)
	told("the lead ending", [2]bool{false, false})
	if screen, led := read(); screen || led {
		t.Errorf("after the login it reads %v and %v, want neither", screen, led)
	}
	set(handleWith(base, map[string]any{store.PendingPrompt: "first prompt"}))
	told("a write after the login")
}

// Crew and GetCrew say the two facts of a running session alone, and say
// them in JSON only while they are true, as model and kind are said only
// where the config names them. They are read out of the handle each time
// and stored nowhere: a definition read at the screen and saved back keeps
// neither. A session that starts or stops has its own event, and the facts
// changing with it tell nothing more.
func TestCrewLoginFieldsAreDerived(t *testing.T) {
	t.Parallel()
	s := storetest.New(t)
	ctx := context.Background()
	yardmaster(t, s)
	served := func() string {
		t.Helper()
		r, err := s.GetCrew(ctx, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		return jsonOf(t, r)
	}
	says := func(raw, key string) bool { return strings.Contains(raw, `"`+key+`"`) }
	at, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// A mark on the handle of a session that does not run is no login
	// screen, and tells nothing.
	marked := map[string]any{store.HarnessLoginKey: "Select login method:", store.HarnessLoginAskKey: map[string]any{"ask": 4, "step": "pick"}}
	if err := s.SetCrewHandle(ctx, "yardmaster", marked); err != nil {
		t.Fatal(err)
	}
	if raw := served(); says(raw, "login_screen") || says(raw, "login_led") {
		t.Errorf("a stopped session is served as %s, want neither login field", raw)
	}
	if evs := crewLogins(t, s, at); len(evs) != 0 {
		t.Errorf("a mark on a session that does not run told %+v", evs)
	}

	// A session started with the marks on its handle sits there: the start
	// is told as a start.
	if err := s.CrewStarted(ctx, "yardmaster", marked); err != nil {
		t.Fatal(err)
	}
	if raw := served(); !says(raw, "login_screen") || !says(raw, "login_led") {
		t.Errorf("a session at the login screen is served as %s, want both login fields", raw)
	}

	// A definition read at the screen and saved back keeps neither field.
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil || !r.LoginScreen || !r.LoginLed {
		t.Fatalf("GetCrew at the screen = %+v, %v", r, err)
	}
	saved, err := s.PutCrew(ctx, *r, "me")
	if err != nil {
		t.Fatal(err)
	}
	if saved.LoginScreen || saved.LoginLed {
		t.Errorf("a definition read at the screen was stored with %v and %v", saved.LoginScreen, saved.LoginLed)
	}

	// The stop takes the screen with it, and says so as a stop.
	if err := s.CrewStopped(ctx, "yardmaster", "restarted"); err != nil {
		t.Fatal(err)
	}
	if raw := served(); says(raw, "login_screen") || says(raw, "login_led") {
		t.Errorf("a session stopped at the screen is served as %s, want neither login field", raw)
	}
	if evs := crewLogins(t, s, at); len(evs) != 0 {
		t.Errorf("a start and a stop told %+v besides crew_started and crew_stopped", evs)
	}
}

// A conversation session sits at its own login screen: its facts are on its
// row of Conversations, and the event says whose they are, as crew_status
// does. The line session's stay as they were.
func TestCrewLoginOfAConversation(t *testing.T) {
	t.Parallel()
	s, ctx := sessionYard(t)
	serveYardmaster(t, s, ctx, "", map[string]string{"hal": "/tmp/hal.env"})
	if err := s.CrewStarted(ctx, "yardmaster", map[string]any{"agent": "ym"}); err != nil {
		t.Fatal(err)
	}
	if err := s.CrewStartedFor(ctx, "yardmaster", "hal", map[string]any{"agent": "ym-hal"}); err != nil {
		t.Fatal(err)
	}
	at, err := s.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCrewHandleFor(ctx, "yardmaster", "hal", map[string]any{"agent": "ym-hal", store.HarnessLoginKey: "Select login method:"}); err != nil {
		t.Fatal(err)
	}
	evs := crewLogins(t, s, at)
	if len(evs) != 1 || evs[0].Data["crew"] != "yardmaster" || evs[0].Data["user"] != "hal" ||
		evs[0].Data["login_screen"] != true || evs[0].Data["login_led"] != false {
		t.Fatalf("hal's session sat down at the login screen: %+v, want one crew_login of yardmaster for hal", evs)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	if r.LoginScreen || r.LoginLed {
		t.Errorf("the line session reads %v and %v, want neither: hal's is at the screen", r.LoginScreen, r.LoginLed)
	}
	if cs := r.Conversations["hal"]; !cs.LoginScreen || cs.LoginLed {
		t.Errorf("hal's session reads %v and %v, want true and false", cs.LoginScreen, cs.LoginLed)
	}
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
