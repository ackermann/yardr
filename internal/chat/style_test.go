package chat

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// profile renders as a terminal of profile p on a dark or light background
// does, for the rest of the test. A test's own output is no terminal, so
// without it nothing is styled.
func profile(t *testing.T, p termenv.Profile, dark bool) {
	t.Helper()
	was, wasDark := lipgloss.ColorProfile(), lipgloss.HasDarkBackground()
	t.Cleanup(func() {
		lipgloss.SetColorProfile(was)
		lipgloss.SetHasDarkBackground(wasDark)
	})
	lipgloss.SetColorProfile(p)
	lipgloss.SetHasDarkBackground(dark)
}

// Without colour (NO_COLOR, a terminal that has none: lipgloss renders
// both as the Ascii profile) the view has no escape codes, and its text is
// the coloured view's: colour adds nothing a reader of the text misses,
// and moves nothing.
func TestWithoutColour(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	must(t, st.Mail(ctx, me, "landed", "refinery"))
	for _, size := range [][2]int{{80, 24}, {Wide - 1, 24}, {Wide, 24}, {140, 30}} {
		w, h := size[0], size[1]
		v := open(t, st, "mayor", w, h)
		v.typed("deploys are frozen")
		v.key(tea.KeyEnter)
		must(t, st.Mail(ctx, me, "noted", "mayor"))
		must(t, st.ResidentStatus(ctx, "mayor", "blocked"))
		v.stream()
		v.typed("until Friday")
		v.key(tea.KeyEnter)
		v.send(StreamMsg{Up: false})

		profile(t, termenv.Ascii, true)
		plain := v.m.View()
		if strings.Contains(plain, "\x1b") {
			t.Fatalf("escape codes without colour at %d columns:\n%q", w, plain)
		}
		for _, dark := range []bool{true, false} {
			profile(t, termenv.TrueColor, dark)
			coloured := v.m.View()
			if !strings.Contains(coloured, "\x1b[38;2;") {
				t.Fatalf("no colour in a terminal that has it, at %d columns:\n%q", w, coloured)
			}
			if got := ansi.Strip(coloured); got != plain {
				t.Fatalf("at %d columns the coloured view reads\n%s\nand the one without colour\n%s", w, got, plain)
			}
		}
		v.fits(w, h)
	}
}

// The keys' line reads as it did before its keys and words had styles.
func TestKeysLine(t *testing.T) {
	const want = "enter send  ^j new line  tab next resident  pgup/pgdn scroll  ^g pane  ^c quit"
	if Keys != want {
		t.Fatalf("the keys' line is %q", Keys)
	}
	profile(t, termenv.TrueColor, true)
	if look.key.Render("x") == look.word.Render("x") {
		t.Fatal("a key and its word look alike")
	}
}

// What the colours have to tell apart, they do, on a dark and on a light
// background: the states that need someone from those that do not, the
// user's name from a resident's, and the three ways a sent mail stands.
func TestColoursTellApart(t *testing.T) {
	blocked := yard(t)
	must(t, blocked.ResidentStatus(context.Background(), "mayor", "blocked"))
	rs, err := blocked.Residents(context.Background())
	must(t, err)
	mayor, refinery := &rs[0], &rs[1]
	if StateOf(*mayor) != Blocked || StateOf(*refinery) != Idle {
		t.Fatalf("mayor is %s, refinery %s", StateOf(*mayor), StateOf(*refinery))
	}
	for _, dark := range []bool{true, false} {
		profile(t, termenv.TrueColor, dark)
		in := func(s lipgloss.Style) string { return s.Render("x") }
		plain := in(lipgloss.NewStyle())
		differ := func(what string, a, b lipgloss.Style) {
			t.Helper()
			if in(a) == in(b) {
				t.Errorf("dark %v: %s look alike: %q", dark, what, in(a))
			}
		}
		for _, loud := range []string{Blocked, Failed} {
			if in(look.state(loud)) == plain {
				t.Errorf("dark %v: %s has no colour", dark, loud)
			}
			for _, quiet := range []string{Idle, Working, NotRunning, Running} {
				differ(loud+" and "+quiet, look.state(loud), look.state(quiet))
			}
		}
		differ("working and idle", look.state(Working), look.state(Idle))
		differ("the selected resident and another", look.selected, lipgloss.NewStyle())
		differ("the user's name and a resident's", look.own, look.resident)
		differ("read and sent", look.standing(true, refinery), look.standing(false, refinery))
		differ("sent and cannot be read", look.standing(false, refinery), look.standing(false, mayor))
		differ("read and cannot be read", look.standing(true, mayor), look.standing(false, mayor))
		if in(look.standing(false, nil)) != in(look.standing(false, refinery)) {
			t.Errorf("dark %v: mail to a resident that is gone does not stand as sent", dark)
		}
	}
}
