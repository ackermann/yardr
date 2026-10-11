package web

import (
	"context"
	"net/http"
	"slices"
	"time"

	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// Limits is /limits: what `yardr limits` answers with no kind named, one
// entry per harness kind in use that a source answers for, and `unmapped`
// naming the kinds in use no source answers for, beside the yard's head
// from before the read, as the lists have it. It has no page: JSON whatever
// the request accepts.
type Limits struct {
	Limits   []KindLimits `json:"limits"`
	Unmapped []string     `json:"unmapped"`
	Head     uint64       `json:"head"`
}

// KindLimits is what one ask came to, for one harness kind: the provider's
// report and what the yard reads from it, or why nothing is known. It is
// what `yardr limits --json` prints per kind, field for field.
type KindLimits struct {
	Kind string `json:"kind"`
	// Limits is the report; nil when nothing is known.
	Limits *limits.Report `json:"limits,omitempty"`
	// Window and ResetsAt are the window an agent of the kind waits for,
	// when one is used up (limits.Report.UsedUp).
	Window   string     `json:"window,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Unknown is why nothing is known, in a line.
	Unknown string `json:"limits_unknown,omitempty"`
}

// defaultLimitsEvery is how long one ask of the limits source stands: a GET
// asks again when the last answer is older. A minute, as signalbox's quota
// kept (QUOTA_EVERY): a provider may be bounded itself in how often it is
// asked, and may renew a stored login as it reads it.
const defaultLimitsEvery = time.Minute

// limitsEntry is what the view knows of one harness kind's limits: the last
// answer and when it was asked for, and the run on its way, if one is.
type limitsEntry struct {
	asked  time.Time
	answer KindLimits
	// run is the run on its way; nil when none is.
	run *limitsRun
}

// limitsRun is one run of the source's command for a kind. done is closed
// when the answer is in, so a read that arrived while it was on its way
// shares it instead of starting a second run.
type limitsRun struct {
	done   chan struct{}
	answer KindLimits
}

// kindsInUse are the harness kinds the yard runs on, each once: its
// default_kind first, then the kinds its herdr crew members and groups name,
// in the store's order. As `yardr limits` asks them.
func kindsInUse(ctx context.Context, st store.API) ([]string, error) {
	var out []string
	add := func(kind string) {
		if kind == "" || slices.Contains(out, kind) {
			return
		}
		out = append(out, kind)
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	add(all[store.DefaultKindSetting])
	crew, err := st.Crew(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range crew {
		if kind, _ := m.Config["kind"].(string); m.Runner == "herdr" {
			add(kind)
		}
	}
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	for _, g := range groups {
		if kind, _ := g.Config["kind"].(string); g.Runner == "herdr" {
			add(kind)
		}
	}
	return out, nil
}

// answered reports whether a source of as says the limits of kind.
func answered(as []limits.Answerer, kind string) bool {
	for _, a := range as {
		if _, ok := a.Limits.Kinds[kind]; ok {
			return true
		}
	}
	return false
}

// limitsKind asks for kind what `yardr limits` asks for it: one run of the
// operator's command, or the last answer while it still stands. Concurrent
// reads share the one run on its way and never start a second per kind; a
// run that fails is an entry with no report, not an error. ctx ends the
// wait, not the run: the run is detached, so the next reader still gets its
// answer, and is bounded by limits.Timeout however it was asked.
func (s *Server) limitsKind(ctx context.Context, kind string) (KindLimits, error) {
	s.limitsMu.Lock()
	e := s.limitsBy[kind]
	if e != nil && e.run == nil && time.Since(e.asked) < s.limitsEvery {
		answer := e.answer
		s.limitsMu.Unlock()
		return answer, nil
	}
	if e != nil && e.run != nil {
		run := e.run
		s.limitsMu.Unlock()
		select {
		case <-run.done:
			return run.answer, nil
		case <-ctx.Done():
			return KindLimits{}, ctx.Err()
		}
	}
	if e == nil {
		e = &limitsEntry{}
		s.limitsBy[kind] = e
	}
	run := &limitsRun{done: make(chan struct{})}
	e.run = run
	e.asked = time.Now()
	s.limitsMu.Unlock()

	answer := askKind(context.WithoutCancel(ctx), s.st, s.home, kind)
	s.limitsMu.Lock()
	e.answer, e.run = answer, nil
	run.answer = answer
	s.limitsMu.Unlock()
	close(run.done)
	return answer, nil
}

// handleLimits answers /limits: what `yardr limits` answers with no kind
// named, beside the head from before the read. A GET may run the
// operator's command: at most one run per kind a minute, however often the
// route is read; reads in between share the last answer, and concurrent
// reads the one run on its way. A provider that fails nulls its entry
// without failing the route. The answer is one shape, the command's, on
// loopback and with --unsafe alike.
func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, err := s.st.Head(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := Limits{Limits: []KindLimits{}, Unmapped: []string{}, Head: head}
	as, err := limits.Answerers(ctx, s.st, s.home)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(as) == 0 {
		// As `yardr limits`: no source says limits, so nothing is known
		// and nothing was asked.
		w.Header().Set("Cache-Control", "no-store")
		s.data(w, r, http.StatusOK, out)
		return
	}
	uses, err := kindsInUse(ctx, s.st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, kind := range uses {
		if !answered(as, kind) {
			out.Unmapped = append(out.Unmapped, kind)
			continue
		}
		k, err := s.limitsKind(ctx, kind)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out.Limits = append(out.Limits, k)
	}
	// The yard moves under the list; a stored copy is a stale one.
	w.Header().Set("Cache-Control", "no-store")
	s.data(w, r, http.StatusOK, out)
}

// askKind asks for kind, once, and says what it came to.
func askKind(ctx context.Context, st limits.Store, home, kind string) KindLimits {
	out := KindLimits{Kind: kind}
	r, err := limits.Ask(ctx, st, home, kind)
	if err != nil {
		out.Unknown = err.Error()
		return out
	}
	out.Limits = r
	if w := r.UsedUp(); w != nil {
		out.Window, out.ResetsAt = w.Label, w.ResetsAt
	}
	return out
}
