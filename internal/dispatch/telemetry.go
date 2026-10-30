package dispatch

import (
	"context"
	"errors"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/telemetry"
)

// telemetry sends the yard's daily batch of counts and fixed names to the
// project the build compiles in, when the operator turned telemetry on
// (docs/telemetry.md; design 2026-10-28-yardr-fwhv-telemetry.md, 3.3). It
// reads the settings on every pass, as backups does, so a change holds from
// the next pass with no restart; off, it does nothing at all.
//
// A day gets one attempt at most. The attempt is written to the state file
// before the request begins, so a daemon that restarts mid-day makes no
// second one, and a batch the project did not take is not tried again until
// the next UTC day: one bounded call a day is the cost. The failure is told
// once, by its status and a class, never by the request, the answer, the
// install id or the key. The source-day cursor moves only when the project
// took the batch, so the days it did not take go again the next day with
// the same timestamps and UUIDs, which the project deduplicates.
//
// The batch is built by telemetry.Prepare, as yardr telemetry show builds
// it, so with no change between the two the project receives the request
// show printed.
func (d *Dispatcher) telemetry(ctx context.Context) {
	if d.Home == "" {
		return // no home, no state file: nothing is sent
	}
	// A pass that met no fault of its own ends the one said before, so a
	// fault that goes and comes back is said again, and one that stays is
	// said once.
	faulted := false
	defer func() {
		if !faulted {
			d.telemetryTold = ""
		}
	}()
	fault := func(what string, err error) {
		faulted = true
		d.telemetryFault(what, err)
	}
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		fault("reading the settings", err)
		return
	}
	on := settings[store.TelemetrySetting] == "true"
	if !on {
		return
	}
	path := store.TelemetryState(d.Home)
	state, err := telemetry.LoadState(path)
	if err != nil {
		fault("reading the state file", err)
		return
	}
	now := d.now()
	switch telemetry.NoBatchFor(on, state, now) {
	case "":
	case telemetry.NoBatchNoID:
		fault("telemetry is on but this yard has no install id; yardr telemetry on mints one", nil)
		return
	default:
		return // a batch was attempted today
	}
	next, err := telemetry.Prepare(ctx, d.Store, state, settings[store.HostedSetting] == "true", now)
	if err != nil {
		if ctx.Err() == nil {
			fault("building the batch", err)
		}
		return
	}
	if next.Request == nil {
		return // a hosted yard with nothing to count: no request, and no attempt spent
	}
	// Before the request, and on the file as it stands: see MarkAttempt.
	marked, err := telemetry.MarkAttempt(path, state.InstallID, now)
	if err != nil {
		fault("recording the attempt", err)
		return
	}
	if !marked {
		return
	}
	if n := len(next.Dropped); n > 0 {
		d.Log.Info("telemetry: eligible source days older than a batch holds are dropped",
			"from", next.Dropped[0], "to", next.Dropped[n-1])
	}
	url := d.TelemetryURL
	if url == "" {
		url = telemetry.Endpoint
	}
	if err := telemetry.Post(ctx, url, next.Request, d.TelemetryTimeout); err != nil {
		if ctx.Err() != nil {
			return // the daemon was told to stop; the day's attempt is spent
		}
		class, status := telemetry.ClassRequest, 0
		var refused *telemetry.Refused
		if errors.As(err, &refused) {
			class, status = refused.Class, refused.Status
		}
		d.Log.Warn("telemetry: batch not accepted", "status", status, "class", class)
		return
	}
	if n := len(next.SourceDays); n > 0 {
		if err := telemetry.MarkSent(path, state.InstallID, next.SourceDays[n-1]); err != nil {
			// The days go again tomorrow and deduplicate there.
			fault("advancing the source-day cursor", err)
		}
	}
	d.Log.Info("telemetry: batch sent", "events", len(next.Request.Batch), "source_days", len(next.SourceDays))
}

// telemetryFault says a fault of the pass's own, one the project had no
// part in (the state file cannot be read or written, the store does not
// answer), once until it is gone: said on every wake, a state file that
// cannot be written would fill the log. err is a local error, never a
// request or an answer.
func (d *Dispatcher) telemetryFault(what string, err error) {
	msg := what
	var args []any
	if err != nil {
		msg += ": " + err.Error()
		args = []any{"err", err}
	}
	if msg == d.telemetryTold {
		return
	}
	d.telemetryTold = msg
	d.Log.Warn("telemetry: "+what, args...)
}
