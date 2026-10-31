package web_test

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// At 1100 px and wider every page fills the window: home and the line
// keep the aside, and every other page counts what waits in its header
// instead. The markup carries which pages keep the aside (the yard
// panel's wide shell TestYardPageIsWide covers), the stylesheet the
// fluid centre, the readable measure and the unchanged narrower windows;
// what the pixels do is the bead's screenshots'.
func TestPagesFillTheWindow(t *testing.T) {
	t.Parallel()
	// Peers are the operator's to read, so the walk is the operator's.
	f, _ := talkFixture(t, "ada")
	must(t, f.st.SetSetting(context.Background(), store.OperatorSetting, "ada", "ada"))
	run, _, _, _, _ := stocked(t, f)
	for _, path := range []string{"/", "/line"} {
		r := f.get(path)
		if r.status != 200 {
			t.Errorf("%s: status %d", path, r.status)
			continue
		}
		if !strings.Contains(r.body, `<div class="shell"`) {
			t.Errorf("%s: not in the shell", path)
		}
		if strings.Contains(r.body, "shell wide") {
			t.Errorf("%s: wide, so it keeps no aside", path)
		}
		if !strings.Contains(r.body, "<aside") {
			t.Errorf("%s: keeps no aside", path)
		}
	}
	for _, path := range []string{"/beads", "/sessions", "/crew", "/flows", "/beads/" + run.ID,
		"/config", "/depots", "/groups", "/routes", "/peers", "/deps", "/limits"} {
		r := f.get(path)
		if r.status != 200 {
			t.Errorf("%s: status %d", path, r.status)
			continue
		}
		if !strings.Contains(r.body, `<div class="shell"`) {
			t.Errorf("%s: not in the shell", path)
		}
		if strings.Contains(r.body, "shell wide") {
			t.Errorf("%s: wide", path)
		}
		if strings.Contains(r.body, "<aside") {
			t.Errorf("%s: keeps the aside home and the line alone keep", path)
		}
	}
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	for _, want := range []string{
		// The desktop shell: a fluid centre between the rail, the column
		// and the aside.
		"grid-template-columns: var(--rail-width) var(--column-width) minmax(0, 1fr) var(--right-width);",
		// A page without the aside fills the window too.
		".shell:not(:has(aside.right)) { grid-template-columns: var(--rail-width) var(--column-width) minmax(0, 1fr); }",
		// An error page stands under the rail with no column.
		".shell:not(:has(nav.rail-column)) { grid-template-columns: var(--rail-width) minmax(0, 1fr); }",
		// Running text keeps a measure of about 90 characters.
		".text, .entry-note { max-width: 90ch; }",
		// Below 1100 px the centre keeps its width, centred.
		".shell { grid-template-columns: var(--rail-width) var(--column-width) minmax(0, var(--centre-width)); justify-content: center; }",
		// A list's rows keep their one line.
		".entry-line .entry-what { display: flex; align-items: baseline; gap: 6px; margin-top: 0; overflow: hidden; white-space: nowrap; }",
		".needs li > a { display: flex; align-items: baseline; gap: 6px; width: 100%; min-width: 0; box-sizing: border-box; overflow: hidden; white-space: nowrap; font-family: var(--sans); }",
	} {
		if !strings.Contains(string(css), want) {
			t.Errorf("the stylesheet lacks %s", want)
		}
	}
	// --centre-width caps the narrow centre only: the desktop shell no
	// longer names it.
	if n := strings.Count(string(css), "var(--centre-width)"); n != 2 {
		t.Errorf("var(--centre-width) is used %d times, want twice (with and without the column, below 1100 px)", n)
	}
}

// Home's weight: the answer is the only large line, human words (the
// answer, reasons, titles) render in the proportional face and machine
// words (ids, counts, times, stages) stay monospace, and every row runs
// one line with an ellipsis. What the pixels do is the bead's screenshot.
func TestHomeWeight(t *testing.T) {
	t.Parallel()
	answer := cssRule(t, ".answer")
	for _, want := range []string{"font-family: var(--sans);", "font-size: var(--large);"} {
		if !strings.Contains(answer, want) {
			t.Errorf(".answer lacks %s: %s", want, answer)
		}
	}
	needs := cssRule(t, ".needs li > a")
	if !strings.Contains(needs, "font-family: var(--sans);") {
		t.Errorf("a needs row's words are not sans: %s", needs)
	}
	machine := cssRule(t, ".needs .label, .needs .faint")
	if !strings.Contains(machine, "font-family: var(--mono);") {
		t.Errorf("a needs row's id and age are not monospace: %s", machine)
	}
	stage := cssRule(t, ".needs .stage")
	if !strings.Contains(stage, "font-family: var(--mono);") {
		t.Errorf("a parked row's stage is not monospace: %s", stage)
	}
	rows := cssRule(t, ".lines li")
	for _, want := range []string{"overflow: hidden;", "text-overflow: ellipsis;", "white-space: nowrap;"} {
		if !strings.Contains(rows, want) {
			t.Errorf(".lines li lacks %s: %s", want, rows)
		}
	}
	line := cssRule(t, ".entry-line .entry-title")
	for _, want := range []string{"font-family: var(--sans);", "text-overflow: ellipsis;", "flex: 1 1 0;", "min-width: 10ch;"} {
		if !strings.Contains(line, want) {
			t.Errorf(".entry-line .entry-title lacks %s: %s", want, line)
		}
	}
	// A crowded row shrinks the title first, but never past its floor:
	// without one the title vanishes instead of ending in an ellipsis,
	// and past it the what shrinks with it.
	needsTitle := cssRule(t, ".needs .entry-title")
	for _, want := range []string{"flex: 1 1 0;", "min-width: 10ch;"} {
		if !strings.Contains(needsTitle, want) {
			t.Errorf(".needs .entry-title lacks %s: %s", want, needsTitle)
		}
	}
	title := cssRule(t, ".wait .wait-title")
	if !strings.Contains(title, "font-family: var(--sans);") {
		t.Errorf("a wait's title is not sans: %s", title)
	}
	why := cssRule(t, ".wait .why")
	if !strings.Contains(why, "font-family: var(--sans);") {
		t.Errorf("a wait's reason is not sans: %s", why)
	}
	// No new face: every font-family names a token.
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	for _, m := range regexp.MustCompile(`(?m)font-family:\s*([^;]+);`).FindAllStringSubmatch(string(css), -1) {
		if m[1] != "var(--sans)" && m[1] != "var(--mono)" {
			t.Errorf("a face outside the tokens: font-family: %s", m[1])
		}
	}
}

// mediaCSS is the bodies of the stylesheet's @media blocks for query,
// joined: what the window gets at that width.
func mediaCSS(t *testing.T, css, query string) string {
	t.Helper()
	var out strings.Builder
	rest := css
	for {
		_, after, ok := strings.Cut(rest, "@media ("+query+") {")
		if !ok {
			break
		}
		depth := 1
		i := 0
		for ; i < len(after); i++ {
			switch after[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			t.Fatalf("the %s block never closes", query)
		}
		out.WriteString(after[:i])
		rest = after[i+1:]
	}
	if out.Len() == 0 {
		t.Fatalf("the stylesheet has no %s block", query)
	}
	return out.String()
}

// /routes shows one row per route on a wide window and the stacked cards
// below 700 px, and a group's config keys stand bare under their heading.
// What the pixels do is the bead's screenshot.
func TestRoutesCompactRows(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	text := string(css)
	// Below 700 px the table goes and the stacked cards stand; above, the
	// cards go and the table stands.
	phone := mediaCSS(t, text, "max-width: 699px")
	for _, want := range []string{
		".routes-table { display: none; }",
		".routes-cards { display: block; }",
	} {
		if !strings.Contains(phone, want) {
			t.Errorf("the phone rules lack %s", want)
		}
	}
	if cards := cssRule(t, ".routes-cards"); !strings.Contains(cards, "display: none;") {
		t.Errorf("the stacked cards stand on a wide window too: %s", cards)
	}
	// The table reads as the command's: quiet small-caps headers, one
	// ruled row per route, long words wrapping inside their cell.
	head := cssRule(t, ".routes thead th")
	for _, want := range []string{"color: var(--faint);", "font-size: var(--small);", "letter-spacing: var(--label-spacing);", "text-transform: uppercase;"} {
		if !strings.Contains(head, want) {
			t.Errorf(".routes thead th lacks %s: %s", want, head)
		}
	}
	row := cssRule(t, ".routes tbody tr")
	if !strings.Contains(row, "border-top: 1px solid var(--hairline);") {
		t.Errorf(".routes tbody tr lacks its rule: %s", row)
	}
	cells := cssRule(t, ".routes th, .routes td")
	for _, want := range []string{"text-align: left;", "font: inherit;", "overflow-wrap: anywhere;"} {
		if !strings.Contains(cells, want) {
			t.Errorf(".routes th, .routes td lacks %s: %s", want, cells)
		}
	}
	// The stacked card's id sits quiet beside its heading until opened.
	id := cssRule(t, ".route-id")
	for _, want := range []string{"color: var(--faint);", "font-size: var(--small);"} {
		if !strings.Contains(id, want) {
			t.Errorf(".route-id lacks %s: %s", want, id)
		}
	}
	// The config keys stand under their heading, clear of the rows above.
	sub := cssRule(t, ".item > .facts + h3")
	if !strings.Contains(sub, "margin-top: 12px;") {
		t.Errorf(".item > .facts + h3 lacks its margin: %s", sub)
	}
}

// On a phone the rail is a bottom bar of its four tiles and More: five
// targets of about 78 px at 390 px, none cut, each 48 high. More opens
// the open mode's column above the bar. The desktop rail shows the chip
// and tiles with the column beside them, as before. What the pixels do
// is the bead's screenshots.
func TestPhoneBarMore(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	body := f.get("/").body
	// The bar's tiles in the rail's order, Decide open on home.
	for _, want := range []string{
		`<a class="tile" href="/talk" title="Crew" aria-label="Crew">`,
		`<a class="tile open" href="/line" title="Decide" aria-label="Decide" aria-current="true">`,
		`<a class="tile" href="/flows?panel=1" title="Lines" aria-label="Lines">`,
		`<a class="tile" href="/config" title="Settings" aria-label="Settings">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the bar lacks %s", want)
		}
	}
	// More stands last, after Settings, and holds the open mode's
	// column, with the current page marked in both copies.
	more := strings.Index(body, `<div class="more">`)
	if more < 0 {
		t.Fatal("the bar has no More entry")
	}
	if settings := strings.Index(body, `href="/config" title="Settings"`); settings < 0 || settings > more {
		t.Error("More does not stand after Settings")
	}
	want := `<li><a href="/" aria-current="page">Home</a></li>` +
		`<li><a href="/line">Line</a></li>` +
		`<li><a href="/previews">Previews</a></li>`
	if !strings.Contains(body[more:], want) {
		t.Errorf("More does not hold Decide's column: %s", body[more:])
	}
	if n := strings.Count(body, `<a href="/" aria-current="page">`); n != 2 {
		t.Errorf("/ marks %d copies of itself, want 2 (the column's and More's)", n)
	}
	// On another mode's page More holds that mode's column instead.
	beads := f.get("/beads").body
	more = strings.Index(beads, `<div class="more">`)
	if more < 0 {
		t.Fatal("/beads has no More entry")
	}
	want = `<li><a href="/flows?panel=1">Yard</a></li>` +
		`<li><a href="/flows">Flows</a></li>` +
		`<li><a href="/beads" aria-current="page">Beads</a></li>`
	if !strings.Contains(beads[more:], want) {
		t.Errorf("More does not hold Lines' column: %s", beads[more:])
	}
	// More itself is never current: the open tile stands marked.
	if strings.Contains(body, `<summary aria-current`) || strings.Contains(beads, `<summary aria-current`) {
		t.Error("More stands marked current")
	}
	if !strings.Contains(body, `<summary>More</summary>`) {
		t.Error("More's summary is not plain")
	}
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	text := string(css)
	// The desktop rail shows the column beside it: More shows below
	// 700 px only.
	if more := cssRule(t, ".rail .more"); !strings.Contains(more, "display: none;") {
		t.Errorf(".rail .more stands on a wide window too: %s", more)
	}
	phone := mediaCSS(t, text, "max-width: 699px")
	for _, want := range []string{
		".rail-column { display: none; }",
		".rail .chip { display: none; }",
		".rail .tiles, .rail .foot { display: contents; }",
		".rail .tile { flex: 1 1 0; min-width: 0; min-height: 48px; justify-content: center; border-radius: 0; }",
		".rail .more { display: block; flex: 1 1 0; min-width: 0; }",
		".rail .more summary::-webkit-details-marker { display: none; }",
		`.rail .more summary::marker { content: ""; }`,
	} {
		if !strings.Contains(phone, want) {
			t.Errorf("the phone rules lack %s", want)
		}
	}
	// More's summary keeps the bar's 48-pixel target, five to the bar.
	summary := phone[strings.Index(phone, ".rail .more summary {"):]
	summary = summary[:strings.Index(summary, "}")]
	if !strings.Contains(summary, "min-height: 48px;") {
		t.Errorf("More's summary is not 48 high: %s", summary)
	}
	// More opens upward, a full-width list above the bar, each row tall
	// enough to tap; the bar keeps its overflow for the popup.
	for _, want := range []string{"position: absolute;", "bottom: 100%;", "left: 0;", "right: 0;", "display: block;"} {
		if !strings.Contains(phone, want) {
			t.Errorf("More's list lacks %s", want)
		}
	}
	if !strings.Contains(phone, "overflow: visible;") {
		t.Error("the phone bar clips More's list")
	}
	if !strings.Contains(phone, "min-height: 48px;") {
		t.Error("the bar's targets are not 48 high")
	}
}

// On a phone a feed row takes two lines: the verb in full and the bead on
// the first, the time at its right, the title below ending in an
// ellipsis. At 700 px and wider the row is one line, as today. What the
// pixels do is the bead's screenshots.
func TestFeedRowOnPhone(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	text := string(css)
	phone := mediaCSS(t, text, "max-width: 699px")
	for _, want := range []string{
		".entry-line .entry-what { flex-wrap: wrap; gap: 3px; font-size: var(--small); }",
		".entry-line .entry-what > :first-child { flex: 1 1 0; max-width: max-content; letter-spacing: -0.06em; }",
		".entry-line .separator { display: none; }",
		".entry-line .label { order: 1; margin-left: auto; }",
		".entry-line .entry-title { order: 2; flex: 1 1 100%; font-size: var(--text); }",
	} {
		if !strings.Contains(phone, want) {
			t.Errorf("the phone rules lack %s", want)
		}
	}
	// The title still ends in an ellipsis, on its own line now.
	title := cssRule(t, ".entry-line .entry-title")
	for _, want := range []string{"overflow: hidden;", "text-overflow: ellipsis;"} {
		if !strings.Contains(title, want) {
			t.Errorf(".entry-line .entry-title lacks %s: %s", want, title)
		}
	}
	// The wide row is one line, as today.
	wide := cssRule(t, ".entry-line .entry-what")
	for _, want := range []string{"display: flex;", "white-space: nowrap;"} {
		if !strings.Contains(wide, want) {
			t.Errorf(".entry-line .entry-what lacks %s: %s", want, wide)
		}
	}
}

// More opens as a layer over the page: a backdrop dims the page under it,
// so the sheet's edge reads, and a tap on it closes the sheet. What the
// pixels do is the bead's screenshot.
func TestMoreBackdrop(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	phone := mediaCSS(t, string(css), "max-width: 699px")
	for _, want := range []string{
		".rail .more details[open] > summary { position: relative; z-index: 1; }",
		`.rail .more details[open] > summary::before { content: ""; position: fixed; inset: 0; background: var(--shade); }`,
		".rail .more details[open] > ul { z-index: 1; }",
	} {
		if !strings.Contains(phone, want) {
			t.Errorf("the phone rules lack %s", want)
		}
	}
}

// On a phone an ask's row gives the question the row's first line whole,
// ending in an ellipsis, and the bead and age stand under it.
// At 1100 px and wider the row is one line, as today. What the pixels do is
// the bead's screenshot.
func TestAskRowOnPhone(t *testing.T) {
	t.Parallel()
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	text := string(css)
	phone := mediaCSS(t, text, "max-width: 699px")
	for _, want := range []string{
		".needs a.ask { display: flex; flex-wrap: wrap; }",
		".needs a.ask .ask-q { order: -1; flex: 1 1 100%; }",
		".needs a.ask > .separator { display: none; }",
	} {
		if !strings.Contains(phone, want) {
			t.Errorf("the phone rules lack %s", want)
		}
	}
	// The phone row comes after the wide row's: at the same specificity the
	// later rule wins, so the wide grid would stand on a phone otherwise.
	if grid, flex := strings.Index(text, ".needs a.ask { display: grid;"), strings.Index(text, ".needs a.ask { display: flex;"); grid < 0 || flex < grid {
		t.Error("the phone row must come after the wide row's")
	}
	// The question still ends in an ellipsis, on its own line now.
	q := cssRule(t, ".needs .entry-title")
	for _, want := range []string{"overflow: hidden;", "text-overflow: ellipsis;", "white-space: nowrap;"} {
		if !strings.Contains(q, want) {
			t.Errorf(".needs .entry-title lacks %s: %s", want, q)
		}
	}
	// The wide row is one grid line, as today.
	wide := cssRule(t, ".needs a.ask")
	for _, want := range []string{"display: grid;", "grid-template-columns: minmax(0, 1fr) auto auto;"} {
		if !strings.Contains(wide, want) {
			t.Errorf(".needs a.ask lacks %s: %s", want, wide)
		}
	}
	meta := cssRule(t, ".needs .ask-meta")
	for _, want := range []string{"display: flex;", "white-space: nowrap;"} {
		if !strings.Contains(meta, want) {
			t.Errorf(".needs .ask-meta lacks %s: %s", want, meta)
		}
	}
}

// The talk page reads as a messaging app: the centre fills the viewport,
// the crew stays above the letters and the form below, and the letters
// scroll between them, bottom-up, so the newest is in view on open with
// the form under it. On a phone the shell fills the viewport instead and
// the centre takes what the bottom bar leaves. What the pixels do is the
// bead's screenshot.
func TestTalkLayout(t *testing.T) {
	t.Parallel()
	centre := cssRule(t, ".centre:has(#talk)")
	for _, want := range []string{"display: flex;", "flex-direction: column;", "height: 100dvh;"} {
		if !strings.Contains(centre, want) {
			t.Errorf(".centre:has(#talk) lacks %s: %s", want, centre)
		}
	}
	talk := cssRule(t, "#talk")
	for _, want := range []string{"display: flex;", "flex-direction: column;", "flex: 1;", "min-height: 0;"} {
		if !strings.Contains(talk, want) {
			t.Errorf("#talk lacks %s: %s", want, talk)
		}
	}
	letters := cssRule(t, "#letters")
	for _, want := range []string{"flex-direction: column-reverse;", "flex: 1;", "min-height: 0;", "overflow-y: auto;"} {
		if !strings.Contains(letters, want) {
			t.Errorf("#letters lacks %s: %s", want, letters)
		}
	}
	blocks := cssRule(t, "#letters > *")
	if !strings.Contains(blocks, "flex: none;") {
		t.Errorf("#letters > * lacks flex: none;: %s", blocks)
	}
	// The mail stands in the reading column, footplate's 880 pixels,
	// centred; the conversation's head carries the member's state quietly.
	column := cssRule(t, "#live, #earlier")
	for _, want := range []string{"max-width: var(--reading-width);", "margin-inline: auto;"} {
		if !strings.Contains(column, want) {
			t.Errorf("#live, #earlier lacks %s: %s", want, column)
		}
	}
	if root := cssRule(t, ":root"); !strings.Contains(root, "--reading-width: 880px;") {
		t.Errorf(":root lacks footplate's reading width: %s", root)
	}
	head := cssRule(t, ".talk-head .state")
	if !strings.Contains(head, "color: var(--faint);") {
		t.Errorf(".talk-head .state lacks color: var(--faint);: %s", head)
	}
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	// Where the window has no room for the right column, every page's
	// centre is capped at --centre-width but the talk page's, which takes
	// the reading width.
	narrow := mediaCSS(t, string(css), "max-width: 1099px")
	if want := ".shell:has(#talk) { grid-template-columns: var(--rail-width) var(--column-width) minmax(0, var(--reading-width)); }"; !strings.Contains(narrow, want) {
		t.Errorf("the rules below 1100 px lack %s", want)
	}
	phone := mediaCSS(t, string(css), "max-width: 699px")
	for _, want := range []string{
		".shell:has(#talk) { height: 100vh; height: 100dvh; }",
		".shell:has(#talk) > .centre { height: auto; flex: 1 1 auto; min-height: 0; }",
	} {
		if !strings.Contains(phone, want) {
			t.Errorf("the phone rules lack %s", want)
		}
	}
}

// The files behind a summary card read on a phone: the pager is a row of
// controls that wraps instead of widening the page, each as high as the
// move form's buttons; a text keeps its lines and pans inside its block; a
// file's name, a path with no space to break at, breaks where it would run
// past the window; and a card's sentence that opens a file reads as a link.
// What the pixels do is the bead's screenshot.
func TestFilePagesOnPhone(t *testing.T) {
	t.Parallel()
	for selector, wants := range map[string][]string{
		".preview-actions, .file-pager": {"display: flex;", "flex-wrap: wrap;"},
		".preview-actions button, .preview-actions a, .file-pager a, .file-pager span": {"min-height: 44px;", "overflow-wrap: anywhere;"},
		".file-pager a, .file-pager span":                                              {"flex: 1 1 0;", "min-width: max-content;", "text-align: center;"},
		".file-pager":                                                                  {"max-width: 90ch;"},
		".text.file":                                                                   {"white-space: pre;", "overflow-x: auto;"},
		".item-head h2":                                                                {"overflow-wrap: anywhere;"},
		".opens":                                                                       {"text-decoration: underline;"},
	} {
		rule := cssRule(t, selector)
		for _, want := range wants {
			if !strings.Contains(rule, want) {
				t.Errorf("%s lacks %s: %s", selector, want, rule)
			}
		}
	}
}
