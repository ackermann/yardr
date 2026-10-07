package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

// A timer is how a crew member or a user asks the yard to tell someone later
// (docs/timers.md). The records and their firing are the store's and the
// daemon's; this file is the commands and how a timer is printed.

func (a *app) timerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "timer", Short: "Ask the yard to tell you, or a user, something later, unless an event came first",
		Long: `A timer tells someone something at a later time: once (--in, --at) or again
and again (--every), and not at all when what it waited for came first
(--unless). It lives in the yard and the daemon fires it, so it outlives the
session that set it (docs/timers.md).

Prefer an event: a subscription or a bead's done (yardr bead watch <id>
--done) is heard when it happens, with no clock. A timer is the fallback for
when the event does not come, or for a check by the clock.

A crew member and a user set timers; an agent working a bead does not. A timer
is for whoever set it, or, set by a crew member, for a user (--to). To a
crew member it is an event of kind timer in its inbox, which it is nudged for
when idle. To a user it is mail from whoever set it, beginning "timer:", in
yardr inbox and yardr chat, and nobody is woken for it.

The yard's limits are settings (yardr settings get): timer_limit, the live
timers one setter may have (default 5), and timer_min_every, the shortest
--every (default 1h). A one-shot is at most 30d ahead. A recurring timer
runs 7 days unless --for says otherwise (at most 30d), and its last firing
says it was the last.`}

	var in, at, every, runs, to, unless string
	add := &cobra.Command{
		Use:   `add (--in <duration> | --at <time> | --every <duration>) [--to <user>] [--unless bead=<id>[,stage=<stage>]] "<reason>"`,
		Short: "Set a timer; the reason is what the receiver reads",
		Long: `Sets a timer. The reason is required: it is what the receiver reads when
the timer fires, with who set it and when.

--in 40m fires once, that long from now. --at fires once at a time: RFC 3339
(2026-10-06T09:00:00+02:00), or 15:04 for the next time the local clock
shows it. --every 1d fires each period, first one period from now, for 7
days or --for <duration>. Durations are Go's (90s, 40m, 12h) or whole days
(7d).

--unless bead=<id> drops the timer, with a timer_dropped event and nothing
for the receiver, when the bead closes before it fires. With ,stage=<stage>
it is dropped when the bead has entered that stage: it is there, was there,
is in a stage its flow orders after it, or is closed. A recurring timer with
--unless ends when that holds. A firing says what was awaited and where the
bead stands.`,
		Example: `  yardr timer add --in 40m "look at the deploy again"
  yardr timer add --in 30m --to mckean --unless bead=yardr-1a2b,stage=review "yardr-1a2b is stuck in new"
  yardr timer add --at 09:00 "read the night's CI"
  yardr timer add --every 1d --for 14d --unless bead=yardr-1a2b "yardr-1a2b is still open"`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			nt := store.NewTimer{To: to, Reason: args[0]}
			set := 0
			for _, v := range []string{in, at, every} {
				if v != "" {
					set++
				}
			}
			if set != 1 {
				return errors.New("timer add: say when with one of --in <duration>, --at <time> or --every <duration>")
			}
			now := time.Now()
			var err error
			switch {
			case in != "":
				d, err := store.ParseSpan(in)
				if err != nil {
					return fmt.Errorf("--in: %w", err)
				}
				nt.Due = now.Add(d)
			case at != "":
				if nt.Due, err = parseAt(at, now); err != nil {
					return err
				}
			default:
				if nt.Every, err = store.ParseSpan(every); err != nil {
					return fmt.Errorf("--every: %w", err)
				}
			}
			if runs != "" {
				if nt.For, err = store.ParseSpan(runs); err != nil {
					return fmt.Errorf("--for: %w", err)
				}
			}
			if nt.UnlessBead, nt.UnlessStage, err = parseUnless(unless); err != nil {
				return err
			}
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			t, err := st.AddTimer(c.Context(), nt, a.who())
			if err != nil {
				return err
			}
			return a.out(t, func() { fmt.Printf("timer %s set: %s\n", t.ID, timerLine(*t, a.who(), time.Now())) })
		},
	}
	f := add.Flags()
	f.StringVar(&in, "in", "", "fire once, this long from now: 40m, 12h, 2d")
	f.StringVar(&at, "at", "", "fire once at this time: RFC 3339, or 15:04 (the next such local time)")
	f.StringVar(&every, "every", "", "fire each period: 1h, 1d (no shorter than the timer_min_every setting)")
	f.StringVar(&runs, "for", "", "with --every: how long it runs (default 7d, at most 30d)")
	f.StringVar(&to, "to", "", "whom it tells: yourself (the default), or as a crew member a user of the yard")
	f.StringVar(&unless, "unless", "", "drop it when this came first: bead=<id> (it closed) or bead=<id>,stage=<stage> (it entered the stage)")

	var all bool
	list := &cobra.Command{
		Use: "list", Short: "List your live timers: those you set and those that tell you (--all: the yard's, for the operator)",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			name := a.who()
			if all {
				if err := a.operatorOnly(c.Context(), st, "list everyone's timers"); err != nil {
					return err
				}
				name = ""
			}
			ts, err := st.Timers(c.Context(), name)
			if err != nil {
				return err
			}
			return a.out(ts, func() {
				now := time.Now()
				rows := [][]string{}
				for _, t := range ts {
					unless := "-"
					if t.Unless != nil {
						unless = t.Unless.String()
					}
					rows = append(rows, []string{t.ID, t.By, t.To, timerDue(t, now), or(t.Every, "-"), unless, firstLine(t.Reason)})
				}
				table("ID\tBY\tTO\tDUE\tEVERY\tUNLESS\tREASON", rows)
			})
		},
	}
	list.Flags().BoolVar(&all, "all", false, "every timer of the yard (the operator's to ask)")

	rm := &cobra.Command{
		Use: "rm <id>", Short: "Remove a timer: one you set or that tells you, or as the operator any",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			t, err := st.RemoveTimer(c.Context(), args[0], a.who())
			if err != nil {
				return err
			}
			return a.out(t, func() { fmt.Printf("timer %s removed\n", t.ID) })
		},
	}
	cmd.AddCommand(add, list, rm)
	return cmd
}

// parseAt reads --at: RFC 3339, or 15:04 for the next time the local clock
// shows it after now.
func parseAt(s string, now time.Time) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	clock, err := time.ParseInLocation("15:04", s, now.Location())
	if err != nil {
		return time.Time{}, fmt.Errorf("--at %q: use RFC 3339 (%s) or 15:04", s, now.Format(time.RFC3339))
	}
	t := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, now.Location())
	if !t.After(now) {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

// parseUnless reads --unless: bead=<id>, or bead=<id>,stage=<stage>.
func parseUnless(s string) (bead, stage string, err error) {
	if s == "" {
		return "", "", nil
	}
	for _, part := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		switch {
		case !ok || v == "":
			err = fmt.Errorf("%q is no key=value", part)
		case k == "bead" && bead == "":
			bead = v
		case k == "stage" && stage == "":
			stage = v
		default:
			err = fmt.Errorf("%q: the keys are bead and stage, once each", part)
		}
		if err != nil {
			return "", "", fmt.Errorf("--unless %s: %w: use bead=<id> or bead=<id>,stage=<stage>", s, err)
		}
	}
	if bead == "" {
		return "", "", fmt.Errorf("--unless %s: a stage is some bead's: use bead=<id>,stage=<stage>", s)
	}
	return bead, stage, nil
}

// timerDue is when t fires next, as a time and how far off it is.
func timerDue(t store.Timer, now time.Time) string {
	due := t.Due.Local().Format(mailTime)
	if d := t.Due.Sub(now); d > 0 {
		return due + " (in " + roughly(d) + ")"
	}
	return due + " (due)"
}

// roughly rounds a wait to what a reader needs of it.
func roughly(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Round(time.Second)/time.Second)) + "s"
	case d < 48*time.Hour:
		return store.Span(d.Round(time.Minute))
	}
	return store.Span(d.Round(24 * time.Hour))
}

// timerLine is one live timer as a prime and the show commands list it,
// read by reader: whom it tells when that is someone else, when, what it
// waits for, and the reason.
func timerLine(t store.Timer, reader string, now time.Time) string {
	var b strings.Builder
	switch {
	case t.To != reader:
		fmt.Fprintf(&b, "tells %s ", t.To)
	case t.By != reader:
		fmt.Fprintf(&b, "set by %s, tells you ", t.By)
	}
	b.WriteString(timerDue(t, now))
	if t.Every != "" {
		fmt.Fprintf(&b, ", then every %s", t.Every)
		if t.Ends != nil {
			fmt.Fprintf(&b, " until %s", t.Ends.Local().Format(mailTime))
		}
	}
	if t.Unless != nil {
		fmt.Fprintf(&b, ", unless %s", t.Unless)
	}
	return b.String() + ": " + firstLine(t.Reason)
}

// printTimers lists name's live timers under indent, for the show
// commands; nothing when there are none.
func printTimers(ts []store.Timer, name, indent string) {
	now := time.Now()
	for _, t := range ts {
		fmt.Printf("%stimer %s %s\n", indent, t.ID, timerLine(t, name, now))
	}
}
