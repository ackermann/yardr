# Several reviews of one bead at once

Design yardr-ndv0, proposed 2026-10-12. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr; a bead is one durable piece of work; a flow is the stages a bead of some type goes through; a stage is one step of a flow; a route says which group works a stage's beads; a group is who works a stage, by its runner (an agent, a script, people, or another yard); a session is one agent working one bead in one stage; an outcome is how a session says its stage ended; a train is a bead whose children land on its branch and reach the base as one. docs/glossary.md has all of yardr's words. In this document a **review bead** is a bead of type `review` that carries one aspect's review of another bead, the **parent**; the **gate** is the script that makes the review beads and reads their verdicts.

## 1. The question

mckean, mail 19354, 12 Oct 2026: "let's say we have a bead and it's in stage build, then we go to review, but we would like to have an agent review based on code qualtiy, and one review based on security and one agent about ui/ux, those reviews could all run in parallel, would we be able to do that already?" Not in parallel today, and one after another works already; the question is how one bead gets several reviews at the same time, each by an agent with its own brief, and moves on when all have passed, or goes back when one has not.

## 2. What exists today

### 2.1. One stage, one group, one session

A route sends the beads of one stage to one group. `Route` in internal/store/routing.go carries `Stage` with optional `Type`, `Depot` and `Label` filters, one `Group`, and a `Priority`; `MatchRoute` returns the first route in `SortRoutes` order that fits the bead, so every bead in a stage has exactly one group. The dispatcher (`route` in internal/dispatch/dispatch.go) claims each dispatchable bead to that group and starts one session.

One session owns the bead. A bead row carries a single `Group` and `Session`; `Claim` in internal/store/routing.go returns `ErrTaken` for a bead that is owned, and `Dispatchable` in internal/store/beads.go lists only ready beads that no group owns. A move from a session that no longer owns its bead is refused as stale (docs/line.md, "How agents are attributed"). Nothing in the store, the dispatcher, or any runner starts, tracks, or ends a second session on a bead in a stage.

The herdr runner assumes serial sessions per bead: one worktree per bead at `~/.yardr/worktrees/<depot>/<bead>` on branch `yard/<bead>`, reused across stages and relabelled on each reopen, one tab per stage, and the tab closed when the session ends (internal/runner/herdr.go, docs/groups.md "Runners"). Two sessions at once would share one worktree and one branch.

### 2.2. Waiting is the dependency gate

Only `blocks` edges keep a bead from being ready (`ready` in internal/store/beads.go); every other kind is a record. A blocked bead is not routed at all, in whatever stage it sits, and is routed again, from that stage, once its blockers close; a blocker that reopens holds again at the next hand-over. The gate is visible: `yardr bead show` prints `blocked by <id>` for each holding edge, `yardr ready --waiting` lists holding edges with why, and the store logs `unblocked` and `reblocked` when a write flips a gate (docs/line.md, "Dependencies"). `yardr dep add <from> <to>` makes a `blocks` edge; `yardr bead create` takes `--blocked-by` for edges made with the bead.

A train waits exactly this way. `dep add <train> <child> -k parent` also adds `<child> -blocks-> <train` (internal/store/graph.go), so the train sits in `open`, unrouted, until its last child closes, and its integrator runs when it becomes ready. But a parent edge is a train's edge: `checkParent` in internal/store/trains.go refuses a parent that is not an open train, a train as a child (trains do not nest), a child in another depot, and a second parent. It carries branch semantics with it: the child branches from the train's branch and lands on it (`familyOf`).

### 2.3. Review today, and what already works

One review stage, one reviewer, fix-forward. The reviewer reads the whole diff on `yard/<bead>`, fixes what it can with confidence in commits starting `review:`, and runs the depot's test command only when it committed or the builder's test line is not for the commit under review (packs/starter/roles/reviewer.md, .yardr/flows/default/review.md). The whole suite runs once per bead, in the assembly, with `YARDR_LANDING=1`, on exactly what lands (docs/line.md, "One whole-suite run per bead").

Three reviews one after another work today with no code, as the bead says: three stages in the flow (review-quality, review-security, review-ux), each with its own stage file (docs/flows.md, "Stage files") routed to its own group, each reporting `done` on and `changes` back. Stages, routes, stage files, and per-stage outcomes are all per-stage already.

Two narrower precedents matter. A read-only reviewer exists: the contributors pack's `pr-reviewer.md` role ("You run nothing. … You change nothing. You commit nothing"), enforced by its agent's sandbox. And one reviewer for some beads exists: a label on the bead plus a higher-priority route for that label sends labelled beads to another group (docs/groups.md, "Example: a crew member reviews the beads with a label").

### 2.4. Gates: a stage worked by a script

A gate is a stage routed to an `exec` group, and the flow says where its outcomes lead (docs/flows.md). The script gets everything in its environment (`YARDR_BEAD`, `YARDR_DEPOT`, `YARDR_BASE`, `YARDR_BRANCH`, `YARDR_WORKTREE`, `YARDR_BIN`, `YARDR_TYPE`, `YARDR_TRAIN`; docs/groups.md, "Runners") and finishes by moving the bead. `contrib/assembly/integrate-train` is the pattern this design copies: it reports outcomes with `yardr bead done <id> --outcome <o> --note -`, notes and holds what it cannot handle, and creates beads itself (`bead create "Fix the tests on train …" … -b -`, then `dep add`). A flow may lead an outcome back to its own stage (`failed: open` in the starter pack's train flow), and the dispatcher waits out rapid self-loops generically (`backingOff` in internal/dispatch/dispatch.go: 30 seconds, doubling to an hour, counted from consecutive `advanced` events back to the stage).

A type with its own flow exists: wagons follow `.yardr/flows/type/wagon.flow`, the depot's flow without `review`, and depot yardr's untyped routes carry them to the same builders and assembly (docs/line.md, "Wagons"). Entering a terminal stage closes the bead with the stage as the close reason, which `bead show` prints as `closed: <reason>` (docs/flows.md, internal/cli/beads.go).

### 2.5. Briefs, models, and quota

Each session is prompted with its own brief: its group's role prompt first, then the bead, the reporting commands, the flow map, and the stage file (`Brief` in internal/runner/runner.go). A herdr group's `model` goes to its agent as `--model`, and a bead's `--meta model=…` replaces it for that bead (docs/groups.md, "Models"). What a session burns is its harness kind's subscription, read by `yardr limits` (docs/groups.md, "A usage limit says its window"). A group's `limit` is how many beads it works at once. So per-review brief, model, and quota already exist wherever per-review groups exist.

### 2.6. The event trail says how a bead closed

Every write to a bead appends an event to the store's log (`w.event`, reached through `changeBead` in internal/store/beads.go): `advanced` carrying `from`, `to`, and, for `bead done`, the `outcome` ("The outcome is recorded on the advanced event", `Done` in internal/store/beads.go; a plain `advance --to` writes no `outcome`); `closed` carrying the `reason`; `reopened` carrying nothing. Entering a terminal stage through the flow writes two events in one transaction: `advanced` with the outcome, then `closed` with the stage as the reason (`moveBead` in internal/store/beads.go). A close from outside the flow writes only `closed`, with any reason string and the stage untouched; a reopen writes `reopened` and likewise preserves the stage (`closeWith` and `Reopen` in internal/store/beads.go). Events are written by the store alone — nothing a person or script runs writes an arbitrary event (the `LogEvent` callers are the server, pairing, dispatch, and pruning in internal/server/server.go, internal/store/pairing.go, internal/store/events.go, and internal/store/prune.go; none writes `advanced`, `reopened`, or `closed`) — so the trail cannot be forged, only appended to. The gate reads it with `yardr events --bead <id>` (internal/cli/ops.go, a top-level command in internal/cli/root.go), oldest first, `--json` for scripts. One caveat: `yardr store prune` drops events past retention (default 90 days, `YARDR_EVENT_RETENTION`), of open beads too.

## 3. The proposal

Review beads with `blocks` edges, made and judged by one gate script. No yardr code changes: one stage, one script, one type flow, three groups with three roles, label routes. The waiting is the dependency gate of section 2.2, which is how a train waits for its wagons; the fan-out and the verdict are a gate of section 2.4, which is how integrate-train adds a fix child and reports.

Where a choice below is the operator's, the text names its open question of section 5 and describes the option this draft would take.

The depot's flow gains one stage, `reviews`, between `new` and `approved`. Whether `reviews` replaces `review` or stands beside it is open question 4; the sketch shows both paths, the builder choosing by outcome:

```text
new -> review on done | reviews on deep-review | decide on question | backlog
reviews -> approved on done | new on changes | decide on question | reviews on waiting
approved -> merged on done | new on failed | decide on question
```

`reviews` is routed to an `exec` group running the script `review-gate`. The outcome `waiting` loops to the stage itself; it is how the gate's first run ends, and the bead then sits blocked (section 3.1), so the dispatcher's self-loop backoff (30 seconds) has expired long before the bead is ready again.

### 3.1. The gate's runs

The gate runs in `reviews` once per round. A round is one branch tip: the parent's branch cannot move while reviews run, because no session owns the parent then (only a person committing by hand could move it; each review records the tip it reviewed, section 3.2, so that shows).

On every run the gate lists the parent's review beads: the `blocks` edges into the parent (`yardr dep list <parent> --json`) whose source is of type `review`. Rounds are keyed by the tip each review's metadata records (`--meta parent=<id> --meta tip=<sha>`, written at creation):

- **No round for the current tip.** The gate decides which reviews the bead needs (section 3.4), creates one review bead per review (section 3.2), adds `<review> -blocks-> <parent>` for each, and reports `waiting`. The parent is now blocked and waits unrouted; each review dispatches at once to its group.
- **The current round is all closed.** The gate reads each verdict — the stage/reason pair plus the bead's event trail (section 2.6) — writes one verdict note on the parent naming each review and its verdict, and reports `done` when all passed (to `approved`), `changes` when any failed (to `new`, with the findings to answer), `question` never from the gate itself. A verdict is valid only when the review is closed in a terminal stage of the review flow whose name equals its close reason — (`passed`, `passed`) or (`failed`, `failed`) — and in `yardr events --bead <review> --json` the last `advanced` event names that stage in `to` with the outcome that leads there in `outcome` (`done` for `passed`, `changes` for `failed`), and no `reopened` event comes after that `advanced`. A closed review the trail does not confirm is one the gate cannot read: it notes what it saw and holds the parent, which reaches the yardmaster, rather than guess (open question 5).
- **The current round still has an open bead.** Unreachable through plain `blocks` edges (the parent would not be ready), so the gate reports `waiting` again as a guard.

The pair alone cannot be the verdict. `bead close --reason` accepts any string and `CloseBead` stores it without checking the flow (`internal/cli/beads.go:937-977`, `internal/store/beads.go:1229-1253`, `closeWith` at 1367-1383), a close leaves the stage untouched, and `Reopen` preserves the stage (`internal/store/beads.go:1397`): a hand close from `new` reads (`new`, `passed`), and a reopen plus a close with reason `passed` re-reads (`passed`, `passed`). The trail defeats both. Only the flow's `done` writes `advanced` with an `outcome`, so a hand `advance --to passed` leaves an `advanced` with no `outcome` and is held; and a reopen always appends `reopened` after the verdict's `advanced`, which no flow move writes, so a reopened-and-closed review is held even when its pair reads (`passed`, `passed`). Two boundaries, stated plainly: a `done` reported by hand through the flow counts as the verdict it is — the flow accepted it, and the trail names its actor — and a review whose trail is pruned past the verdict (section 2.6) holds as unreadable; rounds last hours against a 90-day retention, so that is a guard, not a path.

When the parent comes back from `changes` with a new tip, the next gate run finds no round for that tip and fans out a fresh one; closed rounds stay as the record.

### 3.2. A review bead

```sh
yardr bead create "Review (security): Add the export" -t review -d yardr -s new \
  -l review:security --meta parent=yardr-d5zc --meta tip=9bf658a -b -
yardr dep add yardr-r4k1 yardr-d5zc
```

The body, written by the gate, names the parent, the tip under review, and the aspect in one line each. The bead is created straight into `new`, so it dispatches at once; nothing blocks it.

A review bead follows the flow of type `review`, beside the depot's flow as the wagon flow is:

```text
backlog -> new
new -> passed on done | failed on changes | decide on question
decide -> new | backlog
passed terminal
failed terminal
```

In `new`, one route per aspect, matched by type and label, sends the bead to its group (one group per aspect is open question 2):

```sh
yardr group add yardr-quality --runner herdr --limit 3 \
  --set kind=claude --set model=sonnet --set prompt=roles/review-quality.md
yardr route add --stage new --type review --label review:quality --group yardr-quality --depot yardr
```

The two further aspects are the same commands with `security` and `ux`. Review beads never match the depot's untyped `new` route, because a route with more filters sorts first (`SortRoutes`).

The reviewer's session opens its own worktree on its own branch, which does not have the parent's changes — and must not get them. Its role says to read the parent's branch in the same repository (`git diff <base>...yard/<parent>`, at the recorded tip) and to change nothing: no commits, no test runs, findings in notes. Read-only is forced, not chosen: two fix-forward reviewers on one branch race, and git will not check out one branch in two worktrees. What fixes what they find is the parent's builder, on the parent's next round through `new`. The contributors pack's pr-reviewer role is the wording to copy.

A reviewer that needs a person reports `question`: the review bead waits in `decide`, routed to people as any bead's does, the person answers in a note and moves it back to `new`, and the parent keeps waiting meanwhile. A reviewer that is stuck holds its own bead; the parent waits until the yardmaster unholds it, exactly as a train waits on a held child.

### 3.3. Who runs the tests

Nobody new. The builder runs the depot's test command before `done`, as today; the reviewers run nothing, as the pr-reviewer runs nothing; the gate runs nothing; the assembly runs the whole suite once on what lands, as today. Three parallel reviews cost three agent sessions and zero extra test suites, which is the point under a CPU limit: the parallel part is reading, and the one expensive run stays serial in the assembly. Group limits bound how many reviews run at once per aspect, as they bound builders today.

### 3.4. Which reviews run for which beads

The gate decides, per bead, per round, in the script: the parent's labels and type, and path rules over the round's diff (`git diff --stat <base>...<tip>` in the depot). A UI review only when the diff touches the interface, a security review for the paths and labels the operator names, the quality review always. Labels are the escape hatch both ways: a label the gate reads adds a review the paths would skip, and one skips a review the paths would run. The rules live in the script and its stage file, reviewed where scripts are reviewed, and need no store change because routes already match on type and label. That the rules are paths with label overrides is open question 3.

### 3.5. What a person sees and types

While reviews run, the parent says what it waits for:

```text
yardr-d5zc  Add the export
  type task · stage reviews · open · P2
  blocked by yardr-r4k1
  blocked by yardr-r4k2
  blocked by yardr-r4k3
```

`yardr ready --waiting` says the same with why (`waits until yardr-r4k1 closes`), each review bead is a bead like any other with its aspect's notes, and `yardr dep list yardr-d5zc` keeps the edges after they close. When the round ends, the gate's verdict note on the parent names each review and its verdict, and the bead's history shows the round trip. Closed review beads stay out of the way: `yardr bead list -t review` is where they are counted, and no listing the yardmaster watches changes.

The operator wires one depot with the commands of section 3.2, once per aspect, plus the gate group and its route:

```sh
yardr group add yardr-review-gate --runner exec --limit 3 \
  --set cmd='~/.yardr/scripts/review-gate'
yardr route add --stage reviews --group yardr-review-gate --depot yardr
```

The gate's limit is above one: it holds no queue (each run is seconds), and several parents may need verdicts at once.

### 3.6. What a round costs

In sessions: one per review, each against its own group's limit, plus two gate runs of seconds that hold no slot worth counting. Three aspects means three concurrent agent sessions per parent under review — the same three any parallel design pays. In quota: each session burns its own group's harness subscription (section 2.5), so the quality review can run Sonnet while the security review runs Opus, and `yardr limits` shows each kind's windows as it does today. In beads: three per round, closed when the round ends; they are the audit trail, countable with `bead list -t review`. In test runs: none beyond today's (section 3.3).

## 4. Left out

**A stage with several groups (the bead's idea 1).** Refused: it rebuilds, in the store, what the dependency gate already does. One bead with N sessions needs multi-owner beads (`Claim` and the `Group`/`Session` pointers), multi-claim dispatch (`Dispatchable`, group limits counted N ways), verdict aggregation (each session reports an outcome; the bead moves on the worst; a hold by one of N; a death mid-round), and concurrent sessions in one worktree, where the runner assumes serial use and two fix-forward commits race on one branch. All of that machinery, new and subtle, to arrive where blocks edges, labels, and one script already arrive. If review beads ever prove too heavy, this stays the design to write — but "costs beads" is cheap: beads are rows, and closed review beads are the audit trail.

Had it been taken: nobody could fix — N reviewers on one branch and worktree may only read, for the same reason as section 3.2, and separately briefed read-only sessions cannot coordinate a shared fix. Selection would need a new rule (which groups a stage names for which bead: a label match per group, a path rule somewhere new), since a route names one group. A person would see one bead with N owners, which `bead show` cannot print today. Sessions and quota would match this design (one session per aspect per group); tests likewise (reviewers run none). Every one of those answers is this design's, at the price of the store change.

**One reviewer session with subagents (the bead's idea 3).** Refused as the mechanism: the yard sees one review, one brief, one model, one quota line, and per-aspect routing, limits, and retries do not exist inside a harness. Nothing in this design stops a reviewer from using subagents within its own session; they are then that review's private labour, not the yard's parallelism.

Had it been taken: the one session could fix forward as today, since parallel writers never meet — but its three aspects would then review a branch its own fixes keep moving. Selection would be prose in one brief ("also cover security and UI"), unenforceable and invisible. A person would see one review note, with no per-aspect verdict to point at. Sessions and quota would be one session on one subscription — cheaper, and exactly what mckean did not ask: no agent per review, no model per review. Tests: one suite at most, as today.

**Parent edges for the review beads.** Refused: a parent edge is a train's edge and carries landing semantics (branch from the train, land on it), while reviews land nothing. Worse, only trains take children and trains do not nest, so a bead that is itself a child of a train could never be reviewed. Plain `blocks` edges give the waiting with no code and no constraint on what the parent is.

**Fix-forward parallel reviews.** Refused for the reason in section 3.2: parallel writers on one branch conflict, and each reviewer's branch is not the parent's. Fix-forward survives in the serial stages of bead 1 and in today's single `review` where it stays.

**Reviewers running the test command.** Refused: three suites at once for three read-only reviews buys nothing the builder's line and the assembly's run do not already say, and spends the machine's scarcest resource. A reviewer that doubts a specific behaviour says which test to run in its findings, and the builder runs it.

**A first-class `review` type in yardr's code.** Not proposed: no code switches on bead types except `train` (branch and landing) and `wagon` (no review, plain test run), and `review` needs neither. The type is a string that flows and routes match on, settable per depot without touching the binary.

**Selecting reviews by anything cleverer than labels, types, and paths.** No model call, no history mining: the gate's rules are read in the script, and a wrong selection is one label away from right.

## 5. Open questions

**1. Serial first, or parallel straight away?** Option A, land the serial pack (bead 1) first, then the gate: the aspect roles and stage files are exercised one at a time before they run together, and a yard that never wants parallel reviews keeps something useful. Option B, build the gate first: one review path to learn instead of two, and the serial stages are thrown away on the depots that take the gate. I would take A: the serial pack is small, needs no code, and its roles are the gate's roles.

**2. One group per aspect, or one pool for all reviews?** Option A, a group per aspect, as proposed: each aspect gets its own role, model, limit, and quota line, and a slow security queue never starves quality reviews; it costs three groups and three routes per depot. Option B, one reviewers pool with the aspect in the brief (by label): one group to tend, one limit, one model, and aspects queue behind each other. I would take A: per-review model and quota is half of what mckean asked for.

**3. Which reviews run for which beads — who writes the gate's rules?** Option A, path rules in the script with label overrides, as proposed: deterministic, read where scripts are read, and wrong selections are fixed with a label. Option B, labels only, set by the yardmaster or the builder: no paths to keep, and every bead pays a human judgment per round. Option C, all reviews for every bead: no rules at all, and UI reviewers read backend-only diffs. I would take A, with C as the starting ruleset (every bead gets all three) until the paths have been watched for a month.

**4. Does depot yardr cut over at once, or run both review paths?** The flow cannot route one bead to `review` and another to `reviews`: one outcome maps to one stage. Option A, cut over: `new -> reviews on done`, and every bead is reviewed in parallel from then on; it costs a single cutover at which the gate must be right first time, and beads sitting in `review` must be moved out before the stage goes. Option B, both paths, chosen by the builder's outcome (`done` for the single review, a second outcome for the parallel round): beads opt in one by one, and two review paths live side by side until the single one is removed. I would take B, with the builder's role saying which beads take the parallel round, and remove the single path in a later bead.

**5. A review closed by hand: hold the parent, or fail the round?** A hand close is any closed review the trail does not confirm as a flow verdict (section 3.1) — for example (`new`, `passed`) from a `bead close --reason passed` outside the flow, (`passed`, `passed`) reached again by a reopen plus a close with reason `passed` (the `reopened` event after the verdict's `advanced` gives it away), or an `advanced` to `passed` with no `outcome` (a hand `advance --to`). Option A, hold with a note naming what the gate saw, as proposed: a human looks, and the verdict is never guessed; it costs a human step for what may be a trivial close. Option B, treat any unconfirmed review as `failed`: the round fails closed, the builder reads the notes, and a mistaken close costs a builder round. I would take A: hand-closes are rare, and a builder round is the expensive mistake.

## 6. The beads it would become

All in depot yardr, in the order they land. Bead 1 depends on nothing. Beads 2 and 3 build on bead 1's roles where open question 1 is answered A; under B, bead 2 carries the roles itself and bead 1 does not land.

**1. A pack with the three serial review stages.** Goal: per-aspect reviews one after another, with no code, worth having alone. Where to look: packs/starter/pack.yaml for the pack format and docs/packs.md for what `pack check` holds it to; packs/contributors/roles/pr-reviewer.md for read-only review wording; .yardr/flows/default/review.md for when each outcome applies. Done when: a new pack (flows with review-quality, review-security, and review-ux stages, each with its stage file, role, and group; each `done` onward and `changes` back to `new`) passes `yardr pack check` against a depot, is documented in docs/line.md beside the single review, and `sh .yardr/test` passes. The three roles it writes are the roles bead 2's groups use.

**2. Parallel reviews: the gate, the review flow, and the trial.** Goal: sections 3.1–3.5 working in an isolated yard. Where to look: contrib/assembly/integrate-train for the gate pattern (`bead done --outcome`, `bead note`, `bead hold`, creating beads from a script); .yardr/flows/type/wagon.flow for a type flow beside the depot's; internal/store/routing.go `SortRoutes`/`MatchRoute` for why the typed, labelled routes win; docs/groups.md "Example: a crew member reviews the beads with a label" for the route shape; `yardr events --bead` (internal/cli/ops.go, internal/store/events.go) for the trail the gate judges by. Done when: `review-gate` fans out one round per tip, waits by `blocks` edges, and judges the worst verdict from each review's stage/reason pair plus its event trail (all trail-confirmed (`passed`, `passed`) to `done`, any trail-confirmed (`failed`, `failed`) to `changes`, anything the trail does not confirm to hold with a note naming what it saw); the type-`review` flow and the aspect routes dispatch each review to its group with its brief; reviewers read the parent's branch and commit nothing; a trial in an isolated yard (`contrib/isolated-yard/isolated-yard up`, stand-in agent, stopped by pid and removed after) runs a bead through a passing round, a failing round, a reopened-and-hand-closed review (reopened from a terminal stage, closed again with reason `passed`), and a hand-advanced review (`bead advance --to passed`, no outcome), the last two of which must hold the parent rather than count as verdicts, and its transcript is on the bead; `sh .yardr/test` passes. Depends on bead 1 for the roles where open question 1 is answered A, and on open questions 2 and 3 for the groups and the selection rules.

**3. Cut depot yardr over to parallel review.** Goal: the depot's own beads are reviewed the new way, one path or two as open question 4 is answered. Where to look: .yardr/flows/default.flow and .yardr/flows/default/ for the repo flow and its stage files (read from the base branch, so the change applies once landed); the operator commands of section 3.5 for the store side (groups, routes); docs/line.md "Review" for the account to rewrite; docs/flows.md "A base change that drops a stage" if the single `review` stage is removed (move its beads out first: a removed stage strands them). Done when: the flow change has landed on the base, the groups and routes exist in the yard, one real bead has gone through a parallel round to merge with the verdict note on it, docs/line.md describes the round (who fixes, who tests, what a person sees), and `sh .yardr/test` passes. Depends on bead 2 and on open questions 1, 4, and 5.
