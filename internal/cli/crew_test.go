package cli

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// firstLine cut at 100 bytes, so a line of multi-byte characters (the row of
// "─" a harness draws) ended inside one and printed as "�…" (yardr-tpxc).
func TestFirstLineCutsBetweenCharacters(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"short", "  one line  ", "one line"},
		{"first of several", "one\ntwo\n", "one"},
		{"100 ascii is whole", strings.Repeat("a", 100), strings.Repeat("a", 100)},
		{"101 ascii is cut", strings.Repeat("a", 101), strings.Repeat("a", 100) + "…"},
		{"90 three-byte is whole", strings.Repeat("─", 90), strings.Repeat("─", 90)},
		{"100 three-byte is whole", strings.Repeat("─", 100), strings.Repeat("─", 100)},
		{"101 three-byte is cut", strings.Repeat("─", 101), strings.Repeat("─", 100) + "…"},
		// 99 one-byte characters put every later boundary off a multiple of
		// the character's width, whatever byte count the cut is made at.
		{"mixed widths", strings.Repeat("a", 99) + "───", strings.Repeat("a", 99) + "─…"},
		{"cut line of several", strings.Repeat("é", 150) + "\nrest", strings.Repeat("é", 100) + "…"},
	} {
		got := firstLine(tc.in)
		if got != tc.want {
			t.Errorf("%s: firstLine = %q, want %q", tc.name, got, tc.want)
		}
		if !utf8.ValidString(got) {
			t.Errorf("%s: firstLine = %q, not valid UTF-8", tc.name, got)
		}
	}
}
