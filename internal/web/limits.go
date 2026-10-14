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
// entry per provider in use, one per source that answers for itself, and
// `unmapped` naming the kinds in use that resolve to nothing, beside the
// yard's head from before the read, as the lists have it. It has a page
// under the Config entry, as the lists do.
type Limits struct {
	Limits   []ProviderLimits `json:"limits"`
	Sources  []SourceLimits   `json:"sources"`
	Unmapped []string         `json:"unmapped"`
	Head     uint64           `json:"head"`
}

// ProviderLimits is what one ask came to, for one provider: who burns it,
// the provider's report and what the yard reads from it, or why nothing is
// known. It is what `yardr limits --json` prints per provider, field for
// field.
type ProviderLimits struct {
	Provider string `json:"provider"`
	// Kinds are the harness kinds burning the provider, each once.
	Kinds []string `json:"kinds"`
	// BurnedBy is who burns it, as doctor names them: "group builders",
	// "crew member yardmaster", "the yard's default".
	BurnedBy []string `json:"burned_by"`
	// Limits is the report; nil when nothing is known.
	Limits *limits.Report `json:"limits,omitempty"`
	// Window and ResetsAt are the window an agent burning the provider
	// waits for, when one is used up (limits.Report.UsedUp).
	Window   string     `json:"window,omitempty"`
	ResetsAt *time.Time `json:"resets_at,omitempty"`
	// Unknown is why nothing is known, in a line.
	Unknown string `json:"limits_unknown,omitempty"`
}

// SourceLimits is what one ask came to, for a source that answers for
// itself. It is what `yardr limits --json` prints per source under
// sources, field for field.
type SourceLimits struct {
	Source string `json:"source"`
	// Limits is the report; nil when nothing is known.
	Limits *limits.Report `json:"limits,omitempty"`
	// Window and ResetsAt are the window that is used up and resets last,
	// when one is (limits.Report.UsedUp).
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

// limitsEntry is what the view knows of one ask: the last answer and when
// it was asked for, and the run on its way, if one is.
type limitsEntry[T any] struct {
	asked  time.Time
	answer T
	// run is the run on its way; nil when none is.
	run *limitsRun[T]
}

// limitsRun is one run of the source's command. done is closed when the
// answer is in, so a read that arrived while it was on its way shares it
// instead of starting a second run.
type limitsRun[T any] struct {
	done   chan struct{}
	answer T
}

// limitUse is one place the yard burns quota: a harness kind with its
// provider setting, and who names it.
type limitUse struct {
	kind, provider, by string
}

// limitUsesInUse are the harness kinds the yard runs on with their provider
// settings: its default_kind and every kind a herdr group or crew member
// names, each with who names it. As `yardr limits` asks them.
func limitUsesInUse(ctx context.Context, st store.API) ([]limitUse, error) {
	groups, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	crew, err := st.Crew(ctx)
	if err != nil {
		return nil, err
	}
	all, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	return limitUses(all[store.DefaultKindSetting], groups, crew), nil
}

// limitUses are the harness kinds the yard runs on with their provider
// settings, one per user: the default_kind first, then the herdr crew
// members and groups in the store's order. A config naming no kind runs on
// nothing yardr can name, and is left out.
func limitUses(def string, groups []store.Group, crew []store.CrewMember) []limitUse {
	var out []limitUse
	if def != "" {
		out = append(out, limitUse{kind: def, by: "the yard's default"})
	}
	for _, r := range crew {
		if r.Runner != "herdr" {
			continue
		}
		kind, _ := r.Config["kind"].(string)
		if kind == "" {
			continue
		}
		provider, _ := r.Config["provider"].(string)
		out = append(out, limitUse{kind: kind, provider: provider, by: "crew member " + r.Name})
	}
	for _, g := range groups {
		if g.Runner != "herdr" {
			continue
		}
		kind, _ := g.Config["kind"].(string)
		if kind == "" {
			continue
		}
		provider, _ := g.Config["provider"].(string)
		out = append(out, limitUse{kind: kind, provider: provider, by: "group " + g.Name})
	}
	return out
}

// providerGroup is one provider the yard burns: the kinds burning it, who
// burns it, and the source it is asked through.
type providerGroup struct {
	name string
	// kinds are the harness kinds burning the provider, each once; by who
	// burns it, in the uses' order.
	kinds, by []string
	answerer  limits.Answerer
	answered  bool
}

// groupUses resolves each use to its provider and groups them: one group
// per distinct provider, answered or not, in first-seen order, asked
// through the first source by name of those resolving to it. Kinds that
// resolve to nothing are unmapped, each once, in first-seen order.
func groupUses(as []limits.Answerer, uses []limitUse) ([]providerGroup, []string) {
	var groups []providerGroup
	var unmapped []string
	at := map[string]int{}
	for _, u := range uses {
		a, name, ok := limits.Resolve(as, u.kind, u.provider)
		if !ok && u.provider == "" {
			if !slices.Contains(unmapped, u.kind) {
				unmapped = append(unmapped, u.kind)
			}
			continue
		}
		i, seen := at[name]
		if !seen {
			i = len(groups)
			at[name] = i
			groups = append(groups, providerGroup{name: name, answerer: a, answered: ok})
		}
		g := &groups[i]
		if !slices.Contains(g.kinds, u.kind) {
			g.kinds = append(g.kinds, u.kind)
		}
		g.by = append(g.by, u.by)
		if ok && (!g.answered || a.Source < g.answerer.Source) {
			g.answerer, g.answered = a, true
		}
	}
	return groups, unmapped
}

// limitsAsk answers from the last ask while it stands, else runs ask: one
// run per key at a time, shared by concurrent reads. fresh carries what is
// new every read onto the answer. A run that fails is an entry with no
// report, not an error. ctx ends the wait, not the run: the run is
// detached, so the next reader still gets its answer, and is bounded by
// limits.Timeout however it was asked.
func (s *Server) limitsAsk[T any](ctx context.Context, by map[string]*limitsEntry[T], key string, ask func(context.Context) T, fresh func(T) T) (T, error) {
	s.limitsMu.Lock()
	e := by[key]
	if e != nil && e.run == nil && time.Since(e.asked) < s.limitsEvery {
		answer := fresh(e.answer)
		s.limitsMu.Unlock()
		return answer, nil
	}
	if e != nil && e.run != nil {
		run := e.run
		s.limitsMu.Unlock()
		select {
		case <-run.done:
			return fresh(run.answer), nil
		case <-ctx.Done():
			var none T
			return none, ctx.Err()
		}
	}
	if e == nil {
		e = &limitsEntry[T]{}
		by[key] = e
	}
	run := &limitsRun[T]{done: make(chan struct{})}
	e.run = run
	e.asked = time.Now()
	s.limitsMu.Unlock()

	answer := ask(context.WithoutCancel(ctx))
	s.limitsMu.Lock()
	e.answer, e.run = answer, nil
	run.answer = answer
	s.limitsMu.Unlock()
	close(run.done)
	return fresh(answer), nil
}

// limitsProvider asks for the provider what `yardr limits` asks for it: one
// run of the operator's command, or the last answer while it still stands.
// Who burns the provider is fresh every read: only the ask stands a
// minute.
func (s *Server) limitsProvider(ctx context.Context, g providerGroup) (ProviderLimits, error) {
	return s.limitsAsk(ctx, s.limitsBy, g.name,
		func(ctx context.Context) ProviderLimits { return askProvider(ctx, g) },
		func(a ProviderLimits) ProviderLimits { a.Kinds, a.BurnedBy = g.kinds, g.by; return a })
}

// limitsSource asks for the source what `yardr limits` asks for it: one run
// of the operator's command, or the last answer while it still stands,
// shared by concurrent reads as a provider's is.
func (s *Server) limitsSource(ctx context.Context, a limits.Answerer) (SourceLimits, error) {
	return s.limitsAsk(ctx, s.sourceLimitsBy, a.Source,
		func(ctx context.Context) SourceLimits { return askSource(ctx, a) },
		func(s SourceLimits) SourceLimits { return s })
}

// handleLimits answers /limits: what `yardr limits` answers with no kind
// named, beside the head from before the read, as a page under the Config
// entry and as the same JSON field for field. A GET may run the
// operator's command: at most one run per provider and per source a minute,
// however often the route is read; reads in between share the last answer,
// and concurrent reads the one run on its way. The page reads through this
// handler, so its re-read costs no extra run. A provider that fails nulls
// its entry without failing the route; a provider no source answers for is
// not asked, and is not known with why. The answer is one shape, the
// command's, on loopback and with --unsafe alike.
func (s *Server) handleLimits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	head, err := s.st.Head(ctx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	out := Limits{Limits: []ProviderLimits{}, Sources: []SourceLimits{}, Unmapped: []string{}, Head: head}
	as, err := limits.Answerers(ctx, s.st, s.home)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	uses, err := limitUsesInUse(ctx, s.st)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	groups, unmapped := groupUses(as, uses)
	out.Unmapped = append(out.Unmapped, unmapped...)
	for _, g := range groups {
		if !g.answered {
			// As `yardr limits`: no source names the provider, so
			// nothing is known and nothing was asked.
			out.Limits = append(out.Limits, ProviderLimits{Provider: g.name, Kinds: g.kinds, BurnedBy: g.by,
				Unknown: (&limits.NoSource{Provider: g.name}).Error()})
			continue
		}
		p, err := s.limitsProvider(ctx, g)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out.Limits = append(out.Limits, p)
	}
	for _, a := range as {
		if !a.Limits.Self() {
			continue
		}
		src, err := s.limitsSource(ctx, a)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		out.Sources = append(out.Sources, src)
	}
	s.respond(w, r, Page{Template: "limits", Title: "Limits", Menu: "config", Data: out, head: head, stream: "seq"})
}

// askProvider asks for the provider once through its source, and says what
// it came to.
func askProvider(ctx context.Context, g providerGroup) ProviderLimits {
	out := ProviderLimits{Provider: g.name, Kinds: g.kinds, BurnedBy: g.by}
	r, err := g.answerer.AskName(ctx, g.name)
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

// askSource asks the source for itself once, and says what it came to.
func askSource(ctx context.Context, a limits.Answerer) SourceLimits {
	out := SourceLimits{Source: a.Source}
	r, err := a.AskSelf(ctx)
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
