package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

// The flows the fold rows follow. foldF has the way a siding takes; foldG,
// which the web depot's beads resolve to, has neither stage, so a carrier in
// ready is governed by foldF or refused. foldRelease is a type's flow.
const (
	foldF = `
backlog -> siding | new
siding -> ready | backlog
ready -> new | backlog
new -> merged
merged terminal
`
	foldG = `
backlog -> new
new -> merged
merged terminal
`
	foldRelease = `
backlog -> ship
ship -> merged
merged terminal
`
)

// foldYard is a store with the depots the rows fold in: plain, which no flow
// governs; web, whose beads follow fold.g unless they pin fold.f; and repo, a
// directory depot whose repository holds a default flow and one for the type
// release. mckean and sam are its users.
func foldYard(t *testing.T) *Store {
	t.Helper()
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "y.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close(ctx) })
	for _, u := range []string{"mckean", "sam"} {
		if _, err := s.AddUser(ctx, u, "mckean"); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	for name, text := range map[string]string{"default.flow": foldF, "type/release.flow": foldRelease} {
		p := filepath.Join(dir, RepoFlowDir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, d := range []Depot{{Name: "plain", Base: "main"}, {Name: "web", Base: "main"}, {Name: "repo", Kind: DepotDir, Path: dir}} {
		if err := s.PutDepot(ctx, d, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for name, text := range map[string]string{"fold.f": foldF, "fold.g": foldG} {
		f, err := ParseFlow(text)
		if err != nil {
			t.Fatal(err)
		}
		f.Name = name
		if _, err := s.PutFlow(ctx, *f, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetFlowPointer(ctx, "web", "", "fold.g", "me"); err != nil {
		t.Fatal(err)
	}
	return s
}

func foldWant[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// foldRow is one fold: its members are made in order, so the first is the
// oldest, and folded as fold says (every one, in the order made, when nil).
type foldRow struct {
	name    string
	depot   string
	members []NewBead
	fold    []int
	extra   []string // ids that name no bead, folded after the members
	opts    FoldOptions
	actor   string // mckean unless set
	prepare func(t *testing.T, s *Store, made []Bead)
	wantErr string // the fold is refused, and makes no bead
	check   func(t *testing.T, s *Store, got *Bead, made []Bead)
}

// sizes makes a bead for each list of labels.
func sizes(labels ...string) []NewBead {
	var out []NewBead
	for i, l := range labels {
		out = append(out, NewBead{Title: "part " + string(rune('a'+i)), Labels: strings.Fields(l)})
	}
	return out
}

// A fold makes one carrier, and what it is made of is the done-when of
// yardr-d1hb: each row is one of its lines. A refused fold makes no bead.
func TestFoldBeads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := foldYard(t)

	bead := func(title string) NewBead { return NewBead{Title: title} }
	pinned := func(title, flow string) NewBead {
		return NewBead{Title: title, Metadata: map[string]any{"flow": flow}}
	}
	// carrierSize is the size: a carrier of beads with these labels gets.
	carrierSize := func(want string, members ...string) foldRow {
		return foldRow{
			name: "size " + want + " from " + strings.Join(members, " + "), depot: "plain", members: sizes(members...), opts: FoldOptions{To: "ready"},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				var size []string
				for _, l := range got.Labels {
					if strings.HasPrefix(l, "size:") {
						size = append(size, l)
					}
				}
				if !slices.Equal(size, []string{"size:" + want}) {
					t.Errorf("sizes = %v, want size:%s (labels %v)", size, want, got.Labels)
				}
			},
		}
	}

	rows := []foldRow{
		{
			name: "its depot is the members'", depot: "plain", members: []NewBead{bead("a"), bead("b")}, opts: FoldOptions{To: "ready"},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				if got.Depot == nil || *got.Depot != "plain" || !strings.HasPrefix(got.ID, "plain-") {
					t.Errorf("depot = %v, id %s; want plain", got.Depot, got.ID)
				}
			},
		},
		{
			name: "members of no depot make a carrier of none", members: []NewBead{bead("a"), bead("b")}, opts: FoldOptions{To: "ready"},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				if got.Depot != nil {
					t.Errorf("depot = %v, want none", *got.Depot)
				}
				foldWant(t, "title", got.Title, "bundle: a; b")
			},
		},
		{
			name: "its type is the members' common type", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", Type: "bug"}, {Title: "b", Type: "bug"}},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "type", got.Type, "bug")
			},
		},
		{
			name: "its type is the oldest member's when they differ, however they are named", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", Type: "task"}, {Title: "b", Type: "bug"}, {Title: "c", Type: "bug"}},
			fold:    []int{2, 1, 0},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "type", got.Type, "task")
			},
		},
		{
			name: "it pins the flow its members are pinned to, though their type resolves to another", depot: "web", opts: FoldOptions{To: "ready"},
			members: []NewBead{pinned("a", "fold.f"), pinned("b", "fold.f")},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "pin", PinnedFlow(got), "fold.f")
				// fold.g has no ready: the carrier is in it because it is in fold.f.
				next, err := s.NextStages(ctx, got.ID)
				if err != nil || next.Flow == nil {
					t.Fatalf("next stages = %+v, %v", next, err)
				}
				foldWant(t, "flow", next.Flow.Name, "fold.f")
				foldWant(t, "stage", got.Stage, "ready")
				if !slices.Equal(next.Stages, []string{"new", "backlog"}) {
					t.Errorf("it may go to %v, want fold.f's new, backlog", next.Stages)
				}
			},
		},
		{
			name: "it pins the flow its members' type resolves to", depot: "web", members: []NewBead{bead("a"), bead("b")}, opts: FoldOptions{To: "new"},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "pin", PinnedFlow(got), "fold.g")
			},
		},
		{
			name: "it pins a flow of the repository by its name", depot: "repo", members: []NewBead{bead("a"), bead("b")}, opts: FoldOptions{To: "ready"},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "pin", PinnedFlow(got), "default")
				next, err := s.NextStages(ctx, got.ID)
				if err != nil || next.Flow == nil {
					t.Fatalf("next stages = %+v, %v", next, err)
				}
				foldWant(t, "flow", next.Flow.Name, "default")
				foldWant(t, "source", next.Source.Kind+" "+next.Source.Path, "pin "+RepoFlowDir+"/default.flow")
			},
		},
		{
			name: "it leaves a type's repository flow to its type, which has no name to pin", depot: "repo", opts: FoldOptions{To: "ship"},
			members: []NewBead{{Title: "a", Type: "release"}, {Title: "b", Type: "release"}},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "pin", PinnedFlow(got), "")
				next, err := s.NextStages(ctx, got.ID)
				if err != nil || next.Flow == nil {
					t.Fatalf("next stages = %+v, %v", next, err)
				}
				foldWant(t, "flow", next.Flow.Name, "type/release")
			},
		},
		{
			name: "its stage is To", depot: "plain", members: []NewBead{bead("a"), bead("b")}, opts: FoldOptions{To: "ready"},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "stage", got.Stage, "ready")
				foldWant(t, "status", got.Status, "open")
			},
		},
		{
			name: "its title is the members' titles", depot: "plain", members: []NewBead{bead("Fix the link"), bead("Add the flag")}, opts: FoldOptions{To: "ready"},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "title", got.Title, "plain bundle: Fix the link; Add the flag")
			},
		},
		{
			name: "its title is cut at the titles that fit, counting the rest", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead(strings.Repeat("a", 30)), bead(strings.Repeat("b", 30)), bead(strings.Repeat("c", 30)), bead(strings.Repeat("d", 30))},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "title", got.Title, "plain bundle: "+strings.Repeat("a", 30)+"; "+strings.Repeat("b", 30)+" (+2 more)")
			},
		},
		{
			name: "Title replaces its title", depot: "plain", members: []NewBead{bead("a"), bead("b")},
			opts: FoldOptions{To: "ready", Title: strings.Repeat("a long title of the person's own. ", 5)},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "title", got.Title, strings.Repeat("a long title of the person's own. ", 5))
			},
		},
		{
			name: "its priority is the highest member's", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", Priority: 3}, {Title: "b", Priority: 1}, {Title: "c", Priority: 2}},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "priority", got.Priority, 1)
			},
		},
		{
			name: "its priority is 0 when a member's is, the highest", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", Priority: 3}, {Title: "b", Priority: 0}, {Title: "c", Priority: 2}},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "priority", got.Priority, 0)
			},
		},
		{
			name: "its originator is the oldest member's, not whoever folds", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", For: "sam"}, bead("b"), bead("c")},
			fold:    []int{2, 0, 1},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "originator", got.Originator, "sam")
			},
		},
		{
			name: "its originator is the oldest member's when the folder is another's", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), {Title: "b", For: "sam"}}, actor: "sam",
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "originator", got.Originator, "mckean")
			},
		},
		{
			name: "its originator is empty when the oldest member's is", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				err := s.write(func(w *wtx) error {
					row, err := w.loadBead(made[0].ID)
					if err != nil {
						return err
					}
					row.Originator = ""
					return w.mintBead(row, "test")
				})
				if err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "originator", got.Originator, "")
			},
		},
		{
			name: "its labels are the union but the estimates, and its estimate is the highest difficulty and the points' size", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{
				{Title: "a", Labels: []string{"size:small", "difficulty:1", "eyes"}},
				{Title: "b", Labels: []string{"size:small", "difficulty:2", "shaped"}},
				{Title: "c", Labels: []string{"size:medium", "difficulty:1", "eyes", "topic:x"}},
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				if want := []string{"difficulty:2", "eyes", "shaped", "size:medium", "topic:x"}; !slices.Equal(got.Labels, want) {
					t.Errorf("labels = %v, want %v", got.Labels, want)
				}
			},
		},
		{
			name: "its labels hold no difficulty when no member has one", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", Labels: []string{"eyes"}}, bead("b")},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				if want := []string{"eyes", "size:medium"}; !slices.Equal(got.Labels, want) {
					t.Errorf("labels = %v, want %v", got.Labels, want)
				}
			},
		},
		carrierSize("small", "size:small"),
		carrierSize("medium", "size:small", "size:small"),
		carrierSize("medium", "size:small", "eyes"),
		carrierSize("medium", "size:large"),
		carrierSize("large", "size:large", "size:small"),
		carrierSize("large", "size:medium", "size:medium", "size:medium"),
		carrierSize("medium", "size:small", "size:huge"),
		{
			name: "its body is a part for each member, verbatim, and the bundle", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{
				{ID: "plain-p1", Title: "Fix the link", Body: "Goal: fix it.\n\n## Done when\n\n    go test ./...\n"},
				{ID: "plain-p2", Title: "Add the flag"},
				{ID: "plain-p3", Title: "Rename it", Body: "Goal: rename."},
			},
			fold: []int{2, 0, 1},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				want := "Goal: the parts below ride one bead: one builder session and one review for all of them.\n\n" +
					"## Part 1: Rename it (plain-p3)\n\nGoal: rename.\n\n" +
					"## Part 2: Fix the link (plain-p1)\n\nGoal: fix it.\n\n## Done when\n\n    go test ./...\n\n\n" +
					"## Part 3: Add the flag (plain-p2)\n\n" +
					"## The bundle\n\n" +
					"Done when every part's done-when holds and the depot's test command is\n" +
					"green. Each part is its own commit, its message starting with the part's\n" +
					"bead id, so one part can be taken out alone (`yardr bead unfold`).\n" +
					"Folded from plain-p3, plain-p1, plain-p2 by mckean (hand)."
				foldWant(t, "body", got.Body, want)
			},
		},
		{name: "a fold with no stage is refused", depot: "plain", members: []NewBead{bead("a"), bead("b")}, wantErr: "the stage its carrier starts in"},
		{
			name: "a stage the carrier's flow lacks is refused", depot: "web", opts: FoldOptions{To: "ship"},
			members: []NewBead{pinned("a", "fold.f"), pinned("b", "fold.f")}, wantErr: "stage ship is not in flow fold.f",
		},
		{
			name: "a bead named twice is refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")}, fold: []int{0, 1, 0}, wantErr: "named twice",
		},
		{
			name: "a bead that does not exist is refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a")}, extra: []string{"plain-none"}, wantErr: "plain-none",
		},
		{name: "no bead is refused", depot: "plain", opts: FoldOptions{To: "ready"}, wantErr: "needs beads"},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			made := make([]Bead, len(row.members))
			for i, nb := range row.members {
				nb.Depot = row.depot
				b, err := s.CreateBead(ctx, nb, "mckean")
				if err != nil {
					t.Fatal(err)
				}
				made[i] = *b
			}
			if row.prepare != nil {
				row.prepare(t, s, made)
			}
			var ids []string
			for _, i := range foldOrder(row.fold, len(made)) {
				ids = append(ids, made[i].ID)
			}
			ids = append(ids, row.extra...)
			count := func() int {
				all, err := s.ListBeads(ctx, BeadFilter{All: true})
				if err != nil {
					t.Fatal(err)
				}
				return len(all)
			}
			before := count()
			got, err := s.foldBeads(ctx, ids, row.opts, or(row.actor, "mckean"))
			if row.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), row.wantErr) {
					t.Fatalf("folded %v: %+v, %v; want an error with %q", ids, got, err, row.wantErr)
				}
				foldWant(t, "beads after a refused fold", count(), before)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			foldWant(t, "beads made", count()-before, 1)
			// What the fold answers is what the store holds.
			stored, err := s.GetBead(ctx, got.ID)
			if err != nil || stored.Revision != got.Revision {
				t.Fatalf("the carrier answered is at %v, the bead stored %+v, %v", got.Revision, stored, err)
			}
			row.check(t, s, stored, made)
		})
	}
}

// foldOrder is the indexes of a row's fold: the ones it names, else 0 to n-1.
func foldOrder(fold []int, n int) []int {
	if fold != nil {
		return fold
	}
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// A title that passes 100 characters keeps the titles that fit whole, the
// count of the rest in the 100; only a first title that is too long on its
// own is cut, on a word.
func TestFoldTitle(t *testing.T) {
	t.Parallel()
	yardr := "yardr"
	x := func(n int, c string) string { return strings.Repeat(c, n) }
	for _, c := range []struct {
		name   string
		depot  *string
		titles []string
		want   string
	}{
		{"all of them", &yardr, []string{"a", "b"}, "yardr bundle: a; b"},
		{"no depot", nil, []string{"a", "b"}, "bundle: a; b"},
		{"exactly 100", &yardr, []string{x(42, "a"), x(42, "b")}, "yardr bundle: " + x(42, "a") + "; " + x(42, "b")},
		{"101", &yardr, []string{x(42, "a"), x(43, "b")}, "yardr bundle: " + x(42, "a") + " (+1 more)"},
		{"the last that fits, to 100 with the count", &yardr, []string{x(30, "a"), x(44, "b"), x(30, "c"), x(30, "d")},
			"yardr bundle: " + x(30, "a") + "; " + x(44, "b") + " (+2 more)"},
		{"one more than that", &yardr, []string{x(30, "a"), x(45, "b"), x(30, "c"), x(30, "d")},
			"yardr bundle: " + x(30, "a") + " (+3 more)"},
		{"a count of two digits", &yardr, []string{x(30, "a"), x(30, "b"), x(30, "c"), x(30, "d"), x(30, "e"), x(30, "f"), x(30, "g"), x(30, "h"), x(30, "i"), x(30, "j"), x(30, "k"), x(30, "l")},
			"yardr bundle: " + x(30, "a") + "; " + x(30, "b") + " (+10 more)"},
		{"characters, not bytes", &yardr, []string{x(20, "é"), x(20, "é"), x(20, "é"), x(20, "é"), x(20, "é")},
			"yardr bundle: " + x(20, "é") + "; " + x(20, "é") + "; " + x(20, "é") + " (+2 more)"},
		{"a first title too long, cut on a word", &yardr, []string{strings.TrimSpace(x(30, "word ")), "b"},
			"yardr bundle: " + strings.TrimSpace(x(15, "word ")) + "… (+1 more)"},
		{"one title, too long", &yardr, []string{strings.TrimSpace(x(30, "word "))},
			"yardr bundle: " + strings.TrimSpace(x(17, "word ")) + "…"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := foldTitle(c.depot, c.titles)
			if got != c.want {
				t.Errorf("foldTitle = %q, want %q", got, c.want)
			}
			if n := utf8.RuneCountInString(got); n > foldTitleMax {
				t.Errorf("foldTitle is %d characters, past %d", n, foldTitleMax)
			}
		})
	}
}
