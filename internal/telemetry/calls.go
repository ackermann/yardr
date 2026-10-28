package telemetry

import (
	"context"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Switch serves On, Off and Show to the store's API (store.TelemetryShow
// and store.TelemetrySet): the yard's socket and listener share the
// CLI's functions rather than copy them, so consent bookkeeping is the
// same whichever way the switch is turned. The owning process registers
// it on its store (store.SetTelemetrySwitch).
type Switch struct{}

// TelemetryOn turns telemetry on through On.
func (Switch) TelemetryOn(ctx context.Context, st store.API, statePath, actor string, now time.Time) (*store.TelemetryChange, error) {
	change, err := On(ctx, st, statePath, actor, now)
	if err != nil {
		return nil, err
	}
	return &store.TelemetryChange{
		Switch:       change.Switch,
		Changed:      change.Changed,
		InstallID:    change.InstallID,
		Since:        change.Since,
		EligibleFrom: change.EligibleFrom,
		Endpoint:     change.Endpoint,
	}, nil
}

// TelemetryOff turns telemetry off through Off.
func (Switch) TelemetryOff(ctx context.Context, st store.API, statePath, actor string, now time.Time) (*store.TelemetryChange, error) {
	change, err := Off(ctx, st, statePath, actor, now)
	if err != nil {
		return nil, err
	}
	return &store.TelemetryChange{
		Switch:       change.Switch,
		Changed:      change.Changed,
		InstallID:    change.InstallID,
		Since:        change.Since,
		EligibleFrom: change.EligibleFrom,
		Endpoint:     change.Endpoint,
	}, nil
}

// TelemetryShow previews the next request through Show.
func (Switch) TelemetryShow(ctx context.Context, st store.API, statePath string, now time.Time) (*store.TelemetryPreview, error) {
	preview, err := Show(ctx, st, statePath, now)
	if err != nil {
		return nil, err
	}
	out := &store.TelemetryPreview{
		InstallID:  preview.InstallID,
		Switch:     preview.Switch,
		Since:      preview.Since,
		SendingDay: preview.SendingDay,
		SourceDays: preview.SourceDays,
		Hosted:     preview.Hosted,
		NoBatch:    preview.NoBatch,
	}
	if preview.Request != nil {
		out.Request = &store.TelemetryRequest{
			APIKey: preview.Request.APIKey,
			Batch:  make([]store.TelemetryEvent, len(preview.Request.Batch)),
		}
		for i, e := range preview.Request.Batch {
			out.Request.Batch[i] = store.TelemetryEvent{
				Name:       e.Name,
				DistinctID: e.DistinctID,
				Timestamp:  e.Timestamp,
				UUID:       e.UUID,
				Properties: e.Properties,
			}
		}
	}
	return out, nil
}
