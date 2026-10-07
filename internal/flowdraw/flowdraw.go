// Package flowdraw draws a flow as a signalling diagram, in SVG: the track
// runs left to right past a platform per stage, a stage only a person moves
// a bead out of is a siding above the line, a transition the track does not
// carry is a line below it, and the group that works a stage stands under
// its platform. One function draws it (SVG) for every way in: a depot's
// flow with the groups its routes name, a flow file, a pack.
//
// The drawing is of the flow, not of the yard: no bead is on it.
package flowdraw

import (
	"fmt"
	"html"
	"slices"
	"strings"

	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
)

//go:generate go run ./gen ../../docs/flows

// Kind is what a group is, as far as its drawing goes.
type Kind string

const (
	// Sessions is a group that runs agent sessions: a shed, a bay per bead
	// it works at once.
	Sessions Kind = "sessions"
	// People is a manual group: a station building.
	People Kind = "people"
	// Script is an exec group: a works.
	Script Kind = "script"
	// Peer is a group whose beads another yard works: a line out.
	Peer Kind = "peer"
)

// Group is the group that works a stage.
type Group struct {
	Name  string
	Kind  Kind
	Limit int
	// Yard is the yard that works a Peer group's beads.
	Yard string
}

// Diagram is what is drawn: a flow, and per stage the group that works it.
// A stage without one gets no building.
type Diagram struct {
	Flow   store.Flow
	Groups map[string]Group
}

// GroupOf is a yard's group as it is drawn.
func GroupOf(g store.Group) Group {
	out := Group{Name: g.Name, Kind: Sessions, Limit: g.Limit}
	switch {
	case g.Runner == "manual":
		out.Kind = People
	case g.Runner == "exec":
		out.Kind = Script
	case g.Runner == store.PeerRunner:
		out.Kind, out.Yard = Peer, g.AtPeer()
	}
	return out
}

// FromPack is the pack's flow with the groups the pack puts at its stages.
func FromPack(p *pack.Pack, f pack.Flow) Diagram {
	d := Diagram{Flow: f.StoreFlow(f.Name), Groups: map[string]Group{}}
	for _, s := range f.Stages {
		i := slices.IndexFunc(p.Groups, func(g pack.Group) bool { return g.Name == s.Group })
		if i < 0 {
			continue
		}
		g := Group{Name: s.Group, Kind: Sessions, Limit: p.Groups[i].Limit}
		switch {
		case p.Groups[i].People:
			g.Kind = People
		case p.Groups[i].Run != "":
			g.Kind = Script
		}
		d.Groups[s.Name] = g
	}
	return d
}

// The palette and the faces are the signal box sketch's. The colours are
// custom properties with a second set under prefers-color-scheme, so one
// file reads on a light page and a dark one; the faces fall back to what
// every machine has, since an SVG shown as an image loads no web font.
const style = `.flowdraw{--board:#f5f1e6;--fg:#1d2420;--muted:#5d665f;--line:#2a2f2b;--platform:#cfc7b2;--accent:#b5412a;--mail:#c9962b}
@media (prefers-color-scheme:dark){.flowdraw{--board:#202622;--fg:#e6e1d3;--muted:#9aa39b;--line:#d8d2c0;--platform:#3a423c;--accent:#e0674e;--mail:#e5b74f}}
.board{fill:var(--board)}
text{font:11px "IBM Plex Mono",ui-monospace,Menlo,monospace;fill:var(--fg)}
.title{font:700 22px "Barlow Condensed","Arial Narrow",sans-serif;letter-spacing:.04em}
.muted{fill:var(--muted)}
.small{font-size:9px;fill:var(--muted)}
.mid{text-anchor:middle}
.end{text-anchor:end}
.ln{fill:none;stroke:var(--line)}
.track{stroke-width:2.5}
.idle{stroke-width:1;stroke-dasharray:1 4}
.branch{stroke-width:1.5}
.hand{stroke-dasharray:4 3}
.platform>rect{fill:var(--platform);stroke:var(--line)}
.platform.terminal>rect{fill:none;stroke-dasharray:3 2}
.fill{fill:var(--board);stroke:var(--line)}
.solid{fill:var(--line)}
.lamp{fill:var(--accent)}
.mail{fill:var(--mail)}`

// Sizes, in the SVG's own units. A column is a platform and the gap after
// it; everything is placed from a stage's index, so the same flow draws the
// same picture.
const (
	charW      = 6.6 // the advance of the 11px monospace face
	smallW     = 5.4 // and of the 9px one
	minPitch   = 170
	platformW  = 88
	bayW       = 16
	maxBays    = 6
	shedH      = 30
	lanePitch  = 18
	sidingStep = 24
)

type canvas struct {
	b    strings.Builder
	maxX int
}

func (c *canvas) f(format string, args ...any) { fmt.Fprintf(&c.b, format+"\n", args...) }

// text writes s at x, y and returns the width it takes.
func (c *canvas) text(x, y int, class, s string) int {
	w := int(float64(len([]rune(s))) * charW)
	if strings.Contains(class, "small") {
		w = int(float64(len([]rune(s))) * smallW)
	}
	if !strings.Contains(class, "end") && !strings.Contains(class, "mid") {
		c.maxX = max(c.maxX, x+w)
	}
	if class != "" {
		class = fmt.Sprintf(` class="%s"`, class)
	}
	c.f(`<text x="%d" y="%d"%s>%s</text>`, x, y, class, html.EscapeString(s))
	return w
}

func textW(s string) int { return int(float64(len([]rune(s))) * charW) }

// label is what stands under a group's building.
func (g Group) label() string {
	switch {
	case g.Kind == Peer && g.Yard != "":
		return g.Name + " (yard " + g.Yard + ")"
	case g.Kind == Sessions && g.Limit > maxBays:
		return fmt.Sprintf("%s ×%d", g.Name, g.Limit)
	}
	return g.Name
}

// building draws the group's building, its top middle at the origin of the
// group it is written into, and returns its width.
func (c *canvas) building(g Group) int {
	switch g.Kind {
	case People:
		// A station building: a house with a door and two windows.
		c.f(`<path class="fill" d="M-24 %d V12 L0 0 L24 12 V%d Z"/>`, shedH, shedH)
		c.f(`<rect class="solid" x="-4" y="18" width="8" height="12"/><rect class="ln" x="-17" y="16" width="7" height="7"/><rect class="ln" x="10" y="16" width="7" height="7"/>`)
		return 48
	case Script:
		// A works: a sawtooth roof and a chimney.
		c.f(`<path class="fill" d="M-26 %d V10 L-14 2 V10 L-2 2 V10 L10 2 V10 H26 V%d Z"/>`, shedH, shedH)
		c.f(`<rect class="solid" x="16" y="-4" width="6" height="14"/>`)
		return 52
	case Peer:
		// The line to another yard, with a goods wagon on it.
		c.f(`<line class="ln track hand" x1="-30" y1="20" x2="30" y2="20"/><rect class="mail" x="-7" y="9" width="14" height="10"/>`)
		return 60
	}
	// A shed: a bay per bead the group works at once.
	bays := min(max(g.Limit, 1), maxBays)
	w := 12 + bays*bayW
	c.f(`<rect class="ln hand" x="%d" y="0" width="%d" height="%d"/>`, -w/2, w, shedH)
	for i := range bays {
		x := -w/2 + 6 + i*bayW + bayW/2
		c.f(`<line class="ln" x1="%d" y1="%d" x2="%d" y2="6"/>`, x, shedH, x)
	}
	return w
}

// SVG draws the diagram. The same diagram gives the same bytes.
func SVG(d Diagram) []byte {
	f := &d.Flow
	p := planOf(f)
	c := &canvas{}

	pitch := minPitch
	for _, s := range p.line {
		pitch = max(pitch, textW(s)+24)
		if g, ok := d.Groups[s]; ok {
			pitch = max(pitch, textW(g.label())+20)
		}
	}
	for _, l := range p.loops {
		pitch = max(pitch, int(float64(len(strings.Join(l.outcomes, ", ")))*smallW)+platformW)
	}
	at := func(s string) int { return slices.Index(p.line, s) }
	left := 44
	x := func(i int) int { return left + pitch/2 + i*pitch }
	// gap is the middle of the gap after platform i; before the first for -1.
	gap := func(i int) int { return x(i) + pitch/2 }

	ty := 110
	if n := len(p.sidings); n > 0 {
		ty = 144 + (n-1)*sidingStep
	}
	sidingY := func(j int) int { return ty - 62 - j*sidingStep }
	sheds := false
	for _, s := range p.line {
		if _, ok := d.Groups[s]; ok {
			sheds = true
		}
	}
	lane0 := ty + 40
	if sheds {
		lane0 = ty + 92
	}

	// The lines below the track. Every transition to one platform shares a
	// trunk, which rises to the track before that platform; a source drops
	// to it after its own. Trunks that would overlap get a lane each, the
	// shortest nearest the track.
	type drop struct {
		l    loop
		x    int
		lane int
	}
	type trunk struct {
		to     int
		lo, hi int
		lane   int
		drops  []*drop
	}
	var trunks []*trunk
	drops := map[int][]*drop{} // by source platform
	for _, l := range p.loops {
		to := at(l.to)
		i := slices.IndexFunc(trunks, func(t *trunk) bool { return t.to == to })
		if i < 0 {
			trunks = append(trunks, &trunk{to: to, lo: gap(to-1) + 10, hi: gap(to-1) + 10})
			i = len(trunks) - 1
		}
		t, from := trunks[i], at(l.from)
		dr := &drop{l: l}
		t.drops = append(t.drops, dr)
		drops[from] = append(drops[from], dr)
		// The label stands left of the stage's drops, under its platform.
		t.lo = min(t.lo, x(from)-30)
		t.hi = max(t.hi, gap(from))
	}
	byLen := slices.Clone(trunks)
	slices.SortStableFunc(byLen, func(a, b *trunk) int { return (a.hi - a.lo) - (b.hi - b.lo) })
	lanes := 0
	for i, t := range byLen {
		for t.lane = 0; slices.ContainsFunc(byLen[:i], func(o *trunk) bool {
			return o.lane == t.lane && o.lo < t.hi+8 && t.lo < o.hi+8
		}); t.lane++ {
		}
		lanes = max(lanes, t.lane+1)
		for _, dr := range t.drops {
			dr.lane = t.lane
		}
	}
	// A stage's drops stand side by side before the gap's middle: those
	// that run back first, the shallowest leftmost, then those that run
	// forward, the deepest leftmost, so no drop crosses its own stage's
	// lines on the way down.
	for from, ds := range drops {
		slices.SortStableFunc(ds, func(a, b *drop) int {
			fa, fb := at(a.l.to) > from, at(b.l.to) > from
			switch {
			case fa != fb && fb:
				return -1
			case fa != fb:
				return 1
			case fa:
				return b.lane - a.lane
			}
			return a.lane - b.lane
		})
		for k, dr := range ds {
			dr.x = gap(from) - 6 - 8*(len(ds)-1-k)
		}
	}

	height := ty + 34
	if sheds {
		height = ty + 82
	}
	if lanes > 0 {
		height = lane0 + (lanes-1)*lanePitch + 22
	}
	last := len(p.line) - 1
	c.maxX = gap(last)

	// Title.
	first, end := "", ""
	if len(p.line) > 0 {
		first, end = p.line[0], p.line[last]
	}
	c.text(24, 36, "title", strings.ToUpper("flow "+f.Name))
	c.text(24, 54, "muted", fmt.Sprintf("%s → %s · %d stages", first, end, len(f.Stages)))

	// Track: heavy where it carries a stage's way on.
	if len(p.line) > 0 {
		c.f(`<line class="ln track" x1="%d" y1="%d" x2="%d" y2="%d"/>`, left-20, ty, x(0), ty)
		for i := range last {
			class := "ln idle"
			if p.track[i] {
				class = "ln track"
			}
			c.f(`<line class="%s" x1="%d" y1="%d" x2="%d" y2="%d"/>`, class, x(i), ty, x(i+1), ty)
		}
		// The buffer stop.
		stop := x(last) + platformW/2 + 20
		c.f(`<line class="ln track" x1="%d" y1="%d" x2="%d" y2="%d"/>`, x(last), ty, stop, ty)
		c.f(`<path class="ln track" d="M%d %d V%d M%d %d h8 M%d %d h8"/>`, stop, ty-9, ty+9, stop, ty-9, stop, ty+9)
	}

	// Platforms, each with its name, its signal and its group's building.
	for i, s := range p.line {
		class := "platform"
		if f.IsTerminal(s) {
			class += " terminal"
		}
		c.f(`<g class="%s" data-stage="%s">`, class, html.EscapeString(s))
		c.f(`<rect x="%d" y="%d" width="%d" height="12"/>`, x(i)-platformW/2, ty-34, platformW)
		c.text(x(i), ty+16, "mid", s)
		if slices.Contains(p.signals, s) {
			sx := x(i) + platformW/2 + 8
			c.f(`<g class="signal"><line class="ln" stroke-width="2" x1="%d" y1="%d" x2="%d" y2="%d"/><circle class="lamp" cx="%d" cy="%d" r="5"/><circle class="ln" cx="%d" cy="%d" r="4"/></g>`,
				sx, ty, sx, ty-26, sx, ty-28, sx, ty-17)
		}
		if g, ok := d.Groups[s]; ok {
			c.f(`<g class="group %s" data-group="%s" transform="translate(%d %d)">`, g.Kind, html.EscapeString(g.Name), x(i), ty+26)
			c.building(g)
			c.text(0, shedH+13, "mid muted", g.label())
			c.f(`</g>`)
		}
		c.f(`</g>`)
	}

	// Sidings: a line above the platforms, a spur up from the track after
	// each stage that leads in (with its outcome), a spur down before each
	// stage a person may move a bead out to.
	for j, s := range p.sidings {
		sy := sidingY(j)
		lo, hi := 0, 0
		c.f(`<g class="siding" data-stage="%s">`, html.EscapeString(s))
		n := 0
		for _, sp := range p.spurs {
			if sp.siding != s {
				continue
			}
			i := at(sp.stage)
			class := "ln branch"
			if len(sp.outcomes) == 0 {
				class += " hand"
			}
			var top int
			if sp.in {
				top = gap(i) - 6 - 10*j
				c.f(`<path class="%s" d="M%d %d L%d %d"/>`, class, top-12, ty, top, sy)
				if len(sp.outcomes) > 0 {
					c.text(gap(i)-22-10*j, ty-39-11*j, "small end", strings.Join(sp.outcomes, ", "))
				}
			} else {
				top = gap(i-1) + 6 + 10*j
				c.f(`<path class="%s" d="M%d %d L%d %d"/>`, class, top, sy, top+12, ty)
			}
			if n == 0 || top < lo {
				lo = top
			}
			if n == 0 || top > hi {
				hi = top
			}
			n++
		}
		if n == 0 {
			lo = gap(0)
			hi = lo
		}
		if hi-lo < 70 {
			hi = lo + 70
		}
		c.f(`<line class="ln branch" stroke-width="2" x1="%d" y1="%d" x2="%d" y2="%d"/>`, lo, sy, hi, sy)
		tx := hi + 8
		tx += c.text(tx, sy+4, "", s) + 12
		if g, ok := d.Groups[s]; ok {
			// The same building as under a platform, smaller, beside the name.
			c.f(`<g class="group %s" data-group="%s" transform="translate(%d %d) scale(.6)">`, g.Kind, html.EscapeString(g.Name), tx+18, sy-13)
			w := c.building(g)
			c.f(`</g>`)
			c.text(tx+18+w*3/10+8, sy+4, "muted", g.label())
		}
		c.f(`</g>`)
	}

	// The lines below the track.
	for _, t := range trunks {
		ly := lane0 + t.lane*lanePitch
		ax := gap(t.to-1) + 10
		lo, hi := ax, ax
		c.f(`<g class="loops" data-to="%s">`, html.EscapeString(p.line[t.to]))
		for _, dr := range t.drops {
			class := "ln branch"
			if len(dr.l.outcomes) == 0 {
				class += " hand"
			}
			turn := -6 // towards the platform it leads to
			if dr.x < ax {
				turn = 6
			}
			c.f(`<path class="%s" data-from="%s" d="M%d %d V%d L%d %d"/>`, class, html.EscapeString(dr.l.from), dr.x, ty, ly-6, dr.x+turn, ly)
			lo, hi = min(lo, dr.x), max(hi, dr.x)
			if len(dr.l.outcomes) > 0 {
				ds := drops[at(dr.l.from)]
				c.text(ds[0].x-9, ly-4, "small end", strings.Join(dr.l.outcomes, ", "))
			}
		}
		// The trunk, and its way up to the track, with an arrowhead.
		c.f(`<line class="ln branch" x1="%d" y1="%d" x2="%d" y2="%d"/>`, lo+6, ly, hi-6, ly)
		up := ""
		if lo < ax {
			up += fmt.Sprintf("M%d %d L%d %d ", ax-6, ly, ax, ly-6)
		}
		if hi > ax {
			up += fmt.Sprintf("M%d %d L%d %d ", ax+6, ly, ax, ly-6)
		}
		c.f(`<path class="ln branch" d="%sV%d"/>`, up, ty+9)
		c.f(`<path class="solid" d="M%d %d l-4 8 h8 Z"/>`, ax, ty+2)
		c.f(`</g>`)
	}

	width := c.maxX + 24
	var out strings.Builder
	fmt.Fprintf(&out, `<svg xmlns="http://www.w3.org/2000/svg" class="flowdraw" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Signalling diagram of flow %s">`+"\n",
		width, height, width, height, html.EscapeString(f.Name))
	fmt.Fprintf(&out, "<title>Flow %s</title>\n<style>\n%s\n</style>\n", html.EscapeString(f.Name), style)
	fmt.Fprintf(&out, `<rect class="board" width="%d" height="%d"/>`+"\n", width, height)
	out.WriteString(c.b.String())
	out.WriteString("</svg>\n")
	return []byte(out.String())
}
