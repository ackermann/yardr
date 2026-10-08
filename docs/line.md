# The line: how a bead moves in practice

yardr builds no line in: stages, groups, routes and a flow compose one.
This page follows a bead down the line yardr itself is built with
(depot yardr, as of 2026-10-03). Its flows are in [flows.md](flows.md#worked-example-depot-yardr).

```
backlog ──yardmaster shapes──▶ new ──builder──▶ review ──Codex──▶ approved ──assembly──▶ merged on main
   ▲                       ▲                 │                  │
   │                       └──── changes ────┘                  │
   │                       └────────── red check or conflict ───┘
   └──────── decide (Christopher) ◀── a question from backlog, new, review or approved
```

## 1. Backlog: the yardmaster shapes the bead

New ideas land in stage `backlog`, routed to the manual group `backlog`. The
yardmaster (see [crew.md](crew.md), prompt this repo's
`.yardr/roles/yardmaster.md`) reads them and shapes each one until a builder
could finish it without asking: a goal, where to look, what "done" means and
how to test it. Large beads are split, and `yardr dep add` orders them: a bead
is only ready once its blockers are closed.

A shaped bead is advanced to `new` (`yardr bead advance <id> --to new`).
Christopher can do the same from `yardr review -g backlog`.

### Cycles of blockers

`yardr dep add` refuses a `blocks` edge that would close a cycle, and names
it in order, the new edge first: `yardr-a blocks yardr-b blocks yardr-c
blocks yardr-a`. An open cycle leaves no bead ready, and nothing would say
why. The child's edge that `dep add -k parent` adds ([Trains](#trains)) is
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

**A move refused to an agent is logged.** A move that works ends the
agent's session, so an agent whose `yardr bead done` was refused sits idle
with "done" as its last word, and the bead shows nothing of it. The store
therefore logs `move_refused` on the bead when it refuses `done`, `advance`,
`close`, `hold` or `release` to the agent of a session (the session the
move was checked as: the pane's, else the one `YARDR_SESSION` names):

| field | |
|---|---|
| `reason` | `stale` (the session no longer owns the bead, or the bead changed since `--revision`), `not_allowed` (the flow has no such move or outcome for the stage, or a part has no train), `not_member` (the stage's group has members and the agent is none), `not_found` (no such bead), `other` (anything else that failed the move: a flow that does not resolve, say) |
| `command` | the move as `yardr` words, with the bead as the agent named it: `yardr bead done <id> --outcome shipped` |
| `error` | what the agent was told |
| `session`, `agent`, `group`, `stage` | whose session it was and the stage it was started for |
| `session_bead` | the session's own bead, when the move named another |

It is on the bead the move named, or on the session's own for `not_found`,
and at most once per bead, session and reason, so an agent that retries
says it once. A person's or a crew member's refused command logs nothing (they
are in no session and read the error where they typed it), nor does a
manual group's claim. It is in a new crew member's default `--on`, so the
yardmaster hears it; `yardr inbox` leads its row with `reason`, `command` and
`error`.

What never reaches the store is not logged: a command `yardr` itself
refuses for its shape (`advance` without `--to`, a missing id, an unknown
flag), one that finds no server, and one the harness never ran (a
permission prompt it waits on, a hook that denied it). The first two print
their error in the pane, which `session_idle` carries as `tail`. A
permission prompt raises `session_blocked` when the agent's status turns
to it, and that event carries the end of the pane as `tail` too (its last
20 lines, as `session_stalled` and `session_harness_error` do): the
question the agent waits on is in `yardr inbox`, with no `herdr agent read`
to get it.

The builder implements the change, commits on its branch (never pushes or
merges), runs the light gate on the last commit (`yardr depot check yardr
--light --dir <worktree>`, which runs `.yardr/check-light` from fetched
`origin/main`: `gofmt`, `go vet`, `go build`, then `go test -race` on the
packages the branch changed and their importers), leaves a note with what it
changed and verified, ending in a gate line (the command, the commit, the
result with counts), and reports `yardr bead done`, which the flow sends to
`review`.

### One full gate per bead

The full gate (`.yardr/check`: `go test -race` on every package, then the
store, dispatch and bdp suites again over the socket) runs once per bead, in
the assembly, on exactly the commit that lands (section 4). Until 5 Oct 2026
it ran three times, for the builder, the reviewer and the assembly: about 16
of a bead's 37 minutes at the median. The builder now runs the light gate,
and the reviewer the light gate or none: none when the builder's gate line
names the commit under review and the reviewer committed nothing.

What that costs is that some faults are now found one stage later, in the
assembly, where a red gate takes a slot of the serial queue and sends the
bead back to a builder. They are the faults depot yardr's light gate cannot
see:

- a test that reads a file by path, when the table in `.yardr/check-light`
  leaves it out. `go list` knows what a package imports and embeds, not
  what its tests open, so the script has a table of path to packages
  (the starter pack runs `internal/pack` and `internal/cli`, whose tests
  read it; `.yardr/flows/` runs the four packages that read the repository's own
  flows), and a changed file that is neither Go, embedded nor in the table
  runs every package. What is left is a row that names too few packages;
- a test that reaches code by building the binary instead of importing it
  (`contrib/assembly`, `contrib/isolated-yard`, the ask harness);
- store, dispatch or bdp behaviour that differs over the unix socket: those
  suites run only in the full gate;
- a test in a package the branch neither changed nor is imported by, which
  fails for a reason no import shows (shared state on disk, timing under the
  load of the whole run).

For these the builder runs the full gate itself
([`.yardr/roles/builder.md`](../.yardr/roles/builder.md), step 3). An edit
to `.yardr/check` or `.yardr/check-light` is seen by no gate at all, before
or after this: both are read from `main`. A reviewer's `review:` commits get
the light gate too, so a fault in one of them is the assembly's to find.

No more than two full gates run at once on the machine (the yard's
`gate_slots`), whoever starts them: a third `depot check` says `2 gates are
running on this machine; waiting for a slot` and starts when one ends. The
wait is not part of the check's timeout
([configuration.md](configuration.md#gate-slots-how-many-gates-run-at-once)).

### What the gate does not catch

The gate's script comes from the base, so a branch cannot change what is
run, but it runs in the branch's worktree, against the branch's code and
the branch's tests. A branch that deletes a test, skips it or makes it
trivially true passes the gate vacuously. That is the accepted model for
now: the reviewer reads the diff and is the one who catches a weakened
test, not the gate. The builder's light gate and the assembly's full gate
are the same in this.

## 3. Review: Codex reviews forward

`review` is routed to `yardr-reviewers`: Codex in the same worktree, with
the reviewer prompt from this repo's `.yardr/roles/reviewer.md` (how a
reviewer works) and the stage file `.yardr/flows/default/review.md` (when
each outcome applies). It is the last review
before merge, so it fixes what it can on the branch (commits starting
`review:`) rather than only reporting, tries the change, runs the light gate
if it committed or the builder's gate line is not for the commit under
review (the full gate is the assembly's), and then:

- **approves**: a note with what it checked and fixed, then `yardr bead
  done` (to `approved`);
- **sends back**: concrete findings, then `--outcome changes` (to `new`);
  the bead returns to
  the builders, in the same worktree. After two send-backs it holds the bead
  instead of a third, so the yardmaster looks;
- **asks**: a question only Christopher can answer goes to `decide`
  (`--outcome question`).

In a flow with a `merge` stage (section 5; depot yardr's has none), when a
merger sends a bead back for a `merge re-review`, the reviewer records
the old tip from the merger's note and runs
`git range-diff <base>...<old-tip> <base>...HEAD`. It checks only the
conflict-resolution changes: that both sides' intent was kept, the merger's
note names every conflicting file, and the gate result is credible. It does
not redo the original feature review. If the resolution is sound, it notes
the range-diff and advances to `approved`; otherwise it sends the bead to
`new` or `decide` with a concrete finding.

## 4. Approved: the assembly lands it on main

yardr takes no position on how work lands: a PR per bead, a shared PR
several beads funnel into, or a direct merge. The assembly is not built in;
it is a composition. A stage (`approved`) is routed to an `exec` group whose
script does the landing, and `--limit 1` on the group makes it a serial merge
queue. yardr provides the mechanism and ships one reference script,
[`contrib/assembly/merge-local`](../contrib/assembly/merge-local); depot yardr
uses it to merge directly to `main`.

**The mechanism.** The exec runner starts the script in the depot with
everything it needs in its environment ([groups.md](groups.md#runners)):
`YARDR_BEAD`, `YARDR_DEPOT`, `YARDR_DEPOT_PATH`, `YARDR_BASE`, `YARDR_BRANCH`
(`yard/<bead>`), `YARDR_WORKTREE`, `YARDR_BIN` (this yardr), and
`YARDR_ACTOR` set to the group. The assembly runs the gate itself, whatever
the builder reported: `yardr depot check "$YARDR_DEPOT" --dir
"$YARDR_WORKTREE"`, i.e. `.yardr/check` as it is on the base (the fetched
`origin/main` when the base has a remote), or the depot's `--check`; a depot
with neither fails with "no gate" unless it allows that
([configuration.md](configuration.md#the-merge-gate-yardrcheck)). Its gate takes a
[gate slot](configuration.md#gate-slots-how-many-gates-run-at-once) like any
other, and goes before the builders' and reviewers' gates that wait for one
(`YARDR_GATE_FIRST=1`); with none free for 30 minutes the gate could not run
(exit 2), and the bead is held, not sent back. The script finishes by moving the bead, with a note:
`yardr bead advance "$YARDR_BEAD" --to <stage> --note -`. A script that
exits without advancing, closing or holding counts as a failed session, like
an agent that gives up, and after 3 of those the bead is held. Advancing ends
the session and the daemon stops the script, so it is the script's last step.

**merge-local.** For each bead it:

1. rebases `yard/<bead>` onto the base in the bead's worktree, if the base
   moved; on a conflict it aborts the rebase and advances to `merge` with the
   conflicting files (section 5), or to `new` if the flow has no `merge`,
   as in depot yardr;
2. runs the gate on exactly what will land, every time, including after a
   merger's rebase, and records the commit it ran on; red (exit 1) reports
   the outcome `failed` (to `new`; to `open` for a train), or advances to
   `new` in a flow without outcomes. The note starts with the branch, the
   commit the gate ran on, the base and the file `depot check` kept the whole
   output in (`<home>/gates/<depot>/<bead>-<time>.log`); then come the lines
   of the output matching the depot's `.yardr/check-fail-pattern`, each with
   the lines around it, when the depot has one, and then the last 200 lines
   ([configuration.md](configuration.md#a-red-gates-output-the-file-and-yardrcheck-fail-pattern)).
   The builder reads it with `yardr prime --bead <id>`, which shows every
   note whole. A gate that could not run at all (exit 2: git, the base, the depot) holds
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
   whatever state the depot's checkout is in: keep the base checked out
   nowhere and a dirty checkout never blocks the queue;
4. pushes the base if `YARDR_PUSH` names a remote (off by default, never
   forced). A failed push leaves the bead merged; the next landing's push
   carries it, and `YARDR_NOTIFY` (default the yard's `yardmaster` setting, else
   `yardmaster`) is mailed;
5. advances to the terminal stage `merged` with the note `merged <sha>`,
   which closes the bead; in a flow without `merged` it closes the bead with
   that reason.

It never forces, and a base that is not a local branch (`origin/main`) is
held: merge-local lands only on local branches.

### Setup

```sh
install contrib/assembly/merge-local ~/.yardr/scripts/    # from main
yardr group add yardr-refinery --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin YARDR_NOTIFY=yardmaster ~/.yardr/scripts/merge-local' \
  --set log="$HOME/.yardr/logs/refinery.log"
yardr route add --stage approved --group yardr-refinery --depot yardr
```

The group is `yardr-refinery` because an assembly was called a refinery
when it was made, and a group keeps the name it was given; in a new yard it
would be `yardr-assembly`.

The script is installed from `main` rather than run from a checkout: a bead
branch must not change how it is landed. Without a checkout of yardr, the
starter pack carries the same script: `yardr depot init <depot>`,
which is what `yardr setup` runs, installs it as
`~/.yardr/packs/starter/scripts/merge-local` and adds the group
`<depot>-assembly`, without `YARDR_PUSH` ([setup.md](setup.md)). The flow gains `merged`:

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

Depot yardr does not run this stage as of 2026-10-03: its flow has no
`merge`, so a conflict goes back to `new`. The depot it was before the rename
did, and the merger's role is still in the repository
(`.yardr/roles/merger.md`); this section is how the stage works where it is
set up.

Two of the first 22 landings conflicted with what had landed since their
branch was made. Sending those back to `new` cost a full builder and Codex
cycle for what is usually a mechanical rebase, so a conflict goes to `merge`
instead.

`merge` is routed to `mergers`: a herdr group running Claude Code (Sonnet,
at most one at a time) with `prompt=roles/merger.md`. The merger reopens the
bead's worktree on `yard/<bead>` (the assembly aborted its rebase, so it is
clean) and:

1. rebases onto the depot's base;
2. resolves each conflict keeping the intent of both sides, from the bead and
   its notes on one side and the base's commits on the file
   (`git log -p <merge-base>..main -- <file>`) on the other;
3. runs the gate, `yardr depot check yardr --dir .`;
4. leaves one `merge re-review:` note naming the old tip, each conflict and
   its resolution, and reports `yardr bead done`, which the flow sends to
   `review`.

The reviewer then checks only the resolution with a range-diff and advances
the bead to `approved`. The assembly picks it up again: `main` no longer
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

The yardmaster would wire this in the store; nothing in `~/.yardr` is versioned:

```sh
yardr group add mergers --runner herdr --limit 1 \
  --set kind=claude --set model=sonnet --set prompt=roles/merger.md \
  --set args='--permission-mode auto'      # as yardr-builders: it works unattended
yardr route add --stage merge --group mergers --depot yardr
```

The flow gains two rules (the rest of [`default.flow`](flows.md#worked-example-depot-yardr)
is unchanged), with outcomes so the merger reports how the merge ended
instead of naming stages:

```
approved -> merged on done | new on failed | merge on conflict | decide on question
merge -> review on done | new on rework | decide on question
```

and the assembly sends a conflicting bead to `merge` (merge-local does; it
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
- by the assembly, when it cannot land without touching a dirty tree (the
  base checked out with uncommitted changes, or the bead's worktree dirty).

The yardmaster watches held beads, finds out why, and either fixes the cause and
unholds, reshapes the bead, or sends it to `decide`.

Not every failure is the bead's. If herdr is missing or down, no bead is
held for it: the group logs one `runtime_down`, backs off (30s, doubling to
5m) and its beads wait untouched until a start works again (`runtime_up`).
A session whose agent does nothing with its first prompt (a model it may
not use, no login) raises `session_stalled` after 2m, with the end of its
pane, and its group starts no more sessions with that model; one whose
agent reports another model than asked raises `session_model_mismatch`
([groups.md](groups.md#models)). One whose harness stops on an error of its
own later (the model at capacity, a usage limit, a login that ran out)
raises `session_harness_error` on the first pass that finds it idle, with
the reason and the end of its pane (a model at capacity in answer to the
first prompt is given the prompt again first, 3 times 5 minutes apart, and
raised when that did not help)
([groups.md](groups.md#a-harness-that-stopped-on-an-error)). An agent whose
move the yard refused raises `move_refused`, with the command and why
([above](#2-new-a-builder-implements-it)). A session that
sits idle 30m or runs 4h
raises `session_idle` (with the end of its pane) or `session_aged` in the
yardmaster's inbox (one waiting
for its own `yardr depot check` is not idle, and `yardr session list` shows
the gate); with a group `grace` set it is then
stopped, and without one a session that has run twice its `max_age` is
stopped anyway and the bead released with a note; see
[configuration.md](configuration.md#session-limits-and-outages).

## Decide

`decide` is routed to the manual group `decisions`. Backlog, new, review and approved may
send a bead there with the question and the options written into the bead. Christopher
answers in `yardr review -g decisions` (or with a note and
`yardr bead advance`), and the bead goes back to `backlog` or `new`.

Decisions that are not about one piece of code are beads created straight
into `decide` and closed with the answer as the reason.

## Working a bead

The same commands for humans and agents alike:

```sh
yardr prime --bead webapp-3                  # bead, edges, notes
yardr bead note webapp-3 "tests green: ..."  # evidence
yardr bead advance webapp-3 --to review      # hand to the next stage
yardr bead advance webapp-3 --to new --note "rename the flag"  # note and move in one write
yardr bead close webapp-3 --reason "merged"
yardr bead hold webapp-3                     # stop routing; a human looks
```

Every command takes `--json`. Set `YARDR_ACTOR` so the event log shows who
acted.

### Dependencies

Work that spans several beads and must land as one unit is a
[train](#trains). Work that can land bead by bead is a chain instead:
`yardr dep add <blocker> <bead>` orders it.

A dependency's kind is a free word. Only `blocks` (the default) keeps a bead
from being ready, and only `parent` makes a child of a train; any other kind
is a record. One has a meaning of its own: `discovered-from`, from a bead to
a bead that was found while working on it. `yardr bead create "<title>"
--discovered-from <bead>` makes the bead and the edge in one write (the
origin may be open or closed, in any depot; an unknown one creates nothing),
and the new bead inherits nothing from it. `yardr bead show` says
"discovered from" on the one and "led to" on the other,
`yardr bead list --discovered-from <bead>` lists what was found from a bead
(`-a` for the closed too), and `yardr dep rm <origin> <found> -k
discovered-from` removes it. It never affects `yardr ready`, routing, a
train's children or what lands.

### Bead history

```sh
yardr bead history webapp-3                  # when, who, what; a diff under each change of the body
```

A bead holds what it is now. What it was is in the event log and nowhere
else: an `updated` event carries the previous value of every field it
changed (`was`), the body among them. `yardr bead history <id>` prints the
bead's events, the oldest first, and the bead's page of the web view shows
the same.

The history lives as long as the events do. `yardr store prune`, which the
daemon runs every hour, removes events older than `YARDR_EVENT_RETENTION`
(90 days), of open beads as of closed ones, and the old titles and bodies
on them go too: there is no second copy and no "last N versions". Set the
retention to `off` to keep every version for good
([configuration.md](configuration.md#retention-keeping-yarddb-bounded)).

### Bead ids

Every bead has two names. Its **handle** (`webapp-k3f9`: the depot, or
`--prefix`, then random base32) is what yardr prints and what branches
(`yard/<handle>`), worktrees and herdr labels are built from; it is chosen
once and never changes. Its **UUID** (a UUIDv7) is its identity: the store
is keyed by it, so ids need no central counter. `--json` shows both
(`id` is the handle, `uuid` the UUID); `--id` on create picks the handle.

Commands accept the UUID, the handle, or a unique prefix of the handle
with at least 4 characters after the `-` (`webapp-k3f`... is too short; an
ambiguous prefix lists its candidates).

A UUIDv7 starts with its creation time, so the store's key order is
creation order, and listings use it. Handles are random and carry no
order: never sort by handle.

## Trains

A train is work that spans several beads and must land on main as one
unit. It is a bead of type `train`; its children are the beads with a
`parent` edge from it:

```sh
yardr bead create "Trains" -t train -d yardr
yardr dep add <train> <child> -k parent
```

- **Branch(b)** is `yard/<id>` for every bead; the train's branch is
  Branch(train).
- **BaseOf(b)** is Branch(parent train) for a child of a train, and the depot's
  base for everything else, the train itself included. `yardr bead show`
  prints it as `base` (and `train`, and a train's `children`; the same
  fields are in `--json`).

A child goes through the same flow, groups and gate as any bead; only its
base differs. When a child is first started and the train's branch does not
exist yet, the herdr runner creates it from the depot's base tip (fetched
first when the base is a remote's) and logs `train_branched` on the train
with the commit. An existing train branch is never recreated or moved. The
child's worktree is branched from the train's branch, and `YARDR_BASE` in
the agent's and the assembly's environment is BaseOf(b), so merge-local
rebases the child onto the train's branch, lands it there and pushes that
branch; main does not move. The gate is still read from the depot's base
(main), not from the train's branch, so neither a train nor its children can
change the bar they land under; it runs on the child's branch as before.

`dep add -k parent` refuses a parent that is not a train, a closed train
(it has landed, or was dropped: a child added now would land on a branch
nothing integrates again), a child already in another train, a train as a
child (trains do not nest) and a child in another depot. Only parent edges
from a train count: a bead whose parent is not a train keeps the depot's
base. A bead that is a child of two trains
anyway (a type changed to `train` later) is not dispatched; yardr logs
`bad_train` until one parent edge is removed.

`yardr bead list`, `yardr ready` and `yardr session list` show each
bead's train in a TRAIN column after the bead (empty for a bead with no
train), and their `--json` rows carry it as `train`. `yardr bead list --train
<train>` lists only that train, first, and its children. In herdr, a child's
workspace is labelled `<group> [<train>] <bead>` (e.g. `builders [sv6b]
2cet` for `yardr-2cet`) and any other bead's `<group> <bead>` (`builders
cbdc`): the workspace sits under the depot's node, so the depot prefix is
trimmed from the ids and the group, and the group goes by its `label`
config when set ([groups.md](groups.md#herdr-setup)). The workspace is
reused across stages and relabelled with the current group on each; each
stage's tab mirrors it with the stage in front, `<stage> [<train>] <bead>`
(`review [sv6b] 2cet`).

`dep add <train> <child> -k parent` also adds `<child> -blocks-> <train>`, so
one command makes the child and keeps the train from being ready until the
child closes; `dep rm <train> <child> -k parent` removes both edges.

### Wagons

A wagon is a child of a train that takes a shorter road to the train's
branch. It is for several pieces of one feature, built in parallel, that
are not worth having on main one by one: each piece would otherwise pay
for the full gate three times (builder, reviewer, assembly) and a review of
its own, and the assembly lands one bead at a time. A wagon pays for a light
gate, twice, and no review; the full gate and one review happen once, on the
whole train, before main moves. Everything about the train itself is as above
and below: nothing changes in what a train needs to reach main.

```sh
yardr bead create "Trains" -t train -d yardr
yardr bead create "The parser" -t wagon -d yardr
yardr dep add <train> <wagon> -k parent
```

- **The type.** A wagon is a bead of type `wagon`. In depot yardr the type has
  a flow of its own, [`.yardr/flows/type/wagon.flow`](../.yardr/flows/type/wagon.flow):
  the depot's flow without `review` (`new -> approved on done`). The depot's
  routes for `new`, `approved` and `decide` carry no type, so wagons go to the
  same builders and the same assembly as any bead.
- **The light gate.** A depot's second gate, `.yardr/check-light`, read from
  the base and run by `yardr depot check <depot> --light` under every rule of
  the full one ([configuration.md](configuration.md#the-light-gate-yardrcheck-light)).
  Depot yardr's formats, vets and builds everything and tests, with the race
  detector, only the packages the branch changed and the packages that
  import them. The builder runs it before `done`, as the builder of any
  bead does; merge-local runs it again on exactly what lands, where any
  other bead gets the full gate.
- **What a wagon's light gate is compared with.** Its train's branch, not the
  depot's base: "what the branch changed" is what this wagon changed, not the
  train's earlier wagons again. `depot check --light` finds the wagon from the
  branch its `--dir` is on (`yard/<wagon>`) and gives the gate the tip of
  `yard/<train>`, for the builder as that branch is locally, for merge-local
  as it has just rebased the wagon onto it; the first line of the output
  says so (`light gate, changes since yard/<train> at <sha>`). The gate is
  still read from the depot's base, and the train's full gate runs everything
  before main moves
  ([configuration.md](configuration.md#the-light-gate-yardrcheck-light)).
- **Which gate merge-local runs.** The exec runner tells a script the
  bead's type and its train (`YARDR_TYPE`, `YARDR_TRAIN`, the latter empty for
  a bead with no train). merge-local runs `depot check --light` only when all
  three hold: the type is `wagon`, there is a train, and the base it lands on
  is that train's branch. Everything else gets the full gate, as before: a
  task that is a child of a train, and a wagon with no train, which would land
  on the depot's base. A depot with no light gate runs its full gate under
  `--light` and says so in the output. A wagon's landing note says
  `merged <sha> on yard/<train> under the light gate`.
- **A wagon needs a train.** The light gate and the missing review are only
  safe where the full gate and a review still stand before main, so yardr
  keeps a wagon with no parent train in the stage its flow starts in
  (`backlog`): `bead advance`, `bead done` and `bead create -s <stage>`
  refuse to put it anywhere else, saying how to give it a train
  (`dep add <train> <wagon> -k parent`) or make it a task
  (`bead update <wagon> --type task`). A wagon that lost its train after it
  moved (`dep rm -k parent`), or that no flow governs, is not started by any
  agent or script: the dispatcher logs `wagon_without_train` once, and only a
  manual group still gets it. It can be taken back to `backlog`.
- **The train pays.** integrate-train rebases the train's branch and runs the
  full gate on it as for any train, and a red gate adds a fix child. The
  train's reviewer is told that wagons were not reviewed or fully gated one by
  one ([`.yardr/flows/yardr.train/review.md`](../.yardr/flows/yardr.train/review.md)),
  and reviews each. So keep a train to about five wagons, which one review
  can hold, and leave single fixes on the normal path.

### The train's own flow

A train follows `yardr.train` ([flows.md](flows.md#worked-example-depot-yardr)).
As of 2026-10-03 depot yardr has the flow but not the groups for it: `open`,
`merge`, `in-pr` and `land` have no route, so a train there waits for a
human until the [setup below](#setup-2) is done. The steps describe the
path with that setup:

1. **backlog**: the yardmaster shapes the train and adds its children, then
   advances it to `open`.
2. **open**: the train waits while any child is open (they block it). When
   the last child closes it is ready, and the route for type `train` in
   `open` sends it to `yardr-integrators`, an exec group running
   [`contrib/assembly/integrate-train`](../contrib/assembly/integrate-train).
   The integrator fetches the base when it is a remote's, rebases
   `yard/<train>` onto the depot's base in the train's worktree, runs the gate
   on it (`yardr depot check yardr --dir <worktree>`, the gate read from main),
   pushes the rebased branch when `YARDR_PUSH` is set (replacing the remote
   branch only when everything on it was in what it rebased, or in a
   recorded old tip; step 5), and reports `done`, to review. A red gate
   reports `failed`, back to `open`, after adding a child in `backlog` to
   fix it ("Fix the gate on train <id>", type bug). The note and the child's
   body carry what merge-local's note does: the branch, the commit, the base
   and the file the whole output is in, then the lines matching the depot's
   `.yardr/check-fail-pattern`, then the gate's tail. The child
   blocks the train, so it is not integrated again until the fix has landed
   on its branch. A rebase that conflicts aborts it and reports
   `conflict`, to `merge`, with the conflicting files and both tips in the
   note (step 5). A gate that could not run (exit 2) or a branch it cannot
   work on (missing, dirty, nothing ahead of the base) holds the train with
   a note. A gate ended by a signal (exit 128+N) is run once more, as
   merge-local does, and holds the train if a signal ends it again; no
   child is added for it.

   A train reported `failed` back to `open` with no child to wait for (the
   push or the pull request failed: GitHub down) is not integrated again on
   the daemon's next pass. Each return to the stage it was in, in a row,
   doubles the wait before the next try, from the daemon's backoff (30s) up
   to an hour, so an outage is retried less and less often rather than on
   every pass; the daemon logs each wait once on the train as a `backoff`
   event, with the count. The count is read from the train's events, so a
   restarted daemon keeps waiting; it starts over when the train enters the
   stage any other way or a human un-holds it.
3. **review**: `yardr-reviewers` reviews the whole diff, `<base>..yard/<train>`:
   the train's brief says it is a train, names its children and gives the
   range. Changes go back to `open` (`--outcome changes`) after adding the
   fix as a child; without one the train would be integrated again unchanged.
4. **approved**: the train lands, through a pull request (below) or
   locally: `yardr-refinery` and merge-local land `yard/<train>` on the depot's
   base exactly as a bead's branch (BaseOf(train) is the depot's base) and
   advance it to `merged` by the flow's bare `approved -> merged` edge. A
   red gate there reports the flow's `failed` outcome, which for a train is
   `open` (merge-local falls back to `new` only for flows without it).
5. **merge**: a conflict between the train's branch and the depot's base,
   from the integrator in `open` or from merge-local or pr-train in
   `approved`, goes to the mergers as any bead's does. The merger rebases
   `yard/<train>` onto the base in the train's worktree, keeping both sides'
   intent, runs the gate, and does not push (the integrator pushes, with a
   lease); the brief says the branch and what happens next. The tip the
   merger starts from is recorded on the train as `old tip: <sha>`, by the
   integrator's conflict note and again by the merger's note (as for any
   bead): the remote still has that tip, which the rebase rewrote, and the
   integrator's lease accepts a remote branch whose commits are all in the
   branch as it was before its own rebase or in a recorded old tip, so the
   resolved branch replaces it. A remote with commits in neither is never
   forced over: the train is held, or mailed about when there is no pull
   request. Its `done` sends the train back to `open`, not to review: with
   no child open it is ready, so the integrator takes it again, rebases it
   (a no-op unless the base moved again), gates it, pushes it and opens or
   updates its pull request, and the train goes to review as what will
   land. `rework` also goes to `open` (add the fix as a child first),
   `question` to `decide`.

### Through a pull request

**Experimental, and off by default.** A train lands locally (step 4 above)
unless `YARDR_PR=1` is in the cmd of every group on this path: the
integrators, `yardr-pr` and `yardr-pr-merge`. Without it integrate-train opens
no pull request, and pr-train and pr-merge hold the train with a note naming
the switch, before pushing, merging or asking GitHub anything, so a stray
route or a move by hand does not start the path: nothing reports `done` in
`approved`, and the train reaches `merged` only by merge-local's bare edge.

Do not switch it on for a repository others can open pull requests
against while it is experimental. Its known limits:

- **Green is the base branch's required checks.** The daemon lands a
  train when every check the base requires (branch protection or a
  ruleset) is green on the reviewed commit
  ([GitHub events](#github-events)). A base that requires none is never
  green to it: a human says merge.
- **A commit status sends no event.** A required check an external CI
  reports as a commit status is read with the rest, but only
  `workflow_run` and `check_suite` deliveries have the state read again;
  a train waiting on a status alone waits for a human, or for the next
  event about its pull request.

With `YARDR_PR=1` set, a train goes to main as a pull
request against the depot's base instead of a local fast-forward. Three exec
groups (limit 1 each) and two type routes do it, besides the integrators.

A train is bound to one pull request, not to its branch's name, which a
fork's pull request can carry too. integrate-train stores the binding in the
train's metadata when it opens the PR: `pr_url`, `pr_number` and
`pr_head_repo` (owner/name of the repository the head branch is in). No
script looks a PR up by branch: pr-train and pr-merge read the bound one
and compare what GitHub says of it (number, head repository, head branch)
with the binding; a difference holds the train, untouched. A train bound to
none reports `failed`, to `open`, where integrating binds it.

What is merged is the reviewed commit and no other. As the train enters
`in-pr`, pr-train stores the commit it pushed, the branch as the reviewer
approved it with their own commits, in the train's metadata as
`pr_reviewed_sha`, beside the binding. pr-merge merges with
`--match-head-commit <pr_reviewed_sha>`: a PR whose head is any other
commit, pushed after the review, is not merged, and the train is held in
`land` with a note naming both shas. CI is read for that commit too
([GitHub events](#github-events)).

- **open**, `yardr-integrators`: after pushing the rebased branch (it needs
  `YARDR_PUSH`; set without it, the train is held), integrate-train opens a
  PR for `yard/<train>`, titled `<train title> (<id>)`, its body the train's
  body, a "Children" list (ids and titles) and the trailer `Train: <id>`; if
  the train is bound to an open PR already it replaces that PR's body, and a
  bound PR that was closed or merged gives way to a new one. The URL goes
  into the note, and the binding (`pr_url`, `pr_number`, `pr_head_repo`)
  into the train's metadata. A PR open for the branch that the train is not
  bound to (opened by hand, say) is not adopted: the train is held until
  someone binds it (`yardr bead update <train> --meta pr_url=<url>`; the
  next integration stores the rest) or closes that PR. A push or `gh` that
  fails reports `failed`, back to `open`, so the next integration retries,
  each time after a longer wait (step 2 above); a remote branch with
  commits the train lacks is held.
- **approved**, `yardr-pr`
  ([`contrib/assembly/pr-train`](../contrib/assembly/pr-train), with
  `YARDR_PR=1`; held without it): pushes
  `yard/<train>`, which may carry the reviewer's commits (with a lease: a
  remote branch with commits of its own is never overwritten), replaces the
  PR's body with the latest review note under "Review", stores the pushed
  commit as `pr_reviewed_sha`, and reports `done`,
  to `in-pr`. No bound PR, a refused push, or a PR GitHub reports as
  conflicting with the base reports `failed`, to `open`, for the integrator.
- **in-pr**, `decisions` (members mckean, yardmaster): a manual stage. Look at
  the PR and its checks; `yardr bead done <train>` means "merge it" (to
  `land`), `--outcome changes` sends it back to `open` (add the fix as a
  child first), `--outcome question` to `decide`. The daemon does this
  itself when the required checks of the reviewed commit are green
  (below).
- **land**, `yardr-pr-merge`
  ([`contrib/assembly/pr-merge`](../contrib/assembly/pr-merge), with
  `YARDR_PR=1`; held without it): runs `gh
  pr merge <pr_url> --rebase --delete-branch --match-head-commit
  <pr_reviewed_sha>`, then `git fetch` and `git
  merge --ff-only <remote>/<base>` in the depot's checkout, and reports
  `done`, to `merged`, which closes the train. A PR whose head is not the
  reviewed commit, when pr-merge looks or when GitHub refuses the merge for
  it, holds the train, unmerged: bring the new commits into the train's
  branch and send it back to `open` to have them reviewed, or put the
  remote branch back at the reviewed commit and unhold. A train with no
  `pr_reviewed_sha` reports `failed`, to `open`. A merge GitHub refuses
  for another reason
  (checks pending, review required, conflicts) reports `failed`, to `open`,
  with gh's message, once. The checkout must be on the base and clean, or
  the train is held before anything is merged.

### GitHub events

With a github hook ([subscriptions.md](subscriptions.md#github)) on the
repository, the daemon moves a train on what GitHub reports about its pull
request, so a PR that is green and approved waits on no one. What an event
means is not in the daemon: the hook carries a table of what its events say
([links.md](links.md#the-sources-events-move-the-bead)), and each line
names an outcome, which is reported for the train as a session reports one
(`yardr bead done --outcome`). This flow's edges route it. The daemon reads
every `hook` event whose `train` field (the head branch is `yard/<train>`)
names an open train, in seq order, and puts those that are about the pull
request the train is bound to (below) to the table of the hook that
recorded them. A held train is a human's: it is left where it is (the event
is judged, and logged as ignored because the train is held) until it is
un-held, after which the next event moves it; its CI is not read while it
is held, and is read when it is un-held.

The table this flow is written for, set on the yard's hook with `yardr hook
add` (it replaces the hook's definition, so the lines go with the rest of it;
`yardr hook show gh` prints them):

```sh
yardr hook add gh --source github --secret-file ~/.yardr/github.secret \
  --says "event=pull_request action=closed merged=true -> merged: PR merged" \
  --says "event=pull_request action=closed merged=false -> question: PR closed unmerged: reopen, or drop the train?" \
  --says "event=pull_request_review action=submitted state=changes_requested sender=user -> changes: Changes requested on the PR" \
  --says "event=workflow_run event=check_suite action=completed conclusion=failure conclusion=timed_out stage=review -> changes: CI failed on the head of the PR" \
  --says "event=ci conclusion=success stage=in-pr -> done" \
  --says "event=ci conclusion=failure stage=in-pr -> changes"
```

1. **PR merged** is `merged`, which `approved`, `in-pr` and `land` route to
   `merged`: one move, also for a pull request merged by hand, whose
   checkout catches up at the next fetch. A train in `land` whose
   yardr-pr-merge session is still running is moved as any bead is by a
   `done` from outside its session: the session ends with the move, and its
   own `done` afterwards is refused as stale.
2. **PR closed unmerged** is `question`: a train in `review` or `in-pr`
   goes to `decide` with "PR closed unmerged: reopen, or drop the train?".
3. **Changes requested** by a person (`sender=user`: a login ending in
   `[bot]` is a bot; the yard posts no reviews, as pr-train writes its
   review into the PR's body) is `changes`: from `review`, `approved` and
   `in-pr` the train goes back to `open` with the review in the note, where
   its integrators take it up again. Comments (`issue_comment`) have no
   line: a crew member subscribed to github with `--scope mentions` gets them.
4. **CI red, in `review`**: a `workflow_run` or `check_suite` completed
   with conclusion `failure` or `timed_out` on the head is `changes` too,
   and the line holds in `review` only (`stage=review`). A `cancelled` run
   or suite is not red: someone, or a newer push, stopped it, so it says
   nothing about the commit.
5. **CI, in `in-pr`** (`event=ci`): not taken from an event. CI is state,
   read from GitHub, and an event only has it read again (see "CI is read
   as state" below). What was read is put to the table as an event of its
   own, green as `done` (to `land`) and red as `changes` (to `open`).

A stage with no edge for the outcome refuses it: a pull request merged
while its train is in `open` moves nothing, and the train's events say why
(`refused`, with the outcome and the stage). A depot whose train flow names
its stages otherwise gives its hook its own lines, and a hook without a
table moves nothing: each event about a train is then logged as ignored,
with that reason.

For a run or suite, the head commit is the one the last `pull_request` event
(opened, synchronize, ...) or review for the train carried, kept in the
train's metadata as `pr_head`; without one the daemon asks `gh pr view` for
`headRefOid`. A failure on another commit (a run a later push superseded)
is ignored. `gh` is asked about the bound pull request, by its URL or
number, never by the branch.

**CI is read as state.** For a train in a stage the table has an `event=ci`
line for (`in-pr` here; a line that names no stage has every open train's
CI read) the daemon takes no conclusion from an event. It asks GitHub (`gh api`) about the reviewed
commit, `pr_reviewed_sha`: the pull request's head and base, the checks
the base branch requires (its protection's required status checks and
those of the rulesets that apply to it), and the check runs and commit
statuses on that commit, the latest of each name. Then:

- **green**, every required check is there and concluded `success`
  (`neutral` and `skipped` count as GitHub counts them): the event
  `event=ci conclusion=success`, so the train goes to `land` with the note
  "CI green on <sha>: <checks> (the checks <base> requires)", and
  yardr-pr-merge merges it, in the same pass;
- **red**, a required check concluded `failure`, `timed_out` or
  `startup_failure`, also while others still run: the event `event=ci
  conclusion=failure`, so the train goes to `open` with the failed checks
  and the first one's URL in the note;
- **pending**, a required check is queued or running, was `cancelled`,
  waits for someone's approval, or has not been reported at all: nothing
  moves. One green workflow among several required ones is pending.

It asks when it first sees the train in `in-pr`, so CI that finished while
the train was under review, or while the daemon was down, is seen without
any new run; after that, a `workflow_run` or `check_suite` delivery of any
action or conclusion, a `pull_request` event or a review about the bound
pull request has it ask again, as does any change to the train (a note, an
un-hold). Between those nothing is polled. A read GitHub does not answer
is tried again after 30s, doubling up to an hour.

Checks that are not required are not counted, failed or not. A base that
requires no checks has nothing the daemon can call green (a workflow that
has not reported yet cannot be told from one that does not exist), so the
train stays for a human; a failed check on the commit still sends it back
to `open`. A pull request whose head is no longer the reviewed commit is
left alone: pr-merge would not merge that head. A train with no
`pr_reviewed_sha`, or bound to no pull request, is not asked about. A
required check is matched by its name, whichever app reports it.

The branch name only finds the train; the bound pull request (`pr_url`,
`pr_number`, `pr_head_repo`, which integrate-train stores) decides whether
the event counts. An event is ignored, logged as such, and sets no
`pr_head` when it is in another repository than the PR's, names another
pull request number, or has its head in another repository than
`pr_head_repo`: a fork's pull request from a branch of the same name has
its own number and head repository, and a workflow run on a fork's branch,
for which GitHub lists no pull request, still names the fork as its head
repository. A merged event for a number the train is not bound to therefore
does nothing. A train bound to no pull request is moved by no event, and
one bound before the number and head repository were stored (`pr_url`
alone) is held to the URL's repository and number, its head taken to be in
that repository.

A delivery reaches the table once. The hook listener drops, before any
event is logged, a delivery whose body the hook has recorded (by its
sha256) or whose `X-GitHub-Delivery` id it has: the id is a header the
signature does not cover, so a captured delivery sent again under a new id
is caught by its body, which is signed. Both keys are written in the
transaction that records the delivery
([subscriptions.md](subscriptions.md#redeliveries)).

Each event
judged for a train is recorded in its metadata (`github_seq`), so a daemon
that restarts and reads the log again from the start does nothing twice.
Every move (`action`), every move the flow or the stage's group refused
(`refused`), and every event left alone because of the train's hold, its
pull request or a hook without a table (`ignored`) is logged as a
`github_rule` dispatch event on the train, with the hook, the event's type
(`event`), the line it matched (`says`) and its `outcome`, the train, its
`stage`, the sha and the hook event's seq. Every read of a train's CI is
logged the same way, as event `ci` with no seq: what it did (`green: in-pr
-> land`), or why it did nothing (`pending: waiting for <checks>`).

The daemon acts as `yard`, so the group holding `in-pr` must list it among
its members (below); otherwise its moves out of `in-pr` are refused, and the
`github_rule` event says so. A repository should make every workflow that
must pass a required check of the base branch: those are what the daemon
waits for, and what GitHub refuses the merge without (pr-merge then
reports `failed`, to `open`, and the next integration tries again).

`gh` (logged in as the operator) and `jq` must be on the scripts' PATH.
Without `YARDR_PR`, the default, route `approved` for trains to
`yardr-refinery` (or leave the stage route alone) and nothing else changes:
merge-local lands the train locally and advances it to `merged`, the bare
edge kept for it.

### Setup

```sh
install contrib/assembly/integrate-train ~/.yardr/scripts/   # from main
yardr depot flow yardr --type train yardr.train                  # the repo's .yardr/flows/yardr.train.flow
yardr group add yardr-integrators --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/integrate-train' \
  --set log="$HOME/.yardr/logs/integrator.log"
yardr route add --stage open --type train --depot yardr --group yardr-integrators --priority 10
```

Through pull requests (experimental, see above), every cmd on the path
carries `YARDR_PR=1`:

```sh
install contrib/assembly/pr-train contrib/assembly/pr-merge ~/.yardr/scripts/   # from main
yardr group add yardr-integrators --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin YARDR_PR=1 ~/.yardr/scripts/integrate-train' \
  --set log="$HOME/.yardr/logs/integrator.log"
yardr group add yardr-pr --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin YARDR_PR=1 ~/.yardr/scripts/pr-train' \
  --set log="$HOME/.yardr/logs/pr.log"
yardr group add yardr-pr-merge --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin YARDR_PR=1 ~/.yardr/scripts/pr-merge' \
  --set log="$HOME/.yardr/logs/pr-merge.log"
yardr route add --stage approved --type train --depot yardr --group yardr-pr --priority 10
yardr group add decisions --runner manual --limit 50 --members mckean,yardmaster,yard   # yard: the daemon, for the github rules
yardr route add --stage in-pr --type train --depot yardr --group decisions
yardr route add --stage land --type train --depot yardr --group yardr-pr-merge
```

The train flow's stage file for review (`.yardr/flows/yardr.train/review.md`,
shown in the reviewer's brief) says how a train is reviewed as a whole, and
how it is reviewed on its pull request: the
reviewer pushes its fix-forward commits to `yard/<train>` with a lease (the
one branch any role pushes; the merger does not push even that one, the
integrator pushes its rebase), and posts its review on the PR. GitHub
refuses `--approve` and `--request-changes` from the login that opened the
PR, which is the operator's, the same login the reviewer's `gh` uses: on
its own PR the reviewer posts a `--comment` with the verdict in the note's
first line, and the yard learns the verdict from `yardr bead done`.
