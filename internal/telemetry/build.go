package telemetry

import (
	"cmp"
	"regexp"
	"slices"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/store"
)

// Event names in the batch.
const (
	DailyEvent   = "yardr_daily"
	HarnessEvent = "yardr_harness_day"
	CrewEvent    = "yardr_crew_day"
	FailureEvent = "yardr_failure_day"
)

// Failure kinds of yardr_failure_day, in batch order: lenses, not a
// partition, since a bead the dispatcher gave up on is also held.
var failureKinds = []string{"gave_up", "held", "landing_failed"}

// Event is one PostHog capture event: the envelope fields PostHog reads
// (event, distinct_id, timestamp, uuid) and the allow-listed properties.
// Every event sets $process_person_profile so deletion by distinct_id
// does not depend on a project default, and $geoip_disable so no GeoIP
// location is derived from the request even though the shared project
// may have enrichment on.
type Event struct {
	Name       string         `json:"event"`
	DistinctID string         `json:"distinct_id"`
	Timestamp  string         `json:"timestamp"`
	UUID       string         `json:"uuid"`
	Properties map[string]any `json:"properties"`
}

// Request is the exact PostHog request show previews and the daemon
// posts: the shared public key and the allow-listed batch.
type Request struct {
	APIKey string  `json:"api_key"`
	Batch  []Event `json:"batch"`
}

// BuildInput is everything the batch is built from: the sending day, the
// eligible source days, the store's log and crew rows, and the build and
// yard facts. Event timestamps decide which source day an event counts
// on; only events within the source days are included.
type BuildInput struct {
	State      *State
	SendingDay string // UTC day S the batch is built on
	SourceDays []string
	Hosted     bool
	Tag        string // version.Tag()
	GOOS       string // runtime.GOOS
	Claimed    []store.Event
	Held       []store.Event
	Advanced   []store.Event
	GaveUp     []store.Event
	Crew       []store.CrewMember
}

// Build makes the sending day's batch: the runtime snapshot for S and
// one aggregate per completed source day. It is pure: the same input
// builds the same batch, with the same timestamps and UUIDs, so a retry
// after a lost response deduplicates. It reads only the fields the
// design's allow-list names; a future event kind or data key reaches the
// batch only by being added here first.
func Build(in BuildInput) []Event {
	if in.State == nil || in.State.InstallID == "" {
		// No install id, no batch: every event carries it as
		// distinct_id.
		return nil
	}
	b := &builder{in: in, kinds: map[string]bool{}, offers: map[string]map[string]bool{}}
	for _, k := range agents.Kinds() {
		b.kinds[k] = true
	}
	var out []Event
	if !in.Hosted {
		out = append(out, b.daily())
	}
	out = append(out, b.crew()...)
	for _, day := range in.SourceDays {
		out = append(out, b.harness(day)...)
		out = append(out, b.failures(day)...)
	}
	for i := range out {
		// IP capture off keeps the connecting address off the stored
		// events but leaves GeoIP enrichment free to use it first, so
		// the guard is carried on each event rather than trusted to
		// the shared project's enrichment setting. It is stamped here,
		// where every event passes, so an event kind added later
		// carries it without remembering.
		out[i].Properties["$geoip_disable"] = true
	}
	return out
}

// builder holds the per-build caches: the filed harness kinds and each
// kind's offered models.
type builder struct {
	in     BuildInput
	kinds  map[string]bool
	offers map[string]map[string]bool
}

// snapshotAt is the day marker of the runtime snapshot: 00:00 UTC on the
// sending day, or the latest consent grant's timestamp when the grant
// was on that day, so no event is dated before consent.
func (b *builder) snapshotAt() string {
	if g := b.in.State.latestGrant(); len(g) >= len(dayLayout) && g[:len(dayLayout)] == b.in.SendingDay {
		return g
	}
	return b.in.SendingDay + "T00:00:00Z"
}

// age is the whole UTC days from the first consent date through the
// sending day, 0 or more: the 1/7/30-day cohorts filter on it.
func (b *builder) age() int {
	minted, err1 := parseDay(b.in.State.Minted)
	sending, err2 := parseDay(b.in.SendingDay)
	if err1 != nil || err2 != nil {
		return 0
	}
	if age := int(sending.Sub(minted).Hours() / 24); age > 0 {
		return age
	}
	return 0
}

// daily is the yardr_daily runtime snapshot: the running build's tag,
// the process's OS, and the telemetry age. Hosted yards omit it: the
// hosting service is the source for their version and activity.
func (b *builder) daily() Event {
	return Event{
		Name:       DailyEvent,
		DistinctID: b.in.State.InstallID,
		Timestamp:  b.snapshotAt(),
		UUID:       DeriveUUID(b.in.State.InstallID, DailyEvent, b.in.SendingDay),
		Properties: map[string]any{
			"$process_person_profile": true,
			"version":                 bucketVersion(b.in.Tag),
			"os":                      bucketOS(b.in.GOOS),
			"telemetry_age_days":      b.age(),
		},
	}
}

// versionTag is the closed shape a release tag is sent as: v<major>.
// <minor>.<patch>, optionally with one -alpha.<n>, -beta.<n> or -rc.<n>
// suffix. The tag is whatever -ldflags -X stamped, so only this shape
// keeps a free-form suffix's words from leaving with it.
var versionTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-(alpha|beta|rc)\.[0-9]+)?$`)

// bucketVersion sends a release tag as it stands; everything else —
// pseudo-versions, devel builds, unknown — is the one bucket
// development. The revision is never sent.
func bucketVersion(tag string) string {
	if versionTag.MatchString(tag) {
		return tag
	}
	return "development"
}

// bucketOS sends darwin, linux or windows; anything else is other, since
// no question asks for more.
func bucketOS(goos string) string {
	switch goos {
	case "darwin", "linux", "windows":
		return goos
	default:
		return "other"
	}
}

// pair is one (kind, model) pair as the batch names it.
type pair struct{ kind, model string }

// crew counts the running crew sessions by pair: the line session and
// each live conversation session whose state is running. Enabled but
// stopped members and starting sessions are not counted. A row with no
// kind names no pair and is not counted, as a kindless session is not.
func (b *builder) crew() []Event {
	counts := map[pair]int{}
	for _, r := range b.in.Crew {
		kind, ok := b.bucketKind(r.Kind)
		if !ok {
			continue
		}
		n := 0
		if r.CrewSession.State == "running" {
			n++
		}
		for _, c := range r.Conversations {
			if c.State == "running" {
				n++
			}
		}
		if n > 0 {
			p := pair{kind, b.bucketModel(r.Kind, r.Model)}
			counts[p] += n
		}
	}
	var out []Event
	for _, p := range sortedPairs(counts) {
		out = append(out, Event{
			Name:       CrewEvent,
			DistinctID: b.in.State.InstallID,
			Timestamp:  b.snapshotAt(),
			UUID:       DeriveUUID(b.in.State.InstallID, CrewEvent, b.in.SendingDay, p.kind, p.model),
			Properties: map[string]any{
				"$process_person_profile": true,
				"kind":                    p.kind,
				"model":                   p.model,
				"running_crew_sessions":   counts[p],
				"hosted":                  b.in.Hosted,
			},
		})
	}
	return out
}

// harness counts one source day's claimed bead sessions by pair. The
// claim stamps kind and model from the group as it stands at the claim,
// so a model a group stops using disappears from the next day.
func (b *builder) harness(day string) []Event {
	counts := map[pair]int{}
	for _, e := range b.in.Claimed {
		if dayOf(e.At) != day {
			continue
		}
		raw, _ := e.Data["kind"].(string)
		kind, ok := b.bucketKind(raw)
		if !ok {
			continue
		}
		model, _ := e.Data["model"].(string)
		counts[pair{kind, b.bucketModel(raw, model)}]++
	}
	var out []Event
	for _, p := range sortedPairs(counts) {
		out = append(out, Event{
			Name:       HarnessEvent,
			DistinctID: b.in.State.InstallID,
			Timestamp:  day + "T12:00:00Z",
			UUID:       DeriveUUID(b.in.State.InstallID, HarnessEvent, day, p.kind, p.model),
			Properties: map[string]any{
				"$process_person_profile": true,
				"kind":                    p.kind,
				"model":                   p.model,
				"sessions_claimed":        counts[p],
				"hosted":                  b.in.Hosted,
			},
		})
	}
	return out
}

// failures counts one source day's failure events by kind: gave_up
// events, holds placed, and moves out of the conventional approved
// stage with outcome failed. A failed transition from another stage is
// not a landing failure; stage names are checked, never sent.
func (b *builder) failures(day string) []Event {
	counts := map[string]int{}
	for _, e := range b.in.GaveUp {
		if dayOf(e.At) == day {
			counts["gave_up"]++
		}
	}
	for _, e := range b.in.Held {
		if dayOf(e.At) == day {
			counts["held"]++
		}
	}
	for _, e := range b.in.Advanced {
		if dayOf(e.At) != day {
			continue
		}
		from, _ := e.Data["from"].(string)
		outcome, _ := e.Data["outcome"].(string)
		if from == "approved" && outcome == "failed" {
			counts["landing_failed"]++
		}
	}
	var out []Event
	for _, kind := range failureKinds {
		if counts[kind] == 0 {
			continue
		}
		out = append(out, Event{
			Name:       FailureEvent,
			DistinctID: b.in.State.InstallID,
			Timestamp:  day + "T12:00:00Z",
			UUID:       DeriveUUID(b.in.State.InstallID, FailureEvent, day, kind),
			Properties: map[string]any{
				"$process_person_profile": true,
				"kind":                    kind,
				"count":                   counts[kind],
				"hosted":                  b.in.Hosted,
			},
		})
	}
	return out
}

// bucketKind sends a filed harness kind as it stands; anything else is
// other. A session with no kind (exec, manual, peer) names no pair and
// is not counted at all.
func (b *builder) bucketKind(kind string) (string, bool) {
	if kind == "" {
		return "", false
	}
	if b.kinds[kind] {
		return kind, true
	}
	return "other", true
}

// bucketModel sends only an exact model id in the kind's checked-in
// Offers list under its own name: those ids are fixed product names,
// while the config value itself is arbitrary text no shape check could
// hold. Any other non-empty value is other, and an empty model — the
// harness's own default was used — is default.
func (b *builder) bucketModel(kind, model string) string {
	if model == "" {
		return "default"
	}
	offers, ok := b.offers[kind]
	if !ok {
		offers = map[string]bool{}
		if h, found := agents.Lookup(kind); found {
			for _, o := range h.Offers {
				offers[o.Model] = true
			}
		}
		b.offers[kind] = offers
	}
	if offers[model] {
		return model
	}
	return "other"
}

// sortedPairs orders pairs for a stable batch: kind, then model.
func sortedPairs(counts map[pair]int) []pair {
	out := make([]pair, 0, len(counts))
	for p := range counts {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, c pair) int {
		if a.kind != c.kind {
			return cmp.Compare(a.kind, c.kind)
		}
		return cmp.Compare(a.model, c.model)
	})
	return out
}
