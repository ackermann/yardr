package store

import (
	"context"

	bolt "go.etcd.io/bbolt"

	"yardr.dev/yardr/internal/peer"
)

type callerKey struct{}

// WithCaller records on ctx the pid of the process a call came from (the
// peer of its unix socket connection, see rpc.ConnContext), so a bead
// write can be checked as the session whose pane it was made from (see
// Expect). A call made in the owning process has no caller.
func WithCaller(ctx context.Context, pid int) context.Context {
	return context.WithValue(ctx, callerKey{}, pid)
}

func callerOf(ctx context.Context) int {
	pid, _ := ctx.Value(callerKey{}).(int)
	return pid
}

// paneSession is the session whose pane the process pid runs in: the one
// whose handle names pid or a process above it. An exec runner's handle
// names its script's pid (pid, with its start time as start); a herdr
// runner's names the shell of the agent's tab (shell_pid, shell_start); a
// session whose runner recorded neither is not found this way. The pane
// counts only while the recorded process still runs, live session or
// ended (an agent kept open, or orphaned by a restart, after its bead
// moved on): a pid the kernel has since given to someone else makes nobody
// that session, even while the session is still live (its shell closed
// under it and the daemon has not noticed yet), so a person whose new
// shell got the pid is not checked as it. Only a pane whose start time was
// not recorded is trusted by pid alone, and only while its session lives.
// nil when pid is 0 or no session's pane holds it.
func paneSession(tx *bolt.Tx, pid int) (*Session, error) {
	if pid <= 0 {
		return nil, nil
	}
	// Every session's pane, by pid. Two sessions may name one pid (an ended
	// one's, reused): a live one wins, else the later started.
	type pane struct {
		sess  *Session
		start uint64
		known bool // start recorded
	}
	panes := map[int]pane{}
	err := each(tx, bSessions, "", func(_ []byte, sess *Session) error {
		for _, k := range [][2]string{{"pid", "start"}, {"shell_pid", "shell_start"}} {
			p, ok := pidIn(sess.Handle[k[0]])
			if !ok {
				continue
			}
			if prev, dup := panes[p]; dup && (prev.sess.live() || !sess.live() && prev.sess.StartedAt.After(sess.StartedAt)) {
				continue
			}
			start, known := uintIn(sess.Handle[k[1]])
			panes[p] = pane{sess: sess, start: start, known: known}
		}
		return nil
	})
	if err != nil || len(panes) == 0 {
		return nil, err
	}
	for _, p := range peer.Ancestors(pid) {
		pn, ok := panes[p]
		if ok && (pn.known && peer.Alive(p, pn.start) || !pn.known && pn.sess.live()) {
			return pn.sess, nil
		}
	}
	return nil, nil
}

// paneCrew is the crew member whose pane the process pid runs in: the
// one whose handle names the shell of its tab (shell_pid, with its start
// time as shell_start) as pid or a process above it, while that shell still
// runs. nil when pid is 0, and for a crew member whose runner recorded no
// shell: that one is known by the actor its commands name alone.
func paneCrew(tx *bolt.Tx, pid int) (*CrewMember, error) {
	if pid <= 0 {
		return nil, nil
	}
	panes := map[int]*CrewMember{}
	err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if p, ok := pidIn(r.Handle["shell_pid"]); ok && r.Live() {
			panes[p] = r
		}
		return nil
	})
	if err != nil || len(panes) == 0 {
		return nil, err
	}
	for _, p := range peer.Ancestors(pid) {
		r, ok := panes[p]
		if !ok {
			continue
		}
		if start, known := uintIn(r.Handle["shell_start"]); !known || peer.Alive(p, start) {
			return r, nil
		}
	}
	return nil, nil
}

// pidIn reads a pid from a handle, which JSON may have made a float.
func pidIn(v any) (int, bool) {
	n, ok := uintIn(v)
	return int(n), ok && n > 0
}

func uintIn(v any) (uint64, bool) {
	switch n := v.(type) {
	case int:
		return uint64(n), n >= 0
	case int64:
		return uint64(n), n >= 0
	case uint64:
		return n, true
	case float64:
		return uint64(n), n >= 0
	}
	return 0, false
}
