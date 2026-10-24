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
	"yardr.dev/yardr/internal/agents"
	"yardr.dev/yardr/internal/limits"
	"yardr.dev/yardr/internal/runner"
	"yardr.dev/yardr/internal/store"
)

// routeReportOpts is what `yardr route report` reads the event log for: the
// label prefixes that make a row, the stages its claims come from, the
// earliest event it counts, and whose beads it keeps. A zero since counts
// everything retained; an empty originator keeps every originator's.
type routeReportOpts struct {
	prefixes []string
	stages   map[string]bool
	since    time.Time
	// originator keeps one person's beads; operator is what an empty
	// originator reads as.
	originator string
	operator   string
}

// routeReportRow is one GROUP/ORIGINATOR/LABEL cell of the report: the
// closed beads one group claimed in the stages for one user, what they
// cost. SentBack is send-backs per bead, AtLimit the beads a round limit
// sent to its then stage from one the group claimed them in, ReviewFixed
// review's forward fixes per bead and SecFixed the security review's;
// MedianBuild is how long a claim took to leave the stage, nil when no
// counted claim did (its exit aged out of the log, or the bead closed
// without leaving the stage). TokensPerBead is the median new tokens per
// bead whose sessions are all counted or partial, nil when no bead
// qualifies; Tokens the new tokens over the cell's counted sessions,
// Usage the five counts behind it; Partial says one session read partial,
// Uncounted how many came back
// missing, Pending how many have no result yet.
type routeReportRow struct {
	Group         string       `json:"group"`
	Originator    string       `json:"originator"`
	Label         string       `json:"label"`
	Beads         int          `json:"beads"`
	SentBack      float64      `json:"sent_back"`
	AtLimit       int          `json:"at_limit"`
	ReviewFixed   float64      `json:"review_fixed"`
	SecFixed      float64      `json:"sec_fixed"`
	Failed        int          `json:"failed"`
	MedianBuild   *string      `json:"median_build"`
	TokensPerBead *int         `json:"tokens_per_bead"`
	Tokens        int          `json:"tokens"`
	Usage         agents.Usage `json:"usage"`
	Partial       bool         `json:"partial"`
	Uncounted     int          `json:"uncounted"`
	Pending       int          `json:"pending"`
}

// routeKindRow is one BY HARNESS row: the sessions one harness kind ran in
// the window. Sessions counts its claims, Hours their claim-to-exit time
// summed and Median the middle of it, nil when no counted claim exited;
// Stages is the stages they ran, sorted. Tokens, Usage, Partial,
// Uncounted and Pending are the kind's counts, as the cell's are.
type routeKindRow struct {
	Kind      string       `json:"kind"`
	Sessions  int          `json:"sessions"`
	Hours     *string      `json:"hours"`
	Median    *string      `json:"median"`
	Stages    []string     `json:"stages"`
	Tokens    int          `json:"tokens"`
	Usage     agents.Usage `json:"usage"`
	Partial   bool         `json:"partial"`
	Uncounted int          `json:"uncounted"`
	Pending   int          `json:"pending"`
}

// routeCrewRow is one CREW row: one member's counted crew sessions in the
// window. Sessions counts its crew_usage events; Tokens, Usage, Partial
// and Uncounted are as the cell's. There is no pending: the log shows no
// crew session until its count is recorded.
type routeCrewRow struct {
	Member    string       `json:"member"`
	Sessions  int          `json:"sessions"`
	Tokens    int          `json:"tokens"`
	Usage     agents.Usage `json:"usage"`
	Partial   bool         `json:"partial"`
	Uncounted int          `json:"uncounted"`
}

// routeReport is the report: From is when the oldest retained event was
// logged (nil when the log holds none), Rows one row per group and label,
// ByKind one row per harness kind, Crew one row per crew member, best the
// best builder per difficulty, text only.
type routeReport struct {
	From   *time.Time       `json:"from"`
	Rows   []routeReportRow `json:"rows"`
	ByKind []routeKindRow   `json:"by_kind"`
	Crew   []routeCrewRow   `json:"crew"`
	best   []routeBest
}

// routeBest is one best line: the group with the lowest sent back +
// repaired per bead for a difficulty label, over the report's own rows.
type routeBest struct {
	label   string
	group   string
	perBead float64
	beads   int
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
	usage   agents.Usage
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
	u.usage = agents.Usage{
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
func newTokens(u agents.Usage) int {
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

// reviewStages are the stages where review happens: the default flow's
// two, and design's critique, which is review work under another name
// (packs/design/flows/design/critique.md, worked by the reviewers). A
// group qualifies for a best line only by claiming a bead elsewhere.
var reviewStages = []string{"review", "review-security", "critique"}

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
	tokens      int
	usage       agents.Usage
	partial     bool
	uncounted   int
	pending     int
	beadMeds    []int // new tokens per bead with counted or partial sessions
}

// bestBuilders names, per difficulty label in the cells, the group with
// the lowest sent back + repaired per bead: the send-back and fix counts
// summed over the group's cells for the label, across originators, over
// their beads. Only a group that claimed one of those beads outside the
// review stages qualifies, so reviewer rows never win; ties go to more
// beads, then to the name. Over the report's own rows, whatever --since
// says.
func bestBuilders(cells map[string]*routeCell) []routeBest {
	type acc struct {
		beads     int
		total     int
		qualified bool
	}
	byLabel := map[string]map[string]*acc{}
	for k, ce := range cells {
		group, rest, _ := strings.Cut(k, "\x00")
		_, label, _ := strings.Cut(rest, "\x00")
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
				if !slices.Contains(reviewStages, s) {
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
			if !a.qualified || a.beads == 0 {
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
		}
	}
	slices.SortFunc(out, func(a, b routeBest) int { return strings.Compare(a.label, b.label) })
	return out
}

// summarizeRouteEvents builds the report from the events read oldest first.
// Beads come from the bead records, not the log: only closed beads count,
// under the labels they have now and the user each is for, or under "-"
// when they have none of the prefixes. A bead a group never claimed is in
// no cell; one two groups worked in one stage is in each group's. Notes
// are the bead's notes by handle, in seq order: what the fix columns
// count. BY HARNESS counts every claim with a kind under the same stage,
// time and originator filters, open beads included: sessions ran, not
// beads closed. Tokens come from session_usage events through the claims:
// a count belongs to the cells of its session's claim, whatever filters
// the claim passed, and a claim with no count yet is pending; CREW reads
// crew_usage, which belongs to no bead and keeps only the time filter.
func summarizeRouteEvents(evs []store.Event, beads map[string]store.Bead, o routeReportOpts, notes map[string][]store.Note) *routeReport {
	rep := &routeReport{Rows: []routeReportRow{}, ByKind: []routeKindRow{}, Crew: []routeCrewRow{}}
	if len(evs) > 0 {
		from := evs[0].At
		rep.From = &from
	}
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
		switch e.Kind {
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
		for _, l := range labels {
			k := c.group + "\x00" + org + "\x00" + l
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
	for k, ce := range cells {
		group := k[:strings.Index(k, "\x00")]
		for bead := range ce.beads {
			ce.failed += failed[group+"\x00"+bead]
			ce.builds = append(ce.builds, builds[group+"\x00"+bead]...)
			for s := range ce.stages[bead] {
				ce.sendBacks += sendBacks[bead+"\x00"+s]
				// A bead a limit sent on counts once, however many
				// redirects it has in the window.
				if atLimit[bead+"\x00"+s] {
					ce.atLimit[bead] = true
				}
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
		group, rest, _ := strings.Cut(k, "\x00")
		org, label, _ := strings.Cut(rest, "\x00")
		row := routeReportRow{
			Group:       group,
			Originator:  org,
			Label:       label,
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
	kindUsage := map[string]agents.Usage{}
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
	crewUsage := map[string]agents.Usage{}
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
	return summarizeRouteEvents(evs, beads, o, notes), nil
}

func (a *app) routeReportCmd() *cobra.Command {
	var prefixes []string
	var stage, since, originator string
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
harness error; each session counts once. MEDIAN BUILD is from the claim
to the move that left the stage. A bead two groups worked in one stage
counts for each, under the user it is for; a bead with no estimate label
counts under "-"; a bead that never closed counts nowhere. ORIGINATOR is
that user, beside the group; --originator reads one person's beads.

The fix columns read the beads' notes, which stay when sessions and events
prune: a commit a review note names counts when no earlier note on the
bead named it. A note counts for review when its author is the review
session's agent or moved the bead out of review, and likewise for the
security review. A group's cell shows a stage's fixes when the group
claimed the bead in a stage the bead reached before that one, so a
reviewer row reads 0.0 for its own stage. Under the first table, one line
per difficulty names the builder with the lowest sent back + repaired
per bead; a group qualifies by claiming a bead outside the review
stages (review, review-security, critique), so reviewer groups never
win.

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

--json carries, per row, tokens (and tokens_per_bead in the first
table, null where the text shows -), usage (input, fresh_input, output,
cache_read and cache_write summed over the counted sessions), partial
(any session read partial), uncounted, and pending, which crew rows
have not. The
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
					median := "-"
					if r.MedianBuild != nil {
						median = *r.MedianBuild
					}
					perBead := "-"
					if r.TokensPerBead != nil {
						perBead = shortTokens(*r.TokensPerBead)
					}
					rows[i] = []string{r.Group, r.Originator, r.Label, strconv.Itoa(r.Beads),
						strconv.FormatFloat(r.SentBack, 'f', 1, 64),
						strconv.Itoa(r.AtLimit),
						strconv.FormatFloat(r.ReviewFixed, 'f', 1, 64),
						strconv.FormatFloat(r.SecFixed, 'f', 1, 64),
						strconv.Itoa(r.Failed), median, perBead,
						lowerBound(shortTokens(r.Tokens), r.Partial, r.Uncounted, r.Pending),
						strconv.Itoa(r.Uncounted), strconv.Itoa(r.Pending)}
				}
				table(w, "GROUP\tORIGINATOR\tLABEL\tBEADS\tSENT BACK\tAT LIMIT\tREVIEW FIXED\tSEC FIXED\tFAILED\tMEDIAN BUILD\tTOKENS/BEAD\tTOKENS\tUNCOUNTED\tPENDING", rows)
				for _, b := range rep.best {
					fmt.Fprintf(w, "best %s: %s at %s sent back + repaired per bead (%d beads)\n",
						b.label, b.group, strconv.FormatFloat(b.perBead, 'f', 1, 64), b.beads)
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
	cmd.Flags().StringVar(&stage, "stage", "", "only this stage (default: every stage a herdr group works)")
	cmd.Flags().StringVar(&since, "since", "", "only what is that new: 7d, 12h, 30m or RFC3339")
	cmd.Flags().StringVar(&originator, "originator", "", "only this originator's beads (default: every originator's)")
	return cmd
}
