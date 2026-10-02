# Groups and runners

A group is who works a stage's beads: a pool of agents, a code step, or
humans. Its runner says how work starts; routes (`yardr route add`) send a
stage's beads to it. What a group's settings are, and where they are kept,
is in [configuration.md](configuration.md).

## Groups, interactively

`yardr group add` with no name opens a form: pick an existing group to edit or
create one, choose the runner, and for herdr the agent (Claude Code, Codex,
Grok, Kimi, opencode, pi, or any other herdr agent), a model read live from
that agent (press `/` to search long lists), and its permission flags. With a
name, `yardr group add <name> --runner … --set …` stays scriptable.

## Runners

| runner | starts | config |
|---|---|---|
| `manual` | nothing; the bead waits on the group | — |
| `exec` | `sh -c cmd` in the rig, with `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP`, `YARDR_ACTOR` (the group); for a bead with a rig also `YARDR_RIG`, `YARDR_RIG_PATH`, `YARDR_BASE` (what the bead lands on: the rig's base, or its epic's branch, see [pipeline.md](pipeline.md#epics)), `YARDR_BRANCH` (`yard/<bead>`), `YARDR_WORKTREE` (where herdr groups check that branch out); and `YARDR_BIN`, `YARDR_HOME` for the script's own `yardr` commands. A refinery is one ([pipeline.md](pipeline.md#4-approved-the-refinery-lands-it-on-main)) | `cmd`, `dir`, `log`, `worktrees` |
| `herdr` | a herdr worktree workspace on branch `yard/<bead>` (from the bead's base) at `~/.yardr/worktrees/<rig>/<bead>` (reopened for later stages, one tab per stage), an agent in it, in a tab of its own with `YARDR_ACTOR` (the agent), `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP`, `YARDR_BASE` (what it branches from: the rig's base, or its epic's branch), `YARDR_HOME` and `YARDR_BIN` in its environment, prompted with a brief in the worktree's git dir (`git rev-parse --git-path yardr`) | `kind`, `prompt`, `args`, `keep`, `worktrees`, `idle`, `max_age`, `grace` |

A session ends when the bead is advanced, closed or released. If the runner
reports the work gone (process exited, herdr agent gone) the session is marked
`died` and the bead is routed again after a backoff; after 3 failures in one
stage the bead is held (`yardr bead unhold` grants fresh attempts).
A runner that cannot start anything (herdr missing or down) is an outage, not
a failure of the bead: the group backs off and logs `runtime_down` once.
Groups can also limit how long a session sits idle or runs (`idle`,
`max_age`, `grace`; [configuration.md](configuration.md#session-limits-and-outages)).

### herdr setup

- **Folder trust.** Claude Code asks to trust each new project, and every bead's
  worktree is its own git root, so trusting a parent folder does not cover them:
  each builder stops at the trust dialog until someone answers it in its pane.
  yardr keeps such a session (`session_blocked` in `yardr watch`) and sends its
  first prompt once it is answered. Starting without the dialog is yard-16.
- **Decide what agents may run unattended.** Anything outside `args`'
  permissions blocks the agent on a prompt. Pass what the role needs, e.g.
  `--set 'args=["--permission-mode","acceptEdits","--allowedTools","Bash(yardr:*),Bash(git:*)"]'`;
  compound shell commands still prompt unless allowed or auto mode is on.
- **Blocked agents are reported.** When herdr sees an agent waiting on a
  prompt, yardr writes a `session_blocked` event (visible in `yardr watch`);
  answer it in herdr and the session carries on.
- **Worktrees are cleaned up when the bead closes**, not when a stage ends,
  so later stages reuse them. A worktree with uncommitted or untracked changes
  is kept (a `workspace_kept` event says why) and `yard/<bead>` branches are
  never deleted. Set `--set cleanup=false` on a group to keep its worktrees.
  Reopening a bead checks its branch out again.
- The daemon subscribes to herdr's event stream, so an agent that exits is
  noticed within a second; the reconcile timer remains the fallback.
