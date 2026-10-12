package flowdraw

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// A running block carries its group's short name: what stands before the
// first dash, the whole name when there is none. A train's children carry
// their train's short id: what stands after the last dash.
func TestShortNames(t *testing.T) {
	for _, c := range []struct{ in, group, id string }{
		{"muse-builders", "muse", "builders"},
		{"yardr-reviewers", "yardr", "reviewers"},
		{"decisions", "decisions", "decisions"},
		{"shop-ab12", "shop", "ab12"},
		{"a-b-c", "a", "c"},
	} {
		if got := shortGroup(c.in); got != c.group {
			t.Errorf("shortGroup(%q) = %q, want %q", c.in, got, c.group)
		}
		if got := shortID(c.in); got != c.id {
			t.Errorf("shortID(%q) = %q, want %q", c.in, got, c.id)
		}
	}
}

// Blocks stand newest first, a train's children together with their train's
// short id once, past ten folded into "+N".
func TestRowOf(t *testing.T) {
	blocks := func(n int, train string) []Block {
		var out []Block
		for i := range n {
			out = append(out, Block{ID: fmt.Sprintf("%s-%d", train, i), Train: train})
		}
		return out
	}
	ids := func(items []item) []string {
		var out []string
		for _, it := range items {
			switch {
			case it.block != nil:
				out = append(out, it.block.ID)
			case it.train != "":
				out = append(out, "id:"+it.train)
			default:
				out = append(out, fmt.Sprintf("+%d", it.fold))
			}
		}
		return out
	}
	// Singles stand as they came, the newest first.
	in := []Block{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	if got, want := ids(rowOf(in)), []string{"a", "b", "c"}; !slices.Equal(got, want) {
		t.Errorf("singles stand %v, want %v", got, want)
	}
	// The train's children stand together where the newest stands, under
	// their train's short id once.
	mixed := []Block{{ID: "w2", Train: "shop-t1"}, {ID: "a"}, {ID: "w1", Train: "shop-t1"}, {ID: "b"}}
	if got, want := ids(rowOf(mixed)), []string{"id:t1", "w2", "w1", "a", "b"}; !slices.Equal(got, want) {
		t.Errorf("mixed stands %v, want %v", got, want)
	}
	// Past ten the rest fold, and a train left with no child standing
	// leaves its id out too.
	many := append(blocks(9, ""), blocks(3, "shop-t9")...)
	var want []string
	for i := range 9 {
		want = append(want, fmt.Sprintf("-%d", i))
	}
	want = append(want, "id:t9", "shop-t9-0", "+2")
	if got := ids(rowOf(many)); !slices.Equal(got, want) {
		t.Errorf("twelve stand %v, want %v", got, want)
	}
	// A train whose first child is the eleventh block is entirely folded;
	// its id must not survive ahead of +1.
	cutoff := append(blocks(10, ""), Block{ID: "wagon", Train: "shop-t10"})
	if got, want := ids(rowOf(cutoff)), append(ids(rowOf(blocks(10, ""))), "+1"); !slices.Equal(got, want) {
		t.Errorf("cut-off train stands %v, want %v", got, want)
	}
}

// What Panel writes is safe to send unescaped: every name from the yard in
// it is escaped here.
func TestPanelEscapes(t *testing.T) {
	f := flow(t, "code", "backlog -> new\nnew -> merged\nmerged terminal\n")
	d := Diagram{Flow: *f, Groups: map[string]Group{}}
	s := Standing{
		Blocks: map[string][]Block{
			"new": {{ID: "shop-1", Group: `evil"><script`, Working: true}},
		},
		Stage: func(stage string) string { return "/flows#at/shop//" + stage },
		Bead:  func(id string) string { return "/beads/" + id + "?x=1&y=2" },
	}
	out := string(Panel(d, s))
	for _, not := range []string{"><script", "?x=1&y=2", "<style", "xmlns"} {
		if strings.Contains(out, not) {
			t.Errorf("the drawing has %q unescaped", not)
		}
	}
	for _, want := range []string{
		`evil&#34;&gt;&lt;script`,
		`href="/beads/shop-1?x=1&amp;y=2"`,
		`<a href="/flows#at/shop//new">`,
		`<g class="platform lit" data-stage="new">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the drawing lacks %s", want)
		}
	}
	// The platform's stage link must not contain its independently linked
	// bead block. SVG anchors cannot be nested reliably by browsers.
	platformAt := strings.Index(out, `<g class="platform lit" data-stage="new">`)
	blockAt := strings.Index(out, `<a href="/beads/shop-1?x=1&amp;y=2">`)
	if platformAt < 0 || blockAt < 0 || strings.Index(out[platformAt:blockAt], `</a>`) < 0 {
		t.Error("the stage platform link wraps the block link")
	}
	// The same diagram and standing give the same bytes.
	if again := string(Panel(d, s)); again != out {
		t.Error("the drawing is not stable")
	}
}
