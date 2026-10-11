# A blocks edge can name the stage it holds at, and the stage that releases it

Design yardr-5t60, proposed 2026-10-11. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

Design for bead yardr-5t60 (flow `design`, stage `draft`). A bead is one
durable piece of work; a blocks edge orders two beads, its source the
blocker, which must close first, and its target the bead that waits. A
bead is ready when nothing keeps it from being worked; routes send ready
beads in a stage to the group that works them. A flow is the stages a
bead goes through and the moves allowed between them; an outcome is how
work in a stage ends, and the flow picks the next stage for it. The
assembly is the group that tests an approved bead and lands it on the
base. A train is a bead whose children land on its branch and reach the
base as one. See docs/glossary.md for all of yardr's words.

## 1. The question

mckean, 8 Oct 2026, thinking about footplate-xq35 (held in new until
yardr-x7wf, the socket API it builds against, is merged): "a blocked
bead could already go into implementation and review but not get merged
until another one is done ... could be interesting if the contract API
is already agreed on". Today a blocks edge holds its target from the
first routed stage until the blocker closes. Can the edge instead hold
only where it matters, so the target is built and reviewed against an
agreed contract while the blocker is still open, and lands only after
the blocker does?

## 2. What exists today

**The edge and its store.** `Dep` in internal/store/graph.go has only
`From`, `To`, `Kind` and `CreatedAt`; the stored `depRow` adds an id,
UUIDs and bookkeeping. `AddDep` refuses a bead depending on itself, a
duplicate `(from, to, kind)` pair (`hasEdge`), and a blocks edge that
would close a cycle of blockers (`checkCycle`, over every blocks edge,
closed beads included, since reopening would make the cycle live). It
never consults any flow. Kind `parent` additionally stores the paired
child-blocks-train edge. `RemoveDep` deletes every edge matching
`(from, to, kind)` and is a no-op when there is none. `bead create
--blocked-by` makes plain blocks edges in the same write that creates
the bead (`newBlockers`, refused for an unknown bead or one named
twice). Nothing in the store names a stage on an edge.

**Readiness.** `ready` in internal/store/beads.go is one line: the bead
is open, unheld, of a working type, and `openBlockers` is empty. An
open blocker is a bead with a blocks edge into this one whose status is
not closed. Readiness is evaluated wherever routing asks: `Ready`
lists it, `Dispatchable` adds "no group owns it yet", and the
dispatcher claims from there, logging `claimed`. Nothing else reads
it. In particular a move never checks it: `AdvanceNote` checks the
flow, the wagon rule, the group's members and staleness, but not
blockers, so a blocked bead can be advanced into any stage its flow
allows today; it just sits there unrouted until its blockers close.
`OpenBlockers` serves the same list to callers by handle.

**What people see.** `yardr dep add <from> <to>` (the dep command
lives in internal/cli/beads.go, not internal/cli/dep.go, which does
not exist) prints `from -kind-> to`; `dep list` prints every edge
touching a bead the same way. Three corrections to the bead's account
matter for what follows. First, `bead show` prints no blockers at
all: it prints the flow, the family (base, train, children), the
discovery edges and the links (`printBeadFlow`). Only `yardr prime
--bead` prints `BLOCKED BY (open): ...` (from `OpenBlockers`) and the
edge list. Second, `yardr ready` lists only beads that are ready, as a
table with no flags and no reason column; a bead that waits never
appears in it, so "ready says why a bead waits" needs a new surface,
not a new column. Third, the readiness rule the bead cites under
"Backlog" lives in docs/line.md ("a bead is only ready once its
blockers are closed", "Cycles of blockers", "Dependencies"):
docs/pipeline.md does not exist.

**Landing.** `approved` is routed to an exec group with limit 1 (in
depot yardr, `yardr-refinery`), whose script does the landing; the
assembly is this composition, not built-in code. The reference script,
contrib/assembly/merge-local, rebases the bead's branch onto the base,
runs the base's copy of the test command with `YARDR_LANDING=1` on
exactly what will land, lands it by fast-forward only, pushes, and
advances to `merged`, which closes the bead (docs/line.md, "Approved:
the assembly lands it on main"). The default flow is `backlog ->
new -> review -> approved -> merged`, with `review -> new on changes`
and `approved -> new on failed`. A bead held before landing therefore
waits in `approved`, unrouted, exactly as a blocked bead waits
anywhere today.

**Flows and their real stage names.** `Flow` in
internal/store/flows.go holds stages, transitions, terminal stages
and per-stage outcomes. `OutcomesFrom` also offers an implicit
`done` for a stage with a single way out; `Resolve` maps an outcome
to its stage; `Next` lists allowed moves. The bead's "(train:
open/review/merge/approved)" is shorthand: depot yardr's train flow
(.yardr/flows/yardr.train.flow) has `backlog open review approved
in-pr land merge decide merged`, its wagon flow
(.yardr/flows/type/wagon.flow) has `backlog new approved decide
merged`, and the default flow (.yardr/flows/default.flow) has
`backlog new review approved decide merged`. A stage can have no
outcomes at all (the default flow's `decide -> backlog | new` is two
bare edges), which ends any walk that follows outcomes.

**Trains and wagons, and why a train does not answer this.**
`checkParent` in internal/store/trains.go refuses a parent edge
unless the parent is an open train, the child is not a train, the
child has no other train, and both are in the same depot: the child
branches from the train's branch in that depot's repository and lands
on it. Each child also blocks the train, so the train waits in `open`
until its last child closes; then it is integrated, reviewed as one
diff, and landed as one unit on the base. A wagon is a child type
that skips its own review and landing run. The need here is the
opposite shape: two depots (footplate against yardr), two separate
landings on two bases, ordered in time, joined only by a written
contract (docs/api.md). A train cannot span depots and lands jointly;
this bead lands separately and constrains only when. Whether that
difference is worth a new mechanism is Open question 1.

**Events and the yardmaster's inbox.** The store logs `dep_added`
and `dep_removed` on the target, `claimed` when the dispatcher takes
a bead, `advanced` on a move (with the outcome), `closed` on close.
Nothing is logged on a target when a blocker closing makes it
routable, or when anything about its blockers changes: release today
is silent, and routing just starts. A crew member hears events
through subscriptions and its `--on` kinds (default: the class
`attention`, docs/subscriptions.md); kinds such as `dep_added` are
plain bead kinds, not attention kinds. Store-logged kinds (state
changes) and dispatcher-logged kinds (`DispatchKinds` in
internal/store/events.go) travel the same inbox.

**Depot removal.** `DeleteDepot` in internal/store/graph.go already
does what the bead asks: it refuses while the depot has open beads
or live sessions, touches no edge, and closed beads keep the depot's
name as history. No design is needed there.

## 3. The proposal

**The edge grows two fields.** `Dep`, `depRow` and the BDP `SnapDep`
gain `At` and `Until`, both meaning "today's behaviour" when empty,
so stored edges without them need no migration. `yardr dep add`
gains `--at <stage>` and `--until <stage>` for kind blocks only
(refused for any other kind: only blocks gates readiness, and
`parent` is train machinery). `--at` must name a stage of the
target's own flow and `--until` a stage of the blocker's, refused
otherwise, as the bead's caveats say; where no flow governs the bead
it may move to any stage, so any well-formed stage name is accepted.
The duplicate key becomes `(from, to, kind, at, until)`, so the
useful pair for xq35 is two commands: `dep add x7wf xq35 --until
review` (wait nowhere once the contract is visible as code) plus
`dep add x7wf xq35 --at approved` (land only once the blocker has
closed). The cycle check stays on the `(from, to)` pairs. `dep rm`
takes the same two flags, defaulting to empty, and removes exactly
that edge. A single edge may set both fields: held at one stage
until the blocker reaches another. The two flags are proposed to
land as ordered beads, `--at` first (Open question 2).

**Readiness becomes one rule.** A blocks edge holds its target while
its blocker is unreleased and (`at` is empty or the target is in
that stage). A blocker is released while it is closed, or while
`until` is set and the blocker is in that stage or in a stage its
done-route leads to from there. The done-route is the chain found
by following the `done` outcome from the stage (`OutcomesFrom`,
implicit single-edge `done` included), stopping at visited stages
and at stages with no `done`. Closing always releases, so won't-do
releases exactly like merging. The check is stateless: a blocker
that goes back (review to new on changes) holds again at the
target's next hand-over; a running session is never stopped. Like
today, the rule gates routing only, never a move: the yardmaster can
still advance a held bead by hand; it waits unrouted.

**What carries it, and what is new.** The `at` half is the one
filter the bead promises: `openBlockers` already walks the target's
incoming blocks edges, and comparing one stored string with the
target's stage needs no flow. The `until` half is the one structural
cost: deciding it needs the blocker's flow, but `ready` and
`openBlockers` are transaction-only helpers today, while resolving a
flow needs the repository's flows (`repoFor`, a base read) for repo
governed beads. The implementation threads a flow lookup into that
read path (`ready`, `openBlockers`, `OpenBlockers`; callers `Ready`
and `Dispatchable` unchanged in shape). Everything else is existing
mechanism: edges, readiness gating routing, events, the CLI printing
what the store returns.

**The yard says the condition in words.** `dep add` and `dep list`
print the non-empty parts: `yardr-x7wf -blocks-> footplate-xq35 at
approved`, `... until review`. `bead show` gains blocked-by lines,
which it has never printed: `blocked by yardr-x7wf at approved`,
`blocked by yardr-x7wf until review`. Prime's `BLOCKED BY` line
carries the same conditions. And since `ready` never lists a waiting
bead, it gains `--waiting`: open, unheld, not-ready beads with why,
in the bead's words:

```text
$ yardr dep add yardr-x7wf footplate-xq35 --at approved
yardr-x7wf -blocks-> footplate-xq35 at approved
$ yardr ready --waiting
footplate-xq35  waits at approved until yardr-x7wf closes
footplate-xq35  waits until yardr-x7wf is in review
```

**Two events, nothing automatic.** The store logs `unblocked` on
the target in the same write that flips it from held to routable,
and `reblocked` in the write that flips it back: a blocker closing,
reopening or advancing, or a blocks edge removed. The data names the
blocker. Both are plain bead kinds like `dep_added`, delivered to
whoever subscribes, rather than attention kinds (Open question 5).
The event means "routable now"; the dispatcher claims the bead on
its next pass, as it claims anything ready. The release the bead
names as "(the target is routed)" is therefore this event plus the
existing claim, not a new routing path. No move is ever made for
anyone: when a blocker goes back after releasing a target, the
yardmaster is told and looks at the target again.

**Close says what it releases.** `bead close` prints, after `closed`,
`releases <ids>`: the open targets for which this bead was the last
open blocker, so the yardmaster sees the consequence before deciding.
It is computed client-side from `Deps` and `OpenBlockers` and needs
no store change, as the bead says the store does not decide. Which
targets are named is Open question 4.

**What the builder and the reviewer see.** The target's worktree is
branched from its depot's base, and the blocker is not on that base:
the builder codes against the written contract (docs/api.md or the
like, on the base) and the blocker's commit named in the target's
body, and the target's gate must pass without the blocker on the
base (xq35 tests against a fake server). The brief and prime show
the conditions, so nobody mistakes the state. The reviewer reviews
the diff against the contract as written. If the blocker then
changes the contract, the review may be stale: the events above tell
the yardmaster the blocker moved, and the yardmaster sends the
target back to `new` when a fresh look is needed. This proposal
makes no move by itself (Open question 3).

**The yardmaster's rule, in docs/line.md.** Use `--until` only when
the contract is written (docs/api.md or the like) and the target's
gate passes without the blocker on the base; name the blocker's
commit in the target's body. It goes where the readiness rule
already lives (Dependencies, Backlog), since docs/pipeline.md does
not exist.

## 4. Left out

**Sending the target back by itself.** When a blocker lands with
changes after the target was reviewed, the target could return to
`new` automatically. Left out because no mechanism can tell a change
that alters the contract from one that does not: it would send back
on every rebase, and a bead that keeps returning is worse than one
the yardmaster looks at once. The events carry the moment; the
decision stays a person's. Open question 3 confirms this.

**Stages on other edge kinds.** Only blocks gates readiness, and
`parent` is train machinery with its own flow and landing rules; a
staged parent would be a second, vaguer train. Other kinds are
records. If a record kind ever needs staging, it is a new proposal.

**Enforcing the contract rule at dep-add time.** The store cannot
check that a contract document exists and agrees, or that a gate
passes without the blocker: both are judgments about another depot's
tree. The docs rule asks them of the yardmaster instead.

**`--at` and `--until` on `bead create --blocked-by`.** Creating
blocked and then adding the staged edge is two commands for a rare
case; create stays small.

**Logging the release where the claim happens.** The dispatcher
could log the release event when it claims a bead that had waited.
But a claim cannot tell a bead that waited from one that never did
without kept state, while the write that flips readiness knows
exactly. Flip-time logging is precise and stateless.

**A sticky release.** "Released once the blocker has reached the
stage, even if it leaves it" would survive detours, but the bead
already chooses stateless ("a blocker that goes back holds again"),
and sticky needs kept state for every edge. The proposal follows the
bead.

**Any-path downstream for `--until`.** Release could cover every
stage reachable from the `until` stage by any transition, not only
the done-chain. But flows loop through `decide` back to `backlog`,
so "reachable" would keep a target released while its blocker sits
back at the start. The done-chain is the only reading under which
"past" means progress.

**A migration.** Stored edges without the fields decode them as
empty, which is today's behaviour exactly. There is nothing to
backfill and no schema step.

## 5. Open questions

**1. A new mechanism, or trains?** Options: build this design; or
keep staged cross-depot work out and let trains carry it, moving the
work into one depot where joint landing is wanted. Trains cost a
shared repository, a shared branch and one landing for work that
lands in two places under two owners; this design costs a new edge
shape, a readiness rule with flow lookups, and new wording and
events. I would take the new mechanism: the motivating work spans
two depots and two landings, which a train refuses by construction
(same-depot children, one unit on the base).

**2. Ship `--at` and `--until` together, or `--at` first?**
Options: both at once, as the beads below order them; or `--at`
alone first, with `--until` a later bead once the filter has proved
itself. `--at` is one string comparison with no flow lookup;
`--until` needs the blocker's flow in the readiness read path, the
design's one structural cost. Together they deliver the xq35 pair
from day one; apart, the first half ships sooner and smaller, but
the motivating case waits. I would take both, as ordered beads with
`--at` first, so each lands working and `--until` can still wait if
its review stalls.

**3. Who watches for a stale review?** Options: the yardmaster,
told by the `unblocked`/`reblocked` events, sends the target back to
`new` when the blocker's changes touch the contract; or the yard
sends it back itself whenever a blocker lands or moves after the
target's review. Automatic costs false returns on changes that
touch nothing the contract promises, and a bead can ping-pong;
manual costs attention and trusts the inbox. I would take the
yardmaster with the events: the bead already says nothing automatic
beyond them, and only a person can tell a contract change from a
rebase.

**4. What does `bead close` name?** Options: only the targets this
close newly releases (`releases a, b`); every open target of the
bead (`also blocked on this: a, b`); or nothing, as today. Newly
released needs one `OpenBlockers` read per open target, all
client-side; every target is cheaper but claims nothing precise;
silence keeps the decision invisible. I would take naming the newly
released: it is the exact consequence the yardmaster decides on.

**5. Plain kinds or attention?** Options: `unblocked` and
`reblocked` are plain bead kinds, and the yardmaster subscribes to
them; or they join the `attention` class, so every yard's default
`--on` hears them. Attention costs inbox noise in yards that never
stage an edge, for an event that needs no action more often than
not; plain costs one explicit subscription where the mechanism is
used. I would take plain, like `dep_added` and `dep_removed`: the
bead asks that they be delivered like other bead events.

## 6. The beads it would become

In landing order. Each lands working and tested; each names what it
leaves to a later bead.

**1. Staged blocks edges in the store.** Goal: `Dep`, `depRow` and
`SnapDep` carry `at` and `until`; `dep add --at/--until` stores
them for kind blocks, validated against the target's and blocker's
flows, with the duplicate key `(from, to, kind, at, until)` and the
cycle check still on `(from, to)`; `dep rm` takes the same flags;
readiness holds while the blocker is unreleased and the stage
matches, with `closed` always releasing. Where to look:
internal/store/graph.go (`Dep`, `AddDep`, `hasEdge`, `RemoveDep`,
`Snapshot`), internal/store/beads.go (`ready`, `openBlockers`,
`Ready`, `Dispatchable`, `OpenBlockers`), internal/store/flows.go
(`resolveFlow`, `OutcomesFrom`) for the done-route walk and stage
validation, internal/cli/beads.go (`depCmd`), internal/store/*_test.go
for the `AddDep` and readiness patterns. Done when: the xq35 pair of
edges stores and lists; a target routes in `new` while held in
`approved`; closing or won't-doing the blocker releases; reopening
holds again; a blocker past the `until` stage along `done` releases
and one that goes back holds again; duplicates on the full key are
refused and cycles still are; old edges behave exactly as today;
the gate passes. Left out: wording beyond `dep add`'s echo (bead
2), events (bead 3), close's line (bead 4).

**2. The yard says the condition.** Goal: `dep list` prints the
non-empty parts; `bead show` gains blocked-by lines with them;
prime's `BLOCKED BY` carries them; `yardr ready --waiting` lists
open, unheld, not-ready beads with why ("waits at approved until
yardr-x7wf closes", "until yardr-x7wf is in review"). Where to look:
internal/cli/beads.go (`depCmd`, `printBeadFlow`, `primeCmd`,
`readyCmd`), the `Dep` fields from bead 1, docs/line.md for the
documented lines. Done when: each surface prints the conditions in
the bead's words; `ready` without the flag is byte-identical to
today; the docs show the lines; the gate passes. Depends on bead 1.

**3. Release and return events.** Goal: the store logs `unblocked`
on the target in the write that flips it held to routable, and
`reblocked` in the write that flips it back (blocker close, reopen
and advance; blocks edge removed), with the blocker in the data;
both travel the inbox like other bead events. Where to look: the
readiness check from bead 1 evaluated before and after in
`changeBead`'s callers (internal/store/beads.go: advance, close,
reopen) and `RemoveDep` (internal/store/graph.go), the `dep_added`
event as the pattern, docs/subscriptions.md for delivery. Done
when: each flip logs exactly one event on the target and no flip
logs none; a reopened blocker logs the return; the yardmaster is
shown subscribed and hears them in a test; the gate passes. Depends
on bead 1. Left out: anything automatic on the return (decided in
Open question 3).

**4. Close names what it releases.** Goal: `bead close` prints
`releases <ids>` for the open targets this close newly releases,
computed client-side. Where to look: the close command in
internal/cli/beads.go, `Deps` and `OpenBlockers` for the reads, bead
2's condition wording for the shared phrasing. Done when: closing a
blocker names exactly the targets left with no open blocker;
closing one of several blockers names none of them; `--json` is
unchanged in shape apart from the added field; the gate passes.
Depends on beads 1 and 2.

**5. The yardmaster's rule.** Goal: docs/line.md says when to use
`--until` (only with a written contract such as docs/api.md, only
when the target's gate passes without the blocker on the base) and
to name the blocker's commit in the target's body; the readiness and
Dependencies sections describe staged edges. Where to look:
docs/line.md (Backlog, Dependencies), the lines beads 2 to 4 print.
Done when: the rule reads as one paragraph a yardmaster can follow;
every new flag, line and event in beads 1 to 4 is documented where
its reader looks; the gate passes. Depends on beads 2 and 3.
