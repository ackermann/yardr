package cli

import (
	"errors"
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
the assignee or the delegate (the agent a Linear issue is handed to) is its
--id, and never hears of deliveries it is the actor of.

In Linear, --id is the user id and --key the profile URL key
(https://linear.app/<org>/profiles/<key>): markdown mentions carry only the key.

In GitHub, --key is the login, in any case. A resident that speaks as a
GitHub App has the App's bot as its login, <slug>[bot]: what the bot does
is then its own, and @<slug> in a comment mentions it.`,
		Example: `  yardr resident identity mayor --source linear --id 4d3a2b1c-... --key mayor
  yardr resident identity mayor --source github --key 'yardr[bot]'
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
				case source == "github":
					// A login is the same in any case, and deliveries carry
					// it in lowercase: Yardr[bot] is kept as yardr[bot].
					id.Key = strings.ToLower(id.Key)
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

// subSource is the source a subscription command names: its second
// argument or --source (not both), a hook source or yardr.
func subSource(args []string, flag string) (string, error) {
	src := flag
	if len(args) == 2 {
		if flag != "" && flag != args[1] {
			return "", fmt.Errorf("source given twice: %s and --source %s", args[1], flag)
		}
		src = args[1]
	}
	if src == "" {
		return "", fmt.Errorf("name a source: %s or %s", strings.Join(hooks.Sources(), ", "), store.SourceYardr)
	}
	if src == store.SourceYardr {
		return src, nil
	}
	return src, checkSource(src)
}

func (a *app) subscribeCmd() *cobra.Command {
	var sub store.Subscription
	var where []string
	var done bool
	cmd := &cobra.Command{
		Use:   "subscribe <resident|group> [source]",
		Short: "Deliver a source's events to a resident's inbox",
		Long: `Which events reach the resident, decided as each is logged: hook deliveries
from a hook source (` + strings.Join(hooks.Sources(), ", ") + `), or yardr's own bead events from
the yardr source.

  all       every event from the source
  mentions  events that mention it: in a hook source, under its identity
            (yardr resident identity); in yardr, @<name> in the bead's body
            or a note
  involved  mentions, plus: in a hook source, events on what is assigned
            or (a Linear issue's delegate) delegated to it, or in a thread
            it participates in (it acted there, or yardr thread follow);
            in yardr, beads it created, advanced or noted

The scope defaults to involved for a hook source and to all for yardr.

--where narrows a subscription by exact matches, all of which must hold.
Hook sources: ` + strings.Join(store.WhereKeys, ", ") + `. Linear exposes team
(key or name), project and label (any of the issue's labels). GitHub exposes
repo (owner/name) and label, and also ` + strings.Join(store.GitHubWhereKeys, ", ") + `
(epic: the yardr epic whose yard/<epic> branch a pull request or CI run is on).
yardr: ` + strings.Join(store.YardrWhereKeys, ", ") + `:
  bead, rig, type, label  the bead's (label: any of its labels)
  kind    the event: created, advanced, noted, held, unheld, closed,
          gave_up, session_blocked, ...
  stage   the stage entered (created and advanced events); from: the stage left
  group   the group that owns the bead, or whose session the event ended
  actor   who acted
  epic    the bead, or a bead it blocks or is the parent of (direct edges)
  state   done: the bead closed, by a terminal stage of its flow or by a
          close; once per bead. --done is short for --where state=done.

The subscriber may be a manual group: its subscriptions deliver to each of
its members that is a resident (reason "<reason> via <group>").

Each delivery records why it reached the resident: all, mentioned,
assigned (a delegation reads assigned too), participant or done. Events a resident is the actor of never
reach it. A resident's --on kinds work as implicit "yardr all kind=<k>"
subscriptions (and also cover events not about a bead).`,
		Example: `  yardr subscribe mayor linear --scope involved
  yardr subscribe triage linear --scope all --where type=issue --where action=create --where team=ENG
  yardr subscribe mayor github --scope all --where epic=yard-sv6b --where conclusion=failure
  yardr subscribe mayor yardr --done
  yardr subscribe mayor yardr --done --where rig=yard --where label=ui
  yardr subscribe approvers yardr --where stage=approved`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			src, err := subSource(args, sub.Source)
			if err != nil {
				return err
			}
			sub.Source = src
			if !c.Flags().Changed("scope") && src == store.SourceYardr {
				sub.Scope = store.ScopeAll
			}
			w, err := parseWhere(where)
			if err != nil {
				return err
			}
			if done {
				if src != store.SourceYardr {
					return fmt.Errorf("--done is for the yardr source")
				}
				if st, ok := w["state"]; ok && st != store.StateDone {
					return fmt.Errorf("--done and --where state=%s disagree", st)
				}
				w["state"] = store.StateDone
			}
			sub.Where = w
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := st.AddSubscription(c.Context(), args[0], sub, a.who()); err != nil {
				return err
			}
			return a.showSubscriptions(c, st, args[0], func(subs []store.Subscription, r *store.Resident) {
				fmt.Printf("%s subscribed: %s\n", args[0], subs[len(subs)-1])
				if r == nil || src == store.SourceYardr {
					return
				}
				if _, ok := r.Identities[src]; !ok && sub.Scope != store.ScopeAll {
					fmt.Printf("note: %s has no %s identity, so it is never mentioned or assigned: yardr resident identity %s --source %s --id ...\n",
						r.Name, src, r.Name, src)
				}
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&sub.Source, "source", "", "source: "+strings.Join(append(hooks.Sources(), store.SourceYardr), ", "))
	f.StringVar(&sub.Scope, "scope", store.ScopeInvolved, "all, mentions or involved (yardr: default all)")
	f.StringArrayVar(&where, "where", nil, "key=value filter (repeatable, ANDed)")
	f.BoolVar(&done, "done", false, "yardr: only beads becoming done (--where state=done)")
	return cmd
}

// showSubscriptions prints (or outputs) the subscriptions of the resident
// or group called name; r is nil for a group.
func (a *app) showSubscriptions(c *cobra.Command, st store.API, name string, text func([]store.Subscription, *store.Resident)) error {
	r, err := st.GetResident(c.Context(), name)
	if errors.Is(err, store.ErrNotFound) {
		g, gerr := st.GetGroup(c.Context(), name)
		if gerr != nil {
			return gerr
		}
		return a.out(g.Subscriptions, func() { text(g.Subscriptions, nil) })
	}
	if err != nil {
		return err
	}
	return a.out(r.Subscriptions, func() { text(r.Subscriptions, r) })
}

func (a *app) unsubscribeCmd() *cobra.Command {
	var source, scope string
	var where []string
	cmd := &cobra.Command{
		Use:   "unsubscribe <resident|group> [source]",
		Short: "Remove subscriptions to a source",
		Long: `Removes every subscription of the resident (or manual group) to the source;
--scope and --where narrow it to the subscriptions with that scope and
exactly those filters.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			src, err := subSource(args, source)
			if err != nil {
				return err
			}
			w, err := parseWhere(where)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			n, err := st.RemoveSubscriptions(c.Context(), args[0], src, scope, w, a.who())
			if err != nil {
				return err
			}
			return a.out(map[string]any{"subscriber": args[0], "removed": n}, func() {
				fmt.Printf("%s: %d subscription(s) removed\n", args[0], n)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&source, "source", "", "source")
	f.StringVar(&scope, "scope", "", "only subscriptions of this scope")
	f.StringArrayVar(&where, "where", nil, "only subscriptions with exactly these filters")
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
	fmt.Printf("%6d %s  hook (%s) %s", e.Seq, e.At.Local().Format("01-02 15:04"), or(store.Reason(e, resident), "-"), what)
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
		Short: "Threads a resident participates in, and what is said in them",
		Long: `A resident participates in a source's thread (in Linear, an issue) once a hook
delivery shows it acting there under its identity, once it answers there
with yardr reply, opens it with yardr thread open, or after yardr thread
follow. An involved subscription
then also delivers what others do in the thread, with reason participant.
Unfollowing stops that until the resident acts in the thread again.

yardr thread show <seq> reads the source thread of a hook delivery, and
yardr thread state sets the status of a thread the resident takes part in.`,
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
	cmd.AddCommand(follow, unfollow, list, a.threadShowCmd(), a.threadOpenCmd(), a.threadStateCmd())
	return cmd
}

// printBeadDelivery shows a bead event in resident's inbox, with why it
// is there: a subscription's reason, or "on" for a --on kind.
func printBeadDelivery(e store.Event, resident string) {
	data := map[string]any{}
	for k, v := range e.Data {
		if k != "deliveries" && k != "watches_ended" {
			data[k] = v
		}
	}
	extra := ""
	if len(data) > 0 {
		extra = fmtConfig(data)
	}
	fmt.Printf("%6d %s  %-8s %s (%s) by %s %s\n", e.Seq, e.At.Local().Format("01-02 15:04"), deref(e.Bead), e.Kind,
		or(store.Reason(e, resident), "on"), e.Actor, extra)
}

// watchCmds are yardr bead watch and unwatch: sugar for yardr subscriptions
// with bead=<id>, which end when the bead closes.
func (a *app) watchCmds() []*cobra.Command {
	var resident, stage, kind string
	var done bool
	watch := &cobra.Command{
		Use:   "watch <id>",
		Short: "Deliver a bead's events to a resident's inbox until it closes",
		Long: `Subscribes the resident (or manual group) to the bead's events in yardr:
yardr subscribe <resident> yardr --where bead=<id>, narrowed by --stage (the
stage it enters), --kind and --done (it closes, by a terminal stage or a
close). The watch ends when the bead closes, after the close is delivered.`,
		Example: `  yardr bead watch yard-k3f9 --done
  yardr bead watch yard-k3f9 --resident mayor --stage review`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name, err := currentResident(resident)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			b, err := st.GetBead(c.Context(), args[0])
			if err != nil {
				return err
			}
			sub := store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"bead": b.ID}}
			for k, v := range map[string]string{"stage": stage, "kind": kind} {
				if v != "" {
					sub.Where[k] = v
				}
			}
			if done {
				sub.Where["state"] = store.StateDone
			}
			if err := st.AddSubscription(c.Context(), name, sub, a.who()); err != nil {
				return err
			}
			return a.out(sub, func() { fmt.Printf("%s watches %s: %s\n", name, b.ID, sub) })
		},
	}
	f := watch.Flags()
	f.StringVar(&resident, "resident", "", "resident or manual group (default $YARDR_RESIDENT)")
	f.StringVar(&stage, "stage", "", "only when it enters this stage")
	f.StringVar(&kind, "kind", "", "only events of this kind (advanced, noted, closed, ...)")
	f.BoolVar(&done, "done", false, "only when it is done")

	var unResident string
	unwatch := &cobra.Command{
		Use:   "unwatch <id>",
		Short: "Stop delivering a bead's events to a resident",
		Long:  "Removes every yardr subscription of the resident (or manual group) for the bead.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name, err := currentResident(unResident)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			n, err := st.Unwatch(c.Context(), name, args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(map[string]any{"subscriber": name, "bead": args[0], "removed": n}, func() {
				fmt.Printf("%s: %d watch(es) on %s removed\n", name, n, args[0])
			})
		},
	}
	unwatch.Flags().StringVar(&unResident, "resident", "", "resident or manual group (default $YARDR_RESIDENT)")
	return []*cobra.Command{watch, unwatch}
}
