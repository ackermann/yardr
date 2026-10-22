package store

import (
	"context"
	"fmt"
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
// shared a target. A fold fills it in as each thing is taken from the member;
// so far that is the close.
type FoldMoved struct {
	// Stage is the stage the member stood in when the fold closed it.
	Stage string `json:"stage"`
}

// FoldBeads folds the beads ids into one carrier, in one write: a bead in
// opts.To that holds each of them as a part, a folded edge from it to each,
// and each of them closed "folded into <carrier>". It is all or nothing. A
// member that is closed, claimed or another yard's, members of two depots or
// of two flows, and a caller in the pane of a bead's agent (which may close
// only its own bead) are refused before anything is made, and a fold that
// fails after it leaves nothing behind.
//
// What git must read is read here, before the write: the repository flows
// for the flow of every member (the carrier is governed by the oldest's, and
// a refusal names them all), and for the gates the members' closes flip. The
// write reads the members again, so a member edited meanwhile is folded as it
// is.
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
	var targets []string
	for _, m := range pre {
		targets = append(targets, s.blocksTargets(ctx, m.ID)...)
	}
	look := &untilLooks{repos: s.gateRepos(ctx, targets...), memo: flowMemo{}}
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
		row, err := w.fold(ctx, reads, look, members, opts, "hand", actor)
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
// each member as a part, in the order given. Then, for each member, a folded
// edge from the carrier and the member's close, and one folded event on the
// carrier. reads are what readsFor read for the oldest member, look how the
// gates the closes flip are read. trigger says what folded them ("hand", or
// what made a siding ready) and is written into the body.
//
// Everything that can be refused is refused before the first thing is made,
// and a failure after it rolls the whole write back.
func (w *wtx) fold(ctx context.Context, reads *beadReads, look *untilLooks, members []*beadRow, opts FoldOptions, trigger, actor string) (*beadRow, error) {
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
	for _, m := range members {
		if err := w.checkFoldable(ctx, m); err != nil {
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
	carrier, err := w.createBead(ctx, nb, author, reads)
	if err != nil {
		return nil, err
	}
	if look == nil {
		look = &untilLooks{memo: flowMemo{}}
	}
	ids := make([]string, len(members))
	for i, m := range members {
		ids[i] = m.ID
		// The edge is made before the close, so that it records the member
		// as it stood, and the member is read again after it, which
		// minted it.
		cur, err := w.loadBead(m.UUID)
		if err != nil {
			return nil, err
		}
		if err := w.putDep(carrier, cur, &depRow{Kind: DepFolded, Moved: &FoldMoved{Stage: cur.Stage}}, author); err != nil {
			return nil, err
		}
		if cur, err = w.loadBead(m.UUID); err != nil {
			return nil, err
		}
		if _, err := w.closeBead(cur, "folded into "+carrier.ID, author, look); err != nil {
			return nil, err
		}
	}
	// A siding's fold comes from the stage its members wait in; a hand fold
	// from none.
	from := "-"
	if trigger != "hand" {
		from = oldest.Stage
	}
	if err := w.event(carrier.UUID, "folded", author, map[string]any{
		"members": ids, "left": []string{}, "from": from, "to": opts.To, "trigger": trigger, "points": foldPoints(members),
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

// checkFoldable refuses a member a fold cannot take, saying why: a bead the
// caller may not close (checkCloseOwn, since a fold closes it), one already
// closed, one a session has claimed (taking it would end that session's
// work), and one of another yard's, which closing returns to its yard as
// refused, or takes back from it.
func (w *wtx) checkFoldable(ctx context.Context, m *beadRow) error {
	if err := w.checkCloseOwn(ctx, m); err != nil {
		return fmt.Errorf("a fold closes its members: %w", err)
	}
	switch {
	case m.Status == "closed":
		return fmt.Errorf("bead %s is closed: a fold takes open beads", m.ID)
	case m.Group != nil: // as Claim has it: a claim sets a group and a session together
		return fmt.Errorf("bead %s is claimed: a fold takes beads no session has", m.ID)
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
	return nil
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

// sizePoints is the points a bead with these labels adds to a fold, by its
// size: label (the yardmaster's rubric): small 1, medium 2, large 4, and 2
// for a bead with none of those.
func sizePoints(labels []string) int {
	for _, l := range labels {
		switch l {
		case "size:small":
			return 1
		case "size:medium":
			return 2
		case "size:large":
			return 4
		}
	}
	return 2
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
