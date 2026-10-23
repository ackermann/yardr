package dispatch_test

// The issue flow of the contributors pack, as the dispatcher runs it: the
// pack's flow and its hook's tables, with GitHub's deliveries normalized
// through the github pack as a yard does. A bead is made by an issue's
// event, is accepted into the backlog, and is worked through the starter's
// stages; a close on GitHub ends it wherever it waits.

import (
	"context"
	"fmt"
	"io/fs"
	"slices"
	"testing"

	"yardr.dev/yardr/internal/dispatch"
	"yardr.dev/yardr/internal/pack"
	"yardr.dev/yardr/internal/store"
	"yardr.dev/yardr/packs"
)

// newContributorsIssueYard is a yard whose depot yard, of acme/yardr,
// follows the contributors pack for beads of the type issue: its flow, and
// the hook gh with the pack's tables. The yard has the groups the stages
// are worked by, as an import beside the starter leaves them: builders,
// reviewers and assembly run the fake runner, and backlog and decisions
// are held by hand.
func newContributorsIssueYard(t *testing.T) *ghDepot {
	t.Helper()
	r := newGHDepot(t)
	ctx := context.Background()
	sub, err := fs.Sub(packs.FS, packs.Contributors)
	must(t, err)
	p, err := pack.Load(sub, packs.Embedded)
	must(t, err)
	for _, f := range p.Flows {
		if f.Name == "issue" {
			_, err := r.api.PutFlow(ctx, f.StoreFlow("contributors.issue"), "me")
			must(t, err)
		}
	}
	must(t, r.api.SetFlowPointer(ctx, "yard", "issue", "contributors.issue", "me"))
	h, err := r.api.GetHook(ctx, "gh")
	must(t, err)
	h.Says, h.Makes = p.Sources[0].Says, p.Sources[0].Makes
	_, err = r.api.PutHook(ctx, *h, "me")
	must(t, err)
	r.d.DepotRepo = func(_ context.Context, q dispatch.DepotRepoQuery) (string, error) {
		if q.Depot.Name == "yard" {
			return "acme/yardr", nil
		}
		return "", nil
	}
	return r
}

// issueEvent edits the issues fixtures to issue number by sender, a user
// or a bot.
func issueEvent(number int, login, kind string) func(map[string]any) {
	return func(m map[string]any) {
		who := map[string]any{"login": login, "type": kind}
		m["sender"] = who
		issue := m["issue"].(map[string]any)
		issue["number"], issue["user"] = number, who
		issue["html_url"] = fmt.Sprintf("https://github.com/acme/yardr/issues/%d", number)
		issue["title"] = "Hook listener logs the secret path"
	}
}

// An issue closed on GitHub ends its bead wherever the bead waits past
// triage, which had the edge first: in the backlog, under build, under
// review, waiting to land and in decide, where a question leaves it for a
// person. The close is the decision, made by someone who may close that
// repository's issues. Where the bead is worked its session ends with it:
// the builder's, the reviewer's and the assembly's are stopped. The yard's
// own close when a bead is merged finds the bead already terminal and is
// refused quietly.
func TestContributorsClosedIssueEndsItsBead(t *testing.T) {
	t.Parallel()
	r := newContributorsIssueYard(t)
	ctx := context.Background()
	// Backlog and decide are stages of people, crossed as the hook's crew
	// member: the members of backlog and of the decisions include it, as
	// an import's must for the label accepted to move a bead. Their
	// runner starts nothing of the yard's, as the daemon's manual one.
	r.d.Runners["manual"] = &fakeRunner{dead: map[string]bool{}}
	must(t, r.api.PutGroup(ctx, store.Group{Name: "backlog", Runner: "manual", Limit: 50, Members: []string{"mckean", "yardmaster"}}, "me"))
	must(t, r.api.PutGroup(ctx, store.Group{Name: "decisions", Runner: "manual", Limit: 50, Members: []string{"mckean", "yardmaster"}}, "me"))
	issue, everyone := "issue", store.OriginatorAny
	peopleGroup := map[string]string{"backlog": "backlog", "decide": "decisions"}
	for stage, group := range peopleGroup {
		_, err := r.api.AddRoute(ctx, store.Route{Stage: stage, Type: &issue, Originator: &everyone, Group: group}, "me")
		must(t, err)
	}
	// The worked stages, given to the yard's groups, which start a
	// session each, as the starter's routes do.
	for stage, group := range map[string]string{"new": "builders", "review": "reviewers", "approved": "assembly"} {
		must(t, r.api.PutGroup(ctx, store.Group{Name: group, Runner: "fake", Limit: 5}, "me"))
		_, err := r.api.AddRoute(ctx, store.Route{Stage: stage, Type: &issue, Originator: &everyone, Group: group}, "me")
		must(t, err)
	}

	// arrive makes the bead of a new issue and takes it to stage as its
	// events and reports would: opened, labeled accepted into the
	// backlog, taken up, reported on, or asked about.
	number := 100
	arrive := func(stage string) *store.Bead {
		number++
		r.deliverJSON(t, "issues", "issues_opened", issueEvent(number, "jane-doe", "User"))
		r.pass(t)
		b := r.made(t, fmt.Sprintf("https://github.com/acme/yardr/issues/%d", number))
		if b.Type != "issue" || b.Stage != "triage" {
			t.Fatalf("issue %d made %s of type %s in %s, want an issue in triage", number, b.ID, b.Type, b.Stage)
		}
		r.deliverJSON(t, "issues", "issues_labeled", issueEvent(number, "mckean", "User"))
		r.pass(t)
		if st := r.stageOf(t, b.ID); st != "backlog" {
			t.Fatalf("%s in %s after its issue was labeled accepted, want backlog", b.ID, st)
		}
		if stage == "backlog" {
			return b
		}
		_, err := r.api.Advance(ctx, b.ID, "new", store.Expect{}, "yardmaster")
		must(t, err)
		switch stage {
		case "review":
			_, err := r.api.Done(ctx, b.ID, "done", "built", store.Expect{Stage: "new"}, "me")
			must(t, err)
		case "approved":
			_, err := r.api.Done(ctx, b.ID, "done", "built", store.Expect{Stage: "new"}, "me")
			must(t, err)
			_, err = r.api.Done(ctx, b.ID, "done", "looks good", store.Expect{Stage: "review"}, "me")
			must(t, err)
		case "decide":
			_, err := r.api.Done(ctx, b.ID, "question", "is this change wanted?", store.Expect{Stage: "new"}, "me")
			must(t, err)
		}
		r.pass(t)
		if st := r.stageOf(t, b.ID); st != stage {
			t.Fatalf("%s in %s, want %s", b.ID, st, stage)
		}
		return b
	}

	// workedIn are the sessions of the yard that worked bead id in stage:
	// the builder's, the reviewer's, the assembly's. Nobody of the yard
	// works the stages of people.
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

	for _, stage := range []string{"backlog", "new", "review", "approved", "decide"} {
		b := arrive(stage)
		worked := workedIn(b.ID, stage)
		if _, people := peopleGroup[stage]; people != (len(worked) == 0) {
			t.Fatalf("%s in %s worked by %v", b.ID, stage, worked)
		}

		r.deliverJSON(t, "issues", "issues_closed", issueEvent(number, "mckean", "User"))
		r.pass(t)
		got := r.bead(t, b.ID)
		if got.Stage != "closed" || got.Status != "closed" {
			t.Errorf("%s: a closed issue left its bead in %s (%s), want closed", stage, got.Stage, got.Status)
		}
		wantRules(t, r.rules(t, b.ID), "issues made "+b.ID, "accepted triage -> backlog", "dropped "+stage+" -> closed")

		// Whatever worked the stage is stopped with the bead.
		r.pass(t)
		for _, s := range worked {
			if !slices.Contains(r.stoppedSessions(), s) {
				t.Errorf("%s: session %s of the old stay still runs: stopped %v", stage, s, r.stoppedSessions())
			}
		}
	}

	// The yard's own close when a bead is merged: the closed event finds
	// the bead already terminal and is refused quietly, moving and
	// logging nothing.
	landed := arrive("approved")
	_, err := r.api.Done(ctx, landed.ID, "done", "landed", store.Expect{Stage: "approved"}, "me")
	must(t, err)
	r.pass(t)
	if st := r.stageOf(t, landed.ID); st != "merged" {
		t.Fatalf("%s in %s after its bead was landed, want merged", landed.ID, st)
	}
	before := r.rules(t, landed.ID)
	r.deliverJSON(t, "issues", "issues_closed", issueEvent(number, "mckean", "User"))
	r.pass(t)
	if got := r.bead(t, landed.ID); got.Stage != "merged" || got.Status != "closed" {
		t.Errorf("a close at merged left %s in %s (%s), want merged closed", landed.ID, got.Stage, got.Status)
	}
	if after := r.rules(t, landed.ID); !slices.Equal(after, before) {
		t.Errorf("hook_rule events %q after a close at merged, want %q", after, before)
	}
}
