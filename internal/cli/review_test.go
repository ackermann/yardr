package cli

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

const reviewFlow = `
backlog -> new
new -> review | backlog
review -> approved | new
approved -> merged | new
decide -> backlog
merged terminal
`

func reviewYard(t *testing.T, rigPath string) (store.API, context.Context) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutRig(ctx, store.Rig{Name: "yard", Path: rigPath, Base: "main"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}))
	must(st.PutGroup(ctx, store.Group{Name: "approved", Runner: "manual", Limit: 1}))
	must(st.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"mckean", "mayor"}}))
	for stage, group := range map[string]string{"new": "builders", "approved": "approved", "decide": "deciders"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Group: group})
		must(err)
	}
	f, err := store.ParseFlow(reviewFlow)
	must(err)
	f.Name = "yard"
	_, err = st.PutFlow(ctx, *f, "me")
	must(err)
	must(st.SetFlowPointer(ctx, "yard", "", "yard", "me"))
	return st, ctx
}

func TestReviewQueue(t *testing.T) {
	st, ctx := reviewYard(t, "/tmp/yard")
	create := func(title, stage string) store.Bead {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: title, Rig: "yard", Stage: stage}, "me")
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // distinct UpdatedAt
		return *b
	}
	approvedNew := create("approved later", "approved")
	create("building", "new")
	create("no route", "review")
	question := create("a question", "decide")
	approvedOld := create("approved first", "approved")
	// A bead claimed by the manual group (it is at its limit of one) is
	// still listed, by its owning group.
	if _, err := st.Claim(ctx, approvedOld.ID, "approved", "approved", "yard"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	// Touching the other approved bead makes approvedOld the longest untouched.
	if err := st.AddNote(ctx, approvedNew.ID, "poke", "me"); err != nil {
		t.Fatal(err)
	}
	ids := func(items []reviewItem) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Bead.ID)
		}
		return out
	}

	// A member sees the decide bead; herdr and unrouted stages are not human.
	items, err := reviewQueue(ctx, st, "mckean", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(items), []string{question.ID, approvedOld.ID, approvedNew.ID}; !slices.Equal(got, want) {
		t.Fatalf("mckean's queue = %v, want %v", got, want)
	}
	if items[0].Group != "deciders" || !slices.Equal(items[0].Members, []string{"mckean", "mayor"}) || items[1].Group != "approved" {
		t.Errorf("groups = %+v", items)
	}
	// Anyone else only sees the member-less group's beads.
	items, err = reviewQueue(ctx, st, "yard-21-new", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids(items), []string{approvedOld.ID, approvedNew.ID}; !slices.Equal(got, want) {
		t.Fatalf("non-member queue = %v, want %v", got, want)
	}
	// --group narrows to the named groups.
	if items, _ = reviewQueue(ctx, st, "mckean", []string{"deciders"}); !slices.Equal(ids(items), []string{question.ID}) {
		t.Fatalf("deciders only = %v", ids(items))
	}
	// Closed beads drop out.
	if _, err := st.AdvanceNote(ctx, approvedOld.ID, "merged", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if items, _ = reviewQueue(ctx, st, "yard-21-new", nil); !slices.Equal(ids(items), []string{approvedNew.ID}) {
		t.Fatalf("after merge = %v", ids(items))
	}
}

func TestReviewActions(t *testing.T) {
	f, err := store.ParseFlow(reviewFlow)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	rig := "yard"
	routes := []store.Route{{Stage: "new"}, {Stage: "approved"}, {Stage: "decide"}, {Stage: "new"}}

	type act struct {
		kind, stage string
		note, term  bool
	}
	flat := func(as []reviewAction) []act {
		var out []act
		for _, a := range as {
			out = append(out, act{a.Kind, a.Stage, a.NoteRequired, a.Terminal})
		}
		return out
	}
	tail := []act{{kind: actDiff}, {kind: actSkip}}

	// Under a flow: its next stages; going back needs a note, a terminal
	// stage closes the bead.
	got := reviewActions(store.Bead{Stage: "approved", Rig: &rig}, f, routes)
	want := append([]act{{actAdvance, "merged", false, true}, {actAdvance, "new", true, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("approved actions = %+v, want %+v", flat(got), want)
	}
	if !strings.Contains(got[1].Label, "Send back to new") || !strings.Contains(got[0].Label, "closes the bead") {
		t.Errorf("labels = %q, %q", got[0].Label, got[1].Label)
	}
	got = reviewActions(store.Bead{Stage: "review", Rig: &rig}, f, routes)
	want = append([]act{{actAdvance, "approved", false, false}, {actAdvance, "new", true, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("review actions = %+v, want %+v", flat(got), want)
	}
	// decide is written after backlog in the flow, so decide -> backlog is
	// a move back too: the answer goes in the note. Its single way out is
	// offered as the done outcome.
	got = reviewActions(store.Bead{Stage: "decide", Rig: &rig}, f, routes)
	if want := append([]act{{actDone, "backlog", true, false}}, tail...); !slices.Equal(flat(got), want) {
		t.Fatalf("decide actions = %+v, want %+v", flat(got), want)
	}
	if got[0].Outcome != "done" || got[0].Label != "done: send back to backlog (note required)" {
		t.Errorf("decide done action = %+v", got[0])
	}

	// A stage with outcomes offers them, not its stages: a bare edge is
	// left to advance --to.
	of, err := store.ParseFlow("new -> review\nreview -> approved on done | new on changes | decide on question | backlog\napproved -> merged on done\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	got = reviewActions(store.Bead{Stage: "review", Rig: &rig}, of, routes)
	want = append([]act{{actDone, "approved", false, false}, {actDone, "new", true, false}, {actDone, "decide", false, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("review outcome actions = %+v, want %+v", flat(got), want)
	}
	var labels []string
	for _, a := range got[:3] {
		labels = append(labels, a.Outcome+"|"+a.Label)
	}
	if wantL := []string{"done|done: move to approved", "changes|changes: send back to new (note required)", "question|question: move to decide"}; !slices.Equal(labels, wantL) {
		t.Errorf("labels = %q, want %q", labels, wantL)
	}
	got = reviewActions(store.Bead{Stage: "approved", Rig: &rig}, of, routes)
	if got[0].Kind != actDone || !got[0].Terminal || got[0].Label != "done: finish as merged (closes the bead)" {
		t.Errorf("terminal outcome action = %+v", got[0])
	}

	// No flow: the stages routes use, other than the bead's own, once each.
	got = reviewActions(store.Bead{Stage: "approved"}, nil, routes)
	want = append([]act{{kind: actAdvance, stage: "decide"}, {kind: actAdvance, stage: "new"}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("no-flow actions = %+v, want %+v", flat(got), want)
	}
}

func TestDiffStat(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("commit", "-q", "--allow-empty", "-m", "base")
	st, ctx := reviewYard(t, repo)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Rig: "yard", Stage: "approved"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if got := diffStat(ctx, st, *b); !strings.Contains(got, "no branch yard/"+b.ID) {
		t.Fatalf("missing branch: %q", got)
	}
	git("checkout", "-q", "-b", "yard/"+b.ID)
	if got := diffStat(ctx, st, *b); got != "no changes against main...yard/"+b.ID {
		t.Fatalf("empty branch: %q", got)
	}
	if err := os.WriteFile(repo+"/hello.txt", []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "hello.txt")
	git("commit", "-q", "-m", "hello")
	// main moving on does not show up: the diff is from the merge base.
	git("checkout", "-q", "main")
	if err := os.WriteFile(repo+"/other.txt", []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "other.txt")
	git("commit", "-q", "-m", "other")
	got := diffStat(ctx, st, *b)
	if !strings.Contains(got, "hello.txt") || strings.Contains(got, "other.txt") || !strings.Contains(got, "1 file changed") {
		t.Fatalf("stat = %q", got)
	}
}

// A dir rig's bead has no branch to diff: review says where its outputs are
// rather than that a branch is missing.
func TestDiffStatDirRig(t *testing.T) {
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := st.PutRig(ctx, store.Rig{Name: "invoices", Path: dir, Kind: store.RigDir}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Rig: "invoices", Stage: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if got := diffStat(ctx, st, *b); got != "no diff in dir rig invoices: the outputs are in "+dir+", as the notes name them" {
		t.Fatalf("dir rig: %q", got)
	}
}

// Off a terminal yardr review lists the queue instead of opening a form that
// nobody can answer.
func TestReviewListsWhenNotATerminal(t *testing.T) {
	st, ctx := reviewYard(t, t.TempDir())
	b, err := st.CreateBead(ctx, store.NewBead{Title: "which way", Rig: "yard", Stage: "decide"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "mckean"}
	out, err := run(t, a.reviewCmd())
	if err != nil || !strings.Contains(out, "ID") || !strings.Contains(out, b.ID) || !strings.Contains(out, "which way") {
		t.Fatalf("review off a terminal = %q, %v", out, err)
	}
}

func TestReviewListsEmptyQueueWhenNotATerminal(t *testing.T) {
	st, _ := reviewYard(t, t.TempDir())
	a := &app{st: st, actor: "mckean"}
	out, err := run(t, a.reviewCmd())
	if err != nil || !strings.Contains(out, "ID") {
		t.Fatalf("empty review queue off a terminal = %q, %v", out, err)
	}
}
