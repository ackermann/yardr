package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/telemetry"
)

func (a *app) telemetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "yardr's opt-in telemetry: one daily batch of counts, on or off",
		Long: `yardr's opt-in telemetry: one daily batch of counts and fixed names, on or off with one switch.

Off unless turned on. ` + telemetry.Questions + `
` + telemetry.Profile + ` yardr telemetry show prints the exact next batch before anything is sent.

` + telemetry.IDSentence,
	}
	on := &cobra.Command{
		Use:   "on",
		Short: "Turn telemetry on, minting the install id when there is none",
		Long: `Turn telemetry on.

` + telemetry.Questions + `
` + telemetry.Profile + `

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
			change, err := telemetry.On(c.Context(), st, store.TelemetryState(cfg.Home), a.who(), time.Now())
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

` + telemetry.Questions + `
` + telemetry.Profile + `

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
			change, err := telemetry.Off(c.Context(), st, store.TelemetryState(cfg.Home), a.who(), time.Now())
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
			preview, err := telemetry.Show(c.Context(), st, store.TelemetryState(cfg.Home), time.Now())
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
		fmt.Fprintln(w, "next request: sending day "+p.SendingDay+" (UTC); "+telemetry.SourceDaysSay(p.SourceDays))
		data, _ := json.MarshalIndent(p.Request, "", "  ")
		fmt.Fprintln(w, string(data))
		fmt.Fprintln(w)
		fmt.Fprintln(w, telemetry.DeletionSentence)
		return
	}
	if p.Switch == "on" && p.Since != "" {
		fmt.Fprintln(w, "telemetry: on (since "+p.Since+")")
	} else if p.Switch == "on" {
		fmt.Fprintln(w, "telemetry: on")
	} else {
		fmt.Fprintln(w, "telemetry: off — nothing is sent")
	}
	fmt.Fprintln(w, telemetry.NoBatchSay(p.NoBatch))
	printInstallID(w, p.InstallID)
	if p.InstallID != "" {
		fmt.Fprintln(w, telemetry.DeletionSentence)
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

// askTelemetry is the onboarding's one telemetry question, asked after the
// yard exists and answered either way: Yes goes through the same consent
// activation as yardr telemetry on, and No writes the off setting, so a
// completed onboarding never asks again. A yard whose setting is already
// set is not asked: a resumed onboarding skips the question, and so does
// one the operator answered by hand. Non-interactive init never reaches
// here, and leaves the switch off. An answer prints nothing, as the
// form's other confirms do; the question carries what telemetry is, the
// four questions, where batches go, the pseudonymous id and its id-only
// profile, that show previews the exact next batch, and that off prevents
// new requests from starting. A form left with ctrl+c records nothing:
// an aborted consent is not consent, and the onboarding goes on.
func (a *app) askTelemetry(ctx context.Context, cfg *config.Config) error {
	st, err := a.store(ctx)
	if err != nil {
		return err
	}
	stored, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	if _, set := stored[store.TelemetrySetting]; set {
		return nil
	}
	on := false
	err = ask(huh.NewConfirm().Key("telemetry").Title("Turn telemetry on?").
		Description(telemetry.Questions + " " + telemetry.Profile + " " + telemetry.IDSentence +
			" `yardr telemetry show` previews the exact allow-listed next batch from current state" +
			" before anything is sent. The daemon sends the first batch the day you turn it on; counts from earlier days are never sent." +
			" `yardr telemetry off` prevents new requests from starting.").
		Value(&on))
	if errors.Is(err, huh.ErrUserAborted) {
		return nil
	} else if err != nil {
		return err
	}
	if on {
		_, err = telemetry.On(ctx, st, store.TelemetryState(cfg.Home), a.who(), time.Now())
		return err
	}
	_, err = telemetry.Off(ctx, st, store.TelemetryState(cfg.Home), a.who(), time.Now())
	return err
}

// sayTelemetry names the telemetry switch after a non-interactive init,
// which asks nothing and leaves it off: where a terminal is available,
// the operator is told the command that turns it on. A yard that already
// decided, and --json, stay quiet. A hint that cannot be read is skipped,
// never an error: the yard is made.
func (a *app) sayTelemetry(w io.Writer, ctx context.Context, cfg *config.Config) {
	if a.json || !interactive() {
		return
	}
	st, err := a.store(ctx)
	if err != nil {
		return
	}
	stored, err := st.Settings(ctx)
	if err != nil {
		return
	}
	if _, set := stored[store.TelemetrySetting]; set {
		return
	}
	fmt.Fprintf(w, "Telemetry is off. `%s telemetry on` records consent for one daily batch of counts and fixed names (docs/telemetry.md).\n", yardrAt(cfg.Home))
}
