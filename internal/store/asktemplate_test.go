package store_test

import (
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A stage file may end in an ## Ask section: the stage's ask template. A
// choice, moves, input or check section parses to the template's fields.
func TestAskTemplateParse(t *testing.T) {
	t.Parallel()
	choice := &store.StageFile{Text: `Build the thing.

## Ask
kind: choice
question: Where should this bead go next?
context:
- The builder asked for a decision; the question is in their note.
- The branch is ready to review either way.
recommend: 1
why: The question is small and the branch is green.
done: The bead moves to new with your words as the note.
backlog: The bead goes back to be reshaped by the yardmaster.
`}
	tmpl, err := choice.Ask()
	if err != nil {
		t.Fatalf("choice: %v", err)
	}
	if tmpl.Kind != store.AskChoice || tmpl.Question != "Where should this bead go next?" {
		t.Errorf("choice: kind %q question %q", tmpl.Kind, tmpl.Question)
	}
	if len(tmpl.Context) != 2 || tmpl.Context[0] != "The builder asked for a decision; the question is in their note." {
		t.Errorf("choice: context %q", tmpl.Context)
	}
	if len(tmpl.Options) != 2 || tmpl.Options[0].Outcome != "done" || tmpl.Options[1].Outcome != "backlog" ||
		tmpl.Options[0].Consequence != "The bead moves to new with your words as the note." {
		t.Errorf("choice: options %+v", tmpl.Options)
	}
	if tmpl.Recommend != 1 || tmpl.Why != "The question is small and the branch is green." {
		t.Errorf("choice: recommend %d why %q", tmpl.Recommend, tmpl.Why)
	}
	if tmpl.Move != "" {
		t.Errorf("choice: move %q, want none", tmpl.Move)
	}

	input := &store.StageFile{Text: "Decide.\n\n## Ask\nkind: input\nquestion: What was decided?\ncontext:\n- The bead waits in decide.\n"}
	tmpl, err = input.Ask()
	if err != nil {
		t.Fatalf("input: %v", err)
	}
	if tmpl.Kind != store.AskInput || tmpl.Question != "What was decided?" || len(tmpl.Context) != 1 ||
		len(tmpl.Options) != 0 || tmpl.Recommend != 0 || tmpl.Why != "" {
		t.Errorf("input: %+v", tmpl)
	}
	if tmpl.Move != "done" {
		t.Errorf("input: move %q, want the default done", tmpl.Move)
	}

	none := &store.StageFile{Text: "Decide.\n\n## Ask\nkind: input\nquestion: What was decided?\ncontext:\n- The bead waits in decide.\nmove: none\n"}
	if tmpl, err = none.Ask(); err != nil || tmpl.Move != "none" {
		t.Fatalf("input with move none: %+v, %v", tmpl, err)
	}

	check := &store.StageFile{Text: "Check it.\n\n## Ask\nkind: check\nquestion: Is it done?\ncontext:\n- The release waits on this.\n"}
	if tmpl, err = check.Ask(); err != nil || tmpl.Kind != store.AskCheck || tmpl.Move != "done" {
		t.Fatalf("check: %+v, %v", tmpl, err)
	}
	ship := &store.StageFile{Text: "Check it.\n\n## Ask\nkind: check\nquestion: Is it done?\ncontext:\n- The release waits on this.\nmove: ship\n"}
	if tmpl, err = ship.Ask(); err != nil || tmpl.Move != "ship" {
		t.Fatalf("check with move ship: %+v, %v", tmpl, err)
	}

	// A moves section writes the question and the context and nothing of the
	// options: they are the stage's moves, known when the ask is put.
	moves := &store.StageFile{Text: "A bead waits here.\n\n## Ask\nkind: moves\nquestion: Where should this bead go next?\ncontext:\n- A person decides.\n"}
	if tmpl, err = moves.Ask(); err != nil || tmpl.Kind != store.AskMoves || tmpl.Question != "Where should this bead go next?" ||
		len(tmpl.Context) != 1 || len(tmpl.Options) != 0 || tmpl.Recommend != 0 || tmpl.Why != "" || tmpl.Move != "" {
		t.Fatalf("moves: %+v, %v", tmpl, err)
	}

	// "options:" alone opens the options: every line after it is one, whatever
	// its name, so an outcome can be named like a field. The fields above it
	// keep their meaning, an option above it stays one, and recommend counts
	// the options in the order they stand.
	named := &store.StageFile{Text: `Build the thing.

## Ask
kind: choice
question: Where should this bead go next?
context:
- The reviewer left a question in their note.
recommend: 2
why: The question is the reason the bead is here.
done: The bead moves on.
options:
question: The bead parks in decide for a longer look.
kind: The outcome is named kind.
context: The outcome is named context.
recommend: The outcome is named recommend.
why: The outcome is named why.
move: The outcome is named move.
options: The outcome is named options.
`}
	if tmpl, err = named.Ask(); err != nil {
		t.Fatalf("options: %v", err)
	}
	if tmpl.Kind != store.AskChoice || tmpl.Question != "Where should this bead go next?" || len(tmpl.Context) != 1 ||
		tmpl.Recommend != 2 || tmpl.Why != "The question is the reason the bead is here." || tmpl.Move != "" {
		t.Errorf("options: fields %+v", tmpl)
	}
	var outcomes []string
	for _, o := range tmpl.Options {
		outcomes = append(outcomes, o.Outcome)
	}
	if got := strings.Join(outcomes, " "); got != "done question kind context recommend why move options" ||
		tmpl.Options[1].Consequence != "The bead parks in decide for a longer look." {
		t.Errorf("options: outcomes %q, options %+v", got, tmpl.Options)
	}

	// No section is no template, not an error.
	for _, text := range []string{"", "Build it.\n", "Build it.\n\n### Ask\nNot the section.\n"} {
		if tmpl, err := (&store.StageFile{Text: text}).Ask(); err != nil || tmpl != nil {
			t.Errorf("text %q: template %+v, %v", text, tmpl, err)
		}
	}
	var noneFile *store.StageFile
	if tmpl, err := noneFile.Ask(); err != nil || tmpl != nil {
		t.Errorf("no stage file: template %+v, %v", tmpl, err)
	}
}

// A fenced example may show the ## Ask syntax without defining a template.
func TestAskTemplateIgnoresFencedExamples(t *testing.T) {
	t.Parallel()
	text := "Use this syntax:\n\n```md\n## Ask\nkind: input\nquestion: Example?\ncontext:\n- Only an example.\n```\n\n~~~\n## Ask\nkind: check\nquestion: Also an example?\ncontext:\n- Still prose.\n~~~\n\n    ## Ask\n    kind: input\n    question: Indented example?\n\nFor this stage:\n\n## Ask\nkind: input\nquestion: What was decided?\ncontext:\n- The stage is waiting.\n"
	sf := &store.StageFile{Text: text}
	tmpl, err := sf.Ask()
	if err != nil {
		t.Fatalf("parse actual template: %v", err)
	}
	if tmpl == nil || tmpl.Question != "What was decided?" {
		t.Fatalf("template = %+v, want the section after both examples", tmpl)
	}
	body := sf.Body()
	if !strings.Contains(body, "## Ask\nkind: input\nquestion: Example?") ||
		!strings.Contains(body, "## Ask\nkind: check\nquestion: Also an example?") ||
		!strings.Contains(body, "    ## Ask\n    kind: input\n    question: Indented example?") ||
		strings.Contains(body, "What was decided?") {
		t.Errorf("body did not retain examples and strip the template:\n%s", body)
	}

	exampleOnly := "Example:\n\n```md\n## Ask\nkind: input\nquestion: Example?\ncontext:\n- Only an example.\n```\n"
	sf = &store.StageFile{Text: exampleOnly}
	if tmpl, err := sf.Ask(); err != nil || tmpl != nil {
		t.Errorf("example-only file: template %+v, %v", tmpl, err)
	}
	if got := sf.Body(); got != exampleOnly {
		t.Errorf("example-only body changed: %q", got)
	}
}

// A section that is present must parse fully: every malformed section is
// refused naming its line.
func TestAskTemplateMalformed(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, text, want string }{
		{"bad kind", "## Ask\nkind: choce\nquestion: Q?\ncontext:\n- Why.\n", "ask line 2: kind \"choce\": want choice, moves, input or check"},
		{"missing kind", "Prose.\n\n## Ask\nquestion: Q?\ncontext:\n- Why.\n", "ask line 3: kind is required: choice, moves, input or check"},
		{"kind twice", "## Ask\nkind: input\nkind: check\nquestion: Q?\ncontext:\n- Why.\n", "ask line 3: kind twice; write it once"},
		{"missing question", "## Ask\nkind: input\ncontext:\n- Why.\n", "ask line 1: question is required: the question in one line"},
		{"empty question", "## Ask\nkind: input\nquestion:\ncontext:\n- Why.\n", "ask line 3: question is empty: the question in one line"},
		{"missing context", "## Ask\nkind: input\nquestion: Q?\n", "ask line 1: context is required, with at least one '- ' line"},
		{"context with a value", "## Ask\nkind: input\nquestion: Q?\ncontext: Why.\n", "ask line 4: context takes '- ' lines beneath it, not a value"},
		{"context without lines", "## Ask\nkind: input\nquestion: Q?\ncontext:\n", "ask line 4: context needs at least one '- ' line"},
		{"context line before context", "## Ask\nkind: input\nquestion: Q?\n- Why.\ncontext:\n- Why.\n", "ask line 4: a '- ' line before any 'context:' line: context lines go under 'context:'"},
		{"choice without options", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\n", "ask line 1: a choice ask needs at least one '<outcome>: <consequence>' line"},
		{"choice with a move", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\nmove: done\ndone: Go.\n", "ask line 6: a choice ask takes no 'move:': each option names its outcome"},
		{"option without consequence", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone:\n", "ask line 6: outcome done needs a consequence: 'done: <what picking it does>'"},
		{"option with a bad name", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\nnot an outcome: Go.\n", "ask line 6: outcome \"not an outcome\": use letters, digits, '.', '_' or '-'"},
		{"option twice", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\ndone: Go again.\n", "ask line 7: outcome done twice; write it once"},
		{"recommend not a number", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\nrecommend: one\ndone: Go.\n", "ask line 6: recommend \"one\": the recommended option's number, 1-based into the options"},
		{"recommend out of range", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\nrecommend: 2\nwhy: Because.\ndone: Go.\n", "ask line 6: recommend 2: the ask has options 1..1"},
		{"recommend without why", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\nrecommend: 1\ndone: Go.\n", "ask line 6: recommend 1 needs 'why:': why this option, in a line"},
		{"recommend with empty why", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\nrecommend: 1\nwhy:\ndone: Go.\n", "ask line 7: why is empty: why the recommended option, in a line"},
		{"why without recommend", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\nwhy: Because.\ndone: Go.\n", "ask line 6: 'why:' needs 'recommend:': which option it is for"},
		{"input with an option", "## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\n", "ask line 6: an input ask takes no options: its answer is words, and 'move:' says what they do"},
		{"input with recommend", "## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\nrecommend: 1\n", "ask line 6: an input ask takes no 'recommend:': its answer is words"},
		{"input with why", "## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\nwhy: Because.\n", "ask line 6: an input ask takes no 'why:': its answer is words"},
		{"input with a bad move", "## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\nmove: no way\n", "ask line 6: move \"no way\": an outcome name uses letters, digits, '.', '_' or '-', or 'none'"},
		{"check with an option", "## Ask\nkind: check\nquestion: Done?\ncontext:\n- Why.\ndone: Go.\n", "ask line 6: a check ask takes no options: Done fires 'move:'"},
		{"check with recommend", "## Ask\nkind: check\nquestion: Done?\ncontext:\n- Why.\nrecommend: 1\n", "ask line 6: a check ask takes no 'recommend:': Done is its only option"},
		{"check with none", "## Ask\nkind: check\nquestion: Done?\ncontext:\n- Why.\nmove: none\n", "ask line 6: 'move: none' is for an input ask: a check ask moves on Done"},
		{"moves with an option", "## Ask\nkind: moves\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\n", "ask line 6: a moves ask takes no options: the stage's moves are its options"},
		{"moves with options:", "## Ask\nkind: moves\nquestion: Q?\ncontext:\n- Why.\noptions:\n", "ask line 6: a moves ask takes no options: the stage's moves are its options"},
		{"moves with recommend", "## Ask\nkind: moves\nquestion: Q?\ncontext:\n- Why.\nrecommend: 1\n", "ask line 6: a moves ask takes no 'recommend:': its options are the stage's moves, known when the ask is put"},
		{"moves with why", "## Ask\nkind: moves\nquestion: Q?\ncontext:\n- Why.\nwhy: Because.\n", "ask line 6: a moves ask takes no 'why:': it recommends nothing"},
		{"moves with a move", "## Ask\nkind: moves\nquestion: Q?\ncontext:\n- Why.\nmove: done\n", "ask line 6: a moves ask takes no 'move:': each option is a move of the stage"},
		{"field name as an option without options:", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\nquestion: Ask.\n", "ask line 7: question twice; write it once"},
		{"options with a value", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\noptions: Go.\n", "ask line 6: options takes the '<outcome>: <consequence>' lines beneath it, not a value"},
		{"options: with nothing beneath it", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\noptions:\n", "ask line 6: options needs at least one '<outcome>: <consequence>' line beneath it"},
		{"options: with nothing beneath it but an option above", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\noptions:\n", "ask line 7: options needs at least one '<outcome>: <consequence>' line beneath it"},
		{"option named options without a consequence", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\noptions:\noptions:\n", "ask line 7: outcome options needs a consequence: 'options: <what picking it does>'"},
		{"option twice across options:", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\noptions:\ndone: Go again.\n", "ask line 8: outcome done twice; write it once"},
		{"context line after options:", "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\noptions:\ndone: Go.\n- More.\n", "ask line 8: a '- ' line after 'options:': context lines go under 'context:', above it"},
		{"context below options:", "## Ask\nkind: choice\nquestion: Q?\noptions:\ncontext:\n- Why.\ndone: Go.\n", "ask line 6: a '- ' line after 'options:': context lines go under 'context:', above it"},
		{"kind below options:", "## Ask\nquestion: Q?\ncontext:\n- Why.\noptions:\ndone: Go.\nkind: choice\n", "ask line 1: kind is required: choice, moves, input or check"},
		{"input with options:", "## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\noptions:\n", "ask line 6: an input ask takes no options: its answer is words, and 'move:' says what they do"},
		{"input with an option above options:", "## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\noptions:\nquestion: Ask.\n", "ask line 6: an input ask takes no options: its answer is words, and 'move:' says what they do"},
		{"check with options:", "## Ask\nkind: check\nquestion: Done?\ncontext:\n- Why.\noptions:\n", "ask line 6: a check ask takes no options: Done fires 'move:'"},
		{"stray prose", "Prose.\n\n## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\nSome more prose.\n", "ask line 8: want 'field: value', a '- ' context line or a blank line, got \"Some more prose.\""},
		{"another section after", "## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\n\n## Outcomes\n", "ask line 7: want 'field: value', a '- ' context line or a blank line, got \"## Outcomes\""},
	} {
		tmpl, err := (&store.StageFile{Text: c.text}).Ask()
		if tmpl != nil || err == nil || err.Error() != c.want {
			t.Errorf("%s: template %+v, error %q, want %q", c.name, tmpl, err, c.want)
		}
	}
}

// A template's outcomes must resolve under the flow the stage file belongs
// to, or the template is broken.
func TestAskTemplateCheck(t *testing.T) {
	t.Parallel()
	flow, err := store.ParseFlow("new -> review\nreview -> merged on done | new on changes\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	flow.Name = "code"
	ask := func(text string) *store.AskTemplate {
		t.Helper()
		tmpl, err := (&store.StageFile{Text: text}).Ask()
		if err != nil {
			t.Fatalf("parse %q: %v", text, err)
		}
		return tmpl
	}
	choice := ask("## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\nchanges: Back.\n")
	if err := choice.Check(flow, "review"); err != nil {
		t.Errorf("choice with the stage's outcomes: %v", err)
	}
	bad := ask("## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\nship: Away.\n")
	if err := bad.Check(flow, "review"); err == nil || !strings.HasPrefix(err.Error(), "ask line 7: flow code has no outcome ship for stage review; outcomes: done, changes") {
		t.Errorf("choice with a bad outcome: %v", err)
	}
	// The implicit done of a stage with one way out resolves.
	if err := choice.Check(flow, "new"); err == nil || !strings.HasPrefix(err.Error(), "ask line 7: ") {
		t.Errorf("choice in a stage with fewer outcomes: %v", err)
	} else if !strings.Contains(err.Error(), "flow code has no outcome changes for stage new") {
		t.Errorf("choice in a stage with fewer outcomes: %v", err)
	}
	input := ask("## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\n")
	if err := input.Check(flow, "review"); err != nil {
		t.Errorf("input with the default move: %v", err)
	}
	// The default move on a stage with no outcomes is broken where the
	// section stands; move none moves nothing anywhere.
	if err := input.Check(flow, "merged"); err == nil || !strings.HasPrefix(err.Error(), "ask line 1: ") {
		t.Errorf("input with the default move in a terminal stage: %v", err)
	}
	none := ask("## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\nmove: none\n")
	if err := none.Check(flow, "merged"); err != nil {
		t.Errorf("input with move none: %v", err)
	}
	check := ask("## Ask\nkind: check\nquestion: Done?\ncontext:\n- Why.\nmove: changes\n")
	if err := check.Check(flow, "review"); err != nil {
		t.Errorf("check with move changes: %v", err)
	}
	if err := check.Check(flow, "new"); err == nil || !strings.HasPrefix(err.Error(), "ask line 6: flow code has no outcome changes for stage new") {
		t.Errorf("check with a move the stage lacks: %v", err)
	}
	// A moves ask offers NextStages, whether outcomes, bare edges or the
	// first-stage fallback for a reopened terminal stage.
	moves := ask("## Ask\nkind: moves\nquestion: Q?\ncontext:\n- Why.\n")
	for _, stage := range []string{"new", "review"} {
		if err := moves.Check(flow, stage); err != nil {
			t.Errorf("moves in %s: %v", stage, err)
		}
	}
	if err := moves.Check(flow, "merged"); err != nil {
		t.Errorf("moves in a reopened terminal stage: %v", err)
	}
	noWay, err := store.ParseFlow("solo terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	noWay.Name = "solo"
	if err := moves.Check(noWay, "solo"); err == nil || err.Error() != "ask line 1: stage solo has no way out under flow solo: a moves ask offers the stage's moves" {
		t.Errorf("moves in a stage with no available move: %v", err)
	}
	var noTemplate *store.AskTemplate
	if err := noTemplate.Check(flow, "review"); err != nil {
		t.Errorf("no template: %v", err)
	}

	// An outcome named like a field is offered after "options:" and resolves
	// like any other, so BrokenAsk, which flow show warns by, says nothing. A
	// field line after "options:" is an option too, refused as one.
	withQuestion, err := store.ParseFlow("new -> review\nreview -> merged on done | new on changes | decide on question\ndecide -> review\nmerged terminal\n")
	if err != nil {
		t.Fatal(err)
	}
	withQuestion.Name = "code"
	offersQuestion := &store.StageFile{Path: "review.md", Text: "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\noptions:\nquestion: Park it.\n"}
	if got := offersQuestion.BrokenAsk(withQuestion, "review"); got != "" {
		t.Errorf("an option named question: %s", got)
	}
	offersKind := &store.StageFile{Path: "review.md", Text: "## Ask\nkind: choice\nquestion: Q?\ncontext:\n- Why.\ndone: Go.\noptions:\nkind: Back.\n"}
	if got := offersKind.BrokenAsk(withQuestion, "review"); !strings.HasPrefix(got, "review.md: ask line 8: flow code has no outcome kind for stage review; outcomes: done, changes, question") {
		t.Errorf("an option named kind: %q", got)
	}
}

// The brief, prime and the web view show the stage file without its ## Ask
// section, as they show it without its summary: line; the map's one line
// never reads the template.
func TestStageFileAskStripped(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ name, text, summary, body string }{
		{"no section", "Build it.\n", "Build it.", "Build it.\n"},
		{"section stripped", "Build it.\n\n## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\n", "Build it.", "Build it."},
		{"broken section stripped too", "Build it.\n\n## Ask\nkind: choce\n", "Build it.", "Build it."},
		{"section alone", "## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\n", "", ""},
		{"summary and section", "summary: Built here.\n\nBuild it.\n\n## Ask\nkind: input\nquestion: Q?\ncontext:\n- Why.\n", "Built here.", "Build it."},
	} {
		sf := &store.StageFile{Text: c.text}
		if got := sf.Summary(); got != c.summary {
			t.Errorf("%s: summary = %q, want %q", c.name, got, c.summary)
		}
		if got := sf.Body(); got != c.body {
			t.Errorf("%s: body = %q, want %q", c.name, got, c.body)
		}
	}
}

// Depot yardr's own train flow carries the first template: in-pr asks
// whether the pull request is merged, and Done reports done, where in-pr's
// done leads. The decide stage of each of the depot's own flows offers its
// moves, which neither flow names as outcomes, so a card of moves is what
// fits; the starter pack's decide carries none, as decided: a card on every
// bead in decide is the depot's own choice, not a default every yard imports.
func TestDepotTrainInPRTemplate(t *testing.T) {
	t.Parallel()
	flowText, err := os.ReadFile("../../.yardr/flows/yardr.train.flow")
	if err != nil {
		t.Fatal(err)
	}
	flow, err := store.ParseFlow(string(flowText))
	if err != nil {
		t.Fatalf("yardr.train.flow: %v", err)
	}
	inpr, err := os.ReadFile("../../.yardr/flows/yardr.train/in-pr.md")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := (&store.StageFile{Path: ".yardr/flows/yardr.train/in-pr.md", Text: string(inpr)}).Ask()
	if err != nil {
		t.Fatalf("in-pr: %v", err)
	}
	if tmpl == nil {
		t.Fatal("in-pr has no ## Ask section")
	}
	if tmpl.Kind != store.AskCheck || tmpl.Move != "done" || tmpl.Question != "Is the PR merged?" {
		t.Errorf("in-pr: kind %q move %q question %q, want a check firing done", tmpl.Kind, tmpl.Move, tmpl.Question)
	}
	if err := tmpl.Check(flow, "in-pr"); err != nil {
		t.Errorf("in-pr: %v", err)
	}
	for _, c := range []struct{ flow, decide string }{
		{"../../.yardr/flows/default.flow", "../../.yardr/flows/default/decide.md"},
		{"../../.yardr/flows/yardr.train.flow", "../../.yardr/flows/yardr.train/decide.md"},
	} {
		flowText, err := os.ReadFile(c.flow)
		if err != nil {
			t.Fatal(err)
		}
		flow, err := store.ParseFlow(string(flowText))
		if err != nil {
			t.Fatalf("%s: %v", c.flow, err)
		}
		text, err := os.ReadFile(c.decide)
		if err != nil {
			t.Fatal(err)
		}
		sf := &store.StageFile{Path: c.decide, Text: string(text)}
		tmpl, err := sf.Ask()
		if err != nil || tmpl == nil || tmpl.Kind != store.AskMoves {
			t.Fatalf("%s: template %+v, %v; want a moves ask", c.decide, tmpl, err)
		}
		if broken := sf.BrokenAsk(flow, "decide"); broken != "" {
			t.Errorf("%s: %s", c.decide, broken)
		}
	}
	starter := "../../packs/starter/flows/default/decide.md"
	text, err := os.ReadFile(starter)
	if err != nil {
		t.Fatal(err)
	}
	if tmpl, err := (&store.StageFile{Path: starter, Text: string(text)}).Ask(); err != nil || tmpl != nil {
		t.Errorf("%s carries an ## Ask section %+v (%v), want none", starter, tmpl, err)
	}
}
