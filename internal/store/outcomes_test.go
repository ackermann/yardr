package store_test

import (
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// outcomeFlow is the yard flow from the yard-qexg bead: agents report an
// outcome, the flow picks the stage. backlog's and decide's edges carry no
// outcome; backlog has one way out, so it gets done implicitly.
const outcomeFlow = `
backlog  -> new
new      -> review on done | decide on question | backlog
review   -> approved on done | new on changes | decide on question
approved -> new on failed | merged
decide   -> backlog | new
merged terminal
`

func TestParseFlowOutcomes(t *testing.T) {
	s, ctx := flowDepot(t)
	saved := putNamed(t, s, "yard", outcomeFlow)
	want := []store.Outcome{{Name: "done", To: "approved"}, {Name: "changes", To: "new"}, {Name: "question", To: "decide"}}
	if got := saved.Outcomes["review"]; !slices.Equal(got, want) {
		t.Fatalf("review outcomes = %+v, want %+v", got, want)
	}
	// Every outcome's stage is a transition; bare edges stay transitions.
	if !slices.Equal(saved.Transitions["new"], []string{"review", "decide", "backlog"}) {
		t.Fatalf("new -> %v", saved.Transitions["new"])
	}
	if !slices.Equal(saved.Transitions["approved"], []string{"new", "merged"}) {
		t.Fatalf("approved -> %v", saved.Transitions["approved"])
	}
	// String writes the outcomes back in the format ParseFlow reads.
	if !strings.Contains(saved.String(), "new -> review on done | decide on question | backlog\n") {
		t.Fatalf("String() =\n%s", saved)
	}
	again := putNamed(t, s, "yard", saved.String())
	if again.String() != saved.String() || !slices.Equal(again.Outcomes["new"], saved.Outcomes["new"]) {
		t.Fatalf("round trip changed the flow:\n%s\nvs\n%s", saved, again)
	}
	// Two outcomes may lead to one stage; each is written once.
	two := putNamed(t, s, "two", "a -> b on done | b on skipped | c\n")
	if got := two.String(); got != "a -> b on done | b on skipped | c\n" {
		t.Fatalf("shared target written as %q", got)
	}
	if !slices.Equal(two.Transitions["a"], []string{"b", "c"}) {
		t.Fatalf("shared target transitions = %v", two.Transitions["a"])
	}
	// JSON may give only the outcome: its stage becomes a transition.
	j, err := store.ParseFlow(`{"outcomes": {"a": [{"name": "done", "to": "b"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	j.Name = "json"
	sj, err := s.PutFlow(ctx, *j, "me")
	if err != nil || !slices.Equal(sj.Transitions["a"], []string{"b"}) || !slices.Equal(sj.Stages, []string{"a", "b"}) {
		t.Fatalf("json outcome flow = %+v, %v", sj, err)
	}

	for _, bad := range []string{"a -> b on", "a -> b when done", "a -> b on done now", "a -> on done"} {
		if _, err := store.ParseFlow(bad); err == nil {
			t.Errorf("ParseFlow(%q) accepted", bad)
		}
	}
}

// Outcomes are unique per stage, named sensibly, and never leave a
// terminal stage; flow set refuses the rest.
func TestPutFlowValidatesOutcomes(t *testing.T) {
	s, ctx := flowDepot(t)
	for name, text := range map[string]string{
		"duplicate on a line":   "a -> b on done | c on done\n",
		"duplicate over lines":  "a -> b on done\na -> c on done\n",
		"terminal with outcome": "a -> b\nb -> a on done\nb terminal\n",
	} {
		f, err := store.ParseFlow(text)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		f.Name = "bad"
		if _, err := s.PutFlow(ctx, *f, "me"); err == nil {
			t.Errorf("%s: accepted", name)
		} else if name != "terminal with outcome" && !strings.Contains(err.Error(), "outcome done twice") {
			t.Errorf("%s: error %q does not name the duplicate", name, err)
		}
	}
	badName := store.Flow{Name: "bad", Outcomes: map[string][]store.Outcome{"a": {{Name: "-x", To: "b"}}}}
	if _, err := s.PutFlow(ctx, badName, "me"); err == nil {
		t.Error("outcome named -x accepted")
	}
	// The same outcome name in different stages is fine.
	putNamed(t, s, "ok", "a -> b on done\nb -> c on done\n")
}

func TestOutcomesFrom(t *testing.T) {
	f, err := store.ParseFlow(outcomeFlow)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]store.Outcome{
		"new":     {{Name: "done", To: "review"}, {Name: "question", To: "decide"}},
		"backlog": {{Name: "done", To: "new"}}, // one way out: done implicitly
		"decide":  nil,                         // two bare edges: explicit moves only
		"merged":  nil,
	}
	for stage, want := range cases {
		if got := f.OutcomesFrom(stage); !slices.Equal(got, want) {
			t.Errorf("OutcomesFrom(%s) = %+v, want %+v", stage, got, want)
		}
	}
	// A single edge that names its outcome offers only that one.
	g, _ := store.ParseFlow("approved -> new on failed\n")
	if got := g.OutcomesFrom("approved"); !slices.Equal(got, []store.Outcome{{Name: "failed", To: "new"}}) {
		t.Errorf("single named edge offers %+v", got)
	}
}

func TestDone(t *testing.T) {
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	next, err := s.NextStages(ctx, b.ID)
	if err != nil || !slices.Equal(next.Outcomes, []store.Outcome{{Name: "done", To: "review"}, {Name: "question", To: "decide"}}) {
		t.Fatalf("next outcomes = %+v, %v", next, err)
	}

	// No --outcome means done.
	got, err := s.Done(ctx, b.ID, "", "built it", store.Expect{}, "builder")
	if err != nil || got.Stage != "review" {
		t.Fatalf("done from new = %+v, %v", got, err)
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 1 || notes[0].Text != "built it" {
		t.Fatalf("notes = %+v", notes)
	}
	evs, err := s.Events(ctx, b.ID, 1)
	if err != nil || len(evs) != 1 || evs[0].Kind != "advanced" || evs[0].Data["outcome"] != "done" ||
		evs[0].Data["from"] != "new" || evs[0].Data["to"] != "review" {
		t.Fatalf("last event = %+v, %v", evs, err)
	}

	// An explicit outcome picks its edge.
	if got, err = s.Done(ctx, b.ID, "changes", "rename the flag", store.Expect{}, "reviewer"); err != nil || got.Stage != "new" {
		t.Fatalf("changes from review = %+v, %v", got, err)
	}

	// An unknown outcome is refused, names the stage's outcomes, and
	// leaves neither a move nor a note behind.
	_, err = s.Done(ctx, b.ID, "approved", "lgtm", store.Expect{}, "me")
	if err == nil {
		t.Fatal("unknown outcome accepted")
	}
	for _, want := range []string{"no outcome approved", "stage new", "outcomes: done, question"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if cur, _ := s.GetBead(ctx, b.ID); cur.Stage != "new" {
		t.Fatalf("refused outcome moved the bead to %s", cur.Stage)
	}
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 2 {
		t.Fatalf("refused outcome left a note: %+v", notes)
	}

	// A stage with several bare edges has no outcomes: advance --to.
	if _, err := s.Done(ctx, b.ID, "question", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	_, err = s.Done(ctx, b.ID, "", "", store.Expect{}, "me")
	if err == nil || !strings.Contains(err.Error(), "names no outcomes for stage decide") || !strings.Contains(err.Error(), "advance --to") {
		t.Fatalf("done from decide err = %v", err)
	}
	// advance --to still works and is still checked against the flow.
	if _, err := s.Advance(ctx, b.ID, "approved", store.Expect{}, "me"); err == nil {
		t.Fatal("decide -> approved allowed")
	}
	if got, err = s.Advance(ctx, b.ID, "backlog", store.Expect{}, "me"); err != nil || got.Stage != "backlog" {
		t.Fatalf("advance decide -> backlog = %+v, %v", got, err)
	}

	// A single way out gets done implicitly.
	if got, err = s.Done(ctx, b.ID, "", "", store.Expect{}, "me"); err != nil || got.Stage != "new" {
		t.Fatalf("implicit done from backlog = %+v, %v", got, err)
	}

	// An edge without an outcome is reachable by advance only, even when
	// it leads to a terminal stage; entering it still closes the bead.
	for _, o := range []string{"", "done"} {
		if _, err := s.Done(ctx, b.ID, o, "", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Done(ctx, b.ID, "merged", "", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "outcomes: failed") {
		t.Fatalf("done --outcome merged err = %v", err)
	}
	if got, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "refinery"); err != nil || got.Status != "closed" {
		t.Fatalf("advance to merged = %+v, %v", got, err)
	}
}

// A bead no flow governs has no outcomes; the error says to advance.
func TestDoneWithoutFlow(t *testing.T) {
	s, ctx := flowDepot(t)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "free", Depot: "yard"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Done(ctx, b.ID, "", "", store.Expect{}, "me")
	if err == nil || !strings.Contains(err.Error(), "no flow governs it") || !strings.Contains(err.Error(), "yardr bead advance "+b.ID+" --to") {
		t.Fatalf("done without a flow err = %v", err)
	}
	if next, _ := s.NextStages(ctx, b.ID); len(next.Outcomes) != 0 {
		t.Fatalf("outcomes without a flow: %+v", next.Outcomes)
	}
}

// Done into a terminal stage closes the bead, like advance.
func TestDoneIntoTerminalCloses(t *testing.T) {
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", "new -> merged on done | new on retry\nmerged terminal\n")
	b, err := s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "yard"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Done(ctx, b.ID, "", "", store.Expect{}, "me")
	if err != nil || got.Status != "closed" || got.CloseReason == nil || *got.CloseReason != "merged" {
		t.Fatalf("done into terminal = %+v, %v", got, err)
	}
}
