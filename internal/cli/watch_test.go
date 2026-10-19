package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// yardr watch --json prints one event per line, so a consumer reads a line
// and parses it. The shared printer pretty-prints, which broke a line
// reader at its first event (yardr-pj1h). One event's data holds a newline,
// which the compact encoding escapes onto the line.
func TestWatchJSONOneEventPerLine(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "a door", Body: "Hang it.\nOn two hinges.\n"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	body := "Hung."
	if _, err := st.UpdateBead(ctx, b.ID, store.BeadPatch{Body: &body}, store.Expect{}, "jo"); err != nil {
		t.Fatal(err)
	}

	a := &app{st: st, actor: "me", json: true}
	cmd := a.watchCmd()
	cmd.SetArgs([]string{"--all"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	wctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// watch never returns on its own: read its lines while it runs, and
	// end it once the replay has our updated event on the pipe.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		for sc := bufio.NewScanner(r); sc.Scan(); {
			lines <- sc.Text()
		}
	}()
	cmd.SetOut(w)
	done := make(chan error, 1)
	go func() { done <- cmd.ExecuteContext(wctx) }()
	var got []string
	timeout := time.After(15 * time.Second)
wait:
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				t.Fatal("watch ended before the updated event")
			}
			got = append(got, line)
			var e store.Event
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				break wait // the assertions below say which line broke
			}
			if e.Kind == "updated" && e.Bead != nil && *e.Bead == b.ID {
				break wait
			}
		case <-timeout:
			t.Fatal("watch printed no updated event in 15s")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("watch: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("watch did not end after cancel")
	}
	w.Close()
	for line := range lines {
		got = append(got, line)
	}

	// Every line parses by itself, and the updated event kept its newline.
	var updated *store.Event
	var updatedLine string
	for _, line := range got {
		var e store.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("a line of watch --json parses by itself: %v: %q", err, line)
			continue
		}
		if e.Kind == "updated" && e.Bead != nil && *e.Bead == b.ID {
			updated, updatedLine = &e, line
		}
	}
	if updated == nil {
		t.Fatalf("no updated event for %s in %d lines", b.ID, len(got))
	}
	was, _ := updated.Data["was"].(map[string]any)
	if was["body"] != "Hang it.\nOn two hinges.\n" {
		t.Errorf("the updated event lost its newline: %+v", updated.Data)
	}
	if !strings.Contains(updatedLine, `Hang it.\nOn two hinges.\n`) {
		t.Errorf("the newline is not escaped onto one line: %q", updatedLine)
	}
}

// The help of watch says how --json prints: one event per line.
func TestWatchHelpSaysOneEventPerLine(t *testing.T) {
	cmd := (&app{}).watchCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"--help"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "one event per line") {
		t.Errorf("watch --help says nothing of one event per line:\n%s", buf.String())
	}
}
