package dispatch_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// contributorMakes is a table of what makes a bead under contributorFlow:
// an issue someone opens, or opens again, and a pull request once it is
// there to be read.
var contributorMakes = []string{
	"event=issues action=opened action=reopened -> issue:triage",
	"event=pull_request action=opened action=ready_for_review -> task:review",
}

const pull17 = "https://github.com/acme/yardr/pull/17"

// newMakingDepot is newTrackingDepot whose hook makes beads by lines too,
// in a depot that is of acme/yardr, the fixtures' repository.
func newMakingDepot(t *testing.T, lines ...string) *ghDepot {
	t.Helper()
	r := newTrackingDepot(t, contributorSays...)
	r.makes(t, lines...)
	r.d.DepotRepo = func(_ context.Context, q dispatch.DepotRepoQuery) (string, error) {
		if q.Depot.Name == "yard" {
			return "acme/yardr", nil
		}
		return "", nil
	}
	return r
}

// makes gives hook gh the table lines, beside what it says.
func (r *ghDepot) makes(t *testing.T, lines ...string) {
	t.Helper()
	ctx := context.Background()
	h, err := r.api.GetHook(ctx, "gh")
	must(t, err)
	h.Makes = nil
	for _, l := range lines {
		m, err := store.ParseMake(l)
		must(t, err)
		h.Makes = append(h.Makes, m)
	}
	_, err = r.api.PutHook(ctx, *h, "me")
	must(t, err)
}

// tracking are the open and closed beads that track the thread at url, by
// handle, and the ones that only relate to it.
func (r *ghDepot) tracking(t *testing.T, url string) (tracks, relates []string) {
	t.Helper()
	links, err := r.api.IssueLinks(context.Background(), url)
	must(t, err)
	for _, l := range links {
		if l.Kind == store.LinkTracks {
			tracks = append(tracks, l.Bead)
		} else {
			relates = append(relates, l.Bead)
		}
	}
	return tracks, relates
}

// made is the one bead that tracks the thread at url.
func (r *ghDepot) made(t *testing.T, url string) *store.Bead {
	t.Helper()
	tracks, _ := r.tracking(t, url)
	if len(tracks) != 1 {
		t.Fatalf("beads tracking %s: %v, want one", url, tracks)
	}
	return r.bead(t, tracks[0])
}

// beadless are the github_rule events logged on no bead, as rules gives
// those of one.
func (r *ghDepot) beadless(t *testing.T) []string {
	t.Helper()
	evs, err := r.api.Events(context.Background(), "", 500)
	must(t, err)
	var out []string
	for _, e := range evs {
		if e.Kind != "github_rule" || e.Bead != nil {
			continue
		}
		if e.Data["makes"] == nil || e.Data["seq"] == nil || e.Data["thread"] == nil {
			t.Fatalf("github_rule on no bead without the line, the seq or the thread: %v", e.Data)
		}
		switch {
		case e.Data["ignored"] != nil:
			out = append(out, "ignored: "+e.Data["ignored"].(string))
		case e.Data["refused"] != nil:
			out = append(out, "refused: "+e.Data["refused"].(string))
		default:
			t.Fatalf("github_rule on no bead that neither ignored nor refused: %v", e.Data)
		}
	}
	return out
}

// An issue opened in the depot's repository makes the bead that tracks it:
// of the line's type, in the line's stage, with the thread's title, the
// event's text and url, made by the hook, and logged as what the event
// made. The next event about the issue says something to that bead and
// makes no other.
func TestEventMakesTheBeadThatTracksItsThread(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	b := r.made(t, issue23)
	if b.Type != "issue" || b.Stage != "triage" || b.Depot == nil || *b.Depot != "yard" || b.Status != "open" {
		t.Fatalf("made %s: type %s, stage %s, depot %v, %s", b.ID, b.Type, b.Stage, b.Depot, b.Status)
	}
	if b.Title != "Hook listener logs the secret path" || !strings.Contains(b.Body, "Seen on startup.") || !strings.HasSuffix(b.Body, issue23) {
		t.Fatalf("made %s: title %q, body %q", b.ID, b.Title, b.Body)
	}
	if b.CreatedBy == nil || *b.CreatedBy != "hook:gh" {
		t.Fatalf("made by %v, want hook:gh", b.CreatedBy)
	}
	evs, err := r.api.Events(context.Background(), b.ID, 50)
	must(t, err)
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.Kind)
		if e.Kind == "github_rule" && (e.Data["makes"] != "action=opened action=reopened event=issues -> issue:triage" || e.Data["depot"] != "yard" || e.Data["thread"] != issue23) {
			t.Fatalf("github_rule of the make: %v", e.Data)
		}
	}
	// What follows is the dispatcher's, of a bead in a stage like any other.
	if !strings.HasPrefix(strings.Join(kinds, " "), "created linked github_rule") {
		t.Fatalf("events of %s: %v", b.ID, kinds)
	}
	wantRules(t, r.rules(t, b.ID), "issues made "+b.ID)

	// The event that made it says nothing to it; the next one does.
	r.deliverAs(t, "gh", "issues", "issues_labeled", nil)
	r.pass(t)
	if again := r.made(t, issue23); again.ID != b.ID || again.Stage != "backlog" {
		t.Fatalf("after the issue was labeled accepted: %s in %s, want %s in backlog", again.ID, again.Stage, b.ID)
	}
	wantRules(t, r.rules(t, b.ID), "issues made "+b.ID, "accepted triage -> backlog")

	// A daemon that starts again reads the log from the start and makes
	// nothing of what it reads there.
	fresh := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI, DepotRepo: r.d.DepotRepo}
	must(t, fresh.Reconcile(context.Background()))
	wantRules(t, r.rules(t, b.ID), "issues made "+b.ID, "accepted triage -> backlog")
	if all, err := r.api.ListBeads(context.Background(), store.BeadFilter{}); err != nil || len(all) != 1 {
		t.Fatalf("beads after a restart: %d (%v), want the one", len(all), err)
	}
}

// A hook table saved after a delivery was recorded does not apply to that
// older event, even when this dispatcher already cached its make history.
func TestHookUpdateDoesNotMakeFromEarlierQueuedEvent(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	first := r.made(t, issue23)

	const issue24 = "https://github.com/acme/yardr/issues/24"
	r.deliverAs(t, "gh", "issues", "issues_opened", func(s string) string {
		return strings.ReplaceAll(s, "23", "24")
	})
	h, err := r.api.GetHook(context.Background(), "gh")
	must(t, err)
	h.Makes[0].Stage = "backlog"
	_, err = r.api.PutHook(context.Background(), *h, "me")
	must(t, err)
	r.pass(t)

	links, err := r.api.IssueLinks(context.Background(), issue24)
	must(t, err)
	if len(links) != 0 {
		t.Fatalf("delivery recorded before the hook update made a bead: %+v", links)
	}
	all, err := r.api.ListBeads(context.Background(), store.BeadFilter{})
	must(t, err)
	if len(all) != 1 || all[0].ID != first.ID {
		t.Fatalf("beads after the hook update: %+v, want only %s", all, first.ID)
	}
}

// A closed bead that tracks the thread does not stand in the way of a new
// one: what happens to the thread after the close makes the next bead,
// which takes the link, and the closed one keeps a relates link. The events
// from before the close were the closed bead's, and make nothing when a
// restarted daemon reads them again.
func TestClosedBeadDoesNotStopANewOne(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	ctx := context.Background()
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	first := r.made(t, issue23)
	_, err := r.api.CloseBead(ctx, first.ID, "not ours", store.Expect{}, "me")
	must(t, err)

	fresh := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI, DepotRepo: r.d.DepotRepo}
	must(t, fresh.Reconcile(ctx))
	if b := r.made(t, issue23); b.ID != first.ID {
		t.Fatalf("a restart made %s of an event from before %s was closed", b.ID, first.ID)
	}

	r.deliverAs(t, "gh", "issues", "issues_opened", func(s string) string {
		return strings.Replace(s, `"action": "opened"`, `"action": "reopened"`, 1)
	})
	r.pass(t)
	second := r.made(t, issue23)
	if second.ID == first.ID || second.Status != "open" || second.Stage != "triage" {
		t.Fatalf("after the issue was opened again: %s (%s, %s), want a new open bead in triage", second.ID, second.Status, second.Stage)
	}
	if _, relates := r.tracking(t, issue23); len(relates) != 1 || relates[0] != first.ID {
		t.Fatalf("beads relating to %s: %v, want the closed %s", issue23, relates, first.ID)
	}
	// The same holds for a bead closed by hand that an event never made.
	fresh = &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI, DepotRepo: r.d.DepotRepo}
	must(t, fresh.Reconcile(ctx))
	if b := r.made(t, issue23); b.ID != second.ID {
		t.Fatalf("a restart made %s beside %s", b.ID, second.ID)
	}
}

// A draft pull request that is marked ready comes as two events the line
// matches, and a delivery can come twice: one bead, not two.
func TestOneThreadMakesOneBead(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	r.deliverAs(t, "gh", "pull_request", "pull_request_opened", nil)
	r.deliverAs(t, "gh", "pull_request", "pull_request_opened", nil)
	r.deliverAs(t, "gh", "pull_request", "pull_request_ready_for_review", nil)
	r.pass(t)
	b := r.made(t, pull17)
	if b.Type != "task" || b.Stage != "review" {
		t.Fatalf("made %s: type %s in %s, want a task in review", b.ID, b.Type, b.Stage)
	}
	r.deliverAs(t, "gh", "pull_request", "pull_request_ready_for_review", func(s string) string {
		return strings.Replace(s, `"draft": false`, `"draft":false`, 1)
	})
	r.pass(t)
	if all, err := r.api.ListBeads(context.Background(), store.BeadFilter{}); err != nil || len(all) != 1 {
		t.Fatalf("beads: %d (%v), want the one", len(all), err)
	}
	wantRules(t, r.rules(t, b.ID), "pull_request made "+b.ID)
}

// The write that makes the bead is the one that looks whether a bead
// tracks the thread: where one came to meanwhile, nothing is made.
func TestMakeIsRefusedOfATrackedThread(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	ctx := context.Background()
	r.tracker(t, "yard-t", "triage", issue23)
	nb := store.NewBead{Title: "again", Depot: "yard", Stage: "triage", Untracked: true,
		Links: []store.NewLink{{Kind: store.LinkTracks, Source: "github", URL: issue23}}}
	if _, err := r.api.CreateBead(ctx, nb, "hook:gh"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("a bead for a thread yard-t tracks: %v, want it refused as stale", err)
	}
	if all, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(all) != 1 {
		t.Fatalf("beads: %d (%v), want yard-t alone", len(all), err)
	}
	// An event about it is yard-t's, whatever the table of makes has.
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	if b := r.made(t, issue23); b.ID != "yard-t" {
		t.Fatalf("%s tracks %s, want yard-t", b.ID, issue23)
	}
}

// The depot is the line's, else the one whose repository the event is in.
// An event no depot is found for makes nothing and says why, once: not
// again when a restarted daemon reads it.
func TestMakeFindsTheDepot(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	ctx := context.Background()
	r.d.DepotRepo = func(context.Context, dispatch.DepotRepoQuery) (string, error) { return "acme/other", nil }
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	if tracks, _ := r.tracking(t, issue23); len(tracks) != 0 {
		t.Fatalf("%v made in a depot of another repository", tracks)
	}
	wantRules(t, r.beadless(t), "ignored: no depot's origin is acme/yardr")
	fresh := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI, DepotRepo: r.d.DepotRepo}
	must(t, fresh.Reconcile(ctx))
	wantRules(t, r.beadless(t), "ignored: no depot's origin is acme/yardr")

	// Two depots of one repository: the line must say which.
	must(t, r.api.PutDepot(ctx, store.Depot{Name: "fork", Base: "main"}, "me"))
	r.d = &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI,
		DepotRepo: func(context.Context, dispatch.DepotRepoQuery) (string, error) { return "Acme/Yardr", nil }}
	must(t, r.d.Reconcile(ctx))
	r.deliverAs(t, "gh", "issues", "issues_opened", func(s string) string { return strings.Replace(s, "Seen on startup.", "Seen.", 1) })
	r.pass(t)
	wantRules(t, r.beadless(t), "ignored: no depot's origin is acme/yardr", "ignored: depots fork, yard are all of acme/yardr")

	// A line that names its depot asks no one, and a stage the type's flow
	// has not there is the store's to refuse.
	r.makes(t, "event=issues action=opened -> issue:triage@yard", "event=pull_request action=opened -> task:nowhere@yard")
	r.deliverAs(t, "gh", "issues", "issues_opened", func(s string) string { return strings.Replace(s, "Seen on startup.", "Seen again.", 1) })
	r.deliverAs(t, "gh", "pull_request", "pull_request_opened", nil)
	r.pass(t)
	if b := r.made(t, issue23); *b.Depot != "yard" {
		t.Fatalf("made in %s, want yard", *b.Depot)
	}
	if tracks, _ := r.tracking(t, pull17); len(tracks) != 0 {
		t.Fatalf("%v made in a stage the flow has not", tracks)
	}
	if got := r.beadless(t); len(got) != 3 || !strings.HasPrefix(got[2], "refused: stage nowhere is not in flow yard") {
		t.Fatalf("github_rule events on no bead: %q", got)
	}
}

// What the yard did itself makes nothing, as it says nothing: an issue a
// crew member opened is no one's from outside.
func TestTheYardsOwnActionMakesNothing(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	ctx := context.Background()
	_, err := r.api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "fake"}, "me")
	must(t, err)
	must(t, r.api.SetIdentity(ctx, "yardmaster", "github", store.Identity{Key: "Yardr[bot]"}, "me"))
	r.deliverAs(t, "gh", "issues", "issues_opened", func(s string) string {
		return strings.ReplaceAll(s, "jane-doe", "yardr[bot]")
	})
	r.pass(t)
	if tracks, _ := r.tracking(t, issue23); len(tracks) != 0 {
		t.Fatalf("%v made of the yard's own issue", tracks)
	}
}

// A table says nothing of what came before it: the events a daemon reads
// again after a start, from before the hook got its lines, make nothing.
func TestMakesSaysNothingOfEventsBeforeIt(t *testing.T) {
	r := newTrackingDepot(t, contributorSays...)
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	r.makes(t, contributorMakes...)
	fresh := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI,
		DepotRepo: func(context.Context, dispatch.DepotRepoQuery) (string, error) { return "acme/yardr", nil }}
	must(t, fresh.Reconcile(context.Background()))
	if tracks, _ := r.tracking(t, issue23); len(tracks) != 0 {
		t.Fatalf("%v made of an event from before the hook had the line", tracks)
	}
}

// With no one else to ask, the depot's repository is the one its origin
// remote names, however the URL is written, read at each event.
func TestDepotIsFoundByItsOriginRemote(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	r.d.DepotRepo = nil
	ctx := context.Background()
	repo := t.TempDir()
	gitIn(t, repo, "init", "-q")
	gitIn(t, repo, "remote", "add", "origin", "git@github.com:Acme/yardr.git")
	must(t, r.api.PutDepot(ctx, store.Depot{Name: "yard", Path: repo, Base: "main"}, "me"))
	// A depot with no checkout, and one of a repository elsewhere, are of none.
	must(t, r.api.PutDepot(ctx, store.Depot{Name: "notes", Kind: store.DepotDir, Path: t.TempDir()}, "me"))
	other := t.TempDir()
	gitIn(t, other, "init", "-q")
	gitIn(t, other, "remote", "add", "origin", "https://gitlab.com/acme/yardr.git")
	must(t, r.api.PutDepot(ctx, store.Depot{Name: "mirror", Path: other, Base: "main"}, "me"))

	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	if b := r.made(t, issue23); *b.Depot != "yard" {
		t.Fatalf("made in %s, want yard", *b.Depot)
	}
	// Read again: the origin moved on, so the next thread in the old
	// repository makes nothing.
	gitIn(t, repo, "remote", "set-url", "origin", "git@github.com:acme/elsewhere.git")
	r.deliverAs(t, "gh", "pull_request", "pull_request_opened", nil)
	r.pass(t)
	if tracks, _ := r.tracking(t, pull17); len(tracks) != 0 {
		t.Fatalf("%v made after the depot's origin moved on", tracks)
	}
	if got := r.beadless(t); len(got) != 1 || !strings.HasPrefix(got[0], "ignored: no depot's origin is acme/yardr") {
		t.Fatalf("github_rule events on no bead: %q", got)
	}
}

// The depot's repository is asked at each event, not kept: a stub that
// answers one repository and then another makes a bead in the depot of an
// event from each, without a new Dispatcher.
func TestDepotRepoIsAskedAtEachEvent(t *testing.T) {
	r := newMakingDepot(t, contributorMakes...)
	repo := "acme/yardr"
	r.d.DepotRepo = func(_ context.Context, q dispatch.DepotRepoQuery) (string, error) {
		if q.Depot.Name == "yard" {
			return repo, nil
		}
		return "", nil
	}
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	if b := r.made(t, issue23); *b.Depot != "yard" {
		t.Fatalf("made in %s, want yard", *b.Depot)
	}
	// The checkout stays, the origin moves: the next event is in the new
	// repository and makes its bead in the same depot.
	repo = "acme/elsewhere"
	r.deliverAs(t, "gh", "pull_request", "pull_request_opened", func(s string) string {
		return strings.Replace(s, `"repository": {
  "full_name": "acme/yardr"`, `"repository": {
  "full_name": "acme/elsewhere"`, 1)
	})
	r.pass(t)
	if b := r.made(t, pull17); *b.Depot != "yard" {
		t.Fatalf("made in %s, want yard", *b.Depot)
	}
}
