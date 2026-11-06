package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// The prime tells a crew member how to use the yard's memory and
// journal: what goes where, when to read, and whose memory is home.
func TestPrimeTeachesMemoryAndJournal(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	for _, want := range []string{
		"yardr remember - -b - with the words on stdin, the title its first line and the body",
		"the title is what a later session",
		"so let it say the rule",
		"yardr crew journal yardmaster",
		"what happened and why, to look up later",
		"What the next session must do or watch goes in the handoff",
		"what concerns one bead",
		`yardr recall <words>`,
		"yardr bead show <mem-id> reads one memory",
		"This session has no memory of its own",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime has no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "lasting knowledge") {
		t.Errorf("the old remember line is still there:\n%s", out)
	}
}

// With more memories than the cap, the prime prints the cap, newest
// first, and the line about the rest.
func TestPrimePrintsCappedMemories(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	for i := 0; i < store.PrimeMemories+2; i++ {
		if _, err := st.Remember(ctx, store.NewMemory{Title: fmt.Sprintf("standing order %02d", i)}, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	if got := strings.Count(out, "standing order "); got != store.PrimeMemories {
		t.Fatalf("prime prints %d titles, want %d:\n%s", got, store.PrimeMemories, out)
	}
	if !strings.Contains(out, "... and 2 earlier not shown: yardr recall finds them") {
		t.Fatalf("prime names no rest:\n%s", out)
	}
	if first, last := strings.Index(out, "standing order 21"), strings.Index(out, "standing order 02"); first < 0 || last < 0 || first > last {
		t.Fatalf("titles are not newest first:\n%s", out)
	}
}

// The prime's omitted-entries line points at a command that reads them:
// prime --bead, whose notes are the journal whole.
func TestPrimeOmittedJournalPointsAtPrimeBead(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	if _, err := st.Journal(ctx, "yardmaster", store.NoteHandoff, "handoff", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.PrimeEntries+1; i++ {
		if _, err := st.Journal(ctx, "yardmaster", store.NoteJournal, fmt.Sprintf("entry %d", i), "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	if !strings.Contains(out, "1 earlier not shown: yardr prime --bead yardmaster-journal reads them") {
		t.Fatalf("omitted line:\n%s", out)
	}
}

// yardr recall <words> finds journal entries too: the journal read
// back. Without a query it lists memories alone, as before.
func TestRecallFindsJournalEntries(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	yardEnv(t, st)["YARDR_CREW"] = "yardmaster"
	if _, err := st.Journal(ctx, "yardmaster", store.NoteJournal, "deploys are frozen until Friday", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Remember(ctx, store.NewMemory{Title: "Deploy freeze", Body: "frozen until Friday", Scope: "crew:yardmaster"}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	out := mustAs(t, st, "yardmaster", "recall", "frozen")
	if !strings.Contains(out, "Deploy freeze") {
		t.Fatalf("recall lost the memory:\n%s", out)
	}
	if !strings.Contains(out, `Journal entries matching "frozen":`) || !strings.Contains(out, "deploys are frozen until Friday") {
		t.Fatalf("recall found no journal entry:\n%s", out)
	}
	if out := mustAs(t, st, "yardmaster", "recall"); strings.Contains(out, "Journal entries matching") {
		t.Fatalf("recall without a query lists the journal:\n%s", out)
	}
	var got struct {
		Memories []store.MemoryInfo `json:"memories"`
		Journal  []store.JournalHit `json:"journal"`
		Omitted  int                `json:"journal_omitted"`
	}
	if err := json.Unmarshal([]byte(mustAs(t, st, "yardmaster", "recall", "frozen", "--json")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Memories) != 1 || len(got.Journal) != 1 || got.Journal[0].Crew != "yardmaster" ||
		got.Journal[0].Note.Text != "deploys are frozen until Friday" || got.Omitted != 0 {
		t.Fatalf("recall --json = %+v", got)
	}

	// The words need no quotes: each argument is a word of one query, and
	// an entry that holds any of them is found.
	if _, err := st.Remember(ctx, store.NewMemory{Title: "Worktree cleanup kept a half-removed tree", Scope: "crew:yardmaster"}, "yardmaster"); err != nil {
		t.Fatal(err)
	}
	out = mustAs(t, st, "yardmaster", "recall", "worktree", "cleanup")
	if !strings.Contains(out, "Worktree cleanup kept a half-removed tree") || strings.Contains(out, "Deploy freeze") {
		t.Fatalf("recall worktree cleanup:\n%s", out)
	}
	out = mustAs(t, st, "yardmaster", "recall", "frozen", "friday")
	if !strings.Contains(out, "Deploy freeze") || !strings.Contains(out, `Journal entries matching "frozen friday":`) ||
		!strings.Contains(out, "deploys are frozen until Friday") {
		t.Fatalf("recall frozen friday:\n%s", out)
	}

	// More entries answer than a recall prints: the best are printed, and a
	// line counts the rest.
	for i := 0; i <= store.RecallHits; i++ {
		if _, err := st.Journal(ctx, "yardmaster", store.NoteJournal, fmt.Sprintf("deploy window %02d", i), "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	out = mustAs(t, st, "yardmaster", "recall", "deploy", "window")
	if n := strings.Count(out, "- [yardmaster "); n != store.RecallHits {
		t.Fatalf("recall deploy window prints %d journal entries, want %d:\n%s", n, store.RecallHits, out)
	}
	if !strings.Contains(out, "deploy window 20") || strings.Contains(out, "deploy window 00") || strings.Contains(out, "deploys are frozen") {
		t.Fatalf("recall deploy window does not keep the best, newest first:\n%s", out)
	}
	if !strings.HasSuffix(out, "... and 2 more: narrow the query\n") {
		t.Fatalf("recall deploy window counts no rest:\n%s", out)
	}
	got.Journal, got.Omitted = nil, 0
	if err := json.Unmarshal([]byte(mustAs(t, st, "yardmaster", "recall", "deploy", "window", "--json")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Journal) != store.RecallHits || got.Omitted != 2 {
		t.Fatalf("recall --json = %d entries, %d omitted; want %d, 2", len(got.Journal), got.Omitted, store.RecallHits)
	}
}

// remember - and -b - take the title and the memory from the command's
// stdin, trailing newlines trimmed as before. Stdin can be read once, so
// with both the first line is the title and the lines after it the body, and
// a title alone is one line. A title with no words is refused and remembers
// nothing.
func TestRememberStdin(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	a := appOn(st)
	a.actor = "jo"
	remember := func(stdin string, args ...string) (*store.Bead, error) {
		c := a.rememberCmd()
		c.SetIn(strings.NewReader(stdin))
		out, err := run(t, c, args...)
		if err != nil {
			return nil, err
		}
		return st.GetBead(ctx, strings.TrimSpace(out))
	}
	got, err := remember("no webapp deploys until Friday\n\n", "Deploy freeze", "-b", "-")
	if err != nil {
		t.Fatalf("remember -b -: %v", err)
	}
	if got.Title != "Deploy freeze" || got.Body != "no webapp deploys until Friday" {
		t.Fatalf("remember -b - wrote %+v", got)
	}
	// The title from stdin, as typed: a code span and $( ) stay text.
	got, err = remember("the `title`, $(not run)\n\n", "-")
	if err != nil {
		t.Fatalf("remember -: %v", err)
	}
	if got.Title != "the `title`, $(not run)" || got.Body != "" {
		t.Fatalf("remember - wrote %+v", got)
	}
	got, err = remember("  the title \t\n\nthe body\nwith two lines\n\n", "-", "-b", "-")
	if err != nil {
		t.Fatalf("remember - -b -: %v", err)
	}
	if got.Title != "  the title \t" || got.Body != "the body\nwith two lines" {
		t.Fatalf("remember - -b - wrote %+v", got)
	}
	head, err := st.Head(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		stdin string
		flags []string
		want  string
	}{
		{"", []string{"-"}, "nothing to remember: stdin gave no title"},
		{" \n\n", []string{"-"}, "nothing to remember: stdin gave no title"},
		{"\n\nthe body\n", []string{"-", "-b", "-"}, "nothing to remember: stdin gave no title"},
		{"one\ntwo\n", []string{"-"}, "a title is one line, and stdin gave 2"},
	} {
		if _, err := remember(row.stdin, row.flags...); err == nil || !strings.Contains(err.Error(), row.want) {
			t.Fatalf("remember %v with %q stdin: %v", row.flags, row.stdin, err)
		}
	}
	if now, err := st.Head(ctx); err != nil || now != head {
		t.Fatalf("refused remembers logged %d events, %v", now-head, err)
	}
}
