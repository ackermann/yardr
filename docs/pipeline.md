# The pipeline: how a bead moves in practice

yardr builds no pipeline in: stages, groups, routes and a flow compose one.
This page follows a bead through the pipeline yardr itself is built with
(rig yard, as of 2026-10-02). Its flows are in [flows.md](flows.md#worked-example-rig-yard).

```
backlog ──mayor shapes──▶ new ──builder──▶ review ──Codex──▶ approved ──refinery──▶ merged on main
   ▲                       ▲                 │                  │
   │                       └──── changes ────┘                  │
   │                       └───────────────── red check ────────┘
   │                       └── can't keep both ── merge ── merger ──▶ review
   │                                              ▲           │
   │                                              └─ conflict ┘
   └──────── decide (Christopher) ◀── a question from backlog, new, review or merge
```

## 1. Backlog: the mayor shapes the bead

New ideas land in stage `backlog`, routed to the manual group `backlog`. The
mayor (a resident, see [residents.md](residents.md), prompt
`roles/mayor.md`) reads them and shapes each one until a builder could finish
it without asking: a goal, where to look, what "done" means and how to test
it. Large beads are split, and `yardr dep add` orders them: a bead is only
ready once its blockers are closed.

A shaped bead is advanced to `new` (`yardr bead advance <id> --to new`).
Christopher can do the same from `yardr review -g backlog`.

## 2. New: a builder implements it

`new` is routed to `yard-builders`: a herdr group running Claude Code with
`prompt=roles/builder.md`, at most 2 at a time. The daemon opens a worktree
on branch `yard/<bead>`, starts the agent in it and hands it a brief: the
role prompt, the bead, and the stages it may move to next.

**How agents are attributed.** The agent runs in a herdr tab created with
its identity in the environment: `YARDR_ACTOR` (the agent's name,
`<bead>-<stage>`), `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`,
`YARDR_GROUP`, `YARDR_HOME` and `YARDR_BIN`. Nothing depends on the agent
exporting anything: every event its `yardr` commands log names it as actor.
`yardr bead done`, `advance` and `close` also send `YARDR_SESSION`, and the
store refuses the move unless that session still owns the bead
(`stale: bead <id> is now in <stage> owned by <session>`). An agent kept
open, or orphaned by a restart, after its bead moved on therefore cannot
end the session that owns the bead now; nothing changes. A person at a
shell has no `YARDR_SESSION` and is not checked. `yardr review` does the
same with the stage: it refuses a decision on a bead that left the stage
the form showed it in.

The builder implements the change, runs the gate (`yardr rig check yard
--dir <worktree>`, which runs `.yardr/check` from `main`: `go vet`,
`go test -race`, the RPC suites), commits on its branch (never pushes or merges), leaves a
note with what it changed and verified, and advances to `review`.

## 3. Review: Codex reviews forward

`review` is routed to `yard-codex-review`: Codex in the same worktree, with
the reviewer prompt from `~/.yardr/roles/reviewer.md`. It is the last review
before merge, so it fixes what it can on the branch (commits starting
`review:`) rather than only reporting, re-runs the checks and then:

- **approves**: a note with what it checked and fixed, then `--to approved`;
- **sends back**: concrete findings, then `--to new`; the bead returns to
  the builders, in the same worktree. After two send-backs it holds the bead
  instead of a third, so the mayor looks;
- **asks**: a question only Christopher can answer goes `--to decide`.

When a merger sends a bead back for a `merge re-review`, the reviewer records
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
[`contrib/refinery/merge-local`](../contrib/refinery/merge-local); rig yard
uses it to merge directly to `main`.

**The mechanism.** The exec runner starts the script in the rig with
everything it needs in its environment ([groups.md](groups.md#runners)):
`YARDR_BEAD`, `YARDR_RIG`, `YARDR_RIG_PATH`, `YARDR_BASE`, `YARDR_BRANCH`
(`yard/<bead>`), `YARDR_WORKTREE`, `YARDR_BIN` (this yardr), and
`YARDR_ACTOR` set to the group. The gate is `yardr rig check "$YARDR_RIG"
--dir "$YARDR_WORKTREE"`: `.yardr/check` as it is on the base, or the rig's
`--check`. The script finishes by moving the bead, with a note:
`yardr bead advance "$YARDR_BEAD" --to <stage> --note -`. A script that
exits without advancing, closing or holding counts as a failed session, like
an agent that gives up, and after 3 of those the bead is held. Advancing ends
the session and the daemon stops the script, so it is the script's last step.

**merge-local.** For each bead it:

1. rebases `yard/<bead>` onto the base in the bead's worktree, if the base
   moved; on a conflict it aborts the rebase and advances to `merge` with the
   conflicting files (section 5), or to `new` if the flow has no `merge`;
2. runs the gate on exactly what will land, every time, including after a
   merger's rebase; red advances to `new` with the last 200 lines of output;
3. lands by fast-forward only. Where the base is checked out it fast-forwards
   that checkout, only when it is clean; a dirty one is someone's work in
   progress, so the bead is held with a note saying so and the tree is never
   touched. Where the base is checked out nowhere it moves the ref alone
   (`git fetch . yard/<bead>:<base>`), which works whatever state the rig's
   checkout is in: keep the base checked out nowhere and a dirty checkout
   never blocks the queue;
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
yardr group add yard-refinery --runner exec --limit 1 \
  --set cmd='YARDR_PUSH=origin ~/.yardr/scripts/merge-local' \
  --set log="$HOME/.yardr/logs/refinery.log"
yardr route add --stage approved --group yard-refinery --rig yard
```

The script is installed from `main` rather than run from a checkout: a bead
branch must not change how it is landed. The flow gains `merged`:

```
approved -> merged | merge | new
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
3. runs the gate, `yardr rig check yard --dir .`;
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

The mayor wires this in the store; nothing in `~/.yardr` is versioned:

```sh
yardr group add mergers --runner herdr --limit 1 \
  --set kind=claude --set model=sonnet --set prompt=roles/merger.md \
  --set args='--permission-mode auto'      # as yard-builders: it works unattended
yardr route add --stage merge --group mergers --rig yard
```

The flow gains two rules (the rest of [`yard.flow`](flows.md#worked-example-rig-yard)
is unchanged), with outcomes so the merger reports how the merge ended
instead of naming stages:

```
approved -> merged on done | new on failed | merge on conflict
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
A session that sits idle 30m or runs 4h raises `session_idle` or
`session_aged` in the mayor's inbox, and with a group `grace` set it is then
stopped; see [configuration.md](configuration.md#session-limits-and-outages).

## Decide

`decide` is routed to the manual group `decisions`. Backlog, new and review may
send a bead there with the question and the options written into the bead. Christopher
answers in `yardr review -g decisions` (or with a note and
`yardr bead advance`), and the bead goes back to `backlog` or `new`.

Decisions that are not about one piece of code are beads of type `decision`,
created straight into `decide`; their type flow has one move, to the terminal
stage `decided`, which closes the bead. yard-17, which settled the
repo/store split in [configuration.md](configuration.md), is one.

## Planned changes to this pipeline

- The rig's flow names outcomes (`new -> review on done | decide on
  question`, see [flows.md](flows.md#outcomes-agents-say-how-the-work-ended)),
  so builders, the reviewer and the refinery report `yardr bead done`
  (`--outcome changes`, `--outcome question`) instead of naming stages.
  `bead done` and outcomes are built (yard-qexg); the live flow, the reviewer
  prompt and the refinery script move to them once it lands.
