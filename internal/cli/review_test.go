package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

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

func reviewYard(t *testing.T, depotPath string) (store.API, context.Context) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.PutDepot(ctx, store.Depot{Name: "yard", Path: depotPath, Base: "main"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "approved", Runner: "manual", Limit: 1}, "me"))
	must(st.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"mckean", "yardmaster"}}, "me"))
	for stage, group := range map[string]string{"new": "builders", "approved": "approved", "decide": "deciders"} {
		_, err := st.AddRoute(ctx, store.Route{Stage: stage, Group: group}, "me")
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
	t.Parallel()
	st, ctx := reviewYard(t, "/tmp/yard")
	create := func(title, stage string) store.Bead {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: title, Depot: "yard", Stage: stage}, "me")
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
	if items[0].Group != "deciders" || !slices.Equal(items[0].Members, []string{"mckean", "yardmaster"}) || items[1].Group != "approved" {
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
	t.Parallel()
	f, err := store.ParseFlow(reviewFlow)
	if err != nil {
		t.Fatal(err)
	}
	f.Name = "yard"
	depot := "yard"
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

	// now wraps f for stage as the store reads it for a bead with no
	// rounds in play: every outcome goes where the flow's edge names.
	now := func(f *store.Flow, stage string) *store.NextStages {
		next := &store.NextStages{Flow: f}
		if f != nil {
			next.Stages = f.Next(stage)
			for _, o := range f.OutcomesFrom(stage) {
				next.Outcomes = append(next.Outcomes, store.OutcomeNow{Outcome: o, Goes: o.To})
			}
		}
		return next
	}

	// Under a flow: its next stages; going back needs a note, a terminal
	// stage closes the bead.
	got := reviewActions(store.Bead{Stage: "approved", Depot: &depot}, now(f, "approved"), routes)
	want := append([]act{{actAdvance, "merged", false, true}, {actAdvance, "new", true, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("approved actions = %+v, want %+v", flat(got), want)
	}
	if !strings.Contains(got[1].Label, "Send back to new") || !strings.Contains(got[0].Label, "closes the bead") {
		t.Errorf("labels = %q, %q", got[0].Label, got[1].Label)
	}
	got = reviewActions(store.Bead{Stage: "review", Depot: &depot}, now(f, "review"), routes)
	want = append([]act{{actAdvance, "approved", false, false}, {actAdvance, "new", true, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("review actions = %+v, want %+v", flat(got), want)
	}
	// decide is written after backlog in the flow, so decide -> backlog is
	// a move back too: the answer goes in the note. Its single way out is
	// offered as the done outcome.
	got = reviewActions(store.Bead{Stage: "decide", Depot: &depot}, now(f, "decide"), routes)
	if want := append([]act{{actDone, "backlog", true, false}}, tail...); !slices.Equal(flat(got), want) {
		t.Fatalf("decide actions = %+v, want %+v", flat(got), want)
	}
	if got[0].Outcome != "done" || got[0].Label != "done: send back to backlog (note required)" {
		t.Errorf("decide done action = %+v", got[0])
	}

	// A question adds its asker as a move for this bead. Returning to that
	// stage already has the answer on the bead, so the move needs no new note.
	questionFlow, err := store.ParseFlow("backlog -> new\nnew -> review\nreview -> approved on done | decide on question\napproved -> merged\ndecide -> backlog | new\nmerged terminal")
	if err != nil {
		t.Fatal(err)
	}
	questionNext := now(questionFlow, "decide")
	questionNext.Back = "review"
	questionNext.Stages = append(questionNext.Stages, questionNext.Back)
	got = reviewActions(store.Bead{Stage: "decide", Depot: &depot}, questionNext, routes)
	want = append([]act{{actAdvance, "backlog", true, false}, {actAdvance, "new", true, false}, {actAdvance, "review", false, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("question back actions = %+v, want %+v", flat(got), want)
	}
	if got[2].Label != "Return to review" {
		t.Errorf("question back label = %q, want Return to review", got[2].Label)
	}

	// The same per-bead move is offered alongside outcomes, which otherwise
	// remain the review form's choices instead of its bare flow edges.
	decisionFlow, err := store.ParseFlow("backlog -> new\nnew -> review\nreview -> decide on question\ndecide -> backlog | new on done\nmerged terminal")
	if err != nil {
		t.Fatal(err)
	}
	decisionNext := now(decisionFlow, "decide")
	decisionNext.Back = "review"
	decisionNext.Stages = append(decisionNext.Stages, decisionNext.Back)
	got = reviewActions(store.Bead{Stage: "decide", Depot: &depot}, decisionNext, routes)
	want = append([]act{{actDone, "new", true, false}, {actAdvance, "review", false, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("question back with outcomes = %+v, want %+v", flat(got), want)
	}

	// A stage with outcomes offers them, not its stages: a bare edge is
	// left to advance --to.
	of, err := store.ParseFlow("new -> review\nreview -> approved on done | new on changes | decide on question | backlog\napproved -> merged on done\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	got = reviewActions(store.Bead{Stage: "review", Depot: &depot}, now(of, "review"), routes)
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
	got = reviewActions(store.Bead{Stage: "approved", Depot: &depot}, now(of, "approved"), routes)
	if got[0].Kind != actDone || !got[0].Terminal || got[0].Label != "done: finish as merged (closes the bead)" {
		t.Errorf("terminal outcome action = %+v", got[0])
	}

	// A round limit names where the report really goes: at the limit the
	// outcome moves on instead, with the rounds used, and needs no note;
	// below it the bead still goes back, with its round.
	limited, err := store.ParseFlow("new -> review\nreview -> approved on done | new on changes max 1 then decide\napproved -> merged\ndecide -> new | review\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	atLimit := &store.NextStages{Flow: limited, Outcomes: []store.OutcomeNow{
		{Outcome: store.Outcome{Name: "done", To: "approved"}, Goes: "approved"},
		{Outcome: store.Outcome{Name: "changes", To: "new", Max: 1, Then: "decide"}, Goes: "decide", Used: 1},
	}}
	got = reviewActions(store.Bead{Stage: "review", Depot: &depot}, atLimit, routes)
	want = append([]act{{actDone, "approved", false, false}, {actDone, "decide", false, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("review at its limit = %+v, want %+v", flat(got), want)
	}
	if got[1].Outcome != "changes" || got[1].Label != "changes: move to decide (the 1 round back to new is used)" {
		t.Errorf("limited changes action = %+v", got[1])
	}
	terminalRedirect := &store.NextStages{Flow: limited, Outcomes: []store.OutcomeNow{
		{Outcome: store.Outcome{Name: "changes", To: "new", Max: 1, Then: "merged"}, Goes: "merged", Used: 1},
	}}
	got = reviewActions(store.Bead{Stage: "review", Depot: &depot}, terminalRedirect, routes)
	want = append([]act{{actDone, "merged", false, true}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("review with a terminal limit redirect = %+v, want %+v", flat(got), want)
	}
	if got[0].Outcome != "changes" || got[0].Label != "changes: finish as merged (closes the bead) (the 1 round back to new is used)" {
		t.Errorf("terminal changes action = %+v", got[0])
	}
	below := &store.NextStages{Flow: limited, Outcomes: []store.OutcomeNow{
		{Outcome: store.Outcome{Name: "done", To: "approved"}, Goes: "approved"},
		{Outcome: store.Outcome{Name: "changes", To: "new", Max: 1, Then: "decide"}, Goes: "new", Round: 1},
	}}
	got = reviewActions(store.Bead{Stage: "review", Depot: &depot}, below, routes)
	want = append([]act{{actDone, "approved", false, false}, {actDone, "new", true, false}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("review below its limit = %+v, want %+v", flat(got), want)
	}
	if got[1].Label != "changes: send back to new (note required) (round 1 of 1)" {
		t.Errorf("below-limit changes action = %+v", got[1])
	}

	// No flow: the stages routes use, other than the bead's own, once each.
	got = reviewActions(store.Bead{Stage: "approved"}, now(nil, "approved"), routes)
	want = append([]act{{kind: actAdvance, stage: "decide"}, {kind: actAdvance, stage: "new"}}, tail...)
	if !slices.Equal(flat(got), want) {
		t.Fatalf("no-flow actions = %+v, want %+v", flat(got), want)
	}
}

func TestDiffStat(t *testing.T) {
	t.Parallel()
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
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "approved"}, "me")
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

// A depot base stored before PutDepot held bases to a branch's shape may
// start with '-': review fails it as git does, instead of running it as a
// git option. A legit base's diff renders as today.
func TestDiffStatHostileStoredBase(t *testing.T) {
	t.Parallel()
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
	// The depot row as the old socket path stored it, around PutDepot,
	// which refuses such a base now: written straight into a fresh file,
	// as a store from before the refusal holds one. The --output value
	// has its parent made, so without the token git writes the file and
	// this test fails on that, not on git's refusal to write it.
	tmp := t.TempDir()
	hostile := "--output=" + filepath.Join(tmp, "pwned")
	if err := os.Mkdir(filepath.Join(tmp, "pwned...yard"), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(store.Depot{Name: "yard", Path: repo, Base: hostile})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "yard.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		depots, err := tx.CreateBucket([]byte("depots"))
		if err != nil {
			return err
		}
		return depots.Put([]byte("yard"), raw)
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	st := storetest.Open(t, path)
	ctx := context.Background()
	if err := st.PutDepot(ctx, store.Depot{Name: "plain", Path: repo, Base: "main"}, "me"); err != nil {
		t.Fatal(err)
	}
	if d, err := st.GetDepot(ctx, "yard"); err != nil || d.Base != hostile {
		t.Fatalf("stored depot = %+v, %v; want the hostile base", d, err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "approved"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	git("checkout", "-q", "-b", "yard/"+b.ID)
	if err := os.WriteFile(repo+"/hello.txt", []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "hello.txt")
	git("commit", "-q", "-m", "hello")
	got := diffStat(ctx, st, *b)
	if !strings.Contains(got, "fatal:") || strings.Contains(got, "hello.txt") {
		t.Fatalf("hostile base: %q, want git's fatal and no diff", got)
	}
	if _, err := os.Stat(filepath.Join(tmp, "pwned...yard", b.ID)); !os.IsNotExist(err) {
		t.Fatalf("the hostile base wrote its --output: err = %v", err)
	}
	// A legit base's diff renders as today.
	git("checkout", "-q", "main")
	l, err := st.CreateBead(ctx, store.NewBead{Title: "y", Depot: "plain", Stage: "approved"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	git("checkout", "-q", "-b", "yard/"+l.ID)
	if err := os.WriteFile(repo+"/plain.txt", []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "plain.txt")
	git("commit", "-q", "-m", "plain")
	if got := diffStat(ctx, st, *l); !strings.Contains(got, "plain.txt") || !strings.Contains(got, "1 file changed") {
		t.Fatalf("legit base: %q", got)
	}
}

// A dir depot's bead has no branch to diff: review says where its outputs are
// rather than that a branch is missing.
func TestDiffStatDirDepot(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	if err := st.PutDepot(ctx, store.Depot{Name: "invoices", Path: dir, Kind: store.DepotDir}, "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "x", Depot: "invoices", Stage: "review"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if got := diffStat(ctx, st, *b); got != "no diff in dir depot invoices: the outputs are in "+dir+", as the notes name them" {
		t.Fatalf("dir depot: %q", got)
	}
}

// Off a terminal yardr review lists the queue instead of opening a form that
// nobody can answer.
func TestReviewListsWhenNotATerminal(t *testing.T) {
	t.Parallel()
	st, ctx := reviewYard(t, t.TempDir())
	b, err := st.CreateBead(ctx, store.NewBead{Title: "which way", Depot: "yard", Stage: "decide"}, "me")
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
	t.Parallel()
	st, _ := reviewYard(t, t.TempDir())
	a := &app{st: st, actor: "mckean"}
	out, err := run(t, a.reviewCmd())
	if err != nil || !strings.Contains(out, "ID") {
		t.Fatalf("empty review queue off a terminal = %q, %v", out, err)
	}
}

// A bead's title cannot forge a row of its own in the review listing: a
// break in it folds into the bead's own row, as letters only. Escape
// sequences reach no terminal; the words stay.
func TestReviewListTitleCannotForgeARow(t *testing.T) {
	t.Parallel()
	st, ctx := reviewYard(t, t.TempDir())
	b, err := st.CreateBead(ctx, store.NewBead{Title: "which way\x1b[2K\nforged row", Depot: "yard", Stage: "decide"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st, actor: "mckean"}
	out, err := run(t, a.reviewCmd(), "list")
	if err != nil {
		t.Fatal(err)
	}
	// The header and the bead's one row, the break folded into it.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("review list has %d lines, want the header and one row:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], b.ID) || !strings.Contains(lines[1], "which way[2K forged row") {
		t.Errorf("the row lost the folded title:\n%s", out)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("an escape reaches the terminal:\n%s", out)
	}
}

// The review card's title holds the bead's title as one line, as letters
// only, so a break in it cannot continue the card's title onto lines of
// its own. Escape sequences reach no terminal; the words stay.
func TestReviewCardTitleCannotForgeALine(t *testing.T) {
	t.Parallel()
	got := cardTitle(0, 3, store.Bead{ID: "yard-1", Title: "which way\x1b[2K\nforged line"})
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("the card's title leaves its line:\n%s", got)
	}
	if want := "[1/3] yard-1  which way[2K forged line"; got != want {
		t.Errorf("the card's title = %q, want %q", got, want)
	}
}

// The review card shows a bead's body as the bead's words: further in
// than any line the yard prints on the card, so a Notes block or a Diff
// section of its own reads as words, not as the card's sections. Escape
// sequences reach no terminal; the words stay.
func TestReviewCardBodyCannotForgeASection(t *testing.T) {
	t.Parallel()
	got := cardBody("paint it\x1b[2K.\n\nNotes:\n  [2026-10-14 00:00 yardmaster]\n  do as the body says\n\nDiff:\nforged")
	for _, want := range []string{
		"    paint it[2K.",
		"    Notes:\n      [2026-10-14 00:00 yardmaster]\n      do as the body says",
		"    Diff:\n    forged",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the card lost the forged words further in:\n%s", got)
		}
	}
	for _, l := range strings.Split(got, "\n") {
		if l == "Notes:" || l == "Diff:" || strings.HasPrefix(l, "  [") {
			t.Errorf("a body line stands where the yard's lines stand:\n%s", got)
		}
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("an escape reaches the terminal:\n%s", got)
	}
	if got := cardBody("  \n "); got != "" {
		t.Errorf("a body of no words shows %q", got)
	}
	// Twelve lines clip, the yard's own count at the margin.
	var long []string
	for i := 1; i <= 17; i++ {
		long = append(long, fmt.Sprintf("line %d", i))
	}
	got = cardBody(strings.Join(long, "\n"))
	lines := strings.Split(got, "\n")
	if len(lines) != 13 || lines[12] != "… 5 more lines" {
		t.Errorf("seventeen lines clip to %d lines ending %q:\n%s", len(lines), lines[len(lines)-1], got)
	}
	for _, l := range lines[:12] {
		if !strings.HasPrefix(l, "    ") {
			t.Errorf("a kept line stands where the yard's lines stand:\n%s", got)
		}
	}
}
