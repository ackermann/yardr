package telemetry

import (
	"context"
	"encoding/json"
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
		want, dropped  []string
	}{
		{"nothing sent yet", "2026-10-27", "", days("2026-10-27", "2026-10-27"), nil},
		{"cursor advances", "2026-10-20", "2026-10-25", days("2026-10-26", "2026-10-27"), nil},
		{"caught up", "2026-10-20", "2026-10-27", nil, nil},
		{"consent tomorrow", "2026-10-29", "", nil, nil},
		{"no consent period", "", "", nil, nil},
		{"new consent drops the old days", "2026-10-27", "2026-10-20", days("2026-10-27", "2026-10-27"), nil},
		{"a gap of three days", "2026-10-20", "2026-10-24", days("2026-10-25", "2026-10-27"), nil},
		{"exactly seven days", "2026-10-21", "", days("2026-10-21", "2026-10-27"), nil},
		{"ten days capped to seven", "2026-10-18", "", days("2026-10-21", "2026-10-27"), days("2026-10-18", "2026-10-20")},
		{"a stale cursor under a long gap", "2026-10-01", "2026-10-15", days("2026-10-21", "2026-10-27"), days("2026-10-16", "2026-10-20")},
		{"a corrupt eligible_from includes nothing", "soon", "", nil, nil},
	} {
		got, dropped := SourceRange(tc.eligible, tc.sent, now)
		if !equalDays(got, tc.want) || !equalDays(dropped, tc.dropped) {
			t.Errorf("%s: range = %v, dropped %v, want %v, dropped %v", tc.name, got, dropped, tc.want, tc.dropped)
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

// An off before any on, the onboarding's No, leaves the home as it was: no
// state file is made for a yard that never consented, and no lock file.
func TestOffBeforeOnLeavesTheHomeAsItWas(t *testing.T) {
	t.Parallel()
	st := storetest.New(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "telemetry.json")
	change, err := Off(context.Background(), st, path, "me", time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC))
	if err != nil || change.InstallID != "" {
		t.Fatalf("off = %+v, %v", change, err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("the home holds %v, %v", entries, err)
	}
}

// What stops a batch is one answer, for show and the daemon's pass alike.
func TestNoBatchFor(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		on    bool
		state *State
		want  string
	}{
		{"off", false, &State{InstallID: "id"}, NoBatchOff},
		{"off wins over a missing id", false, nil, NoBatchOff},
		{"on with no state file", true, nil, NoBatchNoID},
		{"on with no id in it", true, &State{}, NoBatchNoID},
		{"attempted today", true, &State{InstallID: "id", LastAttemptDay: "2026-10-28"}, NoBatchAttempted},
		{"attempted yesterday", true, &State{InstallID: "id", LastAttemptDay: "2026-10-27"}, ""},
		{"never attempted", true, &State{InstallID: "id"}, ""},
	} {
		if got := NoBatchFor(tc.on, tc.state, now); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// fakeStore answers what Prepare reads with the events and crew a test
// gives: the store stamps an event with the moment it is written, and a
// test of source days needs them on days it chooses.
type fakeStore struct {
	store.API
	events map[string][]store.Event
	crew   []store.CrewMember
}

func (f fakeStore) EventsOfKind(_ context.Context, kind string, limit int) ([]store.Event, error) {
	evs := f.events[kind]
	return evs[max(len(evs)-limit, 0):], nil
}

func (f fakeStore) Crew(context.Context) ([]store.CrewMember, error) { return f.crew, nil }

// gaveUpOn is a gave_up event at ten o'clock on each of the days.
func gaveUpOn(days ...string) []store.Event {
	var out []store.Event
	for _, day := range days {
		at, err := time.Parse(time.RFC3339, day+"T10:00:00Z")
		if err != nil {
			panic(err)
		}
		out = append(out, store.Event{Kind: "gave_up", Actor: "yard", At: at})
	}
	return out
}

// failureDays are the source days of the batch's failure events, in order.
func failureDays(batch []Event) []string {
	var out []string
	for _, e := range batch {
		if e.Name == FailureEvent {
			out = append(out, e.Timestamp[:len("2026-10-27")])
		}
	}
	return out
}

// Prepare builds one batch from the days the cursor and the consent leave:
// a gap of three days backfills three days in one batch, ten days include
// the latest seven and name the three older ones as dropped, and a day
// before the latest consent is never in it, whatever the old cursor says.
func TestPrepareSourceDays(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 28, 15, 0, 0, 0, time.UTC)
	everyDay := gaveUpOn("2026-10-17", "2026-10-18", "2026-10-19", "2026-10-20", "2026-10-21", "2026-10-22",
		"2026-10-23", "2026-10-24", "2026-10-25", "2026-10-26", "2026-10-27", "2026-10-28")
	st := fakeStore{events: map[string][]store.Event{"gave_up": everyDay}}
	days := func(from, to string) []string { // inclusive, as the batch names them
		var out []string
		for d := from; d <= to; d = nextDay(d) {
			out = append(out, d)
		}
		return out
	}
	for _, tc := range []struct {
		name       string
		state      State
		sourceDays []string
		dropped    []string
		failureOn  []string
	}{
		{"three days since the last send",
			State{EligibleFrom: "2026-10-20", LastDaySent: "2026-10-24"},
			days("2026-10-25", "2026-10-27"), nil, days("2026-10-25", "2026-10-27")},
		{"ten days since consent",
			State{EligibleFrom: "2026-10-18"},
			days("2026-10-21", "2026-10-27"), days("2026-10-18", "2026-10-20"), days("2026-10-21", "2026-10-27")},
		{"consent again after the days of an old cursor",
			State{EligibleFrom: "2026-10-26", LastDaySent: "2026-10-20"},
			days("2026-10-26", "2026-10-27"), nil, days("2026-10-26", "2026-10-27")},
		{"consent that began today has no day yet",
			State{EligibleFrom: "2026-10-29"},
			nil, nil, nil},
	} {
		state := tc.state
		state.InstallID, state.Minted = "id", "2026-10-10"
		next, err := Prepare(context.Background(), st, &state, false, now)
		if err != nil {
			t.Fatal(err)
		}
		if !equalDays(next.SourceDays, tc.sourceDays) || !equalDays(next.Dropped, tc.dropped) {
			t.Errorf("%s: source days %v, dropped %v; want %v, %v", tc.name, next.SourceDays, next.Dropped, tc.sourceDays, tc.dropped)
		}
		if next.Request == nil || next.Request.APIKey != APIKey {
			t.Fatalf("%s: request = %+v", tc.name, next.Request)
		}
		if got := failureDays(next.Request.Batch); !equalDays(got, tc.failureOn) {
			t.Errorf("%s: failure events on %v, want %v", tc.name, got, tc.failureOn)
		}
		if next.Request.Batch[0].Name != DailyEvent {
			t.Errorf("%s: a self-run batch opens with %s, not %s", tc.name, next.Request.Batch[0].Name, DailyEvent)
		}
	}

	// Hosted: no daily, so with no counts and no crew there is no request,
	// and with counts the request holds them alone.
	hosted := State{InstallID: "id", Minted: "2026-10-10", EligibleFrom: "2026-10-20"}
	next, err := Prepare(context.Background(), fakeStore{}, &hosted, true, now)
	if err != nil || next.Request != nil {
		t.Fatalf("a hosted yard with nothing to count: %+v, %v", next, err)
	}
	next, err = Prepare(context.Background(), st, &hosted, true, now)
	if err != nil || next.Request == nil {
		t.Fatalf("a hosted yard with counts: %+v, %v", next, err)
	}
	for _, e := range next.Request.Batch {
		if e.Name == DailyEvent {
			t.Errorf("a hosted batch carries %s", e.Name)
		}
	}
}

// The store's calls answer through On, Off and Show: the converted replies
// are the same JSON the functions return, so the parallel shapes cannot
// drift apart. On and off run on twin yards, which mint their own ids;
// show runs on the same seeded consent, with and without a batch.
func TestSwitchConvertsOnOffShowLosslessly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 10, 28, 15, 4, 5, 0, time.UTC)
	stA, pathA := switchYard(t)
	stB, pathB := switchYard(t)
	var sw Switch
	equal := func(what string, got, want any) {
		t.Helper()
		raw, _ := json.Marshal(got)
		expected, _ := json.Marshal(want)
		if string(raw) != string(expected) {
			t.Errorf("%s: the call answers %s, the function %s", what, raw, expected)
		}
	}
	blank := func(got *store.TelemetryChange, want *Change) {
		t.Helper()
		got.InstallID, want.InstallID = "", ""
	}
	onGot, err := sw.TelemetryOn(ctx, stA, pathA, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	onWant, err := On(ctx, stB, pathB, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	blank(onGot, onWant)
	equal("on", onGot, onWant)
	offGot, err := sw.TelemetryOff(ctx, stA, pathA, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	offWant, err := Off(ctx, stB, pathB, "me", now)
	if err != nil {
		t.Fatal(err)
	}
	blank(offGot, offWant)
	equal("off", offGot, offWant)

	seed := &State{
		InstallID:    "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
		Minted:       "2026-10-28",
		Grants:       []string{"2026-10-28T15:04:05Z"},
		EligibleFrom: "2026-10-29",
	}
	for _, path := range []string{pathA, pathB} {
		if err := seed.Save(path); err != nil {
			t.Fatal(err)
		}
	}
	for _, st := range []store.API{stA, stB} {
		if err := st.SetSetting(ctx, store.TelemetrySetting, "true", "me"); err != nil {
			t.Fatal(err)
		}
	}
	show := func(what string) {
		t.Helper()
		got, err := sw.TelemetryShow(ctx, stA, pathA, now)
		if err != nil {
			t.Fatal(err)
		}
		want, err := Show(ctx, stB, pathB, now)
		if err != nil {
			t.Fatal(err)
		}
		equal(what, got, want)
	}
	show("show")
	// A hosted yard with no counts and no running crew has no request:
	// the nil request converts as a nil.
	for _, st := range []store.API{stA, stB} {
		if err := st.SetSetting(ctx, store.HostedSetting, "true", "me"); err != nil {
			t.Fatal(err)
		}
	}
	show("show with no batch")
}
