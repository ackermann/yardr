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

// foldBeads makes one carrier of the beads ids, in one write: a bead in
// opts.To that holds each of them as a part. It makes the carrier and
// nothing else yet, which is why nothing outside the store can call it.
//
// What git must read for the carrier's flow is read here, before the write,
// for the oldest member, whose flow the carrier is governed by; the write
// reads the members again, so a member edited meanwhile is folded as it is.
func (s *Store) foldBeads(ctx context.Context, ids []string, opts FoldOptions, actor string) (*Bead, error) {
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
		row, err := w.fold(ctx, reads, members, opts, "hand", actor)
		if err != nil {
			return err
		}
		carrier = &row.Bead
		return nil
	})
	return carrier, err
}

// fold makes the carrier of members inside the write w, as actor: one bead,
// in opts.To, governed by the flow of the oldest member, whose body holds
// each member as a part, in the order given. reads are what readsFor read
// for that member. trigger says what folded them and is written into the
// body.
func (w *wtx) fold(ctx context.Context, reads *beadReads, members []*beadRow, opts FoldOptions, trigger, actor string) (*beadRow, error) {
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
	oldest := oldestOf(members)
	f, src, err := resolveFlow(w.Tx, oldest.Depot, oldest.Type, PinnedFlow(&oldest.Bead), reads.repo)
	if err != nil {
		return nil, changedMeanwhile(err)
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
	// The carrier leaves under the flow the members are in, which their
	// type alone need not say: a member pinned to F whose type resolves to
	// G is in F. A type's repository flow has no name a pin could find
	// (RepoFlows.find reads named flows), but it governs by type, and the
	// carrier has the type.
	if f != nil && !(src.Kind == "repo" && src.Type != "") {
		nb.Metadata = map[string]any{"flow": f.Name}
	}
	return w.createBead(ctx, nb, author, reads)
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
// and ends in " (+n more)" for the members it names no more; only when the
// first title alone is too long is it cut, on a word.
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
// for a bead with none.
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

// foldLabels are the carrier's: the members' labels but their estimates,
// then its own. Its difficulty: is the highest of theirs (none when they
// have none), since one hard part does not add up with easy ones; its size:
// is from their points, 1 small, 2 to 4 medium, 5 or more large, which is
// how a bundle of small beads has been sized by hand.
func foldLabels(members []*beadRow) []string {
	var labels []string
	hardest, points := 0, 0
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
		points += sizePoints(m.Labels)
	}
	if hardest > 0 {
		labels = append(labels, "difficulty:"+strconv.Itoa(hardest))
	}
	size := "large"
	switch {
	case points <= 1:
		size = "small"
	case points <= 4:
		size = "medium"
	}
	return append(labels, "size:"+size)
}
