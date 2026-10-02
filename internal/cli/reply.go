package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// hookEvent is the hook event seq, as an envelope, and the Replier of its
// source. Only the event is asked of the server: the call to the source is
// made from here, with this process's credential.
func hookEvent(ctx context.Context, st store.API, arg string) (uint64, hooks.Envelope, hooks.Replier, error) {
	var env hooks.Envelope
	seq, err := strconv.ParseUint(arg, 10, 64)
	if err != nil || seq == 0 {
		return 0, env, nil, fmt.Errorf("event seq %q: want the number an inbox line starts with", arg)
	}
	evs, err := st.EventsSince(ctx, seq-1, 1)
	if err != nil {
		return 0, env, nil, err
	}
	if len(evs) == 0 || evs[0].Seq != seq {
		return 0, env, nil, fmt.Errorf("event %d: %w", seq, store.ErrNotFound)
	}
	e := evs[0]
	if e.Kind != "hook" {
		return 0, env, nil, fmt.Errorf("event %d is a %s event, not a hook delivery: only what a source delivered can be answered in it", seq, e.Kind)
	}
	raw, err := json.Marshal(e.Data)
	if err == nil {
		err = json.Unmarshal(raw, &env)
	}
	if err != nil {
		return 0, env, nil, fmt.Errorf("event %d: %w", seq, err)
	}
	r, ok := hooks.ReplierFor(env.Source)
	if !ok {
		return 0, env, nil, fmt.Errorf("event %d came from %s, which yardr cannot answer in (it can in %s)",
			seq, or(env.Source, "an unknown source"), strings.Join(hooks.Repliable(), ", "))
	}
	return seq, env, r, nil
}

func (a *app) replyCmd() *cobra.Command {
	var resident, to string
	cmd := &cobra.Command{
		Use:   "reply <seq> <text|->",
		Short: "Answer a hook delivery in its source, as yourself",
		Long: `Posts text (- reads it from stdin) in the thread of hook event <seq>, the
number an inbox line starts with: in Linear, under the conversation the
delivered comment belongs to (a new comment for an issue delivery); in
GitHub, as a comment on the issue or pull request. --to <message-id>
answers under that message's conversation instead (Linear; ids from
yardr thread show).

The call is made from this process with your own credential, from your
environment: LINEAR_API_KEY, GITHUB_TOKEN (else gh auth token). A resident's
env_file sets them; the server never sees them. yardr only types: what is
said, and whether to say it, is the resident's.

After posting, the resident (--resident, default $YARDR_RESIDENT) takes
part in the thread at once (yardr thread list shows it, how=replied) and a
replied event is logged. Prints the URL of the reply.`,
		Example: `  yardr reply 4211 "Filed as yard-k3f9; I'll post here when it lands."
  yardr reply 4211 --to 2174add1-f7c8-44e3-bbf3-2d60b5ea8bc9 - < answer.md`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			name, err := currentResident(resident)
			if err != nil {
				return err
			}
			body := args[1]
			if body == "-" {
				b, err := io.ReadAll(c.InOrStdin())
				if err != nil {
					return err
				}
				body = string(b)
			}
			body = strings.TrimSpace(body)
			if body == "" {
				return fmt.Errorf("nothing to say: the reply is empty")
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if _, err := st.GetResident(ctx, name); err != nil {
				return err
			}
			seq, env, r, err := hookEvent(ctx, st, args[0])
			if err != nil {
				return err
			}
			cred, err := r.Credential(ctx)
			if err != nil {
				return err
			}
			url, err := r.Reply(ctx, env, to, body, cred)
			if err != nil {
				return fmt.Errorf("reply to event %d in %s: %w", seq, env.Source, err)
			}
			rp := store.Reply{Source: env.Source, Thread: env.Thread.ID, Title: env.Thread.Title, ThreadURL: env.Thread.URL, URL: url}
			if err := st.Replied(ctx, name, rp, name); err != nil {
				return fmt.Errorf("posted %s, but could not record it: %w", url, err)
			}
			return a.out(map[string]any{"seq": seq, "resident": name, "source": env.Source, "thread": env.Thread.ID, "url": url},
				func() { fmt.Println(url) })
		},
	}
	cmd.Flags().StringVar(&resident, "resident", "", "who is replying (default $YARDR_RESIDENT)")
	cmd.Flags().StringVar(&to, "to", "", "answer under this message's conversation (an id from yardr thread show)")
	return cmd
}

func (a *app) threadShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <seq>",
		Short: "Print the source thread of a hook delivery",
		Long: `Reads the thread of hook event <seq> from its source, with your own
credential (as yardr reply does), and prints every message in order: full
id, the message it answers (in Linear, its conversation's root), author,
time and body. The delivered message is marked with *.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			seq, env, r, err := hookEvent(ctx, st, args[0])
			if err != nil {
				return err
			}
			cred, err := r.Credential(ctx)
			if err != nil {
				return err
			}
			ms, err := r.Thread(ctx, env, cred)
			if err != nil {
				return fmt.Errorf("thread of event %d in %s: %w", seq, env.Source, err)
			}
			if ms == nil {
				ms = []hooks.Message{}
			}
			return a.out(map[string]any{"seq": seq, "source": env.Source, "thread": env.Thread, "message": env.Message, "messages": ms}, func() {
				fmt.Printf("%s thread %s", env.Source, env.Thread.ID)
				if env.Thread.Title != "" {
					fmt.Printf(" %q", env.Thread.Title)
				}
				if env.Thread.URL != "" {
					fmt.Printf("  %s", env.Thread.URL)
				}
				fmt.Printf("\n%d message(s)\n", len(ms))
				for _, m := range ms {
					mark := " "
					if m.ID == env.Message {
						mark = "*"
					}
					fmt.Printf("\n%s %s  %s  %s", mark, m.ID, m.At.Local().Format("2006-01-02 15:04"), or(m.Author.Name, "-"))
					if m.Author.ID != "" && m.Author.ID != m.Author.Name {
						fmt.Printf(" (%s)", m.Author.ID)
					}
					if m.Parent != "" {
						fmt.Printf("  reply to %s", m.Parent)
					}
					fmt.Println()
					for _, line := range strings.Split(strings.TrimRight(m.Body, "\n"), "\n") {
						fmt.Println("    " + line)
					}
				}
			})
		},
	}
}
