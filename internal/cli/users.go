package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

// A user is a person with an inbox (docs/users.md): the operator, and
// whoever the operator adds. A command acts as a user when its actor is a
// user's name. Nothing nudges a person: yardr doctor and bare yardr say
// what waits for them.

const mailTime = "01-02 15:04"

func (a *app) userCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "user", Short: "The people of the yard: each a name and an inbox",
		Long: `A user is a person as a party of the yard: a name and an inbox, as a resident
has. Residents and users mail a user with yardr mail <name>; the user reads
with yardr inbox and yardr mail --with <name> (docs/users.md).

The operator (yardr settings get operator) is a user, added with the yard.
Only the operator adds and removes users, and the operator's own is not
removed. Residents and users share one space of names.

A command acts as a user when its actor (--actor, $YARDR_ACTOR, else the
login name) is the user's name. yardr does not check who is at the keyboard.`}
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
		Use: "show <name>", Short: "Show a user and what waits in their inbox", Args: cobra.ExactArgs(1),
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
	cmd.AddCommand(add, list, show, rm)
	return cmd
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
// operator: who is a party of the yard is theirs to say, and a resident's
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
// user's name. nil otherwise: a resident's session acts as the resident,
// whose name no user has.
func (a *app) actingUser(ctx context.Context, st store.API) (*store.User, error) {
	u, err := st.GetUser(ctx, a.who())
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return u, err
}

// userInbox is yardr inbox run as a user: the unread, then the latest
// read, and with ack the unread shown marked read.
func (a *app) userInbox(ctx context.Context, st store.API, name string, limit int, ack bool) error {
	box, err := st.UserInbox(ctx, name, limit)
	if err != nil {
		return err
	}
	shown := 0
	for _, l := range box.Mail {
		if !l.Read {
			shown++
		}
	}
	if ack && shown > 0 {
		if err := st.AckUserInbox(ctx, name, box.Mail[shown-1].Seq); err != nil {
			return err
		}
	}
	return a.out(box, func() {
		if len(box.Mail) == 0 {
			fmt.Println("inbox empty")
			return
		}
		if box.Unread == 0 {
			fmt.Println("nothing unread; the latest read:")
		}
		for _, l := range box.Mail {
			state := ""
			if l.Read {
				state = " (read)"
			}
			fmt.Printf("%6d %s  mail from %s%s: %s\n", l.Seq, l.At.Local().Format(mailTime), l.From, state, l.Text)
		}
		if rest := box.Unread - shown; rest > 0 {
			fmt.Printf("… %d more unread\n", rest)
		}
		if ack {
			fmt.Printf("acknowledged %d\n", shown)
		}
	})
}

// conversation is yardr mail --with: the mail between who acts and the
// other party, oldest first.
func (a *app) conversation(ctx context.Context, st store.API, with string, after uint64) error {
	if strings.Contains(with, "@") {
		return fmt.Errorf("--with %s: a conversation is of this yard's mail; mail to and from another yard is in yardr inbox and yardr peer show", with)
	}
	// The other has to be a party of the yard, or a typo reads as a
	// conversation nobody has had yet.
	if _, err := st.GetResident(ctx, with); errors.Is(err, store.ErrNotFound) {
		if _, err := st.GetUser(ctx, with); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return fmt.Errorf("--with %s: no resident and no user of that name: %w", with, store.ErrNotFound)
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
			if l.Read {
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
// resident that has the operator's name, and members of groups that are
// neither a user nor a resident.
func partyNotes(ctx context.Context, st store.API, residents []store.Resident, groups []store.Group, yardr string) ([]string, error) {
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	users, err := st.Users(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, u := range users {
		known[u.Name] = true
	}
	notes := []string{}
	for _, r := range residents {
		known[r.Name] = true
		if r.Name == all[store.OperatorSetting] {
			notes = append(notes, fmt.Sprintf("resident %s has the operator's name, so the operator has no user and no inbox: mail to %s is the resident's. "+
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
			notes = append(notes, fmt.Sprintf("group %s: %s%s neither a user nor a resident, so nobody of that name can be mailed (%s user add <name>)",
				g.Name, strings.Join(unknown, ", "), isAre(unknown), yardr))
		}
	}
	return notes, nil
}

// unreadLine prints what waits for the acting user under bare yardr's
// help. It is a courtesy: a store it cannot read says nothing here.
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
	fmt.Printf("\nMail for %s: %s (yardr inbox).\n", m.User, m.unread())
}
