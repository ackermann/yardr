package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/notify"
	"yardr.dev/yardr/internal/store"
)

// A user is a person with an inbox (docs/users.md): the operator, and
// whoever the operator adds. A command acts as a user when its actor is a
// user's name. Nothing nudges a person: yardr doctor and bare yardr say
// what waits for them.

const mailTime = "01-02 15:04"

func (a *app) userCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "The people of the yard: each a name and an inbox",
		Long: `A user is a person as a party of the yard: a name and an inbox, as a crew member
has. Crew members and users mail a user with yardr mail <name>; the user reads
with yardr inbox and yardr mail --with <name> (docs/users.md).

The operator (yardr settings get operator) is a user, added with the yard.
Only the operator adds and removes users, and the operator's own is not
removed. Crew members and users share one space of names.

A command acts as a user when its actor (--actor, $YARDR_ACTOR, else the
login name) is the user's name. yardr does not check who is at the keyboard.

A user is told outside the yard through the source they name with
yardr user set <name> notify=<source>.`}
	add := &cobra.Command{
		Use: "add <name>", Short: "Add a user", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := a.mayManageUsers(c.Context(), st, "adds"); err != nil {
				return err
			}
			u, err := st.AddUser(c.Context(), args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(u, func() { fmt.Printf("user %s added\n", u.Name) })
		},
	}
	list := &cobra.Command{
		Use: "list", Short: "List the users", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			views, err := userViews(c.Context(), st, "")
			if err != nil {
				return err
			}
			return a.out(views, func() {
				rows := [][]string{}
				for _, v := range views {
					role := "-"
					if v.Operator {
						role = "operator"
					}
					rows = append(rows, []string{v.Name, role, fmt.Sprint(v.Unread)})
				}
				table("NAME\tROLE\tUNREAD", rows)
			})
		},
	}
	show := &cobra.Command{
		Use: "show <name>", Short: "Show a user: what waits in their inbox, and what they subscribe to", Args: cobra.ExactArgs(1),
		Long: `For a user who is told outside the yard (yardr user set <name>
notify=<source>), "last told" is how the last message through that source
ended: when, whether the source took it, and how many items of the inbox it
was for. One that failed is tried again by the daemon for up to six hours;
one given up is a push_failed event (yardr events). The inbox has
everything either way.`,
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			views, err := userViews(c.Context(), st, args[0])
			if err != nil {
				return err
			}
			v := views[0]
			if v.Timers, err = st.Timers(c.Context(), v.Name); err != nil {
				return err
			}
			notes, err := st.UserNotifications(c.Context(), v.Name, 1)
			if err != nil {
				return err
			}
			v.Notifications = notes.Unread
			v.Told = told(c.Context(), st, &v.User)
			return a.out(v, func() {
				fmt.Print(v.Name)
				if v.Operator {
					fmt.Print("  the yard's operator")
				}
				fmt.Printf("\n  added %s by %s\n", v.CreatedAt.Local().Format("2006-01-02 15:04"), v.CreatedBy)
				if v.Unread == 0 {
					fmt.Println("  inbox: nothing unread")
				} else {
					fmt.Printf("  inbox: %d unread from %s\n", v.Unread, strings.Join(v.From, ", "))
				}
				if v.Notifications > 0 {
					fmt.Printf("  %s unread\n", notifications(v.Notifications))
				}
				for _, sub := range v.Subscriptions {
					fmt.Printf("  subscribed: %s\n", sub)
				}
				if v.Told != "" {
					fmt.Printf("  told outside the yard: %s\n", v.Told)
				}
				if last := lastPush(&v.User); last != "" {
					fmt.Printf("  last told: %s\n", last)
				}
				printTimers(v.Timers, v.Name, "  ")
			})
		},
	}
	rm := &cobra.Command{
		Use: "rm <name>", Short: "Remove a user (the mail stays in the log)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := a.mayManageUsers(c.Context(), st, "removes"); err != nil {
				return err
			}
			if err := st.DeleteUser(c.Context(), args[0], a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"removed": args[0]}, func() { fmt.Printf("user %s removed\n", args[0]) })
		},
	}
	set := &cobra.Command{
		Use:   "set <name> [notify=<source>] [env_file=<file>]",
		Short: "Say which source tells a user outside the yard, and where their address is",
		Long: `notify is the source that tells the user outside the yard: a declared source
whose file has a notify key (yardr source list shows which), or a source
yardr has built in that can. One that cannot is refused, with the ones that
can. notify= with nothing after it is nowhere.

env_file is a file of KEY=VALUE lines that sets what the source needs of
this person: for a declared source the variable its notify key names
(notify.address.env), the person's own address in it. It is held to the rule
of every env_file: a regular file of mode 0600 or stricter, read at each
send. The store keeps its path, never what is in it. A file that is open to
others or does not set the name is refused here.

The user sets their own, or the operator does it for them. It is refused
from the pane of an agent, whoever the command says it is: what lands in a
person's inbox goes where these point.

What is told: mail to the user (their own too, and a timer they set) and
the notifications of their subscriptions, by the daemon, once each is 20
seconds old and still unread. What was acknowledged by then (yardr chat
showing it, yardr inbox --ack) is not told. All that is due goes as one
message: who it is from or which bead, what happened, and the start of the
text; several are counted and named. At most 20 messages an hour; what is
over waits and goes as one. A send that fails is tried again for up to 8
tries and 6 hours, then given up as a push_failed event; yardr user show
has how the last one ended. The inbox is filled first and never waits for
the source. notify= drops what was still to be told.`,
		Example: `  printf 'PUSHOVER_USER=%s\n' '<your user key>' > ~/.yardr/users/mckean.env
  chmod 600 ~/.yardr/users/mckean.env
  yardr user set mckean notify=pushover env_file=~/.yardr/users/mckean.env
  yardr source check pushover --user mckean`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			u, err := st.GetUser(c.Context(), args[0])
			if err != nil {
				return err
			}
			set := map[string]string{}
			for _, arg := range args[1:] {
				key, value, ok := strings.Cut(arg, "=")
				if _, twice := set[key]; !ok || twice || (key != store.UserNotify && key != store.UserEnvFile) {
					return fmt.Errorf("%q: want %s=<source> or %s=<file>, each once", arg, store.UserNotify, store.UserEnvFile)
				}
				if key == store.UserEnvFile && value != "" {
					if value, err = config.ExpandPath(value); err != nil {
						return err
					}
					// Refused before it is kept, whatever it is to set.
					if _, err := config.ReadEnvFile(value); err != nil {
						return err
					}
				}
				if key == store.UserEnvFile {
					u.EnvFile = value
				} else {
					u.Notify = value
				}
				set[key] = value
			}
			// With both there, the file is held to what the source reads of
			// it. A source that cannot tell anyone is the store's to refuse,
			// which names the ones that can.
			if _, err := notify.Names(c.Context(), st, u.Notify); err == nil && u.EnvFile != "" {
				if _, err := notify.Ready(c.Context(), st, u); err != nil {
					return err
				}
			}
			saved, err := st.SetUser(c.Context(), args[0], set, a.who())
			if err != nil {
				return err
			}
			state := told(c.Context(), st, saved)
			return a.out(map[string]any{"user": saved, "told": state}, func() {
				fmt.Printf("user %s: told outside the yard: %s\n", saved.Name, or(state, "nowhere"))
				if saved.Notify != "" && saved.EnvFile != "" {
					fmt.Printf("next: yardr source check %s --user %s sends one test message\n", saved.Notify, saved.Name)
				}
			})
		},
	}
	cmd.AddCommand(add, list, show, rm, set)
	return cmd
}

// told says in a line where a user is told outside the yard and whether
// that is set up, "" for a user who is told nowhere. It reads the user's
// env file for the names it sets and never prints a value.
func told(ctx context.Context, st store.API, u *store.User) string {
	if u.Notify == "" {
		if u.EnvFile != "" {
			return "nowhere (env_file " + u.EnvFile + " is set, and no notify)"
		}
		return ""
	}
	state, err := notify.Ready(ctx, st, u)
	if err != nil {
		return "through " + u.Notify + ", which is not set up: " + err.Error()
	}
	return "through " + u.Notify + " (" + state + ")"
}

// lastPush says in a line how the last message to a user outside the yard
// ended, "" for a user none was sent to: when, through which source, and
// whether the source took it. It is what the user's record keeps
// (store.PushResult), which has nothing of the message.
func lastPush(u *store.User) string {
	l := u.LastPush
	if l == nil {
		return ""
	}
	out := l.At.Local().Format("2006-01-02 15:04") + " through " + or(l.Source, "no source")
	what := fmt.Sprintf("%d of the inbox in one message", l.Count)
	switch {
	case l.OK():
		return fmt.Sprintf("%s, taken (status %d, %s)", out, l.Status, what)
	case l.Reason == store.PushRetry:
		out += fmt.Sprintf(", failed (%s, %s)", l.Error, what)
		if l.RetryAt != nil {
			out += "; next try " + l.RetryAt.Local().Format("15:04:05")
		}
		return out
	}
	return fmt.Sprintf("%s, not sent: %s (%s, %s)", out, l.Reason, l.Error, what)
}

// userView is a user as yardr user list and show print one.
type userView struct {
	store.User
	Operator bool     `json:"operator"`
	Unread   int      `json:"unread"`
	From     []string `json:"unread_from"`
	// Timers are the live timers that tell the user or that they set; only
	// yardr user show reads them.
	Timers []store.Timer `json:"timers,omitempty"`
	// Notifications are the unread notifications, counted apart from the
	// mail (Unread); only yardr user show reads them.
	Notifications int `json:"unread_notifications"`
	// Told is where the user is told outside the yard and whether that is
	// set up, in words; only yardr user show reads it.
	Told string `json:"told,omitempty"`
}

// userViews reads the user name, or all of them for "".
func userViews(ctx context.Context, st store.API, name string) ([]userView, error) {
	var users []store.User
	if name == "" {
		var err error
		if users, err = st.Users(ctx); err != nil {
			return nil, err
		}
	} else {
		u, err := st.GetUser(ctx, name)
		if err != nil {
			return nil, err
		}
		users = []store.User{*u}
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	out := []userView{}
	for _, u := range users {
		box, err := st.UserInbox(ctx, u.Name, 1)
		if err != nil {
			return nil, err
		}
		out = append(out, userView{User: u, Operator: u.Name == all[store.OperatorSetting], Unread: box.Unread, From: box.From})
	}
	return out, nil
}

// mayManageUsers refuses adding or removing a user to anyone but the
// operator: who is a party of the yard is theirs to say, and a crew member's
// session adding one would make itself a second voice. Like maySpeakAs it
// is a line, not a wall.
func (a *app) mayManageUsers(ctx context.Context, st store.API, does string) error {
	operator, is, err := a.operator(ctx, st)
	if err != nil || is {
		return err
	}
	return fmt.Errorf("only the operator (%s) %s users; this is %s", operator, does, a.who())
}

// mayMailUser is the one rule about who writes to a user: not an agent
// working a bead. What it has to say to a person belongs on its bead,
// where the next stage and the person both read it. It is a rule of this
// command, not of the user's record, and is lifted here if that changes.
func (a *app) mayMailUser(user string) error {
	bead := a.identity().bead
	if bead == "" {
		return nil
	}
	return fmt.Errorf("mail to %s: an agent working a bead does not mail a user, its channel is the bead (%s). "+
		"Note there what %s should read (yardr bead note %s \"…\"), and when you cannot go on without an answer, "+
		"end the stage with the question outcome (yardr bead done %s --outcome question)", user, bead, user, bead, bead)
}

// actingUser is the user this run acts as: its actor, when that is a
// user's name. nil otherwise: a crew member's session acts as the crew member,
// whose name no user has.
func (a *app) actingUser(ctx context.Context, st store.API) (*store.User, error) {
	u, err := st.GetUser(ctx, a.who())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return u, err
}

// userBox is a user's inbox as yardr inbox prints it: its two parts, the
// mail (a person or a crew member talking) and the notifications (the yard
// reporting). A part that was not asked for is absent. The mail is at the
// top level, where it was when the inbox had one part.
type userBox struct {
	*store.Mailbox
	Notifications *store.NotificationPage `json:"notifications,omitempty"`
}

// userInbox is yardr inbox run as a user: of each part asked for, the
// unread and the latest read, under the part's name and unread count. With
// ack the unread shown are marked read, in the parts shown only: each part
// has a cursor of its own.
func (a *app) userInbox(ctx context.Context, st store.API, name string, limit int, ack, mail, notes bool) error {
	var box userBox
	var err error
	acked := map[string]int{}
	if mail {
		if box.Mailbox, err = st.UserInbox(ctx, name, limit); err != nil {
			return err
		}
	}
	if notes {
		if box.Notifications, err = st.UserNotifications(ctx, name, limit); err != nil {
			return err
		}
	}
	// Both are read before either is acknowledged: an ack is logged, and
	// what is shown is the inbox as it was found.
	if mail {
		// The unread come first, oldest first.
		shown := 0
		for _, l := range box.Mail {
			if !l.Read {
				shown++
			}
		}
		if acked["mail"] = shown; ack && shown > 0 {
			if err := st.AckUserInbox(ctx, name, box.Mail[shown-1].Seq); err != nil {
				return err
			}
		}
	}
	if notes {
		// Oldest first, so the unread are last and the newest of them ends
		// the page unless the limit cut it.
		ns := box.Notifications.Notifications
		shown := 0
		for _, n := range ns {
			if !n.Read {
				shown++
			}
		}
		if acked["notifications"] = shown; ack && shown > 0 {
			if err := st.AckUserNotifications(ctx, name, ns[len(ns)-1].Seq); err != nil {
				return err
			}
		}
	}
	return a.out(box, func() {
		if mail {
			fmt.Println(partHead("Mail", len(box.Mail), box.Unread))
			for _, l := range box.Mail {
				state := ""
				if l.Read {
					state = " (read)"
				}
				fmt.Printf("%6d %s  mail from %s%s: %s\n", l.Seq, l.At.Local().Format(mailTime), l.From, state, l.Text)
			}
			partFoot(box.Unread-acked["mail"], acked["mail"], ack)
		}
		if mail && notes {
			fmt.Println()
		}
		if notes {
			p := box.Notifications
			fmt.Println(partHead("Notifications", len(p.Notifications), p.Unread))
			for _, n := range unreadFirst(p.Notifications) {
				state := ""
				if n.Read {
					state = "(read) "
				}
				fmt.Printf("%6d %s  %s%s\n", n.Seq, n.At.Local().Format(mailTime), state, n.Text)
			}
			partFoot(p.Unread-acked["notifications"], acked["notifications"], ack)
		}
	})
}

// unreadFirst is a page of notifications in the order the mail above it
// is listed in: the unread, oldest first, then the read, the latest first.
// The store gives a page oldest first throughout, which put the unread
// under the read in one part and over them in the other.
func unreadFirst(page []store.Notification) []store.Notification {
	rows := make([]store.Notification, 0, len(page))
	for _, n := range page {
		if !n.Read {
			rows = append(rows, n)
		}
	}
	for i := len(page) - 1; i >= 0; i-- {
		if page[i].Read {
			rows = append(rows, page[i])
		}
	}
	return rows
}

// partHead is the line over a part of a user's inbox: its name and how
// much of it is unread.
func partHead(part string, shown, unread int) string {
	switch {
	case shown == 0:
		return part + ": none"
	case unread == 0:
		return part + ": nothing unread; the latest read:"
	}
	return fmt.Sprintf("%s: %d unread", part, unread)
}

// partFoot says, under a part, what of its unread was not shown and what
// an ack marked read.
func partFoot(rest, shown int, ack bool) {
	if rest > 0 {
		fmt.Printf("… %d more unread\n", rest)
	}
	if ack {
		fmt.Printf("acknowledged %d\n", shown)
	}
}

// notifications is "1 notification" or "N notifications".
func notifications(n int) string {
	if n == 1 {
		return "1 notification"
	}
	return fmt.Sprintf("%d notifications", n)
}

// conversation is yardr mail --with: the mail between who acts and the
// other party, oldest first.
func (a *app) conversation(ctx context.Context, st store.API, with string, after uint64) error {
	// The other has to be a party of the yard, or a crew member of a yard it
	// is linked with, or a typo reads as a conversation nobody has had yet.
	// Whether that yard has the crew member is not this one's to know.
	if _, peer, outside := strings.Cut(with, "@"); outside {
		if _, err := st.GetPeer(ctx, peer); err != nil {
			return fmt.Errorf("--with %s: %w", with, err)
		}
	} else if _, err := st.GetCrew(ctx, with); errors.Is(err, store.ErrNotFound) {
		if _, err := st.GetUser(ctx, with); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("--with %s: no crew member and no user of that name: %w", with, store.ErrNotFound)
			}
			return err
		}
	} else if err != nil {
		return err
	}
	me := a.who()
	letters, err := st.Conversation(ctx, me, with, after)
	if err != nil {
		return err
	}
	return a.out(letters, func() {
		if len(letters) == 0 {
			if after > 0 {
				fmt.Printf("no mail between %s and %s after %d\n", me, with, after)
			} else {
				fmt.Printf("no mail between %s and %s\n", me, with)
			}
			return
		}
		for _, l := range letters {
			state := "unread"
			switch {
			case l.Delivery != "":
				state = l.Delivery
			case l.Read:
				state = "read"
			}
			fmt.Printf("%6d %s  %-6s  %s: %s\n", l.Seq, l.At.Local().Format(mailTime), state, l.From, l.Text)
		}
	})
}

// mailState is what waits for the user a command acts as.
type mailState struct {
	User   string   `json:"user"`
	Unread int      `json:"unread"`
	From   []string `json:"from"`
}

// readMail reads what waits for the acting user; nil when who acts is no
// user.
func (a *app) readMail(ctx context.Context, st store.API) (*mailState, error) {
	u, err := a.actingUser(ctx, st)
	if err != nil || u == nil {
		return nil, err
	}
	box, err := st.UserInbox(ctx, u.Name, 1)
	if err != nil {
		return nil, err
	}
	return &mailState{User: u.Name, Unread: box.Unread, From: box.From}, nil
}

// unread is "N unread from <names>".
func (m *mailState) unread() string {
	return fmt.Sprintf("%d unread from %s", m.Unread, strings.Join(m.From, ", "))
}

// line is the mail's line in the report of the yard.
func (m *mailState) line(yardr string) docLine {
	if m.Unread == 0 {
		return docLine{tagOK, "mail", "nothing unread for " + m.User}
	}
	return docLine{tagNote, "mail", fmt.Sprintf("%s for %s: %s inbox reads them, --ack marks them read", m.unread(), m.User, yardr)}
}

// partyNotes are what yardr doctor notes about the yard's names: a
// crew member that has the operator's name, and members of groups that are
// neither a user nor a crew member.
func partyNotes(ctx context.Context, st store.API, crew []store.CrewMember, groups []store.Group, yardr string) ([]string, error) {
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	users, err := st.Users(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	notes := []string{}
	for _, u := range users {
		known[u.Name] = true
		// The last message, while the user is still told through a source:
		// a failure that a later message made good is gone from the record.
		if u.Notify != "" && u.LastPush != nil && !u.LastPush.OK() {
			notes = append(notes, fmt.Sprintf("user %s was not told outside the yard: %s. Their inbox has it all the same; "+
				"%s source check %s --user %s sends one test message", u.Name, lastPush(&u), yardr, u.Notify, u.Name))
		}
	}
	for _, r := range crew {
		known[r.Name] = true
		if r.Name == all[store.OperatorSetting] {
			notes = append(notes, fmt.Sprintf("crew member %s has the operator's name, so the operator has no user and no inbox: mail to %s is the crew member's. "+
				"Rename one of the two: %s settings set operator <name> gives the operator a name of their own and adds their user", r.Name, r.Name, yardr))
		}
	}
	for _, g := range groups {
		var unknown []string
		for _, m := range g.Members {
			if !known[m] && !slices.Contains(unknown, m) {
				unknown = append(unknown, m)
			}
		}
		if len(unknown) > 0 {
			notes = append(notes, fmt.Sprintf("group %s: %s%s neither a user nor a crew member, so nobody of that name can be mailed (%s user add <name>)",
				g.Name, strings.Join(unknown, ", "), isAre(unknown), yardr))
		}
	}
	return notes, nil
}

// unreadLine prints what waits for the acting user under bare yardr's
// help: the unread mail, and how many notifications. It is a courtesy: a
// store it cannot read says nothing here.
func (a *app) unreadLine(ctx context.Context) {
	if a.json {
		return
	}
	st, err := a.store(ctx)
	if err != nil {
		return
	}
	m, err := a.readMail(ctx, st)
	if err != nil || m == nil || m.Unread == 0 {
		return
	}
	// Notifications are no mail and are not counted as mail: they are
	// said after it, by their number alone. Without unread mail the line
	// is not printed for them: nobody is talking, and a person who never
	// acknowledges a stream would read it under every command.
	told := 0
	if p, err := st.UserNotifications(ctx, m.User, 1); err == nil {
		told = p.Unread
	}
	if told > 0 {
		fmt.Printf("\nMail for %s: %s, and %s (yardr inbox).\n", m.User, m.unread(), notifications(told))
		return
	}
	fmt.Printf("\nMail for %s: %s (yardr inbox).\n", m.User, m.unread())
}
