package store

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/agents"
)

// Group is whoever works a stage. Runner names the implementation (herdr,
// exec, manual, peer); Config is runner-specific: the store knows which
// keys there are and what each is stored as (ConfigKeys), and reads a peer
// group's itself to send the bead (peersend.go).
type Group struct {
	Name   string         `json:"name"`
	Runner string         `json:"runner"`
	Config map[string]any `json:"config"`
	Limit  int            `json:"limit"`
	// Members are the actors who may move a bead out of a stage this group
	// works (see Advance). Only manual groups have them; none means anyone.
	// A guardrail against mistakes, not security: anyone can set YARDR_ACTOR.
	Members []string `json:"members,omitempty"`
	// Subscriptions deliver events to each member that is a crew member. Kept
	// when the group is redefined.
	Subscriptions []Subscription `json:"subscriptions,omitempty"`
}

// Kind is the kind the agents of g start as: the one its config names, and
// for a herdr group that names none the yard's default_kind (def, "" where
// the setting is not set), then the runner's own default. A group whose
// runner is not herdr has none. Claim gives a session its kind by it, and
// whoever asks what a group burns (a route's condition, route add's
// warning, doctor's note) asks it too, so that they speak of the provider
// the session burns.
func (g Group) Kind(def string) string {
	if kind, _ := g.Config["kind"].(string); kind != "" || g.Runner != "herdr" {
		return kind
	}
	if def != "" {
		return def
	}
	return agents.DefaultKind
}

// PutGroup adds or replaces a group, and logs group_set: whoever follows
// the yard by its events sees a group appear or change as it sees a depot,
// a flow or a crew member do.
func (s *Store) PutGroup(ctx context.Context, g Group, actor string) error {
	return s.write(func(w *wtx) error {
		// Batch reruns this closure, so it writes a copy.
		g := g
		return putGroup(w, &g, actor)
	})
}

// putGroup is PutGroup in a transaction: g is stored as it is given but for
// its subscriptions, which are the stored group's. Its config is held to
// ConfigKeys here, whoever writes it (the CLI, the group form, a pack); a
// key the stored group has already, with the same value, is kept unasked.
func putGroup(w *wtx, g *Group, actor string) error {
	if g.Config == nil {
		g.Config = map[string]any{}
	}
	if g.Limit < 1 {
		g.Limit = 1
	}
	g.Members = dedupeOrdered(g.Members)
	if len(g.Members) > 0 && g.Runner != "manual" {
		return fmt.Errorf("group %s: members are only for manual groups (this one is %s)", g.Name, g.Runner)
	}
	var old Group
	replaced, err := get(w.Tx, bGroups, g.Name, &old)
	if err != nil {
		return err
	}
	if g.Name == RouteWait && !replaced {
		return fmt.Errorf("group wait: wait ends a route (--else wait), so no group takes the name")
	}
	g.Subscriptions = old.Subscriptions
	kept := old.Config
	if old.Runner != g.Runner {
		kept = nil
	}
	if ps := configProblems(GroupConfig, g.Name, g.Runner, g.Config, kept); len(ps) > 0 {
		return ps[0]
	}
	if len(g.Subscriptions) > 0 && g.Runner != "manual" {
		return fmt.Errorf("group %s has subscriptions, which only manual groups may: unsubscribe it first", g.Name)
	}
	if err := checkPeerGroup(w.Tx, g); err != nil {
		return err
	}
	if g.Runner != "herdr" {
		if err := checkQuotaRoutes(w.Tx, g); err != nil {
			return err
		}
	}
	if err := put(w.Tx, bGroups, g.Name, *g); err != nil {
		return err
	}
	data := map[string]any{"name": g.Name, "runner": g.Runner, "limit": g.Limit}
	if len(g.Members) > 0 {
		data["members"] = g.Members
	}
	return w.event("", "group_set", actor, data)
}

// GroupEdit is what SetGroup changes of a group: the config keys of Set
// written, those of Unset removed, and Limit and Members where they are
// given. What it does not name is kept.
type GroupEdit struct {
	Set     map[string]any `json:"set,omitempty"`
	Unset   []string       `json:"unset,omitempty"`
	Limit   *int           `json:"limit,omitempty"`
	Members *[]string      `json:"members,omitempty"`
}

// GroupChange is a group as SetGroup found it and as it left it.
type GroupChange struct {
	Was Group `json:"was"`
	Is  Group `json:"is"`
}

// SetGroup changes what e names of the group name and keeps the rest, in
// one transaction: PutGroup writes the whole group, so a caller that read
// it, changed a key and put it back would undo what was written in
// between. The result is held to what PutGroup holds a group to. A group
// that is so already is not written and logs nothing; one that changes
// logs group_set, as PutGroup does.
func (s *Store) SetGroup(ctx context.Context, name string, e GroupEdit, actor string) (*GroupChange, error) {
	if e.Limit != nil && *e.Limit < 1 {
		return nil, fmt.Errorf("group %s: a limit is at least 1", name)
	}
	var ch *GroupChange
	err := s.write(func(w *wtx) error {
		ch = new(GroupChange)
		if ok, err := get(w.Tx, bGroups, name, &ch.Was); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("group %s: %w", name, ErrNotFound)
		}
		// Read again, not copied: the two share no map or slice.
		if _, err := get(w.Tx, bGroups, name, &ch.Is); err != nil {
			return err
		}
		g := &ch.Is
		var err error
		if g.Config, err = MergeConfig(name, g.Config, e.Set, e.Unset); err != nil {
			return err
		}
		if e.Limit != nil {
			g.Limit = *e.Limit
		}
		if e.Members != nil {
			g.Members = dedupeOrdered(*e.Members)
		}
		// Compared as stored: a list is the same list whatever type it was
		// given as.
		was, err := json.Marshal(ch.Was)
		if err != nil {
			return err
		}
		if is, err := json.Marshal(g); err != nil {
			return err
		} else if bytes.Equal(was, is) {
			ch.Is = ch.Was
			return nil
		}
		return putGroup(w, g, actor)
	})
	if err != nil {
		return nil, err
	}
	return ch, nil
}

// MergeConfig is a runner config with the keys of sets written over it and
// those of unset removed; config itself is left as it was. A key to unset
// that is not there is refused, naming whose config it is: a misspelt key
// would otherwise read as removed.
func MergeConfig(whose string, config, sets map[string]any, unset []string) (map[string]any, error) {
	out := make(map[string]any, len(config)+len(sets))
	for k, v := range config {
		out[k] = v
	}
	for _, k := range unset {
		if _, is := out[k]; !is {
			has := strings.Join(slices.Sorted(maps.Keys(config)), ", ")
			if has == "" {
				has = "none"
			}
			return nil, fmt.Errorf("--unset %s: %s has no such setting (it has: %s)", k, whose, has)
		}
		delete(out, k)
	}
	for k, v := range sets {
		out[k] = v
	}
	return out, nil
}

func (s *Store) Groups(ctx context.Context) ([]Group, error) {
	out := []Group{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bGroups, "", func(_ []byte, g *Group) error {
			out = append(out, *g)
			return nil
		})
	})
	return out, err
}

func (s *Store) GetGroup(ctx context.Context, name string) (*Group, error) {
	g := new(Group)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bGroups, name, g)
		if err == nil && !ok {
			err = fmt.Errorf("group %s: %w", name, ErrNotFound)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return g, nil
}

// DeleteGroup removes the group and the routes that send work to it, those
// that name it after another group too: what is left of such a route would
// say something its operator did not write. Its
// live sessions end as released and their beads go back to routing in the
// same transaction: a bead owned by a deleted group would otherwise sit in
// its stage until released by hand, since no route can claim it. The
// dispatcher's teardown stops the runner resources. It reports how many
// beads it released. It logs one group_removed, which names the routes that
// went with the group, as depot_removed does; a group that was not there
// logs nothing.
func (s *Store) DeleteGroup(ctx context.Context, name, actor string) (int, error) {
	released := 0
	err := s.write(func(w *wtx) error {
		// Batch reruns this closure when another call in the batch fails, so
		// the count starts over with each attempt.
		released = 0
		if w.Bucket(bGroups).Get([]byte(name)) == nil {
			return nil
		}
		drop := []string{}
		if err := each(w.Tx, bRoutes, "", func(k []byte, r *Route) error {
			if r.Names(name) {
				drop = append(drop, string(k))
			}
			return nil
		}); err != nil {
			return err
		}
		for _, k := range drop {
			if err := w.Bucket(bRoutes).Delete([]byte(k)); err != nil {
				return err
			}
		}
		live, err := s.liveSessions(w.Tx, name)
		if err != nil {
			return err
		}
		reason := "group " + name + " deleted"
		for _, sess := range live {
			owned := false
			if _, err := w.changeBead(sess.Bead, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
				if b.Session == nil || *b.Session != sess.ID {
					return "", nil, nil
				}
				owned = true
				return "released", map[string]any{"reason": reason}, w.endSession(b, "released")
			}); err != nil {
				return err
			}
			if owned {
				released++
				continue
			}
			// The bead moved on without this session; end it so it is not live.
			t := now()
			sess.State, sess.Error, sess.EndedAt = "released", &reason, &t
			if err := putSession(w.Tx, &sess); err != nil {
				return err
			}
		}
		if err := w.Bucket(bGroups).Delete([]byte(name)); err != nil {
			return err
		}
		return w.event("", "group_removed", actor, map[string]any{
			"name": name, "routes": drop, "released": released})
	})
	return released, err
}

// Route sends open, unblocked beads in Stage to Group. Nil fields match any.
//
// A route may name further groups after the first (Else), and each group it
// names may carry a condition on the windows of its provider's usage limits
// or of a source's own (When, RouteGroup.When); wait ends it. Group, When
// and Else are how the record is stored, and a route from before them is one
// group without a condition; they are read through Groups and Choose alone,
// so that the dispatcher and every view give one answer about a route.
//
// Label and AndLabels are stored the same way, a route from before the
// second being one with a label or none; they are read through Labels.
type Route struct {
	ID    string  `json:"id"`
	Stage string  `json:"stage"`
	Type  *string `json:"type,omitempty"`
	Depot *string `json:"depot,omitempty"`
	Label *string `json:"label,omitempty"`
	// AndLabels are the labels a bead must have besides Label.
	AndLabels []string `json:"and_labels,omitempty"`
	// BeadPriority matches beads of this priority or more urgent (a lower
	// number): 1 is P0 and P1.
	BeadPriority *int   `json:"bead_priority,omitempty"`
	Group        string `json:"group"`
	Priority     int    `json:"priority"`
	// When is Group's condition, as RouteGroup.When is a further group's.
	When string       `json:"when,omitempty"`
	Else []RouteGroup `json:"else,omitempty"`
}

// RouteWait ends a route: the bead waits, as when no group's condition
// holds. It is not a group: no group is named it, it takes no condition,
// and nothing is tried after it.
const RouteWait = "wait"

// RouteGroup is one group a route names and the condition it takes the
// route's beads under.
type RouteGroup struct {
	Name string `json:"name"`
	// When is the condition as it was typed after `@`: "Weekly<80,5h<90".
	// "" always holds.
	When string `json:"when,omitempty"`
}

// String is the group as `yardr route add` takes it: name, or name@condition.
func (g RouteGroup) String() string {
	if g.When == "" {
		return g.Name
	}
	return g.Name + "@" + g.When
}

// ParseRouteGroup reads a group as `yardr route add` takes it: its name,
// and after `@` the condition, which AddRoute checks.
func ParseRouteGroup(s string) RouteGroup {
	name, when, _ := strings.Cut(s, "@")
	return RouteGroup{Name: strings.TrimSpace(name), When: strings.TrimSpace(when)}
}

// Labels are the labels a bead must have, every one of them, for the route
// to match it; none, and the route asks for no label.
func (r Route) Labels() []string {
	if r.Label == nil {
		return slices.Clone(r.AndLabels)
	}
	return append([]string{*r.Label}, r.AndLabels...)
}

// LabelList is the route's labels on one line: "difficulty:3, size:large".
func (r Route) LabelList() string { return strings.Join(r.Labels(), ", ") }

// BeadPriorities is the bead priorities the route matches, as a bead's
// priority is written: "P0-P1", "P0" for the most urgent alone, "" for a
// route that matches any.
func (r Route) BeadPriorities() string {
	switch {
	case r.BeadPriority == nil:
		return ""
	case *r.BeadPriority <= 0:
		return "P0"
	}
	return "P0-P" + strconv.Itoa(*r.BeadPriority)
}

// ByBead reports whether the route asks something of the single bead, a
// label or a priority, beyond where it is (stage, depot) and what it is
// (type): the routes a pack leaves alone, since it says nothing of them.
func (r Route) ByBead() bool { return len(r.Labels()) > 0 || r.BeadPriority != nil }

// Groups are the groups the route names, in the order they are tried, each
// with its condition. There is always one.
func (r Route) Groups() []RouteGroup {
	return append([]RouteGroup{{Name: r.Group, When: r.When}}, r.Else...)
}

// Names reports whether group is one of the route's.
func (r Route) Names(group string) bool {
	return slices.ContainsFunc(r.Groups(), func(g RouteGroup) bool { return g.Name == group })
}

// Worker is the group that says who works the route's stage (people,
// agents, a script, another yard) and who of a manual group must act, for
// a bead no group has claimed. It is the first of Groups: a route with
// several names only groups that start agents (AddRoute), so each of them
// gives the answer the first does, whatever the quota.
func (r Route) Worker() string { return r.Groups()[0].Name }

// Further are the names of the groups after the first, in order. The wait
// marker is none: it ends the route, and nothing after it is tried.
func (r Route) Further() []string {
	var out []string
	for _, g := range r.Groups()[1:] {
		if g.Name == RouteWait {
			continue
		}
		out = append(out, g.Name)
	}
	return out
}

// Quotaed reports whether the route names a further group or carries a
// condition: what only groups that start agents may have.
func (r Route) Quotaed() bool {
	gs := r.Groups()
	return len(gs) > 1 || gs[0].When != ""
}

// GroupList is the route's groups in order with their conditions, on one
// line: "a@Weekly<80 else b".
func (r Route) GroupList() string {
	var parts []string
	for _, g := range r.Groups() {
		parts = append(parts, g.String())
	}
	return strings.Join(parts, " else ")
}

// QuotaLimit is one comparison of a condition: the window the provider
// labels Label is less than Under percent used, or, where Target is set,
// less than the window's target (the share of it that has passed, as the
// source reports it) plus Margin. Label "" is every window the provider
// reports. Source names a source of the yard, and the window is one of the
// windows it answers for itself; "" is the group's provider.
type QuotaLimit struct {
	Source string
	Label  string
	Under  float64
	Target bool
	Margin float64
}

// ParseQuota reads a condition, comparisons separated by commas:
// "Weekly<80,5h<90", or "<80" for every window. A comparison may name a
// source for the window: "machine.load<80", or "machine.<80" for every
// window the source reports. In place of a number the right side may be the
// window's target with an optional margin: "Weekly<target",
// "Weekly<target+10". The label is the limits source's own text for the
// window, so the yard knows no provider's windows; the number and the
// margin are the operator's.
func ParseQuota(when string) ([]QuotaLimit, error) {
	var out []QuotaLimit
	for _, part := range strings.Split(when, ",") {
		label, num, ok := strings.Cut(part, "<")
		if !ok {
			return nil, quotaError(when, part)
		}
		l := QuotaLimit{}
		label = strings.TrimSpace(label)
		if head, window, dotted := strings.Cut(label, "."); dotted {
			l.Source, l.Label = strings.TrimSpace(head), strings.TrimSpace(window)
			if l.Source == "" {
				return nil, quotaError(when, part)
			}
		} else {
			l.Label = label
		}
		if under, err := strconv.ParseFloat(strings.TrimSpace(num), 64); err == nil && under > 0 && under <= 100 {
			l.Under = under
			out = append(out, l)
			continue
		}
		margin, is := parseMargin(strings.TrimSpace(num))
		if !is {
			return nil, quotaError(when, part)
		}
		l.Target, l.Margin = true, margin
		out = append(out, l)
	}
	return out, nil
}

// quotaError is why a comparison is none: it names the window's two forms
// and both right sides, the number and the target.
func quotaError(when, part string) error {
	return fmt.Errorf("condition %q: %q is not <window><<percent used>, as in Weekly<80 (or machine.load<80 for a window of a source, <80 for every window), with a percentage above 0 and at most 100, nor <window><target> with an optional margin, as in Weekly<target+10", when, part)
}

// parseMargin reads the right side of a comparison with the target: the
// word target and an optional margin, a number of points above (+) or
// below (-) it. The word is lowercase, as the examples write it; the
// margin is any number from 0.
func parseMargin(right string) (float64, bool) {
	rest, ok := strings.CutPrefix(right, "target")
	if !ok {
		return 0, false
	}
	if rest == "" {
		return 0, true
	}
	sign := 1.0
	switch rest[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, false
	}
	// The operator above owns the sign. ParseFloat also accepts a leading
	// sign, but a second one (target++1 or target-+1) is not part of this
	// condition's grammar.
	number := rest[1:]
	if number == "" || number[0] == '+' || number[0] == '-' {
		return 0, false
	}
	margin, err := strconv.ParseFloat(number, 64)
	if err != nil || math.IsNaN(margin) || math.IsInf(margin, 0) || margin < 0 {
		return 0, false
	}
	return sign * margin, true
}

// Quota is what is known of the windows of a group's provider, or of a
// source's own, when a route chooses: what the yard's limits source said.
// The store asks nobody; the dispatcher hands it in (Route.Choose).
type Quota struct {
	// Provider names whose windows these are, for the sentence about one.
	Provider string
	Windows  []QuotaWindow
	// Unknown is why nothing is known (no source answers for the provider,
	// its command failed); Windows is then not read.
	Unknown string
}

// QuotaWindow is one window of a usage limit as a condition reads it.
type QuotaWindow struct {
	Label         string
	UsedPercent   *float64
	TargetPercent *float64
	ResetsAt      *time.Time
}

// RouteChoice is what a route chose from the windows it was handed.
type RouteChoice struct {
	// Group takes the bead: the first whose condition holds. "" when none
	// does, and the bead waits.
	Group string `json:"group,omitempty"`
	// Passed are the groups before it whose condition failed, in order.
	Passed []PassedGroup `json:"passed,omitempty"`
	// Unknown says, of Group, what of its condition could not be evaluated
	// and why: such a condition holds, as it would with no condition, so a
	// limits command that fails takes the gate away and stops nothing.
	Unknown string `json:"limits_unknown,omitempty"`
}

// PassedGroup is a group a route passed over, with why in words.
type PassedGroup struct {
	Group  string `json:"group"`
	Reason string `json:"reason"`
	// ResetsAt is the earliest reset among the windows that failed, where
	// the provider said one.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// Resets is the earliest reset among the windows that made a group be
// passed over; nil when the provider said none.
func (c RouteChoice) Resets() *time.Time {
	var out *time.Time
	for _, p := range c.Passed {
		if p.ResetsAt != nil && (out == nil || p.ResetsAt.Before(*out)) {
			out = p.ResetsAt
		}
	}
	return out
}

// Why is the groups passed over with their reasons, as one line.
func (c RouteChoice) Why() string {
	var parts []string
	for _, p := range c.Passed {
		parts = append(parts, fmt.Sprintf("%s passed over (%s)", p.Group, p.Reason))
	}
	return strings.Join(parts, "; ")
}

// Choose goes through the route's groups in order and takes the first
// whose condition holds. quota is asked for the windows of a group's
// provider, and of each source its condition names, only for a group that
// has a condition and only once the groups before it failed theirs: whoever
// calls decides whether and how they are learned, and a route with no
// condition asks for nothing. Whether the group chosen can start a bead now
// (its limit, its runtime) is not asked here: a group at its limit is
// waited for, not passed over.
func (r Route) Choose(quota func(group, source string) Quota) RouteChoice {
	var c RouteChoice
	for _, g := range r.Groups() {
		if g.Name == RouteWait {
			return c
		}
		if g.When == "" {
			c.Group = g.Name
			return c
		}
		// AddRoute stored only what parses; a record that does not is
		// treated as what cannot be evaluated.
		limits, err := ParseQuota(g.When)
		if err != nil {
			c.Group, c.Unknown = g.Name, err.Error()
			return c
		}
		// Each source a condition names is asked once, "" being the
		// group's provider.
		asked := map[string]Quota{}
		get := func(source string) Quota {
			q, ok := asked[source]
			if !ok {
				q = quota(g.Name, source)
				asked[source] = q
			}
			return q
		}
		failed, unknown, resets := judge(limits, get)
		if len(failed) == 0 {
			c.Group = g.Name
			if len(unknown) > 0 {
				c.Unknown = "the limits of " + g.Name + " were not known: " + strings.Join(unknown, "; ")
			}
			return c
		}
		c.Passed = append(c.Passed, PassedGroup{Group: g.Name, Reason: strings.Join(failed, "; "), ResetsAt: resets})
	}
	return c
}

// judge compares the windows with limits: the comparisons that failed, in
// words, those that could not be made, and the earliest reset among the
// windows that failed. Each comparison reads the quota of the source it
// names, "" being the group's provider; a source nothing is known of is
// said once, however many comparisons name it.
func judge(limits []QuotaLimit, quota func(source string) Quota) (failed, unknown []string, resets *time.Time) {
	said := map[string]bool{}
	for _, l := range limits {
		q := quota(l.Source)
		if q.Unknown != "" {
			if !said[l.Source] {
				said[l.Source] = true
				unknown = append(unknown, q.Unknown)
			}
			continue
		}
		whose := "its provider's"
		if q.Provider != "" {
			whose = q.Provider + "'s"
		}
		seen := false
		for _, w := range q.Windows {
			if l.Label != "" && w.Label != l.Label {
				continue
			}
			seen = true
			if w.UsedPercent == nil {
				unknown = append(unknown, fmt.Sprintf("%s report does not say how far the %s window is used", whose, w.Label))
				continue
			}
			under, want := l.Under, percent(l.Under)
			if l.Target {
				// No second rule: a window with no target reads as the
				// comparisons above that cannot be made.
				if w.TargetPercent == nil {
					unknown = append(unknown, fmt.Sprintf("%s report does not say the target of the %s window", whose, w.Label))
					continue
				}
				under = *w.TargetPercent + l.Margin
				want = "target " + percent(*w.TargetPercent) + "%" + marginText(l.Margin)
			}
			if *w.UsedPercent < under {
				continue
			}
			failed = append(failed, fmt.Sprintf("%s %s window is %s%% used, the route asks under %s",
				whose, w.Label, percent(*w.UsedPercent), want))
			if w.ResetsAt != nil && (resets == nil || w.ResetsAt.Before(*resets)) {
				resets = w.ResetsAt
			}
		}
		switch {
		case seen:
		case l.Label == "":
			unknown = append(unknown, whose+" report has no windows")
		default:
			unknown = append(unknown, fmt.Sprintf("%s report has no window labelled %s", whose, l.Label))
		}
	}
	return failed, unknown, resets
}

// percent prints a percentage with at most one decimal.
func percent(p float64) string {
	return strconv.FormatFloat(math.Round(p*10)/10, 'f', -1, 64)
}

// marginText is a target comparison's margin as a reason says it: " + 10",
// " - 5", nothing for none.
func marginText(margin float64) string {
	switch {
	case margin > 0:
		return " + " + percent(margin)
	case margin < 0:
		return " - " + percent(-margin)
	}
	return ""
}

// routeEvent is a route as route_added and route_removed carry it: its id
// and what it matches, the filters it does not set left out. `group` is
// the first group, as it always was; `groups` is all of them with their
// conditions, for a route that names more than a group. `label` and
// `labels` are the same for a route that asks for more than a label.
func routeEvent(r Route) map[string]any {
	data := map[string]any{"id": r.ID, "stage": r.Stage, "group": r.Worker(), "priority": r.Priority}
	if r.Quotaed() {
		data["groups"] = r.Groups()
	}
	labels := r.Labels()
	if len(labels) > 0 {
		data["label"] = labels[0]
	}
	if len(labels) > 1 {
		data["labels"] = labels
	}
	if r.BeadPriority != nil {
		data["bead_priority"] = *r.BeadPriority
	}
	for k, p := range map[string]*string{"type": r.Type, "depot": r.Depot} {
		if p != nil {
			data[k] = *p
		}
	}
	return data
}

// AddRoute adds a route and logs route_added. Every group it names must
// exist. One that names a further group or carries a condition may name
// only groups that start agents, or gate one exec group alone on the
// windows of sources: a quota means nothing for people or another yard, and
// with it holds that whichever group of such a route takes a bead, one
// runner works the stage and nobody is a member.
//
// A label given twice is stored once, and the first of them as Label,
// where a route with one label always had it.
func (s *Store) AddRoute(ctx context.Context, r Route, actor string) (*Route, error) {
	labels := dedupeOrdered(r.Labels())
	r.Label, r.AndLabels = nil, nil
	if len(labels) > 0 {
		r.Label, r.AndLabels = &labels[0], labels[1:]
	}
	if len(r.AndLabels) == 0 {
		r.AndLabels = nil
	}
	if r.BeadPriority != nil && *r.BeadPriority < 0 {
		return nil, fmt.Errorf("bead priority %d: a bead's priority is 0 or more, 0 the most urgent", *r.BeadPriority)
	}
	err := s.write(func(w *wtx) error {
		gs := r.Groups()
		runners := make(map[string]string, len(gs))
		for i, rg := range gs {
			if rg.Name == RouteWait {
				switch {
				case i == 0:
					return fmt.Errorf("wait ends a route (--else wait): it is not a group to send to")
				case rg.When != "":
					return fmt.Errorf("wait takes no condition: it ends the route, and the bead waits")
				case i != len(gs)-1:
					return fmt.Errorf("nothing is tried after wait: it ends the route")
				}
				continue
			}
			var g Group
			if ok, err := get(w.Tx, bGroups, rg.Name, &g); err != nil {
				return err
			} else if !ok {
				return fmt.Errorf("group %s: %w", rg.Name, ErrNotFound)
			}
			runners[rg.Name] = g.Runner
			if slices.ContainsFunc(gs[:i], func(o RouteGroup) bool { return o.Name == rg.Name }) {
				return fmt.Errorf("group %s is named twice", rg.Name)
			}
			if rg.When != "" {
				if _, err := ParseQuota(rg.When); err != nil {
					return fmt.Errorf("group %s: %w", rg.Name, err)
				}
			}
		}
		if err := quotaGroupsOK(r, runners); err != nil {
			return err
		}
		if r.Depot != nil && w.Bucket(bDepots).Get([]byte(*r.Depot)) == nil {
			return fmt.Errorf("depot %s: %w", *r.Depot, ErrNotFound)
		}
		r.ID = newEdgeID()
		if err := put(w.Tx, bRoutes, r.ID, r); err != nil {
			return err
		}
		return w.event("", "route_added", actor, routeEvent(r))
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// quotaGroupsOK is nil when the groups of r, a route with --else or a
// condition, may carry it: every group named starts agents, or the route
// gates one exec group alone on the windows of sources. runners names each
// group's runner; the wait marker is none. A group that starts no agents
// burns no provider, so a condition on one names a source alone; and with
// one group whichever takes a bead, one runner works the stage.
func quotaGroupsOK(r Route, runners map[string]string) error {
	if !r.Quotaed() {
		return nil
	}
	var real []RouteGroup
	for _, g := range r.Groups() {
		if g.Name != RouteWait {
			real = append(real, g)
		}
	}
	for _, g := range real {
		switch runners[g.Name] {
		case "herdr":
			continue
		case "exec":
		default:
			return fmt.Errorf("group %s is %s: --else and a quota condition are for groups that start agents; one exec group may be gated alone on a source, as refinery@machine.load<80", g.Name, runners[g.Name])
		}
		if len(real) > 1 {
			return fmt.Errorf("group %s is exec: a route with several groups names only groups that start agents; gate one exec group alone, as refinery@machine.load<80", g.Name)
		}
		if g.When == "" {
			continue
		}
		// AddRoute parsed this already; a record that does not is refused
		// as it reads.
		limits, err := ParseQuota(g.When)
		if err != nil {
			return fmt.Errorf("group %s: %w", g.Name, err)
		}
		for _, l := range limits {
			if l.Source == "" {
				what := "the window " + l.Label
				if l.Label == "" {
					what = "every window"
				}
				return fmt.Errorf("group %s starts no agents: its condition names %s of a provider, which only a group that starts agents burns; a condition on it names a source alone, as machine.load<80", g.Name, what)
			}
		}
	}
	return nil
}

// checkQuotaRoutes refuses saving g, a group that starts no agents, while a
// route that names a further group or carries a condition names it and would
// no longer carry it: AddRoute let that route in because the group started
// agents, or was the one exec group it gates on a source.
func checkQuotaRoutes(tx *bolt.Tx, g *Group) error {
	return each(tx, bRoutes, "", func(_ []byte, r *Route) error {
		if !r.Quotaed() || !r.Names(g.Name) {
			return nil
		}
		runners := map[string]string{}
		for _, rg := range r.Groups() {
			if rg.Name == RouteWait {
				continue
			}
			if rg.Name == g.Name {
				runners[rg.Name] = g.Runner
				continue
			}
			var o Group
			if ok, err := get(tx, bGroups, rg.Name, &o); err != nil {
				return err
			} else if ok {
				runners[rg.Name] = o.Runner
			}
		}
		if err := quotaGroupsOK(*r, runners); err != nil {
			return fmt.Errorf("group %s: route %s (%s, stage %s) names it with --else or a quota condition, and %s: it cannot become %s (yardr route rm %s first)",
				g.Name, r.ID, r.GroupList(), r.Stage, err, g.Runner, r.ID)
		}
		return nil
	})
}

// sortedRoutes are the yard's routes in the order MatchRoute reads them.
func sortedRoutes(tx *bolt.Tx) ([]Route, error) {
	var routes []Route
	if err := each(tx, bRoutes, "", func(_ []byte, r *Route) error {
		routes = append(routes, *r)
		return nil
	}); err != nil {
		return nil, err
	}
	SortRoutes(routes)
	return routes, nil
}

func (s *Store) Routes(ctx context.Context) ([]Route, error) {
	out := []Route{}
	err := s.read(func(tx *bolt.Tx) error {
		return each(tx, bRoutes, "", func(_ []byte, r *Route) error {
			out = append(out, *r)
			return nil
		})
	})
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Stage != out[j].Stage {
			return out[i].Stage < out[j].Stage
		}
		return out[i].Priority > out[j].Priority
	})
	return out, err
}

// SortRoutes orders routes so the first match wins: higher priority first,
// then the more specific route (more filters set), then by id for stability.
// Each label is a filter, so a route that asks for two sorts before a route
// that asks for one of them.
func SortRoutes(rs []Route) {
	spec := func(r Route) int {
		n := len(r.Labels())
		for _, p := range []*string{r.Type, r.Depot} {
			if p != nil {
				n++
			}
		}
		if r.BeadPriority != nil {
			n++
		}
		return n
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].Priority != rs[j].Priority {
			return rs[i].Priority > rs[j].Priority
		}
		if si, sj := spec(rs[i]), spec(rs[j]); si != sj {
			return si > sj
		}
		return rs[i].ID < rs[j].ID
	})
}

// MatchRoute returns the first route (in SortRoutes order) that applies to b.
// It reads b's stage, type, depot, labels and priority, so whoever asks for
// a bead it has only part of fills those in; a bead that is no bead of the
// yard's (a view's stand-in for "a bead of this depot") has no labels and
// DefaultPriority, as a bead made without either.
func MatchRoute(sorted []Route, b Bead) *Route {
	for i := range sorted {
		r := &sorted[i]
		if r.Stage != b.Stage {
			continue
		}
		if r.Type != nil && *r.Type != b.Type {
			continue
		}
		if r.Depot != nil && (b.Depot == nil || *r.Depot != *b.Depot) {
			continue
		}
		if slices.ContainsFunc(r.Labels(), func(l string) bool { return !slices.Contains(b.Labels, l) }) {
			continue
		}
		if r.BeadPriority != nil && b.Priority > *r.BeadPriority {
			continue
		}
		return r
	}
	return nil
}

// stageGroup is the group a bead answers to in its stage: the group working
// it, else the one the routes would send it to (a manual group at its limit
// leaves beads waiting unclaimed). Nil when neither exists. For a bead no
// group has claimed whose route names several, it is the route's Worker:
// which of them will take it is the dispatcher's to choose, and what is
// asked here (who must act) is the same for each.
func stageGroup(tx *bolt.Tx, b *beadRow) (*Group, error) {
	name := ""
	if b.Group != nil {
		name = *b.Group
	} else {
		routes, err := sortedRoutes(tx)
		if err != nil {
			return nil, err
		}
		if r := MatchRoute(routes, b.Bead); r != nil {
			name = r.Worker()
		}
	}
	if name == "" {
		return nil, nil
	}
	g := new(Group)
	if ok, err := get(tx, bGroups, name, g); err != nil || !ok {
		return nil, err
	}
	return g, nil
}

// checkMembers refuses moving b out of its stage when the stage's group g
// has members and whoever asks is none of them, naming who may. Who asks is
// told by the store's own records before the caller's word: a move from the
// pane of a bead's agent, or one that names such a session as its own
// (callerSession), is that agent's, which is a member of no group, and one
// from a crew member's pane is that crew member's, whatever actor the
// command names. A stage of people is what stands between another
// yard's text and this yard's agents (docs/peers.md, "The intake"), and a
// crew member that reads such a bead and is talked into moving it on
// cannot do it by naming someone who may. A call the store knows no pane
// of is told by its actor, as a manual group's session is: a person at
// work.
func (w *wtx) checkMembers(ctx context.Context, exp Expect, g *Group, b *beadRow, actor string) error {
	if g == nil || len(g.Members) == 0 {
		return nil
	}
	only := fmt.Sprintf("bead %s: only members of group %s may move it out of stage %s: %s", b.ID, g.Name, b.Stage, strings.Join(g.Members, ", "))
	sess, err := w.callerSession(ctx, exp, b)
	if err != nil {
		return err
	}
	if sess != nil && !sess.Manual() {
		return fmt.Errorf("%s (this is the session %s of a bead's agent, whatever actor the command names)", only, sess.ID)
	}
	r, err := paneCrew(w.Tx, callerOf(ctx))
	if err != nil {
		return err
	}
	switch {
	case r != nil && slices.Contains(g.Members, r.Name):
		return nil
	case r != nil && r.Name != actor:
		return fmt.Errorf("%s (this is the pane of the crew member %s, whatever actor the command names: %q)", only, r.Name, actor)
	case r == nil && slices.Contains(g.Members, actor):
		return nil
	}
	return fmt.Errorf("%s (you are %q)", only, actor)
}

// DeleteRoute removes a route and logs route_removed with what it matched.
// A route that was not there logs nothing.
func (s *Store) DeleteRoute(ctx context.Context, id, actor string) error {
	return s.write(func(w *wtx) error {
		var r Route
		if ok, err := get(w.Tx, bRoutes, id, &r); err != nil || !ok {
			return err
		}
		if err := w.Bucket(bRoutes).Delete([]byte(id)); err != nil {
			return err
		}
		return w.event("", "route_removed", actor, routeEvent(r))
	})
}

type Session struct {
	ID     string `json:"id"`
	Bead   string `json:"bead"` // stored as the UUID, returned as the handle
	Group  string `json:"group"`
	Stage  string `json:"stage"`
	Runner string `json:"runner,omitempty"` // the group's runner when it started
	// Kind and Model are the harness kind and model the session was started
	// with, resolved by the claim as the group has them then. They are
	// additive: a session from before they were recorded has neither, so
	// there is no schema change for them.
	Kind      string         `json:"kind,omitempty"`
	Model     string         `json:"model,omitempty"`
	State     string         `json:"state"` // starting running done released held died failed aborted
	Handle    map[string]any `json:"handle,omitempty"`
	Error     *string        `json:"error,omitempty"`
	Stopped   bool           `json:"stopped"`
	Cleaned   bool           `json:"cleaned,omitempty"`
	StartedAt time.Time      `json:"started_at"`
	EndedAt   *time.Time     `json:"ended_at,omitempty"`
	// Train is the handle of the train the session's bead is a child of, set
	// on listed sessions (see Bead.Train); never stored.
	Train string `json:"train,omitempty"`
}

// PendingPrompt is a key of a handle, a session's or a crew member's: the
// first prompt a runner could not deliver because the agent was not ready
// (often blocked on a question a human must answer, such as trusting a
// folder), or sent and did not see arrive. The dispatcher delivers it once
// the agent is idle, and keeps it until it arrived. It is named here, with
// the record that carries the handle, because the runner writes it, the
// dispatcher delivers it and the views read it.
const PendingPrompt = "pending_prompt"

// RunnerName is the runner that started the session. A session from before
// sessions recorded their runner is known by what its runner left on its
// handle, and is "" when that tells nothing.
func (s Session) RunnerName() string {
	switch {
	case s.Runner != "":
		return s.Runner
	case s.Handle["manual"] != nil:
		return "manual"
	case s.Handle["pid"] != nil:
		return "exec"
	case s.Handle["peer"] != nil:
		return PeerRunner
	case s.Handle["agent"] != nil:
		return "herdr"
	}
	return ""
}

// Manual reports whether the session is a manual group's claim: nothing
// runs in it, a person works the bead.
func (s Session) Manual() bool { return s.RunnerName() == "manual" }

// RunsHere reports whether a session of the runner named runs something on
// this machine, an agent or a script, and so counts against max_sessions
// (MaxSessions): not a manual group's claim, which is a person's turn, and
// not a peer group's, whose work another yard does.
func RunsHere(runner string) bool { return runner != "manual" && runner != "peer" }

func (s Session) live() bool { return s.State == "starting" || s.State == "running" }

// liveKey is the session's entry in the live index, under its group so a
// claim counts a group's sessions by prefix.
func (s Session) liveKey() []byte { return []byte(join(s.Group, s.ID)) }

// startKey is the session's entry in the index by start: the microsecond
// it started at, big-endian so the keys are in that order, then its id.
// beadKey is its entry in the index by bead: the same under its bead, so a
// bead's sessions are one prefix, in the order they started.
func (s Session) startKey() []byte {
	return append(startStamp(s.StartedAt), s.ID...)
}

func (s Session) beadKey() []byte {
	return append([]byte(s.Bead+"\x00"), s.startKey()...)
}

// startStamp is t as the two indexes key it; stampLen is its length.
func startStamp(t time.Time) []byte {
	return binary.BigEndian.AppendUint64(nil, uint64(t.UnixMicro()))
}

const stampLen = 8

// paneHandle names the two places a handle records a pane's process: the
// pid and the key of its start time.
var paneHandle = [][2]string{{"pid", "start"}, {"shell_pid", "shell_start"}}

// paneKeys are the session's entries in the pane index, one for each pid
// its handle names.
func (s Session) paneKeys() []string {
	var out []string
	for _, k := range paneHandle {
		if p, ok := pidIn(s.Handle[k[0]]); ok {
			if key := join(strconv.Itoa(p), s.ID); !slices.Contains(out, key) {
				out = append(out, key)
			}
		}
	}
	return out
}

// unstopped reports an ended session whose runner resources are still up,
// and workspaceLeft a stopped one whose runner left something on disk that
// has not been cleaned up: a worktree (handle.worktree), or a brief in a
// dir depot's directory (handle.dir).
func (s Session) unstopped() bool { return !s.live() && !s.Stopped }

func (s Session) workspaceLeft() bool {
	return s.Stopped && !s.Cleaned && (s.Handle["worktree"] != nil || s.Handle["dir"] != nil)
}

// pending reports a session the daemon is not done with: one Unstopped or
// ClosedWithWorkspace may list. It is the two listings' own conditions and
// nothing more, so the index answers as a walk of every session would in
// any state a session row can be in.
func (s Session) pending() bool { return s.unstopped() || s.workspaceLeft() }

// putSession writes sess and keeps it in, or out of, the live, pane and
// pending indexes, and in those by bead and by start. Every write of a session by a running store goes
// through here, so the indexes are exact; the move to UUIDs, which writes
// the sessions without it, fills them itself.
func putSession(tx *bolt.Tx, sess *Session) error {
	// The pane index is keyed by what the handle names, and a write may
	// replace the handle: the entries of the row as it was go with it.
	var old Session
	had, err := get(tx, bSessions, sess.ID, &old)
	if err != nil {
		return err
	}
	if err := put(tx, bSessions, sess.ID, sess); err != nil {
		return err
	}
	if sess.live() {
		err = tx.Bucket(bSessLive).Put(sess.liveKey(), []byte{})
	} else {
		err = tx.Bucket(bSessLive).Delete(sess.liveKey())
	}
	if err != nil {
		return err
	}
	// Most writes change neither the pids nor whether the session is
	// pending: those indexes are written only where they differ.
	was, is := old.paneKeys(), sess.paneKeys()
	for _, k := range was {
		if !slices.Contains(is, k) {
			if err := tx.Bucket(bSessPane).Delete([]byte(k)); err != nil {
				return err
			}
		}
	}
	for _, k := range is {
		if !slices.Contains(was, k) {
			if err := tx.Bucket(bSessPane).Put([]byte(k), []byte{}); err != nil {
				return err
			}
		}
	}
	switch pend := tx.Bucket(bSessPend); {
	case sess.pending() && !(had && old.pending()):
		err = pend.Put([]byte(sess.ID), []byte{})
	case !sess.pending() && had && old.pending():
		err = pend.Delete([]byte(sess.ID))
	}
	if err != nil {
		return err
	}
	// A session keeps its bead and its start, so these two are written
	// with its first write; one that did change leaves no entry behind.
	if had && bytes.Equal(old.beadKey(), sess.beadKey()) {
		return nil
	}
	if had {
		if err := tx.Bucket(bSessBead).Delete(old.beadKey()); err != nil {
			return err
		}
		if err := tx.Bucket(bSessTime).Delete(old.startKey()); err != nil {
			return err
		}
	}
	if err := tx.Bucket(bSessBead).Put(sess.beadKey(), []byte{}); err != nil {
		return err
	}
	return tx.Bucket(bSessTime).Put(sess.startKey(), []byte{})
}

// indexSessionOrder builds the indexes by bead and by start, once, for a
// store from before them.
func indexSessionOrder(tx *bolt.Tx) error {
	if tx.Bucket(bSessions) == nil || tx.Bucket(bSessBead) != nil && tx.Bucket(bSessTime) != nil {
		return nil
	}
	for _, b := range [][]byte{bSessBead, bSessTime} {
		if tx.Bucket(b) != nil {
			if err := tx.DeleteBucket(b); err != nil {
				return err
			}
		}
		if _, err := tx.CreateBucket(b); err != nil {
			return err
		}
	}
	return fillSessionOrder(tx)
}

// fillSessionOrder puts every session in the indexes by bead and by start.
func fillSessionOrder(tx *bolt.Tx) error {
	return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		if err := tx.Bucket(bSessBead).Put(sess.beadKey(), []byte{}); err != nil {
			return err
		}
		return tx.Bucket(bSessTime).Put(sess.startKey(), []byte{})
	})
}

// indexPanesAndPending builds the pane and pending indexes, once, for a
// store from before them.
func indexPanesAndPending(tx *bolt.Tx) error {
	if tx.Bucket(bSessions) == nil || tx.Bucket(bSessPane) != nil && tx.Bucket(bSessPend) != nil {
		return nil
	}
	for _, b := range [][]byte{bSessPane, bSessPend} {
		if tx.Bucket(b) != nil {
			if err := tx.DeleteBucket(b); err != nil {
				return err
			}
		}
		if _, err := tx.CreateBucket(b); err != nil {
			return err
		}
	}
	return fillPanesAndPending(tx)
}

// fillPanesAndPending puts every session in the pane index under the pids
// its handle names, and the pending ones in the pending index.
func fillPanesAndPending(tx *bolt.Tx) error {
	return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		for _, k := range sess.paneKeys() {
			if err := tx.Bucket(bSessPane).Put([]byte(k), []byte{}); err != nil {
				return err
			}
		}
		if !sess.pending() {
			return nil
		}
		return tx.Bucket(bSessPend).Put([]byte(sess.ID), []byte{})
	})
}

// liveSessions reads the sessions that are starting or running, of one
// group or ("") of all, through the live index: the store keeps every
// session until it is pruned, and nothing on the dispatcher's path should
// scale with that.
func (s *Store) liveSessions(tx *bolt.Tx, group string) ([]Session, error) {
	var out []Session
	prefix := ""
	if group != "" {
		prefix = group + "\x00"
	}
	for _, k := range keys(tx, bSessLive, prefix) {
		id := k[strings.LastIndexByte(k, 0)+1:]
		var sess Session
		if ok, err := get(tx, bSessions, id, &sess); err != nil {
			return nil, err
		} else if ok {
			out = append(out, sess)
		}
	}
	s.sessionReads.Add(int64(len(out)))
	return out, nil
}

// indexSessions builds the live index, once, for a store from before it.
func indexSessions(tx *bolt.Tx) error {
	if tx.Bucket(bSessLive) != nil || tx.Bucket(bSessions) == nil {
		return nil
	}
	if _, err := tx.CreateBucket(bSessLive); err != nil {
		return err
	}
	return fillLiveSessions(tx)
}

// fillLiveSessions puts every session that is starting or running in the
// live index.
func fillLiveSessions(tx *bolt.Tx) error {
	return each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		if !sess.live() {
			return nil
		}
		return tx.Bucket(bSessLive).Put(sess.liveKey(), []byte{})
	})
}

// Claim atomically assigns an unowned bead to group and opens a session.
// It returns ErrTaken if the bead is missing, owned, in another stage, or
// blocked or held: anything that changed since the caller read it; and
// ErrAtLimit if the group already has as many live sessions as its limit.
//
// via is how the dispatcher came to the group, for the claimed event: the
// route's id (`route`), the groups the route passed over with why
// (`passed`), and what of the group's condition could not be evaluated
// (`limits_unknown`). A claim that names none logs the event as it was.
// Whatever the claim came by, the event names the group that last held the
// bead in this stage when it is another (`was`).
func (s *Store) Claim(ctx context.Context, beadID, group, stage, actor string, via ...Routed) (*Session, error) {
	var sess *Session
	// The blockers' repository flows, read before the write: git must not
	// run inside one. The readiness check below resolves their flows
	// through them, and answers what the read path does.
	look := &untilLooks{repos: s.untilBlockerRepos(ctx, beadID), memo: flowMemo{}}
	err := s.write(func(w *wtx) error {
		row, err := w.loadBead(beadID)
		if err != nil {
			return ErrTaken
		}
		if row.Group != nil || row.Stage != stage || !ready(w.Tx, &row.Bead, look) {
			return ErrTaken
		}
		var g Group
		if ok, err := get(w.Tx, bGroups, group, &g); err != nil {
			return err
		} else if !ok {
			return fmt.Errorf("group %s: %w", group, ErrNotFound)
		}
		// The limit is checked here, in the claim's own transaction, so two
		// dispatchers (or a dispatcher and a stale in-memory count) cannot
		// both take the last slot. The dispatcher's own count is a fast path.
		// The live index answers it without reading the sessions that ended.
		live := len(keys(w.Tx, bSessLive, group+"\x00"))
		s.sessionReads.Add(int64(live))
		if live >= max(g.Limit, 1) {
			return fmt.Errorf("group %s runs %d of %d: %w", group, live, max(g.Limit, 1), ErrAtLimit)
		}
		// The session keeps the kind and model it starts with, as the group
		// has them now: the group's config, and for a herdr group without a
		// kind the yard's default_kind, as group add resolves it. A group
		// whose runner is not herdr has neither.
		var def string
		if _, err := get(w.Tx, bSettings, DefaultKindSetting, &def); err != nil {
			return err
		}
		kind := g.Kind(def)
		model, _ := g.Config["model"].(string)
		stored := Session{ID: newEdgeID(), Bead: row.UUID, Group: group, Stage: stage, Runner: g.Runner,
			Kind: kind, Model: model, State: "starting", StartedAt: now()}
		if err := putSession(w.Tx, &stored); err != nil {
			return err
		}
		row.Group, row.Session = &group, &stored.ID
		at := stored.StartedAt
		row.StayClaimAt, row.StayClaimGroup, row.StayClaimSession = &at, group, stored.ID
		if err := w.mintBead(row, actor); err != nil {
			return err
		}
		sess = &stored
		sess.Bead = row.ID
		data := map[string]any{"group": group, "stage": stage, "session": stored.ID}
		if kind != "" {
			data["kind"] = kind
		}
		if model != "" {
			data["model"] = model
		}
		// A bead that came back to the stage may go to another group than
		// the one that worked it there before, because a window crossed its
		// threshold in between: the event says whose work the session
		// continues.
		if was, err := lastHeld(w.Tx, row.UUID, stage); err != nil {
			return err
		} else if was != "" && was != group {
			data["was"] = was
		}
		for _, v := range via {
			if v.Route != "" {
				data["route"] = v.Route
			}
			if len(v.Passed) > 0 {
				data["passed"] = v.Passed
			}
			if v.Unknown != "" {
				data["limits_unknown"] = v.Unknown
			}
		}
		return w.event(row.UUID, "claimed", actor, data)
	})
	return sess, err
}

// lastHeld is the group that last claimed the bead in stage, as the event
// log still has it; "" for none.
func lastHeld(tx *bolt.Tx, uuid, stage string) (string, error) {
	ks := keys(tx, bEventsBy, uuid+"\x00")
	for i := len(ks) - 1; i >= 0; i-- {
		var e Event
		if ok, err := get(tx, bEvents, ks[i][len(uuid)+1:], &e); err != nil {
			return "", err
		} else if !ok || e.Kind != "claimed" || e.Data["stage"] != stage {
			continue
		}
		group, _ := e.Data["group"].(string)
		return group, nil
	}
	return "", nil
}

// Routed is how a claim came to its group: the route that matched the
// bead, and what the route's choice among its groups said (RouteChoice).
type Routed struct {
	Route   string        `json:"route,omitempty"`
	Passed  []PassedGroup `json:"passed,omitempty"`
	Unknown string        `json:"limits_unknown,omitempty"`
}

// changeSession applies fn to a session inside a write transaction.
func (s *Store) changeSession(id string, fn func(*wtx, *Session) error) error {
	return s.write(func(w *wtx) error {
		var sess Session
		ok, err := get(w.Tx, bSessions, id, &sess)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		if err := fn(w, &sess); err != nil {
			return err
		}
		return putSession(w.Tx, &sess)
	})
}

// MarkRunning records the runner handle once the runner has started.
func (s *Store) MarkRunning(ctx context.Context, sessionID string, handle map[string]any) error {
	return s.changeSession(sessionID, func(w *wtx, sess *Session) error {
		if sess.State != "starting" {
			return nil
		}
		sess.State, sess.Handle = "running", handle
		return w.event(sess.Bead, "started", "yard", map[string]any{"session": sessionID, "handle": handle})
	})
}

// EndSession ends a live session as 'died' or 'failed' and, if the bead is
// still owned by it, returns the bead for routing. A death or failure in
// the bead's current stage counts against it (see FailureCount). 'aborted'
// is a start the runtime could not make (herdr down): it returns the bead
// the same way but is no failure of the bead's, so it is not counted.
// Ending an ended session is a no-op.
func (s *Store) EndSession(ctx context.Context, sessionID, state, reason string) error {
	if state != "died" && state != "failed" && state != "aborted" {
		return fmt.Errorf("EndSession: state %q not allowed", state)
	}
	return s.changeSession(sessionID, func(w *wtx, sess *Session) error {
		if !sess.live() {
			return nil
		}
		t := now()
		sess.State, sess.Error, sess.EndedAt = state, &reason, &t
		old, err := w.loadBead(sess.Bead)
		if err != nil {
			return err
		}
		row := cloneRow(old)
		if old.Session != nil && *old.Session == sessionID {
			row.Group, row.Session = nil, nil
		}
		if state != "aborted" && sess.Stage == row.Stage {
			row.Failures++
		}
		// A failed claim never pairs as a build: its time is cleared,
		// its group kept for the counts. Aborted is no failure of the
		// bead's (a start the runtime could not make), so it still
		// pairs; a session that is not the stay's latest claim leaves
		// the reclaim's alone.
		if (state == "failed" || state == "died") && row.StayClaimSession == sessionID {
			row.StayClaimAt = nil
		}
		if err := w.Bucket(bETAFailed).Delete([]byte(sessionID)); err != nil {
			return err
		}
		if _, err := w.saveBead(old, row, "yard"); err != nil {
			return err
		}
		return w.event(sess.Bead, "session_"+state, "yard", map[string]any{"session": sessionID, "reason": reason})
	})
}

func (s *Store) MarkStopped(ctx context.Context, sessionID string) error {
	return s.changeSession(sessionID, func(_ *wtx, sess *Session) error {
		sess.Stopped = true
		return nil
	})
}

// SetHandle records runner resources on a session without changing state,
// so teardown can clean up after a failed start.
func (s *Store) SetHandle(ctx context.Context, sessionID string, handle map[string]any) error {
	return s.changeSession(sessionID, func(_ *wtx, sess *Session) error {
		sess.Handle = handle
		return nil
	})
}

// MarkCleaned records that a closed bead's workspace was handled, so it is
// attempted once.
func (s *Store) MarkCleaned(ctx context.Context, sessionID string) error {
	return s.changeSession(sessionID, func(_ *wtx, sess *Session) error {
		sess.Cleaned = true
		return nil
	})
}

func (s *Store) GetSession(ctx context.Context, id string) (*Session, error) {
	sess := new(Session)
	err := s.read(func(tx *bolt.Tx) error {
		ok, err := get(tx, bSessions, id, sess)
		if err == nil && !ok {
			err = fmt.Errorf("session %s: %w", id, ErrNotFound)
		}
		if ok {
			sess.Bead = handlesIn(tx).of(sess.Bead)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// sessionsWhere returns matching sessions by start time. match sees the
// session as stored, naming its bead by UUID. With pending, match keeps
// only sessions the daemon is not done with, and only those are read,
// through their index, which has the keys of the sessions bucket and so
// its order: the store keeps every session until it is pruned, and the
// dispatcher's pass should not scale with that.
func (s *Store) sessionsWhere(pending bool, match func(*bolt.Tx, *Session) bool) ([]Session, error) {
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		h := handlesIn(tx)
		see := func(_ []byte, sess *Session) error {
			if match(tx, sess) {
				sess.Train = trainOf(tx, sess.Bead)
				sess.Bead = h.of(sess.Bead)
				out = append(out, *sess)
			}
			return nil
		}
		if !pending {
			return each(tx, bSessions, "", see)
		}
		ids := keys(tx, bSessPend, "")
		s.pendingReads.Add(int64(len(ids)))
		for _, id := range ids {
			sess := new(Session)
			if ok, err := get(tx, bSessions, id, sess); err != nil {
				return err
			} else if !ok {
				continue
			}
			if err := see(nil, sess); err != nil {
				return err
			}
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, err
}

// Sessions lists live sessions, or every session when all is set. The
// live ones come from their index, so the dispatcher's pass does not read
// every session the store keeps.
func (s *Store) Sessions(ctx context.Context, all bool) ([]Session, error) {
	if all {
		return s.sessionsWhere(false, func(_ *bolt.Tx, sess *Session) bool { return true })
	}
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		live, err := s.liveSessions(tx, "")
		if err != nil {
			return err
		}
		h := handlesIn(tx)
		for _, sess := range live {
			sess.Train = trainOf(tx, sess.Bead)
			sess.Bead = h.of(sess.Bead)
			out = append(out, sess)
		}
		return nil
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].StartedAt.Before(out[j].StartedAt) })
	return out, err
}

// SessionFilter says which sessions ListSessions lists.
type SessionFilter struct {
	All bool // include ended
	// Bead keeps the sessions of this bead (a handle or UUID).
	Bead string
	// Running leaves out a manual group's claims, which nothing runs in.
	Running bool
	// Since keeps the sessions that started at it or later.
	Since time.Time
	// Limit keeps the newest that many of what the rest kept; 0 is all.
	Limit int
}

// ListSessions lists the sessions f keeps, by start time. Without All they
// are live ones, read through their index as Sessions reads them. With it
// they are read newest first through the index by start, or through the
// index by bead for one bead's, and the read ends at Limit or at Since: the
// store keeps every session until it is pruned, and neither a bead's
// sessions nor the newest few should cost what all of them do.
func (s *Store) ListSessions(ctx context.Context, f SessionFilter) ([]Session, error) {
	out := []Session{}
	err := s.read(func(tx *bolt.Tx) error {
		bead := ""
		if f.Bead != "" {
			u, err := resolve(tx, f.Bead)
			if err != nil {
				return err
			}
			bead = u
		}
		h := handlesIn(tx)
		keep := func(sess *Session) {
			sess.Train = trainOf(tx, sess.Bead)
			sess.Bead = h.of(sess.Bead)
			out = append(out, *sess)
		}
		if !f.All && bead == "" {
			live, err := s.liveSessions(tx, "")
			if err != nil {
				return err
			}
			sort.SliceStable(live, func(i, j int) bool { return live[i].StartedAt.After(live[j].StartedAt) })
			for _, sess := range live {
				if f.Limit > 0 && len(out) == f.Limit {
					break
				}
				if (bead == "" || sess.Bead == bead) && !(f.Running && sess.Manual()) && !sess.StartedAt.Before(f.Since) {
					keep(&sess)
				}
			}
			return nil
		}
		bucket, prefix := bSessTime, []byte{}
		if bead != "" {
			bucket, prefix = bSessBead, []byte(bead+"\x00")
		}
		var floor []byte
		if !f.Since.IsZero() {
			floor = startStamp(f.Since)
		}
		n := 0
		defer func() { s.orderReads.Add(int64(n)) }()
		c := tx.Bucket(bucket).Cursor()
		// The last key under the prefix is the one before the next
		// prefix's first; ids never contain NUL, so that is <bead>\x01.
		k, _ := c.Last()
		if bead != "" {
			if k, _ = c.Seek([]byte(bead + "\x01")); k == nil {
				k, _ = c.Last()
			} else {
				k, _ = c.Prev()
			}
		}
		for ; k != nil && bytes.HasPrefix(k, prefix) && (f.Limit <= 0 || len(out) < f.Limit); k, _ = c.Prev() {
			at := k[len(prefix):]
			if len(at) < stampLen || floor != nil && bytes.Compare(at[:stampLen], floor) < 0 {
				break
			}
			sess := new(Session)
			if ok, err := get(tx, bSessions, string(at[stampLen:]), sess); err != nil {
				return err
			} else if !ok {
				continue
			}
			n++
			if (f.All || sess.live()) && !(f.Running && sess.Manual()) {
				keep(sess)
			}
		}
		return nil
	})
	slices.Reverse(out)
	return out, err
}

// Unstopped lists ended sessions whose runner resources are still up.
func (s *Store) Unstopped(ctx context.Context) ([]Session, error) {
	return s.sessionsWhere(true, func(_ *bolt.Tx, sess *Session) bool { return sess.unstopped() })
}

// ClosedWithWorkspace lists stopped sessions of closed beads whose runner
// left something on disk that has not been cleaned up: a worktree
// (handle.worktree), or a brief in a dir depot's directory (handle.dir).
func (s *Store) ClosedWithWorkspace(ctx context.Context) ([]Session, error) {
	return s.sessionsWhere(true, func(tx *bolt.Tx, sess *Session) bool {
		if !sess.workspaceLeft() {
			return false
		}
		var b beadRow
		ok, _ := get(tx, bBeads, sess.Bead, &b)
		return ok && b.Status == "closed"
	})
}

// FailureCount is how many sessions have died or failed for the bead in
// stage since it entered the stage or a human last un-held it, so
// un-holding grants fresh attempts. The count lives on the bead (see
// beadRow.Failures): it does not depend on the unheld event or the ended
// sessions, which the log and the session table prune. It is 0 for any
// stage but the bead's current one.
func (s *Store) FailureCount(ctx context.Context, ref, stage string) (int, error) {
	n := 0
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, ref)
		if err != nil {
			return err
		}
		if row.Stage == stage {
			n = row.Failures
		}
		return nil
	})
	return n, err
}
