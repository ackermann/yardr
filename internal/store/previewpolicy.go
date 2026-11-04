package store

import (
	"context"
	"fmt"

	bolt "go.etcd.io/bbolt"
)

// The two gates of a preview that tell who the caller is: who may stop one,
// and who may watch a bead's files. Approving, refusing and forgetting a
// preview are the operator's alone (ruleOperatorRequired), and need no rule
// of their own.

// mayStopPreview lets a preview's proposer or a person of the yard stop it.
// The proposer is the actor stored with the proposal, not a role the caller
// may choose by naming another actor.
func mayStopPreview(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	if obj.Whose != "" && resolved == obj.Whose {
		return nil
	}
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if ops.is(resolved) {
		return nil
	}
	who := ops.words()
	if ops.none() {
		who = "the operator"
	}
	return fmt.Errorf("only %s or %s may stop preview %s; this is %s", obj.Whose, who, obj.Name, or(resolved, "nobody"))
}

// mayWatchPreview lets an operator, the originator of the bead Object.Name
// (Object.Whose), or an agent working that bead start a watch of its files.
// The agent is told by the store's own records of the calling pane, not by
// the actor it names, so no pane can serve the files of a bead it does not
// work.
func mayWatchPreview(ctx context.Context, tx *bolt.Tx, actor string, _ Action, obj Object) error {
	resolved, err := paneAuthor(tx, callerOf(ctx), actor)
	if err != nil {
		return err
	}
	ops, err := operatorsIn(tx)
	if err != nil {
		return err
	}
	if ops.is(resolved) || obj.Whose != "" && resolved == obj.Whose {
		return nil
	}
	sess, err := paneSession(tx, callerOf(ctx))
	if err != nil {
		return err
	}
	if sess != nil && !sess.Manual() && obj.row != nil && sess.Bead == obj.row.UUID {
		return nil
	}
	who := ops.words()
	if ops.none() {
		who = "the operator"
	}
	return fmt.Errorf("only an agent working %s, the user it is for or %s may watch its files; this is %s",
		obj.Name, who, or(resolved, "nobody"))
}
