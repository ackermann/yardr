// A stage file may end in an ## Ask section: the stage's ask template.
// When a bead enters the stage, the yard puts an ask on it built from the
// template, and the person's answer moves the bead. Four kinds answer four
// wants: choice offers the outcomes the file names, moves offers the stage's
// moves as the flow has them, input asks for words, check asks whether
// something has been done:
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
// A line named like a field is that field, so an outcome named question,
// kind, context, recommend, why, move or options cannot be offered as a bare
// line. A line of "options:", which takes no value, opens the options
// instead: every line after it is an option whatever its name, to the end of
// the section.
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
	AskMoves  = "moves"
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
	// Kind is AskChoice, AskMoves, AskInput or AskCheck.
	Kind string `json:"kind"`
	// Question is the question in one line; Context what it is about, at
	// least one line.
	Question string   `json:"question"`
	Context  []string `json:"context"`
	// Options are a choice ask's options: each names an outcome of the
	// stage and what picking it does. The option's label is the outcome's
	// name; the card offers these and no YouDecide, which a hand ask gets. A
	// moves ask writes none: stageTemplate fills them from the flow (movesOf).
	Options []AskTemplateOption `json:"options,omitempty"`
	// Recommend is the recommended option's number, 1-based into Options,
	// 0 for no recommendation; Why says why in a line. Choice only.
	Recommend int    `json:"recommend,omitempty"`
	Why       string `json:"why,omitempty"`
	// Move is the outcome an input ask's words fire, or a check ask's Done
	// fires; the default done. AskMoveNone on an input ask records the
	// words and moves nothing. Empty on a choice or moves ask.
	Move string `json:"move,omitempty"`
	// askLine is the ## Ask line, moveLine the move: line (0 for the
	// default), both 1-based: where an error about them points.
	askLine  int
	moveLine int
}

// AskTemplateOption is one option of a choice template: an outcome of the
// stage and what picking it does in a line. A moves template's are the
// flow's, each a label and the move it fires.
type AskTemplateOption struct {
	Outcome     string `json:"outcome"`
	Consequence string `json:"consequence"`
	// line is the option's line, 1-based: where an error about it points.
	line int
	// move is what picking the option fires when that is not the outcome
	// the label names: the advance to a stage that has no outcome (moveTo).
	move string
}

// fires is the move picking the option fires: its outcome, else its own.
func (o AskTemplateOption) fires() string { return or(o.move, o.Outcome) }

// Ask parses the stage file's ## Ask section. Nil with no error when the
// file has no section. A section that is present must parse fully; a
// malformed one is refused naming its line.
func (sf *StageFile) Ask() (*AskTemplate, error) {
	if sf == nil {
		return nil, nil
	}
	lines := strings.Split(sf.Text, "\n")
	_, at := askSectionStart(sf.Text)
	if at < 0 {
		return nil, nil
	}
	return parseAsk(lines, at)
}

// isAskHeading reports whether the line opens a stage file's ask template:
// a heading that reads ## Ask.
func isAskHeading(line string) bool {
	i := 0
	for i < len(line) && line[i] == ' ' {
		i++
	}
	if i > 3 || (i == 0 && strings.HasPrefix(line, "\t")) {
		return false
	}
	return strings.Join(strings.Fields(line[i:]), " ") == "## Ask"
}

// askSectionStart finds the ## Ask heading outside a fenced code block. Stage
// files are markdown, so an example of the template in a code fence is prose,
// not the section that controls the stage. It returns the byte offset and
// zero-based line number, or -1 for each when there is no section.
func askSectionStart(text string) (offset, lineNumber int) {
	var fence byte
	fenceWidth := 0
	for i, line := range strings.SplitAfter(text, "\n") {
		content := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if fence != 0 {
			if closesMarkdownFence(content, fence, fenceWidth) {
				fence, fenceWidth = 0, 0
			}
		} else if mark, width, ok := opensMarkdownFence(content); ok {
			fence, fenceWidth = mark, width
		} else if isAskHeading(content) {
			return offset, i
		}
		offset += len(line)
	}
	return -1, -1
}

// opensMarkdownFence recognizes a CommonMark-style backtick or tilde fence.
func opensMarkdownFence(line string) (mark byte, width int, ok bool) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i == len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, false
	}
	mark = line[i]
	for i+width < len(line) && line[i+width] == mark {
		width++
	}
	if width < 3 {
		return 0, 0, false
	}
	// CommonMark does not allow backticks in a backtick fence's info string.
	if mark == '`' && strings.Contains(line[i+width:], "`") {
		return 0, 0, false
	}
	return mark, width, true
}

func closesMarkdownFence(line string, mark byte, width int) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i == len(line) || line[i] != mark {
		return false
	}
	count := 0
	for i+count < len(line) && line[i+count] == mark {
		count++
	}
	return count >= width && strings.TrimSpace(line[i+count:]) == ""
}

// cutAskSection cuts a stage file's text at its ## Ask section, which the
// brief, prime and the web view leave out as they leave out the summary:
// line. Text without the section is unchanged.
func cutAskSection(text string) string {
	start, _ := askSectionStart(text)
	if start >= 0 {
		return strings.TrimRight(text[:start], " \t\r\n")
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
		contextLine, recommendLine, whyLine, optionsLine                      int // 1-based
		optionsAbove                                                          int // options before the 'options:' line
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
			// Past 'options:' there is no way back to the fields, and the
			// context is one: its lines would otherwise attach to it from here.
			if optionsLine != 0 {
				return fail(n, "a '- ' line after 'options:': context lines go under 'context:', above it")
			}
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
		// Past 'options:' every line is an option, whatever its name, so that
		// an outcome can be named like a field, options included.
		if optionsLine != 0 {
			options = append(options, askOptionLine{outcome: key, consequence: value, line: n})
			continue
		}
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
			if value != AskChoice && value != AskMoves && value != AskInput && value != AskCheck {
				return fail(n, "kind %q: want choice, moves, input or check", value)
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
		case "options":
			if value != "" {
				return fail(n, "options takes the '<outcome>: <consequence>' lines beneath it, not a value")
			}
			optionsLine, optionsAbove = n, len(options)
		default:
			options = append(options, askOptionLine{outcome: key, consequence: value, line: n})
		}
	}
	if t.Kind == "" {
		return fail(t.askLine, "kind is required: choice, moves, input or check")
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
	// Where the ask first takes options, for the kinds that take none: its
	// first option's line, or the 'options:' line when no option stands above it.
	firstOption := optionsLine
	if len(options) > 0 && (firstOption == 0 || options[0].line < firstOption) {
		firstOption = options[0].line
	}
	switch t.Kind {
	case AskChoice:
		if seenMove {
			return fail(t.moveLine, "a choice ask takes no 'move:': each option names its outcome")
		}
		t.Move = ""
		if optionsLine != 0 && len(options) == optionsAbove {
			return fail(optionsLine, "options needs at least one '<outcome>: <consequence>' line beneath it")
		}
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
	case AskMoves:
		if firstOption != 0 {
			return fail(firstOption, "a moves ask takes no options: the stage's moves are its options")
		}
		if seenRecommend {
			return fail(recommendLine, "a moves ask takes no 'recommend:': its options are the stage's moves, known when the ask is put")
		}
		if seenWhy {
			return fail(whyLine, "a moves ask takes no 'why:': it recommends nothing")
		}
		if seenMove {
			return fail(t.moveLine, "a moves ask takes no 'move:': each option is a move of the stage")
		}
		t.Move = ""
	case AskInput:
		if firstOption != 0 {
			return fail(firstOption, "an input ask takes no options: its answer is words, and 'move:' says what they do")
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
		if firstOption != 0 {
			return fail(firstOption, "a check ask takes no options: Done fires 'move:'")
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
	case AskMoves:
		// The moves are the stage's NextStages, including the first-stage
		// fallback for a bead reopened in a terminal or unknown stage.
		if len(f.Next(stage)) == 0 {
			return fmt.Errorf("ask line %d: stage %s has no way out under flow %s: a moves ask offers the stage's moves", t.askLine, stage, f.Name)
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
