package store

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

// Link kinds. A bead that tracks an issue is the one whose stage moves it;
// a bead that relates to an issue only shows it.
const (
	LinkTracks  = "tracks"
	LinkRelates = "relates"
)

var (
	bLinks     = []byte("links")      // <link id>
	bLinkBead  = []byte("link_bead")  // <bead>\x00<link id>
	bLinkIssue = []byte("link_issue") // <issue url>\x00<link id>
)

func init() { allBuckets = append(allBuckets, bLinks, bLinkBead, bLinkIssue) }

// Link is a bead's link to an issue outside the yard (a Linear issue, a
// GitHub issue). It is its own record, not an edge: an edge joins two
// beads, and the issue is no bead.
type Link struct {
	Bead   string `json:"bead"` // stored as the UUID, returned as the handle
	Kind   string `json:"kind"`
	Source string `json:"source"`
	// URL is the issue in its source's fixed form (hooks.Issue, which the
	// store reaches through IssueForm): the store takes two links to be to
	// the same issue exactly when their URLs are equal.
	URL string `json:"url"`
	// Thread is the issue's thread id in the source, as its deliveries name
	// it; empty until a link to the issue was made from a delivery.
	Thread    string    `json:"thread,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitempty"`
	// Of a row for the bead's counterpart in another yard (Bead.PeerLinks),
	// which is read from the record of the pair and stored nowhere: its
	// Kind is LinkAt, LinkWasAt or LinkFrom, its Source SourcePeer and its
	// URL <the other yard's handle>@<peer>, "" until that yard has named
	// its bead. Peer is the yard as it is called today, Stage the stage of
	// that yard a bead that came from it is worked for, CreatedAt when the
	// bead was sent or came, and Until when its stay there ended.
	Peer  string     `json:"peer,omitempty"`
	Stage string     `json:"stage,omitempty"`
	Until *time.Time `json:"until,omitempty"`
}

// NewLink is a link to make. URL is the issue's in any spelling: the store
// brings it to the source's fixed form (IssueForm).
type NewLink struct {
	Kind   string
	Source string
	URL    string
	Thread string
}

// linkRow is a stored link.
type linkRow struct {
	ID        string    `json:"id"`
	Bead      string    `json:"bead"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	Thread    string    `json:"thread,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by"`
	Revision  string    `json:"revision"`
	LastActor string    `json:"last_actor"`
}

// LinkChange is what AddLink or MoveLink did.
type LinkChange struct {
	Link Link `json:"link"`
	// Replaced is the kind of the link the bead had to the issue before,
	// when this one took its place.
	Replaced string `json:"replaced,omitempty"`
	// From is the bead that tracked the issue before a move; it keeps a
	// relates link.
	From string `json:"from,omitempty"`
}

// issueForm turns an issue's URL into its source's fixed form. Until
// IssueForm sets it, every URL is its own fixed form.
var issueForm = func(source, raw string) (string, error) { return raw, nil }

// IssueForm sets the function that brings an issue's URL to the one
// spelling its source stores and compares, so a caller that did not fix the
// URL itself (any client of the socket but the CLI) cannot link one issue
// under two. The sources are in internal/hooks, which imports this package
// and so registers the function here, from its init: a process that serves
// a store links internal/hooks. f is given the link's source, or "" where
// only the URL is known and the source is told by its host; it returns raw
// as it is for a source, or a host, that has no fixed form.
func IssueForm(f func(source, raw string) (string, error)) { issueForm = f }

// fixedIssue is the issue raw names in its fixed form, for finding the
// links to it. What has no fixed form is looked up as given: it matches
// nothing made since IssueForm, and a link from before it can still be
// found and removed.
func fixedIssue(raw string) string {
	if fixed, err := issueForm("", raw); err == nil {
		return fixed
	}
	return raw
}

// checkLink checks the link and brings its URL to the source's fixed form.
func checkLink(nl *NewLink) error {
	if nl.Source == SourcePeer || PeerIssue(nl.URL) {
		return PeerRowRefusal(nl.URL)
	}
	if nl.Kind != LinkTracks && nl.Kind != LinkRelates {
		return fmt.Errorf("link kind %q is neither %s nor %s", nl.Kind, LinkTracks, LinkRelates)
	}
	if err := checkName("link source", nl.Source); err != nil {
		return err
	}
	fixed, err := issueForm(nl.Source, nl.URL)
	if err != nil {
		return err
	}
	nl.URL = fixed
	return checkIssueURL(nl.URL)
}

// checkIssueURL takes any absolute http(s) URL: which URLs are issues, and
// their one spelling, is the source's to say. NUL would break the index key.
func checkIssueURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsRune(raw, 0) {
		return fmt.Errorf("issue %q: want the issue's URL", raw)
	}
	return nil
}

// AddLink links a bead to an issue. A bead has one link per issue: the
// other kind to an issue it already links replaces that link, reported in
// LinkChange.Replaced. For kind tracks it refuses a bead that already
// tracks an issue of the source, and an issue another bead tracks. The
// bead's representation lists its links, so it is re-minted, as by an edge.
func (s *Store) AddLink(ctx context.Context, ref string, nl NewLink, actor string) (*LinkChange, error) {
	if err := checkLink(&nl); err != nil {
		return nil, err
	}
	var out *LinkChange
	err := s.write(func(w *wtx) error {
		row, err := w.loadBead(ref)
		if err != nil {
			return err
		}
		l, replaced, err := w.addLink(row, nl, actor, nil)
		if err != nil {
			return err
		}
		out = &LinkChange{Link: l.link(row.ID), Replaced: replaced}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// addLink is AddLink on a loaded bead, which it re-mints. extra is added to
// the linked event's data.
func (w *wtx) addLink(row *beadRow, nl NewLink, actor string, extra map[string]any) (*linkRow, string, error) {
	own, err := beadLinks(w.Tx, row.UUID)
	if err != nil {
		return nil, "", err
	}
	var old *linkRow
	for i := range own {
		if own[i].URL == nl.URL {
			old = &own[i]
		}
	}
	if old != nil && old.Kind == nl.Kind {
		return nil, "", fmt.Errorf("%s already %s %s", row.ID, linkVerb(nl.Kind), nl.URL)
	}
	others, err := issueLinks(w.Tx, nl.URL)
	if err != nil {
		return nil, "", err
	}
	if nl.Kind == LinkTracks {
		for _, l := range own {
			if l.Kind == LinkTracks && l.Source == nl.Source && l.URL != nl.URL {
				return nil, "", fmt.Errorf("%s already tracks %s, and a bead tracks one %s issue: remove that link first (yardr link rm %s %s), or link this one with --relates",
					row.ID, l.URL, nl.Source, row.ID, l.URL)
			}
		}
		h := handlesIn(w.Tx)
		for _, l := range others {
			if l.Kind == LinkTracks && l.Bead != row.UUID {
				return nil, "", fmt.Errorf("%s is tracked by %s, and an issue has one tracking bead: move the link (yardr link move %s --to %s), or link %s with --relates",
					nl.URL, h.of(l.Bead), nl.URL, row.ID, row.ID)
			}
		}
	}
	// The thread id comes with a link made from a delivery; a link made from
	// the URL takes it from one that has it.
	for _, l := range others {
		if nl.Thread == "" {
			nl.Thread = l.Thread
		}
	}
	replaced := ""
	if old != nil {
		replaced = old.Kind
		if err := w.dropLink(old); err != nil {
			return nil, "", err
		}
		if err := w.event(row.UUID, "unlinked", actor, linkData(old, map[string]any{"replaced_by": nl.Kind})); err != nil {
			return nil, "", err
		}
	}
	// Ids are never reused, and a link of the other kind is another link:
	// what serves a link (BDP) takes its kind to be fixed.
	l := &linkRow{ID: newEdgeID(), Bead: row.UUID, Kind: nl.Kind, Source: nl.Source, URL: nl.URL, Thread: nl.Thread,
		CreatedAt: now(), CreatedBy: actor, Revision: newRevision(), LastActor: actor}
	if err := put(w.Tx, bLinks, l.ID, l); err != nil {
		return nil, "", err
	}
	if err := w.Bucket(bLinkBead).Put([]byte(join(l.Bead, l.ID)), []byte{}); err != nil {
		return nil, "", err
	}
	if err := w.Bucket(bLinkIssue).Put([]byte(join(l.URL, l.ID)), []byte{}); err != nil {
		return nil, "", err
	}
	if err := w.mintBead(row, actor); err != nil {
		return nil, "", err
	}
	data := map[string]any{}
	for k, v := range extra {
		data[k] = v
	}
	if replaced != "" {
		data["replaced"] = replaced
	}
	return l, replaced, w.event(row.UUID, "linked", actor, linkData(l, data))
}

// linkData is a linked or unlinked event's data: the link, and extra.
func linkData(l *linkRow, extra map[string]any) map[string]any {
	data := map[string]any{"kind": l.Kind, "source": l.Source, "url": l.URL}
	if l.Thread != "" {
		data["thread"] = l.Thread
	}
	for k, v := range extra {
		data[k] = v
	}
	return data
}

func linkVerb(kind string) string {
	if kind == LinkRelates {
		return "relates to"
	}
	return kind
}

// dropLink deletes the link and its index entries.
func (w *wtx) dropLink(l *linkRow) error {
	for _, del := range []struct {
		b   []byte
		key string
	}{{bLinks, l.ID}, {bLinkBead, join(l.Bead, l.ID)}, {bLinkIssue, join(l.URL, l.ID)}} {
		if err := w.Bucket(del.b).Delete([]byte(del.key)); err != nil {
			return err
		}
	}
	return nil
}

// RemoveLink deletes the bead's link to the issue, of either kind; the
// unlinked event is its record.
func (s *Store) RemoveLink(ctx context.Context, ref, issue, actor string) error {
	if PeerIssue(issue) {
		return PeerRowRefusal(issue)
	}
	issue = fixedIssue(issue)
	return s.write(func(w *wtx) error {
		row, err := w.loadBead(ref)
		if err != nil {
			return err
		}
		own, err := beadLinks(w.Tx, row.UUID)
		if err != nil {
			return err
		}
		for i := range own {
			l := &own[i]
			if l.URL != issue {
				continue
			}
			if err := w.dropLink(l); err != nil {
				return err
			}
			if err := w.mintBead(row, actor); err != nil {
				return err
			}
			return w.event(row.UUID, "unlinked", actor, linkData(l, nil))
		}
		return fmt.Errorf("%s has no link to %s: %w", row.ID, issue, ErrNotFound)
	})
}

// MoveLink makes the bead toRef the issue's tracker in place of the bead
// that tracks it, in one write, so the issue is never without a tracker nor
// with two: for work that grew into a train. The earlier tracker keeps a
// relates link. It refuses an issue no bead tracks (AddLink is for that)
// and, as AddLink does, a bead that already tracks another issue of the
// source.
func (s *Store) MoveLink(ctx context.Context, issue, toRef, actor string) (*LinkChange, error) {
	if PeerIssue(issue) {
		return nil, PeerRowRefusal(issue)
	}
	issue = fixedIssue(issue)
	var out *LinkChange
	err := s.write(func(w *wtx) error {
		to, err := w.loadBead(toRef)
		if err != nil {
			return err
		}
		ls, err := issueLinks(w.Tx, issue)
		if err != nil {
			return err
		}
		var tracker *linkRow
		for i := range ls {
			if ls[i].Kind == LinkTracks {
				tracker = &ls[i]
			}
		}
		if tracker == nil {
			return fmt.Errorf("no bead tracks %s, so there is no link to move (yardr link add %s %s makes one): %w", issue, to.ID, issue, ErrNotFound)
		}
		if tracker.Bead == to.UUID {
			return fmt.Errorf("%s already tracks %s", to.ID, issue)
		}
		from, err := w.loadBead(tracker.Bead)
		if err != nil {
			return err
		}
		nl := NewLink{Kind: LinkRelates, Source: tracker.Source, URL: tracker.URL, Thread: tracker.Thread}
		if _, _, err := w.addLink(from, nl, actor, map[string]any{"moved_to": to.ID}); err != nil {
			return err
		}
		nl.Kind = LinkTracks
		l, replaced, err := w.addLink(to, nl, actor, map[string]any{"moved_from": from.ID})
		if err != nil {
			return err
		}
		out = &LinkChange{Link: l.link(to.ID), Replaced: replaced, From: from.ID}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MoveLinkSource moves the yard's links of source from to source to in one
// write, for a yard whose source moved (a builtin one to a declared one on
// the same host). The URLs stay as they are, brought to the new source's
// fixed form where it has one; a URL the new source takes no issue at is
// refused, and so is a bead that would then track two issues of the new
// source, naming the bead and the issue. Each moved link logs unlinked and
// linked on its bead, and the bead is re-minted. Moving back undoes it.
func (s *Store) MoveLinkSource(ctx context.Context, from, to, actor string) (int, error) {
	if from == "" || to == "" {
		return 0, fmt.Errorf("move links from a source to a source, not from %q to %q", from, to)
	}
	if from == to {
		return 0, fmt.Errorf("move links from %s to itself: give two sources", from)
	}
	if err := checkName("link source", to); err != nil {
		return 0, err
	}
	// No link is stored under the source peer, and none is to be: its rows
	// are read from the records of the pairs.
	if from == SourcePeer || to == SourcePeer {
		return 0, fmt.Errorf("move links from %s to %s: the rows of the source %s are read from this yard's records of its beads at other yards and are no links anyone made, so nothing moves them", from, to, SourcePeer)
	}
	var moved int
	err := s.write(func(w *wtx) error {
		var all []linkRow
		if err := each(w.Tx, bLinks, "", func(_ []byte, l *linkRow) error {
			all = append(all, *l)
			return nil
		}); err != nil {
			return err
		}
		var targets []linkRow
		for _, l := range all {
			if l.Source == from {
				targets = append(targets, l)
			}
		}
		if len(targets) == 0 {
			moved = 0
			return nil
		}
		fixed := map[string]string{}
		for _, l := range targets {
			f, err := issueForm(to, l.URL)
			if err != nil {
				return err
			}
			if err := checkIssueURL(f); err != nil {
				return err
			}
			fixed[l.ID] = f
		}
		byBead := map[string][]linkRow{}
		for _, l := range all {
			byBead[l.Bead] = append(byBead[l.Bead], l)
		}
		h := handlesIn(w.Tx)
		for _, t := range targets {
			own := byBead[t.Bead]
			seen := map[string]string{}
			for _, l := range own {
				url, source := l.URL, l.Source
				if l.ID == t.ID {
					url, source = fixed[l.ID], to
				} else if l.Source == from {
					url = fixed[l.ID]
					source = to
				}
				if was, dup := seen[url]; dup {
					return fmt.Errorf("%s links %s twice after the move (as %s and %s): remove one first", h.of(t.Bead), url, was, l.Kind)
				}
				seen[url] = l.Kind
				_ = source
			}
			tracks := map[string][]string{}
			for _, l := range own {
				url, source := l.URL, l.Source
				if l.Source == from {
					url = fixed[l.ID]
					source = to
				}
				if l.Kind == LinkTracks {
					tracks[source] = append(tracks[source], url)
				}
			}
			if len(tracks[to]) > 1 {
				return fmt.Errorf("%s would track two %s issues after the move (%s and %s): remove one link first", h.of(t.Bead), to, tracks[to][0], tracks[to][1])
			}
		}
		minted := map[string]*beadRow{}
		for i := range targets {
			l := &targets[i]
			old := *l
			l.Source, l.URL = to, fixed[l.ID]
			l.Revision, l.LastActor = newRevision(), actor
			if err := put(w.Tx, bLinks, l.ID, l); err != nil {
				return err
			}
			if old.URL != l.URL {
				if err := w.Bucket(bLinkIssue).Delete([]byte(join(old.URL, l.ID))); err != nil {
					return err
				}
				if err := w.Bucket(bLinkIssue).Put([]byte(join(l.URL, l.ID)), []byte{}); err != nil {
					return err
				}
			}
			row := minted[l.Bead]
			if row == nil {
				var err error
				if row, err = w.loadBead(l.Bead); err != nil {
					return err
				}
				minted[l.Bead] = row
			}
			if err := w.mintBead(row, actor); err != nil {
				return err
			}
			if err := w.event(row.UUID, "unlinked", actor, linkData(&old, map[string]any{"moved_source_to": to})); err != nil {
				return err
			}
			if err := w.event(row.UUID, "linked", actor, linkData(l, map[string]any{"moved_source_from": from})); err != nil {
				return err
			}
		}
		moved = len(targets)
		return nil
	})
	if err != nil {
		return 0, err
	}
	return moved, nil
}

// handOn clears the way for the new bead to to track the issue at url: a
// closed bead that tracks it keeps a relates link instead, as the earlier
// tracker does after MoveLink. An open one stays the tracker, and the error
// is ErrConflict.
func (w *wtx) handOn(url string, to *beadRow, actor string) error {
	ls, err := issueLinks(w.Tx, url)
	if err != nil {
		return err
	}
	for i := range ls {
		l := &ls[i]
		if l.Kind != LinkTracks {
			continue
		}
		from, err := w.loadBead(l.Bead)
		if err != nil {
			return err
		}
		if from.Status != "closed" {
			return fmt.Errorf("%s is tracked by %s: %w", url, from.ID, ErrConflict)
		}
		nl := NewLink{Kind: LinkRelates, Source: l.Source, URL: l.URL, Thread: l.Thread}
		if _, _, err := w.addLink(from, nl, actor, map[string]any{"moved_to": to.ID}); err != nil {
			return err
		}
	}
	return nil
}

// Links lists the bead's links, oldest first: the stored ones. The rows for
// its counterpart in another yard are no links and are read off the bead
// (Bead.PeerLinks).
func (s *Store) Links(ctx context.Context, ref string) ([]Link, error) {
	out := []Link{}
	err := s.read(func(tx *bolt.Tx) error {
		row, err := loadBead(tx, ref)
		if err != nil {
			return err
		}
		ls, err := beadLinks(tx, row.UUID)
		for i := range ls {
			out = append(out, ls[i].link(row.ID))
		}
		return err
	})
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, err
}

// IssueLinks lists the links to an issue, the tracking bead's first, then
// oldest first. For a bead of another yard, named <handle>@<peer>
// (PeerIssue), it lists the rows of this yard's beads it is the
// counterpart of.
func (s *Store) IssueLinks(ctx context.Context, issue string) ([]Link, error) {
	if PeerIssue(issue) {
		var out []Link
		err := s.read(func(tx *bolt.Tx) (err error) {
			out, err = peerIssueLinks(tx, issue)
			return err
		})
		return out, err
	}
	issue = fixedIssue(issue)
	out := []Link{}
	err := s.read(func(tx *bolt.Tx) error {
		ls, err := issueLinks(tx, issue)
		h := handlesIn(tx)
		for i := range ls {
			out = append(out, ls[i].link(h.of(ls[i].Bead)))
		}
		return err
	})
	sort.SliceStable(out, func(i, j int) bool {
		if a, b := out[i].Kind == LinkTracks, out[j].Kind == LinkTracks; a != b {
			return a
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, err
}

func (l *linkRow) link(handle string) Link {
	return Link{Bead: handle, Kind: l.Kind, Source: l.Source, URL: l.URL, Thread: l.Thread,
		CreatedAt: l.CreatedAt, CreatedBy: l.CreatedBy}
}

// beadLinks reads the links of the bead with UUID u.
func beadLinks(tx *bolt.Tx, u string) ([]linkRow, error) { return linksUnder(tx, bLinkBead, u) }

// issueLinks reads the links to the issue at url.
func issueLinks(tx *bolt.Tx, url string) ([]linkRow, error) { return linksUnder(tx, bLinkIssue, url) }

func linksUnder(tx *bolt.Tx, index []byte, owner string) ([]linkRow, error) {
	var out []linkRow
	for _, k := range keys(tx, index, owner+"\x00") {
		var l linkRow
		if _, err := get(tx, bLinks, k[len(owner)+1:], &l); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}
