package telemetry

import (
	"context"
	"runtime"
	"time"

	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/version"
)

// maxSourceDays is how many eligible source days one batch holds: a
// daemon that was down rebuilds at most the most recent seven completed
// days from the log.
const maxSourceDays = 7

// SourceRange is the eligible completed source days of a batch built at
// now: from the later of eligibleFrom and the day after lastDaySent
// (eligibleFrom when none was sent), through the most recent completed
// UTC day, at most the latest seven. The older eligible days the cap
// leaves out are dropped, and returned so the sender can log the range
// once. A day before consent is never included, and neither is the
// incomplete current day. Empty eligibleFrom includes nothing: without a
// consent period no source day is eligible.
func SourceRange(eligibleFrom, lastDaySent string, now time.Time) (days, dropped []string) {
	if eligibleFrom == "" {
		return nil, nil
	}
	first := eligibleFrom
	if _, err := parseDay(first); err != nil {
		return nil, nil
	}
	if lastDaySent != "" {
		if after, err := parseDay(lastDaySent); err == nil {
			if next := dayOf(after.AddDate(0, 0, 1)); next > first {
				first = next
			}
		}
	}
	last := dayOf(now.Add(-24 * time.Hour))
	var out []string
	for day := first; day <= last; day = nextDay(day) {
		out = append(out, day)
	}
	if cut := len(out) - maxSourceDays; cut > 0 {
		return out[cut:], out[:cut]
	}
	return out, nil
}

// nextDay is the UTC day after day, as dayOf wrote it.
func nextDay(day string) string {
	d, err := parseDay(day)
	if err != nil {
		return day
	}
	return dayOf(d.AddDate(0, 0, 1))
}

// Change is what yardr telemetry on and off did.
type Change struct {
	Switch       string `json:"telemetry"` // "on" or "off"
	Changed      bool   `json:"changed"`
	InstallID    string `json:"install_id,omitempty"`
	Since        string `json:"since,omitempty"` // latest grant's UTC date
	EligibleFrom string `json:"eligible_from,omitempty"`
	Endpoint     string `json:"endpoint"`
}

// On turns telemetry on: it mints the install id when none exists,
// records this consent grant, and records eligible_from as the next UTC
// day, so locally recorded activity from before consent is never
// included. Repeating on while already on records the grant but leaves
// eligible_from alone; off and on again starts a new eligible day and
// drops any unsent days from the earlier consent period. The setting is
// written first, so a refused actor changes nothing local either.
func On(ctx context.Context, st store.API, statePath, actor string, now time.Time) (*Change, error) {
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	was := settings[store.TelemetrySetting] == "true"
	if err := st.SetSetting(ctx, store.TelemetrySetting, "true", actor); err != nil {
		return nil, err
	}
	// From the read to the save: the daemon's sender writes the file too.
	unlock, err := lockState(statePath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, err := LoadState(statePath)
	if err != nil {
		return nil, err
	}
	if state == nil || state.InstallID == "" {
		id, err := MintID()
		if err != nil {
			return nil, err
		}
		state = &State{InstallID: id, Minted: dayOf(now)}
	}
	state.Grants = append(state.Grants, stampOf(now))
	if !was || state.EligibleFrom == "" {
		state.EligibleFrom = nextDay(dayOf(now))
	}
	if err := state.Save(statePath); err != nil {
		return nil, err
	}
	return &Change{
		Switch:       "on",
		Changed:      !was,
		InstallID:    state.InstallID,
		Since:        state.since(),
		EligibleFrom: state.EligibleFrom,
		Endpoint:     Endpoint,
	}, nil
}

// Off turns telemetry off: it records the withdrawal time locally and
// the setting stops new requests from starting on the next pass. A
// request already submitted may still complete. The id is kept, so a
// yard that turns off and on again is the same yard; forgetting it is
// deleting the state file, a separate deliberate act. As in On, the
// setting is written first.
func Off(ctx context.Context, st store.API, statePath, actor string, now time.Time) (*Change, error) {
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	was := settings[store.TelemetrySetting] == "true"
	if err := st.SetSetting(ctx, store.TelemetrySetting, "false", actor); err != nil {
		return nil, err
	}
	state, unlock, err := lockedState(statePath)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if state != nil {
		state.Withdrawals = append(state.Withdrawals, stampOf(now))
		if err := state.Save(statePath); err != nil {
			return nil, err
		}
	}
	change := &Change{Switch: "off", Changed: was, Endpoint: Endpoint}
	if state != nil {
		change.InstallID, change.Since, change.EligibleFrom = state.InstallID, state.since(), state.EligibleFrom
	}
	return change, nil
}

// No-batch reasons of a Preview: the switch is off, a batch was already
// attempted today, the yard has nothing to send, or the setting stands
// on with no install id (a failed activation to run again).
const (
	NoBatchOff       = "off"
	NoBatchAttempted = "attempted"
	NoBatchEmpty     = "empty"
	NoBatchNoID      = "no-id"
)

// SourceDaysSay is the source days of the next request line: the same
// line the CLI and the web view print.
func SourceDaysSay(days []string) string {
	switch len(days) {
	case 0:
		return "no eligible source days"
	case 1:
		return "source day " + days[0]
	default:
		return "source days " + days[0] + ".." + days[len(days)-1]
	}
}

// NoBatchSay is why a preview has no batch, in words: the same line the
// CLI and the web view print.
func NoBatchSay(reason string) string {
	switch reason {
	case NoBatchOff:
		return "no batch available until telemetry is enabled"
	case NoBatchAttempted:
		return "no batch available until the next UTC day"
	case NoBatchEmpty:
		return "no batch available: nothing to send"
	default:
		return "no batch available: no install id (run yardr telemetry on again)"
	}
}

// Preview is what yardr telemetry show prints: the install id and the
// exact request the daemon would send from the current state, without
// sending it or advancing any cursor. Request is nil when there is no
// batch to send, and NoBatch says why.
type Preview struct {
	InstallID  string   `json:"install_id,omitempty"`
	Switch     string   `json:"telemetry"` // "on" or "off"
	Since      string   `json:"since,omitempty"`
	SendingDay string   `json:"sending_day"`
	SourceDays []string `json:"source_days,omitempty"`
	Hosted     bool     `json:"hosted"`
	Request    *Request `json:"request"`
	NoBatch    string   `json:"no_batch,omitempty"`
}

// NoBatchFor says why no batch goes out at now, "" when one may: the
// switch is off, the setting stands on with no install id, or a batch was
// already attempted on this UTC day. show prints the reason, and the
// daemon's pass goes by the same answer, so what show says is what the
// pass will do.
func NoBatchFor(on bool, state *State, now time.Time) string {
	switch {
	case !on:
		return NoBatchOff
	case state == nil || state.InstallID == "":
		return NoBatchNoID
	case state.LastAttemptDay == dayOf(now):
		return NoBatchAttempted
	}
	return ""
}

// Pending is the request the yard would send at a moment, with the
// eligible completed source days it holds and the older ones the
// seven-day cap leaves out. Request is nil when there is nothing to send:
// a hosted yard with no eligible counts and no running crew (a self-run
// batch always holds at least its daily).
type Pending struct {
	Request    *Request
	SourceDays []string
	Dropped    []string
}

// Prepare builds the request a pass at now would send from the yard as it
// stands. It is the one place the batch is made: show prints what it
// returns and the daemon posts it, so with no change of state between
// the two they are the same request. It sends nothing and advances
// nothing, and never includes pre-consent activity.
func Prepare(ctx context.Context, st store.API, state *State, hosted bool, now time.Time) (*Pending, error) {
	out := &Pending{}
	if state == nil {
		return out, nil // no id, no batch (Build says the same)
	}
	out.SourceDays, out.Dropped = SourceRange(state.EligibleFrom, state.LastDaySent, now)
	in := BuildInput{
		State:      state,
		SendingDay: dayOf(now),
		SourceDays: out.SourceDays,
		Hosted:     hosted,
		Tag:        version.Tag(),
		GOOS:       runtime.GOOS,
	}
	if len(out.SourceDays) > 0 {
		since, until := dayStart(out.SourceDays[0]), dayStart(in.SendingDay)
		for _, q := range []struct {
			kind string
			to   *[]store.Event
		}{
			{"claimed", &in.Claimed},
			{"held", &in.Held},
			{"advanced", &in.Advanced},
			{"gave_up", &in.GaveUp},
		} {
			evs, err := eventsInWindow(ctx, st, q.kind, since, until)
			if err != nil {
				return nil, err
			}
			*q.to = evs
		}
	}
	var err error
	if in.Crew, err = st.Crew(ctx); err != nil {
		return nil, err
	}
	if batch := Build(in); len(batch) > 0 {
		out.Request = &Request{APIKey: APIKey, Batch: batch}
	}
	return out, nil
}

// Show previews the current request: the sending-day runtime snapshot
// and the eligible completed source days in the batch. It sends nothing
// and advances nothing, and never previews pre-consent activity.
func Show(ctx context.Context, st store.API, statePath string, now time.Time) (*Preview, error) {
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	on := settings[store.TelemetrySetting] == "true"
	hosted := settings[store.HostedSetting] == "true"
	state, err := LoadState(statePath)
	if err != nil {
		return nil, err
	}
	out := &Preview{SendingDay: dayOf(now), Hosted: hosted}
	if on {
		out.Switch = "on"
	} else {
		out.Switch = "off"
	}
	if state != nil {
		out.InstallID, out.Since = state.InstallID, state.since()
	}
	if out.NoBatch = NoBatchFor(on, state, now); out.NoBatch != "" {
		return out, nil
	}
	next, err := Prepare(ctx, st, state, hosted, now)
	if err != nil {
		return nil, err
	}
	out.SourceDays, out.Request = next.SourceDays, next.Request
	if next.Request == nil {
		out.NoBatch = NoBatchEmpty
	}
	return out, nil
}

// dayStart is the UTC midnight a YYYY-MM-DD day starts at.
func dayStart(day string) time.Time {
	d, err := parseDay(day)
	if err != nil {
		return time.Time{}
	}
	return d
}

// eventPageLimit is the first page read of one event kind for a batch's
// window, doubled while the window may hold more, up to eventLimitCap: a
// busy yard's week of claims still counts whole.
const (
	eventPageLimit = 1000
	eventLimitCap  = 100000
)

// eventsInWindow reads one kind's events with timestamps in
// [since, until): the most recent page first, grown while its oldest
// event is still inside the window, so no in-window event is missed for
// a page cut short.
func eventsInWindow(ctx context.Context, st store.API, kind string, since, until time.Time) ([]store.Event, error) {
	var out []store.Event
	for limit := eventPageLimit; ; limit *= 2 {
		evs, err := st.EventsOfKind(ctx, kind, limit)
		if err != nil {
			return nil, err
		}
		out = out[:0]
		for _, e := range evs {
			if !e.At.Before(since) && e.At.Before(until) {
				out = append(out, e)
			}
		}
		if len(evs) < limit || evs[0].At.Before(since) || limit >= eventLimitCap {
			return out, nil
		}
	}
}
