# contrib/assembly

Five shell scripts, each run as the `cmd` of an `exec` group with `--limit 1`
(one bead at a time). They are how depot yard lands work; the flow they sit in
is described in [docs/line.md](../../docs/line.md), sections 4 and
Trains. The daemon stops a script as soon as it has reported, so each takes
away what it made (the files of a test run, a directory to work in) and stops
what it started (the test command and every process below it) before it
reports; a script stopped sooner, by TERM or INT, does the same before it
exits. Install them from `main`, not a checkout, so a branch cannot change
how it is landed. The starter pack carries a copy of merge-local: a yard set
up from it (`yardr setup`, `yardr depot init`) runs
`~/.yardr/packs/starter/scripts/merge-local`, with no checkout of yardr
(docs/setup.md).

**merge-local** lands a bead's branch on its base (the depot's base, or the
train's branch for a child of a train). It is routed for `approved`. With
`YARDR_PUSH` set it first brings the base up to that remote, fast-forward
only; a base that diverged from the remote's branch reports `failed` with a
note saying so, never merged or forced. It then
rebases `yard/<bead>` onto the base if the base moved; a conflict goes to
the mergers with the conflicting files: it reports `conflict` where the flow
offers that outcome, else advances to `merge` where the flow allows it, else
reports `failed` (back to `new`). It asks the flow first (`prime --bead
--json`), so a flow with no `merge` raises no refused move. With
`YARDR_LAND=squash` (`rebase` is the default, and lands the branch's commits
as they are) it then makes the branch one commit, so that the commit the tests
run on is the commit that lands: a Conventional Commit written from the bead,
`<type>[(<scope>)][!]: <bead title> (<bead id>)`, then the branch's messages
without their trailers, then the `BREAKING CHANGE:` lines the branch carried,
a `Refs:` line for each issue the bead is linked to, `Yardr-Bead:` and the
distinct `Yardr-Agent:` values (docs/line.md, "Landing by squash", has the
type, the scope and the rest). Every type of bead is squashed but a train,
which lands by rebase in either mode so that the base gets the commit of each
child. Any other value of `YARDR_LAND` holds the bead before anything moves.
It then runs the
depot's test command (`YARDR_TEST`,
from `yardr depot add --test`) in the worktree, on exactly what will land,
with `YARDR_LANDING=1` set: for depot yardr that run is the selection
plus the six black-box suites, the whole suite running on a quiet
machine instead (docs/line.md, "The landing run, and the whole suite
on a quiet machine"); what it means is the depot command's own. Red
reports `failed` (back to `new`, or `open`
for a train), and a command that says it could not run (exit 2) holds the
bead; one that exits 2 for a failing test, as make does, is wrapped
(`--test 'make test || exit 1'`). Where the command is the path of a file
the base has (`--test .yardr/test`), the base's copy of that file is what
runs, with sh: a bead cannot change the script that judges it, and a change
to the script counts from the next bead on. A depot with no test command
lands untested, and the note says so.
The note of a red run starts with the branch, the commit, the base and what
ran, and carries the last 200 lines of the output: what matters of a long
run is the test command's to print at its end.
The run is bounded (`YARDR_TEST_TIMEOUT`, in seconds; 1800 when unset),
besides what it waited for the race lock: at the deadline the command and
everything it started is killed, and the run is red. A run ended by a signal
from outside (exit 128+N, e.g. 143 for SIGTERM)
is neither red nor "could not run": it is run once more on the same commit,
with a note of the first run's output, and held with both outputs if a
signal ends it again.
Before the tests run, the yard keeps its disk (`yardr disk keep`): under
`disk_keep_gb` free on GOCACHE's volume it rotates GOCACHE at a fresh
sibling directory, removes a rotated directory once it has stood aside
for 30 minutes, and clears go-build directories in GOTMPDIR older than 3
hours, logging a `disk_kept` event with what it did
(docs/setup.md, "The machine's disk"). Best effort: a keep that fails is
in the log and the landing goes on to the tests. Where the installed
yardr predates the command the step is skipped, and activates once the
new yardr is in.
Green fast-forwards the base to the commit the tests ran on, by its sha,
only ever by fast-forward, and never in a dirty checkout (it holds
instead); a branch that moved while the tests ran reports `failed` with
both commits, so nothing untested lands. With `YARDR_PUSH` set it pushes the base,
never forced; a failed push mails `YARDR_NOTIFY` and leaves the bead merged.
Where the yard's `upgrade_from` setting names the bead's depot it then runs
the installed yardr's `upgrade`, and the landing note says how the install
went (`merged 6a07178, tested with the base's copy of .yardr/test in 355 s;
install ok in 24 s (scripts refreshed: merge-local)`), with the install's
seconds in the log line. A failed install where the yard still answers still
merges, like a failed push. Where the installed yardr predates the command
the note says to install the new yardr once by hand first. When the install
leaves the yard dead no advance is possible, and the bead fails its session
toward a hold. The step activates only once the operator has installed this
script and sets `upgrade_from` (docs/upgrade.md, "The assembly step"). The
install also keeps the scripts the yard has in `~/.yardr/scripts` current
from the checkout it builds, this one among them, and the note names the
ones it refreshed (docs/upgrade.md, "The scripts").
Finally it advances the bead to `merged` with `merged <sha>`, what tested
it, the seconds the tests took, and how the install went where it ran
(`merged 6a07178, tested with the base's copy of .yardr/test in 355 s`).
Before the outcome, whichever it is, one
line of the log has the seconds of each step that ran (`== <bead> took:
rebase 1 s, the tests 355 s, push 2 s, upgrade 24 s`); where the steps of the test command
went is the command's to print, as depot yardr's does after each of them
(`test: go vet ./... took 14 s`).

A wagon of a train (`YARDR_TYPE=wagon`, `YARDR_TRAIN` set, and that train's
branch as `YARDR_BASE`) is tested with the same command without
`YARDR_LANDING`, which a depot's command may take for a quicker run of what
the wagon changed; the landing run is integrate-train's, on the whole train.
Everything else is a landing, a wagon with no train included. See
[docs/line.md](../../docs/line.md#wagons).

A design bead (`YARDR_TYPE=design`) lands one document under `docs/design/`
and no code: a branch that changes any other path is held before the tests
run, naming every such path, so code no reviewer read never lands.

**integrate-train** runs once a train's last child has landed on its branch,
routed for the train flow's `open` stage (type `train`). It fetches the base
when it is a remote's, rebases `yard/<train>` onto the depot's base and runs
the depot's test command on it, with `YARDR_LANDING=1` and from the base's
copy where the command is a file the base has, as merge-local does, and for
at most `YARDR_TEST_TIMEOUT` seconds besides what it waited for the race lock
(a run killed at the deadline is red); a depot
with no test command goes to review untested. The command's `YARDR_BEAD` is
the train, which is no change of its own, so the run also sets
`YARDR_TEST_BEADS` to the train's children, sorted and joined by commas (set
and empty when `yardr bead show --json` and `jq` cannot name them): depot
yardr's command keeps it in its flaky log, to count a flaky test once per
change. Green pushes the rebased branch when `YARDR_PUSH` is set
(replacing the remote branch only if everything on it was in what it
rebased, or in a tip a note on the train records as `old tip: <sha>`, which
its own conflict note and the merger's note both do: the merger rebases
without pushing, so the remote still has the tip it started from) and
reports `done`, to review. Red adds a child in `backlog`, "Fix
the tests on train <id>", which blocks the train, and reports `failed`, back to
`open`; the child's body and the train's note start as merge-local's red
note does, with the commit and what ran, and carry the last 200 lines of
the output. A rebase conflict aborts the rebase, reports `conflict` to `merge`
with the conflicting files and both tips, and leaves the branch for the
mergers; after they report done, the train returns to `open` for integration.
A test command that says it could not run (exit 2), or a branch it cannot
work on (missing, dirty, nothing ahead of the base), holds the train. A run
ended by a signal is run once more, as in merge-local, and holds the train the second time.

With `YARDR_PR=1` (and `YARDR_PUSH`), integrate-train also opens a pull
request for the pushed branch against the base, or replaces the body of the
open one the train is bound to: the train's body, a "Children" list and the
trailer `Train: <id>`. It binds the train to that pull request (`--meta
pr_url=<url>`, `pr_number`, and `pr_head_repo`, the repository its head is
in) and puts the URL in the note. No script finds a PR by the branch's
name, which a fork's can carry: one open for the branch that the train is
not bound to holds the train. A push or `gh` that fails reports `failed`, back to `open`, so the
next integration retries; the daemon waits longer before each retry in a
row (docs/line.md, "The train's own flow").

The pull-request path is experimental and off by default: a train lands
through merge-local unless `YARDR_PR=1` is in the cmd of integrate-train,
pr-train and pr-merge alike. Without it pr-train and pr-merge hold the train
with a note naming the switch and touch neither the remote nor GitHub.
docs/line.md, "Through a pull request", lists its known limits (the
daemon lands a train on the base branch's required checks only).

**pr-train** is routed for the train flow's `approved` stage (type `train`)
when trains land through pull requests. It pushes `yard/<train>` with a lease
(it may carry the reviewer's commits; a remote branch with commits of its
own is not overwritten), replaces the PR's body with the first review's note
under "Review" and the security review's under "Security review" where it
has one, stores the commit it pushed in the train's metadata as
`pr_reviewed_sha`, and reports `done`, to `in-pr`. No bound PR, a refused push
or a PR GitHub reports as conflicting reports `failed`, to `open`.

**pr-merge** is routed for `land` (type `train`). It runs `gh pr merge
<pr_url> --rebase --delete-branch --match-head-commit <pr_reviewed_sha>`
(`--squash` with the subject and body merge-local would write for the train,
over the pull request's commits, where its group sets `YARDR_LAND=squash`),
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

**merge-pr** is routed for `approved` (type `pr`, label `merge-ok`), for the
pull requests of the contributors pack. It merges the pull request the bead
tracks on GitHub, as the yard's App: a token minted by `github-mint` for that
repository from `GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY_FILE`, which the
group's `env_file` gives (the mint is the github source's `mint_command`, or
`YARDR_GITHUB_MINT` in the cmd). The bead must carry `merge-ok` and track a
`https://github.com/<owner>/<repo>/pull/<n>` link, and nothing else is
looked up. A bot's pull request from a branch of the repository itself is
merged at its live head; every other is merged at the bead's `merge_head`,
the full sha a maintainer's yes recorded (`yardr bead update <id>
--add-label merge-ok --meta merge_head=<sha>`), and a head that is another
commit holds the bead ("moved since your yes"). The yard's CI must have passed
on that commit: the bead's `ci_head`, which pr-ci records, is it, and GitHub's
check runs are not asked. `gh pr merge` runs with
`--match-head-commit`: `--rebase --delete-branch` for a branch of the
repository's own, `--merge` and no deletion for a fork. A pull request merged
already reports `merged`, one closed unmerged `dropped`, and nothing else is
ever reported: whatever stops the merge holds the bead with a note. It
touches no checkout and needs `gh` and `jq`, and never uses the operator's
own `gh` login. docs/line.md, "Merging pull requests", has the setup.

**pr-ci** is routed for `ciwait` (type `pr`), the stage after a reviewer has
read a pull request of the contributors pack, which carries a copy of it that
a test holds equal, and is the yard's CI of it:
GitHub Actions is off, so it runs the depot's test command on the pull
request's head. It asks GitHub as the yard's App, as merge-pr does (the same
`env_file` and mint, and the same text for the tracks link and the token, held
equal by a test), reads the head and compares it with the bead's
`reviewed_head`, the commit its reviewer recorded as the one it read (a full
sha, compared without regard to case): another head, or none recorded, holds
the bead, naming both commits and the command that sends it back to review,
and nothing is fetched. Then it fetches `refs/pull/<n>/head` from the
depot's `origin` and checks that commit out, detached, in a worktree of its
own (`<worktrees>/<depot>/<bead>-ci`), removed when the script ends. It runs
`YARDR_TEST` there as merge-local does, with the base's copy of a test file
the base has, with `YARDR_LANDING=1` (the run is the pull request's landing
run: no landing by the assembly follows it), red on exit 1, held on exit 2, run
once more when a signal ended it, and killed and red past `YARDR_TEST_TIMEOUT`.
Green on the head GitHub still names records it as the bead's `ci_head` and
reports `done`, to `approved`; red, or a head that moved meanwhile, reports
`changes`, to `contributor`, with the end of the output. Whatever is no
verdict (no token, a refused gh or fetch, a depot with no test command, a
bead not in `ciwait`, a pull request no longer open) holds the bead with a
note. Nothing is written to GitHub. Needs `gh`, `jq` and `git` on PATH.
docs/line.md, "The yard as the CI of a pull request", has the setup.

**Environment.** The four above read what the exec runner sets: `YARDR_BEAD`,
`YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_BASE`, `YARDR_BRANCH` (default
`yard/<bead>`), `YARDR_WORKTREE`, `YARDR_BIN` and `YARDR_HOME` (used for the
default worktree path); merge-local also `YARDR_TYPE` and `YARDR_TRAIN`. merge-pr
reads `YARDR_BEAD` and `YARDR_BIN` alone, and pr-ci those and `YARDR_DEPOT`,
`YARDR_DEPOT_PATH`, `YARDR_BASE`, `YARDR_WORKTREE` and `YARDR_TEST`, the depot's
test command, with `YARDR_TEST_TIMEOUT` in its cmd as merge-local has it.
Options go in the group's `cmd`: `YARDR_PUSH`, a remote to push to (off when
empty; pr-train and pr-merge default to `origin`), `YARDR_NOTIFY`, the crew member
mailed when that push fails (default the yard's `yardmaster` setting, else
`yardmaster`), `YARDR_LAND`, `rebase` (the default) or `squash`, how a bead
lands, read by merge-local and pr-merge (a copy of merge-local's message
writer is in pr-merge, held equal by a test), and `YARDR_PR=1`, the switch for
the pull-request path, read by integrate-train, pr-train and pr-merge.

**Setup** (from docs/line.md):

```sh
install contrib/assembly/merge-local contrib/assembly/integrate-train ~/.yardr/scripts/
yardr group add yard-assembly --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/merge-local' \
  --set log="$HOME/.yardr/logs/assembly.log"
yardr route add --stage approved --group yard-assembly --depot yard --originator any

yardr depot flow yardr --type train yardr.train                  # the repo's .yardr/flows/yardr.train.flow
yardr group add yard-integrators --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/integrate-train' \
  --set log="$HOME/.yardr/logs/integrator.log"
yardr route add --stage open --type train --depot yard --group yard-integrators --priority 10 --originator any
```

The install is the first-time step: from then on `yardr upgrade` refreshes
each script from the checkout it builds (docs/upgrade.md, "The scripts").

Landing trains through pull requests adds the groups `yard-pr` and
`yard-pr-merge`, routed for `approved` and `land` (type `train`), with
`YARDR_PR=1` in their cmd and the integrators'; docs/line.md, "Through a pull request", has
the commands.
