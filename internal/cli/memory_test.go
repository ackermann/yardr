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
		`yardr remember "<title>" -b "<what, who said it and when, why>"`,
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
	if err := st.Journal(ctx, "yardmaster", store.NoteHandoff, "handoff", "yardmaster"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < store.PrimeEntries+1; i++ {
		if err := st.Journal(ctx, "yardmaster", store.NoteJournal, fmt.Sprintf("entry %d", i), "yardmaster"); err != nil {
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
	if err := st.Journal(ctx, "yardmaster", store.NoteJournal, "deploys are frozen until Friday", "yardmaster"); err != nil {
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
	}
	if err := json.Unmarshal([]byte(mustAs(t, st, "yardmaster", "recall", "frozen", "--json")), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Memories) != 1 || len(got.Journal) != 1 || got.Journal[0].Crew != "yardmaster" ||
		got.Journal[0].Note.Text != "deploys are frozen until Friday" {
		t.Fatalf("recall --json = %+v", got)
	}
}

// remember -b - takes the memory from the command's stdin, trailing
// newlines trimmed as before.
func TestRememberStdinBody(t *testing.T) {
	t.Parallel()
	st := userYard(t)
	ctx := context.Background()
	a := appOn(st)
	a.actor = "jo"
	c := a.rememberCmd()
	c.SetIn(strings.NewReader("no webapp deploys until Friday\n\n"))
	out, err := run(t, c, "Deploy freeze", "-b", "-")
	if err != nil {
		t.Fatalf("remember -b -: %v\n%s", err, out)
	}
	got, err := st.GetBead(ctx, strings.TrimSpace(out))
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "no webapp deploys until Friday" {
		t.Fatalf("remember -b - wrote body %q", got.Body)
	}
}
