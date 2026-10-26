package store_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/peer"
	"yardr.dev/yardr/internal/store"
)

// An exec session counts as its group for a move out of a stage whose
// group lists that group among its members: the release script's group
// moves the bead its stage's people would. The actor names nothing that
// matters; the session's group is what is listed.
func TestExecSessionMovesAsItsListedGroup(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "scripts", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"scripts"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "deciders"}, "me"); err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, w.ID, "scripts", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "to move", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Advance(ctx, b.ID, "review", store.Expect{Session: sess.ID}, "stranger")
	if err != nil || got.Stage != "review" {
		t.Fatalf("an exec session of a listed group moves = %+v, %v", got, err)
	}
}

// The same exec session is refused once its group is no longer listed:
// only what the stage's group lists counts.
func TestExecSessionRefusedWhenItsGroupIsNotListed(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "scripts", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"scripts"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "deciders"}, "me"); err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, w.ID, "scripts", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "to move", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Advance(ctx, b.ID, "review", store.Expect{Session: sess.ID}, "stranger")
	if err != nil || got.Stage != "review" {
		t.Fatalf("an exec session of a listed group moves = %+v, %v", got, err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"jo"}}, "me"); err != nil {
		t.Fatal(err)
	}
	next, err := s.CreateBead(ctx, store.NewBead{Title: "next", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, next.ID, "review", store.Expect{Session: sess.ID}, "stranger"); err == nil ||
		!strings.Contains(err.Error(), "only members of group deciders may move it out of stage new: jo") {
		t.Fatalf("an exec session of an unlisted group moves = %v", err)
	}
}

// boxFlow is a flow in the box flow's shape: a bead is asked for in
// request, a person of the approvers group says yes in approve (or of the
// fastlane group in expedite), and only then may a bead be filed in
// create, which a script group works.
const boxFlow = `
request  -> approve | expedite
approve  -> create on done | request
expedite -> create
create   -> finished on done
finished terminal
`

// boxDepot is a yard of boxFlow whose approve stage the approvers group
// holds (ada, and the scripts group by name) and whose expedite stage the
// fastlane group holds (hal alone); create is the scripts group's.
func boxDepot(t *testing.T) (store.API, context.Context) {
	t.Helper()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", boxFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "approvers", Runner: "manual", Members: []string{"ada", "scripts"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "fastlane", Runner: "manual", Members: []string{"hal"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "scripts", Runner: "exec", Config: map[string]any{"cmd": "true"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "agents", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	for stage, group := range map[string]string{"approve": "approvers", "expedite": "fastlane", "create": "scripts"} {
		if _, err := s.AddRoute(ctx, store.Route{Stage: stage, Group: group}, "me"); err != nil {
			t.Fatal(err)
		}
	}
	return s, ctx
}

// Filing straight into create, past stages only their members may move a
// bead out of, is refused to whoever is a member of none of those groups,
// naming each blocking group as a move out of it is named.
func TestCreatePastMembersStagesRefusedForNonMember(t *testing.T) {
	t.Parallel()
	s, ctx := boxDepot(t)
	_, err := s.CreateBead(ctx, store.NewBead{Title: "spend", Depot: "yard", Stage: "create"}, "agent-1")
	if err == nil ||
		!strings.Contains(err.Error(), "only members of group approvers may file a bead in stage create: ada, scripts") ||
		!strings.Contains(err.Error(), "only members of group fastlane may file a bead in stage create: hal") ||
		!strings.Contains(err.Error(), `(you are "agent-1")`) {
		t.Fatalf("a non-member files in create = %v", err)
	}
	if all, err := s.ListBeads(ctx, store.BeadFilter{}); err != nil || len(all) != 0 {
		t.Fatalf("beads after the refused create: %d (%v), want none", len(all), err)
	}
}

// A member of any one of the stages before create files there, as
// advancing from that stage would let them.
func TestCreatePastMembersStagesForAMemberOfAny(t *testing.T) {
	t.Parallel()
	s, ctx := boxDepot(t)
	for _, member := range []string{"ada", "hal"} {
		b, err := s.CreateBead(ctx, store.NewBead{Title: "spend", Depot: "yard", Stage: "create"}, member)
		if err != nil || b.Stage != "create" {
			t.Fatalf("member %s files in create = %+v, %v", member, b, err)
		}
	}
}

// The flow's first stage takes a bead from anyone, member or not, as
// before: the gate is on filing past a members stage, not on filing.
func TestCreateInFirstStageUnaffectedByMembers(t *testing.T) {
	t.Parallel()
	s, ctx := boxDepot(t)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "ask", Depot: "yard", Stage: "request"}, "agent-1")
	if err != nil || b.Stage != "request" {
		t.Fatalf("a non-member files in request = %+v, %v", b, err)
	}
	if b, err := s.CreateBead(ctx, store.NewBead{Title: "ask", Depot: "yard"}, "agent-1"); err != nil || b.Stage != "request" {
		t.Fatalf("a non-member files with no stage = %+v, %v", b, err)
	}
}

// A flow whose stages no members group holds files past its first stage
// as before.
func TestCreatePastFirstStageOfUnrestrictedFlowUnaffected(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	if err := s.PutDepot(ctx, store.Depot{Name: "plain", Base: "origin/main"}, "me"); err != nil {
		t.Fatal(err)
	}
	putFlow(t, s, "plain", "", "one -> two\ntwo -> three\nthree terminal\n")
	b, err := s.CreateBead(ctx, store.NewBead{Title: "plain", Depot: "plain", Stage: "two"}, "agent-1")
	if err != nil || b.Stage != "two" {
		t.Fatalf("a stranger files in two of a member-less flow = %+v, %v", b, err)
	}
}

// An exec session counts as its group for filing in create too, where a
// stage before create lists that group among its members: the script is
// the operator's, as it is for a move out.
func TestExecSessionFilesPastMembersStageAsItsListedGroup(t *testing.T) {
	t.Parallel()
	s, ctx := boxDepot(t)
	w, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "approve"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, w.ID, "scripts", "approve", "yard")
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	handle := map[string]any{"pid": me}
	if st, err := peer.Describe(me); err == nil {
		handle["start"] = st.Start
	}
	if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(store.WithCaller(ctx, me), store.NewBead{Title: "spend", Depot: "yard", Stage: "create"}, "stranger")
	if err != nil || b.Stage != "create" {
		t.Fatalf("an exec session of a listed group files in create = %+v, %v", b, err)
	}
}

// A bead's agent filing past the first stage is still refused by the
// agent's own guard first, where the stage is one nobody is shown before
// work starts: the members gate does not speak for it.
func TestPaneAgentStillRefusedIntoUnseenStageOnCreate(t *testing.T) {
	t.Parallel()
	s, ctx := boxDepot(t)
	w, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "request"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, w.ID, "agents", "request", "yard")
	if err != nil {
		t.Fatal(err)
	}
	me := os.Getpid()
	handle := map[string]any{"agent": "yard-w-request", "shell_pid": me}
	if st, err := peer.Describe(me); err == nil {
		handle["shell_start"] = st.Start
	}
	if err := s.MarkRunning(ctx, sess.ID, handle); err != nil {
		t.Fatal(err)
	}
	_, err = s.CreateBead(store.WithCaller(ctx, me), store.NewBead{Title: "spend", Depot: "yard", Stage: "create"}, "yard-w-request")
	if err == nil ||
		!strings.Contains(err.Error(), "the session "+sess.ID+" of a bead's agent may create a bead only in its flow's first stage") {
		t.Fatalf("a pane agent files in create = %v", err)
	}
}

// An agent session of a listed group is still refused: the intake guard
// stands for runners with a model to talk to, whatever group lists them.
func TestAgentSessionOfListedGroupStillRefused(t *testing.T) {
	t.Parallel()
	s, ctx := flowDepot(t)
	putFlow(t, s, "yard", "", outcomeFlow)
	if err := s.PutGroup(ctx, store.Group{Name: "agents", Runner: "herdr"}, "me"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(ctx, store.Group{Name: "deciders", Runner: "manual", Members: []string{"agents"}}, "me"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddRoute(ctx, store.Route{Stage: "new", Group: "deciders"}, "me"); err != nil {
		t.Fatal(err)
	}
	w, err := s.CreateBead(ctx, store.NewBead{Title: "work", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	sess, err := s.Claim(ctx, w.ID, "agents", "new", "yard")
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateBead(ctx, store.NewBead{Title: "to move", Depot: "yard", Stage: "new"}, "me")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Advance(ctx, b.ID, "review", store.Expect{Session: sess.ID}, "agents"); err == nil ||
		!strings.Contains(err.Error(), "only members of group deciders may move it out of stage new: agents (this is the session "+sess.ID+" of a bead's agent, whatever actor the command names)") {
		t.Fatalf("an agent session of a listed group moves = %v", err)
	}
}

// A hand fold names the stage its carrier starts in, so it files there as
// bead create -s does: a non-member cannot fold a bead filed in the first
// stage into a carrier past the members stages, and a member can.
func TestHandFoldPastMembersStagesRefusedForNonMember(t *testing.T) {
	t.Parallel()
	s, ctx := boxDepot(t)
	b, err := s.CreateBead(ctx, store.NewBead{Title: "ask", Depot: "yard", Stage: "request"}, "agent-1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.FoldBeads(ctx, []string{b.ID}, store.FoldOptions{To: "create"}, "agent-1")
	if err == nil || !strings.Contains(err.Error(), "only members of group approvers may file a bead in stage create") {
		t.Fatalf("a non-member folds into create = %v", err)
	}
	carrier, err := s.FoldBeads(ctx, []string{b.ID}, store.FoldOptions{To: "create"}, "ada")
	if err != nil || carrier.Stage != "create" {
		t.Fatalf("a member folds into create = %+v, %v", carrier, err)
	}
}
