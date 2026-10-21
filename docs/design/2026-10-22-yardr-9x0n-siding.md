# The siding: small beads that wait in a stage and leave folded

Design yardr-9x0n, proposed 2026-10-22. A proposal as it was decided on, not a
description of what the code does today; the decision is on the bead.

## 1. The question

mckean and the yardmaster settled the shape on 21 Oct 2026 (22:45-23:15, in
the yardmaster's pane); the bead asks for the rest. In its words: "small
shaped beads wait in a stage of their depot's flow and leave folded into one
bead, so the yard builds and reviews several small changes in one round
instead of one round each. Today the shaper and the yardmaster bundle by
hand." Decided already, and not reopened here: the siding is a **stage**,
not a bead type; one siding per depot's flow, and the depot is the area (two
unrelated small diffs of one depot bundle together, mem-c05v); the siding is
ready when the count of beads in it reaches **N** (and then everything
waiting is folded, no split) or when **x** hours have passed since the
latest bead entered; a **size ceiling** makes the yard tell instead of fold;
`yardr bead fold` makes one **carrier** bead whose body holds each member's
body as a part, closes each member "folded into <carrier>", moves tracking
links and carries labels; the carrier goes to the siding's **first next
stage**, so the flow says whether it builds at once (`siding -> new`) or
waits for a person (`siding -> ready -> new`). The design is to answer five
questions: what runs the readiness check; whether the fold is automatic;
where N, x and the ceiling live and how `yardr flow show` shows them; what
a member's notes, attachments, asks and dependencies become, and what a
reviewer's split-off of a failing part looks like; and which events there
are and who hears them.

Words used throughout. A **member** is a bead in a siding, or one named to
`bead fold`. The **carrier** is the one bead a fold makes from its members.
A **part** is one member's section of the carrier's body. **Points** are
the size weight the ceiling counts (3.4).

## 2. What exists today

**The hand fold.** It happens all the time: in the 30 days of this yard's
log (`yardr events --kind closed --since 30d`, read 22 Oct) 117 beads were
closed "folded into <id>", into 50 carriers. 26 carriers took one other
bead, 17 two, 5 three, 1 four and one (a yardr-hosted sweep) seventeen.
The shaper's prompt (`~/.yardr/roles/shaper-crew.md`, "A batch", step 3)
writes the shape: keep the member that carries the most, retitle it
`<area> bundle: <part>; <part>`, one `## Part n: <what>` section per member
with the finder's report kept, close each other member `--reason "folded
into <id>"`, "a bundle stays to about four parts and one session's work".
footplate-w1x3 and yardr-vzsy are two such carriers; both end in a `## The
bundle` section ("Done when all parts' lines hold and the depot's test
command is green"). The estimates the carriers got: three `size:small
difficulty:1` members made a `size:medium difficulty:1` carrier
(yardr-58xw); a `difficulty:1` and a `difficulty:2` small made a
`difficulty:2 size:medium` one (yardr-6y82, yardr-ycby). Nothing in the code
knows any of this: there is no fold, dupe or supersede concept for beads;
"folded into" is free text in a close reason.

**footplate's siding.** footplate's `.yardr/flows/default.flow` (footplate
at fdd29ae, landed by footplate-az85) has `backlog -> siding | new |
decide` and `siding -> new | backlog`; `default/siding.md` says no agent
works it and the yardmaster moves a bead on by hand. `yardr flow show
footplate` shows the stage routed to a **manual** group `siding` (members
mckean and the yardmaster). Five beads entered it in 30 days. yardr's own
flow has no siding (`backlog -> shape | new | decide`).

**Flows** (`internal/store/flows.go`). `Flow` is `Name`, `Stages`,
`Transitions map[string][]string`, `Terminal`, `Outcomes`; a stage carries
no metadata. Transitions keep the order they were written in
(`dedupeOrdered`), and `Flow.Next(stage)` returns them in that order, so
"the first next stage" is `Next(stage)[0]`. `ParseFlow` reads edges,
`terminal` and `#` comments, and refuses anything else. The drawing
(`internal/flowdraw`, docs/flows.md "Drawing a flow") already calls a
stage that is off the track and that no outcome leads out of "a siding";
a siding in this design's sense is one, so the word holds in both places.

**The dispatcher's pass** (`internal/dispatch/dispatch.go`). `Run` (587)
runs `Reconcile` on every store write (`Store.Wake`, coalesced over
100 ms), on a 30 s safety tick (`Interval`), and at the earliest of
`timerDue`, `promptDue` and `pushDue` (694-706). `Reconcile` (827-870)
calls, in order, `reap`, `teardown`, `cleanup`, `timers`, `waits`,
`quietHooks`, `crew`, `rules`, `ci`, `issues`, `pushes`, `bases`, `route`
and `backups`.

- `timers` (887) calls `Store.FireTimers(ctx, now)` (`internal/store/timers.go:472`),
  which decides outside the write what is due, then fires it in one
  `s.write` that re-checks each timer, and returns `TimerPass{Fired,
  Dropped, Next}`; `Next` becomes `timerDue`.
- `waits` (`internal/dispatch/waits.go`) is the nearest thing to this
  design: for a bead routed to a **manual** group whose config sets `wait`
  (`internal/store/configkeys.go:84`, docs/groups.md "A bead that waits
  long"), it raises one `waited` event (attention) per stay once the bead
  has been in the stage that long, reading the stay's start from the bead's
  log (`stayOf`) and keeping that event as its own memory. It arms no wake
  of its own and fires on the 30 s tick.
- `route` dispatches **per bead**: `Store.Dispatchable` lists open, unheld,
  unblocked beads, `Match` finds each one's route, and an `exec` group's
  `Exec.Start` (`internal/runner/exec.go:82`) runs its `cmd` once per bead
  with that bead's `YARDR_*` environment. An exec group on a siding would
  run once for each member, never once for the siding (checked: there is no
  per-stage action anywhere; a train collects its children by `parent`
  edges a person adds).

**Groups' config** (`internal/store/configkeys.go`). `ConfigKeys` lists
every key a group may set, with the runners that read it and a type
(`ConfigSpan` for `wait`); `PutGroup`/`SetGroup` refuse any other key. A
group is per depot when a depot route names it (design yardr-w6mh,
`EffectiveRoutes`, `internal/store/routing.go:1447`).

**Beads** (`internal/store/beads.go`). `Bead` (21-72) and `beadRow`
(76-103, which adds `StayStart`, when the current stay began). `newBead`
(393) returns a closure that creates a bead inside a write; `AskNew`
(`asks.go:645`) already runs that closure with more writes in the same
transaction. `w.closeBead` (1892) closes inside a write, takes back the
bead's open **stage asks** (`takeBackAllStageAsks`, `stageasks.go:343`)
and settles peer copies; `checkCloseOwn` (1852) keeps a session's agent to
closing its own bead. `Reopen` (2062) reopens. The store reads a depot's
repository flows before a write, never in it (`s.repoFor` in `advance`,
1557).

**Edges** (`internal/store/graph.go`): `blocks` (with an optional `until
<stage>`), `parent`, `discovered-from`, `related` and any other valid name;
`w.addDep` (459) and `w.removeDep` (586) work inside a write; there is no
re-point. **Notes** (`graph.go:759`) carry files as `BlobRef`s by hash
(`blobs.go:89`); a closed bead's notes stay readable. **Person asks** stay
open on a closed bead (`decisions.go:144`).

**Links** (`internal/store/links.go`): `tracks` and `relates`. A bead
tracks at most one issue per source and an issue has one tracking bead
(`addLink`, 182-212). `MoveLink` (338) moves a tracks link in its own
write, leaving the old bead a `relates` link and logging `moved_to` /
`moved_from`.

**Labels.** yardr gives them no meaning; the rubric is the yardmaster's
(`packs/starter/roles/yardmaster.md`, docs/groups.md): `difficulty:1..3`,
`size:small|medium|large`, `eyes`. `store.DifficultyLabel` (`eta.go:263`)
and the route report read the first `difficulty:` label.

**Events and who hears them.** `store.AttentionKinds`
(`internal/store/crew.go:1040`) is the class `attention`, which the
yardmaster hears by default (`--on attention`); `store.DispatchKinds`
(`events.go:165`) is what `LogDispatch` accepts from the daemon. A
watcher of one bead (`yardr bead watch`) is a subscription `bead=<id>`,
ended when that bead closes (`endWatchesOn`, `yardrsource.go:431`).
Nothing tells an originator by default.

**Trains.** A train (docs/line.md "Trains") is the other bundling: children
on one branch, one review of the whole, "keep a train to about five
wagons, which one review can hold". The ceiling below borrows that number.

## 3. The proposal

The siding is a stage routed to a **manual group whose config sets
`fold`**. The manual group already says "no session starts here"; three new
group config keys say when its beads leave and how many may leave
together. The readiness check and the fold are one store call the
dispatcher makes on every pass, beside `FireTimers`, built the same way:
decide outside the write, re-check and act inside one write, and return
when the next one is due. `yardr bead fold` is the same write with the
members named by hand. New: three config keys, two store methods
(`FoldBeads`, `FoldSidings`), one edge kind (`folded`), three event kinds
(`folded`, `fold_refused`, `unfolded`), two commands (`bead fold`, `bead
unfold`). No new stage kind, flow syntax, setting or group runner.

### 3.1 Where N, x and the ceiling live (question 3)

On the siding's manual group, as `wait` already is:

| key | type | meaning | unset |
|---|---|---|---|
| `fold` | a count, 2 or more | the siding folds when this many members wait | the group is no siding |
| `fold_after` | `ConfigSpan` (`8h`) | the siding folds when its latest member has waited this long | `8h` |
| `fold_max` | a count of points | the most points one fold may carry (3.4) | `6` |

```sh
yardr group add siding --runner manual --members mckean,yardmaster \
  --set fold=4 --set fold_after=8h --set fold_max=6
yardr route add --stage siding --group siding --depot yardr
```

Per depot: a depot route names the depot's own siding group, so yardr and
signalbox may fold at different counts; a yard default route serves every
depot that states none, and each depot's members are still counted apart
(the area is the depot). `fold` is what makes a stage a siding, so
`fold_after` and `fold_max` have defaults and `fold` has none. A siding
whose stage has no next stage, or whose group is not manual, is refused
when the key is set (`ConfigError`, `internal/store/configkeys.go:130`).

`yardr flow show` adds one line under the table for each siding stage, and
a `siding` object in `--json`:

```
flow default (repo@…)
STAGE    GROUP     MEMBERS            OPEN  FILE  NEXT
backlog  backlog   mckean,yardmaster  6     yes   shape | siding | new | decide
siding   siding    mckean,yardmaster  3     yes   ready | backlog
ready    ready     mckean,yardmaster  0     yes   new | backlog
…
siding: folds at 4 beads, or 8h after the latest arrives; at most 6 points; the carrier goes to ready
        now 3 waiting, 4 points; folds at 4 or at 09:40
```

The rows come from `flowViews` (`internal/cli/flows.go:569`), which
already resolves each stage's group by `store.MatchRoute`.

### 3.2 What runs the readiness check (question 1)

`Store.FoldSidings(ctx, at) (*SidingPass, error)`, called by a new
`Dispatcher.sidings` in `Reconcile` right after `timers`, with the same
shape as `FireTimers` and `timers`:

1. Outside the write: list the open beads, resolve each one's route
   (`MatchRoute` with the effective routes), and keep those in a stage whose
   manual group sets `fold`. Group them by **(depot, stage)**: one siding.
2. A siding is **ready** when its eligible members (3.5) number at least
   `fold`, or when `at - latest StayStart >= fold_after`. Otherwise its due
   time is `latest StayStart + fold_after`.
3. For each ready siding, one `s.write` that reads the members again (a
   member that left, or a new one that arrived, changes the set: the write
   folds what is there when it runs, so 8 waiting and 3 arriving fold 11)
   and folds them through `w.fold` (3.3), or refuses (3.4).
4. Return `SidingPass{Folded, Refused, Next}`; `Next` is the earliest due
   time, and the dispatcher keeps it as `sidingDue` and adds it to the
   `next` it arms in `Run` (dispatch.go:694-706).

What it costs: a count reaches N on the very write that brings the Nth
member, since every store write wakes the pass; the time trigger fires on
its due time, not on the 30 s tick, for one more `if` in `Run`. Each pass
lists the open beads once more; `waits` already does the same, and
`sidings` shares its list and routes (one `ListBeads`, one `Routes`) by
taking them as arguments. A daemon that was down folds on its first pass,
as a late timer fires. Rejected: an event on stage entry (it would cover
the count but not the hours, and an entry hook in `move` would fold inside
another bead's write); a yard timer per siding (`yardr timer` tells a crew
member something, it does not act, and it would have to be re-armed on
every arrival).

A siding of one member that is ready by time is not folded: the bead is
moved on by itself (`advanced`, with `by: siding` in its data), since a
carrier of one part is the same bead with a worse title.

### 3.3 The fold (questions 2 and 4)

**Automatic.** The dispatcher folds on readiness; nothing waits for a
person to type `bead fold`. Quota is the flow's business: a depot that
wants a person before the carrier builds writes `siding -> ready`, with
`ready` routed to a manual group, which this yard's depots do (3.7). An
exec group could not do it: it is dispatched once per member (2).

**One write**, `w.fold(members, to, trigger, actor)`, shared by
`FoldSidings` and `Store.FoldBeads(ctx, ids, FoldOptions{To, Title},
actor)` (`yardr bead fold`):

- **The carrier** is made by `newBead`'s closure in the same write (as
  `AskNew` does):
  - **depot**: the members' (one depot, or the fold is refused);
  - **type**: the members' common type, else `task`; the carrier's flow
    must be the members' flow, or the fold is refused (`fold_refused`,
    reason `flow`);
  - **stage**: for a siding fold, `Next(siding)[0]`; for `bead fold`,
    `--to`, else that same stage when every member is in one siding, else
    the members' common stage (two backlog beads folded by hand stay in
    backlog), else the command asks for `--to`;
  - **title**: `<depot> bundle: <title>; <title>`, cut on a word at 100
    characters with `(+n more)`; `--title` replaces it;
  - **priority**: the highest of the members';
  - **originator**: the oldest member's (see Open questions, 5.4);
  - **labels**: the union of the members' labels except `size:` and
    `difficulty:`, plus the carrier's estimate: `difficulty:` the highest
    of the members', `size:` from the points (1 point small, 2-4 medium, 5
    or more large). That reproduces what the shaper gave yardr-58xw,
    yardr-6y82 and yardr-ycby;
  - **body**, in the shaper's shape:

    ```
    Goal: the parts below ride one bead: one builder session and one review for all of them.

    ## Part 1: <member title> (<member id>)

    <member body, verbatim>

    ## Part 2: …

    ## The bundle

    Done when every part's done-when holds and the depot's test command is
    green. Each part is its own commit, its message starting with the part's
    bead id, so one part can be taken out alone (`yardr bead unfold`).
    Folded from <ids> by <actor> (<trigger>).
    ```

- **Each member** gets a `folded` edge from the carrier (`w.addDep(carrier,
  member, "folded", …)`), which is what `bead show` reads for "folded into
  <carrier>" on the member and "folded from <ids>" on the carrier, and what
  `bead unfold` checks. Then `w.closeBead(member, "folded into <carrier>")`,
  which already takes back its stage asks.
- **Notes and attachments** stay on the member: a closed bead's notes are
  read with `yardr bead show <member>`. The carrier gets one note listing,
  per member, its note count and every attached file by name and note
  (`yardr-abcd note 9081: shot-mac.sh`), so the builder knows where to
  look; the files are not copied, a note's file is the record.
- **Asks.** A member with an open person ask is not eligible (3.5), so a
  fold never strands a question; stage asks are taken back by
  `closeBead`, as on any close.
- **Dependencies.** A `blocks` edge from a member to another bead (the
  member blocks Y) is re-pointed: the carrier blocks Y, with the same
  `until`. A member that is blocked is not eligible (3.5), so nothing
  blocks a member at fold time. `discovered-from` and `related` edges stay
  on the member, which is still where it came from.
- **Links.** Each member's `tracks` link moves to the carrier as `MoveLink`
  does (its body lifted into a `w.moveLink`), leaving the member a
  `relates` link. A second tracked issue of a source the carrier already
  tracks becomes a `relates` link on the carrier, since a bead tracks one
  issue per source; the carrier's fold note names it, because that issue
  will not move with the carrier's stage.
- **Watchers.** Each `bead=<member>` subscription gets a copy for the
  carrier before the member closes, so whoever watched a part watches the
  bundle.
- **Events**: one `folded` on the carrier (3.6); the members' `closed`
  events say "folded into <carrier>" as the hand folds' do.

**Who may fold by hand.** `bead fold` applies `checkCloseOwn` to each
member, so an agent in a bead's pane cannot fold other beads; the shaper,
the yardmaster and people can. Members must be open, unclaimed and in one
depot. A hand fold is not held to the ceiling: the person is who the
ceiling tells.

```
$ yardr bead fold yardr-ab12 yardr-cd34 yardr-ef56
yardr-k9p2 folded from yardr-ab12, yardr-cd34, yardr-ef56 (3 points, size:medium difficulty:1), in ready
```

### 3.4 The ceiling

Points per member by its `size:` label: small 1, medium 2, large 4, none 2.
A fold of more than `fold_max` points (default 6: six smalls, or three
mediums) is refused: nothing moves, and `fold_refused` is logged on the
siding's latest member, with the members, their points and `fold_max`. It
is logged once for a set of members: a pass that finds the same latest
member already told says nothing (the event is its own memory, as
`waited`'s is), and a new arrival, which changes the latest member, tells
again. A person then folds part by hand (`bead fold` with the ids, which
the event lists), moves some back to `backlog`, or raises `fold_max`.
`difficulty:` adds nothing: one hard part does not add up with easy ones,
it sets the carrier's difficulty.

### 3.5 Who is folded

A member of a siding fold is a bead in the siding that is open, unheld,
unclaimed, not blocked by an open bead, and has no open person ask: what
`Dispatchable` would call ready, plus the ask. The others wait in the
siding and count for neither N nor x.

### 3.6 Events, and who is told (question 5)

| event | on | data | class | told by default |
|---|---|---|---|---|
| `folded` | the carrier | `members`, `from` (stage or `-` by hand), `to`, `trigger` (`count`, `wait` or `hand`), `points` | none | the members' watchers (copied, 3.3), any subscription that matches the carrier; the `ready` group as any manual stage tells |
| `closed` | each member | `reason: folded into <carrier>` | none | as any close |
| `fold_refused` | the latest member | `members`, `points`, `fold_max`, `reason` (`ceiling` or `flow`) | `attention` | the yardmaster |
| `unfolded` | the carrier | `member`, `to`, `reason` | none | the carrier's watchers |

`fold_refused` goes into `AttentionKinds`, `knownKinds` in
`internal/cli/crew.go:77`, the attention list in
`internal/cli/attention_test.go` and docs/subscriptions.md "The class
attention". There is no `siding_ready`: with the fold automatic, readiness
and the fold are one write, and `folded` says both. (If the operator takes
the other answer to 5.1, `siding_ready` replaces the automatic fold, as an
attention event once per member set, as `fold_refused` is.)

### 3.7 A reviewer's split-off of a failing part

A reviewer who finds one part wrong and the rest good takes that part out
instead of sending the whole carrier back:

```
$ yardr bead unfold yardr-k9p2 yardr-cd34 --reason "part 2 breaks the gate on linux"
yardr-cd34 reopened in backlog, out of yardr-k9p2; revert its commit on yard/yardr-k9p2
```

`Store.Unfold(ctx, carrier, member, to, reason, actor)`: one write that
reopens the member (as `Reopen`) into `--to`, default the flow's first
stage, with the reason as a note; removes the `folded` edge; appends a
line to the carrier's `## The bundle` section (`Part 2 (yardr-cd34) was
taken out by <actor>: <reason>`); and logs `unfolded`. The carrier's own
session may unfold its members (as `checkCloseOwn` lets it close its own
bead). The git side stays the session's: since each part is its own commit
(the carrier's body says so), the reviewer reverts that commit and goes on
reviewing, as reviewers here fix forward, or reports `changes` and names
the commit.

### 3.8 This yard's flows

yardr's `.yardr/flows/default.flow` gains the siding the decision names,
with a `ready` stage so a person still sends each carrier, for quota:

```
backlog -> shape | siding | new | decide
siding -> ready | backlog
ready -> new | backlog
```

with `default/siding.md` and `default/ready.md`, and in the store a
manual `siding` group with `fold=4 fold_after=8h fold_max=6` and a manual
`ready` group (mckean, yardmaster) routed for depot yardr. footplate's
`siding -> new | backlog` becomes `siding -> ready | backlog` the same way
(a footplate bead). The shaper's prompt (in this yard's home, not the
repository) changes from "fold by hand" to "a shaped small bead goes to
siding"; that is the yardmaster's to edit.

## 4. Left out

- **A siding before merge** (grouping approved changes): decided out on
  the bead; trains cover it. The first-next-stage rule would allow it
  later without new machinery.
- **Folding into a train of wagons.** footplate's siding.md names it as the
  other way to bundle. A train costs a parent edge per wagon and a session
  per wagon; the point here is one session. A later bead could add
  `fold_as=train` if parallel building is wanted.
- **Splitting a full siding automatically** at the ceiling. The decision
  says the yard tells instead; choosing which parts go together is a
  person's.
- **Areas narrower than the depot** (a package, a label). mem-c05v made the
  depot the area.
- **Copying attachments** onto the carrier. Notes and files stay on the
  members and the carrier's note points at them; a copy would double every
  blob reference and the `files_max` count for no new information.
- **The ETA of a bead in a siding.** `internal/eta` has no word for "until
  N arrive"; the siding's `flow show` line says when it folds. A later bead
  can teach the ETA the siding's due time.
- **The web view and footplate.** `bead show` and `flow show` carry the
  fold; the web's bead page and footplate's card pick up the `folded`
  edge later.
- **A siding in the starter pack.** The packs ship without one until this
  yard has run it for a while; its flows are the operator's to change.
- **Carrying unrelated depots in one fold**, and folding beads that belong
  to linked yards (peer beads): refused, as their flows and bases differ.

## 5. Open questions

### 5.1 Fold automatically, or tell and wait?

- **(a) The dispatcher folds on readiness** (3.3). Costs the store write
  and nothing else; a person who wants a say writes `siding -> ready`.
- **(b) Readiness raises `siding_ready`** (attention, once per member set)
  and a person or the yardmaster runs `yardr bead fold`. Every fold costs a
  person's turn, which is today's cost with a better tool; the hours
  trigger becomes a reminder.

I would take (a): the flow already carries the person's say, and (b) keeps
the work this design was asked to remove.

### 5.2 Where N, x and the ceiling live

- **(a) The siding's manual group config** (3.1): `wait` already lives
  there, per depot through depot routes, and `yardr group set` changes it
  with no landing.
- **(b) The flow file**, a new line form such as `siding fold 4 or 8h max
  6`: the numbers travel with the depot's repository and its review, but
  `ParseFlow`, `Flow.String`, packs (`pack.Stage`, export) and the drawing
  all learn a stage-metadata form they have none of today.
- **(c) Depot fields** (`Depot` in `graph.go:1225`): per depot by
  construction, but a depot has no settings of this kind and one flow may
  have several stages a person might want to fold.

I would take (a).

### 5.3 The default numbers

- **(a) `fold=4`, `fold_after=8h`, `fold_max=6`** with points small 1,
  medium 2, large 4, none 2. Four is the shaper's "about four parts"; six
  points stays under the trains' five wagons when the parts are mediums
  and lets six smalls go together. At yardr's rate (338 `size:small` beads
  in 14 active days) a siding fills in hours; at signalbox's (17 in 5)
  the hours trigger folds about once a day, and the wait is bounded by
  3 x 8h = 24h.
- **(b) `fold=3`, `fold_after=4h`**: carriers like most hand folds (2-3
  parts), less waiting, more rounds.
- **(c) No defaults for `fold_after` and `fold_max`**: each siding states
  all three, as `wait` has no default today.

I would take (a) for this yard's depots, with (a)'s `fold_after` and
`fold_max` as the defaults in code.

### 5.4 Whose bead is the carrier

The originator decides whose plan pays for the build. Members of one
siding may have different originators.

- **(a) The oldest member's originator.** Simple, deterministic; another
  originator's small change rides on that plan.
- **(b) The operator** whenever the members' originators differ.
- **(c) Fold only members of one originator**: a siding per (depot,
  originator), which narrows the area mem-c05v widened.

I would take (a): on this yard every originator is mckean today, and (b)
is one condition away if that changes.

### 5.5 Members that are not ready

- **(a) Left in the siding** (3.5): held, blocked, claimed or asked members
  wait; the fold needs no edge re-pointing for blockers and strands no
  question.
- **(b) Folded too**: blockers re-pointed to the carrier, open asks moved
  or taken back. Every fold then carries the slowest member's wait.

I would take (a).

## 6. The beads it would become

In landing order. Checked for overlap per mem-b1dm: each done-when names
only its own behaviours, and none lists more than ten.

**1. The fold write: carrier and members.** Goal: `Store.FoldBeads` folds
named beads into one carrier in one write. Where: `internal/store/beads.go`
(`newBead`, `w.closeBead`, `checkCloseOwn`), `graph.go` (`w.addDep`, a
`DepFolded` kind beside `DepBlocks`), `store/api.go` and `go generate
./internal/rpc`. Done when (8): a table test in `internal/store` shows the
carrier's depot, type (common or `task`), stage (`To`), title (cut at 100
with `(+n more)`), priority, originator (oldest member's), labels (union,
estimate from points) and body (parts, `## The bundle`); each member has a
`folded` edge and is closed "folded into <carrier>"; members of two depots,
a closed member and a claimed member are refused; an agent in another
bead's pane is refused; one `folded` event is logged; everything is one
write (a refused member leaves nothing changed).

**2. What moves with a fold.** Depends on 1. Goal: a fold keeps a member's
links, dependents, watchers and asks right. Where: `w.fold` from bead 1,
`links.go` (`MoveLink` lifted into `w.moveLink`), `graph.go`
(`w.removeDep`/`w.addDep`), `yardrsource.go` (watch subscriptions),
`stageasks.go`. Done when (7): a member's `tracks` link moves to the
carrier and leaves `relates` on the member; a second tracked issue of the
same source becomes `relates` on the carrier and is named in the fold
note; a member's outgoing `blocks` (with `until`) is re-pointed to the
carrier; `discovered-from` stays on the member; `bead=<member>` watches are
copied to the carrier; a member's stage asks are taken back; the carrier's
fold note lists each member's note count and files.

**3. `yardr bead fold` and how a fold shows.** Depends on 1. Goal: a
person folds by hand and reads a fold back. Where: `internal/cli/bead_move.go`
or a new `internal/cli/bead_fold.go`, registered in `beads.go`;
`yardr bead show` (folded into / folded from lines, and `--json`);
docs/line.md (a section "Folding beads" after "Trains"). Done when (6):
`bead fold <ids>` prints the carrier line shown in 3.3; `--to` and
`--title` apply; without `--to` it takes the siding's next stage or the
common stage and asks for `--to` otherwise; `bead show` prints "folded
into" and "folded from"; `--json` holds both; docs/line.md describes it.

**4. Siding config keys and `flow show`.** Independent of 1-3. Goal: a
manual group can be a siding, and `flow show` says so. Where:
`internal/store/configkeys.go` (`fold`, `fold_after`, `fold_max`, with a
count type if none fits), `internal/cli/flows.go` (`flowViews`, the line
under the table, `siding` in `--json`), docs/groups.md (beside `wait`),
docs/flows.md. Done when (6): the three keys are accepted on a manual
group and refused on others; `fold` below 2 is refused; the line and the
`now` line print as in 3.1 for a siding with members and without; `--json`
carries them; a stage whose group sets `fold` but that has no next stage
gets a `warning:` line; docs describe the keys.

**5. The readiness check.** Depends on 1 and 4. Goal: the dispatcher folds
a ready siding on time. Where: a new `internal/store/sidings.go`
(`FoldSidings`, `SidingPass`, after `timers.go`'s `FireTimers`);
`internal/dispatch` (a `sidings` step after `timers`, `sidingDue` in
`Run`'s next-due, sharing `waits`' bead list and routes). Done when (9): a
siding folds when its eligible members reach `fold`; one member past
`fold_after` with others folds them all; one alone past `fold_after` is
advanced, not folded; held, blocked, claimed and asked members are
neither counted nor folded; two depots on one yard default route fold
apart; members arriving between decision and write are folded too; a
fold over `fold_max` logs one `fold_refused` (attention) on the latest
member and none on the next pass, and a new arrival tells again; the
dispatcher wakes at the due time (a fake clock in a dispatch test, no
sleep); `fold_refused` is in `AttentionKinds`, `knownKinds`, the attention
test and docs/subscriptions.md.

**6. `yardr bead unfold`.** Depends on 1 and 3. Goal: a reviewer takes a
failing part out of a carrier. Where: `Store.Unfold` beside `Reopen`
(`beads.go:2062`), the CLI beside `bead fold`, the `## The bundle` line,
packs' `reviewer.md` (the starter's and its byte-identical copies, see
`packs/packs_test.go`), docs/line.md. Done when (6): the member reopens in
`--to` (default the flow's first stage) with the reason as a note; the
`folded` edge is gone; the carrier's body gains the taken-out line; an
`unfolded` event is logged; a bead that is not a member of the carrier is
refused; the reviewer role says when to unfold and to revert the part's
commit.

**7. This yard's sidings.** Depends on 5. Goal: yardr's own flow has the siding and
`ready`. Where: `.yardr/flows/default.flow`, new
`.yardr/flows/default/siding.md` and `ready.md`, `default/backlog.md` (a
line naming siding), docs/flows.md's copy of this depot's flow, docs/
glossary.md (`siding`, `carrier`, `fold` as yard words). Done when (4): the
flow parses and `flow show yardr` lists siding and ready with the new
moves; the stage files say who moves what; the docs' copy matches; the
glossary has the three words. The store groups and routes (3.8) and the
shaper's prompt are the yardmaster's after landing; footplate's
`siding -> ready` is a footplate bead filed beside this one.
