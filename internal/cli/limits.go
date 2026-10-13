package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// kindLimits is what one ask came to, for one harness kind: the provider's
// report and what the yard reads from it, or why nothing is known.
type kindLimits struct {
	Kind string `json:"kind"`
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

func (a *app) limitsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "limits [kind]",
		Short: "What the subscriptions the yard burns still carry: their windows, how far each is used, when each resets",
		Long: `Asks the source of the yard that says limits (a declared source with a
limits key: yardr source --help) what the subscriptions the yard burns
still carry, and prints each window of their usage limits: how far it is
used, when it resets and in how long, and whether it is used up. With a
kind it asks for that one; without, every kind a group, a crew member or
the yard's default_kind names is resolved to its provider — its provider
setting where one is set, else the kinds table's mapping of the kind — and
each provider is asked once, naming who burns it.

  yardr limits kimi
  kimi  aiquokka says (provider Kimi):
    5h      100% used (100 of 100)  resets 00:02 (in 42m)             used up
    Weekly  38% used (38 of 100)    resets Sat 10 Oct 09:02 (in 3d 9h)
    an agent of kind kimi waits for the 5h window, which resets 00:02 (in 42m)

  yardr limits
  kimi  aiquokka says (provider Kimi): burned by groups builders, nightshift (kind kimi)
    5h      100% used (100 of 100)  resets 00:02 (in 42m)             used up
    an agent burning kimi waits for the 5h window, which resets 00:02 (in 42m)

Every run asks: the source's command is run once per provider, once for
the kind with one named, now, by this command and as whoever runs it, for
at most ` + limits.Timeout.String() + ` each. Nothing is remembered between two runs. A
provider may be slow and may be bounded in how often it answers, so this
is a command for when you want to know, not one to run in a loop.

The answer is for the login the provider reads on this machine, which is
the operator's. A group or crew member whose env_file gives its harness
another account is not described by it.

A provider is "not known", with the reason, when no source of the yard
names it or when its command gave no answer: another exit status than 0,
nothing within the bound, or output that is not the JSON the yard reads.
A kind that resolves to nothing is not asked, and is named under unmapped.
The exit status is then 1. --json prints, per provider, the burning kinds
and users beside the report under limits, the window that is used up and
resets last under window and resets_at, or the reason under
limits_unknown.`,
		Example: `  yardr limits
  yardr limits kimi
  yardr limits kimi --json | jq -r '.limits[0].resets_at'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			st, err := a.store(ctx)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				asked := []kindLimits{askLimits(ctx, func(ctx context.Context, kind string) (*limits.Report, error) {
					return limits.Ask(ctx, st, cfg.Home, kind)
				}, args[0])}
				if err := a.out(map[string]any{"limits": asked, "unmapped": []string{}}, func() {
					now := time.Now()
					for _, k := range asked {
						for _, l := range k.lines(now) {
							fmt.Println(l)
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
			if len(as) == 0 && len(groups) == 0 {
				return a.out(map[string]any{"limits": []providerLimits{}, "unmapped": unmapped}, func() {
					fmt.Println("No source of this yard says limits: nothing is known, and nothing was asked.")
					fmt.Println("A source says them by a limits key in its file (yardr source --help, docs/integrations.md).")
				})
			}
			asked := []providerLimits{}
			for _, g := range groups {
				asked = append(asked, askProvider(ctx, g))
			}
			if len(asked) == 0 {
				return a.out(map[string]any{"limits": asked, "unmapped": unmapped}, func() {
					fmt.Printf("Nothing was asked: no group, crew member or default_kind of this yard names a kind a source says the limits of (%s).\n", answeredKinds(as))
					fmt.Println("yardr limits <kind> asks for one.")
				})
			}
			if err := a.out(map[string]any{"limits": asked, "unmapped": unmapped}, func() {
				now := time.Now()
				for _, p := range asked {
					for _, l := range p.lines(now) {
						fmt.Println(l)
					}
				}
				if len(unmapped) > 0 {
					fmt.Printf("not asked: %s, in use here and listed by no source (limits.kinds)\n", strings.Join(unmapped, ", "))
				}
			}); err != nil {
				return err
			}
			if slices.ContainsFunc(asked, func(p providerLimits) bool { return p.Unknown != "" }) {
				return errCheckFailed // the lines said which and why
			}
			return nil
		},
	}
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
// claude, codex, kimi".
func answeredKinds(as []limits.Answerer) string {
	parts := make([]string, len(as))
	for i, a := range as {
		parts[i] = a.Source + " for " + strings.Join(a.Limits.KindNames(), ", ")
	}
	return strings.Join(parts, "; ")
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
	out := []string{head + "):"}
	if len(r.Windows) == 0 {
		return append(out, "  no window: the provider knows of no limit")
	}
	label, used := 0, 0
	for _, w := range r.Windows {
		label, used = max(label, len(w.Label)), max(used, len(usedText(w)))
	}
	for _, w := range r.Windows {
		line := fmt.Sprintf("  %-*s  %-*s  %s", label, w.Label, used, usedText(w), resetText(w.ResetsAt, now))
		if w.UsedUp() {
			line += "  used up"
		}
		out = append(out, line)
	}
	return append(out, "  "+k.summary(now))
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
	out := []string{head + "): burned by " + p.burnedBy()}
	if len(r.Windows) == 0 {
		return append(out, "  no window: the provider knows of no limit")
	}
	label, used := 0, 0
	for _, w := range r.Windows {
		label, used = max(label, len(w.Label)), max(used, len(usedText(w)))
	}
	for _, w := range r.Windows {
		line := fmt.Sprintf("  %-*s  %-*s  %s", label, w.Label, used, usedText(w), resetText(w.ResetsAt, now))
		if w.UsedUp() {
			line += "  used up"
		}
		out = append(out, line)
	}
	return append(out, "  "+p.summary(now))
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

// usedText is how far a window is used, as the provider said it.
func usedText(w limits.Window) string {
	count := ""
	if w.Used != nil && w.Limit != nil {
		count = fmt.Sprintf("%d of %d", *w.Used, *w.Limit)
	}
	switch {
	case w.UsedPercent != nil && count != "":
		return fmt.Sprintf("%s%% used (%s)", percent(*w.UsedPercent), count)
	case w.UsedPercent != nil:
		return percent(*w.UsedPercent) + "% used"
	case count != "":
		return count + " used"
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
	gated := routeNotes(as, routes, all)
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
	part.Notes = append(part.Notes, gated...)
	return part, nil
}

// checkLimits is the part of yardr source check for a source that says
// limits: one ask per kind its file lists. It reports whether every kind
// was answered.
func checkLimits(ctx context.Context, a limits.Answerer) (asked []kindLimits, ok bool) {
	ok = true
	for _, kind := range a.Limits.KindNames() {
		k := askLimits(ctx, a.Ask, kind)
		ok = ok && k.Unknown == ""
		asked = append(asked, k)
	}
	return asked, ok
}
