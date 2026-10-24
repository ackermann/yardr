package store

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A timer is a crew member's or a user's request to be told something later:
// once, or every so often, and not at all when what it waited for came
// first (Unless). It is a record, fired by the daemon's pass (FireTimers),
// so it outlives the session that set it. To a crew member a firing is an
// event of kind timer in its inbox, which the usual nudge wakes it for; to
// a user it is mail from whoever set it, marked as a timer, and wakes
// nobody.
//
// A timer is the fallback: what can be heard as an event (a subscription,
// a bead's done) is heard sooner and without a clock.

var bTimers = []byte("timers") // <id>

func init() { allBuckets = append(allBuckets, bTimers) }

// The settings a yard bounds its timers with, and what holds without
// them.
const (
	TimerLimitSetting    = "timer_limit"     // live timers one setter may have
	TimerMinEverySetting = "timer_min_every" // the shortest --every

	DefaultTimerLimit    = 5
	DefaultTimerMinEvery = time.Hour
)

// Bounds no setting moves.
const (
	// MaxTimerAhead is how far ahead a one-shot timer may be due, and the
	// longest a recurring one may run.
	MaxTimerAhead = 30 * 24 * time.Hour
	// DefaultTimerFor is how long a recurring timer runs when its setter
	// did not say.
	DefaultTimerFor = 7 * 24 * time.Hour
	// MaxTimerReason bounds the reason, which a firing carries whole.
	MaxTimerReason = 1 << 10
	// TimerLate is how long after it was due a firing is called late. The
	// daemon fires within a pass of the due time, so anything past this
	// means it was not running.
	TimerLate = time.Minute
)

// LastFiring is what the last firing of a recurring timer ends with.
const LastFiring = "This was the last; set it again if still wanted."

// Timer is one live timer. Fired or dropped for good, it is gone: the log
// keeps what became of it.
type Timer struct {
	ID string `json:"id"`
	// By set it; To is told. A crew member is only ever told by itself.
	By     string    `json:"by"`
	To     string    `json:"to"`
	ToUser bool      `json:"to_user,omitempty"`
	Reason string    `json:"reason"`
	SetAt  time.Time `json:"set_at"`
	// Due is when it fires next.
	Due time.Time `json:"due"`
	// Every is the period of a recurring timer ("1h", "1d"), "" for a
	// one-shot; Ends is when a recurring one stops.
	Every string     `json:"every,omitempty"`
	Ends  *time.Time `json:"ends,omitempty"`
	// Unless is what it waits for; nil fires regardless.
	Unless *TimerUnless `json:"unless,omitempty"`
	Fired  int          `json:"fired,omitempty"` // firings so far
}

// TimerUnless is what a timer is dropped for: the bead closed, or with a
// stage, the bead in that stage, past it, or closed.
type TimerUnless struct {
	Bead  string `json:"bead"`
	Stage string `json:"stage,omitempty"`
}

func (u TimerUnless) String() string {
	if u.Stage == "" {
		return "bead=" + u.Bead
	}
	return "bead=" + u.Bead + ",stage=" + u.Stage
}

// NewTimer is a timer as it is asked for.
type NewTimer struct {
	To     string `json:"to"` // "" is the setter
	Reason string `json:"reason"`
	// Due is when a one-shot fires. A recurring timer has Every instead,
	// first fires one period from now, and runs For (0: DefaultTimerFor).
	Due         time.Time     `json:"due"`
	Every       time.Duration `json:"every"`
	For         time.Duration `json:"for"`
	UnlessBead  string        `json:"unless_bead"`
	UnlessStage string        `json:"unless_stage"`
}

// TimerPass is what one FireTimers did.
type TimerPass struct {
	Fired   int `json:"fired"`
	Dropped int `json:"dropped"`
	// Next is when the earliest timer left is due, nil with none: when to
	// call FireTimers again at the latest.
	Next *time.Time `json:"next,omitempty"`
}

// ParseSpan is time.ParseDuration plus whole days ("1d", "30d").
func ParseSpan(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days < 1 {
			return 0, fmt.Errorf("bad duration %q (use e.g. 40m, 12h or 7d)", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad duration %q (use e.g. 40m, 12h or 7d)", s)
	}
	return d, nil
}

// Span prints a duration as ParseSpan reads it: 1d, 1h30m, 45s.
func Span(d time.Duration) string {
	if d >= 24*time.Hour && d%(24*time.Hour) == 0 {
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	}
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// timerLimits reads the yard's bounds; a setting that does not parse
// counts as unset.
func timerLimits(tx *bolt.Tx) (limit int, minEvery time.Duration) {
	limit, minEvery = DefaultTimerLimit, DefaultTimerMinEvery
	var v string
	if ok, _ := get(tx, bSettings, TimerLimitSetting, &v); ok {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	v = ""
	if ok, _ := get(tx, bSettings, TimerMinEverySetting, &v); ok {
		if d, err := ParseSpan(v); err == nil {
			minEvery = d
		}
	}
	return limit, minEvery
}

// AddTimer sets a timer as actor, who must be a crew member or a user. It is
// for the setter, or, set by a crew member, for a user; nobody sets one for
// another crew member. It refuses what the yard's limits do not allow, and a
// timer whose Unless already holds. From a session's or a crew member's
// pane the timer is the pane's actor's (paneAuthor), whatever actor says;
// from anywhere else, as given.
func (s *Store) AddTimer(ctx context.Context, nt NewTimer, actor string) (*Timer, error) {
	reason := strings.TrimSpace(nt.Reason)
	if reason == "" {
		return nil, errors.New("timer: a reason is required: it is what the receiver reads")
	}
	if len(reason) > MaxTimerReason {
		return nil, fmt.Errorf("timer: the reason is %d bytes, over the %d-byte limit", len(reason), MaxTimerReason)
	}
	if nt.UnlessStage != "" && nt.UnlessBead == "" {
		return nil, errors.New("timer: --unless stage=<stage> needs its bead: bead=<id>,stage=<stage>")
	}
	t := &Timer{Reason: reason}
	if nt.UnlessBead != "" {
		// Outside the write: a stage's place in the flow may need the
		// depot's repository read.
		st, err := s.unlessState(ctx, TimerUnless{Bead: nt.UnlessBead, Stage: nt.UnlessStage}, true)
		if err != nil {
			return nil, fmt.Errorf("timer: --unless: %w", err)
		}
		if st.gone {
			return nil, fmt.Errorf("timer: --unless: bead %s: %w", nt.UnlessBead, ErrNotFound)
		}
		if st.met {
			return nil, fmt.Errorf("timer: --unless: %s: there is nothing left to wait for", st.stands)
		}
		t.Unless = &TimerUnless{Bead: st.bead, Stage: nt.UnlessStage}
	}
	err := s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		t.By, t.To = actor, or(nt.To, actor)
		byCrew := w.Bucket(bCrew).Get([]byte(actor)) != nil
		byUser := w.Bucket(bUsers).Get([]byte(actor)) != nil
		if !byCrew && !byUser {
			return fmt.Errorf("timer: %s is neither a crew member nor a user of the yard, and only they set timers "+
				"(an agent working a bead says what it has to say on its bead)", actor)
		}
		toCrew := w.Bucket(bCrew).Get([]byte(t.To)) != nil
		t.ToUser = !toCrew && w.Bucket(bUsers).Get([]byte(t.To)) != nil
		switch {
		case t.To == actor:
		case toCrew:
			return fmt.Errorf("timer: nobody sets a timer for another crew member: mail it (yardr mail %s \"...\"), "+
				"and it sets its own if it wants one", t.To)
		case !t.ToUser:
			return fmt.Errorf("timer: --to %s: no user of that name (yardr user list): %w", t.To, ErrNotFound)
		case !byCrew:
			return fmt.Errorf("timer: a user sets timers for themselves; what %s should know, mail them (yardr mail %s \"...\")", t.To, t.To)
		}
		limit, minEvery := timerLimits(w.Tx)
		t.SetAt = now()
		switch {
		case nt.Every > 0:
			if !nt.Due.IsZero() {
				return errors.New("timer: a recurring timer first fires one period from now: give --every alone, without --in or --at")
			}
			if nt.Every < minEvery {
				return fmt.Errorf("timer: --every %s is shorter than the yard allows (%s %s)", Span(nt.Every), TimerMinEverySetting, Span(minEvery))
			}
			runs := nt.For
			if runs == 0 {
				runs = DefaultTimerFor
			}
			if runs < 0 || runs > MaxTimerAhead {
				return fmt.Errorf("timer: --for %s: a recurring timer runs at most %s", Span(nt.For), Span(MaxTimerAhead))
			}
			if nt.Every > runs {
				return fmt.Errorf("timer: --every %s would never fire within the %s it runs: give a longer --for (at most %s)",
					Span(nt.Every), Span(runs), Span(MaxTimerAhead))
			}
			ends := t.SetAt.Add(runs)
			t.Every, t.Ends, t.Due = Span(nt.Every), &ends, t.SetAt.Add(nt.Every)
		case nt.For != 0:
			return errors.New("timer: --for is how long a recurring timer runs: it needs --every")
		case nt.Due.IsZero():
			return errors.New("timer: say when: --in <duration>, --at <time> or --every <duration>")
		case !nt.Due.After(t.SetAt):
			return fmt.Errorf("timer: %s is not in the future", nt.Due.Local().Format(timerTime))
		case nt.Due.After(t.SetAt.Add(MaxTimerAhead)):
			return fmt.Errorf("timer: %s is more than %s ahead, the furthest a timer may be set",
				nt.Due.Local().Format(timerTime), Span(MaxTimerAhead))
		default:
			t.Due = nt.Due.UTC().Truncate(time.Microsecond)
		}
		live := 0
		if err := each(w.Tx, bTimers, "", func(_ []byte, o *Timer) error {
			if o.By == actor {
				live++
			}
			return nil
		}); err != nil {
			return err
		}
		if live >= limit {
			return fmt.Errorf("timer: %s has %d live timers, the most the yard allows one setter (%s): "+
				"remove one first (yardr timer list, yardr timer rm <id>)", actor, live, TimerLimitSetting)
		}
		n, err := w.Bucket(bTimers).NextSequence()
		if err != nil {
			return err
		}
		t.ID = fmt.Sprintf("t%d", n)
		if err := put(w.Tx, bTimers, t.ID, t); err != nil {
			return err
		}
		data := map[string]any{"timer": t.ID, "to": t.To, "due": t.Due, "reason": t.Reason}
		if t.Every != "" {
			data["every"], data["ends"] = t.Every, *t.Ends
		}
		if t.Unless != nil {
			data["unless"] = t.Unless.String()
		}
		return w.event("", "timer_set", actor, data)
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// timerTime is how a firing and a refusal spell a moment, in the time zone
// of the process that owns the store.
const timerTime = "01-02 15:04"

// Timers lists the live timers name set or is told by, soonest first; ""
// lists every timer of the yard.
func (s *Store) Timers(ctx context.Context, name string) ([]Timer, error) {
	out := []Timer{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bTimers, "", func(_ []byte, t *Timer) error {
			if name == "" || t.By == name || t.To == name {
				out = append(out, *t)
			}
			return nil
		})
	})
	slices.SortStableFunc(out, func(a, b Timer) int { return a.Due.Compare(b.Due) })
	return out, err
}

// RemoveTimer removes the timer id as actor: whoever set it or is told by
// it, or the operator (operatorOnly), who removes anyone's. From a
// session's or a crew member's pane the actor is the pane's (paneAuthor)
// before the rule is read, so a pane can never pass as the setter, the
// one told, or the operator by --actor alone.
func (s *Store) RemoveTimer(ctx context.Context, id, actor string) (*Timer, error) {
	t := new(Timer)
	err := s.write(func(w *wtx) error {
		var err error
		t, err = w.removeTimer(ctx, id, actor, nil)
		return err
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

// timerRemoveDoes is what removing the timer id set by By for To is
// refused as: the operator's, unless By or To removes it.
func timerRemoveDoes(id string, t *Timer) string {
	return fmt.Sprintf("remove the timer %s, which %s set for %s", id, t.By, t.To)
}

// checkTimerRemove refuses removing the timer id as actor when it is
// neither theirs nor told to them: anyone's own is anyone's to remove,
// and anyone else's is the operator's.
func (w *wtx) checkTimerRemove(ctx context.Context, id, actor string) error {
	_, err := w.timerForRemoval(ctx, id, actor)
	return err
}

// timerForRemoval is the permission and existence check RemoveTimer uses;
// a run ask uses the same check to decide whether the operator must approve.
func (w *wtx) timerForRemoval(ctx context.Context, id, actor string) (*Timer, error) {
	resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
	if err != nil {
		return nil, err
	}
	actor = resolved
	t := new(Timer)
	if ok, err := get(w.Tx, bTimers, id, t); err != nil {
		return nil, err
	} else if !ok {
		return nil, fmt.Errorf("timer %s: %w", id, ErrNotFound)
	}
	if t.By != actor && t.To != actor {
		if err := w.may(ctx, actor, ActRemoveTimer, Object{Kind: KindTimer, Name: id, Does: timerRemoveDoes(id, t), Ask: Askable}); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// removeTimer is RemoveTimer inside the transaction, so that an approved
// run ask removes the timer in the write that records its approval: by is
// then the ask it was approved on, which the event carries.
func (w *wtx) removeTimer(ctx context.Context, id, actor string, by *approvedOn) (*Timer, error) {
	t, err := w.timerForRemoval(ctx, id, actor)
	if err != nil {
		return nil, err
	}
	if err := w.Bucket(bTimers).Delete([]byte(id)); err != nil {
		return nil, err
	}
	data := map[string]any{"timer": id, "by": t.By, "to": t.To, "reason": t.Reason}
	if by != nil {
		data["approved"] = by
	}
	if err := w.event("", "timer_removed", actor, data); err != nil {
		return nil, err
	}
	return t, nil
}

// unlessState is how a timer's awaited bead stands.
type unlessState struct {
	bead   string // its handle
	met    bool   // what the timer waited for has happened
	gone   bool   // the bead is not in the yard
	stands string // "yardr-x is still in review", for the one told
}

// unlessState reads how u's bead stands. With a stage, the bead has
// entered it when it is there, was there (its path), is in a stage the
// flow orders after it, or is closed. checkStage refuses a stage the
// bead's flow does not have.
func (s *Store) unlessState(ctx context.Context, u TimerUnless, checkStage bool) (unlessState, error) {
	b, err := s.GetBead(ctx, u.Bead)
	if errors.Is(err, ErrNotFound) {
		return unlessState{bead: u.Bead, gone: true, stands: u.Bead + " is no longer in the yard"}, nil
	} else if err != nil {
		return unlessState{}, err
	}
	st := unlessState{bead: b.ID}
	if b.Status == "closed" {
		st.met, st.stands = true, b.ID+" is closed"
		return st, nil
	}
	st.stands = fmt.Sprintf("%s is still in %s", b.ID, b.Stage)
	if b.Hold {
		st.stands += ", held"
	}
	if u.Stage == "" {
		return st, nil
	}
	entered := b.Stage == u.Stage
	// A flow that cannot be read (its depot's repository is away) leaves the
	// bead's own stage to go by: the timer fires rather than be lost.
	if m, err := s.FlowMap(ctx, b.ID); err == nil && m != nil {
		at, want := -1, -1
		for i, fs := range m.Stages {
			if fs.Name == u.Stage {
				want = i
			}
			if fs.Name == b.Stage {
				at = i
			}
		}
		if want < 0 && checkStage {
			names := make([]string, len(m.Stages))
			for i, fs := range m.Stages {
				names[i] = fs.Name
			}
			return st, fmt.Errorf("stage %q is not in %s's flow %s (%s)", u.Stage, b.ID, m.Name, strings.Join(names, ", "))
		}
		entered = entered || (want >= 0 && at > want) ||
			slices.ContainsFunc(m.Path, func(p PathStep) bool { return p.Stage == u.Stage })
	}
	if entered {
		st.met = true
		if b.Stage == u.Stage {
			st.stands = fmt.Sprintf("%s is in %s", b.ID, b.Stage)
		} else {
			st.stands = fmt.Sprintf("%s has been in %s and is in %s", b.ID, u.Stage, b.Stage)
		}
	}
	return st, nil
}

// timerStep is what FireTimers decided for one timer, applied in its
// write if the timer is still as it was read.
type timerStep struct {
	t      Timer
	drop   string // why it is dropped; "" fires it
	stands string // how its awaited bead stands
}

// FireTimers fires every timer due at at and drops those whose Unless
// holds or whose receiver is gone; the daemon calls it on each pass with
// its clock. A timer that was due while nothing called fires once, marked
// late, and a recurring one then waits for its next period after at: time
// missed is one firing, not one per period.
//
// Unless is read on every call, not only when the timer is due, so a
// dropped timer leaves the setter's limit, and the log says so, when what
// it waited for happens.
func (s *Store) FireTimers(ctx context.Context, at time.Time) (*TimerPass, error) {
	all, err := s.Timers(ctx, "")
	if err != nil {
		return nil, err
	}
	var steps []timerStep
	for _, t := range all {
		step := timerStep{t: t}
		if t.Unless != nil {
			// A bead that cannot be read holds up no other timer: this
			// one fires when due, saying nothing of the bead.
			st, err := s.unlessState(ctx, *t.Unless, false)
			if err != nil {
				st = unlessState{}
			}
			step.stands = st.stands
			if st.met {
				step.drop = st.stands
			}
		}
		if step.drop != "" || !t.Due.After(at) {
			steps = append(steps, step)
		}
	}
	pass := &TimerPass{}
	if len(steps) > 0 {
		err := s.write(func(w *wtx) error {
			pass.Fired, pass.Dropped = 0, 0 // Batch may run this again
			for _, step := range steps {
				var t Timer
				if ok, err := get(w.Tx, bTimers, step.t.ID, &t); err != nil {
					return err
				} else if !ok || !t.Due.Equal(step.t.Due) {
					continue // removed, or fired by another call, meanwhile
				}
				if step.drop == "" {
					bucket := bCrew
					if t.ToUser {
						bucket = bUsers
					}
					if w.Bucket(bucket).Get([]byte(t.To)) == nil {
						step.drop = t.To + " is no longer in the yard"
					}
				}
				if step.drop != "" {
					if err := w.dropTimer(t, step.drop); err != nil {
						return err
					}
					pass.Dropped++
					continue
				}
				if err := w.fireTimer(t, step.stands, at); err != nil {
					return err
				}
				pass.Fired++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if all, err = s.Timers(ctx, ""); err != nil {
			return nil, err
		}
	}
	if len(all) > 0 {
		pass.Next = &all[0].Due
	}
	return pass, nil
}

func (w *wtx) dropTimer(t Timer, why string) error {
	if err := w.Bucket(bTimers).Delete([]byte(t.ID)); err != nil {
		return err
	}
	return w.event("", "timer_dropped", "yard", map[string]any{"timer": t.ID, "by": t.By, "to": t.To, "reason": t.Reason, "why": why})
}

// fireTimer tells t's receiver and keeps a recurring timer for its next
// period, unless this was its last.
func (w *wtx) fireTimer(t Timer, stands string, at time.Time) error {
	late := time.Duration(0)
	if d := at.Sub(t.Due); d >= TimerLate {
		late = d.Round(time.Second)
	}
	last := false
	if t.Every != "" {
		every, err := ParseSpan(t.Every)
		if err != nil {
			return fmt.Errorf("timer %s: %w", t.ID, err)
		}
		// The first period after at: the ones missed are this firing.
		next := t.Due.Add(every)
		if !next.After(at) {
			next = next.Add(at.Sub(next)/every*every + every)
		}
		last = t.Ends != nil && next.After(*t.Ends)
		t.Due = next
	}
	t.Fired++
	text := timerText(t, stands, late, last)
	data := map[string]any{"timer": t.ID, "by": t.By, "to": t.To, "reason": t.Reason, "text": text, "set_at": t.SetAt}
	if late > 0 {
		data["late"] = Span(late)
	}
	if last {
		data["last"] = true
	}
	if t.Unless != nil {
		data["unless"] = t.Unless.String()
	}
	if t.ToUser {
		// The mail is what the user reads; nothing nudges a person, and the
		// timer event below is delivered to no one.
		data["to_user"] = true
		if err := w.event("", "mail", t.By, map[string]any{"to": t.To, "text": text, "to_user": true, "timer": t.ID}); err != nil {
			return err
		}
	} else {
		data["deliveries"] = map[string]any{t.To: ReasonTimer}
	}
	if err := w.event("", "timer", "yard", data); err != nil {
		return err
	}
	if t.Every == "" || last {
		return w.Bucket(bTimers).Delete([]byte(t.ID))
	}
	return put(w.Tx, bTimers, t.ID, t)
}

// ReasonTimer is the delivery reason of a fired timer in its crew member's
// inbox.
const ReasonTimer = "timer"

// timerText is what a firing says: the reason, who set it and when, how
// the awaited bead stands, that it is late and that it was the last.
func timerText(t Timer, stands string, late time.Duration, last bool) string {
	set := "set " + t.SetAt.Local().Format(timerTime) + " by " + t.By
	if t.Every != "" {
		set += ", every " + t.Every
	}
	text := fmt.Sprintf("timer: %s (%s).", t.Reason, set)
	if t.Unless != nil && stands != "" {
		waited := "to close"
		if t.Unless.Stage != "" {
			waited = "to reach " + t.Unless.Stage
		}
		text += fmt.Sprintf(" %s; this timer waited for it %s.", stands, waited)
	}
	if late > 0 {
		text += fmt.Sprintf(" Late by %s: the yard's daemon was not running when it was due.", Span(late))
	}
	if last {
		text += " " + LastFiring
	}
	return text
}

// timerOf is the id of the timer a mail came from, "" for mail someone
// wrote.
func timerOf(e Event) string {
	id, _ := e.Data["timer"].(string)
	if e.Kind != "mail" {
		return ""
	}
	return id
}
