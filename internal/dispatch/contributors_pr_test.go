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

// deliverJSON records fixture name as GitHub delivered it, its JSON changed
// by edit first.
func (r *ghDepot) deliverJSON(t *testing.T, event, name string, edit func(map[string]any)) {
	t.Helper()
	body, err := os.ReadFile("../hooks/testdata/github/" + name + ".json")
	must(t, err)
	var m map[string]any
	must(t, json.Unmarshal(body, &m))
	edit(m)
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
// review and from the wait; a push while it waits does too, before any success
// for the head it had can approve it; and a success anywhere but in the wait
// says nothing. The runs of a fork's branch name no pull request, and no bead
// hears them.
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

	// A push while it waits bounces it, and a success that comes after for
	// the head it had approves nothing.
	pushed := open(23)
	r.reviewerSays(t, pushed.ID, false)
	r.deliverJSON(t, "pull_request", "pull_request_synchronize", prEvent(23, "dependabot[bot]", "Bot"))
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

// Who skips the maintainer, through the normalizing a yard does: a bot's pull
// request goes to review, a human's from outside waits in triage.
func TestContributorsOnlyBotsSkipTriage(t *testing.T) {
	t.Parallel()
	r := newContributorsYard(t)
	r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(31, "someone", "User"))
	r.madeIn(t, 31, "triage")
	r.deliverJSON(t, "pull_request", "pull_request_opened", prEvent(32, "renovate[bot]", "Bot"))
	r.madeIn(t, 32, "review")
}
