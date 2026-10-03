# contrib/refinery

Four shell scripts, each run as the `cmd` of an `exec` group with `--limit 1`
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
Green fast-forwards the base to the commit the gate ran on, by its sha,
only ever by fast-forward, and never in a dirty checkout (it holds
instead); a branch that moved while the gate ran reports `failed` with
both commits, so nothing ungated lands. With `YARDR_PUSH` set it pushes the base,
never forced; a failed push mails `YARDR_NOTIFY` and leaves the bead merged.
Finally it advances the bead to `merged` with `merged <sha>`.

**integrate-epic** runs once an epic's last child has landed on its branch,
routed for the epic flow's `open` stage (type `epic`). It fetches the base
when it is a remote's, rebases `yard/<epic>` onto the rig's base and runs
the gate on it. Green pushes the rebased branch when `YARDR_PUSH` is set
(replacing the remote branch only if everything on it was in what it
rebased, or in a tip a note on the epic records as `old tip: <sha>`, which
its own conflict note and the merger's note both do: the merger rebases
without pushing, so the remote still has the tip it started from) and
reports `done`, to review. Red adds a child in `backlog`, "Fix
the gate on epic <id>", which blocks the epic, and reports `failed`, back to
`open`. A rebase conflict aborts the rebase, reports `conflict` to `merge`
with the conflicting files and both tips, and leaves the branch for the
mergers; after they report done, the epic returns to `open` for integration.
A gate that could not run, or a branch it cannot work on (missing, dirty,
nothing ahead of the base), holds the epic.

With `YARDR_PR=1` (and `YARDR_PUSH`), integrate-epic also opens a pull
request for the pushed branch against the base, or replaces the body of the
open one the epic is bound to: the epic's body, a "Children" list and the
trailer `Epic: <id>`. It binds the epic to that pull request (`--meta
pr_url=<url>`, `pr_number`, and `pr_head_repo`, the repository its head is
in) and puts the URL in the note. No script finds a PR by the branch's
name, which a fork's can carry: one open for the branch that the epic is
not bound to holds the epic. A push or `gh` that fails reports `failed`, back to `open`, so the
next integration retries; the daemon waits longer before each retry in a
row (docs/pipeline.md, "The epic's own flow").

The pull-request path is experimental and off by default: an epic lands
through merge-local unless `YARDR_PR=1` is in the cmd of integrate-epic,
pr-epic and pr-merge alike. Without it pr-epic and pr-merge hold the epic
with a note naming the switch and touch neither the remote nor GitHub.
docs/pipeline.md, "Through a pull request", lists what the path does not
yet guarantee (the first green workflow counts as CI green).

**pr-epic** is routed for the epic flow's `approved` stage (type `epic`)
when epics land through pull requests. It pushes `yard/<epic>` with a lease
(it may carry the reviewer's commits; a remote branch with commits of its
own is not overwritten), replaces the PR's body with the latest review note
under "Review", stores the commit it pushed in the epic's metadata as
`pr_reviewed_sha`, and reports `done`, to `in-pr`. No bound PR, a refused push
or a PR GitHub reports as conflicting reports `failed`, to `open`.

**pr-merge** is routed for `land` (type `epic`). It runs `gh pr merge
<pr_url> --rebase --delete-branch --match-head-commit <pr_reviewed_sha>`,
fetches the remote, fast-forwards the base in the rig's checkout and
reports `done`, to `merged`. Only the reviewed commit is merged: a PR whose
head moved after the review holds the epic, unmerged, and an epic with no
`pr_reviewed_sha` reports `failed`, to `open`. A merge GitHub refuses for
another reason reports `failed`, to `open`, with gh's message; it is not
retried.
A rig checkout that is not on the base or not clean is held before merging.
Both read the PR the epic is bound to and hold the epic, touching nothing,
when its number, head repository or head branch on GitHub differ from the
binding.
Both need `gh`, logged in as the operator, and `jq` on PATH.

**Environment.** All four read what the exec runner sets: `YARDR_BEAD`,
`YARDR_RIG`, `YARDR_RIG_PATH`, `YARDR_BASE`, `YARDR_BRANCH` (default
`yard/<bead>`), `YARDR_WORKTREE`, `YARDR_BIN` and `YARDR_HOME` (used for the
default worktree path). Options go in the group's `cmd`: `YARDR_PUSH`, a
remote to push to (off when empty; pr-epic and pr-merge default to
`origin`), `YARDR_NOTIFY`, the resident mailed when that push fails (default
`mayor`), and `YARDR_PR=1`, the switch for the pull-request path, read by
integrate-epic, pr-epic and pr-merge.

**Setup** (from docs/pipeline.md):

```sh
install contrib/refinery/merge-local contrib/refinery/integrate-epic ~/.yardr/scripts/
yardr group add yard-refinery --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/merge-local' \
  --set log="$HOME/.yardr/logs/refinery.log"
yardr route add --stage approved --group yard-refinery --rig yard

yardr rig flow yardr --type epic yardr.epic                  # the repo's .yardr/flows/yardr.epic.flow
yardr group add yard-integrators --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/integrate-epic' \
  --set log="$HOME/.yardr/logs/integrator.log"
yardr route add --stage open --type epic --rig yard --group yard-integrators --priority 10
```

Landing epics through pull requests adds the groups `yard-pr` and
`yard-pr-merge`, routed for `approved` and `land` (type `epic`), with
`YARDR_PR=1` in their cmd and the integrators'; docs/pipeline.md, "Through a pull request", has
the commands.
