package cli

import (
	"io"
	"os"
	"testing"
	"time"

	"github.com/mckean/yard/internal/store"
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
		"deliveries": map[string]any{"mayor": "mentioned"},
	}}
	got := capture(t, func() { printInboxEvent(e, "mayor") })
	want := "     7 10-02 09:14  hook (mentioned) linear comment create by Jane on \"Retries\": can you look?  https://x/1\n"
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
