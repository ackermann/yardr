package otel_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"yardr.dev/yardr/internal/otel"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

func TestMain(m *testing.M) { storetest.Main(m) }

// collector is an OTLP/HTTP endpoint that keeps what it is sent.
type collector struct {
	*httptest.Server
	mu sync.Mutex
	// refuse answers the next requests with this status, one each.
	refuse   []int
	requests int
	bodies   []string
	spans    []gotSpan
	logs     []gotLog
}

type attrs []struct {
	Key   string `json:"key"`
	Value struct {
		String *string `json:"stringValue"`
		Int    *string `json:"intValue"`
	} `json:"value"`
}

func (a attrs) get(key string) string {
	for _, kv := range a {
		if kv.Key == key {
			if kv.Value.String != nil {
				return *kv.Value.String
			}
			if kv.Value.Int != nil {
				return *kv.Value.Int
			}
		}
	}
	return ""
}

type gotSpan struct {
	TraceID      string `json:"traceId"`
	SpanID       string `json:"spanId"`
	ParentSpanID string `json:"parentSpanId"`
	Name         string `json:"name"`
	Start        string `json:"startTimeUnixNano"`
	End          string `json:"endTimeUnixNano"`
	Attributes   attrs  `json:"attributes"`
	Links        []struct {
		TraceID string `json:"traceId"`
		SpanID  string `json:"spanId"`
	} `json:"links"`
	Status *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

type gotLog struct {
	TraceID string `json:"traceId"`
	Body    struct {
		String string `json:"stringValue"`
	} `json:"body"`
	Attributes attrs `json:"attributes"`
}

func newCollector(t *testing.T, refuse ...int) *collector {
	c := &collector{refuse: refuse}
	c.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.requests++
		if len(c.refuse) > 0 {
			code := c.refuse[0]
			c.refuse = c.refuse[1:]
			http.Error(w, "not now", code)
			return
		}
		if r.Header.Get("Content-Type") != "application/json" || r.Header.Get("X-Token") != "t" {
			t.Errorf("request headers = %v", r.Header)
		}
		c.bodies = append(c.bodies, string(body))
		switch r.URL.Path {
		case "/v1/traces":
			var req struct {
				ResourceSpans []struct {
					Resource   struct{ Attributes attrs }
					ScopeSpans []struct{ Spans []gotSpan }
				}
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("traces body: %v\n%s", err, body)
			}
			for _, rs := range req.ResourceSpans {
				if rs.Resource.Attributes.get("service.name") != "yardr" {
					t.Errorf("resource = %+v", rs.Resource)
				}
				for _, ss := range rs.ScopeSpans {
					c.spans = append(c.spans, ss.Spans...)
				}
			}
		case "/v1/logs":
			var req struct {
				ResourceLogs []struct {
					ScopeLogs []struct{ LogRecords []gotLog }
				}
			}
			if err := json.Unmarshal(body, &req); err != nil {
				t.Errorf("logs body: %v\n%s", err, body)
			}
			for _, rl := range req.ResourceLogs {
				for _, sl := range rl.ScopeLogs {
					c.logs = append(c.logs, sl.LogRecords...)
				}
			}
		default:
			t.Errorf("request to %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"partialSuccess":{}}`))
	}))
	t.Cleanup(c.Close)
	return c
}

// seqs are the seq of every log record received, in order.
func (c *collector) seqs(t *testing.T) []uint64 {
	t.Helper()
	var out []uint64
	for _, l := range c.logs {
		n, err := strconv.ParseUint(l.Attributes.get("yardr.seq"), 10, 64)
		if err != nil {
			t.Fatalf("log record without a seq: %+v", l)
		}
		out = append(out, n)
	}
	return out
}

// named are the spans whose name starts with prefix, in the order received.
func (c *collector) named(prefix string) []gotSpan {
	var out []gotSpan
	for _, s := range c.spans {
		if strings.HasPrefix(s.Name, prefix) {
			out = append(out, s)
		}
	}
	return out
}

const lineFlow = `
new      -> review on done
review   -> approved on done | new on changes
approved -> merged on done
merged terminal
`

// yard is a store with a depot whose beads follow lineFlow, a herdr group of
// builders and an exec group of reviewers.
func yard(t *testing.T) (store.API, context.Context) {
	t.Helper()
	st, ctx := storetest.New(t), context.Background()
	must(t, st.PutDepot(ctx, store.Depot{Name: "web", Base: "origin/main"}, "me"))
	f, err := store.ParseFlow(lineFlow)
	must(t, err)
	f.Name = "web"
	_, err = st.PutFlow(ctx, *f, "me")
	must(t, err)
	must(t, st.SetFlowPointer(ctx, "web", "", "web", "me"))
	must(t, st.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr", Limit: 2,
		Config: map[string]any{"kind": "codex", "model": "gpt-test"}}, "me"))
	must(t, st.PutGroup(ctx, store.Group{Name: "reviewers", Runner: "exec", Limit: 2,
		Config: map[string]any{"cmd": "true"}}, "me"))
	return st, ctx
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// claim starts a session of group for bead in stage and returns its id.
func claim(t *testing.T, st store.API, bead, group, stage string) string {
	t.Helper()
	s, err := st.Claim(context.Background(), bead, group, stage, "yard")
	must(t, err)
	must(t, st.MarkRunning(context.Background(), s.ID, map[string]any{"pane": "p1"}))
	return s.ID
}

func done(t *testing.T, st store.API, bead, outcome, note string) {
	t.Helper()
	_, err := st.Done(context.Background(), bead, outcome, note, store.Expect{}, "agent")
	must(t, err)
}

// export runs an exporter against c until it has caught up.
func export(t *testing.T, st store.API, c *collector, state string, change func(*otel.Exporter)) error {
	t.Helper()
	x := &otel.Exporter{Store: st, Traces: c.URL + "/v1/traces", Logs: c.URL + "/v1/logs",
		Headers: map[string]string{"X-Token": "t"}, State: state, Once: true,
		Backoff: time.Millisecond, Linger: 5 * time.Millisecond}
	if change != nil {
		change(x)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	err := x.Run(ctx)
	if ctx.Err() != nil {
		t.Fatal("the export did not catch up")
	}
	return err
}

// secrets is the text of the bead under test: none of it may leave.
var secrets = []string{"SECRET-TITLE", "SECRET-BODY", "SECRET-NOTE", "SECRET-REASON"}

// A bead that goes new, review, back to new, review, approved, merged is
// one trace: a span per stay, in order and with no parent, and under each
// the spans of the sessions that worked in it.
func TestSpansOfABeadsLife(t *testing.T) {
	st, ctx := yard(t)
	train, err := st.CreateBead(ctx, store.NewBead{Title: "the train", Type: "train", Stage: "new", Depot: "web", ID: "web-e"}, "me")
	must(t, err)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "SECRET-TITLE", Body: "SECRET-BODY", Stage: "new", Depot: "web"}, "me")
	must(t, err)
	must(t, st.AddDep(ctx, train.ID, b.ID, store.DepParent, "me"))

	var sessions []string
	// new: a first session dies, the second reports done.
	died := claim(t, st, b.ID, "builders", "new")
	must(t, st.EndSession(ctx, died, "died", "SECRET-REASON"))
	sessions = append(sessions, died, claim(t, st, b.ID, "builders", "new"))
	done(t, st, b.ID, "done", "")
	sessions = append(sessions, claim(t, st, b.ID, "reviewers", "review"))
	done(t, st, b.ID, "changes", "SECRET-NOTE")
	sessions = append(sessions, claim(t, st, b.ID, "builders", "new"))
	done(t, st, b.ID, "done", "")
	sessions = append(sessions, claim(t, st, b.ID, "reviewers", "review"))
	done(t, st, b.ID, "done", "")
	// approved has no session: a person lands it.
	done(t, st, b.ID, "done", "")
	if got, _ := st.GetBead(ctx, b.ID); got.Stage != "merged" || got.Status != "closed" {
		t.Fatalf("bead = %+v", got)
	}

	c := newCollector(t)
	must(t, export(t, st, c, filepath.Join(t.TempDir(), "state.json"), nil))

	trace := otel.TraceID(b.ID)
	var stays []gotSpan
	for _, s := range c.named("stage ") {
		if s.Attributes.get("yardr.bead") == b.ID {
			stays = append(stays, s)
		}
	}
	var names, outcomes []string
	for _, s := range stays {
		names = append(names, s.Name)
		outcomes = append(outcomes, s.Attributes.get("yardr.outcome"))
		if s.TraceID != trace || s.ParentSpanID != "" {
			t.Errorf("%s: trace %s parent %q, want the bead's trace %s and no parent", s.Name, s.TraceID, s.ParentSpanID, trace)
		}
		if s.Attributes.get("yardr.depot") != "web" || "stage "+s.Attributes.get("yardr.stage") != s.Name {
			t.Errorf("%s: attributes %+v", s.Name, s.Attributes)
		}
		if len(s.Links) != 1 || s.Links[0].TraceID != otel.TraceID(train.ID) || s.Links[0].SpanID == "" {
			t.Errorf("%s: links %+v, want one to the train's trace", s.Name, s.Links)
		}
		if a, b := mustInt(t, s.Start), mustInt(t, s.End); a > b {
			t.Errorf("%s ends before it starts", s.Name)
		}
	}
	if want := []string{"stage new", "stage review", "stage new", "stage review", "stage approved", "stage merged"}; !slices.Equal(names, want) {
		t.Fatalf("stage spans = %v, want %v", names, want)
	}
	if want := []string{"done", "changes", "done", "done", "done", "closed"}; !slices.Equal(outcomes, want) {
		t.Errorf("stage outcomes = %v, want %v", outcomes, want)
	}
	if n := stays[0].Attributes.get("yardr.sessions"); n != "2" {
		t.Errorf("first stay counted %s sessions, want 2", n)
	}

	// Each session is a child of the stay it ran in, under the span id its
	// TRACEPARENT named.
	type want struct {
		parent                          int
		group, runner, attempt, outcome string
		harness, model                  string
		failed                          bool
	}
	wants := []want{
		{0, "builders", "herdr", "1", "died", "codex", "gpt-test", true},
		{0, "builders", "herdr", "2", "done", "codex", "gpt-test", false},
		{1, "reviewers", "exec", "1", "changes", "", "", false},
		{2, "builders", "herdr", "1", "done", "codex", "gpt-test", false},
		{3, "reviewers", "exec", "1", "done", "", "", false},
	}
	got := c.named("session ")
	if len(got) != len(wants) {
		t.Fatalf("%d session spans, want %d: %+v", len(got), len(wants), got)
	}
	for i, w := range wants {
		s := got[i]
		a := s.Attributes
		if s.SpanID != otel.SessionSpanID(sessions[i]) || s.TraceID != trace || s.ParentSpanID != stays[w.parent].SpanID {
			t.Errorf("session %d: span %s trace %s parent %s, want %s %s %s (%s)", i, s.SpanID, s.TraceID, s.ParentSpanID,
				otel.SessionSpanID(sessions[i]), trace, stays[w.parent].SpanID, stays[w.parent].Name)
		}
		if s.Name != "session "+w.group || a.get("yardr.group") != w.group || a.get("yardr.runner") != w.runner ||
			a.get("yardr.attempt") != w.attempt || a.get("yardr.outcome") != w.outcome ||
			a.get("yardr.harness") != w.harness || a.get("yardr.model") != w.model ||
			a.get("yardr.bead") != b.ID || a.get("yardr.depot") != "web" || a.get("yardr.session") != sessions[i] ||
			"stage "+a.get("yardr.stage") != stays[w.parent].Name {
			t.Errorf("session %d: %s %+v, want %+v", i, s.Name, a, w)
		}
		if (s.Status != nil && s.Status.Code == 2) != w.failed {
			t.Errorf("session %d: status %+v, failed should be %v", i, s.Status, w.failed)
		}
	}

	// Every event is a log record, once and in order, under its bead's trace.
	head, err := st.Head(ctx)
	must(t, err)
	seqs := c.seqs(t)
	if len(seqs) != int(head) || seqs[0] != 1 || seqs[len(seqs)-1] != head || !slices.IsSorted(seqs) {
		t.Fatalf("log seqs = %v, want 1..%d", seqs, head)
	}
	var advanced int
	for _, l := range c.logs {
		if l.Body.String != l.Attributes.get("yardr.kind") || l.Attributes.get("yardr.actor") == "" {
			t.Errorf("log record %+v", l)
		}
		if bead := l.Attributes.get("yardr.bead"); (bead == "") != (l.TraceID == "") || (bead != "" && l.TraceID != otel.TraceID(bead)) {
			t.Errorf("log record of bead %q has trace %q", bead, l.TraceID)
		}
		if l.Body.String == "advanced" && l.Attributes.get("yardr.bead") == b.ID {
			advanced++
			if l.Attributes.get("yardr.data.from") == "" || l.Attributes.get("yardr.data.to") == "" {
				t.Errorf("advanced record lacks its stages: %+v", l.Attributes)
			}
		}
	}
	if advanced != 5 {
		t.Errorf("%d advanced records, want 5", advanced)
	}

	// Ids only: nothing a person or an agent wrote is in any request.
	for _, body := range c.bodies {
		for _, s := range secrets {
			if strings.Contains(body, s) {
				t.Errorf("a request carries %s:\n%s", s, body)
			}
		}
	}
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("not a number: %q", s)
	}
	return n
}

// Text is the one way a title or a reason leaves: with it they do.
func TestTextIsSentOnlyWhenAsked(t *testing.T) {
	st, ctx := yard(t)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "SECRET-TITLE", Body: "SECRET-BODY", Stage: "new", Depot: "web"}, "me")
	must(t, err)
	must(t, st.EndSession(ctx, claim(t, st, b.ID, "builders", "new"), "failed", "SECRET-REASON"))
	_, err = st.CloseBead(ctx, b.ID, "SECRET-REASON", store.Expect{}, "me")
	must(t, err)

	c := newCollector(t)
	must(t, export(t, st, c, filepath.Join(t.TempDir(), "state.json"), func(x *otel.Exporter) { x.Text = true }))
	all := strings.Join(c.bodies, "\n")
	if !strings.Contains(all, "SECRET-TITLE") || !strings.Contains(all, "SECRET-REASON") {
		t.Errorf("--include-text sent neither title nor reason:\n%s", all)
	}
	// A bead's body is in no event and on no span, with the flag or not.
	if strings.Contains(all, "SECRET-BODY") {
		t.Errorf("a body was sent:\n%s", all)
	}
	// A bead closed in the stage it was created in still names the stage.
	if s := c.named("stage "); len(s) != 1 || s[0].Name != "stage new" || s[0].Attributes.get("yardr.title") != "SECRET-TITLE" {
		t.Errorf("stage spans = %+v", s)
	}
}

func TestCreatedStageNamesItsStay(t *testing.T) {
	st, ctx := yard(t)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "late", Stage: "review", Depot: "web"}, "me")
	must(t, err)
	_, err = st.CloseBead(ctx, b.ID, "", store.Expect{}, "me")
	must(t, err)

	c := newCollector(t)
	must(t, export(t, st, c, filepath.Join(t.TempDir(), "state.json"), nil))
	if spans := c.named("stage "); len(spans) != 1 || spans[0].Name != "stage review" || spans[0].Attributes.get("yardr.stage") != "review" {
		t.Errorf("stage spans = %+v, want the created stage review", spans)
	}
}

// An exporter that is stopped and started again goes on where it was: the
// spans open at the stop are sent when they end, and no span or log record
// is sent twice or left out.
func TestResumesWhereItStopped(t *testing.T) {
	st, ctx := yard(t)
	state := filepath.Join(t.TempDir(), "home", "otel-export.json")
	c := newCollector(t)

	b, err := st.CreateBead(ctx, store.NewBead{Title: "t", Stage: "new", Depot: "web"}, "me")
	must(t, err)
	first := claim(t, st, b.ID, "builders", "new")
	must(t, export(t, st, c, state, nil))
	if len(c.spans) != 0 {
		t.Fatalf("spans sent before anything ended: %+v", c.spans)
	}
	mid, err := st.Head(ctx)
	must(t, err)
	if got := c.seqs(t); len(got) != int(mid) {
		t.Fatalf("first run sent %v, want 1..%d", got, mid)
	}

	done(t, st, b.ID, "done", "")
	second := claim(t, st, b.ID, "reviewers", "review")
	// A run with nothing new sends nothing; then the rest, in two runs.
	must(t, export(t, st, c, state, nil))
	done(t, st, b.ID, "done", "")
	must(t, export(t, st, c, state, nil))
	must(t, export(t, st, c, state, nil))

	head, err := st.Head(ctx)
	must(t, err)
	seqs := c.seqs(t)
	want := make([]uint64, head)
	for i := range want {
		want[i] = uint64(i + 1)
	}
	if !slices.Equal(seqs, want) {
		t.Fatalf("log seqs over the runs = %v, want each of 1..%d once", seqs, head)
	}
	var names []string
	ids := map[string]bool{}
	for _, s := range c.spans {
		names = append(names, s.Name)
		ids[s.SpanID] = true
	}
	if want := []string{"session builders", "stage new", "session reviewers", "stage review"}; !slices.Equal(names, want) || len(ids) != len(want) {
		t.Fatalf("spans over the runs = %v, want %v once each", names, want)
	}
	// The stay and session that began before the first stop kept their
	// start and their ids.
	sess, stay := c.spans[0], c.spans[1]
	if sess.SpanID != otel.SessionSpanID(first) || sess.ParentSpanID != stay.SpanID || c.spans[2].SpanID != otel.SessionSpanID(second) {
		t.Errorf("spans after the restart: %+v", c.spans)
	}
	evs, err := st.EventsSince(ctx, 0, 0)
	must(t, err)
	for _, e := range evs {
		if e.Bead != nil && *e.Bead == b.ID && e.Kind == "created" && stay.Start != strconv.FormatInt(e.At.UnixNano(), 10) {
			t.Errorf("stay starts at %s, the bead was created at %d", stay.Start, e.At.UnixNano())
		}
	}

	var st2 struct{ Seq, Logs uint64 }
	data, err := os.ReadFile(state)
	must(t, err)
	must(t, json.Unmarshal(data, &st2))
	if st2.Seq != head || st2.Logs != head {
		t.Errorf("state = %s, want both cursors at %d", data, head)
	}
}

// A collector that answers 503 is waited for; when it takes requests again
// it gets everything, once.
func TestWaitsForACollectorThatIsDown(t *testing.T) {
	st, ctx := yard(t)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "t", Stage: "new", Depot: "web"}, "me")
	must(t, err)
	claim(t, st, b.ID, "builders", "new")
	done(t, st, b.ID, "done", "")

	// Three refusals, then it is up.
	c := newCollector(t, 503, 503, 429)
	state := filepath.Join(t.TempDir(), "state.json")
	must(t, export(t, st, c, state, nil))
	head, err := st.Head(ctx)
	must(t, err)
	if seqs := c.seqs(t); len(seqs) != int(head) {
		t.Errorf("log seqs = %v, want 1..%d", seqs, head)
	}
	if len(c.spans) != 2 || c.requests != 5 {
		t.Errorf("%d spans in %d requests, want 2 spans in 5 (3 refused, traces, logs)", len(c.spans), c.requests)
	}
}

// A collector that takes the spans and then goes down before the logs gets
// the logs alone when the exporter is started again.
func TestALostLogsRequestDoesNotRepeatTheSpans(t *testing.T) {
	st, ctx := yard(t)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "t", Stage: "new", Depot: "web"}, "me")
	must(t, err)
	claim(t, st, b.ID, "builders", "new")
	done(t, st, b.ID, "done", "")
	head, err := st.Head(ctx)
	must(t, err)

	c := newCollector(t)
	state := filepath.Join(t.TempDir(), "state.json")
	// The first run is stopped while it waits for the refused logs request.
	stopped := make(chan error, 1)
	rctx, cancel := context.WithCancel(ctx)
	var once sync.Once
	gate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusServiceUnavailable)
		once.Do(cancel)
	}))
	defer gate.Close()
	go func() {
		x := &otel.Exporter{Store: st, Traces: c.URL + "/v1/traces", Logs: gate.URL, Headers: map[string]string{"X-Token": "t"},
			State: state, Backoff: time.Hour, Linger: 5 * time.Millisecond}
		stopped <- x.Run(rctx)
	}()
	select {
	case err := <-stopped:
		must(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the exporter did not stop")
	}
	if len(c.spans) != 2 || len(c.logs) != 0 {
		t.Fatalf("first run: %d spans, %d logs, want 2 and 0", len(c.spans), len(c.logs))
	}
	must(t, export(t, st, c, state, nil))
	if len(c.spans) != 2 {
		t.Errorf("the spans were sent again: %d", len(c.spans))
	}
	if seqs := c.seqs(t); len(seqs) != int(head) {
		t.Errorf("log seqs = %v, want 1..%d", seqs, head)
	}
}

// A refusal that sending again cannot mend ends the export with the
// collector's answer, and the state does not move: nothing is dropped.
func TestARefusedRequestIsAnErrorAndLosesNothing(t *testing.T) {
	st, ctx := yard(t)
	_, err := st.CreateBead(ctx, store.NewBead{Title: "t", Stage: "new", Depot: "web"}, "me")
	must(t, err)
	c := newCollector(t, 401)
	state := filepath.Join(t.TempDir(), "state.json")
	err = export(t, st, c, state, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want the collector's 401", err)
	}
	must(t, export(t, st, c, state, nil))
	head, err := st.Head(ctx)
	must(t, err)
	if seqs := c.seqs(t); len(seqs) != int(head) {
		t.Errorf("log seqs after the refusal = %v, want 1..%d", seqs, head)
	}
}

// Two exporters on one state file would send everything twice.
func TestSecondExporterOnTheSameStateIsRefused(t *testing.T) {
	st, _ := yard(t)
	c := newCollector(t)
	state := filepath.Join(t.TempDir(), "state.json")
	ctx, cancel := context.WithCancel(context.Background())
	running := make(chan error, 1)
	go func() {
		x := &otel.Exporter{Store: st, Logs: c.URL + "/v1/logs", Headers: map[string]string{"X-Token": "t"}, State: state}
		running <- x.Run(ctx)
	}()
	// Once the first has sent something it holds the lock.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		c.mu.Lock()
		n := len(c.logs)
		c.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the first exporter sent nothing")
		}
	}
	err := export(t, st, c, state, nil)
	if err == nil || !strings.Contains(err.Error(), "already uses") {
		t.Errorf("second exporter: %v, want it refused", err)
	}
	cancel()
	must(t, <-running)
}

// restarting is a store whose first subscriptions fail as they do when the
// yardr server restarts: the first ends after one event, the second is
// refused, the third ends after one event. From the fourth on it is the
// store's own, so the test waits for three resubscriptions and no more.
type restarting struct {
	store.API
	mu    sync.Mutex
	calls int
}

func (r *restarting) Subscribe(ctx context.Context, after uint64) (<-chan store.Event, error) {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	switch {
	case call == 2:
		return nil, errors.New("yardr server: connection refused")
	case call > 3:
		return r.API.Subscribe(ctx, after)
	}
	ctx, cancel := context.WithCancel(ctx)
	in, err := r.API.Subscribe(ctx, after)
	if err != nil {
		cancel()
		return nil, err
	}
	out := make(chan store.Event)
	go func() {
		defer close(out)
		defer cancel()
		if e, ok := <-in; ok {
			select {
			case out <- e:
			case <-ctx.Done():
			}
		}
	}()
	return out, nil
}

// An exporter whose yardr server keeps going away goes on after the last
// event it sent each time: every log record once, none left out.
func TestExportsAcrossServerRestarts(t *testing.T) {
	st, ctx := yard(t)
	c := newCollector(t)
	b, err := st.CreateBead(ctx, store.NewBead{Title: "t", Stage: "new", Depot: "web"}, "me")
	must(t, err)
	claim(t, st, b.ID, "builders", "new")
	done(t, st, b.ID, "done", "")
	r := &restarting{API: st}
	must(t, export(t, r, c, filepath.Join(t.TempDir(), "otel-export.json"), nil))
	head, err := st.Head(ctx)
	must(t, err)
	want := make([]uint64, head)
	for i := range want {
		want[i] = uint64(i + 1)
	}
	if got := c.seqs(t); !slices.Equal(got, want) {
		t.Fatalf("log seqs across the restarts = %v, want each of 1..%d once", got, head)
	}
	if r.calls != 4 {
		t.Fatalf("the store was subscribed to %d times, want 4: two streams that ended, one refused, one that lasted", r.calls)
	}
}
