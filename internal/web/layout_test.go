package web_test

import (
	"os"
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
		".needs li > a { display: flex; align-items: baseline; gap: 6px; width: 100%; min-width: 0; box-sizing: border-box; overflow: hidden; white-space: nowrap; }",
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
