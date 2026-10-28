package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	bolt "go.etcd.io/bbolt"
)

// Unfold takes member out of carrier, restored whole: the member reopens in
// to (the flow's first stage when to is empty) with the reason as a note, and
// what the fold moved from it comes back from the provenance its folded edge
// recorded (FoldMoved): its tracking links, its outgoing blocks edges and its
// watches. It is one write, all or nothing. The folded edge is gone, the
// carrier's body gains the taken-out line, and an unfolded event is logged on
// the carrier.
//
// What the carrier keeps is what the fold's moves leave behind: a relates
// link to each issue the member tracks again, as any earlier tracker keeps,
// and whatever other members still folded moved from them. A blocks edge or
// a watch two parts moved stays on the carrier until its last part is out,
// so each part restores exactly what it moved even when parts share a
// target. Two members never moved the same link: an issue has one tracking
// bead, so the fold would have refused the second.
//
// A bead that is not a member of the carrier is refused, as are an open
// member (already out), a closed carrier (nothing is taken out of a
// finished bundle), a blank reason, and a to outside the member's flow or
// in a terminal stage. A bead's agent may unfold the members of its own
// carrier and no other's (checkUnfoldOwn), and only into the flow's first
// stage or one people work (checkAgentStage), as a created bead: into
// review or approved the part would land without the stages before it.
//
// What git must read is read before the write: the member's repository
// flows, which say what stage it reopens in and what its gates read, and
// the gates of what the carrier and the member block, which the write reads
// again.
func (s *Store) Unfold(ctx context.Context, carrierRef, memberRef, to, reason, actor string) (*Bead, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, fmt.Errorf("an unfold needs the reason the part comes out")
	}
	pre, err := s.GetBead(ctx, memberRef)
	if err != nil {
		return nil, err
	}
	repo, err := s.repoFor(ctx, pre.Depot, pre.Type, PinnedFlow(pre))
	if err != nil {
		return nil, err
	}
	home, err := s.stageHomeBefore(pre, repo)
	if err != nil {
		return nil, err
	}
	gated := append(s.blocksTargets(ctx, carrierRef), s.blocksTargets(ctx, memberRef)...)
	look := &untilLooks{repos: s.gateRepos(ctx, gated...), memo: flowMemo{}}
	var result *Bead
	err = s.write(func(w *wtx) error {
		resolved, err := paneAuthor(w.Tx, callerOf(ctx), actor)
		if err != nil {
			return err
		}
		actor = resolved
		carrier, err := w.loadBead(carrierRef)
		if err != nil {
			return err
		}
		member, err := w.loadBead(memberRef)
		if err != nil {
			return err
		}
		if err := w.checkUnfoldOwn(ctx, carrier); err != nil {
			return err
		}
		moved := foldedMoved(w.Tx, carrier.UUID, member.UUID)
		if moved == nil {
			return fmt.Errorf("bead %s is not a member of %s: an unfold takes a part out of its carrier", member.ID, carrier.ID)
		}
		if member.Status != "closed" {
			return fmt.Errorf("bead %s is open: an unfold reopens a folded member, and it is already out", member.ID)
		}
		if carrier.Status == "closed" {
			return fmt.Errorf("bead %s is closed: an unfold takes a part out of an open carrier", carrier.ID)
		}
		f, src, err := resolveFlow(w.Tx, member.Depot, member.Type, PinnedFlow(&member.Bead), repo)
		if err != nil {
			// As Reopen's: a stale read tries again, anything else
			// reopens without a flow to check the stage by.
			if errors.Is(err, errNeedRepo) {
				return changedMeanwhile(err)
			}
			f, src = nil, nil
		}
		stage := to
		if stage == "" {
			stage = firstStage(f)
		}
		if err := checkUnfoldStage(f, stage); err != nil {
			return err
		}
		if err := w.checkAgentStage(ctx, f, member.Bead, stage, "take a part out", "take it out with --to"); err != nil {
			return err
		}
		rows, before, err := unfoldTargets(w.Tx, carrier, moved, look)
		if err != nil {
			return err
		}
		if _, err := w.changeBead(member.UUID, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
			b.Status, b.CloseReason, b.ClosedAt = "open", nil, nil
			delete(b.Metadata, "close_seq")
			b.Stage = stage
			b.StayStart = now()
			b.StayClaimAt, b.StayClaimGroup, b.StayClaimSession = nil, "", ""
			b.Visited = AppendVisited(b.Visited, stage)
			return "reopened", nil, nil
		}); err != nil {
			return err
		}
		// The reason note follows the entry event; a stage ask names the event
		// that opened this stay, just as it does for Reopen.
		entry := w.Bucket(bEvents).Sequence()
		row, err := w.loadBead(member.UUID)
		if err != nil {
			return err
		}
		if _, err := w.addNoted(row, NoteNote, reason, actor); err != nil {
			return err
		}
		tmpl, broken, fatal := stageTemplate(f, src, repo, home, stage)
		if fatal != nil {
			return fatal
		}
		if row, err := w.loadBead(member.UUID); err != nil {
			return err
		} else if err := w.putStageAsks(row, stage, entry, f, tmpl, broken); err != nil {
			return err
		}
		if err := w.unfoldLinks(carrier, member, moved, actor); err != nil {
			return err
		}
		if err := w.unfoldBlocks(carrier, member, moved, actor); err != nil {
			return err
		}
		if err := unfoldWatches(w.Tx, carrier, member, moved); err != nil {
			return err
		}
		from, err := w.loadBead(carrier.UUID)
		if err != nil {
			return err
		}
		gone, err := w.loadBead(member.UUID)
		if err != nil {
			return err
		}
		if _, err := w.removeDep(from, gone, DepFolded, "", "", actor); err != nil {
			return err
		}
		line := unfoldLine(carrier.Body, member.ID, actor, reason)
		if _, err := w.changeBead(carrier.UUID, actor, func(w *wtx, b *beadRow) (string, map[string]any, error) {
			was := b.Body
			b.Body = unfoldBody(was, line)
			return "updated", map[string]any{"body": true, "was": map[string]any{"body": was}}, nil
		}); err != nil {
			return err
		}
		if err := w.event(carrier.UUID, "unfolded", actor, map[string]any{"member": member.ID, "to": stage, "reason": reason}); err != nil {
			return err
		}
		if err := w.logTargetsFlips(rows, before, look, actor); err != nil {
			return err
		}
		if row, err := w.loadBead(member.UUID); err != nil {
			return err
		} else {
			result = &row.Bead
		}
		return nil
	})
	return result, err
}

// checkUnfoldOwn refuses unfolding a carrier's members to a bead's agent
// (beadAgent) whose session does not own the carrier. As checkCloseOwn's:
// an agent that could take another bead's parts out could reopen the
// weakness the security stage filed against its own and land it past the
// wait. The carrier's own session may unfold its members.
func (w *wtx) checkUnfoldOwn(ctx context.Context, carrier *beadRow) error {
	sess, err := beadAgent(w.Tx, ctx)
	if err != nil || sess == nil || sess.Bead == carrier.UUID {
		return err
	}
	return fmt.Errorf("bead %s: the session %s of a bead's agent may unfold the members of its own carrier %s and no other's, whatever actor the command names; write a note on it instead, so a person or the line takes it out: yardr bead note %s \"why it comes out\"",
		carrier.ID, sess.ID, handlesIn(w.Tx).of(sess.Bead), carrier.ID)
}

// foldedMoved is what the fold took from the member the folded edge from
// carrier ends in, or nil when member is no member of carrier.
func foldedMoved(tx *bolt.Tx, carrier, member string) *FoldMoved {
	for _, d := range edges(tx, bDepFrom, carrier, DepFolded) {
		if d.To == member {
			return d.Moved
		}
	}
	return nil
}

// checkUnfoldStage refuses reopening an unfolded member in a stage its flow
// does not have, or in a terminal stage, which would leave it open where it
// should be closed. Where no flow governs it, any stage goes, as for a
// created bead.
func checkUnfoldStage(f *Flow, stage string) error {
	if f == nil {
		return nil
	}
	if !slices.Contains(f.Stages, stage) {
		return fmt.Errorf("stage %s is not in flow %s; stages: %s", stage, f.Name, strings.Join(f.Stages, ", "))
	}
	if f.IsTerminal(stage) {
		return fmt.Errorf("stage %s is terminal in flow %s; unfold the part into an earlier stage", stage, f.Name)
	}
	return nil
}

// unfoldTargets lists the open beads whose gates an unfold may flip, each
// once, with each one's holding blockers' UUIDs now: what the carrier
// blocks, and what the member blocked, whose edges the carrier may have
// lost since. Compared after the write, as openTargets is (logTargetsFlips).
func unfoldTargets(tx *bolt.Tx, carrier *beadRow, moved *FoldMoved, look *untilLooks) ([]*beadRow, map[string]map[string]bool, error) {
	rows, before := openTargets(tx, carrier, look)
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.UUID] = true
	}
	for _, b := range moved.Blocks {
		t := new(beadRow)
		uuid, err := resolve(tx, b.To)
		if err != nil {
			return nil, nil, err
		}
		if seen[uuid] {
			continue
		}
		seen[uuid] = true
		if ok, _ := get(tx, bBeads, uuid, t); !ok || t.Status == "closed" {
			continue
		}
		rows = append(rows, t)
		before[t.UUID] = gateHolders(tx, &t.Bead, look)
	}
	slices.SortFunc(rows, func(a, b *beadRow) int { return strings.Compare(a.ID, b.ID) })
	return rows, before, nil
}

// unfoldLinks gives member back each issue it tracked, from the carrier. An
// issue the carrier tracks moves back as any link moves (moveLink): the
// member tracks it and the carrier keeps a relates link to it. An issue
// the carrier relates to (a second issue of its source, which no bead
// tracks) or no longer holds is tracked by the member again, replacing the
// relates link the fold left it. An issue the member tracks already is
// left as it is.
func (w *wtx) unfoldLinks(carrier, member *beadRow, moved *FoldMoved, actor string) error {
	for _, l := range moved.Links {
		to, err := w.loadBead(member.UUID)
		if err != nil {
			return err
		}
		own, err := beadLinks(w.Tx, to.UUID)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(own, func(e linkRow) bool { return e.URL == l.URL && e.Kind == LinkTracks }) {
			continue
		}
		ls, err := issueLinks(w.Tx, l.URL)
		if err != nil {
			return err
		}
		tracked := false
		for i := range ls {
			if ls[i].Kind == LinkTracks && ls[i].Bead == carrier.UUID {
				tracked = true
			}
		}
		if tracked {
			if _, err := w.moveLink(l.URL, to, LinkTracks, actor); err != nil {
				return err
			}
			continue
		}
		nl := NewLink{Kind: LinkTracks, Source: l.Source, URL: l.URL}
		if _, _, err := w.addLink(to, nl, actor, map[string]any{"moved_from": carrier.ID}); err != nil {
			return err
		}
	}
	return nil
}

// unfoldBlocks points the member's outgoing blocks back at it, each with
// the stages it names. The carrier's edge to the same target goes with it,
// unless another member the carrier still holds moved the same blocks
// edge: two parts that blocked one bead the same way share the carrier's
// one edge, and it stays until the last of them is out. An edge the member
// holds already is not doubled, and one the carrier lost since is not
// missed.
func (w *wtx) unfoldBlocks(carrier, member *beadRow, moved *FoldMoved, actor string) error {
	kept, err := unfoldKeptBlocks(w.Tx, carrier.UUID, member.UUID)
	if err != nil {
		return err
	}
	for _, b := range moved.Blocks {
		target, err := w.loadBead(b.To)
		if err != nil {
			return err
		}
		from, err := w.loadBead(member.UUID)
		if err != nil {
			return err
		}
		if !hasEdge(w.Tx, from.UUID, target.UUID, DepBlocks, b.At, b.Until) {
			if err := checkCycle(w.Tx, from, target, ""); err != nil {
				return err
			}
			if err := w.addDep(from, target, DepBlocks, b.At, b.Until, actor); err != nil {
				return err
			}
		}
		if kept[unfoldBlockKey{to: target.UUID, at: b.At, until: b.Until}] {
			continue
		}
		fromC, err := w.loadBead(carrier.UUID)
		if err != nil {
			return err
		}
		if _, err := w.removeDep(fromC, target, DepBlocks, b.At, b.Until, actor); err != nil {
			return err
		}
	}
	return nil
}

// unfoldBlockKey is a blocks edge by what the carrier shares: the bead it
// blocks and the stages it names.
type unfoldBlockKey struct {
	to    string
	at    string
	until string
}

// unfoldKeptBlocks are the blocks edges the carrier keeps after member is
// out: those other members the carrier still holds moved, named by their
// folded edges' records.
func unfoldKeptBlocks(tx *bolt.Tx, carrier, member string) (map[unfoldBlockKey]bool, error) {
	kept := map[unfoldBlockKey]bool{}
	for _, d := range edges(tx, bDepFrom, carrier, DepFolded) {
		if d.To == member || d.Moved == nil {
			continue
		}
		for _, b := range d.Moved.Blocks {
			uuid, err := resolve(tx, b.To)
			if err != nil {
				return nil, err
			}
			kept[unfoldBlockKey{to: uuid, at: b.At, until: b.Until}] = true
		}
	}
	return kept, nil
}

// unfoldWatches gives member back each watch the fold copied from it, and
// takes the carrier's copy away unless another member the carrier still
// holds moved the same watch: one who watched two parts watches the carrier
// once, and keeps it until the last part is out. A watch the member holds
// already is not doubled, and a subscriber gone since is not missed.
func unfoldWatches(tx *bolt.Tx, carrier, member *beadRow, moved *FoldMoved) error {
	kept := unfoldKeptWatches(tx, carrier.UUID, member.UUID)
	for _, fw := range moved.Watchers {
		if err := addWatch(tx, fw.Kind, fw.Name, fw.Watch); err != nil {
			return err
		}
		if kept[watchKey(fw.Kind, fw.Name, fw.Watch)] {
			continue
		}
		copy := fw.Watch
		copy.Where = maps.Clone(fw.Watch.Where)
		copy.Where["bead"] = carrier.ID
		if err := dropWatch(tx, fw.Kind, fw.Name, copy); err != nil {
			return err
		}
	}
	return nil
}

// unfoldKeptWatches are the watches the carrier keeps after member is out:
// those other members the carrier still holds moved, named by their folded
// edges' records, by who holds them and what they filter on but the bead.
func unfoldKeptWatches(tx *bolt.Tx, carrier, member string) map[string]bool {
	kept := map[string]bool{}
	for _, d := range edges(tx, bDepFrom, carrier, DepFolded) {
		if d.To == member || d.Moved == nil {
			continue
		}
		for _, fw := range d.Moved.Watchers {
			kept[watchKey(fw.Kind, fw.Name, fw.Watch)] = true
		}
	}
	return kept
}

// watchKey names a moved watch by who holds it and what it filters on but
// the bead it watches: the same watch of two members, and of their carrier,
// has one key.
func watchKey(kind, name string, sub Subscription) string {
	var filters []string
	for k, v := range sub.Where {
		if k == "bead" {
			continue
		}
		filters = append(filters, k+"="+v)
	}
	slices.Sort(filters)
	return kind + "\x00" + name + "\x00" + sub.Source + "\x00" + sub.Scope + "\x00" + strings.Join(filters, "\x00")
}

// addWatch gives the subscriber name of kind the watch, as it stood: what
// an unfold gives back. One it holds already is not doubled, and one whose
// subscriber is gone since is not missed.
func addWatch(tx *bolt.Tx, kind, name string, sub Subscription) error {
	switch kind {
	case "crew":
		var r CrewMember
		ok, err := get(tx, bCrew, name, &r)
		if err != nil || !ok {
			return err
		}
		if slices.ContainsFunc(r.Subscriptions, sub.same) {
			return nil
		}
		r.Subscriptions = append(r.Subscriptions, sub)
		return put(tx, bCrew, name, &r)
	case "user":
		var u User
		ok, err := get(tx, bUsers, name, &u)
		if err != nil || !ok {
			return err
		}
		if slices.ContainsFunc(u.Subscriptions, sub.same) {
			return nil
		}
		u.Subscriptions = append(u.Subscriptions, sub)
		return put(tx, bUsers, name, &u)
	case "group":
		var g Group
		ok, err := get(tx, bGroups, name, &g)
		if err != nil || !ok {
			return err
		}
		if slices.ContainsFunc(g.Subscriptions, sub.same) {
			return nil
		}
		g.Subscriptions = append(g.Subscriptions, sub)
		return put(tx, bGroups, name, &g)
	}
	return fmt.Errorf("a watch is held by a crew member, a user or a group, not %q", kind)
}

// dropWatch takes the watch from the subscriber name of kind: the copy a
// fold made on the carrier, once no member it came from is still folded. A
// subscriber gone since, or holding no such watch, is not missed.
func dropWatch(tx *bolt.Tx, kind, name string, sub Subscription) error {
	switch kind {
	case "crew":
		var r CrewMember
		ok, err := get(tx, bCrew, name, &r)
		if err != nil || !ok {
			return err
		}
		r.Subscriptions = slices.DeleteFunc(r.Subscriptions, sub.same)
		return put(tx, bCrew, name, &r)
	case "user":
		var u User
		ok, err := get(tx, bUsers, name, &u)
		if err != nil || !ok {
			return err
		}
		u.Subscriptions = slices.DeleteFunc(u.Subscriptions, sub.same)
		return put(tx, bUsers, name, &u)
	case "group":
		var g Group
		ok, err := get(tx, bGroups, name, &g)
		if err != nil || !ok {
			return err
		}
		g.Subscriptions = slices.DeleteFunc(g.Subscriptions, sub.same)
		return put(tx, bGroups, name, &g)
	}
	return fmt.Errorf("a watch is held by a crew member, a user or a group, not %q", kind)
}

// unfoldLine is the line an unfold appends to the carrier's body: the part
// taken out, by its number in the body, or by its id alone when the body
// no longer numbers it.
func unfoldLine(body, member, actor, reason string) string {
	for line := range strings.Lines(body) {
		n, ok := strings.CutPrefix(line, "## Part ")
		if !ok || !strings.HasSuffix(strings.TrimSuffix(line, "\n"), " ("+member+")") {
			continue
		}
		digits, _, _ := strings.Cut(n, ":")
		if _, err := strconv.Atoi(digits); err == nil {
			return fmt.Sprintf("Part %s (%s) was taken out by %s: %s", digits, member, actor, reason)
		}
	}
	return fmt.Sprintf("%s was taken out by %s: %s", member, actor, reason)
}

// unfoldBody appends the taken-out line to the carrier's body, in whose
// ## The bundle section it lands: the section the fold wrote last.
func unfoldBody(body, line string) string {
	if body == "" {
		return line
	}
	return strings.TrimSuffix(body, "\n") + "\n" + line
}
