package store

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"yardr.dev/yardr/internal/peer"
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
// governs; web, whose beads follow fold.g unless they pin fold.f; repo, a
// directory depot whose repository holds a default flow and one for the type
// release; and mixed, the same repository with its beads pointed at fold.g,
// so that only a bead pinned to the repository's default needs it. mckean and
// sam are its users, and builders a group that can hold ten sessions.
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
	for _, d := range []Depot{{Name: "plain", Base: "main"}, {Name: "web", Base: "main"}, {Name: "repo", Kind: DepotDir, Path: dir}, {Name: "mixed", Kind: DepotDir, Path: dir}} {
		if err := s.PutDepot(ctx, d, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutGroup(ctx, Group{Name: "builders", Runner: "herdr", Limit: 10}, "me"); err != nil {
		t.Fatal(err)
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
	for _, depot := range []string{"web", "mixed"} {
		if err := s.SetFlowPointer(ctx, depot, "", "fold.g", "me"); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func foldWant[T comparable](t *testing.T, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// foldClaim has a session of the builders group claim b, as the daemon does.
func foldClaim(t *testing.T, s *Store, b Bead) *Session {
	t.Helper()
	sess, err := s.Claim(context.Background(), b.ID, "builders", b.Stage, "yard")
	if err != nil {
		t.Fatal(err)
	}
	return sess
}

// foldPane makes this test process the pane of a session of b, as MarkRunning
// takes a herdr tab's shell: a call made with WithCaller(ctx, os.Getpid()) is
// then that of b's agent.
func foldPane(t *testing.T, s *Store, b Bead) {
	t.Helper()
	sess := foldClaim(t, s, b)
	me := os.Getpid()
	handle := map[string]any{"agent": b.ID + "-new", "shell_pid": me}
	if st, err := peer.Describe(me); err == nil {
		handle["shell_start"] = st.Start
	}
	if err := s.MarkRunning(context.Background(), sess.ID, handle); err != nil {
		t.Fatal(err)
	}
}

// foldRow is one fold: its members are made in order, so the first is the
// oldest, and folded as fold says (every one, in the order made, when nil).
type foldRow struct {
	name    string
	depot   string // the depot of every member that names none
	members []NewBead
	fold    []int
	extra   []string // ids that name no bead, folded after the members
	opts    FoldOptions
	actor   string // mckean unless set
	pane    bool   // the fold is asked from the pane prepare made of this process
	prepare func(t *testing.T, s *Store, made []Bead)
	wantErr string // the fold is refused, and changes nothing
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
			name: "it follows its repository default by depot and type", depot: "repo", members: []NewBead{bead("a"), bead("b")}, opts: FoldOptions{To: "ready"},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "pin", PinnedFlow(got), "")
				next, err := s.NextStages(ctx, got.ID)
				if err != nil || next.Flow == nil {
					t.Fatalf("next stages = %+v, %v", next, err)
				}
				foldWant(t, "flow", next.Flow.Name, "default")
				foldWant(t, "source", next.Source.Kind+" "+next.Source.Path, "repo "+RepoFlowDir+"/default.flow")
			},
		},
		{
			name: "same-named store and repository flows are distinct", depot: "repo", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "repo-f10", Title: "store", Type: "task"}, {ID: "repo-f11", Title: "repository", Type: "bug"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				f, err := ParseFlow("backlog -> siding | new | decide\nsiding -> ready | backlog\nready -> merged\nnew -> merged\ndecide -> backlog\nmerged terminal\n")
				if err != nil {
					t.Fatal(err)
				}
				f.Name = "default"
				if _, err := s.PutFlow(ctx, *f, "mckean"); err != nil {
					t.Fatal(err)
				}
				if err := s.SetFlowPointer(ctx, "repo", "task", "default", "mckean"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := s.SetFlowPointer(ctx, "repo", "task", "", "mckean"); err != nil {
						t.Errorf("clear task flow pointer: %v", err)
					}
				})
			},
			wantErr: "repo-f10 under flow default (store), repo-f11 under flow default (" + RepoFlowDir + "/default.flow)",
		},
		{
			name: "a repository default stays the carrier flow beside a same-named store flow", depot: "repo", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", Type: "task"}, {Title: "b", Type: "task"}},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "pin", PinnedFlow(got), "")
				next, err := s.NextStages(ctx, got.ID)
				if err != nil || next.Flow == nil {
					t.Fatalf("next stages = %+v, %v", next, err)
				}
				foldWant(t, "source", next.Source.Kind+" "+next.Source.Path, "repo "+RepoFlowDir+"/default.flow")
				if !slices.Equal(next.Stages, []string{"new", "backlog"}) {
					t.Errorf("next stages = %v, want repository default's new, backlog", next.Stages)
				}
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
		{
			name: "each member has a folded edge from the carrier, with the stage it stood in", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", Stage: "siding"}, {Title: "b", Stage: "backlog"}, {Title: "c", Stage: "siding"}},
			fold:    []int{2, 0, 1},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				deps, err := s.Deps(ctx, got.ID)
				if err != nil {
					t.Fatal(err)
				}
				moved := map[string]string{}
				for _, d := range deps {
					switch {
					case d.Kind != DepFolded:
						t.Errorf("the carrier has an edge of kind %s: %+v", d.Kind, d)
					case d.From != got.ID:
						t.Errorf("a folded edge runs %s -> %s, want it from the carrier %s", d.From, d.To, got.ID)
					case d.Moved == nil:
						t.Errorf("the folded edge to %s says nothing was moved", d.To)
					default:
						moved[d.To] = d.Moved.Stage
					}
				}
				want := map[string]string{made[0].ID: "siding", made[1].ID: "backlog", made[2].ID: "siding"}
				if !maps.Equal(moved, want) {
					t.Errorf("folded edges moved %v, want %v", moved, want)
				}
				// The member reads the same edge from its end.
				for _, m := range made {
					deps, err := s.Deps(ctx, m.ID)
					if err != nil || len(deps) != 1 || deps[0].Kind != DepFolded || deps[0].From != got.ID || deps[0].To != m.ID {
						t.Errorf("%s has edges %+v, %v; want the one folded edge from %s", m.ID, deps, err, got.ID)
					}
				}
				if err := s.RemoveDep(ctx, got.ID, made[0].ID, DepFolded, "", "", "mckean"); err == nil || !strings.Contains(err.Error(), "only a fold or an unfold") {
					t.Errorf("RemoveDep of folded membership = %v, want a refusal", err)
				}
				deps, err = s.Deps(ctx, made[0].ID)
				if err != nil || len(deps) != 1 || deps[0].Kind != DepFolded {
					t.Errorf("folded membership after refused removal = %+v, %v; want it to remain", deps, err)
				}
			},
		},
		{
			name: "each member is closed as folded into the carrier", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b"), bead("c")},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				for _, m := range made {
					b, err := s.GetBead(ctx, m.ID)
					if err != nil {
						t.Fatal(err)
					}
					reason := ""
					if b.CloseReason != nil {
						reason = *b.CloseReason
					}
					foldWant(t, m.ID+" status", b.Status, "closed")
					foldWant(t, m.ID+" close reason", reason, "folded into "+got.ID)
				}
			},
		},
		{
			name: "one folded event is logged on the carrier, with the members it took and its points", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{Title: "a", Labels: []string{"size:small"}}, {Title: "b", Labels: []string{"size:large"}}},
			fold:    []int{1, 0},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				evs, err := s.Events(ctx, got.ID, 50)
				if err != nil {
					t.Fatal(err)
				}
				var folded []Event
				for _, e := range evs {
					if e.Kind == "folded" {
						folded = append(folded, e)
					}
				}
				if len(folded) != 1 {
					t.Fatalf("%d folded events on the carrier, want one: %+v", len(folded), evs)
				}
				foldWant(t, "actor", folded[0].Actor, "mckean")
				want := map[string]any{"members": []any{made[1].ID, made[0].ID}, "left": []any{}, "from": "-", "to": "ready", "trigger": "hand", "points": float64(5)}
				if !reflect.DeepEqual(folded[0].Data, want) {
					t.Errorf("folded event data = %v, want %v", folded[0].Data, want)
				}
			},
		},
		{
			name: "a pin to the flow the type resolves to is the same flow", depot: "web", opts: FoldOptions{To: "new"},
			members: []NewBead{bead("a"), pinned("b", "fold.g")},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "pin", PinnedFlow(got), "fold.g")
			},
		},
		{
			name: "a bead that went back to the yard it came from is folded", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				at := time.Now()
				err := s.write(func(w *wtx) error {
					return put(w.Tx, bPeerBeads, made[1].UUID, &peerBead{Bead: made[1].UUID, Key: "north", Direction: "in", ReturnedAt: &at})
				})
				if err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "folded", got.Stage, "ready")
			},
		},
		{
			name: "members of two depots are refused, each named with its depot", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-d1", Title: "a", Depot: "plain"}, {ID: "web-d2", Title: "b", Depot: "web"}},
			wantErr: "plain-d1 in depot plain, web-d2 in depot web",
		},
		{
			name: "one bead of another depot among others is named with them", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-d7", Title: "a", Depot: "plain"}, {ID: "web-d8", Title: "b", Depot: "web"}, {ID: "plain-d9", Title: "c", Depot: "plain"}},
			wantErr: "plain-d7 in depot plain, web-d8 in depot web, plain-d9 in depot plain",
		},
		{
			name: "a bead of a depot and one of none are refused", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-d3", Title: "a", Depot: "plain"}, {ID: "none-d4", Title: "b"}},
			wantErr: "plain-d3 in depot plain, none-d4 in no depot",
		},
		{
			name: "members under two flows are refused, each named with its flow", depot: "web", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "web-f1", Title: "a"}, {ID: "web-f2", Title: "b", Metadata: map[string]any{"flow": "fold.f"}}},
			wantErr: "web-f1 under flow fold.g, web-f2 under flow fold.f",
		},
		{
			name: "one bead under another flow among others is named with them", depot: "web", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "web-f9", Title: "a"}, {ID: "web-f10", Title: "b", Metadata: map[string]any{"flow": "fold.f"}}, {ID: "web-f11", Title: "c"}},
			wantErr: "web-f9 under flow fold.g, web-f10 under flow fold.f, web-f11 under flow fold.g",
		},
		{
			// The oldest's flow is in the store, so reading its repository was
			// not needed; the other's is, and the refusal still names it.
			name: "a store flow and a repository flow are two, though the oldest's needs no repository", depot: "mixed", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "mixed-f3", Title: "a"}, {ID: "mixed-f4", Title: "b", Metadata: map[string]any{"flow": "default"}}},
			wantErr: "mixed-f3 under flow fold.g, mixed-f4 under flow default",
		},
		{
			name: "a type's repository flow and the default are two", depot: "repo", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "repo-f5", Title: "a"}, {ID: "repo-f6", Title: "b", Type: "release"}},
			wantErr: "repo-f5 under flow default, repo-f6 under flow type/release",
		},
		{
			name: "a bead no flow governs and one a flow does are two", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-f7", Title: "a"}, {ID: "plain-f8", Title: "b", Metadata: map[string]any{"flow": "fold.f"}}},
			wantErr: "plain-f7 under no flow, plain-f8 under flow fold.f",
		},
		{
			name: "a closed member is refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				if _, err := s.CloseBead(ctx, made[1].ID, "done", Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "is closed",
		},
		{
			name: "a claimed member is refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) { foldClaim(t, s, made[1]) },
			wantErr: "is claimed",
		},
		{
			name: "a bead from another yard is refused, for closing it would return it as refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				err := s.write(func(w *wtx) error {
					return put(w.Tx, bPeerBeads, made[1].UUID, &peerBead{Bead: made[1].UUID, Key: "north", Direction: "in"})
				})
				if err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "came from another yard",
		},
		{
			name: "a bead away at another yard is refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				err := s.write(func(w *wtx) error {
					return put(w.Tx, bPeerSent, made[0].UUID, &peerSent{Bead: made[0].UUID, Key: "north", Stage: "new", Message: "m1", At: time.Now()})
				})
				if err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "is away at another yard",
		},
		{
			// plain-pane is claimed and open in the pane this process is the
			// shell of; the two it folds are not its own, and are not claimed.
			name: "an agent in another bead's pane is refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-pane", Title: "mine"}, bead("b"), bead("c")},
			fold:    []int{1, 2}, pane: true,
			prepare: func(t *testing.T, s *Store, made []Bead) { foldPane(t, s, made[0]) },
			wantErr: "may close only its own bead plain-pane",
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
				if nb.Depot == "" {
					nb.Depot = row.depot
				}
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
			// A member's revision changes with anything done to it: a close,
			// an edge.
			revisions := func() []string {
				var out []string
				for _, m := range made {
					b, err := s.GetBead(ctx, m.ID)
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, b.Revision)
				}
				return out
			}
			folded := func() int {
				evs, err := s.EventsOfKind(ctx, "folded", 1000)
				if err != nil {
					t.Fatal(err)
				}
				return len(evs)
			}
			before, was, foldedBefore := count(), revisions(), folded()
			askedAs := ctx
			if row.pane {
				askedAs = WithCaller(ctx, os.Getpid())
			}
			got, err := s.FoldBeads(askedAs, ids, row.opts, or(row.actor, "mckean"))
			if row.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), row.wantErr) {
					t.Fatalf("folded %v: %+v, %v; want an error with %q", ids, got, err, row.wantErr)
				}
				// All or nothing: no carrier, no member closed or given an
				// edge, no fold on record.
				foldWant(t, "beads after a refused fold", count(), before)
				if now := revisions(); !slices.Equal(now, was) {
					t.Errorf("a refused fold changed its members: revisions %v, were %v", now, was)
				}
				foldWant(t, "folded events after a refused fold", folded(), foldedBefore)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			foldWant(t, "beads made", count()-before, 1)
			foldWant(t, "folded events made", folded()-foldedBefore, 1)
			// What the fold answers is what the store holds.
			stored, err := s.GetBead(ctx, got.ID)
			if err != nil || stored.Revision != got.Revision {
				t.Fatalf("the carrier answered is at %v, the bead stored %+v, %v", got.Revision, stored, err)
			}
			row.check(t, s, stored, made)
		})
	}
}

// A fold is one write: the carrier, the edges, the closes and the event are
// all in the transaction, so a write that fails after the fold takes every one
// of them with it. No row of TestFoldBeads can show it, since a fold refuses
// everything it can before it makes anything.
func TestFoldInOneWrite(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := foldYard(t)
	var ids []string
	for _, title := range []string{"a", "b", "c"} {
		b, err := s.CreateBead(ctx, NewBead{Title: title, Depot: "plain"}, "mckean")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, b.ID)
	}
	depot := "plain"
	reads, err := s.readsFor(ctx, &depot, "task", nil)
	if err != nil {
		t.Fatal(err)
	}
	late := errors.New("a failure after the fold")
	err = s.write(func(w *wtx) error {
		var members []*beadRow
		for _, id := range ids {
			row, err := w.loadBead(id)
			if err != nil {
				return err
			}
			members = append(members, row)
		}
		if _, err := w.fold(ctx, reads, nil, members, FoldOptions{To: "ready"}, "hand", "mckean"); err != nil {
			return err
		}
		return late
	})
	if !errors.Is(err, late) {
		t.Fatalf("the write = %v, want the failure after the fold", err)
	}
	all, err := s.ListBeads(ctx, BeadFilter{All: true})
	if err != nil {
		t.Fatal(err)
	}
	foldWant(t, "beads after the failed write", len(all), len(ids))
	for _, b := range all {
		foldWant(t, b.ID+" status", b.Status, "open")
	}
	edges, err := s.AllDeps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foldWant(t, "edges after the failed write", len(edges), 0)
	folded, err := s.EventsOfKind(ctx, "folded", 10)
	if err != nil {
		t.Fatal(err)
	}
	foldWant(t, "folded events after the failed write", len(folded), 0)
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
