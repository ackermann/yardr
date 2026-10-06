package otel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"yardr.dev/yardr/internal/store"
)

// Exporter reads the yard's event log as one more subscriber and sends it
// to an OTLP/HTTP collector: every event as a log record, and a span for
// each stay of a bead in a stage and each session in it, when it ends. It
// only reads the yard; what it has sent it keeps in its own file (State).
type Exporter struct {
	Store store.API
	// Traces and Logs are the URLs spans and log records are posted to; a
	// signal with no URL is not sent.
	Traces, Logs string
	Headers      map[string]string
	// State is the file the exporter resumes from: the last event sent and
	// the spans still open.
	State string
	// After is where a first run (no state file yet) starts: after this
	// event. 0 is every retained event.
	After uint64
	// Once stops when everything committed before the run began is sent.
	Once bool
	// Text also sends what is not an id: bead titles on spans, and every
	// plain value of an event's data on its log record.
	Text bool
	// Version is this yardr's, the resource's service.version.
	Version string
	Log     *slog.Logger
	Client  *http.Client
	// Backoff is the first wait after a collector that did not take a
	// request, doubled up to MaxBackoff; Linger is how long a batch waits
	// for more events once it has one. Zero values get defaults.
	Backoff, MaxBackoff, Linger time.Duration
}

// batchMax bounds the events of one request to the collector.
const batchMax = 500

// resubscribe is how long the exporter waits before it asks the yardr
// server again after losing it, as yardr watch does.
const resubscribe = 500 * time.Millisecond

// state is what the exporter keeps between runs. Spans are built from the
// events up to Seq and logs are sent up to Logs; the two differ only when a
// run ended between the two requests of one batch.
type state struct {
	Seq  uint64 `json:"seq"`
	Logs uint64 `json:"logs"`
	// Beads holds, per bead, the stay whose span is not sent yet.
	Beads map[string]*stay `json:"beads,omitempty"`
}

// stay is a bead's time in one stage, from the event that put it there.
type stay struct {
	// Stage is "" until an event names it: a created event does not.
	Stage string    `json:"stage,omitempty"`
	Since time.Time `json:"since"`
	Span  string    `json:"span"`
	// Sessions counts the sessions started in this stay, so each knows
	// which attempt it is.
	Sessions int      `json:"sessions,omitempty"`
	Session  *session `json:"session,omitempty"`
}

// session is the live session of a stay.
type session struct {
	ID      string    `json:"id"`
	Group   string    `json:"group,omitempty"`
	Since   time.Time `json:"since"`
	Attempt int       `json:"attempt"`
}

// Run exports until ctx ends (or, with Once, until it has caught up). It
// returns an error only for what waiting cannot mend: a state file it
// cannot use, a collector that refuses the request itself.
func (x *Exporter) Run(ctx context.Context) error {
	x.defaults()
	unlock, err := lockState(x.State)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := loadState(x.State, x.After)
	if err != nil {
		return err
	}
	var head uint64
	if x.Once {
		if head, err = x.Store.Head(ctx); err != nil {
			return err
		}
	}
	caughtUp := func() bool { return x.Once && min(st.Seq, st.Logs) >= head }
	for ctx.Err() == nil && !caughtUp() {
		ch, err := x.Store.Subscribe(ctx, min(st.Seq, st.Logs))
		for open := err == nil; open && !caughtUp(); {
			var evs []store.Event
			if evs, open = x.take(ctx, ch); len(evs) == 0 {
				continue
			}
			if err := x.export(ctx, st, evs); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		}
		if caughtUp() {
			break
		}
		// The server went away (or dropped us as too slow): wait for it and
		// go on after the last event sent.
		select {
		case <-ctx.Done():
		case <-time.After(resubscribe):
		}
	}
	return nil
}

func (x *Exporter) defaults() {
	if x.Log == nil {
		x.Log = slog.New(slog.DiscardHandler)
	}
	if x.Client == nil {
		x.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if x.Backoff <= 0 {
		x.Backoff = time.Second
	}
	if x.MaxBackoff <= 0 {
		x.MaxBackoff = 30 * time.Second
	}
	if x.Linger <= 0 {
		x.Linger = 50 * time.Millisecond
	}
}

// take waits for an event and then gathers what follows closely, up to
// batchMax. open is false once the subscription has ended.
func (x *Exporter) take(ctx context.Context, ch <-chan store.Event) (evs []store.Event, open bool) {
	select {
	case <-ctx.Done():
		return nil, false
	case e, ok := <-ch:
		if !ok {
			return nil, false
		}
		evs = append(evs, e)
	}
	linger := time.NewTimer(x.Linger)
	defer linger.Stop()
	for len(evs) < batchMax {
		select {
		case <-ctx.Done():
			return evs, false
		case <-linger.C:
			return evs, true
		case e, ok := <-ch:
			if !ok {
				return evs, false
			}
			evs = append(evs, e)
		}
	}
	return evs, true
}

// export sends one batch: the spans its events end, then the events as log
// records, saving the state after each request the collector took. A run
// that ends between the two resumes with the logs alone, so a clean restart
// sends nothing twice. Only a run killed between a request and the save
// that follows it repeats that one request.
func (x *Exporter) export(ctx context.Context, st *state, evs []store.Event) error {
	if last := evs[len(evs)-1].Seq; last > st.Seq {
		var spans []span
		look := &lookup{ctx: ctx, st: x.Store}
		for _, e := range evs {
			if e.Seq > st.Seq {
				x.apply(st, e, look, &spans)
			}
		}
		if len(spans) > 0 && x.Traces != "" {
			req := tracesRequest{[]resourceSpans{{x.resource(), []scopeSpans{{scope{Name: scopeName}, spans}}}}}
			if err := x.post(ctx, x.Traces, req); err != nil {
				return err
			}
		}
		st.Seq = last
		if err := saveState(x.State, st); err != nil {
			return err
		}
	}
	var logs []logRecord
	for _, e := range evs {
		if e.Seq > st.Logs {
			logs = append(logs, x.record(e))
		}
	}
	if len(logs) == 0 {
		return nil
	}
	if x.Logs != "" {
		req := logsRequest{[]resourceLogs{{x.resource(), []scopeLogs{{scope{Name: scopeName}, logs}}}}}
		if err := x.post(ctx, x.Logs, req); err != nil {
			return err
		}
	}
	st.Logs = evs[len(evs)-1].Seq
	return saveState(x.State, st)
}

const scopeName = "yardr"

func (x *Exporter) resource() resource {
	r := resource{[]keyValue{strAttr("service.name", "yardr")}}
	if x.Version != "" {
		r.Attributes = append(r.Attributes, strAttr("service.version", x.Version))
	}
	return r
}

// apply moves st past e, adding to spans the ones e ends. The events that
// matter are the ones that put a bead in a stage (created, advanced,
// reopened), take it out (advanced, closed), and start or end a session
// (claimed; advanced, closed, held, released, session_died, session_failed,
// session_aborted).
func (x *Exporter) apply(st *state, e store.Event, look *lookup, spans *[]span) {
	if e.Bead == nil {
		return
	}
	bead := *e.Bead
	cur := st.Beads[bead]
	enter := func(stage string) {
		if st.Beads == nil {
			st.Beads = map[string]*stay{}
		}
		cur = &stay{Stage: stage, Since: e.At, Span: StaySpanID(bead, e.Seq)}
		st.Beads[bead] = cur
	}
	endSession := func(outcome string, failed bool) {
		if cur == nil || cur.Session == nil {
			return
		}
		*spans = append(*spans, x.sessionSpan(bead, cur, e.At, outcome, failed, text(e.Data, "reason"), look))
		cur.Session = nil
	}
	leave := func(outcome string) {
		if cur == nil {
			return
		}
		*spans = append(*spans, x.staySpan(bead, cur, e.At, outcome, look))
		delete(st.Beads, bead)
		cur = nil
	}
	switch e.Kind {
	case "created", "reopened":
		// Neither names the stage; the event that ends the stay does, or
		// the bead is asked (see staySpan).
		if cur == nil {
			enter("")
		}
	case "claimed":
		// A bead first seen here (its earlier events pruned, or the export
		// begun after them) has its stay start with the claim.
		if cur == nil {
			enter(text(e.Data, "stage"))
		}
		if cur.Stage == "" {
			cur.Stage = text(e.Data, "stage")
		}
		endSession("", false) // not expected: a bead has one session at a time
		cur.Sessions++
		cur.Session = &session{ID: text(e.Data, "session"), Group: text(e.Data, "group"), Since: e.At, Attempt: cur.Sessions}
	case "advanced":
		outcome := text(e.Data, "outcome")
		if outcome == "" {
			outcome = "advanced" // a move no outcome was reported for
		}
		if cur != nil && cur.Stage == "" {
			cur.Stage = text(e.Data, "from")
		}
		endSession(outcome, false)
		leave(outcome)
		enter(text(e.Data, "to"))
	case "closed":
		endSession("closed", false)
		leave("closed")
	case "held", "released":
		endSession(e.Kind, false)
	case "session_died", "session_failed", "session_aborted":
		if cur != nil && cur.Session != nil && cur.Session.ID == text(e.Data, "session") {
			state := e.Kind[len("session_"):]
			// An aborted start is the runtime's failure, not the session's.
			endSession(state, state != "aborted")
		}
	}
}

// staySpan is the span of a stay that ended at end.
func (x *Exporter) staySpan(bead string, s *stay, end time.Time, outcome string, look *lookup) span {
	b := look.bead(bead)
	stage := s.Stage
	if stage == "" && b != nil {
		// Only a bead closed in the stage it was created or reopened in gets
		// here, and a closed bead stays in its stage.
		stage = b.Stage
	}
	sp := span{TraceID: TraceID(bead), SpanID: s.Span, Name: "stage " + stage, Kind: spanInternal,
		Start: nanos(s.Since), End: nanos(end)}
	sp.Attributes = append(x.beadAttrs(bead, b, stage),
		strAttr("yardr.outcome", outcome), intAttr("yardr.sessions", uint64(s.Sessions)))
	if train := look.train(bead); train != "" {
		sp.Links = []link{{TraceID: TraceID(train), SpanID: BeadSpanID(train),
			Attributes: []keyValue{strAttr("yardr.train", train)}}}
	}
	return sp
}

// sessionSpan is the span of s's session, which ended at end: a child of
// the stay's span, under the id the session's TRACEPARENT named.
func (x *Exporter) sessionSpan(bead string, s *stay, end time.Time, outcome string, failed bool, reason string, look *lookup) span {
	b := look.bead(bead)
	sess := s.Session
	sp := span{TraceID: TraceID(bead), SpanID: SessionSpanID(sess.ID), ParentSpanID: s.Span,
		Name: "session " + sess.Group, Kind: spanInternal, Start: nanos(sess.Since), End: nanos(end)}
	sp.Attributes = append(x.beadAttrs(bead, b, s.Stage),
		strAttr("yardr.group", sess.Group), strAttr("yardr.session", sess.ID),
		intAttr("yardr.attempt", uint64(sess.Attempt)), strAttr("yardr.outcome", outcome))
	runner := ""
	if rec := look.session(sess.ID); rec != nil {
		runner = rec.Runner
	}
	g := look.group(sess.Group)
	if runner == "" && g != nil {
		runner = g.Runner
	}
	if runner != "" {
		sp.Attributes = append(sp.Attributes, strAttr("yardr.runner", runner))
	}
	// Only a herdr session is an agent of some harness; what it runs is the
	// group's to say, the model the bead's first (as the runner picks it).
	if runner == "herdr" && g != nil {
		kind, _ := g.Config["kind"].(string)
		if kind == "" {
			kind = "claude"
		}
		sp.Attributes = append(sp.Attributes, strAttr("yardr.harness", kind))
		model, _ := g.Config["model"].(string)
		if b != nil {
			if m, _ := b.Metadata["model"].(string); m != "" {
				model = m
			}
		}
		if model != "" {
			sp.Attributes = append(sp.Attributes, strAttr("yardr.model", model))
		}
	}
	if failed {
		sp.Status = &status{Code: statusError}
		if x.Text {
			sp.Status.Message = reason
		}
	}
	return sp
}

// beadAttrs are the attributes every span of bead has. b is the bead as
// the yard holds it now, nil when it is gone.
func (x *Exporter) beadAttrs(bead string, b *store.Bead, stage string) []keyValue {
	attrs := []keyValue{strAttr("yardr.bead", bead), strAttr("yardr.stage", stage)}
	if b == nil {
		return attrs
	}
	if b.Depot != nil {
		attrs = append(attrs, strAttr("yardr.depot", *b.Depot))
	}
	if x.Text {
		attrs = append(attrs, strAttr("yardr.title", b.Title))
	}
	return attrs
}

// idKeys are the keys of an event's data whose values are ids and names
// (a stage, a group, a session), never text a person or an agent wrote.
// They are what a log record carries of the data unless Text is set.
var idKeys = map[string]bool{"from": true, "to": true, "stage": true, "outcome": true, "group": true,
	"session": true, "crew": true, "flow": true, "hook": true, "kind": true, "runner": true,
	// What crew was in an event logged before this release.
	"resident": true}

// record is e as a log record. Its body is the event's kind.
func (x *Exporter) record(e store.Event) logRecord {
	r := logRecord{Time: nanos(e.At), Observed: nanos(e.At), SeverityNumber: severityInfo, SeverityText: "INFO",
		Body: anyValue{String: &e.Kind}}
	r.Attributes = []keyValue{strAttr("yardr.kind", e.Kind), strAttr("yardr.actor", e.Actor), intAttr("yardr.seq", e.Seq)}
	if e.Bead != nil {
		r.Attributes = append(r.Attributes, strAttr("yardr.bead", *e.Bead))
		r.TraceID = TraceID(*e.Bead)
	}
	keys := make([]string, 0, len(e.Data))
	for k := range e.Data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !x.Text && !idKeys[k] {
			continue
		}
		switch v := e.Data[k].(type) {
		case string:
			r.Attributes = append(r.Attributes, strAttr("yardr.data."+k, v))
		case bool, float64, int, int64, uint64, json.Number:
			r.Attributes = append(r.Attributes, strAttr("yardr.data."+k, fmt.Sprint(v)))
		}
	}
	return r
}

func text(data map[string]any, key string) string {
	s, _ := data[key].(string)
	return s
}

// lookup asks the yard what the events do not say (a bead's depot and train,
// a session's runner, a group's harness), once per batch for each. Nothing
// it finds missing is an error: a span then lacks the attribute.
type lookup struct {
	ctx      context.Context
	st       store.API
	beads    map[string]*store.Bead
	trains   map[string]string
	groups   map[string]*store.Group
	sessions map[string]*store.Session
}

func (l *lookup) bead(id string) *store.Bead {
	if b, ok := l.beads[id]; ok {
		return b
	}
	b, err := l.st.GetBead(l.ctx, id)
	if err != nil {
		b = nil
	}
	if l.beads == nil {
		l.beads = map[string]*store.Bead{}
	}
	l.beads[id] = b
	return b
}

func (l *lookup) train(bead string) string {
	if e, ok := l.trains[bead]; ok {
		return e
	}
	train := ""
	if f, err := l.st.Family(l.ctx, bead); err == nil && f != nil {
		train = f.Train
	}
	if l.trains == nil {
		l.trains = map[string]string{}
	}
	l.trains[bead] = train
	return train
}

func (l *lookup) group(name string) *store.Group {
	if g, ok := l.groups[name]; ok {
		return g
	}
	g, err := l.st.GetGroup(l.ctx, name)
	if err != nil {
		g = nil
	}
	if l.groups == nil {
		l.groups = map[string]*store.Group{}
	}
	l.groups[name] = g
	return g
}

func (l *lookup) session(id string) *store.Session {
	if s, ok := l.sessions[id]; ok {
		return s
	}
	s, err := l.st.GetSession(l.ctx, id)
	if err != nil {
		s = nil
	}
	if l.sessions == nil {
		l.sessions = map[string]*store.Session{}
	}
	l.sessions[id] = s
	return s
}

// loadState reads the state file; a first run starts after the event after.
func loadState(path string, after uint64) (*state, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &state{Seq: after, Logs: after}, nil
	}
	if err != nil {
		return nil, err
	}
	st := &state{}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("%s: %w; remove it to export from the start again", path, err)
	}
	return st, nil
}

// saveState replaces the state file in one step, so a run killed while
// writing leaves the old one.
func saveState(path string, st *state) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// lockState keeps a second exporter off the same state file: two would send
// everything twice. The lock is a file beside it, held while this one runs.
func lockState(path string) (unlock func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("another yardr otel export already uses %s", path)
		}
		return nil, fmt.Errorf("lock %s: %w", path+".lock", err)
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
