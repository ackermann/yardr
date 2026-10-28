// The telemetry switch as the yard's socket and listener serve it: what
// yardr telemetry on, off and show do, for the operator's device and the
// web view. The switch itself is the telemetry package's, which the CLI
// calls; the store cannot call it directly (telemetry imports the store),
// so the owning process registers it (SetTelemetrySwitch) and these
// methods delegate to it. Consent bookkeeping is the CLI's exactly,
// because it is the same functions, and the setting writes through the
// same ActChangeSetting gate, so a refused actor changes nothing local
// either.
package store

import (
	"context"
	"fmt"
	"time"

	"yardr.dev/yardr/internal/config"
)

// TelemetryChange is what TelemetrySet did: the same object yardr
// telemetry on and off print.
type TelemetryChange struct {
	Switch       string `json:"telemetry"` // "on" or "off"
	Changed      bool   `json:"changed"`
	InstallID    string `json:"install_id,omitempty"`
	Since        string `json:"since,omitempty"` // latest grant's UTC date
	EligibleFrom string `json:"eligible_from,omitempty"`
	Endpoint     string `json:"endpoint"`
}

// TelemetryEvent is one allow-listed event of the next request's batch:
// counts and fixed names only, never a bead, an error's words or an
// actor's name.
type TelemetryEvent struct {
	Name       string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Timestamp  string         `json:"timestamp"`
	UUID       string         `json:"uuid"`
	Properties map[string]any `json:"properties"`
}

// TelemetryRequest is the exact next request: the shared public project
// key and the batch.
type TelemetryRequest struct {
	APIKey string           `json:"api_key"`
	Batch  []TelemetryEvent `json:"batch"`
}

// TelemetryPreview is what TelemetryShow answers: the same object yardr
// telemetry show prints. Request is nil when there is no batch, and
// NoBatch says why: "off", "attempted", "empty" or "no-id".
type TelemetryPreview struct {
	InstallID  string            `json:"install_id,omitempty"`
	Switch     string            `json:"telemetry"` // "on" or "off"
	Since      string            `json:"since,omitempty"`
	SendingDay string            `json:"sending_day"`
	SourceDays []string          `json:"source_days,omitempty"`
	Hosted     bool              `json:"hosted"`
	Request    *TelemetryRequest `json:"request"`
	NoBatch    string            `json:"no_batch,omitempty"`
}

// TelemetrySwitch serves the telemetry switch to the store's API: on,
// off and show through the one implementation. The process that owns
// the store registers it, as it registers how that process reads a
// pane's screen (SetPaneReader); a store no server owns has none.
type TelemetrySwitch interface {
	TelemetryOn(ctx context.Context, st API, statePath, actor string, now time.Time) (*TelemetryChange, error)
	TelemetryOff(ctx context.Context, st API, statePath, actor string, now time.Time) (*TelemetryChange, error)
	TelemetryShow(ctx context.Context, st API, statePath string, now time.Time) (*TelemetryPreview, error)
}

// SetTelemetrySwitch tells the store how its owning process turns
// telemetry on and off and previews the next request.
func (s *Store) SetTelemetrySwitch(sw TelemetrySwitch) { s.telemetry = sw }

// telemetryState is where this yard keeps the install id and its consent
// bookkeeping: the same file the CLI reads, beside the store.
func (s *Store) telemetryState() string { return config.At(s.home).TelemetryState() }

// TelemetryShow previews the install id and the exact request the daemon
// would send from the current state, as yardr telemetry show prints it.
// It sends nothing and advances nothing. It is the operator's call
// (rpc.OperatorCalls): on the socket its Operator class keeps it from
// agent and crew panes, and on the listener it is there for a device of
// the operator and for no other caller.
func (s *Store) TelemetryShow(ctx context.Context) (*TelemetryPreview, error) {
	if s.telemetry == nil {
		return nil, fmt.Errorf("telemetry: no server has registered the telemetry switch on this store")
	}
	return s.telemetry.TelemetryShow(ctx, s, s.telemetryState(), time.Now())
}

// TelemetrySet turns telemetry on or off, as yardr telemetry on and off
// do: through the same functions, so consent bookkeeping
// (eligible_from, grant and withdrawal times) is exactly the CLI's, and
// through the same ActChangeSetting gate, so anyone but the operator,
// and any call from an agent's pane, is refused and changes nothing. It
// is the operator's call (rpc.OperatorCalls): on the listener a device
// of an operator's, and never down the link from the edge.
func (s *Store) TelemetrySet(ctx context.Context, on bool, actor string) (*TelemetryChange, error) {
	if s.telemetry == nil {
		return nil, fmt.Errorf("telemetry: no server has registered the telemetry switch on this store")
	}
	if on {
		return s.telemetry.TelemetryOn(ctx, s, s.telemetryState(), actor, time.Now())
	}
	return s.telemetry.TelemetryOff(ctx, s, s.telemetryState(), actor, time.Now())
}
