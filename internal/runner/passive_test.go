package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// herdr scrolls the transcript of an idle agent on the alternate screen to
// answer a text read of `recent` or `recent-unwrapped`, and no other read
// (its "Alternate-screen history reads"). Only the form a person's command
// asks for is such a read (yardr-dsmt).
func TestPaneReadArgs(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		form paneForm
		want string
	}{
		{paneScreen, "pane read p1 --source visible --format ansi"},
		{paneRecent, "pane read p1 --source recent-unwrapped --lines 200 --format ansi"},
		{paneHistory, "pane read p1 --source recent-unwrapped --lines 200"},
	} {
		if got := strings.Join(paneReadArgs("p1", c.form, 200), " "); got != c.want {
			t.Errorf("paneReadArgs(%d) = %q, want %q", c.form, got, c.want)
		}
	}
}

// scrollingReads are the pane reads among a fake herdr's logged calls that
// herdr may answer by scrolling an agent's transcript: every one that is
// not of the visible screen and not styled.
func scrollingReads(calls string) []string {
	var out []string
	for _, l := range strings.Split(calls, "\n") {
		if !strings.HasPrefix(l, "pane read ") {
			continue
		}
		if f := strings.Fields(l); slices.Contains(f, "ansi") || slices.Contains(f, "--ansi") ||
			slices.Contains(f, "visible") || slices.Contains(f, "detection") {
			continue
		}
		out = append(out, l)
	}
	return out
}

// No pass of the daemon reads a pane in a form herdr may scroll it for:
// over a crew member whose first prompt is lost and delivered again, over
// an idle one, and over a bead's session, with every check that reads a
// pane on its way (the prompt's place, the harness's screen, the input, a
// dialog, the tail). Nor does the history read, should a pass reach it.
func TestNoPassReadsAPanesHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("crew", func(t *testing.T) {
		t.Parallel()
		h, calls := droppingHerdr(t, 1)
		s := storetest.New(t)
		d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
		if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
			t.Fatal(err)
		}
		for range 5 {
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		r, _ := s.GetCrew(ctx, "yardmaster")
		pass := dispatch.WithPaneCache(ctx)
		if _, err := h.Tail(pass, r.Handle, 20); err != nil {
			t.Fatal(err)
		}
		if _, err := h.History(pass, r.Handle, 20); err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(calls(), "pane read "); n == 0 {
			t.Fatalf("the passes read no pane, so the test shows nothing:\n%s", calls())
		}
		if bad := scrollingReads(calls()); len(bad) > 0 {
			t.Errorf("a pass read a pane's history, which scrolls an idle fullscreen agent:\n%s", strings.Join(bad, "\n"))
		}
	})
	t.Run("bead", func(t *testing.T) {
		t.Parallel()
		repo := seedRepo(t)
		s := storetest.New(t)
		mustT(t, s.PutDepot(ctx, store.Depot{Name: "r", Path: repo, Base: "main"}, "me"))
		mustT(t, s.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2}, "me"))
		_, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "builders"}, "me")
		mustT(t, err)
		_, err = s.CreateBead(ctx, store.NewBead{Title: "b0", Stage: "new", Depot: "r"}, "me")
		mustT(t, err)
		dir := t.TempDir()
		log := filepath.Join(dir, "calls")
		h := &Herdr{Socket: fakeHerdrAPI(t, log), Bin: beadPassHerdr(t, dir), WorktreeRoot: t.TempDir(), Store: s}
		d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}}
		for range 4 {
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		}
		raw, _ := os.ReadFile(log)
		if n := strings.Count(string(raw), "pane read "); n == 0 {
			t.Fatalf("the passes read no pane, so the test shows nothing:\n%s", raw)
		}
		if bad := scrollingReads(string(raw)); len(bad) > 0 {
			t.Errorf("a pass read a pane's history, which scrolls an idle fullscreen agent:\n%s", strings.Join(bad, "\n"))
		}
	})
}

// Outside a pass the history read is herdr's own, and only that one: what
// a person's command asks for. Tail, which the chat polls, stays passive,
// and PaneScreen, which the operator's SessionScreen answers with, is the
// screen form.
func TestOnlyHistoryReadsHistory(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	h := &Herdr{Bin: fakeHerdrBin(t, `printf '%s\n' "$*" >> `+dir+`/calls
case "$1 $2" in
  "pane read") echo "❯ " ;;
esac`)}
	handle := map[string]any{"pane": "p1"}
	if _, err := h.Tail(ctx, handle, 20); err != nil {
		t.Fatal(err)
	}
	if bad := scrollingReads(readCalls(t, dir+"/calls")); len(bad) > 0 {
		t.Fatalf("Tail read the pane's history:\n%s", strings.Join(bad, "\n"))
	}
	if _, err := h.History(ctx, handle, 20); err != nil {
		t.Fatal(err)
	}
	if bad := scrollingReads(readCalls(t, dir+"/calls")); len(bad) != 1 {
		t.Fatalf("History made %d history reads, want 1:\n%s", len(bad), readCalls(t, dir+"/calls"))
	}
	if _, err := h.PaneScreen(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	calls := readCalls(t, dir+"/calls")
	if bad := scrollingReads(calls); len(bad) != 1 {
		t.Fatalf("with the screen read, %d history reads, want History's 1:\n%s", len(bad), calls)
	}
	if want := "pane read p1 --source visible --format ansi"; !strings.Contains(calls, want) {
		t.Errorf("PaneScreen read no %q:\n%s", want, calls)
	}
}

// fullScreen is the screen of a fullscreen Claude Code that took its first
// prompt and printed more than a screenful after it: the end of its answer
// and its input, and nothing of the prompt. A passive read gives no more.
const fullScreen = `  seconds.

  Where I was wrong
  - I said the program in the pane receives nothing from a read.

✻ Cooked for 14s
────────────────
❯
────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)`

// scrolledHerdr is a fake herdr whose agent takes every prompt, works on it
// and has answered with more than its screen holds by the time anyone
// looks: herdr says working of each prompt (or `took`, if given), `agent
// get` says working as often as there is a line in dir/working and idle
// after, and the pane shows fullScreen.
func scrolledHerdr(t *testing.T, start, took string) (h *Herdr, calls func() string, dir string) {
	t.Helper()
	dir = t.TempDir()
	log := filepath.Join(dir, "calls")
	if err := os.WriteFile(filepath.Join(dir, "screen"), []byte(fullScreen+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h = &Herdr{Socket: fakeHerdrAPI(t, log), Bin: deafHerdrBin(t, `cd `+dir+`
printf '%s\n' "$*" >> calls
case "$1 $2" in
  "agent start") `+start+` ;;
  "agent prompt") echo x > working; answer `+took+` ;;
  "pane read") cat screen ;;
  "agent get")
    if [ -s working ]; then : > working; echo '{"result":{"agent":{"agent_status":"working"}}}'
    else echo '{"result":{"agent":{"agent_status":"idle"}}}'; fi ;;
  *) echo '{"result":{"type":"ok"}}' ;;
esac`)}
	return h, func() string { return readCalls(t, log) }, dir
}

// A first prompt the agent took, answered with more than a screenful and
// went idle after is not on its screen, which is all a passive read of a
// fullscreen agent gives. Before yardr-2zst the daemon's plain read fetched
// the history and found it; since, nothing of it is in the read, as nothing
// of a lost prompt is. herdr saw the agent start on it, so it is not sent a
// second time and no session_prompt_lost is said: it arrived, unseen.
func TestPromptThatLeftTheScreenIsNotLost(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name  string
		start string // what `agent start` answers
	}{
		// The runner sent it at start, and herdr saw the agent working.
		{"sent at start", `echo '{"result":{"type":"ok"}}'`},
		// The dispatcher delivered it, to an agent that was not ready at
		// start, and saw the agent working right after.
		{"delivered by the dispatcher", `echo '{"error":{"code":"agent_not_ready","message":"agent is blocked during startup and is not ready for prompts"},"id":"x"}' >&2; exit 1`},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			h, calls, dir := scrolledHerdr(t, c.start, "working")
			ctx := context.Background()
			s := storetest.New(t)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
			if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
				t.Fatal(err)
			}
			if err := d.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "working"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			// From here the agent is idle whenever a pass asks, so no pass
			// sees it work; the look the dispatcher takes right after a
			// delivery of its own does (the fake's next `agent get`).
			for range 6 {
				if err := d.Reconcile(ctx); err != nil {
					t.Fatal(err)
				}
			}
			r, err := s.GetCrew(ctx, "yardmaster")
			if err != nil || !r.Live() {
				t.Fatalf("crew member not running: %+v %v", r, err)
			}
			if r.Handle[store.PendingPrompt] != nil || r.Handle[dispatch.PromptStarted] != nil {
				t.Fatalf("the prompt is still pending: %v", r.Handle)
			}
			if n := strings.Count(calls(), "agent prompt"); n != 1 {
				t.Fatalf("the prompt was sent %d times, want once:\n%s", n, calls())
			}
			evs, err := s.Events(ctx, r.Journal, 100)
			if err != nil {
				t.Fatal(err)
			}
			got, unseen := map[string]int{}, false
			for _, e := range evs {
				got[e.Kind]++
				if e.Kind == "session_prompted" {
					_, unseen = e.Data["unseen"].(string)
					if e.Data["shown"] != nil {
						t.Errorf("session_prompted says the prompt was found in the pane: %+v", e.Data)
					}
				}
			}
			if got["session_prompted"] != 1 || got["session_prompt_lost"] != 0 || got["session_prompt_gave_up"] != 0 || !unseen {
				t.Fatalf("events = %v (unseen %v), want one session_prompted that says unseen and nothing lost", got, unseen)
			}
		})
	}
}

// The same screen with a prompt herdr saw nothing start on: that one is
// lost, and is delivered again as before.
func TestPromptNothingStartedOnIsStillLost(t *testing.T) {
	t.Parallel()
	h, calls, _ := scrolledHerdr(t, `echo '{"result":{"type":"ok"}}'`, "idle")
	ctx := context.Background()
	s := storetest.New(t)
	d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"herdr": h}, PromptSettle: -1, PromptWait: -1}
	if _, err := s.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "herdr", Dir: t.TempDir()}, "me"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := d.Reconcile(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(calls(), "agent prompt"); n < 2 {
		t.Fatalf("a prompt nothing started on was sent %d times, want it delivered again:\n%s", n, calls())
	}
}

// A harness on the normal screen has its history in herdr's scrollback, and
// a read is its last promptScan lines. A prompt that is not in a read as
// long as that may be above it: nothing is known, which is said as an
// error, as of a pane that could not be read. In a shorter read, all herdr
// holds, a prompt that is not there is not shown.
func TestPromptBeyondTheReadIsUnknown(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, c := range []struct {
		lines int
		known bool
	}{{promptScan + 50, false}, {40, true}} {
		pane := filepath.Join(t.TempDir(), "pane")
		text := strings.Repeat("• output\n", c.lines) + "› \n"
		if err := os.WriteFile(pane, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		// As herdr: the last lines of what it holds.
		h := &Herdr{Bin: fakeHerdrBin(t, `case "$1 $2" in
  "pane read") tail -n `+fmt.Sprint(promptScan)+` `+pane+` ;;
esac`)}
		handle := map[string]any{"pane": "p1", harnessKind: "codex"}
		shown, err := h.PromptShown(ctx, handle, "Read /tmp/brief.md and follow it.")
		if shown || (err == nil) != c.known || (!c.known && !errors.Is(err, errBeyond)) {
			t.Errorf("%d lines above the input: PromptShown = %v, %v; want known %v", c.lines, shown, err, c.known)
		}
		if typed, err := h.PromptTyped(ctx, handle, "Read /tmp/brief.md and follow it."); typed || (err == nil) != c.known {
			t.Errorf("%d lines above the input: PromptTyped = %v, %v; want known %v", c.lines, typed, err, c.known)
		}
		saw := h.arrived(ctx, handle, "Read /tmp/brief.md and follow it.")
		if (saw == sawAbsent) != c.known {
			t.Errorf("%d lines above the input: arrived = %q", c.lines, saw)
		}
		if !c.known && saw != sawUnknown {
			t.Errorf("%d lines above the input: arrived = %q, want unknown", c.lines, saw)
		}
	}
}

// A stall's tail is asked for as 20 lines, and the screen of a fullscreen
// agent in a short pane has fewer. The tail is then what the screen has:
// no error, and nothing made up for the lines that scrolled off.
func TestTailOfAPaneShorterThanTheTail(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	h := &Herdr{Bin: fakeHerdrBin(t, `case "$1 $2" in
  "pane read") printf '%s\n' '' '  Run /model to pick a different model.' '' '────' '❯ ' '────' '' ;;
esac`)}
	tail, err := h.Tail(ctx, map[string]any{"pane": "p1"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if want := "  Run /model to pick a different model.\n────\n❯\n────"; tail != want {
		t.Errorf("tail = %q, want %q", tail, want)
	}
}
