package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// kindLimits is what one ask came to, for one harness kind: the provider's
// report and what the yard reads from it, or why nothing is known.
type kindLimits struct {
	Kind string `json:"kind"`
	// Group is the group whose login was read, with --group; "" reads the
	// machine's.
	Group string `json:"group,omitempty"`
	// Limits is the report; nil when nothing is known.
	Limits *limits.Report `json:"limits,omitempty"`
	// Window and ResetsAt are the window an agent of the kind waits for,
	// when one is used up (limits.Report.UsedUp).
	Window   string     `json:"window,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Unknown is why nothing is known, in a line.
	Unknown string `json:"limits_unknown,omitempty"`
}

// askLimits asks for kind, once, and says what it came to.
func askLimits(ctx context.Context, ask func(context.Context, string) (*limits.Report, error), kind string) kindLimits {
	out := kindLimits{Kind: kind}
	r, err := ask(ctx, kind)
	if err != nil {
		out.Unknown = err.Error()
		return out
	}
	out.Limits = r
	if w := r.UsedUp(); w != nil {
		out.Window, out.ResetsAt = w.Label, w.ResetsAt
	}
	return out
}

// providerLimits is what one ask came to, for one provider: who burns it,
// the provider's report and what the yard reads from it, or why nothing is
// known.
type providerLimits struct {
	Provider string `json:"provider"`
	// Kinds are the harness kinds burning the provider, each once.
	Kinds []string `json:"kinds"`
	// BurnedBy is who burns it, as doctor names them: "group builders",
	// "crew member yardmaster", "the yard's default".
	BurnedBy []string `json:"burned_by"`
	// Limits is the report; nil when nothing is known.
	Limits *limits.Report `json:"limits,omitempty"`
	// Window and ResetsAt are the window an agent burning the provider
	// waits for, when one is used up (limits.Report.UsedUp).
	Window   string     `json:"window,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Unknown is why nothing is known, in a line.
	Unknown string `json:"limits_unknown,omitempty"`
}

// askProvider asks for the provider once through its source, and says what
// it came to. One no source answers for is not asked: it is not known, with
// why.
func askProvider(ctx context.Context, g providerGroup) providerLimits {
	out := providerLimits{Provider: g.name, Kinds: g.kinds, BurnedBy: g.by}
	if !g.answered {
		out.Unknown = (&limits.NoSource{Provider: g.name}).Error()
		return out
	}
	r, err := g.answerer.AskName(ctx, g.name)
	if err != nil {
		out.Unknown = err.Error()
		return out
	}
	out.Limits = r
	if w := r.UsedUp(); w != nil {
		out.Window, out.ResetsAt = w.Label, w.ResetsAt
	}
	return out
}

// sourceLimits is what one ask came to, for a source that answers for
// itself: the source's report and what the yard reads from it, or why
// nothing is known.
type sourceLimits struct {
	Source string `json:"source"`
	// Group is the group whose env the source was asked under, with
	// --group; "" asks with the caller's.
	Group string `json:"group,omitempty"`
	// Limits is the report; nil when nothing is known.
	Limits *limits.Report `json:"limits,omitempty"`
	// Window and ResetsAt are the window that is used up and resets last,
	// when one is (limits.Report.UsedUp).
	Window   string     `json:"window,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Unknown is why nothing is known, in a line.
	Unknown string `json:"limits_unknown,omitempty"`
}

// askSource asks the source for itself, once, and says what it came to.
func askSource(ctx context.Context, a limits.Answerer) sourceLimits {
	out := sourceLimits{Source: a.Source}
	r, err := a.AskSelf(ctx)
	if err != nil {
		out.Unknown = err.Error()
		return out
	}
	out.Limits = r
	if w := r.UsedUp(); w != nil {
		out.Window, out.ResetsAt = w.Label, w.ResetsAt
	}
	return out
}

func (a *app) limitsCmd() *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "limits [kind|source]",
		Short: "What the subscriptions the yard burns still carry, and what its sources say of themselves",
		Long: `Asks the source of the yard that says limits (a declared source with a
limits key: yardr source --help) what the subscriptions the yard burns
still carry, and prints each window of their usage limits: how far it is
used, when it resets and in how long, and whether it is used up. With a
kind it asks for that one; without, every kind a group, a crew member or
the yard's default_kind names is resolved to its provider — its provider
setting where one is set, else the kinds table's mapping of the kind — and
each provider is asked once, naming who burns it. With --group and no kind
it asks for that group's own: its kind through its provider setting, as a
route's condition reads it.

  yardr limits kimi
  kimi  aiquokka says (provider Kimi):
    5h      100% used (100 of 100)  resets 00:02 (in 42m)             used up
    Weekly  38% used (38 of 100)    resets Sat 10 Oct 09:02 (in 3d 9h)
    an agent of kind kimi waits for the 5h window, which resets 00:02 (in 42m)

  yardr limits
  kimi  aiquokka says (provider Kimi): burned by groups builders, nightshift (kind kimi)
    5h      100% used (100 of 100)  resets 00:02 (in 42m)             used up
    an agent burning kimi waits for the 5h window, which resets 00:02 (in 42m)

A source that answers for itself (a limits key with no kinds: yardr source
--help) is listed beside the providers, each with its windows, and with its
name it is asked for alone:

  yardr limits machine
  machine says (provider machine):
    load  12.5% used  reset not known
    no window is used up

Where the provider says what share of a window has passed, it is printed
beside the used figure, and a window burned past it is marked over target;
a route's quota condition may compare with it (yardr route add --help).

A window the provider counts for one model alone says so, and is marked with
that model. A group with no condition waits while a window of its provider is
used up (yardr route add --help), but not for one of these; a condition that
names the window still reads it.

  yardr limits claude
  claude  aiquokka says (provider Claude):
    5h            24% used  resets 19:00 (in 33m)
    Weekly        45% used  resets Thu 15 Oct 14:27 (in 2d 20h)
    Weekly Fable  67% used  resets Thu 15 Oct 14:27 (in 2d 20h)  model Fable
    no window is used up

Every run asks: the source's command is run once per provider and once per
source that answers for itself, once for the kind or source with one named,
now, by this command and as whoever runs it, for at most ` + limits.Timeout.String() + ` each.
Nothing is remembered between two runs. A provider may be slow and may be
bounded in how often it answers, so this is a command for when you want to
know, not one to run in a loop.

With --group the command runs with that group's env, as its agents run,
and the answer is for the login the group burns: two groups on different
logins report different windows. Without it the answer is for the login
the provider reads on this machine, which is the operator's; a group whose
env gives its harness another account is not described by it, nor is a
crew member on another login.

A provider is "not known", with the reason, when no source of the yard
names it or when its command gave no answer: another exit status than 0,
nothing within the bound, or output that is not the JSON the yard reads.
A kind that resolves to nothing is not asked, and is named under unmapped.
The exit status is then 1. --json prints, per provider, the burning kinds
and users beside the report under limits, and per source that answers for
itself under sources: the window that is used up and resets last under
window and resets_at, or the reason under limits_unknown.`,
		Example: `  yardr limits
  yardr limits kimi
  yardr limits machine
  yardr limits --group mckean2-builders
  yardr limits kimi --json | jq -r '.limits[0].resets_at'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			cfg, err := a.config()
			if err != nil {
				return err
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			var env []string
			var g store.Group
			if group != "" {
				if env, g, err = groupReadEnv(ctx, st, group); err != nil {
					return err
				}
			}
			if len(args) == 1 {
				as, err := limits.Answerers(ctx, st, cfg.Home)
				if err != nil {
					return err
				}
				if _, _, listed := limits.Resolve(as, args[0], ""); !listed {
					if self, ok := limits.SelfAnswerer(as, args[0]); ok {
						// No source lists the kind: the name is a source
						// that answers for itself.
						self.Env = env
						own := []sourceLimits{askSource(ctx, self)}
						own[0].Group = group
						if err := a.out(c.OutOrStdout(), map[string]any{"limits": []kindLimits{}, "sources": own, "unmapped": []string{}}, func(w io.Writer) {
							now := time.Now()
							for _, l := range own[0].lines(now) {
								fmt.Fprintln(w, l)
							}
						}); err != nil {
							return err
						}
						if own[0].Unknown != "" {
							return errCheckFailed // the line said which and why
						}
						return nil
					}
				}
				asked := []kindLimits{askLimits(ctx, func(ctx context.Context, kind string) (*limits.Report, error) {
					return askKind(ctx, st, cfg.Home, kind, env)
				}, args[0])}
				asked[0].Group = group
				if err := a.out(c.OutOrStdout(), map[string]any{"limits": asked, "sources": []sourceLimits{}, "unmapped": []string{}}, func(w io.Writer) {
					now := time.Now()
					for _, k := range asked {
						for _, l := range k.lines(now) {
							fmt.Fprintln(w, l)
						}
					}
				}); err != nil {
					return err
				}
				if asked[0].Unknown != "" {
					return errCheckFailed // the line said which and why
				}
				return nil
			}
			if group != "" {
				kind := g.Kind(defaultKind(ctx, st))
				if kind == "" {
					if g.Runner != "herdr" {
						return fmt.Errorf("group %s starts no agents, so no subscription burns for it: nothing has windows to read", group)
					}
					kind = agents.DefaultKind
				}
				provider, _ := g.Config["provider"].(string)
				asked := []kindLimits{askLimits(ctx, func(ctx context.Context, kind string) (*limits.Report, error) {
					return askKindFor(ctx, st, cfg.Home, kind, provider, env)
				}, kind)}
				asked[0].Group = group
				if err := a.out(c.OutOrStdout(), map[string]any{"limits": asked, "sources": []sourceLimits{}, "unmapped": []string{}}, func(w io.Writer) {
					now := time.Now()
					for _, k := range asked {
						for _, l := range k.lines(now) {
							fmt.Fprintln(w, l)
						}
					}
				}); err != nil {
					return err
				}
				if asked[0].Unknown != "" {
					return errCheckFailed // the line said which and why
				}
				return nil
			}
			as, err := limits.Answerers(ctx, st, cfg.Home)
			if err != nil {
				return err
			}
			unmapped := []string{}
			uses, err := limitUsesInUse(ctx, st)
			if err != nil {
				return err
			}
			groups, unasked := groupUses(as, uses)
			for _, u := range unasked {
				unmapped = append(unmapped, u.kind)
			}
			var selves []limits.Answerer
			for _, a := range as {
				if a.Limits.Self() {
					selves = append(selves, a)
				}
			}
			if len(as) == 0 && len(groups) == 0 {
				return a.out(c.OutOrStdout(), map[string]any{"limits": []providerLimits{}, "sources": []sourceLimits{}, "unmapped": unmapped}, func(w io.Writer) {
					fmt.Fprintln(w, "No source of this yard says limits: nothing is known, and nothing was asked.")
					fmt.Fprintln(w, "A source says them by a limits key in its file (yardr source --help, docs/integrations.md).")
				})
			}
			asked := []providerLimits{}
			for _, g := range groups {
				asked = append(asked, askProvider(ctx, g))
			}
			own := []sourceLimits{}
			for _, a := range selves {
				own = append(own, askSource(ctx, a))
			}
			if len(asked) == 0 && len(own) == 0 {
				return a.out(c.OutOrStdout(), map[string]any{"limits": asked, "sources": own, "unmapped": unmapped}, func(w io.Writer) {
					fmt.Fprintf(w, "Nothing was asked: no group, crew member or default_kind of this yard names a kind a source says the limits of (%s).\n", answeredKinds(as))
					fmt.Fprintln(w, "yardr limits <kind> asks for one.")
				})
			}
			if err := a.out(c.OutOrStdout(), map[string]any{"limits": asked, "sources": own, "unmapped": unmapped}, func(w io.Writer) {
				now := time.Now()
				for _, p := range asked {
					for _, l := range p.lines(now) {
						fmt.Fprintln(w, l)
					}
				}
				for _, s := range own {
					for _, l := range s.lines(now) {
						fmt.Fprintln(w, l)
					}
				}
				if len(unmapped) > 0 {
					fmt.Fprintf(w, "not asked: %s, in use here and listed by no source (limits.kinds)\n", strings.Join(unmapped, ", "))
				}
			}); err != nil {
				return err
			}
			if slices.ContainsFunc(asked, func(p providerLimits) bool { return p.Unknown != "" }) ||
				slices.ContainsFunc(own, func(s sourceLimits) bool { return s.Unknown != "" }) {
				return errCheckFailed // the lines said which and why
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "read the login the group burns: its kind, with its env")
	return cmd
}

// askKind asks for kind under env: env are the entries of the group whose
// login is read, nil for the machine's. The first source by name whose
// kinds table lists the kind is asked.
func askKind(ctx context.Context, st store.API, home, kind string, env []string) (*limits.Report, error) {
	as, err := limits.Answerers(ctx, st, home)
	if err != nil {
		return nil, err
	}
	for i := range as {
		if _, ok := as[i].Limits.Kinds[kind]; ok {
			as[i].Env = env
			return as[i].Ask(ctx, kind)
		}
	}
	return nil, &limits.NoSource{Kind: kind}
}

// askKindFor asks for kind with the provider setting under env: with the
// setting the first source naming the provider is asked for it, without it
// the kind resolves through the kinds table.
func askKindFor(ctx context.Context, st store.API, home, kind, provider string, env []string) (*limits.Report, error) {
	as, err := limits.Answerers(ctx, st, home)
	if err != nil {
		return nil, err
	}
	a, name, ok := limits.Resolve(as, kind, provider)
	if !ok {
		if provider != "" {
			return nil, &limits.NoSource{Provider: provider}
		}
		return nil, &limits.NoSource{Kind: kind}
	}
	a.Env = env
	if provider != "" {
		return a.AskName(ctx, name)
	}
	return a.Ask(ctx, kind)
}

// limitUse is one place the yard burns quota: a harness kind with its
// provider setting, and who names it.
type limitUse struct {
	kind, provider, by string
}

// limitUsesInUse are the harness kinds the yard runs on with their provider
// settings: its default_kind and every kind a herdr group or crew member
// names, each with who names it.
func limitUsesInUse(ctx context.Context, st store.API) ([]limitUse, error) {
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return nil, err
	}
	return limitUses(defaultKind(ctx, st), groups, crew), nil
}

// limitUses are the harness kinds the yard runs on with their provider
// settings, one per user: the default_kind first, then the herdr crew
// members and groups in the store's order. A config naming no kind runs on
// nothing yardr can name, and is left out.
func limitUses(def string, groups []store.Group, crew []store.CrewMember) []limitUse {
	var out []limitUse
	if def != "" {
		out = append(out, limitUse{kind: def, by: "the yard's default"})
	}
	for _, r := range crew {
		if r.Runner != "herdr" {
			continue
		}
		kind, _ := r.Config["kind"].(string)
		if kind == "" {
			continue
		}
		provider, _ := r.Config["provider"].(string)
		out = append(out, limitUse{kind: kind, provider: provider, by: "crew member " + r.Name})
	}
	for _, g := range groups {
		if g.Runner != "herdr" {
			continue
		}
		kind, _ := g.Config["kind"].(string)
		if kind == "" {
			continue
		}
		provider, _ := g.Config["provider"].(string)
		out = append(out, limitUse{kind: kind, provider: provider, by: "group " + g.Name})
	}
	return out
}

// providerGroup is one provider the yard burns: the kinds burning it, who
// burns it, and the source it is asked through.
type providerGroup struct {
	name string
	// kinds are the harness kinds burning the provider, each once; by who
	// burns it, in the uses' order.
	kinds, by []string
	answerer  limits.Answerer
	answered  bool
}

// groupUses resolves each use to its provider and groups them: one group
// per distinct provider, answered or not, in first-seen order, asked
// through the first source by name of those resolving to it. Kinds that
// resolve to nothing are unmapped, each with who names it.
func groupUses(as []limits.Answerer, uses []limitUse) ([]providerGroup, []harnessUse) {
	var groups []providerGroup
	var unmapped []harnessUse
	at := map[string]int{}
	for _, u := range uses {
		a, name, ok := limits.Resolve(as, u.kind, u.provider)
		if !ok && u.provider == "" {
			found := false
			for i := range unmapped {
				if unmapped[i].kind == u.kind {
					unmapped[i].by = append(unmapped[i].by, u.by)
					found = true
					break
				}
			}
			if !found {
				unmapped = append(unmapped, harnessUse{kind: u.kind, by: []string{u.by}})
			}
			continue
		}
		i, seen := at[name]
		if !seen {
			i = len(groups)
			at[name] = i
			groups = append(groups, providerGroup{name: name, answerer: a, answered: ok})
		}
		g := &groups[i]
		if !slices.Contains(g.kinds, u.kind) {
			g.kinds = append(g.kinds, u.kind)
		}
		g.by = append(g.by, u.by)
		if ok && (!g.answered || a.Source < g.answerer.Source) {
			g.answerer, g.answered = a, true
		}
	}
	return groups, unmapped
}

// answeredKinds says which source answers for which kinds: "aiquokka for
// claude, codex, kimi"; a source that answers for itself says so, the
// built-in with its mark.
func answeredKinds(as []limits.Answerer) string {
	parts := make([]string, len(as))
	for i, a := range as {
		if a.Limits.Self() {
			parts[i] = answererName(a) + " answers for itself"
		} else {
			parts[i] = answererName(a) + " for " + strings.Join(a.Limits.KindNames(), ", ")
		}
	}
	return strings.Join(parts, "; ")
}

// answererName is the source's name as doctor says it: the built-in with
// its mark.
func answererName(a limits.Answerer) string {
	if a.Builtin {
		return a.Source + " (built in)"
	}
	return a.Source
}

// lines are what an ask came to, for a person.
func (k kindLimits) lines(now time.Time) []string {
	if k.Limits == nil {
		return []string{fmt.Sprintf("%s  not known: %s", k.Kind, k.Unknown)}
	}
	r := k.Limits
	head := fmt.Sprintf("%s  %s says (provider %s", k.Kind, r.Source, r.Provider)
	if r.Plan != "" {
		head += ", plan " + r.Plan
	}
	head += ")"
	if k.Group != "" {
		head += ", for group " + k.Group
	}
	return append([]string{head + ":"}, append(windowLines(r, now), "  "+k.summary(now))...)
}

// summary says in a line what the report means for an agent of the kind.
func (k kindLimits) summary(now time.Time) string {
	if k.Window == "" {
		return "no window is used up"
	}
	return fmt.Sprintf("an agent of kind %s waits for the %s window, which %s", k.Kind, k.Window, resetText(k.ResetsAt, now))
}

// lines are what an ask came to, for a person.
func (p providerLimits) lines(now time.Time) []string {
	if p.Limits == nil {
		return []string{fmt.Sprintf("%s  not known: %s; burned by %s", p.Provider, p.Unknown, p.burnedBy())}
	}
	r := p.Limits
	head := fmt.Sprintf("%s  %s says (provider %s", p.Provider, r.Source, r.Provider)
	if r.Plan != "" {
		head += ", plan " + r.Plan
	}
	return append([]string{head + "): burned by " + p.burnedBy()}, append(windowLines(r, now), "  "+p.summary(now))...)
}

// windowLines are a report's windows, each how far it is used and when it
// resets, the columns aligned, used up and over target marked, and the model
// of a window that counts for one alone.
func windowLines(r *limits.Report, now time.Time) []string {
	if len(r.Windows) == 0 {
		return []string{"  no window: the provider knows of no limit"}
	}
	label, used := 0, 0
	for _, w := range r.Windows {
		label, used = max(label, len(w.Label)), max(used, len(usedText(w)))
	}
	var out []string
	for _, w := range r.Windows {
		line := fmt.Sprintf("  %-*s  %-*s  %s", label, w.Label, used, usedText(w), resetText(w.ResetsAt, now))
		if w.UsedUp() {
			line += "  used up"
		}
		if w.OverTarget() {
			line += "  over target"
		}
		if w.Model != "" {
			line += "  model " + w.Model
		}
		out = append(out, line)
	}
	return out
}

// lines are what an ask came to, for a person.
func (s sourceLimits) lines(now time.Time) []string {
	if s.Limits == nil {
		return []string{fmt.Sprintf("%s  not known: %s", s.Source, s.Unknown)}
	}
	r := s.Limits
	head := fmt.Sprintf("%s says (provider %s", s.Source, r.Provider)
	if r.Plan != "" {
		head += ", plan " + r.Plan
	}
	head += ")"
	if s.Group != "" {
		head += ", for group " + s.Group
	}
	return append([]string{head + ":"}, append(windowLines(r, now), "  "+s.summary(now))...)
}

// summary says in a line what the report means: which window is used up,
// when one is.
func (s sourceLimits) summary(now time.Time) string {
	if s.Window == "" {
		return "no window is used up"
	}
	return fmt.Sprintf("the %s window is used up, which %s", s.Window, resetText(s.ResetsAt, now))
}

// summary says in a line what the report means for an agent burning the
// provider.
func (p providerLimits) summary(now time.Time) string {
	if p.Window == "" {
		return "no window is used up"
	}
	return fmt.Sprintf("an agent burning %s waits for the %s window, which %s", p.Provider, p.Window, resetText(p.ResetsAt, now))
}

// burnedBy says who burns the provider: "groups builders, nightshift" when
// every user is a group, else each user as doctor names it, with the kinds:
// "the yard's default, crew member scribe (kinds claude, kimi)".
func (p providerLimits) burnedBy() string {
	who := strings.Join(p.BurnedBy, ", ")
	if names, ok := groupNames(p.BurnedBy); ok {
		who = "group " + strings.Join(names, ", ")
		if len(names) > 1 {
			who = "groups " + strings.Join(names, ", ")
		}
	}
	kinds := "kind " + strings.Join(p.Kinds, ", ")
	if len(p.Kinds) > 1 {
		kinds = "kinds " + strings.Join(p.Kinds, ", ")
	}
	return who + " (" + kinds + ")"
}

// groupNames are the bare names of by, when every user is a group.
func groupNames(by []string) ([]string, bool) {
	var out []string
	for _, b := range by {
		name, ok := strings.CutPrefix(b, "group ")
		if !ok || name == "" {
			return nil, false
		}
		out = append(out, name)
	}
	return out, len(out) > 0
}

// usedText is how far a window is used, as the provider said it, with the
// share of it that has passed where the provider said that too.
func usedText(w limits.Window) string {
	count := ""
	if w.Used != nil && w.Limit != nil {
		count = fmt.Sprintf("%d of %d", *w.Used, *w.Limit)
	}
	detail := count
	if w.TargetPercent != nil {
		target := "target " + percent(*w.TargetPercent) + "%"
		if detail == "" {
			detail = target
		} else {
			detail += ", " + target
		}
	}
	with := func(s string) string {
		if detail == "" {
			return s
		}
		return s + " (" + detail + ")"
	}
	switch {
	case w.UsedPercent != nil:
		return with(percent(*w.UsedPercent) + "% used")
	case count != "":
		return with(count + " used")
	case w.TargetPercent != nil:
		return "use not known (" + detail + ")"
	}
	return "use not known"
}

func percent(p float64) string { return strconv.FormatFloat(p, 'f', -1, 64) }

// resetText is when a window resets and in how long: "resets 00:02 (in
// 42m)", with the day for a time that is not today.
func resetText(at *time.Time, now time.Time) string {
	if at == nil {
		return "reset not known"
	}
	when := limits.Clock(*at, now)
	if d := at.Sub(now); d >= 0 {
		return fmt.Sprintf("resets %s (in %s)", when, limits.Span(d))
	}
	// The provider's word, kept: it has not seen the window open yet.
	return fmt.Sprintf("was to reset %s (%s ago)", when, limits.Span(now.Sub(*at)))
}

// limitsPart is doctor's line on limits: which source says the limits of
// which harness kinds, with what to know about it. It asks no provider
// unless try: the real ask is slow, and is a person's to want.
func limitsPart(ctx context.Context, cfg *config.Config, st store.API, uses []limitUse, try bool) (optionalPart, error) {
	yardr := yardrAt(cfg.Home)
	part := optionalPart{Key: "limits", Title: "limits",
		Detail: fmt.Sprintf("no source says what a harness kind's subscription still carries, so a usage limit is known only by the harness stopping on it; "+
			"a source with a limits key says it (%s source --help, docs/integrations.md)", yardr)}
	as, err := limits.Answerers(ctx, st, cfg.Home)
	if err != nil {
		return part, err
	}
	groups, unmapped := groupUses(as, uses)
	// A route's condition on a group no source answers for gates nothing,
	// with a source or without one.
	routes, err := st.Routes(ctx)
	if err != nil {
		return part, err
	}
	all, err := st.Groups(ctx)
	if err != nil {
		return part, err
	}
	gated := routeNotes(as, routes, all, defaultKind(ctx, st))
	if len(as) == 0 {
		for _, g := range groups {
			for _, by := range g.by {
				part.Notes = append(part.Notes, fmt.Sprintf("%s names provider %s and no source answers for it (limits.kinds)", by, g.name))
			}
		}
		part.Notes = append(part.Notes, gated...)
		return part, nil
	}
	part.Configured = true
	part.Detail = answeredKinds(as)
	if !try {
		part.Detail += fmt.Sprintf("; not asked (%s limits asks, and %s doctor --try)", yardr, yardr)
	}
	for _, a := range as {
		if err := a.Missing(); err != nil {
			part.Notes = append(part.Notes, fmt.Sprintf("source %s gives no answer as this command finds it: %v. The daemon's PATH is its own: name the program by its whole path", a.Source, err))
		}
	}
	for _, g := range groups {
		if !g.answered {
			for _, by := range g.by {
				part.Notes = append(part.Notes, fmt.Sprintf("%s names provider %s and no source answers for it (limits.kinds)", by, g.name))
			}
			continue
		}
		if !try {
			continue
		}
		p := askProvider(ctx, g)
		who := strings.Join(g.by, ", ")
		if p.Unknown != "" {
			part.Notes = append(part.Notes, fmt.Sprintf("tried: provider %s (%s) is not known: %s", g.name, who, p.Unknown))
		} else {
			part.Notes = append(part.Notes, fmt.Sprintf("tried: %s answered for provider %s (%s): %s; %s",
				g.answerer.Source, g.name, who, plural(len(p.Limits.Windows), "window"), p.summary(time.Now())))
		}
	}
	for _, u := range unmapped {
		part.Notes = append(part.Notes, fmt.Sprintf("%s is in use (%s) and no source says its limits (limits.kinds)", u.kind, strings.Join(u.by, ", ")))
	}
	for _, a := range as {
		if !a.Limits.Self() || !try {
			continue
		}
		s := askSource(ctx, a)
		if s.Unknown != "" {
			part.Notes = append(part.Notes, fmt.Sprintf("tried: source %s is not known: %s", answererName(a), s.Unknown))
		} else {
			part.Notes = append(part.Notes, fmt.Sprintf("tried: %s answered for itself: %s; %s",
				answererName(a), plural(len(s.Limits.Windows), "window"), s.summary(time.Now())))
		}
	}
	part.Notes = append(part.Notes, gated...)
	return part, nil
}

// checkLimits is the part of yardr source check for a source that says
// limits: one ask per kind its file lists, or one ask of the source where
// it answers for itself. It reports whether every ask was answered.
func checkLimits(ctx context.Context, a limits.Answerer) (asked any, ok bool) {
	if a.Limits.Self() {
		s := askSource(ctx, a)
		return []sourceLimits{s}, s.Unknown == ""
	}
	ok = true
	var kinds []kindLimits
	for _, kind := range a.Limits.KindNames() {
		k := askLimits(ctx, a.Ask, kind)
		ok = ok && k.Unknown == ""
		kinds = append(kinds, k)
	}
	return kinds, ok
}

// checkLines are what source check's asks came to, for a person.
func checkLines(asked any, now time.Time) []string {
	var out []string
	switch a := asked.(type) {
	case []kindLimits:
		for _, k := range a {
			out = append(out, k.lines(now)...)
		}
	case []sourceLimits:
		for _, s := range a {
			out = append(out, s.lines(now)...)
		}
	}
	return out
}
