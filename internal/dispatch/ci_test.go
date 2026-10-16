package dispatch_test

import (
	"context"
	"errors"
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

// reviewed stores sha as the commit pr-train pushed for the train.
func (r *ghDepot) reviewed(t *testing.T, id, sha string) {
	t.Helper()
	_, err := r.api.UpdateBead(context.Background(), id, store.BeadPatch{Metadata: map[string]any{"pr_reviewed_sha": sha}},
		store.Expect{}, "me")
	must(t, err)
}

// enterInPR moves a train in review through approved to in-pr as pr-train
// does, with sha as the reviewed commit.
func (r *ghDepot) enterInPR(t *testing.T, id, sha string) {
	t.Helper()
	ctx := context.Background()
	_, err := r.api.AdvanceNote(ctx, id, "approved", "", store.Expect{Stage: "review"}, "me")
	must(t, err)
	r.reviewed(t, id, sha)
	_, err = r.api.AdvanceNote(ctx, id, "in-pr", "", store.Expect{Stage: "approved"}, "me")
	must(t, err)
}

// CI that finished while the train was under review is seen when the train
// enters in-pr: the run's event came and went in review, where it moves
// nothing, and no event follows, yet the train lands. This is the
// round-three finding (yardr-7daf): green was taken from the event, so a
// train whose CI was done before it reached in-pr waited for a human.
func TestCIFinishedDuringReviewIsSeenOnEnteringInPR(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "review")
	r.ci = checks([]string{"build"}, ck("build", "completed", "success"))
	r.deliver(t, "pull_request", "pull_request_synchronize", "yard-a", "")
	r.deliver(t, "workflow_run", "workflow_run_success", "yard-a", "")
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "review" {
		t.Fatalf("yard-a in %s after a green run in review, want review", st)
	}
	if len(r.asked) != 0 {
		t.Fatalf("GitHub asked about a train in review: %+v", r.asked)
	}

	r.enterInPR(t, "yard-a", fixtureSHA)
	r.pass(t)
	if st := r.stageOf(t, "yard-a"); st != "land" {
		t.Fatalf("yard-a in %s, want land: its CI finished during review", st)
	}
	want := dispatch.CIQuery{Train: "yard-a", Repo: "acme/yardr", Number: "17", SHA: fixtureSHA}
	if !slices.Equal(r.asked, []dispatch.CIQuery{want}) {
		t.Fatalf("asked %+v, want the reviewed commit of the bound PR, once", r.asked)
	}
	if n := r.lastNote(t, "yard-a"); n != "CI green on "+fixtureSHA+": build (the checks main requires)\n(github ci completed)" {
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

// One green workflow among several required ones is not green: the train
// waits for the others, the first run's own success event included, and
// lands when the last required check is green. An event is only a reason
// to read the state again; without one nothing is read.
func TestCIOneGreenAmongRequiredIsNotGreen(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "in-pr")
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
	if n := r.lastNote(t, "yard-a"); !strings.HasPrefix(n, "CI green on "+fixtureSHA+": build, lint, test (the checks main requires)") {
		t.Fatalf("note %q", n)
	}
}

// A required check that failed is the outcome changes: the train goes back
// to open with the failed checks in a note, also while other required
// checks still run. A cancelled one is not red: the train waits for it to
// run again.
func TestCIRedRequiredCheckSendsTheTrainBack(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	ctx := context.Background()
	r.train(t, "yard-a", "in-pr")
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
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: pending", "ci red: in-pr -> open")
	wantRules(t, r.rules(t, "yard-a"))
	if n := r.lastNote(t, "yard-a"); !strings.HasPrefix(n, "CI red on "+fixtureSHA+": build failed: https://github.com/acme/yardr/actions/runs/5/job/build") {
		t.Fatalf("note %q", n)
	}
	if fam, err := r.api.Family(ctx, "yard-a"); err != nil || len(fam.Children) != 0 {
		t.Fatalf("yard-a children %v %v, want none: the train itself is sent back", fam.Children, err)
	}

	// Back in in-pr at the same commit, still red: back to open again.
	_, err := r.api.AdvanceNote(ctx, "yard-a", "review", "", store.Expect{Stage: "open"}, "me")
	must(t, err)
	r.enterInPR(t, "yard-a", fixtureSHA)
	r.pass(t)
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: pending", "ci red: in-pr -> open", "ci red: in-pr -> open")
}

// A base that requires no checks gives the daemon nothing to call green:
// the train is left to a human however green the commit looks, since a
// workflow that has not reported yet cannot be told from one that does
// not exist. A failed check still sends it back.
func TestCIWithoutRequiredChecksIsNeverGreen(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	r.train(t, "yard-a", "in-pr")
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
// pushed since is not what was reviewed, and a train with no reviewed
// commit, or whose PR is closed, is not judged.
func TestCIIsReadForTheReviewedCommit(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	moved := "cccccccccccccccccccccccccccccccccccccccc"
	green := checks([]string{"build"}, ck("build", "completed", "success"))
	r.ci = func(q dispatch.CIQuery) (dispatch.CIChecks, error) {
		c, _ := green(q)
		switch q.Train {
		case "yard-a":
			c.Head = moved
		case "yard-c":
			c.State = "closed"
		}
		return c, nil
	}
	r.train(t, "yard-a", "in-pr")
	r.reviewed(t, "yard-a", fixtureSHA)
	r.train(t, "yard-b", "in-pr")
	r.train(t, "yard-c", "in-pr")
	r.reviewed(t, "yard-c", fixtureSHA)
	r.pass(t)
	for _, id := range []string{"yard-a", "yard-b", "yard-c"} {
		if st := r.stageOf(t, id); st != "in-pr" {
			t.Fatalf("%s in %s, want in-pr", id, st)
		}
	}
	wantRules(t, r.ciRules(t, "yard-a"), "ci ignored: the PR's head "+moved+" is not the reviewed commit "+fixtureSHA)
	wantRules(t, r.ciRules(t, "yard-b"), "ci ignored: train has no pr_reviewed_sha")
	wantRules(t, r.ciRules(t, "yard-c"), "ci ignored: the PR is closed")
}

// A held train is a human's: its CI is not read while it is held, and is
// read when it is un-held, with no event needed.
func TestCISkipsHeldTrains(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	ctx := context.Background()
	r.train(t, "yard-a", "in-pr")
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

// When GitHub cannot be asked nothing is known: the train stays, the error
// is logged, and the read is tried again after a wait that doubles, with
// no event needed.
func TestCIReadFailureIsRetried(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t)
	now := time.Now()
	dispatch.SetClock(r.d, func() time.Time { return now })
	r.train(t, "yard-a", "in-pr")
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
