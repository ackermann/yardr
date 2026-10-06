package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"yardr.dev/yardr/internal/store"
)

// told subscribes ada to every bead of the yard that closes.
func told(t *testing.T, st store.API) {
	t.Helper()
	must(t, st.AddSubscription(context.Background(), me, store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll}, me))
}

// landed creates a bead titled title and closes it as the refinery does,
// which notifies ada twice (told): it returns the bead's id and the seq of
// the notification of its close.
func landed(t *testing.T, st store.API, title string) (string, uint64) {
	t.Helper()
	ctx := context.Background()
	b, err := st.CreateBead(ctx, store.NewBead{Title: title}, "jane")
	must(t, err)
	_, err = st.CloseBead(ctx, b.ID, "merged abc1234", store.Expect{}, "refinery")
	must(t, err)
	return b.ID, head(t, st)
}

// noteCursor is how far ada's notifications are acknowledged in the store.
func (v *view) noteCursor() uint64 {
	v.t.Helper()
	u, err := v.st.GetUser(context.Background(), me)
	must(v.t, err)
	return u.NotificationCursor
}

// row is the line of the wide view's column that lists name.
func (v *view) row(name string) string {
	v.t.Helper()
	for _, l := range strings.Split(v.screen(), "\n") {
		left, _, _ := strings.Cut(l, " │ ")
		if f := strings.Fields(strings.TrimPrefix(left, ">")); len(f) > 0 && f[0] == name {
			return strings.TrimRight(left, " ")
		}
	}
	v.t.Fatalf("no row of %s:\n%s", name, v.screen())
	return ""
}

// The yard's row is above the residents with the count of unread
// notifications, which lights no resident's row and is none of the mail's.
// Selecting it shows the notifications, newest last, each with its bead's
// id on a line of its own, and acknowledges them and not the mail.
func TestYardRow(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	told(t, st)
	first, _ := landed(t, st, "the first")
	second, newest := landed(t, st, "the second")
	must(t, st.Mail(ctx, me, "a word with you", "refinery"))

	v := open(t, st, "chief", 120, 24)
	if got, want := v.row(YardRow), fmt.Sprintf("  %-11s %-15s %d", YardRow, "", 4); got != want {
		t.Fatalf("the yard's row is %q, want %q", got, want)
	}
	lines := strings.Split(v.screen(), "\n")
	if !strings.HasPrefix(lines[1], "  yard ") || !strings.HasPrefix(lines[2], "> chief ") {
		t.Fatalf("the yard's row is not above the residents:\n%s", v.screen())
	}
	// Four notifications, and the residents' rows say nothing of them:
	// the chief has none, the refinery its one mail.
	if got := v.row("chief"); got != "> chief       idle" {
		t.Fatalf("the chief's row is %q", got)
	}
	if got := v.row("refinery"); got != "  refinery    idle            1" {
		t.Fatalf("the refinery's row is %q", got)
	}
	if v.m.Notifications() != 4 || v.m.Unread("chief") != 0 || v.m.Unread("refinery") != 1 {
		t.Fatalf("unread: yard %d, chief %d, refinery %d", v.m.Notifications(), v.m.Unread("chief"), v.m.Unread("refinery"))
	}
	v.hides(first, second) // in no conversation
	if v.noteCursor() != 0 {
		t.Fatalf("notifications acknowledged through %d before the row was opened", v.noteCursor())
	}

	v.key(tea.KeyShiftTab)
	if !v.m.OnYard() {
		t.Fatalf("shift+tab from the first resident selected %q", v.m.Selected())
	}
	v.fits(120, 24)
	v.shows("> yard", "  "+first, "  "+second, `closed (all) by refinery reason=merged abc1234 on "the second"`)
	s := v.screen()
	if strings.Index(s, first) > strings.Index(s, second) {
		t.Fatalf("the newest notification is not the last:\n%s", s)
	}
	// The id stands alone behind the time: nothing to cut off it.
	alone := false
	for _, l := range strings.Split(s, "\n") {
		alone = alone || strings.HasSuffix(strings.TrimRight(l, " "), "  "+second)
	}
	if !alone {
		t.Fatalf("no line ends with the bead's id %s:\n%s", second, s)
	}
	if v.noteCursor() != newest || v.m.Notifications() != 0 {
		t.Fatalf("opening the row acknowledged through %d, want %d (unread %d)", v.noteCursor(), newest, v.m.Notifications())
	}
	// The mail is another part of the inbox: still unread, there and here.
	if box, err := st.UserInbox(ctx, me, 1); err != nil || box.Unread != 1 || v.cursor() != 0 || v.m.Unread("refinery") != 1 {
		t.Fatalf("opening the yard's row read mail: %d unread in the store, cursor %d, %v", box.Unread, v.cursor(), err)
	}
	if got := v.row(YardRow); got != "> yard" {
		t.Fatalf("the yard's row, opened, is %q", got)
	}

	// One that arrives while the row is open is seen and acknowledged.
	third, newest := landed(t, st, "the third")
	v.hides(third)
	v.stream()
	v.shows(third)
	if v.noteCursor() != newest || v.m.Notifications() != 0 {
		t.Fatalf("a notification in the open row acknowledged through %d, want %d", v.noteCursor(), newest)
	}

	// One that arrives elsewhere is counted on the yard's row and nowhere
	// else, until it is acknowledged, here or by yardr inbox.
	v.key(tea.KeyTab)
	fourth, newest := landed(t, st, "the fourth")
	v.stream()
	v.hides(fourth)
	if got := v.row(YardRow); !strings.HasSuffix(got, " 2") || v.row("chief") != "> chief       idle" || v.noteCursor() >= newest {
		t.Fatalf("after one more: the yard's row %q, the chief's %q, cursor %d of %d", got, v.row("chief"), v.noteCursor(), newest)
	}
	v.send(tea.WindowSizeMsg{Width: 80, Height: 24})
	v.fits(80, 24)
	if got := strings.Split(v.screen(), "\n")[1]; got != "yard (2)  [chief idle]  refinery idle (1)" {
		t.Fatalf("the folded line is %q", got)
	}
	must(t, st.AckUserNotifications(ctx, me, newest))
	v.stream()
	if got := strings.Split(v.screen(), "\n")[1]; got != "yard  [chief idle]  refinery idle (1)" {
		t.Fatalf("the folded line after an ack elsewhere is %q", got)
	}
	v.key(tea.KeyShiftTab)
	v.fits(80, 24)
	v.shows("[yard]  chief idle  refinery idle (1)", fourth)
}

func TestYardRowDoesNotCollideWithResidentName(t *testing.T) {
	st := yard(t)
	ctx := context.Background()
	if _, err := st.PutResident(ctx, store.Resident{Name: YardRow, Runner: "herdr", Config: map[string]any{"kind": "claude"}}, "jo"); err != nil {
		t.Fatal(err)
	}
	v := open(t, st, "chief", 120, 24)
	s := v.screen()
	if !strings.Contains(s, "yard-notify") || !strings.Contains(s, "  yard        not running") {
		t.Fatalf("the system row and resident named yard are not distinct:\n%s", s)
	}
}

// The yard's row has no line to type in and the keys that do something
// there: what is typed goes nowhere, to no resident and into no draft.
func TestYardRowTakesNoAnswer(t *testing.T) {
	for _, w := range []int{80, 140} {
		st := yard(t)
		told(t, st)
		landed(t, st, "work")
		v := open(t, st, "chief", w, 24)
		v.shows("Write to chief", "enter send")
		v.key(tea.KeyShiftTab)
		v.fits(w, 24)
		v.hides("Write to", "enter send", "new line", "^g")
		for _, l := range strings.Split(v.screen(), "\n") {
			_, right, cut := strings.Cut(l, " │ ")
			if !cut {
				right = l
			}
			if strings.HasPrefix(right, "> ") && !strings.HasPrefix(l, "> "+YardRow) {
				t.Fatalf("a prompt on the yard's row at %d columns:\n%s", w, v.screen())
			}
		}
		lines := strings.Split(v.screen(), "\n")
		if lines[len(lines)-1] != YardKeys {
			t.Fatalf("the last line is %q", lines[len(lines)-1])
		}

		before := head(t, st)
		v.typed("to nobody")
		v.key(tea.KeyEnter)
		v.key(tea.KeyCtrlG)
		if head(t, st) != before {
			t.Fatal("something typed on the yard's row was sent")
		}
		v.shows("nobody to answer here")
		v.hides("to nobody")
		v.key(tea.KeyTab)
		if v.m.Selected() != "chief" || v.m.input.Value() != "" {
			t.Fatalf("back on %q the line holds %q", v.m.Selected(), v.m.input.Value())
		}
		v.shows("Write to chief")
		v.hides("nobody to answer here")
	}
	if YardKeys != "tab next resident  pgup/pgdn scroll  ^c quit" {
		t.Fatalf("the yard's keys are %q", YardKeys)
	}
}

// Without notifications the row says what would be here; a yard without
// residents has the row all the same, and tab reaches it.
func TestYardRowEmpty(t *testing.T) {
	v := open(t, yard(t), "chief", 120, 24)
	if got := v.row(YardRow); got != "  yard" {
		t.Fatalf("the yard's row without notifications is %q", got)
	}
	v.key(tea.KeyShiftTab)
	v.shows("No notifications.", "yardr subscribe ada yardr --done")

	st := yard(t)
	ctx := context.Background()
	must(t, st.DeleteResident(ctx, "chief", "", "jo"))
	must(t, st.DeleteResident(ctx, "refinery", "", "jo"))
	told(t, st)
	id, _ := landed(t, st, "work")
	v = open(t, st, "", 120, 24)
	v.shows("The yard has no residents")
	v.hides(id)
	v.key(tea.KeyTab)
	v.shows("> yard", id)
	v.fits(120, 24)
}

// A long run of notifications scrolls as a conversation does, the newest
// in view first; one about no bead names its source, and what a source
// wrote reaches the terminal as text only.
func TestYardRowLines(t *testing.T) {
	v := open(t, yard(t), "chief", 80, 16)
	var notes []store.Notification
	for i := range 30 {
		id := fmt.Sprintf("yard-%04d", i)
		notes = append(notes, store.Notification{Seq: uint64(1000 + i), At: time.Now(), Source: store.SourceYardr, Kind: "closed",
			Bead: id, Title: "work", Text: id + ` closed (done) by refinery reason=merged on "work"`})
	}
	notes = append(notes, store.Notification{Seq: 2000, At: time.Now(), Source: "linear", Kind: "hook",
		Text: "hook (all) linear comment create by Ann: \x1b[31mred\x1b[0m \x07bell  https://linear.example/c/1"})
	v.send(notesMsg{notes: notes})
	v.shows("yard (31)")
	v.key(tea.KeyShiftTab)
	v.fits(80, 16)
	v.shows("  linear", "by Ann: red bell  https://linear.example/c/1")
	v.hides("yard-0000", "yard-0029 closed") // the id is said once, on its own line
	if raw := v.m.View(); strings.Contains(raw, "\x07") || strings.Contains(raw, "\x1b[31m") {
		t.Fatalf("a notification's control characters reach the terminal:\n%q", raw)
	}
	for range 40 {
		v.key(tea.KeyPgUp)
	}
	v.shows("yard-0000")
	v.hides("linear.example")
	v.fits(80, 16)

	// An older read of the store that answers late does not take the
	// newer ones away.
	v.send(notesMsg{notes: notes[:3]})
	v.key(tea.KeyPgDown)
	for range 40 {
		v.key(tea.KeyPgDown)
	}
	v.shows("linear.example")
}

// noAck is a store that acknowledges no notification.
type noAck struct{ store.API }

func (noAck) AckUserNotifications(context.Context, string, uint64) error {
	return errors.New("the store is away")
}

// Notifications that could not be marked read are said to be, and count
// as unread again.
func TestYardRowAckFails(t *testing.T) {
	st := yard(t)
	told(t, st)
	landed(t, st, "work")
	v := open(t, noAck{st}, "chief", 120, 24)
	v.key(tea.KeyShiftTab)
	v.shows("your notifications could not be marked read: the store is away")
	if v.m.Notifications() != 2 {
		t.Fatalf("%d unread after an ack that failed", v.m.Notifications())
	}
}

// The yard's row reads the same with and without colour, and has colour
// where a terminal has it.
func TestYardRowWithoutColour(t *testing.T) {
	st := yard(t)
	told(t, st)
	landed(t, st, "work")
	for _, w := range []int{80, 140} {
		v := open(t, st, "chief", w, 24)
		v.key(tea.KeyShiftTab)
		profile(t, termenv.Ascii, true)
		plain := v.m.View()
		if strings.Contains(plain, "\x1b") {
			t.Fatalf("escape codes without colour at %d columns:\n%q", w, plain)
		}
		profile(t, termenv.TrueColor, true)
		coloured := v.m.View()
		if !strings.Contains(coloured, "\x1b[38;2;") || ansi.Strip(coloured) != plain {
			t.Fatalf("at %d columns the coloured view reads\n%s\nand the one without colour\n%s", w, ansi.Strip(coloured), plain)
		}
	}
}
