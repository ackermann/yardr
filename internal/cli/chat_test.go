package cli

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/store"
)

// chatYard is userYard with a terminal, a second resident, and the view
// itself replaced by a record of what it was opened with.
func chatYard(t *testing.T) (st store.API, opened *[]string) {
	t.Helper()
	st = userYard(t)
	mustAs(t, st, "jo", "resident", "add", "refinery", "--set", "kind=claude")
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

// yardr chat opens on the mayor, or on the resident named, for a user of
// the yard; anyone else is told who adds users.
func TestChatCommand(t *testing.T) {
	st, opened := chatYard(t)
	mustAs(t, st, "jo", "chat")
	mustAs(t, st, "jo", "chat", "refinery")
	if got := strings.Join(*opened, ","); got != "mayor,refinery" {
		t.Fatalf("opened on %q", got)
	}
	refusedAs(t, st, "jo", "nobody", "chat", "nobody")
	refusedAs(t, st, "eve", "yardr chat is for a user of the yard, and eve is none: the operator (jo) adds one with yardr user add eve", "chat")
	// A resident's session is no user either: its mail is yardr mail.
	refusedAs(t, st, "mayor", "is for a user of the yard", "chat")
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
	refusedAs(t, st, "jo", "yardr mail <resident> <text> sends a message, and yardr mail --with <resident> prints the conversation", "chat")
	interactive = func() bool { return true }
	refusedAs(t, st, "jo", "is a view for a terminal", "chat", "--json")
	if len(*opened) != 0 {
		t.Fatalf("the view opened without a terminal: %v", *opened)
	}
}

// A yard without residents has nobody to talk with, and the command says
// how to get one instead of showing an empty view.
func TestChatWithoutResidents(t *testing.T) {
	st, opened := chatYard(t)
	mustAs(t, st, "jo", "resident", "rm", "mayor")
	mustAs(t, st, "jo", "resident", "rm", "refinery")
	refusedAs(t, st, "jo", "the yard has no residents to talk with", "chat")
	if len(*opened) != 0 {
		t.Fatalf("the view opened: %v", *opened)
	}
}
