package dispatch_test

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/scripttest"
	"yardr.dev/yardr/internal/source"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The fixtures are yard-11's, for train yard-sv6b's PR 17 at fixtureSHA.
const fixtureSHA = "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"

// yardSays is the table of yardr's own github hook, for the train flow with
// a pull request (docs/line.md): the starter pack's lines, and CI's
// result for a train in in-pr.
var yardSays = []string{
	"event=pull_request action=closed merged=true -> merged: PR merged",
	"event=pull_request action=closed merged=false -> question: PR closed unmerged: reopen, or drop the train?",
	"event=pull_request_review action=submitted state=changes_requested sender=user -> changes: Changes requested on the PR",
	"event=workflow_run event=check_suite action=completed conclusion=failure conclusion=timed_out stage=review -> changes: CI failed on the head of the PR",
	"event=ci conclusion=success stage=in-pr -> done",
	"event=ci conclusion=failure stage=in-pr -> changes",
}

// says reads lines as yardr hook add --says takes them.
func says(t *testing.T, lines ...string) []store.Say {
	t.Helper()
	var out []store.Say
	for _, l := range lines {
		s, err := store.ParseSay(l)
		must(t, err)
		out = append(out, s)
	}
	return out
}

// ghDepot is a yard whose trains follow yard.train through pull requests, with
// the yard a member of the group that holds in-pr, and a github hook gh that
// carries yardSays.
type ghDepot struct {
	api store.API
	own *store.Store
	d   *dispatch.Dispatcher
	f   *fakeRunner
	// ci is what GitHub says of a commit's checks when the daemon asks
	// (see ci_test.go); asked are the questions it was asked.
	ci    func(dispatch.CIQuery) (dispatch.CIChecks, error)
	asked []dispatch.CIQuery
	// dec is the yard's declared github source, which deliveries to the
	// gh hook normalize through.
	dec hooks.Declared
}

// declareGitHubPack declares the pack's github source in the yard, with
// its command files under a home of the test's, and returns the declared
// adapter deliveries normalize through: the dispatcher reads its runs,
// heads, links and repositories from the declaration, as a real yard
// does.
func declareGitHubPack(t *testing.T, api store.API) hooks.Declared {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is not installed, and the pack's normalize is written in it")
	}
	dir := filepath.Join("..", "..", "packs", "github")
	text, err := os.ReadFile(filepath.Join(dir, "github.yaml"))
	must(t, err)
	d, err := source.Parse(filepath.Join(dir, "github.yaml"), text)
	must(t, err)
	home := t.TempDir()
	must(t, d.CopyCommandFiles(dir, home))
	scripttest.Relink(t, filepath.Join(home, "sources", d.Name))
	_, err = api.PutSource(context.Background(), "github.yaml", string(text), "me")
	must(t, err)
	return hooks.Declared{D: d, Home: home}
}

func newGHDepot(t *testing.T, members ...string) *ghDepot {
	t.Helper()
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	dec := declareGitHubPack(t, api)
	_, err := api.PutCrew(ctx, store.CrewMember{Name: "yardmaster", Runner: "fake"}, "me")
	must(t, err)
	must(t, api.SetIdentity(ctx, "yardmaster", "github", store.Identity{ID: "u-yardmaster"}, "me"))
	_, err0 := api.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: "/nonexistent/github.secret", As: "yardmaster", Says: says(t, yardSays...)}, "me")
	must(t, err0)
	must(t, api.PutDepot(ctx, store.Depot{Name: "yard", Base: "main"}, "me"))
	_, err = api.PutFlow(ctx, *trainFlow(t), "me")
	must(t, err)
	must(t, api.SetFlowPointer(ctx, "yard", store.TypeTrain, "yard.train", "me"))
	if members == nil {
		members = []string{"mckean", "yardmaster", "yard"}
	}
	must(t, api.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 50, Members: members}, "me"))
	depot, train := "yard", store.TypeTrain
	_, err = api.AddRoute(ctx, store.Route{Stage: "in-pr", Type: &train, Depot: &depot, Group: "decisions"}, "me")
	must(t, err)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: api, Runners: map[string]dispatch.Runner{"fake": f},
		PRHead: func(context.Context, dispatch.PRQuery) (string, error) {
			return "", errors.New("no gh in tests")
		}}
	r := &ghDepot{api: api, own: own, d: d, f: f, ci: checks(nil), dec: dec}
	d.CI = func(_ context.Context, q dispatch.CIQuery) (dispatch.CIChecks, error) {
		r.asked = append(r.asked, q)
		return r.ci(q)
	}
	return r
}

// train adds a train bound to the fixtures' pull request, acme/yardr#17 with
// its head in acme/yardr, as integrate-train binds one.
func (r *ghDepot) train(t *testing.T, id, stage string) {
	t.Helper()
	r.unboundTrain(t, id, stage)
	_, err := r.api.UpdateBead(context.Background(), id, store.BeadPatch{Metadata: map[string]any{
		"pr_url": "https://github.com/acme/yardr/pull/17", "pr_number": "17", "pr_head_repo": "acme/yardr"}},
		store.Expect{}, "me")
	must(t, err)
}

// unboundTrain adds a train with no pull request stored on it.
func (r *ghDepot) unboundTrain(t *testing.T, id, stage string) {
	t.Helper()
	_, err := r.api.CreateBead(context.Background(), store.NewBead{ID: id, Title: "train " + id, Type: store.TypeTrain,
		Depot: "yard", Stage: stage}, "me")
	must(t, err)
}

// deliver records fixture name as GitHub delivered it, retargeted from
// yard-sv6b to train and, when sha is set, to that head commit.
func (r *ghDepot) deliver(t *testing.T, event, name, train, sha string) {
	t.Helper()
	body, err := os.ReadFile("../hooks/testdata/github/" + name + ".json")
	must(t, err)
	s := strings.ReplaceAll(string(body), "yard-sv6b", train)
	if sha != "" {
		s = strings.ReplaceAll(s, fixtureSHA, sha)
	}
	r.record(t, "gh", event, s)
}

// deliverEdited is deliver with the fixture's text edited first.
func (r *ghDepot) deliverEdited(t *testing.T, event, name, train, sha string, edit func(string) string) {
	t.Helper()
	body, err := os.ReadFile("../hooks/testdata/github/" + name + ".json")
	must(t, err)
	s := edit(strings.ReplaceAll(string(body), "yard-sv6b", train))
	if sha != "" {
		s = strings.ReplaceAll(s, fixtureSHA, sha)
	}
	r.record(t, "gh", event, s)
}

// record normalizes body as a delivery of event through the pack and
// records it on the gh hook. The train is derived from the head branch
// as the receiver derives it: the pack leaves that yardr convention to
// the core.
func (r *ghDepot) record(t *testing.T, hook, event, s string) {
	t.Helper()
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	env, err := r.dec.Normalize(h, []byte(s))
	must(t, err)
	if len(env.Fields["train"]) == 0 {
		for _, branch := range env.Fields["head_branch"] {
			if id, ok := strings.CutPrefix(branch, store.Branch("")); ok && id != "" {
				env.Fields["train"] = []string{id}
				break
			}
		}
	}
	_, _, err = r.own.RecordHook(context.Background(), hook, env.Map(), []byte(s), nil)
	must(t, err)
}

func (r *ghDepot) pass(t *testing.T) {
	t.Helper()
	must(t, r.d.Reconcile(context.Background()))
}

func (r *ghDepot) bead(t *testing.T, id string) *store.Bead {
	t.Helper()
	b, err := r.api.GetBead(context.Background(), id)
	must(t, err)
	return b
}

func (r *ghDepot) stageOf(t *testing.T, id string) string { return r.bead(t, id).Stage }

// rules are the hook_rule events logged on train for hook events, as
// "outcome action", "outcome refused: why", "outcome ignored: why" or
// "outcome error: msg", with the event's type where no line matched.
func (r *ghDepot) rules(t *testing.T, train string) []string {
	t.Helper()
	return r.ruleEvents(t, train, false)
}

// ciRules are those logged for reads of the train's CI, which no event has
// a seq for.
func (r *ghDepot) ciRules(t *testing.T, train string) []string {
	t.Helper()
	return r.ruleEvents(t, train, true)
}

func (r *ghDepot) ruleEvents(t *testing.T, train string, ci bool) []string {
	t.Helper()
	evs, err := r.api.Events(context.Background(), train, 200)
	must(t, err)
	var out []string
	for _, e := range evs {
		if e.Kind != "hook_rule" {
			continue
		}
		rule, _ := e.Data["event"].(string)
		if (rule == "ci") != ci {
			continue
		}
		if outcome, _ := e.Data["outcome"].(string); outcome != "" && !ci {
			rule = outcome
		}
		// The event names its bead, and a train as train besides.
		named := e.Data["train"]
		if r.bead(t, train).Type != store.TypeTrain {
			named = cmp.Or(named, any(train))
		}
		if e.Data["bead"] != train || named != train || e.Data["hook"] == nil || (!ci && e.Data["seq"] == nil) {
			t.Fatalf("hook_rule without bead, train, hook or seq: %v", e.Data)
		}
		if e.Data["outcome"] != nil && (e.Data["says"] == nil || e.Data["stage"] == nil) {
			t.Fatalf("hook_rule with an outcome and no line or stage: %v", e.Data)
		}
		switch {
		case e.Data["action"] != nil:
			out = append(out, rule+" "+e.Data["action"].(string))
		case e.Data["refused"] != nil:
			out = append(out, rule+" refused: "+e.Data["refused"].(string))
		case e.Data["ignored"] != nil:
			out = append(out, rule+" ignored: "+e.Data["ignored"].(string))
		default:
			out = append(out, rule+" error: "+e.Data["error"].(string))
		}
	}
	return out
}

func (r *ghDepot) lastNote(t *testing.T, id string) string {
	t.Helper()
	notes, err := r.api.Notes(context.Background(), id)
	must(t, err)
	if len(notes) == 0 {
		return ""
	}
	return notes[len(notes)-1].Text
}

func wantRules(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("hook_rule events %q, want %d like %q", got, len(want), want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("hook_rule events %q, want %q", got, want)
		}
	}
}

// A run that succeeded has no line: for a train in in-pr it has the CI
// read as state (ci_test.go), and anywhere else it moves and logs nothing.
// The head a pull_request event carries is still kept.
func TestGitHubGreenRunIsNoRule(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	for id, stage := range map[string]string{"yard-a": "review", "yard-b": "approved", "yard-c": "land", "yard-d": "open"} {
		r.train(t, id, stage)
		r.deliver(t, "pull_request", "pull_request_synchronize", id, "")
		r.deliver(t, "workflow_run", "workflow_run_success", id, "")
		r.pass(t)
		if b := r.bead(t, id); b.Stage != stage || b.Metadata["pr_head"] != fixtureSHA {
			t.Fatalf("%s in %s with pr_head %v after a green run, want it left in %s", id, b.Stage, b.Metadata["pr_head"], stage)
		}
		wantRules(t, r.rules(t, id))
	}
	if len(r.asked) != 0 {
		t.Fatalf("GitHub asked about trains outside in-pr: %+v", r.asked)
	}
}

// A failure on a commit that is not the PR's head (a push superseded it)
// moves nothing. Without a pull_request event, gh is asked for the head.
func TestGitHubCIRedNeedsThePRHead(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "review")
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-a", "")
	r.pass(t)
	wantRules(t, r.rules(t, "yard-a"), "changes ignored: "+fixtureSHA+" is not the PR's head bbbb")
	if st := r.stageOf(t, "yard-a"); st != "review" {
		t.Fatalf("yard-a in %s after a failure on an old commit, want review", st)
	}

	// No pull_request event seen: gh's answer counts, and when gh cannot
	// tell, nothing moves and the error is logged.
	r.train(t, "yard-b", "review")
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-b", "")
	r.pass(t)
	wantRules(t, r.rules(t, "yard-b"), "changes error: the PR's head is unknown: no gh in tests")

	var asked dispatch.PRQuery
	r.d.PRHead = func(_ context.Context, q dispatch.PRQuery) (string, error) {
		asked = q
		return fixtureSHA, nil
	}
	r.train(t, "yard-c", "review")
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-c", "")
	r.pass(t)
	wantRules(t, r.rules(t, "yard-c"), "changes review -> open")
	if asked != (dispatch.PRQuery{Train: "yard-c", URL: "https://github.com/acme/yardr/pull/17", Repo: "acme/yardr", Number: "17"}) {
		t.Fatalf("asked gh about %+v", asked)
	}
}

// The hook's crew member must be allowed out of in-pr: with members that
// leave it out, the refusal is logged and the train stays for a human. A
// member moves it: CI's outcome, like any says outcome, is reported as the
// hook's crew member.
func TestGitHubRuleRefusedByMembers(t *testing.T) {
	t.Parallel()
	green := func(t *testing.T, members ...string) *ghDepot {
		t.Helper()
		r := newGHDepot(t, members...)
		r.train(t, "yard-a", "in-pr")
		r.reviewed(t, "yard-a", fixtureSHA)
		r.ci = checks([]string{"build"}, ck("build", "completed", "success"))
		r.pass(t)
		r.pass(t)
		return r
	}
	r := green(t, "mckean")
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s", st)
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci refused: bead yard-a: only members of group decisions")

	moved := green(t, "mckean", "yardmaster")
	if st := moved.stageOf(t, "yard-a"); st != "land" {
		t.Fatalf("yard-a in %s, want land", st)
	}
	wantRules(t, moved.ciRules(t, "yard-a"), "ci green: in-pr -> land")
}

// A failed run on the head of a train in review is the outcome changes: the
// train goes back to open with the run in a note, and a second failed run
// on the commit finds it there, where the line does not hold. In in-pr the
// event moves nothing: the required checks are read (ci_test.go). Nor does
// it in approved, which the line does not name.
func TestGitHubCIRedSendsTheTrainBack(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "in-pr")
	r.train(t, "yard-b", "review")
	r.train(t, "yard-c", "approved")
	for _, e := range []string{"yard-a", "yard-b", "yard-c"} {
		r.deliver(t, "pull_request", "pull_request_synchronize", e, "")
		r.deliver(t, "workflow_run", "workflow_run_failure", e, "")
		r.deliver(t, "workflow_run", "workflow_run_failure", e, "")
	}
	r.pass(t)

	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s, want in-pr: a failed run is not the state of its required checks", st)
	}
	wantRules(t, r.rules(t, "yard-a"))

	if st := r.stageOf(t, "yard-b"); st != "open" {
		t.Fatalf("yard-b in %s, want open", st)
	}
	wantRules(t, r.rules(t, "yard-b"), "changes review -> open")
	if n := r.lastNote(t, "yard-b"); !strings.HasPrefix(n, "CI failed on the head of the PR: https://github.com/acme/yardr/actions/runs/5") ||
		!strings.Contains(n, "failure") || !strings.Contains(n, "(github workflow_run completed") {
		t.Fatalf("note %q", n)
	}
	if fam, err := r.api.Family(context.Background(), "yard-b"); err != nil || len(fam.Children) != 0 {
		t.Fatalf("yard-b children %v %v, want none: the train itself is sent back", fam.Children, err)
	}

	if st := r.stageOf(t, "yard-c"); st != "approved" {
		t.Fatalf("yard-c in %s", st)
	}
	wantRules(t, r.rules(t, "yard-c"))
}

// A cancelled run or suite is not red: it says nothing about the commit
// (someone, or a newer push, stopped it), so the train stays until a run
// fails.
func TestGitHubCancelledIsNotRed(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "review")
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliver(t, "check_suite", "check_suite_cancelled", "yard-a", "")
	r.pass(t)

	wantRules(t, r.rules(t, "yard-a"))
	if st := r.stageOf(t, "yard-a"); st != "review" {
		t.Fatalf("yard-a in %s after a cancelled suite, want review", st)
	}
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-a", "")
	r.pass(t)
	wantRules(t, r.rules(t, "yard-a"), "changes review -> open")
}

// A held train is a human's: the rules leave it where it is, whatever
// GitHub reports, and say so in the hook_rule event. The event is judged
// (its seq recorded, the PR head kept), so an un-hold does not replay it.
func TestGitHubRulesSkipHeldTrains(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	ctx := context.Background()
	r.train(t, "yard-a", "in-pr")
	_, err := r.api.SetHold(ctx, "yard-a", true, store.Expect{}, "human")
	must(t, err)
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliver(t, "pull_request_review", "pull_request_review_changes_requested", "yard-a", "")
	r.pass(t)

	e := r.bead(t, "yard-a")
	if e.Stage != "in-pr" || !e.Hold {
		t.Fatalf("held yard-a %s hold=%v after a review asking for changes, want left in in-pr", e.Stage, e.Hold)
	}
	wantRules(t, r.rules(t, "yard-a"), "changes ignored: train is held")
	if e.Metadata["pr_head"] != fixtureSHA {
		t.Fatalf("pr_head %v, want %s kept while held", e.Metadata["pr_head"], fixtureSHA)
	}

	_, err = r.api.SetHold(ctx, "yard-a", false, store.Expect{}, "human")
	must(t, err)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s after the un-hold, want in-pr: the review was judged while held", st)
	}
	wantRules(t, r.rules(t, "yard-a"), "changes ignored: train is held")
}

// A merged PR is the outcome merged, which the flow routes to merged from
// land, in-pr and approved, in one move; open has no edge for it, and the
// refusal is logged with the outcome and the stage. A train in land whose
// merger still runs is moved as any bead is by a done from outside its
// session: the session ends with the move.
func TestGitHubPRMerged(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	ctx := context.Background()
	must(t, r.api.PutGroup(ctx, store.Group{Name: "yard-pr-merge", Runner: "fake", Limit: 1}, "me"))
	for id, stage := range map[string]string{"yard-a": "land", "yard-b": "in-pr", "yard-c": "approved",
		"yard-d": "open", "yard-e": "land"} {
		r.train(t, id, stage)
	}
	// yard-e's merger is at work.
	if _, err := r.api.Claim(ctx, "yard-e", "yard-pr-merge", "land", "yard"); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"yard-a", "yard-b", "yard-c", "yard-d", "yard-e"} {
		r.deliver(t, "pull_request", "pull_request_closed_merged", e, "")
	}
	r.pass(t)
	for _, id := range []string{"yard-a", "yard-b", "yard-c", "yard-e"} {
		if b := r.bead(t, id); b.Stage != "merged" || b.Status != "closed" || b.Session != nil {
			t.Fatalf("%s in %s (%s) session %v, want merged and closed", id, b.Stage, b.Status, b.Session)
		}
	}
	wantRules(t, r.rules(t, "yard-a"), "merged land -> merged")
	wantRules(t, r.rules(t, "yard-b"), "merged in-pr -> merged")
	wantRules(t, r.rules(t, "yard-c"), "merged approved -> merged")
	wantRules(t, r.rules(t, "yard-e"), "merged land -> merged")
	if n := r.lastNote(t, "yard-b"); !strings.HasPrefix(n, "PR merged: https://github.com/acme/yardr/pull/17") {
		t.Fatalf("note %q", n)
	}
	if st := r.stageOf(t, "yard-d"); st != "open" {
		t.Fatalf("yard-d in %s", st)
	}
	wantRules(t, r.rules(t, "yard-d"), "merged refused: bead yard-d: flow yard.train has no outcome merged for stage open")
	evs, err := r.api.Events(ctx, "yard-d", 50)
	must(t, err)
	for _, e := range evs {
		if e.Kind == "hook_rule" && (e.Data["outcome"] != "merged" || e.Data["stage"] != "open") {
			t.Fatalf("the refusal names outcome %v and stage %v, want merged and open", e.Data["outcome"], e.Data["stage"])
		}
	}

	// Closed trains are past every line: a redelivery logs nothing.
	r.deliver(t, "pull_request", "pull_request_closed_merged", "yard-a", "")
	r.pass(t)
	wantRules(t, r.rules(t, "yard-a"), "merged land -> merged")
}

// A PR closed unmerged asks a human whether to reopen it or drop the train.
func TestGitHubPRClosedUnmerged(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "in-pr")
	r.train(t, "yard-b", "open")
	for _, e := range []string{"yard-a", "yard-b"} {
		r.deliver(t, "pull_request", "pull_request_closed_unmerged", e, "")
	}
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "decide" {
		t.Fatalf("yard-a in %s, want decide", st)
	}
	if n := r.lastNote(t, "yard-a"); !strings.HasPrefix(n, "PR closed unmerged: reopen, or drop the train?") {
		t.Fatalf("note %q", n)
	}
	wantRules(t, r.rules(t, "yard-a"), "question in-pr -> decide")
	if st := r.stageOf(t, "yard-b"); st != "open" {
		t.Fatalf("yard-b in %s", st)
	}
	wantRules(t, r.rules(t, "yard-b"), "question refused: bead yard-b: flow yard.train has no outcome question for stage open")
}

// A human's changes_requested review sends the train to open with the
// review in a note; approving or commenting reviews, and bots, do not.
func TestGitHubChangesRequested(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "in-pr")
	r.train(t, "yard-b", "open")
	r.train(t, "yard-c", "in-pr")
	for _, e := range []string{"yard-a", "yard-b"} {
		r.deliver(t, "pull_request_review", "pull_request_review_approved", e, "")
		r.deliver(t, "pull_request_review", "pull_request_review_commented", e, "")
		r.deliver(t, "pull_request_review", "pull_request_review_changes_requested", e, "")
	}
	body, err := os.ReadFile("../hooks/testdata/github/pull_request_review_changes_requested.json")
	must(t, err)
	bot := strings.ReplaceAll(strings.ReplaceAll(string(body), "yard-sv6b", "yard-c"), `"login": "yardmaster"`, `"login": "ci-helper[bot]"`)
	r.record(t, "gh", "pull_request_review", bot)
	r.pass(t)

	if st := r.stageOf(t, "yard-a"); st != "open" {
		t.Fatalf("yard-a in %s, want open", st)
	}
	if n := r.lastNote(t, "yard-a"); !strings.HasPrefix(n, "Changes requested on the PR: https://") || !strings.Contains(n, "by yardmaster") ||
		!strings.Contains(n, "the tests are red, see the race") {
		t.Fatalf("note %q", n)
	}
	wantRules(t, r.rules(t, "yard-a"), "changes in-pr -> open")
	if st := r.stageOf(t, "yard-b"); st != "open" {
		t.Fatalf("yard-b in %s", st)
	}
	wantRules(t, r.rules(t, "yard-b"), "changes refused: bead yard-b: flow yard.train has no outcome changes for stage open")
	if st := r.stageOf(t, "yard-c"); st != "in-pr" {
		t.Fatalf("a bot's review moved yard-c to %s", st)
	}
	wantRules(t, r.rules(t, "yard-c"))
}

// Hook events for branches that are not a train's and issue comments move
// nothing and log nothing. Another source's event that names a train is put
// to its own hook's table, as github's is: one without a table says so.
func TestGitHubIgnoresOtherEvents(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "in-pr")
	_, err := r.api.CreateBead(context.Background(), store.NewBead{ID: "yard-t", Title: "task", Depot: "yard"}, "me")
	must(t, err)
	r.deliver(t, "issue_comment", "issue_comment_pr", "yard-a", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-t", "") // a task's branch
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-zzz", "")
	_, _, err = r.own.RecordHook(context.Background(), "lin", map[string]any{"source": "linear", "type": "comment",
		"fields": map[string]any{"train": []any{"yard-a"}}}, []byte("{}"), nil)
	must(t, err)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s", st)
	}
	wantRules(t, r.rules(t, "yard-a"), "comment ignored: hook lin has no table of what its events say of a bead")
	wantRules(t, r.rules(t, "yard-t"))
}

// A train is judged on the pull request it is bound to only: events for a
// branch of the same name in another repository, or for another pull
// request of the branch, move nothing, set no head and have no CI read.
func TestGitHubRulesJudgeTheTrainsOwnPR(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.train(t, "yard-r", "review")
	elsewhere := func(s string) string { return strings.ReplaceAll(s, "acme/yardr", "other/yardr") }
	otherPR := func(s string) string { return strings.ReplaceAll(s, `"number": 17`, `"number": 18`) }
	// The failed run's fixture lists no pull request: this one is PR 18's.
	otherRun := func(s string) string {
		return strings.Replace(s, `"pull_requests": []`, `"pull_requests": [{"number": 18}]`, 1)
	}
	old := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// The train's CI is pending when it enters in-pr, and green after: only
	// an event about its own pull request has that seen.
	r.ci = checks([]string{"build"}, ck("build", "in_progress", ""))
	r.pass(t)
	r.ci = checks([]string{"build"}, ck("build", "completed", "success"))

	// A PR in another repository and another PR of the branch, at another
	// commit, say nothing of the train's head; their runs have nothing read.
	r.deliverEdited(t, "pull_request", "pull_request_synchronize", "yard-a", old, elsewhere)
	r.deliverEdited(t, "pull_request", "pull_request_synchronize", "yard-a", old, otherPR)
	r.deliverEdited(t, "workflow_run", "workflow_run_success", "yard-a", "", elsewhere)
	r.deliverEdited(t, "workflow_run", "workflow_run_success", "yard-a", "", otherPR)
	r.deliverEdited(t, "workflow_run", "workflow_run_failure", "yard-r", "", elsewhere)
	r.deliverEdited(t, "workflow_run", "workflow_run_failure", "yard-r", "", otherRun)
	r.pass(t)
	if b := r.bead(t, "yard-a"); b.Stage != "in-pr" || b.Metadata["pr_head"] != nil {
		t.Fatalf("yard-a in %s with pr_head %v after events for other PRs", b.Stage, b.Metadata["pr_head"])
	}
	if len(r.asked) != 1 {
		t.Fatalf("GitHub asked %d times, want once: events for other PRs are no reason to read the train's CI", len(r.asked))
	}
	wantRules(t, r.rules(t, "yard-r"),
		"changes ignored: event is in other/yardr, not the train's PR acme/yardr#17",
		"changes ignored: event is for acme/yardr#18, not the train's PR acme/yardr#17")

	// The train's own PR: its head is recorded and its CI read again.
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.pass(t)
	if b := r.bead(t, "yard-a"); b.Stage != "land" || b.Metadata["pr_head"] != fixtureSHA {
		t.Fatalf("yard-a in %s with pr_head %v, want land at %s", b.Stage, b.Metadata["pr_head"], fixtureSHA)
	}

	// A train bound before pr_number and pr_head_repo were stored has its
	// pr_url, which names the same pull request.
	r.unboundTrain(t, "yard-b", "review")
	_, err := r.api.UpdateBead(context.Background(), "yard-b", store.BeadPatch{Metadata: map[string]any{
		"pr_url": "https://github.com/acme/yardr/pull/17"}}, store.Expect{}, "me")
	must(t, err)
	r.deliverEdited(t, "workflow_run", "workflow_run_failure", "yard-b", "", otherRun)
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-b", "")
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-b", "")
	r.pass(t)
	wantRules(t, r.rules(t, "yard-b"),
		"changes ignored: event is for acme/yardr#18, not the train's PR acme/yardr#17", "changes review -> open")
}

// A fork's pull request from a branch of the train's name is in the train's
// repository and carries its train field, as its CI does: neither its head,
// its green run nor its merge is the train's. This is the round-three
// finding (yardr-ca0d): the branch name is anyone's to use.
func TestGitHubIgnoresAForksPRWithTheTrainsBranch(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.train(t, "yard-r", "review")
	// Pending when the train enters in-pr, green after: a read would land it.
	r.ci = checks([]string{"build"}, ck("build", "in_progress", ""))
	r.pass(t)
	r.ci = checks([]string{"build"}, ck("build", "completed", "success"))
	forkSHA := "cccccccccccccccccccccccccccccccccccccccc"
	// What GitHub sends for a fork's PR: the repository is the base's, the
	// head repository the fork's, the number its own.
	forkPR := func(s string) string {
		s = strings.ReplaceAll(s, `"number": 17`, `"number": 18`)
		return strings.Replace(s, `"repo": {
    "full_name": "acme/yardr"`, `"repo": {
    "full_name": "mallory/yardr"`, 1)
	}
	// A run on a fork's branch lists no pull request; only its head
	// repository tells it from a run on the train's branch.
	forkRun := func(s string) string {
		s = strings.Replace(s, `"head_repository": {
   "full_name": "acme/yardr"`, `"head_repository": {
   "full_name": "mallory/yardr"`, 1)
		a, b := strings.Index(s, `"pull_requests": [`), strings.Index(s, ` "repository"`)
		return s[:a] + `"pull_requests": []` + "\n }," + "\n" + s[b:]
	}

	r.deliverEdited(t, "pull_request", "pull_request_opened", "yard-a", forkSHA, forkPR)
	r.deliverEdited(t, "workflow_run", "workflow_run_success", "yard-a", forkSHA, forkRun)
	r.deliverEdited(t, "pull_request", "pull_request_closed_merged", "yard-a", forkSHA, forkPR)
	r.deliverEdited(t, "workflow_run", "workflow_run_failure", "yard-r", forkSHA, forkRun)
	r.pass(t)
	if b := r.bead(t, "yard-a"); b.Stage != "in-pr" || b.Metadata["pr_head"] != nil {
		t.Fatalf("yard-a in %s with pr_head %v after a fork's PR, want in-pr and no head", b.Stage, b.Metadata["pr_head"])
	}
	wantRules(t, r.rules(t, "yard-a"),
		"merged ignored: event is for acme/yardr#18, not the train's PR acme/yardr#17")
	wantRules(t, r.rules(t, "yard-r"),
		"changes ignored: event's head is in mallory/yardr, not acme/yardr, the head repository of the train's PR acme/yardr#17")

	// The fork's run at the very commit the train's PR has is still the
	// fork's: its workflows are not the train's CI, and it has nothing read.
	r.deliverEdited(t, "workflow_run", "workflow_run_success", "yard-a", "", forkRun)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("a fork's green run on the PR's head moved yard-a to %s", st)
	}
	if len(r.asked) != 1 {
		t.Fatalf("GitHub asked %d times, want once: a fork's events are no reason to read the train's CI", len(r.asked))
	}

	// A number that matches is not enough when the head repository does
	// not: the train is bound to both.
	sameNumber := func(s string) string {
		return strings.Replace(s, `"repo": {
    "full_name": "acme/yardr"`, `"repo": {
    "full_name": "mallory/yardr"`, 1)
	}
	r.deliverEdited(t, "pull_request", "pull_request_closed_merged", "yard-a", "", sameNumber)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("a merged event with another head repository moved yard-a to %s", st)
	}
}

// A merged event for a pull request the train is not bound to does nothing,
// in any stage a merge would move: neither for a bound train and another
// number, nor for a train bound to no pull request at all, whose branch
// name is all the event has.
func TestGitHubForgedMergeForAnUnknownPRDoesNothing(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	unknown := func(s string) string { return strings.ReplaceAll(s, `"number": 17`, `"number": 99`) }
	for i, stage := range []string{"land", "in-pr", "approved"} {
		bound, unbound := "yard-b"+strconv.Itoa(i), "yard-u"+strconv.Itoa(i)
		r.train(t, bound, stage)
		r.unboundTrain(t, unbound, stage)
		r.deliverEdited(t, "pull_request", "pull_request_closed_merged", bound, "", unknown)
		r.deliverEdited(t, "pull_request", "pull_request_closed_merged", unbound, "", unknown)
		r.deliver(t, "pull_request", "pull_request_closed_merged", unbound, "")
		r.pass(t)
		for _, id := range []string{bound, unbound} {
			if b := r.bead(t, id); b.Stage != stage || b.Status == "closed" || b.Metadata["pr_head"] != nil {
				t.Fatalf("%s is %s/%s with pr_head %v after a forged merge, want it left in %s", id, b.Stage, b.Status, b.Metadata["pr_head"], stage)
			}
		}
		wantRules(t, r.rules(t, bound), "merged ignored: event is for acme/yardr#99, not the train's PR acme/yardr#17")
		wantRules(t, r.rules(t, unbound),
			"merged ignored: train is bound to no pull request", "merged ignored: train is bound to no pull request")
	}

	// An unbound train's green CI lands nothing either: GitHub is not asked
	// about a pull request it was never bound to, and no head is taken
	// from one.
	r.unboundTrain(t, "yard-g", "in-pr")
	r.reviewed(t, "yard-g", fixtureSHA)
	r.ci = checks([]string{"build"}, ck("build", "completed", "success"))
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-g", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-g", "")
	r.pass(t)
	if b := r.bead(t, "yard-g"); b.Stage != "in-pr" || b.Metadata["pr_head"] != nil {
		t.Fatalf("unbound yard-g in %s with pr_head %v", b.Stage, b.Metadata["pr_head"])
	}
	wantRules(t, r.ciRules(t, "yard-g"), "ci ignored: train is bound to no pull request")
	for _, q := range r.asked {
		if q.Train == "yard-g" {
			t.Fatalf("GitHub asked about unbound yard-g: %+v", q)
		}
	}
}

// A hook without a table moves no bead: every event about a train is left
// alone, and says why.
func TestGitHubHookWithoutATableMovesNothing(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	_, err := r.api.PutHook(context.Background(), store.Hook{Name: "gh", Source: "github", SecretFile: "/nonexistent/github.secret"}, "me")
	must(t, err)
	r.train(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.train(t, "yard-b", "review")
	r.ci = checks([]string{"build"}, ck("build", "completed", "success"))
	r.deliver(t, "pull_request", "pull_request_closed_merged", "yard-a", "")
	r.deliver(t, "pull_request_review", "pull_request_review_changes_requested", "yard-b", "")
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-b", "")
	r.pass(t)
	r.pass(t)
	if a, b := r.stageOf(t, "yard-a"), r.stageOf(t, "yard-b"); a != "in-pr" || b != "review" {
		t.Fatalf("yard-a in %s, yard-b in %s: a hook without a table moved a train", a, b)
	}
	const why = " ignored: hook gh has no table of what its events say of a bead (yardr hook add --says, or a pack's sources.github.says)"
	wantRules(t, r.rules(t, "yard-a"), "pull_request"+why)
	wantRules(t, r.rules(t, "yard-b"), "pull_request_review"+why, "workflow_run"+why)
	if len(r.asked) != 0 {
		t.Fatalf("GitHub asked about CI with no line for it: %+v", r.asked)
	}
}

// The table names no stage of its own: in a flow whose stages are called
// otherwise, the same events route by that flow's edges, and CI is read
// where the table's line says.
func TestGitHubTableFollowsARenamedFlow(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	ctx := context.Background()
	f, err := store.ParseFlow(`backlog -> assembling
assembling -> checking on done
checking -> open-pr on done | assembling on changes | ask on question
open-pr -> landed on done | assembling on changes | ask on question | landed on merged
ask -> assembling
landed terminal
`)
	must(t, err)
	f.Name = "yard.train"
	_, err = r.api.PutFlow(ctx, *f, "me")
	must(t, err)
	_, err = r.api.PutHook(ctx, store.Hook{Name: "gh", Source: "github", SecretFile: "/nonexistent/github.secret", As: "yardmaster", Says: says(t,
		yardSays[0], yardSays[1], yardSays[2],
		"event=workflow_run action=completed conclusion=failure stage=checking -> changes",
		"event=ci conclusion=success stage=open-pr -> done",
		"event=ci conclusion=failure stage=open-pr -> changes")}, "me")
	must(t, err)

	r.train(t, "yard-a", "open-pr")
	r.train(t, "yard-b", "checking")
	r.train(t, "yard-c", "open-pr")
	r.reviewed(t, "yard-c", fixtureSHA)
	r.train(t, "yard-d", "open-pr")
	r.deliver(t, "pull_request", "pull_request_closed_merged", "yard-a", "")
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-b", "")
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-b", "")
	r.deliver(t, "pull_request", "pull_request_closed_unmerged", "yard-d", "")
	r.ci = func(q dispatch.CIQuery) (dispatch.CIChecks, error) {
		if q.Train != "yard-c" {
			return dispatch.CIChecks{Head: q.SHA, Base: "main", State: "open", Required: []string{"build"}}, nil
		}
		return checks([]string{"build"}, ck("build", "completed", "success"))(q)
	}
	r.pass(t)
	for id, want := range map[string]string{"yard-a": "landed", "yard-b": "assembling", "yard-c": "landed", "yard-d": "ask"} {
		if st := r.stageOf(t, id); st != want {
			t.Errorf("%s in %s, want %s", id, st, want)
		}
	}
	wantRules(t, r.rules(t, "yard-a"), "merged open-pr -> landed")
	wantRules(t, r.rules(t, "yard-b"), "changes checking -> assembling")
	wantRules(t, r.ciRules(t, "yard-c"), "ci green: open-pr -> landed")
	wantRules(t, r.rules(t, "yard-d"), "question open-pr -> ask")
}
