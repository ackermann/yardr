# Flows: bead lifecycles

Routes say who works a stage; a **flow** says where a bead may go next. A
bead no flow governs may move to any stage. A flow file has one rule per
line:

```
# flows/code.flow
backlog -> new
new -> review | backlog
review -> approved | new
approved -> merged | merge | new
merge -> review | new | decide
decide -> backlog
merged terminal
```

## Named flows and pointers

Flows are stored once under a name, and depots point at them, so several depots
share one flow instead of copying it:

```sh
yardr flow set code -f flows/code.flow        # store (replace) the named flow
yardr flow list                               # store and repo flows, and what each governs
yardr flow show code                          # one named flow and who points at it
yardr flow rm code                            # refused while a depot, type or open bead uses it

yardr depot flow yardr code                     # depot yardr's beads follow code
yardr depot flow webapp code                    # and webapp's
yardr depot flow yardr --type decision decide   # yardr's decisions follow decide
yardr depot flow yardr --type decision --clear  # back to the depot's flow
yardr depot list                                # shows each depot's flow and its source

yardr flow show yardr                         # a depot: every flow governing it, per stage
```

Changing a named flow with `flow set` changes it for every depot and type
pointing at it, and every bead pinning it, at once. `flow rm` names what still uses the flow (`depot
yardr`, `depot yardr type decision`, `bead yardr-x` for an open bead pinning it)
and removes nothing until they point elsewhere.

`depot flow`, setting or clearing a pointer, checks every open bead the
change would hand to another flow: the depot's beads, or the type's, that
are not pinned and, for the depot's pointer, whose type has no pointer of its
own. A bead sitting in a stage the new flow lacks is named and the change
refused, so a configuration command never moves a bead or leaves one in a
stage nobody is routed to:

```
flow code lacks the stage of open beads in depot yardr: yardr-x (triage),
yardr-y (triage); advance them to a stage the flow has, or pin them to the
flow they follow now (yardr bead update <id> --meta flow=<name>), then
point the depot again
```

Clearing a pointer checks against whatever would govern next (the depot's
pointer, the repository's `type/<type>.flow` or `default.flow`), and
names that flow per bead (`yardr-x (triage, not in flow default)`). A depot
whose flows would come from a checkout git cannot read is refused with
git's error.

`flow set`, replacing a named flow, makes the same check for every open
bead that follows the name: those a depot's or type's pointer at it reaches
(as above), and those pinning it. A bead in a stage the new version lacks
is named with what leads it to the flow, and the stored flow is kept:

```
the new flow code lacks the stage of open beads in depot yardr: yardr-x
(triage); in depot webapp type bug: webapp-y (triage); pinning it: yardr-z
(triage); advance them to a stage it has, or pin them to another flow
(yardr bead update <id> --meta flow=<name>), then set the flow again
```

A flow in a depot's repository has no such check, since no command changes
it and yardr cannot refuse a commit on the base branch: when the base moves
to a version of the file without a stage, an open bead in that stage stays
there, **stranded**. yardr reports it instead (see
[A base change that drops a stage](#a-base-change-that-drops-a-stage)).

### Stage order

A flow's stages are ordered as its rules first name them, and the order
matters: the **first stage** is where a bead in a stage the flow does not
know may rejoin it, by an explicit `yardr bead advance`, and `yardr flow
show` lists stages in it. Such a bead
comes from a repository flow that lost a stage on the base branch, or
from before the depot had a flow: `flow set` and `depot flow` refuse a change
that would leave one (above). The line
format carries the order as written. A JSON flow lists it under
`"stages"`, in full or in part; stages left out follow in the order the
rules reach them from the listed ones (each stage's next stages, in
order). Two or more source stages that no listed or reached stage leads
to have no order, and `yardr flow set` refuses the flow rather than sort
it (`the order of stages alpha, zeta is ambiguous ... list every stage
under "stages" in order`). That includes a plain chain (`a` to `b`, `b` to
`c`) when no stage is listed: nothing says the flow starts at `a`. A JSON
flow with a single source stage needs no list. A JSON flow file in a
repository is held to the same rule; an ambiguous one counts as a file
that does not parse (below).

Flow names use letters, digits, `.`, `_` and `-`. Stores from before named
flows were migrated when the server first opened them: each depot's flow
became the named flow `<depot>`, each type flow `<depot>.<type>`, with the depot
and type pointing at them, so no bead changed lifecycle.

## Repo flows

A depot's repository can carry its flows under `.yardr/flows/`:

```
.yardr/flows/default.flow          the depot's flow
.yardr/flows/type/<type>.flow      the flow for the depot's beads of <type>
.yardr/flows/<name>.flow           a named flow, for pins and pointers
```

yardr reads them from the tip of the depot's **base branch**
(`git cat-file blob <base>:.yardr/flows/...`), never from a bead's branch
or the checkout's working tree: a builder who edits a flow on `yard/<bead>`
changes nothing for that bead. The edit applies once it has been reviewed
and landed on the base. That review applies to a depot that keeps its flow
in its repository; a depot set up by `yardr depot init` follows a pack's flow in
the store ([below](#named-flows-and-pointers)), which is reviewed where the
pack is kept and changes when the operator applies the pack again
([configuration.md](configuration.md#why-the-repo-holds-what-the-work-is)). yardr resolves the base on every check and re-reads
the files when its commit moves (it caches them by commit). A depot without a
path, or whose base does not exist in its checkout (`git rev-parse --verify
--quiet` finds nothing), has no repo flows. Any other git failure, such as a
path that is not a checkout, is an error for the beads that need the repo's
flows, not "no flows".

A file that does not parse is shown by `yardr flow list` with its error;
a bead it would govern cannot be created or moved until the base is fixed,
rather than silently running without its flow.

## A base change that drops a stage

A commit on the base branch that removes a stage from a repository flow
(`default.flow`, a type's file, or a named file a pin or pointer follows)
takes effect as soon as yardr sees the base move. An open bead sitting in
the removed stage is **stranded**: its stage is not in the flow that governs
it. yardr does not move it and does not refuse the commit; it says so in
three places until someone moves the bead:

- `yardr depot check <depot>` names each stranded bead of the depot, with its
  stage, the flow and the commit the flow was read at, and fails:

  ```
  stranded: bead yardr-x is in stage triage, which flow default (repo@9bf658a) lacks; advance it to a stage the flow has (yardr bead advance yardr-x --to <stage>)
  depot yardr: FAIL (open beads in a stage their flow lacks: 1)
  ```

  With a red gate it exits 1 as before. With a green gate it exits 2, not
  1: no branch can fix a stranded bead, so the assembly holds the bead it
  was landing instead of sending it back to its builder. `--json` lists
  them under `stranded`.
- `yardr flow show <depot>` prints a warning per bead under the flow that
  lacks its stage, next to "stage has no route".
- The dispatcher logs one `stranded` event for the bead (`stage`, `flow`,
  `source` with the commit), which a crew member hears like `unrouted` (it is
  of the class `attention`, which is the default `--on` and the starter
  pack's `hears.events`). It is
  logged again only when the stage or the flow changes, not when the base
  merely moves on, and once more after a daemon restart.

A stranded bead is **not routed**: the removed stage's route usually still
exists, and would hand the bead to its old group. No group claims it until
someone advances it to the flow's first stage, the one move the flow
allows from a stage it does not know
(`yardr bead advance <id> --to <first stage>`, see
[What a flow enforces](#what-a-flow-enforces)); from there it is routed
like any bead. A bead a session already holds when the stage goes is left
to that session: its `yardr bead done` is refused with the allowed move,
and it is reported once the session ends.

To drop a stage without stranding anything, move its beads out first, or
keep the stage in the flow with only a way out (`triage -> new`) until it
is empty.

## A flow that does not resolve

A pin naming a flow that is gone, a repo flow that does not parse, or a
checkout git cannot read leaves the bead where it is, never ungoverned:

- its moves (`advance`, `done`) are refused with the error;
- the dispatcher records one `bad_flow` event for it (again only if the
  reason changes), does not claim it, and goes on dispatching every other
  bead;
- `yardr bead show` and `yardr prime --bead` print `flow error: ...` in place
  of the flow, and `yardr review` shows the error with only diff and skip.

## Which flow governs a bead

```
pin on the bead  >  store pointer  >  repo .yardr/flows  >  none
```

- **Pin**: the bead names its flow in its metadata
  (`yardr bead update <id> --meta flow=<name>`, or `bead create --meta`).
  The pin governs even a bead without a depot. `create` and `update` refuse a
  pin naming no flow (`the bead pins flow ghost: not found`); a pin that
  stops resolving later, say a repo flow removed from the base, refuses the
  bead's moves (see above).
- **Store pointer**: the type's pointer, else the depot's (`yardr depot flow`).
- **Repo**: `type/<type>.flow`, else `default.flow`, on the base branch.
- **None**: any stage is allowed.

The store wins as a whole: a depot pointer in the store overrides the repo's
type files too. A name, pinned or pointed at, is looked up among the store's
named flows, then in the depot's repository (`.yardr/flows/<name>.flow`).
Journals and memories are never governed.

`yardr bead show` and `yardr prime --bead` say which flow governs a bead
and where it came from:

```
  flow code (store)                       a store pointer for the depot
  flow decide (store, type decision)      a store pointer for the type
  flow default (repo@9bf658a)             the repo's default.flow at that base commit
  flow type/bug (repo@9bf658a, type bug)
  flow hotfix (pin)                       the bead's own pin
  flow alt (pin, from repo@9bf658a)       a pin naming a repo flow
```

## What a flow enforces

With a flow in place:

- `yardr bead advance` refuses a move the flow does not allow and lists the
  allowed next stages (`allowed next: approved, new`).
- Entering a **terminal** stage closes the bead in the same write, with the
  stage as close reason (`closed: merged`).
- `yardr bead create -s <stage>` must name a non-terminal stage of the flow.
  Without `-s` a bead starts in the flow's first stage (`backlog` in the
  example and in the starter flow, where the yardmaster shapes it before
  advancing it to `new`); a bead no flow governs starts in `new`. The
  `created` event says where (`"stage"`, with the bead's `"title"`, `"type"`
  and, when it has one, `"depot"`), as `advanced` says `"from"` and `"to"`:
  the event log alone places a bead in its stage.
- A bead in a stage the flow has no way out of (a stage the flow does not
  know, or a terminal stage after `yardr bead reopen`) may move only to the
  flow's first stage (`backlog` in the example), so it is never stuck but
  cannot skip ahead to `approved` or `merged`.
- `yardr prime --bead` prints the stage's outcomes, or the allowed next
  stages when it has none; the store API exposes both as `NextStages`.
  Agents' briefs list them the same way, with the exact command for each.

## Outcomes: agents say how the work ended

Agents should not choose stages: they report how their work ended, and the
flow decides where the bead goes. An edge may carry an **outcome** with
`on <outcome>`:

```
new      -> review on done | decide on question | backlog
review   -> approved on done | new on changes | decide on question
approved -> new on failed | merged
```

```sh
yardr bead done <id>                          # outcome done
yardr bead done <id> --outcome question       # the edge "on question"
yardr bead done <id> --outcome changes --note "rename the flag"
```

`yardr bead done` resolves the outcome for the bead's current stage under
the flow that governs it and makes the same move `advance` would (members
checked, session ended, a terminal stage closes the bead, the note added in
the same write). The `advanced` event records the outcome (`"outcome":
"changes"`).

- **Implicit done.** A stage that names no outcomes but has exactly one way
  out offers `done` for it: `backlog -> new` needs no `on done`. A single edge
  that names its outcome (`approved -> new on failed`) offers only that one.
- **Bare edges** (no `on`) are not outcomes: `review -> backlog` or
  `approved -> merged` above are reachable only by an explicit
  `yardr bead advance --to`, for humans and the yardmaster. `advance --to` still
  works for every edge and is still checked against the flow.
- **Unique per stage.** A stage may name each outcome once (two outcomes may
  lead to the same stage); `flow set`, and a repo flow, refuse duplicates and
  outcomes on terminal stages. Outcome names use letters, digits, `.`, `_`
  and `-`.
- **Errors.** An unknown outcome is refused with the stage's outcomes
  (`outcomes: done, question`); a stage without outcomes, or a bead no flow
  governs, is refused with a pointer to `advance --to`.

In JSON a flow carries them per stage, in order:
`"outcomes": {"review": [{"name": "done", "to": "approved"}, ...]}`; an
outcome's stage becomes a transition if it is not one already.

Agents' briefs list the stage's outcomes, one line each saying where the
bead goes and who picks it up there, with the exact `yardr bead done`
command, instead of stage names; `yardr prime --bead` prints them under
`OUTCOMES:`. `yardr review` offers a stage's outcomes as its actions
(`changes: send back to new (note required)`). `yardr flow show` writes each
edge with its outcome. A stage without outcomes keeps listing its next
stages, so flows without outcomes behave as before.

## Stage files: what a stage is for

A stage can carry a markdown **stage file**: what the stage is for, what
must be true to leave it, and when each outcome applies. Whoever works a
bead in the stage is shown it, whichever group they belong to: an agent in
its brief, a person or a crew member with `yardr prime --bead`. Stage files are
optional; a stage without one behaves as it always did: its brief has no
stage section, `prime` no `STAGE` block, and its line on
[the map of the flow](#the-map-of-the-flow) says only who works it.

A stage file belongs to one flow, not to a stage name: `review` in `default`
and `review` in `yardr.train` are two files. It lives where its flow lives.

**A flow in a depot's repository** has them beside the flow file, in a
directory of the flow's name:

```
.yardr/flows/default.flow
.yardr/flows/default/review.md         review, in the depot's flow
.yardr/flows/type/train.flow
.yardr/flows/type/train/review.md       review, in the flow of type train
.yardr/flows/<name>.flow
.yardr/flows/<name>/<stage>.md
```

They are read like the flow itself: from the tip of the depot's base branch,
never from a bead's branch or the working tree, so a bead cannot rewrite the
definition of the stage it is in. An edit applies once it has landed on the
base. A dir depot has no base and reads the directory as it is.

**A flow in the store** (`yardr flow set <name>`, followed with `yardr depot
flow <depot> <name>`) has them as plain files in the yard's home, outside any
depot:

```
~/.yardr/flows/<name>/<stage>.md       e.g. ~/.yardr/flows/code/review.md
```

The store holds no prose, as with role prompts. Every depot that follows the
flow reads the same files, as they are on disk when the brief is written,
and no bead's branch can change them. A pack's import copies the stage
files of its flows there ([packs.md](packs.md#how-a-pack-maps-to-the-yard)).
The depot's repository is not asked: a
`.yardr/flows/code/review.md` in a depot that follows the store flow `code` is
shown to nobody (and fails the depot's check, below). A name that a pin or a
pointer finds only in the depot's repository is a repository flow.

A stage whose name holds a slash has its file a directory deeper
(`default/qa/smoke.md` for `qa/smoke`). An empty file counts as none.

Where it shows:

- **The brief.** Under `## Stage <stage>`, after the role prompt and the
  bead, right before the outcomes. The heading is the brief's, so the file
  needs none; a file that uses headings should start at `###`.
- **`yardr prime --bead <id>`.** As `STAGE <stage> (<path>):` before
  `OUTCOMES:`, and in `--json` as `next.stage_file` (`path`, `text`). Above
  it, the `FLOW` block has one line of every stage's file
  ([The map of the flow](#the-map-of-the-flow)).
- **`yardr flow show <depot>`.** The `FILE` column says per stage whether it
  has one (`stage_file`, the path, in `--json`). `yardr flow show <name>`
  lists the stages of a store flow that have one and where the files live.
- **`yardr depot check`.** A note names, per flow governing the depot, the
  non-terminal stages without a stage file; it never fails the check. A
  stage file named after no stage of its flow does fail it, since nobody
  would otherwise see the typo: `default/reveiw.md`, or a file in a
  directory with no flow file beside it. Repository stage files are checked
  in the tree the gate runs in (`--dir`, a bead's worktree), not on the
  base, so the typo is caught before it lands and its fix can pass: exit 1,
  the branch's to fix. A stray file of a store flow in the yard's home is no
  branch's to fix: exit 2 when nothing else fails, and `yardr flow show`
  warns of it too.
- **`yardr depot sources <depot>`.** One line per non-terminal stage of the
  flows governing the depot: where its stage file comes from, or `none`, and
  the repository's stage file it shadows when a store flow governs
  ([configuration.md](configuration.md#where-a-depots-flows-stage-files-and-roles-come-from)).
  `yardr depot check` names each shadowed file in a note.

What goes in a stage file and what in a role prompt is in
[groups.md](groups.md#role-file-or-stage-file). The packs yardr carries have a
stage file for every stage of their flows, which `yardr depot init` installs
in the yard's home
([configuration.md](configuration.md#seeding-a-depot-yardr-depot-init)), and depot
yardr has one for every stage of both its flows
([`.yardr/flows/default/`](../.yardr/flows/default),
[`.yardr/flows/yardr.train/`](../.yardr/flows/yardr.train)).

### The map of the flow

`yardr prime --bead <id>` draws the whole flow before the `STAGE` block, so
whoever works a bead sees how big the flow is, who works each stage, where
the bead stands and where it has been. The brief an agent is started with
has the same block, under `## Flow <name>` directly before its stage
section, so a bead sent back says so in the first text its new session
reads:

```
FLOW default · 6 stages (context: your stage's file below says what you must do)
  backlog    people   New beads wait here, where `yardr bead create` puts them, until the yardmaster has shaped them.
> new        agents   A builder implements the bead here, on branch `yard/<bead>` in the bead's own worktree.   <- you are here
  decide     people   A bead here waits for a person: Christopher decides.
  review     agents   The finished bead is reviewed against its goal; what the reviewer can fix is fixed, not sent back.
  approved   script   The assembly gates the bead and lands it on `main`, one bead at a time.
  merged     end
PATH  new -> review -> new (changes)
```

The map is context: with a testing stage later in the flow, a builder can
see what is not its job. What must hold before the bead leaves its stage is
in that stage's file, which is the only one printed in full; the heading
says so, or `(context: it does not say what you must do)` when the stage has
no file. The block is the same whoever asks and the same in the brief,
drawn as the bead is handed out; a bead no flow governs has none, in prime
and in its brief. An exec group's script gets no brief and no map.

- **Order.** The flow's stages in the order its file first names them, so
  what a rule leads to comes before the stage of the next rule (`decide`
  above, which `backlog -> new | decide` names). A train, or any bead of a
  type with its own flow, gets the map of that flow.
- **Who.** From the route a bead of this depot and type (and these labels)
  would take in the stage, by the group's runner: `agents`, `script` for an
  exec group, `people` for a manual one, `end` for a terminal stage whatever
  is routed there, `nobody` for a stage without a route. Groups are not
  named.
- **The line.** The first sentence of the stage's file: its first
  paragraph up to the first `.`, `!` or `?` that a space or the paragraph's
  end follows, on one line. A line longer than 100 characters is cut on a
  word and ends in `…`. When the first sentence does not serve, the file
  starts with a line `summary: <text>`: that is the line then, and it is
  for the map alone: the `STAGE` block, the brief and the web view leave it
  out (`next.stage_file.text` in `--json` is the whole file). A stage
  without a file has no line.
- **`>` and `<- you are here`** mark the stage the bead is in.
- **`PATH`.** Every stage the bead entered, oldest first, from its
  `advanced` events; a bead that never moved has no `PATH` line. A move to
  a stage that is not later in the flow's order (the bead was sent back, or
  stayed for another try) names the outcome that took it there, or `by
  hand` for a move that reported none (`yardr bead advance --to`). Of more
  than eight stages the first two and the last five are printed, with `…`
  between.
- **`--json`.** `flow`: `name`, `stages` (each `name`, `who`, `summary`,
  `current`) and `path` (each `stage`, and `back` where the move went
  against the order: the outcome, or `by hand`), which is never shortened.

## Drawing a flow

yardr draws a flow as a signalling diagram, in SVG on standard output. This
is the starter pack's:

![The starter pack's flow as a signalling diagram](flows/starter.svg)

Its train flow has the path through review and landing:

![The starter pack's train flow as a signalling diagram](flows/starter.train.svg)

```sh
yardr flow show yardr --svg > flow.svg                # a depot's flow, with the groups its routes name
yardr flow show yardr --svg --flow train > train.svg  # another of its flows, by name or bead type
yardr flow show code --svg                            # a named flow of the store
yardr flow draw .yardr/flows/default.flow             # a flow file
yardr flow draw starter                               # a pack yardr embeds, by name
yardr flow draw ./packs/dir --flow train              # a pack's directory, and which of its flows
```

`flow draw` reads files only and needs no yard. All of them draw with one
function, so the same flow with the same groups is the same file whichever
way it was asked for. A depot or pack with several flows draws its own (a
pack's first) unless `--flow` names another, and says on standard error which
others there are.

How to read it:

- **The track** runs left to right past a platform per stage, in the order
  a bead travels from the first stage, the terminal stages last: their
  platforms are dashed, and the track ends in a buffer stop. The heavy track
  from one platform to the next is the stage's way on: its outcome `done`,
  else its first outcome, else (a stage without outcomes) its first
  transition. Where two platforms only stand next to each other, with no
  such transition between them, the track is dotted.
- **Below the track** is every other transition between platforms: a line
  that leaves the track after its stage, with the outcome that takes it, and
  rises to the track, at the arrowhead, before the stage it leads to. Most
  lead back (`review` to `new` on `changes`); one that leads forward passes
  a platform. Lines to the same stage join.
- **A siding**, above the line, is a stage that is not on the way from the
  first stage and that no outcome leads out of: a bead waits there for a
  person (`decide`). A spur rises to it after each stage that leads there,
  with its outcome, and one comes down before each stage a person may move
  the bead on to. A move from one siding to another is not drawn.
- **A dashed line** is a move no outcome takes: someone advances the bead by
  hand (`yardr bead advance`).
- **A signal** stands at a stage that can report `changes`: a review.
- **A building** under a platform (beside a siding's name) is the group that
  works the stage: a shed with a bay per bead the group works at once (its
  limit; at most six are drawn, and the name then says the limit) for a
  group that runs agent sessions, a station building for a `manual` group, a
  works for an `exec` group, and a dashed line with a goods wagon for a
  group whose beads another yard works. A flow file names no group, so its
  drawing has no buildings.

Which stages are platforms and in what order follows from the flow alone,
never from who works a stage, so a flow file, a pack and a depot draw the
same line for the same flow. No bead is drawn: the picture is of the flow,
not of the yard.

The file carries a light and a dark set of colours and picks by
`prefers-color-scheme`, also when a page shows it as an image, as GitHub
shows a README's; it paints its own background, so it reads on a page of
either colour. It names its faces (IBM Plex Mono, Barlow Condensed) and
falls back to the machine's monospace and sans-serif.

`docs/flows/starter.svg` above is written by `go generate ./internal/flowdraw`
from `packs/starter/pack.yaml`, one file per flow of the pack
(`starter.<flow>.svg` for any but its first). A test fails when the
committed file is not what the generator writes now, so after a change to
the starter pack's flow or to the drawing, generate and commit.

## Type flows

A type's flow governs that type's beads in the depot; every other type
follows the depot's flow. `yardr flow show <depot>` lists the depot's flow first,
then one per type, and warns about non-terminal stages no route sends
anywhere: beads there wait for a human.

Type `train` has its own flow in depot yardr, `yardr.train` (below), for
integrating a train's branch and landing it on main. Its children follow the
depot's flow like any bead; what a train changes for them is only their base,
see [line.md](line.md#trains).

Type `wagon` has its own flow in depot yardr too, the repository's
[`.yardr/flows/type/wagon.flow`](../.yardr/flows/type/wagon.flow), with its
stage files in `.yardr/flows/type/wagon/`. No pointer is needed: a
`type/<type>.flow` governs the type by being there. It is the depot's flow
without the review stage:

```
# type/wagon.flow
backlog -> new
new -> approved on done | decide on question | backlog
approved -> merged on done | new on failed | decide on question
decide -> backlog | new
merged terminal
```

A wagon is a child of a train that lands on the train's branch under the
depot's light gate, with no review of its own; the full gate and the review
are the train's ([line.md](line.md#wagons)). The stage names are the
depot's, so the depot's routes send wagons to the same groups. One rule is
yardr's, whatever the flow says: a wagon with no parent train stays in its
flow's first stage. Moving or creating it anywhere else is refused.

## Terminal stages

A line `<stage> terminal` marks a stage that ends the bead: advancing into it
closes the bead with the stage name as its reason, in one write. A terminal
stage has no outgoing rules and needs no route. `yardr bead close` still
works without one, for beads that end outside the flow (the reference
assembly falls back to it when the flow has no `merged`, see
[line.md](line.md#4-approved-the-assembly-lands-it-on-main)).

## Worked example: depot yardr

This is the depot flow yardr reports in `yardr flow show yardr` (as of
2026-10-03). The live `OPEN` counts are omitted because they change as beads
move:

```
flow default (repo@2c2a589)
STAGE     GROUP            MEMBERS            NEXT
backlog   backlog          mckean,yardmaster  new | decide
new       yardr-builders   -                  review on done | decide on question | backlog
decide    decisions        mckean,yardmaster  backlog | new
review    yardr-reviewers  -                  approved on done | new on changes | decide on question
approved  yardr-refinery   -                  merged on done | new on failed | decide on question
merged    -                -                  (terminal: closes the bead)
```

The depot's flow is the repository's
[`.yardr/flows/default.flow`](../.yardr/flows/default.flow), read from main;
no store pointer overrides it. It governs every bead that is not a train
(tasks, features, bugs):

```
# default.flow
backlog -> new | decide
new -> review on done | decide on question | backlog
review -> approved on done | new on changes | decide on question
approved -> merged on done | new on failed | decide on question
decide -> backlog | new
merged terminal
```

| stage | group | runner |
|---|---|---|
| backlog | `backlog` | manual (Christopher and the yardmaster, who shapes beads here) |
| new | `yardr-builders` | herdr, Claude Code, limit 5, `prompt=roles/builder.md` |
| review | `yardr-reviewers` | herdr, Codex, limit 3, `prompt=roles/reviewer.md` |
| approved | `yardr-refinery` | exec, limit 1, [`contrib/assembly/merge-local`](../contrib/assembly/merge-local) |
| decide | `decisions` | manual (Christopher and the yardmaster) |

The assembly lands an approved bead and advances it to `merged`, which
closes it; a red gate sends it back to `new`. The flow has no `merge`
stage, so a rebase that conflicts goes back to `new` as well: merge-local
falls back to the `failed` outcome when the flow refuses `merge`. A flow
with a `merge` stage and a merger group resolves conflicts without a
builder ([line.md](line.md#5-merge-a-merger-resolves-conflicts)).

Before the rename the depot was `yard`, and its flows lived in the store as
the named flows `yard` and `yard.decision` (migrated from the depot and type
flows they were), the second a type flow for decision beads (`decide ->
decided`, `decided terminal`). Depot yardr has neither: a decision is a bead
in `decide`, closed with the answer as its reason.

Trains follow `yardr.train`, which the repository carries as
[`.yardr/flows/yardr.train.flow`](../.yardr/flows/yardr.train.flow) (a named
repo flow, read from main; `yardr depot flow yardr --type train yardr.train`
points the type at it, which `flow show` prints as `flow yardr.train (store,
type train, from repo@2c2a589)`):

```
# yardr.train.flow
backlog -> open
open -> review on done | open on failed | merge on conflict | backlog
review -> approved on done | open on changes | decide on question
approved -> in-pr on done | open on failed | merge on conflict | merged
in-pr -> land on done | open on changes | decide on question
land -> merged on done | open on failed
merge -> open on done | open on rework | decide on question
decide -> open | backlog
merged terminal
```

Depot yardr has the flow but, as of 2026-10-03, only the depot's stage routes:
a train in `backlog`, `review`, `approved` or `decide` goes to the same
group as any bead, and `open`, `merge`, `in-pr` and `land` have no route
(`flow show` warns for each: beads there wait for a human). The groups in
the table that the depot lacks are the ones
[line.md](line.md#setup-2) sets up.

| stage | group | runner |
|---|---|---|
| backlog | `backlog` | manual (the yardmaster adds children, then advances to open) |
| open | none yet; `yardr-integrators` (route `--type train`, priority 10) | exec, limit 1, [`contrib/assembly/integrate-train`](../contrib/assembly/integrate-train) |
| review | `yardr-reviewers` | as for any bead; the brief says it is a train and gives the range |
| approved | `yardr-refinery` | merge-local, as for any bead; on the PR path `yardr-pr` (route `--type train`, priority 10): exec, limit 1, [`contrib/assembly/pr-train`](../contrib/assembly/pr-train) with `YARDR_PR=1` |
| in-pr | none yet; `decisions` (route `--type train`), PR path only | manual: `yardr bead done <train>` means "merge it" |
| land | none yet; `yardr-pr-merge` (route `--type train`), PR path only | exec, limit 1, [`contrib/assembly/pr-merge`](../contrib/assembly/pr-merge) with `YARDR_PR=1` |
| merge | none yet; `mergers` | herdr, Claude Code, `prompt=roles/merger.md`, on branch `yard/<train>`; `done` goes back to `open`, where the integrator gates and pushes it again |
| decide | `decisions` | manual |

A train waits in `open` while any child is open: each child blocks it
(`dep add -k parent` adds that edge too), and routes only send ready beads.
Where `new` is a bead's way back to its builders, `open` is a train's: the
fix is a new child, which blocks the train until it lands.

A train lands one of two ways from `approved`. Locally, the default (no
`YARDR_PR`, `approved` routed to `yardr-refinery`): merge-local
fast-forwards main and advances the train straight to `merged` by the bare
edge, which no outcome offers, so an agent reporting `done` in `approved`
always means the PR path. Through a pull request, experimental and off
unless `YARDR_PR=1` is in the cmd of integrate-train, pr-train and pr-merge:
pr-train pushes the reviewed branch and updates the PR, reporting `done` to
`in-pr`; a human there reports `done` to `land`, and pr-merge merges the PR
with a rebase and reports `done` to `merged`. Without the switch pr-train
and pr-merge hold the train with a note instead, so nothing reports `done`
in `approved` and `in-pr` is not reached. The details are
in [line.md](line.md#trains).

How beads actually move through these stages is in [line.md](line.md).
