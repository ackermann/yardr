package chat

import (
	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"yardr.dev/yardr/internal/store"
)

// styles are the view's styles, all of them. Their colours are those of the
// theme the onboarding's forms have (huh's default, ThemeCharm), taken from
// its styles and never written out here, so that the view and the forms
// cannot drift apart. They are adaptive: a light and a dark terminal each
// get their own. A terminal without colour, and NO_COLOR, get none of it,
// neither colour nor bold: lipgloss leaves the text as it is there.
//
// No colour says anything alone: a state keeps its word, the selection its
// mark, a mail's standing its sentence, a weighty card its WEIGHTY word.
type styles struct {
	title lipgloss.Style // the view's name
	faint lipgloss.Style // what is beside the point: the time, the user's name in the title
	line  lipgloss.Style // the rules and the column's edge

	selected lipgloss.Style // the selected crew member's name and its mark
	unread   lipgloss.Style // the count of unread mail

	working lipgloss.Style
	idle    lipgloss.Style
	absent  lipgloss.Style // not running
	alarm   lipgloss.Style // blocked, failed to start, not logged in: someone has to act; a weighty card's WEIGHTY lines: read before approving

	member lipgloss.Style // a crew member's name on its mail
	own    lipgloss.Style // the user's name on theirs

	bead lipgloss.Style // what a notification is about: a bead's id, a source

	read    lipgloss.Style // under a sent mail: it was read
	waiting lipgloss.Style // it is in the inbox and will be read
	warn    lipgloss.Style // it cannot be read now; and the view's own warnings

	code lipgloss.Style // code in a mail's text: a block, and between backticks

	key  lipgloss.Style // a key on the keys' line
	word lipgloss.Style // what the key does

	prompt, placeholder, cursor lipgloss.Style // the line to type in
}

// look is the view's styles.
var look = themed(huh.ThemeCharm())

// themed takes the view's styles from a theme of huh's.
func themed(t *huh.Theme) styles {
	// The colour alone: a style of the theme may carry text of its own
	// (the error's " *", the selector's "> ") and margins.
	fg := func(s lipgloss.Style) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(s.GetForeground())
	}
	f := t.Focused
	var (
		heading = fg(f.Title)          // a field's title
		muted   = fg(f.Description)    // a field's description
		pick    = fg(f.SelectSelector) // the mark before the option under the cursor
		good    = fg(f.SelectedOption) // a chosen option
		bad     = fg(f.ErrorMessage)   // a field's error
	)
	return styles{
		title: heading.Bold(true),
		faint: muted,
		// The help's separator and not the forms' border: the border has
		// one colour for both backgrounds, nearly black on a light one.
		line: fg(t.Help.ShortSeparator),

		selected: pick.Bold(true),
		unread:   heading.Bold(true),

		working: good,
		idle:    muted,
		absent:  fg(t.Help.ShortKey),
		alarm:   bad.Bold(true),

		// A crew member's name has the colour its unread count has: both are
		// mail from it.
		member: heading.Bold(true),
		own:    fg(f.Option).Bold(true),

		// Bold and no colour: it is from nobody, neither a crew member's
		// name nor the user's.
		bead: lipgloss.NewStyle().Bold(true),

		read:    good,
		waiting: muted,
		warn:    bad,

		// The colour of what went well, which no name and no heading has:
		// code is told from the prose around it and is no warning.
		code: good,

		key:  t.Help.ShortKey,
		word: t.Help.ShortDesc,

		prompt:      f.TextInput.Prompt,
		placeholder: f.TextInput.Placeholder,
		cursor:      f.TextInput.Cursor,
	}
}

// span is the style of a run of a mail's text of the kinds given (strong,
// em, code): bold, italic, and code's colour. A tab stays the one character
// the wrapping counted.
func (s styles) span(kind uint8) lipgloss.Style {
	st := lipgloss.NewStyle().TabWidth(lipgloss.NoTabConversion)
	if kind&code != 0 {
		st = st.Foreground(s.code.GetForeground())
	}
	return st.Bold(kind&strong != 0).Italic(kind&em != 0)
}

// state is the style a crew member's state is listed in.
func (s styles) state(state string) lipgloss.Style {
	switch state {
	case Working:
		return s.working
	case Idle:
		return s.idle
	case NotRunning:
		return s.absent
	case Blocked, Failed, NotLoggedIn:
		return s.alarm
	}
	return lipgloss.NewStyle() // running: nothing more is known
}

// standing is the style of the line under a mail the user sent to r.
func (s styles) standing(read bool, r *store.CrewMember) lipgloss.Style {
	switch {
	case read:
		return s.read
	case r == nil:
		return s.waiting
	}
	if Waits(*r) {
		return s.warn
	}
	switch StateOf(*r) {
	case NotRunning, Failed:
		return s.warn
	}
	return s.waiting
}

// input styles the line to type in as a text field of the forms is.
func (s styles) input(in *textarea.Model) {
	for _, st := range []*textarea.Style{&in.FocusedStyle, &in.BlurredStyle} {
		st.Prompt, st.Placeholder = s.prompt, s.placeholder
	}
	in.Cursor.Style = s.cursor
}
