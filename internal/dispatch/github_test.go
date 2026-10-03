package dispatch_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/internal/storetest"
)

// The fixtures are yard-11's, for epic yard-sv6b's PR 17 at fixtureSHA.
const fixtureSHA = "4f2c9e1a7b3d5e6f8091a2b3c4d5e6f708192a3b"

// ghRig is a yard whose epics follow yard.epic through pull requests, with
// the yard a member of the group that holds in-pr.
type ghRig struct {
	api store.API
	own *store.Store
	d   *dispatch.Dispatcher
	f   *fakeRunner
}

func newGHRig(t *testing.T, members ...string) *ghRig {
	t.Helper()
	api, own := storetest.NewOwned(t)
	ctx := context.Background()
	must(t, api.PutRig(ctx, store.Rig{Name: "yard", Base: "main"}, "me"))
	_, err := api.PutFlow(ctx, *epicFlow(t), "me")
	must(t, err)
	must(t, api.SetFlowPointer(ctx, "yard", store.TypeEpic, "yard.epic", "me"))
	if members == nil {
		members = []string{"mckean", "mayor", "yard"}
	}
	must(t, api.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 50, Members: members}))
	rig, epic := "yard", store.TypeEpic
	_, err = api.AddRoute(ctx, store.Route{Stage: "in-pr", Type: &epic, Rig: &rig, Group: "decisions"})
	must(t, err)
	f := &fakeRunner{dead: map[string]bool{}}
	d := &dispatch.Dispatcher{Store: api, Runners: map[string]dispatch.Runner{"fake": f},
		PRHead: func(context.Context, dispatch.PRQuery) (string, error) {
			return "", errors.New("no gh in tests")
		}}
	return &ghRig{api: api, own: own, d: d, f: f}
}

// epic adds an epic bound to the fixtures' pull request, acme/yardr#17 with
// its head in acme/yardr, as integrate-epic binds one.
func (r *ghRig) epic(t *testing.T, id, stage string) {
	t.Helper()
	r.unboundEpic(t, id, stage)
	_, err := r.api.UpdateBead(context.Background(), id, store.BeadPatch{Metadata: map[string]any{
		"pr_url": "https://github.com/acme/yardr/pull/17", "pr_number": "17", "pr_head_repo": "acme/yardr"}},
		store.Expect{}, "me")
	must(t, err)
}

// unboundEpic adds an epic with no pull request stored on it.
func (r *ghRig) unboundEpic(t *testing.T, id, stage string) {
	t.Helper()
	_, err := r.api.CreateBead(context.Background(), store.NewBead{ID: id, Title: "epic " + id, Type: store.TypeEpic,
		Rig: "yard", Stage: stage}, "me")
	must(t, err)
}

// deliver records fixture name as GitHub delivered it, retargeted from
// yard-sv6b to epic and, when sha is set, to that head commit.
func (r *ghRig) deliver(t *testing.T, event, name, epic, sha string) {
	t.Helper()
	body, err := os.ReadFile("../hooks/testdata/github/" + name + ".json")
	must(t, err)
	s := strings.ReplaceAll(string(body), "yard-sv6b", epic)
	if sha != "" {
		s = strings.ReplaceAll(s, fixtureSHA, sha)
	}
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	env, err := hooks.GitHub{}.Normalize(h, []byte(s))
	must(t, err)
	_, _, err = r.own.RecordHook(context.Background(), "gh", env.Map(), []byte(s), nil)
	must(t, err)
}

// deliverEdited is deliver with the fixture's text edited first.
func (r *ghRig) deliverEdited(t *testing.T, event, name, epic, sha string, edit func(string) string) {
	t.Helper()
	body, err := os.ReadFile("../hooks/testdata/github/" + name + ".json")
	must(t, err)
	s := edit(strings.ReplaceAll(string(body), "yard-sv6b", epic))
	if sha != "" {
		s = strings.ReplaceAll(s, fixtureSHA, sha)
	}
	h := http.Header{}
	h.Set("X-GitHub-Event", event)
	env, err := hooks.GitHub{}.Normalize(h, []byte(s))
	must(t, err)
	_, _, err = r.own.RecordHook(context.Background(), "gh", env.Map(), []byte(s), nil)
	must(t, err)
}

func (r *ghRig) pass(t *testing.T) {
	t.Helper()
	must(t, r.d.Reconcile(context.Background()))
}

func (r *ghRig) bead(t *testing.T, id string) *store.Bead {
	t.Helper()
	b, err := r.api.GetBead(context.Background(), id)
	must(t, err)
	return b
}

func (r *ghRig) stageOf(t *testing.T, id string) string { return r.bead(t, id).Stage }

// rules are the github_rule events logged on epic, as "rule action" or
// "rule ignored: why" or "rule error: msg".
func (r *ghRig) rules(t *testing.T, epic string) []string {
	t.Helper()
	evs, err := r.api.Events(context.Background(), epic, 200)
	must(t, err)
	var out []string
	for _, e := range evs {
		if e.Kind != "github_rule" {
			continue
		}
		if e.Data["epic"] != epic || e.Data["seq"] == nil {
			t.Fatalf("github_rule without epic or seq: %v", e.Data)
		}
		rule, _ := e.Data["rule"].(string)
		switch {
		case e.Data["action"] != nil:
			out = append(out, rule+" "+e.Data["action"].(string))
		case e.Data["ignored"] != nil:
			out = append(out, rule+" ignored: "+e.Data["ignored"].(string))
		default:
			out = append(out, rule+" error: "+e.Data["error"].(string))
		}
	}
	return out
}

func (r *ghRig) lastNote(t *testing.T, id string) string {
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
		t.Fatalf("github_rule events %q, want %d like %q", got, len(want), want)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("github_rule events %q, want %q", got, want)
		}
	}
}

// CI green on the PR head lands an epic in in-pr, once: the second suite
// on the same commit finds it in land, and neither a later pass nor a
// restarted daemon reading the log from the start does it again.
func TestGitHubCIGreenLands(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
	r.epic(t, "yard-b", "review")
	for _, e := range []string{"yard-a", "yard-b"} {
		r.deliver(t, "pull_request", "pull_request_synchronize", e, "")
		r.deliver(t, "workflow_run", "workflow_run_success", e, "")
	}
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "land" {
		t.Fatalf("yard-a in %s, want land", st)
	}
	if n := r.lastNote(t, "yard-a"); n != "CI green on "+fixtureSHA+": https://github.com/acme/yardr/actions/runs/5" {
		t.Fatalf("note %q", n)
	}
	wantRules(t, r.rules(t, "yard-a"), "ci_green in-pr -> land")
	// Not in in-pr: logged and left.
	if st := r.stageOf(t, "yard-b"); st != "review" {
		t.Fatalf("yard-b in %s, want review", st)
	}
	wantRules(t, r.rules(t, "yard-b"), "ci_green ignored: epic in review")

	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	r.pass(t)
	wantRules(t, r.rules(t, "yard-a"), "ci_green in-pr -> land", "ci_green ignored: epic in land")

	restarted := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead}
	must(t, restarted.Reconcile(context.Background()))
	wantRules(t, r.rules(t, "yard-a"), "ci_green in-pr -> land", "ci_green ignored: epic in land")
	if b := r.bead(t, "yard-a"); b.Metadata["pr_head"] != fixtureSHA {
		t.Fatalf("pr_head %v, want %s", b.Metadata["pr_head"], fixtureSHA)
	}
}

// A conclusion on a commit that is not the PR's head (a push superseded
// it) moves nothing. Without a pull_request event, gh is asked.
func TestGitHubCIGreenNeedsThePRHead(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("green on an old commit moved the epic to %s", st)
	}
	wantRules(t, r.rules(t, "yard-a"), "ci_green ignored: "+fixtureSHA+" is not the PR's head bbbb")

	// No pull_request event seen: gh's answer counts, and when gh cannot
	// tell, nothing moves and the error is logged.
	r.epic(t, "yard-b", "in-pr")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-b", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-b"); st != "in-pr" {
		t.Fatalf("unknown head moved the epic to %s", st)
	}
	wantRules(t, r.rules(t, "yard-b"), "ci_green error: the PR's head is unknown: no gh in tests")

	var asked dispatch.PRQuery
	r.d.PRHead = func(_ context.Context, q dispatch.PRQuery) (string, error) {
		asked = q
		return fixtureSHA, nil
	}
	r.epic(t, "yard-c", "in-pr")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-c", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-c"); st != "land" {
		t.Fatalf("yard-c in %s, want land", st)
	}
	if asked != (dispatch.PRQuery{Epic: "yard-c", URL: "https://github.com/acme/yardr/pull/17", Repo: "acme/yardr", Number: "17"}) {
		t.Fatalf("asked gh about %+v", asked)
	}
}

// The yard must be allowed out of in-pr: with members that leave it out,
// the refusal is logged and the epic stays for a human.
func TestGitHubRuleRefusedByMembers(t *testing.T) {
	r := newGHRig(t, "mckean", "mayor")
	r.epic(t, "yard-a", "in-pr")
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s", st)
	}
	wantRules(t, r.rules(t, "yard-a"), "ci_green error: bead yard-a: only members of group decisions")
}

// CI red files one fix child per head commit, a bug in backlog that blocks
// the epic, and sends an epic in in-pr back to open; one in review stays.
func TestGitHubCIRedFilesAFix(t *testing.T) {
	r := newGHRig(t)
	ctx := context.Background()
	r.epic(t, "yard-a", "in-pr")
	r.epic(t, "yard-b", "review")
	r.epic(t, "yard-c", "approved")
	for _, e := range []string{"yard-a", "yard-b", "yard-c"} {
		r.deliver(t, "pull_request", "pull_request_synchronize", e, "")
		r.deliver(t, "workflow_run", "workflow_run_failure", e, "")
		r.deliver(t, "workflow_run", "workflow_run_failure", e, "")
	}
	r.pass(t)

	if st := r.stageOf(t, "yard-a"); st != "open" {
		t.Fatalf("yard-a in %s, want open", st)
	}
	wantRules(t, r.rules(t, "yard-a"), "ci_red added yard-a-ci-4f2c9e1; in-pr -> open", "ci_red ignored: epic in open")
	if n := r.lastNote(t, "yard-a"); !strings.Contains(n, "CI failure on "+fixtureSHA) || !strings.Contains(n, "yard-a-ci-4f2c9e1") {
		t.Fatalf("note %q", n)
	}
	fix := r.bead(t, "yard-a-ci-4f2c9e1")
	if fix.Title != "Fix CI on epic yard-a: ci" || fix.Type != "bug" || fix.Stage != "backlog" ||
		fix.Rig == nil || *fix.Rig != "yard" ||
		!strings.Contains(fix.Body, "https://github.com/acme/yardr/actions/runs/5") || !strings.Contains(fix.Body, "failure") {
		t.Fatalf("fix child %+v", fix)
	}
	fam, err := r.api.Family(ctx, "yard-a")
	must(t, err)
	if !slices.Equal(fam.Children, []string{"yard-a-ci-4f2c9e1"}) {
		t.Fatalf("yard-a children %v", fam.Children)
	}
	if bl, err := r.api.OpenBlockers(ctx, "yard-a"); err != nil || !slices.Equal(bl, []string{"yard-a-ci-4f2c9e1"}) {
		t.Fatalf("yard-a blockers %v %v", bl, err)
	}

	// In review the epic stays; the second failed run on the same commit
	// adds no second child.
	if st := r.stageOf(t, "yard-b"); st != "review" {
		t.Fatalf("yard-b in %s, want review", st)
	}
	wantRules(t, r.rules(t, "yard-b"), "ci_red added yard-b-ci-4f2c9e1", "ci_red ignored: yard-b-ci-4f2c9e1 already fixes CI")
	fam, err = r.api.Family(ctx, "yard-b")
	must(t, err)
	if !slices.Equal(fam.Children, []string{"yard-b-ci-4f2c9e1"}) {
		t.Fatalf("yard-b children %v", fam.Children)
	}

	// approved: neither rule's stage.
	if st := r.stageOf(t, "yard-c"); st != "approved" {
		t.Fatalf("yard-c in %s", st)
	}
	wantRules(t, r.rules(t, "yard-c"), "ci_red ignored: epic in approved", "ci_red ignored: epic in approved")
	if _, err := r.api.GetBead(ctx, "yard-c-ci-4f2c9e1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a fix child for an epic in approved: %v", err)
	}

	// A new push that fails again gets its own child.
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-b", "cccccccccccccccccccccccccccccccccccccccc")
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-b", "cccccccccccccccccccccccccccccccccccccccc")
	r.pass(t)
	fam, err = r.api.Family(ctx, "yard-b")
	must(t, err)
	if !slices.Equal(fam.Children, []string{"yard-b-ci-4f2c9e1", "yard-b-ci-ccccccc"}) {
		t.Fatalf("yard-b children %v", fam.Children)
	}
}

// A cancelled run or suite is neither green nor red: it says nothing about
// the commit (someone, or a newer push, stopped it), so the epic stays
// where it is, with no fix child, until a run concludes.
func TestGitHubCancelledIsNeitherGreenNorRed(t *testing.T) {
	r := newGHRig(t)
	ctx := context.Background()
	r.epic(t, "yard-a", "in-pr")
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliver(t, "check_suite", "check_suite_cancelled", "yard-a", "")
	r.pass(t)

	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s after a cancelled suite, want in-pr", st)
	}
	wantRules(t, r.rules(t, "yard-a"))
	if _, err := r.api.GetBead(ctx, "yard-a-ci-4f2c9e1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a fix child for a cancelled suite: %v", err)
	}
	// The next conclusion on the head decides.
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "open" {
		t.Fatalf("yard-a in %s after the failed run, want open", st)
	}
	wantRules(t, r.rules(t, "yard-a"), "ci_red added yard-a-ci-4f2c9e1; in-pr -> open")
}

// A held epic is a human's: the rules leave it where it is, whatever
// GitHub reports, and say so in the github_rule event. The event is judged
// (its seq recorded, the PR head kept), so an un-hold does not replay it;
// the next conclusion after the un-hold moves the epic.
func TestGitHubRulesSkipHeldEpics(t *testing.T) {
	r := newGHRig(t)
	ctx := context.Background()
	r.epic(t, "yard-a", "in-pr")
	_, err := r.api.SetHold(ctx, "yard-a", true, store.Expect{}, "human")
	must(t, err)
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)

	e := r.bead(t, "yard-a")
	if e.Stage != "in-pr" || !e.Hold {
		t.Fatalf("held yard-a %s hold=%v after CI green, want left in in-pr", e.Stage, e.Hold)
	}
	wantRules(t, r.rules(t, "yard-a"), "ci_green ignored: epic is held")
	if e.Metadata["pr_head"] != fixtureSHA {
		t.Fatalf("pr_head %v, want %s kept while held", e.Metadata["pr_head"], fixtureSHA)
	}

	_, err = r.api.SetHold(ctx, "yard-a", false, store.Expect{}, "human")
	must(t, err)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s after the un-hold, want in-pr: the green run was judged while held", st)
	}
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "land" {
		t.Fatalf("yard-a in %s after CI green on the un-held epic, want land", st)
	}
	wantRules(t, r.rules(t, "yard-a"), "ci_green ignored: epic is held", "ci_green in-pr -> land")
}

// A merged PR closes the epic through the flow from land, in-pr and
// approved; an epic in land whose merger still runs is left to it.
func TestGitHubPRMerged(t *testing.T) {
	r := newGHRig(t)
	ctx := context.Background()
	must(t, r.api.PutGroup(ctx, store.Group{Name: "yard-pr-merge", Runner: "fake", Limit: 1}))
	for id, stage := range map[string]string{"yard-a": "land", "yard-b": "in-pr", "yard-c": "approved",
		"yard-d": "open", "yard-e": "land"} {
		r.epic(t, id, stage)
	}
	// yard-e's merger is at work.
	if _, err := r.api.Claim(ctx, "yard-e", "yard-pr-merge", "land", "yard"); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"yard-a", "yard-b", "yard-c", "yard-d", "yard-e"} {
		r.deliver(t, "pull_request", "pull_request_closed_merged", e, "")
	}
	r.pass(t)
	for _, id := range []string{"yard-a", "yard-b", "yard-c"} {
		if b := r.bead(t, id); b.Stage != "merged" || b.Status != "closed" {
			t.Fatalf("%s in %s (%s), want merged and closed", id, b.Stage, b.Status)
		}
	}
	wantRules(t, r.rules(t, "yard-a"), "pr_merged land -> merged")
	wantRules(t, r.rules(t, "yard-b"), "pr_merged in-pr -> land -> merged")
	wantRules(t, r.rules(t, "yard-c"), "pr_merged approved -> merged")
	if st := r.stageOf(t, "yard-d"); st != "open" {
		t.Fatalf("yard-d in %s", st)
	}
	wantRules(t, r.rules(t, "yard-d"), "pr_merged ignored: epic in open")
	if b := r.bead(t, "yard-e"); b.Stage != "land" || b.Session == nil {
		t.Fatalf("yard-e in %s session %v, want left to its merger", b.Stage, b.Session)
	}
	wantRules(t, r.rules(t, "yard-e"), "pr_merged ignored: epic in land with session")

	// Closed epics are past every rule: a redelivery logs nothing.
	r.deliver(t, "pull_request", "pull_request_closed_merged", "yard-a", "")
	r.pass(t)
	wantRules(t, r.rules(t, "yard-a"), "pr_merged land -> merged")
}

// A PR closed unmerged asks a human whether to reopen it or drop the epic.
func TestGitHubPRClosedUnmerged(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
	r.epic(t, "yard-b", "open")
	for _, e := range []string{"yard-a", "yard-b"} {
		r.deliver(t, "pull_request", "pull_request_closed_unmerged", e, "")
	}
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "decide" {
		t.Fatalf("yard-a in %s, want decide", st)
	}
	if n := r.lastNote(t, "yard-a"); !strings.HasPrefix(n, "PR closed unmerged: reopen, or drop the epic?") {
		t.Fatalf("note %q", n)
	}
	wantRules(t, r.rules(t, "yard-a"), "pr_closed in-pr -> decide")
	if st := r.stageOf(t, "yard-b"); st != "open" {
		t.Fatalf("yard-b in %s", st)
	}
	wantRules(t, r.rules(t, "yard-b"), "pr_closed ignored: epic in open")
}

// A human's changes_requested review sends the epic to open with the
// review in a note; approving or commenting reviews, and bots, do not.
func TestGitHubChangesRequested(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
	r.epic(t, "yard-b", "open")
	r.epic(t, "yard-c", "in-pr")
	for _, e := range []string{"yard-a", "yard-b"} {
		r.deliver(t, "pull_request_review", "pull_request_review_approved", e, "")
		r.deliver(t, "pull_request_review", "pull_request_review_commented", e, "")
		r.deliver(t, "pull_request_review", "pull_request_review_changes_requested", e, "")
	}
	body, err := os.ReadFile("../hooks/testdata/github/pull_request_review_changes_requested.json")
	must(t, err)
	bot := strings.ReplaceAll(strings.ReplaceAll(string(body), "yard-sv6b", "yard-c"), `"login": "mayor"`, `"login": "ci-helper[bot]"`)
	h := http.Header{}
	h.Set("X-GitHub-Event", "pull_request_review")
	env, err := hooks.GitHub{}.Normalize(h, []byte(bot))
	must(t, err)
	_, _, err = r.own.RecordHook(context.Background(), "gh", env.Map(), []byte(bot), nil)
	must(t, err)
	r.pass(t)

	if st := r.stageOf(t, "yard-a"); st != "open" {
		t.Fatalf("yard-a in %s, want open", st)
	}
	if n := r.lastNote(t, "yard-a"); !strings.Contains(n, "Changes requested on the PR by mayor") ||
		!strings.Contains(n, "the gate is red, see the race") {
		t.Fatalf("note %q", n)
	}
	wantRules(t, r.rules(t, "yard-a"), "changes_requested in-pr -> open")
	if st := r.stageOf(t, "yard-b"); st != "open" {
		t.Fatalf("yard-b in %s", st)
	}
	wantRules(t, r.rules(t, "yard-b"), "changes_requested ignored: epic in open")
	if st := r.stageOf(t, "yard-c"); st != "in-pr" {
		t.Fatalf("a bot's review moved yard-c to %s", st)
	}
	wantRules(t, r.rules(t, "yard-c"))
}

// Hook events for branches that are not an epic's, other sources and
// issue comments move nothing and log nothing.
func TestGitHubIgnoresOtherEvents(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
	_, err := r.api.CreateBead(context.Background(), store.NewBead{ID: "yard-t", Title: "task", Rig: "yard"}, "me")
	must(t, err)
	r.deliver(t, "issue_comment", "issue_comment_pr", "yard-a", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-t", "") // a task's branch
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-zzz", "")
	_, _, err = r.own.RecordHook(context.Background(), "lin", map[string]any{"source": "linear", "type": "comment",
		"fields": map[string]any{"epic": []any{"yard-a"}}}, []byte("{}"), nil)
	must(t, err)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s", st)
	}
	wantRules(t, r.rules(t, "yard-a"))
	wantRules(t, r.rules(t, "yard-t"))
}

// An epic is judged on the pull request it is bound to only: events for a
// branch of the same name in another repository, or for another pull
// request of the branch, move nothing and set no head.
func TestGitHubRulesJudgeTheEpicsOwnPR(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
	elsewhere := func(s string) string { return strings.ReplaceAll(s, "acme/yardr", "other/yardr") }
	otherPR := func(s string) string { return strings.ReplaceAll(s, `"number": 17`, `"number": 18`) }
	old := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	// A PR in another repository and another PR of the branch, at another
	// commit, say nothing of the epic's head; their green runs land nothing.
	r.deliverEdited(t, "pull_request", "pull_request_synchronize", "yard-a", old, elsewhere)
	r.deliverEdited(t, "pull_request", "pull_request_synchronize", "yard-a", old, otherPR)
	r.deliverEdited(t, "workflow_run", "workflow_run_success", "yard-a", "", elsewhere)
	r.deliverEdited(t, "workflow_run", "workflow_run_success", "yard-a", "", otherPR)
	r.pass(t)
	if b := r.bead(t, "yard-a"); b.Stage != "in-pr" || b.Metadata["pr_head"] != nil {
		t.Fatalf("yard-a in %s with pr_head %v after events for other PRs", b.Stage, b.Metadata["pr_head"])
	}
	wantRules(t, r.rules(t, "yard-a"),
		"ci_green ignored: event is in other/yardr, not the epic's PR acme/yardr#17",
		"ci_green ignored: event is for acme/yardr#18, not the epic's PR acme/yardr#17")

	// The epic's own PR: its head is recorded and green on it lands.
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	if b := r.bead(t, "yard-a"); b.Stage != "land" || b.Metadata["pr_head"] != fixtureSHA {
		t.Fatalf("yard-a in %s with pr_head %v, want land at %s", b.Stage, b.Metadata["pr_head"], fixtureSHA)
	}

	// An epic bound before pr_number and pr_head_repo were stored has its
	// pr_url, which names the same pull request.
	r.unboundEpic(t, "yard-b", "in-pr")
	_, err := r.api.UpdateBead(context.Background(), "yard-b", store.BeadPatch{Metadata: map[string]any{
		"pr_url": "https://github.com/acme/yardr/pull/17"}}, store.Expect{}, "me")
	must(t, err)
	r.deliverEdited(t, "workflow_run", "workflow_run_success", "yard-b", "", otherPR)
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-b", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-b", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-b"); st != "land" {
		t.Fatalf("yard-b, bound by pr_url alone, in %s, want land", st)
	}
	wantRules(t, r.rules(t, "yard-b"),
		"ci_green ignored: event is for acme/yardr#18, not the epic's PR acme/yardr#17", "ci_green in-pr -> land")
}

// A fork's pull request from a branch of the epic's name is in the epic's
// repository and carries its epic field, as its CI does: neither its head,
// its green run nor its merge is the epic's. This is the round-three
// finding (yardr-ca0d): the branch name is anyone's to use.
func TestGitHubIgnoresAForksPRWithTheEpicsBranch(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
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
	// repository tells it from a run on the epic's branch.
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
	r.pass(t)
	if b := r.bead(t, "yard-a"); b.Stage != "in-pr" || b.Metadata["pr_head"] != nil {
		t.Fatalf("yard-a in %s with pr_head %v after a fork's PR, want in-pr and no head", b.Stage, b.Metadata["pr_head"])
	}
	wantRules(t, r.rules(t, "yard-a"),
		"ci_green ignored: event's head is in mallory/yardr, not acme/yardr, the head repository of the epic's PR acme/yardr#17",
		"pr_merged ignored: event is for acme/yardr#18, not the epic's PR acme/yardr#17")

	// The fork's run at the very commit the epic's PR has is still the
	// fork's: its workflows are not the epic's CI.
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliverEdited(t, "workflow_run", "workflow_run_success", "yard-a", "", forkRun)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("a fork's green run on the PR's head moved yard-a to %s", st)
	}

	// A number that matches is not enough when the head repository does
	// not: the epic is bound to both.
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

// A merged event for a pull request the epic is not bound to does nothing,
// in any stage a merge would move: neither for a bound epic and another
// number, nor for an epic bound to no pull request at all, whose branch
// name is all the event has.
func TestGitHubForgedMergeForAnUnknownPRDoesNothing(t *testing.T) {
	r := newGHRig(t)
	unknown := func(s string) string { return strings.ReplaceAll(s, `"number": 17`, `"number": 99`) }
	for i, stage := range []string{"land", "in-pr", "approved"} {
		bound, unbound := "yard-b"+strconv.Itoa(i), "yard-u"+strconv.Itoa(i)
		r.epic(t, bound, stage)
		r.unboundEpic(t, unbound, stage)
		r.deliverEdited(t, "pull_request", "pull_request_closed_merged", bound, "", unknown)
		r.deliverEdited(t, "pull_request", "pull_request_closed_merged", unbound, "", unknown)
		r.deliver(t, "pull_request", "pull_request_closed_merged", unbound, "")
		r.pass(t)
		for _, id := range []string{bound, unbound} {
			if b := r.bead(t, id); b.Stage != stage || b.Status == "closed" || b.Metadata["pr_head"] != nil {
				t.Fatalf("%s is %s/%s with pr_head %v after a forged merge, want it left in %s", id, b.Stage, b.Status, b.Metadata["pr_head"], stage)
			}
		}
		wantRules(t, r.rules(t, bound), "pr_merged ignored: event is for acme/yardr#99, not the epic's PR acme/yardr#17")
		wantRules(t, r.rules(t, unbound),
			"pr_merged ignored: epic is bound to no pull request", "pr_merged ignored: epic is bound to no pull request")
	}

	// An unbound epic's green run lands nothing either, and no head is
	// taken from a pull request it was never bound to.
	r.unboundEpic(t, "yard-g", "in-pr")
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-g", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-g", "")
	r.pass(t)
	if b := r.bead(t, "yard-g"); b.Stage != "in-pr" || b.Metadata["pr_head"] != nil {
		t.Fatalf("unbound yard-g in %s with pr_head %v", b.Stage, b.Metadata["pr_head"])
	}
}
