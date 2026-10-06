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

func (a *app) limitsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "limits [kind]",
		Short: "What the subscription of a harness kind still carries: its windows, how far each is used, when it resets",
		Long: `Asks the source of the yard that says limits (a declared source with a
limits key: yardr source --help) what the subscription behind a harness kind
still carries, and prints each window of its usage limit: how far it is
used, when it resets and in how long, and whether it is used up. With a
kind it asks for that one; without, for every kind a group, a crew member or
the yard's default_kind names that a source answers for.

  yardr limits kimi
  kimi  aiquokka says (provider Kimi):
    5h      100% used (100 of 100)  resets 00:02 (in 42m)             used up
    Weekly  38% used (38 of 100)    resets Sat 10 Oct 09:02 (in 3d 9h)
    an agent of kind kimi waits for the 5h window, which resets 00:02 (in 42m)

Every run asks: the source's command is run once per kind, now, by this
command and as whoever runs it, for at most ` + limits.Timeout.String() + ` each. Nothing is
remembered between two runs. A provider may be slow and may be bounded in
how often it answers, so this is a command for when you want to know, not
one to run in a loop.

The answer is for the login the provider reads on this machine, which is
the operator's. A group or crew member whose env_file gives its harness
another account is not described by it.

A kind is "not known", with the reason, when no source of the yard lists it
or when its command gave no answer: another exit status than 0, nothing
within the bound, or output that is not the JSON the yard reads. The exit
status is then 1. --json prints, per kind, the report under limits, the
window that is used up and resets last under window and resets_at, or the
reason under limits_unknown.`,
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
			ask := func(ctx context.Context, kind string) (*limits.Report, error) {
				return limits.Ask(ctx, st, cfg.Home, kind)
			}
			var asked []kindLimits
			unmapped := []string{}
			if len(args) == 1 {
				asked = append(asked, askLimits(ctx, ask, args[0]))
			} else {
				as, err := limits.Answerers(ctx, st, cfg.Home)
				if err != nil {
					return err
				}
				if len(as) == 0 {
					return a.out(map[string]any{"limits": []kindLimits{}, "unmapped": unmapped}, func() {
						fmt.Println("No source of this yard says limits: nothing is known, and nothing was asked.")
						fmt.Println("A source says them by a limits key in its file (yardr source --help, docs/integrations.md).")
					})
				}
				uses, err := kindsInUse(ctx, st)
				if err != nil {
					return err
				}
				for _, u := range uses {
					if answersFor(as, u.kind) == "" {
						unmapped = append(unmapped, u.kind)
						continue
					}
					asked = append(asked, askLimits(ctx, ask, u.kind))
				}
				if len(asked) == 0 {
					return a.out(map[string]any{"limits": asked, "unmapped": unmapped}, func() {
						fmt.Printf("Nothing was asked: no group, crew member or default_kind of this yard names a kind a source says the limits of (%s).\n", answeredKinds(as))
						fmt.Println("yardr limits <kind> asks for one.")
					})
				}
			}
			if err := a.out(map[string]any{"limits": asked, "unmapped": unmapped}, func() {
				now := time.Now()
				for _, k := range asked {
					for _, l := range k.lines(now) {
						fmt.Println(l)
					}
				}
				if len(unmapped) > 0 {
					fmt.Printf("not asked: %s, in use here and listed by no source (limits.kinds)\n", strings.Join(unmapped, ", "))
				}
			}); err != nil {
				return err
			}
			if slices.ContainsFunc(asked, func(k kindLimits) bool { return k.Unknown != "" }) {
				return errCheckFailed // the lines said which and why
			}
			return nil
		},
	}
}

// kindsInUse are the harness kinds the yard runs on: its default_kind and
// every kind a group or crew member names, as doctor lists them.
func kindsInUse(ctx context.Context, st store.API) ([]harnessUse, error) {
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return nil, err
	}
	return harnessUses(defaultKind(ctx, st), groups, crew), nil
}

// answersFor is the source among as that says the limits of kind, "" for none.
func answersFor(as []limits.Answerer, kind string) string {
	for _, a := range as {
		if _, ok := a.Limits.Kinds[kind]; ok {
			return a.Source
		}
	}
	return ""
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
	t := at.Local()
	when := t.Format("15:04")
	if y, m, d := t.Date(); !sameDay(y, m, d, now.Local()) {
		when = t.Format("Mon 2 Jan 15:04")
	}
	if d := t.Sub(now); d >= 0 {
		return fmt.Sprintf("resets %s (in %s)", when, span(d))
	}
	// The provider's word, kept: it has not seen the window open yet.
	return fmt.Sprintf("was to reset %s (%s ago)", when, span(now.Sub(t)))
}

func sameDay(y int, m time.Month, d int, t time.Time) bool {
	ty, tm, td := t.Date()
	return y == ty && m == tm && d == td
}

// span is a length of time in its two largest units: 42m, 3h 5m, 3d 9h.
func span(d time.Duration) string {
	d = d.Round(time.Minute)
	days, hours, mins := int(d/(24*time.Hour)), int(d/time.Hour)%24, int(d/time.Minute)%60
	switch {
	case days > 0 && hours > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case days > 0:
		return fmt.Sprintf("%dd", days)
	case hours > 0 && mins > 0:
		return fmt.Sprintf("%dh %dm", hours, mins)
	case hours > 0:
		return fmt.Sprintf("%dh", hours)
	case mins > 0:
		return fmt.Sprintf("%dm", mins)
	}
	return "under a minute"
}

// limitsPart is doctor's line on limits: which source says the limits of
// which harness kinds, with what to know about it. It asks no provider
// unless try: the real ask is slow, and is a person's to want.
func limitsPart(ctx context.Context, cfg *config.Config, st store.API, uses []harnessUse, try bool) (optionalPart, error) {
	yardr := yardrAt(cfg.Home)
	part := optionalPart{Key: "limits", Title: "limits",
		Detail: fmt.Sprintf("no source says what a harness kind's subscription still carries, so a usage limit is known only by the harness stopping on it; "+
			"a source with a limits key says it (%s source --help, docs/integrations.md)", yardr)}
	as, err := limits.Answerers(ctx, st, cfg.Home)
	if err != nil {
		return part, err
	}
	if len(as) == 0 {
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
	for _, u := range uses {
		src := answersFor(as, u.kind)
		if src == "" {
			part.Notes = append(part.Notes, fmt.Sprintf("%s is in use (%s) and no source says its limits (limits.kinds)", u.kind, strings.Join(u.by, ", ")))
			continue
		}
		if !try {
			continue
		}
		k := askLimits(ctx, func(ctx context.Context, kind string) (*limits.Report, error) {
			return limits.Ask(ctx, st, cfg.Home, kind)
		}, u.kind)
		if k.Unknown != "" {
			part.Notes = append(part.Notes, fmt.Sprintf("tried: %s is not known: %s", u.kind, k.Unknown))
		} else {
			part.Notes = append(part.Notes, fmt.Sprintf("tried: %s answered for %s: %s; %s", src, u.kind, plural(len(k.Limits.Windows), "window"), k.summary(time.Now())))
		}
	}
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
