// Reading the command a run ask names: the list as a refusal and the help
// of yardr bead ask name it (RunCommands), and the one reading of the
// line, split at white space and nowhere else, into a row of the list in
// its stored form. It reads no record of the yard.
package store

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// RunCommands are the commands of the list, each as a refusal and the help
// of yardr bead ask name it.
func RunCommands() []string {
	var uses []string
	for _, c := range runList {
		uses = append(uses, c.use)
	}
	return uses
}

// RunArgvHeld reports whether the run command argv holds, as its asker typed
// it, a value or a path: settings set, whose value is a setting's (the
// operator's own words to the agents, the address hooks are registered at, or
// what Settings withholds from a device), source add, whose file is a
// path on the yard's machine, and pack apply with any word past the pack's
// name, whose mappings are the operator's configuration as the card's are
// (Mappings). Bead and UserAsks tell those words to a device
// of the operator alone, and the sentence of an approval of one is marked for
// WithoutHeld. Every other row's words are names and numbers the stream and
// the calls tell every device already.
func RunArgvHeld(argv []string) bool {
	return len(argv) >= 2 && (slices.Equal(argv[:2], []string{"settings", "set"}) ||
		slices.Equal(argv[:2], []string{"source", "add"})) ||
		len(argv) > 3 && slices.Equal(argv[:2], []string{"pack", "apply"})
}

// errNotRow is when the words name no row of the list: askedRun then
// tries what is the operator's alone, and approveAsk refuses what the
// yard did not write.
var errNotRow = errors.New("is no command the yard runs from a card")

// runNoneChar refuses a character no run command takes.
func runNoneChar(c rune) error {
	return fmt.Errorf("--run takes one yardr command of names, numbers and flags, split at spaces: %q is none of their characters, and nothing is quoted. A command that needs it is the operator's to type in a terminal", c)
}

// unknownRun refuses words that are no row of the list and nothing the
// yard names as alone or the asker's own to run.
func unknownRun() error {
	return fmt.Errorf("--run takes a command of the list (%s): anything else is run directly, or typed by the operator in a terminal", strings.Join(RunCommands(), ", "))
}

// parseRun reads the command a run ask names, as its writer sent it, and
// answers its row of the list with the command in the yard's own form. The
// line is split at white space and nowhere else, and is read by nothing
// after this: a character outside peer add's invite that no name, number
// or flag of the list has is refused, so there is no quoting and no second
// reading of the line.
func parseRun(sent []string) (*runCommand, []string, error) {
	c, argv, err := parseRow(sent)
	if err == errNotRow {
		return nil, nil, unknownRun()
	}
	return c, argv, err
}

// parseRow is parseRun for a row of the list, and errNotRow for anything
// else: a command the asker may run themselves, one that is the
// operator's alone, or none the yard runs from a card. askedRun tells
// those apart; approveAsk only ever sees what askedRun stored.
func parseRow(sent []string) (*runCommand, []string, error) {
	line := strings.Join(sent, " ")
	words := strings.Fields(line)
	wordOffset := 0
	if len(words) > 0 && words[0] == "yardr" {
		words = words[1:]
		wordOffset = 1
	}
	var row *runCommand
	for i := range runList {
		c := &runList[i]
		if len(words) >= len(c.words) && slices.Equal(words[:len(c.words)], c.words) {
			row = c
			break
		}
	}
	word, inWord := -1, false
	for _, char := range line {
		if char == ' ' || char == '\t' {
			inWord = false
			continue
		}
		if unicode.IsControl(char) || (unicode.IsSpace(char) && char != ' ') {
			return nil, nil, runNoneChar(char)
		}
		if !inWord {
			word++
			inWord = true
		}
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case strings.ContainsRune("._/=-", char):
		case char == '@' || char == ':' || char == '+':
			// A trust is a name at a peer, an address is a host and a
			// port or holds a '+' (an invite's address): the row that
			// reads them says which.
		case char == ',':
			// Held to the command that takes a list, below.
		case row != nil && slices.Equal(row.words, []string{"peer", "add"}) && word-wordOffset == len(row.words)+1:
			// The invite is one word; ParseInvite checks it after the
			// command has been split, including its URL's punctuation.
		default:
			return nil, nil, runNoneChar(char)
		}
	}
	if row == nil {
		return nil, nil, errNotRow
	}
	if strings.ContainsRune(line, ',') && !row.commas {
		return nil, nil, runNoneChar(',')
	}
	rest, err := row.parse(words[len(row.words):])
	if err != nil {
		return nil, nil, err
	}
	return row, append(slices.Clone(row.words), rest...), nil
}
