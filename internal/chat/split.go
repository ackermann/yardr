package chat

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"yardr.dev/yardr/internal/store"
)

// MaxParts is the most mails one text is sent as. The line takes no more:
// a text of that size is a document, and belongs in a file the mail names.
const MaxParts = 16

// room is what one part holds of the text: a mail, less the first line
// that numbers it. That line is counted at its longest, so where a text is
// split does not depend on how many parts it comes to.
var room = store.MaxMailBytes - len(mark(MaxParts, MaxParts))

// mark is the first line of part i of n. It is what tells the receiver
// that the mails belong together and whether more follow.
func mark(i, n int) string { return fmt.Sprintf("(%d/%d)\n", i, n) }

// part is one mail's share of a text, and where in the text it begins.
type part struct {
	at   int
	text string
}

// mail is the part as it is sent: numbered, when the text has more than
// this one.
func (p part) mail(i, n int) string {
	if n == 1 {
		return p.text
	}
	return mark(i+1, n) + p.text
}

// split is text as the parts it is sent in: itself when one mail holds it,
// else consecutive parts of at most room bytes. The break a part ends at is
// not sent, the numbering stands for it.
func split(text string) []part {
	if len(text) <= store.MaxMailBytes {
		return []part{{text: text}}
	}
	var parts []part
	at := 0
	for rest := text; rest != ""; {
		cut, next := len(rest), len(rest)
		if len(rest) > room {
			cut, next = breakAt(rest)
		}
		parts = append(parts, part{at: at, text: rest[:cut]})
		at += next
		rest = rest[next:]
	}
	return parts
}

// breakAt is where a part of s, which is longer than a part, ends (cut)
// and where the next begins (next). In order: the last paragraph break that
// leaves the part at least half full, the last line break, the last space,
// and for a line without any the limit itself, never inside a character.
func breakAt(s string) (cut, next int) {
	// A break may begin right where the part is full.
	if i := strings.LastIndex(s[:min(room+2, len(s))], "\n\n"); i >= room/2 {
		if cut, next = around(s, i, "\n"); cut > 0 {
			return cut, next
		}
	}
	for _, set := range []string{"\n", " \t"} {
		if i := strings.LastIndexAny(s[:room+1], set); i > 0 {
			if cut, next = around(s, i, set); cut > 0 {
				return cut, next
			}
		}
	}
	for cut = room; cut > 0 && !utf8.RuneStart(s[cut]); cut-- {
	}
	if cut == 0 {
		cut = room // no character begins here at all: bytes, cut as such
	}
	return cut, cut
}

// around widens a break found at s[i] to the whole run of set it is in:
// the part ends before the run and the next begins after it.
func around(s string, i int, set string) (cut, next int) {
	cut = len(strings.TrimRight(s[:i], set))
	return cut, len(s) - len(strings.TrimLeft(s[cut:], set))
}
