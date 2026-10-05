package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// yardr chat is the view of internal/chat in a terminal (docs/chat.md): a
// user's conversations with the yard's residents. It sends with the store's
// Mail and reads with its Conversation, as yardr mail and yardr mail --with
// do, and follows the event stream as yardr watch does.

func (a *app) chatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chat [resident]",
		Short: "Talk with the yard's residents in one view: who runs, the conversation with each, a line to type",
		Long: `Opens a view of the yard's residents and your conversation with each, in the
terminal: the residents with what each is doing (not running, working, idle,
blocked, failed to start) and how much mail from it you have not seen, the
mail between you and the selected one, and a line to type. Enter sends the
line as mail to the selected resident, as yardr mail does.

The resident named is selected first, else the mayor. New mail and a
resident's state appear as they change, from the yard's event stream.
Opening a conversation marks your unread mail in it as read.

A resident reads its mail when it is idle: mail does not interrupt it, and
what it is working on stays in its pane. Under what you sent the view says
where it stands. A resident that waits at a prompt in its pane (a login, a
question, a dialog) cannot read mail: while it is selected, a notice above
the line you type in says what yardr knows and shows the last lines of its
pane. ctrl+g goes to the pane of the selected resident when this runs in
herdr, and names the herdr command anywhere else.

It is for a user of the yard (yardr user list), and needs a terminal.
Without one, yardr mail <resident> <text> sends and yardr mail --with
<resident> prints the conversation. Leaving the view (ctrl+c) stops nothing.`,
		Example: `  yardr chat
  yardr chat refinery`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if a.json || !interactive() {
				return errors.New("yardr chat is a view for a terminal, and this is none: " +
					"yardr mail <resident> <text> sends a message, and yardr mail --with <resident> prints the conversation (--json for a program)")
			}
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			me, err := a.actingUser(ctx, st)
			if err != nil {
				return err
			}
			if me == nil {
				operator, _, err := a.operator(ctx, st)
				if err != nil {
					return err
				}
				return fmt.Errorf("yardr chat is for a user of the yard, and %s is none: the operator (%s) adds one with yardr user add %s",
					a.who(), operator, a.who())
			}
			first := yardMayor(ctx, st)
			if len(args) == 1 {
				if _, err := st.GetResident(ctx, args[0]); err != nil {
					return err
				}
				first = args[0]
			}
			return a.chatOn(ctx, st, me.Name, first, "")
		},
	}
}

// chatOn shows the view to the user me, with the resident first selected,
// until they leave it. expect is a resident whose first mail is on its way
// (chat.Options.Expect), "" for none.
func (a *app) chatOn(ctx context.Context, st store.API, me, first, expect string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	state, err := chat.Load(ctx, st, me)
	if err != nil {
		return err
	}
	if len(state.Residents) == 0 {
		return errors.New("the yard has no residents to talk with: yardr resident add adds one, yardr init the mayor")
	}
	m := chat.New(ctx, st, chat.Options{Me: me, Select: first, Yardr: "yardr", Expect: expect,
		InHerdr: os.Getenv("HERDR_ENV") != "", Focus: focusAgent, Tail: paneTail, Refresh: paneRefresh}, state)
	return runChat(ctx, st, m, state.Head)
}

// chats reports whether this run can end in the chat: a person at a
// terminal who did not ask for none. An agent's pane is a terminal too, and
// a view would sit there.
func (a *app) chats() bool {
	return !a.json && !a.noChat && interactive() && !agentSession()
}

// toChat is the hand-over to the mayor: it opens the chat on it for the
// user this run acts as and, once they leave the view, says how to come
// back. A mayor in the session that was asked to greet (runner.Greeted) is
// expected to write first, and the view says so until it has.
//
// It reports false, having said why and opened nothing, when who acts is no
// user of the yard: the operator was given another name than the one this
// terminal acts under, and the mayor's mail goes to the operator.
func (a *app) toChat(ctx context.Context, cfg *config.Config, st store.API, mayor string) (bool, error) {
	yardr := yardrAt(cfg.Home)
	me, err := a.actingUser(ctx, st)
	if err != nil {
		return false, err
	}
	if me == nil {
		operator, _, err := a.operator(ctx, st)
		if err != nil {
			return false, err
		}
		fmt.Printf("\n%s chat is for a user of the yard, and this terminal acts as %s, who is none: the operator is %s,\n"+
			"and %s writes to them. As the operator: %s --actor %s chat (or YARDR_ACTOR=%s). As yourself, once\n"+
			"added: %s --actor %s user add %s\n",
			yardr, a.who(), operator, mayor, yardr, operator, operator, yardr, operator, a.who())
		return false, nil
	}
	expect := ""
	if r, err := st.GetResident(ctx, mayor); err != nil {
		return false, err
	} else if runner.Greeted(*r) {
		expect = mayor
	}
	if err := a.chatOn(ctx, st, me.Name, mayor, expect); err != nil {
		return false, err
	}
	fmt.Printf("\nYou left the chat; %s keeps running. To come back: %s chat\n", mayor, yardr)
	return true, nil
}

// paneRefresh is how often the view reads the pane of a resident that
// waits in it again, for the lines its notice quotes: one call of herdr,
// and only while such a resident is selected.
const paneRefresh = 5 * time.Second

// streamRetry is how long the view waits before it asks the yardr server
// for the stream again, as yardr watch does.
const streamRetry = 500 * time.Millisecond

// runChat shows the view until the person leaves it, feeding it the
// yard's events after seq. Like yardr watch, it resumes after the last
// event it got when the yardr server goes away and comes back. Tests
// replace it: they have no terminal.
var runChat = func(ctx context.Context, st store.API, m *chat.Model, after uint64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	go chat.Follow(ctx, st, after, streamRetry,
		func(e store.Event) { p.Send(chat.EventMsg(e)) },
		func(up bool) { p.Send(chat.StreamMsg{Up: up}) })
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil // the command's context ended: no error of the view
	}
	return err
}
