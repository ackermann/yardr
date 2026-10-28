package telemetry

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/store"
)

// sentinel words name a fixture yard's beads, depots, groups, models and
// actors: none may reach the batch, which holds counts and fixed names
// only.
const (
	sentinelBead  = "SENTINEL-bead-tumbledown"
	sentinelDepot = "SENTINEL-depot-northwind"
	sentinelGroup = "SENTINEL-group-otters"
	sentinelModel = "SENTINEL sentence-shaped model value"
	sentinelKind  = "SENTINEL-kind-zephyr"
	sentinelActor = "SENTINEL-actor-bramble"
	sentinelShort = "SENTINEL-x1"
)

// fixtureDay is one source day's log: claims across the model buckets,
// a kindless claim, failures of every counted kind and two more of the
// kinds that are not counted, and crew rows across the counted and
// uncounted states. Every event carries the fixture's sentinel names in
// the fields the batch must never read.
func fixtureInput() BuildInput {
	day := func(hour int) time.Time {
		return time.Date(2026, 10, 27, hour, 0, 0, 0, time.UTC)
	}
	claimed := func(kind, model string, hour int) store.Event {
		data := map[string]any{
			"group": sentinelGroup, "stage": "new", "session": "SENTINEL-session",
			"bead": sentinelBead, "depot": sentinelDepot,
		}
		if kind != "" {
			data["kind"] = kind
		}
		if model != "" {
			data["model"] = model
		}
		return store.Event{Kind: "claimed", Actor: sentinelActor, Data: data, At: day(hour)}
	}
	return BuildInput{
		State: &State{
			InstallID: "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
			Minted:    "2026-10-26",
			Grants:    []string{"2026-10-26T09:30:00Z"},
		},
		SendingDay: "2026-10-28",
		SourceDays: []string{"2026-10-27"},
		Tag:        "v0.9.1",
		GOOS:       "darwin",
		Claimed: []store.Event{
			claimed("claude", "sonnet", 9),  // a listed offer stays fixed
			claimed("claude", "sonnet", 10), // thrice with the last: the count is 3
			claimed("claude", sentinelModel, 11),
			claimed("claude", sentinelShort, 12),
			claimed("claude", "", 13), // the harness's default
			claimed(sentinelKind, "sonnet", 14),
			claimed("", "sonnet", 15),      // exec: no kind, not counted
			claimed("claude", "sonnet", 9), // a duplicate hour still counts
		},
		Held: []store.Event{
			{Kind: "held", Actor: sentinelActor, At: day(9)},
			{Kind: "held", Actor: sentinelActor, At: day(10)},
			{Kind: "held", Actor: sentinelActor, At: time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)},
		},
		GaveUp: []store.Event{
			{Kind: "gave_up", Actor: "yard", Data: map[string]any{
				"stage": "new", "failures": 3, "hint": "SENTINEL-hint",
			}, At: day(11)},
		},
		Advanced: []store.Event{
			{Kind: "advanced", Actor: sentinelActor, Data: map[string]any{
				"from": "approved", "to": "merged", "outcome": "failed",
			}, At: day(12)},
			{Kind: "advanced", Actor: sentinelActor, Data: map[string]any{
				"from": "new", "to": "backlog", "outcome": "failed",
			}, At: day(13)},
			{Kind: "advanced", Actor: sentinelActor, Data: map[string]any{
				"from": "approved", "to": "merged", "outcome": "done",
			}, At: day(14)},
		},
		Crew: []store.CrewMember{
			{Name: "yardmaster", Runner: "herdr", Kind: "claude", Model: "sonnet",
				CrewSession: store.CrewSession{State: "running"},
				Conversations: map[string]store.CrewSession{
					"ada": {State: "running"},
					"hal": {State: "stopped"},
				}},
			{Name: "scribe", Runner: "herdr", Kind: "codex", Model: "",
				CrewSession: store.CrewSession{State: "running"}},
			{Name: "runner", Runner: "exec", Kind: "", Model: "",
				CrewSession: store.CrewSession{State: "running"}},
			{Name: "sleeper", Runner: "herdr", Kind: "claude", Model: "sonnet",
				CrewSession: store.CrewSession{State: "stopped"}},
		},
	}
}

// batchJSON marshals the batch as show prints it.
func batchJSON(t *testing.T, batch []Event) string {
	t.Helper()
	data, err := json.Marshal(Request{APIKey: APIKey, Batch: batch})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// No sentinel word of the fixture yard — its beads, depots, groups,
// sentence-shaped model, unknown kind, actors and hints — reaches the
// batch: it holds counts and fixed names only.
func TestBuildHoldsTheNeverList(t *testing.T) {
	t.Parallel()
	serial := batchJSON(t, Build(fixtureInput()))
	for _, sentinel := range []string{
		sentinelBead, sentinelDepot, sentinelGroup, sentinelModel,
		sentinelKind, sentinelActor, sentinelShort, "SENTINEL-session", "SENTINEL-hint",
	} {
		if strings.Contains(serial, sentinel) {
			t.Errorf("the batch holds %q:\n%s", sentinel, serial)
		}
	}
}

// The fixture builds the exact batch: one daily, the crew pairs, one
// harness event per counted pair and one failure event per kind seen,
// with the fixed dimensions the design names.
func TestBuildFixtureBatch(t *testing.T) {
	t.Parallel()
	batch := Build(fixtureInput())
	if len(batch) != 10 {
		t.Fatalf("the batch holds %d events, want 10:\n%s", len(batch), batchJSON(t, batch))
	}
	byName := map[string][]Event{}
	for _, e := range batch {
		byName[e.Name] = append(byName[e.Name], e)
		for _, f := range []string{e.DistinctID, e.Timestamp, e.UUID} {
			if f == "" {
				t.Errorf("event %s has an empty envelope field: %+v", e.Name, e)
			}
		}
		if e.DistinctID != "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90" {
			t.Errorf("event %s has distinct_id %q", e.Name, e.DistinctID)
		}
		if e.Properties["$process_person_profile"] != true {
			t.Errorf("event %s has no $process_person_profile: %+v", e.Name, e.Properties)
		}
	}

	daily := byName[DailyEvent]
	if len(daily) != 1 {
		t.Fatalf("daily events = %d, want 1", len(daily))
	}
	if got := daily[0].Properties; got["version"] != "v0.9.1" || got["os"] != "darwin" || got["telemetry_age_days"] != 2 {
		t.Errorf("daily properties = %v", got)
	}
	if daily[0].Timestamp != "2026-10-28T00:00:00Z" {
		t.Errorf("daily timestamp = %s", daily[0].Timestamp)
	}

	crew := byName[CrewEvent]
	if len(crew) != 2 {
		t.Fatalf("crew events = %d, want 2", len(crew))
	}
	crewCounts := map[string]int{}
	for _, e := range crew {
		crewCounts[e.Properties["kind"].(string)+"/"+e.Properties["model"].(string)] = e.Properties["running_crew_sessions"].(int)
		if e.Timestamp != "2026-10-28T00:00:00Z" {
			t.Errorf("crew timestamp = %s", e.Timestamp)
		}
		if e.Properties["hosted"] != false {
			t.Errorf("crew hosted = %v", e.Properties["hosted"])
		}
	}
	if crewCounts["claude/sonnet"] != 2 || crewCounts["codex/default"] != 1 || len(crewCounts) != 2 {
		t.Errorf("crew counts = %v", crewCounts)
	}

	harness := byName[HarnessEvent]
	if len(harness) != 4 {
		t.Fatalf("harness events = %d, want 4", len(harness))
	}
	harnessCounts := map[string]int{}
	for _, e := range harness {
		harnessCounts[e.Properties["kind"].(string)+"/"+e.Properties["model"].(string)] = e.Properties["sessions_claimed"].(int)
		if e.Timestamp != "2026-10-27T12:00:00Z" {
			t.Errorf("harness timestamp = %s", e.Timestamp)
		}
	}
	want := map[string]int{"claude/sonnet": 3, "claude/other": 2, "claude/default": 1, "other/other": 1}
	if len(harnessCounts) != len(want) {
		t.Fatalf("harness counts = %v, want %v", harnessCounts, want)
	}
	for p, n := range want {
		if harnessCounts[p] != n {
			t.Errorf("harness counts = %v, want %v", harnessCounts, want)
			break
		}
	}

	failures := byName[FailureEvent]
	if len(failures) != 3 {
		t.Fatalf("failure events = %d, want 3", len(failures))
	}
	failureCounts := map[string]int{}
	for i, e := range failures {
		if e.Properties["kind"] != failureKinds[i] {
			t.Errorf("failure event %d is kind %v, want %s", i, e.Properties["kind"], failureKinds[i])
		}
		failureCounts[e.Properties["kind"].(string)] = e.Properties["count"].(int)
	}
	if failureCounts["gave_up"] != 1 || failureCounts["held"] != 2 || failureCounts["landing_failed"] != 1 {
		t.Errorf("failure counts = %v", failureCounts)
	}
}

// Rebuilding the same input builds the identical batch: stable
// timestamps and UUIDs, so a retry deduplicates.
func TestBuildIsStable(t *testing.T) {
	t.Parallel()
	first, second := batchJSON(t, Build(fixtureInput())), batchJSON(t, Build(fixtureInput()))
	if first != second {
		t.Errorf("two builds differ:\n%s\n%s", first, second)
	}
	batch := Build(fixtureInput())
	seen := map[string]string{}
	for _, e := range batch {
		if prev, dup := seen[e.UUID]; dup {
			t.Errorf("uuid %s on two events (%s and %s)", e.UUID, prev, e.Name)
		}
		seen[e.UUID] = e.Name
	}
}

// A self-run day with no claims, failures or running crew yields
// yardr_daily alone; a hosted day with the same yields no event, and a
// hosted day with crew yields the crew snapshot without the daily.
func TestBuildEmptyDays(t *testing.T) {
	t.Parallel()
	in := fixtureInput()
	in.Claimed, in.Held, in.Advanced, in.GaveUp, in.Crew = nil, nil, nil, nil, nil
	in.SourceDays = []string{"2026-10-27"}
	if batch := Build(in); len(batch) != 1 || batch[0].Name != DailyEvent {
		t.Fatalf("self-run empty day = %v", batchJSON(t, batch))
	}
	in.Hosted = true
	if batch := Build(in); len(batch) != 0 {
		t.Fatalf("hosted empty day = %v", batchJSON(t, batch))
	}
	in.Crew = fixtureInput().Crew
	batch := Build(in)
	if len(batch) != 2 || batch[0].Name != CrewEvent || batch[1].Name != CrewEvent {
		t.Fatalf("hosted crew day = %v", batchJSON(t, batch))
	}
	for _, e := range batch {
		if e.Properties["hosted"] != true {
			t.Errorf("hosted crew event has hosted = %v", e.Properties["hosted"])
		}
	}
}

// Without an install id there is no batch: every event would carry it
// as distinct_id.
func TestBuildWithoutAnIDBuildsNothing(t *testing.T) {
	t.Parallel()
	in := fixtureInput()
	in.State = nil
	if batch := Build(in); len(batch) != 0 {
		t.Fatalf("batch without state = %v", batchJSON(t, batch))
	}
	in = fixtureInput()
	in.State.InstallID = ""
	if batch := Build(in); len(batch) != 0 {
		t.Fatalf("batch without an id = %v", batchJSON(t, batch))
	}
}

// telemetry_age_days is the whole UTC days from the first consent date
// through the sending day, so the 1/7/30-day cohorts filter on it.
func TestBuildTelemetryAgeDays(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		minted, sending string
		age             int
	}{
		{"2026-10-28", "2026-10-28", 0},
		{"2026-10-27", "2026-10-28", 1},
		{"2026-10-21", "2026-10-28", 7},
		{"2026-09-28", "2026-10-28", 30},
		{"2026-10-29", "2026-10-28", 0}, // a clock moved back: never negative
	} {
		in := fixtureInput()
		in.State.Minted = tc.minted
		in.SendingDay = tc.sending
		batch := Build(in)
		if len(batch) == 0 || batch[0].Name != DailyEvent {
			t.Fatalf("minted %s, sending %s: no daily", tc.minted, tc.sending)
		}
		if got := batch[0].Properties["telemetry_age_days"]; got != tc.age {
			t.Errorf("minted %s, sending %s: age = %v, want %d", tc.minted, tc.sending, got, tc.age)
		}
	}
}

// The runtime snapshot is marked 00:00 UTC, unless the latest consent
// grant was on the sending day, when it uses the grant's timestamp, so
// no event is dated before consent.
func TestBuildSnapshotAtGrant(t *testing.T) {
	t.Parallel()
	in := fixtureInput()
	in.State.Grants = []string{"2026-10-26T09:30:00Z", "2026-10-28T14:22:31Z"}
	for _, e := range Build(in) {
		switch e.Name {
		case DailyEvent, CrewEvent:
			if e.Timestamp != "2026-10-28T14:22:31Z" {
				t.Errorf("%s timestamp = %s", e.Name, e.Timestamp)
			}
		default:
			if e.Timestamp != "2026-10-27T12:00:00Z" {
				t.Errorf("%s timestamp = %s", e.Name, e.Timestamp)
			}
		}
	}
}

// Only events within the source days count: an event on the sending day
// itself (incomplete) or before the range is never included.
func TestBuildIncludesOnlySourceDays(t *testing.T) {
	t.Parallel()
	in := fixtureInput()
	in.Claimed = append(in.Claimed,
		store.Event{Kind: "claimed", Data: map[string]any{"kind": "claude", "model": "sonnet"},
			At: time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)},
		store.Event{Kind: "claimed", Data: map[string]any{"kind": "claude", "model": "sonnet"},
			At: time.Date(2026, 10, 26, 23, 0, 0, 0, time.UTC)},
	)
	for _, e := range Build(in) {
		if e.Name != HarnessEvent || e.Properties["kind"] != "claude" || e.Properties["model"] != "sonnet" {
			continue
		}
		if got := e.Properties["sessions_claimed"]; got != 3 {
			t.Fatalf("claude/sonnet claimed = %v, want 3", got)
		}
	}
}

func TestBucketVersion(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ tag, want string }{
		{"v0.9.1", "v0.9.1"},
		{"v1.2.3-rc.1", "v1.2.3-rc.1"},
		{"v10.20.30-alpha.4", "v10.20.30-alpha.4"},
		{"v1.2.3-beta.0", "v1.2.3-beta.0"},
		{"", "development"},
		{"v1.2", "development"},
		{"1.2.3", "development"},
		{"v1.2.3-alice-laptop", "development"},
		{"v1.2.3-rc1", "development"},
		{"v0.0.0-20251028120000-abcdef123456", "development"},
	} {
		if got := bucketVersion(tc.tag); got != tc.want {
			t.Errorf("bucketVersion(%q) = %q, want %q", tc.tag, got, tc.want)
		}
	}
}

func TestBucketOS(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ goos, want string }{
		{"darwin", "darwin"}, {"linux", "linux"}, {"windows", "windows"},
		{"freebsd", "other"}, {"", "other"},
	} {
		if got := bucketOS(tc.goos); got != tc.want {
			t.Errorf("bucketOS(%q) = %q, want %q", tc.goos, got, tc.want)
		}
	}
}
