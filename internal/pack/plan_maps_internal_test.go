package pack

import (
	"slices"
	"strings"
	"testing"
)

// line stands in for a says or makes line: merged goes by how a line is
// written, nothing else.
type line string

func (l line) String() string { return string(l) }

func lines(words string) []line {
	var out []line
	for _, w := range strings.Fields(words) {
		out = append(out, line(w))
	}
	return out
}

// The table a pack's lines are set in keeps every other line where it stands
// and the pack's own in the order the pack wrote them (the first line an event
// matches counts): the pack's lines the table has stand in their places, a
// line it gains goes before the pack line that follows it, the last after the
// rest, and what it let go of goes. Capital letters are the pack's lines,
// small ones are another pack's or the operator's.
func TestMergedTable(t *testing.T) {
	for _, c := range []struct {
		name             string
		have, drop, want string
		out              string
	}{
		{"the same lines change nothing", "a B c D e", "", "B D", "a B c D e"},
		{"a first import goes last, in the pack's order", "a b", "", "B C", "a b B C"},
		{"into an empty table", "", "", "B C", "B C"},
		{"a line the pack lets go of goes alone", "a B c D e", "C", "B D", "a B c D e"},
		{"a line it let go of is taken off", "a B C D e", "C", "B D", "a B D e"},
		{"a gained line after its last goes last", "a B c D e", "", "B D F", "a B c D e F"},
		{"a gained line before the first goes before it", "a B c D e", "", "F B D", "a F B c D e"},
		{"a gained line between goes before its successor", "a B c D e", "", "B F D", "a B c F D e"},
		{"several gained lines keep the pack's order", "a B c D e", "", "F B G H D I", "a F B c G H D e I"},
		{"a replaced line stands in its place", "a B C D e", "C", "B F D", "a B F D e"},
		{"a reorder reorders the pack's lines alone", "a D b B c", "", "B D", "a B b D c"},
		{"a pack line twice in the table stands once", "B a B", "", "B", "B a"},
		{"a repeated line does not move the next pack line", "B a B c D", "", "B D", "B a c D"},
		{"a pack line given twice stands once", "a B c", "", "B B F", "a B c F"},
		{"every line let go of", "a B c D", "B D", "F", "a c F"},
	} {
		t.Run(c.name, func(t *testing.T) {
			// merged is told which lines are the pack's by drop and want
			// alone; the capitals only make the cases easy to read.
			got := merged(lines(c.have), lines(c.drop), lines(c.want))
			if want := lines(c.out); !slices.Equal(got, want) {
				t.Errorf("merged(%q, drop %q, want %q) = %q, want %q", c.have, c.drop, c.want, got, want)
			}
			// Done again, it is done: an import is once.
			if again := merged(got, nil, lines(c.want)); !slices.Equal(again, got) {
				t.Errorf("merged again = %q, want %q", again, got)
			}
		})
	}
}
