package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// askYard is a store with one bead and mckean as the operator.
func askYard(t *testing.T) (store.API, context.Context, string) {
	t.Helper()
	st := storetest.New(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, "operator", "mckean", "me"); err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBead(ctx, store.NewBead{Title: "the work"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	return st, ctx, b.ID
}

func askArgs(id string) []string {
	return []string{"ask", id, "Move the links now?",
		"--context", "64 links still point at the old source.",
		"--context", "The move was tested on a copy and passed.",
		"--option", "Move them now: all 64 links move tonight",
		"--option", "After Friday: keep the old source",
		"--recommend", "1", "--why", "the test passed"}
}

// bead ask stores the question and prints it as numbered text, with yardr's
// own option last and the recommendation marked; --to defaults to the
// operator.
func TestBeadAsk(t *testing.T) {
	st, _, id := askYard(t)
	a := &app{st: st, actor: "yardmaster"}

	out, err := run(t, a.beadCmd(), askArgs(id)...)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ask 1 · open", "to mckean", "asked by yardmaster",
		"Move the links now?", "64 links still point at the old source.",
		"1. Move them now - all 64 links move tonight (recommended: the test passed)",
		"2. After Friday - keep the old source",
		"3. You decide - leave it to yardmaster",
		"answer with: yardr bead answer " + id + " 1 <n> [words]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ask printed no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"question"`) {
		t.Errorf("ask printed raw JSON:\n%s", out)
	}

	// An explicit --to, and --json for a script.
	out, err = run(t, a.beadCmd(), "ask", id, "And the docs?", "--to", "ada",
		"--context", "the docs mention the old source.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "to ada") || !strings.Contains(out, "words wanted") {
		t.Fatalf("an ask to ada reads:\n%s", out)
	}
	j := &app{st: st, actor: "yardmaster", json: true}
	out, err = run(t, j.beadCmd(), "ask", id, "JSON?", "--context", "why.")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Bead string
		Ask  uint64
		To   string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.Bead != id || got.Ask == 0 || got.To != "mckean" {
		t.Fatalf("ask --json = %q (%v)", out, err)
	}
}

// bead ask refuses an ask with no context, an option with no consequence,
// and an option the writer called You decide.
func TestBeadAskRefusals(t *testing.T) {
	st, _, id := askYard(t)
	a := &app{st: st, actor: "yardmaster"}

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"no context", []string{"ask", id, "Go?", "--option", "Go: it goes"}, "--context"},
		{"no consequence", []string{"ask", id, "Go?", "--context", "why.", "--option", "Go"}, "<label>: <consequence>"},
		{"empty consequence", []string{"ask", id, "Go?", "--context", "why.", "--option", "Go: "}, "consequence"},
		{"writer you decide", []string{"ask", id, "Go?", "--context", "why.", "--option", "You decide: x"}, "leave it out"},
		{"recommend without why", []string{"ask", id, "Go?", "--context", "why.", "--option", "Go: it goes", "--recommend", "1"}, "--why"},
	}
	for _, c := range cases {
		if _, err := run(t, a.beadCmd(), c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	if notes, _ := st.Notes(context.Background(), id); len(notes) != 0 {
		t.Fatalf("a refused ask left notes: %+v", notes)
	}
}

// bead answer records the pick with the words; words alone on an ask with
// options are refused with where they go.
func TestBeadAnswer(t *testing.T) {
	st, _, id := askYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}
	if _, err := run(t, yard.beadCmd(), askArgs(id)...); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, me.beadCmd(), "answer", id, "1", "1", "move them tonight")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "ask 1 · answered") || !strings.Contains(out, "mckean picked 1 (Move them now): move them tonight") {
		t.Fatalf("answer printed:\n%s", out)
	}
	// A newer pick supersedes.
	if _, err := run(t, me.beadCmd(), "answer", id, "1", "3"); err != nil {
		t.Fatal(err)
	}
	out, err = run(t, me.beadCmd(), "answer", id, "1", "2", "soon", "--words", "x")
	if err == nil || !strings.Contains(err.Error(), "one of the two") {
		t.Fatalf("words twice: err = %v", err)
	}
	if _, err := run(t, me.beadCmd(), "answer", id, "1", "2", "--words", "friday"); err != nil {
		t.Fatalf("--words with a pick: err = %v", err)
	}
	if _, err := run(t, me.beadCmd(), "answer", id, "1", "2", "--later"); err == nil ||
		!strings.Contains(err.Error(), "one of the three") {
		t.Fatalf("a pick with --later: err = %v", err)
	}

	// Words alone on an open ask with options go as a plain note.
	if _, err := run(t, yard.beadCmd(), "ask", id, "And this?", "--context", "why.",
		"--option", "Yes: it happens", "--option", "No: it does not"); err != nil {
		t.Fatal(err)
	}
	_, err = run(t, me.beadCmd(), "answer", id, "5", "--words", "just words")
	if err == nil || !strings.Contains(err.Error(), "words alone go as yardr bead note "+id) ||
		!strings.Contains(err.Error(), "leave the ask open") {
		t.Fatalf("words alone: err = %v", err)
	}
	// --json carries the state.
	j := &app{st: st, actor: "mckean", json: true}
	out, err = run(t, j.beadCmd(), "answer", id, "1", "2")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Bead  string
		Ask   uint64
		State string
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || got.State != "answered" {
		t.Fatalf("answer --json = %q (%v)", out, err)
	}
}

// --later puts the ask aside, --decline ends it, and words close an ask
// with no options; the combinations that mean nothing are refused.
func TestBeadAnswerLaterDeclineWords(t *testing.T) {
	st, _, id := askYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}
	if _, err := run(t, yard.beadCmd(), askArgs(id)...); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, yard.beadCmd(), "ask", id, "What tone?", "--context", "the greeting."); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, me.beadCmd(), "answer", id, "1", "--later")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "put aside until") || !strings.Contains(out, "back sooner if yardmaster writes") {
		t.Fatalf("later printed:\n%s", out)
	}
	out, err = run(t, me.beadCmd(), "answer", id, "2", "--words", "warm")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "answered") || !strings.Contains(out, "mckean answered: warm") {
		t.Fatalf("words printed:\n%s", out)
	}
	out, err = run(t, me.beadCmd(), "answer", id, "1", "--decline", "not mine to decide")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "mckean declined to decide this: not mine to decide") {
		t.Fatalf("decline printed:\n%s", out)
	}

	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"nothing", []string{"answer", id, "1"}, "nothing to record"},
		{"later with words", []string{"answer", id, "1", "--later", "--words", "x"}, "nothing else"},
		{"decline with words", []string{"answer", id, "1", "--decline", "x", "--words", "y"}, "its own words"},
		{"not a number", []string{"answer", id, "1", "soon"}, "not an option number"},
		{"no ask", []string{"answer", id, "9", "1"}, "has no ask 9"},
	} {
		if _, err := run(t, me.beadCmd(), c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

// prime --bead shows asks as numbered text with their state, answers
// folded into them, and no raw JSON anywhere.
func TestPrimeShowsAsks(t *testing.T) {
	st, _, id := askYard(t)
	yard := &app{st: st, actor: "yardmaster"}
	me := &app{st: st, actor: "mckean"}
	if _, err := run(t, yard.beadCmd(), "note", id, "the test log"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, yard.beadCmd(), askArgs(id)...); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, me.beadCmd(), "answer", id, "2", "1"); err != nil {
		t.Fatal(err)
	}

	out, err := run(t, me.primeCmd(), "--bead", id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"the test log",
		"ask 2 · answered",
		"1. Move them now - all 64 links move tonight (recommended: the test passed)",
		"3. You decide - leave it to yardmaster",
		"mckean picked 1 (Move them now)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prime has no %q:\n%s", want, out)
		}
	}
	for _, raw := range []string{`"question"`, `"consequence"`, `"option"`, `"ask":`} {
		if strings.Contains(out, raw) {
			t.Errorf("prime shows raw JSON %q:\n%s", raw, out)
		}
	}
}

// review shows a bead's asks as numbered text when it prints the bead;
// its queue is untouched: beads in manual stages by bead age.
func TestReviewShowsAsks(t *testing.T) {
	st, ctx := reviewYard(t, "/tmp/yard")
	b, err := st.CreateBead(ctx, store.NewBead{Title: "decide this", Depot: "yard", Stage: "decide"}, "yardmaster")
	if err != nil {
		t.Fatal(err)
	}
	yard := &app{st: st, actor: "yardmaster"}
	if _, err := run(t, yard.beadCmd(), askArgs(b.ID)...); err != nil {
		t.Fatal(err)
	}
	notes, err := st.Notes(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	text := lastNotes(notes, 3, nil)
	if !strings.Contains(text, "ask 1 · open") || !strings.Contains(text, "3. You decide") {
		t.Fatalf("review notes read:\n%s", text)
	}
	if strings.Contains(text, `"question"`) {
		t.Fatalf("review notes show raw JSON:\n%s", text)
	}
	items, err := reviewQueue(ctx, st, "mckean", nil)
	if err != nil || len(items) != 1 || items[0].Bead.ID != b.ID {
		t.Fatalf("review queue = %+v, %v", items, err)
	}
}
