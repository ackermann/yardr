package store

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/peer"
)

// The flows the fold rows follow. foldF has the way a siding takes; foldG,
// which the web depot's beads resolve to, has neither stage, so a carrier in
// ready is governed by foldF or refused. foldRelease and foldChain are a
// type's flow.
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
	// foldChain has one way out of each stage, so that done leads from
	// each stage to the next: an edge that releases at ready has let go
	// of a blocker in new.
	foldChain = `
backlog -> siding
siding -> ready
ready -> new
new -> merged
merged terminal
`
)

// foldYard is a store with the depots the rows fold in: plain, which no flow
// governs; web, whose beads follow fold.g unless they pin fold.f; repo, a
// directory depot whose repository holds a default flow and one for each of
// the types release and chain; and mixed, the same repository with its beads pointed at fold.g,
// so that only a bead pinned to the repository's default needs it. mckean and
// sam are its users, ada a crew member, builders a group that can hold ten
// sessions and desk a manual group of sam's, so that a bead can have watchers
// of each kind.
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
	for name, text := range map[string]string{"default.flow": foldF, "type/release.flow": foldRelease, "type/chain.flow": foldChain} {
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
	if _, err := s.PutCrew(ctx, CrewMember{Name: "ada", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, Group{Name: "desk", Runner: "manual", Members: []string{"sam"}}, "me"); err != nil {
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

// foldGitHub and foldLinear are the issues the rows link. Each row names
// its own: the yard the rows share has one tracking bead for an issue.
func foldGitHub(n int) string { return "https://github.com/yardr-dev/yardr/issues/" + strconv.Itoa(n) }
func foldLinear(n int) string { return "https://linear.app/yardr/issue/YRD-" + strconv.Itoa(n) }

// foldLink links bead id to url as kind, of the source its host names.
func foldLink(t *testing.T, s *Store, id, kind, url string) {
	t.Helper()
	source := "linear"
	if strings.Contains(url, "github.com") {
		source = "github"
	}
	if _, err := s.AddLink(context.Background(), id, NewLink{Kind: kind, Source: source, URL: url}, "mckean"); err != nil {
		t.Fatal(err)
	}
}

// foldLinksOf lists the links of bead id as "kind url", sorted.
func foldLinksOf(t *testing.T, s *Store, id string) []string {
	t.Helper()
	ls, err := s.Links(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, l := range ls {
		out = append(out, l.Kind+" "+l.URL)
	}
	slices.Sort(out)
	return out
}

// foldWantLinks wants the links of bead id to be exactly want, as foldLinksOf
// says them.
func foldWantLinks(t *testing.T, s *Store, id string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := foldLinksOf(t, s, id); !slices.Equal(got, want) {
		t.Errorf("links of %s = %v, want %v", id, got, want)
	}
}

// foldEdges lists the edges of kind that touch bead id as "from -> to", with
// the stages they name as " at <stage>" and " until <stage>", sorted.
func foldEdges(t *testing.T, s *Store, id, kind string) []string {
	t.Helper()
	ds, err := s.Deps(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, d := range ds {
		if d.Kind != kind {
			continue
		}
		edge := d.From + " -> " + d.To
		if d.At != "" {
			edge += " at " + d.At
		}
		if d.Until != "" {
			edge += " until " + d.Until
		}
		out = append(out, edge)
	}
	slices.Sort(out)
	return out
}

// foldWantEdges wants the edges of kind that touch bead id to be exactly want,
// as foldEdges says them.
func foldWantEdges(t *testing.T, s *Store, id, kind string, want ...string) {
	t.Helper()
	slices.Sort(want)
	if got := foldEdges(t, s, id, kind); !slices.Equal(got, want) {
		t.Errorf("%s edges of %s = %v, want %v", kind, id, got, want)
	}
}

// foldWantMoved wants the folded edge from the carrier to member to record
// exactly what.
func foldWantMoved(t *testing.T, s *Store, carrier, member string, want FoldMoved) {
	t.Helper()
	ds, err := s.Deps(context.Background(), carrier)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Kind == DepFolded && d.To == member {
			if d.Moved == nil || !reflect.DeepEqual(*d.Moved, want) {
				t.Errorf("the fold took from %s: %+v, want %+v", member, d.Moved, want)
			}
			return
		}
	}
	t.Errorf("no folded edge from %s to %s", carrier, member)
}

// foldWatch watches bead id as the subscriber name of kind (a crew member, a
// user or a group), with scope and the filters beside bead=id that are given
// as key=value.
func foldWatch(t *testing.T, s *Store, kind, name, scope, id string, also ...string) Subscription {
	t.Helper()
	sub := Subscription{Source: SourceYardr, Scope: scope, Where: map[string]string{"bead": id}}
	for _, kv := range also {
		k, v, _ := strings.Cut(kv, "=")
		sub.Where[k] = v
	}
	actor := name
	if kind == "group" {
		actor = "sam" // a member of the desk group
	}
	if err := s.AddSubscriptionKind(context.Background(), name, kind, sub, actor); err != nil {
		t.Fatal(err)
	}
	return sub
}

// foldWatches lists the watches of the subscriber name of kind that name one
// of ids, as "scope bead=<id> <other filters>", sorted: a yard the rows share
// holds the watches of the rows before.
func foldWatches(t *testing.T, s *Store, kind, name string, ids ...string) []string {
	t.Helper()
	var subs []Subscription
	err := s.read(func(tx *bolt.Tx) error {
		switch kind {
		case "crew":
			var r CrewMember
			_, err := get(tx, bCrew, name, &r)
			subs = r.Subscriptions
			return err
		case "user":
			var u User
			_, err := get(tx, bUsers, name, &u)
			subs = u.Subscriptions
			return err
		}
		var g Group
		_, err := get(tx, bGroups, name, &g)
		subs = g.Subscriptions
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, sub := range subs {
		if slices.Contains(ids, sub.Where["bead"]) {
			out = append(out, sub.Scope+" "+whereString(sub.Where))
		}
	}
	slices.Sort(out)
	return out
}

// foldGateFlips lists the gate events of bead id as "<kind> <blockers>",
// oldest first.
func foldGateFlips(t *testing.T, s *Store, id string) []string {
	t.Helper()
	evs, err := s.Events(context.Background(), id, 100)
	if err != nil {
		t.Fatal(err)
	}
	var flips []string
	for _, e := range evs {
		if e.Kind == "unblocked" || e.Kind == "reblocked" {
			flips = append(flips, e.Kind+" "+strings.Join(foldStrings(e.Data["blockers"]), ","))
		}
	}
	return flips
}

// foldStrings reads the list of strings an event's data holds under a key, as
// the store hands it back.
func foldStrings(v any) []string {
	var out []string
	l, _ := v.([]any)
	for _, x := range l {
		out = append(out, x.(string))
	}
	return out
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

// foldManualHold has a manual group hold each of beads, as the daemon has a
// group of that runner hold the beads of its stage: claimed, then running
// with the handle the manual runner leaves, since nothing runs in the claim.
// The group takes fifty, as the yard's backlog does; foldYard's desk, which
// the watcher rows use, takes one.
func foldManualHold(t *testing.T, s *Store, beads ...Bead) []*Session {
	t.Helper()
	ctx := context.Background()
	if err := s.PutGroup(ctx, Group{Name: "backlog", Runner: "manual", Limit: 50}, "me"); err != nil {
		t.Fatal(err)
	}
	var held []*Session
	for _, b := range beads {
		sess, err := s.Claim(ctx, b.ID, "backlog", b.Stage, "yard")
		if err != nil {
			t.Fatal(err)
		}
		if err := s.MarkRunning(ctx, sess.ID, map[string]any{"manual": true}); err != nil {
			t.Fatal(err)
		}
		held = append(held, sess)
	}
	return held
}

// foldSpoilSession leaves the record of the session sess as raw says: gone when
// raw is nil, else those bytes, which the store may not be able to read. The
// record is put back when the row ends, so that no row after it finds a
// session it cannot list.
func foldSpoilSession(t *testing.T, s *Store, sess *Session, raw []byte) {
	t.Helper()
	var whole []byte
	err := s.read(func(tx *bolt.Tx) error {
		whole = slices.Clone(tx.Bucket(bSessions).Get([]byte(sess.ID)))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	set := func(rec []byte) error {
		return s.write(func(w *wtx) error {
			if rec == nil {
				return w.Bucket(bSessions).Delete([]byte(sess.ID))
			}
			return w.Bucket(bSessions).Put([]byte(sess.ID), rec)
		})
	}
	if err := set(raw); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := set(whole); err != nil {
			t.Error(err)
		}
	})
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
	// outside makes a bead the fold does not take, which its members' edges
	// aim at; y is the one a row's edges aim at, made by the row that has
	// one (the rows run in turn).
	outside := func(t *testing.T, s *Store, nb NewBead) Bead {
		t.Helper()
		b, err := s.CreateBead(ctx, nb, "mckean")
		if err != nil {
			t.Fatal(err)
		}
		return *b
	}
	var y Bead
	// underTrain makes the train id and gives it each of children, as dep add
	// -k parent does: a parent edge, and the child's blocks edge to the train.
	underTrain := func(t *testing.T, s *Store, id string, children ...Bead) Bead {
		t.Helper()
		train := outside(t, s, NewBead{ID: id, Title: "train " + id, Type: TypeTrain, Depot: "plain"})
		for _, c := range children {
			if err := s.AddDep(ctx, train.ID, c.ID, DepParent, "", "", "mckean"); err != nil {
				t.Fatal(err)
			}
		}
		return train
	}
	// held are the sessions a manual group holds a row's members by, for that
	// row's check to read (the rows run in turn).
	var held []*Session
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
			name: "a member's tracks link moves to the carrier and leaves a relates link on the member", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				foldLink(t, s, made[0].ID, LinkTracks, foldGitHub(1))
				// A link that only relates stays on its bead.
				foldLink(t, s, made[1].ID, LinkRelates, foldGitHub(2))
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWantLinks(t, s, got.ID, "tracks "+foldGitHub(1))
				foldWantLinks(t, s, made[0].ID, "relates "+foldGitHub(1))
				foldWantLinks(t, s, made[1].ID, "relates "+foldGitHub(2))
				foldWantMoved(t, s, got.ID, made[0].ID, FoldMoved{Stage: "new", Links: []FoldLink{{Source: "github", URL: foldGitHub(1), Kind: LinkTracks}}})
				foldWantMoved(t, s, got.ID, made[1].ID, FoldMoved{Stage: "new"})
				// The move is told on both beads, as MoveLink tells it.
				told := func(id, key, other string) {
					evs, err := s.Events(ctx, id, 50)
					if err != nil {
						t.Fatal(err)
					}
					var named []any
					for _, e := range evs {
						if e.Kind == "linked" && e.Data[key] != nil {
							named = append(named, e.Data[key])
						}
					}
					if !reflect.DeepEqual(named, []any{other}) {
						t.Errorf("%s of the linked events of %s = %v, want %s", key, id, named, other)
					}
				}
				told(got.ID, "moved_from", made[0].ID)
				told(made[0].ID, "moved_to", got.ID)
			},
		},
		{
			name: "a second tracked issue of a source is a relates link on the carrier, and its note names it", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b"), bead("c")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				foldLink(t, s, made[0].ID, LinkTracks, foldGitHub(3))
				foldLink(t, s, made[1].ID, LinkTracks, foldGitHub(4))
				foldLink(t, s, made[2].ID, LinkTracks, foldLinear(3))
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				// The carrier tracks one issue per source, so the second of
				// github only relates; the member relates it too, so that no
				// bead that is closed tracks it.
				foldWantLinks(t, s, got.ID, "tracks "+foldGitHub(3), "relates "+foldGitHub(4), "tracks "+foldLinear(3))
				foldWantLinks(t, s, made[1].ID, "relates "+foldGitHub(4))
				ls, err := s.IssueLinks(ctx, foldGitHub(4))
				if err != nil {
					t.Fatal(err)
				}
				for _, l := range ls {
					foldWant(t, "kind of a link to the second issue", l.Kind, LinkRelates)
				}
				foldWantMoved(t, s, got.ID, made[1].ID, FoldMoved{Stage: "new", Links: []FoldLink{{Source: "github", URL: foldGitHub(4), Kind: LinkRelates}}})
				foldWantMoved(t, s, got.ID, made[2].ID, FoldMoved{Stage: "new", Links: []FoldLink{{Source: "linear", URL: foldLinear(3), Kind: LinkTracks}}})
				notes, err := s.Notes(ctx, got.ID)
				if err != nil || len(notes) != 1 {
					t.Fatalf("notes of the carrier = %+v, %v; want the fold's one", notes, err)
				}
				if want := made[1].ID + " tracked " + foldGitHub(4) + " (github)"; !strings.Contains(notes[0].Text, want) {
					t.Errorf("the note does not say %q:\n%s", want, notes[0].Text)
				}
				for _, not := range []string{foldGitHub(3), foldLinear(3)} {
					if strings.Contains(notes[0].Text, not) {
						t.Errorf("the note names %s, an issue the carrier tracks:\n%s", not, notes[0].Text)
					}
				}
			},
		},
		{
			name: "a member's outgoing blocks edge, with the stages it names, is re-pointed to the carrier", depot: "web", opts: FoldOptions{To: "backlog"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				y = outside(t, s, NewBead{Title: "y", Depot: "web", Stage: "new"})
				if err := s.AddDep(ctx, made[0].ID, y.ID, DepBlocks, "new", "new", "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				edge := got.ID + " -> " + y.ID + " at new until new"
				foldWantEdges(t, s, got.ID, DepBlocks, edge)
				foldWantEdges(t, s, made[0].ID, DepBlocks)
				foldWantEdges(t, s, y.ID, DepBlocks, edge)
				foldWantMoved(t, s, got.ID, made[0].ID, FoldMoved{Stage: "backlog", Blocks: []FoldBlock{{To: y.ID, At: "new", Until: "new"}}})
				// The carrier has not reached the stage the edge releases at,
				// so it holds the bead as its part did.
				holders, err := s.OpenBlockers(ctx, y.ID)
				if err != nil || !slices.Equal(holders, []string{got.ID}) {
					t.Errorf("%s is held by %v, %v; want the carrier %s", y.ID, holders, err, got.ID)
				}
			},
		},
		{
			name: "two parts that block one bead the same way leave the carrier one edge, and both record it", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b"), bead("c")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				y = outside(t, s, NewBead{Title: "y", Depot: "plain"})
				for _, m := range made[:2] {
					if err := s.AddDep(ctx, m.ID, y.ID, DepBlocks, "", "", "mckean"); err != nil {
						t.Fatal(err)
					}
				}
				if err := s.AddDep(ctx, made[2].ID, y.ID, DepBlocks, "", "review", "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWantEdges(t, s, y.ID, DepBlocks, got.ID+" -> "+y.ID, got.ID+" -> "+y.ID+" until review")
				for i, until := range []string{"", "", "review"} {
					foldWantMoved(t, s, got.ID, made[i].ID, FoldMoved{Stage: "new", Blocks: []FoldBlock{{To: y.ID, Until: until}}})
				}
			},
		},
		{
			name: "a stage the carrier starts in releases what its part held until it, and the gate says so", depot: "web", opts: FoldOptions{To: "new"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				y = outside(t, s, NewBead{Title: "y", Depot: "web", Stage: "new"})
				if err := s.AddDep(ctx, made[0].ID, y.ID, DepBlocks, "", "new", "mckean"); err != nil {
					t.Fatal(err)
				}
				// The part is in backlog, short of new: it holds y.
				if holders, err := s.OpenBlockers(ctx, y.ID); err != nil || !slices.Equal(holders, []string{made[0].ID}) {
					t.Fatalf("%s is held by %v, %v before the fold; want %s", y.ID, holders, err, made[0].ID)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWantEdges(t, s, y.ID, DepBlocks, got.ID+" -> "+y.ID+" until new")
				if holders, err := s.OpenBlockers(ctx, y.ID); err != nil || len(holders) != 0 {
					t.Errorf("%s is held by %v, %v; want none, the carrier standing in new", y.ID, holders, err)
				}
				// The edge held y when it was made, and the fold let go of it.
				if flips, want := foldGateFlips(t, s, y.ID), []string{"reblocked " + made[0].ID, "unblocked " + made[0].ID}; !slices.Equal(flips, want) {
					t.Errorf("gate events of %s = %v, want %v", y.ID, flips, want)
				}
			},
		},
		{
			name: "a repository flow reads the stage a re-pointed edge releases at, for the carrier", depot: "repo", opts: FoldOptions{To: "new"},
			members: []NewBead{{Title: "a", Type: "chain"}, {Title: "b", Type: "chain"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				y = outside(t, s, NewBead{Title: "y", Depot: "plain"})
				if err := s.AddDep(ctx, made[0].ID, y.ID, DepBlocks, "", "ready", "mckean"); err != nil {
					t.Fatal(err)
				}
				// The part is in backlog, short of ready: it holds y.
				if holders, err := s.OpenBlockers(ctx, y.ID); err != nil || !slices.Equal(holders, []string{made[0].ID}) {
					t.Fatalf("%s is held by %v, %v before the fold; want %s", y.ID, holders, err, made[0].ID)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				// The carrier stands in new, past ready along done, which only
				// the flow in the repository says. A read of the gate finds
				// the repository on its own; the fold's write has to be given
				// it, or its log says y is held still.
				foldWantEdges(t, s, y.ID, DepBlocks, got.ID+" -> "+y.ID+" until ready")
				if holders, err := s.OpenBlockers(ctx, y.ID); err != nil || len(holders) != 0 {
					t.Errorf("%s is held by %v, %v; want none, the carrier standing past ready", y.ID, holders, err)
				}
				if flips, want := foldGateFlips(t, s, y.ID), []string{"reblocked " + made[0].ID, "unblocked " + made[0].ID}; !slices.Equal(flips, want) {
					t.Errorf("gate events of %s = %v, want %v", y.ID, flips, want)
				}
			},
		},
		{
			// The filing's repro: x holds b, and the carrier is made with none
			// of b's blockers, so it would route and build while x is open.
			// The blocked member is the second, to show the check is not made
			// on the first alone.
			name: "a member that an open bead blocks is refused, naming the blocker", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-h1", Title: "a"}, {ID: "plain-h2", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				x := outside(t, s, NewBead{ID: "plain-hx", Title: "x", Depot: "plain"})
				if err := s.AddDep(ctx, x.ID, made[1].ID, DepBlocks, "", "", "mckean"); err != nil {
					t.Fatal(err)
				}
				if holders, err := s.OpenBlockers(ctx, made[1].ID); err != nil || !slices.Equal(holders, []string{x.ID}) {
					t.Fatalf("%s is held by %v, %v before the fold; want %s", made[1].ID, holders, err, x.ID)
				}
			},
			wantErr: "bead plain-h2 is blocked by plain-hx: a fold takes beads nothing blocks",
		},
		{
			// Two edges from one blocker hold as one, as the gate counts them.
			name: "each blocker is named once, however many edges it holds the member by", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-h3", Title: "a"}, {ID: "plain-h4", Title: "b"}, {ID: "plain-h5", Title: "c"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				for _, id := range []string{"plain-hy", "plain-hz"} {
					outside(t, s, NewBead{ID: id, Title: id, Depot: "plain"})
				}
				for _, e := range [][2]string{{"plain-hz", ""}, {"plain-hy", ""}, {"plain-hz", "review"}} {
					if err := s.AddDep(ctx, e[0], made[2].ID, DepBlocks, "", e[1], "mckean"); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantErr: "bead plain-h5 is blocked by plain-hy, plain-hz: a fold",
		},
		{
			// A manual group holds the beads of its stage, and a fold's main use
			// is on those: its claim is a person's turn and does not stop a fold,
			// and what blocks the member is looked at all the same.
			name: "a member a manual group holds is refused when an open bead blocks it", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-he", Title: "a"}, {ID: "plain-hf", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				foldManualHold(t, s, made...)
				x := outside(t, s, NewBead{ID: "plain-hg", Title: "x", Depot: "plain"})
				if err := s.AddDep(ctx, x.ID, made[1].ID, DepBlocks, "", "", "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "bead plain-hf is blocked by plain-hg: a fold takes beads nothing blocks",
		},
		{
			name: "a member whose blocker has closed is folded, and the carrier is not blocked", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-h6", Title: "a"}, {ID: "plain-h7", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				x := outside(t, s, NewBead{ID: "plain-hw", Title: "x", Depot: "plain"})
				if err := s.AddDep(ctx, x.ID, made[0].ID, DepBlocks, "", "", "mckean"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.CloseBead(ctx, x.ID, "done", Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "stage", got.Stage, "ready")
				if holders, err := s.OpenBlockers(ctx, got.ID); err != nil || len(holders) != 0 {
					t.Errorf("the carrier is held by %v, %v; want none", holders, err)
				}
			},
		},
		{
			// The blocker closes in the same write and the parts are built in
			// the order given, so what orders them is no reason to refuse.
			name: "a member that another member blocks is folded with it", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-h8", Title: "a"}, {ID: "plain-h9", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				if err := s.AddDep(ctx, made[1].ID, made[0].ID, DepBlocks, "", "", "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "stage", got.Stage, "ready")
				if holders, err := s.OpenBlockers(ctx, got.ID); err != nil || len(holders) != 0 {
					t.Errorf("the carrier is held by %v, %v; want none", holders, err)
				}
			},
		},
		{
			// The member skip covers a future hold too: the blocker closes
			// in the same write, so what would order the parts at another
			// stage orders one bundle and refuses nothing.
			name: "members joined by an edge at another stage are folded with it", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-hj", Title: "a"}, {ID: "plain-hk", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				if err := s.AddDep(ctx, made[1].ID, made[0].ID, DepBlocks, "approved", "", "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "stage", got.Stage, "ready")
				if holders, err := s.OpenBlockers(ctx, got.ID); err != nil || len(holders) != 0 {
					t.Errorf("the carrier is held by %v, %v; want none", holders, err)
				}
			},
		},
		{
			// a stands in new, so an edge that holds it at approved holds
			// nothing now: ready and bead show say nothing blocks it. The
			// fold refuses it all the same, naming the blocker and the
			// stage, since the carrier would pass approved with x open.
			name: "an edge that holds a member only at another stage refuses, naming the blocker and the stage", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-ha", Title: "a"}, {ID: "plain-hb", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				x := outside(t, s, NewBead{ID: "plain-hv", Title: "x", Depot: "plain"})
				if err := s.AddDep(ctx, x.ID, made[0].ID, DepBlocks, "approved", "", "mckean"); err != nil {
					t.Fatal(err)
				}
				if holders, err := s.OpenBlockers(ctx, made[0].ID); err != nil || len(holders) != 0 {
					t.Fatalf("%s is held by %v, %v before the fold; want none, standing short of approved", made[0].ID, holders, err)
				}
			},
			wantErr: "bead plain-ha is blocked by plain-hv at approved: a fold takes beads nothing blocks",
		},
		{
			// The release is the repository flow's to say (x stands in new,
			// past ready along its chain), and a fold finds the repository on
			// its own as a read of the gate does, or it would refuse what
			// bead show says nothing blocks.
			name: "a blocker a repository flow has released through its until stage holds nothing, and does not refuse", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-hc", Title: "a"}, {ID: "plain-hd", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				x := outside(t, s, NewBead{ID: "repo-hu", Title: "x", Depot: "repo", Type: "chain", Stage: "new"})
				if err := s.AddDep(ctx, x.ID, made[0].ID, DepBlocks, "", "ready", "mckean"); err != nil {
					t.Fatal(err)
				}
				if holders, err := s.OpenBlockers(ctx, made[0].ID); err != nil || len(holders) != 0 {
					t.Fatalf("%s is held by %v, %v before the fold; want none, its blocker past ready", made[0].ID, holders, err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "stage", got.Stage, "ready")
			},
		},
		{
			// The filing's repro: a and b are children of a train, and a carrier
			// made of them is no child, so it would branch from and land on the
			// depot's base while the train waits on it by a blocks edge alone.
			name: "a member that is a child of a train is refused, naming the train", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-t1", Title: "a"}, {ID: "plain-t2", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				underTrain(t, s, "plain-tt1", made...)
			},
			wantErr: "bead plain-t1 is a child of train plain-tt1: a fold takes beads no train holds",
		},
		{
			// The refusal names the commands that get past it, and the carrier
			// those leave is the train's child, as a bead made for it would be.
			// The second member is refused after the first is off the train, so
			// every member is looked at, not the first alone.
			name: "a member taken off its train is folded, and the carrier put on the train is its child", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-t3", Title: "a"}, {ID: "plain-t4", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				y = underTrain(t, s, "plain-tt2", made...)
				for _, m := range made {
					_, err := s.FoldBeads(ctx, []string{made[0].ID, made[1].ID}, FoldOptions{To: "ready"}, "mckean")
					if rm := "yardr dep rm " + y.ID + " " + m.ID + " -k parent"; err == nil || !strings.Contains(err.Error(), rm) {
						t.Fatalf("fold with %s still on its train: %v, want the way out %q", m.ID, err, rm)
					}
					if err := s.RemoveDep(ctx, y.ID, m.ID, DepParent, "", "", "mckean"); err != nil {
						t.Fatal(err)
					}
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				if err := s.AddDep(ctx, y.ID, got.ID, DepParent, "", "", "mckean"); err != nil {
					t.Fatalf("dep add %s %s -k parent: %v", y.ID, got.ID, err)
				}
				f, err := s.Family(ctx, got.ID)
				if err != nil {
					t.Fatal(err)
				}
				foldWant(t, "train of the carrier", f.Train, y.ID)
				foldWant(t, "base of the carrier", f.Base, Branch(y.ID))
				foldWantEdges(t, s, y.ID, DepParent, y.ID+" -> "+got.ID)
				foldWantEdges(t, s, y.ID, DepBlocks, got.ID+" -> "+y.ID)
				if holders, err := s.OpenBlockers(ctx, y.ID); err != nil || !slices.Equal(holders, []string{got.ID}) {
					t.Errorf("the train is held by %v, %v; want the carrier %s", holders, err, got.ID)
				}
			},
		},
		{
			// A closed train has landed or was dropped, and its open child has
			// its branch for a base still (Family), so the way out is the same.
			name: "a child of a closed train is refused too", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-t5", Title: "a"}, {ID: "plain-t6", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				train := underTrain(t, s, "plain-tt3", made[1])
				if _, err := s.CloseBead(ctx, train.ID, "dropped", Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "bead plain-t6 is a child of train plain-tt3: a fold takes beads no train holds",
		},
		{
			// Only a parent edge from a bead of type train makes a child (one
			// imported from beads makes none, nor one left when the parent's type
			// changed): such a bead lands on the depot's base, so the carrier
			// leaves it nowhere it was not.
			name: "a parent edge from a bead that is no train does not refuse", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-t7", Title: "a"}, {ID: "plain-t8", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				parent := underTrain(t, s, "plain-tt4", made...)
				task := "task"
				if _, err := s.UpdateBead(ctx, parent.ID, BeadPatch{Type: &task}, Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
				for _, m := range made {
					if f, err := s.Family(ctx, m.ID); err != nil || f.Train != "" || f.Base != "main" {
						t.Fatalf("%s is %+v, %v before the fold; want no train, the depot's base", m.ID, f, err)
					}
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "stage", got.Stage, "ready")
			},
		},
		{
			// bad_train: a type changed to train after the bead had a train.
			// The fold takes it for the train's child that it is, in the words
			// that name both trains.
			name: "a child of two trains is refused, naming both", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-t9", Title: "a"}, {ID: "plain-ta", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				first := underTrain(t, s, "plain-tt5", made[1])
				task, train := "task", TypeTrain
				if _, err := s.UpdateBead(ctx, first.ID, BeadPatch{Type: &task}, Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
				underTrain(t, s, "plain-tt6", made[1])
				if _, err := s.UpdateBead(ctx, first.ID, BeadPatch{Type: &train}, Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "bead plain-ta: child of two trains, plain-tt5 and plain-tt6; remove one parent edge",
		},
		{
			// The filing's repro: T is a train whose child has closed, so
			// nothing blocks it, and X is an ordinary task. The fold refuses
			// T itself: the carrier would be a hollow bead of type train with
			// no children while the train's children keep pointing at the
			// closed T.
			name: "a member that is a train is refused, naming it", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-tm1", Title: "train", Type: TypeTrain}, {ID: "plain-tm2", Title: "x"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				c := outside(t, s, NewBead{ID: "plain-tm3", Title: "child", Depot: "plain"})
				if err := s.AddDep(ctx, made[0].ID, c.ID, DepParent, "", "", "mckean"); err != nil {
					t.Fatal(err)
				}
				if _, err := s.CloseBead(ctx, c.ID, "landed", Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
				if holders, err := s.OpenBlockers(ctx, made[0].ID); err != nil || len(holders) != 0 {
					t.Fatalf("%s is held by %v, %v before the fold; want none, its child closed", made[0].ID, holders, err)
				}
			},
			wantErr: "bead plain-tm1 is a train: a fold takes no train",
		},
		{
			// A train beside the fold, with its child closed as in the
			// filing, holds no member: tasks fold as before.
			name: "a task fold beside a train is unaffected", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-tm4", Title: "a"}, {ID: "plain-tm5", Title: "b"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				c := outside(t, s, NewBead{ID: "plain-tm6", Title: "child", Depot: "plain"})
				underTrain(t, s, "plain-tt7", c)
				if _, err := s.CloseBead(ctx, c.ID, "landed", Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "type", got.Type, "task")
			},
		},
		{
			// A train with an open child is held by it, so the fold refuses
			// the train as blocked, before it is refused as a train.
			name: "a train with an open child is refused as blocked, not as a train", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{{ID: "plain-tm7", Title: "train", Type: TypeTrain}, {ID: "plain-tm8", Title: "x"}},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				c := outside(t, s, NewBead{ID: "plain-tm9", Title: "child", Depot: "plain"})
				if err := s.AddDep(ctx, made[0].ID, c.ID, DepParent, "", "", "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "bead plain-tm7 is blocked by plain-tm9: a fold takes beads nothing blocks",
		},
		func() foldRow {
			var origin, found, other Bead
			return foldRow{
				name: "discovered-from and related edges stay on the member", depot: "plain", opts: FoldOptions{To: "ready"},
				members: []NewBead{bead("a"), bead("b")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					origin = outside(t, s, NewBead{Title: "origin", Depot: "plain"})
					found = outside(t, s, NewBead{Title: "found", Depot: "plain"})
					other = outside(t, s, NewBead{Title: "other", Depot: "plain"})
					for _, e := range [][3]string{{origin.ID, made[0].ID, DepDiscoveredFrom}, {made[0].ID, found.ID, DepDiscoveredFrom}, {made[0].ID, other.ID, "related"}} {
						if err := s.AddDep(ctx, e[0], e[1], e[2], "", "", "mckean"); err != nil {
							t.Fatal(err)
						}
					}
				},
				check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
					a := made[0].ID
					foldWantEdges(t, s, a, DepDiscoveredFrom, origin.ID+" -> "+a, a+" -> "+found.ID)
					foldWantEdges(t, s, a, "related", a+" -> "+other.ID)
					foldWantEdges(t, s, got.ID, DepDiscoveredFrom)
					foldWantEdges(t, s, got.ID, "related")
					foldWantMoved(t, s, got.ID, a, FoldMoved{Stage: "new"})
				},
			}
		}(),
		func() foldRow {
			var elsewhere Bead
			return foldRow{
				name: "bead=<member> watches are copied to the carrier, whoever holds them", depot: "plain", opts: FoldOptions{To: "ready"},
				members: []NewBead{bead("a"), bead("b"), bead("c")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					a, b := made[0].ID, made[1].ID
					elsewhere = outside(t, s, NewBead{Title: "elsewhere", Depot: "plain"})
					foldWatch(t, s, "crew", "ada", ScopeAll, a)
					foldWatch(t, s, "crew", "ada", ScopeAll, b) // the same watch of another part: one copy
					foldWatch(t, s, "crew", "ada", ScopeMentions, b)
					foldWatch(t, s, "user", "sam", ScopeAll, a, "kind=noted") // what else it filters on is kept
					foldWatch(t, s, "user", "sam", ScopeAll, made[2].ID)
					foldWatch(t, s, "group", "desk", ScopeAll, b)
					foldWatch(t, s, "user", "sam", ScopeAll, elsewhere.ID)
				},
				check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
					a, b, c := made[0].ID, made[1].ID, got.ID
					parts := []string{a, b, made[2].ID}
					for _, w := range []struct {
						kind, name string
						want       []string // sorted below, as the handles are random
					}{
						{"crew", "ada", []string{"all bead=" + c, "mentions bead=" + c}},
						{"user", "sam", []string{"all bead=" + c + " kind=noted", "all bead=" + c, "all bead=" + elsewhere.ID}},
						{"group", "desk", []string{"all bead=" + c}},
					} {
						slices.Sort(w.want)
						if got := foldWatches(t, s, w.kind, w.name, append(parts, c, elsewhere.ID)...); !slices.Equal(got, w.want) {
							t.Errorf("watches of %s %s = %v, want %v (the parts' own ended with their closes)", w.kind, w.name, got, w.want)
						}
					}
					watch := func(scope string, where ...string) Subscription {
						sub := Subscription{Source: SourceYardr, Scope: scope, Where: map[string]string{}}
						for _, kv := range where {
							k, v, _ := strings.Cut(kv, "=")
							sub.Where[k] = v
						}
						return sub
					}
					foldWantMoved(t, s, c, a, FoldMoved{Stage: "new", Watchers: []FoldWatcher{
						{Kind: "crew", Name: "ada", Watch: watch(ScopeAll, "bead="+a)},
						{Kind: "user", Name: "sam", Watch: watch(ScopeAll, "bead="+a, "kind=noted")},
					}})
					foldWantMoved(t, s, c, b, FoldMoved{Stage: "new", Watchers: []FoldWatcher{
						{Kind: "crew", Name: "ada", Watch: watch(ScopeAll, "bead="+b)},
						{Kind: "crew", Name: "ada", Watch: watch(ScopeMentions, "bead="+b)},
						{Kind: "group", Name: "desk", Watch: watch(ScopeAll, "bead="+b)},
					}})
					foldWantMoved(t, s, c, made[2].ID, FoldMoved{Stage: "new", Watchers: []FoldWatcher{
						{Kind: "user", Name: "sam", Watch: watch(ScopeAll, "bead="+made[2].ID)},
					}})
					// They watch the carrier before the fold is told, so the
					// ones who watched a part hear of the bundle.
					evs, err := s.Events(ctx, c, 50)
					if err != nil {
						t.Fatal(err)
					}
					for _, e := range evs {
						if e.Kind != "folded" {
							continue
						}
						if Reason(e, "ada") == "" {
							t.Errorf("ada, who watched parts, did not hear of the fold: %v", e.Data)
						}
						if notified, _ := e.Data["notified"].(map[string]any); notified["sam"] == nil {
							t.Errorf("sam, who watched a part, was not told of the fold: %v", e.Data)
						}
					}
				},
			}
		}(),
		{
			name: "a member's stage asks are taken back", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				err := s.write(func(w *wtx) error {
					row, err := w.loadBead(made[0].ID)
					if err != nil {
						return err
					}
					_, err = w.storeStageAsk(row, Ask{Question: "Move it on?", To: "mckean", Stage: "new"})
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				notes, err := s.Notes(ctx, made[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				asks := ResolveAsks(notes, time.Now())
				if len(asks) != 1 || asks[0].AskErr != nil {
					t.Fatalf("asks of the member = %+v", asks)
				}
				foldWant(t, "state of its stage ask", asks[0].State, AskTakenBack)
				foldWant(t, "who took it back", asks[0].Current.Note.Author, "yard")
			},
		},
		func() foldRow {
			var files [3]BlobRef
			return foldRow{
				name: "the carrier's note lists each member's note count and files", depot: "plain", opts: FoldOptions{To: "ready"},
				members: []NewBead{bead("a"), bead("b"), bead("c")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					put := func(text string) BlobRef {
						ref, err := s.PutBlob(ctx, []byte(text))
						if err != nil {
							t.Fatal(err)
						}
						return *ref
					}
					for i, text := range []string{"shot", "paste", "trace"} {
						files[i] = put(text)
					}
					note := func(id, text string, fs ...BlobFile) {
						if err := s.AddNote(ctx, id, text, "mckean", fs...); err != nil {
							t.Fatal(err)
						}
					}
					note(made[0].ID, "found it")
					note(made[0].ID, "what it shows", BlobFile{Hash: files[0].Hash, Name: "shot-mac.sh"}, BlobFile{Hash: files[1].Hash})
					note(made[0].ID, "more")
					note(made[2].ID, "the trace", BlobFile{Hash: files[2].Hash, Name: "trace, 1.txt"})
					if err := s.write(func(w *wtx) error {
						row, err := w.loadBead(made[0].ID)
						if err != nil {
							return err
						}
						_, err = w.storeStageAsk(row, Ask{Question: "Move it on?", To: "mckean", Stage: "new"})
						return err
					}); err != nil {
						t.Fatal(err)
					}
				},
				check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
					seq := func(id string, n int) string {
						notes, err := s.Notes(ctx, id)
						if err != nil || len(notes) <= n {
							t.Fatalf("notes of %s = %+v, %v", id, notes, err)
						}
						return strconv.FormatUint(notes[n].Seq, 10)
					}
					a, b, c := made[0].ID, made[1].ID, made[2].ID
					want := "Each part's notes and files stay on the part: read them with yardr bead show <part>.\n\n" +
						a + ": 5 notes\n" +
						a + " note " + seq(a, 1) + ": shot-mac.sh, file " + files[1].Hash[:12] + "\n" +
						b + ": no notes\n" +
						c + ": 1 note\n" +
						c + " note " + seq(c, 0) + ": \"trace, 1.txt\""
					notes, err := s.Notes(ctx, got.ID)
					if err != nil || len(notes) != 1 {
						t.Fatalf("notes of the carrier = %+v, %v; want the fold's one", notes, err)
					}
					foldWant(t, "kind of the note", notes[0].Kind, NoteNote)
					foldWant(t, "author of the note", notes[0].Author, "mckean")
					foldWant(t, "text of the note", notes[0].Text, want)
					// What the note lists is not copied: the files stay with
					// the member's note.
					foldWant(t, "files on the carrier's note", len(notes[0].Files), 0)
				},
			}
		}(),
		func() foldRow {
			var y Bead
			return foldRow{
				name: "the folded edge records what was taken from each member", depot: "plain", opts: FoldOptions{To: "ready"},
				members: []NewBead{bead("a"), bead("b"), bead("c")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					y = outside(t, s, NewBead{Title: "y", Depot: "plain"})
					foldLink(t, s, made[0].ID, LinkTracks, foldGitHub(5))
					foldLink(t, s, made[1].ID, LinkTracks, foldGitHub(6))
					for _, m := range made[:2] {
						if err := s.AddDep(ctx, m.ID, y.ID, DepBlocks, "", "review", "mckean"); err != nil {
							t.Fatal(err)
						}
					}
					foldWatch(t, s, "crew", "ada", ScopeAll, made[0].ID)
					foldWatch(t, s, "user", "sam", ScopeAll, made[1].ID)
				},
				check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
					watch := func(id string) Subscription {
						return Subscription{Source: SourceYardr, Scope: ScopeAll, Where: map[string]string{"bead": id}}
					}
					foldWantMoved(t, s, got.ID, made[0].ID, FoldMoved{
						Stage:    "new",
						Links:    []FoldLink{{Source: "github", URL: foldGitHub(5), Kind: LinkTracks}},
						Blocks:   []FoldBlock{{To: y.ID, Until: "review"}},
						Watchers: []FoldWatcher{{Kind: "crew", Name: "ada", Watch: watch(made[0].ID)}},
					})
					// The second of a source, and a target the first shares.
					foldWantMoved(t, s, got.ID, made[1].ID, FoldMoved{
						Stage:    "new",
						Links:    []FoldLink{{Source: "github", URL: foldGitHub(6), Kind: LinkRelates}},
						Blocks:   []FoldBlock{{To: y.ID, Until: "review"}},
						Watchers: []FoldWatcher{{Kind: "user", Name: "sam", Watch: watch(made[1].ID)}},
					})
					// A member nothing was taken from records only where it was.
					foldWantMoved(t, s, got.ID, made[2].ID, FoldMoved{Stage: "new"})
				},
			}
		}(),
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
			// A manual group claims the beads of its stage up to its limit, and
			// the fold's main use is on those. Its claim is a person's turn,
			// with nothing running to end.
			name: "members a manual group holds are folded, and the fold ends their claims", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) { held = foldManualHold(t, s, made...) },
			check: func(t *testing.T, s *Store, got *Bead, made []Bead) {
				foldWant(t, "carrier stage", got.Stage, "ready")
				if got.Group != nil || got.Session != nil {
					t.Errorf("the carrier is held by group %v, session %v; want it free to route", got.Group, got.Session)
				}
				foldWantEdges(t, s, got.ID, DepFolded, got.ID+" -> "+made[0].ID, got.ID+" -> "+made[1].ID)
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
					if b.Group != nil || b.Session != nil {
						t.Errorf("%s is closed and still held by group %v, session %v", m.ID, b.Group, b.Session)
					}
				}
				// The close ended the claims, so the group has its places back.
				foldWant(t, "claims held", len(held), len(made))
				for _, h := range held {
					sess, err := s.GetSession(ctx, h.ID)
					if err != nil {
						t.Fatal(err)
					}
					foldWant(t, "session "+h.ID+" state", sess.State, "done")
					if sess.EndedAt == nil {
						t.Errorf("session %s has no end", h.ID)
					}
				}
			},
		},
		{
			// A claim sets a group and a session together, so a bead that names
			// a session the store lacks is in a state nothing makes. If it ever
			// is, the fold does not guess that nothing runs in the claim.
			name: "a member claimed by a session the store lacks is refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				foldSpoilSession(t, s, foldManualHold(t, s, made[1])[0], nil)
			},
			wantErr: "is claimed",
		},
		{
			// The record decodes as a manual claim up to its bad field, then
			// fails. The fold goes by what reads whole.
			name: "a member claimed by a session the store cannot read is refused", depot: "plain", opts: FoldOptions{To: "ready"},
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				foldSpoilSession(t, s, foldManualHold(t, s, made[1])[0], []byte(`{"runner":"manual","state":1}`))
			},
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

// A fold is one write: the carrier, the moves, the edges, the closes and the
// event are all in the transaction, so a write that fails after the fold takes
// every one of them with it. No row of TestFoldBeads can show it, since a fold
// refuses everything it can before it makes anything.
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
	// What the fold moves from a, one of each: its issue, the bead it blocks
	// and its watcher.
	y, err := s.CreateBead(ctx, NewBead{Title: "y", Depot: "plain"}, "mckean")
	if err != nil {
		t.Fatal(err)
	}
	foldLink(t, s, ids[0], LinkTracks, foldGitHub(7))
	if err := s.AddDep(ctx, ids[0], y.ID, DepBlocks, "", "", "mckean"); err != nil {
		t.Fatal(err)
	}
	foldWatch(t, s, "crew", "ada", ScopeAll, ids[0])
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
		if _, err := w.fold(ctx, reads, nil, members, nil, FoldOptions{To: "ready"}, "hand", "mckean"); err != nil {
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
	foldWant(t, "beads after the failed write", len(all), len(ids)+1)
	for _, b := range all {
		foldWant(t, b.ID+" status", b.Status, "open")
	}
	// The one edge there is is a's, and nothing was moved from a.
	edges, err := s.AllDeps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foldWant(t, "edges after the failed write", len(edges), 1)
	foldWantEdges(t, s, y.ID, DepBlocks, ids[0]+" -> "+y.ID)
	foldWantLinks(t, s, ids[0], "tracks "+foldGitHub(7))
	if got, want := foldWatches(t, s, "crew", "ada", ids...), []string{"all bead=" + ids[0]}; !slices.Equal(got, want) {
		t.Errorf("watches of ada after the failed write = %v, want %v", got, want)
	}
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
