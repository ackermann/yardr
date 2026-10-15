package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
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
	got := capture(t, func() { printInboxEvent(e, "yardmaster", "") })
	want := "     7 10-02 09:14  hook (mentioned) linear comment create by Jane on \"Retries\": can you look?  https://x/1\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestHookDeliveryPrintsPrintableThreadFields(t *testing.T) {
	e := store.Event{Seq: 7, Kind: "hook", At: time.Date(2026, 10, 2, 9, 14, 0, 0, time.Local), Data: map[string]any{
		"source": "linear", "type": "comment", "action": "create",
		"thread": map[string]any{"title": "Retries\r\n\x1b[2K"}, "url": "https://x/\rhidden\x1b[2K",
	}}
	got := capture(t, func() { printInboxEvent(e, "yardmaster", "") })
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("hook delivery split across lines: %q", got)
	}
	if strings.ContainsAny(got, "\r\x1b") {
		t.Fatalf("hook delivery printed a control character: %q", got)
	}
	want := "     7 10-02 09:14  hook (-) linear comment create on \"Retries???[2K\"  https://x/?hidden?[2K\n"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestEventPrintersPrintThreadFieldsWithoutControlCharacters(t *testing.T) {
	id := "t1\r    something else\x1b[2K"
	title := "Retries\r\n\x1b[2K"
	url := "https://x/\rhidden\x1b[2K"
	wants := []string{"thread=t1?    something else?[2K", "title=Retries???[2K", "url=https://x/?hidden?[2K"}
	check := func(name string, print func()) {
		t.Helper()
		got := capture(t, print)
		if strings.Count(got, "\n") != 1 || strings.ContainsAny(got, "\r\x1b") {
			t.Fatalf("%s printed a control character or split a row: %q", name, got)
		}
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s printed no %q:\n%s", name, want, got)
			}
		}
	}

	e := store.Event{Seq: 8, Kind: "thread_state", Actor: "ann", At: time.Date(2026, 10, 2, 9, 14, 0, 0, time.Local), Data: map[string]any{
		"thread": id, "title": title, "url": url,
	}}
	check("event log", func() { printEvent(e) })
	check("inbox journal", func() { printBeadDelivery(e, "ann") })
	if e.Data["thread"] != id || e.Data["title"] != title || e.Data["url"] != url {
		t.Fatalf("printing changed event data: %+v", e.Data)
	}
}

func TestEventLogPrintsPrintableNestedThreadFields(t *testing.T) {
	e := store.Event{Seq: 9, Kind: "hook", At: time.Date(2026, 10, 2, 9, 14, 0, 0, time.Local), Data: map[string]any{
		"thread": map[string]any{"id": "t1\r    something else\x1b[2K", "title": "Retries\r\n\x1b[2K", "url": "https://x/\rhidden\x1b[2K"},
	}}
	got := capture(t, func() { printEvent(e) })
	if strings.Count(got, "\n") != 1 || strings.ContainsAny(got, "\r\x1b") {
		t.Fatalf("event log printed a control character or split a row: %q", got)
	}
	for _, want := range []string{"id=t1?    something else?[2K", "title=Retries???[2K", "url=https://x/?hidden?[2K"} {
		if !strings.Contains(got, want) {
			t.Errorf("event log printed no %q:\n%s", want, got)
		}
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
	got := capture(t, func() { printInboxEvent(e, "yardmaster", "") })
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
	got := capture(t, func() { printInboxEvent(e, "yardmaster", "") })
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
	got := capture(t, func() { printInboxEvent(e, "yardmaster", "") })
	want := "    15 10-06 09:05  yardr-87dd move_refused (on) by yard reason=stale command=yardr bead done yardr-87dd " +
		"error=stale: bead yardr-87dd is now in review agent=yardr-87dd-new hint=it did not move session=s1\n"
	if got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

// capture returns what fn prints to stdout. The pipe is read while fn runs:
// it holds 64 KB, and a write past that waits for a reader.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	read := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		r.Close()
		read <- b
	}()
	old := os.Stdout
	os.Stdout = w
	fn()
	os.Stdout = old
	w.Close()
	return string(<-read)
}

// capture reads what fn prints while fn runs: read only once fn has
// returned, a command printing more than the pipe holds (64 KB on macOS)
// blocked in its write until go test's timeout (yardr-bbyz).
func TestCaptureOutputLargerThanThePipe(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	const total = 205
	for i := 0; i < total; i++ {
		if _, err := st.CreateBead(ctx, store.NewBead{Title: "bead " + strconv.Itoa(i)}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := run(t, (&app{st: st}).rootCmd(), "bead", "list", "--all", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if len(out) <= 64<<10 {
		t.Fatalf("the listing is %d bytes, no more than a pipe holds: the test shows nothing", len(out))
	}
	var beads []json.RawMessage
	if err := json.Unmarshal([]byte(out), &beads); err != nil {
		t.Fatalf("the captured listing is not the whole of it: %v", err)
	}
	if len(beads) != total {
		t.Errorf("captured %d beads, want %d", len(beads), total)
	}
}
