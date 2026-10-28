package dispatch_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
	"yardr.dev/yardr/internal/telemetry"
)

// answerWords is what a refusing endpoint says: it must not reach the log.
const answerWords = "SENTINEL-answer-words"

// received is one request the stand-in was sent.
type received struct {
	method, contentType string
	body                []byte
}

// posthogStandIn stands in for the project's batch endpoint. It records
// every request it is sent and answers as the test says, and it is the
// only address a telemetry test gives the dispatcher, so none reaches the
// real project.
type posthogStandIn struct {
	URL string

	mu      sync.Mutex
	got     []received
	answer  http.HandlerFunc
	hook    func() // runs as a request arrives, before it is answered
	release chan struct{}
}

func newPosthogStandIn(t *testing.T) *posthogStandIn {
	t.Helper()
	p := &posthogStandIn{release: make(chan struct{})}
	p.takes()
	srv := httptest.NewServer(http.HandlerFunc(p.serve))
	// Cleanups run last in, first out: a handler that hangs is let go
	// before the listener waits for it.
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(p.release) })
	p.URL = srv.URL
	return p
}

func (p *posthogStandIn) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	p.got = append(p.got, received{r.Method, r.Header.Get("Content-Type"), body})
	answer, hook := p.answer, p.hook
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	answer(w, r)
}

// answers makes the stand-in answer every request that comes with status
// and body.
func (p *posthogStandIn) answers(status int, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answer = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		io.WriteString(w, body)
	}
}

// takes is the answer of a project that took the batch.
func (p *posthogStandIn) takes() { p.answers(http.StatusOK, `{"status":"Ok"}`) }

// loses takes the request and closes the connection without an answer, as
// a response lost on its way back.
func (p *posthogStandIn) loses() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answer = func(w http.ResponseWriter, _ *http.Request) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}
}

// hangs takes the request and never answers it.
func (p *posthogStandIn) hangs() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answer = func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-p.release:
		case <-r.Context().Done():
		}
	}
}

// requests are the requests the stand-in was sent, as they came.
func (p *posthogStandIn) requests() []received {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]received(nil), p.got...)
}

// batches are those requests read as telemetry requests.
func (p *posthogStandIn) batches(t *testing.T) []telemetry.Request {
	t.Helper()
	var out []telemetry.Request
	for _, r := range p.requests() {
		var req telemetry.Request
		if err := json.Unmarshal(r.body, &req); err != nil {
			t.Fatalf("a request that is no batch: %v\n%s", err, r.body)
		}
		out = append(out, req)
	}
	return out
}

// eventsNamed are the events of one name in a request.
func eventsNamed(req telemetry.Request, name string) []telemetry.Event {
	var out []telemetry.Event
	for _, e := range req.Batch {
		if e.Name == name {
			out = append(out, e)
		}
	}
	return out
}

// telemetryYard is a dispatcher on a throwaway store, in the store's home
// as the daemon is, that posts to a stand-in listener and reads a clock
// the test moves.
type telemetryYard struct {
	t       *testing.T
	s       store.API
	d       *dispatch.Dispatcher
	posthog *posthogStandIn
	log     *lockedWriter
	now     time.Time
	state   string // the state file's path
}

func newTelemetryYard(t *testing.T) *telemetryYard {
	t.Helper()
	home := t.TempDir()
	y := &telemetryYard{
		t:       t,
		s:       storetest.NewIn(t, home),
		posthog: newPosthogStandIn(t),
		log:     &lockedWriter{},
		state:   config.At(home).TelemetryState(),
	}
	y.d = y.dispatcher()
	return y
}

// dispatcher is a daemon of the yard, as a restarted one is.
func (y *telemetryYard) dispatcher() *dispatch.Dispatcher {
	d := &dispatch.Dispatcher{
		Store:        y.s,
		Runners:      map[string]dispatch.Runner{},
		Home:         filepath.Dir(y.state),
		Log:          slog.New(slog.NewTextHandler(y.log, nil)),
		TelemetryURL: y.posthog.URL,
	}
	dispatch.SetClock(d, func() time.Time { return y.now })
	return d
}

// pass runs one pass of the daemon at at.
func (y *telemetryYard) pass(at time.Time) {
	y.t.Helper()
	y.now = at
	must(y.t, y.d.Reconcile(context.Background()))
}

// on turns telemetry on at at, as yardr telemetry on does.
func (y *telemetryYard) on(at time.Time) *telemetry.Change {
	y.t.Helper()
	change, err := telemetry.On(context.Background(), y.s, y.state, "me", at)
	must(y.t, err)
	return change
}

func (y *telemetryYard) off(at time.Time) {
	y.t.Helper()
	_, err := telemetry.Off(context.Background(), y.s, y.state, "me", at)
	must(y.t, err)
}

// gaveUp logs one gave_up event and returns the UTC day the store stamped
// it with, which is the source day the pass counts it on. The store stamps
// with the wall clock, so a test of source days is laid out around this
// day: the consent and the dispatcher's clock are set from it.
func (y *telemetryYard) gaveUp() time.Time {
	y.t.Helper()
	ctx := context.Background()
	must(y.t, y.s.LogDispatch(ctx, "", "gave_up", "yard", map[string]any{"failures": 3}))
	evs, err := y.s.EventsOfKind(ctx, "gave_up", 1)
	must(y.t, err)
	if len(evs) != 1 {
		y.t.Fatalf("gave_up events = %v", evs)
	}
	return evs[0].At.UTC().Truncate(24 * time.Hour)
}

func (y *telemetryYard) loadState() *telemetry.State {
	y.t.Helper()
	state, err := telemetry.LoadState(y.state)
	must(y.t, err)
	if state == nil {
		y.t.Fatalf("no state file at %s", y.state)
	}
	return state
}

// said is the lines of the dispatcher's log that carry text.
func (y *telemetryYard) said(text string) []string {
	var out []string
	for _, line := range strings.Split(y.log.String(), "\n") {
		if strings.Contains(line, text) {
			out = append(out, line)
		}
	}
	return out
}

// dayAt is hour o'clock UTC, days after day.
func dayAt(day time.Time, days, hour int) time.Time {
	return day.AddDate(0, 0, days).Add(time.Duration(hour) * time.Hour)
}

func isoDay(t time.Time) string { return t.UTC().Format("2006-01-02") }

// A yard that never turned telemetry on sends nothing and keeps no file.
// Turned on, the daemon sends one well-formed batch a day however many
// passes run or daemons start, a day's batch is never made twice, and
// turned off it sends none: not that day, not the next.
func TestTelemetrySendsOneBatchADay(t *testing.T) {
	t.Parallel()
	y := newTelemetryYard(t)
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)

	y.pass(dayAt(day, 0, 9))
	if n := len(y.posthog.requests()); n != 0 {
		t.Fatalf("a yard that never turned telemetry on sent %d requests", n)
	}
	if _, err := os.Stat(y.state); !os.IsNotExist(err) {
		t.Fatalf("the state file of a yard that never turned telemetry on: %v", err)
	}

	change := y.on(dayAt(day, 0, 10))
	for _, hour := range []int{10, 11, 23} {
		y.pass(dayAt(day, 0, hour))
	}
	reqs := y.posthog.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests on the first day, want 1", len(reqs))
	}
	if reqs[0].method != http.MethodPost || reqs[0].contentType != "application/json" {
		t.Errorf("request = %s as %s", reqs[0].method, reqs[0].contentType)
	}
	first := y.posthog.batches(t)[0]
	if first.APIKey != telemetry.APIKey || len(first.Batch) != 1 {
		t.Fatalf("batch = %+v", first)
	}
	daily := first.Batch[0]
	wantOS := runtime.GOOS
	if wantOS != "darwin" && wantOS != "linux" && wantOS != "windows" {
		wantOS = "other"
	}
	if daily.Name != telemetry.DailyEvent || daily.DistinctID != change.InstallID ||
		daily.Timestamp != "2026-10-28T10:00:00Z" || daily.UUID == "" ||
		!reflect.DeepEqual(daily.Properties, map[string]any{
			"$process_person_profile": true, "$geoip_disable": true, "version": "development", "os": wantOS, "telemetry_age_days": 0.0}) {
		t.Errorf("daily = %+v", daily)
	}
	if st := y.loadState(); st.LastAttemptDay != "2026-10-28" || st.LastDaySent != "" {
		t.Errorf("state after the first day = %+v", st)
	}

	// A daemon that starts anew the same day goes by the file.
	y.d = y.dispatcher()
	y.pass(dayAt(day, 0, 12))
	if n := len(y.posthog.requests()); n != 1 {
		t.Fatalf("a restarted daemon made request %d on the same day", n)
	}

	// The next day it sends again, dated that day, with a UUID of its own.
	y.pass(dayAt(day, 1, 0))
	if n := len(y.posthog.requests()); n != 2 {
		t.Fatalf("%d requests after the second day, want 2", n)
	}
	second := eventsNamed(y.posthog.batches(t)[1], telemetry.DailyEvent)[0]
	if second.Timestamp != "2026-10-29T00:00:00Z" || second.UUID == daily.UUID || second.Properties["telemetry_age_days"] != 1.0 {
		t.Errorf("second daily = %+v", second)
	}

	// Turned off before the third day's batch, it makes no request that
	// day or the next.
	y.off(dayAt(day, 2, 6))
	y.pass(dayAt(day, 2, 6))
	y.pass(dayAt(day, 2, 12))
	y.pass(dayAt(day, 3, 9))
	if n := len(y.posthog.requests()); n != 2 {
		t.Fatalf("%d requests after telemetry was turned off, want 2", n)
	}
	if st := y.loadState(); st.LastAttemptDay != "2026-10-29" {
		t.Errorf("last_attempt_day = %q after the off", st.LastAttemptDay)
	}
}

// A batch the project did not take, for whatever reason, is told once by
// its status and a class and by nothing of the request, the answer or the
// install id; it is not tried again that day however many passes run, and
// the next UTC day it goes again with the source-day events' own UUIDs and
// timestamps while the daily describes the retry day. A 2xx that names
// quota-limited entries is a refusal that moves no source cursor.
func TestTelemetryRefusedBatchWaitsForTomorrow(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		refuse  func(*posthogStandIn)
		timeout time.Duration // the dispatcher's, where the refusal is its own timeout; else its default
		status  string
		class   string
	}{
		{"service unavailable", func(p *posthogStandIn) { p.answers(503, answerWords) }, 0, "503", "status"},
		{"unauthorized", func(p *posthogStandIn) { p.answers(401, `{"detail":"`+answerWords+`"}`) }, 0, "401", "status"},
		{"quota limited", func(p *posthogStandIn) { p.answers(200, `{"status":"Ok","quota_limited":["events"]}`) }, 0, "200", "quota_limited"},
		{"response lost", (*posthogStandIn).loses, 0, "0", "network"},
		// Short, but long enough for the request to arrive before it ends;
		// every case that expects an answer keeps the default, which a loaded
		// machine does not outrun.
		{"no answer in time", (*posthogStandIn).hangs, time.Second, "0", "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			y := newTelemetryYard(t)
			evDay := y.gaveUp()
			change := y.on(dayAt(evDay, -1, 10)) // eligible from evDay
			tc.refuse(y.posthog)
			if tc.timeout > 0 {
				y.d.TelemetryTimeout = tc.timeout
			}

			s1 := dayAt(evDay, 1, 9)
			y.pass(s1)
			if n := len(y.posthog.requests()); n != 1 {
				t.Fatalf("%d requests on the day it was refused, want 1", n)
			}
			warned := y.said("telemetry: batch not accepted")
			if len(warned) != 1 || !strings.Contains(warned[0], "status="+tc.status) || !strings.Contains(warned[0], "class="+tc.class) {
				t.Fatalf("warnings = %q, want one of status %s class %s", warned, tc.status, tc.class)
			}
			for _, secret := range []string{change.InstallID, telemetry.APIKey, answerWords, y.posthog.URL,
				strings.TrimPrefix(y.posthog.URL, "http://"), telemetry.FailureEvent, "distinct_id"} {
				if strings.Contains(y.log.String(), secret) {
					t.Errorf("the log carries %q:\n%s", secret, y.log.String())
				}
			}
			y.pass(dayAt(evDay, 1, 10))
			y.pass(dayAt(evDay, 1, 23))
			if n := len(y.posthog.requests()); n != 1 || len(y.said("telemetry: batch not accepted")) != 1 {
				t.Fatalf("a refused batch was tried again the same day: %d requests, %d warnings", n, len(y.said("telemetry: batch not accepted")))
			}
			if st := y.loadState(); st.LastAttemptDay != isoDay(s1) || st.LastDaySent != "" {
				t.Fatalf("state after the refusal = %+v", st)
			}

			// The next day it goes again, and is taken.
			y.posthog.takes()
			y.d.TelemetryTimeout = time.Minute
			s2 := dayAt(evDay, 2, 9)
			y.pass(s2)
			batches := y.posthog.batches(t)
			if len(batches) != 2 {
				t.Fatalf("%d requests after the next day, want 2", len(batches))
			}
			sources := func(req telemetry.Request) []telemetry.Event { return eventsNamed(req, telemetry.FailureEvent) }
			if len(sources(batches[0])) != 1 || !reflect.DeepEqual(sources(batches[0]), sources(batches[1])) {
				t.Errorf("the source-day events changed between the tries:\n%+v\n%+v", sources(batches[0]), sources(batches[1]))
			}
			if e := sources(batches[1])[0]; e.Timestamp != isoDay(evDay)+"T12:00:00Z" || e.Properties["kind"] != "gave_up" || e.Properties["count"] != 1.0 {
				t.Errorf("source-day event = %+v", e)
			}
			d1, d2 := eventsNamed(batches[0], telemetry.DailyEvent)[0], eventsNamed(batches[1], telemetry.DailyEvent)[0]
			if d1.UUID == d2.UUID || d1.Timestamp != isoDay(s1)+"T00:00:00Z" || d2.Timestamp != isoDay(s2)+"T00:00:00Z" ||
				d2.Properties["telemetry_age_days"] != d1.Properties["telemetry_age_days"].(float64)+1 {
				t.Errorf("the daily did not describe the day it was sent:\n%+v\n%+v", d1, d2)
			}
			if st := y.loadState(); st.LastDaySent != isoDay(s1) {
				t.Errorf("last_day_sent = %q, want %s", st.LastDaySent, isoDay(s1))
			}
		})
	}
}

// A yard that was down, or whose batches were refused, catches up in one
// batch: three eligible days are three source days, and ten are the
// latest seven with the earlier range said once in the log.
func TestTelemetryBackfillsAtMostSevenDays(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		eligible  int // eligible_from, in days from the source day with the event
		drops     bool
		droppedTo int // the last day dropped, in days from the same
	}{
		{name: "three days", eligible: -2},
		{name: "seven days", eligible: -6},
		{name: "ten days", eligible: -9, drops: true, droppedTo: -7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			y := newTelemetryYard(t)
			evDay := y.gaveUp()
			y.on(dayAt(evDay, tc.eligible-1, 10))
			y.pass(dayAt(evDay, 1, 9)) // the source days run through evDay
			y.pass(dayAt(evDay, 1, 12))
			batches := y.posthog.batches(t)
			if len(batches) != 1 {
				t.Fatalf("%d requests, want 1 batch for the whole gap", len(batches))
			}
			if got := eventsNamed(batches[0], telemetry.FailureEvent); len(got) != 1 || got[0].Timestamp != isoDay(evDay)+"T12:00:00Z" {
				t.Errorf("failure events = %+v", got)
			}
			if st := y.loadState(); st.LastDaySent != isoDay(evDay) {
				t.Errorf("last_day_sent = %q, want %s: the cursor goes through the latest day the batch held", st.LastDaySent, isoDay(evDay))
			}
			said := y.said("older than a batch holds")
			if !tc.drops {
				if len(said) != 0 {
					t.Errorf("a gap that fits said %q", said)
				}
				return
			}
			want := []string{"from=" + isoDay(dayAt(evDay, tc.eligible, 0)), "to=" + isoDay(dayAt(evDay, tc.droppedTo, 0))}
			if len(said) != 1 || !strings.Contains(said[0], want[0]) || !strings.Contains(said[0], want[1]) {
				t.Errorf("the dropped range was said as %q, want one line with %q", said, want)
			}
		})
	}
}

// Turning off and on skips every day before the new eligible_from, even
// those the old cursor had not reached, and nothing goes while it is off.
func TestTelemetryOffAndOnSkipsTheDaysBetween(t *testing.T) {
	t.Parallel()
	y := newTelemetryYard(t)
	evDay := y.gaveUp()
	y.on(dayAt(evDay, -5, 10)) // eligible from evDay-4
	y.pass(dayAt(evDay, -3, 9))
	if n := len(y.posthog.requests()); n != 1 {
		t.Fatalf("%d requests while on, want 1", n)
	}
	y.off(dayAt(evDay, -1, 12)) // mid-day, before that day's pass: none follows
	y.pass(dayAt(evDay, -1, 13))
	y.pass(dayAt(evDay, 0, 9))
	y.pass(dayAt(evDay, 1, 8))
	if n := len(y.posthog.requests()); n != 1 {
		t.Fatalf("%d requests after it was turned off, want still 1", n)
	}
	y.on(dayAt(evDay, 1, 10)) // eligible from evDay+2: the event on evDay is before it
	y.pass(dayAt(evDay, 1, 10))
	y.pass(dayAt(evDay, 3, 9))
	batches := y.posthog.batches(t)
	if len(batches) != 3 {
		t.Fatalf("%d requests after it was turned on again, want 3", len(batches))
	}
	for i, b := range batches {
		if got := eventsNamed(b, telemetry.FailureEvent); len(got) != 0 {
			t.Errorf("request %d carries %+v from before the new eligible_from", i, got)
		}
	}
}

// A hosted yard sends no daily: with nothing to count and no crew there is
// no request and no attempt spent for the day, and with counts the batch
// holds only them, marked hosted.
func TestTelemetryHostedSendsCountsOnly(t *testing.T) {
	t.Parallel()
	y := newTelemetryYard(t)
	must(t, y.s.SetSetting(context.Background(), store.HostedSetting, "true", "me"))
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	y.on(dayAt(day, 0, 10))
	y.pass(dayAt(day, 0, 11))
	y.pass(dayAt(day, 0, 20))
	if n := len(y.posthog.requests()); n != 0 {
		t.Fatalf("a hosted yard with nothing to count sent %d requests", n)
	}
	if st := y.loadState(); st.LastAttemptDay != "" {
		t.Fatalf("an attempt was spent with nothing to send: %+v", st)
	}

	counted := newTelemetryYard(t)
	must(t, counted.s.SetSetting(context.Background(), store.HostedSetting, "true", "me"))
	evDay := counted.gaveUp()
	counted.on(dayAt(evDay, -1, 10))
	counted.pass(dayAt(evDay, 1, 9))
	batches := counted.posthog.batches(t)
	if len(batches) != 1 || len(batches[0].Batch) != 1 {
		t.Fatalf("batches = %+v", batches)
	}
	if e := batches[0].Batch[0]; e.Name != telemetry.FailureEvent || e.Properties["hosted"] != true || e.Properties["kind"] != "gave_up" {
		t.Errorf("hosted batch = %+v", e)
	}

	// A crew session that runs is a snapshot worth a request on its own.
	crewed := newTelemetryYard(t)
	ctx := context.Background()
	must(t, crewed.s.SetSetting(ctx, store.HostedSetting, "true", "me"))
	_, err := crewed.s.PutCrew(ctx, store.CrewMember{Name: "ops", Runner: "fake", Config: map[string]any{"kind": "claude"}, Enabled: true}, "me")
	must(t, err)
	must(t, crewed.s.CrewStarted(ctx, "ops", map[string]any{"agent": "a1"}))
	crewed.on(dayAt(day, 0, 10))
	crewed.pass(dayAt(day, 0, 11))
	batches = crewed.posthog.batches(t)
	if len(batches) != 1 || len(batches[0].Batch) != 1 {
		t.Fatalf("a hosted yard with a running crew session sent %+v", batches)
	}
	if e := batches[0].Batch[0]; e.Name != telemetry.CrewEvent || e.Properties["hosted"] != true ||
		e.Properties["kind"] != "claude" || e.Properties["model"] != "default" || e.Properties["running_crew_sessions"] != 1.0 {
		t.Errorf("hosted crew batch = %+v", e)
	}
}

// With no change between the preview and the pass, the stand-in receives
// the request yardr telemetry show printed, and by then the attempt is
// already written, so a daemon that dies mid-request makes no second one.
func TestTelemetrySendsWhatShowPrinted(t *testing.T) {
	t.Parallel()
	y := newTelemetryYard(t)
	evDay := y.gaveUp()
	y.on(dayAt(evDay, -1, 10))
	now := dayAt(evDay, 1, 9)
	preview, err := telemetry.Show(context.Background(), y.s, y.state, now)
	must(t, err)
	if preview.Request == nil || len(preview.SourceDays) != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	attempts := make(chan string, 1)
	y.posthog.hook = func() {
		state, _ := telemetry.LoadState(y.state)
		attempts <- state.LastAttemptDay
	}
	y.pass(now)
	reqs := y.posthog.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(reqs))
	}
	want, err := json.Marshal(preview.Request)
	must(t, err)
	if string(reqs[0].body) != string(want) {
		t.Errorf("the stand-in received\n%s\nshow printed\n%s", reqs[0].body, want)
	}
	if got := <-attempts; got != isoDay(now) {
		t.Errorf("last_attempt_day = %q as the request arrived, want %s", got, isoDay(now))
	}
	if after, err := telemetry.Show(context.Background(), y.s, y.state, now); err != nil || after.NoBatch != telemetry.NoBatchAttempted {
		t.Errorf("show after the send = %+v, %v", after, err)
	}
}

// A fault of the yard's own, a state file that cannot be read, an id that
// is gone or a lock that cannot be taken, is said once however many passes
// find it, and said again when it comes back after a pass that met none.
// It sends nothing, and does not make a state file.
func TestTelemetryLocalFaultsAreSaidOnce(t *testing.T) {
	t.Parallel()
	y := newTelemetryYard(t)
	day := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	y.on(dayAt(day, 0, 10))
	must(t, os.WriteFile(y.state, []byte("{broken"), 0o600))
	for hour := 11; hour < 14; hour++ {
		y.pass(dayAt(day, 0, hour))
	}
	if said := y.said("telemetry: reading the state file"); len(said) != 1 {
		t.Fatalf("an unreadable state file was said %d times:\n%s", len(said), y.log.String())
	}
	must(t, os.Remove(y.state))
	for hour := 14; hour < 17; hour++ {
		y.pass(dayAt(day, 0, hour))
	}
	if said := y.said("this yard has no install id"); len(said) != 1 || !strings.Contains(said[0], "yardr telemetry on") {
		t.Fatalf("a missing id was said as %q", said)
	}
	if n := len(y.posthog.requests()); n != 0 {
		t.Fatalf("%d requests from a yard with no id", n)
	}
	if _, err := os.Stat(y.state); !os.IsNotExist(err) {
		t.Fatalf("the sender made a state file: %v", err)
	}
	// The setting stands on, and the way out is on again: the pass finds
	// the new id and sends.
	y.on(dayAt(day, 0, 17))
	y.pass(dayAt(day, 0, 18))
	if n := len(y.posthog.requests()); n != 1 {
		t.Fatalf("%d requests after on again, want 1", n)
	}

	// A fault met after the batch is built is said once as well, for the
	// passes that meet it again, and said again the day it returns: here
	// the lock file is a directory, which cannot be opened as one.
	lock := y.state + ".lock"
	for round, offset := range []int{1, 2} {
		must(t, os.Remove(lock))
		must(t, os.Mkdir(lock, 0o700))
		for hour := 9; hour < 12; hour++ {
			y.pass(dayAt(day, offset, hour))
		}
		if said := y.said("telemetry: recording the attempt"); len(said) != round+1 {
			t.Fatalf("round %d: a lock that cannot be taken was said %d times, want %d:\n%s", round+1, len(said), round+1, y.log.String())
		}
		if n := len(y.posthog.requests()); n != 1+round {
			t.Fatalf("round %d: %d requests while the lock could not be taken, want %d", round+1, n, 1+round)
		}
		must(t, os.Remove(lock)) // the way out: a pass that meets no fault sends
		y.pass(dayAt(day, offset, 12))
		if n := len(y.posthog.requests()); n != 2+round {
			t.Fatalf("round %d: %d requests after the lock could be taken, want %d", round+1, n, 2+round)
		}
	}
}
