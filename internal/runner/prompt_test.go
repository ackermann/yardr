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

// Found live (yardr-ykbt): after a handoff the daemon restarted a resident,
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
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := d.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	r, err := s.GetResident(ctx, "mayor")
	if err != nil || !r.Live() {
		t.Fatalf("resident not running: %+v %v", r, err)
	}
	prompt, _ := r.Handle[dispatch.PendingPrompt].(string)
	if !strings.HasPrefix(prompt, ResidentPromptLead) || strings.Count(calls(), "agent prompt") != 1 {
		t.Fatalf("a prompt that did not show is not pending: handle %v\n%s", r.Handle, calls())
	}
	for range 3 {
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if r, _ = s.GetResident(ctx, "mayor"); r.Handle[dispatch.PendingPrompt] != nil {
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
	if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.GetResident(ctx, "mayor")
	if err != nil || !r.Live() || r.Handle[dispatch.PendingPrompt] != nil {
		t.Fatalf("resident = %+v %v", r, err)
	}
	if n := strings.Count(calls(), "agent prompt"); n != 1 {
		t.Fatalf("prompt sent %d times:\n%s", n, calls())
	}
}

// A bead session's first prompt goes the same way (startAgent serves both).
// An agent that is working took the prompt, whatever its pane shows; one
// that is idle with nothing of the prompt in its pane did not.
func TestStartAgentChecksThePromptArrived(t *testing.T) {
	shortPromptWait(t)
	for status, pending := range map[string]bool{"working": false, "idle": true} {
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
		if got := handle[dispatch.PendingPrompt] != nil; got != pending {
			t.Errorf("agent %s: pending = %v, want %v (%v)", status, got, pending, handle)
		}
	}
}

// A herdr whose panes cannot be read tells nothing about the prompt: it is
// taken as arrived, as before yardr looked, and not sent a second time.
func TestStartAgentUnreadablePaneIsNotPending(t *testing.T) {
	h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "pane read") echo '{"error":{"code":"unknown_command","message":"no pane read"}}' >&2; exit 1 ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	handle, err := h.startAgent(context.Background(), []string{"agent", "start", "b1"}, "b1", "read the brief",
		map[string]any{"agent": "b1", "pane": "w1:p1"}, true)
	if err != nil || handle[dispatch.PendingPrompt] != nil {
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

// The mayor yardr init adds greets the operator by mail in its first
// session: the onboarding ends in yardr chat, which shows mail and no pane.
// The prompt still begins and ends as every resident's does, which is what
// the pane is searched for, and a later session is asked for no greeting.
func TestFirstPromptAsksForAGreetingByMail(t *testing.T) {
	r := store.Resident{Name: "mayor", Config: map[string]any{GreetKey: "ada"}}
	got := residentPrompt("yardr --home /y", r)
	for _, want := range []string{"`yardr --home /y mail ada \"<text>\"`", "`yardr --home /y chat`", "the next setup step",
		"`yardr --home /y prime --resident mayor` " + ResidentPromptEnd} {
		if !strings.Contains(got, want) {
			t.Errorf("prompt lacks %q:\n%s", want, got)
		}
	}
	plain := residentPrompt("yardr", store.Resident{Name: "mayor"})
	if plain != "You are the yardr resident \"mayor\". Run `yardr prime --resident mayor` and follow it." {
		t.Errorf("a resident that greets nobody got %q", plain)
	}
	if !strings.HasPrefix(got, ResidentPromptLead+` "mayor". `) || !strings.HasSuffix(got, ResidentPromptEnd) ||
		!showsPrompt("❯ "+got, got) {
		t.Errorf("the greeting's prompt does not begin and end as a resident's:\n%s", got)
	}
	now := time.Now()
	r.StartedAt = &now
	if again := residentPrompt("yardr", r); again != plain {
		t.Errorf("a session after the first is asked to greet again:\n%s", again)
	}
	if Greets(r) != "" || Greeted(r) {
		t.Errorf("a resident that is not running greets: %q %v", Greets(r), Greeted(r))
	}
	r.State = "running"
	if !Greeted(r) {
		t.Error("the first session of a resident that greets is not the greeting one")
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
			if _, err := s.PutResident(ctx, store.Resident{Name: "mayor", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			r, err := s.GetResident(ctx, "mayor")
			if err != nil || !r.Live() {
				t.Fatalf("resident not running: %+v %v", r, err)
			}
			prompt, _ := r.Handle[dispatch.PendingPrompt].(string)
			if !strings.HasPrefix(prompt, ResidentPromptLead) || strings.Contains(calls(), "agent prompt") {
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
			r, _ = s.GetResident(ctx, "mayor")
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
					if hint, _ := e.Data["hint"].(string); agent == "" || !strings.HasPrefix(hint, "herdr agent prompt "+agent+" '"+ResidentPromptLead) {
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
