package dispatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/hooks"
	"yardr.dev/yardr/internal/store"
)

// A hook with a map moves the issue a bead tracks when the bead enters a
// stage (docs/links.md). The store decides that as the bead's event is
// logged, and keeps the move (store.IssueMove) until it is made; making it
// is the daemon's, here, for three reasons. The call needs the credential of
// the resident the hook acts as, which is in its env_file or comes from its
// env_command: the daemon reads both for the panes it starts, the server
// holds no one's tokens. The call must be outside the store's write, and the daemon is what already
// calls a source between passes (GitHub, in github.go and ci.go) and waits
// out its outages. And every pass re-reads the moves from the store, so a
// daemon restarted, or replaced by a new build, between the event and the
// call carries on with no state of its own.
//
// A move is made at most once: it is gone from the store with the event that
// says how it ended. Should the daemon die between the source's answer and
// that write, the next pass reads the issue again, finds it at the status
// and logs the move as skipped: the issue is still changed once.

// issueTimeout bounds one call to a source from a pass, which holds up
// routing while it waits.
const issueTimeout = 30 * time.Second

// issues makes the issue moves that are due.
func (d *Dispatcher) issues(ctx context.Context) error {
	moves, err := d.Store.IssueMoves(ctx)
	if err != nil {
		return err
	}
	if d.issueWait == nil {
		d.issueWait = map[string]time.Time{}
	}
	for _, m := range moves {
		if m.RetryAt != nil && d.now().Before(*m.RetryAt) {
			continue
		}
		// A source that just failed one move is not asked for the next in
		// the same breath: each would wait out its own timeout.
		if until, ok := d.issueWait[m.Source]; ok && d.now().Before(until) {
			continue
		}
		d.issueMove(ctx, m)
	}
	return nil
}

// issueMove makes one move and records how it ended, or that it is to be
// tried again.
func (d *Dispatcher) issueMove(ctx context.Context, m store.IssueMove) {
	skip := func(reason, detail string, res hooks.Forwarded) {
		d.Log.Info("issue not moved", "bead", m.Bead, "hook", m.Hook, "issue", m.URL, "state", m.State, "reason", reason, "detail", detail)
		err := d.Store.IssueMoveSkipped(ctx, m.ID, store.IssueSkip{Reason: reason, Detail: detail,
			Status: res.Status, Title: res.Thread.Title, Thread: res.Thread.ID})
		if err != nil {
			d.Log.Warn("thread_state_skipped event", "move", m.ID, "err", err)
			d.issueWait[m.Source] = d.now().Add(d.Backoff) // the move stays: not asked again at once
		}
	}
	f, ok := hooks.ForwarderFor(m.Source)
	if !ok {
		skip(store.SkipFailed, fmt.Sprintf("yardr cannot move an issue in %s (it can in %s)", m.Source, strings.Join(hooks.Forwardable(), ", ")), hooks.Forwarded{})
		return
	}
	// The store is asked before the source is: a resident that may not set
	// a status there has nothing sent in its name.
	why, err := d.Store.IssueMoveAllowed(ctx, m.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) { // gone: its hook was removed meanwhile
			d.Log.Warn("issue move: asking the store", "move", m.ID, "err", err)
		}
		return
	}
	if why != "" {
		skip(store.SkipNoGrant, why, hooks.Forwarded{})
		return
	}
	thread := m.Thread
	if thread == "" {
		if thread, err = f.IssueThread(m.URL); err != nil {
			skip(store.SkipFailed, err.Error(), hooks.Forwarded{})
			return
		}
	}
	cred, err := d.credential(ctx, m.As, m.Source, f)
	if err != nil {
		d.issueFailed(ctx, m, err, skip)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, issueTimeout)
	res, err := f.Forward(cctx, thread, m.State, cred)
	cancel()
	switch {
	case hooks.Final(err):
		skip(store.SkipFailed, err.Error(), hooks.Forwarded{})
	case err != nil:
		d.issueWait[m.Source] = d.now().Add(d.Backoff)
		d.issueFailed(ctx, m, err, skip)
	case !res.Changed:
		skip(store.SkipPast, fmt.Sprintf("the issue is %s, which is at or past %s", res.Status, m.State), res)
	default:
		delete(d.issueWait, m.Source)
		d.Log.Info("issue moved", "bead", m.Bead, "hook", m.Hook, "issue", m.URL, "state", m.State, "status", res.Status, "was", res.Was)
		ts := store.ThreadState{Source: m.Source, Thread: res.Thread.ID, Title: res.Thread.Title, URL: m.URL,
			State: m.State, Status: res.Status, Was: res.Was, Instead: res.Instead}
		if err := d.Store.IssueMoved(ctx, m.ID, ts); err != nil {
			d.Log.Warn("thread_state event", "move", m.ID, "err", err)
			d.issueWait[m.Source] = d.now().Add(d.Backoff)
		}
	}
}

// issueFailed has a move that failed tried again, after a wait that doubles
// from Backoff up to BackoffMax, or gives it up when it has failed
// IssueTries times.
func (d *Dispatcher) issueFailed(ctx context.Context, m store.IssueMove, cause error, skip func(string, string, hooks.Forwarded)) {
	tries := m.Attempts + 1
	retry := d.now().Add(min(d.Backoff<<min(tries-1, 30), d.BackoffMax))
	if m.Attempts == 0 {
		d.Log.Warn("issue move failed; trying again", "bead", m.Bead, "hook", m.Hook, "issue", m.URL, "state", m.State, "err", cause)
	}
	// Recorded also when it is the last, so the event counts every try.
	if err := d.Store.IssueMoveFailed(ctx, m.ID, cause.Error(), retry); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.Log.Warn("issue move: recording the failure", "move", m.ID, "err", err)
		}
		return
	}
	if tries >= d.IssueTries {
		skip(store.SkipFailed, fmt.Sprintf("gave up after %d tries: %v", tries, cause), hooks.Forwarded{})
	}
}

// credential is the resident's credential for a source: what its env_file
// and env_command set for it (config.ReadResidentEnv), the one its own pane
// would use, asked for again at each move. Nothing else is tried: the
// daemon's environment, or its gh login, is the operator's.
func (d *Dispatcher) credential(ctx context.Context, resident, source string, f hooks.Forwarder) (string, error) {
	r, err := d.Store.GetResident(ctx, resident)
	if err != nil {
		return "", err
	}
	names := hooks.CredentialNames(source)
	env, err := config.ReadResidentEnv(ctx, r.Name, r.Dir, d.Home, r.Config)
	if !env.Set() {
		return "", fmt.Errorf("resident %s has no env_file or env_command, where its %s is read from", resident, names)
	}
	if err != nil {
		return "", err
	}
	cred, err := f.CredentialFrom(env.Get)
	if missing := (*hooks.MissingCredential)(nil); errors.As(err, &missing) {
		return "", fmt.Errorf("resident %s's %s does not set %s", resident, env.From(), names)
	}
	if err != nil {
		return "", fmt.Errorf("resident %s's %s: %w", resident, env.From(), err)
	}
	return cred, nil
}
