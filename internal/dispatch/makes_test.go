package dispatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
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

// beadless are the hook_rule events logged on no bead, as rules gives
// those of one.
func (r *ghDepot) beadless(t *testing.T) []string {
	t.Helper()
	evs, err := r.api.Events(context.Background(), "", 500)
	must(t, err)
	var out []string
	for _, e := range evs {
		if e.Kind != "hook_rule" || e.Bead != nil {
			continue
		}
		if e.Data["makes"] == nil || e.Data["seq"] == nil || e.Data["thread"] == nil {
			t.Fatalf("hook_rule on no bead without the line, the seq or the thread: %v", e.Data)
		}
		switch {
		case e.Data["ignored"] != nil:
			out = append(out, "ignored: "+e.Data["ignored"].(string))
		case e.Data["refused"] != nil:
			out = append(out, "refused: "+e.Data["refused"].(string))
		default:
			t.Fatalf("hook_rule on no bead that neither ignored nor refused: %v", e.Data)
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
	t.Parallel()
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
		if e.Kind == "hook_rule" && (e.Data["makes"] != "action=opened action=reopened event=issues -> issue:triage" || e.Data["depot"] != "yard" || e.Data["thread"] != issue23) {
			t.Fatalf("hook_rule of the make: %v", e.Data)
		}
	}
	// What follows is the dispatcher's, of a bead in a stage like any other.
	if !strings.HasPrefix(strings.Join(kinds, " "), "created linked hook_rule") {
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
	t.Parallel()
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
	t.Parallel()
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
	t.Run("close and reopen before one pass", testReopenedIssueInOnePassStartsAFreshBead)
	t.Run("manual reclose uses its own time", testManualRecloseAfterEventCloseUsesCloseTime)
}

// An issue closed and reopened before the pass that reads either has a
// bead again, as when a pass runs between the two (above): the close ends
// the old bead, and the reopen, later in the log, makes the next.
func testReopenedIssueInOnePassStartsAFreshBead(t *testing.T) {
	t.Helper()
	r := newMakingDepot(t, contributorMakes...)
	ctx := context.Background()
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	first := r.made(t, issue23)

	r.deliverAs(t, "gh", "issues", "issues_closed", nil)
	r.deliverAs(t, "gh", "issues", "issues_opened", func(s string) string {
		return strings.Replace(s, `"action": "opened"`, `"action": "reopened"`, 1)
	})
	r.pass(t)

	if got := r.bead(t, first.ID); got.Status != "closed" || got.Stage != "closed" {
		t.Fatalf("%s after the close: %s in %s, want it closed", first.ID, got.Status, got.Stage)
	}
	second := r.made(t, issue23)
	if second.ID == first.ID || second.Status != "open" || second.Stage != "triage" {
		t.Fatalf("after the issue was closed and opened again in one pass: %s (%s, %s), want a new open bead in triage", second.ID, second.Status, second.Stage)
	}
	if _, relates := r.tracking(t, issue23); len(relates) != 1 || relates[0] != first.ID {
		t.Fatalf("beads relating to %s: %v, want the closed %s", issue23, relates, first.ID)
	}
	if open, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(open) != 1 || open[0].ID != second.ID {
		t.Fatalf("open beads %v (%v), want %s alone", open, err, second.ID)
	}
}

// A hand close after reopening a stream-closed bead is judged by its time,
// not the seq of the older close, when the dispatcher reads a queued event.
func testManualRecloseAfterEventCloseUsesCloseTime(t *testing.T) {
	t.Helper()
	r := newMakingDepot(t, contributorMakes...)
	ctx := context.Background()
	r.deliverAs(t, "gh", "issues", "issues_opened", nil)
	r.pass(t)
	bead := r.made(t, issue23)

	r.deliverAs(t, "gh", "issues", "issues_closed", nil)
	r.pass(t)
	if r.bead(t, bead.ID).Metadata["close_seq"] == nil {
		t.Fatalf("%s has no close_seq after the stream close", bead.ID)
	}
	_, err := r.api.Reopen(ctx, bead.ID, store.Expect{}, "me")
	must(t, err)
	if got := r.bead(t, bead.ID).Metadata["close_seq"]; got != nil {
		t.Fatalf("%s retained close_seq after reopen: %v", bead.ID, got)
	}
	r.deliverAs(t, "gh", "issues", "issues_opened", func(s string) string {
		return strings.Replace(s, `"action": "opened"`, `"action": "reopened"`, 1)
	})
	_, err = r.api.CloseBead(ctx, bead.ID, "not ours", store.Expect{}, "me")
	must(t, err)
	r.pass(t)

	if got := r.made(t, issue23); got.ID != bead.ID || got.Status != "closed" {
		t.Fatalf("after the queued reopen: tracking bead is %s (%s), want closed %s only", got.ID, got.Status, bead.ID)
	}
	if open, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(open) != 0 {
		t.Fatalf("open beads after the hand close: %d (%v), want none", len(open), err)
	}
}

// A draft pull request that is marked ready comes as two events the line
// matches, and a delivery can come twice: one bead, not two.
func TestOneThreadMakesOneBead(t *testing.T) {
	t.Parallel()
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

const (
	branchMain = "https://github.com/acme/yardr/tree/main"
	runsOfCI   = "https://github.com/acme/yardr/actions/runs/"
)

// failedRun delivers fixture, a run that failed for no pull request, as the
// run with the number run on branch.
func (r *ghDepot) failedRun(t *testing.T, fixture, branch, run string) {
	t.Helper()
	r.deliverAs(t, "gh", "workflow_run", fixture, func(s string) string {
		s = strings.ReplaceAll(s, "yard/yard-sv6b", branch)
		return strings.ReplaceAll(s, "runs/5", "runs/"+run)
	})
}

// A run that failed on the repository's own branch for no pull request has
// the thread of the branch, the same for every run of it, so a line that
// makes a bead of the failed runs of main makes one while none tracks the
// branch, a later run finds it and makes none, and one after it was closed
// makes the next. The bead's body ends with the run's own page, not the
// branch's, and what the table says of a run takes effect on it without a
// pull request's head being asked. A run on another branch, and a run on a
// fork's branch of the same name (which is no branch of this repository),
// make nothing.
func TestFailedRunsOnABranchMakeOneBeadWhileItIsOpen(t *testing.T) {
	t.Parallel()
	r := newMakingDepot(t, "event=workflow_run conclusion=failure head_branch=main -> task:new")
	ctx := context.Background()
	h, err := r.api.GetHook(ctx, "gh")
	must(t, err)
	h.Says = append(h.Says, says(t, "event=workflow_run conclusion=failure head_branch=main type=task stage=new -> done: failed again")...)
	_, err = r.api.PutHook(ctx, *h, "me")
	must(t, err)

	// What belongs to no thread of the repository's makes nothing, and says
	// nothing of why.
	r.failedRun(t, "workflow_run_failure", "feature", "4")
	r.failedRun(t, "workflow_run_fork", "main", "3")
	r.pass(t)
	if tracks, _ := r.tracking(t, branchMain); len(tracks) != 0 {
		t.Fatalf("%v made of a run on a fork's branch of the same name", tracks)
	}
	if all, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(all) != 0 {
		t.Fatalf("beads after a run on another branch and a fork's: %d (%v), want none", len(all), err)
	}
	wantRules(t, r.beadless(t))

	r.failedRun(t, "workflow_run_failure", "main", "5")
	r.pass(t)
	first := r.made(t, branchMain)
	if first.Type != "task" || first.Stage != "new" || first.Status != "open" || first.Title != "CI on acme/yardr@main" {
		t.Fatalf("made %s: type %s in %s (%s), title %q", first.ID, first.Type, first.Stage, first.Status, first.Title)
	}
	if want := "ci failure on main\n\n" + runsOfCI + "5"; first.Body != want {
		t.Fatalf("made %s: body %q, want %q", first.ID, first.Body, want)
	}
	links, err := r.api.IssueLinks(ctx, branchMain)
	must(t, err)
	if len(links) != 1 || links[0].Kind != store.LinkTracks || links[0].Source != "github" || links[0].Thread != "acme/yardr@main" {
		t.Fatalf("links to the branch: %+v, want the one tracks link", links)
	}
	wantRules(t, r.rules(t, first.ID), "workflow_run made "+first.ID)

	// The next failed run is the open bead's: it makes none, and the line
	// for the bead's stage moves it, with no ask of a pull request's head.
	r.failedRun(t, "workflow_run_failure", "main", "6")
	r.pass(t)
	if again := r.made(t, branchMain); again.ID != first.ID || again.Stage != "review" {
		t.Fatalf("after a second failed run: %s in %s, want %s in review", again.ID, again.Stage, first.ID)
	}
	wantRules(t, r.rules(t, first.ID), "workflow_run made "+first.ID, "done new -> review")
	if all, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(all) != 1 {
		t.Fatalf("beads after a second failed run: %d (%v), want the one", len(all), err)
	}

	// Once closed it stands in no one's way: the run after its close makes
	// the next, which takes the link, and the first keeps a relates link. A
	// daemon that starts again makes nothing of the runs from before.
	_, err = r.api.CloseBead(ctx, first.ID, "fixed", store.Expect{}, "me")
	must(t, err)
	fresh := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI, DepotRepo: r.d.DepotRepo}
	must(t, fresh.Reconcile(ctx))
	if b := r.made(t, branchMain); b.ID != first.ID {
		t.Fatalf("a restart made %s of a run from before %s was closed", b.ID, first.ID)
	}
	r.failedRun(t, "workflow_run_failure", "main", "7")
	r.pass(t)
	second := r.made(t, branchMain)
	if second.ID == first.ID || second.Status != "open" || second.Stage != "new" || !strings.HasSuffix(second.Body, runsOfCI+"7") {
		t.Fatalf("after a run following the close: %s (%s, %s) %q, want a new open bead in new for run 7", second.ID, second.Status, second.Stage, second.Body)
	}
	if _, relates := r.tracking(t, branchMain); len(relates) != 1 || relates[0] != first.ID {
		t.Fatalf("beads relating to the branch: %v, want the closed %s", relates, first.ID)
	}
}

// The ci-red pack's line, on a depot's hook, makes one task bead in new of
// the failed runs of main: the first failed run makes it, titled for the
// branch, and the next finds it and makes none. The rest of the line's
// behavior is TestFailedRunsOnABranchMakeOneBeadWhileItIsOpen's.
func TestCiRedPackLineMakesOneBeadOfFailedRuns(t *testing.T) {
	t.Parallel()
	sub, err := fs.Sub(packs.FS, packs.CiRed)
	must(t, err)
	p, err := pack.Load(sub, packs.Embedded)
	must(t, err)
	if len(p.Sources) != 1 || len(p.Sources[0].Makes) != 1 {
		t.Fatalf("ci-red sources = %+v, want the one makes line", p.Sources)
	}
	r := newMakingDepot(t, store.MakeLines(p.Sources[0].Makes)...)
	ctx := context.Background()
	r.failedRun(t, "workflow_run_failure", "main", "5")
	r.pass(t)
	first := r.made(t, branchMain)
	if first.Type != "task" || first.Stage != "new" || first.Status != "open" || first.Title != "CI on acme/yardr@main" {
		t.Fatalf("made %s: type %s in %s (%s), title %q", first.ID, first.Type, first.Stage, first.Status, first.Title)
	}
	if want := "ci failure on main\n\n" + runsOfCI + "5"; first.Body != want {
		t.Fatalf("made %s: body %q, want %q", first.ID, first.Body, want)
	}
	r.failedRun(t, "workflow_run_failure", "main", "6")
	r.pass(t)
	if again := r.made(t, branchMain); again.ID != first.ID {
		t.Fatalf("a second failed run made %s beside %s", again.ID, first.ID)
	}
	if all, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(all) != 1 {
		t.Fatalf("beads after a second failed run: %d (%v), want the one", len(all), err)
	}
}

// The write that makes the bead is the one that looks whether a bead
// tracks the thread: where one came to meanwhile, nothing is made.
func TestMakeIsRefusedOfATrackedThread(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
		t.Fatalf("hook_rule events on no bead: %q", got)
	}
}

// A makes line whose bead the store refuses (a stage the type's flow has
// not in the depot) is heard, not only logged: beside the hook_rule event
// the yard logs make_refused, of the class attention, with why and the
// thread. A daemon that starts again reads both and makes nothing of
// either (yardr-pdpk).
func TestAMakesLineTheStoreRefusesIsHeard(t *testing.T) {
	t.Parallel()
	r := newMakingDepot(t, "event=pull_request action=opened -> task:nowhere@yard")
	r.deliverAs(t, "gh", "pull_request", "pull_request_opened", nil)
	r.pass(t)
	if tracks, _ := r.tracking(t, pull17); len(tracks) != 0 {
		t.Fatalf("%v made in a stage the flow has not", tracks)
	}
	wantRules(t, r.beadless(t), "refused: stage nowhere is not in flow yard")
	evs, err := r.api.Events(context.Background(), "", 500)
	must(t, err)
	var found []store.Event
	for _, e := range evs {
		if e.Kind == "make_refused" {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("make_refused events: %d, want one", len(found))
	}
	got := found[0]
	if got.Bead != nil {
		t.Fatalf("make_refused on bead %v, want none", *got.Bead)
	}
	if refused, _ := got.Data["refused"].(string); !strings.HasPrefix(refused, "stage nowhere is not in flow yard") {
		t.Fatalf("make_refused refused %q", got.Data["refused"])
	}
	for _, k := range []string{"hook", "event", "makes", "depot", "thread"} {
		if got.Data[k] == nil {
			t.Fatalf("make_refused without %s: %v", k, got.Data)
		}
	}
	if got.Data["thread"] != pull17 {
		t.Fatalf("make_refused thread %v, want %s", got.Data["thread"], pull17)
	}
	if !slices.Contains(store.AttentionKinds, "make_refused") {
		t.Fatalf("make_refused is not of the class attention: %v", store.AttentionKinds)
	}

	// A daemon that starts again reads the log from the start and makes
	// nothing of what it reads there: the second event leaves off where
	// the first did.
	fresh := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI, DepotRepo: r.d.DepotRepo}
	must(t, fresh.Reconcile(context.Background()))
	wantRules(t, r.beadless(t), "refused: stage nowhere is not in flow yard")
	evs, err = r.api.Events(context.Background(), "", 500)
	must(t, err)
	again := 0
	for _, e := range evs {
		if e.Kind == "make_refused" {
			again++
		}
	}
	if again != 1 {
		t.Fatalf("make_refused events after a restart: %d, want the one", again)
	}
}

// A makes line that files in a stage only a members group may reach —
// every stage before it is held by one, and the hook is a member of none —
// is refused by the store as any filing past that stage is, and heard the
// same way: make_refused, of the class attention, naming the group whose
// members may file there.
func TestAMakesLineIntoAMembersStageIsRefusedAndHeard(t *testing.T) {
	t.Parallel()
	r := newMakingDepot(t, "event=pull_request action=opened -> task:review")
	ctx := context.Background()
	must(t, r.api.PutGroup(ctx, store.Group{Name: "maintainers", Runner: "manual", Limit: 50, Members: []string{"mckean", "yard"}}, "me"))
	r.deliverAs(t, "gh", "pull_request", "pull_request_opened", nil)
	r.pass(t)
	if tracks, _ := r.tracking(t, pull17); len(tracks) != 0 {
		t.Fatalf("%v made in a stage only members may file in", tracks)
	}
	wantRules(t, r.beadless(t), "refused: only members of group maintainers may file a bead in stage review: mckean, yard (you are \"hook:gh\")")
	evs, err := r.api.Events(ctx, "", 500)
	must(t, err)
	var found []store.Event
	for _, e := range evs {
		if e.Kind == "make_refused" {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("make_refused events: %d, want one", len(found))
	}
	if refused, _ := found[0].Data["refused"].(string); !strings.Contains(refused, "only members of group maintainers may file a bead in stage review") {
		t.Fatalf("make_refused refused %q", found[0].Data["refused"])
	}
	if !slices.Contains(store.AttentionKinds, "make_refused") {
		t.Fatalf("make_refused is not of the class attention: %v", store.AttentionKinds)
	}
}

// What the yard did itself makes nothing, as it says nothing: an issue a
// crew member opened is no one's from outside.
func TestTheYardsOwnActionMakesNothing(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
		t.Fatalf("hook_rule events on no bead: %q", got)
	}
}

// tokenBoxFlow is the flow box beads follow in a token depot: requested,
// then closed.
const tokenBoxFlow = `
requested -> closed on done
closed terminal
`

// newTokenDepot is newGHDepot with a token hook carrying lines, and box
// beads following tokenBoxFlow in yard.
func newTokenDepot(t *testing.T, lines ...string) *ghDepot {
	t.Helper()
	r := newGHDepot(t)
	ctx := context.Background()
	flow, err := store.ParseFlow(tokenBoxFlow)
	must(t, err)
	flow.Name = "box"
	_, err = r.api.PutFlow(ctx, *flow, "me")
	must(t, err)
	must(t, r.api.SetFlowPointer(ctx, "yard", "box", "box", "me"))
	h := store.Hook{Name: "provisioner", Source: "token", SecretFile: "/nonexistent/token.secret"}
	for _, l := range lines {
		m, err := store.ParseMake(l)
		must(t, err)
		h.Makes = append(h.Makes, m)
	}
	_, err = r.api.PutHook(ctx, h, "me")
	must(t, err)
	return r
}

// deliverToken records body as provisioner took it with the delivery id: as
// the receiver would, normalized as a token delivery and through its
// dedupe keys. It answers whether the delivery was a redelivery.
func (r *ghDepot) deliverToken(t *testing.T, body, id string) (dup bool) {
	t.Helper()
	env, err := hooks.Token{}.Normalize(nil, []byte(body))
	must(t, err)
	env.Delivery = id
	_, dup, err = r.own.RecordHook(context.Background(), "provisioner", env.Map(), []byte(body),
		hooks.DedupeKeys("provisioner", "token", hooks.Token{}, []byte(body), id))
	must(t, err)
	return dup
}

// A token hook's line makes a standalone bead from a delivery with no
// external thread: of the line's type and stage in the depot the line
// names, with no tracks link, the delivery's request fields in metadata
// under hook.* and listed in the body. The token actor is
// sender-asserted, so even one claiming a linked user's id files on the
// operator's, with no originator fallback recorded. Reposting the same
// delivery id makes no second bead, and a malformed or an incomplete
// delivery makes none: refusal is no bead.
func TestTokenDeliveryMakesStandaloneBead(t *testing.T) {
	t.Parallel()
	r := newTokenDepot(t, "event=box verb=create -> box:requested@yard")
	ctx := context.Background()
	if _, err := r.api.AddUser(ctx, "jo", "jo"); err != nil {
		t.Fatal(err)
	}
	must(t, r.api.SetSetting(ctx, store.OperatorSetting, "jo", "jo"))
	if _, err := r.api.AddUser(ctx, "hal", "jo"); err != nil {
		t.Fatal(err)
	}
	// hal would bill their own login on the threaded path: linked, with a
	// personal group and route. A standalone bead stays the operator's, and
	// the sender-asserted actor cannot make the yard mistake it for its own.
	must(t, r.api.SetIdentity(ctx, "hal", "token", store.Identity{ID: "u-hal"}, "jo"))
	must(t, r.api.SetIdentity(ctx, "yardmaster", "token", store.Identity{ID: "u-hal"}, "jo"))
	halGroup(t, r, writeEnv(t))
	halRoute(t, r, "requested", "hal-builders")

	const handle = `h-1$(id)` + "`x`" + `"q"`
	if dup := r.deliverToken(t, `{"type":"box","verb":"create","handle":`+quoteForJSON(t, handle)+
		`,"yard":"y-1","size":"big","region":"iad","text":"provision a box please","actor":{"id":"u-hal","name":"Hal"}}`, "d-1"); dup {
		t.Fatal("the first delivery is a redelivery")
	}
	r.pass(t)
	all, err := r.api.ListBeads(ctx, store.BeadFilter{})
	must(t, err)
	if len(all) != 1 {
		t.Fatalf("beads: %d, want the one", len(all))
	}
	b := r.bead(t, all[0].ID)
	if b.Type != "box" || b.Stage != "requested" || b.Depot == nil || *b.Depot != "yard" || b.Status != "open" {
		t.Fatalf("made %s: type %s, stage %s, depot %v, %s", b.ID, b.Type, b.Stage, b.Depot, b.Status)
	}
	if b.Title != "provision a box please" {
		t.Fatalf("made %s: title %q", b.ID, b.Title)
	}
	if b.CreatedBy == nil || *b.CreatedBy != "hook:provisioner" {
		t.Fatalf("made by %v, want hook:provisioner", b.CreatedBy)
	}
	if b.Originator != "jo" {
		t.Fatalf("made %s for %q, want the operator's", b.ID, b.Originator)
	}
	if b.Metadata["hook_seq"] == nil {
		t.Fatalf("made %s without hook_seq: %v", b.ID, b.Metadata)
	}
	for k, want := range map[string]string{"verb": "create", "handle": handle, "yard": "y-1", "size": "big", "region": "iad"} {
		if got, _ := b.Metadata["hook."+k].(string); got != want {
			t.Fatalf("made %s: hook.%s = %q, want %q", b.ID, k, got, want)
		}
	}
	if want := "handle: " + handle + "\nregion: iad\nsize: big\nverb: create\nyard: y-1\n\nprovision a box please\n\ndelivery d-1"; b.Body != want {
		t.Fatalf("made %s: body %q, want %q", b.ID, b.Body, want)
	}
	if links, err := r.api.Links(ctx, b.ID); err != nil || len(links) != 0 {
		t.Fatalf("made %s with links %+v (%v), want none", b.ID, links, err)
	}
	if data := makeEvent(t, r, b.ID); data["makes"] != "event=box verb=create -> box:requested@yard" ||
		data["depot"] != "yard" || data["linked_author"] != nil || data["originator_rule"] != nil {
		t.Fatalf("hook_rule of the make: %v", data)
	}
	wantRules(t, r.rules(t, b.ID), "box made "+b.ID)

	// Reposting the same delivery id is the receiver's duplicate: no event,
	// no second bead.
	if dup := r.deliverToken(t, `{"type":"box","verb":"create","handle":"h-1"}`, "d-1"); !dup {
		t.Fatal("reposting the delivery id recorded it again")
	}
	r.pass(t)
	if all, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(all) != 1 {
		t.Fatalf("beads after the repost: %d (%v), want the one", len(all), err)
	}

	// A malformed body (not a JSON object) and an incomplete one (no verb)
	// match no line and make nothing.
	r.deliverToken(t, `[1,2]`, "d-2")
	r.deliverToken(t, `{"type":"box","text":"no verb"}`, "d-3")
	r.pass(t)
	if all, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(all) != 1 {
		t.Fatalf("beads after a malformed and an incomplete delivery: %d (%v), want the one", len(all), err)
	}

	// A delivery with no text still files, titled by its type and id.
	r.deliverToken(t, `{"type":"box","verb":"create"}`, "d-4")
	r.pass(t)
	all, err = r.api.ListBeads(ctx, store.BeadFilter{})
	must(t, err)
	if len(all) != 2 {
		t.Fatalf("beads after a textless delivery: %d, want two", len(all))
	}
	var textless *store.Bead
	for _, c := range all {
		if c.ID != b.ID {
			textless = r.bead(t, c.ID)
		}
	}
	if textless.Title != "box request d-4" || textless.Body != "verb: create\n\ndelivery d-4" {
		t.Fatalf("textless bead: title %q, body %q", textless.Title, textless.Body)
	}
}

// A token delivery's request fields are data, never routing: one naming a
// train files the standalone bead its line makes all the same, carrying
// no hook.train, and the train is moved nowhere, whatever the hook's says
// table holds for the delivery.
func TestTokenTrainFieldMovesNoTrain(t *testing.T) {
	t.Parallel()
	r := newTokenDepot(t, "event=box verb=create -> box:requested@yard")
	ctx := context.Background()
	h, err := r.api.GetHook(ctx, "provisioner")
	must(t, err)
	h.Says = says(t, "event=box type=train -> done")
	if _, err := r.api.PutHook(ctx, *h, "me"); err != nil {
		t.Fatal(err)
	}
	r.train(t, "yard-a", "in-pr")
	r.deliverToken(t, `{"type":"box","verb":"create","handle":"h-9","train":"yard-a"}`, "d-train")
	r.pass(t)
	all, err := r.api.ListBeads(ctx, store.BeadFilter{})
	must(t, err)
	if len(all) != 2 {
		t.Fatalf("beads: %d, want the train and the one", len(all))
	}
	var made *store.Bead
	for _, c := range all {
		if c.ID != "yard-a" {
			made = r.bead(t, c.ID)
		}
	}
	if made.Type != "box" || made.Stage != "requested" {
		t.Fatalf("made %s: type %s, stage %s", made.ID, made.Type, made.Stage)
	}
	if _, ok := made.Metadata["hook.train"]; ok {
		t.Fatalf("made %s carries hook.train: %v", made.ID, made.Metadata)
	}
	if tr := r.bead(t, "yard-a"); tr.Stage != "in-pr" || tr.Metadata["hook_seq"] != nil {
		t.Fatalf("train judged: stage %s, metadata %v", tr.Stage, tr.Metadata)
	}
	if rules := r.rules(t, "yard-a"); len(rules) != 0 {
		t.Fatalf("hook_rule events on the train: %v", rules)
	}
}

// quoteForJSON is s as a JSON string, for a token body carrying shell
// metacharacters that must reach the bead byte-identical.
func quoteForJSON(t *testing.T, s string) string {
	t.Helper()
	raw, err := json.Marshal(s)
	must(t, err)
	return string(raw)
}

// The depot's repository is asked at each event, not kept: a stub that
// answers one repository and then another makes a bead in the depot of an
// event from each, without a new Dispatcher.
func TestDepotRepoIsAskedAtEachEvent(t *testing.T) {
	t.Parallel()
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
