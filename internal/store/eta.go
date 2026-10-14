package store

import (
	"context"
	"slices"
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
	if entry != nil {
		c.Entry = append(c.Entry, max(*entry, 0))
		if len(c.Entry) > ETASamplesCap {
			c.Entry = append([]time.Duration(nil), c.Entry[len(c.Entry)-ETASamplesCap:]...)
		}
	}
	if claim != nil {
		c.Claim = append(c.Claim, max(*claim, 0))
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
	failed := map[string]bool{}
	if err := each(tx, bEvents, "", func(_ []byte, e *Event) error {
		if !etaFailedKinds[e.Kind] {
			return nil
		}
		if session, _ := e.Data["session"].(string); session != "" {
			failed[session] = true
		}
		return nil
	}); err != nil {
		return err
	}
	for session := range failed {
		var sess Session
		if ok, err := get(tx, bSessions, session, &sess); err != nil {
			return err
		} else if ok && sess.live() {
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
}

// etaSeedClaim is one claimed event the seed replays.
type etaSeedClaim struct {
	group   string
	stage   string
	session string
	at      time.Time
	paired  bool
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
	type entry struct {
		stage string
		at    time.Time
	}
	entries := map[string]entry{}
	claims := map[string][]*etaSeedClaim{}
	failed := map[string]bool{}
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
			claims[bead] = append(claims[bead], &etaSeedClaim{group: group, stage: stage, session: session, at: e.At})
		case "advanced":
			from, _ := e.Data["from"].(string)
			to, _ := e.Data["to"].(string)
			if from == "" || to == "" || from == to {
				return nil
			}
			sendBack := IsSendBack(visited[bead], from, to)
			visited[bead] = AppendVisited(visited[bead], from)
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
			// The latest claim of the stay no earlier exit ended.
			pick, fallback := -1, -1
			for i, cl := range claims[bead] {
				if cl.paired || cl.stage != from || cl.at.After(e.At) {
					continue
				}
				if exitGroup != "" && cl.group != exitGroup {
					continue
				}
				if failed[cl.session] {
					fallback = i
					continue
				}
				pick = i
			}
			switch {
			case pick >= 0 && exitGroup != "":
				claims[bead][pick].paired = true
				d := e.At.Sub(claims[bead][pick].at)
				group := claims[bead][pick].group
				if exitGroup != "" {
					group = exitGroup
				}
				if err := recordETA(tx, group, label, from, entryDur, &d, sendBack); err != nil {
					return err
				}
			case exitGroup != "":
				if fallback >= 0 {
					claims[bead][fallback].paired = true
				}
				// A failed claim counts only; a claim the retention
				// pruned keeps its entry duration with no claim to pair.
				if fallback >= 0 {
					entryDur = nil
				}
				if err := recordETA(tx, exitGroup, label, from, entryDur, nil, sendBack); err != nil {
					return err
				}
			case fallback >= 0:
				claims[bead][fallback].paired = true
				if err := recordETA(tx, claims[bead][fallback].group, label, from, nil, nil, sendBack); err != nil {
					return err
				}
			default:
				// No claim and no owner at the exit: truly unclaimed.
				if latest := latestSeedClaim(claims[bead], from, e.At); latest >= 0 {
					claims[bead][latest].paired = true
					if err := recordETA(tx, claims[bead][latest].group, label, from, nil, nil, sendBack); err != nil {
						return err
					}
				}
			}
		default:
			if etaFailedKinds[e.Kind] {
				if session, _ := e.Data["session"].(string); session != "" {
					failed[session] = true
				}
			}
		}
		return nil
	}); err != nil {
		return err
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

// latestSeedClaim is the latest unpaired claim of stage at or before at, of
// any group, or -1 with none: what an unowned exit counts in.
func latestSeedClaim(cs []*etaSeedClaim, stage string, at time.Time) int {
	pick := -1
	for i, cl := range cs {
		if cl.paired || cl.stage != stage || cl.at.After(at) {
			continue
		}
		pick = i
	}
	return pick
}
