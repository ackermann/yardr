# The pipeline: how a bead moves in practice

yardr builds no pipeline in: stages, groups, routes and a flow compose one.
This page follows a bead through the pipeline yardr itself is built with
(rig yardr, as of 2026-10-03). Its flows are in [flows.md](flows.md#worked-example-rig-yardr).

```
backlog ──mayor shapes──▶ new ──builder──▶ review ──Codex──▶ approved ──refinery──▶ merged on main
   ▲                       ▲                 │                  │
   │                       └──── changes ────┘                  │
   │                       └────────── red check or conflict ───┘
   └──────── decide (Christopher) ◀── a question from backlog, new, review or approved
```

## 1. Backlog: the mayor shapes the bead

New ideas land in stage `backlog`, routed to the manual group `backlog`. The
mayor (the resident `yardmaster`, see [residents.md](residents.md), prompt this repo's
`.yardr/roles/mayor.md`) reads them and shapes each one until a builder could finish
it without asking: a goal, where to look, what "done" means and how to test
it. Large beads are split, and `yardr dep add` orders them: a bead is only
ready once its blockers are closed.

A shaped bead is advanced to `new` (`yardr bead advance <id> --to new`).
Christopher can do the same from `yardr review -g backlog`.

### Cycles of blockers

`yardr dep add` refuses a `blocks` edge that would close a cycle, and names
it in order, the new edge first: `yardr-a blocks yardr-b blocks yardr-c
blocks yardr-a`. An open cycle leaves no bead ready, and nothing would say
why. The child's edge that `dep add -k parent` adds ([Epics](#epics)) is
refused the same way; edges of any other kind (`related`, ...) decide no
readiness and are never refused for it. Closed beads count: a closed blocker
blocks nothing, but reopening it would make the cycle live, and `bead
reopen` does not look at edges. The refusal then says which beads of the
cycle are closed; `yardr dep rm` on the old edge makes room for the new one.

A store written before this check may hold a cycle. `yardr doctor` names
each cycle of open beads in a `cycle` line ([doctor.md](doctor.md)), and
`yardr dep rm <blocker> <bead>` on the edge that is wrong ends it. yardr
removes no edge itself.

## 2. New: a builder implements it

`new` is routed to `yardr-builders`: a herdr group running Claude Code with
`prompt=roles/builder.md` (this repo's `.yardr/roles/builder.md`, as on
main), at most 5 at a time. The daemon opens a worktree
on branch `yard/<bead>`, starts the agent in it and hands it a brief: the
role prompt, the bead, and the outcomes its stage offers.

**How agents are attributed.** The agent runs in a herdr tab created with
its identity in the environment: `YARDR_ACTOR` (the agent's name,
`<bead>-<stage>`), `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`,
`YARDR_GROUP`, `YARDR_HOME` and `YARDR_BIN`. Nothing depends on the agent
exporting anything: every event its `yardr` commands log names it as actor.
Every move (`yardr bead done`, `advance`, `close`, `hold`, `release` and
`reopen`) also sends `YARDR_SESSION`, which the store checks against its
own session table: the id must be one the yard started, and the bead the
session was started for comes from that record, not from the caller. A
move of that bead is refused unless the session still owns it (`stale:
bead <id> is now in <stage> owned by <session>`), so an agent kept open,
or orphaned by a restart, after its bead moved on cannot end the session
that owns the bead now; nothing changes. Another bead moved from the same
pane (a follow-up the agent filed and advances) is checked as the actor's,
like a person at a shell, except that one owned by a live session is
refused rather than taken from under its agent. The store also knows the
pane itself: the runner records the pid of the tab's shell (for an exec
group, of its script) on the session, and a call whose process descends
from it is checked as that session whatever its environment says, so
unsetting `YARDR_SESSION` in the pane is not a way out, and naming another
session from it is refused. An agent whose commands run outside its pane
with another session's environment (Codex's shared background server) is
told apart by the worktree it runs in
([groups.md](groups.md#codex)). A person at a shell of their own has no
`YARDR_SESSION` and no pane the store knows, and is not checked. `yardr
review` sends the stage and revision the form showed instead: it refuses a
decision on a bead that moved or changed since. Every move takes
`--revision <rev>` for the same compare-and-swap, as BDP writes take
`If-Match`.

The builder implements the change, runs the gate (`yardr rig check yardr
--dir <worktree>`, which runs `.yardr/check` from fetched `origin/main`:
`go vet`, `go test -race`, the RPC suites), commits on its branch (never
pushes or merges), leaves a note with what it changed and verified, and
reports `yardr bead done`, which the flow sends to `review`.

### What the gate does not catch

The gate's script comes from the base, so a branch cannot change what is
run, but it runs in the branch's worktree, against the branch's code and
the branch's tests. A branch that deletes a test, skips it or makes it
trivially true passes the gate vacuously. That is the accepted model for
now: the reviewer reads the diff and is the one who catches a weakened
test, not the gate. Both the builder's run and the refinery's run of the
gate are this same check.

## 3. Review: Codex reviews forward

`review` is routed to `yardr-reviewers`: Codex in the same worktree, with
the reviewer prompt from this repo's `.yardr/roles/reviewer.md` (how a
reviewer works) and the stage file `.yardr/flows/default/review.md` (when
each outcome applies). It is the last review
before merge, so it fixes what it can on the branch (commits starting
`review:`) rather than only reporting, re-runs the checks and then:

- **approves**: a note with what it checked and fixed, then `yardr bead
  done` (to `approved`);
- **sends back**: concrete findings, then `--outcome changes` (to `new`);
  the bead returns to
  the builders, in the same worktree. After two send-backs it holds the bead
  instead of a third, so the mayor looks;
- **asks**: a question only Christopher can answer goes to `decide`
  (`--outcome question`).

In a flow with a `merge` stage (section 5; rig yardr's has none), when a
merger sends a bead back for a `merge re-review`, the reviewer records
the old tip from the merger's note and runs
`git range-diff <base>...<old-tip> <base>...HEAD`. It checks only the
conflict-resolution changes: that both sides' intent was kept, the merger's
note names every conflicting file, and the gate result is credible. It does
not redo the original feature review. If the resolution is sound, it notes
the range-diff and advances to `approved`; otherwise it sends the bead to
`new` or `decide` with a concrete finding.

## 4. Approved: the refinery lands it on main

yardr takes no position on how work lands: a PR per bead, a shared PR
several beads funnel into, or a direct merge. The refinery is not built in;
it is a composition. A stage (`approved`) is routed to an `exec` group whose
script does the landing, and `--limit 1` on the group makes it a serial merge
queue. yardr provides the mechanism and ships one reference script,
[`contrib/refinery/merge-local`](../contrib/refinery/merge-local); rig yardr
uses it to merge directly to `main`.

**The mechanism.** The exec runner starts the script in the rig with
everything it needs in its environment ([groups.md](groups.md#runners)):
`YARDR_BEAD`, `YARDR_RIG`, `YARDR_RIG_PATH`, `YARDR_BASE`, `YARDR_BRANCH`
(`yard/<bead>`), `YARDR_WORKTREE`, `YARDR_BIN` (this yardr), and
`YARDR_ACTOR` set to the group. The refinery runs the gate itself, whatever
the builder reported: `yardr rig check "$YARDR_RIG" --dir
"$YARDR_WORKTREE"`, i.e. `.yardr/check` as it is on the base (the fetched
`origin/main` when the base has a remote), or the rig's `--check`; a rig
with neither fails with "no gate" unless it allows that
([configuration.md](configuration.md#the-merge-gate-yardrcheck)). The script finishes by moving the bead, with a note:
`yardr bead advance "$YARDR_BEAD" --to <stage> --note -`. A script that
exits without advancing, closing or holding counts as a failed session, like
an agent that gives up, and after 3 of those the bead is held. Advancing ends
the session and the daemon stops the script, so it is the script's last step.

**merge-local.** For each bead it:

1. rebases `yard/<bead>` onto the base in the bead's worktree, if the base
   moved; on a conflict it aborts the rebase and advances to `merge` with the
   conflicting files (section 5), or to `new` if the flow has no `merge`,
   as in rig yardr;
2. runs the gate on exactly what will land, every time, including after a
   merger's rebase, and records the commit it ran on; red (exit 1) reports
   the outcome `failed` (to `new`; to `open` for an epic), or advances to
   `new` in a flow without outcomes. The note starts with the branch, the
   commit the gate ran on, the base and the file `rig check` kept the whole
   output in (`<home>/gates/<rig>/<bead>-<time>.log`); then come the lines
   of the output matching the rig's `.yardr/check-fail-pattern`, each with
   the lines around it, when the rig has one, and then the last 200 lines
   ([configuration.md](configuration.md#a-red-gates-output-the-file-and-yardrcheck-fail-pattern)).
   The builder reads it with `yardr prime --bead <id>`, which shows every
   note whole. A gate that could not run at all (exit 2: git, the base, the rig) holds
   the bead, since no builder can fix that. Nor can one fix a gate ended
   by a signal from outside (exit 128+N; 143 is a `go test` killed with
   SIGTERM after every package printed `ok`): it is run once more on the
   same commit, after a note with the first run's output, and what the
   second run says counts; ended by a signal again, the bead is held with
   both outputs. The gate's own kill at `--timeout` is not that: it exits
   1, a red gate. A branch that moved while the
   gate ran (a commit pushed to it meanwhile) reports `failed` the same
   way, with both commits in the note: nothing the gate did not see lands;
3. lands that commit, by its sha, by fast-forward only. Where the base is
   checked out it fast-forwards that checkout, only when it is clean; a
   dirty one is someone's work in progress, so the bead is held with a note
   saying so and the tree is never touched. Where the base is checked out
   nowhere it moves the ref alone (`git fetch . <sha>:<base>`), which works
   whatever state the rig's checkout is in: keep the base checked out
   nowhere and a dirty checkout never blocks the queue;
4. pushes the base if `YARDR_PUSH` names a remote (off by default, never
   forced). A failed push leaves the bead merged; the next landing's push
   carries it, and `YARDR_NOTIFY` (default `mayor`) is mailed;
5. advances to the terminal stage `merged` with the note `merged <sha>`,
   which closes the bead; in a flow without `merged` it closes the bead with
   that reason.

It never forces, and a base that is not a local branch (`origin/main`) is
held: merge-local lands only on local branches.

### Setup

```sh
install contrib/refinery/merge-local ~/.yardr/scripts/    # from main
yardr group add yardr-refinery --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin YARDR_NOTIFY=yardmaster ~/.yardr/scripts/merge-local' \
  --set log="$HOME/.yardr/logs/refinery.log"
yardr route add --stage approved --group yardr-refinery --rig yardr
```

The script is installed from `main` rather than run from a checkout: a bead
branch must not change how it is landed. Without a checkout of yardr,
`yardr setup` does the same in a terminal: its page for landing writes the
merge-local this yardr was built with to `~/.yardr/scripts/merge-local`
(one already there is kept) and adds the group `<rig>/refinery`, without
`YARDR_PUSH` ([setup.md](setup.md)). The flow gains `merged`:

```
approved -> merged on done | new on failed | decide on question
merged terminal
```

**A PR-based variant** is the same group and route with another script. In
place of steps 3 to 5, it pushes the branch and opens (or updates) a PR, and
parks the bead until the PR is merged:

```sh
git -C "$YARDR_WORKTREE" push --force-with-lease origin "$YARDR_BRANCH"
url=$(gh pr create --head "$YARDR_BRANCH" --base "$YARDR_BASE" --fill 2>/dev/null ||
  gh pr view "$YARDR_BRANCH" --json url -q .url)
echo "PR $url" | "$YARDR_BIN" bead advance "$YARDR_BEAD" --to in-pr --note -
```

with the flow rules `approved -> in-pr | merge | new` and
`in-pr -> merged | new`, and `in-pr` routed to a manual group, or to nothing
with a subscription on the forge's merge webhook
([hook sources](subscriptions.md#hook-sources)) advancing the bead to
`merged`. Beads funneling into a shared PR push to one integration branch
instead of their own.

After a merge, `go install` on `main` is the whole upgrade: the running
server and daemon replace themselves with the new build within seconds, and
builders keep advancing beads through it
([configuration.md](configuration.md#upgrading)).

## 5. Merge: a merger resolves conflicts

Rig yardr does not run this stage as of 2026-10-03: its flow has no
`merge`, so a conflict goes back to `new`. The rig it was before the rename
did, and the merger's role is still in the repository
(`.yardr/roles/merger.md`); this section is how the stage works where it is
set up.

Two of the first 22 landings conflicted with what had landed since their
branch was made. Sending those back to `new` cost a full builder and Codex
cycle for what is usually a mechanical rebase, so a conflict goes to `merge`
instead.

`merge` is routed to `mergers`: a herdr group running Claude Code (Sonnet,
at most one at a time) with `prompt=roles/merger.md`. The merger reopens the
bead's worktree on `yard/<bead>` (the refinery aborted its rebase, so it is
clean) and:

1. rebases onto the rig's base;
2. resolves each conflict keeping the intent of both sides, from the bead and
   its notes on one side and the base's commits on the file
   (`git log -p <merge-base>..main -- <file>`) on the other;
3. runs the gate, `yardr rig check yardr --dir .`;
4. leaves one `merge re-review:` note naming the old tip, each conflict and
   its resolution, and reports `yardr bead done`, which the flow sends to
   `review`.

The reviewer then checks only the resolution with a range-diff and advances
the bead to `approved`. The refinery picks it up again: `main` no longer
moved past the branch, so it runs the checks and fast-forwards. If `main`
moved again in the meantime and conflicts again, the bead goes back to
`merge`.

When both intents cannot be kept, or keeping them needs a real design choice,
the merger aborts the rebase, notes why, and reports `--outcome rework` (to
`new`: the builders rework the bead) or `--outcome question` (to `decide`:
Christopher chooses). It never touches `main`,
never pushes, and never forces anything but its own branch.

A red gate (not a conflict) still sends the bead to `new`: the change, not
its placement on `main`, needs work.

### Setup

The mayor would wire this in the store; nothing in `~/.yardr` is versioned:

```sh
yardr group add mergers --runner herdr --limit 1 \
  --set kind=claude --set model=sonnet --set prompt=roles/merger.md \
  --set args='--permission-mode auto'      # as yardr-builders: it works unattended
yardr route add --stage merge --group mergers --rig yardr
```

The flow gains two rules (the rest of [`default.flow`](flows.md#worked-example-rig-yardr)
is unchanged), with outcomes so the merger reports how the merge ended
instead of naming stages:

```
approved -> merged on done | new on failed | merge on conflict | decide on question
merge -> review on done | new on rework | decide on question
```

and the refinery sends a conflicting bead to `merge` (merge-local does; it
falls back to `new` when the flow has no `merge` stage).

## Holds

A held bead is never routed; it waits until someone runs
`yardr bead unhold`, which routes it again from its stage (with fresh
attempts). Holding a bead ends the session working it (state `held`, not a
failure), so the daemon closes the agent's tab and the group's slot goes to
the next ready bead at once; the worktree and its branch stay. Beads get held:

- by yardr, after 3 failed sessions in one stage (an agent that died or
  exited without advancing);
- by an agent that is stuck or finds the bead unclear: it notes the question
  and runs `yardr bead hold`;
- by the reviewer, instead of a third send-back;
- by the refinery, when it cannot land without touching a dirty tree (the
  base checked out with uncommitted changes, or the bead's worktree dirty).

The mayor watches held beads, finds out why, and either fixes the cause and
unholds, reshapes the bead, or sends it to `decide`.

Not every failure is the bead's. If herdr is missing or down, no bead is
held for it: the group logs one `runtime_down`, backs off (30s, doubling to
5m) and its beads wait untouched until a start works again (`runtime_up`).
A session whose agent does nothing with its first prompt (a model it may
not use, no login) raises `session_stalled` after 2m, with the end of its
pane, and its group starts no more sessions with that model; one whose
agent reports another model than asked raises `session_model_mismatch`
([groups.md](groups.md#models)). A session that sits idle 30m or runs 4h
raises `session_idle` or `session_aged` in the mayor's inbox; with a group `grace` set it is then
stopped, and without one a session that has run twice its `max_age` is
stopped anyway and the bead released with a note; see
[configuration.md](configuration.md#session-limits-and-outages).

## Decide

`decide` is routed to the manual group `decisions`. Backlog, new, review and approved may
send a bead there with the question and the options written into the bead. Christopher
answers in `yardr review -g decisions` (or with a note and
`yardr bead advance`), and the bead goes back to `backlog` or `new`.

Decisions that are not about one piece of code are beads created straight
into `decide` and closed with the answer as the reason. The rig before the
rename gave them a type of their own, `decision`, whose type flow had one
move, to the terminal stage `decided`; yard-17, which settled the
repo/store split in [configuration.md](configuration.md), was one.

## Epics

An epic is work that spans several beads and must land on main as one
unit. It is a bead of type `epic`; its children are the beads with a
`parent` edge from it:

```sh
yardr bead create "Epics" -t epic -r yardr
yardr dep add <epic> <child> -k parent
```

- **Branch(b)** is `yard/<id>` for every bead; the epic's branch is
  Branch(epic).
- **BaseOf(b)** is Branch(parent epic) for a child of an epic, and the rig's
  base for everything else, the epic itself included. `yardr bead show`
  prints it as `base` (and `epic`, and an epic's `children`; the same
  fields are in `--json`).

A child goes through the same flow, groups and gate as any bead; only its
base differs. When a child is first started and the epic's branch does not
exist yet, the herdr runner creates it from the rig's base tip (fetched
first when the base is a remote's) and logs `epic_branched` on the epic
with the commit. An existing epic branch is never recreated or moved. The
child's worktree is branched from the epic's branch, and `YARDR_BASE` in
the agent's and the refinery's environment is BaseOf(b), so merge-local
rebases the child onto the epic's branch, lands it there and pushes that
branch; main does not move. The gate is still read from the rig's base
(main), not from the epic's branch, so neither an epic nor its children can
change the bar they land under; it runs on the child's branch as before.

`dep add -k parent` refuses a parent that is not an epic, a closed epic
(it has landed, or was dropped: a child added now would land on a branch
nothing integrates again), a child already in another epic, an epic as a
child (epics do not nest) and a child in another rig. Only parent edges
from an epic count: a bead whose parent is not an epic keeps the rig's
base. A bead that is a child of two epics
anyway (a type changed to `epic` later) is not dispatched; yardr logs
`bad_epic` until one parent edge is removed.

`yardr bead list`, `yardr ready` and `yardr session list` show each
bead's epic in an EPIC column after the bead (empty for a bead with no
epic), and their `--json` rows carry it as `epic`. `yardr bead list --epic
<epic>` lists only that epic, first, and its children. In herdr, a child's
workspace is labelled `<group> [<epic>] <bead>` (e.g. `builders [sv6b]
2cet` for `yardr-2cet`) and any other bead's `<group> <bead>` (`builders
cbdc`): the workspace sits under the rig's node, so the rig prefix is
trimmed from the ids and the group, and the group goes by its `label`
config when set ([groups.md](groups.md#herdr-setup)). The workspace is
reused across stages and relabelled with the current group on each; each
stage's tab mirrors it with the stage in front, `<stage> [<epic>] <bead>`
(`review [sv6b] 2cet`).

`dep add <epic> <child> -k parent` also adds `<child> -blocks-> <epic>`, so
one command makes the child and keeps the epic from being ready until the
child closes; `dep rm <epic> <child> -k parent` removes both edges.

### Parts

A part is a child of an epic that takes a shorter road to the epic's
branch. It is for several pieces of one feature, built in parallel, that
are not worth having on main one by one: each piece would otherwise pay
for the full gate three times (builder, reviewer, refinery) and a review of
its own, and the refinery lands one bead at a time. A part pays for a light
gate, twice, and no review; the full gate and one review happen once, on the
whole epic, before main moves. Everything about the epic itself is as above
and below: nothing changes in what an epic needs to reach main.

```sh
yardr bead create "Epics" -t epic -r yardr
yardr bead create "The parser" -t part -r yardr
yardr dep add <epic> <part> -k parent
```

- **The type.** A part is a bead of type `part`. In rig yardr the type has
  a flow of its own, [`.yardr/flows/type/part.flow`](../.yardr/flows/type/part.flow):
  the rig's flow without `review` (`new -> approved on done`). The rig's
  routes for `new`, `approved` and `decide` carry no type, so parts go to the
  same builders and the same refinery as any bead.
- **The light gate.** A rig's second gate, `.yardr/check-light`, read from
  the base and run by `yardr rig check <rig> --light` under every rule of
  the full one ([configuration.md](configuration.md#the-light-gate-yardrcheck-light)).
  Rig yardr's formats, vets and builds everything and tests, with the race
  detector, only the packages the branch changed and the packages that
  import them. The builder runs it before `done`; merge-local runs it again
  on exactly what lands.
- **Which gate merge-local runs.** The exec runner tells a script the
  bead's type and its epic (`YARDR_TYPE`, `YARDR_EPIC`, the latter empty for
  a bead with no epic). merge-local runs `rig check --light` only when all
  three hold: the type is `part`, there is an epic, and the base it lands on
  is that epic's branch. Everything else gets the full gate, as before: a
  task that is a child of an epic, and a part with no epic, which would land
  on the rig's base. A rig with no light gate runs its full gate under
  `--light` and says so in the output. A part's landing note says
  `merged <sha> on yard/<epic> under the light gate`.
- **A part needs an epic.** The light gate and the missing review are only
  safe where the full gate and a review still stand before main, so yardr
  keeps a part with no parent epic in the stage its flow starts in
  (`backlog`): `bead advance`, `bead done` and `bead create -s <stage>`
  refuse to put it anywhere else, saying how to give it an epic
  (`dep add <epic> <part> -k parent`) or make it a task
  (`bead update <part> --type task`). A part that lost its epic after it
  moved (`dep rm -k parent`), or that no flow governs, is not started by any
  agent or script: the dispatcher logs `part_without_epic` once, and only a
  manual group still gets it. It can be taken back to `backlog`.
- **The epic pays.** integrate-epic rebases the epic's branch and runs the
  full gate on it as for any epic, and a red gate adds a fix child. The
  epic's reviewer is told that parts were not reviewed or fully gated one by
  one ([`.yardr/flows/yardr.epic/review.md`](../.yardr/flows/yardr.epic/review.md)),
  and reviews each. So keep an epic to about five parts, which one review
  can hold, and leave single fixes on the normal path.

### The epic's own flow

An epic follows `yardr.epic` ([flows.md](flows.md#worked-example-rig-yardr)).
As of 2026-10-03 rig yardr has the flow but not the groups for it: `open`,
`merge`, `in-pr` and `land` have no route, so an epic there waits for a
human until the [setup below](#setup-2) is done. The steps describe the
path with that setup:

1. **backlog**: the mayor shapes the epic and adds its children, then
   advances it to `open`.
2. **open**: the epic waits while any child is open (they block it). When
   the last child closes it is ready, and the route for type `epic` in
   `open` sends it to `yardr-integrators`, an exec group running
   [`contrib/refinery/integrate-epic`](../contrib/refinery/integrate-epic).
   The integrator fetches the base when it is a remote's, rebases
   `yard/<epic>` onto the rig's base in the epic's worktree, runs the gate
   on it (`yardr rig check yardr --dir <worktree>`, the gate read from main),
   pushes the rebased branch when `YARDR_PUSH` is set (replacing the remote
   branch only when everything on it was in what it rebased, or in a
   recorded old tip; step 5), and reports `done`, to review. A red gate
   reports `failed`, back to `open`, after adding a child in `backlog` to
   fix it ("Fix the gate on epic <id>", type bug). The note and the child's
   body carry what merge-local's note does: the branch, the commit, the base
   and the file the whole output is in, then the lines matching the rig's
   `.yardr/check-fail-pattern`, then the gate's tail. The child
   blocks the epic, so it is not integrated again until the fix has landed
   on its branch. A rebase that conflicts aborts it and reports
   `conflict`, to `merge`, with the conflicting files and both tips in the
   note (step 5). A gate that could not run (exit 2) or a branch it cannot
   work on (missing, dirty, nothing ahead of the base) holds the epic with
   a note. A gate ended by a signal (exit 128+N) is run once more, as
   merge-local does, and holds the epic if a signal ends it again; no
   child is added for it.

   An epic reported `failed` back to `open` with no child to wait for (the
   push or the pull request failed: GitHub down) is not integrated again on
   the daemon's next pass. Each return to the stage it was in, in a row,
   doubles the wait before the next try, from the daemon's backoff (30s) up
   to an hour, so an outage is retried less and less often rather than on
   every pass; the daemon logs each wait once on the epic as a `backoff`
   event, with the count. The count is read from the epic's events, so a
   restarted daemon keeps waiting; it starts over when the epic enters the
   stage any other way or a human un-holds it.
3. **review**: `yardr-reviewers` reviews the whole diff, `<base>..yard/<epic>`:
   the epic's brief says it is an epic, names its children and gives the
   range. Changes go back to `open` (`--outcome changes`) after adding the
   fix as a child; without one the epic would be integrated again unchanged.
4. **approved**: the epic lands, through a pull request (below) or
   locally: `yardr-refinery` and merge-local land `yard/<epic>` on the rig's
   base exactly as a bead's branch (BaseOf(epic) is the rig's base) and
   advance it to `merged` by the flow's bare `approved -> merged` edge. A
   red gate there reports the flow's `failed` outcome, which for an epic is
   `open` (merge-local falls back to `new` only for flows without it).
5. **merge**: a conflict between the epic's branch and the rig's base,
   from the integrator in `open` or from merge-local or pr-epic in
   `approved`, goes to the mergers as any bead's does. The merger rebases
   `yard/<epic>` onto the base in the epic's worktree, keeping both sides'
   intent, runs the gate, and does not push (the integrator pushes, with a
   lease); the brief says the branch and what happens next. The tip the
   merger starts from is recorded on the epic as `old tip: <sha>`, by the
   integrator's conflict note and again by the merger's note (as for any
   bead): the remote still has that tip, which the rebase rewrote, and the
   integrator's lease accepts a remote branch whose commits are all in the
   branch as it was before its own rebase or in a recorded old tip, so the
   resolved branch replaces it. A remote with commits in neither is never
   forced over: the epic is held, or mailed about when there is no pull
   request. Its `done` sends the epic back to `open`, not to review: with
   no child open it is ready, so the integrator takes it again, rebases it
   (a no-op unless the base moved again), gates it, pushes it and opens or
   updates its pull request, and the epic goes to review as what will
   land. `rework` also goes to `open` (add the fix as a child first),
   `question` to `decide`.

### Through a pull request

**Experimental, and off by default.** An epic lands locally (step 4 above)
unless `YARDR_PR=1` is in the cmd of every group on this path: the
integrators, `yardr-pr` and `yardr-pr-merge`. Without it integrate-epic opens
no pull request, and pr-epic and pr-merge hold the epic with a note naming
the switch, before pushing, merging or asking GitHub anything, so a stray
route or a move by hand does not start the path: nothing reports `done` in
`approved`, and the epic reaches `merged` only by merge-local's bare edge.

Do not switch it on for a repository others can open pull requests
against while it is experimental. Its known limits:

- **Green is the base branch's required checks.** The daemon lands an
  epic when every check the base requires (branch protection or a
  ruleset) is green on the reviewed commit
  ([GitHub events](#github-events)). A base that requires none is never
  green to it: a human says merge.
- **A commit status sends no event.** A required check an external CI
  reports as a commit status is read with the rest, but only
  `workflow_run` and `check_suite` deliveries have the state read again;
  an epic waiting on a status alone waits for a human, or for the next
  event about its pull request.

With `YARDR_PR=1` set, an epic goes to main as a pull
request against the rig's base instead of a local fast-forward. Three exec
groups (limit 1 each) and two type routes do it, besides the integrators.

An epic is bound to one pull request, not to its branch's name, which a
fork's pull request can carry too. integrate-epic stores the binding in the
epic's metadata when it opens the PR: `pr_url`, `pr_number` and
`pr_head_repo` (owner/name of the repository the head branch is in). No
script looks a PR up by branch: pr-epic and pr-merge read the bound one
and compare what GitHub says of it (number, head repository, head branch)
with the binding; a difference holds the epic, untouched. An epic bound to
none reports `failed`, to `open`, where integrating binds it.

What is merged is the reviewed commit and no other. As the epic enters
`in-pr`, pr-epic stores the commit it pushed, the branch as the reviewer
approved it with their own commits, in the epic's metadata as
`pr_reviewed_sha`, beside the binding. pr-merge merges with
`--match-head-commit <pr_reviewed_sha>`: a PR whose head is any other
commit, pushed after the review, is not merged, and the epic is held in
`land` with a note naming both shas. CI is read for that commit too
([GitHub events](#github-events)).

- **open**, `yardr-integrators`: after pushing the rebased branch (it needs
  `YARDR_PUSH`; set without it, the epic is held), integrate-epic opens a
  PR for `yard/<epic>`, titled `<epic title> (<id>)`, its body the epic's
  body, a "Children" list (ids and titles) and the trailer `Epic: <id>`; if
  the epic is bound to an open PR already it replaces that PR's body, and a
  bound PR that was closed or merged gives way to a new one. The URL goes
  into the note, and the binding (`pr_url`, `pr_number`, `pr_head_repo`)
  into the epic's metadata. A PR open for the branch that the epic is not
  bound to (opened by hand, say) is not adopted: the epic is held until
  someone binds it (`yardr bead update <epic> --meta pr_url=<url>`; the
  next integration stores the rest) or closes that PR. A push or `gh` that
  fails reports `failed`, back to `open`, so the next integration retries,
  each time after a longer wait (step 2 above); a remote branch with
  commits the epic lacks is held.
- **approved**, `yardr-pr`
  ([`contrib/refinery/pr-epic`](../contrib/refinery/pr-epic), with
  `YARDR_PR=1`; held without it): pushes
  `yard/<epic>`, which may carry the reviewer's commits (with a lease: a
  remote branch with commits of its own is never overwritten), replaces the
  PR's body with the latest review note under "Review", stores the pushed
  commit as `pr_reviewed_sha`, and reports `done`,
  to `in-pr`. No bound PR, a refused push, or a PR GitHub reports as
  conflicting with the base reports `failed`, to `open`, for the integrator.
- **in-pr**, `decisions` (members mckean, yardmaster): a manual stage. Look at
  the PR and its checks; `yardr bead done <epic>` means "merge it" (to
  `land`), `--outcome changes` sends it back to `open` (add the fix as a
  child first), `--outcome question` to `decide`. The daemon does this
  itself when the required checks of the reviewed commit are green
  (below).
- **land**, `yardr-pr-merge`
  ([`contrib/refinery/pr-merge`](../contrib/refinery/pr-merge), with
  `YARDR_PR=1`; held without it): runs `gh
  pr merge <pr_url> --rebase --delete-branch --match-head-commit
  <pr_reviewed_sha>`, then `git fetch` and `git
  merge --ff-only <remote>/<base>` in the rig's checkout, and reports
  `done`, to `merged`, which closes the epic. A PR whose head is not the
  reviewed commit, when pr-merge looks or when GitHub refuses the merge for
  it, holds the epic, unmerged: bring the new commits into the epic's
  branch and send it back to `open` to have them reviewed, or put the
  remote branch back at the reviewed commit and unhold. An epic with no
  `pr_reviewed_sha` reports `failed`, to `open`. A merge GitHub refuses
  for another reason
  (checks pending, review required, conflicts) reports `failed`, to `open`,
  with gh's message, once. The checkout must be on the base and clean, or
  the epic is held before anything is merged.

### GitHub events

With a github hook ([subscriptions.md](subscriptions.md#github)) on the
repository, the daemon moves an epic on what GitHub reports about its pull
request, so a PR that is green and approved waits on no one. It reads every
`hook` event from the github adapter whose `epic` field (the head branch is
`yard/<epic>`) names an open epic, in seq order, and applies four rules to
those that are about the pull request the epic is bound to (below). A
held epic is a human's: every rule leaves it where it is (the event is
judged, and logged as ignored because the epic is held) until it is
un-held, after which the next event moves it; its CI is not read while it
is held, and is read when it is un-held.

1. **CI, in `in-pr`** (`ci`): not a rule on an event. CI is state, read
   from GitHub, and an event only has it read again (see "CI is read as
   state" below).
2. **CI red, in `review`** (`ci_red`): a `workflow_run` or `check_suite`
   completed with conclusion `failure` or `timed_out` on the head, epic in
   `review`: it adds a child "Fix CI on epic <id>: <workflow>" (type bug,
   in `backlog`, the run's URL and conclusion in its body), which blocks
   the epic; the epic stays in review. There is one fix child per head
   commit, its handle `<epic>-ci-<sha7>`: a second failed run or suite on
   the same commit adds none. A `cancelled` run or suite is not red:
   someone, or a newer push, stopped it, so it says nothing about the
   commit.
3. **PR merged** (`pr_merged`): `pull_request` `closed` with `merged`
   true. From `land` the epic is reported `done`, to `merged`; from `in-pr`
   (merged by hand) it goes `land` then `merged`; from `approved` by the
   bare edge to `merged`. An epic in `land` whose yardr-pr-merge session is
   still running is left to it, as it has the rig's checkout to fast-forward.
   Merged by hand, the checkout catches up at the next fetch. **PR closed
   unmerged** (`pr_closed`): an epic in `in-pr` or `review` goes to `decide`
   with the question "PR closed unmerged: reopen, or drop the epic?".
4. **Changes requested** (`changes_requested`): a `pull_request_review`
   with state `changes_requested` by a human (a login ending in `[bot]` is
   not one; the yard posts no reviews, as pr-epic writes its review into the
   PR's body), epic in `review`, `approved`, `in-pr` or `land` (with no session):
   the review goes into a note and the epic to `open`, where the mayor files
   the fix as a child. Comments (`issue_comment`) are not the daemon's: a
   resident subscribed to github with `--scope mentions` gets them.

For `ci_red`, the head commit is the one the last `pull_request` event
(opened, synchronize, ...) or review for the epic carried, kept in the
epic's metadata as `pr_head`; without one the daemon asks `gh pr view` for
`headRefOid`. A failure on another commit (a run a later push superseded)
is ignored. `gh` is asked about the bound pull request, by its URL or
number, never by the branch.

**CI is read as state.** For an epic in `in-pr` the daemon takes no
conclusion from an event. It asks GitHub (`gh api`) about the reviewed
commit, `pr_reviewed_sha`: the pull request's head and base, the checks
the base branch requires (its protection's required status checks and
those of the rulesets that apply to it), and the check runs and commit
statuses on that commit, the latest of each name. Then:

- **green**, every required check is there and concluded `success`
  (`neutral` and `skipped` count as GitHub counts them): the epic goes to
  `land` with the note "CI green on <sha>: <checks> (the checks <base>
  requires)", and yardr-pr-merge merges it, in the same pass;
- **red**, a required check concluded `failure`, `timed_out` or
  `startup_failure`, also while others still run: a child "Fix CI on epic
  <id>: <checks>" as above (one per commit), and the epic goes to `open`;
- **pending**, a required check is queued or running, was `cancelled`,
  waits for someone's approval, or has not been reported at all: nothing
  moves. One green workflow among several required ones is pending.

It asks when it first sees the epic in `in-pr`, so CI that finished while
the epic was under review, or while the daemon was down, is seen without
any new run; after that, a `workflow_run` or `check_suite` delivery of any
action or conclusion, a `pull_request` event or a review about the bound
pull request has it ask again, as does any change to the epic (a note, an
un-hold). Between those nothing is polled. A read GitHub does not answer
is tried again after 30s, doubling up to an hour.

Checks that are not required are not counted, failed or not. A base that
requires no checks has nothing the daemon can call green (a workflow that
has not reported yet cannot be told from one that does not exist), so the
epic stays for a human; a failed check on the commit still sends it back
to `open`. A pull request whose head is no longer the reviewed commit is
left alone: pr-merge would not merge that head. An epic with no
`pr_reviewed_sha`, or bound to no pull request, is not asked about. A
required check is matched by its name, whichever app reports it.

The branch name only finds the epic; the bound pull request (`pr_url`,
`pr_number`, `pr_head_repo`, which integrate-epic stores) decides whether
the event counts. An event is ignored, logged as such, and sets no
`pr_head` when it is in another repository than the PR's, names another
pull request number, or has its head in another repository than
`pr_head_repo`: a fork's pull request from a branch of the same name has
its own number and head repository, and a workflow run on a fork's branch,
for which GitHub lists no pull request, still names the fork as its head
repository. A merged event for a number the epic is not bound to therefore
does nothing. An epic bound to no pull request is moved by no event, and
one bound before the number and head repository were stored (`pr_url`
alone) is held to the URL's repository and number, its head taken to be in
that repository.

A delivery reaches the rules once. The hook listener drops, before any
event is logged, a delivery whose body the hook has recorded (by its
sha256) or whose `X-GitHub-Delivery` id it has: the id is a header the
signature does not cover, so a captured delivery sent again under a new id
is caught by its body, which is signed. Both keys are written in the
transaction that records the delivery
([subscriptions.md](subscriptions.md#redeliveries)).

Each rule acts only on the stage it names: one that
finds the epic elsewhere, past it or not there yet, does nothing. Each event
judged for an epic is recorded in its metadata (`github_seq`), so a daemon
that restarts and reads the log again from the start does nothing twice.
Every action, every event ignored because of the stage, and every refused
move is logged as a `github_rule` dispatch event on the epic, with the rule,
epic, sha and the hook event's seq. Every read of an epic's CI is logged
the same way, as rule `ci` with no seq: what it did (`green: in-pr ->
land`), or why it did nothing (`pending: waiting for <checks>`).

The daemon acts as `yard`, so the group holding `in-pr` must list it among
its members (below); otherwise its moves out of `in-pr` are refused, and the
`github_rule` event says so. A repository should make every workflow that
must pass a required check of the base branch: those are what the daemon
waits for, and what GitHub refuses the merge without (pr-merge then
reports `failed`, to `open`, and the next integration tries again).

`gh` (logged in as the operator) and `jq` must be on the scripts' PATH.
Without `YARDR_PR`, the default, route `approved` for epics to
`yardr-refinery` (or leave the stage route alone) and nothing else changes:
merge-local lands the epic locally and advances it to `merged`, the bare
edge kept for it.

### Setup

```sh
install contrib/refinery/integrate-epic ~/.yardr/scripts/   # from main
yardr rig flow yardr --type epic yardr.epic                  # the repo's .yardr/flows/yardr.epic.flow
yardr group add yardr-integrators --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/integrate-epic' \
  --set log="$HOME/.yardr/logs/integrator.log"
yardr route add --stage open --type epic --rig yardr --group yardr-integrators --priority 10
```

Through pull requests (experimental, see above), every cmd on the path
carries `YARDR_PR=1`:

```sh
install contrib/refinery/pr-epic contrib/refinery/pr-merge ~/.yardr/scripts/   # from main
yardr group add yardr-integrators --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin YARDR_PR=1 ~/.yardr/scripts/integrate-epic' \
  --set log="$HOME/.yardr/logs/integrator.log"
yardr group add yardr-pr --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin YARDR_PR=1 ~/.yardr/scripts/pr-epic' \
  --set log="$HOME/.yardr/logs/pr.log"
yardr group add yardr-pr-merge --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin YARDR_PR=1 ~/.yardr/scripts/pr-merge' \
  --set log="$HOME/.yardr/logs/pr-merge.log"
yardr route add --stage approved --type epic --rig yardr --group yardr-pr --priority 10
yardr group add decisions --runner manual --limit 50 --members mckean,yardmaster,yard   # yard: the daemon, for the github rules
yardr route add --stage in-pr --type epic --rig yardr --group decisions
yardr route add --stage land --type epic --rig yardr --group yardr-pr-merge
```

The epic flow's stage file for review (`.yardr/flows/yardr.epic/review.md`,
shown in the reviewer's brief) says how an epic is reviewed as a whole, and
how it is reviewed on its pull request: the
reviewer pushes its fix-forward commits to `yard/<epic>` with a lease (the
one branch any role pushes; the merger does not push even that one, the
integrator pushes its rebase), and posts its review on the PR. GitHub
refuses `--approve` and `--request-changes` from the login that opened the
PR, which is the operator's, the same login the reviewer's `gh` uses: on
its own PR the reviewer posts a `--comment` with the verdict in the note's
first line, and the yard learns the verdict from `yardr bead done`.
