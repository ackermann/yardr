package cli

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/store"
)

// chatYard is userYard with a terminal, a second crew member, and the view
// itself replaced by a record of what it was opened with.
func chatYard(t *testing.T) (st store.API, opened *[]string) {
	t.Helper()
	st = userYard(t)
	mustAs(t, st, "jo", "crew", "add", "assembly", "--set", "kind=claude")
	was, ran := interactive, runChat
	t.Cleanup(func() { interactive, runChat = was, ran })
	interactive = func() bool { return true }
	opened = new([]string)
	runChat = func(_ context.Context, _ store.API, m *chat.Model, _ uint64) error {
		*opened = append(*opened, m.Selected())
		return nil
	}
	return st, opened
}

// yardr chat opens on the yardmaster, or on the crew member named, for a user of
// the yard; anyone else is told who adds users. The view it opens can answer
// a question, and the command's help names the key that does.
func TestChatCommand(t *testing.T) {
	st, opened := chatYard(t)
	var view *chat.Model
	opens := runChat
	runChat = func(ctx context.Context, s store.API, m *chat.Model, after uint64) error {
		view = m
		return opens(ctx, s, m, after)
	}
	mustAs(t, st, "jo", "chat")
	// A view given no way to answer says a question is answered in a
	// terminal; this one was given it, so it only finds nothing waiting.
	view.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	view.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if got := view.View(); !strings.Contains(got, "nothing to answer: yardmaster asks you nothing that waits") {
		t.Fatalf("%s does not answer in the view the command opens:\n%s", chat.AnswerKey, got)
	}
	help := mustAs(t, st, "jo", "chat", "--help")
	for _, want := range []string{chat.AnswerKey + " answers", chat.SecretKey + " opens a field"} {
		if !strings.Contains(help, want) {
			t.Fatalf("yardr chat --help does not say %q:\n%s", want, help)
		}
	}
	mustAs(t, st, "jo", "chat", "assembly")
	if got := strings.Join(*opened, ","); got != "yardmaster,assembly" {
		t.Fatalf("opened on %q", got)
	}
	refusedAs(t, st, "jo", "nobody", "chat", "nobody")
	refusedAs(t, st, "eve", "yardr chat is for a user of the yard, and eve is none: the operator (jo) adds one with yardr user add eve", "chat")
	// A crew member's session is no user either: its mail is yardr mail.
	refusedAs(t, st, "yardmaster", "is for a user of the yard", "chat")
	mustAs(t, st, "jo", "user", "add", "eve")
	mustAs(t, st, "eve", "chat")
	if len(*opened) != 3 {
		t.Fatalf("the view opened %d times, want 3", len(*opened))
	}
}

// Without a terminal it does not start, and says what does the same from
// a script.
func TestChatNeedsATerminal(t *testing.T) {
	st, opened := chatYard(t)
	interactive = func() bool { return false }
	refusedAs(t, st, "jo", "yardr mail <crew member> <text> sends a message, and yardr mail --with <crew member> prints the conversation", "chat")
	interactive = func() bool { return true }
	refusedAs(t, st, "jo", "is a view for a terminal", "chat", "--json")
	if len(*opened) != 0 {
		t.Fatalf("the view opened without a terminal: %v", *opened)
	}
}

// A yard without crew members has nobody to talk with, and the command says
// how to get one instead of showing an empty view.
func TestChatWithoutCrew(t *testing.T) {
	st, opened := chatYard(t)
	mustAs(t, st, "jo", "crew", "rm", "yardmaster")
	mustAs(t, st, "jo", "crew", "rm", "assembly")
	refusedAs(t, st, "jo", "the yard has no crew to talk with", "chat")
	if len(*opened) != 0 {
		t.Fatalf("the view opened: %v", *opened)
	}
}
