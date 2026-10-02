# The pipeline: how a bead moves in practice

yardr builds no pipeline in: stages, groups, routes and a flow compose one.
This page follows a bead through the pipeline yardr itself is built with
(rig yard, as of 2026-10-02). Its flows are in [flows.md](flows.md#worked-example-rig-yard).

```
backlog ──mayor shapes──▶ new ──builder──▶ review ──Codex──▶ approved ──refinery──▶ merged on main
   ▲                       ▲                 │                │   ▲
   │                       └──── changes ────┘                │   │ resolved
   │                       └───────────────── red check ──────┘   │
   │                       └── can't keep both ── merge ◀─────────┘
   │                                              (merger) ◀── conflict
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

## 4. Approved: the refinery lands it on main

`approved` is routed to `yard-refinery`. Today this is an **interim
refinery**: an `exec` group with limit 1 (so a serial merge queue) whose
`cmd` is a script outside the repo, `~/.yardr/scripts/refinery-local.sh`,
logging to `~/.yardr/logs/refinery.log`. For each bead it:

1. rebases `yard/<bead>` onto `main` if `main` moved; on a conflict it aborts
   the rebase and sends the bead to `merge` with the conflicting files
   (section 5);
2. runs the checks on exactly what will land; red sends the bead back to
   `new` with the tail of the output;
3. fast-forwards `main` (never forcing, never touching a dirty checkout: if
   `main` is checked out with uncommitted changes, it holds the bead) and
   closes the bead with `merged <sha>`.

It hard-codes rig yard, `main`, the worktree path and the checks. The real
refinery (*planned, yard-23*) replaces it: the exec runner passes rig, base,
branch, worktree and check through the environment, a reference script is
versioned in `contrib/refinery/`, and a `merged` terminal stage joins the
flow. yardr takes no position on how work lands: a PR, a
shared PR or a direct merge are all just a different script in the same
exec group. Rig yard merges directly to `main`.

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
4. leaves one note on how each conflict was resolved and advances to
   `approved`.

The refinery then picks the bead up again: `main` no longer moved past the
branch, so it runs the checks and fast-forwards. If `main` moved again in the
meantime and conflicts again, the bead goes back to `merge`.

When both intents cannot be kept, or keeping them needs a real design choice,
the merger aborts the rebase, notes why, and advances to `new` (the builders
rework the bead) or `decide` (Christopher chooses). It never touches `main`,
never pushes, and never forces anything but its own branch.

A red gate (not a conflict) still sends the bead to `new`: the change, not
its placement on `main`, needs work.

### Setup

The mayor wires this in the store; nothing in `~/.yardr` is versioned:

```sh
yardr group add mergers --runner herdr --limit 1 \
  --set kind=claude --set model=sonnet --set prompt=roles/merger.md
yardr route add --stage merge --group mergers --rig yard
```

The flow gains two rules (the rest of [`yard.flow`](flows.md#worked-example-rig-yard)
is unchanged):

```
approved -> new | merge
merge -> approved | new | decide
```

and the refinery script sends a conflicting bead to `merge` instead of `new`.
In `refinery-local.sh`, next to `back`:

```sh
# A conflict goes to the mergers; without a merge stage in the flow the
# advance is refused and the bead goes back to the builders as before.
conflict() { note "$1"; "$Y" bead advance "$BEAD" --to merge ||
  "$Y" bead advance "$BEAD" --to new; exit 0; }
```

and the rebase branch calls `conflict "rebase onto $BASE conflicts in:
$files"` after `git rebase --abort`, where it called `back`.

## Holds

A held bead is never routed; it waits until someone runs
`yardr bead unhold`. Beads get held:

- by yardr, after 3 failed sessions in one stage (an agent that died or
  exited without advancing);
- by an agent that is stuck or finds the bead unclear: it notes the question
  and runs `yardr bead hold`;
- by the reviewer, instead of a third send-back;
- by the refinery, when it cannot land without touching a dirty tree.

The mayor watches held beads, finds out why, and either fixes the cause and
unholds, reshapes the bead, or sends it to `decide`.

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

- Agents report an outcome (`yardr bead done`, `--outcome changes`,
  `--outcome question`) and the flow picks the stage, instead of agents naming
  stages (*planned, yard-qexg*).
- The refinery script calls `yardr rig check` instead of hard-coding the
  checks, so `.yardr/check` (built, read from `main` so a bead cannot weaken
  it) is the one gate both the builder and the refinery run.
- The refinery becomes a versioned script (*planned, yard-23*).
