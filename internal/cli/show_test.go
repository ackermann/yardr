package cli

import (
	"io"
	"os"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

func TestShowNumbers(t *testing.T) {
	for in, want := range map[any]string{
		float64(2366465): "2366465",
		float64(-3):      "-3",
		1.5:              "1.5",
		"x":              "x",
		true:             "true",
	} {
		if got := show(in); got != want {
			t.Errorf("show(%v) = %q, want %q", in, got, want)
		}
	}
	if got := fmtConfig(map[string]any{"handle": map[string]any{"pid": float64(2366465), "log": "/tmp/x"}}); got != "handle={log=/tmp/x pid=2366465}" {
		t.Errorf("nested = %q", got)
	}
}

func TestHookDeliveryLine(t *testing.T) {
	e := store.Event{Seq: 7, Kind: "hook", At: time.Date(2026, 10, 2, 9, 14, 0, 0, time.Local), Data: map[string]any{
		"source": "linear", "type": "comment", "action": "create",
		"actor":  map[string]any{"id": "u1", "name": "Jane"},
		"thread": map[string]any{"title": "Retries"}, "text": "can you look?", "url": "https://x/1",
		"deliveries": map[string]any{"yardmaster": "mentioned"},
	}}
	got := capture(t, func() { printInboxEvent(e, "yardmaster") })
	want := "     7 10-02 09:14  hook (mentioned) linear comment create by Jane on \"Retries\": can you look?  https://x/1\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// The inbox row of a harness's error begins with its reason and the line the
// harness printed, before the rest of what the event carries.
func TestHarnessErrorLineLeadsWithItsReason(t *testing.T) {
	bead := "yardr-hk53"
	e := store.Event{Seq: 14, Kind: "session_harness_error", Actor: "yard", Bead: &bead,
		At: time.Date(2026, 10, 6, 3, 5, 0, 0, time.Local), Data: map[string]any{
			"reason": "at_capacity", "line": "■ Selected model is at capacity.", "group": "reviewers",
			"hint": "get it going", "session": "s1", "tail": "› ",
		}}
	got := capture(t, func() { printInboxEvent(e, "yardmaster") })
	want := "    14 10-06 03:05  yardr-hk53 session_harness_error (on) by yard reason=at_capacity line=■ Selected model is at capacity. " +
		"group=reviewers hint=get it going session=s1 tail=› \n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// The inbox row of a blocked session ends with its pane's last lines: tail
// is the last of the event's fields by name, so the question the agent
// waits on stands under the row, as the pane shows it.
func TestBlockedLineEndsWithThePane(t *testing.T) {
	bead := "yardr-cdmr"
	e := store.Event{Seq: 16, Kind: "session_blocked", Actor: "yard", Bead: &bead,
		At: time.Date(2026, 10, 6, 21, 5, 0, 0, time.Local), Data: map[string]any{
			"session": "s1", "handle": map[string]any{"agent": "yardr-cdmr-new"},
			"tail": "⏺ Bash(git push)\n│ Do you want to proceed?\n│ ❯ 1. Yes",
		}}
	got := capture(t, func() { printInboxEvent(e, "yardmaster") })
	want := "    16 10-06 21:05  yardr-cdmr session_blocked (on) by yard handle={agent=yardr-cdmr-new} session=s1 " +
		"tail=⏺ Bash(git push)\n│ Do you want to proceed?\n│ ❯ 1. Yes\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// The inbox row of a refused move begins with why, the command the agent
// ran and the yard's answer to it.
func TestMoveRefusedLineLeadsWithTheCommand(t *testing.T) {
	bead := "yardr-87dd"
	e := store.Event{Seq: 15, Kind: store.MoveRefused, Actor: "yard", Bead: &bead,
		At: time.Date(2026, 10, 6, 9, 5, 0, 0, time.Local), Data: map[string]any{
			"reason": "stale", "command": "yardr bead done yardr-87dd", "error": "stale: bead yardr-87dd is now in review",
			"agent": "yardr-87dd-new", "hint": "it did not move", "session": "s1",
		}}
	got := capture(t, func() { printInboxEvent(e, "yardmaster") })
	want := "    15 10-06 09:05  yardr-87dd move_refused (on) by yard reason=stale command=yardr bead done yardr-87dd " +
		"error=stale: bead yardr-87dd is now in review agent=yardr-87dd-new hint=it did not move session=s1\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// capture returns what fn prints to stdout.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = old
	w.Close()
	b, _ := io.ReadAll(r)
	return string(b)
}
