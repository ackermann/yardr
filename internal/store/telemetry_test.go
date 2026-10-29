package store_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/telemetry"
)

// telemetryYard is a throwaway yard with the operator jo and the CLI's own
// switch registered on its store: the home, the API and the owning store.
func telemetryYard(t *testing.T) (string, store.API, *store.Store) {
	t.Helper()
	ctx := context.Background()
	home := t.TempDir()
	api, own := storetest.NewOwnedIn(t, home)
	if _, err := api.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	if err := api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	own.SetTelemetrySwitch(telemetry.Switch{})
	return home, api, own
}

// The calls reach the registered switch: on flips the setting and writes
// telemetry.json beside the store, off flips it back keeping the id, and
// show previews the sending-day snapshot. The consent bookkeeping itself
// is the telemetry package's, pinned there.
func TestTelemetryCallsReachTheRegisteredSwitch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, api, _ := telemetryYard(t)
	set, err := api.TelemetrySet(ctx, true, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if set.Switch != "on" || !set.Changed || set.InstallID == "" || set.Endpoint == "" {
		t.Errorf("TelemetrySet(true) = %+v, want on with a minted id and endpoint", set)
	}
	if settings, err := api.Settings(ctx); err != nil || settings[store.TelemetrySetting] != "true" {
		t.Fatalf("settings = %v, %v", settings, err)
	}
	state, err := telemetry.LoadState(filepath.Join(home, "telemetry.json"))
	if err != nil || state == nil || state.InstallID != set.InstallID || len(state.Grants) != 1 {
		t.Fatalf("state = %+v, %v", state, err)
	}
	got, err := api.TelemetryShow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Switch != "on" || got.InstallID != set.InstallID || got.Request == nil ||
		len(got.Request.Batch) != 1 || got.Request.Batch[0].Name != telemetry.DailyEvent {
		t.Errorf("TelemetryShow = %+v, want on with the sending-day snapshot", got)
	}
	unset, err := api.TelemetrySet(ctx, false, "jo")
	if err != nil {
		t.Fatal(err)
	}
	if unset.Switch != "off" || !unset.Changed || unset.InstallID != set.InstallID {
		t.Errorf("TelemetrySet(false) = %+v, want off keeping the id", unset)
	}
	if settings, err := api.Settings(ctx); err != nil || settings[store.TelemetrySetting] != "false" {
		t.Fatalf("settings = %v, %v", settings, err)
	}
}

// TelemetrySet refuses anyone but the operator, and any call from an
// agent's pane whatever actor it names, and a refused actor changes
// nothing: the setting stands and no state file is written. Both calls
// on a store no server registered the switch on are refused naming the
// server.
func TestTelemetryRefusals(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	home, api, own := telemetryYard(t)
	if _, err := api.AddUser(ctx, "sam", "jo"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.TelemetrySet(ctx, true, "sam"); err == nil ||
		!strings.Contains(err.Error(), "may change a setting") {
		t.Errorf("TelemetrySet as sam = %v, want it refused as only the operator's", err)
	}
	if settings, err := api.Settings(ctx); err != nil {
		t.Fatal(err)
	} else if settings[store.TelemetrySetting] == "true" {
		t.Error("the refused set turned telemetry on")
	}
	if _, err := os.Stat(filepath.Join(home, "telemetry.json")); !os.IsNotExist(err) {
		t.Errorf("the refused set wrote the state file: %v", err)
	}
	// A bead's agent cannot turn its yard's telemetry on by naming the
	// operator: from its pane the actor is the pane's before the rule is
	// read. Through the owning store, where the caller stands as named.
	if err := api.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "jo"); err != nil {
		t.Fatal(err)
	}
	b, err := api.CreateBead(ctx, store.NewBead{Title: "Paint the shed"}, "jo")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := api.Claim(ctx, b.ID, "builders", "new", "jo")
	if err != nil {
		t.Fatal(err)
	}
	if err := api.MarkRunning(ctx, sess.ID, paneShell(t, "paint-new")); err != nil {
		t.Fatal(err)
	}
	pane := store.WithCaller(ctx, os.Getpid())
	if _, err := own.TelemetrySet(pane, true, "jo"); err == nil ||
		!strings.Contains(err.Error(), "may change a setting") {
		t.Errorf("TelemetrySet from the session's pane as jo = %v, want it refused", err)
	}
	if _, err := os.Stat(filepath.Join(home, "telemetry.json")); !os.IsNotExist(err) {
		t.Errorf("the pane's set wrote the state file: %v", err)
	}
	plain, _ := storetest.NewOwned(t)
	if _, err := plain.TelemetryShow(ctx); err == nil || !strings.Contains(err.Error(), "no server has registered the telemetry switch") {
		t.Errorf("TelemetryShow without a switch = %v, want it refused naming the server", err)
	}
	if _, err := plain.TelemetrySet(ctx, true, "jo"); err == nil || !strings.Contains(err.Error(), "no server has registered the telemetry switch") {
		t.Errorf("TelemetrySet without a switch = %v, want it refused naming the server", err)
	}
}
