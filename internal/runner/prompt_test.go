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

// droppingHerdr is a fake herdr whose agent is idle from the start and drops
// the first `drop` prompts it is sent, as a Claude Code that is still coming
// up does: the pane shows nothing of it, and herdr sees the agent start on
// nothing. A later prompt shows in the pane, the agent working on it. Every
// call is logged.
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
    if [ "$(wc -l < sent)" -gt `+fmt.Sprint(drop)+` ]; then echo "❯ $4" >> pane; answer working; fi
    answer idle ;;
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
	prompt, _ := r.Handle[store.PendingPrompt].(string)
	if !strings.HasPrefix(prompt, CrewPromptLead) || strings.Count(calls(), "agent prompt") != 1 {
		t.Fatalf("a prompt that did not show is not pending: handle %v\n%s", r.Handle, calls())
	}
	for range 3 {
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r, _ = s.GetCrew(ctx, "yardmaster"); r.Handle[store.PendingPrompt] != nil {
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
	if err != nil || !r.Live() || r.Handle[store.PendingPrompt] != nil {
		t.Fatalf("crew member = %+v %v", r, err)
	}
	if n := strings.Count(calls(), "agent prompt"); n != 1 {
		t.Fatalf("prompt sent %d times:\n%s", n, calls())
	}
}

// A bead session's first prompt goes the same way (startAgent serves both).
// With nothing of the prompt in the pane, an agent herdr saw start on
// nothing did not take it. One it saw working is taken to have, and since
// no echo says so, the prompt stays on the handle as unconfirmed for the
// dispatcher to look at again (yardr-myyv). The handle says what herdr said
// and what the pane showed either way.
func TestStartAgentChecksThePromptArrived(t *testing.T) {
	for status, unconfirmed := range map[string]bool{"working": true, "idle": false} {
		h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "agent prompt") answer `+status+` ;;
  "pane read") echo "❯ " ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
		handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "Read /wt/brief-1.md and follow it.",
			map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if got := handle[dispatch.PromptUnconfirmed] == true; handle[store.PendingPrompt] == nil || got != unconfirmed {
			t.Errorf("agent %s: unconfirmed = %v, want a pending prompt and %v (%v)", status, got, unconfirmed, handle)
		}
		if want := map[string]string{"working": "absent, herdr working", "idle": "absent, herdr stalled"}[status]; handle[dispatch.PromptLook] != want {
			t.Errorf("agent %s: %s = %q, want %q", status, dispatch.PromptLook, handle[dispatch.PromptLook], want)
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
  "agent prompt") answer idle ;;
  "pane read") echo '{"error":{"code":"unknown_command","message":"no pane read"}}' >&2; exit 1 ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
	if err != nil || handle[store.PendingPrompt] != "read the brief" || handle[dispatch.PromptUnconfirmed] != true ||
		handle[dispatch.PromptLook] != "unreadable, herdr stalled" {
		t.Fatalf("handle = %v, err = %v", handle, err)
	}
}

// A handle without a pane has nothing to look at, now or on a later pass:
// its prompt is not kept.
func TestStartAgentWithoutPaneIsNotPending(t *testing.T) {
	h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "agent prompt") answer idle ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1"}, true)
	if err != nil || handle[store.PendingPrompt] != nil || handle[dispatch.PromptLook] != "no pane, herdr stalled" {
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
			prompt, _ := r.Handle[store.PendingPrompt].(string)
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
			if pending := r.Handle[store.PendingPrompt] != nil; pending != (c.prompted == 0) {
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
// its rules, and nothing above it, and herdr sees the agent start on
// nothing. Enter sent to the pane (pane send-keys
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
  "agent prompt") printf '%s' "$4" >> input; answer idle ;;
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
			prompt, _ := r.Handle[store.PendingPrompt].(string)
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
			if pending := r.Handle[store.PendingPrompt] != nil; pending != (c.prompted == 0) {
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

// flickeringHerdr is a typingHerdr that is ready, and whose first sign goes
// wrong in one of two ways: flicker "working" has herdr see the agent
// working after the prompt, which it is not at any later `agent get`, and
// flicker "unreadable" has the first `pane read` after the prompt fail. The prompt's text is
// in the input all the while, and Enter in the pane submits it.
func flickeringHerdr(t *testing.T, flicker string) (h *Herdr, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
first() { echo x >> "$1"; [ "$(wc -l < "$1")" -eq 1 ] && [ `+flicker+` = "$1" ]; }
case "$1 $2" in
  "agent prompt") printf '%s' "$4" >> input
    if first working; then answer working; fi
    answer idle ;;
  "pane send-keys")
    if [ "$4" = enter ] && [ -s input ]; then
      echo "❯ $(cat input)" >> pane; echo "⏺ On it." >> pane; : > input
    fi ;; # as herdr: nothing printed
  "pane read")
    if [ -s input ] && first unreadable; then echo '{"error":{"code":"timeout","message":"no answer"}}' >&2; exit 1; fi
    cat pane 2>/dev/null; echo "────"; echo "❯ $(cat input 2>/dev/null)"; echo "────"; echo "  ? for shortcuts" ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }
}

// Found live (yardr-myyv), twice on 6 Oct on a machine at load 15 to 40: a
// builder's first prompt was in its input, not submitted, the agent idle,
// and yardr sent no Enter until a person did. Its started event shows a
// handle without a pending prompt, so arrived had said yes; the pane at
// rest says typed, so it had said yes on a sign other than the echo: herdr
// reporting the agent working at one moment, or one read of the pane failing.
// Either took the prompt off the handle, and the dispatcher submits only a
// prompt that is on it. Neither sign ends the look any more: the prompt
// stays pending, and the dispatcher's pass finds it typed and sends Enter.
func TestPromptTypedBehindAWrongFirstLookIsSubmitted(t *testing.T) {
	for flicker, saw := range map[string]string{"working": "typed, herdr working", "unreadable": "unreadable, herdr stalled"} {
		t.Run(flicker+" at the first look", func(t *testing.T) {
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
			if look := r.Handle[dispatch.PromptLook]; look != saw {
				t.Errorf("%s = %v, want %q", dispatch.PromptLook, look, saw)
			}
			pane, _ := r.Handle["pane"].(string)
			if n := strings.Count(calls(), "pane send-keys "+pane+" enter"); pane == "" || n != 1 {
				t.Fatalf("Enter sent %d times to pane %q, want once:\n%s", n, pane, calls())
			}
			if n := strings.Count(calls(), "agent prompt"); n != 1 {
				t.Fatalf("the text was typed %d times, want once:\n%s", n, calls())
			}
			if r.Handle[store.PendingPrompt] != nil || r.Handle[dispatch.PromptUnconfirmed] != nil {
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

// risingHerdr is a fake herdr whose Claude Code is still starting when its
// first prompt is typed: the pane's terminal echoes the keys under the
// command line, and for the first `bare` reads of the pane that is all it
// shows. Then Claude Code has drawn itself below the echo, with the text in
// its input and the Enter not taken: herdr sees it start on nothing. Enter
// in the pane submits it.
func risingHerdr(t *testing.T, bare int) (h *Herdr, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "agent prompt") printf '%s' "$4" > keys; printf '%s' "$4" > input; answer idle ;;
  "pane send-keys")
    if [ "$4" = enter ] && [ -s input ] && [ "$(wc -l < reads)" -gt `+fmt.Sprint(bare)+` ]; then
      echo "❯ $(cat input)" >> pane; echo "⏺ On it." >> pane; : > input
    fi ;; # as herdr: nothing printed
  "pane read")
    echo x >> reads
    echo '$ claude --remote-control'; cat keys 2>/dev/null; echo
    if [ "$(wc -l < reads)" -gt `+fmt.Sprint(bare)+` ]; then
      echo "✻ Welcome"; cat pane 2>/dev/null; echo "────"; echo "❯ $(cat input 2>/dev/null)"; echo "────"; echo "  ? for shortcuts"
    fi ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }
}

// Found live (yardr-7y16), 7 Oct: the yardmaster's session, started with
// --remote-control after a rotation, had its first prompt in Claude Code's
// input for seven minutes, until a person pressed Enter. Its handle said
// "shown, look 1" and held no pending prompt, so nothing looked again: the
// keys had been echoed by the pane before Claude Code drew its input, and
// the text without an input mark was read as a message it took. A pane of
// Claude Code without its mark proves nothing now: the prompt stays
// pending, and the dispatcher's pass sends the Enter. Since yardr-bcx5 a
// pane that shows no input when the runner looks is typed nothing: the
// dispatcher types the prompt once the input is drawn, and sends the Enter
// the same.
func TestPromptEchoedBeforeTheInputIsDrawnIsSubmitted(t *testing.T) {
	for _, c := range []struct {
		name string
		bare int
		look string
	}{
		{"the input is drawn when the runner looks", 0, "typed, herdr stalled"},
		{"nothing is drawn when the runner looks, and nothing typed", 3, "no screen, not typed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, calls := risingHerdr(t, c.bare)
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
			if look := r.Handle[dispatch.PromptLook]; look != c.look {
				t.Errorf("%s = %v, want %q", dispatch.PromptLook, look, c.look)
			}
			if r.Handle[store.PendingPrompt] == nil || strings.Contains(calls(), "send-keys") {
				t.Fatalf("a prompt that only the pane echoed is not pending: handle %v\n%s", r.Handle, calls())
			}
			for range 4 {
				if err := d.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			r, _ = s.GetCrew(ctx, "yardmaster")
			pane, _ := r.Handle["pane"].(string)
			if n := strings.Count(calls(), "pane send-keys "+pane+" enter"); pane == "" || n != 1 {
				t.Fatalf("Enter sent %d times to pane %q, want once:\n%s", n, pane, calls())
			}
			if n := strings.Count(calls(), "agent prompt"); n != 1 {
				t.Fatalf("the text was typed %d times, want once:\n%s", n, calls())
			}
			if r.Handle[store.PendingPrompt] != nil {
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

// startAgent's own look, for each thing herdr says of the prompt. Text in
// the input of an agent herdr saw start on nothing is pending; the same
// text echoed above an empty input arrived; and with herdr seeing the agent
// working, text that is not echoed is pending as unconfirmed (yardr-myyv).
// An agent that ends at a dialog has its prompt pending unless the pane
// shows it taken, and one that was at a dialog before is typed nothing and
// looked at not at all. A timeout, and an error herdr has no other word
// for, is a stall. Each is one read of the pane: the waiting is herdr's.
//
// Found live (yardr-7y16): the text alone, in a pane where Claude Code has
// drawn no input yet, was taken as the echo. For a kind whose input mark is
// known it is left pending; for any other kind it is all there is to go by,
// as before. Since yardr-bcx5 a pane of a known kind without its input is
// typed nothing at all: the look is "no screen", and herdr is not asked.
func TestStartAgentTellsTypedFromTaken(t *testing.T) {
	prompt := "Read /wt/brief-1.md and follow it."
	typed, echoed := "────\n❯ "+prompt+"\n────", "❯ "+prompt+"\n────\n❯ \n────"
	for _, c := range []struct {
		name, kind, pane, answer string
		pending, unconfirmed     bool
		look                     string
	}{
		{"typed, stalled", "claude", typed, "idle", true, false, "typed, herdr stalled"},
		{"typed, working", "claude", typed, "working", true, true, "typed, herdr working"},
		{"echoed, stalled", "claude", echoed, "idle", false, false, "shown, herdr stalled"},
		{"echoed, working", "claude", echoed, "working", false, false, "shown, herdr working"},
		{"typed, at a dialog after it", "claude", typed, "blocked", true, false, "typed, herdr blocked"},
		{"echoed, at a dialog after it", "claude", echoed, "blocked", false, false, "shown, herdr blocked"},
		{"at a dialog before it", "claude", typed, "refused", true, false, ""},
		{"typed, herdr's wait timed out", "claude", typed, "timeout", true, false, "typed, herdr stalled"},
		{"echoed, herdr's wait timed out", "claude", echoed, "timeout", false, false, "shown, herdr stalled"},
		{"typed, an error of herdr's", "claude", typed, "odd", true, false, "typed, herdr stalled"},
		{"no screen, claude", "claude", "$ claude", "idle", true, false, "no screen, not typed"},
		{"no screen, codex", "codex", "$ codex\n==> Upgrading 1 outdated package:\ncodex 0.160.1 -> 0.161.0", "idle", true, false, "no screen, not typed"},
		{"typed, kimi", "kimi", "╭────╮\n│ > " + prompt + " │\n╰────╯", "idle", true, false, "typed, herdr stalled"},
		{"echoed, kimi", "kimi", "✨ " + prompt + "\n╭────╮\n│ >  │\n╰────╯", "idle", false, false, "shown, herdr stalled"},
		{"no screen, kimi", "kimi", "$ kimi", "idle", true, false, "no screen, not typed"},
		{"no input drawn, a kind with no known mark", "acme", "$ acme\n" + prompt, "idle", false, false, "shown, herdr stalled"},
		{"no input drawn, a handle with no kind", "", "$ claude\n" + prompt, "idle", false, false, "shown, herdr stalled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			answer := "answer " + c.answer
			if c.answer == "odd" {
				answer = `echo '{"error":{"code":"internal_error","message":"something else"}}' >&2; exit 1`
			}
			bin := deafHerdrBin(t, `printf '%s\n' "$*" >> "$0.calls"
case "$1 $2" in
  "agent prompt") `+answer+` ;;
  "pane read") printf '%s\n' '`+c.pane+`' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)
			h := &Herdr{Bin: bin}
			start := map[string]any{"agent": "b1", "pane": "w1:p1"}
			if c.kind != "" {
				start[harnessKind] = c.kind
			}
			handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", prompt,
				start, true)
			if err != nil {
				t.Fatal(err)
			}
			if got := handle[store.PendingPrompt] != nil; got != c.pending {
				t.Errorf("pending = %v, want %v (%v)", got, c.pending, handle)
			}
			if got := handle[dispatch.PromptUnconfirmed] == true; got != c.unconfirmed {
				t.Errorf("unconfirmed = %v (%v)", got, handle)
			}
			if got, _ := handle[dispatch.PromptLook].(string); got != c.look {
				t.Errorf("%s = %q, want %q", dispatch.PromptLook, got, c.look)
			}
			calls := readCalls(t, bin+".calls")
			// One look for the harness's screen before anything is typed
			// (yardr-bcx5), of a kind whose mark says what that is; one after.
			_, marked := marksOf(c.kind)
			want, typed := 1, true
			if marked {
				want++
			}
			switch {
			case c.look == "no screen, not typed":
				want, typed = 1, false
			case c.answer == "refused":
				want-- // nothing was typed: there is nothing to look for
			}
			if give := "agent prompt b1 " + prompt + " --wait --until working --until blocked --timeout 6000\n"; strings.Contains(calls, give) != typed {
				t.Errorf("the prompt given to herdr to wait on, as %q: %v, want %v:\n%s", give, !typed, typed, calls)
			}
			if got := strings.Count(calls, "pane read"); got != want {
				t.Errorf("the pane was read %d times, want %d:\n%s", got, want, calls)
			}
		})
	}
}

// An error that does not say whether the prompt was taken is a stall too:
// the pane, not the error, decides whether it needs another delivery.
func TestStartAgentLooksAfterHerdrOutageError(t *testing.T) {
	h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "agent prompt") echo '{"error":{"code":"server_not_running","message":"no herdr server"}}' >&2; exit 1 ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
	if err != nil || handle[store.PendingPrompt] != "read the brief" || handle[dispatch.PromptLook] != "absent, herdr stalled" {
		t.Fatalf("handle = %v, err = %v", handle, err)
	}
}

func TestStartAgentKeepsBlockedPromptPendingWithoutPane(t *testing.T) {
	h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "agent prompt") answer blocked ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1"}, true)
	if err != nil || handle[store.PendingPrompt] != "read the brief" || handle[dispatch.PromptLook] != "no pane, herdr blocked" {
		t.Fatalf("handle = %v, err = %v", handle, err)
	}
}

// Nudge waits as the first prompt does, and says with its error what the
// dispatcher has to know: nothing for a nudge herdr saw the agent working
// on or at a dialog after, that nothing was typed for an agent at a dialog
// before, and that the text was typed and not started on for a stall, a
// timeout or another error of herdr's.
func TestNudgeSaysWhatHerdrSawOfIt(t *testing.T) {
	for answer, want := range map[string]error{
		"working": nil, "blocked": nil, "refused": dispatch.ErrAgentBlocked,
		"idle": dispatch.ErrPromptStalled, "timeout": dispatch.ErrPromptStalled,
	} {
		bin := deafHerdrBin(t, `printf '%s\n' "$*" >> "$0.calls"
answer `+answer)
		err := (&Herdr{Bin: bin}).Nudge(context.Background(), map[string]any{"agent": "b1"}, "yardr: 1 new in your inbox")
		if err != want {
			t.Errorf("herdr answers %s: Nudge = %v, want %v", answer, err, want)
		}
		if calls, want := readCalls(t, bin+".calls"), "agent prompt b1 yardr: 1 new in your inbox --wait --until working --until blocked --timeout 6000\n"; calls != want {
			t.Errorf("herdr was asked %q, want %q", calls, want)
		}
	}
	down := &Herdr{Bin: deafHerdrBin(t, `echo '{"error":{"code":"server_not_running","message":"no herdr server"}}' >&2; exit 1`)}
	if err := down.Nudge(context.Background(), map[string]any{"agent": "b1"}, "x"); err != dispatch.ErrPromptStalled {
		t.Errorf("herdr down: Nudge = %v, want %v", err, dispatch.ErrPromptStalled)
	}
}

func TestPromptPlace(t *testing.T) {
	prompt := "Read /Users/x/repo/.git/worktrees/b1/yardr/brief-1x7.md and follow it."
	rule := strings.Repeat("─", 40)
	// What the pane's terminal echoes of keys that come before the harness
	// reads them: the text alone, under the command that started it.
	bare := "$ claude --remote-control\n" + prompt + "\n"
	for _, c := range []struct {
		name, kind, text   string
		taken, typed, bare bool
	}{
		{"Claude Code, in the input", "claude", "✻ Welcome\n\n" + rule + "\n❯ " + prompt + "\n" + rule + "\n  ? for shortcuts", false, true, false},
		{"Claude Code, in the input and wrapped", "claude",
			rule + "\n❯ Read /Users/x/repo/.g\n  it/worktrees/b1/yardr/brief-1x7.md and\n  follow it.\n" + rule, false, true, false},
		{"Claude Code, echoed", "claude", "❯ " + prompt + "\n\n⏺ Reading the brief\n\n" + rule + "\n❯ \n" + rule + "\n  ? for shortcuts", true, false, false},
		{"Claude Code, echoed and wrapped", "claude",
			"❯ Read /Users/x/repo/.g\n  it/worktrees/b1/yardr/brief-1x7.md and\n  follow it.\n\n⏺ Reading\n" + rule + "\n❯ \n" + rule, true, false, false},
		{"Claude Code, echoed and typed again", "claude", "❯ " + prompt + "\n" + rule + "\n❯ " + prompt + "\n" + rule, true, false, false},
		{"Claude Code, echoed after an earlier message", "claude",
			"❯ hello\n\n⏺ Hello.\n\n❯ " + prompt + "\n\n⏺ Reading\n" + rule + "\n❯ \n" + rule, true, false, false},
		{"Claude Code, empty", "claude", rule + "\n❯ \n" + rule, false, false, false},
		{"Codex, in the input", "codex", "╭──╮\n│ >_ OpenAI Codex │\n╰──╯\n\n› " + prompt + "\n\n  100% context left", false, true, false},
		{"Codex, echoed", "codex", "› " + prompt + "\n\n• Reading the brief.\n\n› Ask Codex to do anything\n\n  98% context left", true, false, false},
		// Found live (yardr-7y16): no input drawn yet, so the text is the
		// pane's own echo of the keys and proves nothing ...
		{"Claude Code, still coming up", "claude", bare, false, false, true},
		{"Codex, still coming up", "codex", bare, false, false, true},
		// ... and it stays above the input the harness draws afterwards,
		// where it is no message the harness took.
		{"Claude Code, come up with the keys in its input", "claude", bare + "✻ Welcome\n" + rule + "\n❯ " + prompt + "\n" + rule, false, true, false},
		{"Claude Code, come up with the keys dropped", "claude", bare + "✻ Welcome\n" + rule + "\n❯ \n" + rule, false, false, false},
		{"Claude Code, a message that only holds the prompt", "claude", "❯ what does \"" + prompt + "\" mean?\n" + rule + "\n❯ \n" + rule, false, false, false},
		// The other kind's mark is not this kind's input.
		{"Claude Code, a line with Codex's mark", "claude", "› " + prompt + "\n", false, false, true},
		// No input to be told from the rest: as before the two were told
		// apart, for a kind with no known mark and for a handle that names
		// no kind (a session started before handles did).
		{"another harness", "acme", "> " + prompt + "\n", true, false, false},
		{"another harness, the bare text", "acme", bare, true, false, false},
		{"another harness, Kimi's box", "acme", kimiBox("> " + prompt), true, false, false},
		{"no kind on the handle, the bare text", "", bare, true, false, false},
		{"no kind on the handle, Claude Code's input", "", rule + "\n❯ " + prompt + "\n" + rule, false, true, false},
		{"no kind on the handle, Codex's echo", "", "› " + prompt + "\n\n› \n", true, false, false},
		{"nothing", "claude", "", false, false, false},
		// Kimi's input is in a box and its message mark is another: the
		// brakeman's pane of 7 Oct 2026, with this prompt for its own.
		{"Kimi, in the input", "kimi", kimiWelcome + kimiBox("> "+prompt), false, true, false},
		{"Kimi, in the input and wrapped", "kimi",
			kimiWelcome + kimiBox("> Read /Users/x/repo/.g", "  it/worktrees/b1/yardr/brief-1x7.md and", "  follow it."), false, true, false},
		// An unwrapped read joins the rows of a box that fill the pane, and
		// the edges between them are no part of the prompt (yardr-w9vj).
		{"Kimi, in the input, wrapped and read as one line", "kimi",
			kimiWelcome + kimiPane("-jj", "Read /Users/x/repo/.g", "it/worktrees/b1/yardr/brief-1x7.md and follow it."), false, true, false},
		{"Kimi, in the input, the box read as one line", "kimi", kimiWelcome + kimiPane("jj", prompt), false, true, false},
		{"Kimi, empty, the box read as one line", "kimi", kimiWelcome + kimiPane("jj", ""), false, false, false},
		{"Kimi, echoed", "kimi", kimiWelcome + " ✨ " + prompt + "\n\n ● Reading the brief.\n\n" + kimiBox(">"), true, false, false},
		{"Kimi, echoed and wrapped", "kimi",
			kimiWelcome + " ✨ Read /Users/x/repo/.g\n    it/worktrees/b1/yardr/brief-1x7.md and\n    follow it.\n\n ● Reading\n" + kimiBox(">"), true, false, false},
		{"Kimi, echoed and typed again", "kimi", " ✨ " + prompt + "\n" + kimiBox("> "+prompt), true, false, false},
		{"Kimi, empty", "kimi", kimiWelcome + kimiBox(">"), false, false, false},
		{"Kimi, still coming up", "kimi", "$ kimi\n" + prompt + "\n", false, false, true},
		{"Kimi, come up with the keys in its input", "kimi", "$ kimi\n" + prompt + "\n" + kimiWelcome + kimiBox("> "+prompt), false, true, false},
		{"Kimi, come up with the keys dropped", "kimi", "$ kimi\n" + prompt + "\n" + kimiWelcome + kimiBox(">"), false, false, false},
		// Its input mark without the box's edge is a line of its transcript,
		// and so is the prompt in a box that is not its input.
		{"Kimi, its input mark and no box", "kimi", "> " + prompt + "\n", false, false, true},
		{"Kimi, the prompt after its input mark, above the input", "kimi", " > " + prompt + "\n" + kimiBox(">"), false, false, false},
		{"Kimi, a message that only holds the prompt", "kimi", " ✨ what does \"" + prompt + "\" mean?\n" + kimiBox(">"), false, false, false},
		{"Kimi, a line with Claude Code's mark", "kimi", "❯ " + prompt + "\n" + rule + "\n❯ \n" + rule, false, false, true},
	} {
		if taken, typed, bare := promptPlace(c.text, prompt, c.kind); taken != c.taken || typed != c.typed || bare != c.bare {
			t.Errorf("%s: taken %v, typed %v, bare %v; want %v, %v, %v", c.name, taken, typed, bare, c.taken, c.typed, c.bare)
		}
	}
}

// kimiWelcome is the top of a Kimi Code 2.1.1 pane, and kimiBox its input
// with lines in it, the first begun with the input mark: as `herdr pane
// read` gave the brakeman's on 7 Oct 2026, narrowed.
const kimiWelcome = " ╭──────────────────────────────────────╮\n" +
	" │  ▐█▛█▛█▌  Welcome to Kimi Code!      │\n" +
	" │  ▐█████▌  Send /help for help.       │\n" +
	" ╰──────────────────────────────────────╯\n\n"

func kimiBox(lines ...string) string {
	s := " ╭──────────────────────────────────────╮\n"
	for _, l := range lines {
		s += " │ " + l + " │\n"
	}
	return s + " ╰──────────────────────────────────────╯\n Never Ask  K3 thinking: high  ~   ctrl+o expand\n"
}

// Found live (yardr-xk55): Claude Code offered to set auto mode up in the
// pane of a builder that waited for its tests, and the session was told of
// as idle with nothing saying why. The offer's words are those seen there
// (2.1.288, 12 Oct 2026); how they lie on the screen is as Claude Code
// draws its other questions, since the pane was not kept. The selected
// answer carries the mark of the input, so it reads as text in the input
// and no nudge is typed there, whose digits would choose an answer; and
// the offer is named, so that it is told of and not waited on like
// someone's text.
func TestClaudeAutoModeSetupOffer(t *testing.T) {
	const offer = " ⏺ Committed. The tests run in the background.\n" +
		"\n" +
		"────────────────────────────────────────────────────────────\n" +
		" Teach auto mode about your environment?\n" +
		"\n" +
		" Auto mode works better when it knows your environment. Takes about a minute.\n" +
		"\n" +
		" ❯ 1. Yes\n" +
		"   2. Not now\n" +
		"   3. Don't show again\n" +
		"\n" +
		" Enter to confirm · Esc to cancel\n"
	if dialog, line := inputDialog(offer, "claude"); dialog != "auto_mode_setup" || line != "Teach auto mode about your environment?" {
		t.Fatalf("inputDialog = %q, %q; want the offer and its first line", dialog, line)
	}
	if text, known := inputText(offer, "claude"); !known || !strings.HasPrefix(text, "1. Yes") {
		t.Fatalf("inputText = %q, known %v; want the selected answer read as text that holds a nudge back", text, known)
	}
	// Only where the input is: an agent that writes of the offer, above an
	// input of its own, is at no dialog.
	quoted := " ⏺ The harness asked me:\n Teach auto mode about your environment?\n" +
		"────────────────────────────────────────\n ❯ \n────────────────────────────────────────\n"
	if dialog, line := inputDialog(quoted, "claude"); dialog != "" {
		t.Fatalf("inputDialog of a pane that quotes the offer = %q, %q; want none", dialog, line)
	}
}

// Kimi's question to a session that sat idle, as Kimi Code 2.1.1 draws it in
// place of its input box (its cache-hint-dialog; seen live on 7 Oct 2026
// with a prompt of yardr's kept behind it). The dispatcher goes by what
// this runner reads of such a pane: the prompt is not taken and not typed,
// so it is told of and not counted as arrived (dispatch/harness_reset.go),
// and the selected answer, which Kimi marks as Claude Code marks its input,
// reads as text in the input, so no nudge is typed there whose Enter would
// choose it: with no box drawn the pane is read by the other kinds' marks.
func TestKimiIdleMenuKeepsThePromptBehindIt(t *testing.T) {
	const prompt = "yardr: the usage limit your harness stopped on (kimi, 5h window) has reset. Go on where you stopped."
	const menu = " Error: [provider.auth_error] 403 You've reached your\n" +
		" 5-hour usage limit. Please wait for the quota to refresh.\n" +
		" ────────────────────────────────────────────────────────────\n" +
		"  This session has been idle for 44m and is ~198k tokens.\n" +
		"  ↑↓ navigate · Enter select · Esc cancel\n" +
		"  Cache expired — the next message re-sends the entire history at full price.\n" +
		"   ❯ Compact and continue    one-time compact cost · cheapest way to keep this topic\n" +
		"     Start a new session     zero context cost · best for a new task\n" +
		"     Continue as-is          full history kept · highest cost per turn\n" +
		"     Don't ask me again\n" +
		" ────────────────────────────────────────────────────────────\n" +
		" Ask When Needed  K3 thinking: high  ~         ctrl+o expand\n"
	if taken, typed, _ := promptPlace(menu, prompt, "kimi"); taken || typed {
		t.Fatalf("promptPlace = taken %v, typed %v; the prompt is behind the menu and shows nowhere", taken, typed)
	}
	text, known := inputText(menu, "kimi")
	if !known || !strings.HasPrefix(text, "Compact and continue") {
		t.Fatalf("inputText = %q, known %v; want the menu's selected answer read as text that holds a nudge back", text, known)
	}
	// The mark stands in a dialog Kimi's file knows: it is named, with its
	// first line, so that it is told of and not waited on like someone's text.
	if dialog, line := inputDialog(menu, "kimi"); dialog != "cache_menu" || line != "This session has been idle for 44m and is ~198k tokens." {
		t.Fatalf("inputDialog = %q, %q; want the cache menu and its first line", dialog, line)
	}
	// Only where the input is: the same words in what the agent printed,
	// above an input box with a person's text in it, are no dialog. Nor is
	// the menu one in the pane of a kind whose file has no such dialog.
	quoted := " ● Kimi asked me:\n This session has been idle for 44m and is ~198k tokens.\n" + kimiBox("> so what do I pick")
	if dialog, line := inputDialog(quoted, "kimi"); dialog != "" {
		t.Fatalf("inputDialog of a pane that quotes the menu = %q, %q; want none", dialog, line)
	}
	if dialog, _ := inputDialog(kimiWelcome+kimiBox(">"), "kimi"); dialog != "" {
		t.Fatalf("inputDialog of an empty input = %q; want none", dialog)
	}
	if dialog, _ := inputDialog(menu, "claude"); dialog != "" {
		t.Fatalf("inputDialog for claude = %q; its file names no such dialog", dialog)
	}
	// Answered, Kimi takes the prompt: it shows above its input box, begun
	// with the mark of a message Kimi took.
	taken, _, _ := promptPlace(" ✨ "+prompt+"\n ● Going on with the review.\n"+kimiBox(">"), prompt, "kimi")
	if !taken {
		t.Fatal("promptPlace: a prompt Kimi echoes is not taken")
	}
}

// upgradingHerdr is a fake herdr whose Codex upgrades itself before it
// draws anything: for the first `brew` reads of its pane, the pane is
// brew's output, and herdr has the agent idle all the while. Then Codex's
// screen is up, and a prompt typed there is taken. One typed before would
// be lost in brew's output: it is logged like every call, and shows nowhere.
func upgradingHerdr(t *testing.T, brew int) (h *Herdr, calls func() string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
up() { [ "$(wc -l < reads)" -gt `+fmt.Sprint(brew)+` ]; }
case "$1 $2" in
  "agent prompt")
    if up; then echo "› $4" >> pane; echo "• On it." >> pane; answer working; fi
    answer idle ;;
  "pane read")
    echo x >> reads
    echo '$ codex --no-daemon'; echo '==> Upgrading 1 outdated package:'; echo 'codex 0.160.1 -> 0.161.0'
    if up; then
      echo ">_ OpenAI Codex"; cat pane 2>/dev/null; echo "› "; echo; echo "  gpt-6-luna · ~/wt"
    else
      echo '==> Downloading codex-0.161.0.tar.gz'
    fi ;;
  "agent get") echo '{"result":{"agent":{"agent_status":"idle"}}}' ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }
}

// Found live (yardr-bcx5), 8 Oct: three Codex reviewers started while a
// release of Codex was out, and each ran `brew upgrade --cask codex` in its
// pane before it drew its screen. herdr had them idle. The first prompt was
// typed into brew's output, three times over in thirty seconds
// (session_prompt_lost 1 to 3 of 3, session_prompt_gave_up), and the
// sessions sat without it until the idle limit. A pane that has not shown
// the harness's input is typed nothing, by the runner or by the dispatcher:
// one session_harness_starting says so with what the pane shows instead,
// no delivery is counted, and the prompt goes in once the input is there.
func TestFirstPromptWaitsForTheHarnessScreen(t *testing.T) {
	h, calls := upgradingHerdr(t, 40)
	ctx := context.Background()
	s := storetest.New(t)
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "reviewer", Runner: "herdr", Dir: t.TempDir(),
		Config: map[string]any{"kind": "codex"}}, "me"); err != nil {
		t.Fatal(err)
	}
	// More passes than the deliveries a prompt has, all on brew's output.
	for range 2 * dispatch.PromptTries {
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.GetCrew(ctx, "reviewer")
	if err != nil || !r.Live() {
		t.Fatalf("crew member not running: %+v %v", r, err)
	}
	if up, known := h.HarnessScreen(ctx, r.Handle); up || !known {
		t.Fatalf("the fake's screen is up early (up %v, known %v):\n%s", up, known, calls())
	}
	if n := strings.Count(calls(), "agent prompt"); n != 0 {
		t.Fatalf("the prompt was typed %d times into a pane that is not the harness's screen:\n%s", n, calls())
	}
	if r.Handle[store.PendingPrompt] == nil || r.Handle[dispatch.PromptLook] != "no screen, not typed" {
		t.Fatalf("handle = %v, want the prompt pending and not typed", r.Handle)
	}
	events := func() map[string]int {
		t.Helper()
		evs, err := s.Events(ctx, r.Journal, 100)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]int{}
		for _, e := range evs {
			got[e.Kind]++
			if e.Kind == "session_harness_starting" {
				tail, _ := e.Data["tail"].(string)
				if !strings.Contains(tail, "Upgrading 1 outdated package") || e.Data["crew"] != "reviewer" || e.Data["reason"] == nil {
					t.Errorf("session_harness_starting does not say who waits on what: %+v", e.Data)
				}
			}
		}
		return got
	}
	if got := events(); got["session_harness_starting"] != 1 || got["session_prompt_lost"] != 0 ||
		got["session_prompt_gave_up"] != 0 || got["session_prompted"] != 0 {
		t.Fatalf("events = %v, want one session_harness_starting and nothing of a delivery", got)
	}
	// The upgrade is over and Codex has drawn itself.
	for i := 0; i < 60 && r.Handle[store.PendingPrompt] != nil; i++ {
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
		if r, err = s.GetCrew(ctx, "reviewer"); err != nil {
			t.Fatal(err)
		}
	}
	if r.Handle[store.PendingPrompt] != nil {
		t.Fatalf("still pending with the screen up: %v\n%s", r.Handle, calls())
	}
	if n := strings.Count(calls(), "agent prompt"); n != 1 {
		t.Fatalf("the prompt was typed %d times, want once, with the screen up:\n%s", n, calls())
	}
	if got := events(); got["session_harness_starting"] != 1 || got["session_prompt_lost"] != 0 ||
		got["session_prompt_gave_up"] != 0 || got["session_prompted"] != 1 {
		t.Fatalf("events = %v, want one session_harness_starting and one session_prompted", got)
	}
}

// What tells the harness's screen is the mark of its input, of a kind whose
// mark yardr knows; of any other kind, and of a pane that cannot be read,
// nothing is known and yardr types as it did.
func TestHarnessScreen(t *testing.T) {
	for _, c := range []struct {
		name, kind, pane string
		up, known        bool
	}{
		{"brew before codex", "codex", "$ codex\n==> Upgrading 1 outdated package:", false, true},
		{"codex", "codex", ">_ OpenAI Codex\n› Ask Codex to do anything", true, true},
		{"claude", "claude", "✻ Welcome\n────\n❯ \n────", true, true},
		{"claude's mark is not codex's", "codex", "❯ ", false, true},
		{"kimi's box", "kimi", "╭────╮\n│ >  │\n╰────╯", true, true},
		{"a kind with no known mark", "acme", "$ acme", false, false},
		{"a handle with no kind", "", "❯ ", false, false},
		{"unreadable", "codex", "", false, false},
	} {
		read := `printf '%s\n' '` + c.pane + `'`
		if c.name == "unreadable" {
			read = `echo '{"error":{"code":"timeout","message":"no answer"}}' >&2; exit 1`
		}
		h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "pane read") `+read+` ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
		handle := map[string]any{"agent": "b1", "pane": "w1:p1"}
		if c.kind != "" {
			handle[harnessKind] = c.kind
		}
		if up, known := h.HarnessScreen(context.Background(), handle); up != c.up || known != c.known {
			t.Errorf("%s: up %v, known %v, want %v, %v", c.name, up, known, c.up, c.known)
		}
	}
	if up, known := (&Herdr{}).HarnessScreen(context.Background(), map[string]any{harnessKind: "codex"}); up || known {
		t.Errorf("a handle without a pane: up %v, known %v", up, known)
	}
}
