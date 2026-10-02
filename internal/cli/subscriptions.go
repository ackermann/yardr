package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

func checkSource(src string) error {
	if _, ok := hooks.Adapters[src]; !ok {
		return fmt.Errorf("--source %q: one of %s", src, strings.Join(hooks.Sources(), ", "))
	}
	return nil
}

func (a *app) identityCmd() *cobra.Command {
	var source string
	var id store.Identity
	var rm bool
	cmd := &cobra.Command{
		Use:   "identity <resident>",
		Short: "Who a resident is in a source (no --source: list)",
		Long: `A resident's identity in a source is how a hook delivery is matched to it:
it is mentioned when the delivery mentions its --id or --key, assigned when
the assignee is its --id, and never hears of deliveries it is the actor of.

In Linear, --id is the user id and --key the profile URL key
(https://linear.app/<org>/profiles/<key>): markdown mentions carry only the key.`,
		Example: `  yardr resident identity mayor --source linear --id 4d3a2b1c-... --key mayor
  yardr resident identity mayor
  yardr resident identity mayor --source linear --rm`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			name := args[0]
			if source != "" {
				if err := checkSource(source); err != nil {
					return err
				}
				switch {
				case rm && (id.ID != "" || id.Key != ""):
					return fmt.Errorf("--rm takes no --id or --key")
				case !rm && id.ID == "" && id.Key == "":
					return fmt.Errorf("give --id and/or --key, or --rm")
				case rm:
					id = store.Identity{}
				}
				if err := st.SetIdentity(c.Context(), name, source, id, a.who()); err != nil {
					return err
				}
			} else if rm || id.ID != "" || id.Key != "" {
				return fmt.Errorf("--source is required")
			}
			r, err := st.GetResident(c.Context(), name)
			if err != nil {
				return err
			}
			return a.out(r.Identities, func() {
				rows := [][]string{}
				for _, src := range sortedKeys(r.Identities) {
					rows = append(rows, []string{src, or(r.Identities[src].ID, "-"), or(r.Identities[src].Key, "-")})
				}
				table("SOURCE\tID\tKEY", rows)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&source, "source", "", "source: "+strings.Join(hooks.Sources(), ", "))
	f.StringVar(&id.ID, "id", "", "the resident's user id in the source")
	f.StringVar(&id.Key, "key", "", "another name the source mentions it by (Linear: profile key)")
	f.BoolVar(&rm, "rm", false, "remove the identity for --source")
	return cmd
}

// parseWhere reads repeated --where key=value.
func parseWhere(ws []string) (map[string]string, error) {
	m := map[string]string{}
	for _, kv := range ws {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("--where %q: want key=value", kv)
		}
		if _, dup := m[k]; dup {
			return nil, fmt.Errorf("--where %s given twice: filters AND, so one value per key", k)
		}
		m[k] = v
	}
	return m, nil
}

func (a *app) subscribeCmd() *cobra.Command {
	var sub store.Subscription
	var where []string
	cmd := &cobra.Command{
		Use:   "subscribe <resident>",
		Short: "Deliver a source's hook events to a resident's inbox",
		Long: `Which hook deliveries reach the resident, decided when each arrives:
  all       every delivery from the source
  mentions  deliveries that mention it (needs yardr resident identity)
  involved  deliveries that mention it or assign it, or are in a thread it
            participates in (it acted there, or yardr thread follow)

--where narrows a subscription by exact matches, all of which must hold, on
` + strings.Join(store.WhereKeys, ", ") + `. Linear exposes team (key or name),
project and label (any of the issue's labels). Each delivery records why it
reached the resident: all, mentioned, assigned or participant. Deliveries a resident is
the actor of never reach it.`,
		Example: `  yardr subscribe mayor --source linear --scope involved
  yardr subscribe triage --source linear --scope all --where type=issue --where action=create --where team=ENG`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if err := checkSource(sub.Source); err != nil {
				return err
			}
			w, err := parseWhere(where)
			if err != nil {
				return err
			}
			sub.Where = w
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.AddSubscription(c.Context(), args[0], sub, a.who()); err != nil {
				return err
			}
			r, err := st.GetResident(c.Context(), args[0])
			if err != nil {
				return err
			}
			return a.out(r.Subscriptions, func() {
				fmt.Printf("%s subscribed: %s\n", r.Name, sub)
				if _, ok := r.Identities[sub.Source]; !ok && sub.Scope != store.ScopeAll {
					fmt.Printf("note: %s has no %s identity, so it is never mentioned or assigned: yardr resident identity %s --source %s --id ...\n",
						r.Name, sub.Source, r.Name, sub.Source)
				}
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&sub.Source, "source", "", "source: "+strings.Join(hooks.Sources(), ", "))
	f.StringVar(&sub.Scope, "scope", store.ScopeInvolved, "all, mentions or involved")
	f.StringArrayVar(&where, "where", nil, "key=value filter (repeatable, ANDed): "+strings.Join(store.WhereKeys, ", "))
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

func (a *app) unsubscribeCmd() *cobra.Command {
	var source, scope string
	var where []string
	cmd := &cobra.Command{
		Use:   "unsubscribe <resident>",
		Short: "Remove a resident's subscriptions to a source",
		Long: `Removes every subscription of the resident to --source; --scope and --where
narrow it to the subscriptions with that scope and exactly those filters.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			w, err := parseWhere(where)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			n, err := st.RemoveSubscriptions(c.Context(), args[0], source, scope, w, a.who())
			if err != nil {
				return err
			}
			return a.out(map[string]any{"resident": args[0], "removed": n}, func() {
				fmt.Printf("%s: %d subscription(s) removed\n", args[0], n)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&source, "source", "", "source")
	f.StringVar(&scope, "scope", "", "only subscriptions of this scope")
	f.StringArrayVar(&where, "where", nil, "only subscriptions with exactly these filters")
	_ = cmd.MarkFlagRequired("source")
	return cmd
}

// printHookDelivery shows a hook event as it reached resident: what
// happened, who did it, and why the resident hears of it.
func printHookDelivery(e store.Event, resident string) {
	str := func(k string) string { s, _ := e.Data[k].(string); return s }
	actor, _ := e.Data["actor"].(map[string]any)
	who, _ := actor["name"].(string)
	if who == "" {
		who, _ = actor["id"].(string)
	}
	what := strings.Join(slices.DeleteFunc([]string{str("source"), str("type"), str("action")},
		func(s string) bool { return s == "" }), " ")
	if who != "" {
		what += " by " + who
	}
	thread, _ := e.Data["thread"].(map[string]any)
	title, _ := thread["title"].(string)
	if title != "" {
		what += " on " + fmt.Sprintf("%q", title)
	}
	fmt.Printf("%6d %s  hook (%s) %s", e.Seq, e.At.Local().Format("01-02 15:04"), or(store.HookReason(e, resident), "-"), what)
	if t := str("text"); t != "" {
		fmt.Printf(": %s", t)
	}
	if u := str("url"); u != "" {
		fmt.Printf("  %s", u)
	}
	fmt.Println()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func (a *app) threadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "thread",
		Short: "Threads a resident participates in",
		Long: `A resident participates in a source's thread (in Linear, an issue) once a hook
delivery shows it acting there under its identity, or after yardr thread
follow. An involved subscription then also delivers what others do in the
thread, with reason participant. Unfollowing stops that until the resident
acts in the thread again.`,
	}
	threadArgs := func(source *string) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(2)(c, args); err != nil {
				return err
			}
			if *source == "" {
				return fmt.Errorf("--source is required")
			}
			return checkSource(*source)
		}
	}
	var source string
	follow := &cobra.Command{
		Use:     "follow <resident> <thread-id>",
		Short:   "Make a resident a participant in a thread",
		Example: `  yardr thread follow mayor --source linear 539068e2-ae88-4d09-bd75-22eb4a59612f`,
		Args:    threadArgs(&source),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.FollowThread(c.Context(), args[0], source, args[1], a.who()); err != nil {
				return err
			}
			return a.out(map[string]any{"resident": args[0], "source": source, "thread": args[1]}, func() {
				fmt.Printf("%s follows %s thread %s\n", args[0], source, args[1])
			})
		},
	}
	unfollow := &cobra.Command{
		Use:   "unfollow <resident> <thread-id>",
		Short: "End a resident's participation in a thread",
		Args:  threadArgs(&source),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.UnfollowThread(c.Context(), args[0], source, args[1], a.who()); err != nil {
				return err
			}
			return a.out(map[string]any{"resident": args[0], "source": source, "thread": args[1]}, func() {
				fmt.Printf("%s unfollowed %s thread %s\n", args[0], source, args[1])
			})
		},
	}
	for _, c := range []*cobra.Command{follow, unfollow} {
		c.Flags().StringVar(&source, "source", "", "source: "+strings.Join(hooks.Sources(), ", "))
		_ = c.MarkFlagRequired("source")
	}
	list := &cobra.Command{
		Use:   "list <resident>",
		Short: "List the threads a resident participates in",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			ps, err := st.Threads(c.Context(), args[0])
			if err != nil {
				return err
			}
			return a.out(ps, func() {
				rows := [][]string{}
				for _, p := range ps {
					rows = append(rows, []string{p.Source, p.Thread, p.How, p.Since.Local().Format("01-02 15:04"),
						or(p.Title, "-")})
				}
				table("SOURCE\tTHREAD\tHOW\tSINCE\tTITLE", rows)
			})
		},
	}
	cmd.AddCommand(follow, unfollow, list)
	return cmd
}
