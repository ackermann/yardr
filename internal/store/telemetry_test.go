package store_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// telemetryStatesEqual fails unless the two state files hold the same
// consent bookkeeping: the same minted day, the same number of grants
// and withdrawals within the minute of each other, and the same
// eligible day. The install ids are each yard's own mint and are
// compared only for being there.
func telemetryStatesEqual(t *testing.T, a, b *telemetry.State) {
	t.Helper()
	if a == nil || b == nil {
		t.Fatalf("states = %+v, %+v, want both there", a, b)
	}
	if a.InstallID == "" || b.InstallID == "" {
		t.Errorf("install ids = %q, %q, want both minted", a.InstallID, b.InstallID)
	}
	if a.Minted != b.Minted {
		t.Errorf("minted = %q, %q, want the same day", a.Minted, b.Minted)
	}
	stamps := func(what string, x, y []string) {
		t.Helper()
		if len(x) != len(y) {
			t.Fatalf("%s = %v, %v, want the same count", what, x, y)
		}
		for i := range x {
			tx, err := time.Parse("2006-01-02T15:04:05Z", x[i])
			if err != nil {
				t.Fatalf("%s %q does not parse: %v", what, x[i], err)
			}
			ty, err := time.Parse("2006-01-02T15:04:05Z", y[i])
			if err != nil {
				t.Fatalf("%s %q does not parse: %v", what, y[i], err)
			}
			if d := tx.Sub(ty); d < -time.Minute || d > time.Minute {
				t.Errorf("%s = %v, %v, want them within the minute", what, x, y)
			}
		}
	}
	stamps("grants", a.Grants, b.Grants)
	stamps("withdrawals", a.Withdrawals, b.Withdrawals)
	if a.EligibleFrom != b.EligibleFrom {
		t.Errorf("eligible_from = %q, %q, want the same day", a.EligibleFrom, b.EligibleFrom)
	}
}

// Turning the switch through the call keeps the same telemetry.json
// bookkeeping as yardr telemetry on and off: on mints and grants, off
// withdraws, and on again keeps the id and starts a new eligible day.
// Each step runs through TelemetrySet on one throwaway yard and through
// the CLI's functions on a twin, and the replies agree apart from the
// two mints.
func TestTelemetrySetMatchesTheCLI(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	homeA, apiA, _ := telemetryYard(t)
	homeB, apiB, _ := telemetryYard(t)
	changed := func(what string, call *store.TelemetryChange, cli *telemetry.Change) {
		t.Helper()
		call.InstallID, cli.InstallID = "", ""
		got, _ := json.Marshal(call)
		want, _ := json.Marshal(cli)
		if string(got) != string(want) {
			t.Errorf("%s: call answers %s, the CLI's function %s", what, got, want)
		}
	}
	states := func() (*telemetry.State, *telemetry.State) {
		t.Helper()
		a, err := telemetry.LoadState(filepath.Join(homeA, "telemetry.json"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := telemetry.LoadState(filepath.Join(homeB, "telemetry.json"))
		if err != nil {
			t.Fatal(err)
		}
		return a, b
	}
	now := time.Now()
	got, err := apiA.TelemetrySet(ctx, true, "jo")
	if err != nil {
		t.Fatal(err)
	}
	want, err := telemetry.On(ctx, apiB, filepath.Join(homeB, "telemetry.json"), "jo", now)
	if err != nil {
		t.Fatal(err)
	}
	changed("on", got, want)
	a, b := states()
	telemetryStatesEqual(t, a, b)
	idA, idB := a.InstallID, b.InstallID

	got, err = apiA.TelemetrySet(ctx, false, "jo")
	if err != nil {
		t.Fatal(err)
	}
	want, err = telemetry.Off(ctx, apiB, filepath.Join(homeB, "telemetry.json"), "jo", now)
	if err != nil {
		t.Fatal(err)
	}
	changed("off", got, want)
	a, b = states()
	telemetryStatesEqual(t, a, b)
	if a.InstallID != idA || b.InstallID != idB {
		t.Errorf("off keeps the id: %q and %q, want %q and %q", a.InstallID, b.InstallID, idA, idB)
	}

	got, err = apiA.TelemetrySet(ctx, true, "jo")
	if err != nil {
		t.Fatal(err)
	}
	want, err = telemetry.On(ctx, apiB, filepath.Join(homeB, "telemetry.json"), "jo", now)
	if err != nil {
		t.Fatal(err)
	}
	changed("on again", got, want)
	a, b = states()
	telemetryStatesEqual(t, a, b)
	if a.InstallID != idA || b.InstallID != idB {
		t.Errorf("on again keeps the id: %q and %q, want %q and %q", a.InstallID, b.InstallID, idA, idB)
	}
	if len(a.Grants) != 2 || len(b.Grants) != 2 {
		t.Errorf("on again grants twice: %v and %v", a.Grants, b.Grants)
	}
}

// TelemetryShow previews what yardr telemetry show prints: off it says
// off with no batch, on it previews the exact next request. Each runs
// through TelemetryShow on one throwaway yard and through Show on a twin
// with the same state file, and the two previews are the same JSON.
func TestTelemetryShowMatchesShow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	homeA, apiA, _ := telemetryYard(t)
	homeB, apiB, _ := telemetryYard(t)
	got, err := apiA.TelemetryShow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.Switch != "off" || got.Request != nil || got.NoBatch != telemetry.NoBatchOff || got.InstallID != "" {
		raw, _ := json.Marshal(got)
		t.Errorf("TelemetryShow with no state = %s, want off with no batch and no id", raw)
	}
	// The same consent on both yards: granted today, eligible from
	// tomorrow, so the batch holds the sending-day snapshot alone.
	now := time.Now().UTC()
	grant := now.Truncate(time.Second).Format("2006-01-02T15:04:05Z")
	day := now.Format("2006-01-02")
	next := now.Add(24 * time.Hour).Format("2006-01-02")
	seed := &telemetry.State{
		InstallID:    "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
		Minted:       day,
		Grants:       []string{grant},
		EligibleFrom: next,
	}
	for _, home := range []string{homeA, homeB} {
		if err := seed.Save(filepath.Join(home, "telemetry.json")); err != nil {
			t.Fatal(err)
		}
	}
	for _, api := range []store.API{apiA, apiB} {
		if err := api.SetSetting(ctx, store.TelemetrySetting, "true", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	previewed := func() {
		t.Helper()
		got, err := apiA.TelemetryShow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want, err := telemetry.Show(ctx, apiB, filepath.Join(homeB, "telemetry.json"), now)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(got)
		expected, _ := json.Marshal(want)
		if string(raw) != string(expected) {
			t.Errorf("TelemetryShow = %s, Show = %s", raw, expected)
		}
	}
	previewed()
	if got, _ := apiA.TelemetryShow(ctx); got.Request == nil || len(got.Request.Batch) != 1 || got.Request.Batch[0].Name != telemetry.DailyEvent {
		raw, _ := json.Marshal(got)
		t.Errorf("TelemetryShow on = %s, want the sending-day snapshot alone", raw)
	}
	// A hosted yard with no counts and no running crew has no request.
	for _, api := range []store.API{apiA, apiB} {
		if err := api.SetSetting(ctx, store.HostedSetting, "true", "jo"); err != nil {
			t.Fatal(err)
		}
	}
	previewed()
	if got, _ := apiA.TelemetryShow(ctx); got.Request != nil || got.NoBatch != telemetry.NoBatchEmpty {
		raw, _ := json.Marshal(got)
		t.Errorf("TelemetryShow hosted with nothing to send = %s, want no batch", raw)
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
