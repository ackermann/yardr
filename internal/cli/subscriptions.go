package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

func checkSource(ctx context.Context, st store.API, src string) error {
	if _, ok := hooks.Adapters[src]; ok {
		return nil
	}
	d, _, err := declaredSource(ctx, st, src)
	if err != nil {
		return err
	}
	if _, ok := hooks.AdapterFor(src, d, ""); ok {
		return nil
	}
	if d != nil {
		return fmt.Errorf("--source %q is a declared source without a hook key, so the yard hears nothing from it", src)
	}
	return fmt.Errorf("--source %q: one of %s, or a declared source with a hook key%s", src, strings.Join(hooks.Sources(), ", "), packHint(ctx, st, src))
}

// threadSource is checkSource for a source a crew member follows a thread
// in: a hook source, or peer, whose threads are this yard's beads at the
// yards it is linked with and which no hook receives.
func threadSource(ctx context.Context, st store.API, src string) error {
	if src == store.SourcePeer {
		return nil
	}
	return checkSource(ctx, st, src)
}

// packHint is "; yardr pack apply <source> brings it" when the binary
// carries a pack of that name and the yard declares no source of that
// name, else "".
func packHint(ctx context.Context, st store.API, source string) string {
	if !packs.Carries(source) {
		return ""
	}
	if _, err := st.GetSource(ctx, source); err == nil {
		return ""
	}
	return "; yardr pack apply " + source + " brings it"
}

// declaredSource is the yard's declared source src, nil when the yard
// declares none under that name.
// declaredSource is the yard's declared source of that name and its record,
// nil when the yard declares none.
func declaredSource(ctx context.Context, st store.API, name string) (*source.Declaration, *store.Source, error) {
	s, err := st.GetSource(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	d, err := s.Declaration()
	if err != nil {
		return nil, nil, fmt.Errorf("source %s: its declaration is not read by this yardr: %w", name, err)
	}
	return d, s, nil
}

// mintOf is the source's mint_command setting, "" when the yard declares
// no such source or set none.
func mintOf(src *store.Source) string {
	if src == nil {
		return ""
	}
	return src.MintCommand
}

// allDeclarations lists the yard's declared sources, skipping ones no
// yardr reads.
func allDeclarations(ctx context.Context, st store.API) []*source.Declaration {
	srcs, err := st.Sources(ctx)
	if err != nil {
		return nil
	}
	var out []*source.Declaration
	for i := range srcs {
		if d, err := srcs[i].Declaration(); err == nil {
			out = append(out, d)
		}
	}
	return out
}

func (a *app) identityCmd() *cobra.Command {
	return a.identityOf("crew member", `A crew member's identity in a source is how a hook delivery is matched to it:
it is mentioned when the delivery mentions its --id or --key, assigned when
the assignee or the delegate (the agent a Linear issue is handed to) is its
--id, and never hears of deliveries it is the actor of.

In Linear, --id is the user id and --key the profile URL key
(https://linear.app/<org>/profiles/<key>): markdown mentions carry only the key.

In GitHub, --key is the login, in any case. A crew member that speaks as a
GitHub App has the App's bot as its login, <slug>[bot]: what the bot does
is then its own, and @<slug> in a comment mentions it.`,
		`  yardr crew identity yardmaster --source linear --id 4d3a2b1c-... --key yardmaster
  yardr crew identity yardmaster --source github --key 'yardr[bot]'
  yardr crew identity yardmaster
  yardr crew identity yardmaster --source linear --rm`,
		func(ctx context.Context, st store.API, name string) (map[string]store.Identity, error) {
			r, err := st.GetCrew(ctx, name)
			if err != nil {
				return nil, err
			}
			return r.Identities, nil
		})
}

// userIdentityCmd is identityCmd for a user: the same record, read for
// another end.
func (a *app) userIdentityCmd() *cobra.Command {
	return a.identityOf("user", `A user's identity in a source is whom a thread opened there for them is
assigned to: yardr thread open <source> --assign <user> hands the source
the part of it the source's issues name an assignee by, and refuses for a
user who has none there. Only the user and the operator set or remove it.

In a source that hears by identity (Linear, Slack) it also names who
spoke: a delivery from the id is kept with the user as its speaker, one
id naming one user, and a subscription may filter speaker=<user>.

In Linear that part is --id, the user id; --key is the profile URL key
(https://linear.app/<org>/profiles/<key>). In GitHub it is --key, the login,
in any case.`,
		`  yardr user identity jo --source linear --id 4d3a2b1c-... --key jo
  yardr user identity jo --source github --key jo-acme
  yardr user identity jo
  yardr user identity jo --source linear --rm`,
		func(ctx context.Context, st store.API, name string) (map[string]store.Identity, error) {
			u, err := st.GetUser(ctx, name)
			if err != nil {
				return nil, err
			}
			return u.Identities, nil
		})
}

// identityOf is the identity command of a kind of party, a crew member
// or a user: one record (store.SetIdentity), of whichever the name is.
// of reads the identities of the party of that kind called name, and
// refuses a name that is none, so neither command sets the other's.
func (a *app) identityOf(kind, long, example string, of func(context.Context, store.API, string) (map[string]store.Identity, error)) *cobra.Command {
	var source string
	var id store.Identity
	var rm bool
	cmd := &cobra.Command{
		Use:     "identity <" + kind + ">",
		Short:   "Who a " + kind + " is in a source (no --source: list)",
		Long:    long,
		Example: example,
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			name := args[0]
			if _, err := of(c.Context(), st, name); err != nil {
				return err
			}
			if source != "" {
				if err := checkSource(c.Context(), st, source); err != nil {
					return err
				}
				switch {
				case rm && (id.ID != "" || id.Key != ""):
					return fmt.Errorf("--rm takes no --id or --key")
				case !rm && id.ID == "" && id.Key == "":
					return fmt.Errorf("give --id and/or --key, or --rm")
				case rm:
					id = store.Identity{}
				default:
					// A source whose logins are the same in any case carries
					// them folded in its deliveries: Yardr[bot] is kept as
					// yardr[bot].
					d, _, err := declaredSource(c.Context(), st, source)
					if err != nil {
						return err
					}
					if k, ok := hooks.KeyFolderFor(source, d); ok {
						id.Key = k.FoldKey(id.Key)
					}
				}
				if err := st.SetIdentity(c.Context(), name, source, id, a.who()); err != nil {
					return err
				}
			} else if rm || id.ID != "" || id.Key != "" {
				return fmt.Errorf("--source is required")
			}
			ids, err := of(c.Context(), st, name)
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), ids, func(w io.Writer) {
				rows := [][]string{}
				for _, src := range sortedKeys(ids) {
					rows = append(rows, []string{src, or(ids[src].ID, "-"), or(ids[src].Key, "-")})
				}
				table(w, "SOURCE\tID\tKEY", rows)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&source, "source", "", "source: "+strings.Join(hooks.Sources(), ", "))
	f.StringVar(&id.ID, "id", "", "the "+kind+"'s user id in the source")
	f.StringVar(&id.Key, "key", "", "another name the source knows them by (Linear: profile key; GitHub: login)")
	f.BoolVar(&rm, "rm", false, "remove the identity for --source")
	return cmd
}

// parseWhere reads repeated --where key=value. A key given more than once
// holds each of its values, any of which matches: a value is then all that
// follows the "=", whatever is in it, with no separator to escape.
func parseWhere(ws []string) (map[string]string, error) {
	vals := map[string][]string{}
	for _, kv := range ws {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || v == "" {
			return nil, fmt.Errorf("--where %q: want key=value", kv)
		}
		vals[k] = append(vals[k], v)
	}
	m := map[string]string{}
	for k, vs := range vals {
		m[k] = store.JoinWhere(vs)
	}
	return m, nil
}

// subSource is the source a subscription command names: its second
// argument or --source (not both), a hook source, yardr or peer.
func subSource(ctx context.Context, st store.API, args []string, flag string) (string, error) {
	src, err := namedSource(args, flag)
	if err != nil {
		return "", err
	}
	// yardr's own two need no hook: its bead events, and the yards it is
	// linked with.
	if src == store.SourceYardr || src == store.SourcePeer {
		return src, nil
	}
	return src, checkSource(ctx, st, src)
}

// namedSource is subSource without the check that the yard hears from the
// source: what unsubscribe takes, since a subscription outlives the source
// it names (yardr source rm leaves it) and is removed by that name alone.
func namedSource(args []string, flag string) (string, error) {
	src := flag
	if len(args) == 2 {
		if flag != "" && flag != args[1] {
			return "", fmt.Errorf("source given twice: %s and --source %s", args[1], flag)
		}
		src = args[1]
	}
	if src == "" {
		return "", fmt.Errorf("name a source: %s, %s, %s or a declared source with a hook key", strings.Join(hooks.Sources(), ", "), store.SourceYardr, store.SourcePeer)
	}
	return src, nil
}

func (a *app) subscribeCmd() *cobra.Command {
	var sub store.Subscription
	var where []string
	var done, crewFlag, groupFlag bool
	cmd := &cobra.Command{
		Use:   "subscribe [<crew|group|user> [source]]",
		Short: "Deliver a source's events to a crew member's inbox, or a user's as notifications (no arguments: who is told what)",
		Long: `Without arguments it changes nothing and prints who is told what, for the
whole yard: a row for each thing subscribed to, a column for each subscriber
(every crew member and user, and each manual group that subscribes).

  on           the kind is in the crew member's --on
  sub          a subscription of its own
  on+sub       both: the --on kind is also an explicit "yardr all kind=<k>"
               subscription, so one of the two can go
  via <group>  it is a crew member of a manual group that subscribes

REACHES counts the inboxes a row arrives in (crew members and users; a
group has none of its own), so a row more than one of them gets reads 2 or
more, and a group's subscription with no crew member among its members
reads 0. Below the matrix, "reaches no one" lists the kinds of a new crew
member's default --on that are in nobody's --on and in no "yardr all
kind=<k>" subscription that reaches someone. --json prints the same.

With a subscriber: which events reach the crew member, decided as each is logged: hook deliveries
from a hook source (` + strings.Join(hooks.Sources(), ", ") + `), yardr's own bead events from
the yardr source, or what a linked yard says of the beads this yard has away
at it, from the peer source.

  all       every event from the source
  mentions  events that mention it: in a hook source, under its identity
            (yardr crew identity); in yardr, @<name> in the bead's body
            or a note
  involved  mentions, plus: in a hook source, events on what is assigned
            or (a Linear issue's delegate) delegated to it, or in a thread
            it participates in (it acted there, or yardr thread follow);
            in yardr, beads it created, advanced or noted

The scope defaults to involved for a hook source and to all for yardr.

The peer source is the yards this one is linked with (yardr peer list),
heard about the beads this yard has at them: each time such a yard says how
its bead for one of this yard's stands (it arrived, moved on, is held or no
longer held, was given up on), that is one delivery. Nobody hears it until
they subscribe. It has no identities, so mentions matches nothing, and
involved is taking part: yardr thread follow <crew member> --source peer
<bead>. A delivery carries no text of the other yard's.

--where narrows a subscription by exact matches, all of which must hold.
A key given more than once holds each value, and any of them matches:
--where depot=web --where kind=held --where kind=gave_up is either kind,
in that depot. A bead takes one value: its subscription ends with it.
Hook sources: ` + strings.Join(store.WhereKeys, ", ") + `. Linear exposes team
(key or name), project and label (any of the issue's labels). GitHub exposes
repo (owner/name) and label, and also number, state, merged,
conclusion, workflow, head_branch, head_sha, head_repo, base, train,
sender, association
(train: the yardr train whose yard/<train> branch a pull request or CI run is on;
head_repo: owner/name of the repository the head branch is in, a fork's for a
fork's pull request; sender: what acted, user, bot or organization;
association: what the author of the issue or pull request, or the writer of
the comment or review, is to the repository: owner, member, collaborator,
contributor, first_time_contributor, first_timer or none). A declared
source with a hook key exposes the keys its file names under hook.keys.
peer: ` + strings.Join(store.PeerWhereKeys, ", ") + `:
  peer    the yard, as this one calls it (yardr peer rename renames it here)
  bead, depot, label  this yard's bead, its depot and any of its labels
  stage   the stage the other yard's bead is in, as that yard said
  action  arrived, advanced, held, unheld or gave_up
yardr: ` + strings.Join(store.YardrWhereKeys, ", ") + `:
  bead, depot, type, label  the bead's (label: any of its labels)
  kind    the event: created, advanced, noted, held, unheld, closed,
          gave_up, session_blocked, ...; or ` + store.KindAttention + `, the class of
          kinds that need someone (a new crew member's --on), so
          --where depot=web --where kind=` + store.KindAttention + ` is what needs
          someone in one depot
  stage   the stage entered (created and advanced events); from: the stage left
  group   the group that owns the bead, or whose session the event ended
  actor   who acted
  train    the bead, or a bead it blocks or is the parent of (direct edges)
  state   done: the bead closed, by a terminal stage of its flow or by a
          close; once per bead. --done is short for --where state=done.

The subscriber may be a manual group: its subscriptions deliver to each of
its members that is a crew member (reason "<reason> via <group>").

A crew member and a manual group may share a name. Subscribing such a name
subscribes the crew member; --group subscribes the group instead, and --crew
states the crew member.

Each delivery records why it reached the crew member: all, mentioned,
assigned (a delegation reads assigned too), participant or done. Events a crew member is the actor of never
reach it. A crew member's --on kinds work as implicit "yardr all kind=<k>"
subscriptions (and also cover events not about a bead); ` + store.KindAttention + ` in
--on is the class, as in a filter.

The subscriber may be a user: a matching event is then a notification in
the user's inbox, a line the yard writes, apart from the mail (yardr inbox
--notifications). The user subscribes themselves, or the operator does it
for them; a crew member does not decide what a person is told. A user has no
identity in a hook source, so of a hook source only --scope all reaches
them.`,
		Example: `  yardr subscribe yardmaster linear --scope involved
  yardr subscribe triage linear --scope all --where type=issue --where action=create --where team=ENG
  yardr subscribe yardmaster github --scope all --where train=yard-sv6b --where conclusion=failure
  yardr subscribe yardmaster yardr --done
  yardr subscribe yardmaster yardr --done --where depot=yard --where label=ui
  yardr subscribe approvers yardr --where stage=approved
  yardr subscribe clerk yardr --where depot=web --where kind=held --where kind=gave_up
  yardr subscribe jo yardr --done --where depot=yard     # jo, a user, is told when a bead lands
  yardr subscribe yardmaster peer --scope all --where peer=north
  yardr subscribe jo peer --where action=held --where action=gave_up
  yardr subscribe                                        # who is told what`,
		Args: cobra.RangeArgs(0, 2),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				// The view takes none of them, and a subscription typed
				// without its subscriber must not pass for one made.
				if sub.Source != "" || c.Flags().Changed("scope") || len(where) > 0 || done || crewFlag || groupFlag {
					return fmt.Errorf("name who subscribes: yardr subscribe <crew|group|user> [source] (without arguments and flags it prints who is told what)")
				}
				return a.showSubscriptionMatrix(c)
			}
			kind, err := subscriberKind(crewFlag, groupFlag)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			src, err := subSource(c.Context(), st, args, sub.Source)
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
			user := false
			if kind == "" {
				if _, err := st.GetUser(c.Context(), args[0]); err == nil {
					user = true
				} else if !errors.Is(err, store.ErrNotFound) {
					return err
				}
			}
			if user && src != store.SourceYardr && !c.Flags().Changed("scope") {
				// A person has no source identity or thread participation, so
				// the hook's crew member default (involved) would match nothing.
				sub.Scope = store.ScopeAll
			}
			// How many there were: the store keeps an identical subscription
			// once, so only a list that grew ends in the one added. An unknown
			// subscriber is the store's to refuse, below.
			before, _, _ := subscriptionsOf(c.Context(), st, args[0], kind)
			if kind == "" {
				err = st.AddSubscription(c.Context(), args[0], sub, a.who())
			} else {
				err = st.AddSubscriptionKind(c.Context(), args[0], kind, sub, a.who())
			}
			if err != nil {
				return err
			}
			return a.showSubscriptions(c, st, args[0], kind, func(w io.Writer, subs []store.Subscription, r *store.CrewMember) {
				if len(subs) > len(before) {
					fmt.Fprintf(w, "%s subscribed: %s\n", args[0], subs[len(subs)-1])
				} else {
					// As asked: which of the list it is, in the words the
					// store keeps, only the store knows.
					fmt.Fprintf(w, "%s already subscribed: %s\n", args[0], sub)
				}
				// Neither yardr nor the linked yards have identities.
				if r == nil || src == store.SourceYardr || src == store.SourcePeer {
					return
				}
				if _, ok := r.Identities[src]; !ok && sub.Scope != store.ScopeAll {
					fmt.Fprintf(w, "note: %s has no %s identity, so it is never mentioned or assigned: yardr crew identity %s --source %s --id ...\n",
						r.Name, src, r.Name, src)
				}
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&sub.Source, "source", "", "source: "+strings.Join(append(hooks.Sources(), store.SourceYardr, store.SourcePeer), ", "))
	f.StringVar(&sub.Scope, "scope", store.ScopeInvolved, "all, mentions or involved (yardr: default all)")
	f.StringArrayVar(&where, "where", nil, "key=value filter (repeatable: keys are ANDed, a key given again adds a value, any of which matches)")
	f.BoolVar(&done, "done", false, "yardr: only beads becoming done (--where state=done)")
	f.BoolVar(&crewFlag, "crew", false, "the subscriber is the crew member of this name")
	f.BoolVar(&groupFlag, "group", false, "the subscriber is the manual group of this name")
	return cmd
}

// subscriberKind is the kind --crew or --group states, "" for neither: a
// crew member and a manual group may share a name, and the flags say which
// subscribes.
func subscriberKind(crew, group bool) (string, error) {
	if crew && group {
		return "", fmt.Errorf("use one of --crew or --group, not both")
	}
	if group {
		return "group", nil
	}
	if crew {
		return "crew", nil
	}
	return "", nil
}

// subMatrix is who is told what in the whole yard, as bare yardr subscribe
// prints it: a row for each thing subscribed to, a column for each
// subscriber.
type subMatrix struct {
	Subscribers []subColumn `json:"subscribers"`
	Rows        []subRow    `json:"rows"`
	// ReachesNoOne are the kinds of the class a new crew member hears
	// (store.AttentionKinds) that no crew member has in --on, by name or
	// through the class, and no "yardr all kind=<k>" subscription delivers
	// to anyone.
	ReachesNoOne []string `json:"reaches_no_one"`
}

// subColumn is a subscriber: a crew member, a user or a manual group.
type subColumn struct {
	Name string `json:"name"`
	Kind string `json:"kind"` // crew, user, group
	// Reaches are a group's members that are crew members: the inboxes its
	// subscriptions arrive in.
	Reaches []string `json:"reaches,omitempty"`
}

func (c subColumn) String() string {
	if c.Kind == "crew" {
		return c.Name
	}
	return c.Name + " (" + c.Kind + ")"
}

// subRow is one thing subscribed to and who gets it. A --on kind is the
// row of the subscription it works as, yardr all kind=<k>, so a subscriber
// with the kind both ways has both marks in one cell. The class is one
// row, under its name, and not a row for each of its kinds.
type subRow struct {
	Subscription store.Subscription `json:"subscription"`
	// Kind is the event kind of a "yardr all kind=<k>" row.
	Kind string `json:"kind,omitempty"`
	// Marks are, by subscriber, how it has the row: on, sub, via <group>.
	Marks map[string][]string `json:"marks"`
	// Reaches are the inboxes the row arrives in: crew members and users,
	// not groups.
	Reaches []string `json:"reaches"`
}

const (
	markOn  = "on"
	markSub = "sub"
)

// onSubscription is the subscription a --on kind works as.
func onSubscription(kind string) store.Subscription {
	return store.Subscription{Source: store.SourceYardr, Scope: store.ScopeAll, Where: map[string]string{"kind": kind}}
}

// subscriptionMatrix lays the yard's subscribers side by side. Every crew
// member and user has a column; a group has one when it subscribes.
func subscriptionMatrix(crew []store.CrewMember, users []store.User, groups []store.Group) subMatrix {
	m := subMatrix{Subscribers: []subColumn{}, Rows: []subRow{}, ReachesNoOne: []string{}}
	rows := map[string]*subRow{}
	mark := func(sub store.Subscription, column, how, inbox string) {
		row := rows[sub.String()]
		if row == nil {
			row = &subRow{Subscription: sub, Marks: map[string][]string{}, Reaches: []string{}}
			if k := sub.Where["kind"]; sub.Source == store.SourceYardr && sub.Scope == store.ScopeAll && len(sub.Where) == 1 && len(store.WhereValues(k)) == 1 {
				row.Kind = k
			}
			rows[sub.String()] = row
		}
		if !slices.Contains(row.Marks[column], how) {
			row.Marks[column] = append(row.Marks[column], how)
		}
		if inbox != "" && !slices.Contains(row.Reaches, inbox) {
			row.Reaches = append(row.Reaches, inbox)
		}
	}
	isCrew := map[string]bool{}
	for _, r := range crew {
		isCrew[r.Name] = true
		col := subColumn{Name: r.Name, Kind: "crew"}
		m.Subscribers = append(m.Subscribers, col)
		for _, k := range r.On {
			mark(onSubscription(k), col.String(), markOn, r.Name)
		}
		for _, sub := range r.Subscriptions {
			mark(sub, col.String(), markSub, r.Name)
		}
	}
	for _, u := range users {
		col := subColumn{Name: u.Name, Kind: "user"}
		m.Subscribers = append(m.Subscribers, col)
		for _, sub := range u.Subscriptions {
			mark(sub, col.String(), markSub, u.Name)
		}
	}
	for _, g := range groups {
		if len(g.Subscriptions) == 0 {
			continue
		}
		// A group's subscriptions arrive in the inboxes of its members that
		// are crew members, and in no person's.
		col := subColumn{Name: g.Name, Kind: "group"}
		for _, member := range g.Members {
			if isCrew[member] {
				col.Reaches = append(col.Reaches, member)
			}
		}
		m.Subscribers = append(m.Subscribers, col)
		for _, sub := range g.Subscriptions {
			mark(sub, col.String(), markSub, "")
			for _, member := range col.Reaches {
				mark(sub, member, "via "+g.Name, member)
			}
		}
	}
	for _, key := range sortedKeys(rows) {
		slices.Sort(rows[key].Reaches)
		m.Rows = append(m.Rows, *rows[key])
	}
	// The kinds first, as crew show lists them before the subscriptions.
	slices.SortStableFunc(m.Rows, func(a, b subRow) int {
		if (a.Kind == "") != (b.Kind == "") {
			if a.Kind != "" {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	// A kind reaches someone through its own row, or through a row that
	// filters on nothing but kinds and has it among them, by name or as
	// one of the class.
	reached := map[string]bool{}
	for _, row := range m.Rows {
		sub := row.Subscription
		if kinds, ok := sub.Where["kind"]; ok && sub.Source == store.SourceYardr && sub.Scope == store.ScopeAll && len(sub.Where) == 1 && len(row.Reaches) > 0 {
			for _, word := range store.WhereValues(kinds) {
				for _, k := range store.KindsOf(word) {
					reached[k] = true
				}
			}
		}
	}
	for _, k := range store.AttentionKinds {
		if !reached[k] {
			m.ReachesNoOne = append(m.ReachesNoOne, k)
		}
	}
	return m
}

// showSubscriptionMatrix is yardr subscribe without arguments.
func (a *app) showSubscriptionMatrix(c *cobra.Command) error {
	st, err := a.store(c.Context())
	if err != nil {
		return err
	}
	crew, err := st.Crew(c.Context())
	if err != nil {
		return err
	}
	users, err := st.Users(c.Context())
	if err != nil {
		return err
	}
	groups, err := st.Groups(c.Context())
	if err != nil {
		return err
	}
	m := subscriptionMatrix(crew, users, groups)
	return a.out(c.OutOrStdout(), m, func(w io.Writer) {
		header := []string{"SUBSCRIPTION", "REACHES"}
		for _, col := range m.Subscribers {
			header = append(header, col.String())
		}
		rows := [][]string{}
		for _, row := range m.Rows {
			cells := []string{row.Subscription.String(), strconv.Itoa(len(row.Reaches))}
			for _, col := range m.Subscribers {
				cells = append(cells, or(strings.Join(row.Marks[col.String()], "+"), "-"))
			}
			rows = append(rows, cells)
		}
		table(w, strings.Join(header, "\t"), rows)
		for _, col := range m.Subscribers {
			if col.Kind == "group" {
				fmt.Fprintf(w, "%s reaches: %s\n", col, or(strings.Join(col.Reaches, ", "), "no crew member"))
			}
		}
		if len(m.ReachesNoOne) > 0 {
			fmt.Fprintf(w, "reaches no one (kinds of %s, a new crew member's --on, that nobody has): %s\n", store.KindAttention, strings.Join(m.ReachesNoOne, ", "))
		}
	})
}

// subscriptionsOf are the subscriptions of the crew member, user or group
// called name, looked up in the order the store does; r is nil for a user
// and a group. kind states one of them, "" to read them in that order.
func subscriptionsOf(ctx context.Context, st store.API, name, kind string) (subs []store.Subscription, r *store.CrewMember, err error) {
	switch kind {
	case "crew":
		r, err = st.GetCrew(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		return r.Subscriptions, r, nil
	case "group":
		g, err := st.GetGroup(ctx, name)
		if err != nil {
			return nil, nil, err
		}
		return g.Subscriptions, nil, nil
	}
	r, err = st.GetCrew(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		u, uerr := st.GetUser(ctx, name)
		if uerr == nil {
			return u.Subscriptions, nil, nil
		} else if !errors.Is(uerr, store.ErrNotFound) {
			return nil, nil, uerr
		}
		g, gerr := st.GetGroup(ctx, name)
		if gerr != nil {
			return nil, nil, gerr
		}
		return g.Subscriptions, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return r.Subscriptions, r, nil
}

// showSubscriptions prints (or outputs) the subscriptions of the
// crew member, user or group called name.
func (a *app) showSubscriptions(c *cobra.Command, st store.API, name, kind string, text func(io.Writer, []store.Subscription, *store.CrewMember)) error {
	subs, r, err := subscriptionsOf(c.Context(), st, name, kind)
	if err != nil {
		return err
	}
	return a.out(c.OutOrStdout(), subs, func(w io.Writer) { text(w, subs, r) })
}

func (a *app) unsubscribeCmd() *cobra.Command {
	var source, scope string
	var where []string
	var crewFlag, groupFlag bool
	cmd := &cobra.Command{
		Use:   "unsubscribe <crew|group|user> [source]",
		Short: "Remove subscriptions to a source",
		Long: `Removes every subscription of the crew member (or manual group, or user) to the
source; --scope and --where narrow it to the subscriptions with that scope
and exactly those filters. A user's are removed by the user and by the
operator, as they are added.

A crew member and a manual group may share a name. Then the subscriptions
removed are the matching ones of whichever of the two has them; when both
do, the command refuses naming both, and --crew or --group says which.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			kind, err := subscriberKind(crewFlag, groupFlag)
			if err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			src, err := namedSource(args, source)
			if err != nil {
				return err
			}
			w, err := parseWhere(where)
			if err != nil {
				return err
			}
			var n int
			if kind == "" {
				n, err = st.RemoveSubscriptions(c.Context(), args[0], src, scope, w, a.who())
			} else {
				n, err = st.RemoveSubscriptionsKind(c.Context(), args[0], kind, src, scope, w, a.who())
			}
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]any{"subscriber": args[0], "removed": n}, func(w io.Writer) {
				fmt.Fprintf(w, "%s: %d subscription(s) removed\n", args[0], n)
			})
		},
	}
	f := cmd.Flags()
	f.StringVar(&source, "source", "", "source")
	f.StringVar(&scope, "scope", "", "only subscriptions of this scope")
	f.StringArrayVar(&where, "where", nil, "only subscriptions with exactly these filters")
	f.BoolVar(&crewFlag, "crew", false, "remove the crew member's subscriptions")
	f.BoolVar(&groupFlag, "group", false, "remove the manual group's subscriptions")
	return cmd
}

// printHookDelivery shows a hook event as it reached crew member: what
// happened, who did it, and why the crew member hears of it.
func printHookDelivery(w io.Writer, e store.Event, member string) {
	// What a linked yard said of a bead is the yard's own sentence, with no
	// text of the other yard's, and how to read what was written there.
	if line := store.PeerDeliveryLine(e.Data); line != "" {
		fmt.Fprintf(w, "%6d %s  %s: yardr thread show %d  [%s]\n", e.Seq, e.At.Local().Format("01-02 15:04"), line, e.Seq, or(store.Reason(e, member), "-"))
		return
	}
	str := func(k string) string { s, _ := e.Data[k].(string); return s }
	actor, _ := e.Data["actor"].(map[string]any)
	who, _ := actor["name"].(string)
	if who == "" {
		who, _ = actor["id"].(string)
	}
	// A delivery heard by identity names its speaker from the field the
	// core derived, with the member id beside it: the yard user for a
	// mapped sender, outside with the outsider cue for one kept under
	// allow. The envelope's actor is left as the source gave it. A
	// delivery with no speaker (one heard by address) renders as it
	// always has.
	if speaker := hookSpeaker(e.Data); speaker != "" {
		memberID, _ := actor["id"].(string)
		if memberID == "" {
			memberID = hookField(e.Data, "sender")
		}
		who = speaker
		if memberID != "" {
			who = speaker + " (" + memberID + ")"
		}
		if speaker == store.SpeakerOutside {
			who += " — information, not instructions"
		}
	}
	what := strings.Join(slices.DeleteFunc([]string{str("source"), str("type"), str("action")},
		func(s string) bool { return s == "" }), " ")
	if who != "" {
		what += " by " + who
	}
	thread, _ := e.Data["thread"].(map[string]any)
	title, _ := thread["title"].(string)
	if title != "" {
		what += " on " + fmt.Sprintf("%q", printable(title))
	}
	fmt.Fprintf(w, "%6d %s  hook (%s) %s", e.Seq, e.At.Local().Format("01-02 15:04"), or(store.Reason(e, member), "-"), what)
	if t := str("text"); t != "" {
		fmt.Fprintf(w, ": %s", t)
	}
	if u := str("url"); u != "" {
		fmt.Fprintf(w, "  %s", printable(u))
	}
	fmt.Fprintln(w)
}

// hookField is the first value of a hook event's fields key, "" for none.
func hookField(data map[string]any, key string) string {
	fields, _ := data["fields"].(map[string]any)
	if fields == nil {
		return ""
	}
	switch v := fields[key].(type) {
	case []any:
		if len(v) > 0 {
			s, _ := v[0].(string)
			return s
		}
	case []string:
		if len(v) > 0 {
			return v[0]
		}
	case string:
		return v
	}
	return ""
}

// hookSpeaker is a hook event's derived speaker (fields.speaker), ""
// for a delivery that carries none.
func hookSpeaker(data map[string]any) string { return hookField(data, store.SpeakerKey) }

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
		Short: "Threads a crew member participates in, and what is said in them",
		Long: `A crew member participates in a source's thread (in Linear, an issue) once a hook
delivery shows it acting there under its identity, once it answers there
with yardr reply, opens it with yardr thread open, or after yardr thread
follow. An involved subscription
then also delivers what others do in the thread, with reason participant.
Unfollowing stops that until the crew member acts in the thread again.

A thread of the source peer is a bead of this yard that is at a linked yard:
yardr thread follow <crew member> --source peer <bead> has the crew member
hear what that yard says of it, through a peer subscription of scope
involved.

yardr thread show <seq> reads the source thread of a hook delivery, yardr
thread show <bead> what a linked yard wrote on its bead while this yard's
is there, and yardr thread state sets the status of a thread the crew
member takes part in.`,
	}
	threadArgs := func(source *string) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, args []string) error {
			if err := cobra.ExactArgs(2)(c, args); err != nil {
				return err
			}
			if *source == "" {
				return fmt.Errorf("--source is required")
			}
			return nil
		}
	}
	var source string
	follow := &cobra.Command{
		Use:   "follow <crew member> <thread-id>",
		Short: "Make a crew member a participant in a thread",
		Example: `  yardr thread follow yardmaster --source linear 539068e2-ae88-4d09-bd75-22eb4a59612f
  yardr thread follow yardmaster --source peer web-d5zc`,
		Args: threadArgs(&source),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			if err := threadSource(c.Context(), st, source); err != nil {
				return err
			}
			if err := st.FollowThread(c.Context(), args[0], source, args[1], a.who()); err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]any{"crew": args[0], "source": source, "thread": args[1]}, func(w io.Writer) {
				fmt.Fprintf(w, "%s follows %s thread %s\n", args[0], source, printable(args[1]))
			})
		},
	}
	unfollow := &cobra.Command{
		Use:   "unfollow <crew member> <thread-id>",
		Short: "End a crew member's participation in a thread",
		Args:  threadArgs(&source),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// No threadSource here: a participation outlives the source it
			// is in (yardr source rm leaves it), and the store refuses a
			// thread the crew member does not participate in.
			if err := st.UnfollowThread(c.Context(), args[0], source, args[1], a.who()); err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), map[string]any{"crew": args[0], "source": source, "thread": args[1]}, func(w io.Writer) {
				fmt.Fprintf(w, "%s unfollowed %s thread %s\n", args[0], source, printable(args[1]))
			})
		},
	}
	for _, c := range []*cobra.Command{follow, unfollow} {
		c.Flags().StringVar(&source, "source", "", "source: "+strings.Join(append(hooks.Sources(), store.SourcePeer), ", "))
		_ = c.MarkFlagRequired("source")
	}
	list := &cobra.Command{
		Use:   "list <crew member>",
		Short: "List the threads a crew member participates in",
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
			return a.out(c.OutOrStdout(), ps, func(w io.Writer) {
				rows := [][]string{}
				for _, p := range ps {
					rows = append(rows, []string{p.Source, printable(p.Thread), p.How, p.Since.Local().Format("01-02 15:04"),
						printable(or(p.Title, "-"))})
				}
				table(w, "SOURCE\tTHREAD\tHOW\tSINCE\tTITLE", rows)
			})
		},
	}
	cmd.AddCommand(follow, unfollow, list, a.threadShowCmd(), a.threadOpenCmd(), a.threadStateCmd())
	return cmd
}

// printBeadDelivery shows a bead event in crew member's inbox, with why it
// is there: a subscription's reason, or "on" for a --on kind. An answer
// reads as its sentence instead, in words wherever it lands.
func printBeadDelivery(w io.Writer, e store.Event, member string) {
	if line, _ := e.Data["answer"].(string); line != "" {
		fmt.Fprintf(w, "%6d %s  %s\n", e.Seq, e.At.Local().Format("01-02 15:04"), line)
		return
	}
	data := map[string]any{}
	for k, v := range printableThreadFields(store.Brief(e.Data)) {
		if k != "deliveries" && k != "watches_ended" && k != "notified" && k != "answer" {
			data[k] = v
		}
	}
	// A harness's error leads with what it is: among the event's fields in
	// the order of their names, the reason comes after a hint of three lines.
	// A refused move leads the same way, with the command and the yard's
	// answer to it.
	lead := ""
	if first := map[string][]string{"session_harness_error": {"reason", "line"},
		"session_harness_dialog": {"kind", "version", "dialog"},
		store.MoveRefused:        {"reason", "command", "error"}}[e.Kind]; first != nil {
		for _, k := range first {
			if v, ok := data[k]; ok {
				lead += k + "=" + show(v) + " "
				delete(data, k)
			}
		}
	}
	extra := strings.TrimSuffix(lead, " ")
	if len(data) > 0 {
		extra = lead + fmtConfig(data)
	}
	fmt.Fprintf(w, "%6d %s  %-8s %s (%s) by %s %s\n", e.Seq, e.At.Local().Format("01-02 15:04"), deref(e.Bead), e.Kind,
		or(store.Reason(e, member), "on"), e.Actor, extra)
}

// watchCmds are yardr bead watch and unwatch: sugar for yardr subscriptions
// with bead=<id>, which end when the bead closes.
func (a *app) watchCmds() []*cobra.Command {
	var member, stage, kind string
	var done bool
	watch := &cobra.Command{
		Use:   "watch <id>",
		Short: "Deliver a bead's events to a crew member's inbox until it closes",
		Long: `Subscribes the crew member (or manual group) to the bead's events in yardr:
yardr subscribe <crew member> yardr --where bead=<id>, narrowed by --stage (the
stage it enters), --kind and --done (it closes, by a terminal stage or a
close). The watch ends when the bead closes, after the close is delivered.`,
		Example: `  yardr bead watch yard-k3f9 --done
  yardr bead watch yard-k3f9 --crew yardmaster --stage review`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name, err := currentCrew(member)
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
			return a.out(c.OutOrStdout(), sub, func(w io.Writer) { fmt.Fprintf(w, "%s watches %s: %s\n", name, b.ID, sub) })
		},
	}
	f := watch.Flags()
	f.StringVar(&member, "crew", "", "crew member or manual group (default $YARDR_CREW)")
	f.StringVar(&stage, "stage", "", "only when it enters this stage")
	f.StringVar(&kind, "kind", "", "only events of this kind (advanced, noted, closed, ...)")
	f.BoolVar(&done, "done", false, "only when it is done")

	var unCrew string
	unwatch := &cobra.Command{
		Use:   "unwatch <id>",
		Short: "Stop delivering a bead's events to a crew member",
		Long:  "Removes every yardr subscription of the crew member (or manual group) for the bead.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name, err := currentCrew(unCrew)
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
			return a.out(c.OutOrStdout(), map[string]any{"subscriber": name, "bead": args[0], "removed": n}, func(w io.Writer) {
				fmt.Fprintf(w, "%s: %d watch(es) on %s removed\n", name, n, args[0])
			})
		},
	}
	unwatch.Flags().StringVar(&unCrew, "crew", "", "crew member or manual group (default $YARDR_CREW)")
	return []*cobra.Command{watch, unwatch}
}
