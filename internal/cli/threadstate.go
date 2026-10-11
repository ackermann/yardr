package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// stateThread is the source and id of the thread arg names for crew member
// name. With --source it is the thread's id, as yardr thread follow takes
// it. Alone, a number is the seq of a hook delivery, as yardr reply takes
// it, and anything else the id of a thread the crew member takes part in,
// whose source its participation gives.
func stateThread(ctx context.Context, st store.API, home, name, source, arg string) (string, string, error) {
	if source != "" {
		return source, arg, checkSource(ctx, st, source)
	}
	if _, err := strconv.ParseUint(arg, 10, 64); err == nil {
		_, env, _, err := hookEvent(ctx, st, home, arg)
		if err != nil {
			return "", "", err
		}
		if env.Thread.ID == "" {
			return "", "", fmt.Errorf("event %s names no thread", arg)
		}
		return env.Source, env.Thread.ID, nil
	}
	ps, err := st.Threads(ctx, name)
	if err != nil {
		return "", "", err
	}
	var in []string
	for _, p := range ps {
		if p.Thread == arg {
			in = append(in, p.Source)
		}
	}
	switch len(in) {
	case 1:
		return in[0], arg, nil
	case 0:
		return "", "", fmt.Errorf("crew member %s takes no part in a thread %s, and sets the status only of a thread it takes part in (yardr thread follow %s --source <source> %s makes it a participant; yardr thread list %s shows its threads)",
			name, arg, name, arg, name)
	}
	return "", "", fmt.Errorf("crew member %s takes part in a thread %s in %s: say which with --source", name, arg, strings.Join(in, " and "))
}

func (a *app) threadStateCmd() *cobra.Command {
	var member, source string
	cmd := &cobra.Command{
		Use:   "state <thread|seq> " + strings.Join(hooks.States(), "|") + "|label:<name>",
		Short: "Set the status of a thread you take part in, as yourself",
		Long: `Moves the issue a thread is to started, review or done. The thread is named
by its id, as yardr thread follow names one (--source says where; it may be
left out when the crew member takes part in one thread with that id), or by
the seq of a hook delivery from it, the number an inbox line starts with.

The three words are yardr's, and each source has its own statuses:

  linear   started: the team's first status of type started. done: its
           first of type completed. review: its first of type started whose
           name contains "review"; a team without one gets started, and the
           output says so. First is lowest position, as the team orders them.
  github   done closes the issue as completed. started reopens a closed
           issue. review is refused: an issue is open or closed. So is a
           pull request, which this would close unmerged. label:<name>
           adds the label, to an issue or a pull request, and is never
           removed: a label is the status GitHub has for anything between.

A thread already in the status asked for is left alone, and the output says
it was there.

The rules are those of yardr reply. The call is made from this process with
your own credential, from your environment (LINEAR_API_KEY; for GitHub a
GitHub App's GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE, else
GITHUB_TOKEN, else, outside a crew member's session, gh auth token), and the
server never sees it. Only the crew member's own
session or the operator may speak as the crew member (--crew, default
$YARDR_CREW).

A crew member may set a status only in the sources its definition allows:
yardr crew set <crew member> set_state=linear,github, which only the
operator may add a source to. It is off unless set. And only in a thread it takes
part in (yardr thread list): yardr thread follow makes it a participant.

A thread_state event is logged with the word asked and the status set. The
source's delivery of the change does not reach the crew member's own inbox,
as none of what it does there does: that takes its identity in the source
(yardr crew identity), and the command says when it has none.`,
		Example: `  yardr thread state 4211 started
  yardr thread state 539068e2-ae88-4d09-bd75-22eb4a59612f review
  yardr thread state --source github acme/webapp#12 done
  yardr thread state --source github acme/webapp#12 label:in-review`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			name, err := currentCrew(member)
			if err != nil {
				return err
			}
			// A label keeps its name as typed: the source spells it so.
			state := strings.TrimSpace(args[1])
			if _, label := hooks.Label(state); !label {
				state = strings.ToLower(state)
			}
			if !hooks.Word(state) {
				return fmt.Errorf("%q is no state: want %s", args[1], strings.Join(hooks.States(), ", "))
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			r, err := st.GetCrew(ctx, name)
			if err != nil {
				return err
			}
			source, thread, err := stateThread(ctx, st, a.home(), name, source, strings.TrimSpace(args[0]))
			if err != nil {
				return err
			}
			d, src, err := declaredSource(ctx, st, source)
			if err != nil {
				return err
			}
			s, ok := hooks.StaterFor(source, d, a.home(), mintOf(src))
			if !ok {
				return fmt.Errorf("yardr cannot set the status of a thread in %s (it can in %s)%s", source, strings.Join(hooks.Stateable(allDeclarations(ctx, st)...), ", "), packHint(ctx, st, source))
			}
			// The store is asked before the source is: it is where a
			// crew member that may not, or takes no part in the thread, is
			// refused.
			p, err := st.SettingState(ctx, name, source, thread, a.who())
			if err != nil {
				return err
			}
			cred, err := s.Credential(ctx)
			if err != nil {
				return err
			}
			res, err := s.SetState(ctx, thread, state, cred)
			if err != nil {
				return fmt.Errorf("set %s thread %s to %s: %w", source, thread, state, err)
			}
			ts := store.ThreadState{Source: source, Thread: thread, Title: or(res.Thread.Title, p.Title), URL: or(res.Thread.URL, p.URL),
				State: state, Status: res.Status, Instead: res.Instead}
			if res.Changed {
				ts.Was = res.Was
				if err := st.StateSet(ctx, name, ts, a.who()); err != nil {
					return fmt.Errorf("%s thread %s is now %s, but could not record it: %w", source, thread, res.Status, err)
				}
			}
			out := map[string]any{"crew": name, "source": source, "thread": thread, "state": state, "status": res.Status,
				"actor": a.who(), "changed": res.Changed}
			for k, v := range map[string]string{"title": ts.Title, "url": ts.URL, "was": ts.Was, "instead": ts.Instead} {
				if v != "" {
					out[k] = v
				}
			}
			return a.out(out, func() {
				if res.Instead != "" {
					fmt.Printf("%s has no %s status here: %s instead\n", source, state, res.Instead)
				}
				if !res.Changed {
					fmt.Printf("%s thread %s was already %s; nothing set\n", source, thread, res.Status)
					return
				}
				// A command that does not say what the thread was in, or
				// a label, has no "(was …)" rather than "(was -)".
				if res.Was == "" {
					fmt.Printf("%s thread %s is now %s\n", source, thread, res.Status)
				} else {
					fmt.Printf("%s thread %s is now %s (was %s)\n", source, thread, res.Status, res.Was)
				}
				// Its own action is kept from its inbox by its identity in
				// the source (hookDeliveries); without one the delivery of
				// this change is someone else's as far as yardr can tell.
				if id, known := r.Identities[source]; !known || id == (store.Identity{}) {
					fmt.Fprintf(c.ErrOrStderr(), "%s has no %s identity, so the delivery of this change may reach its own inbox: yardr crew identity %s --source %s --id <its id there>\n",
						name, source, name, source)
				}
			})
		},
	}
	cmd.Flags().StringVar(&member, "crew", "", "who is setting it (default $YARDR_CREW)")
	cmd.Flags().StringVar(&source, "source", "", "the thread's source, with a thread id: "+strings.Join(hooks.Stateable(), ", "))
	return cmd
}
