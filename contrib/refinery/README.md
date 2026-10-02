# contrib/refinery

Two shell scripts, each run as the `cmd` of an `exec` group with `--limit 1`
(one bead at a time). They are how rig yard lands work; the flow they sit in
is described in [docs/pipeline.md](../../docs/pipeline.md), sections 4 and
Epics. Install them from `main`, not a checkout, so a branch cannot change
how it is landed.

**merge-local** lands a bead's branch on its base (the rig's base, or the
epic's branch for a child of an epic). It is routed for `approved`. It
rebases `yard/<bead>` onto the base if the base moved; a conflict advances
the bead to `merge` with the conflicting files (or reports `failed` if the
flow has no `merge`). It then runs the rig's gate (`yardr rig check`, read from the
base) on exactly what will land: red reports `failed` (back to `new`, or
`open` for an epic), and a gate that could not run (exit 2) holds the bead.
Green fast-forwards the base, only ever by fast-forward, and never in a
dirty checkout (it holds instead). With `YARDR_PUSH` set it pushes the base,
never forced; a failed push mails `YARDR_NOTIFY` and leaves the bead merged.
Finally it advances the bead to `merged` with `merged <sha>`.

**integrate-epic** runs once an epic's last child has landed on its branch,
routed for the epic flow's `open` stage (type `epic`). It fetches the base
when it is a remote's, rebases `yard/<epic>` onto the rig's base and runs
the gate on it. Green pushes the rebased branch when `YARDR_PUSH` is set
(replacing the remote branch only if everything on it was in what it
rebased) and reports `done`, to review. Red adds a child in `backlog`, "Fix
the gate on epic <id>", which blocks the epic, and reports `failed`, back to
`open`. A rebase conflict, a gate that could not run, or a branch it cannot
work on (missing, dirty, nothing ahead of the base) holds the epic.

**Environment.** Both read what the exec runner sets: `YARDR_BEAD`,
`YARDR_RIG`, `YARDR_RIG_PATH`, `YARDR_BASE`, `YARDR_BRANCH` (default
`yard/<bead>`), `YARDR_WORKTREE`, `YARDR_BIN` and `YARDR_HOME` (used for the
default worktree path). Options go in the group's `cmd`: `YARDR_PUSH`, a
remote to push to (off when empty), and `YARDR_NOTIFY`, the resident mailed
when that push fails (default `mayor`).

**Setup** (from docs/pipeline.md):

```sh
install contrib/refinery/merge-local contrib/refinery/integrate-epic ~/.yardr/scripts/
yardr group add yard-refinery --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/merge-local' \
  --set log="$HOME/.yardr/logs/refinery.log"
yardr route add --stage approved --group yard-refinery --rig yard

yardr rig flow yard --type epic yard.epic
yardr group add yard-integrators --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/integrate-epic' \
  --set log="$HOME/.yardr/logs/integrator.log"
yardr route add --stage open --type epic --rig yard --group yard-integrators --priority 10
```
