# contrib/assembly

Four shell scripts, each run as the `cmd` of an `exec` group with `--limit 1`
(one bead at a time). They are how depot yard lands work; the flow they sit in
is described in [docs/pipeline.md](../../docs/pipeline.md), sections 4 and
Trains. Install them from `main`, not a checkout, so a branch cannot change
how it is landed. merge-local is also built into yardr: `yardr setup`
writes it to `~/.yardr/scripts/` for a yard set up without a checkout
(docs/setup.md).

Until this release the directory was `contrib/refinery`. The scripts are
still there under their old paths, each running the one here, so a group
whose `cmd` names one keeps working; the next release drops them
(docs/configuration.md, "Upgrading").

**merge-local** lands a bead's branch on its base (the depot's base, or the
train's branch for a child of a train). It is routed for `approved`. It
rebases `yard/<bead>` onto the base if the base moved; a conflict advances
the bead to `merge` with the conflicting files (or reports `failed` if the
flow has no `merge`). It then runs the depot's gate (`yardr depot check`, read from the
base) on exactly what will land: red reports `failed` (back to `new`, or
`open` for a train), and a gate that could not run (exit 2) holds the bead.
The note of a red gate starts with the branch, the commit, the base and the
file `depot check` kept the whole output in (under the yard's home,
`gates/<depot>/`); then the lines matching the depot's
`.yardr/check-fail-pattern`, if it has one, and the last 200 lines.
The gate waits for one of the machine's gate slots (`yardr depot check
--help`) ahead of the others waiting (`YARDR_GATE_FIRST=1`, as
integrate-train's does); none within depot check's bound is exit 2 as well.
A gate ended by a signal from outside (exit 128+N, e.g. 143 for SIGTERM) is
neither: it is run once more on the same commit, with a note of the first
run's output, and held with both outputs if a signal ends it again. The
gate's own kill at its deadline exits 1, so it is red.
Green fast-forwards the base to the commit the gate ran on, by its sha,
only ever by fast-forward, and never in a dirty checkout (it holds
instead); a branch that moved while the gate ran reports `failed` with
both commits, so nothing ungated lands. With `YARDR_PUSH` set it pushes the base,
never forced; a failed push mails `YARDR_NOTIFY` and leaves the bead merged.
Finally it advances the bead to `merged` with `merged <sha>`.

A car of a train (`YARDR_TYPE=car`, `YARDR_TRAIN` set, and that train's
branch as `YARDR_BASE`) is gated with `yardr depot check --light`, the depot's
light gate; the full gate is integrate-train's, on the whole train. Everything
else gets the full gate, a car with no train included. See
[docs/pipeline.md](../../docs/pipeline.md#cars).

**integrate-train** runs once a train's last child has landed on its branch,
routed for the train flow's `open` stage (type `train`). It fetches the base
when it is a remote's, rebases `yard/<train>` onto the depot's base and runs
the gate on it. Green pushes the rebased branch when `YARDR_PUSH` is set
(replacing the remote branch only if everything on it was in what it
rebased, or in a tip a note on the train records as `old tip: <sha>`, which
its own conflict note and the merger's note both do: the merger rebases
without pushing, so the remote still has the tip it started from) and
reports `done`, to review. Red adds a child in `backlog`, "Fix
the gate on train <id>", which blocks the train, and reports `failed`, back to
`open`; the child's body and the train's note start as merge-local's red
note does, with the commit and the file the whole output is in. A rebase conflict aborts the rebase, reports `conflict` to `merge`
with the conflicting files and both tips, and leaves the branch for the
mergers; after they report done, the train returns to `open` for integration.
A gate that could not run, or a branch it cannot work on (missing, dirty,
nothing ahead of the base), holds the train. A gate ended by a signal is run
once more, as in merge-local, and holds the train the second time.

With `YARDR_PR=1` (and `YARDR_PUSH`), integrate-train also opens a pull
request for the pushed branch against the base, or replaces the body of the
open one the train is bound to: the train's body, a "Children" list and the
trailer `Train: <id>`. It binds the train to that pull request (`--meta
pr_url=<url>`, `pr_number`, and `pr_head_repo`, the repository its head is
in) and puts the URL in the note. No script finds a PR by the branch's
name, which a fork's can carry: one open for the branch that the train is
not bound to holds the train. A push or `gh` that fails reports `failed`, back to `open`, so the
next integration retries; the daemon waits longer before each retry in a
row (docs/pipeline.md, "The train's own flow").

The pull-request path is experimental and off by default: a train lands
through merge-local unless `YARDR_PR=1` is in the cmd of integrate-train,
pr-train and pr-merge alike. Without it pr-train and pr-merge hold the train
with a note naming the switch and touch neither the remote nor GitHub.
docs/pipeline.md, "Through a pull request", lists its known limits (the
daemon lands a train on the base branch's required checks only).

**pr-train** is routed for the train flow's `approved` stage (type `train`)
when trains land through pull requests. It pushes `yard/<train>` with a lease
(it may carry the reviewer's commits; a remote branch with commits of its
own is not overwritten), replaces the PR's body with the latest review note
under "Review", stores the commit it pushed in the train's metadata as
`pr_reviewed_sha`, and reports `done`, to `in-pr`. No bound PR, a refused push
or a PR GitHub reports as conflicting reports `failed`, to `open`.

**pr-merge** is routed for `land` (type `train`). It runs `gh pr merge
<pr_url> --rebase --delete-branch --match-head-commit <pr_reviewed_sha>`,
fetches the remote, fast-forwards the base in the depot's checkout and
reports `done`, to `merged`. Only the reviewed commit is merged: a PR whose
head moved after the review holds the train, unmerged, and a train with no
`pr_reviewed_sha` reports `failed`, to `open`. A merge GitHub refuses for
another reason reports `failed`, to `open`, with gh's message; it is not
retried.
A depot checkout that is not on the base or not clean is held before merging.
Both read the PR the train is bound to and hold the train, touching nothing,
when its number, head repository or head branch on GitHub differ from the
binding.
Both need `gh`, logged in as the operator, and `jq` on PATH.

**Environment.** All four read what the exec runner sets: `YARDR_BEAD`,
`YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_BASE`, `YARDR_BRANCH` (default
`yard/<bead>`), `YARDR_WORKTREE`, `YARDR_BIN` and `YARDR_HOME` (used for the
default worktree path); merge-local also `YARDR_TYPE` and `YARDR_TRAIN`.
Options go in the group's `cmd`: `YARDR_PUSH`, a remote to push to (off when
empty; pr-train and pr-merge default to `origin`), `YARDR_NOTIFY`, the crew member
mailed when that push fails (default the yard's `yardmaster` setting, else
`yardmaster`), and `YARDR_PR=1`, the switch for the pull-request path, read by
integrate-train, pr-train and pr-merge.

**Setup** (from docs/pipeline.md):

```sh
install contrib/assembly/merge-local contrib/assembly/integrate-train ~/.yardr/scripts/
yardr group add yard-assembly --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/merge-local' \
  --set log="$HOME/.yardr/logs/assembly.log"
yardr route add --stage approved --group yard-assembly --depot yard

yardr depot flow yardr --type train yardr.train                  # the repo's .yardr/flows/yardr.train.flow
yardr group add yard-integrators --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/integrate-train' \
  --set log="$HOME/.yardr/logs/integrator.log"
yardr route add --stage open --type train --depot yard --group yard-integrators --priority 10
```

Landing trains through pull requests adds the groups `yard-pr` and
`yard-pr-merge`, routed for `approved` and `land` (type `train`), with
`YARDR_PR=1` in their cmd and the integrators'; docs/pipeline.md, "Through a pull request", has
the commands.
