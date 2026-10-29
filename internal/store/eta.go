package store

import (
	"context"
	"slices"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// ETA cells keep what beads cost, per group, estimate label and stage, so a
// bead's ETA is arithmetic over keyed reads instead of a scan of the log
// (design yardr-hrah). Each claim stamps the stay it works (Store.Claim),
// each stage exit appends the stay's durations to the claiming group's cell
// (w.move), and a yard that upgrades replays its retained log once to seed
// both (migrateETA).

const (
	// ETASamplesCap is the stays each cell's rings keep: the last 200 that
	// ended under a claim, oldest dropped first.
	ETASamplesCap = 200
	// ETAVisitedCap is the stages a bead's visited list keeps: the first 64
	// it reached, in the order it reached them. Flows are small; what is
	// past the cap reads as a first arrival.
	ETAVisitedCap = 64
	// ETASampleFloor is the durations a cell needs before it times a stage:
	// below it the stage is unclocked and the ETA says so.
	ETASampleFloor = 10
)

// etaCell is one group, estimate label and stage's stays: the last 200 that
// ended under a claim as entry-to-exit and claim-to-exit durations, with a
// count of every stay recorded in the cell and of the exits that sent the
// bead back. The rings are in the order the stays ended; the medians are
// computed when read, by sorting at most 200 durations.
type etaCell struct {
	Entry     []time.Duration `json:"entry,omitempty"`
	Claim     []time.Duration `json:"claim,omitempty"`
	Stays     int             `json:"stays,omitempty"`
	SendBacks int             `json:"send_backs,omitempty"`
}

// etaKey is the cell's key: its group, label and stage. None holds a NUL.
func etaKey(group, label, stage string) string { return join(group, label, stage) }

// recordETA appends a stay that ended in from to the cell of group, label
// and from: its entry-to-exit and claim-to-exit durations where each is
// known (nil where it is not: a pruned claim, or an entry the retention
// kept no record of), and the stay and send-back counts either way. A
// negative duration is a clock that went back, and counts as none.
func recordETA(tx *bolt.Tx, group, label, from string, entry, claim *time.Duration, sendBack bool) error {
	var c etaCell
	if _, err := get(tx, bETACells, etaKey(group, label, from), &c); err != nil {
		return err
	}
	if entry != nil && *entry >= 0 {
		c.Entry = append(c.Entry, *entry)
		if len(c.Entry) > ETASamplesCap {
			c.Entry = append([]time.Duration(nil), c.Entry[len(c.Entry)-ETASamplesCap:]...)
		}
	}
	if claim != nil && *claim >= 0 {
		c.Claim = append(c.Claim, *claim)
		if len(c.Claim) > ETASamplesCap {
			c.Claim = append([]time.Duration(nil), c.Claim[len(c.Claim)-ETASamplesCap:]...)
		}
	}
	c.Stays++
	if sendBack {
		c.SendBacks++
	}
	return put(tx, bETACells, etaKey(group, label, from), c)
}

// ETACell is a cell answered: the medians and p90s of its rings (zero with
// no samples), the samples behind each, and the stay and send-back counts
// the per-stage stretch is computed from.
type ETACell struct {
	EntryMedian time.Duration `json:"entry_median"`
	EntryP90    time.Duration `json:"entry_p90"`
	ClaimMedian time.Duration `json:"claim_median"`
	ClaimP90    time.Duration `json:"claim_p90"`
	EntryN      int           `json:"entry_n"`
	ClaimN      int           `json:"claim_n"`
	Stays       int           `json:"stays"`
	SendBacks   int           `json:"send_backs"`
}

// ETACell reads the cell of group, label and stage: zero with none recorded.
// The caller checks the sample floor (ETASampleFloor against EntryN or
// ClaimN, whichever the clock needs); the medians answer regardless.
func (s *Store) ETACell(ctx context.Context, group, label, stage string) (ETACell, error) {
	var out ETACell
	err := s.read(func(tx *bolt.Tx) error {
		var c etaCell
		ok, err := get(tx, bETACells, etaKey(group, label, stage), &c)
		if err != nil || !ok {
			return err
		}
		out = summarizeETACell(&c)
		return nil
	})
	return out, err
}

// BeadStay is a bead's stay clocks: when its current stay in its stage
// started, and when its live session started, nil when unheld or
// unknown. The served bead carries neither; an estimate reads them here.
type BeadStay struct {
	StayStart    time.Time  `json:"stay_start"`
	SessionStart *time.Time `json:"session_start,omitempty"`
}

// BeadStays answers each bead's stay clocks. It answers every id it is
// asked, zero where a bead is closed and has no stay open; an unknown id
// fails the whole response, as GetBead fails its caller.
func (s *Store) BeadStays(ctx context.Context, ids []string) (map[string]BeadStay, error) {
	out := make(map[string]BeadStay, len(ids))
	for _, id := range ids {
		b, err := s.GetBead(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.Status == "closed" {
			out[id] = BeadStay{}
			continue
		}
		var stay BeadStay
		err = s.read(func(tx *bolt.Tx) error {
			row, err := loadBead(tx, b.ID)
			if err != nil {
				return err
			}
			stay.StayStart = row.StayStart
			stay.SessionStart = row.StayClaimAt
			return nil
		})
		if err != nil {
			return nil, err
		}
		// The running clock is the live session's start; the claim's
		// stamp stands in where no live session answers.
		if b.Session != nil {
			if sess, err := s.GetSession(ctx, *b.Session); err == nil && sess.live() {
				stay.SessionStart = &sess.StartedAt
			}
		}
		out[id] = stay
	}
	return out, nil
}

// summarizeETACell answers a cell read from the store.
func summarizeETACell(c *etaCell) ETACell {
	out := ETACell{EntryN: len(c.Entry), ClaimN: len(c.Claim), Stays: c.Stays, SendBacks: c.SendBacks}
	if len(c.Entry) > 0 {
		sorted := slices.Clone(c.Entry)
		slices.Sort(sorted)
		out.EntryMedian, out.EntryP90 = medianOfSorted(sorted), p90OfSorted(sorted)
	}
	if len(c.Claim) > 0 {
		sorted := slices.Clone(c.Claim)
		slices.Sort(sorted)
		out.ClaimMedian, out.ClaimP90 = medianOfSorted(sorted), p90OfSorted(sorted)
	}
	return out
}

// MedianDuration is the middle of ds (sorted in place), the mean of the two
// middle ones when there are evenly many, or zero with none. The route
// report's median, moved here so the cells answer the same number.
func MedianDuration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	slices.Sort(ds)
	return medianOfSorted(ds)
}

// P90Duration is the duration nine tenths of ds fall at or under (sorted in
// place): the one at index ceiling(0.9n) minus one, or zero with none.
func P90Duration(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	slices.Sort(ds)
	return p90OfSorted(ds)
}

// medianOfSorted is the median of ds, already sorted, with at least one.
func medianOfSorted(ds []time.Duration) time.Duration {
	n := len(ds)
	if n%2 == 1 {
		return ds[n/2]
	}
	return (ds[n/2-1] + ds[n/2]) / 2
}

// p90OfSorted is the p90 of ds, already sorted, with at least one.
func p90OfSorted(ds []time.Duration) time.Duration {
	i := (9*len(ds)+9)/10 - 1
	if i < 0 {
		i = 0
	}
	if i >= len(ds) {
		i = len(ds) - 1
	}
	return ds[i]
}

// ETABuildClaim is a claim used to pair a stage exit with the latest work
// that ran there. Session identifies failures that ended the claim.
type ETABuildClaim struct {
	Bead, Group, Stage, Session string
	At                          time.Time
	Seq                         uint64
}

// ETABuildExit is a stage exit: Stage is the stage being left, and Group is
// the group that still owned the bead when it left, if any.
type ETABuildExit struct {
	Bead, Group, Stage string
	At                 time.Time
	Seq                uint64
}

// ETABuildPair answers one exit. Claim is the index of a claim that supplied
// a build duration, or -1. Latest is the latest claim for counts-only
// attribution when no group still owned the bead, or -1.
type ETABuildPair struct {
	Claim  int
	Latest int
}

// PairETABuilds pairs each exit with the latest unpaired claim for the same
// bead and stage, provided that claim is still owned by the exit's group and
// its session did not fail. Every claim up to an exit is consumed there: a
// failed or superseded claim cannot leak into a later visit to the stage.
// The route report and the ETA seed use the same rule.
func PairETABuilds(claims []ETABuildClaim, exits []ETABuildExit, failed map[string]uint64) []ETABuildPair {
	pairs := make([]ETABuildPair, len(exits))
	paired := make([]bool, len(claims))
	byBeadStage := make(map[string][]int)
	for i, c := range claims {
		key := join(c.Bead, c.Stage)
		byBeadStage[key] = append(byBeadStage[key], i)
	}
	for i := range pairs {
		pairs[i] = ETABuildPair{Claim: -1, Latest: -1}
	}
	for ei, x := range exits {
		candidates := byBeadStage[join(x.Bead, x.Stage)]
		for _, ci := range candidates {
			c := claims[ci]
			if paired[ci] || c.Seq > x.Seq {
				continue
			}
			pairs[ei].Latest = ci
		}
		latest := pairs[ei].Latest
		if latest >= 0 {
			c := claims[latest]
			failedAt, didFail := failed[c.Session]
			if x.Group != "" && c.Group == x.Group && (!didFail || failedAt > x.Seq) {
				pairs[ei].Claim = latest
			}
			for _, ci := range candidates {
				if !paired[ci] && claims[ci].Seq <= x.Seq {
					paired[ci] = true
				}
			}
		}
	}
	return pairs
}

// IsSendBack reports whether a move from from to to sends the bead back: to
// a stage it had already reached, from one it reached later. A first
// arrival, a move on, and a same-stage move are not. A from the list never
// held (a bead from before it) reads as not a send-back.
func IsSendBack(visited []string, from, to string) bool {
	if from == to {
		return false
	}
	toIdx := slices.Index(visited, to)
	if toIdx < 0 {
		return false
	}
	fromIdx := slices.Index(visited, from)
	return fromIdx >= 0 && fromIdx > toIdx
}

// AppendVisited records that the bead reached stage: appended in first-visit
// order, kept under ETAVisitedCap, already there kept as it was.
func AppendVisited(visited []string, stage string) []string {
	if slices.Contains(visited, stage) {
		return visited
	}
	if len(visited) >= ETAVisitedCap {
		return visited
	}
	return append(visited, stage)
}

// DifficultyLabel is the bead's estimate label for its cells: its
// difficulty: label, else the "-" row, as the route report groups beads with
// none of its prefixes.
func DifficultyLabel(labels []string) string {
	for _, l := range labels {
		if strings.HasPrefix(l, "difficulty:") {
			return l
		}
	}
	return "-"
}

// exitStay records the stay in b's stage ending with a move to to at exitAt,
// and starts the new stay there. A same-stage move is not an exit and
// records nothing.
//
// A stay that ends owned by the group that claimed it appends both durations
// to that group's cell unless its session had a failed dispatch event; that
// event leaves the claim live but excludes it from build samples. A stay
// with a claiming group that no longer owns it — failed, released, held, or
// ended by a same-stage move before this one — counts only in that group's
// cell. A stay no group ever claimed records in no cell: there is none to
// keep it.
func exitStay(tx *bolt.Tx, b *beadRow, to string, exitAt time.Time) error {
	from := b.Stage
	if from == to {
		return nil
	}
	sendBack := IsSendBack(b.Visited, from, to)
	label := DifficultyLabel(b.Labels)
	owner := ""
	if b.Group != nil {
		owner = *b.Group
	}
	claimed := b.StayClaimAt != nil
	failed := false
	if b.StayClaimSession != "" {
		if _, err := get(tx, bETAFailed, b.StayClaimSession, &failed); err != nil {
			return err
		}
	}
	switch {
	case claimed && owner != "" && owner == b.StayClaimGroup:
		if failed {
			if err := recordETA(tx, owner, label, from, nil, nil, sendBack); err != nil {
				return err
			}
			break
		}
		var entry, claim *time.Duration
		if !b.StayStart.IsZero() {
			d := exitAt.Sub(b.StayStart)
			entry = &d
		}
		d := exitAt.Sub(*b.StayClaimAt)
		claim = &d
		if err := recordETA(tx, owner, label, from, entry, claim, sendBack); err != nil {
			return err
		}
	case b.StayClaimGroup != "":
		if err := recordETA(tx, b.StayClaimGroup, label, from, nil, nil, sendBack); err != nil {
			return err
		}
	}
	if b.StayClaimSession != "" {
		if err := tx.Bucket(bETAFailed).Delete([]byte(b.StayClaimSession)); err != nil {
			return err
		}
	}
	b.Visited = AppendVisited(b.Visited, from)
	b.Visited = AppendVisited(b.Visited, to)
	b.StayStart = exitAt
	b.StayClaimAt, b.StayClaimGroup, b.StayClaimSession = nil, "", ""
	return nil
}

// migrateETAFailed seeds markers for failed dispatch events on claims that
// are still live. Older schema 21 yards already have the ETA cells and stay
// clocks, so migrateETA does not replay them again; this pass keeps their
// live claims aligned with the normal write path.
func migrateETAFailed(tx *bolt.Tx, from uint64) error {
	if from >= 22 {
		return nil
	}
	failed := map[string]map[string]bool{}
	if err := each(tx, bEvents, "", func(_ []byte, e *Event) error {
		if !etaFailedKinds[e.Kind] || e.Bead == nil {
			return nil
		}
		if session, _ := e.Data["session"].(string); session != "" {
			if failed[session] == nil {
				failed[session] = map[string]bool{}
			}
			failed[session][*e.Bead] = true
		}
		return nil
	}); err != nil {
		return err
	}
	for session, eventBeads := range failed {
		var sess Session
		if ok, err := get(tx, bSessions, session, &sess); err != nil {
			return err
		} else if ok && sess.live() && eventBeads[sess.Bead] {
			if err := put(tx, bETAFailed, session, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// etaFailedKinds are the session events that exclude a claim from build
// samples: the same set the route report counts under FAILED. Events that
// leave the session live are remembered in bETAFailed; an actual failed or
// dead session clears StayClaimAt in EndSession.
var etaFailedKinds = map[string]bool{
	"session_died":           true,
	"session_failed":         true,
	"session_blocked":        true,
	"session_stalled":        true,
	"session_prompt_gave_up": true,
	"session_harness_error":  true,
	"session_harness_dialog": true,
}

// IsETAFailedKind reports whether a session event excludes a claim from build
// samples. The route report uses the same set when counting failed sessions.
func IsETAFailedKind(kind string) bool { return etaFailedKinds[kind] }

// etaSeedExit is one stage exit to record after pairing claims.
type etaSeedExit struct {
	pair     ETABuildExit
	label    string
	entry    *time.Duration
	sendBack bool
	record   bool
}

type etaSeedLabelChange struct {
	seq        uint64
	difficulty string
}

func etaSeedLabels(value any) ([]string, bool) {
	switch labels := value.(type) {
	case []string:
		return labels, true
	case []any:
		out := make([]string, 0, len(labels))
		for _, value := range labels {
			label, ok := value.(string)
			if !ok {
				return nil, false
			}
			out = append(out, label)
		}
		return out, true
	case nil:
		return nil, true
	default:
		return nil, false
	}
}

// migrateETA seeds the ETA cells and the open stays' clocks from the
// retained log, once, for yards from before them. The pairing is the route
// report's — each exit ends the latest claim of its bead in its stage that
// no earlier exit ended, a failed claim never pairs — writing instead of
// printing, for every stage and not only closed beads. Open stays start
// their clocks at the seed; their claim fields come from the live session
// where one holds the bead.
func migrateETA(tx *bolt.Tx, from uint64) error {
	if from >= 21 {
		return nil
	}
	// Already seeded (a fixture made after the cells, aged back): the seed
	// runs once, and replaying again would count every stay twice.
	if k, _ := tx.Bucket(bETACells).Cursor().First(); k != nil {
		return nil
	}
	beads := map[string]*beadRow{}
	if err := each(tx, bBeads, "", func(k []byte, r *beadRow) error {
		beads[string(k)] = r
		return nil
	}); err != nil {
		return err
	}
	visited := map[string][]string{}
	labelChanges := map[string][]etaSeedLabelChange{}
	type entry struct {
		stage string
		at    time.Time
	}
	entries := map[string]entry{}
	claims := map[string][]ETABuildClaim{}
	failed := map[string]uint64{}
	claimBead := map[string]string{}
	var exits []etaSeedExit
	seeded := now()
	if err := each(tx, bEvents, "", func(_ []byte, e *Event) error {
		if e.Bead == nil {
			return nil
		}
		bead := *e.Bead
		switch e.Kind {
		case "created":
			stage, _ := e.Data["stage"].(string)
			if stage == "" {
				return nil
			}
			visited[bead] = AppendVisited(visited[bead], stage)
			entries[bead] = entry{stage: stage, at: e.At}
		case "claimed":
			group, _ := e.Data["group"].(string)
			stage, _ := e.Data["stage"].(string)
			session, _ := e.Data["session"].(string)
			if group == "" || stage == "" {
				return nil
			}
			visited[bead] = AppendVisited(visited[bead], stage)
			claims[bead] = append(claims[bead], ETABuildClaim{Bead: bead, Group: group, Stage: stage, Session: session, At: e.At, Seq: e.Seq})
			if session != "" {
				claimBead[session] = bead
			}
		case "advanced":
			from, _ := e.Data["from"].(string)
			to, _ := e.Data["to"].(string)
			if from == "" || to == "" || from == to {
				return nil
			}
			visited[bead] = AppendVisited(visited[bead], from)
			sendBack := IsSendBack(visited[bead], from, to)
			visited[bead] = AppendVisited(visited[bead], to)
			row := beads[bead]
			if row == nil {
				entries[bead] = entry{stage: to, at: e.At}
				return nil
			}
			label := DifficultyLabel(row.Labels)
			exitGroup, _ := e.Data["group"].(string)
			var entryDur *time.Duration
			if en, ok := entries[bead]; ok && en.stage == from {
				d := e.At.Sub(en.at)
				entryDur = &d
			}
			entries[bead] = entry{stage: to, at: e.At}
			exits = append(exits, etaSeedExit{
				pair:  ETABuildExit{Bead: bead, Group: exitGroup, Stage: from, At: e.At, Seq: e.Seq},
				label: label, entry: entryDur, sendBack: sendBack, record: true,
			})
		case "updated":
			if was, ok := e.Data["was"].(map[string]any); ok {
				if value, ok := was["labels"]; ok {
					if labels, ok := etaSeedLabels(value); ok {
						labelChanges[bead] = append(labelChanges[bead], etaSeedLabelChange{
							seq: e.Seq, difficulty: DifficultyLabel(labels),
						})
					}
				}
			}
		case "closed":
			// Close ends the current attempt without a stage-exit sample.
			// Consume its claims so a later reopen cannot attribute them to
			// a stay the live row correctly starts fresh.
			if en, ok := entries[bead]; ok {
				exits = append(exits, etaSeedExit{
					pair: ETABuildExit{Bead: bead, Stage: en.stage, At: e.At, Seq: e.Seq},
				})
			}
		case "reopened":
			// Reopen starts a fresh stay in the stage left by the last
			// retained advance. Reset its historical entry clock too.
			if en, ok := entries[bead]; ok {
				entries[bead] = entry{stage: en.stage, at: e.At}
				visited[bead] = AppendVisited(visited[bead], en.stage)
			}
		default:
			if etaFailedKinds[e.Kind] {
				if session, _ := e.Data["session"].(string); session != "" {
					if claimBead[session] == bead {
						if _, seen := failed[session]; !seen {
							failed[session] = e.Seq
						}
					}
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	// Each label update records the labels it replaced. The first such
	// update after an exit therefore says which difficulty cell that exit
	// belonged to; with no later update, the bead row still has its label.
	for i := range exits {
		if !exits[i].record {
			continue
		}
		changes := labelChanges[exits[i].pair.Bead]
		j := sort.Search(len(changes), func(j int) bool {
			return changes[j].seq > exits[i].pair.Seq
		})
		if j < len(changes) {
			exits[i].label = changes[j].difficulty
		}
	}
	var pairClaims []ETABuildClaim
	for _, beadClaims := range claims {
		pairClaims = append(pairClaims, beadClaims...)
	}
	pairExits := make([]ETABuildExit, len(exits))
	for i, exit := range exits {
		pairExits[i] = exit.pair
	}
	for i, pair := range PairETABuilds(pairClaims, pairExits, failed) {
		exit := exits[i]
		if !exit.record {
			continue
		}
		switch {
		case pair.Claim >= 0:
			claim := pairClaims[pair.Claim]
			d := exit.pair.At.Sub(claim.At)
			if err := recordETA(tx, claim.Group, exit.label, exit.pair.Stage, exit.entry, &d, exit.sendBack); err != nil {
				return err
			}
		case exit.pair.Group != "":
			if err := recordETA(tx, exit.pair.Group, exit.label, exit.pair.Stage, nil, nil, exit.sendBack); err != nil {
				return err
			}
		case pair.Latest >= 0:
			claim := pairClaims[pair.Latest]
			if err := recordETA(tx, claim.Group, exit.label, exit.pair.Stage, nil, nil, exit.sendBack); err != nil {
				return err
			}
		}
	}
	for uuid, row := range beads {
		if row.Status == "closed" {
			continue
		}
		// A stay that already has its clocks (a bead made after them)
		// keeps them: the seed starts only what has none.
		if !row.StayStart.IsZero() {
			continue
		}
		row.StayStart = seeded
		row.Visited = visited[uuid]
		row.Visited = AppendVisited(row.Visited, row.Stage)
		row.StayClaimAt, row.StayClaimGroup, row.StayClaimSession = nil, "", ""
		if row.Group != nil && row.Session != nil {
			var sess Session
			if ok, err := get(tx, bSessions, *row.Session, &sess); err != nil {
				return err
			} else if ok && sess.live() && sess.Bead == uuid && sess.Stage == row.Stage {
				at := sess.StartedAt
				row.StayClaimAt, row.StayClaimGroup, row.StayClaimSession = &at, sess.Group, sess.ID
			}
		}
		if err := put(tx, bBeads, uuid, row); err != nil {
			return err
		}
	}
	return nil
}
