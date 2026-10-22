package store_test

import (
	"encoding/json"
	"fmt"
	"maps"
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
	t.Parallel()
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
	// A flow with no limits keeps its JSON as it was: no max, no then.
	raw, err := json.Marshal(sj)
	if err != nil || strings.Contains(string(raw), `"max"`) || strings.Contains(string(raw), `"then"`) {
		t.Fatalf("unlimited flow as json = %s, %v", raw, err)
	}

	// An outcome may carry a limit, "max <n> then <stage>": that many rounds
	// back to its stage, and then the bead goes to another. String writes it
	// back unchanged, and then is a transition of the stage.
	const limited = "review -> approved on done | new on changes max 3 then decide | decide on question\n"
	l := putNamed(t, s, "limited", limited)
	wantLimited := []store.Outcome{{Name: "done", To: "approved"}, {Name: "changes", To: "new", Max: 3, Then: "decide"}, {Name: "question", To: "decide"}}
	if got := l.Outcomes["review"]; !slices.Equal(got, wantLimited) {
		t.Fatalf("limited outcomes = %+v, want %+v", got, wantLimited)
	}
	if !slices.Equal(l.Transitions["review"], []string{"approved", "new", "decide"}) || l.String() != limited {
		t.Fatalf("limited flow: transitions %v, String() =\n%s", l.Transitions["review"], l)
	}
	// Then may be a stage no other edge names: it is a transition and a stage
	// where it was written, and written once, by the limited outcome. A bare
	// edge to it besides says nothing more, so it is not written back.
	const thenOnly = "review -> new on changes max 2 then decide | approved on done\n"
	only := putNamed(t, s, "thenonly", thenOnly)
	if !slices.Equal(only.Transitions["review"], []string{"new", "decide", "approved"}) || !slices.Equal(only.Stages, []string{"review", "new", "decide", "approved"}) || only.String() != thenOnly {
		t.Fatalf("then-only flow: transitions %v, stages %v, String() =\n%s", only.Transitions["review"], only.Stages, only)
	}
	if got := putNamed(t, s, "thenbare", "review -> new on changes max 2 then decide | approved on done | decide\n").String(); got != thenOnly {
		t.Fatalf("then named by a bare edge too is written %q, want %q", got, thenOnly)
	}
	// Then may be a stage that closes the bead; two outcomes may lead to one
	// stage and one of them carry the limit.
	toEnd := putNamed(t, s, "toend", "a -> b on done | b on skipped max 2 then dropped\ndropped terminal\n")
	if !toEnd.Allows("a", "dropped") || !toEnd.IsTerminal("dropped") || toEnd.String() != "a -> b on done | b on skipped max 2 then dropped\ndropped terminal\n" {
		t.Fatalf("then a terminal stage: String() =\n%s", toEnd)
	}
	// JSON carries them as max and then, and the store keeps both.
	jl, err := store.ParseFlow(`{"outcomes": {"a": [{"name": "changes", "to": "b", "max": 3, "then": "c"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	jl.Name = "jsonlimit"
	if _, err := s.PutFlow(ctx, *jl, "me"); err != nil {
		t.Fatal(err)
	}
	kept, err := s.GetFlow(ctx, "jsonlimit")
	if err != nil || !slices.Equal(kept.Outcomes["a"], []store.Outcome{{Name: "changes", To: "b", Max: 3, Then: "c"}}) || !slices.Equal(kept.Transitions["a"], []string{"b", "c"}) {
		t.Fatalf("json limit kept as %+v, %v", kept, err)
	}
	if raw, err = json.Marshal(kept); err != nil || !strings.Contains(string(raw), `"max":3,"then":"c"`) {
		t.Fatalf("limited flow as json = %s, %v", raw, err)
	}

	for _, bad := range []string{
		"a -> b on", "a -> b when done", "a -> b on done now", "a -> on done",
		// A limit is "max <n> then <stage>", in that order, after an outcome.
		"a -> b on done max 3", "a -> b on done max 3 then", "a -> b on done then c",
		"a -> b on done then c max 3", "a -> b on done max 3 when c", "a -> b on done max 3 then c d",
		"a -> b max 3 then c",
		// The rounds are a whole number, 1 or more: a 0 would read as no limit.
		"a -> b on done max 0 then c", "a -> b on done max -1 then c",
		"a -> b on done max many then c", "a -> b on done max 1.5 then c",
	} {
		if _, err := store.ParseFlow(bad); err == nil {
			t.Errorf("ParseFlow(%q) accepted", bad)
		}
	}
}

// Outcomes are unique per stage, named sensibly, and never leave a
// terminal stage; flow set refuses the rest.
func TestPutFlowValidatesOutcomes(t *testing.T) {
	t.Parallel()
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
	// A limit is a max of 1 round or more with a then that is a stage other
	// than the outcome's own, and no outcome named done has one. The line
	// format cannot say a max below 1 (ParseFlow refuses it), so JSON does.
	for name, c := range map[string]struct{ text, want string }{
		"max on done":                {"a -> b on done max 3 then c\n", "outcome done of stage a cannot have a limit"},
		"max on done, in JSON":       {`{"outcomes": {"a": [{"name": "done", "to": "b", "max": 3, "then": "c"}]}}`, "outcome done of stage a cannot have a limit"},
		"max below one":              {`{"outcomes": {"a": [{"name": "changes", "to": "b", "max": -1, "then": "c"}]}}`, "has max -1: a limit is 1 round or more"},
		"max without then":           {`{"outcomes": {"a": [{"name": "changes", "to": "b", "max": 3}]}}`, "has max 3 but no then"},
		"then without max":           {`{"outcomes": {"a": [{"name": "changes", "to": "b", "then": "c"}]}}`, "has then c but no max"},
		"then with a max of 0":       {`{"outcomes": {"a": [{"name": "changes", "to": "b", "max": 0, "then": "c"}]}}`, "has then c but no max"},
		"then equal to to":           {"a -> b on changes max 3 then b\n", "has then b, the stage it leads to"},
		"then is no stage name":      {"a -> b on changes max 3 then -c\n", `has then "-c": use letters`},
		"bad limit in a later stage": {"a -> b on changes max 3 then c\nb -> a on done | a on changes max 2 then a\n", "outcome changes of stage b has then a"},
	} {
		f, err := store.ParseFlow(c.text)
		if err != nil {
			t.Fatalf("%s: parse: %v", name, err)
		}
		f.Name = "bad"
		if _, err := s.PutFlow(ctx, *f, "me"); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want one saying %q", name, err, c.want)
		}
	}
	// The same outcome name in different stages is fine.
	putNamed(t, s, "ok", "a -> b on done\nb -> c on done\n")
}

func TestOutcomesFrom(t *testing.T) {
	t.Parallel()
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

	// Outcome finds one by stage and name, with the limit it carries; ""
	// is done, which a single way out offers without a limit. Resolve is its To,
	// and an outcome the stage lacks is refused with the ones it has.
	l, err := store.ParseFlow("backlog -> new\nreview -> approved on done | new on changes max 3 then decide | decide on question\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		stage, name string
		want        store.Outcome
	}{
		{"review", "changes", store.Outcome{Name: "changes", To: "new", Max: 3, Then: "decide"}},
		{"review", "", store.Outcome{Name: "done", To: "approved"}},
		{"review", " question ", store.Outcome{Name: "question", To: "decide"}},
		{"backlog", "done", store.Outcome{Name: "done", To: "new"}},
	} {
		if got, err := l.Outcome(c.stage, c.name); err != nil || got != c.want {
			t.Errorf("Outcome(%s, %q) = %+v, %v; want %+v", c.stage, c.name, got, err, c.want)
		}
		if to, err := l.Resolve(c.stage, c.name); err != nil || to != c.want.To {
			t.Errorf("Resolve(%s, %q) = %q, %v; want %s", c.stage, c.name, to, err, c.want.To)
		}
	}
	for stage, want := range map[string]string{"review": "outcomes: done, changes, question", "decide": "names no outcomes for stage decide"} {
		if _, err := l.Outcome(stage, "failed"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Outcome(%s, failed) err = %v, want one saying %q", stage, err, want)
		}
	}
}

func TestDone(t *testing.T) {
	t.Parallel()
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

	// The bead keeps the stages it has reached, and the rounds an outcome
	// has brought it back to each: a key for every stage, at zero when it
	// is reached for the first time, by hand or by outcome. Read back from
	// the store after each step, whole.
	rounds := func(want map[string]int) {
		t.Helper()
		got, err := s.GetBead(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !maps.Equal(got.Rounds, want) {
			t.Fatalf("rounds = %v, want %v", got.Rounds, want)
		}
	}
	if !maps.Equal(b.Rounds, map[string]int{"new": 0}) {
		t.Fatalf("a created bead has rounds %v, want its first stage at zero", b.Rounds)
	}
	rounds(map[string]int{"new": 0})
	if all, err := s.ListBeads(ctx, store.BeadFilter{Depot: "yard"}); err != nil || len(all) != 1 || !maps.Equal(all[0].Rounds, b.Rounds) {
		t.Fatalf("listed %+v, %v; want the bead with rounds %v", all, err, b.Rounds)
	}
	// A move by hand gives a stage reached for the first time its key, and
	// adds nothing to one that has it: back in new, which has its key, the
	// count stays at zero.
	for _, to := range []string{"backlog", "new"} {
		if _, err := s.Advance(ctx, b.ID, to, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
		rounds(map[string]int{"new": 0, "backlog": 0})
	}

	// No --outcome means done.
	got, err := s.Done(ctx, b.ID, "", "built it", store.Expect{}, "builder")
	if err != nil || got.Stage != "review" {
		t.Fatalf("done from new = %+v, %v", got, err)
	}
	rounds(map[string]int{"new": 0, "backlog": 0, "review": 0})
	if notes, _ := s.Notes(ctx, b.ID); len(notes) != 1 || notes[0].Text != "built it" {
		t.Fatalf("notes = %+v", notes)
	}
	evs, err := s.Events(ctx, b.ID, 1)
	if err != nil || len(evs) != 1 || evs[0].Kind != "advanced" || evs[0].Data["outcome"] != "done" ||
		evs[0].Data["from"] != "new" || evs[0].Data["to"] != "review" {
		t.Fatalf("last event = %+v, %v", evs, err)
	}

	// An explicit outcome picks its edge. Back in new, which the bead has
	// been in, the outcome is a round.
	if got, err = s.Done(ctx, b.ID, "changes", "rename the flag", store.Expect{}, "reviewer"); err != nil || got.Stage != "new" {
		t.Fatalf("changes from review = %+v, %v", got, err)
	}
	rounds(map[string]int{"new": 1, "backlog": 0, "review": 0})

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
	rounds(map[string]int{"new": 1, "backlog": 0, "review": 0})

	// A stage with several bare edges has no outcomes: advance --to.
	if _, err := s.Done(ctx, b.ID, "question", "", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	rounds(map[string]int{"new": 1, "backlog": 0, "review": 0, "decide": 0})
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
	// Neither the refusals nor the move by hand into backlog, which has its
	// key, added a round.
	rounds(map[string]int{"new": 1, "backlog": 0, "review": 0, "decide": 0})

	// A single way out gets done implicitly: back in new again, a second round.
	if got, err = s.Done(ctx, b.ID, "", "", store.Expect{}, "me"); err != nil || got.Stage != "new" {
		t.Fatalf("implicit done from backlog = %+v, %v", got, err)
	}
	rounds(map[string]int{"new": 2, "backlog": 0, "review": 0, "decide": 0})

	// An edge without an outcome is reachable by advance only, even when
	// it leads to a terminal stage; entering it still closes the bead.
	for _, o := range []string{"", "done"} {
		if _, err := s.Done(ctx, b.ID, o, "", store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// The first of the two came back to review, a round; the second reached
	// approved for the first time.
	rounds(map[string]int{"new": 2, "backlog": 0, "review": 1, "decide": 0, "approved": 0})
	if _, err := s.Done(ctx, b.ID, "merged", "", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "outcomes: failed") {
		t.Fatalf("done --outcome merged err = %v", err)
	}
	if got, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "assembly"); err != nil || got.Status != "closed" {
		t.Fatalf("advance to merged = %+v, %v", got, err)
	}
	// The terminal stage is reached too, and the closed bead keeps its record.
	rounds(map[string]int{"new": 2, "backlog": 0, "review": 1, "decide": 0, "approved": 0, "merged": 0})

	// The record is the bead's own: the ETA's visited list holds the first
	// ETAVisitedCap stages a bead reaches and no more, and a flow may have
	// more. In a flow of two stages past that, the first with a way back to
	// itself, a same-stage outcome is a round; and after the bead has been
	// walked to the last stage, sent back to the one before it, which is
	// the first the list never held, that is a round too.
	stages := make([]string, store.ETAVisitedCap+2)
	for i := range stages {
		stages[i] = fmt.Sprintf("s%02d", i)
	}
	past := stages[store.ETAVisitedCap]
	var long strings.Builder
	fmt.Fprintf(&long, "%s -> %s on done | %s on retry\n", stages[0], stages[1], stages[0])
	for i := 1; i < len(stages)-1; i++ {
		fmt.Fprintf(&long, "%s -> %s on done\n", stages[i], stages[i+1])
	}
	fmt.Fprintf(&long, "%s -> %s on changes\n", stages[len(stages)-1], past)
	putFlow(t, s, "yard", "long", long.String())
	l, err := s.CreateBead(ctx, store.NewBead{Title: "long", Depot: "yard", Type: "long"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{stages[0]: 1}
	if got, err := s.Done(ctx, l.ID, "retry", "", store.Expect{}, "me"); err != nil || got.Stage != stages[0] || !maps.Equal(got.Rounds, want) {
		t.Fatalf("retry in %s = %+v, %v; want a round there", stages[0], got, err)
	}
	for _, to := range stages[1:] {
		got, err := s.Done(ctx, l.ID, "", "", store.Expect{}, "me")
		if err != nil || got.Stage != to {
			t.Fatalf("done to %s = %+v, %v", to, got, err)
		}
		want[to] = 0
		if !maps.Equal(got.Rounds, want) {
			t.Fatalf("on reaching %s the rounds are %v, want %v", to, got.Rounds, want)
		}
	}
	want[past] = 1
	if got, err := s.Done(ctx, l.ID, "changes", "", store.Expect{}, "me"); err != nil || got.Stage != past || !maps.Equal(got.Rounds, want) {
		t.Fatalf("changes back to %s = %+v, %v; want the round counted: %v", past, got, err, want)
	}

	// A limit on an outcome: once the rounds back to its stage are used, the
	// outcome sends the bead to its then instead. The flow has the design
	// pack's shape, two outcomes back to draft with different limits that
	// both add to draft's one count.
	putFlow(t, s, "yard", "design", `
draft    -> critique on done
critique -> approved on done | draft on changes max 2 then decide | decide on question
approved -> merged on done | draft on failed max 3 then decide
decide   -> draft | critique
merged terminal
`)
	d, err := s.CreateBead(ctx, store.NewBead{Title: "design", Depot: "yard", Type: "design"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	report := func(id, outcome, stage string) *store.Bead {
		t.Helper()
		got, err := s.Done(ctx, id, outcome, "", store.Expect{}, "me")
		if err != nil || got.Stage != stage {
			t.Fatalf("%s on %s = %+v, %v; want the bead in %s", outcome, id, got, err, stage)
		}
		return got
	}
	hand := func(id, stage string) {
		t.Helper()
		if _, err := s.Advance(ctx, id, stage, store.Expect{}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	// draftRounds is draft's count, read back, and that it has its key.
	draftRounds := func(want int) {
		t.Helper()
		got, err := s.GetBead(ctx, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if n, ok := got.Rounds["draft"]; !ok || n != want {
			t.Fatalf("draft's count = %d (key %v); want %d", n, ok, want)
		}
	}
	// lastMove is the data of the bead's advanced event into stage, the
	// latest, and the kinds of the events after it.
	lastMove := func(id, stage string) (map[string]any, []string) {
		t.Helper()
		evs, err := s.Events(ctx, id, 50)
		if err != nil {
			t.Fatal(err)
		}
		for i := len(evs) - 1; i >= 0; i-- {
			if evs[i].Kind == "advanced" && evs[i].Data["to"] == stage {
				var after []string
				for _, e := range evs[i+1:] {
					after = append(after, e.Kind)
				}
				return evs[i].Data, after
			}
		}
		t.Fatalf("no advanced event into %s: %+v", stage, evs)
		return nil, nil
	}

	// Three failed landings, each back to draft, are allowed by approved's
	// max 3 (the count is 0, 1, 2 before them) and leave draft's count at 3.
	// A move below the limit says nothing of it.
	report(d.ID, "done", "critique")
	report(d.ID, "done", "approved")
	for i := 1; i <= 3; i++ {
		report(d.ID, "failed", "draft")
		draftRounds(i)
		report(d.ID, "done", "critique")
		if i < 3 {
			report(d.ID, "done", "approved")
		}
	}
	if move, after := lastMove(d.ID, "draft"); move["outcome"] != "failed" || move["instead_of"] != nil || move["rounds"] != nil || move["max"] != nil || slices.Contains(after, store.RoundsReached) {
		t.Fatalf("a failed landing under its limit left %v and then %v", move, after)
	}

	// Critique's changes allows 2, and the count is 3: raised past it by
	// the other edge. An equality test would compare 3 with 2, miss, and send
	// the bead round again; "at least" redirects. The advanced event says why,
	// draft's count is zero with its key, and the yard tells whoever hears
	// attention, in the same write.
	got = report(d.ID, "changes", "decide")
	if n, ok := got.Rounds["draft"]; !ok || n != 0 || got.Rounds["decide"] != 0 {
		t.Fatalf("rounds after the redirect = %v, want draft at zero with its key", got.Rounds)
	}
	draftRounds(0)
	move, after := lastMove(d.ID, "decide")
	if move["from"] != "critique" || move["outcome"] != "changes" || move["rounds"] != float64(3) || move["max"] != float64(2) || move["instead_of"] != "draft" {
		t.Fatalf("the redirect's advanced event = %v, want critique -> decide on changes, rounds 3, max 2, instead_of draft", move)
	}
	if !slices.Equal(after, []string{store.RoundsReached}) {
		t.Fatalf("events after the redirect's advanced = %v, want %s alone", after, store.RoundsReached)
	}
	evs, err = s.Events(ctx, d.ID, 1)
	if err != nil || len(evs) != 1 || evs[0].Kind != store.RoundsReached || evs[0].Actor != "yard" {
		t.Fatalf("last event = %+v, %v", evs, err)
	}
	for k, v := range map[string]any{"stage": "critique", "outcome": "changes", "rounds": float64(3), "to": "draft", "then": "decide"} {
		if evs[0].Data[k] != v {
			t.Fatalf("%s carries %v, want %s %v", store.RoundsReached, evs[0].Data, k, v)
		}
	}
	if !slices.Contains(store.AttentionKinds, store.RoundsReached) {
		t.Fatalf("%s is not of the class attention: %v", store.RoundsReached, store.AttentionKinds)
	}

	// A person sends it back to draft, which adds nothing: a fresh
	// allowance. The limit is reached exactly at its max, 2; and a question
	// to decide between the two rounds leaves the count where it was, so
	// asking once a round does not keep a bead going.
	hand(d.ID, "draft")
	draftRounds(0)
	report(d.ID, "done", "critique")
	report(d.ID, "changes", "draft")
	draftRounds(1)
	report(d.ID, "done", "critique")
	report(d.ID, "question", "decide")
	draftRounds(1)
	hand(d.ID, "critique")
	report(d.ID, "changes", "draft")
	draftRounds(2)
	report(d.ID, "done", "critique")
	report(d.ID, "changes", "decide")
	draftRounds(0)
	if move, _ := lastMove(d.ID, "decide"); move["rounds"] != float64(2) || move["max"] != float64(2) || move["instead_of"] != "draft" {
		t.Fatalf("the redirect at exactly max: advanced event = %v, want rounds 2, max 2, instead_of draft", move)
	}

	// A redirect is a move like any other: what refuses it leaves the count,
	// the stage and the log as they were. Stage check is worked by a group
	// whose member may move a bead out of it, and no one else.
	putFlow(t, s, "yard", "gate", "work -> check on done\ncheck -> work on again max 1 then decide | merged on done\ndecide -> work | check\nmerged terminal\n")
	if err := s.PutGroup(ctx, store.Group{Name: "checkers", Runner: "manual", Members: []string{"rev"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "check", Group: "checkers"}, "me"); err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateBead(ctx, store.NewBead{Title: "gate", Depot: "yard", Type: "gate"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ outcome, by, stage string }{{"done", "me", "check"}, {"again", "rev", "work"}, {"done", "me", "check"}} {
		if got, err := s.Done(ctx, g.ID, step.outcome, "", store.Expect{}, step.by); err != nil || got.Stage != step.stage {
			t.Fatalf("%s by %s = %+v, %v; want the bead in %s", step.outcome, step.by, got, err, step.stage)
		}
	}
	before, err := s.Events(ctx, g.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Done(ctx, g.ID, "again", "back it goes", store.Expect{}, "me"); err == nil || !strings.Contains(err.Error(), "only members of group checkers may move it out of stage check") {
		t.Fatalf("again at its limit by a non-member: %v", err)
	}
	cur, err := s.GetBead(ctx, g.ID)
	after2, err2 := s.Events(ctx, g.ID, 50)
	if err != nil || err2 != nil || cur.Stage != "check" || cur.Rounds["work"] != 1 || len(after2) != len(before) {
		t.Fatalf("the refused redirect left stage %s, rounds %v, %d events (was %d): %v %v", cur.Stage, cur.Rounds, len(after2), len(before), err, err2)
	}
	if notes, _ := s.Notes(ctx, g.ID); len(notes) != 0 {
		t.Fatalf("the refused redirect left a note: %+v", notes)
	}
	if got, err := s.Done(ctx, g.ID, "again", "", store.Expect{}, "rev"); err != nil || got.Stage != "decide" || got.Rounds["work"] != 0 {
		t.Fatalf("again at its limit by a member = %+v, %v; want the bead in decide, work's count at zero", got, err)
	}

	// A then that closes the bead closes it in the same write, with the
	// stage as its reason, after the two events of the redirect.
	putFlow(t, s, "yard", "loop", "make -> test on done\ntest -> make on again max 1 then abandoned\nabandoned terminal\n")
	c, err := s.CreateBead(ctx, store.NewBead{Title: "loop", Depot: "yard", Type: "loop"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct{ outcome, stage string }{{"done", "test"}, {"again", "make"}, {"done", "test"}} {
		report(c.ID, step.outcome, step.stage)
	}
	got = report(c.ID, "again", "abandoned")
	if got.Status != "closed" || got.CloseReason == nil || *got.CloseReason != "abandoned" {
		t.Fatalf("again at its limit, then abandoned = %+v; want the bead closed with that reason", got)
	}
	if evs, err = s.Events(ctx, c.ID, 3); err != nil || len(evs) != 3 || evs[0].Kind != "advanced" || evs[1].Kind != store.RoundsReached || evs[2].Kind != "closed" {
		t.Fatalf("last events = %+v, %v; want advanced, %s, closed", evs, err, store.RoundsReached)
	}
}

// A bead no flow governs has no outcomes; the error says to advance.
func TestDoneWithoutFlow(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
