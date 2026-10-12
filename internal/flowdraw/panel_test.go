package flowdraw

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// A light's class tells its kind at a glance: solid red for held, hollow
// red for gave up, green with a red ring for blocked, hollow green for
// stalled, green while a session runs, blue for waiting on a dependency
// when otherwise quiet. A working or held bead that also waits keeps its
// colour and still names the bead holding it in its title.
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

// stagePart is the drawing of the stage's section or siding: from its
// group to the next stage's, or to the lines below.
func stagePart(t *testing.T, svg, stage string) string {
	t.Helper()
	start := strings.Index(svg, `data-stage="`+stage+`"`)
	if start < 0 {
		t.Fatalf("the drawing has no stage %s", stage)
	}
	start = strings.LastIndex(svg[:start], "<g")
	rest := svg[start:]
	next := len(rest)
	for _, g := range []string{`<g class="section`, `<g class="siding`, `<g class="loops`} {
		if i := strings.Index(rest[len(`<g`):], g); i >= 0 {
			next = min(next, len(`<g`)+i)
		}
	}
	return rest[:next]
}

// A failed-back diamond stands before its light in its link, a light's
// title is its bead and names what holds it, a quiet signal is an unlit
// outline while a wait on a stage with none is a bare lamp, a lit section
// runs, a terminal one is dashed, and a stage no route serves is dotted
// with the faint words "no route".
func TestMiniMarks(t *testing.T) {
	f := flow(t, "code", starterFlow)
	d := Diagram{Flow: *f}
	s := Standing{
		Blocks: map[string][]Block{
			"new": {
				{ID: "shop-held", Held: true},
				{ID: "shop-gave", Held: true, GaveUp: true},
				{ID: "shop-stall", Working: true, Stalled: true},
				{ID: "shop-block", Working: true, Blocked: true},
				{ID: "shop-dep", Dep: "shop-hold"},
				{ID: "shop-back", FailedBack: true},
			},
		},
		Lamps:   map[string]Lamp{"new": LampFault, "backlog": LampTurn},
		Noroute: map[string]bool{"review": true},
		Stage:   func(stage string) string { return "/flows#at/shop//" + stage },
		Bead:    func(id string) string { return "/beads/" + id },
	}
	out := string(Mini(d, s))
	for _, want := range []string{
		`<circle class="block held"`,
		`<title>shop-held</title>`,
		`<circle class="block gaveup"`,
		`<circle class="block stalled"`,
		`<circle class="block blocked"`,
		`<circle class="block dep"`,
		`<circle class="block"`,
		`<path class="diamond"`,
		`<title>shop-back</title>`,
		// The light's title names what holds it; no id is written on
		// the plan and none links to the bead holding.
		`<title>shop-dep waits on shop-hold</title>`,
		// Red where a fault waits, blue where a person's turn waits,
		// an unlit outline where a quiet signal stands.
		`<circle class="lamp fault"`,
		`<circle class="lamp turn"`,
		`<circle class="lamp unlit"`,
		// New runs: its section is lit. Review waits on no one and no
		// route serves it: dotted, with the faint words.
		`class="ln branch lit"`,
		`class="ln branch idle"`,
		`>no route<`,
		// The terminal is dashed.
		`data-stage="merged"`,
		`class="ln branch hand"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the drawing lacks %s", want)
		}
	}
	for _, not := range []string{
		`<a href="/beads/shop-hold">`,
		`>hold<`,
		`class="lamp"`,
		`class="platform"`,
		`class="group`,
	} {
		if strings.Contains(out, not) {
			t.Errorf("the drawing has %s", not)
		}
	}
	if n := strings.Count(out, `class="lamp fault"`); n != 1 {
		t.Errorf("%d fault lamps, want new's alone", n)
	}
	if n := strings.Count(out, `class="lamp turn"`); n != 1 {
		t.Errorf("%d turn lamps, want backlog's alone", n)
	}
	if n := strings.Count(out, `class="lamp unlit"`); n != 1 {
		t.Errorf("%d unlit lamps, want review's signal alone", n)
	}
	// The signal's post stands on its section alone: a wait on a stage
	// with no signal is a bare lamp.
	if n := strings.Count(stagePart(t, out, "review"), `<line class="ln"`); n != 1 {
		t.Errorf("review's signal stands on %d posts, want one", n)
	}
	for _, stage := range []string{"new", "backlog"} {
		if strings.Contains(stagePart(t, out, stage), `<line class="ln"`) {
			t.Errorf("the bare lamp on %s stands on a post", stage)
		}
	}
	// The diamond shares its light's link.
	backAt := strings.Index(out, "<title>shop-back</title>")
	linkEnd := strings.Index(out[backAt:], "</a>")
	if backAt < 0 || linkEnd < 0 || !strings.Contains(out[backAt:backAt+linkEnd], `<path class="diamond"`) {
		t.Error("no red diamond stands with the failed-back light")
	}
}

// No bead writes anything on the plan: every word on it is a stage's name
// and count, the fold, or "no route". No outcome, group or id stands on it.
func TestMiniWords(t *testing.T) {
	f := flow(t, "code", starterFlow)
	d := Diagram{Flow: *f}
	var backlog []Block
	for i := range 12 {
		backlog = append(backlog, Block{ID: fmt.Sprintf("shop-%d", i)})
	}
	s := Standing{
		Blocks: map[string][]Block{
			"backlog": backlog,
			"new": {
				{ID: "shop-run", Working: true},
				{ID: "shop-dep", Dep: "shop-hold"},
				{ID: "shop-back", FailedBack: true},
			},
			"decide": {{ID: "shop-park"}},
		},
		Lamps:   map[string]Lamp{"backlog": LampTurn},
		Noroute: map[string]bool{"approved": true},
	}
	out := string(Mini(d, s))
	allowed := map[string]bool{
		"backlog · 12": true, "new · 3": true, "review": true,
		"approved": true, "merged": true, "decide · 1": true,
		"+2": true, "no route": true,
	}
	texts := regexp.MustCompile(`<text[^>]*>([^<]*)</text>`).FindAllStringSubmatch(out, -1)
	if len(texts) == 0 {
		t.Fatal("the drawing writes no word at all")
	}
	for _, m := range texts {
		if !allowed[m[1]] {
			t.Errorf("the plan says %q", m[1])
		}
	}
	for _, not := range []string{
		"changes", "failed", "question", "done",
		"muse", "shop-", "hold",
	} {
		for _, m := range texts {
			if strings.Contains(m[1], not) {
				t.Errorf("the plan says %q in %q", not, m[1])
			}
		}
	}
}

// A flow's own signal is unlit unless the stage waits: red for a fault,
// blue for a person's turn. A wait on a stage with no signal is a bare
// lamp; a quiet stage with none carries nothing.
func TestMiniSignalLamp(t *testing.T) {
	f := flow(t, "code", "a -> b on done | c on changes\nb -> d on done | a on changes\nc -> d\nd terminal\n")
	d := Diagram{Flow: *f}
	// a and b can report changes; c is a siding; d is terminal.
	lit := string(Mini(d, Standing{Lamps: map[string]Lamp{"a": LampFault, "b": LampTurn, "c": LampFault}}))
	for stage, want := range map[string]struct {
		lamp  string
		posts int
	}{
		"a": {`class="lamp fault"`, 1},
		"b": {`class="lamp turn"`, 1},
		"c": {`class="lamp fault"`, 0},
	} {
		part := stagePart(t, lit, stage)
		if !strings.Contains(part, want.lamp) {
			t.Errorf("stage %s lights no %s", stage, want.lamp)
		}
		if n := strings.Count(part, `class="lamp `); n != 1 {
			t.Errorf("stage %s carries %d lamps, want one", stage, n)
		}
		if n := strings.Count(part, `<line class="ln"`); n != want.posts {
			t.Errorf("stage %s stands on %d posts, want %d", stage, n, want.posts)
		}
	}
	if part := stagePart(t, lit, "d"); strings.Contains(part, `class="lamp`) {
		t.Error("the quiet terminal carries a lamp")
	}
	quiet := string(Mini(d, Standing{}))
	for _, stage := range []string{"a", "b"} {
		part := stagePart(t, quiet, stage)
		if !strings.Contains(part, `class="lamp unlit"`) {
			t.Errorf("the quiet signal on %s is not an unlit outline", stage)
		}
	}
	for _, stage := range []string{"c", "d"} {
		if part := stagePart(t, quiet, stage); strings.Contains(part, `class="lamp`) {
			t.Errorf("quiet stage %s carries a lamp", stage)
		}
	}
	if strings.Contains(quiet, `class="lamp fault"`) || strings.Contains(quiet, `class="lamp turn"`) {
		t.Error("a quiet flow lights a lamp")
	}
}

// A yard with no fault, no hold and no failed return has no red element
// on the panel: no held, gave-up or blocked light, no diamond, no fault
// lamp, and no bare signal lamp burning red.
func TestMiniNoRedWhenQuiet(t *testing.T) {
	f := flow(t, "code", starterFlow)
	d := Diagram{Flow: *f}
	s := Standing{
		Blocks: map[string][]Block{
			"new": {
				{ID: "shop-run", Working: true},
				{ID: "shop-quiet"},
				{ID: "shop-dep", Dep: "shop-hold"},
			},
			"backlog": {{ID: "shop-park"}},
		},
		Lamps:   map[string]Lamp{"backlog": LampTurn},
		Noroute: map[string]bool{"review": true},
	}
	out := string(Mini(d, s))
	for _, not := range []string{
		`block held`, `block gaveup`, `block blocked`, `block stalled`,
		`diamond`, `lamp fault`, `class="lamp"`,
	} {
		if strings.Contains(out, not) {
			t.Errorf("a quiet yard shows %s", not)
		}
	}
	for _, want := range []string{
		`block running`, `block dep`, `lamp turn`, `lamp unlit`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("a quiet yard lacks %s", want)
		}
	}
}

// Lights stand newest first; past ten the rest fold into "+N", and the
// count beside the stage name always says the true number.
func TestMiniFold(t *testing.T) {
	f := flow(t, "code", "backlog -> new\nnew -> merged\nmerged terminal\n")
	d := Diagram{Flow: *f}
	var blocks []Block
	for i := range 12 {
		blocks = append(blocks, Block{ID: fmt.Sprintf("shop-%d", i)})
	}
	out := string(Mini(d, Standing{Blocks: map[string][]Block{"backlog": blocks}}))
	part := stagePart(t, out, "backlog")
	if n := strings.Count(part, `<circle class="block`); n != 10 {
		t.Errorf("%d lights stand, want ten", n)
	}
	var titles []string
	for _, m := range regexp.MustCompile(`<title>([^<]+)</title>`).FindAllStringSubmatch(part, -1) {
		titles = append(titles, m[1])
	}
	var want []string
	for i := range 10 {
		want = append(want, fmt.Sprintf("shop-%d", i))
	}
	if !slices.Equal(titles, want) {
		t.Errorf("lights stand %v, want %v", titles, want)
	}
	for _, s := range []string{`>+2<`, `>backlog · 12<`} {
		if !strings.Contains(part, s) {
			t.Errorf("the section lacks %s", s)
		}
	}
}

// The line plan is about a third as high as the signalling diagram, with
// no platform, no building and no word but the names: the board's is the
// only rect on it.
func TestMiniIsThird(t *testing.T) {
	d := Diagram{Flow: *flow(t, "default", starterFlow), Groups: map[string]Group{
		"backlog": {Name: "backlog", Kind: People},
		"new":     {Name: "builders", Kind: Sessions, Limit: 5},
		"review":  {Name: "reviewers", Kind: People},
		"decide":  {Name: "decisions", Kind: People},
	}}
	var backlog []Block
	for i := range 12 {
		backlog = append(backlog, Block{ID: fmt.Sprintf("shop-%d", i)})
	}
	s := Standing{
		Blocks:  map[string][]Block{"backlog": backlog, "new": {{ID: "shop-run", Working: true}}},
		Lamps:   map[string]Lamp{"backlog": LampTurn},
		Noroute: map[string]bool{"approved": true},
	}
	height := func(svg string) int {
		m := regexp.MustCompile(`viewBox="0 0 \d+ (\d+)"`).FindStringSubmatch(svg)
		if m == nil {
			t.Fatalf("no viewBox in %q", svg[:100])
		}
		var h int
		fmt.Sscanf(m[1], "%d", &h)
		return h
	}
	miniH, fullH := height(string(Mini(d, s))), height(string(SVG(d)))
	t.Logf("mini %d high, full %d", miniH, fullH)
	if miniH*2 >= fullH {
		t.Errorf("the line plan is %d high, want less than half the full %d", miniH, fullH)
	}
	out := string(Mini(d, s))
	for _, not := range []string{`class="platform"`, `class="group`, `>changes<`, `>question<`} {
		if strings.Contains(out, not) {
			t.Errorf("the line plan has %s", not)
		}
	}
	if n := strings.Count(out, "<rect"); n != 1 {
		t.Errorf("the line plan has %d rects, want the board's alone", n)
	}
}

// The full drawing is unchanged: the command line's signal still burns
// red, as it always has.
func TestFullDrawingUnchanged(t *testing.T) {
	out := string(SVG(Diagram{Flow: *flow(t, "default", starterFlow)}))
	for _, want := range []string{`<g class="signal">`, `<circle class="lamp"`} {
		if !strings.Contains(out, want) {
			t.Errorf("the full drawing lacks %s", want)
		}
	}
	if strings.Contains(out, `class="lamp unlit"`) {
		t.Error("the full drawing lights an unlit lamp")
	}
}

// MiniSVG is the line plan bare, as a file of its own: no bead on it, no
// count, the signals unlit outlines.
func TestMiniSVG(t *testing.T) {
	out := string(MiniSVG(Diagram{Flow: *flow(t, "default", starterFlow)}))
	for _, want := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg" class="flowdraw"`,
		`prefers-color-scheme:dark`,
		`<circle class="lamp unlit"`,
		`>review<`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the bare line plan lacks %s", want)
		}
	}
	for _, not := range []string{`class="block`, ` · `, `class="lamp fault"`, `class="lamp"`} {
		if strings.Contains(out, not) {
			t.Errorf("the bare line plan has %s", not)
		}
	}
}

// What Mini writes is safe to send unescaped: every name from the yard in
// it is escaped here.
func TestMiniEscapes(t *testing.T) {
	f := flow(t, "code", "backlog -> new\nnew -> merged\nmerged terminal\n")
	d := Diagram{Flow: *f}
	s := Standing{
		Blocks: map[string][]Block{
			"new": {{ID: `x"><script`, Dep: `d&<e`}},
		},
		Stage: func(stage string) string { return "/flows#at/shop//" + stage },
		Bead:  func(id string) string { return "/beads/" + id + "?x=1&y=2" },
	}
	out := string(Mini(d, s))
	for _, not := range []string{`"><script`, `?x=1&y=2`, `d&<e`, "<style", "xmlns"} {
		if strings.Contains(out, not) {
			t.Errorf("the drawing has %q unescaped", not)
		}
	}
	for _, want := range []string{
		`x&#34;&gt;&lt;script`,
		`waits on d&amp;&lt;e`,
		`href="/beads/x&#34;&gt;&lt;script?x=1&amp;y=2"`,
		`<a href="/flows#at/shop//new">`,
		`<g class="section" data-stage="new">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the drawing lacks %s", want)
		}
	}
	// The section's stage link must not contain its independently linked
	// bead light. SVG anchors cannot be nested reliably by browsers.
	part := stagePart(t, out, "new")
	beadAt := strings.Index(part, `<a href="/beads/`)
	if beadAt < 0 || !strings.Contains(part[:beadAt], "</a>") {
		t.Error("the stage section link wraps the light link")
	}
	// The same diagram and standing give the same bytes.
	if again := string(Mini(d, s)); again != out {
		t.Error("the drawing is not stable")
	}
}
