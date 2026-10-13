package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// A fresh yard's yardmaster still primes with its First run section: the
// setup's steps are its job until every step is done.
func TestPrimeCrewKeepsFirstRunWhileSetupTodo(t *testing.T) {
	st := userYard(t)
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	for _, want := range []string{"## First run", "Ask for the repository's path", "yardr depot init"} {
		if !strings.Contains(out, want) {
			t.Errorf("a fresh yard's prime lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, setupDoneLine) {
		t.Errorf("a fresh yard's prime says the setup is done:\n%s", out)
	}
}

// Once the setup is complete, the yardmaster's prime prints one line
// where its First run section was: the section is read at every start
// of every session, and a set-up yard has no use for it.
func TestPrimeCrewReplacesFirstRunWhenSetupComplete(t *testing.T) {
	// As userYard but without its chdir: gitDepot reads testdata by
	// path, which a chdir away from the package leaves behind.
	newHome(t)
	fakeTools(t, map[string]string{"git": toolAnswers, "herdr": toolAnswers, "claude": toolAnswers})
	for _, k := range []string{"ACTOR", "BEAD", "SESSION", "CREW"} {
		t.Setenv("YARDR_"+k, "")
	}
	st := storetest.New(t)
	if out, err := run(t, (&app{st: st, actor: "jo"}).rootCmd(), "init", "--harness", "claude", "--operator", "jo"); err != nil {
		t.Fatalf("init: %v\n%s", err, out)
	}
	ctx := context.Background()
	fresh := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")

	depot := gitDepot(t, st)
	commitDepot(t, depot)
	for _, g := range []store.Group{
		{Name: "web/builders", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "web/reviewers", Runner: "herdr", Config: map[string]any{"kind": "claude"}},
		{Name: "web/mergers", Runner: "manual"},
	} {
		if err := st.PutGroup(ctx, g, "me"); err != nil {
			t.Fatal(err)
		}
	}
	for stage, group := range map[string]string{"new": "web/builders", "review": "web/reviewers", "approved": "web/mergers"} {
		if _, err := st.AddRoute(ctx, store.Route{Stage: stage, Depot: &depot.Name, Group: group}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "first", Depot: depot.Name}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"new", "review", "approved", "merged"} {
		if _, err := st.Advance(ctx, b.ID, stage, store.Expect{}, "me"); err != nil {
			t.Fatalf("to %s: %v", stage, err)
		}
	}
	if steps, err := setupSteps(ctx, st); err != nil || nextStep(steps) != nil {
		t.Fatalf("the yard is not set up: %+v, %v", steps, err)
	}

	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	if !strings.Contains(out, setupDoneLine) {
		t.Errorf("a set-up yard's prime lacks the setup-done line:\n%s", out)
	}
	for _, not := range []string{"## First run", "Ask for the repository's path", "yardr depot init"} {
		if strings.Contains(out, not) {
			t.Errorf("a set-up yard's prime still holds %q:\n%s", not, out)
		}
	}
	// Shorter by about the section (some 4 KB), not by a line.
	if d := len(fresh) - len(out); d < 3000 {
		t.Errorf("a set-up yard's prime is shorter by %d bytes, want over 3000", d)
	}
	// The rest of the role is still there, around the line.
	if i, j, k := strings.Index(out, "You coordinate the work"), strings.Index(out, setupDoneLine), strings.Index(out, "## The work"); i < 0 || j < 0 || k < 0 || !(i < j && j < k) {
		t.Errorf("the line is not where the section was:\n%s", out)
	}
	// JSON carries the same role text.
	js := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster", "--json")
	if !strings.Contains(js, setupDoneLine) || strings.Contains(js, "## First run") {
		t.Errorf("prime --json keeps the section:\n%s", js)
	}
}

// prime --crew lists the crew member's open asks after the timers: the two
// open and the one put aside, oldest first with their states, and neither
// the answered one nor another crew member's; bead list --asked-by lists
// the same rows, which is what the section's overflow line names.
func TestPrimeCrewOpenAsks(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	mustAs(t, st, "jo", "crew", "add", "clerk", "--set", "kind=claude")

	// None yet: the section is one line, as the inbox line is.
	if out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster"); !strings.Contains(out, "## Open asks: 0 waiting\n") {
		t.Fatalf("a crew member with no asks primes no one-line section:\n%s", out)
	}

	bead := func(title string) string {
		t.Helper()
		b, err := st.CreateBead(ctx, store.NewBead{Title: title}, "yardmaster")
		if err != nil {
			t.Fatal(err)
		}
		return b.ID
	}
	first, second := bead("first"), bead("second")
	ask := func(writer, id, question string) uint64 {
		t.Helper()
		seq, err := st.AddAsk(ctx, id, store.Ask{
			Question: question, To: "jo",
			Context: []string{"some context"},
			Options: []store.AskOption{{Label: "Yes", Consequence: "it happens"}},
		}, writer)
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	open1 := ask("yardmaster", first, "Move the links now?")
	open2 := ask("yardmaster", second, "And the docs?")
	aside := ask("yardmaster", first, "Put this one aside?")
	answered := ask("yardmaster", second, "Already decided?")
	ask("clerk", first, "The clerk's own question?")
	if err := st.DeferAsk(ctx, first, aside, "jo"); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerAsk(ctx, second, answered, 1, "", "jo"); err != nil {
		t.Fatal(err)
	}

	row := func(id string, seq uint64, question, state string) string {
		return fmt.Sprintf("- %s ask %d · to jo · %s · %s · ", id, seq, question, state)
	}
	// The answered ask tells its asker, so its question stands in the
	// inbox too: the section is what must not list it.
	sectionOf := func(out string) string {
		t.Helper()
		sec := out[strings.Index(out, "## Open asks:"):]
		if i := strings.Index(sec, "## How you work"); i >= 0 {
			sec = sec[:i]
		}
		return sec
	}
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	sec := sectionOf(out)
	for _, want := range []string{
		"## Open asks: 3 waiting (yardr bead list --asked-by yardmaster)",
		row(first, open1, "Move the links now?", "open"),
		row(second, open2, "And the docs?", "open"),
		row(first, aside, "Put this one aside?", "put aside"),
	} {
		if !strings.Contains(sec, want) {
			t.Errorf("the prime lacks %q:\n%s", want, sec)
		}
	}
	for _, not := range []string{"Already decided?", "The clerk's own question?"} {
		if strings.Contains(sec, not) {
			t.Errorf("the prime lists %q:\n%s", not, sec)
		}
	}
	// Oldest first.
	if i, j, k := strings.Index(sec, "Move the links now?"), strings.Index(sec, "And the docs?"), strings.Index(sec, "Put this one aside?"); !(i >= 0 && i < j && j < k) {
		t.Errorf("the asks are not oldest first:\n%s", sec)
	}
	// After the timers and before the how-to.
	mustAs(t, st, "yardmaster", "timer", "add", "--in", "40m", "look at the deploy")
	out = mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	if i, j, k := strings.Index(out, "## Timers"), strings.Index(out, "## Open asks"), strings.Index(out, "## How you work"); !(i >= 0 && i < j && j < k) {
		t.Errorf("the section is not after the timers:\n%s", out)
	}

	// Another crew member's prime shows its own asks and not this one's.
	clerk := sectionOf(mustAs(t, st, "clerk", "prime", "--crew", "clerk"))
	if !strings.Contains(clerk, "## Open asks: 1 waiting") || !strings.Contains(clerk, "The clerk's own question?") {
		t.Errorf("the clerk's prime lacks its ask:\n%s", clerk)
	}
	if strings.Contains(clerk, "Move the links now?") {
		t.Errorf("the clerk's prime lists the yardmaster's ask:\n%s", clerk)
	}

	// The list command shows the same rows, for a person and for the prime.
	listed := mustAs(t, st, "jo", "bead", "list", "--asked-by", "yardmaster")
	for _, want := range []string{
		row(first, open1, "Move the links now?", "open"),
		row(second, open2, "And the docs?", "open"),
		row(first, aside, "Put this one aside?", "put aside"),
	} {
		if !strings.Contains(listed, want) {
			t.Errorf("bead list --asked-by lacks %q:\n%s", want, listed)
		}
	}
	var js []store.WriterAsk
	if err := json.Unmarshal([]byte(mustAs(t, st, "jo", "bead", "list", "--asked-by", "yardmaster", "--json")), &js); err != nil ||
		len(js) != 3 || js[0].Bead != first || js[0].Ask != open1 || js[0].To != "jo" || js[0].State != "open" || js[2].State != "put aside" {
		t.Errorf("bead list --asked-by --json = %+v, %v", js, err)
	}
	if out := mustAs(t, st, "jo", "bead", "list", "--asked-by", "nobody"); out != "" {
		t.Errorf("bead list --asked-by nobody = %q, want nothing", out)
	}
	refusedAs(t, st, "jo", "--asked-by lists asks, not beads: --stage does not apply",
		"bead", "list", "--asked-by", "yardmaster", "--stage", "new")

	// Closing a bead does not end its asks: the open one is still listed.
	mustAs(t, st, "yardmaster", "bead", "close", second, "--reason", "done")
	if out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster"); !strings.Contains(out, row(second, open2, "And the docs?", "open")) {
		t.Errorf("closing the bead hid its open ask:\n%s", out)
	}
}

// Past twenty open asks the prime shows twenty and names the command that
// lists the rest.
func TestPrimeCrewOpenAsksOverflow(t *testing.T) {
	st := userYard(t)
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: "first"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 21; i++ {
		if _, err := st.AddAsk(ctx, b.ID, store.Ask{
			Question: fmt.Sprintf("Question %d?", i), To: "jo",
			Context: []string{"some context"},
			Options: []store.AskOption{{Label: "Yes", Consequence: "it happens"}},
		}, "yardmaster"); err != nil {
			t.Fatal(err)
		}
	}
	out := mustAs(t, st, "yardmaster", "prime", "--crew", "yardmaster")
	sec := out[strings.Index(out, "## Open asks:"):]
	if i := strings.Index(sec, "## How you work"); i >= 0 {
		sec = sec[:i]
	}
	if !strings.Contains(sec, "## Open asks: 21 waiting") {
		t.Fatalf("the heading counts no 21:\n%s", sec)
	}
	if n := strings.Count(sec, "\n- "); n != 20 {
		t.Fatalf("the section shows %d rows, want 20:\n%s", n, sec)
	}
	if !strings.Contains(sec, "... and 1 more: yardr bead list --asked-by yardmaster lists them") {
		t.Fatalf("the section names no command for the rest:\n%s", sec)
	}
	if !strings.Contains(sec, "Question 0?") || strings.Contains(sec, "Question 20?") {
		t.Fatalf("the section shows not the oldest twenty:\n%s", sec)
	}
	if n := strings.Count(mustAs(t, st, "jo", "bead", "list", "--asked-by", "yardmaster"), "\n"); n != 21 {
		t.Fatalf("bead list --asked-by shows %d rows, want 21", n)
	}
}

// withoutFirstRun works on a role file a yard has edited: without the
// heading it changes nothing, and whatever the section holds is replaced
// by the one line.
func TestWithoutFirstRun(t *testing.T) {
	const edited = "# Yardmaster\n\nYou coordinate the work.\n\n## The work\n\nTriage.\n"
	if got := withoutFirstRun(edited); got != edited {
		t.Errorf("a role without the section changed:\n%s", got)
	}
	const own = "# Yardmaster\n\nYou coordinate.\n\n## First run\n\nAsk them nicely.\n\n## The work\n\nTriage.\n"
	want := "# Yardmaster\n\nYou coordinate.\n\n" + setupDoneLine + "\n\n## The work\n\nTriage."
	if got := withoutFirstRun(own); got != want {
		t.Errorf("an edited section:\n%s\nwant:\n%s", got, want)
	}
	// Last in the file, and with no blank line around the heading.
	const last = "# Yardmaster\n\n## First run\n\nRead yardr doctor.\n"
	if got, want := withoutFirstRun(last), "# Yardmaster\n\n"+setupDoneLine; got != want {
		t.Errorf("the section last:\n%s\nwant:\n%s", got, want)
	}
}
