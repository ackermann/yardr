package dispatch_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
)

// prApprovedFlow mirrors the contributors pack's pr flow: a pull request a
// maintainer approved in approved is merged on GitHub, and the merge says
// merged to the bead that tracks it.
const prApprovedFlow = `
triage     -> review on accepted | closed on dropped
review     -> approved on done | contributor on changes
contributor -> review on pushed | closed on dropped
approved   -> merged on merged | closed on dropped
merged terminal
closed terminal
`

// A says outcome from a hook moves a bead out of a stage of people: the
// move is reported as the hook's crew member, which the operator set for
// the pack's own mapping (the contributors pr flow's approved -> merged).
// An agent's session still cannot move out of such a stage by naming that
// member.
func TestSaysOutcomeMovesBeadOutOfPeopleStage(t *testing.T) {
	t.Parallel()
	r := newGHDepot(t, "mckean", "yardmaster")
	ctx := context.Background()
	flow, err := store.ParseFlow(prApprovedFlow)
	must(t, err)
	flow.Name = "pr"
	_, err = r.api.PutFlow(ctx, *flow, "me")
	must(t, err)
	must(t, r.api.SetFlowPointer(ctx, "yard", "", "pr", "me"))
	must(t, r.api.PutGroup(ctx, store.Group{Name: "maintainers", Runner: "manual", Limit: 50, Members: []string{"mckean", "yardmaster"}}, "me"))
	_, err = r.api.AddRoute(ctx, store.Route{Stage: "approved", Group: "maintainers"}, "me")
	must(t, err)
	r.tracker(t, "yard-p", "approved", "https://github.com/acme/yardr/pull/17")
	r.deliverAs(t, "gh", "pull_request", "pull_request_closed_merged", nil)
	r.pass(t)
	if st := r.stageOf(t, "yard-p"); st != "merged" {
		t.Fatalf("yard-p in %s after its pull request merged, want merged", st)
	}
	wantRules(t, r.rules(t, "yard-p"), "merged approved -> merged")

	// The member itself, with no session behind it, moves the bead: that
	// is who the hook's move above was reported as. It comes before this
	// test process becomes an agent's pane below: over the socket the
	// server sees the process as the caller of every call, so after that
	// a member's move would be checked as the pane's.
	must(t, r.api.PutGroup(ctx, store.Group{Name: "builders", Runner: "herdr"}, "me"))
	held, err := r.api.CreateBead(ctx, store.NewBead{Title: "approved", Depot: "yard", Stage: "approved"}, "me")
	must(t, err)
	if got, err := r.api.Done(ctx, held.ID, "merged", "", store.Expect{}, "yardmaster"); err != nil || got.Stage != "merged" {
		t.Fatalf("yardmaster's done = %+v, %v", got, err)
	}

	// An agent's session naming the hook's crew member moves nothing out
	// of the stage of people.
	work, err := r.api.CreateBead(ctx, store.NewBead{Title: "at work", Depot: "yard", Stage: "review"}, "me")
	must(t, err)
	sess, err := r.api.Claim(ctx, work.ID, "builders", "review", "yard")
	must(t, err)
	stuck, err := r.api.CreateBead(ctx, store.NewBead{Title: "stuck", Depot: "yard", Stage: "approved"}, "me")
	must(t, err)
	handle := map[string]any{"agent": "yardr-bfke-agent", "shell_pid": os.Getpid()}
	if process, err := peer.Describe(os.Getpid()); err == nil {
		handle["shell_start"] = process.Start
	}
	must(t, r.api.MarkRunning(ctx, sess.ID, handle))
	pane := store.WithCaller(ctx, os.Getpid())
	if _, err := r.api.Done(pane, stuck.ID, "merged", "", store.Expect{}, "yardmaster"); err == nil ||
		!strings.Contains(err.Error(), "only members of group maintainers may move it out of stage approved: mckean, yardmaster") ||
		!strings.Contains(err.Error(), "this is the session "+sess.ID+" of a bead's agent, whatever actor the command names") {
		t.Fatalf("an agent pane's done as yardmaster = %v", err)
	}
}
