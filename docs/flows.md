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

A yard that trusts the line drops a stage the same way. The starter pack's
default flow holds a reviewed bead in `clear` for the person's yes before it
lands; once the person would always say yes, the yard edits the flow with
`yardr flow set` so `review-security`'s `done` and review's `done-safe` point
at `approved` again. `flow set` refuses while any open bead sits in `clear`,
so those are sent on or back first.

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
two places until someone moves the bead:

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
  Filing in a stage past the first honors the members gates exactly as
  `advance` does: when every stage the flow's transitions lead into it from
  is held by a group with members, only a member of one of those groups may
  file there (an exec session counts as its group, as it does for a move
  out), and the refusal names the blocking groups. A hand fold's carrier
  (`yardr bead fold --to <stage>`) is filed the same way; a siding's own
  fold is not. Where any stage before
  it is held by no members group — most flows — `-s` files as before, and
  the first stage takes a bead from anyone.
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

A gate is a stage whose group runs a check: route it to a script group
([groups.md](groups.md#runners)), and the flow says where its `done` and its
`failed` lead.

### A limit on rounds

An outcome that sends a bead back to a stage it has been in (`changes` from
review, `failed` from the assembly) may limit how many times it may, and say
where the bead goes instead. The words follow `on <outcome>`:

```
review -> approved on done | new on changes max 3 then decide | decide on question
```

`changes` may send the bead back to `new` three times (`max 3`); after that it
sends it to `decide` (`then decide`). In JSON the outcome carries the words as
`"max": 3, "then": "decide"`; `yardr flow show` writes them as the flow file
does.

`then` names a stage as any edge does, so it may be a stage that closes the
bead, or one no other edge names, which the edge then adds to the flow. It
becomes a transition of the stage, like the outcome's own stage, so `yardr
bead advance --to` allows the move too. A flow is refused, by `flow set` or
as a repo flow that does not parse, when a limit:

- gives `max` without `then`, or `then` without `max`;
- has a `max` that is not a whole number of rounds, 1 or more (leave the
  words out for no limit; `max 0` is refused, not read as one);
- has a `then` equal to the stage the outcome already leads to, which would
  send the bead where it was going;
- is on `done`, or on a bare edge, which is no outcome. The arrival estimate
  follows `done` on and does not count rounds, so a `done` that went elsewhere
  would make it name a stage the bead does not reach; a limit belongs on an
  outcome that sends work back.

yardr counts a bead's rounds (`rounds` in `yardr bead show --json`: for each
stage the bead has reached, how many times an outcome brought it back to that
stage, a same-stage outcome included; a move by hand adds none) and sends it to
`then` once they are used. The count is the stage's, whichever outcomes made
it: `changes` from review and `failed` from the assembly, both back to `new`,
add to one count for `new`, and each one's limit reads it. An outcome whose
stage has a count of its `max` or more sends the bead to `then` instead. It is
"or more" because two outcomes back to one stage may carry different limits
(`changes max 2` and `failed max 3`, both to `draft`): the higher one can raise
the count past the lower, and the lower still sends the bead on. In the same
write:

- the `advanced` event has `"to"` as `then`, and beside `"outcome"` three
  fields: `"rounds"`, the count; `"max"`; and `"instead_of"`, the stage the
  bead did not go back to;
- the count for that stage starts again at zero, and keeps its key. A person
  who sends the bead back (`yardr bead advance --to`) has granted it a fresh
  allowance by doing so. Nothing else resets a count: a bead that asks a
  question in the middle of a loop (`question`, to `decide`) keeps it, so
  asking once a round does not keep the bead going;
- a `rounds_reached` event is logged on the bead, of the class `attention`
  ([subscriptions.md](subscriptions.md#the-class-attention)), by `yard`, with
  `"stage"` (where the outcome was reported), `"outcome"`, `"rounds"`, `"to"`
  (the stage the bead did not go back to) and `"then"`.

The bead is not held: it is moved, and a `then` that people work stops the
sessions on its own (`decide`, in the starter flow). The note given with the
outcome is attached as usual, so whoever the bead goes to reads why the last
round was asked for. The move is made as any other is: only members of the
stage's group may move a bead out of it (and only a member of such a group
may file a bead past it in the first place: `bead create -s`, above), and a
wagon with no train may not be put in any stage but its flow's first. A refusal leaves the count and the
stage as they were, and a stage ask answered with a pick that fires a limited
outcome redirects the bead the same way.

The packs that ship with yardr carry limits ([packs.md](packs.md#packyaml)).
The starter pack's default flow limits its four ways back to `new` (`changes`
from `review`, from `review-security` and from `clear`, `failed` from
`approved`) to 3 rounds, counted together, and sends the fourth round to
`decide`. The website pack's default flow, the starter's with a deploy stage,
has the same four. The design pack's
flow limits `changes` from `critique` to 2 rounds and `failed` from `approved`
to 3, both back to `draft`, then `decide`. The train flow has none. A yard that
has the flow gets the limits when it imports the pack again (`yardr pack check`
shows the change first), and a flow in a depot's repository has them when its
file does: depot yardr's own,
[`.yardr/flows/default.flow`](../.yardr/flows/default.flow), carries the
starter's three.

Where a limit is in play, the bead and whoever works it see it:

- `yardr bead show` and `yardr prime` print a line under the stage for each
  limited outcome of it whose stage has rounds counted, and nothing for a
  stage without a limited way back or a bead that has not gone round:
  `rounds new 2 of 3 (the next changes from review goes to new)`. It is the
  stage's count over the outcome's `max`, and where the next report of the
  outcome goes. Two rounds of three are used, so that report is still the
  third and goes back to `new`; with three used it goes to `decide`.
- `OUTCOMES:` in `yardr prime` and `## Outcomes` in an agent's brief name the
  stage a report will really take the bead to, with the round it is while the
  bead is within the limit (`changes (to new, round 3 of 3)`) and that the
  rounds are used once they are (`changes (to decide: the 3 rounds back to new
  are used)`), and the brief says who picks the bead up there. A reviewer whose
  `changes` would reach a person can write the note for them.
- `yardr prime --json` carries each outcome of the stage under `next.outcomes`
  as the flow has it (`name`, `to`, `max`, `then`) and as it stands for the bead:
  `goes`, the stage a report takes it to now; `used`, the rounds counted; and
  `round`, which round the report would be. The bead's own `rounds` are as they
  were. The web bead page shows the same, and says why a bead that a limit sent
  to a stage people work waits there ([web.md](web.md#the-right-column)).

The stage a report goes to is decided in one place, which the move and these
displays both ask, so what is shown cannot differ from what happens.

### done-safe and the security paths

A flow may offer a way past a stage that only some beads need. Depot yardr's
`review` does: `done` sends a bead on to `review-security`, a second review
for security alone, and `done-safe` sends it straight to `approved`
([line.md](line.md#then-once-more-for-security-alone)); the starter pack's
default flow has the same two, except both end in `clear`: `done` goes
through `review-security` and `done-safe` past it, and the bead waits there
for the person's yes before it is landed from `approved`. Which one a
reviewer reports is its judgment,
by the list its stage file has. A depot can hold that judgment to its code
with a file in its repository, `.yardr/security_paths`:

```
# Paths whose change always has the security review.
internal/store/**
internal/source*/**
go.mod
```

One pattern a line; blank lines and lines starting with `#` are left out.
A pattern is a git pathspec read as a glob from the repository's root, as
git's `:(glob)` magic reads it: `*` stays within a directory, `**` crosses
directories, `internal/store/**` is everything under that directory and
`go.mod` that file alone. A line that starts with a colon is given to git as
it is, with the magic it carries (`:(exclude)docs/**`). Depot yardr's own file
is [`.yardr/security_paths`](../.yardr/security_paths).

When `done-safe` is reported (`yardr bead done <id> --outcome done-safe`, by a
session, a person or the web view) and the bead's diff touches a file a
pattern matches, the report is refused: the refusal names the files, the bead
stays where it is, and the message says to report `done` instead, which goes
to the second review. For a session the refusal is also a `move_refused`
event on the bead, as for any move an agent is refused.

- **The diff** is the one `yardr review` shows, the bead's branch from where it
  left its base (`git diff <base>...yard/<bead>`). The base is the depot's;
  for a child of a train it is the train's branch, so a child answers for its
  own changes and not for what the train's earlier children landed; for a bead
  that came from a linked yard, the commit that yard named. A rename counts as
  the deletion and the addition it is, so a file moved out of a listed path is
  a change to it.
- **Where the list is read.** From the tip of the depot's base branch, as a
  repository flow is ([Repo flows](#repo-flows)), and never from a bead's
  branch or worktree: a bead that empties the list on its branch is still held
  to the list as the base has it, and the edit applies once it has been
  reviewed and landed. A depot that lists `.yardr/**`, as depot yardr does,
  sends a change to the list itself through both reviews.
- **No list, no check.** A depot without the file takes `done-safe` as its flow
  says, and so does a dir depot, which has no branches to compare. The check is
  made for the outcome named `done-safe` alone, and only when the bead's stage
  has it: a stage without it gets the flow's own refusal, which names the
  outcomes it has.
- **What cannot be told holds the report too.** A list git refuses (a pathspec
  it does not know, more than 500 lines), a base or branch git cannot resolve,
  a branch it cannot compare, a git that is slower than 30 seconds: the report
  is refused with the reason, since a depot that lists paths wants the check
  made. `done` is always there to report instead.
- **A line against mistakes, not a wall.** The check is on the outcome, which
  is how a reviewer reports. `yardr bead advance <id> --to approved` is the
  manual move, for a person or the yardmaster, and is not held to the list;
  nothing here stops an agent that runs it, and the roles tell agents to
  report outcomes.

The starter pack ships no list: the paths are the depot's, and a pack writes
nothing into a repository
([packs/starter](../packs/starter/README.md#the-second-review-for-security-alone)).

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
shown to nobody (`yardr flow show <depot>` warns of it, below). A name that a pin or a
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
  A file of a store flow in the yard's home that is named after no stage of
  it (`reveiw.md`) is a warning there, since nobody would ever be shown it.
  So is such a file on the depot's base: one in a repository flow's
  directory named after no stage of that flow, or in a directory with no
  flow file beside it. A file on a bead's branch is not read. A file whose
  `## Ask` section is broken is a warning there too, beside the file.
- **`yardr depot sources <depot>`.** One line per non-terminal stage of the
  flows governing the depot: where its stage file comes from, or `none`, and
  the repository's stage file it shadows when a store flow governs
  ([configuration.md](configuration.md#where-a-depots-flows-stage-files-and-roles-come-from)).

What goes in a stage file and what in a role prompt is in
[groups.md](groups.md#role-file-or-stage-file). The packs yardr carries have a
stage file for every stage of their flows, which `yardr depot init` installs
in the yard's home
([configuration.md](configuration.md#seeding-a-depot-yardr-depot-init)), and depot
yardr has one for every stage of its flows
([`.yardr/flows/default/`](../.yardr/flows/default),
[`.yardr/flows/yardr.public/`](../.yardr/flows/yardr.public),
[`.yardr/flows/yardr.train/`](../.yardr/flows/yardr.train)).

### The ask template: `## Ask`

A stage file may end in an `## Ask` section: the stage's ask template. When a
bead enters the stage, the yard puts an ask on it built from the template, to
the stage's people, and the person's answer moves the bead. A stage file
without the section has no template, and its beads behave as they always did.
Four kinds answer four wants: `choice` offers outcomes the file names, `moves`
offers the stage's moves as the flow has them, `input` asks for words, `check`
asks whether something has been done.

```
## Ask
kind: choice
question: Where should this bead go next?
context:
- The builder asked for a decision; the question is in their note.
- The branch is ready to review either way.
recommend: 1
why: The question is small and the branch is green.
done: The bead moves to new with your words as the note.
backlog: The bead goes back to be reshaped by the yardmaster.
```

- `kind` is required: `choice`, `moves`, `input`, or `check`.
- `question` is required, one line; `context` is required, at least one `- `
  line.
- `choice` offers one option per line of the form `<outcome>: <consequence>`,
  at least one line. The option's label is the outcome's name; each outcome
  must resolve under the flow. `recommend` and `why` follow the ask's rules.
  An outcome named like a field takes `options:`, below.
- `moves` offers the moves of the stage and writes none of them: the section
  is `kind`, `question` and `context`, with no option line, `recommend`, `why`
  or `move`. The options are read from the flow when the ask is put, as
  `yardr review` offers a stage's moves: one for each outcome the flow names
  for the stage, else one for each stage it allows next; the card is put from
  flow and the bead's current `NextStages`, including a return to the stage
  whose question brought the bead when that adds a move. A stage with no
  outcome to name its way out, as `decide` is, is left by `yardr bead advance
  --to`, and picking its option advances the bead there with the checks that
  advance makes: a move the flow has dropped since is refused as it would be
  by hand, and the `advanced` event records no outcome. Each option says
  where it goes in the flow's words: "The bead moves to X.", "The bead goes
  back to X, with your words as the note." for a stage earlier in the flow,
  "The bead finishes as X and closes." for a terminal one, and for a limited
  outcome the current round and where the bead goes after the limit. The stage
  must have a way out in the flow, or the section is broken.
- `input` asks for words: no options and no recommendation. `move:` names the
  outcome the words fire, default `done`; `move: none` records the words and
  moves nothing, for a stage with no outcomes.
- `check` asks "has X been done?": `move:` names the outcome Done fires
  (default `done`). There is no explicit Not-yet option: Later, the existing
  action, is the not-yet.

A `moves` section, for a `decide` stage whose flow lists no outcomes for it,
writes the question and nothing of the moves:

```
## Ask
kind: moves
question: Where should this bead go next?
context:
- The bead waits for a person; what it needs is in its notes.
```

A line named like a field is that field, so an outcome called `question`,
`kind`, `context`, `recommend`, `why`, `move` or `options` cannot be offered as
a bare option line: `question: <consequence>` reads as the question a second
time and is refused, and every stage of the starter flows that has outcomes has
one named `question`. A line of `options:`, which takes no value, opens the
options: every line after it is an option, whatever its name, to the end of
the section.

```
## Ask
kind: choice
question: Where should this bead go next?
context:
- The reviewer left a question in their note.
recommend: 1
why: Landing it is the usual answer once the checks are green.
options:
done: The bead goes to approved, where it lands.
changes: The bead goes back to new with your words as the note.
question: The bead parks in decide for a longer look.
```

Write the fields first and `options:` last. After it there is no way back to
the fields: a `kind:`, `context:` or `recommend:` line there is an option, and
is refused as one when the flow has no outcome of that name, and a `- ` line
there is refused, since context lines go under `context:`. Option lines above
`options:` stay options, so a template that parses without it needs the line
only for the outcomes named like a field, and `recommend` counts the options
in the order they stand, above the line and below it. `options:` takes no
value, as `context:` takes none, so an outcome named `options` is offered
below the line like any other.

The question and context are literal text; nothing is interpolated. The card
the yard puts has a fixed bead header of its own before the template's text,
written when the card is put and frozen into it, so that a person answering
away from the bead (on paper, on the hosted screen, from a notification) knows
what the card is about. The question gains ` — <id>: <title>`. The context
starts with `Bead: <id> — <title> (branch yard/<id>)`; then, where the bead
carries a finish note, that note's first line as its writer wrote it (cut at
600 characters); then the template's lines, word for word. A finish note is a
plain note with a line of its own `test: <command> @ <commit>: pass, ...`, and
the one shown is the latest by the author of the bead's first, the builder's,
so that a reviewer's verdict after it does not take its place. A bead with no
finish note, as one made in the stage, gets the header without that line, and
a card put before keeps the text it has. The card offers the template's
options and no more: a stage's card has no `You decide`, which an ask a person
writes still gets, since on a card that moves the bead it recorded words and
moved nothing.

The section is stripped wherever the stage file is shown (the brief, prime, the
web view), as the `summary:` line already is. A section that is present must
parse fully; `yardr flow show` reports a broken one beside its file, as it
warns of stray files.

A `moves` card is for a bead nobody has asked about. Where an ask that
someone put is open or put aside on the bead when it arrives, no card is put:
that question is what the person is asked, and the bead is moved by hand once
it is answered. A card that stands is taken back, in the write that puts an
ask on the bead, authored `yard` with the number of the ask that is on the
bead now. A card taken back is not put back when the ask is answered. No other
kind of template gives way to an ask. Which stages get a card is the stage
file's to say, as for any template, so a people stage without the section
behaves as it did: `backlog` and the sidings, where beads wait by design, ask
nothing. Depot yardr's own `decide` stages, in its default and train flows,
and the design pack's carry one; the starter pack's does not.

Every entry into a manual stage with a template puts one ask per
user-member of the stage's group, authored `yard`, in the move's write: a
bead created in the stage, advanced or done into it, and reopened in it.
A member naming another yard gets one ask addressed across; a remote
member whose link is missing or carries no ask out gets none, and the put
logs one `ask_unreachable` event naming the bead, the stage and the
member. Crew members are skipped. When no member got an ask, the ask goes
to the operator. A local user's ask also follows the existing reach grants
to linked yards, and a later take-back settles those copies. The ask carries
the stage, the move per option and the entry event's seq, frozen at ask
time. Entering a stage that already has an open ask of that stage puts none.

Any move out takes the stage's open and put-aside asks back in the same
write, authored `yard` with the reason `moved to <to> by <actor>`;
answered and declined asks stay as history. A same-stage transition is a
fresh stay: the old stage ask is taken back and a fresh ask is put.
Closing takes back open stage asks with `closed` as the reason, and
reopening into a templated stage asks fresh. A broken template puts no
ask: the bead enters as today, and the yard logs one `bad_template`
event naming the stage and the error. Prime and review show the error
where the ask would stand; the bead still moves by hand.

An answer on a stage ask is its members' to give: a pick, words, Later or
Decline from anyone else is refused whole, with the membership reason. A
name the links trust as a member answers as them, and where the yard put
the ask to the operator as fallback, the operator answers it. A pick or
words that fire an outcome move the bead in the same write, through the
flow's existing outcomes; the `advanced` event records the outcome, and
the answer's words stay on the answer note, so the move carries no
separate note. A pick of a stage on a `moves` card advances the bead there in
the same write, as `yardr bead advance` does. Words with `move: none` record
without moving, and so does a pick of the `You decide` that a card put before
the yard dropped it from stage cards still holds. Later puts the ask aside and
Decline ends it; the bead waits as today in both cases, and the yard never
re-asks on its own.
Several members resolve as first answer wins: the winner's move takes the
other members' open asks back. A stage ask takes picks only for the stay
that put it: a re-pick after one that moved, a pick from another stage,
and a pick for an earlier stay are each refused, the last naming the
fresh ask where one stands, or saying the template went or broke and the
bead is moved by hand. An answer arriving from a linked yard moves the
bead the same way. A closed bead moves no more.

### The map of the flow

`yardr prime --bead <id>` draws the whole flow before the `STAGE` block, so
whoever works a bead sees how big the flow is, who works each stage, where
the bead stands and where it has been. The brief an agent is started with
has the same block, under `## Flow <name>` directly before its stage
section, so a bead sent back says so in the first text its new session
reads:

```
FLOW default · 10 stages (context: your stage's file below says what you must do)
  backlog           people   New beads wait here, where `yardr bead create` puts them, until the yardmaster or a person picks…
  shape             agents   A shaper reads the bead and the code it names, and shapes it for a builder.
  siding            people   Shaped small beads wait here for company, and leave folded into one bead.
> new               agents   A builder implements the bead here, on branch `yard/<bead>` in the bead's own worktree.   <- you are here
  decide            people   A bead here waits for a person: Christopher decides.
  ready             people   Folded carriers wait here for a person to send them to build.
  review            agents   The finished bead is reviewed against its goal; what the reviewer can fix is fixed, not sent back.
  review-security   agents   The reviewed bead is reviewed once more, for security alone, by another agent.
  approved          script   The assembly tests the bead and lands it on `main`, one bead at a time.
  merged            end
PATH  backlog -> shape -> new
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
  above, which `backlog -> shape | new | decide` names). A train, or any bead of a
  type with its own flow, gets the map of that flow.
- **Who.** From the route a bead of this depot and type (and these labels)
  would take in the stage, by the group's runner: `agents`, `script` for an
  exec group, `people` for a manual one, `end` for a terminal stage whatever
  is routed there, `nobody` for a stage without a route. Groups are not
  named, so a route with several
  ([groups.md](groups.md#a-route-with-further-groups-and-quota-conditions))
  reads as any other: they all start agents.
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
yardr flow draw starter --mini > mini.svg             # the line plan instead
```

`flow draw` reads files only and needs no yard. All of them draw with one
function, so the same flow with the same groups is the same file whichever
way it was asked for. A depot or pack with several flows draws its own (a
pack's first) unless `--flow` names another, and says on standard error which
others there are. With `--mini` they draw the line plan instead — one thin
track, each stage a section of it, the other edges as thin unlabelled
lines — the drawing the yard panel uses (docs/web.md, "The yard panel").

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

A wagon is a child of a train that lands on the train's branch with no
review of its own and tested for what it changed alone; the whole suite and
the review are the train's ([line.md](line.md#wagons)). The stage names are the
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

## A siding

A stage whose route reaches a siding, a manual group that sets `fold`,
`fold_after` or `fold_max` ([groups.md](groups.md#a-siding)), is where small
beads wait to be folded into one ([line.md](line.md#folding-beads)).
`yardr flow show <depot>` says so under the table, two lines for each such
stage of the flow:

```
STAGE    GROUP    MEMBERS         OPEN  FILE  NEXT
backlog  backlog  ada,yardmaster  6     yes   siding | new | decide
siding   siding   ada,yardmaster  3     yes   ready | backlog
ready    ready    ada,yardmaster  0     yes   new | backlog
...
siding: folds at 4 beads, or 8h after the latest arrives; at most 6 points; the carrier goes to ready
        now 3 waiting, 4 points, 1 stuck; folds at 4 or at 09:40
```

The first line is the group's numbers (4, 8h and 6 where it states none) and
the stage the carrier goes to, the first stage the flow leads on to from the
siding. The second is where the siding stands: the open beads in the stage
that this flow governs, what they weigh in points, how many of them a fold
would leave where they are, and when it folds, at the count or at the end of
`fold_after` from the latest bead to enter, whichever comes first. A bead is
**stuck** when it is held, claimed by a session that works it, another
yard's, a train or a train's child, blocked by a bead that has not released
it, in its stage or another, or has a question to a person open; a bead that
a manual group holds, as it holds the beads of its stage up to its `--limit`,
is not. The time is the clock alone for today and has its day otherwise, and
is left out when nothing waits.

A stage that no rule of the flow leaves, or a terminal one, has no stage for the
carrier to go to. It has a `warning:` in place of the second line:

```
siding: folds at 4 beads, or 8h after the latest arrives; at most 6 points
warning: siding has fold=4 but no next stage; nothing will fold here
```

A group that works two stages of a flow has a pair under each, counting that
stage's beads. With `--json` each such stage has a `siding` object: `fold`,
`fold_after`, `fold_max`, `to`, `waiting`, `points`, `stuck`, `folds_at` and
`warning`, of which `to`, `folds_at` and `warning` are absent when there is
nothing to say.

## Worked example: depot yardr

This is the depot flow yardr reports in `yardr flow show yardr` (as of
2026-10-03). The live `OPEN` counts are omitted because they change as beads
move:

```
flow default (repo@2c2a589)
STAGE            GROUP            MEMBERS            NEXT
backlog          backlog          mckean,yardmaster  shape | siding | new | decide
shape            shapers          -                  siding on small | new on done | decide on question | backlog
siding           siding           mckean,yardmaster  ready | backlog
new              yardr-builders   -                  review on done | decide on question | backlog
decide           decisions        mckean,yardmaster  backlog | new
ready            ready            mckean,yardmaster  new | backlog
review           yardr-reviewers  -                  review-security on done | approved on done-safe | new on changes | decide on question
review-security  yardr-security   -                  approved on done | new on changes | decide on question
approved         yardr-refinery   -                  merged on done | new on failed | decide on question
merged           -                -                  (terminal: closes the bead)
```

The depot's flow is the repository's
[`.yardr/flows/default.flow`](../.yardr/flows/default.flow), read from main;
no store pointer overrides it. It governs every bead that is not a train
(tasks, features, bugs):

```
# default.flow
backlog -> shape | siding | new | decide
shape -> siding on small | new on done | decide on question | backlog
siding -> ready | backlog
ready -> new | backlog
new -> review on done | decide on question | backlog
review -> review-security on done | approved on done-safe | new on changes max 3 then decide | decide on question
review-security -> approved on done | new on changes max 3 then decide | decide on question
approved -> merged on done | new on failed max 3 then decide | decide on question
decide -> backlog | new
merged terminal
```

The `flow show` snapshot above predates the limits: the file's three ways back
to `new` now carry `max 3 then decide`, as the [starter pack's](#a-limit-on-rounds)
do.

| stage | group | runner |
|---|---|---|
| backlog | `backlog` | manual (Christopher and the yardmaster, who picks which beads go to `shape`) |
| shape | `shapers` | herdr, Muse, limit 2, `prompt=roles/shaper.md` |
| siding | `siding` | manual (Christopher and the yardmaster; `fold=4`, `fold_after=8h`, `fold_max=6`: the yard folds the waiting beads into one carrier by itself) |
| ready | `ready` | manual (Christopher and the yardmaster, who sends each carrier to `new`) |
| new | `yardr-builders` | herdr, Claude Code, limit 5, `prompt=roles/builder.md` |
| review | `yardr-reviewers` | herdr, Codex, limit 3, `prompt=roles/reviewer.md` |
| review-security | `yardr-security` | herdr, `prompt=roles/review-security.md`: the second review, for security alone ([line.md](line.md#then-once-more-for-security-alone)) |
| approved | `yardr-refinery` | exec, limit 1, [`contrib/assembly/merge-local`](../contrib/assembly/merge-local) |
| decide | `decisions` | manual (Christopher and the yardmaster) |

`shape` is where a backlog bead is specified before a builder takes it: a
shaper reads the bead and the code it names, rewrites the body in the yard's
shape (goal, where to look, what to do, left out, done when) and estimates
it, changing no code. The move from `backlog` to `shape` stays a person's
or the yardmaster's pick, never a route. A bead shaped at `difficulty:1`
and `size:small` goes to `siding` on `small`, to wait for company and leave
folded with it; any other `difficulty:1` bead goes to `new` on `done`; a
heavier one, a duplicate, or one needing a decision only a person can make
goes to `decide` on `question`, with the question asked on the bead first.
Beads in `siding` leave folded into one carrier, which waits in `ready` for
a person to send it to `new`.

The assembly lands an approved bead and advances it to `merged`, which
closes it; a red run of the tests sends it back to `new`. The flow has no `merge`
stage, so a rebase that conflicts goes back to `new` as well: merge-local
asks the flow first and reports the `failed` outcome, raising no refused
move. A flow with a `merge` stage and a merger group resolves conflicts
without a builder ([line.md](line.md#5-merge-a-merger-resolves-conflicts)).

Before the rename the depot was `yard`, and its flows lived in the store as
the named flows `yard` and `yard.decision` (migrated from the depot and type
flows they were), the second a type flow for decision beads (`decide ->
decided`, `decided terminal`). Depot yardr has neither: a decision is a bead
in `decide`, closed with the answer as its reason.

The public line is a second flow of the repository, the default flow with the
bead's branch published as a pull request,
[`.yardr/flows/yardr.public.flow`](../.yardr/flows/yardr.public.flow): a `pr`
stage after `new`, a stage that posts each review after the reviewer that wrote
it (`review-post`, `review-security-post`), a `ciwait` stage before `approved`,
and every stage name the default flow has, so that taking the pin off puts a bead
back on the default line. A bead follows it when it is pinned
(`yardr bead update <id> --meta flow=yardr.public`), as a pin wins over a
pointer; see [line.md](line.md#the-public-line).

Trains follow `yardr.train`, which the repository carries as
[`.yardr/flows/yardr.train.flow`](../.yardr/flows/yardr.train.flow) (a named
repo flow, read from main; `yardr depot flow yardr --type train yardr.train`
points the type at it, which `flow show` prints as `flow yardr.train (store,
type train, from repo@2c2a589)`):

```
# yardr.train.flow
backlog -> open
open -> review on done | open on failed | merge on conflict | backlog
review -> review-security on done | open on changes | decide on question
review-security -> approved on done | open on changes | decide on question
approved -> in-pr on done | open on failed | merge on conflict | merged
in-pr -> land on done | open on changes | decide on question
land -> merged on done | open on failed
merge -> open on done | open on rework | decide on question
decide -> open | backlog
merged terminal
```

Depot yardr has the flow but, as of 2026-10-03, only the depot's stage routes:
a train in `backlog`, `review`, `review-security`, `approved` or `decide` goes to the same
group as any bead, and `open`, `merge`, `in-pr` and `land` have no route
(`flow show` warns for each: beads there wait for a human). The groups in
the table that the depot lacks are the ones
[line.md](line.md#setup-2) sets up.

| stage | group | runner |
|---|---|---|
| backlog | `backlog` | manual (the yardmaster adds children, then advances to open) |
| open | none yet; `yardr-integrators` (route `--type train`) | exec, limit 1, [`contrib/assembly/integrate-train`](../contrib/assembly/integrate-train) |
| review | `yardr-reviewers` | as for any bead; the brief says it is a train and gives the range |
| review-security | `yardr-security` | as for any bead; the second review reads the whole diff, every wagon, for security alone (there is no `done-safe` for a train) |
| approved | `yardr-refinery` | merge-local, as for any bead; on the PR path `yardr-pr` (route `--type train`): exec, limit 1, [`contrib/assembly/pr-train`](../contrib/assembly/pr-train) with `YARDR_PR=1` |
| in-pr | none yet; `decisions` (route `--type train`), PR path only | manual: the check "Is the PR merged?"; Done, or `yardr bead done <train>` by hand, means "merge it" (to `land`) |
| land | none yet; `yardr-pr-merge` (route `--type train`), PR path only | exec, limit 1, [`contrib/assembly/pr-merge`](../contrib/assembly/pr-merge) with `YARDR_PR=1` |
| merge | none yet; `mergers` | herdr, Claude Code, `prompt=roles/merger.md`, on branch `yard/<train>`; `done` goes back to `open`, where the integrator tests and pushes it again |
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
with a rebase (a squash where its group sets `YARDR_LAND=squash`) and reports
`done` to `merged`. Without the switch pr-train
and pr-merge hold the train with a note instead, so nothing reports `done`
in `approved` and `in-pr` is not reached. The details are
in [line.md](line.md#trains).

`in-pr` is the first stage with an ask template (above): a check, "Is the
PR merged?", whose Done reports `done`, moving the train to `land`. Later
is the not-yet, and reporting by hand takes the ask back.

How beads actually move through these stages is in [line.md](line.md).
