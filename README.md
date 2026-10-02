# yard

Route beads of work to whoever works them.

A **bead** is a durable unit of work. **Routes** send open, unblocked beads in
a **stage** to a **group**; a group's **runner** starts the work (an agent in
herdr, a shell command, or nobody — a human). Whoever works the bead advances
it to the next stage or closes it, and the next route takes over. Roles are
not built in: "builder" or "reviewer" is just a group plus a prompt file.

The store is SurrealDB. The daemon is woken by live queries, so nothing polls;
each wake-up re-reads state and acts idempotently, so a missed notification
only costs a delay and a restart is just another pass.

## Quick start

```sh
go build -o ~/.local/bin/yard .        # needs `surreal` (3.x) on PATH

yard rig add webapp ~/projects/webapp --base origin/main

# who does the work
yard group add webapp/builders  --runner herdr --limit 2 \
    --set kind=claude --set prompt=roles/builder.md
yard group add webapp/reviewers --runner herdr --set kind=codex --set prompt=roles/reviewer.md
yard group add webapp/merger    --runner exec  --set cmd='scripts/merge-bead.sh'
yard group add mckean           --runner manual

# which stage goes where
yard route add --stage new      --rig webapp --group webapp/builders
yard route add --stage review   --rig webapp --group webapp/reviewers
yard route add --stage approved --rig webapp --group webapp/merger
yard route add --stage blocked  --group mckean

yard daemon &                          # routes and supervises
yard watch                             # live event stream

yard bead create "Add login page" --rig webapp -t feature -b "Use the OAuth flow"
```

The SurrealDB server starts on first use (`~/.yard/data`, `127.0.0.1:19875`,
random root password in `~/.yard/credentials`). Override with `YARD_HOME`,
`YARD_ADDR`, `YARD_USER`/`YARD_PASS`.

## Rigs

A rig is a git repository beads are worked in. Register an existing clone,
or let yard clone it:

```sh
yard rig add webapp ~/projects/webapp                       # existing clone
yard rig add webapp --clone git@github.com:me/webapp.git    # → ~/.yard/rigs/webapp
yard rig add legacy ~/src/legacy --base origin/master       # base must resolve
```

Bead branches (`yard/<bead>`) start from the rig's base, fetched before each
new worktree. Give beads a rig with `--rig`; routes can match on it, and
group prompt paths are resolved inside it.

## Working a bead (humans and agents alike)

```sh
yard prime --bead webapp-3                  # bead, edges, notes
yard bead note webapp-3 "tests green: ..."  # evidence
yard bead advance webapp-3 --to review      # hand to the next stage
yard bead close webapp-3 --reason "merged"
yard bead hold webapp-3                     # stop routing; a human looks
```

Every command takes `--json`. Set `YARD_ACTOR` so the event log shows who
acted.

## Runners

| runner | starts | config |
|---|---|---|
| `manual` | nothing; the bead waits on the group | — |
| `exec` | `sh -c cmd` in the rig, with `YARD_BEAD`, `YARD_SESSION`, `YARD_STAGE`, `YARD_GROUP`, `YARD_ACTOR` | `cmd`, `dir`, `log` |
| `herdr` | a herdr worktree workspace on branch `yard/<bead>` at `~/.yard/worktrees/<rig>/<bead>` (reopened for later stages, one tab per stage), an agent in it, prompted with a brief in `<worktree>/.yard/` | `kind`, `prompt`, `args`, `keep`, `worktrees` |

A session ends when the bead is advanced, closed or released. If the runner
reports the work gone (process exited, herdr agent gone) the session is marked
`died` and the bead is routed again after a backoff; after 3 failures in one
stage the bead is held (`yard bead unhold` grants fresh attempts).

### herdr setup

- **Trust the worktree root once.** Claude Code (and similar agents) ask to
  trust every new folder but inherit trust from a parent. Run `claude` once in
  `~/.yard/worktrees` and accept, or every bead's agent stops at that prompt.
- **Decide what agents may run unattended.** Anything outside `args`'
  permissions blocks the agent on a prompt. Pass what the role needs, e.g.
  `--set 'args=["--permission-mode","acceptEdits","--allowedTools","Bash(yard:*),Bash(git:*)"]'`;
  compound shell commands still prompt unless allowed or auto mode is on.
- **Blocked agents are reported.** When herdr sees an agent waiting on a
  prompt, yard writes a `session_blocked` event (visible in `yard watch`);
  answer it in herdr and the session carries on.
- **Worktrees are cleaned up when the bead closes**, not when a stage ends,
  so later stages reuse them. A worktree with uncommitted or untracked changes
  is kept (a `workspace_kept` event says why) and `yard/<bead>` branches are
  never deleted. Set `--set cleanup=false` on a group to keep its worktrees.
  Reopening a bead checks its branch out again.
- The daemon subscribes to herdr's event stream, so an agent that exits is
  noticed within a second; the reconcile timer remains the fallback.

## BDP (Bead Protocol)

`yard bdp serve` serves the yard over the [BDP](https://github.com/gastownhall/bdp)
Read profile, so any BDP client reads yard's beads and dependency links over
HTTP:

```sh
yard bdp serve --addr :8765 --scope https://beads.example/team/
```

- Beads are `<scope>beads/<id>`; dependencies are Links whose source is the
  waiting bead and target its blocker, as in bd.
- Bead properties are bd's JSON row (dependencies, counts, `created_by`, …)
  plus yard's own fields; a held bead reads as `deferred`. Types use BDP's
  reference vocabulary (`https://work.example/types/task`, `…/blocks`) where
  names match, else `<scope>types/<name>`.
- Every real change mints an opaque revision (a no-op mints none), served
  as the ETag; listings page over one consistent snapshot.
- BDP's own conformance suite passes against it (31/31 runnable Read
  scenarios), and BDP's `bdp bd ready` client computes the same ready set
  from yard as `yard ready`. See [conformance/bdp](conformance/bdp).

## Layout

```
internal/store      SurrealDB schema and every query; events written in the same transaction
internal/dispatch   routing, claims, liveness, teardown (the only control loop)
internal/runner     manual, exec, herdr
internal/bdp        the BDP Read server (protocol rules, selector, paging)
internal/server     starts/stops the local surreal process
internal/cli        cobra commands
```

`go test ./...` starts a throwaway in-memory SurrealDB (skipped if `surreal`
is not installed).
