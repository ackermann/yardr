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
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
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
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}}
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
	if !h.PromptShown(ctx, r.Handle, prompt) {
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
