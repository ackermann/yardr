package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// switchYard is a throwaway store with a state path in a home of its own.
func switchYard(t *testing.T) (store.API, string) {
	t.Helper()
	return storetest.New(t), filepath.Join(t.TempDir(), "telemetry.json")
}

// On mints an install id that show prints beside the current request; a
// fresh yard's batch is yardr_daily alone, and the setting stands true.
func TestOnMintsAndShowPreviews(t *testing.T) {
	t.Parallel()
	st, path := switchYard(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 15, 4, 5, 0, time.UTC)

	change, err := On(ctx, st, path, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	if !change.Changed || change.Switch != "on" || change.InstallID == "" {
		t.Fatalf("on = %+v", change)
	}
	if change.Since != "2026-10-28" || change.EligibleFrom != "2026-10-29" {
		t.Errorf("on = %+v", change)
	}
	if settings, err := st.Settings(ctx); err != nil || settings[store.TelemetrySetting] != "true" {
		t.Fatalf("settings = %v, %v", settings, err)
	}

	preview, err := Show(ctx, st, path, now)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Switch != "on" || preview.InstallID != change.InstallID {
		t.Fatalf("show = %+v", preview)
	}
	if preview.Request == nil || len(preview.Request.Batch) != 1 {
		t.Fatalf("show = %+v", preview)
	}
	if preview.Request.APIKey != APIKey {
		t.Errorf("show api_key = %q", preview.Request.APIKey)
	}
	daily := preview.Request.Batch[0]
	if daily.Name != DailyEvent || daily.DistinctID != change.InstallID {
		t.Errorf("batch = %+v", daily)
	}
	if got := daily.Properties; got["version"] != "development" || got["telemetry_age_days"] != 0 {
		t.Errorf("daily properties = %v", got)
	}
	if daily.Timestamp != "2026-10-28T15:04:05Z" {
		t.Errorf("daily timestamp = %s", daily.Timestamp)
	}
	if len(preview.SourceDays) != 0 {
		t.Errorf("source days = %v", preview.SourceDays)
	}
}

// Off then on keeps the install id but advances eligible_from; a repeat
// on leaves it alone.
func TestOffAndOnKeepsTheID(t *testing.T) {
	t.Parallel()
	st, path := switchYard(t)
	ctx := context.Background()
	first, err := On(ctx, st, path, "me", time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	again, err := On(ctx, st, path, "me", time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed || again.EligibleFrom != "2026-10-29" {
		t.Fatalf("repeat on = %+v", again)
	}
	off, err := Off(ctx, st, path, "me", time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !off.Changed || off.InstallID != first.InstallID {
		t.Fatalf("off = %+v", off)
	}
	preview, err := Show(ctx, st, path, time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if preview.NoBatch != NoBatchOff || preview.Request != nil || preview.InstallID != first.InstallID {
		t.Fatalf("show while off = %+v", preview)
	}
	on, err := On(ctx, st, path, "me", time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if on.InstallID != first.InstallID || on.EligibleFrom != "2026-11-06" || on.Since != "2026-11-05" {
		t.Fatalf("on again = %+v", on)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Grants) != 3 || len(state.Withdrawals) != 1 {
		t.Errorf("grants = %v, withdrawals = %v", state.Grants, state.Withdrawals)
	}
}

// Deleting the state file and turning on mints a new install id: the
// forget-and-remint that unlinks the yard from deleted data.
func TestForgetAndRemint(t *testing.T) {
	t.Parallel()
	st, path := switchYard(t)
	ctx := context.Background()
	first, err := On(ctx, st, path, "me", time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	second, err := On(ctx, st, path, "me", time.Date(2026, 10, 28, 16, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if second.InstallID == "" || second.InstallID == first.InstallID {
		t.Fatalf("reminted id = %q, was %q", second.InstallID, first.InstallID)
	}
}

// A hosted yard with no eligible counts and no running crew has no
// request to preview.
func TestShowHostedEmptyDayHasNoRequest(t *testing.T) {
	t.Parallel()
	st, path := switchYard(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, store.HostedSetting, "true", "me"); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	change, err := On(ctx, st, path, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := Show(ctx, st, path, now)
	if err != nil {
		t.Fatal(err)
	}
	if preview.NoBatch != NoBatchEmpty || preview.Request != nil {
		t.Fatalf("hosted show = %+v", preview)
	}
	if preview.InstallID != change.InstallID || !preview.Hosted {
		t.Errorf("hosted show = %+v", preview)
	}
}

// Once a batch was attempted today there is no batch until the next UTC
// day; the id still shows.
func TestShowAfterAnAttemptWaitsForTomorrow(t *testing.T) {
	t.Parallel()
	st, path := switchYard(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	change, err := On(ctx, st, path, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	state, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	state.LastAttemptDay = "2026-10-28"
	if err := state.Save(path); err != nil {
		t.Fatal(err)
	}
	preview, err := Show(ctx, st, path, now)
	if err != nil {
		t.Fatal(err)
	}
	if preview.NoBatch != NoBatchAttempted || preview.Request != nil {
		t.Fatalf("show after an attempt = %+v", preview)
	}
	if preview.InstallID != change.InstallID {
		t.Errorf("show after an attempt = %+v", preview)
	}
}

func TestSourceRange(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 15, 4, 5, 0, time.UTC)
	days := func(first, last string) []string {
		var out []string
		for d := first; d <= last; {
			out = append(out, d)
			next, err := parseDay(d)
			if err != nil {
				t.Fatal(err)
			}
			d = dayOf(next.AddDate(0, 0, 1))
		}
		return out
	}
	for _, tc := range []struct {
		name           string
		eligible, sent string
		want           []string
	}{
		{"nothing sent yet", "2026-10-27", "", days("2026-10-27", "2026-10-27")},
		{"cursor advances", "2026-10-20", "2026-10-25", days("2026-10-26", "2026-10-27")},
		{"caught up", "2026-10-20", "2026-10-27", nil},
		{"consent tomorrow", "2026-10-29", "", nil},
		{"no consent period", "", "", nil},
		{"new consent drops the old days", "2026-10-27", "2026-10-20", days("2026-10-27", "2026-10-27")},
		{"ten days capped to seven", "2026-10-10", "", days("2026-10-21", "2026-10-27")},
		{"a corrupt eligible_from includes nothing", "soon", "", nil},
	} {
		if got := SourceRange(tc.eligible, tc.sent, now); !equalDays(got, tc.want) {
			t.Errorf("%s: range = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func equalDays(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
