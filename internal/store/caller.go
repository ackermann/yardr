package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"

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

type sessionKey struct{}

// WithSession records on ctx the session the call says it is of
// ($YARDR_SESSION, sent with the call over the socket), so a call made
// before its session is on record can be refused as its session's (see
// startingRefused). A call made in the owning process names none.
func WithSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

func sessionOf(ctx context.Context) string {
	id, _ := ctx.Value(sessionKey{}).(string)
	return id
}

type unknownCallerKey struct{}

// WithUnknownCaller marks a socket call whose peer the OS could not
// identify. It is distinct from a call made in the owning process, which
// has no caller at all.
func WithUnknownCaller(ctx context.Context) context.Context {
	return context.WithValue(ctx, unknownCallerKey{}, true)
}

// startingRefused refuses a call from no known pane that names a session
// the store has no pane on record for yet: an exec session's process calls
// before the dispatcher records its handle (MarkRunning), and until then
// the pane checks would pass it on the actor's name alone. A manual
// session is a person's claim, and a session past starting has its pane
// recorded already: neither is refused here.
func startingRefused(tx *bolt.Tx, id string) error {
	if id == "" {
		return nil
	}
	sess := new(Session)
	ok, err := get(tx, bSessions, id, sess)
	if err != nil {
		return err
	}
	if ok && (sess.Manual() || sess.State != "starting") {
		return nil
	}
	return fmt.Errorf("the session %s is starting and not on record yet", id)
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
	for _, p := range peer.Ancestors(pid) {
		pn, err := paneOf(tx, p)
		if err != nil {
			return nil, err
		}
		if pn != nil && (pn.known && peer.Alive(p, pn.start) || !pn.known && pn.sess.live()) {
			return pn.sess, nil
		}
	}
	return nil, nil
}

// pane is a session's pane: the process its handle names, by its start
// time where the runner recorded one.
type pane struct {
	sess  *Session
	start uint64
	known bool // start recorded
}

// paneOf is the pane of the process p among the sessions whose handle names
// p, read through the pane index: the store keeps every session until it is
// pruned, and this runs inside every checked write. Two sessions may name
// one pid (an ended one's, reused): a live one wins, else the later
// started. nil when no session names p.
func paneOf(tx *bolt.Tx, p int) (*pane, error) {
	var found *pane
	for _, k := range keys(tx, bSessPane, strconv.Itoa(p)+"\x00") {
		sess := new(Session)
		if ok, err := get(tx, bSessions, k[strings.IndexByte(k, 0)+1:], sess); err != nil {
			return nil, err
		} else if !ok {
			continue
		}
		for _, h := range paneHandle {
			if q, ok := pidIn(sess.Handle[h[0]]); !ok || q != p {
				continue
			}
			if found != nil && (found.sess.live() || !sess.live() && found.sess.StartedAt.After(sess.StartedAt)) {
				continue
			}
			start, known := uintIn(sess.Handle[h[1]])
			found = &pane{sess: sess, start: start, known: known}
		}
	}
	return found, nil
}

// paneCrew is the crew member whose pane the process pid runs in: the
// one whose handle names the shell of its tab (shell_pid, with its start
// time as shell_start) as pid or a process above it, while that shell still
// runs. The pane of a session that is being started counts as that of a
// running one (CrewMember.Starting): its agent acts before the start is
// recorded. nil when pid is 0, and for a crew member whose runner recorded
// no shell: that one is known by the actor its commands name alone.
func paneCrew(tx *bolt.Tx, pid int) (*CrewMember, error) {
	r, _, err := paneCrewSession(tx, pid)
	return r, err
}

// paneCrewSession is paneCrew with the session: the crew member whose pane
// the process pid runs in, and the user of the conversation session whose
// pane it is ("" for the line session's). A journal, handoff or ask note
// written from a conversation's pane is tagged with it, so the handoff is
// read back, and the ask's answer routed, by that session.
func paneCrewSession(tx *bolt.Tx, pid int) (*CrewMember, string, error) {
	if pid <= 0 {
		return nil, "", nil
	}
	type crewPane struct {
		r      *CrewMember
		user   string
		handle map[string]any
	}
	// Two records may name one pid: a shell that is gone, and the shell
	// the kernel gave its pid to before the daemon noticed. Every
	// candidate is kept, so the stale one cannot hide the live one.
	panes := map[int][]crewPane{}
	err := each(tx, bCrew, "", func(_ []byte, r *CrewMember) error {
		if p, ok := pidIn(r.Handle["shell_pid"]); ok && r.Live() {
			panes[p] = append(panes[p], crewPane{r, "", r.Handle})
		}
		if p, ok := pidIn(r.Starting["shell_pid"]); ok {
			panes[p] = append(panes[p], crewPane{r, "", r.Starting})
		}
		for user, cs := range r.Conversations {
			if p, ok := pidIn(cs.Handle["shell_pid"]); ok && cs.Live() {
				panes[p] = append(panes[p], crewPane{r, user, cs.Handle})
			}
			if p, ok := pidIn(cs.Starting["shell_pid"]); ok {
				panes[p] = append(panes[p], crewPane{r, user, cs.Starting})
			}
		}
		return nil
	})
	if err != nil || len(panes) == 0 {
		return nil, "", err
	}
	for _, p := range peer.Ancestors(pid) {
		// A shell whose start time matches wins over one recorded
		// without, which is trusted by its pid alone.
		var unverified *crewPane
		for _, cp := range panes[p] {
			start, known := uintIn(cp.handle["shell_start"])
			if !known {
				if unverified == nil {
					cp := cp
					unverified = &cp
				}
				continue
			}
			if peer.Alive(p, start) {
				return cp.r, cp.user, nil
			}
		}
		if unverified != nil {
			return unverified.r, unverified.user, nil
		}
	}
	return nil, "", nil
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

// CallerKind is who a call over the socket is, as the store's records of
// panes and of the daemon tell it (CallerOf). rpc reads it to refuse a call
// before it reaches the store (rpc.Access); the store's own checks are read
// from the same records and stay where they are.
type CallerKind int

const (
	// CallerOwner is a call with no caller: the owning process or a direct
	// in-process call. The store serves it as its own (daemonOnly passes it
	// too).
	CallerOwner CallerKind = iota
	// CallerUnknown is a socket peer the OS could not identify. The RPC
	// layer lets it read but refuses its writes, since it cannot know
	// whether the call came from a bead's pane.
	CallerUnknown
	// CallerPerson is a process in no pane of the yard's: a person's
	// terminal, a script, a client that is not yardr.
	CallerPerson
	// CallerBead is the pane of a bead's agent: a session's (paneSession)
	// whose runner starts an agent, or a call that names such a session
	// still starting, which has no pane on record yet (startingRefused).
	CallerBead
	// CallerCrew is a crew member's pane (paneCrew).
	CallerCrew
	// CallerDaemon is the process that holds the daemon lock (daemonOnly).
	CallerDaemon
	// CallerScript is the process of an exec session: the command the
	// operator gave its group, run by the daemon as the group (the
	// assembly that lands a bead, a test, an install). It is the
	// operator's own program, not an agent, and is served as a person's
	// terminal is; the store's checks of a session's pane still hold it.
	CallerScript
)

func (k CallerKind) String() string {
	switch k {
	case CallerUnknown:
		return "an unidentified socket peer"
	case CallerPerson:
		return "a person"
	case CallerBead:
		return "the pane of a bead's session"
	case CallerCrew:
		return "a crew member's pane"
	case CallerDaemon:
		return "the daemon"
	case CallerScript:
		return "an exec session's command"
	}
	return "the yard's own process"
}

// CallerOf is the kind of caller ctx carries (WithCaller, WithSession).
// The daemon is asked first, since its process is in no pane; then a
// bead's session, as paneAuthor asks, so a session's agent is never taken
// for the crew member whose tab it may have been started from.
func (s *Store) CallerOf(ctx context.Context) (CallerKind, error) {
	if unknown, _ := ctx.Value(unknownCallerKey{}).(bool); unknown {
		return CallerUnknown, nil
	}
	pid := callerOf(ctx)
	if pid <= 0 {
		return CallerOwner, nil
	}
	if s.daemonOnly(ctx, "") == nil {
		return CallerDaemon, nil
	}
	var kind CallerKind
	err := s.read(func(tx *bolt.Tx) (err error) {
		kind, _, err = callerIn(tx, ctx)
		return err
	})
	return kind, err
}

// callerIn reads the store's records for what CallerOf answers once it has
// ruled out the owning process and the daemon: the kind of caller, and the
// session where the caller is a session's (a bead's agent, or an exec
// command). It runs inside a transaction, so a write asks the same question
// the socket does and the two cannot disagree about who a bead's agent is.
func callerIn(tx *bolt.Tx, ctx context.Context) (CallerKind, *Session, error) {
	pid := callerOf(ctx)
	if sess, err := paneSession(tx, pid); err != nil {
		return CallerPerson, nil, err
	} else if sess != nil && !sess.Manual() {
		return sessionCaller(sess), sess, nil
	}
	if r, err := paneCrew(tx, pid); err != nil {
		return CallerPerson, nil, err
	} else if r != nil {
		return CallerCrew, nil, nil
	}
	// A bead's agent may call before its pane is recorded; it says
	// which session it is, and that is believed when it restricts.
	if id := sessionOf(ctx); id != "" {
		sess := new(Session)
		if ok, err := get(tx, bSessions, id, sess); err != nil {
			return CallerPerson, nil, err
		} else if ok && !sess.Manual() && sess.State == "starting" {
			return sessionCaller(sess), sess, nil
		}
	}
	return CallerPerson, nil, nil
}

// beadAgent is the session of the bead's agent that made the call ctx
// carries: the one whose pane the caller runs in, or the starting one the
// call names (callerIn). It is nil for anyone else. An exec session's command
// and a call made in the owning process are served as a person is. A write
// asks it to hold an agent to a rule the socket's classes cannot state,
// because the rule turns on what the call asks for.
func beadAgent(tx *bolt.Tx, ctx context.Context) (*Session, error) {
	if callerOf(ctx) <= 0 {
		return nil, nil
	}
	kind, sess, err := callerIn(tx, ctx)
	if err != nil || kind != CallerBead {
		return nil, err
	}
	return sess, nil
}

// sessionCaller is the kind of a call from the session sess: its exec
// command's, or its agent's.
func sessionCaller(sess *Session) CallerKind {
	if sess.RunnerName() == "exec" {
		return CallerScript
	}
	return CallerBead
}
