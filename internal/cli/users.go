package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

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
yardr user set <name> notify=<source>, and of the parts they name with
tell=<parts>.

Who a user is in a source, so a thread opened there can be assigned to
them: yardr user identity.

A device is a program of a user that calls the yard from outside it, let in
by a token: yardr user device.`}
	add := &cobra.Command{
		Use: "add <name>", Short: "Add a user", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
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
			// Whom the linked yards expose are no users of this yard: they
			// follow its own in the table, and --json keeps to the users
			// (yardr peer list --json has each peer's roster, as said).
			ps, err := st.Peers(c.Context())
			if err != nil {
				return err
			}
			for i := range views {
				views[i].Told = told(c.Context(), st, &views[i].User)
			}
			return a.out(views, func() {
				rows := [][]string{}
				for _, v := range views {
					role := "-"
					if v.Operator {
						role = "operator"
					}
					rows = append(rows, []string{v.Name, role, fmt.Sprint(v.Unread), or(v.Told, "-")})
				}
				table("NAME\tROLE\tUNREAD\tTOLD", rows)
				printPeerParties(store.PeerParties(ps))
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
			if v.Devices, err = st.Devices(c.Context(), v.Name); err != nil {
				return err
			}
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
				for _, src := range sortedKeys(v.Identities) {
					id := v.Identities[src]
					fmt.Printf("  %s identity: %s\n", src, strings.Join(slices.DeleteFunc([]string{id.ID, id.Key},
						func(s string) bool { return s == "" }), ", "))
				}
				if v.Told != "" {
					fmt.Printf("  told outside the yard: %s\n", v.Told)
				}
				if last := lastPush(&v.User); last != "" {
					fmt.Printf("  last told: %s\n", last)
				}
				for _, d := range v.Devices {
					fmt.Printf("  device: %s, added %s, last used %s\n", d.Name, d.CreatedAt.Local().Format(mailTime), lastUsed(d))
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
			if err := st.DeleteUser(c.Context(), args[0], a.who()); err != nil {
				return err
			}
			return a.out(map[string]string{"removed": args[0]}, func() { fmt.Printf("user %s removed\n", args[0]) })
		},
	}
	set := &cobra.Command{
		Use:   "set <name> [notify=<source>] [env_file=<file>] [tell=<parts>]",
		Short: "Say which source tells a user outside the yard, where their address is, and of what",
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

yardr user key <name> <source> writes that file for you, so nobody writes
it by hand, and sets env_file where there is none.

tell is which parts the source tells them of: a list of notifications, mail
and asks, with commas between. tell= with nothing after it is all of them,
as for a user who never set it. Any other name is refused. What still
waited to be told of a part that is left out is dropped.

The user sets their own, or the operator does it for them. It is refused
from the pane of an agent, whoever the command says it is: what lands in a
person's inbox goes where these point.

What is told, of the parts tell names: mail to the user (their own too, and
a timer they set), the notifications of their subscriptions and the asks
for them, by the daemon, once each is 20
seconds old and still unread. What was acknowledged by then (yardr chat
showing it, yardr inbox --ack) is not told. All that is due goes as one
message: who it is from or which bead, what happened, and the start of the
text; several are counted and named. At most 20 messages an hour; what is
over waits and goes as one. A send that fails is tried again for up to 8
tries and 6 hours, then given up as a push_failed event; yardr user show
has how the last one ended. The inbox is filled first and never waits for
the source. notify= drops what was still to be told.`,
		Example: `  yardr user key mckean phone          # asks for PHONE_USER, your id at the service, unseen
  yardr user set mckean notify=phone
  yardr source check phone --user mckean
  yardr user set mckean notify=phone env_file=~/.yardr/users/mckean.env   # a file you keep yourself
  yardr user set mckean tell=notifications   # no mail and no asks on the phone`,
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
				if _, twice := set[key]; !ok || twice || (key != store.UserNotify && key != store.UserEnvFile && key != store.UserTell) {
					return fmt.Errorf("%q: want %s=<source>, %s=<file> or %s=<%s>, each once", arg, store.UserNotify, store.UserEnvFile, store.UserTell, strings.Join(store.TellParts, ","))
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
				switch key {
				case store.UserEnvFile:
					u.EnvFile = value
				case store.UserNotify:
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
	key := &cobra.Command{
		Use:   "key <name> <source>",
		Short: "Give a user's own address in a source: typed without echo, or a NAME=value line on standard input",
		Long: `Writes the user's own address in a source that tells people into the file
it is read from at each send, so that nobody writes that file by hand. The
name is the one the source's notify key names (notify.address.env). The
file is the user's env_file, or with none set
~/.yardr/users/<name>.env, which is then set as yardr user set sets it. The
yard's server writes it as yardr source key writes a source's: the line of
the name is replaced or added, every other line and comment is kept, and
the file is replaced whole, by a rename, with mode 0600. A file that is
open to others, or is a symbolic link, is refused and not repaired.

On a terminal the value is asked for and nothing is shown as it is typed.
When standard input is no terminal it is read as one NAME=value line, which
is how a script gives it. The value is never an argument and never a
variable of the command's environment: every process of the same OS user
can read both. It is kept in that file and nowhere else: not in the store,
an event or the log, and no command prints it.

It does not say the user is told through the source: that is yardr user
set <name> notify=<source>, which the command names when it is still to be
run. The user gives their own, or the operator does it for them; it is
refused from the pane of an agent, as yardr user set is, and for a source
with no notify key. An agent that needs it asks for it instead: yardr bead
ask --secret <source> --for-user <name>.

The file and the store are two writes. When the yard wrote the file and
could not record it, it says so, with the name and the file: give it
again, and it ends as one giving that worked.`,
		Example: `  yardr user key mckean phone       # asks for PHONE_USER
  op read op://me/phone/user | sed 's/^/PHONE_USER=/' | yardr user key mckean phone`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if _, err := st.GetUser(c.Context(), args[0]); err != nil {
				return err
			}
			src, err := st.GetSource(c.Context(), args[1])
			if err != nil {
				return err
			}
			d, err := src.Declaration()
			if err != nil {
				return err
			}
			// Refused before anything is typed.
			if d.Notify == nil {
				return fmt.Errorf("source %s: its declaration has no notify key, so it reaches no person and reads no address of %s's", d.Name, args[0])
			}
			values, err := readKeys(d, []string{d.Notify.AddressEnv}, c.InOrStdin(), typedUnseen(c.InOrStdin()))
			if err != nil {
				return err
			}
			keyed, err := st.KeyUser(c.Context(), args[0], d.Name, values, a.who())
			if err != nil {
				return err
			}
			return a.out(keyed, func() {
				fmt.Printf("user %s: %s\n", keyed.User, keyed.State)
				fmt.Println("next: " + keyed.Next)
			})
		},
	}
	cmd.AddCommand(add, list, show, rm, set, key, a.userIdentityCmd(), a.deviceCmd())
	return cmd
}

// told says in a line where a user is told outside the yard, whether that
// is set up, and of which parts when they name some (tell); "" for a user
// who is told nowhere. It reads the user's env file for the names it sets
// and never prints a value.
func told(ctx context.Context, st store.API, u *store.User) string {
	said := u.Told()
	if u.Notify == "" {
		return said
	}
	state, err := notify.Ready(ctx, st, u)
	if err != nil {
		return said + ", which is not set up: " + err.Error()
	}
	return said + " (" + state + ")"
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
	// set up, in words; "" for a user told nowhere.
	Told string `json:"told,omitempty"`
	// Devices are the user's devices; only yardr user show reads them.
	Devices []store.Device `json:"devices,omitempty"`
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

// printPeerParties lists whom the linked yards expose to this one, under
// the users: the address mail to each goes to, whether they are a person
// or a crew member there, whether mail reaches them, and the line their
// yard gives of what they do. Nothing for none.
func printPeerParties(parties []store.PeerParty) {
	if len(parties) == 0 {
		return
	}
	rows := [][]string{}
	for _, e := range parties {
		is := "crew"
		if e.Kind == store.RosterUser {
			is = "person"
		}
		rows = append(rows, []string{e.Address(), is, takesMail(e), or(e.Role, "-")})
	}
	fmt.Println()
	table("OF A LINKED YARD\tIS\tMAIL\tROLE", rows)
}

// takesMail is whether mail from this yard reaches e, and whose grant is
// missing where it does not: both sides of a link allow mail.
func takesMail(e store.PeerParty) string {
	switch {
	case !e.Sends:
		return fmt.Sprintf("no (yardr peer allow %s --send mail)", e.Peer)
	case !e.Mail:
		return fmt.Sprintf("no (%s takes none from this yard)", e.Peer)
	}
	return "yes"
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

// unknownLogin is the line for an actor nobody named: neither --actor nor
// $YARDR_ACTOR, so the login name, which is neither a user nor a crew
// member. A command that acts as one (inbox, mail, reply, chat) refuses
// with it: who the actor is and where it came from, the yard's users, and
// both ways to act as one. nil when the actor was named or is known, or
// the yard has no users to name; then each command says its own, as before.
func (a *app) unknownLogin(ctx context.Context, st store.API) error {
	if a.actor != "" || config.Getenv("ACTOR") != "" || a.identity().actor != "" {
		return nil
	}
	if os.Getenv("USER") == "" {
		return nil
	}
	me := a.who()
	if _, err := st.GetUser(ctx, me); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if _, err := st.GetCrew(ctx, me); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	users, err := st.Users(ctx)
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}
	names := make([]string, len(users))
	for i, u := range users {
		names[i] = u.Name
	}
	from := "$USER"
	return fmt.Errorf("%s (from %s) is neither a user nor a crew member of this yard; "+
		"its users are %s. Act as one: --actor %s or export YARDR_ACTOR=%s",
		me, from, strings.Join(names, ", "), names[0], names[0])
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
				from := l.From
				if l.As != "" {
					from += " (as " + l.As + ")"
				}
				from = store.ViaMark(from, l.Via)
				// The sender and the text are writers' words, shown without
				// the mark, and the files stand under them on a marked line
				// of their own, so no mail's words forge its files line.
				fmt.Printf("%6d %s  mail from %s%s: %s\n", l.Seq, l.At.Local().Format(mailTime), store.WithoutFilesMark(from), state, store.WithoutFilesMark(l.Text))
				if line := store.NoteFilesLine(a.mailHome(), l.Files); line != "" {
					fmt.Printf("    %s\n", line)
				}
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
// other party, oldest first. For a user that is also the mail of the names
// trusted as them; --with <name>@<peer> is that address alone.
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
	letters, _, err := st.Conversation(ctx, me, with, after, 0, 0)
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
			// A user's conversation has the mail of the names trusted as
			// them (store.Conversation): each says how it went.
			from := l.From
			if l.As != "" {
				from += " (as " + l.As + ")"
			}
			from = store.ViaMark(from, l.Via)
			if l.To != with && l.To != me {
				from += " to " + l.To
			}
			// As the inbox above: the writers' words without the mark, and
			// the files under them on a marked line of their own.
			fmt.Printf("%6d %s  %-6s  %s: %s\n", l.Seq, l.At.Local().Format(mailTime), state, store.WithoutFilesMark(from), store.WithoutFilesMark(l.Text))
			if line := store.NoteFilesLine(a.mailHome(), l.Files); line != "" {
				fmt.Printf("    %s\n", line)
			}
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
	devices, err := st.Devices(ctx, "")
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
	for _, d := range devices {
		if d.Unused(time.Now()) {
			notes = append(notes, unusedDevice(d, yardr))
		}
	}
	for _, r := range crew {
		known[r.Name] = true
		if r.Name == all[store.OperatorSetting] {
			notes = append(notes, fmt.Sprintf("crew member %s has the operator's name, so the operator has no user and no inbox: mail to %s is the crew member's. "+
				"Rename one of the two: %s --actor %s settings set operator <new name> gives the operator a name of their own and adds their user "+
				"(as %s, since settings are the operator's to change)", r.Name, r.Name, yardr, r.Name, r.Name))
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
