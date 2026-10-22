package dispatch_test

// The pr flow of the contributors pack, whole, as the dispatcher runs it: the
// pack's flow, its makes and says tables and the routes its yard adds for the
// yard's merge, with GitHub's deliveries normalized through the github pack as
// a yard does. A bead is made by a pull request's event, waits for CI in
// ciwait, and is routed from approved by its label, all by the events and
// reports a person or a script would make; what merge-pr does with a bead it
// is given is contrib/assembly's test.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// newContributorsYard is a yard whose depot yard, of acme/yardr, follows the
// contributors pack for beads of the type pr: its flow, and the hook gh with
// the pack's tables. The yard has the groups and routes docs/line.md, "Merging
// pull requests", sets up: pr-merges takes a pr bead in approved that carries
// merge-ok, as a yard default that outranks the depot's own route of a pr bead
// in approved to the decisions, at 11. The review stage has no route: the
// reviewer's report is made by the test.
func newContributorsYard(t *testing.T) *ghDepot {
	t.Helper()
	r := newGHDepot(t)
	ctx := context.Background()
	sub, err := fs.Sub(packs.FS, packs.Contributors)
	must(t, err)
	p, err := pack.Load(sub, packs.Embedded)
	must(t, err)
	for _, f := range p.Flows {
		if f.Name == "pr" {
			_, err := r.api.PutFlow(ctx, f.StoreFlow("contributors.pr"), "me")
			must(t, err)
		}
	}
	must(t, r.api.SetFlowPointer(ctx, "yard", "pr", "contributors.pr", "me"))
	h, err := r.api.GetHook(ctx, "gh")
	must(t, err)
	h.Says, h.Makes = p.Sources[0].Says, p.Sources[0].Makes
	_, err = r.api.PutHook(ctx, *h, "me")
	must(t, err)

	must(t, r.api.PutGroup(ctx, store.Group{Name: "pr-merges", Runner: "fake", Limit: 1}, "me"))
	pr, merge, everyone := "pr", "merge-ok", store.OriginatorAny
	depot := "yard"
	_, err = r.api.AddRoute(ctx, store.Route{Stage: "approved", Type: &pr, Label: &merge, Originator: &everyone, Priority: 12, Group: "pr-merges"}, "me")
	must(t, err)
	_, err = r.api.AddRoute(ctx, store.Route{Stage: "approved", Type: &pr, Depot: &depot, Originator: &everyone, Priority: 11, Group: "decisions"}, "me")
	must(t, err)
	r.d.DepotRepo = func(_ context.Context, q dispatch.DepotRepoQuery) (string, error) {
		if q.Depot.Name == "yard" {
			return "acme/yardr", nil
		}
		return "", nil
	}
	// The head of a pull request that no event has told is read live.
	r.d.PRHead = func(context.Context, dispatch.PRQuery) (string, error) { return fixtureSHA, nil }
	return r
}

// movedSHA is the head a push moves the fixtures' pull request to, from
// fixtureSHA.
const movedSHA = "c0ffee5a1e0f4b2d8a97163e5d4c3b2a1f0e9d8c"

// deliverJSON records fixture name as GitHub delivered it, its JSON changed
// by each of edits first.
func (r *ghDepot) deliverJSON(t *testing.T, event, name string, edits ...func(map[string]any)) {
	t.Helper()
	body, err := os.ReadFile("../hooks/testdata/github/" + name + ".json")
	must(t, err)
	var m map[string]any
	must(t, json.Unmarshal(body, &m))
	for _, edit := range edits {
		edit(m)
	}
	out, err := json.Marshal(m)
	must(t, err)
	r.record(t, "gh", event, string(out))
}

// prEvent edits the pull_request fixtures to pull request number by
// sender, a user or a bot, from a branch of the repository itself.
func prEvent(number int, login, kind string) func(map[string]any) {
	return func(m map[string]any) {
		who := map[string]any{"login": login, "type": kind}
		m["number"], m["sender"] = number, who
		pr := m["pull_request"].(map[string]any)
		pr["number"], pr["user"] = number, who
		pr["html_url"] = fmt.Sprintf("https://github.com/acme/yardr/pull/%d", number)
		pr["title"] = "build(deps): bump golang.org/x/net"
		pr["head"].(map[string]any)["ref"] = "dependabot/go_modules/x-net"
	}
}

// pushedBy edits the pull_request fixtures to a push by login, a user or a
// bot, which is not the author of the pull request when it follows prEvent.
func pushedBy(login, kind string) func(map[string]any) {
	return func(m map[string]any) {
		m["sender"] = map[string]any{"login": login, "type": kind}
	}
}

// atHead edits the pull_request, workflow_run and check_suite fixtures to the
// head commit sha: a push to the pull request, and the run of CI on it.
func atHead(sha string) func(map[string]any) {
	return func(m map[string]any) {
		if pr, ok := m["pull_request"].(map[string]any); ok {
			pr["head"].(map[string]any)["sha"] = sha
		}
		for _, key := range []string{"workflow_run", "check_suite"} {
			if run, ok := m[key].(map[string]any); ok {
				run["head_sha"] = sha
			}
		}
	}
}

// authorIs edits the pull_request fixtures to a pull request whose author is
// association to the repository, as GitHub spells it: OWNER, MEMBER, NONE.
func authorIs(association string) func(map[string]any) {
	return func(m map[string]any) {
		m["pull_request"].(map[string]any)["author_association"] = association
	}
}

// ciRun edits the workflow_run and check_suite fixtures to a run that
// concluded, on the pull request number, or on none when number is 0, as a
// fork's runs are told.
func ciRun(key string, number int, conclusion string) func(map[string]any) {
	return func(m map[string]any) {
		r := m[key].(map[string]any)
		r["conclusion"], r["head_branch"] = conclusion, "dependabot/go_modules/x-net"
		r["pull_requests"] = []any{}
		if number > 0 {
			r["pull_requests"] = []any{map[string]any{"number": number, "head": map[string]any{"ref": "dependabot/go_modules/x-net"}, "base": map[string]any{"ref": "main"}}}
		}
	}
}

// reviewerSays reports bead id done as the reviewer's session would, with
// merge-ok when label is set, which the reviewer adds before it reports.
func (r *ghDepot) reviewerSays(t *testing.T, id string, label bool) {
	t.Helper()
	ctx := context.Background()
	if label {
		_, err := r.api.UpdateBead(ctx, id, store.BeadPatch{AddLabels: []string{"merge-ok"}}, store.Expect{}, "me")
		must(t, err)
	}
	_, err := r.api.Done(ctx, id, "done", "Ready", store.Expect{Stage: "review"}, "me")
	must(t, err)
}

// madeIn is the bead the pull request number made, in want, after the pass.
func (r *ghDepot) madeIn(t *testing.T, number int, want string) *store.Bead {
	t.Helper()
	r.pass(t)
	b := r.made(t, fmt.Sprintf("https://github.com/acme/yardr/pull/%d", number))
	if b.Type != "pr" || b.Stage != want {
		t.Fatalf("pull request %d made %s of type %s in %s, want a pr in %s", number, b.ID, b.Type, b.Stage, want)
	}
	return b
}

// routedTo is the group the yard's routes give bead id to now.
func (r *ghDepot) routedTo(t *testing.T, id string) string {
	t.Helper()
	routes, err := r.api.Routes(context.Background())
	must(t, err)
	dispatch.SortRoutes(routes)
	b := r.bead(t, id)
	if route := dispatch.Match(routes, *b, ""); route != nil {
		return route.Group
	}
	return ""
}

// reviewedBy gives the yard the group of pr-reviewers of a fake runner, and a
// route to it for pull requests in review, which the yard of these tests
// leaves unrouted: they report the reviewer's verdict themselves.
func (r *ghDepot) reviewedBy(t *testing.T, group string) {
	t.Helper()
	ctx := context.Background()
	must(t, r.api.PutGroup(ctx, store.Group{Name: group, Runner: "fake", Limit: 5}, "me"))
	pr, everyone := "pr", store.OriginatorAny
	_, err := r.api.AddRoute(ctx, store.Route{Stage: "review", Type: &pr, Originator: &everyone, Group: group}, "me")
	must(t, err)
}

// sessionsFor are the sessions the daemon started for bead id, oldest first.
func (r *ghDepot) sessionsFor(id string) []string {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	var sessions []string
	for _, j := range r.f.jobs {
		if j.Bead.ID == id {
			sessions = append(sessions, j.Session.ID)
		}
	}
	return sessions
}

// stoppedSessions are the sessions the daemon stopped.
func (r *ghDepot) stoppedSessions() []string {
	r.f.mu.Lock()
	defer r.f.mu.Unlock()
	return slices.Clone(r.f.stopped)
}

// startedFor are the groups the daemon started sessions for bead id with.
func (r *ghDepot) startedFor(id string) []string {
	var groups []string
	for _, j := range r.f.jobs {
		if j.Bead.ID == id {
			groups = append(groups, j.Group.Name)
		}
	}
	return groups
}

// The bead of a bot's bump of Go modules: skips triage into review, is
// reported done by the reviewer, which has marked it merge-ok, and waits in
// ciwait, where nothing starts and a cancelled run says nothing. CI that
// succeeded on its head sends it to approved, where the label has the
// daemon give it to pr-merges.
func TestContributorsBotBumpFlowsFromOpenedToTheMerge(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(17, "dependabot[bot]", "Bot"))
	b := r.madeIn(t, 17, "review")

	r.reviewerSays(t, b.ID, true)
	if st := r.stageOf(t, b.ID); st != "ciwait" {
		t.Fatalf("%s in %s after the reviewer reported done, want ciwait", b.ID, st)
	}
	r.pass(t)
	if got := r.f.starts(); len(got) != 0 {
		t.Fatalf("sessions started for a bead in ciwait: %v", got)
	}

	// A run that was cancelled says nothing; the bead keeps waiting.
	r.deliverJSON(t, "check_suite", "check_suite_cancelled", ciRun("check_suite", 17, "cancelled"))
	r.pass(t)
	if st := r.stageOf(t, b.ID); st != "ciwait" {
		t.Fatalf("%s in %s after a cancelled run, want ciwait", b.ID, st)
	}

	r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", 17, "success"))
	r.pass(t)
	r.pass(t)
	if got := r.bead(t, b.ID); got.Stage != "approved" {
		t.Fatalf("%s in %s after CI succeeded on its head, want approved", b.ID, got.Stage)
	}
	if got := r.startedFor(b.ID); !slices.Equal(got, []string{"pr-merges"}) {
		t.Fatalf("%s given to %v in approved with merge-ok, want pr-merges", b.ID, got)
	}
}

// What is not a bot's bump of Go modules waits at approved for a person: a
// pull request without merge-ok falls to the depot's route to decisions, and
// is given to pr-merges only once the yes is said, the label and the sha of
// the head together.
func TestContributorsUnlabeledPullRequestWaitsForTheYes(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(18, "dependabot[bot]", "Bot"))
	b := r.madeIn(t, 18, "review")
	r.reviewerSays(t, b.ID, false)
	r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", 18, "success"))
	r.pass(t)
	r.pass(t)

	if got := r.bead(t, b.ID); got.Stage != "approved" {
		t.Fatalf("%s in %s, want approved", b.ID, got.Stage)
	}
	if to := r.routedTo(t, b.ID); to != "decisions" {
		t.Fatalf("%s in approved without merge-ok is routed to %q, want decisions", b.ID, to)
	}
	if started := r.startedFor(b.ID); len(started) != 0 {
		t.Fatalf("a session started for a bead without merge-ok: %v", started)
	}

	// The yes, as the maintainers' one command does it.
	_, err := r.api.UpdateBead(context.Background(), b.ID, store.BeadPatch{AddLabels: []string{"merge-ok"},
		Metadata: map[string]any{"merge_head": fixtureSHA}}, store.Expect{}, "me")
	must(t, err)
	r.pass(t)
	if to := r.routedTo(t, b.ID); to != "pr-merges" {
		t.Fatalf("%s with the yes is routed to %q, want pr-merges", b.ID, to)
	}
	if started := r.startedFor(b.ID); !slices.Equal(started, []string{"pr-merges"}) {
		t.Fatalf("%s given to %v after the yes, want pr-merges", b.ID, started)
	}
}

// CI that failed sends a pull request back to its contributor, from the
// review and from the wait; a person's push while it is read or waits does
// too, before any success for the head it had can approve it; and a success
// anywhere but in the wait says nothing. The runs of a fork's branch name no
// pull request, and no bead hears them. A bot's push starts the read again
// instead (TestContributorsBotPushStartsTheReadAgain).
func TestContributorsRedCIAndPushesSendItBack(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	open := func(n int) *store.Bead {
		r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(n, "dependabot[bot]", "Bot"))
		return r.madeIn(t, n, "review")
	}

	// Red CI while the reviewer still reads, and while the bead waits: a
	// workflow run, and a check suite.
	reading := open(21)
	r.deliverJSON(t, "workflow_run", "workflow_run_failure", ciRun("workflow_run", 21, "failure"))
	r.pass(t)
	if st := r.stageOf(t, reading.ID); st != "contributor" {
		t.Fatalf("%s in %s after CI failed under review, want contributor", reading.ID, st)
	}
	waiting := open(22)
	r.reviewerSays(t, waiting.ID, false)
	r.deliverJSON(t, "check_suite", "check_suite_cancelled", ciRun("check_suite", 22, "failure"))
	r.pass(t)
	if st := r.stageOf(t, waiting.ID); st != "contributor" {
		t.Fatalf("%s in %s after CI failed while it waited, want contributor", waiting.ID, st)
	}
	if note := r.lastNote(t, waiting.ID); !strings.Contains(note, "CI failed on the PR") {
		t.Fatalf("note of the move: %q", note)
	}

	// A person's push while it waits bounces it, and a success that comes
	// after for the head it had approves nothing.
	pushed := open(23)
	r.reviewerSays(t, pushed.ID, false)
	r.deliverJSON(t, "pull_request", "pull_request_synchronize", prEvent(23, "dependabot[bot]", "Bot"), pushedBy("a-maintainer", "User"))
	r.pass(t)
	if st := r.stageOf(t, pushed.ID); st != "contributor" {
		t.Fatalf("%s in %s after the pull request changed while it waited on CI, want contributor", pushed.ID, st)
	}
	if note := r.lastNote(t, pushed.ID); !strings.Contains(note, "the PR changed while waiting on CI") {
		t.Fatalf("note of the move: %q", note)
	}
	r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", 23, "success"))
	r.pass(t)
	if st := r.stageOf(t, pushed.ID); st != "contributor" {
		t.Fatalf("%s in %s after a success for the head it had, want contributor", pushed.ID, st)
	}

	// And a person's push while the reviewer still reads.
	changed := open(26)
	r.deliverJSON(t, "pull_request", "pull_request_synchronize", prEvent(26, "dependabot[bot]", "Bot"), pushedBy("a-maintainer", "User"))
	r.pass(t)
	if st := r.stageOf(t, changed.ID); st != "contributor" {
		t.Fatalf("%s in %s after a person pushed under review, want contributor", changed.ID, st)
	}
	if note := r.lastNote(t, changed.ID); !strings.Contains(note, "the PR changed under review") {
		t.Fatalf("note of the move: %q", note)
	}

	// A success while the reviewer still reads approves nothing.
	unread := open(24)
	r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", 24, "success"))
	r.pass(t)
	if st := r.stageOf(t, unread.ID); st != "review" {
		t.Fatalf("%s in %s after CI succeeded while it was read, want review", unread.ID, st)
	}

	// A fork's runs name no pull request: the bead keeps waiting for a hand.
	forks := open(25)
	r.reviewerSays(t, forks.ID, false)
	r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", 0, "success"))
	r.pass(t)
	if st := r.stageOf(t, forks.ID); st != "ciwait" {
		t.Fatalf("%s in %s after a run that named no pull request, want ciwait", forks.ID, st)
	}
}

// A bot's push to a pull request while it is read, or while it waits on CI,
// starts the read again at the new head, where it would otherwise park the
// bead in contributor for a push that a bot, whose rebase is the only one it
// makes, does not send. From the review the session on the old head is
// stopped and a new one starts, as a new stay in the same stage; from the wait
// the bead goes back to review, and once it is read again a success for the
// head it left says nothing, where one for the new head approves it.
func TestContributorsBotPushStartsTheReadAgain(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	r.reviewedBy(t, "pr-reviewers")
	now := time.Now()
	dispatch.SetClock(r.d, func() time.Time { return now })
	open := func(n int) *store.Bead {
		r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(n, "dependabot[bot]", "Bot"))
		return r.madeIn(t, n, "review")
	}

	// While the reviewer reads.
	reading := open(41)
	first := r.sessionsFor(reading.ID)
	if len(first) != 1 {
		t.Fatalf("%s read by %v before the push, want one session", reading.ID, first)
	}
	r.deliverJSON(t, "pull_request", "pull_request_synchronize", prEvent(41, "dependabot[bot]", "Bot"), atHead(movedSHA))
	r.pass(t)
	if st := r.stageOf(t, reading.ID); st != "review" {
		t.Fatalf("%s in %s after a bot pushed under review, want review", reading.ID, st)
	}
	if note := r.lastNote(t, reading.ID); !strings.Contains(note, "a bot pushed to the PR") {
		t.Fatalf("note of the move: %q", note)
	}
	// The dispatcher waits before it reads again a bead its flow sent back to
	// the stage it is in, and doubles the wait for each return in a row; the
	// clock passes it.
	now = now.Add(time.Hour)
	r.pass(t)
	if got := r.sessionsFor(reading.ID); len(got) != 2 || got[0] != first[0] {
		t.Fatalf("%s read by %v after a bot pushed under review, want a second session after %v", reading.ID, got, first)
	}
	if !slices.Contains(r.stoppedSessions(), first[0]) {
		t.Fatalf("session %s of the old head still runs: stopped %v", first[0], r.stoppedSessions())
	}

	// While it waits on CI: back to review, read at the new head, then CI on
	// the new head and not on the old one approves it.
	waiting := open(42)
	r.reviewerSays(t, waiting.ID, false)
	r.deliverJSON(t, "pull_request", "pull_request_synchronize", prEvent(42, "dependabot[bot]", "Bot"), atHead(movedSHA))
	r.pass(t)
	if st := r.stageOf(t, waiting.ID); st != "review" {
		t.Fatalf("%s in %s after a bot pushed while it waited on CI, want review", waiting.ID, st)
	}
	if note := r.lastNote(t, waiting.ID); !strings.Contains(note, "a bot pushed to the PR") {
		t.Fatalf("note of the move: %q", note)
	}
	r.pass(t)
	if got := r.sessionsFor(waiting.ID); len(got) != 2 {
		t.Fatalf("%s read by %v after a bot pushed while it waited, want a second session", waiting.ID, got)
	}
	r.reviewerSays(t, waiting.ID, false)
	r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", 42, "success"))
	r.pass(t)
	if st := r.stageOf(t, waiting.ID); st != "ciwait" {
		t.Fatalf("%s in %s after a success for the head it left, want ciwait", waiting.ID, st)
	}
	r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", 42, "success"), atHead(movedSHA))
	r.pass(t)
	if st := r.stageOf(t, waiting.ID); st != "approved" {
		t.Fatalf("%s in %s after a success for the new head, want approved", waiting.ID, st)
	}
}

// Who skips the maintainer, through the normalizing a yard does: the pull
// request of an owner or a member of the repository, as GitHub spells their
// association (OWNER, MEMBER) and the source's normalize lowercases it, and a
// bot's go to review; a human's from outside waits in triage. The pack's
// table is matched with fields typed by hand in packs_test.go, which cannot
// see whether the line is spelled as normalize writes it.
func TestContributorsWhoSkipTriage(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(31, "someone", "User"), authorIs("NONE"))
	r.madeIn(t, 31, "triage")
	r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(32, "renovate[bot]", "Bot"))
	r.madeIn(t, 32, "review")
	r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(33, "a-member", "User"), authorIs("MEMBER"))
	r.madeIn(t, 33, "review")
	r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(34, "the-owner", "User"), authorIs("OWNER"))
	r.madeIn(t, 34, "review")
}
