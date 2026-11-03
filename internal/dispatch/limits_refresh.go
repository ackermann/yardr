package dispatch

import (
	"context"
	"slices"
	"time"

	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/store"
)

// The background refresher for the limits cache (harness_limits.go): the
// pass only ever reads the cache, and this scheduler is what runs the
// providers' commands, on its own goroutines, so a command that hangs
// holds nothing but its own key's refresh. It is modelled on the peers'
// pullers (peers.go): owned by Run, holding no pass state, waking the
// loop when an answer lands.
//
// A key is wanted under a lease, not a subscription: every cached read
// that finds its key missing or stale renews the want with the smallest
// age any reader asked for, and the refresher drops a key one age after
// its last read, running it at most once more if it came due in between.
// A provider may renew a stored login as it reads it (internal/limits),
// so a key nobody reads must not be asked for the life of the daemon.
// Runs are concurrent across keys and single-flight within one, so a
// key a deferred reset waits on is never queued behind another key's
// run.

// limitWant is one leased want: what a run of the key's command needs,
// when it was last read, and the smallest age its readers asked for.
type limitWant struct {
	key      string
	age      time.Duration
	lastRead time.Time
	// What one run asks: the answerer as the last read resolved it (its
	// Env already joined), the kind, the provider setting and the name
	// it resolved to, or, for a source's own limits, isSource and the
	// source's name.
	answerer limits.Answerer
	kind     string
	provider string
	name     string
	isSource bool
	// warm is a want Run's warm-up registered that no read has renewed:
	// it is dropped when its first answer lands, under the same lease.
	warm bool
	// running is a run of this key in flight.
	running bool
}

// registerLimitWant notes key as wanted at now, with the run the read
// resolved, and wakes the refresher, starting it where none runs. A
// read's want (warm false) renews the lease and takes the smallest age;
// a warm-up want for a key a read already wants changes nothing.
func (d *Dispatcher) registerLimitWant(w limitWant, warm bool) {
	now := d.now()
	d.limitsClock.Store(now.UnixNano())
	d.limitsMu.Lock()
	d.initLimitsLocked()
	if old, ok := d.limitsWants[w.key]; ok {
		if warm && !old.warm {
			d.limitsMu.Unlock()
			d.nudgeLimitsRefresher()
			return // a reader's want already stands
		}
		old.lastRead, old.warm = now, warm && old.warm
		if w.age < old.age {
			old.age = w.age
		}
		old.answerer, old.kind, old.provider, old.name, old.isSource = w.answerer, w.kind, w.provider, w.name, w.isSource
	} else {
		w.lastRead, w.warm = now, warm
		d.limitsWants[w.key] = &w
	}
	d.limitsMu.Unlock()
	d.ensureLimitsRefresher(context.Background(), false, nil)
	d.nudgeLimitsRefresher()
}

// stampLimitsClock records the dispatcher's clock for the refresher,
// from a goroutine that may read it (a pass, a read, the warm-up): the
// scheduler and its runs below read only the stamp.
func (d *Dispatcher) stampLimitsClock() {
	d.limitsClock.Store(d.now().UnixNano())
}

// limitsNow is the scheduler's clock: the last stamp, or the
// dispatcher's own where none has been made (a refresher started
// before any read stamped it, which no read path allows).
func (d *Dispatcher) limitsNow() time.Time {
	if n := d.limitsClock.Load(); n != 0 {
		return time.Unix(0, n).UTC()
	}
	return d.now()
}

// renewLimitWant renews key's lease at a fresh read: a key stays
// wanted while readers keep reading it, fresh answers included, and a
// warmed key a reader has since read is a reader's want, not a warm
// one. The smallest reader age and its current answerer govern the
// next run. Nothing is created where no want stands.
func (d *Dispatcher) renewLimitWant(w limitWant) {
	now := d.now()
	d.limitsClock.Store(now.UnixNano())
	d.limitsMu.Lock()
	if old, ok := d.limitsWants[w.key]; ok {
		if !old.warm && !now.Before(old.lastRead.Add(old.age)) {
			delete(d.limitsWants, w.key)
			d.limitsMu.Unlock()
			return // an expired lease is not revived by a fresh, longer-age read
		}
		old.lastRead, old.warm = now, false
		if w.age < old.age {
			old.age = w.age
		}
		old.answerer, old.kind, old.provider, old.name, old.isSource = w.answerer, w.kind, w.provider, w.name, w.isSource
	}
	d.limitsMu.Unlock()
}

// initLimitsLocked makes the cache's maps and its wake channel, where
// they are not yet. d.limitsMu is held.
func (d *Dispatcher) initLimitsLocked() {
	if d.limitsAsked == nil {
		d.limitsAsked = map[string]limitsAnswer{}
	}
	if d.limitsWants == nil {
		d.limitsWants = map[string]*limitWant{}
	}
	if d.limitsWake == nil {
		d.limitsWake = make(chan struct{}, 1)
	}
}

// nudgeLimitsRefresher wakes the scheduler, where one runs: a nudge
// already pending is enough, as the pump's is (store.go).
func (d *Dispatcher) nudgeLimitsRefresher() {
	d.limitsMu.Lock()
	ch := d.limitsWake
	d.limitsMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- struct{}{}:
	default:
	}
}

// ensureLimitsRefresher starts the scheduler where none runs. Run's
// start (byRun) keeps it for the daemon's life and gives it notify, the
// loop's poke, for each answer that lands; a scheduler started by a
// read alone stops once nothing is wanted, so a dispatcher that is
// only ever reconciled by hand leaks no goroutine.
func (d *Dispatcher) ensureLimitsRefresher(parent context.Context, byRun bool, notify func()) {
	d.limitsMu.Lock()
	d.initLimitsLocked()
	if byRun {
		d.limitsRunOwned = true
		if notify != nil {
			d.limitsNotify = notify
		}
	}
	if d.limitsLoop {
		d.limitsMu.Unlock()
		d.nudgeLimitsRefresher()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	d.limitsCancel = cancel
	d.limitsLoop = true
	d.limitsLoopGen++
	gen := d.limitsLoopGen
	d.limitsMu.Unlock()
	go d.limitsRefresherLoop(ctx, gen)
}

// startLimitsRefresher is Run's start of the scheduler, kept until Run
// ends (stopLimitsRefresher).
func (d *Dispatcher) startLimitsRefresher(ctx context.Context, poke func()) {
	d.stampLimitsClock()
	d.ensureLimitsRefresher(ctx, true, poke)
}

// stopLimitsRefresher stops the scheduler, as Run ends: a key still
// wanted loses nothing, since what is owed is the cache's staleness,
// read again by the next daemon's first read.
func (d *Dispatcher) stopLimitsRefresher() {
	d.limitsMu.Lock()
	cancel := d.limitsCancel
	d.limitsCancel, d.limitsRunOwned, d.limitsNotify = nil, false, nil
	d.limitsMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// limitsRefresherLoop is the scheduler: start every wanted key whose
// answer has gone stale at the age its readers asked for, then sleep
// until the next key comes due, a want wakes it, or a run lands.
func (d *Dispatcher) limitsRefresherLoop(ctx context.Context, gen int) {
	// The deferred clear is for the context's exit alone: the idle exit
	// clears under the decision's lock below.
	defer func() {
		d.limitsMu.Lock()
		if d.limitsLoopGen == gen {
			d.limitsLoop, d.limitsCancel = false, nil
		}
		d.limitsMu.Unlock()
	}()
	for {
		d.startDueLimitRefreshes(ctx)
		d.limitsMu.Lock()
		idle := !d.limitsRunOwned && len(d.limitsWants) == 0
		var hook func()
		if idle {
			hook = d.limitsIdleHook
			if d.limitsLoopGen == gen {
				// Leaving and clearing are one critical section: a want
				// registered before the decision is seen above, and one
				// after finds no loop and starts a new one, so none stands
				// with no loop to run it.
				d.limitsLoop, d.limitsCancel = false, nil
			}
		}
		d.limitsMu.Unlock()
		if hook != nil {
			hook() // in the old exit gap, where the test registers its want
		}
		if idle || ctx.Err() != nil {
			return
		}
		wait := d.nextLimitsWait()
		var timer *time.Timer
		var due <-chan time.Time
		if wait >= 0 {
			timer = time.NewTimer(wait)
			due = timer.C
		}
		d.limitsMu.Lock()
		ch := d.limitsWake
		d.limitsMu.Unlock()
		select {
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			return
		case <-ch:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// startDueLimitRefreshes starts a run for each wanted key that is due
// (never asked, or stale at its readers' age), each on its own
// goroutine, and drops a key whose lease has run out: one age past its
// last read it is wanted no longer, so a key that came due in between
// will already have had its one run at that due time, and none follows
// the drop. Warm-up wants are dropped by their run's landing instead
// (runLimitRefresh).
func (d *Dispatcher) startDueLimitRefreshes(ctx context.Context) {
	now := d.limitsNow()
	type launch struct {
		want      limitWant
		startedAt time.Time
	}
	var launches []launch
	d.limitsMu.Lock()
	d.initLimitsLocked()
	for key, w := range d.limitsWants {
		if w.running {
			continue
		}
		if expired := !now.Before(w.lastRead.Add(w.age)); expired && !w.warm {
			delete(d.limitsWants, key)
			continue
		}
		if ans, asked := d.limitsAsked[key]; !asked || !fresh(ans, now, w.age) {
			w.running = true
			launches = append(launches, launch{want: *w, startedAt: now})
		}
	}
	d.limitsMu.Unlock()
	for _, l := range launches {
		go d.runLimitRefresh(ctx, l.want, l.startedAt)
	}
}

// runLimitRefresh is one run of a wanted key's command, outside every
// lock, bounded as every limits ask is (limits.Timeout, in source.Run).
// Its answer, or its failure, is stored as the cache's memory of the
// key, asked at the run's start as the synchronous ask kept it. A failure
// keeps beside it the report before it (limitsAnswer.kept), so that a
// window the provider said was used up is not forgotten because the next
// ask failed.
func (d *Dispatcher) runLimitRefresh(ctx context.Context, w limitWant, startedAt time.Time) {
	var r *limits.Report
	var err error
	switch {
	case w.isSource:
		r, err = w.answerer.AskSelf(ctx)
	case w.provider != "":
		r, err = w.answerer.AskName(ctx, w.name)
	default:
		r, err = w.answerer.Ask(ctx, w.kind)
	}
	d.limitsMu.Lock()
	d.initLimitsLocked()
	ans := limitsAnswer{at: startedAt, report: r, err: err}
	if r == nil {
		ans.kept = d.limitsAsked[w.key].known()
	}
	d.limitsAsked[w.key] = ans
	if cur, ok := d.limitsWants[w.key]; ok {
		cur.running = false
		if cur.warm {
			delete(d.limitsWants, w.key) // a warm-up want no read renewed
		}
	}
	notify := d.limitsNotify
	d.limitsMu.Unlock()
	if err != nil && d.Log != nil {
		if w.isSource {
			d.Log.Warn("limits of a source not known", "source", w.answerer.Source, "err", err)
		} else {
			d.Log.Warn("limits of a harness kind not known", "kind", w.kind, "source", w.answerer.Source, "err", err)
		}
	}
	d.nudgeLimitsRefresher()
	if notify != nil {
		notify() // an answer landed: a deferred reset can be decided
	}
}

// nextLimitsWait is how long until the next wanted key comes due or its
// lease runs out, by the dispatcher's clock; -1 where no key waits (all
// fresh within their leases, or a run in flight, whose landing wakes
// the scheduler itself).
func (d *Dispatcher) nextLimitsWait() time.Duration {
	now := d.limitsNow()
	var next *time.Time
	consider := func(t time.Time) {
		if next == nil || t.Before(*next) {
			next = &t
		}
	}
	d.limitsMu.Lock()
	for _, w := range d.limitsWants {
		if w.running {
			continue
		}
		if ans, ok := d.limitsAsked[w.key]; ok {
			consider(limitsFreshUntil(ans, w.age))
		} else {
			d.limitsMu.Unlock()
			return 0 // due now; startDueLimitRefreshes missed nothing
		}
		consider(w.lastRead.Add(w.age))
	}
	d.limitsMu.Unlock()
	if next == nil {
		return -1
	}
	if wait := next.Sub(now); wait > 0 {
		return wait
	}
	return 0
}

// limitsFreshUntil is when m stops holding for a reader of the given
// age: the age, or sooner where the answer says how long it holds.
func limitsFreshUntil(m limitsAnswer, age time.Duration) time.Time {
	if m.report != nil && m.report.HoldsFor > 0 && m.report.HoldsFor < age {
		age = m.report.HoldsFor
	}
	return m.at.Add(age)
}

// warmLimits wants, concurrently through the refresher, the keys the
// yard is known at Run's start to need: the gated groups' providers
// under their groups' environments, the crew kinds' providers, and one
// key per distinct source a gated route's condition names for itself.
// A key that cannot be resolved (no source answers, an env_file that
// cannot be read) is skipped: there is nothing to refresh, and the
// first read says so at once, as any read does. A route written after
// startup is not warmed; its key takes the cold path until its first
// answer lands.
func (d *Dispatcher) warmLimits(ctx context.Context) {
	groups, err := d.Store.Groups(ctx)
	if err != nil {
		d.Log.Warn("limits warm-up: groups", "err", err)
		return
	}
	byName := make(map[string]store.Group, len(groups))
	for _, g := range groups {
		byName[g.Name] = g
	}
	settings, err := d.Store.Settings(ctx)
	if err != nil {
		d.Log.Warn("limits warm-up: settings", "err", err)
		return
	}
	def := settings[store.DefaultKindSetting]
	routes, err := d.Store.Routes(ctx)
	if err != nil {
		d.Log.Warn("limits warm-up: routes", "err", err)
		return
	}
	sources := map[string]bool{}
	for _, r := range routes {
		if !r.Gated(byName, def) {
			continue
		}
		for _, rg := range r.Groups() {
			if rg.When != "" {
				if parsed, err := store.ParseQuota(rg.When); err == nil {
					for _, l := range parsed {
						sources[l.Source] = sources[l.Source] || l.Source != ""
					}
				}
			}
			if rg.Name == store.RouteWait {
				continue
			}
			g, ok := byName[rg.Name]
			if !ok {
				continue
			}
			kind := g.Kind(def)
			if kind == "" {
				kind = agents.DefaultKind
			}
			provider, _ := g.Config["provider"].(string)
			env, err := groupEnv(g)
			if err != nil {
				continue // no key, as a read of the group finds
			}
			d.warmLimitWant(ctx, kind, provider, env, routeLimitsMemory)
		}
	}
	for source := range sources {
		if source != "" {
			d.warmSourceWant(ctx, source, routeLimitsMemory)
		}
	}
	crew, err := d.Store.Crew(ctx)
	if err != nil {
		d.Log.Warn("limits warm-up: crew", "err", err)
		return
	}
	for _, m := range crew {
		provider, _ := m.Config["provider"].(string)
		d.warmLimitWant(ctx, harnessKind(m.Config), provider, nil, limitsMemory)
	}
}

// warmLimitWant registers a warm-up want for a provider's key, where
// the provider resolves; nothing where none does.
func (d *Dispatcher) warmLimitWant(ctx context.Context, kind, provider string, env []string, age time.Duration) {
	as, err := limits.Answerers(ctx, d.Store, d.Home)
	if err != nil {
		return
	}
	a, name, ok := limits.Resolve(as, kind, provider)
	if !ok {
		return
	}
	a.Env = slices.Clone(env)
	d.registerLimitWant(limitWant{
		key: a.Source + "\x00" + name + "\x00" + limitsEnvScope(env),
		age: age, answerer: a, kind: kind, provider: provider, name: name,
	}, true)
}

// warmSourceWant registers a warm-up want for a source's own key,
// where the source answers for itself; nothing where none does.
func (d *Dispatcher) warmSourceWant(ctx context.Context, source string, age time.Duration) {
	as, err := limits.Answerers(ctx, d.Store, d.Home)
	if err != nil {
		return
	}
	a, ok := limits.SelfAnswerer(as, source)
	if !ok {
		return
	}
	d.registerLimitWant(limitWant{
		key: a.Source + "\x00", age: age, answerer: a, isSource: true,
	}, true)
}
