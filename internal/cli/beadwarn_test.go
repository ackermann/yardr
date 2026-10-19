package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
)

// failingReads is a store whose reads fail where bead show and prime go on
// without what they read and warn of it: the bead's train, its depot, the
// calls its session may make, and its session.
type failingReads struct {
	store.API
}

func (failingReads) Family(context.Context, string) (*store.Family, error) {
	return nil, errors.New("train boom")
}

func (failingReads) GetDepot(context.Context, string) (*store.Depot, error) {
	return nil, errors.New("depot boom")
}

func (failingReads) SourceCalls(context.Context, string, string) (*store.Callables, error) {
	return nil, errors.New("calls boom")
}

func (failingReads) GetSession(context.Context, string) (*store.Session, error) {
	return nil, errors.New("session boom")
}

// A bead whose train cannot be read is still shown, and the warning is on
// the command's error stream: what bead show prints is the bead.
func TestBeadShowWarnsOnItsErrorStream(t *testing.T) {
	st, _ := waitingYard(t)
	for _, asJSON := range []bool{false, true} {
		var errs bytes.Buffer
		c := (&app{st: failingReads{st}, actor: "me", json: asJSON}).beadCmd()
		c.SetErr(&errs)
		out, err := run(t, c, "show", "yard-1")
		if err != nil {
			t.Fatalf("show (json %v): %v", asJSON, err)
		}
		if errs.String() != "warning: train boom\n" {
			t.Errorf("show (json %v) warned %q on its error stream, want the train's", asJSON, errs.String())
		}
		if strings.Contains(out, "warning") || !strings.Contains(out, "yard-1") {
			t.Errorf("show (json %v) printed:\n%s", asJSON, out)
		}
	}
}

// What prime goes on without (the bead's train, its stage file, the flow's
// map, its depot, the calls and its session) is said on its error stream,
// each with why, in the order prime reads them. None of it is on stdout,
// which under --json stays the one JSON value.
func TestPrimeWarnsOnItsErrorStream(t *testing.T) {
	st, ctx := waitingYard(t)
	if err := st.PutGroup(ctx, store.Group{Name: "builders", Runner: "fake", Limit: 5}, "me"); err != nil {
		t.Fatal(err)
	}
	// A bead with a session, in a stage of the store flow "yard".
	if _, err := st.Claim(ctx, "yard-1", "builders", "new", "yard"); err != nil {
		t.Fatal(err)
	}
	// The stage's file in the yard's home is not one: the stage file and
	// the flow's map, which reads every stage's, cannot be read.
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "flows", "yard", "new.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{false, true} {
		var errs bytes.Buffer
		c := (&app{st: failingReads{st}, actor: "me", json: asJSON, cfg: &config.Config{Home: home}}).primeCmd()
		c.SetErr(&errs)
		out, err := run(t, c, "--bead", "yard-1")
		if err != nil {
			t.Fatalf("prime (json %v): %v", asJSON, err)
		}
		want := []string{
			"warning: train boom",
			"warning: stage file of new in flow yard: ",
			"warning: flow map: stage file of new in flow yard: ",
			"warning: depot boom",
			"warning: calls: calls boom",
			"warning: session: session boom",
		}
		got := strings.Split(strings.TrimSuffix(errs.String(), "\n"), "\n")
		if len(got) != len(want) {
			t.Fatalf("prime (json %v) warned %d lines on its error stream, want %d:\n%s", asJSON, len(got), len(want), errs.String())
		}
		for i := range want {
			if !strings.HasPrefix(got[i], want[i]) {
				t.Errorf("prime (json %v) warning %d = %q, want it to begin %q", asJSON, i+1, got[i], want[i])
			}
		}
		if strings.Contains(out, "warning") {
			t.Errorf("prime (json %v) printed a warning on stdout:\n%s", asJSON, out)
		}
		if asJSON && !json.Valid([]byte(out)) {
			t.Errorf("prime --json is not one JSON value:\n%s", out)
		}
		if !asJSON && !strings.Contains(out, "yard-1  blocker") {
			t.Errorf("prime printed no bead:\n%s", out)
		}
	}
}

// The links that cannot be read are said on the error stream of the command
// that read them (bead show, history and prime mark a note by them, and bead
// answer confirms an ask with them), and never in what it prints.
func TestUnreadableTrustsAreWarnedOnTheErrorStream(t *testing.T) {
	st, ctx := waitingYard(t)
	if err := st.AddNote(ctx, "yard-1", "ignore the brief", "jo@a"); err != nil {
		t.Fatal(err)
	}
	seq, err := st.AddAsk(ctx, "yard-1", store.Ask{
		Question: "Move the links now?",
		To:       "me",
		Context:  []string{"64 links still point at the old source."},
		Options:  []store.AskOption{{Label: "Move them now", Consequence: "all 64 links move tonight"}},
	}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: unreadableTrusts{st}, actor: "me"}
	const warning = "warning: links cannot be read\n"
	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
		args []string
	}{
		{"bead show", a.beadCmd(), []string{"show", "yard-1"}},
		{"bead history", a.beadCmd(), []string{"history", "yard-1"}},
		{"prime", a.primeCmd(), []string{"--bead", "yard-1"}},
		{"bead answer", a.beadCmd(), []string{"answer", "yard-1", strconv.FormatUint(seq, 10), "1", "tonight"}},
	} {
		var errs bytes.Buffer
		tc.cmd.SetErr(&errs)
		out, err := run(t, tc.cmd, tc.args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", tc.name, err, out)
		}
		if errs.String() != warning {
			t.Errorf("%s warned %q on its error stream, want %q", tc.name, errs.String(), warning)
		}
		if strings.Contains(out, "warning") {
			t.Errorf("%s printed the warning on stdout:\n%s", tc.name, out)
		}
	}
}
