package cli

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// The bead bead ask --new makes is a question nothing routes: the
// dispatcher reports it unrouted in no yard, the ask stands among the
// person's decisions, and after the answer the bead is still the asker's
// to close with what they did with it. In a yard with the starter flow
// every route is a depot's, so a bead of no depot matched none and its
// asker was told "unrouted" seconds after asking (yardr-c9z6); in a yard
// with no flow there is no route at all.
func TestBeadAskNewIsNotUnrouted(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	starter := func(t *testing.T) *app {
		a, _ := packYard(t, nil)
		requireNonInteractive(t)
		args := append([]string{"init", "web", "--pack", "starter", "--yes"}, starterFlags...)
		if out, err := run(t, a.depotCmd(), args...); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		return a
	}
	bare := func(t *testing.T) *app {
		st, _, _ := askYard(t)
		return &app{st: st, actor: "yardmaster"}
	}
	for _, c := range []struct {
		name string
		yard func(*testing.T) *app
		args []string // where --new puts the bead
		// stage is where the bead stands: new under no flow, else where
		// its depot's beads start.
		stage string
	}{
		{"the starter flow", starter, nil, "new"},
		{"the starter flow, in its depot", starter, []string{"--depot", "web"}, "backlog"},
		{"no flow", bare, nil, "new"},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := c.yard(t)
			a.actor = "yardmaster"
			if err := a.st.SetSetting(ctx, "operator", "mckean", yardOperator(ctx, a.st)); err != nil {
				t.Fatal(err)
			}
			d := &dispatch.Dispatcher{Store: a.st, Runners: map[string]dispatch.Runner{}, Backoff: time.Nanosecond}
			// passes runs the dispatcher twice and fails on anything it
			// reported of the bead or did with it.
			passes := func(id, when string) {
				t.Helper()
				for range 2 {
					if err := d.Reconcile(ctx); err != nil {
						t.Fatal(err)
					}
				}
				evs, err := a.st.Events(ctx, id, 0)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range evs {
					if slices.Contains(store.AttentionKinds, e.Kind) {
						t.Errorf("%s: the question raised %s: %v", when, e.Kind, e.Data)
					}
				}
				if b, err := a.st.GetBead(ctx, id); err != nil || b.Group != nil || b.Hold || b.Status != "open" || b.Stage != c.stage {
					t.Errorf("%s: bead = %+v, %v; want it open in %s, held by nobody", when, b, err, c.stage)
				}
			}

			args := append([]string{"ask", "--new", "Rename 'depot'?", "--label", "topic:words",
				"--context", "The glossary keeps it.", "--option", "Keep depot: nothing changes"}, c.args...)
			if out, err := run(t, a.beadCmd(), args...); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			made, err := a.st.ListBeads(ctx, store.BeadFilter{Label: "topic:words"})
			if err != nil || len(made) != 1 || made[0].Type != store.TypeQuestion {
				t.Fatalf("the bead of ask --new = %+v, %v", made, err)
			}
			id := made[0].ID
			passes(id, "asked")
			q, err := a.st.UserAsks(ctx, "mckean")
			if err != nil || len(q.Open) != 1 || q.Open[0].Bead != id || q.Open[0].Topic != "topic:words" {
				t.Fatalf("mckean's decisions = %+v, %v", q, err)
			}

			// Answered: the bead waits for its asker, and still nobody is
			// told of it.
			me := &app{st: a.st, actor: "mckean", cfg: a.cfg}
			if out, err := run(t, me.beadCmd(), "answer", id, "1", "1"); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			passes(id, "answered")
			if q, err := a.st.UserAsks(ctx, "mckean"); err != nil || len(q.Open) != 0 || len(q.Decided) != 1 {
				t.Fatalf("mckean's decisions after the answer = %+v, %v", q, err)
			}
			if out, err := run(t, a.beadCmd(), "close", id, "--reason", "kept the word; nothing to change"); err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if b, err := a.st.GetBead(ctx, id); err != nil || b.Status != "closed" {
				t.Fatalf("closed bead = %+v, %v", b, err)
			}
		})
	}
}

// bead ask --new -b - takes the new bead's body from the command's
// stdin, trailing newlines trimmed as before.
func TestBeadAskNewStdinBody(t *testing.T) {
	t.Parallel()
	st, ctx, _ := askYard(t)
	a := &app{st: st, actor: "yardmaster"}
	c := a.beadCmd()
	c.SetIn(strings.NewReader("what the words are for\n\n"))
	if out, err := run(t, c, "ask", "--new", "Rename 'depot'?", "--label", "topic:words",
		"--context", "The glossary keeps it.", "--option", "Keep depot: nothing changes", "-b", "-"); err != nil {
		t.Fatalf("ask --new -b -: %v\n%s", err, out)
	}
	made, err := st.ListBeads(ctx, store.BeadFilter{Label: "topic:words"})
	if err != nil || len(made) != 1 || made[0].Body != "what the words are for" {
		t.Fatalf("the bead of ask --new -b - = %+v, %v", made, err)
	}
}
