package flowdraw

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

func flow(t *testing.T, name, text string) *store.Flow {
	t.Helper()
	f, err := store.ReadFlow(name, text)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

const starterFlow = `backlog -> new | decide
new -> review on done | decide on question | backlog
review -> approved on done | new on changes | decide on question
approved -> merged on done | new on failed | decide on question
decide -> backlog | new
merged terminal
`

// yardr's own train flow, as its repository has it: the platforms stand in
// the order a train travels, merge (off the main path, but worked by a
// group) where the walk reaches it, the terminal stage last, and decide on a
// siding.
func TestTrainFlowOrder(t *testing.T) {
	text, err := os.ReadFile("../../.yardr/flows/yardr.train.flow")
	if err != nil {
		t.Fatal(err)
	}
	p := planOf(flow(t, "yardr.train", string(text)))
	if want := []string{"backlog", "open", "review", "merge", "review-security", "approved", "in-pr", "land", "merged"}; !slices.Equal(p.line, want) {
		t.Errorf("line = %v, want %v", p.line, want)
	}
	if want := []string{"decide"}; !slices.Equal(p.sidings, want) {
		t.Errorf("sidings = %v, want %v", p.sidings, want)
	}
	// review's way on is review-security, past merge: the track on either
	// side of merge carries nothing, and the way on is a line below.
	if want := []bool{true, true, false, false, true, true, true, true}; !slices.Equal(p.track, want) {
		t.Errorf("track = %v, want %v", p.track, want)
	}
	if !slices.ContainsFunc(p.loops, func(l loop) bool {
		return l.from == "review" && l.to == "review-security" && slices.Equal(l.outcomes, []string{"done"})
	}) {
		t.Errorf("no line below for review -> review-security on done: %+v", p.loops)
	}
	// merge leads to open on two outcomes: one line, both named.
	if !slices.ContainsFunc(p.loops, func(l loop) bool {
		return l.from == "merge" && l.to == "open" && slices.Equal(l.outcomes, []string{"done", "rework"})
	}) {
		t.Errorf("no single line for merge -> open on done, rework: %+v", p.loops)
	}
	if want := []string{"review", "review-security", "approved", "in-pr"}; !slices.Equal(p.signals, want) {
		t.Errorf("signals = %v, want %v", p.signals, want)
	}
}

// The siding, the return lines and the signal of the flow a depot starts with.
func TestPlanSidingAndReturnLines(t *testing.T) {
	p := planOf(flow(t, "default", starterFlow))
	if want := []string{"backlog", "new", "review", "approved", "merged"}; !slices.Equal(p.line, want) {
		t.Errorf("line = %v, want %v", p.line, want)
	}
	if want := []string{"decide"}; !slices.Equal(p.sidings, want) {
		t.Errorf("sidings = %v, want %v", p.sidings, want)
	}
	if want := []bool{true, true, true, true}; !slices.Equal(p.track, want) {
		t.Errorf("track = %v, want %v", p.track, want)
	}
	wantLoops := []loop{
		{from: "new", to: "backlog"}, // by hand: no outcome
		{from: "review", to: "new", outcomes: []string{"changes"}},
		{from: "approved", to: "new", outcomes: []string{"failed"}},
	}
	if !slices.EqualFunc(p.loops, wantLoops, func(a, b loop) bool {
		return a.from == b.from && a.to == b.to && slices.Equal(a.outcomes, b.outcomes)
	}) {
		t.Errorf("loops = %+v, want %+v", p.loops, wantLoops)
	}
	wantSpurs := []spur{
		{siding: "decide", stage: "backlog", in: true},
		{siding: "decide", stage: "new", in: true, outcomes: []string{"question"}},
		{siding: "decide", stage: "review", in: true, outcomes: []string{"question"}},
		{siding: "decide", stage: "approved", in: true, outcomes: []string{"question"}},
		{siding: "decide", stage: "backlog"},
		{siding: "decide", stage: "new"},
	}
	if !slices.EqualFunc(p.spurs, wantSpurs, func(a, b spur) bool {
		return a.siding == b.siding && a.stage == b.stage && a.in == b.in && slices.Equal(a.outcomes, b.outcomes)
	}) {
		t.Errorf("spurs = %+v, want %+v", p.spurs, wantSpurs)
	}
	if want := []string{"review"}; !slices.Equal(p.signals, want) {
		t.Errorf("signals = %v, want %v", p.signals, want)
	}

	svg := string(SVG(Diagram{Flow: *flow(t, "default", starterFlow)}))
	for _, want := range []string{
		`<g class="siding" data-stage="decide">`,
		`<g class="loops" data-to="new">`,
		`<path class="ln branch" data-from="review"`,
		// The move by hand is dashed.
		`<path class="ln branch hand" data-from="new"`,
		`>changes</text>`, `>failed</text>`, `>question</text>`,
		`<g class="platform terminal" data-stage="merged">`,
		`<g class="signal">`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("the drawing lacks %s", want)
		}
	}
	if n := strings.Count(svg, `<g class="signal">`); n != 1 {
		t.Errorf("%d signals drawn, want review's alone", n)
	}
	if strings.Contains(svg, `class="group`) {
		t.Error("a flow drawn without groups has a building")
	}
	// When one stage has two ways back, each label sits by its own drop.
	sharedSource := flow(t, "labels", "a -> b on done\nb -> c on done\nc -> a on changes | b on failed\n")
	labelsSVG := string(SVG(Diagram{Flow: *sharedSource}))
	labels := regexp.MustCompile(`<text x="([0-9]+)" y="[0-9]+" class="small end">(changes|failed)</text>`).FindAllStringSubmatch(labelsSVG, -1)
	if len(labels) != 2 {
		t.Errorf("return outcome labels = %v, want changes and failed", labels)
	} else if labels[0][2] == labels[1][2] || labels[0][1] == labels[1][1] {
		t.Errorf("return outcome labels share an x position: %v", labels)
	}
	t.Run("limited loops and redirects", limitedLoopAndRedirect)
}

// A limited loop's label reads its outcome and limit, and the redirect to
// its Then is drawn as the edge to that stage with its own label, even
// where another edge already leads there.
func limitedLoopAndRedirect(t *testing.T) {
	p := planOf(flow(t, "default", `backlog -> new | decide
new -> review on done | decide on question | backlog
review -> approved on done | new on changes max 3 then decide | decide on question
approved -> merged on done | new on failed max 1 then decide | decide on question
decide -> backlog | new
merged terminal
`))
	for _, want := range []loop{
		{from: "review", to: "new", outcomes: []string{"changes, 3 rounds"}},
		{from: "approved", to: "new", outcomes: []string{"failed, 1 round"}},
	} {
		if !slices.ContainsFunc(p.loops, func(l loop) bool {
			return l.from == want.from && l.to == want.to && slices.Equal(l.outcomes, want.outcomes)
		}) {
			t.Errorf("no loop %+v among %+v", want, p.loops)
		}
	}
	// Each redirect is its own spur to decide, beside the question spur,
	// solid: it carries an outcome's label, not a move by hand.
	for _, want := range []spur{
		{siding: "decide", stage: "review", in: true, outcomes: []string{"after 3"}},
		{siding: "decide", stage: "approved", in: true, outcomes: []string{"after 1"}},
	} {
		if !slices.ContainsFunc(p.spurs, func(s spur) bool {
			return s.siding == want.siding && s.stage == want.stage && s.in == want.in && slices.Equal(s.outcomes, want.outcomes)
		}) {
			t.Errorf("no spur %+v among %+v", want, p.spurs)
		}
	}
	n := 0
	for _, s := range p.spurs {
		if s.siding == "decide" && s.stage == "review" && s.in {
			n++
		}
	}
	if n != 2 {
		t.Errorf("%d spurs from review to decide, want question's and the redirect's", n)
	}

	svg := string(SVG(Diagram{Flow: *flow(t, "default", `backlog -> new | decide
new -> review on done | decide on question | backlog
review -> approved on done | new on changes max 3 then decide | decide on question
approved -> merged on done | new on failed max 1 then decide | decide on question
decide -> backlog | new
merged terminal
`)}))
	for _, want := range []string{
		`>changes, 3 rounds</text>`, `>failed, 1 round</text>`,
		`>after 3</text>`, `>after 1</text>`, `>question</text>`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("the drawing lacks %s", want)
		}
	}
	if n := strings.Count(svg, `>after 3</text>`); n != 1 {
		t.Errorf("%d labels read after 3, want review's redirect alone", n)
	}

	// A Then on the line is a loop below the track, even where the heavy
	// track already runs there for the way on.
	q := planOf(flow(t, "way", "a -> b on done\nb -> c on done | a on changes max 2 then c\nc terminal\n"))
	if want := []bool{true, true}; !slices.Equal(q.track, want) {
		t.Errorf("track = %v, want %v", q.track, want)
	}
	for _, want := range []loop{
		{from: "b", to: "a", outcomes: []string{"changes, 2 rounds"}},
		{from: "b", to: "c", outcomes: []string{"after 2"}},
	} {
		if !slices.ContainsFunc(q.loops, func(l loop) bool {
			return l.from == want.from && l.to == want.to && slices.Equal(l.outcomes, want.outcomes)
		}) {
			t.Errorf("no loop %+v among %+v", want, q.loops)
		}
	}

	// A limited way on is drawn explicitly, not carried by the track, which
	// has no label to say the limit.
	r := planOf(flow(t, "carried", "a -> b on retry max 2 then c\nb -> c on done\nc terminal\n"))
	if want := []bool{false, true}; !slices.Equal(r.track, want) {
		t.Errorf("track = %v, want %v", r.track, want)
	}
	for _, want := range []loop{
		{from: "a", to: "b", outcomes: []string{"retry, 2 rounds"}},
		{from: "a", to: "c", outcomes: []string{"after 2"}},
	} {
		if !slices.ContainsFunc(r.loops, func(l loop) bool {
			return l.from == want.from && l.to == want.to && slices.Equal(l.outcomes, want.outcomes)
		}) {
			t.Errorf("no loop %+v among %+v", want, r.loops)
		}
	}

	// The limit stays visible when another outcome shares the way-on stage,
	// whose heavy track would otherwise hide both outcome labels.
	sharedWay := planOf(flow(t, "shared-way", `a -> b on done
b -> c on done | c on retry max 2 then d
c terminal
d terminal
`))
	if want := []bool{true, false, false}; !slices.Equal(sharedWay.track, want) {
		t.Errorf("shared way-on track = %v, want %v", sharedWay.track, want)
	}
	if !slices.ContainsFunc(sharedWay.loops, func(l loop) bool {
		return l.from == "b" && l.to == "c" && slices.Equal(l.outcomes, []string{"done", "retry, 2 rounds"})
	}) {
		t.Errorf("shared way-on limit is not labeled in a loop: %+v", sharedWay.loops)
	}
	sharedSVG := string(SVG(Diagram{Flow: *flow(t, "shared-way", `a -> b on done
b -> c on done | c on retry max 2 then d
c terminal
d terminal
`)}))
	if !strings.Contains(sharedSVG, `>done, retry, 2 rounds</text>`) {
		t.Error("the shared way-on drawing lacks the limited outcome label")
	}
}

// A flow without outcomes has a main path all the same, each stage's first
// transition, and the stages off it are sidings; a move by hand that skips
// ahead does not put a later platform before an earlier one.
func TestPlanWithoutOutcomes(t *testing.T) {
	p := planOf(flow(t, "code", `backlog -> new | merged
new -> review | backlog
review -> approved | new
approved -> merged | merge | new
merge -> review | new | decide
decide -> backlog
merged terminal
`))
	if want := []string{"backlog", "new", "review", "approved", "merged"}; !slices.Equal(p.line, want) {
		t.Errorf("line = %v, want %v", p.line, want)
	}
	if want := []string{"merge", "decide"}; !slices.Equal(p.sidings, want) {
		t.Errorf("sidings = %v, want %v", p.sidings, want)
	}
	if len(p.signals) != 0 {
		t.Errorf("signals = %v: no stage reports changes", p.signals)
	}
	// d is reached from a before c is, and still stands after it.
	p = planOf(flow(t, "skip", "a -> b | d\nb -> c\nc -> d\nd -> e\ne terminal\n"))
	if want := []string{"a", "b", "c", "d", "e"}; !slices.Equal(p.line, want) {
		t.Errorf("line = %v, want %v", p.line, want)
	}
}

// Each kind of group has its building under its platform, a shed a bay per
// bead of its limit, and a siding's group stands beside the siding's name.
func TestBuildings(t *testing.T) {
	d := Diagram{Flow: *flow(t, "default", starterFlow), Groups: map[string]Group{
		"backlog":  {Name: "backlog", Kind: People},
		"new":      {Name: "builders", Kind: Sessions, Limit: 5},
		"review":   {Name: "far-reviewers", Kind: Peer, Yard: "north"},
		"approved": {Name: "assembly", Kind: Script, Limit: 1},
		"decide":   {Name: "decisions", Kind: People},
	}}
	svg := string(SVG(d))
	for _, want := range []string{
		`<g class="group people" data-group="backlog" transform="translate(`,
		`<g class="group sessions" data-group="builders" transform="translate(`,
		`<g class="group peer" data-group="far-reviewers" transform="translate(`,
		`<g class="group script" data-group="assembly" transform="translate(`,
		`>far-reviewers (yard north)</text>`,
		`>builders</text>`,
	} {
		if !strings.Contains(svg, want) {
			t.Errorf("the drawing lacks %s", want)
		}
	}
	// The siding's building is the small one.
	if !strings.Contains(svg, `data-group="decisions" transform="translate(`) || !strings.Contains(svg, `scale(.6)">`) {
		t.Error("the siding's group has no building beside its name")
	}
	shed := func(g Group) string {
		c := &canvas{}
		c.building(g)
		return c.b.String()
	}
	for limit, bays := range map[int]int{0: 1, 1: 1, 5: 5, 6: 6, 12: 6} {
		if n := strings.Count(shed(Group{Kind: Sessions, Limit: limit}), "<line "); n != bays {
			t.Errorf("a shed of limit %d has %d bays, want %d", limit, n, bays)
		}
	}
	// Past the bays that are drawn, the label says the limit.
	if got := (Group{Name: "many", Kind: Sessions, Limit: 12}).label(); got != "many ×12" {
		t.Errorf("label = %q", got)
	}
}

func TestGroupOf(t *testing.T) {
	for runner, want := range map[string]Kind{"manual": People, "exec": Script, "herdr": Sessions, store.PeerRunner: Peer} {
		g := GroupOf(store.Group{Name: "g", Runner: runner, Limit: 3, Config: map[string]any{store.GroupPeer: "north"}})
		if g.Kind != want || g.Limit != 3 || (want == Peer) != (g.Yard == "north") {
			t.Errorf("GroupOf(%s) = %+v", runner, g)
		}
	}
}

// The drawing is XML whatever a stage or group is called, and the same
// diagram is the same bytes.
func TestSVGIsStable(t *testing.T) {
	d := Diagram{Flow: *flow(t, "default", starterFlow), Groups: map[string]Group{"new": {Name: `a<b&"c"`, Kind: Sessions, Limit: 2}}}
	a, b := SVG(d), SVG(d)
	if !bytes.Equal(a, b) {
		t.Error("two drawings of one diagram differ")
	}
	if strings.Contains(string(a), `a<b`) {
		t.Error("a group's name is written unescaped")
	}
	// A page shows the file as an image, which loads no font and takes the
	// colours from the file alone.
	for _, want := range []string{`prefers-color-scheme:dark`, `,monospace;`, `,sans-serif;`, `<svg xmlns="http://www.w3.org/2000/svg"`} {
		if !strings.Contains(string(a), want) {
			t.Errorf("the drawing lacks %s", want)
		}
	}
}

// The drawings the documentation shows are what the generator writes now:
// after a change to the starter pack's flows or to the drawing, run
// `go generate ./internal/flowdraw` and commit docs/flows.
func TestDocsDrawingsAreCurrent(t *testing.T) {
	const dir = "../../docs/flows"
	files, err := Embedded(packs.Starter)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := files["starter.svg"]; !ok {
		t.Fatalf("no starter.svg among %d drawings", len(files))
	}
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%v: run go generate ./internal/flowdraw", err)
		} else if !bytes.Equal(got, want) {
			t.Errorf("%s is not what the generator writes now: run go generate ./internal/flowdraw and commit it", filepath.Join(dir, name))
		}
	}
	// A flow the pack no longer has leaves no drawing behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if _, ok := files[e.Name()]; !ok && strings.HasPrefix(e.Name(), packs.Starter+".") {
			t.Errorf("%s is the drawing of no flow of the starter pack: remove it", filepath.Join(dir, e.Name()))
		}
	}
}
