package store

import (
	"context"
	"fmt"
	"slices"
)

// What a bead's agent may not say on a pull request's bead. The contributors
// pack's pr flow lets the yard act on a pull request on the strength of a few
// records on its bead: the label merge-ok and the head commits a maintainer's
// yes, a reviewer, the yard's CI and a maintainer's acceptance wrote. The
// agent that reads a pull request's text is exactly the one a pull request
// would try to talk into writing them, so the checks here refuse a bead's
// agent (beadAgent) the writes that would make the yard trust a head nobody
// accepted, read, ran or merged, or post a review nobody wrote. Each runs
// inside UpdateBead's write, and the creation of a bead is refused them in
// checkAgentCreate.

// labelMergeOK says the yard may merge the pull request a pr bead tracks
// (the contributors pack's approved stage, merge-pr); metaMergeHead is the
// head commit a maintainer's yes recorded for it.
const (
	labelMergeOK  = "merge-ok"
	metaMergeHead = "merge_head"
)

// checkMergeOK refuses a bead's agent (beadAgent) a patch that records
// merge_head, on any bead, its own included, and one that grants or takes the
// merge-ok label on a bead its session does not own. merge_head is the commit
// a maintainer said yes to, so it is a person's to write; the reviewer's part
// is the label, for a bot's bump, which merge-pr merges at the head it has.
// The label is the whole of the permission to merge the pull request, so an
// agent that could say it on another bead could have the yard merge a pull
// request nobody cleared. check alone lets a pane write any bead no session
// owns, which a pull request waiting at approved is.
func (w *wtx) checkMergeOK(ctx context.Context, b *beadRow, p BeadPatch) error {
	_, yes := p.Metadata[metaMergeHead]
	if !yes && !slices.Contains(p.AddLabels, labelMergeOK) && !slices.Contains(p.RemoveLabel, labelMergeOK) {
		return nil
	}
	sess, err := beadAgent(w.Tx, ctx)
	if err != nil || sess == nil || (sess.Bead == b.UUID && !yes) {
		return err
	}
	return fmt.Errorf("bead %s: the session %s of a bead's agent may say merge-ok only on its own bead %s and may not record merge_head on any, whatever actor the command names; the reviewer says merge-ok on the pull request it read, and a maintainer's yes is theirs: yardr bead note %s - with the words on stdin (why it is merge-ok)",
		b.ID, sess.ID, handlesIn(w.Tx).of(sess.Bead), b.ID)
}

// metaReviewedHead is the commit a reviewer read, as the reviewer records it
// on the pull request's bead before it reports done (the contributors pack's
// review stage): pr-ci runs the depot's tests on the head the bead names and
// on no other.
const metaReviewedHead = "reviewed_head"

// checkReviewedHead refuses a bead's agent (beadAgent) a patch that records
// reviewed_head on a bead its session does not own, as checkMergeOK does for
// merge-ok. pr-ci runs a pull request's code only on the head the bead says
// was read, so an agent that could say it on another bead could have the yard
// run a head nobody read, as the operator. On its own bead the agent may: what
// the field answers is a push between the read and the run, not a reviewer
// that lies about what it read. A person may write it anywhere, a maintainer
// who read the head themselves included.
func (w *wtx) checkReviewedHead(ctx context.Context, b *beadRow, p BeadPatch) error {
	if _, ok := p.Metadata[metaReviewedHead]; !ok {
		return nil
	}
	sess, err := beadAgent(w.Tx, ctx)
	if err != nil || sess == nil || sess.Bead == b.UUID {
		return err
	}
	return fmt.Errorf("bead %s: the session %s of a bead's agent may record reviewed_head only on its own bead %s, whatever actor the command names; it says which head the reviewer read, and the reviewer records it on the pull request it read: yardr bead note %s - with the words on stdin (which head should be read)",
		b.ID, sess.ID, handlesIn(w.Tx).of(sess.Bead), b.ID)
}

// metaReview is the review a reviewer stored on the bead it read, as the
// public line's reviewers do (the flow yardr.public): post-review posts it
// on the bead's pull request as the yard's bot and sends the bead on the way
// its outcome says, giving merge-ok with an approval.
const metaReview = "review"

// checkReview refuses a bead's agent (beadAgent) a patch that records review
// on a bead its session does not own, as checkReviewedHead does for
// reviewed_head. The stored review is posted in public as the yard's bot and
// decides the bead's way on, so an agent that could say it on another bead
// could put its own words on another bead's pull request and have the yard
// merge a head its reviewers rejected. On its own bead the agent may: the
// review is the reviewer's own to write. A person may write it anywhere, and
// the posting script empties it once posted, as no bead's agent.
func (w *wtx) checkReview(ctx context.Context, b *beadRow, p BeadPatch) error {
	if _, ok := p.Metadata[metaReview]; !ok {
		return nil
	}
	sess, err := beadAgent(w.Tx, ctx)
	if err != nil || sess == nil || sess.Bead == b.UUID {
		return err
	}
	return fmt.Errorf("bead %s: the session %s of a bead's agent may record review only on its own bead %s, whatever actor the command names; it is the review the yard posts on the pull request it read, and the reviewer stores it on the pull request it read: yardr bead note %s - with the words on stdin (which review should be stored)",
		b.ID, sess.ID, handlesIn(w.Tx).of(sess.Bead), b.ID)
}

// checkReviewRecords runs the two checks on what a reviewer records of the
// pull request it read: the head (checkReviewedHead) and the review of it
// (checkReview), which the reviewer writes in one update.
func (w *wtx) checkReviewRecords(ctx context.Context, b *beadRow, p BeadPatch) error {
	if err := w.checkReviewedHead(ctx, b, p); err != nil {
		return err
	}
	return w.checkReview(ctx, b, p)
}

// metaCIHead is the commit the yard's own CI passed on, as the script that runs
// the depot's tests on a pull request's head records it (the contributors
// pack's ciwait stage, pr-ci), and as merge-pr takes it: the whole of the
// pull request's CI.
const metaCIHead = "ci_head"

// checkCIHead refuses a bead's agent (beadAgent) a patch that records ci_head,
// on any bead, its own included. merge-pr merges on ci_head as it finds it, so
// an agent that could write it could have the yard merge a pull request nothing
// ran the tests of, and the agent that reads a pull request's text is exactly
// the one a pull request would try to talk into it. The script that ran the
// tests writes it, as a person may.
func (w *wtx) checkCIHead(ctx context.Context, b *beadRow, p BeadPatch) error {
	if _, ok := p.Metadata[metaCIHead]; !ok {
		return nil
	}
	sess, err := beadAgent(w.Tx, ctx)
	if err != nil || sess == nil {
		return err
	}
	return fmt.Errorf("bead %s: the session %s of a bead's agent may not record ci_head, whatever actor the command names; it says which head the yard's CI passed on, and the script that ran the tests writes it: yardr bead note %s - with the words on stdin (why CI should run again)",
		b.ID, sess.ID, b.ID)
}

// metaAcceptedHead is the commit a maintainer accepted a pull request at, as
// the dispatcher records it when the source says accepted (the contributors
// pack's triage stage) and as a maintainer records it by hand: pr-ci runs the
// depot's tests on the head of a pull request that waited in triage only when
// it is this commit.
const metaAcceptedHead = "accepted_head"

// checkAcceptedHead refuses a bead's agent (beadAgent) a patch that records
// accepted_head, on any bead, its own included. It says which commit a person
// judged before the yard ran any of it, so an agent that could write it could
// make the head it was handed the accepted one, and the agent that reads a
// pull request's text is exactly the one a pull request would try to talk into
// it. The dispatcher records it for the source's word, and a person may.
func (w *wtx) checkAcceptedHead(ctx context.Context, b *beadRow, p BeadPatch) error {
	if _, ok := p.Metadata[metaAcceptedHead]; !ok {
		return nil
	}
	sess, err := beadAgent(w.Tx, ctx)
	if err != nil || sess == nil {
		return err
	}
	return fmt.Errorf("bead %s: the session %s of a bead's agent may not record accepted_head, whatever actor the command names; it says which head a maintainer accepted, and the maintainer writes it: yardr bead note %s - with the words on stdin (which head should be accepted)",
		b.ID, sess.ID, b.ID)
}
