package runner

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func shortPromptWait(t *testing.T) {
	t.Helper()
	old := promptWait
	promptWait = 400 * time.Millisecond
	t.Cleanup(func() { promptWait = old })
}

// droppingHerdr is a fake herdr whose agent is idle from the start and drops
// the first `drop` prompts it is sent, as a Claude Code that is still coming
// up does: `agent prompt` succeeds and the pane shows nothing of it. A later
// prompt shows in the pane. Every call is logged.
func droppingHerdr(t *testing.T, drop int) (h *Herdr, calls func() string) {
	t.Helper()
	return promptHerdr(t, drop, `echo '{"result":{"type":"ok"}}'`)
}

// notReadyHerdr is a droppingHerdr whose agent is at a question yardr does
// not answer when it is started (agent start says agent_not_ready, as for a
// Codex at its folder-trust dialog) and idle from then on, as once a person
// has answered: the first prompt is left for the dispatcher to deliver.
func notReadyHerdr(t *testing.T, drop int) (h *Herdr, calls func() string) {
	t.Helper()
	shortTrustWait(t)
	return promptHerdr(t, drop, `echo '{"error":{"code":"agent_not_ready","message":"agent is blocked during startup and is not ready for prompts"},"id":"x"}' >&2; exit 1`)
}

func promptHerdr(t *testing.T, drop int, start string) (h *Herdr, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "agent start") `+start+` ;;
  "agent prompt")
    echo x >> sent
    if [ "$(wc -l < sent)" -gt `+fmt.Sprint(drop)+` ]; then echo "❯ $4" >> pane; fi
    echo '{"result":{"type":"ok"}}' ;;
  "pane read") echo "────"; cat pane 2>/dev/null; echo "❯ " ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }
}

// Found live (yardr-ykbt): after a handoff the daemon restarted a crew member,
// answered its trust dialog and sent the first prompt while Claude Code was
// still coming up. herdr said the keys were sent, the pane never showed
// them, and the session sat idle with no role until someone asked it why.
// A prompt that does not show is kept pending, and the dispatcher delivers
// it on a later pass, once.
func TestLostFirstPromptIsDeliveredAgainOnce(t *testing.T) {
	shortPromptWait(t)
	h, calls := droppingHerdr(t, 1)
	ctx := context.Background()
	s := storetest.New(t)
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := d.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil || !r.Live() {
		t.Fatalf("crew member not running: %+v %v", r, err)
	}
	prompt, _ := r.Handle[dispatch.PendingPrompt].(string)
	if !strings.HasPrefix(prompt, CrewPromptLead) || strings.Count(calls(), "agent prompt") != 1 {
		t.Fatalf("a prompt that did not show is not pending: handle %v\n%s", r.Handle, calls())
	}
	for range 3 {
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r, _ = s.GetCrew(ctx, "yardmaster"); r.Handle[dispatch.PendingPrompt] != nil {
		t.Fatalf("still pending: %v", r.Handle)
	}
	if n := strings.Count(calls(), "agent prompt"); n != 2 {
		t.Fatalf("prompt sent %d times, want the lost one and one more:\n%s", n, calls())
	}
	if shown, err := h.PromptShown(ctx, r.Handle, prompt); !shown || err != nil {
		t.Fatalf("the pane does not show the prompt:\n%s", calls())
	}
}

// The usual start: the prompt shows in the pane, nothing is pending and no
// later pass sends it again.
func TestFirstPromptThatShowsIsSentOnce(t *testing.T) {
	h, calls := droppingHerdr(t, 0)
	ctx := context.Background()
	s := storetest.New(t)
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.GetCrew(ctx, "yardmaster")
	if err != nil || !r.Live() || r.Handle[dispatch.PendingPrompt] != nil {
		t.Fatalf("crew member = %+v %v", r, err)
	}
	if n := strings.Count(calls(), "agent prompt"); n != 1 {
		t.Fatalf("prompt sent %d times:\n%s", n, calls())
	}
}

// A bead session's first prompt goes the same way (startAgent serves both).
// With nothing of the prompt in the pane, an agent that is idle did not
// take it. One that is working is taken to have, and since no echo says
// so, the prompt stays on the handle as unconfirmed for the dispatcher to
// look at again (yardr-myyv). The handle says what was seen either way.
func TestStartAgentChecksThePromptArrived(t *testing.T) {
	shortPromptWait(t)
	for status, unconfirmed := range map[string]bool{"working": true, "idle": false} {
		h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "pane read") echo "❯ " ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"`+status+`"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
		handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "Read /wt/brief-1.md and follow it.",
			map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := handle[dispatch.PromptUnconfirmed] == true; handle[dispatch.PendingPrompt] == nil || got != unconfirmed {
			t.Errorf("agent %s: unconfirmed = %v, want a pending prompt and %v (%v)", status, got, unconfirmed, handle)
		}
		look, _ := handle[dispatch.PromptLook].(string)
		if want := map[string]string{"working": "working, look 1", "idle": "absent, look "}[status]; !strings.HasPrefix(look, want) {
			t.Errorf("agent %s: %s = %q, want %q", status, dispatch.PromptLook, look, want)
		}
	}
}

// A pane that cannot be read tells nothing about the prompt. It is taken as
// arrived, as before yardr looked, and left to the dispatcher as
// unconfirmed: a read that failed once says nothing of the next
// (yardr-myyv), and of a herdr that can read no pane the dispatcher's own
// look fails too, which ends the wait without a second send.
func TestStartAgentUnreadablePaneIsUnconfirmed(t *testing.T) {
	h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "pane read") echo '{"error":{"code":"unknown_command","message":"no pane read"}}' >&2; exit 1 ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
	if err != nil || handle[dispatch.PendingPrompt] != "read the brief" || handle[dispatch.PromptUnconfirmed] != true ||
		handle[dispatch.PromptLook] != "unreadable, look 1" {
		t.Fatalf("handle = %v, err = %v", handle, err)
	}
}

// A handle without a pane has nothing to look at, now or on a later pass:
// its prompt is not kept.
func TestStartAgentWithoutPaneIsNotPending(t *testing.T) {
	h := &Herdr{Bin: deafHerdrBin(t, `echo '{"result":{"type":"ok"}}'`)}
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1"}, true)
	if err != nil || handle[dispatch.PendingPrompt] != nil || handle[dispatch.PromptLook] != "no pane, look 0" {
		t.Fatalf("handle = %v, err = %v", handle, err)
	}
}

func TestShowsPrompt(t *testing.T) {
	prompt := "Read /Users/x/repo/.git/worktrees/b1/yardr/brief-1x7.md and follow it."
	for text, want := range map[string]bool{
		"❯ " + prompt + "\n\n⏺ Reading the brief": true,
		// Wrapped to a narrow pane, in the middle of the path.
		"❯ Read /Users/x/repo/.g\n  it/worktrees/b1/yardr/brief-1x7.md and\n  follow it.": true,
		"────\n❯ \n────":                                                                  false,
		"":                                                                                false,
		// Another session's brief.
		"❯ Read /Users/y/other/brief-9.md and follow it.": false,
	} {
		if got := showsPrompt(text, prompt); got != want {
			t.Errorf("showsPrompt(%q) = %v, want %v", text, got, want)
		}
	}
	if showsPrompt("anything", " ") {
		t.Error("an empty prompt shows nowhere")
	}
}

// The yardmaster yardr init adds greets the operator by mail in its first
// session: the onboarding ends in yardr chat, which shows mail and no pane.
// The prompt still begins and ends as every crew member's does, which is what
// the pane is searched for, and a later session is asked for no greeting.
func TestFirstPromptAsksForAGreetingByMail(t *testing.T) {
	r := store.CrewMember{Name: "yardmaster", Config: map[string]any{GreetKey: "ada"}}
	got := crewPrompt("yardr --home /y", r)
	for _, want := range []string{"`yardr --home /y mail ada \"<text>\"`", "`yardr --home /y chat`", "the next setup step",
		"`yardr --home /y prime --crew yardmaster` " + CrewPromptEnd} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
	plain := crewPrompt("yardr", store.CrewMember{Name: "yardmaster"})
	if plain != "You are the yardr crew member \"yardmaster\". Run `yardr prime --crew yardmaster` and follow it." {
		t.Errorf("a crew member that greets nobody got %q", plain)
	}
	if !strings.HasPrefix(got, CrewPromptLead+` "yardmaster". `) || !strings.HasSuffix(got, CrewPromptEnd) ||
		!showsPrompt("❯ "+got, got) {
		t.Errorf("the greeting's prompt does not begin and end as a crew member's:\n%s", got)
	}
	now := time.Now()
	r.StartedAt = &now
	if again := crewPrompt("yardr", r); again != plain {
		t.Errorf("a session after the first is asked to greet again:\n%s", again)
	}
	if Greets(r) != "" || Greeted(r) {
		t.Errorf("a crew member that is not running greets: %q %v", Greets(r), Greeted(r))
	}
	r.State = "running"
	if !Greeted(r) {
		t.Error("the first session of a crew member that greets is not the greeting one")
	}
	r.PrevStartedAt = &now
	if Greeted(r) {
		t.Error("a second session counts as the greeting one")
	}
}

// Found live (yardr-85jy): a Codex left at its folder-trust dialog got its
// first prompt from the dispatcher in the second a person answered the
// dialog, and dropped the keys; the prompt was cleared as delivered and the
// session sat at an empty input. The dispatcher's delivery is checked for
// arrival as startAgent's is: a prompt that arrives is sent once, one that
// is dropped once is sent again and arrives, and one that is dropped every
// time is sent PromptTries times, reported and left alone.
func TestDispatchersDeliveryIsCheckedForArrival(t *testing.T) {
	for _, c := range []struct {
		name                               string
		drop, sent, lost, gaveUp, prompted int
	}{
		{"arrives", 0, 1, 0, 0, 1},
		{"dropped once", 1, 2, 1, 0, 1},
		{"dropped every time", 1000, dispatch.PromptTries, dispatch.PromptTries, 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, calls := notReadyHerdr(t, c.drop)
			ctx := context.Background()
			s := storetest.New(t)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := s.GetCrew(ctx, "yardmaster")
			if err != nil || !r.Live() {
				t.Fatalf("crew member not running: %+v %v", r, err)
			}
			prompt, _ := r.Handle[dispatch.PendingPrompt].(string)
			if !strings.HasPrefix(prompt, CrewPromptLead) || strings.Contains(calls(), "agent prompt") {
				t.Fatalf("the prompt of an agent that was not ready is not pending: handle %v\n%s", r.Handle, calls())
			}
			for range 8 {
				if err := d.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if n := strings.Count(calls(), "agent prompt"); n != c.sent {
				t.Fatalf("prompt sent %d times, want %d:\n%s", n, c.sent, calls())
			}
			r, _ = s.GetCrew(ctx, "yardmaster")
			if pending := r.Handle[dispatch.PendingPrompt] != nil; pending != (c.prompted == 0) {
				t.Fatalf("pending = %v: handle %v", pending, r.Handle)
			}
			if shown, err := h.PromptShown(ctx, r.Handle, prompt); shown != (c.prompted == 1) || err != nil {
				t.Fatalf("the pane shows the prompt: %v %v\n%s", shown, err, calls())
			}
			evs, err := s.Events(ctx, r.Journal, 100)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, e := range evs {
				got[e.Kind]++
				if e.Kind == "session_prompt_gave_up" {
					agent, _ := r.Handle["agent"].(string)
					if hint, _ := e.Data["hint"].(string); agent == "" || !strings.HasPrefix(hint, "herdr agent prompt "+agent+" '"+CrewPromptLead) {
						t.Errorf("session_prompt_gave_up does not say how to give the prompt by hand: %+v", e.Data)
					}
				}
			}
			if got["session_prompt_lost"] != c.lost || got["session_prompt_gave_up"] != c.gaveUp || got["session_prompted"] != c.prompted {
				t.Fatalf("events = %v, want %d lost, %d gave up, %d prompted", got, c.lost, c.gaveUp, c.prompted)
			}
		})
	}
}

// typingHerdr is a fake herdr whose agent is idle and takes the text of a
// prompt into its input without the Enter that follows it, as the Claude
// Code of yardr-cxsb did: the pane shows the text in the input box, between
// its rules, and nothing above it. Enter sent to the pane (pane send-keys
// ... enter) submits what is in the input, which then shows above an empty
// one, unless the agent is deaf to it. An agent that is not ready when it
// is started leaves its prompt to the dispatcher. Every call is logged.
func typingHerdr(t *testing.T, deaf, ready bool) (h *Herdr, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	start := `echo '{"result":{"type":"ok"}}'`
	if !ready {
		shortTrustWait(t)
		start = `echo '{"error":{"code":"agent_not_ready","message":"agent is blocked during startup and is not ready for prompts"},"id":"x"}' >&2; exit 1`
	}
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "agent start") `+start+` ;;
  "agent prompt") printf '%s' "$4" >> input; echo '{"result":{"type":"ok"}}' ;;
  "pane send-keys")
    if [ "$4" = enter ] && [ `+fmt.Sprint(deaf)+` = false ] && [ -s input ]; then
      echo "❯ $(cat input)" >> pane; echo "⏺ On it." >> pane; : > input
    fi ;; # as herdr: nothing printed
  "pane read") cat pane 2>/dev/null; echo "────"; echo "❯ $(cat input 2>/dev/null)"; echo "────"; echo "  ? for shortcuts" ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }
}

// Found live (yardr-cxsb): a crew member restarted by its handoff had its
// prime in Claude Code's input, not sent, and sat idle for 95 seconds until
// a person pressed Return. The text was in the pane, so the prompt counted
// as arrived and no event said otherwise. A prompt that shows only in the
// agent's input did not arrive: it is submitted with one Enter, and its
// text is typed once in all, whether startAgent sent it or the dispatcher.
// One whose Enter never takes is submitted PromptTries times, reported and
// left alone.
func TestPromptLeftInTheInputIsSubmitted(t *testing.T) {
	for _, c := range []struct {
		name                            string
		deaf, ready                     bool
		enters, typed, gaveUp, prompted int
	}{
		{"startAgent's send", false, true, 1, 1, 0, 1},
		{"the dispatcher's delivery", false, false, 1, 1, 0, 1},
		{"Enter never takes", true, true, dispatch.PromptTries, dispatch.PromptTries, 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			shortPromptWait(t)
			h, calls := typingHerdr(t, c.deaf, c.ready)
			ctx := context.Background()
			s := storetest.New(t)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := s.GetCrew(ctx, "yardmaster")
			if err != nil || !r.Live() {
				t.Fatalf("crew member not running: %+v %v", r, err)
			}
			prompt, _ := r.Handle[dispatch.PendingPrompt].(string)
			if !strings.HasPrefix(prompt, CrewPromptLead) || strings.Contains(calls(), "send-keys") {
				t.Fatalf("a prompt that is only in the input is not pending: handle %v\n%s", r.Handle, calls())
			}
			for range 8 {
				if err := d.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			if n := strings.Count(calls(), "agent prompt"); n != 1 {
				t.Fatalf("the text was typed %d times, want once:\n%s", n, calls())
			}
			pane, _ := r.Handle["pane"].(string)
			if n := strings.Count(calls(), "pane send-keys "+pane+" enter"); pane == "" || n != c.enters {
				t.Fatalf("Enter sent %d times to pane %q, want %d:\n%s", n, pane, c.enters, calls())
			}
			r, _ = s.GetCrew(ctx, "yardmaster")
			if pending := r.Handle[dispatch.PendingPrompt] != nil; pending != (c.prompted == 0) {
				t.Fatalf("pending = %v: handle %v", pending, r.Handle)
			}
			shown, err := h.PromptShown(ctx, r.Handle, prompt)
			typed, terr := h.PromptTyped(ctx, r.Handle, prompt)
			if shown != (c.prompted == 1) || typed == shown || err != nil || terr != nil {
				t.Fatalf("the pane: shown %v, typed %v (%v %v)\n%s", shown, typed, err, terr, calls())
			}
			evs, err := s.Events(ctx, r.Journal, 100)
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]int{}
			for _, e := range evs {
				got[e.Kind]++
				if e.Kind == "session_prompt_gave_up" {
					if e.Data["typed"] != true || e.Data["hint"] != "herdr pane send-keys "+pane+" enter" {
						t.Errorf("session_prompt_gave_up does not say that Enter is all it needs: %+v", e.Data)
					}
				}
			}
			if got["session_prompt_typed"] != c.typed || got["session_prompt_gave_up"] != c.gaveUp ||
				got["session_prompted"] != c.prompted || got["session_prompt_lost"] != 0 {
				t.Fatalf("events = %v, want %d typed, %d gave up, %d prompted, none lost", got, c.typed, c.gaveUp, c.prompted)
			}
		})
	}
}

// flickeringHerdr is a typingHerdr that is ready, and whose first look goes
// wrong in one of two ways: flicker "working" has herdr report the agent
// working at the first `agent get` and idle at every later one, and flicker
// "unreadable" has the first `pane read` fail. The prompt's text is in the
// input all the while, and Enter in the pane submits it.
func flickeringHerdr(t *testing.T, flicker string) (h *Herdr, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
first() { echo x >> "$1"; [ "$(wc -l < "$1")" -eq 1 ] && [ `+flicker+` = "$1" ]; }
case "$1 $2" in
  "agent prompt") printf '%s' "$4" >> input; echo '{"result":{"type":"ok"}}' ;;
  "pane send-keys")
    if [ "$4" = enter ] && [ -s input ]; then
      echo "❯ $(cat input)" >> pane; echo "⏺ On it." >> pane; : > input
    fi ;; # as herdr: nothing printed
  "pane read")
    if first unreadable; then echo '{"error":{"code":"timeout","message":"no answer"}}' >&2; exit 1; fi
    cat pane 2>/dev/null; echo "────"; echo "❯ $(cat input 2>/dev/null)"; echo "────"; echo "  ? for shortcuts" ;;
  "agent get")
    if first working; then echo '{"result":{"agent":{"agent_status":"working"}}}'
    else echo '{"result":{"agent":{"agent_status":"idle"}}}'; fi ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }
}

// Found live (yardr-myyv), twice on 6 Oct on a machine at load 15 to 40: a
// builder's first prompt was in its input, not submitted, the agent idle,
// and yardr sent no Enter until a person did. Its started event shows a
// handle without a pending prompt, so arrived had said yes; the pane at
// rest says typed, so it had said yes on a sign other than the echo: herdr
// reporting the agent working at one look, or one read of the pane failing.
// Either took the prompt off the handle, and the dispatcher submits only a
// prompt that is on it. Neither sign ends the look any more: the prompt
// stays pending, and the dispatcher's pass finds it typed and sends Enter.
func TestPromptTypedBehindAWrongFirstLookIsSubmitted(t *testing.T) {
	for _, flicker := range []string{"working", "unreadable"} {
		t.Run(flicker+" at the first look", func(t *testing.T) {
			fakePromptClock(t)
			h, calls := flickeringHerdr(t, flicker)
			ctx := context.Background()
			s := storetest.New(t)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				if err := d.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			r, err := s.GetCrew(ctx, "yardmaster")
			if err != nil || !r.Live() {
				t.Fatalf("crew member not running: %+v %v", r, err)
			}
			if look := r.Handle[dispatch.PromptLook]; look != flicker+", look 1" {
				t.Errorf("%s = %v, want what the first look saw", dispatch.PromptLook, look)
			}
			pane, _ := r.Handle["pane"].(string)
			if n := strings.Count(calls(), "pane send-keys "+pane+" enter"); pane == "" || n != 1 {
				t.Fatalf("Enter sent %d times to pane %q, want once:\n%s", n, pane, calls())
			}
			if n := strings.Count(calls(), "agent prompt"); n != 1 {
				t.Fatalf("the text was typed %d times, want once:\n%s", n, calls())
			}
			if r.Handle[dispatch.PendingPrompt] != nil || r.Handle[dispatch.PromptUnconfirmed] != nil {
				t.Fatalf("still pending after its Enter: %v", r.Handle)
			}
			got := map[string]int{}
			evs, err := s.Events(ctx, r.Journal, 100)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range evs {
				got[e.Kind]++
			}
			if got["session_prompt_typed"] != 1 || got["session_prompted"] != 1 || got["session_prompt_lost"] != 0 {
				t.Fatalf("events = %v, want one session_prompt_typed and one session_prompted", got)
			}
		})
	}
}

// fakePromptClock puts arrived on a clock that only its own sleeps move, and
// returns how long it has slept: what a look waited is then the same on a
// machine of any speed.
func fakePromptClock(t *testing.T) (waited func() time.Duration) {
	t.Helper()
	now, sleep := promptNow, promptSleep
	t.Cleanup(func() { promptNow, promptSleep = now, sleep })
	begun := time.Now()
	var slept time.Duration
	promptNow = func() time.Time { return begun.Add(slept) }
	promptSleep = func(d time.Duration) { slept += d }
	return func() time.Duration { return slept }
}

// startAgent's own look: text in the input of an idle agent is pending, and
// is not waited on for all of promptWait; the same text echoed above an
// empty input arrived, and with the agent working is pending as unconfirmed
// (yardr-myyv), each told at the first look. The waits are counted on a
// fake clock, not timed: the forks of the fake herdr take as long as the
// machine lets them (yardr-1ph9).
func TestStartAgentTellsTypedFromTaken(t *testing.T) {
	prompt := "Read /wt/brief-1.md and follow it."
	for _, c := range []struct {
		name, pane, status string
		pending            bool
		look               string
		waited             time.Duration
	}{
		{"typed, idle", "────\n❯ " + prompt + "\n────", "idle", true, "typed, look 11", promptWait / 4},
		{"typed, working", "────\n❯ " + prompt + "\n────", "working", true, "working, look 1", 0},
		{"echoed, idle", "❯ " + prompt + "\n────\n❯ \n────", "idle", false, "shown, look 1", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			waited := fakePromptClock(t)
			bin := deafHerdrBin(t, `printf '%s\n' "$1 $2" >> "$0.calls"
case "$1 $2" in
  "pane read") printf '%s\n' '`+c.pane+`' ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"`+c.status+`"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
			h := &Herdr{Bin: bin}
			handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", prompt,
				map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := handle[dispatch.PendingPrompt] != nil; got != c.pending {
				t.Errorf("pending = %v, want %v (%v)", got, c.pending, handle)
			}
			if got := handle[dispatch.PromptUnconfirmed] == true; got != (c.status == "working") {
				t.Errorf("unconfirmed = %v (%v)", got, handle)
			}
			if got := handle[dispatch.PromptLook]; got != c.look {
				t.Errorf("%s = %v, want %q", dispatch.PromptLook, got, c.look)
			}
			if got := waited(); got != c.waited {
				t.Errorf("waited %s of a promptWait of %s, want %s", got, promptWait, c.waited)
			}
			// One look, and one more after each wait.
			calls := readCalls(t, bin+".calls")
			if got, want := strings.Count(calls, "pane read"), 1+int(c.waited/(promptWait/40)); got != want {
				t.Errorf("the pane was read %d times, want %d:\n%s", got, want, calls)
			}
		})
	}
}

func TestPromptPlace(t *testing.T) {
	prompt := "Read /Users/x/repo/.git/worktrees/b1/yardr/brief-1x7.md and follow it."
	rule := strings.Repeat("─", 40)
	for _, c := range []struct {
		name, text   string
		taken, typed bool
	}{
		{"Claude Code, in the input", "✻ Welcome\n\n" + rule + "\n❯ " + prompt + "\n" + rule + "\n  ? for shortcuts", false, true},
		{"Claude Code, in the input and wrapped",
			rule + "\n❯ Read /Users/x/repo/.g\n  it/worktrees/b1/yardr/brief-1x7.md and\n  follow it.\n" + rule, false, true},
		{"Claude Code, echoed", "❯ " + prompt + "\n\n⏺ Reading the brief\n\n" + rule + "\n❯ \n" + rule + "\n  ? for shortcuts", true, false},
		{"Claude Code, echoed and typed again", "❯ " + prompt + "\n" + rule + "\n❯ " + prompt + "\n" + rule, true, false},
		{"Claude Code, empty", rule + "\n❯ \n" + rule, false, false},
		{"Codex, in the input", "╭──╮\n│ >_ OpenAI Codex │\n╰──╯\n\n› " + prompt + "\n\n  100% context left", false, true},
		{"Codex, echoed", "› " + prompt + "\n\n• Reading the brief.\n\n› Ask Codex to do anything\n\n  98% context left", true, false},
		// No input to be told from the rest: as before the two were told apart.
		{"another harness", "> " + prompt + "\n", true, false},
		{"nothing", "", false, false},
	} {
		if taken, typed := promptPlace(c.text, prompt); taken != c.taken || typed != c.typed {
			t.Errorf("%s: taken %v, typed %v; want %v, %v", c.name, taken, typed, c.taken, c.typed)
		}
	}
}
