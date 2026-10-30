package cli

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"io"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// routeReportOpts is what `yardr route report` reads the event log for: the
// label prefixes that make a row, the stages its claims come from, the
// earliest event it counts, and whose beads it keeps. A zero since counts
// everything retained; an empty originator keeps every originator's.
// byDepot splits each group row per depot the group's beads are in.
type routeReportOpts struct {
	prefixes []string
	stages   map[string]bool
	since    time.Time
	// originator keeps one person's beads; operator is what an empty
	// originator reads as.
	originator string
	operator   string
	byDepot    bool
}

// routeReportRow is one GROUP/ORIGINATOR/LABEL cell of the report: the
// closed beads one group claimed in the stages for one user, what they
// cost. Depot names the beads' depot under --by depot and is empty
// otherwise, when the cell spans depots. SentBack is send-backs per bead,
// AtLimit the beads a round limit sent to its then stage from one the
// group claimed them in, ReviewFixed review's forward fixes per bead and
// SecFixed the security review's; MedianWait is how long a bead was ready
// and unclaimed, nil when no counted bead's wait is known; MedianBuild is
// how long a claim took to leave the stage, nil when no counted claim did
// (its exit aged out of the log, or the bead closed without leaving the
// stage). TokensPerBead is the median new tokens per bead whose sessions
// are all counted or partial, nil when no bead qualifies; Tokens the new
// tokens over the cell's counted sessions, Usage the five counts behind
// it; Partial says one session read partial, Uncounted how many came back
// missing, Pending how many have no result yet.
type routeReportRow struct {
	Group         string      `json:"group"`
	Depot         string      `json:"depot"`
	Originator    string      `json:"originator"`
	Label         string      `json:"label"`
	Beads         int         `json:"beads"`
	SentBack      float64     `json:"sent_back"`
	AtLimit       int         `json:"at_limit"`
	ReviewFixed   float64     `json:"review_fixed"`
	SecFixed      float64     `json:"sec_fixed"`
	Failed        int         `json:"failed"`
	MedianWait    *string     `json:"median_wait"`
	MedianBuild   *string     `json:"median_build"`
	TokensPerBead *int        `json:"tokens_per_bead"`
	Tokens        int         `json:"tokens"`
	Usage         store.Usage `json:"usage"`
	Partial       bool        `json:"partial"`
	Uncounted     int         `json:"uncounted"`
	Pending       int         `json:"pending"`
}

// routeKindRow is one BY HARNESS row: the sessions one harness kind ran in
// the window. Sessions counts its claims, Hours their claim-to-exit time
// summed and Median the middle of it, nil when no counted claim exited;
// Stages is the stages they ran, sorted. Tokens, Usage, Partial,
// Uncounted and Pending are the kind's counts, as the cell's are.
type routeKindRow struct {
	Kind      string      `json:"kind"`
	Sessions  int         `json:"sessions"`
	Hours     *string     `json:"hours"`
	Median    *string     `json:"median"`
	Stages    []string    `json:"stages"`
	Tokens    int         `json:"tokens"`
	Usage     store.Usage `json:"usage"`
	Partial   bool        `json:"partial"`
	Uncounted int         `json:"uncounted"`
	Pending   int         `json:"pending"`
}

// routeCrewRow is one CREW row: one member's counted crew sessions in the
// window. Sessions counts its crew_usage events; Tokens, Usage, Partial
// and Uncounted are as the cell's. There is no pending: the log shows no
// crew session until its count is recorded.
type routeCrewRow struct {
	Member    string      `json:"member"`
	Sessions  int         `json:"sessions"`
	Tokens    int         `json:"tokens"`
	Usage     store.Usage `json:"usage"`
	Partial   bool        `json:"partial"`
	Uncounted int         `json:"uncounted"`
}

// routeReport is the report: From is when the oldest retained event was
// logged (nil when the log holds none), Rows one row per group and label,
// ByKind one row per harness kind, Crew one row per crew member, Now where
// each estimate label goes today, best the best builder per difficulty,
// text only.
type routeReport struct {
	From   *time.Time       `json:"from"`
	Rows   []routeReportRow `json:"rows"`
	ByKind []routeKindRow   `json:"by_kind"`
	Crew   []routeCrewRow   `json:"crew"`
	Now    []routeNow       `json:"now"`
	best   []routeBest
}

// routeNow is one now line: where an estimate label goes today. Route is
// the yard default a bead with the label and no depot matches, nil when
// no default takes it; SetAside the depots whose own routes set that
// default aside, sorted.
type routeNow struct {
	Label    string       `json:"label"`
	Route    *routeNowHop `json:"route"`
	SetAside []string     `json:"set_aside"`
}

// routeNowHop is the default a now line names: its id and its groups with
// their conditions, as route list prints them.
type routeNowHop struct {
	ID     string `json:"id"`
	Groups string `json:"groups"`
}

// routeBest is one best line: the group with the lowest sent back +
// repaired per bead for a difficulty label, over the report's own rows.
// tooFew says no group qualified, so the line reads "too few beads to
// compare" instead of naming a builder.
type routeBest struct {
	label   string
	group   string
	perBead float64
	beads   int
	tooFew  bool
}

// reportClaim is one claimed event the report counts: who held which bead in
// which stage, the session it held it with, and the harness kind it ran.
type reportClaim struct {
	bead    string
	group   string
	stage   string
	session string
	kind    string
	at      time.Time
	seq     uint64
}

// reportExit is one advanced event that left a stage (from != to).
type reportExit struct {
	bead  string
	from  string
	to    string
	group string
	at    time.Time
	seq   uint64
}

// routeSessionUsage is one session_usage or crew_usage event read: what
// the session used, whether the reading falls short of it, or why no
// count is there.
type routeSessionUsage struct {
	usage   store.Usage
	partial bool
	missing string
}

// routeCrewUsage is one crew_usage event the report counts: whose member
// it belongs to and what it read.
type routeCrewUsage struct {
	member string
	routeSessionUsage
}

// usageNumber is v as a token count. Counts are ints in the store and
// float64 over the socket's JSON; anything else reads as 0.
func usageNumber(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// usageOf reads the usage record of a session_usage or crew_usage event's
// data, ok when it carries one.
func usageOf(data map[string]any) (routeSessionUsage, bool) {
	rec, ok := data["usage"].(map[string]any)
	if !ok {
		return routeSessionUsage{}, false
	}
	u := routeSessionUsage{}
	u.missing, _ = rec["missing"].(string)
	u.usage = store.Usage{
		Input:      usageNumber(rec["input"]),
		FreshInput: usageNumber(rec["fresh_input"]),
		Output:     usageNumber(rec["output"]),
		CacheRead:  usageNumber(rec["cache_read"]),
		CacheWrite: usageNumber(rec["cache_write"]),
	}
	u.partial, _ = rec["partial"].(bool)
	return u, true
}

// newTokens is the headline number: fresh input, cache writes and output,
// leaving out cache reads, which would drown the difference between beads.
func newTokens(u store.Usage) int {
	return u.FreshInput + u.CacheWrite + u.Output
}

// shortTokens is tokens as the report prints them: 827, 640k, 70.6M. One
// decimal at most, without a trailing zero; what would round to 1000k
// reads as 1M.
func shortTokens(n int) string {
	switch {
	case n < 1000:
		return strconv.Itoa(n)
	case n < 999950:
		return trimDecimal(float64(n)/1000) + "k"
	default:
		return trimDecimal(float64(n)/1000000) + "M"
	}
}

// trimDecimal is f with one decimal, without a trailing ".0".
func trimDecimal(f float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(f, 'f', 1, 64), ".0")
}

// lowerBound marks a printed total that falls short of its row's
// sessions: one read partial, one came back missing, or one has no result
// yet.
func lowerBound(tokens string, partial bool, uncounted, pending int) string {
	if partial || uncounted > 0 || pending > 0 {
		return "≥" + tokens
	}
	return tokens
}

// medianTokens is the middle of v, the mean of the middle two when v is
// even, rounded down. v must not be empty.
func medianTokens(v []int) int {
	s := slices.Clone(v)
	slices.Sort(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

// reportOriginator resolves a bead's stored originator as it is read in the
// report. Older or incomplete records with none belong to the operator.
func reportOriginator(b store.Bead, operator string) string {
	if b.Originator != "" {
		return b.Originator
	}
	return operator
}

// nonBuilderStages are the stages where no builder work happens: the
// default flow's two review stages, design's critique, which is review
// work under another name (packs/design/flows/design/critique.md, worked
// by the reviewers), and shape, where shapers work. A group qualifies for
// a best line only by claiming a bead elsewhere, so reviewer and shaping
// groups never win; every other stage counts as builder work.
var nonBuilderStages = []string{"review", "review-security", "critique", "shape"}

// bestMinBeads is the sample a group needs to win a best line: best
// difficulty:1 on 2 beads names noise, not a builder.
const bestMinBeads = 10

// fixSHARe matches a commit SHA as a review note names it: 7 to 40 hex
// digits on word boundaries.
var fixSHARe = regexp.MustCompile(`\b[0-9a-fA-F]{7,40}\b`)

// fixSHAs is the commit SHAs text names, lowercased and deduped, in
// order. A match without a digit is prose, not a SHA ("defaced"), and
// reads as none; an all-letter short SHA reads as prose with it.
func fixSHAs(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range fixSHARe.FindAllString(text, -1) {
		if !strings.ContainsAny(m, "0123456789") {
			continue
		}
		sha := strings.ToLower(m)
		if !seen[sha] {
			seen[sha] = true
			out = append(out, sha)
		}
	}
	return out
}

// round1 is x to one decimal, as the per-bead columns print.
func round1(x float64) float64 { return math.Round(x*10) / 10 }

// beadFixes is the forward fixes a bead's review stages named new: the
// commits their notes name that no earlier note on the bead named.
type beadFixes struct{ review, sec int }

// countBeadFixes counts bead's forward fixes from its notes, oldest
// first. For stage S the attributed notes are those by the stage's agent
// (runner.AgentName) or by an actor that advanced the bead out of S,
// which covers manual and exec reviews, whose actors are not agent names;
// a note matching both stages reads as review's. A SHA counts for the
// stage of the attributed note that first names it, so a quoted builder
// SHA never counts and a re-quote across send-backs counts once. Only
// notes at or after since count; the earlier set spans every note
// whatever its date.
func countBeadFixes(bead string, notes []store.Note, advancedBy map[string]map[string]bool, since time.Time) beadFixes {
	var out beadFixes
	attributed := func(author, stage string) bool {
		return author == runner.AgentName(bead, stage) || advancedBy[stage][author]
	}
	ordered := slices.Clone(notes)
	slices.SortFunc(ordered, func(a, b store.Note) int { return cmp.Compare(a.Seq, b.Seq) })
	seen := map[string]bool{}
	for _, n := range ordered {
		stage := ""
		if attributed(n.Author, "review") {
			stage = "review"
		} else if attributed(n.Author, "review-security") {
			stage = "review-security"
		}
		for _, sha := range fixSHAs(n.Text) {
			if seen[sha] {
				continue
			}
			seen[sha] = true
			if stage == "" || n.At.Before(since) {
				continue
			}
			if stage == "review" {
				out.review++
			} else {
				out.sec++
			}
		}
	}
	return out
}

// claimedBefore reports whether stages holds a stage the bead reached
// before fix: visited is its stages in the order it first reached them.
func claimedBefore(visited []string, stages map[string]bool, fix string) bool {
	at := slices.Index(visited, fix)
	if at < 0 {
		return false
	}
	for s := range stages {
		if i := slices.Index(visited, s); i >= 0 && i < at {
			return true
		}
	}
	return false
}

// routeCell is one GROUP/ORIGINATOR/LABEL cell while the report sums it.
// Under --by depot the key carries the depot too, but the cell is the
// same shape: the beads are one depot's.
type routeCell struct {
	beads       map[string]bool
	stages      map[string]map[string]bool // bead -> stages claimed by this group
	sessions    map[string][]string        // bead -> session ids claimed by this group
	sendBacks   int
	atLimit     map[string]bool // beads a round limit sent on from a claimed stage
	reviewFixes int
	secFixes    int
	failed      int
	builds      []time.Duration
	waits       []time.Duration
	tokens      int
	usage       store.Usage
	partial     bool
	uncounted   int
	pending     int
	beadMeds    []int // new tokens per bead with counted or partial sessions
}

// bestBuilders names, per difficulty label in the cells, the group with
// the lowest sent back + repaired per bead: the send-back and fix counts
// summed over the group's cells for the label, across originators and
// depots, over their beads. Only a group that claimed one of those beads
// outside the non-builder stages, with bestMinBeads beads at least,
// qualifies, so reviewer and shaping rows never win and tiny samples
// never crown; ties go to more beads, then to the name. A label with no
// winner still gets a line, saying too few beads to compare. Over the
// report's own rows, whatever --since says; --by depot splits the rows
// but never the best lines.
func bestBuilders(cells map[string]*routeCell) []routeBest {
	type acc struct {
		beads     int
		total     int
		qualified bool
	}
	byLabel := map[string]map[string]*acc{}
	for k, ce := range cells {
		parts := strings.Split(k, "\x00")
		group, label := parts[0], parts[3]
		if !strings.HasPrefix(label, "difficulty:") {
			continue
		}
		gl := byLabel[label]
		if gl == nil {
			gl = map[string]*acc{}
			byLabel[label] = gl
		}
		a := gl[group]
		if a == nil {
			a = &acc{}
			gl[group] = a
		}
		a.beads += len(ce.beads)
		a.total += ce.sendBacks + ce.reviewFixes + ce.secFixes
		for bead := range ce.beads {
			for s := range ce.stages[bead] {
				if !slices.Contains(nonBuilderStages, s) {
					a.qualified = true
					break
				}
			}
			if a.qualified {
				break
			}
		}
	}
	var out []routeBest
	for label, gl := range byLabel {
		var groups []string
		for g := range gl {
			groups = append(groups, g)
		}
		slices.Sort(groups)
		winner := ""
		var wt, wb int
		for _, g := range groups {
			a := gl[g]
			if !a.qualified || a.beads < bestMinBeads {
				continue
			}
			// Names ascend, so a tie on the metric and the beads
			// keeps the earlier name.
			if winner == "" || a.total*wb < wt*a.beads ||
				(a.total*wb == wt*a.beads && a.beads > wb) {
				winner, wt, wb = g, a.total, a.beads
			}
		}
		if winner != "" {
			out = append(out, routeBest{label: label, group: winner, perBead: round1(float64(wt) / float64(wb)), beads: wb})
		} else {
			out = append(out, routeBest{label: label, tooFew: true})
		}
	}
	slices.SortFunc(out, func(a, b routeBest) int { return strings.Compare(a.label, b.label) })
	return out
}

// waitVisit is one bead's measured stay in one stage while the report
// adds up its ready-and-unclaimed time. A visit starts at creation in the
// stage or at the move into it and ends at the move out or the close; a
// claim with no visit start in the log (its entry aged out of retention)
// leaves no sample. open is the start of the running ready interval, nil
// while the bead is claimed, held or blocked. dead says the claimed
// session failed with no reclaim yet: the time since still accrues, but
// only a reclaim commits it (deadSnap is what the total was at the
// failure), because a failed event may leave the session live, still
// working towards the move out.
type waitVisit struct {
	inVisit  bool
	has      bool
	dur      time.Duration
	open     *time.Time
	claimed  bool
	session  string
	dead     bool
	deadSnap time.Duration
}

// waitTracker adds up, per bead and stage, the time the bead was ready
// and unclaimed: from entering the stage, from its last blocker closing,
// from unheld, and from the end of a session that left it in the stage,
// until claimed, held, or a new blocker. Blocked time comes from the
// dependency gate's own reblocked/unblocked events, which the store logs
// on every flip (a blocker closing, reopening or moving, an until edge
// releasing, an edge added or removed, a move into a holding stage), and
// so holds across visits: a bead blocked in one stage is still blocked
// when it moves, unless the move logs the opening. Held is likewise the
// bead's, not the visit's. Only counted stages are tracked; intervals
// clip to since, so --since reads the window's share of a longer wait.
type waitTracker struct {
	stages  map[string]bool
	since   time.Time
	current map[string]string     // bead -> stage, including stages outside the report
	visits  map[string]*waitVisit // bead \x00 stage
	held    map[string]bool       // bead, across its visits
	blocked map[string]bool       // bead, across its visits
}

func newWaitTracker(stages map[string]bool, since time.Time) *waitTracker {
	return &waitTracker{stages: stages, since: since, current: map[string]string{}, visits: map[string]*waitVisit{}, held: map[string]bool{}, blocked: map[string]bool{}}
}

func (t *waitTracker) visit(bead, stage string) *waitVisit {
	v := t.visits[bead+"\x00"+stage]
	if v == nil {
		v = &waitVisit{}
		t.visits[bead+"\x00"+stage] = v
	}
	return v
}

// close ends v's running ready interval at at, counting the part at or
// after since.
func (t *waitTracker) close(v *waitVisit, at time.Time) {
	if v.open == nil {
		return
	}
	start := *v.open
	v.open = nil
	if start.Before(t.since) {
		start = t.since
	}
	if at.After(start) {
		v.dur += at.Sub(start)
	}
}

// open starts v's ready interval at at, unless the bead is claimed, held
// or blocked.
func (t *waitTracker) open(bead string, v *waitVisit, at time.Time) {
	if !v.inVisit || v.claimed || t.held[bead] || t.blocked[bead] || v.open != nil {
		return
	}
	v.open = &at
}

// end closes v's visit at at: a move out or the close. Time since an
// unreclaimed failure is dropped with it: the session may have kept
// working to the move, so only a reclaim proves the gap was a wait.
func (t *waitTracker) end(v *waitVisit, at time.Time) {
	t.close(v, at)
	if v.dead {
		v.dur, v.dead = v.deadSnap, false
	}
	v.inVisit, v.claimed, v.session = false, false, ""
}

// enter starts bead's visit in stage at at: created or moved there.
func (t *waitTracker) enter(bead, stage string, at time.Time) {
	t.current[bead] = stage
	if !t.stages[stage] {
		return
	}
	v := t.visit(bead, stage)
	if v.inVisit {
		t.end(v, at)
	}
	v.inVisit, v.has = true, true
	t.open(bead, v, at)
}

// durations is the measured wait per bead and stage: the total, and
// whether a visit start was seen, without which there is no sample.
func (t *waitTracker) durations() (map[string]time.Duration, map[string]bool) {
	dur, has := map[string]time.Duration{}, map[string]bool{}
	for k, v := range t.visits {
		if v.has {
			dur[k], has[k] = v.dur, true
		}
	}
	return dur, has
}

// waitClaim records bead's claim in stage with session at at: the wait
// ends, and a reclaim commits the time since the failure. A claim with
// no visit leaves nothing: with no start there is no sample.
func (t *waitTracker) waitClaim(bead, stage, session string, at time.Time) {
	if !t.stages[stage] {
		return
	}
	v := t.visit(bead, stage)
	if !v.inVisit {
		return
	}
	t.close(v, at)
	v.dead = false
	v.claimed, v.session = true, session
}

// waitFailed records a failed session event for session at at: the
// session stops counting as working the bead, from the first such event,
// and the bead is ready again until the reclaim commits the gap or the
// visit ends and drops it.
func (t *waitTracker) waitFailed(bead, session string, at time.Time) {
	if session == "" {
		return
	}
	for k, v := range t.visits {
		b, _, _ := strings.Cut(k, "\x00")
		if b != bead || !v.claimed || v.session != session || v.dead {
			continue
		}
		v.dead, v.deadSnap = true, v.dur
		v.claimed, v.session = false, ""
		t.open(b, v, at)
	}
}

// waitVisitsOf applies f to bead's tracked visits, in stage order.
func (t *waitTracker) waitVisitsOf(bead string, f func(*waitVisit)) {
	var stages []string
	for k := range t.visits {
		if b, stage, _ := strings.Cut(k, "\x00"); b == bead {
			stages = append(stages, stage)
		}
	}
	slices.Sort(stages)
	for _, s := range stages {
		f(t.visits[bead+"\x00"+s])
	}
}

// summarizeRouteEvents builds the report from the events read oldest first.
// Beads come from the bead records, not the log: only closed beads count,
// under the labels they have now and the user each is for, or under "-"
// when they have none of the prefixes, and under their depot with byDepot.
// A bead a group never claimed is in no cell; one two groups worked in one
// stage is in each group's. A cell's wait samples are its beads' ready
// time in the stages the group claimed them in. Notes
// are the bead's notes by handle, in seq order: what the fix columns
// count. BY HARNESS counts every claim with a kind under the same stage,
// time and originator filters, open beads included: sessions ran, not
// beads closed. Tokens come from session_usage events through the claims:
// a count belongs to the cells of its session's claim, whatever filters
// the claim passed, and a claim with no count yet is pending; CREW reads
// crew_usage, which belongs to no bead and keeps only the time filter.
func summarizeRouteEvents(evs []store.Event, beads map[string]store.Bead, o routeReportOpts, notes map[string][]store.Note) *routeReport {
	rep := &routeReport{Rows: []routeReportRow{}, ByKind: []routeKindRow{}, Crew: []routeCrewRow{}, Now: []routeNow{}}
	if len(evs) > 0 {
		from := evs[0].At
		rep.From = &from
	}
	wt := newWaitTracker(o.stages, o.since)
	var claims []reportClaim
	var exits []reportExit
	var crew []routeCrewUsage
	// sendBacks counts, per bead and stage, the moves back into the stage.
	sendBacks := map[string]int{}
	// atLimit marks, per bead and stage, that a round limit sent the bead
	// on from the stage: a rounds_reached whose to the outcome leads to.
	atLimit := map[string]bool{}
	// visited is the stages a bead has reached, in the order it first
	// reached them: what the send-back rule (store.IsSendBack) reads.
	visited := map[string][]string{}
	sessionOf := map[string]reportClaim{}
	usageBySession := map[string]routeSessionUsage{}
	for _, e := range evs {
		// Crew counts belong to no bead, so they are read before the
		// bead check, under the time filter alone.
		if e.Kind == "crew_usage" {
			if e.At.Before(o.since) {
				continue
			}
			member, _ := e.Data["member"].(string)
			if member == "" {
				continue
			}
			if u, ok := usageOf(e.Data); ok {
				crew = append(crew, routeCrewUsage{member: member, routeSessionUsage: u})
			}
			continue
		}
		if e.Bead == nil {
			continue
		}
		bead := *e.Bead
		// The wait machine reads every event whatever --since says:
		// intervals clip to the window, and a claim out of it keeps
		// the bead out of every cell, unattributed.
		if store.IsETAFailedKind(e.Kind) {
			session, _ := e.Data["session"].(string)
			wt.waitFailed(bead, session, e.At)
		}
		switch e.Kind {
		case "created":
			stage, _ := e.Data["stage"].(string)
			if stage != "" {
				wt.enter(bead, stage, e.At)
			}
		case "closed":
			wt.waitVisitsOf(bead, func(v *waitVisit) { wt.end(v, e.At) })
		case "reopened":
			// Reopening begins a fresh stay in the bead's current stage.
			// The event omits the stage, so retain it from the last entry.
			if stage := wt.current[bead]; stage != "" {
				wt.enter(bead, stage, e.At)
			}
		case "held":
			wt.held[bead] = true
			wt.waitVisitsOf(bead, func(v *waitVisit) {
				wt.close(v, e.At)
				v.claimed, v.session = false, ""
			})
		case "unheld":
			wt.held[bead] = false
			wt.waitVisitsOf(bead, func(v *waitVisit) { wt.open(bead, v, e.At) })
		case "released":
			wt.waitVisitsOf(bead, func(v *waitVisit) {
				v.claimed, v.session = false, ""
				wt.open(bead, v, e.At)
			})
		case "unblocked":
			wt.blocked[bead] = false
			wt.waitVisitsOf(bead, func(v *waitVisit) { wt.open(bead, v, e.At) })
		case "reblocked":
			wt.blocked[bead] = true
			wt.waitVisitsOf(bead, func(v *waitVisit) { wt.close(v, e.At) })
		case "session_usage":
			// The count follows its session's claim, whatever
			// filters that claim passed, so it keeps no filter
			// of its own; one with no in-scope claim is ignored.
			session, _ := e.Data["session"].(string)
			claim, inScope := sessionOf[session]
			if session == "" || !inScope || claim.session == "" {
				continue
			}
			if u, ok := usageOf(e.Data); ok {
				usageBySession[session] = u
			}
		case "claimed":
			group, _ := e.Data["group"].(string)
			stage, _ := e.Data["stage"].(string)
			session, _ := e.Data["session"].(string)
			kind, _ := e.Data["kind"].(string)
			if group == "" || stage == "" {
				continue
			}
			wt.waitClaim(bead, stage, session, e.At)
			visited[bead] = store.AppendVisited(visited[bead], stage)
			if !o.stages[stage] || e.At.Before(o.since) {
				continue
			}
			if o.originator != "" {
				b, ok := beads[bead]
				if !ok || reportOriginator(b, o.operator) != o.originator {
					continue
				}
			}
			c := reportClaim{bead: bead, group: group, stage: stage, session: session, kind: kind, at: e.At, seq: e.Seq}
			claims = append(claims, c)
			if session != "" {
				sessionOf[session] = c
			}
		case "advanced":
			from, _ := e.Data["from"].(string)
			to, _ := e.Data["to"].(string)
			if from == "" || to == "" || from == to {
				continue
			}
			if v, ok := wt.visits[bead+"\x00"+from]; ok {
				wt.end(v, e.At)
			}
			wt.enter(bead, to, e.At)
			visited[bead] = store.AppendVisited(visited[bead], from)
			if store.IsSendBack(visited[bead], from, to) && o.stages[to] {
				if !e.At.Before(o.since) {
					sendBacks[bead+"\x00"+to]++
				}
			} else {
				visited[bead] = store.AppendVisited(visited[bead], to)
			}
			if !e.At.Before(o.since) {
				group, _ := e.Data["group"].(string)
				exits = append(exits, reportExit{bead: bead, from: from, to: to, group: group, at: e.At, seq: e.Seq})
			}
		case store.RoundsReached:
			to, _ := e.Data["to"].(string)
			if to == "" || !o.stages[to] || e.At.Before(o.since) {
				continue
			}
			atLimit[bead+"\x00"+to] = true
		}
	}
	// advancedBy is, per bead and review stage, who moved the bead out of
	// the stage: manual and exec reviewers, whose actors are not agent
	// names. It reads every retained advanced event, whatever --since
	// says.
	advancedBy := map[string]map[string]map[string]bool{}
	for _, e := range evs {
		if e.Kind != "advanced" || e.Bead == nil || e.Actor == "" {
			continue
		}
		from, _ := e.Data["from"].(string)
		if from != "review" && from != "review-security" {
			continue
		}
		bead := *e.Bead
		if advancedBy[bead] == nil {
			advancedBy[bead] = map[string]map[string]bool{}
		}
		if advancedBy[bead][from] == nil {
			advancedBy[bead][from] = map[string]bool{}
		}
		advancedBy[bead][from][e.Actor] = true
	}
	fixes := map[string]beadFixes{}
	for bead, ns := range notes {
		if f := countBeadFixes(bead, ns, advancedBy[bead], o.since); f != (beadFixes{}) {
			fixes[bead] = f
		}
	}
	// Failures are the counted claims' sessions that failed, died, or got
	// stuck: blocked at a prompt, stalled on its first prompt, gave up on
	// it, or stopped on a harness error. Each session counts once,
	// however many such events it has; what the runtime never started
	// (aborted), what was only idle, and what the operator held, do not.
	failed := map[string]int{}
	failedAt := map[string]uint64{}
	for _, e := range evs {
		if !store.IsETAFailedKind(e.Kind) {
			continue
		}
		if e.At.Before(o.since) {
			continue
		}
		session, _ := e.Data["session"].(string)
		c, ok := sessionOf[session]
		if !ok || e.Bead == nil || c.bead != *e.Bead {
			continue
		}
		if _, seen := failedAt[session]; !seen {
			failedAt[session] = e.Seq
			failed[c.group+"\x00"+c.bead]++
		}
	}
	// The store owns this pairing rule so the report and ETA seed agree on
	// failed claims and attempts superseded before an exit.
	builds := map[string][]time.Duration{}
	pairClaims := make([]store.ETABuildClaim, len(claims))
	for i, c := range claims {
		pairClaims[i] = store.ETABuildClaim{Bead: c.bead, Group: c.group, Stage: c.stage, Session: c.session, At: c.at, Seq: c.seq}
	}
	pairExits := make([]store.ETABuildExit, len(exits))
	for i, x := range exits {
		pairExits[i] = store.ETABuildExit{Bead: x.bead, Group: x.group, Stage: x.from, At: x.at, Seq: x.seq}
	}
	pairs := store.PairETABuilds(pairClaims, pairExits, failedAt)
	kindBuilds := map[string][]time.Duration{}
	for xi, pair := range pairs {
		x := exits[xi]
		if !o.stages[x.from] {
			continue
		}
		if pair.Claim < 0 {
			continue
		}
		c := claims[pair.Claim]
		d := x.at.Sub(c.at)
		k := c.group + "\x00" + c.bead
		builds[k] = append(builds[k], d)
		// The same pairing feeds BY HARNESS, so the two tables agree
		// on what a claim cost.
		if c.kind != "" {
			kindBuilds[c.kind] = append(kindBuilds[c.kind], d)
		}
	}
	cells := map[string]*routeCell{}
	for _, c := range claims {
		b, ok := beads[c.bead]
		if !ok || b.Status != "closed" {
			continue
		}
		org := reportOriginator(b, o.operator)
		if o.originator != "" && org != o.originator {
			continue
		}
		var labels []string
		for _, l := range b.Labels {
			if hasReportPrefix(l, o.prefixes) {
				labels = append(labels, l)
			}
		}
		if len(labels) == 0 {
			// A bead with no estimate label has a row of its own,
			// labelled as the table's other empty cells are.
			labels = []string{"-"}
		}
		depot := ""
		if o.byDepot {
			depot = "-"
			if b.Depot != nil {
				depot = *b.Depot
			}
		}
		for _, l := range labels {
			k := c.group + "\x00" + depot + "\x00" + org + "\x00" + l
			ce := cells[k]
			if ce == nil {
				ce = &routeCell{beads: map[string]bool{}, stages: map[string]map[string]bool{}, sessions: map[string][]string{}, atLimit: map[string]bool{}}
				cells[k] = ce
			}
			// A bead claimed twice in one stage counts once; a bead two
			// groups worked counts in each group's cell.
			ce.beads[c.bead] = true
			if ce.stages[c.bead] == nil {
				ce.stages[c.bead] = map[string]bool{}
			}
			ce.stages[c.bead][c.stage] = true
			// Agent claims carry a harness kind and can produce a usage
			// result. Manual and exec claims also have session ids, but
			// never have a count due, so they are not pending.
			if c.kind != "" {
				ce.sessions[c.bead] = append(ce.sessions[c.bead], c.session)
			}
		}
	}
	waitDur, waitHas := wt.durations()
	for k, ce := range cells {
		group := k[:strings.Index(k, "\x00")]
		for bead := range ce.beads {
			ce.failed += failed[group+"\x00"+bead]
			ce.builds = append(ce.builds, builds[group+"\x00"+bead]...)
			var wait time.Duration
			waitOK := false
			for s := range ce.stages[bead] {
				ce.sendBacks += sendBacks[bead+"\x00"+s]
				// A bead a limit sent on counts once, however many
				// redirects it has in the window.
				if atLimit[bead+"\x00"+s] {
					ce.atLimit[bead] = true
				}
				// The bead's wait in the stages the group
				// claimed it in is the cell's sample, summed
				// over its visits; a bead whose entry aged
				// out of the log takes no part.
				if waitHas[bead+"\x00"+s] {
					waitOK = true
					wait += waitDur[bead+"\x00"+s]
				}
			}
			if waitOK {
				ce.waits = append(ce.waits, wait)
			}
			// Tokens cover the counted sessions only; a bead takes
			// part in the median only when every session it names
			// in the cell was counted, so one with a missing or
			// pending session neither drags the median down nor
			// passes for fully counted. A partial reading is a
			// count, though short, and takes part.
			beadTokens, named, counted := 0, 0, 0
			for _, s := range ce.sessions[bead] {
				if s == "" {
					continue
				}
				named++
				u, ok := usageBySession[s]
				switch {
				case !ok:
					ce.pending++
				case u.missing != "":
					ce.uncounted++
				default:
					counted++
					ce.tokens += newTokens(u.usage)
					ce.usage = ce.usage.Add(u.usage)
					beadTokens += newTokens(u.usage)
					if u.partial {
						ce.partial = true
					}
				}
			}
			if named > 0 && counted == named {
				ce.beadMeds = append(ce.beadMeds, beadTokens)
			}
			// Fixes from a review stage count in the cell when the
			// group claimed the bead in a stage the bead reached
			// before that one: a builder cell shows both, a reviewer
			// row reads 0.0 for its own, and security repairs on
			// beads a group passed still show on its row.
			if f, ok := fixes[bead]; ok {
				if claimedBefore(visited[bead], ce.stages[bead], "review") {
					ce.reviewFixes += f.review
				}
				if claimedBefore(visited[bead], ce.stages[bead], "review-security") {
					ce.secFixes += f.sec
				}
			}
		}
	}
	keys := make([]string, 0, len(cells))
	for k := range cells {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		ce := cells[k]
		parts := strings.Split(k, "\x00")
		row := routeReportRow{
			Group:       parts[0],
			Depot:       parts[1],
			Originator:  parts[2],
			Label:       parts[3],
			Beads:       len(ce.beads),
			SentBack:    round1(float64(ce.sendBacks) / float64(len(ce.beads))),
			AtLimit:     len(ce.atLimit),
			ReviewFixed: round1(float64(ce.reviewFixes) / float64(len(ce.beads))),
			SecFixed:    round1(float64(ce.secFixes) / float64(len(ce.beads))),
			Failed:      ce.failed,
			Tokens:      ce.tokens,
			Usage:       ce.usage,
			Partial:     ce.partial,
			Uncounted:   ce.uncounted,
			Pending:     ce.pending,
		}
		if len(ce.waits) > 0 {
			s := limits.Span(store.MedianDuration(ce.waits))
			row.MedianWait = &s
		}
		if len(ce.builds) > 0 {
			s := limits.Span(store.MedianDuration(ce.builds))
			row.MedianBuild = &s
		}
		if len(ce.beadMeds) > 0 {
			m := medianTokens(ce.beadMeds)
			row.TokensPerBead = &m
		}
		rep.Rows = append(rep.Rows, row)
	}
	kindSessions := map[string]int{}
	kindStages := map[string]map[string]bool{}
	kindTokens := map[string]int{}
	kindUsage := map[string]store.Usage{}
	kindPartial := map[string]bool{}
	kindUncounted := map[string]int{}
	kindPending := map[string]int{}
	for _, c := range claims {
		if c.kind == "" {
			continue
		}
		kindSessions[c.kind]++
		if kindStages[c.kind] == nil {
			kindStages[c.kind] = map[string]bool{}
		}
		kindStages[c.kind][c.stage] = true
		// The same join as the cells: the count of the claim's
		// session, or pending while it has none.
		if c.session == "" {
			continue
		}
		u, ok := usageBySession[c.session]
		switch {
		case !ok:
			kindPending[c.kind]++
		case u.missing != "":
			kindUncounted[c.kind]++
		default:
			kindTokens[c.kind] += newTokens(u.usage)
			kindUsage[c.kind] = kindUsage[c.kind].Add(u.usage)
			if u.partial {
				kindPartial[c.kind] = true
			}
		}
	}
	kindKeys := make([]string, 0, len(kindSessions))
	for k := range kindSessions {
		kindKeys = append(kindKeys, k)
	}
	slices.Sort(kindKeys)
	for _, k := range kindKeys {
		stages := make([]string, 0, len(kindStages[k]))
		for s := range kindStages[k] {
			stages = append(stages, s)
		}
		slices.Sort(stages)
		row := routeKindRow{
			Kind: k, Sessions: kindSessions[k], Stages: stages,
			Tokens: kindTokens[k], Usage: kindUsage[k], Partial: kindPartial[k],
			Uncounted: kindUncounted[k], Pending: kindPending[k],
		}
		if ds := kindBuilds[k]; len(ds) > 0 {
			var total time.Duration
			for _, d := range ds {
				total += d
			}
			h := limits.Span(total)
			row.Hours = &h
			m := limits.Span(store.MedianDuration(ds))
			row.Median = &m
		}
		rep.ByKind = append(rep.ByKind, row)
	}
	crewSessions := map[string]int{}
	crewTokens := map[string]int{}
	crewUsage := map[string]store.Usage{}
	crewPartial := map[string]bool{}
	crewUncounted := map[string]int{}
	for _, cu := range crew {
		crewSessions[cu.member]++
		if cu.missing != "" {
			crewUncounted[cu.member]++
			continue
		}
		crewTokens[cu.member] += newTokens(cu.usage)
		crewUsage[cu.member] = crewUsage[cu.member].Add(cu.usage)
		if cu.partial {
			crewPartial[cu.member] = true
		}
	}
	for _, m := range slices.Sorted(maps.Keys(crewSessions)) {
		rep.Crew = append(rep.Crew, routeCrewRow{
			Member: m, Sessions: crewSessions[m],
			Tokens: crewTokens[m], Usage: crewUsage[m], Partial: crewPartial[m],
			Uncounted: crewUncounted[m],
		})
	}
	rep.best = bestBuilders(cells)
	return rep
}

func hasReportPrefix(label string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(label, p) {
			return true
		}
	}
	return false
}

// normalizeReportPrefixes trims the prefixes and spells each with its colon,
// so --prefix difficulty matches difficulty:2.
func normalizeReportPrefixes(prefixes []string) []string {
	out := make([]string, 0, len(prefixes))
	for _, p := range prefixes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !strings.HasSuffix(p, ":") {
			p += ":"
		}
		out = append(out, p)
	}
	return out
}

// reportStages is the stages the report reads: the one --stage names, or
// every stage a route sends to a group that starts agents (a herdr group).
func reportStages(ctx context.Context, st store.API, stage string) (map[string]bool, error) {
	if stage != "" {
		return map[string]bool{stage: true}, nil
	}
	gs, err := st.Groups(ctx)
	if err != nil {
		return nil, err
	}
	herdr := map[string]bool{}
	for _, g := range gs {
		if g.Runner == "herdr" {
			herdr[g.Name] = true
		}
	}
	rs, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	stages := map[string]bool{}
	for _, r := range rs {
		if herdr[r.Group] {
			stages[r.Stage] = true
		}
	}
	return stages, nil
}

// routeNowLines says, per estimate label in the report, where the label
// goes today: the yard default a bead with the label and no depot matches
// (the defaults alone, as a bead with no depot reads them), probed in
// stage new, where the difficulty-to-model rule lives, and the depots
// whose own routes set that default aside. Labels arrive sorted; "-" has
// no line, since it names no label. Current routing only: --stage,
// --since and --originator do not reach it.
func routeNowLines(routes []store.Route, labels []string, operator string) []routeNow {
	now := []routeNow{}
	asideBy := map[string][]string{}
	depots := map[string]bool{}
	for _, r := range routes {
		if r.Depot != nil {
			depots[*r.Depot] = true
		}
	}
	// Only a depot with own routes sets a default aside, so only those
	// are asked.
	for _, d := range slices.Sorted(maps.Keys(depots)) {
		for _, s := range store.SetAsideRoutes(routes, d, operator) {
			asideBy[s.Route.ID] = append(asideBy[s.Route.ID], d)
		}
	}
	for _, l := range labels {
		probe := store.Bead{Stage: "new", Labels: []string{l}, Priority: store.DefaultPriority}
		n := routeNow{Label: l, SetAside: []string{}}
		if r := store.MatchRoute(routes, probe, operator, nil); r != nil {
			n.Route = &routeNowHop{ID: r.ID, Groups: r.GroupList()}
			n.SetAside = append(n.SetAside, asideBy[r.ID]...)
		}
		slices.Sort(n.SetAside)
		now = append(now, n)
	}
	return now
}

// collectRouteReport reads the whole retained log, oldest first, the
// beads, and the notes of every closed bead, and summarizes them. It
// writes nothing.
func collectRouteReport(ctx context.Context, st store.API, o routeReportOpts) (*routeReport, error) {
	var evs []store.Event
	var after uint64
	for {
		page, err := st.EventsSince(ctx, after, 1000)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		evs = append(evs, page...)
		after = page[len(page)-1].Seq
		if len(page) < 1000 {
			break
		}
	}
	bs, err := st.ListBeads(ctx, store.BeadFilter{All: true})
	if err != nil {
		return nil, err
	}
	beads := make(map[string]store.Bead, len(bs))
	for _, b := range bs {
		beads[b.ID] = b
	}
	// The fix columns read the notes, which stay when the log prunes, so
	// they are the record of what the review stages repaired.
	notes := map[string][]store.Note{}
	for _, b := range bs {
		if b.Status != "closed" {
			continue
		}
		ns, err := st.Notes(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		if len(ns) > 0 {
			notes[b.ID] = ns
		}
	}
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	o.operator = settings[store.OperatorSetting]
	rep := summarizeRouteEvents(evs, beads, o, notes)
	routes, err := st.Routes(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var labels []string
	for _, b := range bs {
		if b.Status != "closed" {
			continue
		}
		for _, label := range b.Labels {
			if (!strings.HasPrefix(label, "difficulty:") && !strings.HasPrefix(label, "size:")) ||
				!hasReportPrefix(label, o.prefixes) || seen[label] {
				continue
			}
			seen[label] = true
			labels = append(labels, label)
		}
	}
	slices.Sort(labels)
	rep.Now = routeNowLines(routes, labels, o.operator)
	return rep, nil
}

func (a *app) routeReportCmd() *cobra.Command {
	var prefixes []string
	var stage, since, originator, by string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "What the beads of each estimate cost, per group",
		Long: `Read the event log and print, for each group, each originator and each estimate label, what the beads cost.

BEADS is the closed beads the group claimed in the stage. SENT BACK is how
often per bead a later stage moved one back to the stage. AT LIMIT is how
many of them a round limit sent to its then stage from one the group
claimed the bead in; like BEADS it counts a bead once the bead closes,
so a bead still waiting where the limit sent it shows in that stage's
listing meanwhile. REVIEW FIXED is
how often per bead review fixed one on its branch instead of sending it
back, SEC FIXED how often the security review did. FAILED is the
group's sessions for them that failed, died, or got stuck: blocked at a
prompt, stalled on its first prompt, gave up on it, or stopped on a
harness error; each session counts once. WAIT is how long a bead was
ready and unclaimed: from entering the stage, from its last blocker
closing, from unheld, and from the end of a session that left it in the
stage, until claimed, held, or a new blocker; a bead claimed twice in
one visit, because its first session failed, adds both waits. MEDIAN
BUILD is from the claim to the move that left the stage. A bead two
groups worked in one stage counts for each, under the user it is for; a
bead with no estimate label counts under "-"; a bead that never closed
counts nowhere. ORIGINATOR is that user, beside the group;
--originator reads one person's beads. --by depot adds a DEPOT column
beside GROUP, one row per depot, so two pools of one worker are compared
within one depot.

The fix columns read the beads' notes, which stay when sessions and events
prune: a commit a review note names counts when no earlier note on the
bead named it. A note counts for review when its author is the review
session's agent or moved the bead out of review, and likewise for the
security review. A group's cell shows a stage's fixes when the group
claimed the bead in a stage the bead reached before that one, so a
reviewer row reads 0.0 for its own stage. Under the first table, one line
per difficulty names the builder with the lowest sent back + repaired
per bead; a group qualifies by claiming ten beads at least outside the
stages where no builder work happens (review, review-security, critique,
shape), so reviewer and shaping groups never win, and a label with no
winner says too few beads to compare. --by depot splits the rows but
never the best lines. Under the best lines, one now line per estimate
label names the yard default a bead with the label matches today and
the depots whose own routes set it aside; it is current routing, so
--stage, --since and --originator do not reach it.

TOKENS/BEAD is the median, over the cell's beads whose sessions are all
counted or partial, of the new tokens the group's sessions spent on each
bead: fresh input, cache writes and output, leaving out cache reads, which would drown the
difference between beads. TOKENS is the total over the cell's counted
sessions. UNCOUNTED is the sessions whose count came back missing,
PENDING the agent sessions claimed with no result yet. Counts are read from
the session_usage events through the claims, so --originator, --since
and --stage apply to them as to the claims. A TOKENS cell with a
partial, missing or pending session takes ≥: the total is then a lower
bound. TOKENS/BEAD reads only beads all of whose sessions in the cell
were counted or partial, and - where no bead qualifies.

BY HARNESS prints one row per harness kind the groups ran: SESSIONS is
its claims in the window, HOURS their claim-to-exit time summed and
MEDIAN the middle of it, TOKENS the new tokens over its counted
sessions, UNCOUNTED and PENDING as in the first table, STAGES the
stages they ran. It counts every claim in the selected scope, open
beads included, with the same pairing as the first table, so the two
agree; --originator, --since and --stage apply to it as to the first.

CREW prints one row per crew member with crew_usage in the window:
SESSIONS its sessions, TOKENS their new tokens, UNCOUNTED the ones
missing. Crew counts belong to no bead; --since applies, --stage and
--originator do not.

--json carries, per row, the depot (empty unless --by depot), median_wait
(null where the text shows -), tokens (and tokens_per_bead in the first
table, null where the text shows -), usage (input, fresh_input, output,
cache_read and cache_write summed over the counted sessions), partial
(any session read partial), uncounted, and pending, which crew rows
have not, and beside the rows the now lines (label, route id and groups,
depots set aside; route null where no default matches). The
first line says from when the log still holds events: the report reads
only what retention kept. It changes nothing.

The pairing is the estimate cells' too: each stage exit ends the latest
claim of its bead in its stage that no earlier exit ended, and a failed
claim never pairs. The yard keeps those durations per group, estimate
label and stage as stages end, and a bead's ETA is arithmetic over
them: the eta line of yardr bead show (docs/line.md).`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			o := routeReportOpts{prefixes: normalizeReportPrefixes(prefixes), originator: originator}
			switch by {
			case "":
			case "depot":
				o.byDepot = true
			default:
				return fmt.Errorf("--by: unknown %q, only depot splits a row", by)
			}
			if len(o.prefixes) == 0 {
				return fmt.Errorf("--prefix: give at least one label prefix")
			}
			if since != "" {
				cutoff, err := parseSince(since)
				if err != nil {
					return fmt.Errorf("--since: %w", err)
				}
				o.since = cutoff
			}
			if o.stages, err = reportStages(c.Context(), st, stage); err != nil {
				return err
			}
			rep, err := collectRouteReport(c.Context(), st, o)
			if err != nil {
				return err
			}
			return a.out(c.OutOrStdout(), rep, func(w io.Writer) {
				if rep.From == nil {
					fmt.Fprintln(w, "the event log holds no events")
					return
				}
				fmt.Fprintf(w, "events since %s\n", rep.From.Local().Format("2006-01-02 15:04"))
				rows := make([][]string, len(rep.Rows))
				for i, r := range rep.Rows {
					wait, median := "-", "-"
					if r.MedianWait != nil {
						wait = *r.MedianWait
					}
					if r.MedianBuild != nil {
						median = *r.MedianBuild
					}
					perBead := "-"
					if r.TokensPerBead != nil {
						perBead = shortTokens(*r.TokensPerBead)
					}
					row := []string{r.Group}
					if o.byDepot {
						row = append(row, r.Depot)
					}
					row = append(row, r.Originator, r.Label, strconv.Itoa(r.Beads),
						strconv.FormatFloat(r.SentBack, 'f', 1, 64),
						strconv.Itoa(r.AtLimit),
						strconv.FormatFloat(r.ReviewFixed, 'f', 1, 64),
						strconv.FormatFloat(r.SecFixed, 'f', 1, 64),
						strconv.Itoa(r.Failed), wait, median, perBead,
						lowerBound(shortTokens(r.Tokens), r.Partial, r.Uncounted, r.Pending),
						strconv.Itoa(r.Uncounted), strconv.Itoa(r.Pending))
					rows[i] = row
				}
				header := "GROUP\tORIGINATOR\tLABEL\tBEADS\tSENT BACK\tAT LIMIT\tREVIEW FIXED\tSEC FIXED\tFAILED\tWAIT\tMEDIAN BUILD\tTOKENS/BEAD\tTOKENS\tUNCOUNTED\tPENDING"
				if o.byDepot {
					header = "GROUP\tDEPOT\tORIGINATOR\tLABEL\tBEADS\tSENT BACK\tAT LIMIT\tREVIEW FIXED\tSEC FIXED\tFAILED\tWAIT\tMEDIAN BUILD\tTOKENS/BEAD\tTOKENS\tUNCOUNTED\tPENDING"
				}
				table(w, header, rows)
				for _, b := range rep.best {
					if b.tooFew {
						fmt.Fprintf(w, "best %s: too few beads to compare\n", b.label)
						continue
					}
					fmt.Fprintf(w, "best %s: %s at %s sent back + repaired per bead (%d beads)\n",
						b.label, b.group, strconv.FormatFloat(b.perBead, 'f', 1, 64), b.beads)
				}
				for _, n := range rep.Now {
					if n.Route == nil {
						fmt.Fprintf(w, "now %s: no default route\n", n.Label)
						continue
					}
					fmt.Fprintf(w, "now %s: default %s -> %s", n.Label, n.Route.ID, n.Route.Groups)
					if len(n.SetAside) > 0 {
						fmt.Fprintf(w, " (own routes in %s)", strings.Join(n.SetAside, ", "))
					}
					fmt.Fprintln(w)
				}
				fmt.Fprintln(w, "BY HARNESS")
				kinds := make([][]string, len(rep.ByKind))
				for i, r := range rep.ByKind {
					hours, median := "-", "-"
					if r.Hours != nil {
						hours = *r.Hours
					}
					if r.Median != nil {
						median = *r.Median
					}
					stages := strings.Join(r.Stages, ", ")
					if stages == "" {
						stages = "-"
					}
					kinds[i] = []string{r.Kind, strconv.Itoa(r.Sessions), hours, median,
						lowerBound(shortTokens(r.Tokens), r.Partial, r.Uncounted, r.Pending),
						strconv.Itoa(r.Uncounted), strconv.Itoa(r.Pending), stages}
				}
				table(w, "KIND\tSESSIONS\tHOURS\tMEDIAN\tTOKENS\tUNCOUNTED\tPENDING\tSTAGES", kinds)
				fmt.Fprintln(w, "CREW")
				members := make([][]string, len(rep.Crew))
				for i, r := range rep.Crew {
					members[i] = []string{r.Member, strconv.Itoa(r.Sessions),
						lowerBound(shortTokens(r.Tokens), r.Partial, r.Uncounted, 0),
						strconv.Itoa(r.Uncounted)}
				}
				table(w, "MEMBER\tSESSIONS\tTOKENS\tUNCOUNTED", members)
			})
		},
	}
	cmd.Flags().StringSliceVar(&prefixes, "prefix", []string{"difficulty:", "size:"}, "label prefixes to report, e.g. difficulty,size")
	cmd.Flags().StringVar(&by, "by", "", "split each group row: depot (default: one row per group)")
	cmd.Flags().StringVar(&stage, "stage", "", "only this stage (default: every stage a herdr group works)")
	cmd.Flags().StringVar(&since, "since", "", "only what is that new: 7d, 12h, 30m or RFC3339")
	cmd.Flags().StringVar(&originator, "originator", "", "only this originator's beads (default: every originator's)")
	return cmd
}
