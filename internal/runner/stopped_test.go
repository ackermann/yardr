package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The pane of a Kimi builder (60 columns) on 11 Oct 2026, stopped on its
// usage limit mid-work (yardr-re0h).
const kimiStopped = "       Error: [provider.auth_error] 403 You've reached your\n" +
	"     5-hour usage limit. Your quota will reset when the current\n" +
	"     5-hour window ends. To continue now, purchase extra usage or\n" +
	"     upgrade your plan:\n" +
	"     https://www.kimi.com/membership/subscription?tab=quota\n" +
	"       If this persists, run `/export-debug-zip` and share the\n" +
	"     file with us for diagnosis. Please don't share it publicly.\n" +
	"\n" +
	"     ────────────────────────────────────────────────────────────\n" +
	"       Todo\n" +
	"       ✓ internal/source: runner + command files (command.go)\n" +
	"       ● declaration.go: thread/link keys + refusals\n" +
	"       ○ limits: use source runner, resolve program\n" +
	"       … +3 more (1 done · 2 pending) · ctrl+t to expand\n" +
	"     ╭──────────────────────────────────────────────────────────╮\n" +
	"     │ >                                                        │\n" +
	"     ╰──────────────────────────────────────────────────────────╯\n"

// herdr has a Kimi agent's status from Kimi's hooks alone, and a turn that
// ends on an error tells it nothing: the builder above was "working" to
// herdr until a person prompted it, so no check that reads an idle agent's
// pane ever ran. An agent herdr calls working on the hooks' word, whose
// pane ends in an error of its harness and has stood still for stopStill,
// is idle. One whose pane still moves, one herdr watches the screen of, and
// one with no error at its pane's end are working as herdr says.
func TestStatusOfAHarnessThatStoppedWhileHerdrSaysWorking(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	show := func(screen, agent string) {
		t.Helper()
		for name, text := range map[string]string{"screen": screen, "agent": agent} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	const hooks = `{"result":{"agent":{"agent":"kimi","agent_status":"working","screen_detection_skipped":true}}}`
	const screen = `{"result":{"agent":{"agent":"kimi","agent_status":"working"}}}`
	h := &Herdr{Bin: deafHerdrBin(t, `case "$1 $2" in
  "pane read") cat `+dir+`/screen ;;
  "agent get") cat `+dir+`/agent ;;
esac`)}
	handle := map[string]any{"agent": "yardr-embp-new", "pane": "w1:p2", "kind": "kimi"}
	status := func(want, when string) {
		t.Helper()
		got, err := h.Status(ctx, handle)
		if err != nil || got != want {
			t.Fatalf("%s: status %q err %v, want %q", when, got, err, want)
		}
	}
	// later is stopStill passing over what is remembered of the pane.
	later := func() {
		t.Helper()
		s, ok := h.stops["w1:p2"]
		if !ok {
			t.Fatal("nothing is remembered of the pane")
		}
		s.since = s.since.Add(-stopStill)
		h.stops["w1:p2"] = s
	}

	show(kimiStopped, hooks)
	status("working", "the error has just been printed")
	later()
	status("idle", "the error has stood for stopStill")
	status("idle", "and stands")

	// It was prompted and works: the screen moves, the old error still on it.
	show(" ✨ continue\n"+kimiStopped+" ⠋ Thinking 3s\n", hooks)
	status("working", "the pane changed")
	later()
	show(" ✨ continue\n"+kimiStopped+" ⠙ Thinking 65s\n", hooks)
	status("working", "the pane changed again")

	// herdr reads this agent's screen itself: its word stands.
	show(kimiStopped, screen)
	status("working", "herdr watches the screen")
	if len(h.stops) != 0 {
		t.Fatalf("remembered of the pane: %v, want nothing", h.stops)
	}

	// A pane with no error at its end is never remembered.
	show(" ● Used Read (herdr.go) · 130 lines\n ⠋ Thinking 3s\n", hooks)
	status("working", "no error in the pane")
	if len(h.stops) != 0 {
		t.Fatalf("remembered of the pane: %v, want nothing", h.stops)
	}
}
