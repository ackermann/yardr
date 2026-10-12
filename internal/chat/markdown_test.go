package chat

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"yardr.dev/yardr/internal/store"
)

// drawn fails unless text is drawn as want at w columns behind the mail's
// lead.
func drawn(t *testing.T, text string, w int, want ...string) {
	t.Helper()
	if got := markdown(text, w, "  "); !slices.Equal(got, want) {
		t.Errorf("%q at %d columns is drawn\n%q\nwant\n%q", text, w, got, want)
	}
}

// Each form as it is drawn in a terminal with colour, with the styles as
// lipgloss renders them.
func TestMarkdownForms(t *testing.T) {
	profile(t, termenv.TrueColor, true)
	c := func(s string) string { return look.code.Render(s) }
	b := func(s string) string { return lipgloss.NewStyle().Bold(true).Render(s) }
	i := func(s string) string { return lipgloss.NewStyle().Italic(true).Render(s) }
	if c("x") == "x" || b("x") == "x" || i("x") == "x" || c("x") == b("x") {
		t.Fatalf("code %q, bold %q and italic %q are not told apart", c("x"), b("x"), i("x"))
	}

	// A code block: its lines as they are, the fences and the language gone.
	drawn(t, "run this:\n```sh\ngo test ./...\n  -race\n\n```\nthen tell me", 40,
		"  run this:",
		"    "+c("go test ./..."),
		"    "+c("  -race"),
		"  ",
		"  then tell me")
	// A tilde fence too, and one that is never closed runs to the end.
	drawn(t, "~~~\na  b\n```\n~~~", 40, "    "+c("a  b"), "    "+c("```"))
	drawn(t, "so far:\n```\nfunc main() {", 40, "  so far:", "    "+c("func main() {"))
	// Under an item, the block stands under the item's text.
	drawn(t, "- build:\n  ```\n  make\n  ```", 40, "  • build:", "      "+c("make"))

	// Code within a line, also with a backtick of its own.
	drawn(t, "set `GOFLAGS=-race` first", 40, "  set "+c("GOFLAGS=-race")+" first")
	drawn(t, "the mark is `` ` ``", 40, "  the mark is "+c("`"))
	// Nothing between backticks is drawn but as written.
	drawn(t, "`**not bold** _x_`", 40, "  "+c("**not bold** _x_"))

	// Bullets hang under their text; one indented keeps its place.
	drawn(t, "- one that is long enough to wrap\n* two\n  - nested item, long too", 20,
		"  • one that is long",
		"    enough to wrap",
		"  • two",
		"    • nested item,",
		"      long too")
	// A numbered item keeps its number, and hangs by its width.
	drawn(t, "1. first step of them\n10) the tenth step here", 16,
		"  1. first step",
		"     of them",
		"  10) the tenth",
		"      step here")
	// The forms inside a line are drawn in an item too.
	drawn(t, "- run `make` **now**", 40, "  • run "+c("make")+" "+b("now"))

	// A heading of any level is a bold line, and no larger.
	drawn(t, "# Plan\n### The next step of it", 14,
		"  "+b("Plan"),
		"  "+b("The next"),
		"  "+b("step of it"))
	drawn(t, "## Run `make`", 40, "  "+b("Run ")+look.span(strong|code).Render("make"))

	// Bold and italic, by star and by underscore, and one in the other.
	drawn(t, "it is **done** and *tested*, __really__ and _truly_.", 60,
		"  it is "+b("done")+" and "+i("tested")+", "+b("really")+" and "+i("truly")+".")
	drawn(t, "**all *of* it**", 40,
		"  "+b("all ")+look.span(strong|em).Render("of")+b(" it"))
	drawn(t, "(*see below*)", 40, "  ("+i("see below")+")")
	// A style ends with its row and begins again on the next.
	drawn(t, "say **these three words** now", 14,
		"  say "+b("these"),
		"  "+b("three words"),
		"  now")
}

// samples are texts with none of the forms, what only looks like one among
// them.
var samples = []string{
	"deploys are frozen until Friday",
	"a line\n\nand another after an empty one\n",
	"  indented, and a word-that-is-longer-than-any-narrow-pane-could-hold here",
	"issue #12 is fixed # really\n#12 too, and #hashtag",
	"see internal/chat/view_test.go and my_var_name, __init__.py",
	"rm *.o; ls **/*.go and src/**/x_test.go, then lib/*",
	"2 * 3 * 4 = 24, a*b*c, *args and **kwargs",
	"**/*.go and **/*.md\n*.go and *.md",
	"a ` alone, and `` two that never close",
	"-1 is no item, nor 3.5, nor -x, nor 1.5) this\n---\n* * *",
	"+ a line of a diff\n+ and another",
	"####### seven\n#\n#no space",
	"[a link](https://example.com/x_y_z) and <b>html</b> and ![an image](x.png)",
	"| a | table |\n|---|---|\n| 1 | 2 |",
	"snake_case_name and _private, 5 _ 3, a_b",
	"tabs\tstay\tas they were",
}

// A text with none of the forms is drawn exactly as it was before: the
// lines wrap gives, at every width, with colour and without.
func TestMarkdownLeavesTextAlone(t *testing.T) {
	for _, p := range []termenv.Profile{termenv.TrueColor, termenv.Ascii} {
		profile(t, p, true)
		for _, text := range append(samples, strings.Join(samples, "\n")) {
			for _, w := range []int{1, 3, 10, 24, 40, 80, 200} {
				if got, want := markdown(text, w, "  "), wrap(text, w, "  "); !slices.Equal(got, want) {
					t.Errorf("profile %v, %d columns: %q is drawn\n%q\nand was\n%q", p, w, text, got, want)
				}
			}
		}
	}
}

// A line of code longer than the pane is broken where the row ends, the
// rest marked: no word of it moves as a word of prose would.
func TestMarkdownLongCode(t *testing.T) {
	profile(t, termenv.TrueColor, true)
	c := func(s string) string { return look.code.Render(s) }
	more := look.faint.Render(codeMore)
	drawn(t, "```\n    if a { return b + c }\n```", 16,
		"    "+c("    if a { r"),
		"  "+more+c("eturn b + c "),
		"  "+more+c("}"))
	// Prose of the same words breaks between them.
	drawn(t, "if a { return b + c }", 16, "  if a { return", "  b + c }")
}

const sample = "# Plan\n" +
	"Do **this** in `main.go`:\n" +
	"- first, a point long enough to need a second row\n" +
	"  1. and under it a step\n" +
	"```go\n" +
	"\tfmt.Println(\"a line of code that is longer than the pane\")\n" +
	"```\n" +
	"_done_"

// Without colour nothing is bold either, so a heading and the marks in a
// line stay as written; a code block is told apart by its indentation, and
// a list hangs as it does with colour.
func TestMarkdownWithoutColour(t *testing.T) {
	profile(t, termenv.Ascii, true)
	drawn(t, sample, 34,
		"  # Plan",
		"  Do **this** in `main.go`:",
		"  • first, a point long enough to",
		"    need a second row",
		"    1. and under it a step",
		"        fmt.Println(\"a line of cod",
		"  ↪ e that is longer than the pane",
		"  ↪ \")",
		"  _done_")
}

// At every width the view has (bodyWidth is never under ten) each row fits and carries its styles whole, so that what
// stands beside it (the crew's column) is not drawn in them, and the text
// is the same whatever the terminal's colours: only the marks that colour
// stands for are gone.
func TestMarkdownWidths(t *testing.T) {
	text := sample + "\n" + strings.Join(samples, "\n")
	for w := 10; w <= 120; w++ {
		profile(t, termenv.TrueColor, true)
		rows := markdown(text, w, "  ")
		for _, row := range rows {
			// Less the space wrap leaves at a row's end, as it always did.
			if got := ansi.StringWidth(strings.TrimRight(row, " ")); got > w {
				t.Fatalf("at %d columns a row has %d: %q", w, got, row)
			}
			if strings.LastIndex(row, "\x1b[") != strings.LastIndex(row, "\x1b[0m") {
				t.Fatalf("at %d columns a row leaves a style open: %q", w, row)
			}
		}
		letters := func(rows []string) string {
			return strings.Join(strings.Fields(strings.NewReplacer("*", "", "_", "", "`", "", "#", "").Replace(ansi.Strip(strings.Join(rows, " ")))), "")
		}
		profile(t, termenv.Ascii, true)
		bare := markdown(text, w, "  ")
		if strings.Contains(strings.Join(bare, ""), "\x1b") {
			t.Fatalf("escape codes without colour at %d columns: %q", w, bare)
		}
		if letters(rows) != letters(bare) {
			t.Fatalf("at %d columns the text with colour reads\n%s\nand without\n%s", w, letters(rows), letters(bare))
		}
	}
}

// The view draws a crew member's mail so, in the wide view and the folded
// one, and what is beside a styled row keeps its own look.
func TestMarkdownInTheView(t *testing.T) {
	st := yard(t)
	must(t, st.Mail(context.Background(), me, sample, "chief"))
	for _, w := range []int{60, 140} {
		v := open(t, st, "chief", w, 30)
		profile(t, termenv.TrueColor, true)
		v.shows("  Plan", "  Do this in main.go:", "  • first, a point", "    1. and under it a step", "      fmt.Println(", "  done")
		v.hides("# Plan", "**this**", "```", "_done_")
		if !strings.Contains(v.m.View(), look.code.Render("main.go")) {
			t.Fatalf("at %d columns the code in the line has no colour:\n%q", w, v.m.View())
		}
		v.fits(w, 30)
		// A narrower terminal draws it again for its width.
		v.send(tea.WindowSizeMsg{Width: w - 20, Height: 30})
		v.fits(w-20, 30)
		v.shows("  Plan", "  Do this in main.go:")
		if w == 60 {
			v.shows("      fmt.Println(\"a line of code that", "  ↪  is longer than the pane\")")
		}
	}
}

// Text of another yard is drawn so too, and stays letters: an escape
// sequence in it, in a code block as anywhere, does not reach the
// terminal, and a link is the characters it is.
func TestMarkdownOfALinkedYard(t *testing.T) {
	st, own, key := linkedYard(t)
	allow(t, st, store.PeerAllow{Send: []string{"mail"}, Receive: []string{"mail"}, Expose: []string{me}})
	v := open(t, st, "yardmaster@north", 120, 30)
	fromNorth(t, own, key, "m1", "**read** [this](https://example.com/x)\n```\n\x1b[31mred\x1b[0m\x07 \x1b]8;;https://example.com\x1b\\code\x1b]8;;\x1b\\\n```\nand `\x1b[2Jthat`")
	v.stream()
	profile(t, termenv.TrueColor, true)
	v.shows("> yardmaster@north", "  read [this](https://example.com/x)", "    red code", "  and that")
	screen := v.m.View()
	for _, bad := range []string{"\x07", "\x1b[31m", "\x1b]8", "\x1b[2J"} {
		if strings.Contains(screen, bad) {
			t.Fatalf("north's %q reached the terminal:\n%q", bad, screen)
		}
	}
	if !strings.Contains(screen, look.code.Render("red code")) || !strings.Contains(screen, look.code.Render("that")) {
		t.Fatalf("north's code is not drawn as code:\n%q", screen)
	}
}

// BenchmarkLines draws a long conversation, each mail with every form:
// what a key pressed in the view costs.
//
//	go test ./internal/chat -run '^$' -bench Lines
func BenchmarkLines(b *testing.B) {
	for _, text := range []string{sample, strings.Join(samples[:4], "\n")} {
		name := "forms"
		if text != sample {
			name = "plain"
		}
		for _, draw := range []struct {
			name string
			fn   func(string, int, string) []string
		}{{"markdown", markdown}, {"wrap", func(s string, w int, lead string) []string { return wrap(s, w, lead) }}} {
			b.Run(fmt.Sprintf("%s/%s", name, draw.name), func(b *testing.B) {
				for b.Loop() {
					for range 500 {
						draw.fn(text, 96, "  ")
					}
				}
			})
		}
	}
}
