package dispatch_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/store"
)

// ck is a check run named name.
func ck(name, status, conclusion string) dispatch.CICheck {
	return dispatch.CICheck{Name: name, Status: status, Conclusion: conclusion,
		URL: "https://github.com/acme/yardr/actions/runs/5/job/" + name}
}

// checks is a GitHub whose open pull request has the asked commit as its
// head, a base main that requires required, and cks on the commit.
func checks(required []string, cks ...dispatch.CICheck) func(dispatch.CIQuery) (dispatch.CIChecks, error) {
	return func(q dispatch.CIQuery) (dispatch.CIChecks, error) {
		return dispatch.CIChecks{Head: q.SHA, Base: "main", State: "open", Required: required, Checks: cks}, nil
	}
}

// reviewed stores sha as the commit pr-epic pushed for the epic.
func (r *ghRig) reviewed(t *testing.T, id, sha string) {
	t.Helper()
	_, err := r.api.UpdateBead(context.Background(), id, store.BeadPatch{Metadata: map[string]any{"pr_reviewed_sha": sha}},
		store.Expect{}, "me")
	must(t, err)
}

// enterInPR moves an epic in review through approved to in-pr as pr-epic
// does, with sha as the reviewed commit.
func (r *ghRig) enterInPR(t *testing.T, id, sha string) {
	t.Helper()
	ctx := context.Background()
	_, err := r.api.AdvanceNote(ctx, id, "approved", "", store.Expect{Stage: "review"}, "me")
	must(t, err)
	r.reviewed(t, id, sha)
	_, err = r.api.AdvanceNote(ctx, id, "in-pr", "", store.Expect{Stage: "approved"}, "me")
	must(t, err)
}

// CI that finished while the epic was under review is seen when the epic
// enters in-pr: the run's event came and went in review, where it moves
// nothing, and no event follows, yet the epic lands. This is the
// round-three finding (yardr-7daf): green was taken from the event, so an
// epic whose CI was done before it reached in-pr waited for a human.
func TestCIFinishedDuringReviewIsSeenOnEnteringInPR(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "review")
	r.ci = checks([]string{"build"}, ck("build", "completed", "success"))
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "review" {
		t.Fatalf("yard-a in %s after a green run in review, want review", st)
	}
	if len(r.asked) != 0 {
		t.Fatalf("GitHub asked about an epic in review: %+v", r.asked)
	}

	r.enterInPR(t, "yard-a", fixtureSHA)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "land" {
		t.Fatalf("yard-a in %s, want land: its CI finished during review", st)
	}
	want := dispatch.CIQuery{Epic: "yard-a", Repo: "acme/yardr", Number: "17", SHA: fixtureSHA}
	if !slices.Equal(r.asked, []dispatch.CIQuery{want}) {
		t.Fatalf("asked %+v, want the reviewed commit of the bound PR, once", r.asked)
	}
	if n := r.lastNote(t, "yard-a"); n != "CI green on "+fixtureSHA+": build (the checks main requires)" {
		t.Fatalf("note %q", n)
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci green: in-pr -> land")

	// Neither a later pass nor a restarted daemon does it again.
	r.pass(t)
	restarted := &dispatch.Dispatcher{Store: r.api, Runners: r.d.Runners, PRHead: r.d.PRHead, CI: r.d.CI}
	must(t, restarted.Reconcile(context.Background()))
	wantRules(t, r.ciRules(t, "yard-a"), "ci green: in-pr -> land")
	if len(r.asked) != 1 {
		t.Fatalf("GitHub asked %d times, want once", len(r.asked))
	}
}

// One green workflow among several required ones is not green: the epic
// waits for the others, the first run's own success event included, and
// lands when the last required check is green. An event is only a reason
// to read the state again; without one nothing is read.
func TestCIOneGreenAmongRequiredIsNotGreen(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	required := []string{"build", "test", "lint"}

	// test has not even been reported yet, lint is running.
	r.ci = checks(required, ck("build", "completed", "success"), ck("lint", "in_progress", ""))
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s with one of three required checks green, want in-pr", st)
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: pending: waiting for lint, test (not reported)")

	// The green workflow's event arrives: the state is read, not the event.
	r.ci = checks(required, ck("build", "completed", "success"), ck("lint", "completed", "success"), ck("test", "queued", ""))
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s after one workflow's success event, want in-pr: test is still queued", st)
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: pending", "ci ignored: pending: waiting for test")

	// A check that is not required may fail or run on: it is not counted.
	// Without an event nothing is read, however many passes go by.
	r.ci = checks(required, ck("build", "completed", "success"), ck("lint", "completed", "success"),
		ck("test", "completed", "success"), ck("docs", "completed", "failure"), ck("nightly", "in_progress", ""))
	r.pass(t)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" || len(r.asked) != 2 {
		t.Fatalf("yard-a in %s after %d reads, want in-pr after 2: no event asked for another", st, len(r.asked))
	}
	r.deliver(t, "check_suite", "check_suite_cancelled", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "land" {
		t.Fatalf("yard-a in %s with all required checks green, want land", st)
	}
	if n := r.lastNote(t, "yard-a"); n != "CI green on "+fixtureSHA+": build, lint, test (the checks main requires)" {
		t.Fatalf("note %q", n)
	}
}

// A required check that failed sends the epic back to open with a fix
// child, one per commit, also while other required checks still run. A
// cancelled one is not red: the epic waits for it to run again.
func TestCIRedRequiredCheckFilesAFix(t *testing.T) {
	r := newGHRig(t)
	ctx := context.Background()
	r.epic(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.ci = checks([]string{"build", "test"}, ck("build", "completed", "cancelled"), ck("test", "in_progress", ""))
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s after a cancelled check, want in-pr", st)
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: pending: waiting for build, test")

	r.ci = checks([]string{"build", "test"}, ck("build", "completed", "timed_out"), ck("test", "in_progress", ""))
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "open" {
		t.Fatalf("yard-a in %s, want open", st)
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: pending", "ci red: added yard-a-ci-4f2c9e1; in-pr -> open")
	wantRules(t, r.rules(t, "yard-a"))
	if n := r.lastNote(t, "yard-a"); !strings.Contains(n, "CI red on "+fixtureSHA+": build failed") || !strings.Contains(n, "yard-a-ci-4f2c9e1") {
		t.Fatalf("note %q", n)
	}
	fix := r.bead(t, "yard-a-ci-4f2c9e1")
	if fix.Title != "Fix CI on epic yard-a: build" || fix.Type != "bug" || fix.Stage != "backlog" ||
		fix.Rig == nil || *fix.Rig != "yard" ||
		!strings.Contains(fix.Body, "https://github.com/acme/yardr/actions/runs/5/job/build") || !strings.Contains(fix.Body, "failure") {
		t.Fatalf("fix child %+v", fix)
	}
	if bl, err := r.api.OpenBlockers(ctx, "yard-a"); err != nil || !slices.Equal(bl, []string{"yard-a-ci-4f2c9e1"}) {
		t.Fatalf("yard-a blockers %v %v", bl, err)
	}

	// Back in in-pr at the same commit, still red: no second child.
	_, err := r.api.AdvanceNote(ctx, "yard-a", "review", "", store.Expect{Stage: "open"}, "me")
	must(t, err)
	r.enterInPR(t, "yard-a", fixtureSHA)
	r.pass(t)
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: pending", "ci red: added", "ci red: in-pr -> open (yard-a-ci-4f2c9e1 was there)")
	fam, err := r.api.Family(ctx, "yard-a")
	must(t, err)
	if !slices.Equal(fam.Children, []string{"yard-a-ci-4f2c9e1"}) {
		t.Fatalf("yard-a children %v", fam.Children)
	}
}

// A base that requires no checks gives the daemon nothing to call green:
// the epic is left to a human however green the commit looks, since a
// workflow that has not reported yet cannot be told from one that does
// not exist. A failed check still sends it back.
func TestCIWithoutRequiredChecksIsNeverGreen(t *testing.T) {
	r := newGHRig(t)
	r.epic(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.ci = checks(nil, ck("build", "completed", "success"))
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "in-pr" {
		t.Fatalf("yard-a in %s with no required checks, want in-pr", st)
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: main requires no checks")

	r.ci = checks(nil, ck("build", "completed", "success"), ck("test", "completed", "failure"))
	r.deliver(t, "workflow_run", "workflow_run_failure", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "open" {
		t.Fatalf("yard-a in %s after a failed check, want open", st)
	}
}

// Green counts for the reviewed commit while it is the PR's head: a head
// pushed since is not what was reviewed, and an epic with no reviewed
// commit, or whose PR is closed, is not judged.
func TestCIIsReadForTheReviewedCommit(t *testing.T) {
	r := newGHRig(t)
	moved := "cccccccccccccccccccccccccccccccccccccccc"
	green := checks([]string{"build"}, ck("build", "completed", "success"))
	r.ci = func(q dispatch.CIQuery) (dispatch.CIChecks, error) {
		c, _ := green(q)
		switch q.Epic {
		case "yard-a":
			c.Head = moved
		case "yard-c":
			c.State = "closed"
		}
		return c, nil
	}
	r.epic(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.epic(t, "yard-b", "in-pr")
	r.epic(t, "yard-c", "in-pr")
	r.reviewed(t, "yard-c", fixtureSHA)
	r.pass(t)
	for _, id := range []string{"yard-a", "yard-b", "yard-c"} {
		if st := r.stageOf(t, id); st != "in-pr" {
			t.Fatalf("%s in %s, want in-pr", id, st)
		}
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: the PR's head "+moved+" is not the reviewed commit "+fixtureSHA)
	wantRules(t, r.ciRules(t, "yard-b"), "ci ignored: epic has no pr_reviewed_sha")
	wantRules(t, r.ciRules(t, "yard-c"), "ci ignored: the PR is closed")
}

// A held epic is a human's: its CI is not read while it is held, and is
// read when it is un-held, with no event needed.
func TestCISkipsHeldEpics(t *testing.T) {
	r := newGHRig(t)
	ctx := context.Background()
	r.epic(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.ci = checks([]string{"build"}, ck("build", "completed", "success"))
	_, err := r.api.SetHold(ctx, "yard-a", true, store.Expect{}, "human")
	must(t, err)
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	if e := r.bead(t, "yard-a"); e.Stage != "in-pr" || !e.Hold || len(r.asked) != 0 {
		t.Fatalf("held yard-a %s hold=%v after %d reads, want left in in-pr unread", e.Stage, e.Hold, len(r.asked))
	}
	_, err = r.api.SetHold(ctx, "yard-a", false, store.Expect{}, "human")
	must(t, err)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "land" {
		t.Fatalf("yard-a in %s after the un-hold, want land", st)
	}
}

// When GitHub cannot be asked nothing is known: the epic stays, the error
// is logged, and the read is tried again after a wait that doubles, with
// no event needed.
func TestCIReadFailureIsRetried(t *testing.T) {
	r := newGHRig(t)
	now := time.Now()
	dispatch.SetClock(r.d, func() time.Time { return now })
	r.epic(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.ci = func(dispatch.CIQuery) (dispatch.CIChecks, error) {
		return dispatch.CIChecks{}, errors.New("gh: HTTP 502")
	}
	r.pass(t)
	r.pass(t)
	wantRules(t, r.ciRules(t, "yard-a"), "ci error: CI on "+fixtureSHA+" is unknown: gh: HTTP 502")

	now = now.Add(31 * time.Second)
	r.pass(t)
	now = now.Add(31 * time.Second) // the second wait is a minute
	r.pass(t)
	if len(r.asked) != 2 {
		t.Fatalf("GitHub asked %d times, want 2: once, and once after the first wait", len(r.asked))
	}

	r.ci = checks([]string{"build"}, ck("build", "completed", "success"))
	now = now.Add(time.Minute)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "land" {
		t.Fatalf("yard-a in %s once GitHub answered, want land", st)
	}
}

// ghCI asks gh for the pull request, the checks its base requires (by the
// branch's protection and by its rulesets) and the check runs and statuses
// on the commit, and reads them as the dispatcher judges them.
func TestGhCIReadsRequiredChecksAndTheCommits(t *testing.T) {
	bin := t.TempDir()
	// A gh that answers `gh api <path> --paginate --jq <filter>` with what
	// the real one prints for that filter, and logs the paths it was asked.
	script := `#!/bin/sh
echo "$2" >> "` + bin + `/asked"
case "$2" in
repos/acme/yardr/pulls/17) echo '{"base":"release/1","head":"` + fixtureSHA + `","state":"open"}' ;;
repos/acme/yardr/branches/release%2F1) printf 'build\ntest (ubuntu)\n' ;;
repos/acme/yardr/rules/branches/release%2F1) printf 'build\nlint\n' ;;
repos/acme/yardr/commits/` + fixtureSHA + `/check-runs*)
	echo '{"conclusion":"success","name":"build","status":"completed","url":"https://x/1"}'
	echo '{"conclusion":null,"name":"test (ubuntu)","status":"in_progress","url":"https://x/2"}' ;;
repos/acme/yardr/commits/` + fixtureSHA + `/status*)
	echo '{"conclusion":"failure","name":"lint","status":"completed","url":null}' ;;
*) echo "unexpected $2" >&2; exit 1 ;;
esac
`
	must(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := dispatch.GhCI(context.Background(), dispatch.CIQuery{Epic: "yard-a", Repo: "acme/yardr", Number: "17", SHA: fixtureSHA})
	must(t, err)
	if got.Head != fixtureSHA || got.Base != "release/1" || got.State != "open" {
		t.Fatalf("pull request %+v", got)
	}
	if !slices.Equal(got.Required, []string{"build", "test (ubuntu)", "lint"}) {
		t.Fatalf("required %q", got.Required)
	}
	want := []dispatch.CICheck{
		{Name: "build", Status: "completed", Conclusion: "success", URL: "https://x/1"},
		{Name: "test (ubuntu)", Status: "in_progress", URL: "https://x/2"},
		{Name: "lint", Status: "completed", Conclusion: "failure"},
	}
	if !slices.Equal(got.Checks, want) {
		t.Fatalf("checks %+v, want %+v", got.Checks, want)
	}

	// A gh that fails is an error, not an empty, green-looking answer.
	if _, err := dispatch.GhCI(context.Background(), dispatch.CIQuery{Repo: "acme/other", Number: "1", SHA: fixtureSHA}); err == nil ||
		!strings.Contains(err.Error(), "unexpected repos/acme/other/pulls/1") {
		t.Fatalf("err %v, want gh's failure", err)
	}
}

// GitHub uses 404 for branch policy endpoints when the branch has no
// protection or applicable ruleset. That is an empty policy, not a failed
// read (the caller then leaves an unrequired PR for a human).
func TestGhCITreatsMissingBranchPoliciesAsEmpty(t *testing.T) {
	bin := t.TempDir()
	script := `#!/bin/sh
case "$2" in
repos/acme/yardr/pulls/17) echo '{"base":"main","head":"` + fixtureSHA + `","state":"open"}' ;;
repos/acme/yardr/branches/main|repos/acme/yardr/rules/branches/main)
	echo 'gh: HTTP 404: Branch not protected' >&2
	exit 1 ;;
repos/acme/yardr/commits/` + fixtureSHA + `/check-runs*) ;;
repos/acme/yardr/commits/` + fixtureSHA + `/status*) ;;
*) echo "unexpected $2" >&2; exit 1 ;;
esac
`
	must(t, os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	got, err := dispatch.GhCI(context.Background(), dispatch.CIQuery{Repo: "acme/yardr", Number: "17", SHA: fixtureSHA})
	must(t, err)
	if len(got.Required) != 0 {
		t.Fatalf("required checks %q, want none for unprotected base", got.Required)
	}
}
