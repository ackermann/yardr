package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	bolt "go.etcd.io/bbolt"
)

// FoldedSiding is one siding the pass moved on: its eligible beads folded
// into a carrier, or the one eligible bead advanced alone.
type FoldedSiding struct {
	Depot string `json:"depot"`
	Flow  string `json:"flow"`
	Stage string `json:"stage"`
	// Carrier is the bead that went to To: the fold's carrier, or the one
	// eligible bead, advanced alone.
	Carrier string `json:"carrier"`
	// Members are the beads the carrier folds, oldest first; empty for an
	// advance, which folds none.
	Members []string `json:"members"`
	// Left are the siding's ineligible beads, oldest first, which stay.
	Left []string `json:"left"`
	To   string   `json:"to"`
	// Trigger is what made the siding ready: "count" or "wait".
	Trigger string `json:"trigger"`
	// Advanced says the one eligible bead moved on by itself, not folded:
	// a carrier of one part is the same bead with a worse title.
	Advanced bool `json:"advanced,omitempty"`
}

// SidingPass is what one FoldSidings did.
type SidingPass struct {
	Folded []FoldedSiding `json:"folded"`
	// Refused are the ready sidings the pass told of instead of folding:
	// their eligible beads weigh more than the group allows, or their
	// stage has nowhere to fold to.
	Refused []RefusedSiding `json:"refused"`
	// Next is when the earliest not-ready siding is due, nil with none:
	// when to call FoldSidings again at the latest. A ready siding keeps
	// none: it folds or tells on this pass, or waits for a bead to arrive or
	// to become eligible, and either wakes a pass by its own write.
	Next *time.Time `json:"next,omitempty"`
}

// RefusedSiding is one siding the pass refused to fold: its eligible beads
// stay where they are, and one fold_refused event tells of them.
type RefusedSiding struct {
	Depot string `json:"depot"`
	Flow  string `json:"flow"`
	Stage string `json:"stage"`
	// Members are the beads the fold would have taken, oldest first.
	Members []string `json:"members"`
	// Reason is why the siding cannot fold: "ceiling" (its beads weigh
	// more than the group's fold_max) or "next" (its stage has no next
	// stage to fold to).
	Reason string `json:"reason"`
}

// FoldRefused is the event a pass logs on a siding's latest member when the
// siding is ready but cannot fold. It is of the class attention: the
// yardmaster hears it, and anyone else subscribes.
const FoldRefused = "fold_refused"

// sidingKey is one siding: the depot's beads in the stage of the flow that
// the group works. Two depots on one yard default route are two sidings, as
// are two flows of one depot that both name the stage, and two siding
// groups on one stage, whose numbers may differ.
type sidingKey struct {
	depot, flow, path, stage, group string
}

// sidingMember is a bead waiting in a siding, with why a fold would leave
// it (stuckBead), empty for one it takes.
type sidingMember struct {
	row   *beadRow
	stuck string
}

// FoldSidings folds every ready siding at at: one whose open beads number
// at least its group's fold, or whose latest bead entered at least
// fold_after ago. The count and the clock cover every open bead in the
// siding, stuck or not; eligibility only decides who leaves, and the rest
// stay, named in the folded event's left. A ready siding with one eligible
// bead advances it, not folded. The dispatcher calls it on each pass, with
// its clock.
//
// beads and routes are the open beads and the yard's routes, listed once
// for the pass: the dispatcher shares waits'. What the write folds is read
// again inside it, so a member that arrived after the lists is folded too.
// A bead whose depot's repository cannot be read is left out of the pass,
// and folded on a later one. What git or the disk must read is read before
// the writes; each ready siding then folds in one write of its own, which
// re-checks that it is still ready. A ready siding that cannot fold tells
// instead: one fold_refused on its latest member, once per member set. A
// ready siding with no eligible bead does nothing and logs nothing; its
// beads wait until one becomes eligible.
func (s *Store) FoldSidings(ctx context.Context, at time.Time, beads []Bead, routes []Route) (*SidingPass, error) {
	pass := &SidingPass{}
	var operator string
	sidingByGroup := map[string]Siding{}
	err := s.read(func(tx *bolt.Tx) error {
		var err error
		if operator, err = operatorIn(tx); err != nil {
			return err
		}
		return each(tx, bGroups, "", func(_ []byte, g *Group) error {
			if si, ok := SidingOf(*g); ok {
				sidingByGroup[g.Name] = si
			}
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	if len(sidingByGroup) == 0 {
		return pass, nil
	}
	var cands []Bead
	for _, b := range beads {
		r := MatchRoute(routes, b, operator)
		if r == nil {
			continue
		}
		if _, ok := sidingByGroup[r.Worker()]; !ok {
			continue
		}
		cands = append(cands, b)
	}
	if len(cands) == 0 {
		return pass, nil
	}
	// The repository flows the pass resolves through, read before any
	// transaction, since git must not run inside one: each candidate's
	// own, for the flow that governs it, and its until-blockers', for
	// what holds it. A depot whose repository cannot be read leaves its
	// beads out; its blockers hold by the exact stage, as the read path
	// has them.
	flowRepos := map[string]*RepoFlows{}
	leftOut := map[string]bool{}
	asked := map[flowAsk]error{}
	for _, b := range cands {
		ask := flowAsk{typ: b.Type, pin: PinnedFlow(&b)}
		if b.Depot != nil {
			ask.depot, ask.hasDepot = *b.Depot, true
		}
		if _, ok := asked[ask]; !ok {
			repo, err := s.repoFor(ctx, b.Depot, b.Type, ask.pin)
			if err == nil && b.Depot != nil {
				flowRepos[*b.Depot] = repo
			}
			asked[ask] = err
		}
		if asked[ask] != nil {
			leftOut[b.UUID] = true
		}
	}
	repos := map[string]*RepoFlows{}
	for depot, repo := range flowRepos {
		repos[depot] = repo
	}
	gated := make([]string, 0, len(cands))
	for _, b := range cands {
		gated = append(gated, b.ID)
	}
	for depot, repo := range s.gateRepos(ctx, gated...) {
		repos[depot] = repo
	}
	look := &untilLooks{repos: repos, memo: flowMemo{}}
	members := map[sidingKey][]sidingMember{}
	err = s.read(func(tx *bolt.Tx) error {
		for _, b := range cands {
			if leftOut[b.UUID] {
				continue
			}
			row, err := loadBead(tx, b.UUID)
			if errors.Is(err, ErrNotFound) {
				continue // gone meanwhile
			} else if err != nil {
				return err
			}
			if !row.open() {
				continue // closed meanwhile
			}
			var repo *RepoFlows
			if row.Depot != nil {
				repo = repos[*row.Depot]
			}
			f, src, err := look.memo.resolve(tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
			if errors.Is(err, errNeedRepo) {
				continue // its repository cannot be read
			} else if err != nil {
				return err
			}
			if f == nil {
				continue // no flow governs it: no next stage to fold to
			}
			// The stage may have changed since the lists were read.
			r := MatchRoute(routes, row.Bead, operator)
			if r == nil {
				continue
			}
			if _, ok := sidingByGroup[r.Worker()]; !ok {
				continue
			}
			stuck, err := stuckBead(tx, row, look, at)
			if err != nil {
				return err
			}
			key := sidingKey{depot: sidingDepot(row.Depot), flow: f.Name, path: flowPath(src), stage: row.Stage, group: r.Worker()}
			members[key] = append(members[key], sidingMember{row: row, stuck: stuck})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	keys := make([]sidingKey, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b sidingKey) int {
		return cmp.Or(cmp.Compare(a.depot, b.depot), cmp.Compare(a.flow, b.flow),
			cmp.Compare(a.path, b.path), cmp.Compare(a.stage, b.stage), cmp.Compare(a.group, b.group))
	})
	for _, key := range keys {
		ms := members[key]
		slices.SortFunc(ms, func(a, b sidingMember) int {
			return cmp.Or(a.row.CreatedAt.Compare(b.row.CreatedAt), cmp.Compare(a.row.UUID, b.row.UUID))
		})
		si := sidingByGroup[key.group]
		latest := ms[0].row.StayStart
		for _, m := range ms[1:] {
			latest = maxTime(latest, m.row.StayStart)
		}
		due := latest.Add(si.After)
		ready := len(ms) >= si.Fold || !due.After(at)
		if !ready {
			// A no-next-stage siding still needs its due wake so it can tell
			// the yardmaster when it becomes ready to refuse.
			if pass.Next == nil || due.Before(*pass.Next) {
				pass.Next = &due
			}
			continue
		}
		var eligible []sidingMember
		for _, m := range ms {
			if m.stuck == "" {
				eligible = append(eligible, m)
			}
		}
		if len(eligible) == 0 {
			continue // its beads wait until one becomes eligible
		}
		fold, refused, err := s.foldOrRefuseSiding(ctx, at, key, eligible, repos)
		if err != nil {
			return pass, err
		}
		if fold != nil {
			pass.Folded = append(pass.Folded, *fold)
		}
		if refused != nil {
			pass.Refused = append(pass.Refused, *refused)
		}
	}
	return pass, nil
}

// foldOrRefuseSiding settles one ready siding in one write: its eligible
// members, read again inside it, through w.fold, the one eligible bead
// advanced alone, or one fold_refused when the siding cannot fold: its
// stage has no next stage, or its eligible beads weigh more than the
// group's fold_max. A refusal is told once per member set: the write
// compares the set with the siding's last fold_refused, read here, since
// the event is the siding's own memory of being told. What the write needs
// read before it is read here: the gates its closes flip, the stage files
// of its flow, and that last refusal. The write re-checks that the siding
// is still a siding and still ready; when it is not, it changes nothing,
// and foldOrRefuseSiding answers nil, nil.
func (s *Store) foldOrRefuseSiding(ctx context.Context, at time.Time, key sidingKey, decidedEligible []sidingMember, repos map[string]*RepoFlows) (*FoldedSiding, *RefusedSiding, error) {
	reads := &beadReads{repo: repos[key.depot]}
	home, err := s.stageHomeBefore(&decidedEligible[0].row.Bead, reads.repo)
	if err != nil {
		return nil, nil, err
	}
	reads.home = home
	// The gates the closes flip: each eligible member's targets'.
	seen := map[string]bool{}
	var ids []string
	for _, m := range decidedEligible {
		for _, t := range s.blocksTargets(ctx, m.row.ID) {
			if !seen[t] {
				seen[t] = true
				ids = append(ids, t)
			}
		}
	}
	for depot, repo := range s.gateRepos(ctx, ids...) {
		repos[depot] = repo
	}
	last, err := s.lastRefused(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	var got *FoldedSiding
	var told *RefusedSiding
	err = s.write(func(w *wtx) error {
		got, told = nil, nil // Batch may run this again
		routes, err := sortedRoutes(w.Tx)
		if err != nil {
			return err
		}
		operator, err := operatorIn(w.Tx)
		if err != nil {
			return err
		}
		g := new(Group)
		ok, err := get(w.Tx, bGroups, key.group, g)
		if err != nil {
			return err
		}
		if !ok {
			return nil // the group is gone
		}
		si, ok := SidingOf(*g)
		if !ok {
			return nil // no siding here anymore
		}
		look := &untilLooks{repos: repos, memo: flowMemo{}}
		rows, err := s.openBeads(w.Tx)
		if err != nil {
			return err
		}
		var kept []sidingMember
		for _, row := range rows {
			if sidingDepot(row.Depot) != key.depot || row.Stage != key.stage {
				continue
			}
			r := MatchRoute(routes, row.Bead, operator)
			if r == nil || r.Worker() != key.group {
				continue
			}
			var repo *RepoFlows
			if row.Depot != nil {
				repo = repos[*row.Depot]
			}
			f, src, err := look.memo.resolve(w.Tx, row.Depot, row.Type, PinnedFlow(&row.Bead), repo)
			if errors.Is(err, errNeedRepo) {
				continue // its repository cannot be read
			} else if err != nil {
				return err
			}
			if f == nil || f.Name != key.flow || flowPath(src) != key.path {
				continue // another siding, or none
			}
			stuck, err := stuckBead(w.Tx, row, look, at)
			if err != nil {
				return err
			}
			kept = append(kept, sidingMember{row: row, stuck: stuck})
		}
		if len(kept) == 0 {
			return nil
		}
		slices.SortFunc(kept, func(a, b sidingMember) int {
			return cmp.Or(a.row.CreatedAt.Compare(b.row.CreatedAt), cmp.Compare(a.row.UUID, b.row.UUID))
		})
		latest := kept[0].row.StayStart
		for _, m := range kept[1:] {
			latest = maxTime(latest, m.row.StayStart)
		}
		due := latest.Add(si.After)
		if len(kept) < si.Fold && due.After(at) {
			return nil // ready no longer: a bead left meanwhile
		}
		var eligible, left []sidingMember
		for _, m := range kept {
			if m.stuck == "" {
				eligible = append(eligible, m)
			} else {
				left = append(left, m)
			}
		}
		if len(eligible) == 0 {
			return nil
		}
		// The flow every kept member resolved to; the memo answers it as
		// it did above.
		var repo *RepoFlows
		if kept[0].row.Depot != nil {
			repo = repos[*kept[0].row.Depot]
		}
		f, _, err := look.memo.resolve(w.Tx, kept[0].row.Depot, kept[0].row.Type, PinnedFlow(&kept[0].row.Bead), repo)
		if err != nil {
			return changedMeanwhile(err)
		}
		points := sidingPoints(eligible)
		next := f.Transitions[key.stage]
		if len(next) == 0 {
			// Nowhere to fold to: the flow, not the siding, is wrong,
			// and the yardmaster learns it once per member set.
			return w.refuseSiding(key, kept, eligible, si.Max, "next", points, last, &told)
		}
		if points > si.Max {
			// More than one session can build and one review hold:
			// nothing moves, and the yardmaster learns it once per
			// member set. A person folds part by hand, moves some
			// back, or raises fold_max.
			return w.refuseSiding(key, kept, eligible, si.Max, "ceiling", points, last, &told)
		}
		to := next[0]
		trigger := "wait"
		if len(kept) >= si.Fold {
			trigger = "count"
		}
		fold := FoldedSiding{Depot: key.depot, Flow: key.flow, Stage: key.stage, To: to, Trigger: trigger}
		fold.Left = make([]string, len(left))
		for i, m := range left {
			fold.Left[i] = m.row.ID
		}
		if len(eligible) == 1 {
			moved, err := w.advanceSiding(ctx, eligible[0].row, to, reads.repo, reads.home, look, "yard")
			if err != nil {
				return err
			}
			fold.Carrier, fold.Advanced = moved.ID, true
		} else {
			rows := make([]*beadRow, len(eligible))
			fold.Members = make([]string, len(eligible))
			for i, m := range eligible {
				rows[i], fold.Members[i] = m.row, m.row.ID
			}
			carrier, err := w.fold(ctx, reads, look, rows, fold.Left, FoldOptions{To: to}, trigger, "yard")
			if err != nil {
				return err
			}
			fold.Carrier = carrier.ID
		}
		got = &fold
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return got, told, nil
}

// lastRefused is the siding's most recent fold_refused, or nil with none:
// the siding's own memory of being told, read through the same kind index as
// EventsOfKind so an older refusal cannot disappear behind other sidings'
// recent events.
func (s *Store) lastRefused(ctx context.Context, key sidingKey) (*Event, error) {
	var last *Event
	err := s.read(func(tx *bolt.Tx) error {
		return walkEventsOfKind(tx, FoldRefused, func(e Event) bool {
			if e.Data["depot"] == key.depot && e.Data["flow"] == key.flow && e.Data["stage"] == key.stage {
				last = &e
				return false
			}
			return true
		})
	})
	return last, err
}

// sidingPoints are the points a fold of members would carry: their
// sizePoints, summed.
func sidingPoints(members []sidingMember) int {
	points := 0
	for _, m := range members {
		points += sizePoints(m.row.Labels)
	}
	return points
}

// refuseSiding logs one fold_refused for the siding key on its latest
// member, unless last already told of this set: the eligible members'
// sorted ids and the reason equal its. kept are all the siding's members,
// eligible the ones the fold would have taken, oldest first either way.
// told carries the refusal for the pass when an event was logged, and stays
// nil when the set was told of already.
func (w *wtx) refuseSiding(key sidingKey, kept, eligible []sidingMember, max int, reason string, points int, last *Event, told **RefusedSiding) error {
	members := make([]string, len(eligible))
	for i, m := range eligible {
		members[i] = m.row.ID
	}
	sorted := slices.Clone(members)
	slices.Sort(sorted)
	if last != nil {
		if prev := refusedMembers(last); prev != nil && slices.Equal(prev, sorted) && last.Data["reason"] == reason {
			return nil // told of this set already
		}
	}
	latest := kept[0].row
	for _, m := range kept[1:] {
		if m.row.StayStart.After(latest.StayStart) {
			latest = m.row
		}
	}
	unreadable := []string{}
	for _, m := range eligible {
		if unreadableEstimate(m.row.Labels) {
			unreadable = append(unreadable, m.row.ID)
		}
	}
	slices.Sort(unreadable)
	if err := w.event(latest.UUID, FoldRefused, "yard", map[string]any{
		"depot": key.depot, "flow": key.flow, "stage": key.stage,
		"members": sorted, "points": points, "fold_max": max,
		"reason": reason, "unreadable": unreadable,
	}); err != nil {
		return err
	}
	*told = &RefusedSiding{Depot: key.depot, Flow: key.flow, Stage: key.stage, Members: members, Reason: reason}
	return nil
}

// refusedMembers are the sorted member ids a fold_refused told of, or nil
// when the event carries none it can be compared by.
func refusedMembers(e *Event) []string {
	ids, _ := e.Data["members"].([]any)
	if ids == nil {
		return nil
	}
	var out []string
	for _, id := range ids {
		s, _ := id.(string)
		if s == "" {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// advanceSiding moves the siding's one eligible bead to to: the yard's own
// move, nobody's to refuse (may is nil, as for noteBack), since the yard's
// automation moves it, and the event says by siding. The gates the move can
// flip are logged as advance logs them.
func (w *wtx) advanceSiding(ctx context.Context, row *beadRow, to string, repo *RepoFlows, home *stageHome, look *untilLooks, actor string) (*Bead, error) {
	ownBefore := gateHolders(w.Tx, &row.Bead, look)
	targets, before := openTargets(w.Tx, row, look)
	// to is the flow's first edge out of the stage, allowed by
	// construction; checkAdvance is the hand move's to run.
	moved, err := w.move(row.ID, actor, repo, home, nil, func(_ *Flow, _ *beadRow, data map[string]any) (string, error) {
		data["by"] = "siding"
		return to, nil
	})
	if err != nil {
		return nil, err
	}
	if moved.Status == "closed" && row.Status != "closed" {
		if err := w.checkCloseOwn(ctx, row); err != nil {
			return nil, refuse(RefusedNotAllowed, err)
		}
	}
	after, err := w.loadBead(row.ID)
	if err != nil {
		return nil, err
	}
	if err := w.logGateFlip(after, ownBefore, gateHolders(w.Tx, &after.Bead, look), actor); err != nil {
		return nil, err
	}
	return moved, w.logTargetsFlips(targets, before, look, actor)
}

// sidingDepot is the depot a siding key names: "" for beads with none.
func sidingDepot(depot *string) string {
	if depot == nil {
		return ""
	}
	return *depot
}

// flowPath is where the flow came from: "" in the store.
func flowPath(src *FlowSource) string {
	if src == nil {
		return ""
	}
	return src.Path
}

// maxTime is the later of a and b.
func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
