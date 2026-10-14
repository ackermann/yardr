// A stage file may end in an ## Ask section: the stage's ask template.
// When a bead enters the stage, the yard puts an ask on it built from the
// template, and the person's answer moves the bead. Three kinds answer three
// wants: choice offers the stage's outcomes, input asks for words, check
// asks whether something has been done:
//
//	## Ask
//	kind: choice
//	question: Where should this bead go next?
//	context:
//	- The builder asked for a decision; the question is in their note.
//	recommend: 1
//	why: The question is small and the branch is green.
//	done: The bead moves to new with your words as the note.
//	backlog: The bead goes back to be reshaped by the yardmaster.
//
// The section is stripped wherever the stage file is shown (StageFile.Body),
// as the summary: line already is. A section that is present must parse
// fully; anything else is refused naming its line, and yardr flow show
// reports it beside the file. A stage file without the section has no
// template.
package store

import (
	"fmt"
	"strconv"
	"strings"
)

// The kinds of a stage's ask template.
const (
	AskChoice = "choice"
	AskInput  = "input"
	AskCheck  = "check"
)

// AskMoveNone is an input template's move that records the words and moves
// nothing, for a stage with no outcomes.
const AskMoveNone = "none"

// AskTemplate is a stage file's ## Ask section parsed: what the yard asks
// when a bead enters the stage. The question and context are literal text;
// nothing is interpolated.
type AskTemplate struct {
	// Kind is AskChoice, AskInput or AskCheck.
	Kind string `json:"kind"`
	// Question is the question in one line; Context what it is about, at
	// least one line.
	Question string   `json:"question"`
	Context  []string `json:"context"`
	// Options are a choice ask's options: each names an outcome of the
	// stage and what picking it does. The option's label is the outcome's
	// name; yardr adds YouDecide when the ask is put, as to any ask.
	Options []AskTemplateOption `json:"options,omitempty"`
	// Recommend is the recommended option's number, 1-based into Options,
	// 0 for no recommendation; Why says why in a line. Choice only.
	Recommend int    `json:"recommend,omitempty"`
	Why       string `json:"why,omitempty"`
	// Move is the outcome an input ask's words fire, or a check ask's Done
	// fires; the default done. AskMoveNone on an input ask records the
	// words and moves nothing. Empty on a choice ask.
	Move string `json:"move,omitempty"`
	// askLine is the ## Ask line, moveLine the move: line (0 for the
	// default), both 1-based: where an error about them points.
	askLine  int
	moveLine int
}

// AskTemplateOption is one option of a choice template: an outcome of the
// stage and what picking it does in a line.
type AskTemplateOption struct {
	Outcome     string `json:"outcome"`
	Consequence string `json:"consequence"`
	// line is the option's line, 1-based: where an error about it points.
	line int
}

// Ask parses the stage file's ## Ask section. Nil with no error when the
// file has no section. A section that is present must parse fully; a
// malformed one is refused naming its line.
func (sf *StageFile) Ask() (*AskTemplate, error) {
	if sf == nil {
		return nil, nil
	}
	lines := strings.Split(sf.Text, "\n")
	at := -1
	for i, line := range lines {
		if isAskHeading(line) {
			at = i
			break
		}
	}
	if at < 0 {
		return nil, nil
	}
	return parseAsk(lines, at)
}

// isAskHeading reports whether the line opens a stage file's ask template:
// a heading that reads ## Ask.
func isAskHeading(line string) bool {
	return strings.Join(strings.Fields(line), " ") == "## Ask"
}

// cutAskSection cuts a stage file's text at its ## Ask section, which the
// brief, prime and the web view leave out as they leave out the summary:
// line. Text without the section is unchanged.
func cutAskSection(text string) string {
	start := 0
	for _, line := range strings.SplitAfter(text, "\n") {
		if isAskHeading(line) {
			return strings.TrimRight(text[:start], " \t\r\n")
		}
		start += len(line)
	}
	return text
}

// askOptionLine is an '<outcome>: <consequence>' line, kept with its line
// until the kind is known: an option of a choice ask, refused on any other.
type askOptionLine struct {
	outcome, consequence string
	line                 int // 1-based
}

// parseAsk parses the ## Ask section at lines[at] to the end of the file.
// Line numbers in errors are 1-based into the file.
func parseAsk(lines []string, at int) (*AskTemplate, error) {
	t := &AskTemplate{askLine: at + 1, Move: DefaultOutcome}
	var (
		seenKind, seenQuestion, seenContext, seenRecommend, seenWhy, seenMove bool
		contextLine, recommendLine, whyLine                                   int // 1-based
		options                                                               []askOptionLine
	)
	fail := func(n int, format string, args ...any) (*AskTemplate, error) {
		return nil, fmt.Errorf("ask line %d: %s", n, fmt.Sprintf(format, args...))
	}
	for i := at + 1; i < len(lines); i++ {
		n := i + 1
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		if line == "-" || strings.HasPrefix(line, "- ") {
			if !seenContext {
				return fail(n, "a '- ' line before any 'context:' line: context lines go under 'context:'")
			}
			if item := strings.TrimSpace(strings.TrimPrefix(line, "-")); item != "" {
				t.Context = append(t.Context, item)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return fail(n, "want 'field: value', a '- ' context line or a blank line, got %q", line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		// A field twice is refused where it stands; an option's duplicates
		// are refused once the kind says these lines are options.
		seen := func(had *bool) bool {
			dup := *had
			*had = true
			return dup
		}
		switch key {
		case "kind":
			if seen(&seenKind) {
				return fail(n, "kind twice; write it once")
			}
			if value != AskChoice && value != AskInput && value != AskCheck {
				return fail(n, "kind %q: want choice, input or check", value)
			}
			t.Kind = value
		case "question":
			if seen(&seenQuestion) {
				return fail(n, "question twice; write it once")
			}
			if value == "" {
				return fail(n, "question is empty: the question in one line")
			}
			t.Question = value
		case "context":
			if seen(&seenContext) {
				return fail(n, "context twice; write it once")
			}
			if value != "" {
				return fail(n, "context takes '- ' lines beneath it, not a value")
			}
			contextLine = n
		case "recommend":
			if seen(&seenRecommend) {
				return fail(n, "recommend twice; write it once")
			}
			r, err := strconv.Atoi(value)
			if err != nil || r < 1 {
				return fail(n, "recommend %q: the recommended option's number, 1-based into the options", value)
			}
			t.Recommend = r
			recommendLine = n
		case "why":
			if seen(&seenWhy) {
				return fail(n, "why twice; write it once")
			}
			t.Why = value
			whyLine = n
		case "move":
			if seen(&seenMove) {
				return fail(n, "move twice; write it once")
			}
			if value == "" {
				return fail(n, "move is empty: the outcome the answer fires")
			}
			t.Move = value
			t.moveLine = n
		default:
			options = append(options, askOptionLine{outcome: key, consequence: value, line: n})
		}
	}
	if t.Kind == "" {
		return fail(t.askLine, "kind is required: choice, input or check")
	}
	if t.Question == "" {
		return fail(t.askLine, "question is required: the question in one line")
	}
	if !seenContext {
		return fail(t.askLine, "context is required, with at least one '- ' line")
	}
	if len(t.Context) == 0 {
		return fail(contextLine, "context needs at least one '- ' line")
	}
	switch t.Kind {
	case AskChoice:
		if seenMove {
			return fail(t.moveLine, "a choice ask takes no 'move:': each option names its outcome")
		}
		t.Move = ""
		if len(options) == 0 {
			return fail(t.askLine, "a choice ask needs at least one '<outcome>: <consequence>' line")
		}
		seen := map[string]bool{}
		for _, o := range options {
			if !validOutcome.MatchString(o.outcome) {
				return fail(o.line, "outcome %q: use letters, digits, '.', '_' or '-'", o.outcome)
			}
			if o.consequence == "" {
				return fail(o.line, "outcome %s needs a consequence: '%s: <what picking it does>'", o.outcome, o.outcome)
			}
			if seen[o.outcome] {
				return fail(o.line, "outcome %s twice; write it once", o.outcome)
			}
			seen[o.outcome] = true
			t.Options = append(t.Options, AskTemplateOption{Outcome: o.outcome, Consequence: o.consequence, line: o.line})
		}
		if t.Recommend > len(t.Options) {
			return fail(recommendLine, "recommend %d: the ask has options 1..%d", t.Recommend, len(t.Options))
		}
		switch {
		case t.Recommend != 0 && t.Why == "":
			if seenWhy {
				return fail(whyLine, "why is empty: why the recommended option, in a line")
			}
			return fail(recommendLine, "recommend %d needs 'why:': why this option, in a line", t.Recommend)
		case t.Recommend == 0 && seenWhy:
			return fail(whyLine, "'why:' needs 'recommend:': which option it is for")
		}
	case AskInput:
		if len(options) > 0 {
			return fail(options[0].line, "an input ask takes no options: its answer is words, and 'move:' says what they do")
		}
		if seenRecommend {
			return fail(recommendLine, "an input ask takes no 'recommend:': its answer is words")
		}
		if seenWhy {
			return fail(whyLine, "an input ask takes no 'why:': its answer is words")
		}
		if t.Move != AskMoveNone && !validOutcome.MatchString(t.Move) {
			return fail(t.moveLine, "move %q: an outcome name uses letters, digits, '.', '_' or '-', or 'none'", t.Move)
		}
	case AskCheck:
		if len(options) > 0 {
			return fail(options[0].line, "a check ask takes no options: Done fires 'move:'")
		}
		if seenRecommend {
			return fail(recommendLine, "a check ask takes no 'recommend:': Done is its only option")
		}
		if seenWhy {
			return fail(whyLine, "a check ask takes no 'why:': Done is its only option")
		}
		if t.Move == AskMoveNone {
			return fail(t.moveLine, "'move: none' is for an input ask: a check ask moves on Done")
		}
		if !validOutcome.MatchString(t.Move) {
			return fail(t.moveLine, "move %q: an outcome name uses letters, digits, '.', '_' or '-'", t.Move)
		}
	}
	return t, nil
}

// BrokenAsk is why the stage file's ## Ask section is broken under f, the
// flow the file belongs to, as "<path>: <why>": a section that does not
// parse, or whose outcomes do not resolve under f. "" when the file has no
// section, or it parses and resolves: what yardr flow show warns of.
func (sf *StageFile) BrokenAsk(f *Flow, stage string) string {
	if sf == nil {
		return ""
	}
	t, err := sf.Ask()
	if err != nil {
		return sf.Path + ": " + err.Error()
	}
	if err := t.Check(f, stage); err != nil {
		return sf.Path + ": " + err.Error()
	}
	return ""
}

// Check reports whether the template's outcomes resolve under f, the flow
// the stage file belongs to: every choice option's outcome, and the move of
// an input or check ask (an input move of none moves nothing). An outcome
// that does not resolve leaves the template broken. Nil is no template, and
// checks.
func (t *AskTemplate) Check(f *Flow, stage string) error {
	if t == nil {
		return nil
	}
	// Where a bad move points: its line, or the section's for the default.
	moveLine := t.moveLine
	if moveLine == 0 {
		moveLine = t.askLine
	}
	switch t.Kind {
	case AskChoice:
		for _, o := range t.Options {
			if _, err := f.Resolve(stage, o.Outcome); err != nil {
				return fmt.Errorf("ask line %d: %s", o.line, err)
			}
		}
	case AskInput:
		if t.Move == AskMoveNone {
			return nil
		}
		if _, err := f.Resolve(stage, t.Move); err != nil {
			return fmt.Errorf("ask line %d: %s", moveLine, err)
		}
	case AskCheck:
		if _, err := f.Resolve(stage, t.Move); err != nil {
			return fmt.Errorf("ask line %d: %s", moveLine, err)
		}
	}
	return nil
}
