package cli

import (
	"context"
	"fmt"
	"os"

	"yardr.dev/yardr/internal/config"
	"yardr.dev/yardr/internal/runner"
)

// identity is the session a command acts as: its actor, its session and
// the bead it was started for. Empty fields outside a session.
type identity struct {
	actor, session, bead string
}

// identity is who this run acts as: what the environment names
// (YARDR_ACTOR, YARDR_SESSION, YARDR_BEAD), unless that is another bead's
// session than the one whose worktree the command runs in. Then the
// environment is not this agent's own: an agent may run its commands in a
// process shared with other sessions, which keeps the environment of the
// session that started it (Codex's background server did, yardr-8bhk), and
// its notes would be another agent's and its moves refused as stale. The
// worktree is the bead's alone, so the session yardr last started in it
// (runner.SessionOf) is the one acting, and a line on stderr says so.
//
// Only another bead's environment gives way. One naming this bead is kept
// even when its session is not the worktree's: that is an earlier stage's
// agent left open, which must stay refused as stale. An environment naming
// no bead is a person's or a crew member's, who act as themselves wherever
// they stand.
func (a *app) identity() identity {
	env := identity{actor: config.Getenv("ACTOR"), session: config.Getenv("SESSION"), bead: config.Getenv("BEAD")}
	// Asked several times in a run (the actor, then the session): the
	// worktree is looked at, and the line written, once per environment.
	if a.self == nil || a.self.env != env {
		a.self = &resolved{env: env, as: a.worktreeIdentity(env)}
	}
	return a.self.as
}

// resolved is the identity a run acts as, for the environment it was
// worked out from.
type resolved struct {
	env, as identity
}

// worktreeIdentity is env, or the session of the worktree this run is in
// when env names another bead (see identity).
func (a *app) worktreeIdentity(env identity) identity {
	if env.bead == "" {
		return env
	}
	dir, err := os.Getwd()
	if err != nil {
		return env
	}
	ws := runner.SessionOf(context.Background(), dir)
	if ws == nil || ws.Bead == env.bead {
		return env
	}
	fmt.Fprintf(stderr, "yardr: the environment names %s (bead %s, session %s), but this is the worktree of %s: acting as %s (session %s). An agent whose shell is shared between sessions gets another's environment; run yardr from your own worktree, or name yourself: YARDR_ACTOR=%[5]s YARDR_SESSION=%[6]s YARDR_BEAD=%[4]s yardr ...\n",
		or(env.actor, "nobody"), env.bead, or(env.session, "none"), ws.Bead, ws.Actor, ws.Session)
	return identity{actor: ws.Actor, session: ws.Session, bead: ws.Bead}
}
