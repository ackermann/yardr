package store_test

import (
	"encoding/json"
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
	if got, err = s.Advance(ctx, b.ID, "merged", store.Expect{}, "assembly"); err != nil || got.Status != "closed" {
		t.Fatalf("advance to merged = %+v, %v", got, err)
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
