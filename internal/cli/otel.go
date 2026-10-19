package cli

import (
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/otel"
	"yardr.dev/yardr/internal/version"
)

func (a *app) otelCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "otel", Short: "Send the yard's telemetry to an OpenTelemetry collector"}
	var endpoint, traces, logs, state string
	var headers []string
	var after uint64
	var once, includeText bool
	export := &cobra.Command{
		Use:   "export",
		Short: "Export events as OTLP traces and logs over HTTP",
		Long: `Read the yard's events, as yardr watch does, and post them to an
OpenTelemetry collector as OTLP/HTTP JSON: every event as a log record, and
for each bead a trace, with a span for each stay in a stage and a child span
for each session in it. A span is sent when it ends.

The collector is the standard OTEL_EXPORTER_OTLP_ENDPOINT (spans go to
<endpoint>/v1/traces, logs to <endpoint>/v1/logs) with the headers of
OTEL_EXPORTER_OTLP_HEADERS, or OTEL_EXPORTER_OTLP_TRACES_ENDPOINT and
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT for a full URL per signal; the flags say
the same and win. With no endpoint at all the command says so and does
nothing.

The export only reads the yard. What it has sent it keeps in
<home>/otel-export.json and continues from there on the next run; a first
run sends every retained event. A collector that is down is waited for.

Only ids and names are sent: beads, stages, groups, sessions, actors.
--include-text adds bead titles to spans and the plain values of an event's
data (titles, reasons, mail text, setting values) to its log record. Bodies,
notes and prompts are never sent. See docs/otel.md.`,
		Example: `  OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 yardr otel export
  yardr otel export --endpoint http://localhost:4318 --header 'Authorization=Bearer t0ken'
  yardr otel export --endpoint http://localhost:4318 --once   # send what there is, then stop`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			// Before the store: with nowhere to send to, no server is started.
			x := &otel.Exporter{
				Traces: otel.SignalURL(or(endpoint, os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
					or(traces, os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT")), "traces"),
				Logs: otel.SignalURL(or(endpoint, os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")),
					or(logs, os.Getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT")), "logs"),
				After: after, Once: once, Text: includeText, Version: version.Current(),
				Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
			}
			if x.Traces == "" && x.Logs == "" {
				fmt.Fprintln(c.ErrOrStderr(), "yardr otel export: no collector: set OTEL_EXPORTER_OTLP_ENDPOINT or give --endpoint; nothing is exported")
				return nil
			}
			var err error
			if x.Headers, err = otel.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS")); err != nil {
				return fmt.Errorf("OTEL_EXPORTER_OTLP_HEADERS: %w", err)
			}
			given, err := otel.ParseHeaders(strings.Join(headers, ","))
			if err != nil {
				return fmt.Errorf("--header: %w", err)
			}
			for k, v := range given {
				x.Headers[k] = v
			}
			ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if x.Store, err = a.store(ctx); err != nil {
				return err
			}
			if x.State = state; x.State == "" {
				x.State = a.cfg.OTelState()
			}
			defer kindOf(svcOTel).announced(a.cfg, x.Log, map[string]any{"state": x.State})()
			x.Log.Info("exporting the yard", "traces", x.Traces, "logs", x.Logs, "state", x.State)
			return x.Run(ctx)
		},
	}
	export.Flags().StringVar(&endpoint, "endpoint", "", "OTLP/HTTP endpoint for both signals (default $OTEL_EXPORTER_OTLP_ENDPOINT)")
	export.Flags().StringVar(&traces, "traces-endpoint", "", "full URL for spans (default $OTEL_EXPORTER_OTLP_TRACES_ENDPOINT, else <endpoint>/v1/traces)")
	export.Flags().StringVar(&logs, "logs-endpoint", "", "full URL for log records (default $OTEL_EXPORTER_OTLP_LOGS_ENDPOINT, else <endpoint>/v1/logs)")
	export.Flags().StringArrayVar(&headers, "header", nil, "a request header as key=value, repeatable (added to $OTEL_EXPORTER_OTLP_HEADERS)")
	export.Flags().StringVar(&state, "state", "", "the file the export resumes from (default <home>/otel-export.json)")
	export.Flags().Uint64Var(&after, "after", 0, "on a first run, start after this event seq (default 0: every retained event)")
	export.Flags().BoolVar(&once, "once", false, "send what there is and stop")
	export.Flags().BoolVar(&includeText, "include-text", false, "also send bead titles and the plain values of event data")
	cmd.AddCommand(export)
	a.longRunningCmds(cmd, export, kindOf(svcOTel))
	return cmd
}
