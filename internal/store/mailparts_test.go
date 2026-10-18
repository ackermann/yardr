package store

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// prose is a text of about size bytes: paragraphs of lines of words.
func prose(size int) string {
	var b strings.Builder
	for n := 0; b.Len() < size; n++ {
		switch {
		case n == 0:
		case n%40 == 0:
			b.WriteString("\n\n")
		case n%8 == 0:
			b.WriteString("\n")
		default:
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "word%04d", n)
	}
	return b.String()
}

// A text of two and a half mails is three parts, each within a mail with
// its number, split at the best break there is: a paragraph's end, else a
// line's, else a space, else between two characters.
func TestSplitMail(t *testing.T) {
	t.Parallel()
	size := 5 * MaxMailBytes / 2
	lines := strings.ReplaceAll(prose(size), "\n\n", "\n")
	words := strings.ReplaceAll(lines, "\n", " ")
	for _, c := range []struct{ name, text, sep string }{
		{"paragraphs", prose(size), "\n\n"},
		{"lines", lines, "\n"},
		{"words", words, " "},
		{"two bytes a character", strings.Repeat("é", size/2), ""},
		{"three", "x" + strings.Repeat("€", size/3), ""},
		{"four", "xy" + strings.Repeat("🚂", size/4), ""},
	} {
		parts := SplitMail(c.text)
		if len(parts) != 3 {
			t.Fatalf("%s: %d parts", c.name, len(parts))
		}
		var texts []string
		for i, p := range parts {
			mail := p.Mail(i, len(parts))
			if len(mail) > MaxMailBytes || !strings.HasPrefix(mail, fmt.Sprintf("(%d/3)\n", i+1)) {
				t.Fatalf("%s: part %d has %d bytes and begins %q", c.name, i+1, len(mail), mail[:8])
			}
			if !utf8.ValidString(p.Text) {
				t.Fatalf("%s: part %d is cut inside a character", c.name, i+1)
			}
			if p.Text != strings.TrimSpace(p.Text) || !strings.HasPrefix(c.text[p.At:], p.Text) {
				t.Fatalf("%s: part %d is not the text from byte %d on: %q…", c.name, i+1, p.At, p.Text[:20])
			}
			texts = append(texts, p.Text)
		}
		// Nothing is lost but the breaks themselves, and no part ends at
		// a lesser break than the text has.
		if got := strings.Join(texts, c.sep); got != c.text {
			t.Fatalf("%s: the parts do not make the text again", c.name)
		}
	}

	// A paragraph break that would leave a part nearly empty is out of
	// reach, and a line that is longer than a mail alone is cut at the limit.
	long := strings.Repeat("x", MaxMailBytes+100)
	parts := SplitMail("short\n\n" + long + "\nlast line")
	if len(parts) != 3 || parts[0].Text != "short" || parts[1].Text != long[:mailRoom] || parts[2].Text != long[mailRoom:]+"\nlast line" {
		t.Fatalf("%d parts: %q… %d bytes, %d bytes", len(parts), parts[0].Text[:5], len(parts[1].Text), len(parts[2].Text))
	}
	early := "short\n\n" + strings.ReplaceAll(prose(3*MaxMailBytes/2), "\n\n", "\n")
	if parts = SplitMail(early); len(parts) != 2 || len(parts[0].Text) < mailRoom-80 {
		t.Fatalf("an early paragraph break was taken: %d parts, the first of %d bytes", len(parts), len(parts[0].Text))
	}
}

// A text of one mail's size is one part, sent as it is.
func TestSplitMailOne(t *testing.T) {
	t.Parallel()
	text := "(1/2) is what a part begins with\n\n" + strings.Repeat("x", MaxMailBytes)
	text = text[:MaxMailBytes]
	if parts := SplitMail(text); len(parts) != 1 || parts[0].Mail(0, 1) != text {
		t.Fatalf("a text of one mail's size: %d parts", len(parts))
	}
}
