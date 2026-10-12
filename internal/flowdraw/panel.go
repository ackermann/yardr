package flowdraw

// The yard panel: Mini draws the flow as a line plan, the mimic panel's
// own grammar — one thin track line, each stage a section of it with its
// name and count under it, a manual stage as a short siding above, the
// other edges as thin unlabelled lines — and stands the open beads on it
// as lights: one small lamp per bead on its stage's section, the newest
// first, past ten folded into "+N". A section is lit while a session runs
// in it. Every section and siding links to its stage on /flows and every
// light to its bead, through the hrefs Standing gives. The blocked kinds
// are told apart at a glance, as on the full drawing: held is solid red,
// gave up hollow red, stalled hollow green, blocked on a prompt green
// with a red ring, waiting on a dependency blue, back on failed or
// changes with a red diamond before it; a stage that waits for a person
// carries a lamp, red for a fault and blue for a person's turn, and a
// stage no route serves is dotted with the faint words "no route". No
// bead writes anything on the plan: what a light is stands in its title,
// and a tap opens the bead.
//
// The drawing has no style of its own: it is inline in the web view, whose
// policy refuses one, so its classes are the stylesheet's
// (internal/web/assets/yard.css, .panel-svg), which names every colour the
// panel uses. What Mini writes is safe to send unescaped: every name from
// the yard in it is escaped here.

import (
	"fmt"
	"html"
	"slices"
	"strings"
)

// Block is one bead standing on the panel, as a light.
type Block struct {
	ID string
	// Working is a bead an agent's or a command's session has claimed.
	Working bool
	// Held is a bead on hold: solid red. GaveUp is a hold after repeated
	// failures: hollow red. Blocked is a running session that waits on a
	// prompt: green with a red ring. Stalled is a running session whose
	// last word is session_stalled: hollow green.
	Held    bool
	GaveUp  bool
	Blocked bool
	Stalled bool
	// Dep is the full id of the first open blocker holding the bead, ""
	// when none waits: the light is blue when the bead is otherwise
	// quiet, and its title names the bead holding it.
	Dep string
	// FailedBack is a bead whose last move carries outcome failed or
	// changes: a red diamond stands before its light.
	FailedBack bool
}

// Lamp is a stage's wait lamp: red where the wait is a fault, blue where it
// is a person's turn by design, "" where nothing waits.
type Lamp string

const (
	// LampFault is a fault that waits in the stage: held, blocked, gave up.
	LampFault Lamp = "fault"
	// LampTurn is a person's turn by design: a manual stage with beads.
	LampTurn Lamp = "turn"
)

// Standing is the yard on one diagram: the open beads per stage, the newest
// first, and where a section and a light link to.
type Standing struct {
	Blocks map[string][]Block
	// Lamps are the stages that wait for a person, red for a fault and blue
	// for a person's turn; absent where nothing waits.
	Lamps map[string]Lamp
	// Noroute are the stages no route serves: dotted sections with the
	// faint words "no route".
	Noroute map[string]bool
	// Stage is the href of a stage's section, Bead of a light; "" draws
	// the section or light with no link.
	Stage func(stage string) string
	Bead  func(id string) string
}

// Lights on a section or a siding, in the SVG's own units. Ten stand in a
// row; past ten the rest fold into "+N". A failed-back diamond stands
// before its light.
const (
	beadR        = 4
	beadPitch    = 11
	maxBeads     = 10
	diamondW     = 6
	diamondPitch = 9
	lampR        = 3
	miniPitch    = 64
	segGap       = 12
	miniLane     = 10
)

// class is the light's class: quiet grey, green while a session runs,
// solid red for held, hollow red for gave up, hollow green for stalled,
// green with a red ring for blocked on a prompt, blue for waiting on a
// dependency when otherwise quiet. A working or held bead that also waits
// on a dependency keeps its colour and still names the bead holding it in
// its title.
func (b Block) class() string {
	switch {
	case b.GaveUp:
		return "block gaveup"
	case b.Held:
		return "block held"
	case b.Blocked:
		return "block blocked"
	case b.Stalled:
		return "block stalled"
	case b.Working:
		return "block running"
	case b.Dep != "":
		return "block dep"
	}
	return "block"
}

// titleOf is what a light's title says: its bead, and what holds it when
// something does.
func titleOf(b Block) string {
	if b.Dep != "" {
		return b.ID + " waits on " + b.Dep
	}
	return b.ID
}

// beadsW is the width a stage's lights stand in: ten beads at most, a
// diamond before a bead back on failed or changes, "+N" past ten.
func beadsW(blocks []Block) int {
	w := 0
	for _, b := range blocks[:min(len(blocks), maxBeads)] {
		if b.FailedBack {
			w += diamondPitch
		}
		w += beadPitch
	}
	if rest := len(blocks) - min(len(blocks), maxBeads); rest > 0 {
		w += int(float64(len([]rune(fmt.Sprintf("+%d", rest))))*charW) + 3
	}
	return w
}

// Mini draws the flow as a line plan with the yard on it: the track thin,
// each stage a section with its name and count under it, the beads as
// lights. The same diagram and standing give the same bytes.
func Mini(d Diagram, s Standing) []byte {
	body, width, height := mini(d, s)
	var out strings.Builder
	// No xmlns: the drawing is inline in the page, where the namespace is
	// the HTML parser's to know, and its URL names a host the page's test
	// refuses.
	fmt.Fprintf(&out, `<svg class="panel-svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Panel diagram of flow %s">`+"\n",
		width, height, width, height, html.EscapeString(d.Flow.Name))
	fmt.Fprintf(&out, "<title>Flow %s</title>\n", html.EscapeString(d.Flow.Name))
	fmt.Fprintf(&out, `<rect class="board" width="%d" height="%d"/>`+"\n", width, height)
	out.WriteString(body)
	out.WriteString("</svg>\n")
	return []byte(out.String())
}

// MiniSVG draws the flow's line plan with no yard on it: what Mini draws
// bare, as a file of its own for the command line.
func MiniSVG(d Diagram) []byte {
	body, width, height := mini(d, Standing{})
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" class="flowdraw" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Line plan of flow %s">`+"\n",
		width, height, width, height, html.EscapeString(d.Flow.Name))
	fmt.Fprintf(&out, "<title>Flow %s</title>\n<style>\n%s\n</style>\n", html.EscapeString(d.Flow.Name), style)
	fmt.Fprintf(&out, `<rect class="board" width="%d" height="%d"/>`+"\n", width, height)
	out.WriteString(body)
	out.WriteString("</svg>\n")
	return []byte(out.String())
}

// mini is the line plan itself, for the panel and the command line alike:
// the body's elements with the width and height they stand in.
func mini(d Diagram, s Standing) (string, int, int) {
	f := &d.Flow
	p := planOf(f)
	c := &canvas{}

	widths := map[string]int{}
	for stage, blocks := range s.Blocks {
		widths[stage] = beadsW(blocks)
	}
	running := map[string]bool{}
	for stage, blocks := range s.Blocks {
		running[stage] = slices.ContainsFunc(blocks, func(b Block) bool { return b.Working })
	}

	pitch := miniPitch
	for _, st := range p.line {
		w := textW(counted(st, len(s.Blocks[st])))
		if s.Noroute[st] {
			w += int(float64(len([]rune("no route")))*smallW) + 8
		}
		pitch = max(pitch, w+20)
		pitch = max(pitch, widths[st]+28)
	}
	at := func(stage string) int { return slices.Index(p.line, stage) }
	left := 8
	x := func(i int) int { return left + pitch/2 + i*pitch }
	segW := pitch - segGap
	// A stage's lines leave the track on one side of the middle of the
	// gap after its section and come back on the other side of the middle
	// of the gap before it, so no drop shares its line with a rise.
	dropX := func(i int) int { return x(i) + pitch/2 - 3 }
	riseX := func(i int) int { return x(i) - pitch/2 + 3 }

	ty := 18
	if n := len(p.sidings); n > 0 {
		ty = 49 + (n-1)*20
	}
	sidingY := func(j int) int { return ty - 30 - j*20 }
	lane0 := ty + 26

	// The lines below the track: every transition to one section shares a
	// trunk, which rises to the track before that section; a source drops
	// to it after its own. Trunks that would overlap get a lane each, the
	// shortest nearest the track. With no outcome to say, drops from one
	// source share one line.
	type drop struct {
		x    int
		hand bool
	}
	type trunk struct {
		to     int
		lo, hi int
		drops  []drop
	}
	var trunks []*trunk
	for _, l := range p.loops {
		to := at(l.to)
		i := slices.IndexFunc(trunks, func(t *trunk) bool { return t.to == to })
		if i < 0 {
			trunks = append(trunks, &trunk{to: to})
			i = len(trunks) - 1
		}
		t := trunks[i]
		if dr := (drop{dropX(at(l.from)), len(l.outcomes) == 0}); !slices.Contains(t.drops, dr) {
			t.drops = append(t.drops, dr)
		}
	}
	for _, t := range trunks {
		t.lo, t.hi = riseX(t.to), riseX(t.to)
		for _, dr := range t.drops {
			t.lo = min(t.lo, dr.x)
			t.hi = max(t.hi, dr.x)
		}
	}
	byLen := slices.Clone(trunks)
	slices.SortStableFunc(byLen, func(a, b *trunk) int { return (a.hi - a.lo) - (b.hi - b.lo) })
	laneOf := map[*trunk]int{}
	lanes := 0
	for i, t := range byLen {
		lane := 0
		for ; slices.ContainsFunc(byLen[:i], func(o *trunk) bool {
			return laneOf[o] == lane && o.lo < t.hi+4 && t.lo < o.hi+4
		}); lane++ {
		}
		laneOf[t] = lane
		lanes = max(lanes, lane+1)
	}

	height := ty + 22
	if lanes > 0 {
		height = lane0 + (lanes-1)*miniLane + 10
	}

	stageHref := func(stage string) string {
		if s.Stage == nil {
			return ""
		}
		return s.Stage(stage)
	}
	beadHref := func(id string) string {
		if s.Bead == nil {
			return ""
		}
		return s.Bead(id)
	}
	// link wraps what draw writes in the href's link, or writes it bare
	// when there is none.
	link := func(href string, draw func()) {
		if href == "" {
			draw()
			return
		}
		c.f(`<a href="%s">`, html.EscapeString(href))
		draw()
		c.f(`</a>`)
	}
	// lights stands the stage's beads on the line at ly, centred on cx: a
	// lamp per bead, a diamond before one back on failed or changes, "+N"
	// past ten. A lamp's title is its bead, naming what holds it when
	// something does; tapping it opens the bead.
	lights := func(stage string, cx, ly int) {
		blocks := s.Blocks[stage]
		cursor := cx - beadsW(blocks)/2
		for _, b := range blocks[:min(len(blocks), maxBeads)] {
			lx := cursor
			if b.FailedBack {
				lx += diamondPitch
			}
			link(beadHref(b.ID), func() {
				c.f(`<title>%s</title>`, html.EscapeString(titleOf(b)))
				if b.FailedBack {
					dx := cursor + diamondW/2
					c.f(`<path class="diamond" d="M%d %d l%d %d l%d %d l%d %d Z"/>`,
						dx, ly-4, 3, 4, -3, 4, -3, -4)
				}
				c.f(`<circle class="%s" cx="%d" cy="%d" r="%d"/>`,
					b.class(), lx+beadR, ly, beadR)
			})
			cursor = lx + beadPitch
		}
		if rest := len(blocks) - min(len(blocks), maxBeads); rest > 0 {
			c.text(cursor, ly+4, "", fmt.Sprintf("+%d", rest))
		}
	}
	// signal draws the stage's lamp at lx: a lamp on a short post for a
	// stage that can report changes, a bare lamp where a wait sits on a
	// stage with none. The lamp is an unlit outline unless the stage
	// waits: red for a fault, blue for a person's turn.
	signal := func(stage, href string, lx, ly int) {
		l, waits := s.Lamps[stage]
		post := slices.Contains(p.signals, stage)
		if !waits || l == "" {
			if !post {
				return
			}
			l = "unlit"
		}
		link(href, func() {
			if post {
				c.f(`<line class="ln" x1="%d" y1="%d" x2="%d" y2="%d"/>`, lx, ly+lampR, lx, ly+12)
			}
			c.f(`<circle class="lamp %s" cx="%d" cy="%d" r="%d"/>`, string(l), lx, ly, lampR)
		})
	}

	// Sections: a thin segment per stage with its name and count under
	// it, dashed for a terminal stage, dotted where no route serves, lit
	// green while a session runs. The name sits low enough that a full
	// row of lights and the fold clear it. Tapping the segment or the
	// name opens the stage on /flows.
	for i, st := range p.line {
		href := stageHref(st)
		c.f(`<g class="section" data-stage="%s">`, html.EscapeString(st))
		class := "ln branch"
		switch {
		case s.Noroute[st]:
			class += " idle"
		case f.IsTerminal(st):
			class += " hand"
		}
		if running[st] {
			class += " lit"
		}
		x0 := x(i) - segW/2
		link(href, func() {
			c.f(`<line class="%s" x1="%d" y1="%d" x2="%d" y2="%d"/>`, class, x0, ty, x0+segW, ty)
		})
		lights(st, x(i), ty)
		// Bead links must remain independent of the section link. Nested
		// SVG anchors make the stage win when someone taps a light on
		// some browsers.
		link(href, func() {
			w := c.text(x(i), ty+17, "mid", counted(st, len(s.Blocks[st])))
			if s.Noroute[st] {
				c.text(x(i)+w/2+6, ty+17, "small noroute", "no route")
			}
		})
		signal(st, href, x0+5, ty-11)
		c.f(`</g>`)
	}

	// Sidings: a short stub above the line with the stage's lights on it
	// and its name and count after them; the line runs to cover the row.
	// Spurs join the stub straight to the track, with no outcome to say.
	for j, st := range p.sidings {
		sy := sidingY(j)
		lo, hi := 0, 0
		n := 0
		for _, sp := range p.spurs {
			if sp.siding != st {
				continue
			}
			i := at(sp.stage)
			class := "ln"
			if len(sp.outcomes) == 0 {
				class += " hand"
			}
			var sx int
			if sp.in {
				sx = dropX(i)
				c.f(`<path class="%s" d="M%d %d V%d"/>`, class, sx, ty, sy)
			} else {
				sx = riseX(i)
				c.f(`<path class="%s" d="M%d %d V%d"/>`, class, sx, sy, ty)
			}
			if n == 0 || sx < lo {
				lo = sx
			}
			if n == 0 || sx > hi {
				hi = sx
			}
			n++
		}
		if n == 0 {
			lo, hi = left, left
		}
		end := max(hi, lo+40, lo+widths[st]+28)
		class := "ln branch"
		if s.Noroute[st] {
			class += " idle"
		}
		if running[st] {
			class += " lit"
		}
		href := stageHref(st)
		c.f(`<g class="siding" data-stage="%s">`, html.EscapeString(st))
		link(href, func() {
			c.f(`<line class="%s" x1="%d" y1="%d" x2="%d" y2="%d"/>`, class, lo, sy, end, sy)
		})
		lights(st, (lo+end)/2, sy)
		tx := end + 8
		link(href, func() {
			tx += c.text(tx, sy+4, "", counted(st, len(s.Blocks[st]))) + 8
		})
		if s.Noroute[st] {
			tx += c.text(tx, sy+4, "small noroute", "no route") + 8
		}
		signal(st, href, lo+5, sy-11)
		c.f(`</g>`)
	}

	// The lines below the track.
	for _, t := range trunks {
		ly := lane0 + laneOf[t]*miniLane
		c.f(`<g class="loops" data-to="%s">`, html.EscapeString(p.line[t.to]))
		for _, dr := range t.drops {
			class := "ln"
			if dr.hand {
				class += " hand"
			}
			c.f(`<path class="%s" d="M%d %d V%d"/>`, class, dr.x, ty+1, ly)
		}
		c.f(`<line class="ln" x1="%d" y1="%d" x2="%d" y2="%d"/>`, t.lo, ly, t.hi, ly)
		rx := riseX(t.to)
		c.f(`<path class="ln" d="M%d %d V%d"/>`, rx, ly, ty+1)
		c.f(`<path class="solid" d="M%d %d l-3 5 h6 Z"/>`, rx, ty+2)
		c.f(`</g>`)
	}

	if len(p.line) > 0 {
		last := len(p.line) - 1
		c.maxX = max(c.maxX, x(last)+segW/2)
	}
	return c.b.String(), c.maxX + 12, height
}

// counted is the stage's name with its count of open beads beside it: the
// true number, whatever the row folds. A stage with none carries no count,
// so a flow with no bead draws its bare track.
func counted(stage string, n int) string {
	if n == 0 {
		return stage
	}
	return fmt.Sprintf("%s · %d", stage, n)
}
