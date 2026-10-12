package web_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// At 1100 px and wider every page fills the window: the centre takes all
// the width the menu and the aside leave, and the menu and the aside stand
// in the same place on every page that has them. The markup carries which
// pages keep the aside (every page but the yard panel, whose wide shell
// TestYardPageIsWide covers), the stylesheet the fluid centre, the
// readable measure and the unchanged narrower windows; what the pixels do
// is the bead's screenshots'.
func TestPagesFillTheWindow(t *testing.T) {
	f := newFixture(t)
	run, _, _, _, _ := stocked(t, f)
	for _, path := range []string{"/", "/beads", "/sessions", "/crew", "/line", "/flows", "/beads/" + run.ID} {
		r := f.get(path)
		if r.status != 200 {
			t.Errorf("%s: status %d", path, r.status)
			continue
		}
		if !strings.Contains(r.body, `<div class="shell"`) {
			t.Errorf("%s: not in the shell", path)
		}
		if strings.Contains(r.body, "shell wide") {
			t.Errorf("%s: wide, so it keeps no right column", path)
		}
		if !strings.Contains(r.body, "<aside") {
			t.Errorf("%s: keeps no right column", path)
		}
	}
	css, err := os.ReadFile("assets/yard.css")
	must(t, err)
	for _, want := range []string{
		// The desktop shell: a fluid centre between the menu and the aside.
		"grid-template-columns: var(--menu-width) minmax(0, 1fr) var(--right-width);",
		// Running text keeps a measure of about 90 characters.
		".text, .entry-note { max-width: 90ch; }",
		// Below 1100 px the centre keeps its width, centred.
		".shell { grid-template-columns: var(--menu-width) minmax(0, var(--centre-width)); justify-content: center; }",
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
	if n := strings.Count(string(css), "var(--centre-width)"); n != 1 {
		t.Errorf("var(--centre-width) is used %d times, want once (below 1100 px)", n)
	}
}

// Home's weight: the answer is the only large line, human words (the
// answer, reasons, titles) render in the proportional face and machine
// words (ids, counts, times, stages) stay monospace, and every row runs
// one line with an ellipsis. What the pixels do is the bead's screenshot.
func TestHomeWeight(t *testing.T) {
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
