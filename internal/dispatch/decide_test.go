package dispatch_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/packs"
)

// decideOffers returns the open asks on the bead, oldest first, and how many
// asks it has in all.
func decideOffers(t *testing.T, s store.API, id string) (open []store.ResolvedAsk, all int) {
	t.Helper()
	notes, err := s.Notes(context.Background(), id)
	must(t, err)
	asks := store.ResolveAsks(notes, time.Now())
	for _, r := range asks {
		if r.State == store.AskOpen {
			open = append(open, r)
		}
	}
	return open, len(asks)
}

// A bead that arrives in decide gets one card offering the stage's moves, in
// each of the flows the yard keeps and in the design pack's, where decide is
// entered from a critic's done and leaves by bare edges to approved, draft
// or backlog. The card is the stage file's own making (an ## Ask section of
// the kind moves), put in the move that brought the bead; the dispatcher's
// passes neither double it nor undo it. A member's pick moves the bead on, as
// yardr bead advance would; someone outside the group moves nothing; and a
// bead that comes back to decide is a second arrival with a second card.
func TestDecideOffersItsMoves(t *testing.T) {
	t.Parallel()
	flowsOf := func(t *testing.T, name string) (flow *store.Flow, decide string) {
		t.Helper()
		path := storetest.RepoFlow(t, name)
		text, err := os.ReadFile(path)
		must(t, err)
		flow, err = store.ParseFlow(string(text))
		must(t, err)
		flow.Name = name
		file, err := os.ReadFile(filepath.Join(filepath.Dir(path), name, "decide.md"))
		must(t, err)
		return flow, string(file)
	}
	for _, c := range []struct {
		name string
		// flow is the flow and the decide stage file the yard ships for it.
		flow func(t *testing.T) (*store.Flow, string)
		// from is the stage the bead stands in, whose outcome out brings it
		// to decide.
		from, out string
		// moves are the cards' options, including the question's stage when
		// that is an extra move; pick is the one the member answers.
		moves []string
		pick  string
	}{
		{"the yard's default flow", func(t *testing.T) (*store.Flow, string) { return flowsOf(t, "default") },
			"new", "question", []string{"backlog", "new"}, "new"},
		{"the yard's train flow", func(t *testing.T) (*store.Flow, string) { return flowsOf(t, "yardr.train") },
			"review", "question", []string{"open", "backlog", "review"}, "review"},
		{"the design pack's flow", func(t *testing.T) (*store.Flow, string) {
			sub, err := fs.Sub(packs.FS, packs.Design)
			must(t, err)
			p, err := pack.Load(sub, packs.Embedded)
			must(t, err)
			file, err := fs.ReadFile(packs.FS, packs.Design+"/flows/design/decide.md")
			must(t, err)
			f := p.Flows[0].StoreFlow("design.design")
			return &f, string(file)
		}, "critique", "done", []string{"approved", "draft", "backlog"}, "draft"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			flow, decide := c.flow(t)
			home := t.TempDir()
			stages := store.HomeStageDir(home, flow.Name)
			must(t, os.MkdirAll(stages, 0o755))
			must(t, os.WriteFile(filepath.Join(stages, "decide.md"), []byte(decide), 0o644))
			s := storetest.NewIn(t, home)
			d := &dispatch.Dispatcher{Store: s, Runners: map[string]dispatch.Runner{"manual": &fakeRunner{dead: map[string]bool{}}}, Backoff: time.Nanosecond}
			ctx := context.Background()
			must(t, s.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
			for _, u := range []string{"ada", "bob"} {
				_, err := s.AddUser(ctx, u, "jo")
				must(t, err)
			}
			must(t, s.PutDepot(ctx, store.Depot{Name: "web", Path: t.TempDir()}, "me"))
			_, err := s.PutFlow(ctx, *flow, "me")
			must(t, err)
			must(t, s.SetFlowPointer(ctx, "web", "", flow.Name, "me"))
			must(t, s.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Members: []string{"ada"}}, "jo"))
			_, err = s.AddRoute(ctx, store.Route{Stage: "decide", Group: "decisions"}, "jo")
			must(t, err)

			b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Stage: c.from}, "me")
			must(t, err)
			// arrive reports the outcome that brings the bead to decide, and
			// lets the dispatcher pass over it, as the daemon does on the write.
			arrive := func() {
				t.Helper()
				if cur, err := s.GetBead(ctx, b.ID); err != nil || cur.Stage != c.from {
					_, err := s.Advance(ctx, b.ID, c.from, store.Expect{}, "me")
					must(t, err)
				}
				_, err := s.Done(ctx, b.ID, c.out, "", store.Expect{}, "builder")
				must(t, err)
				must(t, d.Reconcile(ctx))
			}
			stage := func() string {
				t.Helper()
				cur, err := s.GetBead(ctx, b.ID)
				must(t, err)
				return cur.Stage
			}

			arrive()
			if got := stage(); got != "decide" {
				t.Fatalf("after %s the bead is in %s, want decide", c.out, got)
			}
			open, all := decideOffers(t, s, b.ID)
			if len(open) != 1 || all != 1 || open[0].Ask.To != "ada" || open[0].Ask.Stage != "decide" {
				t.Fatalf("on arrival: %d open of %d asks (%+v), want one card, for ada, for decide", len(open), all, open)
			}
			var labels []string
			for _, o := range open[0].Ask.Options {
				labels = append(labels, o.Label)
			}
			if want := append(slices.Clone(c.moves), store.YouDecide); !slices.Equal(labels, want) {
				t.Fatalf("the card offers %q, want the stage's moves %q", labels, want)
			}
			pick := slices.Index(labels, c.pick) + 1

			// Passes over a bead nobody has answered change nothing.
			for range 3 {
				must(t, d.Reconcile(ctx))
			}
			if open, all := decideOffers(t, s, b.ID); len(open) != 1 || all != 1 {
				t.Fatalf("after more passes: %d open of %d asks, want the one card", len(open), all)
			}

			// Someone outside the group moves nothing.
			if err := s.AddAnswer(ctx, b.ID, store.Answer{Ask: open[0].Note.Seq, Option: pick}, "bob"); err == nil {
				t.Fatalf("an answer from outside the group was taken")
			}
			must(t, d.Reconcile(ctx))
			if open, all := decideOffers(t, s, b.ID); stage() != "decide" || len(open) != 1 || all != 1 {
				t.Fatalf("after an outsider's answer: bead in %s, %d open of %d asks, want it in decide with the one card", stage(), len(open), all)
			}

			// A member's pick moves it, and the next pass leaves it there.
			must(t, s.AddAnswer(ctx, b.ID, store.Answer{Ask: open[0].Note.Seq, Option: pick, Words: "not yet"}, "ada"))
			must(t, d.Reconcile(ctx))
			if got := stage(); got != c.pick {
				t.Fatalf("after the member picked %s the bead is in %s", c.pick, got)
			}
			if open, all := decideOffers(t, s, b.ID); len(open) != 0 || all != 1 {
				t.Fatalf("after the pick: %d open of %d asks, want the card decided, not standing", len(open), all)
			}

			// Back in decide it is a second arrival: a second card, none doubled.
			arrive()
			for range 3 {
				must(t, d.Reconcile(ctx))
			}
			second, all := decideOffers(t, s, b.ID)
			if got := stage(); got != "decide" || len(second) != 1 || all != 2 || second[0].Note.Seq == open[0].Note.Seq {
				t.Fatalf("second arrival: bead in %s, %d open of %d asks, want a second card, the first decided", got, len(second), all)
			}
		})
	}
}
