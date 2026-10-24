package dispatch_test

// The pr flow of the contributors pack, whole, as the dispatcher runs it: the
// pack's flow, its makes and says tables and the routes its yard adds for the
// yard's CI and merge, with GitHub's deliveries normalized through the github
// pack as a yard does. A bead is made by a pull request's event, is given to
// the yard's CI in ciwait, and is routed from approved by its label, all by
// the events and reports a person or a script would make; what pr-ci and
// merge-pr do with a bead they are given is contrib/assembly's test.

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"reflect"
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
// the pack's tables. The yard has the groups and routes docs/line.md, "The
// yard as the CI of a pull request" and "Merging pull requests", set up: pr-ci
// takes a pr bead in ciwait, and pr-merges one in approved that carries
// merge-ok, as a yard default that outranks the depot's own route of a pr bead
// in approved to the decisions, at 11. The review stage has no route: the
// reviewer's report is made by the test, as is what a session of pr-ci reports.
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

	must(t, r.api.PutGroup(ctx, store.Group{Name: "pr-ci", Runner: "fake", Limit: 1}, "me"))
	must(t, r.api.PutGroup(ctx, store.Group{Name: "pr-merges", Runner: "fake", Limit: 1}, "me"))
	pr, merge, everyone := "pr", "merge-ok", store.OriginatorAny
	depot := "yard"
	_, err = r.api.AddRoute(ctx, store.Route{Stage: "ciwait", Type: &pr, Originator: &everyone, Group: "pr-ci"}, "me")
	must(t, err)
	_, err = r.api.AddRoute(ctx, store.Route{Stage: "approved", Type: &pr, Label: &merge, Originator: &everyone, Group: "pr-merges"}, "me")
	must(t, err)
	_, err = r.api.AddRoute(ctx, store.Route{Stage: "approved", Type: &pr, Depot: &depot, Originator: &everyone, Group: "decisions"}, "me")
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

// ciSays reports bead id as the session of pr-ci would after the depot's tests
// ran on its head: passed, with the head recorded as ci_head, or not.
func (r *ghDepot) ciSays(t *testing.T, id string, passed bool) {
	t.Helper()
	ctx := context.Background()
	if !passed {
		_, err := r.api.Done(ctx, id, "changes", "the tests failed", store.Expect{Stage: "ciwait"}, "me")
		must(t, err)
		return
	}
	_, err := r.api.UpdateBead(ctx, id, store.BeadPatch{Metadata: map[string]any{"ci_head": fixtureSHA}}, store.Expect{}, "me")
	must(t, err)
	_, err = r.api.Done(ctx, id, "done", "the tests passed", store.Expect{Stage: "ciwait"}, "me")
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
	if route := dispatch.Match(routes, *b, "", nil); route != nil {
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
// reported done by the reviewer, which has marked it merge-ok, and is given to
// the yard's CI in ciwait, where no run of GitHub's CI, cancelled or not,
// moves it. The pass of the yard's own sends it to approved, where the label
// has the daemon give it to pr-merges.
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
	if got := r.startedFor(b.ID); !slices.Equal(got, []string{"pr-ci"}) {
		t.Fatalf("%s given to %v in ciwait, want pr-ci", b.ID, got)
	}

	// Runs of GitHub's CI say nothing, whatever they conclude; the bead stays
	// for the yard's.
	r.deliverJSON(t, "check_suite", "check_suite_cancelled", ciRun("check_suite", 17, "cancelled"))
	r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", 17, "success"))
	r.pass(t)
	if st := r.stageOf(t, b.ID); st != "ciwait" {
		t.Fatalf("%s in %s after runs of GitHub's CI, want ciwait", b.ID, st)
	}

	r.ciSays(t, b.ID, true)
	r.pass(t)
	if got := r.bead(t, b.ID); got.Stage != "approved" || got.Metadata["ci_head"] != fixtureSHA {
		t.Fatalf("%s in %s with ci_head %v after the yard's CI passed, want approved at %s", b.ID, got.Stage, got.Metadata["ci_head"], fixtureSHA)
	}
	if got := r.startedFor(b.ID); !slices.Equal(got, []string{"pr-ci", "pr-merges"}) {
		t.Fatalf("%s given to %v, want pr-ci in ciwait and pr-merges in approved with merge-ok", b.ID, got)
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
	r.ciSays(t, b.ID, true)
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

// The yard's CI sends a pull request back to its contributor when the tests
// failed; a person's push while they wait or run does too, before a pass for
// the head it had can approve it, and a report that comes after the push is
// refused as stale. The runs of GitHub's CI say nothing at any stage, failed or
// not: a failure under review or in the wait moves no bead, and a success
// approves nothing. A bot's push starts the read again instead
// (TestContributorsBotPushStartsTheReadAgain).
func TestContributorsRedCIAndPushesSendItBack(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	open := func(n int) *store.Bead {
		r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(n, "dependabot[bot]", "Bot"))
		return r.madeIn(t, n, "review")
	}

	// Tests that failed.
	red := open(21)
	r.reviewerSays(t, red.ID, false)
	r.ciSays(t, red.ID, false)
	if st := r.stageOf(t, red.ID); st != "contributor" {
		t.Fatalf("%s in %s after the yard's tests failed, want contributor", red.ID, st)
	}
	if got := r.bead(t, red.ID); got.Metadata["ci_head"] != nil {
		t.Fatalf("%s has ci_head %v after tests that failed", red.ID, got.Metadata["ci_head"])
	}

	// A person's push while it waits bounces it; the report of the run that was
	// of the head it had is refused, and approves nothing.
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
	if _, err := r.api.Done(context.Background(), pushed.ID, "done", "the tests passed", store.Expect{Stage: "ciwait"}, "me"); err == nil {
		t.Fatalf("a report from ciwait on %s was taken after it left for the contributor", pushed.ID)
	}
	if st := r.stageOf(t, pushed.ID); st != "contributor" {
		t.Fatalf("%s in %s after a late report, want contributor", pushed.ID, st)
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

	// Runs of GitHub's CI move nothing: not a failure under review, not one
	// in the wait, not a success while the reviewer still reads.
	unread, waiting := open(24), open(25)
	r.reviewerSays(t, waiting.ID, false)
	for _, n := range []int{24, 25} {
		r.deliverJSON(t, "workflow_run", "workflow_run_failure", ciRun("workflow_run", n, "failure"))
		r.deliverJSON(t, "check_suite", "check_suite_cancelled", ciRun("check_suite", n, "failure"))
		r.deliverJSON(t, "workflow_run", "workflow_run_success", ciRun("workflow_run", n, "success"))
		r.deliverJSON(t, "check_suite", "check_suite_cancelled", ciRun("check_suite", n, "success"))
	}
	r.pass(t)
	if st := r.stageOf(t, unread.ID); st != "review" {
		t.Fatalf("%s in %s after runs of GitHub's CI while it was read, want review", unread.ID, st)
	}
	if st := r.stageOf(t, waiting.ID); st != "ciwait" {
		t.Fatalf("%s in %s after runs of GitHub's CI while it waited, want ciwait", waiting.ID, st)
	}
}

// A bot's push to a pull request while it is read, or while it waits on CI,
// starts the read again at the new head, where it would otherwise park the
// bead in contributor for a push that a bot, whose rebase is the only one it
// makes, does not send. From the review the session on the old head is
// stopped and a new one starts, as a new stay in the same stage; from the wait
// the session of the yard's CI on the old head is stopped and a report it still
// makes is refused, the bead goes back to review, and once it is read again the
// yard's CI runs on it afresh, and its pass approves it.
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

	// While the yard's CI runs: its session on the old head is stopped, the
	// bead goes back to review and is read at the new head, and the yard's CI
	// on it, not a report of the old run, approves it.
	waiting := open(42)
	r.reviewerSays(t, waiting.ID, false)
	r.pass(t)
	run := r.sessionsFor(waiting.ID)
	if got := r.startedFor(waiting.ID); !slices.Equal(got, []string{"pr-reviewers", "pr-ci"}) {
		t.Fatalf("%s given to %v before the push, want the reviewer and then pr-ci", waiting.ID, got)
	}
	r.deliverJSON(t, "pull_request", "pull_request_synchronize", prEvent(42, "dependabot[bot]", "Bot"), atHead(movedSHA))
	r.pass(t)
	if st := r.stageOf(t, waiting.ID); st != "review" {
		t.Fatalf("%s in %s after a bot pushed while the yard's CI ran, want review", waiting.ID, st)
	}
	if note := r.lastNote(t, waiting.ID); !strings.Contains(note, "a bot pushed to the PR") {
		t.Fatalf("note of the move: %q", note)
	}
	if _, err := r.api.Done(context.Background(), waiting.ID, "done", "the tests passed", store.Expect{Stage: "ciwait"}, "me"); err == nil {
		t.Fatalf("a report from ciwait on %s was taken after it went back to review", waiting.ID)
	}
	r.pass(t)
	if got := r.startedFor(waiting.ID); !slices.Equal(got, []string{"pr-reviewers", "pr-ci", "pr-reviewers"}) {
		t.Fatalf("%s given to %v after a bot pushed while it waited, want a second reviewer", waiting.ID, got)
	}
	if !slices.Contains(r.stoppedSessions(), run[1]) {
		t.Fatalf("session %s of the yard's CI on the old head still runs: stopped %v", run[1], r.stoppedSessions())
	}
	r.reviewerSays(t, waiting.ID, false)
	r.pass(t)
	if got := r.startedFor(waiting.ID); !slices.Equal(got, []string{"pr-reviewers", "pr-ci", "pr-reviewers", "pr-ci"}) {
		t.Fatalf("%s given to %v once it was read again, want the yard's CI to run on the new head", waiting.ID, got)
	}
	r.ciSays(t, waiting.ID, true)
	r.pass(t)
	if st := r.stageOf(t, waiting.ID); st != "approved" {
		t.Fatalf("%s in %s after the yard's CI passed on the new head, want approved", waiting.ID, st)
	}
}

// A pull request merged or closed on GitHub ends its bead wherever the bead
// waits but approved, which had the edges first: in triage, under review, in
// the wait for the yard's CI, with the contributor and in decide, where a
// reviewer's question leaves it for a person. The pack's two lines name no
// stage, so a stage without an edge for them left the event nowhere to go, and
// the bead open on a pull request that was over. Where the bead is worked its
// session ends with it: the reviewer's is stopped, and so is the yard's CI,
// whose report is then refused as one from a stage the bead has left, as it is
// after a push.
func TestContributorsClosedPullRequestEndsItsBead(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	r.reviewedBy(t, "pr-reviewers")
	ctx := context.Background()
	// Triage, contributor and decide are stages of people, crossed as the
	// hook's crew member: the members of the maintainers and of the decisions
	// include it, as an import's must for the label accepted to move a bead,
	// and the contributor's group has none. Their runner starts nothing of the
	// yard's, as the daemon's manual one.
	r.d.Runners["manual"] = &fakeRunner{dead: map[string]bool{}}
	must(t, r.api.PutGroup(ctx, store.Group{Name: "maintainers", Runner: "manual", Limit: 50, Members: []string{"mckean", "yardmaster"}}, "me"))
	must(t, r.api.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 50, Members: []string{"mckean", "yardmaster"}}, "me"))
	must(t, r.api.PutGroup(ctx, store.Group{Name: "outside", Runner: "manual", Limit: 50}, "me"))
	pr, everyone := "pr", store.OriginatorAny
	peopleGroup := map[string]string{"triage": "maintainers", "contributor": "outside", "decide": "decisions"}
	for stage, group := range peopleGroup {
		_, err := r.api.AddRoute(ctx, store.Route{Stage: stage, Type: &pr, Originator: &everyone, Group: group}, "me")
		must(t, err)
	}

	// arrive makes the bead of a new pull request and takes it to stage as its
	// events and reports would: a stranger's waits in triage, a bot's goes
	// to the reader, whose verdict sends it on to the wait for CI, back to
	// its contributor, or to a person with a question.
	number := 50
	arrive := func(stage string) *store.Bead {
		number++
		if stage == "triage" {
			r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(number, "someone", "User"), authorIs("NONE"))
			return r.madeIn(t, number, "triage")
		}
		r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(number, "dependabot[bot]", "Bot"))
		b := r.madeIn(t, number, "review")
		switch stage {
		case "ciwait":
			r.reviewerSays(t, b.ID, false)
		case "contributor":
			_, err := r.api.Done(ctx, b.ID, "changes", "needs work", store.Expect{Stage: "review"}, "me")
			must(t, err)
		case "decide":
			_, err := r.api.Done(ctx, b.ID, "question", "is this change wanted?", store.Expect{Stage: "review"}, "me")
			must(t, err)
		}
		r.pass(t)
		if st := r.stageOf(t, b.ID); st != stage {
			t.Fatalf("%s in %s, want %s", b.ID, st, stage)
		}
		return b
	}

	// workedIn are the sessions of the yard that worked bead id in stage: the
	// reader's, the yard's CI. Nobody of the yard works the stages of people.
	workedIn := func(id, stage string) []string {
		r.f.mu.Lock()
		defer r.f.mu.Unlock()
		var sessions []string
		for _, j := range r.f.jobs {
			if j.Bead.ID == id && j.Session.Stage == stage {
				sessions = append(sessions, j.Session.ID)
			}
		}
		return sessions
	}

	for _, stage := range []string{"triage", "review", "ciwait", "contributor", "decide"} {
		for _, c := range []struct{ fixture, outcome, to string }{
			{"pull_request_closed_merged", "merged", "merged"},
			{"pull_request_closed_unmerged", "dropped", "closed"},
		} {
			b := arrive(stage)
			worked := workedIn(b.ID, stage)
			if _, people := peopleGroup[stage]; people != (len(worked) == 0) {
				t.Fatalf("%s in %s worked by %v", b.ID, stage, worked)
			}

			r.deliverJSON(t, "pull_request", c.fixture, prEvent(number, "someone", "User"))
			r.pass(t)
			got := r.bead(t, b.ID)
			if got.Stage != c.to || got.Status != "closed" {
				t.Errorf("%s: a pull request %s (%s) left its bead in %s (%s), want %s closed", stage, c.outcome, c.fixture, got.Stage, got.Status, c.to)
			}
			wantRules(t, r.rules(t, b.ID), "pull_request made", c.outcome+" "+stage+" -> "+c.to)

			// Whatever worked the stage is stopped, and the yard's CI cannot
			// report a pass for a pull request that is over.
			r.pass(t)
			for _, s := range worked {
				if !slices.Contains(r.stoppedSessions(), s) {
					t.Errorf("%s: session %s of the old stay still runs: stopped %v", stage, s, r.stoppedSessions())
				}
			}
			if stage == "ciwait" {
				if _, err := r.api.Done(ctx, b.ID, "done", "the tests passed", store.Expect{Stage: "ciwait"}, "me"); err == nil {
					t.Errorf("a report from ciwait on %s was taken after its pull request was %s", b.ID, c.outcome)
				}
				if got := r.bead(t, b.ID); got.Stage != c.to || got.Metadata["ci_head"] != nil {
					t.Errorf("%s in %s with ci_head %v after a late report, want %s with none", b.ID, got.Stage, got.Metadata["ci_head"], c.to)
				}
			}
		}
	}
}

// A pull request closed unmerged and then reopened has a bead again. The
// close ended the old one (TestContributorsClosedPullRequestEndsItsBead), so no
// open bead tracks the thread, and the lines that judged the pull request when
// it was opened make the next. The new bead starts clean, with no label, no
// note and none of the heads a merge is decided on, because what was read and
// tested before was of a head the pull request may no longer have. The old bead
// is as the close left it except for its link, which it hands on and keeps as
// one that only relates. The close is still in the log, and a daemon that
// starts again reads it without ending the new bead.
func TestContributorsReopenedPullRequestStartsAFreshBead(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	ctx := context.Background()
	const url = "https://github.com/acme/yardr/pull/61"
	bot := prEvent(61, "dependabot[bot]", "Bot")

	// A bot's bump, read, passed by the yard's CI and said yes to. It sits in
	// approved with everything a merge is decided on.
	r.deliverJSON(t, "pull_request", "pull_request_opened", bot)
	old := r.madeIn(t, 61, "review")
	r.reviewerSays(t, old.ID, true)
	r.ciSays(t, old.ID, true)
	_, err := r.api.UpdateBead(ctx, old.ID, store.BeadPatch{Metadata: map[string]any{
		"reviewed_head": fixtureSHA, "merge_head": fixtureSHA}}, store.Expect{}, "me")
	must(t, err)
	r.pass(t)

	r.deliverJSON(t, "pull_request", "pull_request_closed_unmerged", bot)
	r.pass(t)
	ended := r.bead(t, old.ID)
	endedNotes, err := r.api.Notes(ctx, old.ID)
	must(t, err)
	if ended.Stage != "closed" || ended.Status != "closed" || !slices.Contains(ended.Labels, "merge-ok") || len(endedNotes) == 0 {
		t.Fatalf("%s after the close: %s (%s) with labels %v and %d notes, want it closed with what it had",
			old.ID, ended.Stage, ended.Status, ended.Labels, len(endedNotes))
	}
	for _, k := range []string{"reviewed_head", "ci_head", "merge_head"} {
		if ended.Metadata[k] != fixtureSHA {
			t.Fatalf("%s after the close has %s %v, want %s", old.ID, k, ended.Metadata[k], fixtureSHA)
		}
	}

	// Reopened, at a head that moved while it was closed.
	r.deliverJSON(t, "pull_request", "pull_request_reopened", bot, atHead(movedSHA))
	r.pass(t)
	fresh := r.made(t, url)
	if fresh.ID == old.ID || fresh.Type != "pr" || fresh.Stage != "review" || fresh.Status != "open" {
		t.Fatalf("tracking %s: %s (%s, %s, %s) beside %s, want a new open pr in review", url, fresh.ID, fresh.Type, fresh.Stage, fresh.Status, old.ID)
	}
	if open, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(open) != 1 || open[0].ID != fresh.ID {
		t.Fatalf("open beads %v (%v), want %s alone", open, err, fresh.ID)
	}
	if _, relates := r.tracking(t, url); !slices.Equal(relates, []string{old.ID}) {
		t.Fatalf("beads relating to %s: %v, want the closed %s", url, relates, old.ID)
	}
	freshNotes, err := r.api.Notes(ctx, fresh.ID)
	must(t, err)
	if len(fresh.Labels) != 0 || len(freshNotes) != 0 || !slices.Equal(slices.Sorted(maps.Keys(fresh.Metadata)), []string{"hook_seq"}) {
		t.Fatalf("%s starts with labels %v, metadata %v and %d notes, want none but the event that made it", fresh.ID, fresh.Labels, fresh.Metadata, len(freshNotes))
	}
	wantRules(t, r.rules(t, fresh.ID), "pull_request made "+fresh.ID)

	// The old bead is as the close left it, and the reopen logged nothing on it.
	after := r.bead(t, old.ID)
	afterNotes, err := r.api.Notes(ctx, old.ID)
	must(t, err)
	if after.Stage != ended.Stage || after.Status != ended.Status || !slices.Equal(after.Labels, ended.Labels) ||
		!reflect.DeepEqual(after.Metadata, ended.Metadata) || !reflect.DeepEqual(afterNotes, endedNotes) {
		t.Fatalf("%s changed when the pull request was reopened: %+v with notes %v, was %+v with notes %v", old.ID, after, afterNotes, ended, endedNotes)
	}
	wantRules(t, r.rules(t, old.ID), "pull_request made "+old.ID, "dropped approved -> closed")

	// A daemon that starts again reads the log from the start, the close
	// among it, and makes and moves nothing.
	again := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI, DepotRepo: r.d.DepotRepo}
	must(t, again.Reconcile(ctx))
	if b := r.made(t, url); b.ID != fresh.ID || b.Stage != "review" || b.Status != "open" {
		t.Fatalf("after a restart %s tracks %s: %s (%s), want %s open in review", url, b.ID, b.Stage, b.Status, fresh.ID)
	}
	if open, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(open) != 1 {
		t.Fatalf("open beads after a restart: %d (%v), want one", len(open), err)
	}
	t.Run("close and reopen before one pass", testContributorsReopenedPullRequestInOnePassStartsAFreshBead)
}

// A pull request closed and reopened before the pass that reads either has
// a bead again, as when a pass runs between the two events (above): the
// close ends the old bead, and the reopen, later in the log, makes the
// next, which starts clean. Judging the reopen against when the pass ran
// would take it for an event from before the close and make nothing.
func testContributorsReopenedPullRequestInOnePassStartsAFreshBead(t *testing.T) {
	t.Helper()
	r := newContributorsYard(t)
	ctx := context.Background()
	const url = "https://github.com/acme/yardr/pull/62"
	bot := prEvent(62, "dependabot[bot]", "Bot")

	r.deliverJSON(t, "pull_request", "pull_request_opened", bot)
	old := r.madeIn(t, 62, "review")

	// Closed and reopened while the dispatcher is busy or down: one pass
	// reads both.
	r.deliverJSON(t, "pull_request", "pull_request_closed_unmerged", bot)
	r.deliverJSON(t, "pull_request", "pull_request_reopened", bot)
	r.pass(t)

	ended := r.bead(t, old.ID)
	if ended.Stage != "closed" || ended.Status != "closed" {
		t.Fatalf("%s after the close: %s (%s), want it closed", old.ID, ended.Stage, ended.Status)
	}
	fresh := r.made(t, url)
	if fresh.ID == old.ID || fresh.Type != "pr" || fresh.Stage != "review" || fresh.Status != "open" {
		t.Fatalf("tracking %s: %s (%s, %s, %s) beside %s, want a new open pr in review", url, fresh.ID, fresh.Type, fresh.Stage, fresh.Status, old.ID)
	}
	if open, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(open) != 1 || open[0].ID != fresh.ID {
		t.Fatalf("open beads %v (%v), want %s alone", open, err, fresh.ID)
	}
	if _, relates := r.tracking(t, url); !slices.Equal(relates, []string{old.ID}) {
		t.Fatalf("beads relating to %s: %v, want the closed %s", url, relates, old.ID)
	}
	freshNotes, err := r.api.Notes(ctx, fresh.ID)
	must(t, err)
	if len(fresh.Labels) != 0 || len(freshNotes) != 0 || !slices.Equal(slices.Sorted(maps.Keys(fresh.Metadata)), []string{"hook_seq"}) {
		t.Fatalf("%s starts with labels %v, metadata %v and %d notes, want none but the event that made it", fresh.ID, fresh.Labels, fresh.Metadata, len(freshNotes))
	}
	wantRules(t, r.rules(t, fresh.ID), "pull_request made "+fresh.ID)

	// The reopen logged nothing on the old bead, and the close recorded
	// its seq there, which the reopen was judged against.
	wantRules(t, r.rules(t, old.ID), "pull_request made "+old.ID, "dropped review -> closed")
	if ended.Metadata["close_seq"] == nil {
		t.Fatalf("%s carries no close_seq after its event closed it: %v", old.ID, ended.Metadata)
	}

	// A daemon that starts again reads the log from the start, the close
	// among it, and makes and moves nothing.
	again := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI, DepotRepo: r.d.DepotRepo}
	must(t, again.Reconcile(ctx))
	if b := r.made(t, url); b.ID != fresh.ID || b.Stage != "review" || b.Status != "open" {
		t.Fatalf("after a restart %s tracks %s: %s (%s), want %s open in review", url, b.ID, b.Stage, b.Status, fresh.ID)
	}
	if open, err := r.api.ListBeads(ctx, store.BeadFilter{}); err != nil || len(open) != 1 {
		t.Fatalf("open beads after a restart: %d (%v), want one", len(open), err)
	}
}

// Who skips the maintainer, through the normalizing a yard does: the pull
// request of an owner or a member of the repository, as GitHub spells their
// association (OWNER, MEMBER) and the source's normalize lowercases it, and a
// bot's go to review; a human's from outside waits in triage. The pack's
// table is matched with fields typed by hand in packs_test.go, which cannot
// see whether the line is spelled as normalize writes it. Closed and reopened,
// each is judged by the same lines, and sender is whoever reopened it, so a
// person who reopens a bot's pull request leaves it to the maintainer.
func TestContributorsWhoSkipTriage(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	for _, c := range []struct {
		number                   int
		login, kind, association string
		opened, reopened         string
		reopenedBy               string // a person other than its author who reopens it
	}{
		{31, "someone", "User", "NONE", "triage", "triage", ""},
		{32, "renovate[bot]", "Bot", "", "review", "review", ""},
		{33, "a-member", "User", "MEMBER", "review", "review", ""},
		{34, "the-owner", "User", "OWNER", "review", "review", ""},
		{35, "renovate[bot]", "Bot", "", "review", "triage", "a-maintainer"},
	} {
		author := []func(map[string]any){prEvent(c.number, c.login, c.kind)}
		if c.association != "" {
			author = append(author, authorIs(c.association))
		}
		r.deliverJSON(t, "pull_request", "pull_request_opened", author...)
		first := r.madeIn(t, c.number, c.opened)

		r.deliverJSON(t, "pull_request", "pull_request_closed_unmerged", author...)
		r.pass(t)
		if got := r.bead(t, first.ID); got.Status != "closed" {
			t.Fatalf("pull request %d closed: %s is %s in %s, want it closed", c.number, first.ID, got.Status, got.Stage)
		}
		reopen := author
		if c.reopenedBy != "" {
			reopen = append(slices.Clone(author), pushedBy(c.reopenedBy, "User"))
		}
		r.deliverJSON(t, "pull_request", "pull_request_reopened", reopen...)
		if again := r.madeIn(t, c.number, c.reopened); again.ID == first.ID {
			t.Fatalf("pull request %d reopened: %s is still its bead", c.number, again.ID)
		}
	}
}
