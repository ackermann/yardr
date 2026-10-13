package dispatch_test

import (
	"context"
	"strings"
	"testing"

	"yardr.dev/yardr/internal/store"
)

// A bead sent to a yard again (docs/peers.md, "A bead sent again"): where
// its work starts the second time, and what the yard that works it knows of
// the first.

// sendingAgain is the flow sending with a way back from review into the
// stage the peer works, which is how a bead is sent a second time.
const sendingAgain = "new -> build\nbuild -> review on done | decide on question | decide on failed | decide on refused | decide on undelivered\nreview -> merged | build\ndecide -> build\nmerged terminal\n"

// sendsAgain gives y's depot web the flow sendingAgain.
func (y *peerYard) sendsAgain(t *testing.T) {
	t.Helper()
	putFlow(t, y, "again", sendingAgain)
	must(t, y.api.SetFlowPointer(context.Background(), "web", "", "again", "jo"))
}

// again moves y's bead back into the stage its peer works, which sends it
// once more on the next pass.
func (y *peerYard) again(t *testing.T, id string) {
	t.Helper()
	_, err := y.api.Advance(context.Background(), id, "build", store.Expect{}, "jo")
	must(t, err)
}

// A bead that came back with a branch and is sent again starts, at the
// yard that works it, at the commit that yard returned: what it returns
// the second time comes after this yard's branch and is taken by
// fast-forward. The yard that works it names its earlier bead.
func TestPeerBeadSentAgain(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	remote := sharedRemote(t)
	bRepo := b.clones(t, remote)
	b.takes(t, "a", "web")
	aRepo := a.sendsFrom(t, remote, "b", nil)
	a.sendsAgain(t)

	mine := a.bead(t, "Add the export")
	a.pass(t)
	first := b.from(t, "a")
	if first.Peer.WasHere() != "" || first.Peer.RefusedBefore() != "" || len(first.Peer.Before) != 0 {
		t.Fatalf("b's first bead names earlier ones: %+v", first.Peer.Before)
	}
	one := b.builds(t, bRepo, first)
	b.done(t, first.ID)
	b.pass(t)
	a.pass(t)
	if got := a.get(t, mine.ID); got.Stage != "review" || branchAt(t, aRepo, store.Branch(mine.ID)) != one {
		t.Fatalf("a's bead after the first return = %+v, branch at %s", got, branchAt(t, aRepo, store.Branch(mine.ID)))
	}

	a.again(t, mine.ID)
	a.pass(t)
	sent := a.beadEvents(t, mine.ID, "peer_bead_sent")
	if len(sent) != 2 || sent[1].Data["base_commit"] != one || sent[1].Data["base"] != store.Branch(first.ID) {
		t.Fatalf("a's second peer_bead_sent = %+v, want it to start at %s", sent, one)
	}
	second := b.from(t, "a")
	fam, err := b.api.Family(context.Background(), second.ID)
	must(t, err)
	if second.ID == first.ID || second.Peer.Git == nil || second.Peer.Git.BaseCommit != one || fam.Base != one {
		t.Fatalf("b's second bead starts from %+v, family %+v, want %s", second.Peer.Git, fam, one)
	}
	if b := second.Peer.Before; len(b) != 1 || b[0].Bead != first.ID || b[0].Outcome != "done" || b[0].ReturnedAt == nil || b[0].ClosedBy != "" {
		t.Fatalf("b's second bead names before it %+v", b)
	}
	if line := second.Peer.WasHere(); !strings.HasPrefix(line, "a's "+mine.ID+" was here before as "+first.ID+" (returned done ") || second.Peer.RefusedBefore() != "" {
		t.Fatalf("b's second bead says %q, %q", line, second.Peer.RefusedBefore())
	}
	// Listed, a bead does not carry them: they are read with the bead.
	if bs, err := b.api.ListBeads(context.Background(), store.BeadFilter{Label: "from-a"}); err != nil || len(bs) != 1 || bs[0].Peer == nil || bs[0].Peer.Before != nil {
		t.Fatalf("b's listed bead = %+v, %v", bs, err)
	}
	two := b.builds(t, bRepo, second)
	if gitIn(t, bRepo, "rev-parse", two+"^") != one {
		t.Fatal("b's second work does not start at what it returned first")
	}
	b.done(t, second.ID)
	b.pass(t)
	a.pass(t)
	got := a.get(t, mine.ID)
	if got.Stage != "review" || got.Hold || branchAt(t, aRepo, store.Branch(mine.ID)) != two {
		t.Fatalf("a's bead after the second return = %+v, %+v, branch at %s, want %s", got, got.Away(), branchAt(t, aRepo, store.Branch(mine.ID)), two)
	}
	if f := a.beadEvents(t, mine.ID, "peer_bead_fetched"); len(f) != 2 || f[1].Data["commit"] != two || len(a.beadEvents(t, mine.ID, "peer_bead_not_fetched")) != 0 {
		t.Fatalf("a's peer_bead_fetched = %+v", f)
	}
}

// With commits of this yard's own on the bead's branch since the other yard
// returned it, the bead is not sent again: that yard does not have them.
// The stage ends as refused, by the yard, with a note that says so, and
// nothing is queued. With the branch back where the return left it the bead
// is sent, from there, though the sending between ended without code.
func TestPeerBeadSentAgainWithTheBranchMovedOn(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	remote := sharedRemote(t)
	bRepo := b.clones(t, remote)
	b.takes(t, "a", "web")
	aRepo := a.sendsFrom(t, remote, "b", nil)
	a.sendsAgain(t)

	mine := a.bead(t, "Add the export")
	a.pass(t)
	first := b.from(t, "a")
	one := b.builds(t, bRepo, first)
	b.done(t, first.ID)
	b.pass(t)
	a.pass(t)
	branch := store.Branch(mine.ID)
	gitIn(t, aRepo, "checkout", "-q", branch)
	own := commitIn(t, aRepo, "review.txt", "a's own\n")
	gitIn(t, aRepo, "checkout", "-q", "main")

	a.again(t, mine.ID)
	queued := len(a.events(t, "peer_message_queued"))
	for i := 0; i < 2; i++ {
		a.pass(t)
		b.pass(t)
	}
	got := a.get(t, mine.ID)
	if got.Stage != "decide" || got.Hold || len(a.live(t)) != 0 {
		t.Fatalf("a's bead with its branch moved on = %+v", got)
	}
	if len(a.beadEvents(t, mine.ID, "peer_bead_sent")) != 1 || len(a.events(t, "peer_message_queued")) != queued {
		t.Fatalf("a queued a message for a bead it could not send: %+v", a.events(t, "peer_message_queued"))
	}
	if bs, err := b.api.ListBeads(context.Background(), store.BeadFilter{All: true}); err != nil || len(bs) != 1 {
		t.Fatalf("b has %d beads, want the first alone (%v)", len(bs), err)
	}
	ref := a.beadEvents(t, mine.ID, "peer_bead_refused")
	if len(ref) != 1 || ref[0].Actor != "yard" || !strings.Contains(ref[0].Data["reason"].(string), "its branch "+branch+" is at "+own[:12]+" here") ||
		!strings.Contains(ref[0].Data["reason"].(string), one[:12]) || ref[0].Data["sent"] != false {
		t.Fatalf("a's peer_bead_refused = %+v", ref)
	}
	adv := a.beadEvents(t, mine.ID, "advanced")
	if last := adv[len(adv)-1]; last.Actor != "yard" || last.Data["outcome"] != "refused" {
		t.Fatalf("a's last advanced = %+v", last)
	}
	ns := a.notes(t, mine.ID)
	if last := ns[len(ns)-1]; last.Author != "yard" || !strings.HasPrefix(last.Text, "This yard did not send this bead to b: its branch "+branch) || !strings.Contains(last.Text, "does not have them") {
		t.Fatalf("a's note of it = %+v", last)
	}
	// The words say it was not sent, not that it was at b and refused
	// there, and its row is still of the stay it had, with b's handle.
	away := got.Away()
	if line := away.Line(); !away.Unsent || !strings.HasPrefix(line, "not sent to b "+away.At.Local().Format("01-02 15:04")+": its branch "+branch) ||
		strings.Contains(line, "was at") || strings.Contains(line, "refused") {
		t.Fatalf("a's line of a bead not sent = %q", line)
	}
	rows := got.PeerLinks()
	if len(rows) != 1 || rows[0].Kind != store.LinkWasAt || rows[0].URL != first.ID+"@b" || rows[0].Until == nil || rows[0].Until.After(away.At) {
		t.Fatalf("a's rows of a bead not sent = %+v", rows)
	}
	if ls, err := a.api.IssueLinks(context.Background(), first.ID+"@b"); err != nil || len(ls) != 1 || ls[0].Bead != mine.ID {
		t.Fatalf("a's bead by b's handle = %+v, %v", ls, err)
	}
	if at := branchAt(t, aRepo, branch); at != own {
		t.Fatalf("a's branch was moved to %s", at)
	}

	// Back where the return left it: sent, and from there.
	gitIn(t, aRepo, "branch", "-f", branch, one)
	a.again(t, mine.ID)
	a.pass(t)
	sent := a.beadEvents(t, mine.ID, "peer_bead_sent")
	if len(sent) != 2 || sent[1].Data["base_commit"] != one {
		t.Fatalf("a's peer_bead_sent with the branch back = %+v", sent)
	}
	if second := b.from(t, "a"); second.Peer.Git == nil || second.Peer.Git.BaseCommit != one {
		t.Fatalf("b's second bead starts from %+v", second.Peer.Git)
	}
}

// A branch that holds nothing its base does not is no reason to refuse: the
// bead is sent as a first sending is, from the base.
func TestPeerBeadSentAgainFromTheBase(t *testing.T) {
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	remote := sharedRemote(t)
	bRepo := b.clones(t, remote)
	b.takes(t, "a", "web")
	aRepo := a.sendsFrom(t, remote, "b", nil)
	a.sendsAgain(t)

	mine := a.bead(t, "Add the export")
	a.pass(t)
	first := b.from(t, "a")
	b.builds(t, bRepo, first)
	b.done(t, first.ID)
	b.pass(t)
	a.pass(t)
	// The base moved on, and the bead's branch was made anew from it.
	base := commitIn(t, aRepo, "b.txt", "two\n")
	gitIn(t, aRepo, "push", "-q", "origin", "main")
	gitIn(t, aRepo, "branch", "-f", store.Branch(mine.ID), base)

	a.again(t, mine.ID)
	a.pass(t)
	sent := a.beadEvents(t, mine.ID, "peer_bead_sent")
	if len(sent) != 2 || sent[1].Data["base_commit"] != base || sent[1].Data["base"] != "main" {
		t.Fatalf("a's second peer_bead_sent = %+v, want the base %s", sent, base)
	}
}

// A bead someone of the working yard closed went back as refused. When it
// comes again, the new bead says so, with who closed the earlier one and
// why. Sent under another handle it says nothing: the handle is all the
// match goes by.
func TestPeerBeadRefusedBefore(t *testing.T) {
	ctx := context.Background()
	clock := newClock()
	a, b := newPeerYard(t, "a", clock), newPeerYard(t, "b", clock)
	link(t, a, b, true, true)
	a.sends(t, "b", nil)
	b.takes(t, "a", "web")

	mine := a.bead(t, "Add the export")
	a.pass(t)
	first := b.from(t, "a")
	_, err := b.api.CloseBead(ctx, first.ID, "not ours\nto build", store.Expect{}, "jo")
	must(t, err)
	b.pass(t)
	a.pass(t)
	if got := a.get(t, mine.ID); got.Stage != "decide" {
		t.Fatalf("a's bead after b closed its own = %+v", got)
	}
	a.again(t, mine.ID)
	a.pass(t)
	second := b.from(t, "a")
	bf := second.Peer.Before
	if second.ID == first.ID || len(bf) != 1 || bf[0].Bead != first.ID || bf[0].ClosedBy != "jo" || bf[0].Reason != "not ours to build" ||
		bf[0].Outcome != store.OutcomeRefused || bf[0].ReturnedAt == nil || second.Peer.WasHere() != "" {
		t.Fatalf("b's second bead names before it %+v, %q", bf, second.Peer.WasHere())
	}
	want := "this yard refused it before (" + first.ID + ", " + bf[0].ReturnedAt.Local().Format("01-02") + ", closed by jo: not ours to build)"
	if got := second.Peer.RefusedBefore(); got != want {
		t.Fatalf("b's second bead says %q, want %q", got, want)
	}
	// The earlier bead names nothing: what came after it is not before it.
	if e := b.get(t, first.ID); len(e.Peer.Before) != 0 {
		t.Fatalf("b's first bead names %+v", e.Peer.Before)
	}
	// The same text as a new bead of a's, so under another handle.
	other := a.bead(t, "Add the export")
	a.pass(t)
	bs, err := b.api.ListBeads(ctx, store.BeadFilter{Label: "from-a"})
	must(t, err)
	for _, l := range bs {
		if got := b.get(t, l.ID); got.Peer.Bead == other.ID && (len(got.Peer.Before) != 0 || got.Peer.RefusedBefore() != "") {
			t.Fatalf("a bead under another handle says %q", got.Peer.RefusedBefore())
		}
	}
	if len(bs) != 2 {
		t.Fatalf("b has %d open beads from a, want two", len(bs))
	}
}
