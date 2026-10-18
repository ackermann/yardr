package store

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

// MaxMailParts is the most mails one text is sent as. The line takes no
// more: a text of that size is a document, and belongs in a file the mail
// names.
const MaxMailParts = 16

// ErrMailTooLong refuses a text no number of mails holds: past MaxMailParts
// parts it goes as a file instead.
var ErrMailTooLong = fmt.Errorf("a text is sent as at most %d mails (about %d KB): send it as a file (--file)",
	MaxMailParts, MaxMailParts*MaxMailBytes>>10)

// mailRoom is what one part holds of the text: a mail, less the first line
// that numbers it. That line is counted at its longest, so where a text is
// split does not depend on how many parts it comes to.
var mailRoom = MaxMailBytes - len(mailMark(MaxMailParts, MaxMailParts))

// mailMark is the first line of part i of n. It is what tells the receiver
// that the mails belong together and whether more follow.
func mailMark(i, n int) string { return fmt.Sprintf("(%d/%d)\n", i, n) }

// MailPart is one mail's share of a text, and where in the text it begins.
type MailPart struct {
	At   int
	Text string
}

// Mail is the part as it is sent: numbered, when the text has more than
// this one.
func (p MailPart) Mail(i, n int) string {
	if n == 1 {
		return p.Text
	}
	return mailMark(i+1, n) + p.Text
}

// SplitMail is text as the parts it is sent in: itself when one mail holds
// it, else consecutive parts of at most mailRoom bytes. The break a part
// ends at is not sent, the numbering stands for it.
func SplitMail(text string) []MailPart {
	if len(text) <= MaxMailBytes {
		return []MailPart{{Text: text}}
	}
	var parts []MailPart
	at := 0
	for rest := text; rest != ""; {
		cut, next := len(rest), len(rest)
		if len(rest) > mailRoom {
			cut, next = mailBreakAt(rest)
		}
		parts = append(parts, MailPart{At: at, Text: rest[:cut]})
		at += next
		rest = rest[next:]
	}
	return parts
}

// mailBreakAt is where a part of s, which is longer than a part, ends (cut)
// and where the next begins (next). In order: the last paragraph break that
// leaves the part at least half full, the last line break, the last space,
// and for a line without any the limit itself, never inside a character.
func mailBreakAt(s string) (cut, next int) {
	// A break may begin right where the part is full.
	if i := strings.LastIndex(s[:min(mailRoom+2, len(s))], "\n\n"); i >= mailRoom/2 {
		if cut, next = mailAround(s, i, "\n"); cut > 0 {
			return cut, next
		}
	}
	for _, set := range []string{"\n", " \t"} {
		if i := strings.LastIndexAny(s[:mailRoom+1], set); i > 0 {
			if cut, next = mailAround(s, i, set); cut > 0 {
				return cut, next
			}
		}
	}
	for cut = mailRoom; cut > 0 && !utf8.RuneStart(s[cut]); cut-- {
	}
	if cut == 0 {
		cut = mailRoom // no character begins here at all: bytes, cut as such
	}
	return cut, cut
}

// mailAround widens a break found at s[i] to the whole run of set it is in:
// the part ends before the run and the next begins after it.
func mailAround(s string, i int, set string) (cut, next int) {
	cut = len(strings.TrimRight(s[:i], set))
	return cut, len(s) - len(strings.TrimLeft(s[cut:], set))
}

// MailText sends text as mail to to from actor: as one mail when one holds
// it, else as its parts, in order, each numbered (SplitMail). Files go with
// the first part only. A text past MaxMailParts parts is refused whole
// (ErrMailTooLong); a part that is not taken ends the send, so that no
// later part arrives without it. It is what every way of writing mail sends
// through but yardr chat, which numbers the parts itself to keep what was
// not sent in the line.
func MailText(api API, ctx context.Context, to, text, actor string, files ...BlobFile) error {
	parts := SplitMail(text)
	if len(parts) > MaxMailParts {
		return ErrMailTooLong
	}
	for i, p := range parts {
		var first []BlobFile
		if i == 0 {
			first = files
		}
		if err := api.Mail(ctx, to, p.Mail(i, len(parts)), actor, first...); err != nil {
			if len(parts) == 1 {
				return err
			}
			return fmt.Errorf("%d of %d mails went to %s: %w", i, len(parts), to, err)
		}
	}
	return nil
}
