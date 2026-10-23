package cli

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// foldFlow is the flow the beads of these tests follow: a siding that leads
// on to ready before new, as the siding design's flows do.
const foldFlow = `
backlog -> siding | new
siding -> ready | backlog
ready -> new | backlog
new -> merged
merged terminal
`

// sidingFolds is a store whose siding group sets fold, the key that makes a
// manual group's stage a siding. No group can set it until the config keys
// accept it (yardr-tywp), so the test presents the group as it will be once
// they do, as oldServer presents an older server.
type sidingFolds struct{ store.API }

func (s sidingFolds) Groups(ctx context.Context) ([]store.Group, error) {
	groups, err := s.API.Groups(ctx)
	for i := range groups {
		if groups[i].Name == "siding" {
			groups[i].Config = map[string]any{"fold": 4}
		}
	}
	return groups, err
}

// noEvents is a store whose events cannot be read, so that the points of a
// fold, which the store writes on the carrier's event, are not to be had.
type noEvents struct{ store.API }

func (noEvents) Events(context.Context, string, int) ([]store.Event, error) {
	return nil, errors.New("events are gone")
}

// foldYard is a store with the depot yd, whose beads follow foldFlow, and a
// manual group working its siding stage. newBead makes a bead of it in the
// stage and with the labels given: yd-1 and on, titled "part <id>".
func foldYard(t *testing.T) (store.API, func(stage string, labels ...string) string) {
	t.Helper()
	ctx := context.Background()
	st := storetest.New(t)
	if err := st.PutDepot(ctx, store.Depot{Name: "yd", Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	f, err := store.ParseFlow(foldFlow)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "fold.f"
	if _, err := st.PutFlow(ctx, *f, "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFlowPointer(ctx, "yd", "", "fold.f", "me"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutGroup(ctx, store.Group{Name: "siding", Runner: "manual", Members: []string{"me"}}, "me"); err != nil {
		t.Fatal(err)
	}
	depot := "yd"
	if _, err := st.AddRoute(ctx, store.Route{Stage: "siding", Depot: &depot, Group: "siding"}, "me"); err != nil {
		t.Fatal(err)
	}
	n := 0
	return st, func(stage string, labels ...string) string {
		t.Helper()
		n++
		id := "yd-" + strconv.Itoa(n)
		b, err := st.CreateBead(ctx, store.NewBead{
			ID: id, Title: "part " + id, Body: "body of " + id, Depot: "yd", Stage: stage, Labels: labels,
		}, "me")
		if err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
}

// bead fold makes one carrier of the beads it names and says so in a line;
// the stage it starts in is --to, else where the beads wait: the stage they
// share, or the stage a siding leads to first. It asks for --to where the
// beads share no stage, and folds nothing it cannot take.
func TestBeadFold(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, newBead := foldYard(t)
	plain := &app{st: st, actor: "me"}
	sided := &app{st: sidingFolds{st}, actor: "me"}

	// The carrier line: its id is not known beforehand.
	carrierOf := func(t *testing.T, out string) string {
		t.Helper()
		id, _, ok := strings.Cut(strings.TrimSpace(out), " folded from ")
		if !ok || strings.ContainsAny(id, " \n") {
			t.Fatalf("the line of a fold is %q, want <carrier> folded from …", out)
		}
		return id
	}

	rows := []struct {
		name   string
		app    *app
		stages []string                  // where the beads wait
		args   []string                  // beside their ids
		want   string                    // the carrier's stage
		err    func(ids []string) string // the fold is refused, with this error
	}{
		{name: "--to names the stage", app: plain, stages: []string{"backlog", "backlog"}, args: []string{"--to", "ready"}, want: "ready"},
		{name: "beads that share a stage start the carrier in it", app: plain, stages: []string{"backlog", "backlog"}, want: "backlog"},
		{name: "beads in a siding start the carrier where it leads first", app: sided, stages: []string{"siding", "siding"}, want: "ready"},
		{name: "--to wins over a siding", app: sided, stages: []string{"siding", "siding"}, args: []string{"--to", "new"}, want: "new"},
		{name: "a stage whose group sets no fold is no siding", app: plain, stages: []string{"siding", "siding"}, want: "siding"},
		{name: "beads in a siding and beads outside it share no stage", app: sided, stages: []string{"backlog", "siding", "backlog"},
			err: func(ids []string) string {
				return "these beads wait in different stages (backlog: " + ids[0] + ", " + ids[2] + "; siding: " + ids[1] + "): say where the carrier starts with --to <stage>"
			}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			ids := make([]string, len(r.stages))
			for i, stage := range r.stages {
				ids[i] = newBead(stage)
			}
			out, err := run(t, r.app.beadCmd(), append(append([]string{"fold"}, ids...), r.args...)...)
			if r.err != nil {
				if want := r.err(ids); err == nil || err.Error() != want {
					t.Fatalf("fold = %q, %v; want the error %q", out, err, want)
				}
				// Nothing was made or closed.
				for _, id := range ids {
					if b, _ := st.GetBead(ctx, id); b == nil || b.Status != "open" {
						t.Errorf("%s after the refusal: %+v", id, b)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("fold: %v", err)
			}
			carrier, err := st.GetBead(ctx, carrierOf(t, out))
			if err != nil {
				t.Fatal(err)
			}
			if carrier.Stage != r.want {
				t.Errorf("the carrier starts in %s, want %s", carrier.Stage, r.want)
			}
		})
	}

	t.Run("the line says what the carrier is made of and weighs", func(t *testing.T) {
		a, b, c := newBead("backlog", "size:small", "difficulty:1"), newBead("backlog", "size:small"), newBead("backlog", "size:small", "difficulty:1")
		out, err := run(t, plain.beadCmd(), "fold", c, a, b, "--to", "ready")
		if err != nil {
			t.Fatal(err)
		}
		// In the order typed, with the points the store counted and the
		// estimate the carrier got.
		want := carrierOf(t, out) + " folded from " + c + ", " + a + ", " + b + " (3 points, size:medium difficulty:1), in ready\n"
		if out != want {
			t.Fatalf("the line of a fold:\n%q\nwant\n%q", out, want)
		}
	})

	t.Run("the line has no difficulty the beads did not have", func(t *testing.T) {
		a, b := newBead("backlog", "size:large"), newBead("backlog", "size:medium")
		out, err := run(t, plain.beadCmd(), "fold", a, b, "--to", "ready")
		if err != nil {
			t.Fatal(err)
		}
		if want := carrierOf(t, out) + " folded from " + a + ", " + b + " (6 points, size:large), in ready\n"; out != want {
			t.Fatalf("the line of a fold:\n%q\nwant\n%q", out, want)
		}
	})

	t.Run("--title is the carrier's title", func(t *testing.T) {
		a, b := newBead("backlog"), newBead("backlog")
		out, err := run(t, plain.beadCmd(), "fold", a, b, "--title", "the pair")
		if err != nil {
			t.Fatal(err)
		}
		carrier, err := st.GetBead(ctx, carrierOf(t, out))
		if err != nil {
			t.Fatal(err)
		}
		if carrier.Title != "the pair" {
			t.Errorf("title = %q", carrier.Title)
		}
		// Without it the store's: the depot, then the titles.
		x, y := newBead("backlog"), newBead("backlog")
		out, err = run(t, plain.beadCmd(), "fold", x, y)
		if err != nil {
			t.Fatal(err)
		}
		if carrier, err = st.GetBead(ctx, carrierOf(t, out)); err != nil || !strings.HasPrefix(carrier.Title, "yd bundle: part ") {
			t.Errorf("default title = %v, %v", carrier, err)
		}
	})

	t.Run("--json is the carrier, its members and their points", func(t *testing.T) {
		a, b := newBead("backlog", "size:small"), newBead("backlog", "size:medium")
		j := &app{st: st, actor: "me", json: true}
		out, err := run(t, j.beadCmd(), "fold", b, a, "--to", "ready")
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			ID      string
			Stage   string
			Labels  []string
			Members []string
			Points  int
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v\n%s", err, out)
		}
		if !strings.HasPrefix(got.ID, "yd-") || got.Stage != "ready" || !slices.Equal(got.Members, []string{b, a}) || got.Points != 3 ||
			!slices.Contains(got.Labels, "size:medium") {
			t.Fatalf("fold --json:\n%s", out)
		}
	})

	t.Run("a bead typed by its uuid is named by its handle", func(t *testing.T) {
		a, b := newBead("backlog"), newBead("backlog")
		ba, err := st.GetBead(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		out, err := run(t, plain.beadCmd(), "fold", ba.UUID, b, "--to", "ready")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, " folded from "+a+", "+b+" ") {
			t.Fatalf("the line of a fold: %q", out)
		}
	})

	t.Run("one bead is no fold", func(t *testing.T) {
		a := newBead("backlog")
		if out, err := run(t, plain.beadCmd(), "fold", a, "--to", "ready"); err == nil || !strings.Contains(err.Error(), "a fold takes two beads or more") {
			t.Fatalf("fold of one bead = %q, %v", out, err)
		}
		if b, _ := st.GetBead(ctx, a); b == nil || b.Status != "open" {
			t.Errorf("%s after the refusal: %+v", a, b)
		}
	})

	t.Run("a bead the yard does not know folds nothing", func(t *testing.T) {
		a := newBead("backlog")
		before, err := st.ListBeads(ctx, store.BeadFilter{All: true})
		if err != nil {
			t.Fatal(err)
		}
		if out, err := run(t, plain.beadCmd(), "fold", a, "yd-nope", "--to", "ready"); err == nil {
			t.Fatalf("fold of an unknown bead = %q", out)
		}
		after, err := st.ListBeads(ctx, store.BeadFilter{All: true})
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := st.GetBead(ctx, a); b == nil || b.Status != "open" || len(after) != len(before) {
			t.Errorf("after the refusal: %+v, %d beads, were %d", b, len(after), len(before))
		}
	})

	t.Run("a fold whose points cannot be read says what it made", func(t *testing.T) {
		a, b := newBead("backlog"), newBead("backlog")
		unread := &app{st: noEvents{st}, actor: "me"}
		out, err := run(t, unread.beadCmd(), "fold", a, b, "--to", "ready")
		// The fold went through: the error names the beads, which are closed,
		// and the carrier, which is in the store.
		if err == nil || !strings.Contains(err.Error(), " folded from "+a+", "+b+", but its points could not be read: events are gone") {
			t.Fatalf("fold = %q, %v", out, err)
		}
		carrier, _, _ := strings.Cut(err.Error(), " folded from ")
		if cb, err := st.GetBead(ctx, carrier); err != nil || cb.Stage != "ready" {
			t.Errorf("the carrier named by the error: %+v, %v", cb, err)
		}
		if ba, _ := st.GetBead(ctx, a); ba == nil || ba.Status != "closed" {
			t.Errorf("%s after the fold: %+v", a, ba)
		}
	})

	t.Run("a member the route sends elsewhere makes the stage no siding", func(t *testing.T) {
		depot, label := "yd", "elsewhere"
		if err := st.PutGroup(ctx, store.Group{Name: "other", Runner: "manual", Members: []string{"me"}}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddRoute(ctx, store.Route{Stage: "siding", Depot: &depot, Label: &label, Group: "other"}, "me"); err != nil {
			t.Fatal(err)
		}
		a, b := newBead("siding"), newBead("siding", "elsewhere")
		out, err := run(t, sided.beadCmd(), "fold", a, b)
		if err != nil {
			t.Fatal(err)
		}
		if carrier, err := st.GetBead(ctx, carrierOf(t, out)); err != nil || carrier.Stage != "siding" {
			t.Errorf("the carrier of a siding bead and another: %+v, %v; want it in siding", carrier, err)
		}
	})

	t.Run("a siding no stage follows asks for --to", func(t *testing.T) {
		// A depot that follows no flow: no stage follows siding.
		depot := "yd-free"
		if err := st.PutDepot(ctx, store.Depot{Name: depot, Base: "main"}, "me"); err != nil {
			t.Fatal(err)
		}
		if _, err := st.AddRoute(ctx, store.Route{Stage: "siding", Depot: &depot, Group: "siding"}, "me"); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, id := range []string{"yd-free-1", "yd-free-2"} {
			if _, err := st.CreateBead(ctx, store.NewBead{ID: id, Title: id, Depot: depot, Stage: "siding"}, "me"); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, id)
		}
		out, err := run(t, sided.beadCmd(), "fold", ids[0], ids[1])
		if want := "stage siding is a siding, but no stage follows it: say where the carrier starts with --to <stage>"; err == nil || err.Error() != want {
			t.Fatalf("fold = %q, %v; want the error %q", out, err, want)
		}
	})

	t.Run("what the store refuses is told as it is", func(t *testing.T) {
		a, b := newBead("backlog"), newBead("backlog")
		if _, err := st.CloseBead(ctx, b, "done", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		if out, err := run(t, plain.beadCmd(), "fold", a, b, "--to", "ready"); err == nil || !strings.Contains(err.Error(), "bead "+b+" is closed") {
			t.Fatalf("fold of a closed bead = %q, %v", out, err)
		}
	})
}

// bead show says where a bead stands in a fold, in text and in JSON: the
// carrier a bead was folded into, and the beads folded into a carrier.
func TestBeadShowFolded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st, newBead := foldYard(t)
	a := &app{st: st, actor: "me"}

	m1, m2, loose := newBead("backlog"), newBead("backlog"), newBead("backlog")
	// A bead found from a member keeps its edge to it through the fold, and
	// that edge is no fold.
	found, err := st.CreateBead(ctx, store.NewBead{ID: "yd-found", Title: "a loose end", Depot: "yd", DiscoveredFrom: m2}, "me")
	if err != nil {
		t.Fatal(err)
	}
	out, err := run(t, a.beadCmd(), "fold", m1, m2, "--to", "ready")
	if err != nil {
		t.Fatal(err)
	}
	carrier, _, _ := strings.Cut(out, " folded from ")
	cb, err := st.GetBead(ctx, carrier)
	if err != nil {
		t.Fatal(err)
	}

	show := func(id string) string {
		t.Helper()
		out, err := run(t, a.beadCmd(), "show", id)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// The member says where it went, with the carrier's title.
	if want := "  folded into " + carrier + " " + cb.Title + "\n"; !strings.Contains(show(m1), want) {
		t.Errorf("bead show of a member lacks %q:\n%s", want, show(m1))
	}
	// The carrier says what it is made of, in the order of its parts, each
	// closed.
	want := "  folded from " + m1 + " part " + m1 + " (closed)\n  folded from " + m2 + " part " + m2 + " (closed)\n"
	if shown := show(carrier); !strings.Contains(shown, want) || strings.Contains(shown, "folded into") {
		t.Errorf("bead show of the carrier lacks %q:\n%s", want, shown)
	}
	if shown := show(loose); strings.Contains(shown, "folded") {
		t.Errorf("bead show of a bead in no fold:\n%s", shown)
	}
	// The bead found from a member is in no fold, and the member it was found
	// from says it led to it and folded nothing from it.
	if shown := show(found.ID); strings.Contains(shown, "folded") {
		t.Errorf("bead show of a bead found from a member:\n%s", shown)
	}
	if shown := show(m2); !strings.Contains(shown, "  led to: "+found.ID+" ") || strings.Contains(shown, "folded from") {
		t.Errorf("bead show of a member with a bead found from it:\n%s", shown)
	}
	// A carrier that landed is marked on the member's line.
	if _, err := st.CloseBead(ctx, carrier, "landed", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if want := "  folded into " + carrier + " " + cb.Title + " (closed)\n"; !strings.Contains(show(m1), want) {
		t.Errorf("bead show of a member of a closed carrier lacks %q:\n%s", want, show(m1))
	}

	// A member reopened and folded again was folded into both carriers, and
	// show names each, the older first.
	if _, err := st.Reopen(ctx, m1, store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	other := newBead("backlog")
	out, err = run(t, a.beadCmd(), "fold", m1, other, "--to", "ready")
	if err != nil {
		t.Fatal(err)
	}
	second, _, _ := strings.Cut(out, " folded from ")
	shown := show(m1)
	first, next := strings.Index(shown, "  folded into "+carrier+" "), strings.Index(shown, "  folded into "+second+" ")
	if first < 0 || next < first {
		t.Errorf("bead show of a member folded twice:\n%s", shown)
	}

	// --json holds both lists, there and empty when there is nothing to list.
	a.json = true
	type folds struct {
		FoldedInto []store.BeadRef `json:"folded_into"`
		FoldedFrom []store.BeadRef `json:"folded_from"`
	}
	read := func(id string) folds {
		t.Helper()
		var f folds
		raw := show(id)
		if err := json.Unmarshal([]byte(raw), &f); err != nil || f.FoldedInto == nil || f.FoldedFrom == nil {
			t.Fatalf("bead show --json %s: %v\n%s", id, err, raw)
		}
		return f
	}
	if f := read(m2); !slices.Equal(f.FoldedInto, []store.BeadRef{{ID: carrier, Title: cb.Title, Status: "closed"}}) || len(f.FoldedFrom) != 0 {
		t.Errorf("a member as JSON: %+v", f)
	}
	if f := read(carrier); len(f.FoldedInto) != 0 || !slices.Equal(f.FoldedFrom, []store.BeadRef{
		{ID: m1, Title: "part " + m1, Status: "closed"}, {ID: m2, Title: "part " + m2, Status: "closed"}}) {
		t.Errorf("a carrier as JSON: %+v", f)
	}
	if f := read(loose); len(f.FoldedInto) != 0 || len(f.FoldedFrom) != 0 {
		t.Errorf("a bead in no fold as JSON: %+v", f)
	}
}
