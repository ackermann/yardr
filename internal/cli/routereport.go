package cli

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/limits"
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
// cost. SentBack is send-backs per bead; MedianBuild is how long a claim
// took to leave the stage, nil when no counted claim did (its exit aged
// out of the log, or the bead closed without leaving the stage).
type routeReportRow struct {
	Group       string  `json:"group"`
	Originator  string  `json:"originator"`
	Label       string  `json:"label"`
	Beads       int     `json:"beads"`
	SentBack    float64 `json:"sent_back"`
	Failed      int     `json:"failed"`
	MedianBuild *string `json:"median_build"`
}

// routeKindRow is one BY HARNESS row: the sessions one harness kind ran in
// the window. Sessions counts its claims, Hours their claim-to-exit time
// summed and Median the middle of it, nil when no counted claim exited;
// Stages is the stages they ran, sorted.
type routeKindRow struct {
	Kind     string   `json:"kind"`
	Sessions int      `json:"sessions"`
	Hours    *string  `json:"hours"`
	Median   *string  `json:"median"`
	Stages   []string `json:"stages"`
}

// routeReport is the report: From is when the oldest retained event was
// logged (nil when the log holds none), Rows one row per group and label,
// ByKind one row per harness kind.
type routeReport struct {
	From   *time.Time       `json:"from"`
	Rows   []routeReportRow `json:"rows"`
	ByKind []routeKindRow   `json:"by_kind"`
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

// reportOriginator resolves a bead's stored originator as it is read in the
// report. Older or incomplete records with none belong to the operator.
func reportOriginator(b store.Bead, operator string) string {
	if b.Originator != "" {
		return b.Originator
	}
	return operator
}

// summarizeRouteEvents builds the report from the events read oldest first.
// Beads come from the bead records, not the log: only closed beads count,
// under the labels they have now and the user each is for, or under "-"
// when they have none of the prefixes. A bead a group never claimed is in
// no cell; one two groups worked in one stage is in each group's. BY
// HARNESS counts every claim with a kind under the same stage, time and
// originator filters, open beads included: sessions ran, not beads closed.
func summarizeRouteEvents(evs []store.Event, beads map[string]store.Bead, o routeReportOpts) *routeReport {
	rep := &routeReport{Rows: []routeReportRow{}, ByKind: []routeKindRow{}}
	if len(evs) > 0 {
		from := evs[0].At
		rep.From = &from
	}
	var claims []reportClaim
	var exits []reportExit
	// sendBacks counts, per bead and stage, the moves back into the stage.
	sendBacks := map[string]int{}
	// visited is the stages a bead has reached, in the order it first
	// reached them: what the send-back rule (store.IsSendBack) reads.
	visited := map[string][]string{}
	sessionOf := map[string]reportClaim{}
	for _, e := range evs {
		if e.Bead == nil {
			continue
		}
		bead := *e.Bead
		switch e.Kind {
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
	type cell struct {
		beads     map[string]bool
		stages    map[string]map[string]bool // bead -> stages claimed by this group
		sendBacks int
		failed    int
		builds    []time.Duration
	}
	cells := map[string]*cell{}
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
				ce = &cell{beads: map[string]bool{}, stages: map[string]map[string]bool{}}
				cells[k] = ce
			}
			// A bead claimed twice in one stage counts once; a bead two
			// groups worked counts in each group's cell.
			ce.beads[c.bead] = true
			if ce.stages[c.bead] == nil {
				ce.stages[c.bead] = map[string]bool{}
			}
			ce.stages[c.bead][c.stage] = true
		}
	}
	for k, ce := range cells {
		group := k[:strings.Index(k, "\x00")]
		for bead := range ce.beads {
			ce.failed += failed[group+"\x00"+bead]
			ce.builds = append(ce.builds, builds[group+"\x00"+bead]...)
			for s := range ce.stages[bead] {
				ce.sendBacks += sendBacks[bead+"\x00"+s]
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
			Group:      group,
			Originator: org,
			Label:      label,
			Beads:      len(ce.beads),
			SentBack:   math.Round(float64(ce.sendBacks)/float64(len(ce.beads))*10) / 10,
			Failed:     ce.failed,
		}
		if len(ce.builds) > 0 {
			s := limits.Span(store.MedianDuration(ce.builds))
			row.MedianBuild = &s
		}
		rep.Rows = append(rep.Rows, row)
	}
	kindSessions := map[string]int{}
	kindStages := map[string]map[string]bool{}
	for _, c := range claims {
		if c.kind == "" {
			continue
		}
		kindSessions[c.kind]++
		if kindStages[c.kind] == nil {
			kindStages[c.kind] = map[string]bool{}
		}
		kindStages[c.kind][c.stage] = true
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
		row := routeKindRow{Kind: k, Sessions: kindSessions[k], Stages: stages}
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

// collectRouteReport reads the whole retained log, oldest first, and the
// beads, and summarizes them. It writes nothing.
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
	settings, err := st.Settings(ctx)
	if err != nil {
		return nil, err
	}
	o.operator = settings[store.OperatorSetting]
	return summarizeRouteEvents(evs, beads, o), nil
}

func (a *app) routeReportCmd() *cobra.Command {
	var prefixes []string
	var stage, since, originator string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "What the beads of each estimate cost, per group",
		Long: `Read the event log and print, for each group, each originator and each estimate label, what the beads cost.

BEADS is the closed beads the group claimed in the stage. SENT BACK is how
often per bead a later stage moved one back to the stage. FAILED is the
group's sessions for them that failed, died, or got stuck: blocked at a
prompt, stalled on its first prompt, gave up on it, or stopped on a
harness error; each session counts once. MEDIAN BUILD is from the claim
to the move that left the stage. A bead two groups worked in one stage
counts for each, under the user it is for; a bead with no estimate label
counts under "-"; a bead that never closed counts nowhere. ORIGINATOR is
that user, beside the group; --originator reads one person's beads.

BY HARNESS prints one row per harness kind the groups ran: SESSIONS is
its claims in the window, HOURS their claim-to-exit time summed and
MEDIAN the middle of it, STAGES the stages they ran. It counts every
claim in the selected scope, open beads included, with the same pairing
as the first table, so the two agree; --originator, --since and --stage
apply to it as to the first. The
first line says from when the log still holds events: the report reads
only what retention kept. It changes nothing.`,
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
			return a.out(rep, func() {
				if rep.From == nil {
					fmt.Println("the event log holds no events")
					return
				}
				fmt.Printf("events since %s\n", rep.From.Local().Format("2006-01-02 15:04"))
				rows := make([][]string, len(rep.Rows))
				for i, r := range rep.Rows {
					median := "-"
					if r.MedianBuild != nil {
						median = *r.MedianBuild
					}
					rows[i] = []string{r.Group, r.Originator, r.Label, strconv.Itoa(r.Beads),
						strconv.FormatFloat(r.SentBack, 'f', 1, 64), strconv.Itoa(r.Failed), median}
				}
				table("GROUP\tORIGINATOR\tLABEL\tBEADS\tSENT BACK\tFAILED\tMEDIAN BUILD", rows)
				fmt.Println("BY HARNESS")
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
					kinds[i] = []string{r.Kind, strconv.Itoa(r.Sessions), hours, median, stages}
				}
				table("KIND\tSESSIONS\tHOURS\tMEDIAN\tSTAGES", kinds)
			})
		},
	}
	cmd.Flags().StringSliceVar(&prefixes, "prefix", []string{"difficulty:", "size:"}, "label prefixes to report, e.g. difficulty,size")
	cmd.Flags().StringVar(&stage, "stage", "", "only this stage (default: every stage a herdr group works)")
	cmd.Flags().StringVar(&since, "since", "", "only what is that new: 7d, 12h, 30m or RFC3339")
	cmd.Flags().StringVar(&originator, "originator", "", "only this originator's beads (default: every originator's)")
	return cmd
}
