package flowdraw

// The yard panel: Panel draws the same diagram SVG does — the platforms,
// sidings and return lines of planOf, with the same canvas — and stands the
// open beads on it as blocks: one small rect per bead on its stage's
// platform or siding, the newest first, past ten folded into "+N". A
// platform is lit while a session runs in its stage, and a running block
// carries the short name of the group that works it. Every platform links
// to its stage on /flows and every block to its bead, through the hrefs
// Standing gives.
//
// The drawing has no style of its own: it is inline in the web view, whose
// policy refuses one, so its classes are the stylesheet's
// (internal/web/assets/yard.css, .panel-svg), which names every colour the
// panel uses. What Panel writes is safe to send unescaped: every name from
// the yard in it is escaped here.

import (
	"fmt"
	"html"
	"slices"
	"strings"
)

// Block is one bead standing on the panel.
type Block struct {
	ID string
	// Group is the full name of the group working the bead, "" when none
	// works it: a quiet block carries no group.
	Group string
	// Working is a bead an agent's or a command's session has claimed.
	Working bool
	// Train is the train the bead is a child of, "" when none.
	Train string
}

// Standing is the yard on one diagram: the open beads per stage, the newest
// first, and where a platform and a block link to.
type Standing struct {
	Blocks map[string][]Block
	// Stage is the href of a stage's platform, Bead of a block; "" draws
	// the platform or block with no link.
	Stage func(stage string) string
	Bead  func(id string) string
}

// Blocks on a platform or a siding, in the SVG's own units. Ten stand in a
// row; past ten the rest fold into "+N".
const (
	blockW     = 6
	blockPitch = 8
	blockH     = 12
	maxBlocks  = 10
)

// shortGroup is the short name a running block carries for its group: what
// stands before the first dash (muse-builders works as muse), the whole
// name when there is none.
func shortGroup(g string) string {
	if head, _, ok := strings.Cut(g, "-"); ok {
		return head
	}
	return g
}

// shortID is the short id a train's children carry for their train: what
// stands after the last dash (yardr-e3rg travels as e3rg), the whole id
// when there is none.
func shortID(id string) string {
	if _, tail, ok := strings.Cut(id, "-"); ok {
		if i := strings.LastIndex(tail, "-"); i >= 0 {
			return tail[i+1:]
		}
		return tail
	}
	return id
}

// A row of blocks is blocks with, before each train's children, the train's
// short id, and past ten blocks a fold with the count of the rest.
type item struct {
	block *Block
	train string // the short id of the train whose children follow
	fold  int    // past ten: the rest fold into "+N"
}

// rowOf orders stage's blocks as they stand: the newest first, a train's
// children together with their train's short id once, past ten folded.
func rowOf(blocks []Block) []item {
	var out []item
	seen := map[string]bool{}
	shown := 0
	flush := func(train string) {
		if train != "" && !seen[train] {
			seen[train] = true
			out = append(out, item{train: shortID(train)})
		}
	}
	for i := range blocks {
		b := &blocks[i]
		if seen[b.Train] && b.Train != "" {
			// A child whose train already stands joins it, wherever the
			// newest-first order put it.
			at := 0
			for j, it := range out {
				if it.block != nil && it.block.Train == b.Train {
					at = j + 1
				}
			}
			out = slices.Insert(out, at, item{block: b})
			continue
		}
		flush(b.Train)
		out = append(out, item{block: b})
	}
	// Past ten the rest fold, and a train left with no child standing
	// leaves its short id out too.
	var kept []item
	for _, it := range out {
		switch {
		case it.block != nil && shown < maxBlocks:
			shown++
			kept = append(kept, it)
		case it.block == nil && it.train != "":
			kept = append(kept, it)
		}
	}
	for len(kept) > 0 && kept[len(kept)-1].block == nil {
		kept = kept[:len(kept)-1]
	}
	if rest := len(blocks) - shown; rest > 0 {
		kept = append(kept, item{fold: rest})
	}
	return kept
}

// rowW is the width a row of items stands in.
func rowW(items []item) int {
	w := 0
	for _, it := range items {
		switch {
		case it.block != nil:
			w += blockPitch
		case it.train != "":
			w += int(float64(len([]rune(it.train)))*smallW) + 6
		default:
			w += int(float64(len([]rune(fmt.Sprintf("+%d", it.fold))))*smallW) + 6
		}
	}
	if w > 0 {
		w -= blockPitch - blockW
	}
	return w
}

// Panel draws the diagram with the yard on it: SVG's track, and Standing's
// beads as blocks. The same diagram and standing give the same bytes.
func Panel(d Diagram, s Standing) []byte {
	f := &d.Flow
	p := planOf(f)
	c := &canvas{}

	rows := map[string][]item{}
	widths := map[string]int{}
	for stage, blocks := range s.Blocks {
		rows[stage] = rowOf(blocks)
		widths[stage] = rowW(rows[stage])
	}
	running := map[string]bool{}
	for stage, blocks := range s.Blocks {
		running[stage] = slices.ContainsFunc(blocks, func(b Block) bool { return b.Working })
	}

	pitch := minPitch
	for _, st := range p.line {
		pitch = max(pitch, textW(counted(st, len(s.Blocks[st])))+24)
		pitch = max(pitch, widths[st]+24)
		if g, ok := d.Groups[st]; ok {
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

	ty := 56
	if n := len(p.sidings); n > 0 {
		ty = 84 + (n-1)*sidingStep
	}
	sidingY := func(j int) int { return ty - 62 - j*sidingStep }
	sheds := false
	for _, st := range p.line {
		if _, ok := d.Groups[st]; ok {
			sheds = true
		}
	}
	lane0 := ty + 40
	if sheds {
		lane0 = ty + 92
	}

	// The lines below the track, as in SVG: every transition to one
	// platform shares a trunk, which rises to the track before that
	// platform; a source drops to it after its own. Trunks that would
	// overlap get a lane each, the shortest nearest the track.
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
	// row stands the stage's row of blocks with its left at lx, on the line
	// at ly: a block per bead, a running block with its group's short name
	// under it at wy.
	row := func(stage string, lx, ly, wy int) {
		cursor := lx
		for _, it := range rows[stage] {
			switch {
			case it.train != "":
				cursor += c.text(cursor, ly-2, "small train", it.train) + 6
			case it.fold > 0:
				c.text(cursor, ly-2, "small", fmt.Sprintf("+%d", it.fold))
			case it.block.Working:
				link(beadHref(it.block.ID), func() {
					c.f(`<title>%s</title>`, html.EscapeString(it.block.ID))
					c.f(`<rect class="block running" x="%d" y="%d" width="%d" height="%d"/>`,
						cursor, ly-blockH, blockW, blockH)
				})
				if who := shortGroup(it.block.Group); who != "" {
					c.text(cursor+blockW/2, wy, "small mid who", who)
				}
				cursor += blockPitch
			default:
				link(beadHref(it.block.ID), func() {
					c.f(`<title>%s</title>`, html.EscapeString(it.block.ID))
					c.f(`<rect class="block" x="%d" y="%d" width="%d" height="%d"/>`,
						cursor, ly-blockH, blockW, blockH)
				})
				cursor += blockPitch
			}
		}
	}

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

	// Platforms, each with its row of blocks, its name and count, its
	// signal and its group's building. A platform is lit while a session
	// runs in its stage; tapping it opens the stage on /flows.
	for i, st := range p.line {
		class := "platform"
		if f.IsTerminal(st) {
			class += " terminal"
		}
		if running[st] {
			class += " lit"
		}
		href := stageHref(st)
		link(href, func() {
			c.f(`<g class="%s" data-stage="%s">`, class, html.EscapeString(st))
			c.f(`<rect x="%d" y="%d" width="%d" height="12"/>`, x(i)-platformW/2, ty-34, platformW)
			row(st, x(i)-widths[st]/2, ty-34, ty-25)
			c.text(x(i), ty+16, "mid", counted(st, len(s.Blocks[st])))
			if slices.Contains(p.signals, st) {
				sx := x(i) + platformW/2 + 8
				c.f(`<g class="signal"><line class="ln" stroke-width="2" x1="%d" y1="%d" x2="%d" y2="%d"/><circle class="lamp" cx="%d" cy="%d" r="5"/><circle class="ln" cx="%d" cy="%d" r="4"/></g>`,
					sx, ty, sx, ty-26, sx, ty-28, sx, ty-17)
			}
			if g, ok := d.Groups[st]; ok {
				c.f(`<g class="group %s" data-group="%s" transform="translate(%d %d)">`, g.Kind, html.EscapeString(g.Name), x(i), ty+26)
				c.building(g)
				c.text(0, shedH+13, "mid muted", g.label())
				c.f(`</g>`)
			}
			c.f(`</g>`)
		})
	}

	// Sidings, as in SVG, with the stage's row of blocks on the line and
	// its name and count after them; the line runs to cover the row.
	for j, st := range p.sidings {
		sy := sidingY(j)
		lo, hi := 0, 0
		class := "siding"
		if running[st] {
			class += " lit"
		}
		c.f(`<g class="%s" data-stage="%s">`, class, html.EscapeString(st))
		n := 0
		for _, sp := range p.spurs {
			if sp.siding != st {
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
		end := max(hi, lo+2+widths[st])
		c.f(`<line class="ln branch" stroke-width="2" x1="%d" y1="%d" x2="%d" y2="%d"/>`, lo, sy, end, sy)
		row(st, lo+2, sy, sy+11)
		tx := end + 8
		link(stageHref(st), func() {
			tx += c.text(tx, sy+4, "", counted(st, len(s.Blocks[st]))) + 12
		})
		if g, ok := d.Groups[st]; ok {
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
				c.text(dr.x-9, ly-4, "small end", strings.Join(dr.l.outcomes, ", "))
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
	// No xmlns: the drawing is inline in the page, where the namespace is
	// the HTML parser's to know, and its URL names a host the page's test
	// refuses.
	fmt.Fprintf(&out, `<svg class="panel-svg" viewBox="0 0 %d %d" width="%d" height="%d" role="img" aria-label="Panel diagram of flow %s">`+"\n",
		width, height, width, height, html.EscapeString(f.Name))
	fmt.Fprintf(&out, "<title>Flow %s</title>\n", html.EscapeString(f.Name))
	fmt.Fprintf(&out, `<rect class="board" width="%d" height="%d"/>`+"\n", width, height)
	out.WriteString(c.b.String())
	out.WriteString("</svg>\n")
	return []byte(out.String())
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
