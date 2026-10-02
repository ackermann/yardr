# The pipeline: how a bead moves in practice

yardr builds no pipeline in: stages, groups, routes and a flow compose one.
This page follows a bead through the pipeline yardr itself is built with
(rig yard, as of 2026-10-02). Its flows are in [flows.md](flows.md#worked-example-rig-yard).

```
backlog ──mayor shapes──▶ new ──builder──▶ review ──Codex──▶ approved ──refinery──▶ merged on main
   ▲                       ▲                 │                  │
   │                       └──── changes ────┘                  │
   │                       └──────────── conflict / red check ──┘
   └──────── decide (Christopher) ◀── a question from backlog, new or review
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

The builder implements the change, runs the checks (today written out in the
role prompt: `go vet`, `go test -race`, the RPC suites; `.yardr/check` once
yard-p2y7 lands), commits on its branch (never pushes or merges), leaves a
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
   the rebase and sends the bead back to `new` with the conflicting files;
2. runs the checks on exactly what will land; red sends the bead back to
   `new` with the tail of the output;
3. fast-forwards `main` (never forcing, never touching a dirty checkout: if
   `main` is checked out with uncommitted changes, it holds the bead) and
   closes the bead with `merged <sha>`.

It hard-codes rig yard, `main`, the worktree path and the checks. The real
refinery (*planned, yard-23*) replaces it: the exec runner passes rig, base,
branch, worktree and check through the environment, a reference script is
versioned in `contrib/refinery/`, a `merged` terminal stage joins the flow,
and conflicts go to a `merge` stage where a merger agent resolves them
(*planned, yard-24*). yardr takes no position on how work lands: a PR, a
shared PR or a direct merge are all just a different script in the same
exec group. Rig yard merges directly to `main`.

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
- `.yardr/check` is the one gate both the builder and the refinery run, read
  from `main` so a bead cannot weaken it (*planned, yard-p2y7*).
- The refinery becomes a versioned script with a `merge` stage
  (*planned, yard-23, yard-24*).
