package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/telemetry"
)

// telemetryQuestions is the four questions the daily batch answers, as
// the on and off help describe them.
const telemetryQuestions = `When this build includes the telemetry sender and telemetry is on, the daemon sends one batch a day to ` + telemetry.Endpoint + ` answering four questions: which yardr versions and OS are running; which harnesses and models do the work; where beads fail, as counts by kind; whether yards keep running after 1, 7 and 30 days.`

// telemetryProfile is the id-only PostHog profile, as the on and off
// help describe it.
const telemetryProfile = `The batch carries the yard's random install id as PostHog's distinct_id, and PostHog keeps an id-only person profile for it — no name, no email — so a yard's data is deleted by install id.`

func (a *app) telemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "yardr's opt-in telemetry: one daily batch of counts, on or off",
		Long: `yardr's opt-in telemetry: one daily batch of counts and fixed names, on or off with one switch.

Off unless turned on. ` + telemetryQuestions + `
` + telemetryProfile + ` yardr telemetry show prints the exact next batch before anything is sent.

` + telemetry.IDSentence,
	}
	on := &cobra.Command{
		Use:   "on",
		Short: "Turn telemetry on, minting the install id when there is none",
		Long: `Turn telemetry on.

` + telemetryQuestions + `
` + telemetryProfile + `

On mints the install id first when none exists, records this consent
grant, and counts only whole days from tomorrow on, so activity from
before consent is never included. The id is kept across off and on
again, so the yard stays the same yard; deleting <home>/telemetry.json
forgets it, and the next on mints a new one.

` + telemetry.IDSentence,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			change, err := telemetry.On(c.Context(), st, cfg.TelemetryState(), a.who(), time.Now())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), change, func(w io.Writer) { printTelemetryChange(w, change) })
		},
	}
	off := &cobra.Command{
		Use:   "off",
		Short: "Turn telemetry off: nothing is sent",
		Long: `Turn telemetry off: nothing is sent.

` + telemetryQuestions + `
` + telemetryProfile + `

Off records the withdrawal and stops new batches from starting; a
batch already submitted may still complete. The install id is kept,
so on again is the same yard; deleting <home>/telemetry.json forgets
it, and the next on mints a new one.

` + telemetry.IDSentence,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			change, err := telemetry.Off(c.Context(), st, cfg.TelemetryState(), a.who(), time.Now())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), change, func(w io.Writer) { printTelemetryChange(w, change) })
		},
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the install id and the exact next batch, sending nothing",
		Long: `Print the install id and the exact request the daemon would send from the current state, without sending it or advancing anything.

A hosted yard with no eligible counts and no running crew has no
request to preview. When telemetry is off, or a batch was already
attempted today, there is no batch until it is enabled or the next UTC
day. No pre-consent day is ever previewed as a future send.

` + telemetry.IDSentence + `
To have this yard's data deleted, send the project this install id.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			cfg, err := a.config()
			if err != nil {
				return err
			}
			preview, err := telemetry.Show(c.Context(), st, cfg.TelemetryState(), time.Now())
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), preview, func(w io.Writer) { printTelemetryShow(w, preview) })
		},
	}
	cmd.AddCommand(on, off, show)
	return cmd
}

// printTelemetryChange prints what on and off did: what changed, where
// batches go, and the install id.
func printTelemetryChange(w io.Writer, change *telemetry.Change) {
	status := "telemetry: " + change.Switch
	if !change.Changed {
		status = "telemetry: already " + change.Switch
	}
	if change.Switch == "on" {
		status += " (since " + change.Since + ")"
	} else {
		status += " — nothing is sent"
	}
	fmt.Fprintln(w, status)
	fmt.Fprintln(w, "batches go to "+change.Endpoint+" once a day from the daemon")
	printInstallID(w, change.InstallID)
}

// printTelemetryShow prints the install id and the next request, or why
// there is no batch.
func printTelemetryShow(w io.Writer, p *telemetry.Preview) {
	if p.Request != nil {
		printInstallID(w, p.InstallID)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "telemetry: on (since "+p.Since+")")
		fmt.Fprintln(w, "next request: sending day "+p.SendingDay+" (UTC); "+sourceDaysSay(p.SourceDays))
		data, _ := json.MarshalIndent(p.Request, "", "  ")
		fmt.Fprintln(w, string(data))
		fmt.Fprintln(w)
		fmt.Fprintln(w, "To have this yard's data deleted, send the project this install id.")
		return
	}
	if p.Switch == "on" && p.Since != "" {
		fmt.Fprintln(w, "telemetry: on (since "+p.Since+")")
	} else if p.Switch == "on" {
		fmt.Fprintln(w, "telemetry: on")
	} else {
		fmt.Fprintln(w, "telemetry: off — nothing is sent")
	}
	fmt.Fprintln(w, noBatchSay(p.NoBatch))
	printInstallID(w, p.InstallID)
	if p.InstallID != "" {
		fmt.Fprintln(w, "To have this yard's data deleted, send the project this install id.")
	}
}

// printInstallID prints the install id with the pseudonymous sentence,
// or that none exists yet.
func printInstallID(w io.Writer, id string) {
	if id == "" {
		fmt.Fprintln(w, "install id: none yet")
		return
	}
	fmt.Fprintln(w, "install id: "+id)
	fmt.Fprintln(w, telemetry.IDSentence)
}

// sourceDaysSay is the source days of the next request line.
func sourceDaysSay(days []string) string {
	switch len(days) {
	case 0:
		return "no eligible source days"
	case 1:
		return "source day " + days[0]
	default:
		return "source days " + days[0] + ".." + days[len(days)-1]
	}
}

// noBatchSay is why show has no batch to preview.
func noBatchSay(reason string) string {
	switch reason {
	case telemetry.NoBatchOff:
		return "no batch available until telemetry is enabled"
	case telemetry.NoBatchAttempted:
		return "no batch available until the next UTC day"
	case telemetry.NoBatchEmpty:
		return "no batch available: nothing to send"
	default:
		return "no batch available: no install id (run yardr telemetry on again)"
	}
}
