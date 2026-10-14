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
// label prefixes that make a row, the stages its claims come from, and the
// earliest event it counts. A zero since counts everything retained.
type routeReportOpts struct {
	prefixes []string
	stages   map[string]bool
	since    time.Time
}

// routeReportRow is one GROUP/LABEL cell of the report: the closed beads one
// group claimed in the stages, what they cost. SentBack is send-backs per
// bead; MedianBuild is how long a claim took to leave the stage, nil when
// no counted claim did (its exit aged out of the log, or the bead closed
// without leaving the stage).
type routeReportRow struct {
	Group       string  `json:"group"`
	Label       string  `json:"label"`
	Beads       int     `json:"beads"`
	SentBack    float64 `json:"sent_back"`
	Failed      int     `json:"failed"`
	MedianBuild *string `json:"median_build"`
}

// routeReport is the report: From is when the oldest retained event was
// logged (nil when the log holds none), Rows one row per group and label.
type routeReport struct {
	From *time.Time       `json:"from"`
	Rows []routeReportRow `json:"rows"`
}

// reportClaim is one claimed event the report counts: who held which bead in
// which stage, and the session it held it with.
type reportClaim struct {
	bead    string
	group   string
	stage   string
	session string
	at      time.Time
}

// reportExit is one advanced event that left a stage (from != to).
type reportExit struct {
	bead  string
	from  string
	to    string
	group string
	at    time.Time
}

// summarizeRouteEvents builds the report from the events read oldest first.
// Beads come from the bead records, not the log: only closed beads count,
// under the labels they have now, or under "-" when they have none of the
// prefixes. A bead a group never claimed is in no cell; one two groups
// worked in one stage is in each group's.
func summarizeRouteEvents(evs []store.Event, beads map[string]store.Bead, o routeReportOpts) *routeReport {
	rep := &routeReport{Rows: []routeReportRow{}}
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
			if group == "" || stage == "" {
				continue
			}
			visited[bead] = store.AppendVisited(visited[bead], stage)
			if !o.stages[stage] || e.At.Before(o.since) {
				continue
			}
			c := reportClaim{bead: bead, group: group, stage: stage, session: session, at: e.At}
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
				exits = append(exits, reportExit{bead: bead, from: from, to: to, group: group, at: e.At})
			}
		}
	}
	// Failures are the counted claims' sessions that failed, died, or got
	// stuck: blocked at a prompt, stalled on its first prompt, gave up on
	// it, or stopped on a harness error. Each session counts once,
	// however many such events it has; what the runtime never started
	// (aborted), what was only idle, and what the operator held, do not.
	failed := map[string]int{}
	seenFailed := map[string]bool{}
	for _, e := range evs {
		switch e.Kind {
		case "session_died", "session_failed",
			"session_blocked", "session_stalled",
			"session_prompt_gave_up", "session_harness_error":
		default:
			continue
		}
		if e.At.Before(o.since) {
			continue
		}
		session, _ := e.Data["session"].(string)
		c, ok := sessionOf[session]
		if !ok || seenFailed[session] {
			continue
		}
		seenFailed[session] = true
		failed[c.group+"\x00"+c.bead]++
	}
	// Each exit ends the latest claim of its bead in its stage that no
	// earlier exit ended: a claim a failure cut short has no build, and
	// the reclaim's does.
	builds := map[string][]time.Duration{}
	paired := make([]bool, len(claims))
	byBeadStage := map[string][]int{}
	for i, c := range claims {
		k := c.bead + "\x00" + c.stage
		byBeadStage[k] = append(byBeadStage[k], i)
	}
	for _, x := range exits {
		if !o.stages[x.from] {
			continue
		}
		pick := -1
		for _, i := range byBeadStage[x.bead+"\x00"+x.from] {
			if claims[i].session != "" && seenFailed[claims[i].session] {
				continue
			}
			if !paired[i] && claims[i].group == x.group && !claims[i].at.After(x.at) {
				pick = i
			}
		}
		if pick < 0 {
			continue
		}
		paired[pick] = true
		c := claims[pick]
		k := c.group + "\x00" + c.bead
		builds[k] = append(builds[k], x.at.Sub(c.at))
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
			k := c.group + "\x00" + l
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
		group, label, _ := strings.Cut(k, "\x00")
		row := routeReportRow{
			Group:    group,
			Label:    label,
			Beads:    len(ce.beads),
			SentBack: math.Round(float64(ce.sendBacks)/float64(len(ce.beads))*10) / 10,
			Failed:   ce.failed,
		}
		if len(ce.builds) > 0 {
			s := limits.Span(store.MedianDuration(ce.builds))
			row.MedianBuild = &s
		}
		rep.Rows = append(rep.Rows, row)
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
	return summarizeRouteEvents(evs, beads, o), nil
}

func (a *app) routeReportCmd() *cobra.Command {
	var prefixes []string
	var stage, since string
	cmd := &cobra.Command{
		Use:   "report",
		Short: "What the beads of each estimate cost, per group",
		Long: `Read the event log and print, for each group and each estimate label, what the beads cost.

BEADS is the closed beads the group claimed in the stage. SENT BACK is how
often per bead a later stage moved one back to the stage. FAILED is the
group's sessions for them that failed, died, or got stuck: blocked at a
prompt, stalled on its first prompt, gave up on it, or stopped on a
harness error; each session counts once. MEDIAN BUILD is from the claim
to the move that left the stage. A bead two groups worked in one stage
counts for each; a bead with no estimate label counts under "-"; a bead
that never closed counts nowhere. The first line says from when the log
still holds events: the report reads only what retention kept. It changes
nothing.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			o := routeReportOpts{prefixes: normalizeReportPrefixes(prefixes)}
			if len(o.prefixes) == 0 {
				return fmt.Errorf("--prefix: give at least one label prefix")
			}
			if since != "" {
				d, err := parseDays(since)
				if err != nil {
					return fmt.Errorf("--since: %w", err)
				}
				o.since = time.Now().Add(-d)
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
					rows[i] = []string{r.Group, r.Label, strconv.Itoa(r.Beads),
						strconv.FormatFloat(r.SentBack, 'f', 1, 64), strconv.Itoa(r.Failed), median}
				}
				table("GROUP\tLABEL\tBEADS\tSENT BACK\tFAILED\tMEDIAN BUILD", rows)
			})
		},
	}
	cmd.Flags().StringSliceVar(&prefixes, "prefix", []string{"difficulty:", "size:"}, "label prefixes to report, e.g. difficulty,size")
	cmd.Flags().StringVar(&stage, "stage", "", "only this stage (default: every stage a herdr group works)")
	cmd.Flags().StringVar(&since, "since", "", "only what is that new: 7d, 12h, 30m")
	return cmd
}
