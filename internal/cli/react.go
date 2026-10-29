package cli

import (
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"
)

func (a *app) reactCmd() *cobra.Command {
	var clear bool
	cmd := &cobra.Command{
		Use:   "react <seq> <emoji>",
		Short: "Put an emoji reaction on mail, or take yours away (--clear)",
		Long: `Puts an emoji reaction on the mail <seq>, the number a conversation
or inbox line starts with: one of 👍 👎 ❤️ 🎉 👀 ✅. Only the letter's
sender or receiver may react, and only mail its sender wrote takes one:
a timer's letter is refused. One party holds one reaction: a new one
replaces the old one.

--clear takes your reaction away instead of putting one on.

A reaction shows under the letter wherever it is read (yardr mail --with,
yardr inbox, yardr chat). A reaction to a crew member's own mail lands in
that crew member's inbox; nothing reaches a user's inbox. A 👍 counts as a
"yes" mail would, no more: decisions with consequences stay asks.

Reacting in the crew member's own session marks the mail handled through
its seq when nothing older is unread; with older unread, mark it by hand
(yardr inbox --ack <seq>).`,
		Example: `  yardr react 4211 👍
  yardr react 4211 --clear`,
		Args: func(c *cobra.Command, args []string) error {
			if clear {
				return cobra.ExactArgs(1)(c, args)
			}
			return cobra.ExactArgs(2)(c, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			seq, err := strconv.ParseUint(args[0], 10, 64)
			if err != nil || seq == 0 {
				return fmt.Errorf("event seq %q: want the number a conversation or inbox line starts with", args[0])
			}
			emoji := ""
			if !clear {
				emoji = args[1]
			}
			if err := st.React(ctx, seq, emoji, a.who()); err != nil {
				return err
			}
			// In a crew member's own session the answered mail is marked
			// handled with it, as a reply marks it; anywhere else (a user
			// reacting to their own mail) there is no crew cursor to move.
			if crew := a.yardrenv("CREW"); crew != "" {
				if err := a.ackAnsweredMail(ctx, st, crew, seq); err != nil {
					return err
				}
			}
			return a.out(c.OutOrStdout(), map[string]any{"seq": seq, "emoji": emoji}, func(w io.Writer) {
				if clear {
					fmt.Fprintf(w, "cleared reaction on %d\n", seq)
				} else {
					fmt.Fprintf(w, "reacted %s to %d\n", emoji, seq)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&clear, "clear", false, "take your reaction away instead of putting one on")
	return cmd
}
