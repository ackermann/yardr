package store

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	bolt "go.etcd.io/bbolt"
)

// FoldOptions is what whoever folds says about the carrier; the members say
// the rest.
type FoldOptions struct {
	// To is the stage the carrier starts in: for a siding, the first stage
	// its flow leads to from it.
	To string
	// Title replaces the title the members make.
	Title string
}

// FoldMoved is what a fold took from one member, kept on the member's folded
// edge so that an unfold can put back exactly that, even when several parts
// shared a target. A fold fills it in as it takes each thing from the member.
type FoldMoved struct {
	// Stage is the stage the member stood in when the fold closed it.
	Stage string `json:"stage"`
	// Links are the member's tracks links, which moved to the carrier.
	Links []FoldLink `json:"links,omitempty"`
	// Blocks are the member's outgoing blocks edges, which were re-pointed
	// to the carrier. Two parts that blocked one bead the same way both
	// name it, though the carrier blocks it once.
	Blocks []FoldBlock `json:"blocks,omitempty"`
	// Watchers are the watches of the member that were copied to the
	// carrier; the close ended the member's own.
	Watchers []FoldWatcher `json:"watchers,omitempty"`
}

// FoldLink is a tracks link of a member that a fold moved: the issue, and
// the kind of link the carrier got to it. That is tracks, or relates for a
// second issue of a source, since a bead tracks one issue per source; the
// member keeps a relates link either way.
type FoldLink struct {
	Source string `json:"source"`
	URL    string `json:"url"`
	Kind   string `json:"kind"`
}

// FoldBlock is a blocks edge a fold re-pointed from a member to the carrier:
// the bead it blocks, and the stages the edge names, as on the member.
type FoldBlock struct {
	To    string `json:"to"`
	At    string `json:"at,omitempty"`
	Until string `json:"until,omitempty"`
}

// FoldWatcher is a watch of a member that a fold copied to the carrier: who
// held it (kind is crew, user or group, since a crew member and a manual
// group may share a name) and the subscription as it stood on the member.
type FoldWatcher struct {
	Kind  string       `json:"kind"`
	Name  string       `json:"name"`
	Watch Subscription `json:"watch"`
}

// FoldBeads folds the beads ids into one carrier, in one write: a bead in
// opts.To that holds each of them as a part, whatever each tracked, blocked
// and was watched for moved to it, a folded edge from it to each that records
// that, and each of them closed "folded into <carrier>". It is all or
// nothing. A member that is closed, worked by a session, another yard's,
// a train's child, blocked by an open bead that is not folded with it or a
// train (checkFoldable), members of two depots or of two flows, and a caller in the
// pane of a bead's agent (which may close only its own bead) are refused
// before anything is made, and a fold that fails after it leaves nothing
// behind. A manual group's claim is a person's turn, not a session at work, so
// a member it holds is folded and the close ends the claim.
//
// What git must read is read here, before the write: the repository flows
// for the flow of every member (the carrier is governed by the oldest's, and
// a refusal names them all), and for the gates: the members' own, which say
// what blocks them, and those their closes flip. The write reads the members
// again, so a member edited meanwhile is folded as it is.
func (s *Store) FoldBeads(ctx context.Context, ids []string, opts FoldOptions, actor string) (*Bead, error) {
	if len(ids) == 0 {
		return nil, fmt.Errorf("a fold needs beads to fold")
	}
	var pre []*beadRow
	err := s.read(func(tx *bolt.Tx) error {
		for _, id := range ids {
			row, err := loadBead(tx, id)
			if err != nil {
				return err
			}
			pre = append(pre, row)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	oldest := oldestOf(pre)
	reads, err := s.readsFor(ctx, oldest.Depot, oldest.Type, oldest.Metadata)
	if err != nil {
		return nil, err
	}
	// The flow of another member may need the depot's repository where the
	// oldest's does not, and a refusal that names each flow has to read it. (A
	// member of another depot is refused by the write before it uses it.)
	for _, m := range pre {
		if reads.repo != nil {
			break
		}
		if reads.repo, err = s.repoFor(ctx, m.Depot, m.Type, PinnedFlow(&m.Bead)); err != nil {
			return nil, err
		}
	}
	// The gates the write reads through a repository's flows: each member's
	// own, which say what holds it now, and those of the beads it blocks,
	// which its close flips.
	var gated []string
	for _, m := range pre {
		gated = append(gated, m.ID)
		gated = append(gated, s.blocksTargets(ctx, m.ID)...)
	}
	look := &untilLooks{repos: s.gateRepos(ctx, gated...), memo: flowMemo{}}
	var carrier *Bead
	err = s.write(func(w *wtx) error {
		members := make([]*beadRow, len(ids))
		for i, id := range ids {
			row, err := w.loadBead(id)
			if err != nil {
				return err
			}
			members[i] = row
		}
		row, err := w.fold(ctx, reads, look, members, nil, opts, "hand", actor)
		if err != nil {
			return err
		}
		carrier = &row.Bead
		return nil
	})
	if err != nil {
		return nil, err
	}
	return carrier, nil
}

// fold makes the carrier of members inside the write w, as actor: one bead,
// in opts.To, governed by the flow of the oldest member, whose body holds
// each member as a part, in the order given. Then, for each member, what it
// tracked, blocked and was watched for moves to the carrier (foldLinks,
// foldBlocks, copyWatches), a folded edge from the carrier records that, and
// the member closes. The carrier gets a note on where the members' notes and
// files are, and one folded event. reads are what readsFor read for the
// oldest member, look how the gates are read: the members' own, which a
// blocked member is refused by, and those the closes flip. left names the
// members' fellows the fold leaves where they are (a siding's ineligible
// beads, oldest first, as members are); a hand fold leaves none. trigger
// says what folded them ("hand", or what made a siding ready) and is
// written into the body.
//
// Everything that can be refused is refused before the first thing is made,
// and a failure after it rolls the whole write back.
func (w *wtx) fold(ctx context.Context, reads *beadReads, look *untilLooks, members []*beadRow, left []string, opts FoldOptions, trigger, actor string) (*beadRow, error) {
	if len(members) == 0 {
		return nil, fmt.Errorf("a fold needs beads to fold")
	}
	if opts.To == "" {
		return nil, fmt.Errorf("a fold needs the stage its carrier starts in")
	}
	seen := map[string]bool{}
	for _, m := range members {
		if seen[m.UUID] {
			return nil, fmt.Errorf("bead %s is named twice", m.ID)
		}
		seen[m.UUID] = true
	}
	author, err := paneAuthor(w.Tx, callerOf(ctx), actor)
	if err != nil {
		return nil, err
	}
	if look == nil {
		look = &untilLooks{memo: flowMemo{}}
	}
	for _, m := range members {
		if err := w.checkFoldable(ctx, m, seen, look); err != nil {
			return nil, err
		}
	}
	if err := checkFoldDepot(members); err != nil {
		return nil, err
	}
	oldest := oldestOf(members)
	f, src, err := checkFoldFlow(w.Tx, reads.repo, oldest, members)
	if err != nil {
		return nil, err
	}
	titles := make([]string, len(members))
	priority := members[0].Priority
	for i, m := range members {
		titles[i] = m.Title
		priority = min(priority, m.Priority) // 0 is the highest
	}
	nb := NewBead{
		Title:    foldTitle(oldest.Depot, titles),
		Body:     foldBody(members, trigger, author),
		Type:     oldest.Type, // the members' common type, when they have one
		Stage:    opts.To,
		Priority: priority,
		Labels:   foldLabels(members),
		// As the oldest member has it, empty or not (NewBead.originator).
		originator: &oldest.Originator,
	}
	if strings.TrimSpace(opts.Title) != "" {
		nb.Title = opts.Title
	}
	if oldest.Depot != nil {
		nb.Depot = *oldest.Depot
	}
	// A repository flow selected by type and depot already governs the
	// carrier naturally. Pinning its name would resolve store flows first,
	// which could change a repository default into a different store flow
	// with the same name. Other sources need a pin because the carrier's
	// type alone may resolve elsewhere.
	if f != nil && src.Kind != "repo" {
		nb.Metadata = map[string]any{"flow": f.Name}
	}
	// A hand fold names its carrier's stage, so it is a filing there, held
	// to the members gate as bead create -s is; a siding's fold is the yard
	// consolidating beads already in a stage, and is not.
	carrier, err := w.createBead(ctx, nb, author, reads, trigger == "hand")
	if err != nil {
		return nil, err
	}
	ids := make([]string, len(members))
	// What the carrier's note says: where each member's notes and files
	// are after its close, and the issues no bead tracks now.
	var listed, untracked []string
	for i, m := range members {
		ids[i] = m.ID
		cur, err := w.loadBead(m.UUID)
		if err != nil {
			return nil, err
		}
		moved := FoldMoved{Stage: cur.Stage}
		second, err := w.foldLinks(carrier, cur, &moved, author)
		if err != nil {
			return nil, err
		}
		untracked = append(untracked, second...)
		if err := w.foldBlocks(carrier, cur, &moved, look, author); err != nil {
			return nil, err
		}
		if moved.Watchers, err = copyWatches(w.Tx, cur.ID, carrier.ID); err != nil {
			return nil, err
		}
		// The edge is made before the close, so that it records the member
		// as it stood, and the member is read again after it, which
		// minted it.
		if err := w.putDep(carrier, cur, &depRow{Kind: DepFolded, Moved: &moved}, author); err != nil {
			return nil, err
		}
		if cur, err = w.loadBead(m.UUID); err != nil {
			return nil, err
		}
		if _, err := w.closeBead(cur, "folded into "+carrier.ID, author, look); err != nil {
			return nil, err
		}
		// Closing may add a stage ask's take-back answer, so list the notes
		// after the close that bead show will now show.
		notes, err := beadNotes(w.Tx, m.UUID)
		if err != nil {
			return nil, err
		}
		listed = append(listed, foldListing(m.ID, notes)...)
	}
	row, err := w.loadBead(carrier.UUID)
	if err != nil {
		return nil, err
	}
	if _, err := w.addNoted(row, NoteNote, foldNote(listed, untracked), author); err != nil {
		return nil, err
	}
	// A siding's fold comes from the stage its members wait in; a hand fold
	// from none.
	from := "-"
	if trigger != "hand" {
		from = oldest.Stage
	}
	if left == nil {
		left = []string{} // a hand fold leaves none, and the event carries a list either way
	}
	if err := w.event(carrier.UUID, "folded", author, map[string]any{
		"members": ids, "left": left, "from": from, "to": opts.To, "trigger": trigger, "points": foldPoints(members),
	}); err != nil {
		return nil, err
	}
	// The edges and the event minted the carrier again: it is answered as
	// stored.
	if _, err := get(w.Tx, bBeads, carrier.UUID, carrier); err != nil {
		return nil, err
	}
	return carrier, nil
}

// foldLinks moves the tracks links of member to the carrier, as MoveLink
// does, and notes each in moved. A bead tracks one issue per source, so a
// second issue of a source the carrier tracks already becomes a relates link
// there, and member keeps a relates link to it as well: no bead tracks the
// issue now, which the carrier's note says, since it will not move with the
// carrier's stage. foldLinks answers the lines that name those issues.
func (w *wtx) foldLinks(carrier, member *beadRow, moved *FoldMoved, actor string) ([]string, error) {
	own, err := beadLinks(w.Tx, member.UUID)
	if err != nil {
		return nil, err
	}
	// A bead tracks one issue per source, so the source is the order.
	slices.SortFunc(own, func(a, b linkRow) int { return strings.Compare(a.Source, b.Source) })
	var untracked []string
	for _, l := range own {
		if l.Kind != LinkTracks {
			continue
		}
		there, err := beadLinks(w.Tx, carrier.UUID)
		if err != nil {
			return nil, err
		}
		kind := LinkTracks
		if slices.ContainsFunc(there, func(c linkRow) bool { return c.Kind == LinkTracks && c.Source == l.Source }) {
			kind = LinkRelates
			untracked = append(untracked, fmt.Sprintf("%s tracked %s (%s)", member.ID, l.URL, l.Source))
		}
		// A link re-mints the bead it is made on: the carrier is read
		// again for each.
		to, err := w.loadBead(carrier.UUID)
		if err != nil {
			return nil, err
		}
		if _, err := w.moveLink(l.URL, to, kind, actor); err != nil {
			return nil, err
		}
		moved.Links = append(moved.Links, FoldLink{Source: l.Source, URL: l.URL, Kind: kind})
	}
	return untracked, nil
}

// foldBlocks re-points the blocks edges from member to the carrier, each
// with the stages it names, and notes each in moved: whatever the member held
// back, the carrier holds back now. A bead the carrier blocks in the same way
// already keeps that edge, which the member's other part shares. The gates
// this opens or closes are logged as any edge's are, so the member's close,
// with no edge left, flips none.
func (w *wtx) foldBlocks(carrier, member *beadRow, moved *FoldMoved, look *untilLooks, actor string) error {
	targets, before := openTargets(w.Tx, member, look)
	for _, d := range edges(w.Tx, bDepFrom, member.UUID, DepBlocks) {
		to, err := w.loadBead(d.To)
		if err != nil {
			return err
		}
		if !hasEdge(w.Tx, carrier.UUID, to.UUID, DepBlocks, d.At, d.Until) {
			if err := w.addDep(carrier, to, DepBlocks, d.At, d.Until, actor); err != nil {
				return err
			}
		}
		if _, err := w.removeDep(member, to, DepBlocks, d.At, d.Until, actor); err != nil {
			return err
		}
		moved.Blocks = append(moved.Blocks, FoldBlock{To: to.ID, At: d.At, Until: d.Until})
	}
	return w.logTargetsFlips(targets, before, look, actor)
}

// checkFoldable refuses a member a fold cannot take, saying why: a bead the
// caller may not close (checkCloseOwn, since a fold closes it), one already
// closed, one a session works (taking it would end that session's work), one
// of another yard's, which closing returns to its yard as refused, or takes
// back from it, one that is a child of a train, one an open bead blocks, in
// its stage or another, and one that is a train. A manual group's claim is
// a person's turn with
// nothing running, not a session at work, so the bead is taken and the close
// ends the claim, as it
// does for any bead.
//
// A train holds its child as a blocker holds a bead, and a fold leaves it on
// the member as it leaves the blocker: Train is derived from the parent edge,
// which stays on the closed member, so the carrier is a new bead outside the
// train. It would branch from and land on the depot's base and not the train's
// branch: it could not build on what the train's other children landed there,
// and its work would reach main alone, ahead of the train's own run and
// reviews, which is the one landing a train is for. So the fold refuses such a
// bead, naming the train, and the person or crew member who folds takes it off
// the train first and puts the carrier on it after (dep rm, dep add). Only a
// parent edge from a bead of type train counts (parentTrain), as for Family,
// and a closed train's child is refused as well: its base is the train's
// branch still.
//
// A fold re-points what a member blocks to the carrier, but what blocks the
// member stays on the member, which the fold closes: the carrier would route
// and build with that blocker still open. The siding design leaves such a
// bead where it waits (docs/design/2026-10-22-yardr-9x0n-siding.md, 3.5),
// and a fold by hand takes the same rule, widened to the carrier's whole
// future (unreleasedBlockers): an edge that holds the member only at
// another stage refuses too, since the carrier would pass that stage with
// the blocker open, and only an edge its blocker has released holds
// nothing and is not looked at. The refusal names the blocker and, where
// the edge holds at another stage, that stage, so it says what bead show,
// which answers whether the member can move now, does not. A blocker that
// is a member too (folding holds the members' UUIDs) closes in the same
// write and only orders parts of one bundle, so it does not refuse.
//
// A train is refused after what blocks it, so that a train an open child
// holds is refused as blocked. The carrier would take the oldest member's
// type, train, with none of the train's children on it, while the children
// keep their parent edges to the closed train: a hollow train no branch work
// rides, and children of a closed bead. A train lands by itself, so a fold
// takes none.
//
// look reads the gates of the blockers' until edges, as the read path does.
func (w *wtx) checkFoldable(ctx context.Context, m *beadRow, folding map[string]bool, look *untilLooks) error {
	if err := w.checkCloseOwn(ctx, m); err != nil {
		return fmt.Errorf("a fold closes its members: %w", err)
	}
	switch {
	case m.Status == "closed":
		return fmt.Errorf("bead %s is closed: a fold takes open beads", m.ID)
	case m.Group != nil && !heldManually(w.Tx, m): // as Claim has it: a claim sets a group and a session together
		return fmt.Errorf("bead %s is claimed by a session working it: a fold takes beads no session works", m.ID)
	}
	b := m.Bead
	if err := withPeer(w.Tx, &b); err != nil {
		return err
	}
	if p := b.From(); p != nil && p.ReturnedAt == nil {
		return fmt.Errorf("bead %s came from another yard and has not gone back to it: closing it would return it as refused; a fold takes this yard's own beads", m.ID)
	}
	if p := b.Away(); p != nil && p.ReturnedAt == nil {
		return fmt.Errorf("bead %s is away at another yard: a fold takes beads that are here", m.ID)
	}
	train, err := parentTrain(w.Tx, m.UUID)
	if err != nil {
		// A child of two trains, which only a type changed to train later
		// makes: it is a train's child twice over, and the error names both.
		return fmt.Errorf("bead %s: %w", m.ID, err)
	}
	if train != nil {
		return fmt.Errorf("bead %s is a child of train %s: a fold takes beads no train holds, since its carrier is made outside the train and would branch from and land on the depot's base, not the train's branch. Take the bead off the train first (yardr dep rm %s %s -k parent) and put the carrier on it after (yardr dep add %s <carrier> -k parent)",
			m.ID, train.ID, train.ID, m.ID, train.ID)
	}
	h := handlesIn(w.Tx)
	var blockers []string
	for _, d := range unreleasedBlockers(w.Tx, &m.Bead, look) {
		if folding[d.From] {
			continue
		}
		name := h.of(d.From)
		if d.At != "" && d.At != m.Stage {
			name += " at " + d.At
		}
		blockers = append(blockers, name)
	}
	if len(blockers) > 0 {
		// One entry per blocker and stage: several edges that hold the
		// member the same way name one.
		slices.Sort(blockers)
		return fmt.Errorf("bead %s is blocked by %s: a fold takes beads nothing blocks, since its carrier is made without a member's blockers and would be ready while they are open",
			m.ID, strings.Join(slices.Compact(blockers), ", "))
	}
	if m.Type == TypeTrain {
		return fmt.Errorf("bead %s is a train: a fold takes no train, which lands by itself", m.ID)
	}
	return nil
}

// heldManually reports whether the session that holds m is a manual group's
// claim (Session.Manual), a person's turn with nothing running. A claim whose
// session the store does not hold or cannot read is not known to be one, so
// it answers false and the fold refuses the bead.
func heldManually(tx *bolt.Tx, m *beadRow) bool {
	if m.Session == nil {
		return false
	}
	var sess Session
	ok, err := get(tx, bSessions, *m.Session, &sess)
	return err == nil && ok && sess.Manual()
}

// checkFoldDepot refuses members of more than one depot, naming each one's.
func checkFoldDepot(members []*beadRow) error {
	depot := func(m *beadRow) string {
		if m.Depot == nil {
			return "no depot"
		}
		return "depot " + *m.Depot
	}
	for _, m := range members[1:] {
		if depot(m) == depot(members[0]) {
			continue
		}
		in := make([]string, len(members))
		for i, m := range members {
			in[i] = fmt.Sprintf("%s in %s", m.ID, depot(m))
		}
		return fmt.Errorf("a fold takes the beads of one depot, and these are in more: %s", strings.Join(in, ", "))
	}
	return nil
}

// checkFoldFlow refuses members that more than one flow governs, naming each
// member's, and answers the flow of the oldest, which the carrier is governed
// by. Two sources can have the same flow name and different stage paths, so
// identity includes the resolved path. repo is the depot's repository flows
// as read before the write.
func checkFoldFlow(tx *bolt.Tx, repo *RepoFlows, oldest *beadRow, members []*beadRow) (*Flow, *FlowSource, error) {
	memo := flowMemo{}
	var f *Flow
	var src *FlowSource
	flows := make([]*Flow, len(members))
	sources := make([]*FlowSource, len(members))
	keys := make([]string, len(members))
	under := make([]string, len(members))
	byName := map[string]map[string]bool{}
	first := ""
	firstSet, apart := false, false
	for i, m := range members {
		mf, msrc, err := memo.resolve(tx, m.Depot, m.Type, PinnedFlow(&m.Bead), repo)
		if err != nil {
			return nil, nil, fmt.Errorf("bead %s: %w", m.ID, changedMeanwhile(err))
		}
		flows[i], sources[i] = mf, msrc
		if m == oldest {
			f, src = mf, msrc
		}
		identity := ""
		if mf == nil {
			under[i] = "no flow"
		} else {
			path := ""
			if msrc != nil {
				path = msrc.Path
			}
			identity = mf.Name + "\x00" + path
			if byName[mf.Name] == nil {
				byName[mf.Name] = map[string]bool{}
			}
			byName[mf.Name][identity] = true
			under[i] = "flow " + mf.Name
		}
		keys[i] = identity
		if !firstSet {
			first, firstSet = keys[i], true
		} else {
			apart = apart || keys[i] != first
		}
	}
	if apart {
		for i, m := range members {
			if flows[i] != nil && len(byName[flows[i].Name]) > 1 {
				where := "store"
				if sources[i] != nil && sources[i].Path != "" {
					where = sources[i].Path
				}
				under[i] += " (" + where + ")"
			}
			under[i] = fmt.Sprintf("%s under %s", m.ID, under[i])
		}
		return nil, nil, fmt.Errorf("a fold takes the beads of one flow, and these follow more: %s", strings.Join(under, ", "))
	}
	return f, src, nil
}

// oldestOf is the member created first, by creation time and then by UUID,
// which is the order beads were made in.
func oldestOf(members []*beadRow) *beadRow {
	oldest := members[0]
	for _, m := range members[1:] {
		if m.CreatedAt.Before(oldest.CreatedAt) || (m.CreatedAt.Equal(oldest.CreatedAt) && m.UUID < oldest.UUID) {
			oldest = m
		}
	}
	return oldest
}

// foldTitleMax is the longest title a fold gives its carrier, the count of
// what it leaves out included.
const foldTitleMax = 100

// foldTitle names a carrier after its members: "<depot> bundle: <title>;
// <title>". One that would pass foldTitleMax keeps the titles that fit whole
// and ends in " (+n more)", n counting the members it leaves out. Only a
// first title that is too long on its own is cut, on a word, with the same
// ending.
func foldTitle(depot *string, titles []string) string {
	head := "bundle: "
	if depot != nil {
		head = *depot + " " + head
	}
	if full := head + strings.Join(titles, "; "); utf8.RuneCountInString(full) <= foldTitleMax {
		return full
	}
	more := func(left int) string {
		if left == 0 {
			return ""
		}
		return fmt.Sprintf(" (+%d more)", left)
	}
	fits := func(s string, left int) bool { return utf8.RuneCountInString(s+more(left)) <= foldTitleMax }
	title := head + titles[0]
	if !fits(title, len(titles)-1) {
		room := foldTitleMax - utf8.RuneCountInString(more(len(titles)-1))
		return cutOnWord(title, room) + more(len(titles)-1)
	}
	// The last title is not tried: it would fit whole only if all did.
	shown := 1
	for ; shown < len(titles)-1; shown++ {
		next := title + "; " + titles[shown]
		if !fits(next, len(titles)-shown-1) {
			break
		}
		title = next
	}
	return title + more(len(titles)-shown)
}

// foldBody is the carrier's body, in the shape the shaper gives a bundle: a
// part for each member, with the member's body as it is, and what holds for
// all of them.
func foldBody(members []*beadRow, trigger, actor string) string {
	sections := []string{"Goal: the parts below ride one bead: one builder session and one review for all of them."}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.ID
		part := fmt.Sprintf("## Part %d: %s (%s)", i+1, m.Title, m.ID)
		if m.Body != "" {
			part += "\n\n" + m.Body
		}
		sections = append(sections, part)
	}
	sections = append(sections, fmt.Sprintf("## The bundle\n\n"+
		"Done when every part's done-when holds and the depot's test command is\n"+
		"green. Each part is its own commit, its message starting with the part's\n"+
		"bead id, so one part can be taken out alone (`yardr bead unfold`).\n"+
		"Folded from %s by %s (%s).", strings.Join(ids, ", "), actor, trigger))
	return strings.Join(sections, "\n\n")
}

// foldListing says, for the carrier's note, how many notes the member id
// holds, of every kind, as bead show prints them, and, for each that carries
// files, which, by name and note. The files are not copied: a note's file is
// the record, and stays with the note, on the member.
func foldListing(id string, notes []Note) []string {
	count := "no notes"
	switch len(notes) {
	case 0:
	case 1:
		count = "1 note"
	default:
		count = fmt.Sprintf("%d notes", len(notes))
	}
	lines := []string{id + ": " + count}
	for _, n := range notes {
		names := make([]string, len(n.Files))
		for i, f := range n.Files {
			names[i] = foldFileName(f)
		}
		if len(names) > 0 {
			lines = append(lines, fmt.Sprintf("%s note %d: %s", id, n.Seq, strings.Join(names, ", ")))
		}
	}
	return lines
}

// foldFileName is a file's name where the carrier's note lists it: as the
// files line shows it, and quoted when it holds a quote, a parenthesis or the
// comma that lists names, so that what a sender names a file never reads as
// another's. A file with no name is its kind and the start of its hash.
func foldFileName(f BlobRef) string {
	name := Letters(f.Name)
	switch {
	case name == "":
		return f.Kind() + " " + f.Hash[:min(len(f.Hash), 12)]
	case strings.ContainsAny(name, `()",`):
		return strconv.Quote(name)
	}
	return name
}

// foldNote is the note the carrier gets, for its builder to know where to
// look: listed is foldListing of each member, untracked the lines that name
// the issues foldLinks left with no tracker.
func foldNote(listed, untracked []string) string {
	text := "Each part's notes and files stay on the part: read them with yardr bead show <part>.\n\n" + strings.Join(listed, "\n")
	if len(untracked) > 0 {
		text += "\n\nA bead tracks one issue of a source, so these are linked to this bead and tracked by none, " +
			"and will not move with its stage:\n" + strings.Join(untracked, "\n")
	}
	return text
}

// sizePoints is the points a bead with these labels adds to a fold, by its
// size: label (the yardmaster's rubric): small 1, medium 2, large 4, and 2
// for a bead with none of those. A bead with more than one size label
// counts the largest it gives, so a fold never undercounts a bead for the
// order its labels were read in.
func sizePoints(labels []string) int {
	best := 0
	for _, l := range labels {
		switch l {
		case "size:small":
			best = max(best, 1)
		case "size:medium":
			best = max(best, 2)
		case "size:large":
			best = max(best, 4)
		}
	}
	if best == 0 {
		return 2
	}
	return best
}

// unreadableEstimate says whether labels carry no estimate a fold can read:
// no size: label, an unknown one, or more than one. Such a bead still
// counts (sizePoints), but a refusal names it, so the yardmaster sees whose
// estimate to fix.
func unreadableEstimate(labels []string) bool {
	var sizes []string
	for _, l := range labels {
		if strings.HasPrefix(l, "size:") {
			sizes = append(sizes, l)
		}
	}
	if len(sizes) != 1 {
		return true
	}
	switch sizes[0] {
	case "size:small", "size:medium", "size:large":
		return false
	}
	return true
}

// foldPoints are the points a fold carries: its members' sizePoints, summed.
func foldPoints(members []*beadRow) int {
	points := 0
	for _, m := range members {
		points += sizePoints(m.Labels)
	}
	return points
}

// foldLabels are the carrier's: the members' labels but their estimates,
// then its own. Its difficulty: is the highest of theirs (none when they
// have none), since one hard part does not add up with easy ones; its size:
// is from their points, 1 small, 2 to 4 medium, 5 or more large, which is
// how a bundle of small beads has been sized by hand.
func foldLabels(members []*beadRow) []string {
	var labels []string
	hardest := 0
	for _, m := range members {
		for _, l := range m.Labels {
			switch level, isDifficulty := strings.CutPrefix(l, "difficulty:"); {
			case isDifficulty:
				if n, err := strconv.Atoi(level); err == nil {
					hardest = max(hardest, n)
				}
			case !strings.HasPrefix(l, "size:"):
				labels = append(labels, l)
			}
		}
	}
	if hardest > 0 {
		labels = append(labels, "difficulty:"+strconv.Itoa(hardest))
	}
	size, points := "large", foldPoints(members)
	switch {
	case points <= 1:
		size = "small"
	case points <= 4:
		size = "medium"
	}
	return append(labels, "size:"+size)
}
