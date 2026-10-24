package store

import (
	"context"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

// unfoldIssueN numbers the issues the unfold rows link: the rows share one
// yard, and an issue has one tracking bead.
var unfoldIssueN atomic.Int32

// unfoldIssue is an issue no row has linked yet.
func unfoldIssue() string { return foldGitHub(1000 + int(unfoldIssueN.Add(1))) }

// unfoldRow is one unfold: its members are made in order, so the first is
// the oldest, folded as fold says (every one, in the order made, when nil),
// and made[member] is taken out again.
type unfoldRow struct {
	name    string
	depot   string // the depot of every member that names none
	members []NewBead
	fold    []int
	foldTo  string
	prepare func(t *testing.T, s *Store, made []Bead)                // before the fold
	setup   func(t *testing.T, s *Store, carrier *Bead, made []Bead) // after the fold
	member  int
	pick    func(made []Bead) string // unfolded instead of made[member]
	to      string
	reason  string // "why it comes out" unless set
	actor   string // mckean unless set
	pane    bool   // the unfold is asked from the pane prepare made of this process
	wantErr string // the unfold is refused, and changes nothing
	check   func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead)
}

// An unfold takes a part out of its carrier, restored whole: each row is one
// of the done-when of yardr-fssj. A refused unfold changes nothing.
func TestUnfoldBeads(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := foldYard(t)

	bead := func(title string) NewBead { return NewBead{Title: title} }
	pinned := func(title, flow string) NewBead {
		return NewBead{Title: title, Metadata: map[string]any{"flow": flow}}
	}
	outside := func(t *testing.T, s *Store, nb NewBead) Bead {
		t.Helper()
		b, err := s.CreateBead(ctx, nb, "mckean")
		if err != nil {
			t.Fatal(err)
		}
		return *b
	}
	// unfoldNotes are the texts of the notes of bead id, oldest first.
	notesOf := func(t *testing.T, id string) []string {
		t.Helper()
		notes, err := s.Notes(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range notes {
			out = append(out, n.Text)
		}
		return out
	}
	// unfoldedOf is the carrier's unfolded event's data.
	unfoldedOf := func(t *testing.T, carrier string) map[string]any {
		t.Helper()
		evs, err := s.Events(ctx, carrier, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Kind == "unfolded" {
				return e.Data
			}
		}
		t.Fatalf("%s has no unfolded event", carrier)
		return nil
	}
	// kindsOf lists the kinds of the events of bead id, oldest first.
	kindsOf := func(t *testing.T, id string) []string {
		t.Helper()
		evs, err := s.Events(ctx, id, 100)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range evs {
			out = append(out, e.Kind)
		}
		return out
	}

	rows := []unfoldRow{
		{
			name: "the member reopens in --to with the reason as a note", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")}, reason: "part 1 breaks the gate on linux",
			check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
				a := made[0].ID
				foldWant(t, "status", out.Status, "open")
				foldWant(t, "stage", out.Stage, "backlog")
				if out.CloseReason != nil {
					t.Errorf("close reason = %v, want none", *out.CloseReason)
				}
				if notes := notesOf(t, a); !slices.Contains(notes, "part 1 breaks the gate on linux") {
					t.Errorf("notes of %s = %q, want the reason among them", a, notes)
				}
				if kinds := kindsOf(t, a); !slices.Contains(kinds, "reopened") {
					t.Errorf("events of %s = %v, want a reopen", a, kinds)
				}
				// The other part stays folded.
				if b, err := s.GetBead(ctx, made[1].ID); err != nil || b.Status != "closed" {
					t.Errorf("the other part after the unfold: %+v, %v", b, err)
				}
			},
		},
		{
			name: "without --to it reopens in the flow's first stage", depot: "web", foldTo: "ready",
			members: []NewBead{pinned("a", "fold.f"), pinned("b", "fold.f")},
			check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
				foldWant(t, "stage", out.Stage, "backlog") // fold.f starts there
			},
		},
		{
			name: "without --to and without a flow it reopens in new", depot: "plain", foldTo: "ready",
			members: []NewBead{bead("a"), bead("b")},
			check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
				foldWant(t, "stage", out.Stage, "new")
			},
		},
		func() unfoldRow {
			var issue string
			return unfoldRow{
				name: "its tracking link moves back to it", depot: "plain", foldTo: "ready", to: "backlog",
				members: []NewBead{bead("a"), bead("b")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					issue = unfoldIssue()
					foldLink(t, s, made[0].ID, LinkTracks, issue)
				},
				check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
					a := made[0].ID
					foldWantLinks(t, s, a, "tracks "+issue)
					foldWantLinks(t, s, carrier.ID, "relates "+issue)
				},
			}
		}(),
		func() unfoldRow {
			var first, second string
			return unfoldRow{
				name: "a second issue of a source is tracked by the part again", depot: "plain", foldTo: "ready", to: "backlog",
				members: []NewBead{bead("a"), bead("b")}, member: 1,
				prepare: func(t *testing.T, s *Store, made []Bead) {
					first, second = unfoldIssue(), unfoldIssue()
					foldLink(t, s, made[0].ID, LinkTracks, first)
					foldLink(t, s, made[1].ID, LinkTracks, second)
				},
				check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
					// The carrier tracked the first issue and related the
					// second; the part taken out tracks its own again, and
					// the carrier keeps relating to it.
					foldWantLinks(t, s, made[1].ID, "tracks "+second)
					foldWantLinks(t, s, carrier.ID, "tracks "+first, "relates "+second)
					foldWantLinks(t, s, made[0].ID, "relates "+first)
				},
			}
		}(),
		func() unfoldRow {
			var y Bead
			var flips []string
			return unfoldRow{
				name: "its outgoing blocks are re-pointed back to it", depot: "plain", foldTo: "ready", to: "backlog",
				members: []NewBead{bead("a"), bead("b")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					y = outside(t, s, NewBead{Title: "y", Depot: "plain"})
					if err := s.AddDep(ctx, made[0].ID, y.ID, DepBlocks, "new", "merged", "mckean"); err != nil {
						t.Fatal(err)
					}
				},
				setup: func(t *testing.T, s *Store, carrier *Bead, made []Bead) {
					flips = foldGateFlips(t, s, y.ID)
				},
				check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
					a := made[0].ID
					foldWantEdges(t, s, a, DepBlocks, a+" -> "+y.ID+" at new until merged")
					foldWantEdges(t, s, carrier.ID, DepBlocks)
					// The holder changed, the gate did not: no flip is logged.
					if now := foldGateFlips(t, s, y.ID); !slices.Equal(now, flips) {
						t.Errorf("gate flips of %s = %v, were %v", y.ID, now, flips)
					}
				},
			}
		}(),
		func() unfoldRow {
			var y Bead
			return unfoldRow{
				name: "parts blocking one bead the same way restore each its own", depot: "plain", foldTo: "ready", to: "backlog",
				members: []NewBead{bead("a"), bead("b")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					y = outside(t, s, NewBead{Title: "y", Depot: "plain"})
					for _, m := range made {
						if err := s.AddDep(ctx, m.ID, y.ID, DepBlocks, "", "", "mckean"); err != nil {
							t.Fatal(err)
						}
					}
				},
				check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
					a, b, c := made[0].ID, made[1].ID, carrier.ID
					// The part taken out blocks again, and the carrier
					// still blocks for the part it holds.
					foldWantEdges(t, s, a, DepBlocks, a+" -> "+y.ID)
					foldWantEdges(t, s, c, DepBlocks, c+" -> "+y.ID)
					part, err := s.Unfold(ctx, c, b, "backlog", "the second part comes out too", "mckean")
					if err != nil {
						t.Fatal(err)
					}
					foldWant(t, "stage", part.Stage, "backlog")
					foldWantEdges(t, s, b, DepBlocks, b+" -> "+y.ID)
					foldWantEdges(t, s, c, DepBlocks)
				},
			}
		}(),
		func() unfoldRow {
			var y Bead
			return unfoldRow{
				name: "parts blocking one bead apart restore each its own edge", depot: "plain", foldTo: "ready", to: "backlog",
				members: []NewBead{bead("a"), bead("b")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					y = outside(t, s, NewBead{Title: "y", Depot: "plain"})
					if err := s.AddDep(ctx, made[0].ID, y.ID, DepBlocks, "", "merged", "mckean"); err != nil {
						t.Fatal(err)
					}
					if err := s.AddDep(ctx, made[1].ID, y.ID, DepBlocks, "", "ready", "mckean"); err != nil {
						t.Fatal(err)
					}
				},
				check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
					a, c := made[0].ID, carrier.ID
					foldWantEdges(t, s, a, DepBlocks, a+" -> "+y.ID+" until merged")
					foldWantEdges(t, s, c, DepBlocks, c+" -> "+y.ID+" until ready")
				},
			}
		}(),
		func() unfoldRow {
			var elsewhere Bead
			return unfoldRow{
				name: "its watchers watch it again, whoever holds them", depot: "plain", foldTo: "ready", to: "backlog",
				members: []NewBead{bead("a"), bead("b")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					a := made[0].ID
					elsewhere = outside(t, s, NewBead{Title: "elsewhere", Depot: "plain"})
					foldWatch(t, s, "crew", "ada", ScopeAll, a)
					foldWatch(t, s, "user", "sam", ScopeAll, a, "kind=noted")
					foldWatch(t, s, "group", "desk", ScopeAll, a)
					foldWatch(t, s, "user", "sam", ScopeAll, elsewhere.ID)
				},
				check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
					a, c := made[0].ID, carrier.ID
					for _, w := range []struct {
						kind, name string
						want       []string
					}{
						{"crew", "ada", []string{"all bead=" + a}},
						{"user", "sam", []string{"all bead=" + a + " kind=noted", "all bead=" + elsewhere.ID}},
						{"group", "desk", []string{"all bead=" + a}},
					} {
						slices.Sort(w.want)
						if got := foldWatches(t, s, w.kind, w.name, a, c, elsewhere.ID); !slices.Equal(got, w.want) {
							t.Errorf("watches of %s %s = %v, want %v", w.kind, w.name, got, w.want)
						}
					}
				},
			}
		}(),
		{
			name: "parts one watcher watches restore each its own", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")},
			prepare: func(t *testing.T, s *Store, made []Bead) {
				foldWatch(t, s, "user", "mckean", ScopeAll, made[0].ID)
				foldWatch(t, s, "user", "mckean", ScopeAll, made[1].ID)
			},
			check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
				a, b, c := made[0].ID, made[1].ID, carrier.ID
				// The part taken out is watched again, and the carrier
				// still is for the part it holds.
				want := []string{"all bead=" + a, "all bead=" + c}
				slices.Sort(want)
				if got := foldWatches(t, s, "user", "mckean", a, b, c); !slices.Equal(got, want) {
					t.Errorf("watches of mckean = %v, want %v", got, want)
				}
				if _, err := s.Unfold(ctx, c, b, "backlog", "the second part comes out too", "mckean"); err != nil {
					t.Fatal(err)
				}
				want = []string{"all bead=" + a, "all bead=" + b}
				slices.Sort(want)
				if got := foldWatches(t, s, "user", "mckean", a, b, c); !slices.Equal(got, want) {
					t.Errorf("watches of mckean = %v, want %v", got, want)
				}
			},
		},
		{
			name: "the folded edge is gone and the carrier's body gains the taken-out line", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")}, member: 1, reason: "part 2 breaks the gate on linux",
			check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
				b := made[1].ID
				foldWantEdges(t, s, carrier.ID, DepFolded, carrier.ID+" -> "+made[0].ID)
				stored, err := s.GetBead(ctx, carrier.ID)
				if err != nil {
					t.Fatal(err)
				}
				if want := "Part 2 (" + b + ") was taken out by mckean: part 2 breaks the gate on linux"; !strings.Contains(stored.Body, want) {
					t.Errorf("the carrier's body lacks %q:\n%s", want, stored.Body)
				}
			},
		},
		{
			name: "an unfolded event is logged on the carrier", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")}, reason: "part 1 breaks the gate on linux",
			check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
				data := unfoldedOf(t, carrier.ID)
				foldWant(t, "member", data["member"], any(made[0].ID))
				foldWant(t, "to", data["to"], any("backlog"))
				foldWant(t, "reason", data["reason"], any("part 1 breaks the gate on linux"))
			},
		},
		func() unfoldRow {
			var stranger Bead
			return unfoldRow{
				name: "a bead that is not a member of the carrier is refused", depot: "plain", foldTo: "ready", to: "backlog",
				members: []NewBead{bead("a"), bead("b")},
				prepare: func(t *testing.T, s *Store, made []Bead) {
					stranger = outside(t, s, NewBead{Title: "stranger", Depot: "plain"})
				},
				pick:    func(made []Bead) string { return stranger.ID },
				wantErr: "is not a member of",
			}
		}(),
		{
			name: "a part taken out twice is refused the second time", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")},
			check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
				if _, err := s.Unfold(ctx, carrier.ID, made[0].ID, "backlog", "again", "mckean"); err == nil || !strings.Contains(err.Error(), "is not a member of") {
					t.Errorf("second unfold = %v, want not-a-member", err)
				}
			},
		},
		{
			name: "an open member is refused", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")},
			setup: func(t *testing.T, s *Store, carrier *Bead, made []Bead) {
				// Reopened by hand: open, and its folded edge still on record.
				if _, err := s.Reopen(ctx, made[0].ID, Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "is open",
		},
		{
			name: "a closed carrier is refused", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")},
			setup: func(t *testing.T, s *Store, carrier *Bead, made []Bead) {
				if _, err := s.CloseBead(ctx, carrier.ID, "landed", Expect{}, "mckean"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "takes a part out of an open carrier",
		},
		{
			name: "a blank reason is refused", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")}, reason: " ",
			wantErr: "needs the reason",
		},
		{
			name: "a --to the member's flow lacks is refused", depot: "web", foldTo: "ready",
			members: []NewBead{pinned("a", "fold.f"), pinned("b", "fold.f")}, to: "ship",
			wantErr: "stage ship is not in flow fold.f",
		},
		{
			name: "a terminal --to is refused", depot: "web", foldTo: "ready",
			members: []NewBead{pinned("a", "fold.f"), pinned("b", "fold.f")}, to: "merged",
			wantErr: "stage merged is terminal in flow fold.f",
		},
		{
			// plain-pane is claimed and open in the pane this process is the
			// shell of; the carrier it unfolds from is not its own.
			name: "an agent in another bead's pane is refused", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{{ID: "plain-pane", Title: "mine"}, bead("b"), bead("c")},
			fold:    []int{1, 2}, member: 1, pane: true,
			prepare: func(t *testing.T, s *Store, made []Bead) { foldPane(t, s, made[0]) },
			wantErr: "may unfold the members of its own carrier",
		},
		{
			name: "the carrier's own session may unfold", depot: "plain", foldTo: "ready", to: "backlog",
			members: []NewBead{bead("a"), bead("b")}, pane: true,
			setup: func(t *testing.T, s *Store, carrier *Bead, made []Bead) {
				foldPane(t, s, *carrier)
			},
			check: func(t *testing.T, s *Store, carrier *Bead, made []Bead, out *Bead) {
				foldWant(t, "status", out.Status, "open")
			},
		},
		{
			// Ready is past fold.f's first stage and worked by nobody
			// here: the carrier's own session takes a part out into its
			// flow's first stage or one people work, as a created bead,
			// since into review or approved it would land without the
			// stages before it (checkAgentStage).
			name: "the carrier's own session may not unfold past the flow's first stage", depot: "web", foldTo: "ready", to: "ready",
			members: []NewBead{pinned("a", "fold.f"), pinned("b", "fold.f")}, pane: true,
			setup: func(t *testing.T, s *Store, carrier *Bead, made []Bead) {
				foldPane(t, s, *carrier)
			},
			wantErr: "may take a part out only in",
		},
	}

	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			st := s
			if row.pane {
				// A pane names this process, so one yard holds one pane row.
				st = foldYard(t)
			}
			made := make([]Bead, len(row.members))
			for i, nb := range row.members {
				if nb.Depot == "" {
					nb.Depot = row.depot
				}
				b, err := st.CreateBead(ctx, nb, "mckean")
				if err != nil {
					t.Fatal(err)
				}
				made[i] = *b
			}
			if row.prepare != nil {
				row.prepare(t, st, made)
			}
			var ids []string
			for _, i := range foldOrder(row.fold, len(made)) {
				ids = append(ids, made[i].ID)
			}
			carrier, err := st.FoldBeads(ctx, ids, FoldOptions{To: row.foldTo}, "mckean")
			if err != nil {
				t.Fatal(err)
			}
			if row.setup != nil {
				row.setup(t, st, carrier, made)
			}
			// A bead's revision changes with anything done to it: a reopen,
			// an edge, a link, a note.
			revisions := func() []string {
				var out []string
				for _, id := range append([]string{carrier.ID}, ids...) {
					b, err := st.GetBead(ctx, id)
					if err != nil {
						t.Fatal(err)
					}
					out = append(out, b.Revision)
				}
				return out
			}
			unfolded := func() int {
				evs, err := st.EventsOfKind(ctx, "unfolded", 1000)
				if err != nil {
					t.Fatal(err)
				}
				return len(evs)
			}
			was, unfoldedBefore := revisions(), unfolded()
			askedAs := ctx
			if row.pane {
				askedAs = WithCaller(ctx, os.Getpid())
			}
			member := made[row.member].ID
			if row.pick != nil {
				member = row.pick(made)
			}
			out, err := st.Unfold(askedAs, carrier.ID, member, row.to, or(row.reason, "why it comes out"), or(row.actor, "mckean"))
			if row.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), row.wantErr) {
					t.Fatalf("unfolded %s from %s: %+v, %v; want an error with %q", member, carrier.ID, out, err, row.wantErr)
				}
				// All or nothing: no member reopened or given an edge back,
				// no unfold on record.
				if now := revisions(); !slices.Equal(now, was) {
					t.Errorf("a refused unfold changed its beads: revisions %v, were %v", now, was)
				}
				foldWant(t, "unfolded events after a refused unfold", unfolded(), unfoldedBefore)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			foldWant(t, "unfolded events made", unfolded()-unfoldedBefore, 1)
			// What the unfold answers is what the store holds.
			stored, err := st.GetBead(ctx, out.ID)
			if err != nil || stored.Revision != out.Revision {
				t.Fatalf("the member answered is at %v, the bead stored %+v, %v", out.Revision, stored, err)
			}
			row.check(t, st, carrier, made, stored)
		})
	}
}
