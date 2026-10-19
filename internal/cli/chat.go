package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/chat"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// yardr chat is the view of internal/chat in a terminal (docs/chat.md): a
// user's conversations with the yard's crew, and with those the linked
// yards expose. It sends with the store's Mail (MailPeer to another yard)
// and reads with its Conversation, as yardr mail and yardr mail --with do,
// and follows the event stream as yardr watch does.

func (a *app) chatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chat [name]",
		Short: "Talk with the yard's crew in one view: who runs, the conversation with each, a line to type",
		Long: `Opens a view of the yard's crew and your conversation with each, in the
terminal: the crew with what each is doing (not running, working, idle,
blocked, failed to start, not logged in) and how much mail from it you have
not seen, the mail between you and the selected one, and a line to type.
Enter sends the line as mail to the selected crew member, as yardr mail does.

The crew member named is selected first, else the yardmaster. New mail and a
crew member's state appear as they change, from the yard's event stream.
Opening a conversation marks your unread mail in it as read.

After the crew the view lists whom the linked yards expose to this one
(yardr user list has them too), each as <name>@<peer>. Enter there sends as
yardr mail <name>@<peer> does: the mail is queued for the other yard under
your name, and under it the view says whether it is queued, delivered or was
not delivered. The answer shows in the same conversation once the link
exposes you to that yard (yardr peer allow <peer> --receive mail --expose
<you>, the operator's). Such a row has no state and no pane.

A crew member reads its mail when it is idle: mail does not interrupt it, and
what it is working on stays in its pane. Under what you sent the view says
where it stands. A crew member that waits at a prompt in its pane (a login, a
question, a dialog) cannot read mail: while it is selected, a notice above
the line you type in says what yardr knows and shows the last lines of its
pane. ctrl+g goes to the pane of the selected crew member when this runs in
herdr, and names the herdr command anywhere else.

A conversation shows the questions its crew member asks you; yardr bead
answer answers them. A secret ask (yardr bead ask --secret) is answered here
too: ctrl+o opens a field that shows a dot for each character, enter gives
what you typed to the yard's server, which writes it to the file the
source's credential is read from, and esc sends nothing.

It is for a user of the yard (yardr user list), and needs a terminal.
Without one, yardr mail <crew member> <text> sends and yardr mail --with
<crew member> prints the conversation. Leaving the view (ctrl+c) stops nothing.`,
		Example: `  yardr chat
  yardr chat assembly
  yardr chat yardmaster@north`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if a.json || !interactive() {
				return errors.New("yardr chat is a view for a terminal, and this is none: " +
					"yardr mail <crew member> <text> sends a message, and yardr mail --with <crew member> prints the conversation (--json for a program)")
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
				if uerr := a.unknownLogin(ctx, st); uerr != nil {
					return uerr
				}
				operator, _, err := a.operator(ctx, st)
				if err != nil {
					return err
				}
				return fmt.Errorf("yardr chat is for a user of the yard, and %s is none: the operator (%s) adds one with yardr user add %s",
					a.who(), operator, a.who())
			}
			first := yardmasterOf(ctx, st)
			if len(args) == 1 {
				if _, peer, outside := strings.Cut(args[0], "@"); outside {
					// One of a linked yard has a row when its yard exposes
					// them, and no other is written to from here.
					ps, err := st.Peers(ctx)
					if err != nil {
						return err
					}
					if !slices.ContainsFunc(store.PeerParties(ps), func(e store.PeerParty) bool { return e.Address() == args[0] }) {
						return fmt.Errorf("%s: the linked yards expose nobody of that address to this one (yardr user list has whom they do, yardr peer show %s what that yard last said)", args[0], peer)
					}
				} else if _, err := st.GetCrew(ctx, args[0]); err != nil {
					return err
				}
				first = args[0]
			}
			return a.chatOn(ctx, st, me.Name, first, "")
		},
	}
}

// chatOn shows the view to the user me, with the crew member first selected,
// until they leave it. expect is a crew member whose first mail is on its way
// (chat.Options.Expect), "" for none.
func (a *app) chatOn(ctx context.Context, st store.API, me, first, expect string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	state, err := chat.Load(ctx, st, me)
	if err != nil {
		return err
	}
	if len(state.Crew) == 0 && len(store.PeerParties(state.Peers)) == 0 {
		return errors.New("the yard has no crew to talk with: yardr crew add adds one, yardr init the yardmaster")
	}
	m := chat.New(ctx, st, chat.Options{Me: me, Select: first, Yardr: "yardr", Home: a.mailHome(), Expect: expect,
		InHerdr: os.Getenv("HERDR_ENV") != "", Focus: focusAgent, Tail: paneTail, Refresh: paneRefresh,
		Asks: func(ctx context.Context) ([]store.ResolvedAsk, error) { return chat.AsksFor(ctx, st, me) }, Secrets: st}, state)
	return runChat(ctx, st, m, state.Head)
}

// chats reports whether this run can end in the chat: a person at a
// terminal who did not ask for none. An agent's pane is a terminal too, and
// a view would sit there.
func (a *app) chats() bool {
	return !a.json && !a.noChat && interactive() && !agentSession()
}

// toChat is the hand-over to the yardmaster: it opens the chat on it for the
// user this run acts as and, once they leave the view, says how to come
// back. A yardmaster in the session that was asked to greet (runner.Greeted) is
// expected to write first, and the view says so until it has.
//
// It reports false, having said why and opened nothing, when who acts is no
// user of the yard: the operator was given another name than the one this
// terminal acts under, and the yardmaster's mail goes to the operator.
func (a *app) toChat(w io.Writer, ctx context.Context, cfg *config.Config, st store.API, yardmaster string) (bool, error) {
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
		fmt.Fprintf(w, "\n%s chat is for a user of the yard, and this terminal acts as %s, who is none: the operator is %s,\n"+
			"and %s writes to them. As the operator: %s --actor %s chat (or YARDR_ACTOR=%s). As yourself, once\n"+
			"added: %s --actor %s user add %s\n",
			yardr, a.who(), operator, yardmaster, yardr, operator, operator, yardr, operator, a.who())
		return false, nil
	}
	expect := ""
	if r, err := st.GetCrew(ctx, yardmaster); err != nil {
		return false, err
	} else if runner.Greeted(*r) {
		expect = yardmaster
	}
	if err := a.chatOn(ctx, st, me.Name, yardmaster, expect); err != nil {
		return false, err
	}
	fmt.Fprintf(w, "\nYou left the chat; %s keeps running. To come back: %s chat\n", yardmaster, yardr)
	return true, nil
}

// paneRefresh is how often the view reads the pane of a crew member that
// waits in it again, for the lines its notice quotes: one call of herdr,
// and only while such a crew member is selected.
const paneRefresh = 5 * time.Second

// streamRetry is how long a command that follows the yard's events (the
// view, yardr watch, a ping waiting for its pong) waits before it asks the
// yardr server for the stream again.
const streamRetry = 500 * time.Millisecond

// runChat shows the view until the person leaves it, feeding it the
// yard's events after seq. Like yardr watch, it resumes after the last
// event it got when the yardr server goes away and comes back. Tests
// replace it: they have no terminal.
var runChat = func(ctx context.Context, st store.API, m *chat.Model, after uint64) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	go store.Follow(ctx, st, after, streamRetry,
		func(e store.Event) { p.Send(chat.EventMsg(e)) },
		func(up bool) { p.Send(chat.StreamMsg{Up: up}) })
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil {
		return nil // the command's context ended: no error of the view
	}
	return err
}
