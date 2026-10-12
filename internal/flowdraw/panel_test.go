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

// A block's class tells its kind at a glance: solid red for held, hollow
// red for gave up, green with a red ring for blocked, hollow green for
// stalled, green while a session runs, blue for waiting on a dependency
// when otherwise quiet. A working or held block that also waits keeps its
// colour and still carries the blocker's id.
func TestBlockClass(t *testing.T) {
	for _, c := range []struct {
		block Block
		want  string
	}{
		{Block{ID: "a"}, "block"},
		{Block{ID: "a", Working: true}, "block running"},
		{Block{ID: "a", Held: true}, "block held"},
		{Block{ID: "a", Held: true, GaveUp: true}, "block gaveup"},
		{Block{ID: "a", Working: true, Blocked: true}, "block blocked"},
		{Block{ID: "a", Working: true, Stalled: true}, "block stalled"},
		{Block{ID: "a", Dep: "shop-x1"}, "block dep"},
		{Block{ID: "a", Working: true, Dep: "shop-x1"}, "block running"},
		{Block{ID: "a", Held: true, Dep: "shop-x1"}, "block held"},
		{Block{ID: "a", Held: true, GaveUp: true, Blocked: true}, "block gaveup"},
		{Block{ID: "a", Working: true, Blocked: true, Stalled: true}, "block blocked"},
	} {
		if got := c.block.class(); got != c.want {
			t.Errorf("%+v class = %q, want %q", c.block, got, c.want)
		}
	}
}

// A failed-back diamond stands before its block, a dependency's short id
// after it linking to the bead holding, a wait lamp left of its platform,
// and a stage no route serves is hollow with the faint words "no route".
func TestPanelMarks(t *testing.T) {
	f := flow(t, "code", "backlog -> new\nnew -> review on done | backlog\nreview -> approved on done | new on changes\napproved -> merged on done\nmerged terminal\n")
	d := Diagram{Flow: *f, Groups: map[string]Group{"new": {Name: "muse-builders", Kind: Sessions, Limit: 2}}}
	s := Standing{
		Blocks: map[string][]Block{
			"new": {
				{ID: "shop-held", Held: true},
				{ID: "shop-gave", Held: true, GaveUp: true},
				{ID: "shop-stall", Working: true, Group: "muse-builders", Stalled: true},
				{ID: "shop-block", Working: true, Group: "muse-builders", Blocked: true},
				{ID: "shop-dep", Dep: "shop-hold"},
				{ID: "shop-back", FailedBack: true},
			},
		},
		Lamps:   map[string]Lamp{"new": LampFault, "backlog": LampTurn},
		Noroute: map[string]bool{"review": true},
		Stage:   func(stage string) string { return "/flows#at/shop//" + stage },
		Bead:    func(id string) string { return "/beads/" + id },
	}
	out := string(Panel(d, s))
	for _, want := range []string{
		`<rect class="block held"`,
		`<title>shop-held</title>`,
		`<rect class="block gaveup"`,
		`<rect class="block stalled"`,
		`<rect class="block blocked"`,
		`<rect class="block dep"`,
		`<path class="diamond"`,
		`<title>shop-back</title>`,
		// The dependency's short id follows its block and links to the
		// bead holding.
		`<a href="/beads/shop-hold">`,
		`class="small dep">hold<`,
		// Red where a fault waits, blue where a person's turn waits.
		`<circle class="lamp fault"`,
		`<circle class="lamp turn"`,
		// Hollow with the faint words where no route serves.
		`<g class="platform noroute" data-stage="review">`,
		`>no route<`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the drawing lacks %s", want)
		}
	}
	// The diamond shares its block's link; the dependency's id links
	// elsewhere, to the bead holding.
	backAt := strings.Index(out, "<title>shop-back</title>")
	diamondAt := strings.Index(out, `<path class="diamond"`)
	rectAt := strings.Index(out[backAt:], `<rect class="block"`)
	if backAt < 0 || diamondAt < 0 || rectAt < 0 || diamondAt < backAt || diamondAt > backAt+rectAt {
		t.Error("the diamond does not stand with its block in its link")
	}
	depLink := `<a href="/beads/shop-hold">`
	depAt := strings.Index(out, depLink)
	blockAt := strings.Index(out, "<title>shop-dep</title>")
	if depAt < 0 || blockAt < 0 || depAt < blockAt {
		t.Error("the dependency's id does not follow its block to its holder")
	}
	// A row with diamonds and dependencies stands wider than its blocks
	// alone, so the pitch holds it.
	plain := rowW(rowOf([]Block{{ID: "a"}, {ID: "b"}}))
	marked := rowW(rowOf([]Block{{ID: "a", FailedBack: true}, {ID: "b", Dep: "shop-hold"}}))
	if marked <= plain {
		t.Errorf("marked row width %d, want more than plain %d", marked, plain)
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
