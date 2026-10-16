package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

const (
	linA = "https://linear.app/acme/issue/YRD-1"
	linB = "https://linear.app/acme/issue/YRD-2"
	ghA  = "https://github.com/acme/webapp/issues/7"
)

func tracks(url string) store.NewLink {
	return store.NewLink{Kind: store.LinkTracks, Source: sourceOf(url), URL: url}
}

func relates(url string) store.NewLink {
	return store.NewLink{Kind: store.LinkRelates, Source: sourceOf(url), URL: url}
}

func sourceOf(url string) string {
	if strings.Contains(url, "github.com") {
		return "github"
	}
	return "linear"
}

// links is the bead's links as "kind url", oldest first.
func links(t *testing.T, s store.API, id string) []string {
	t.Helper()
	ls, err := s.Links(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, l := range ls {
		if l.Bead != id {
			t.Fatalf("link of %s names bead %q", id, l.Bead)
		}
		out = append(out, l.Kind+" "+l.URL)
	}
	return out
}

func revision(t *testing.T, s store.API, id string) string {
	t.Helper()
	b, err := s.GetBead(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return b.Revision
}

// refused runs a write that must fail with every one of wants in its
// message, and must change nothing: no event is logged.
func refused(t *testing.T, s store.API, what string, err error, head uint64, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: not refused", what)
	}
	for _, w := range wants {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("%s: %v; want it to say %q", what, err, w)
		}
	}
	if now, _ := s.Head(context.Background()); now != head {
		t.Fatalf("%s: refused, yet %d events were logged", what, now-head)
	}
}

// A link is tracks or relates; the bead lists it, a linked event with kind,
// source, URL and actor is its record, and the bead is re-minted, as by an
// edge. Removing it logs unlinked and re-mints again.
func TestLinkAddAndRemove(t *testing.T) {
	t.Parallel()
	s, ctx := trainDepot(t)
	rev := revision(t, s, "web-1")
	ch, err := s.AddLink(ctx, "web-1", tracks(linA), "jane")
	if err != nil {
		t.Fatal(err)
	}
	if l := ch.Link; l.Bead != "web-1" || l.Kind != "tracks" || l.Source != "linear" || l.URL != linA ||
		l.CreatedBy != "jane" || l.CreatedAt.IsZero() || ch.Replaced != "" {
		t.Fatalf("AddLink = %+v", ch)
	}
	if _, err := s.AddLink(ctx, "web-1", relates(ghA), "jane"); err != nil {
		t.Fatal(err)
	}
	if got := links(t, s, "web-1"); len(got) != 2 || got[0] != "tracks "+linA || got[1] != "relates "+ghA {
		t.Fatalf("links = %v", got)
	}
	after := revision(t, s, "web-1")
	if after == rev {
		t.Fatal("linking did not re-mint the bead")
	}
	evs, _ := s.Events(ctx, "web-1", 0)
	var linked []store.Event
	for _, e := range evs {
		if e.Kind == "linked" {
			linked = append(linked, e)
		}
	}
	if len(linked) != 2 {
		t.Fatalf("linked events = %+v", linked)
	}
	for _, e := range linked {
		if e.Actor != "jane" || e.Data["source"] == "" || e.Data["kind"] == "" || e.Data["url"] == "" {
			t.Fatalf("linked event = %+v", e)
		}
	}
	// A bead named by a prefix or its UUID is the same bead.
	b, _ := s.GetBead(ctx, "web-1")
	if got := links(t, s, "web-1"); len(got) != 2 {
		t.Fatal(got)
	}
	if ls, err := s.Links(ctx, b.UUID); err != nil || len(ls) != 2 || ls[0].Bead != "web-1" {
		t.Fatalf("Links by UUID = %+v, %v", ls, err)
	}

	if err := s.RemoveLink(ctx, "web-1", linA, "joe"); err != nil {
		t.Fatal(err)
	}
	if got := links(t, s, "web-1"); len(got) != 1 || got[0] != "relates "+ghA {
		t.Fatalf("links after rm = %v", got)
	}
	if revision(t, s, "web-1") == after {
		t.Fatal("unlinking did not re-mint the bead")
	}
	evs, _ = s.Events(ctx, "web-1", 0)
	last := evs[len(evs)-1]
	if evs[0].Kind == "unlinked" {
		last = evs[0]
	}
	if last.Kind != "unlinked" || last.Actor != "joe" || last.Data["kind"] != "tracks" || last.Data["source"] != "linear" || last.Data["url"] != linA {
		t.Fatalf("unlinked event = %+v", last)
	}
	// The issue is free again.
	if _, err := s.AddLink(ctx, "web-2", tracks(linA), "jane"); err != nil {
		t.Fatal(err)
	}

	h := head(t, s)
	err = s.RemoveLink(ctx, "web-1", linA, "joe")
	refused(t, s, "rm of no link", err, h, "web-1 has no link to "+linA)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rm of no link: %v is not ErrNotFound", err)
	}
	_, err = s.AddLink(ctx, "web-9", tracks(linB), "jane")
	refused(t, s, "link of no bead", err, h, "web-9")
	for _, nl := range []store.NewLink{
		{Kind: "blocks", Source: "linear", URL: linB},
		{Kind: "", Source: "linear", URL: linB},
		{Kind: "tracks", Source: "Linear App", URL: linB},
		{Kind: "tracks", Source: "linear", URL: "YRD-2"},
		{Kind: "tracks", Source: "linear", URL: ""},
	} {
		_, err := s.AddLink(ctx, "web-1", nl, "jane")
		refused(t, s, "bad link", err, h)
		_, err = s.CreateBead(ctx, store.NewBead{Title: "x", Depot: "web", Links: []store.NewLink{nl}}, "jane")
		refused(t, s, "create with a bad link", err, h)
	}
}

// A bead tracks at most one issue per source: a Linear and a GitHub issue
// together, never two of one. The refusal names the one it tracks.
func TestLinkOneTrackedIssuePerSource(t *testing.T) {
	t.Parallel()
	s, ctx := trainDepot(t)
	for _, nl := range []store.NewLink{tracks(linA), tracks(ghA)} {
		if _, err := s.AddLink(ctx, "web-1", nl, "me"); err != nil {
			t.Fatal(err)
		}
	}
	h, rev := head(t, s), revision(t, s, "web-1")
	_, err := s.AddLink(ctx, "web-1", tracks(linB), "me")
	refused(t, s, "second linear tracks", err, h, "web-1 already tracks "+linA, "one linear issue", "--relates")
	_, err = s.AddLink(ctx, "web-1", tracks(linA), "me")
	refused(t, s, "the same link again", err, h, "web-1 already tracks "+linA)
	if revision(t, s, "web-1") != rev {
		t.Fatal("a refused link re-minted the bead")
	}
	// Any number of relates links, to issues of a source it tracks one of.
	for _, u := range []string{linB, "https://linear.app/acme/issue/YRD-3", "https://github.com/acme/webapp/issues/8"} {
		if _, err := s.AddLink(ctx, "web-1", relates(u), "me"); err != nil {
			t.Fatal(err)
		}
	}
	h = head(t, s)
	_, err = s.AddLink(ctx, "web-1", relates(linB), "me")
	refused(t, s, "the same relates again", err, h, "web-1 already relates to "+linB)
	if got := links(t, s, "web-1"); len(got) != 5 {
		t.Fatalf("links = %v", got)
	}
}

// An issue has at most one tracking bead, and any number that relate to it.
// The refusal names the tracker and both ways out.
func TestLinkOneTrackerPerIssue(t *testing.T) {
	t.Parallel()
	s, ctx := trainDepot(t)
	if _, err := s.AddLink(ctx, "web-1", tracks(linA), "me"); err != nil {
		t.Fatal(err)
	}
	h := head(t, s)
	_, err := s.AddLink(ctx, "web-2", tracks(linA), "me")
	refused(t, s, "second tracker", err, h, linA+" is tracked by web-1",
		"yardr link move "+linA+" --to web-2", "--relates")
	_, err = s.CreateBead(ctx, store.NewBead{ID: "web-3", Title: "three", Depot: "web", Links: []store.NewLink{tracks(linA)}}, "me")
	refused(t, s, "created as second tracker", err, h, linA+" is tracked by web-1")
	if _, err := s.GetBead(ctx, "web-3"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a bead whose link was refused was created: %v", err)
	}
	for _, id := range []string{"web-2", "web-e"} {
		if _, err := s.AddLink(ctx, id, relates(linA), "me"); err != nil {
			t.Fatal(err)
		}
	}
	// The issue's beads: the tracker first, then oldest first.
	ls, err := s.IssueLinks(ctx, linA)
	if err != nil || len(ls) != 3 || ls[0].Bead != "web-1" || ls[0].Kind != "tracks" || ls[1].Bead != "web-2" || ls[2].Bead != "web-e" {
		t.Fatalf("IssueLinks = %+v, %v", ls, err)
	}
	if ls, err := s.IssueLinks(ctx, linB); err != nil || len(ls) != 0 {
		t.Fatalf("IssueLinks of an unlinked issue = %+v, %v", ls, err)
	}

	// A closed bead keeps its links, and is still the issue's tracker.
	if _, err := s.CloseBead(ctx, "web-1", "done", store.Expect{}, "me"); err != nil {
		t.Fatal(err)
	}
	if got := links(t, s, "web-1"); len(got) != 1 || got[0] != "tracks "+linA {
		t.Fatalf("links of the closed bead = %v", got)
	}
	h = head(t, s)
	_, err = s.AddLink(ctx, "web-2", tracks(linA), "me")
	refused(t, s, "tracker beside a closed one", err, h, "is tracked by web-1")
}

// A bead has one link per issue: the other kind replaces it, and says so.
// Becoming the tracker that way is held to the tracks rules.
func TestLinkReplacesTheOtherKind(t *testing.T) {
	t.Parallel()
	s, ctx := trainDepot(t)
	if _, err := s.AddLink(ctx, "web-1", relates(linA), "me"); err != nil {
		t.Fatal(err)
	}
	h := head(t, s)
	ch, err := s.AddLink(ctx, "web-1", tracks(linA), "jane")
	if err != nil || ch.Replaced != "relates" || ch.Link.Kind != "tracks" {
		t.Fatalf("relates -> tracks: %+v, %v", ch, err)
	}
	if got := links(t, s, "web-1"); len(got) != 1 || got[0] != "tracks "+linA {
		t.Fatalf("links = %v", got)
	}
	evs, _ := s.EventsSince(ctx, h, 0)
	if len(evs) != 2 || evs[0].Kind != "unlinked" || evs[0].Data["kind"] != "relates" || evs[0].Data["replaced_by"] != "tracks" ||
		evs[1].Kind != "linked" || evs[1].Data["kind"] != "tracks" || evs[1].Data["replaced"] != "relates" || evs[1].Actor != "jane" {
		t.Fatalf("events of a replace = %+v", evs)
	}
	ch, err = s.AddLink(ctx, "web-1", relates(linA), "me")
	if err != nil || ch.Replaced != "tracks" {
		t.Fatalf("tracks -> relates: %+v, %v", ch, err)
	}
	if got := links(t, s, "web-1"); len(got) != 1 || got[0] != "relates "+linA {
		t.Fatalf("links = %v", got)
	}

	// web-2 tracks the issue now; web-1's relates link cannot become tracks.
	if _, err := s.AddLink(ctx, "web-2", tracks(linA), "me"); err != nil {
		t.Fatal(err)
	}
	h = head(t, s)
	_, err = s.AddLink(ctx, "web-1", tracks(linA), "me")
	refused(t, s, "relates -> tracks beside a tracker", err, h, "is tracked by web-2")
	// Nor may web-2's relates link to another linear issue become tracks.
	if _, err := s.AddLink(ctx, "web-2", relates(linB), "me"); err != nil {
		t.Fatal(err)
	}
	h = head(t, s)
	_, err = s.AddLink(ctx, "web-2", tracks(linB), "me")
	refused(t, s, "relates -> a second tracks", err, h, "web-2 already tracks "+linA)
	if got := links(t, s, "web-2"); len(got) != 2 || got[1] != "relates "+linB {
		t.Fatalf("links = %v", got)
	}
}

// link move hands the tracks link to another bead in one write: the earlier
// tracker keeps a relates link. Any bead type may track, a train and a
// train's child included.
func TestLinkMove(t *testing.T) {
	t.Parallel()
	s, ctx := trainDepot(t)
	if err := s.AddDep(ctx, "web-e", "web-1", store.DepParent, "", "", "me"); err != nil {
		t.Fatal(err)
	}
	// The child tracks; yardr does not forbid it.
	if _, err := s.AddLink(ctx, "web-1", store.NewLink{Kind: "tracks", Source: "linear", URL: linA, Thread: "issue-1"}, "me"); err != nil {
		t.Fatal(err)
	}
	h := head(t, s)
	revE, rev1 := revision(t, s, "web-e"), revision(t, s, "web-1")
	ch, err := s.MoveLink(ctx, linA, "web-e", "jane")
	if err != nil {
		t.Fatal(err)
	}
	if ch.From != "web-1" || ch.Link.Bead != "web-e" || ch.Link.Kind != "tracks" || ch.Link.Thread != "issue-1" || ch.Replaced != "" {
		t.Fatalf("MoveLink = %+v", ch)
	}
	if got := links(t, s, "web-e"); len(got) != 1 || got[0] != "tracks "+linA {
		t.Fatalf("train links = %v", got)
	}
	if got := links(t, s, "web-1"); len(got) != 1 || got[0] != "relates "+linA {
		t.Fatalf("earlier tracker's links = %v", got)
	}
	if revision(t, s, "web-e") == revE || revision(t, s, "web-1") == rev1 {
		t.Fatal("a move did not re-mint both beads")
	}
	evs, _ := s.EventsSince(ctx, h, 0)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind+" "+e.Data["kind"].(string))
		if e.Actor != "jane" || e.Data["url"] != linA || e.Data["source"] != "linear" || e.Data["thread"] != "issue-1" {
			t.Fatalf("event of a move = %+v", e)
		}
	}
	if got := strings.Join(kinds, ", "); got != "unlinked tracks, linked relates, linked tracks" {
		t.Fatalf("events of a move = %s", got)
	}
	if evs[2].Data["moved_from"] != "web-1" || evs[1].Data["moved_to"] != "web-e" {
		t.Fatalf("events of a move = %+v", evs)
	}

	// A bead that related to the issue becomes its tracker: one link still.
	if _, err := s.AddLink(ctx, "web-2", relates(linA), "me"); err != nil {
		t.Fatal(err)
	}
	ch, err = s.MoveLink(ctx, linA, "web-2", "me")
	if err != nil || ch.Replaced != "relates" || ch.From != "web-e" {
		t.Fatalf("move to a bead that related: %+v, %v", ch, err)
	}
	if got := links(t, s, "web-2"); len(got) != 1 || got[0] != "tracks "+linA {
		t.Fatalf("links = %v", got)
	}
	if got := links(t, s, "web-e"); len(got) != 1 || got[0] != "relates "+linA {
		t.Fatalf("links = %v", got)
	}

	h = head(t, s)
	_, err = s.MoveLink(ctx, linA, "web-2", "me")
	refused(t, s, "move to the tracker", err, h, "web-2 already tracks "+linA)
	_, err = s.MoveLink(ctx, linB, "web-e", "me")
	refused(t, s, "move of an untracked issue", err, h, "no bead tracks "+linB, "yardr link add web-e "+linB)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("move of an untracked issue: %v is not ErrNotFound", err)
	}
	_, err = s.MoveLink(ctx, linA, "web-9", "me")
	refused(t, s, "move to no bead", err, h, "web-9")
	// The target tracks another linear issue: refused, and the issue keeps
	// its tracker, the write being one.
	if _, err := s.AddLink(ctx, "web-e", tracks(linB), "me"); err != nil {
		t.Fatal(err)
	}
	h = head(t, s)
	_, err = s.MoveLink(ctx, linA, "web-e", "me")
	refused(t, s, "move to a bead tracking another", err, h, "web-e already tracks "+linB)
	if got := links(t, s, "web-2"); len(got) != 1 || got[0] != "tracks "+linA {
		t.Fatalf("a refused move changed the tracker: %v", got)
	}
}

// A bead is created with its links, and a link made from the URL takes the
// thread id from one made from a delivery. The snapshot the BDP server
// reads carries every link with its own revision.
func TestLinkCreatedWithTheBeadAndSnapshot(t *testing.T) {
	t.Parallel()
	s, ctx := trainDepot(t)
	b, err := s.CreateBead(ctx, store.NewBead{ID: "web-3", Title: "three", Type: store.TypeTrain, Depot: "web", Links: []store.NewLink{
		{Kind: "tracks", Source: "linear", URL: linA, Thread: "issue-1"}, tracks(ghA), relates(linB)}}, "jane")
	if err != nil {
		t.Fatal(err)
	}
	if b.Revision != revision(t, s, "web-3") {
		t.Fatal("CreateBead returned a revision the bead no longer has")
	}
	if got := links(t, s, "web-3"); len(got) != 3 {
		t.Fatalf("links = %v", got)
	}
	ch, err := s.AddLink(ctx, "web-1", relates(linA), "me")
	if err != nil || ch.Link.Thread != "issue-1" {
		t.Fatalf("thread of a link made from the URL: %+v, %v", ch, err)
	}
	snap, err := s.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Links) != 4 {
		t.Fatalf("snapshot links = %+v", snap.Links)
	}
	ids := map[string]bool{}
	for _, l := range snap.Links {
		if l.ID == "" || l.Revision == "" || ids[l.ID] || (l.Bead != "web-3" && l.Bead != "web-1") || l.CreatedBy == nil {
			t.Fatalf("snapshot link = %+v", l)
		}
		ids[l.ID] = true
	}
}

// MoveLinkSource moves the links of one source to another in one write,
// keeping kind, URL, thread and creator; each moved link logs unlinked and
// linked, and its bead is re-minted. A bead that would then track two of
// the new source refuses the whole move, and nothing changes.
func TestMoveLinkSource(t *testing.T) {
	t.Parallel()
	s, ctx := trainDepot(t)
	if _, err := s.AddLink(ctx, "web-1", store.NewLink{Kind: "tracks", Source: "linear", URL: linA, Thread: "issue-1"}, "jane"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLink(ctx, "web-2", relates(linB), "joe"); err != nil {
		t.Fatal(err)
	}
	rev1, rev2 := revision(t, s, "web-1"), revision(t, s, "web-2")
	h := head(t, s)
	n, err := s.MoveLinkSource(ctx, "linear", "lin-declared", "me")
	if err != nil || n != 2 {
		t.Fatalf("move = %d, %v", n, err)
	}
	ls, _ := s.Links(ctx, "web-1")
	if len(ls) != 1 || ls[0].Source != "lin-declared" || ls[0].Kind != "tracks" || ls[0].URL != linA ||
		ls[0].Thread != "issue-1" || ls[0].CreatedBy != "jane" {
		t.Fatalf("web-1 links = %+v", ls)
	}
	if revision(t, s, "web-1") == rev1 || revision(t, s, "web-2") == rev2 {
		t.Fatal("a move did not re-mint both beads")
	}
	evs, _ := s.EventsSince(ctx, h, 0)
	if len(evs) != 4 {
		t.Fatalf("events of a move = %d, want 4", len(evs))
	}
	for _, e := range evs {
		if e.Actor != "me" {
			t.Fatalf("event actor = %+v", e)
		}
	}
	if evs[0].Kind != "unlinked" || evs[0].Data["source"] != "linear" || evs[0].Data["moved_source_to"] != "lin-declared" ||
		evs[1].Kind != "linked" || evs[1].Data["source"] != "lin-declared" || evs[1].Data["moved_source_from"] != "linear" {
		t.Fatalf("events = %+v", evs)
	}
	// Back again, and none of a source with no links.
	if n, err := s.MoveLinkSource(ctx, "lin-declared", "linear", "me"); err != nil || n != 2 {
		t.Fatalf("move back = %d, %v", n, err)
	}
	if n, err := s.MoveLinkSource(ctx, "lin-declared", "linear", "me"); err != nil || n != 0 {
		t.Fatalf("move none = %d, %v", n, err)
	}
	// A clash refuses everything.
	if _, err := s.AddLink(ctx, "web-1", store.NewLink{Kind: "tracks", Source: "lin-declared", URL: "https://linear.app/acme/issue/YRD-9"}, "me"); err != nil {
		t.Fatal(err)
	}
	h, rev1 = head(t, s), revision(t, s, "web-1")
	_, err = s.MoveLinkSource(ctx, "linear", "lin-declared", "me")
	refused(t, s, "clashing move", err, h, "web-1 would track two lin-declared issues")
	if revision(t, s, "web-1") != rev1 {
		t.Fatal("a refused move re-minted the bead")
	}
	for _, args := range [][2]string{{"linear", "linear"}, {"", "linear"}, {"linear", ""}, {"linear", "Linear App"}} {
		_, err := s.MoveLinkSource(ctx, args[0], args[1], "me")
		refused(t, s, "bad move", err, h)
	}
}
