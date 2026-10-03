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

Flows are stored once under a name, and rigs point at them, so several rigs
share one flow instead of copying it:

```sh
yardr flow set code -f flows/code.flow        # store (replace) the named flow
yardr flow list                               # store and repo flows, and what each governs
yardr flow show code                          # one named flow and who points at it
yardr flow rm code                            # refused while a rig, type or open bead uses it

yardr rig flow yardr code                     # rig yardr's beads follow code
yardr rig flow webapp code                    # and webapp's
yardr rig flow yardr --type decision decide   # yardr's decisions follow decide
yardr rig flow yardr --type decision --clear  # back to the rig's flow
yardr rig list                                # shows each rig's flow and its source

yardr flow show yardr                         # a rig: every flow governing it, per stage
```

Changing a named flow with `flow set` changes it for every rig and type
pointing at it, and every bead pinning it, at once. `flow rm` names what still uses the flow (`rig
yardr`, `rig yardr type decision`, `bead yardr-x` for an open bead pinning it)
and removes nothing until they point elsewhere.

`rig flow`, setting or clearing a pointer, checks every open bead the
change would hand to another flow: the rig's beads, or the type's, that
are not pinned and, for the rig's pointer, whose type has no pointer of its
own. A bead sitting in a stage the new flow lacks is named and the change
refused, so a configuration command never moves a bead or leaves one in a
stage nobody is routed to:

```
flow code lacks the stage of open beads in rig yardr: yardr-x (triage),
yardr-y (triage); advance them to a stage the flow has, or pin them to the
flow they follow now (yardr bead update <id> --meta flow=<name>), then
point the rig again
```

Clearing a pointer checks against whatever would govern next (the rig's
pointer, the repository's `type/<type>.flow` or `default.flow`), and
names that flow per bead (`yardr-x (triage, not in flow default)`). A rig
whose flows would come from a checkout git cannot read is refused with
git's error.

`flow set`, replacing a named flow, makes the same check for every open
bead that follows the name: those a rig's or type's pointer at it reaches
(as above), and those pinning it. A bead in a stage the new version lacks
is named with what leads it to the flow, and the stored flow is kept:

```
the new flow code lacks the stage of open beads in rig yardr: yardr-x
(triage); in rig webapp type bug: webapp-y (triage); pinning it: yardr-z
(triage); advance them to a stage it has, or pin them to another flow
(yardr bead update <id> --meta flow=<name>), then set the flow again
```

A flow in a rig's repository has no such check, since no command changes
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
from before the rig had a flow: `flow set` and `rig flow` refuse a change
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
flows were migrated when the server first opened them: each rig's flow
became the named flow `<rig>`, each type flow `<rig>.<type>`, with the rig
and type pointing at them, so no bead changed lifecycle.

## Repo flows

A rig's repository can carry its flows under `.yardr/flows/`:

```
.yardr/flows/default.flow          the rig's flow
.yardr/flows/type/<type>.flow      the flow for the rig's beads of <type>
.yardr/flows/<name>.flow           a named flow, for pins and pointers
```

yardr reads them from the tip of the rig's **base branch**
(`git cat-file blob <base>:.yardr/flows/...`), never from a bead's branch
or the checkout's working tree: a builder who edits a flow on `yard/<bead>`
changes nothing for that bead. The edit applies once it has been reviewed
and landed on the base. yardr resolves the base on every check and re-reads
the files when its commit moves (it caches them by commit). A rig without a
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

- `yardr rig check <rig>` names each stranded bead of the rig, with its
  stage, the flow and the commit the flow was read at, and fails:

  ```
  stranded: bead yardr-x is in stage triage, which flow default (repo@9bf658a) lacks; advance it to a stage the flow has (yardr bead advance yardr-x --to <stage>)
  rig yardr: FAIL (open beads in a stage their flow lacks: 1)
  ```

  With a red gate it exits 1 as before. With a green gate it exits 2, not
  1: no branch can fix a stranded bead, so the refinery holds the bead it
  was landing instead of sending it back to its builder. `--json` lists
  them under `stranded`.
- `yardr flow show <rig>` prints a warning per bead under the flow that
  lacks its stage, next to "stage has no route".
- The dispatcher logs one `stranded` event for the bead (`stage`, `flow`,
  `source` with the commit), which a resident hears like `unrouted` (it is
  in the default `--on` list and the starter pack's `hears.events`). It is
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
  The pin governs even a bead without a rig. `create` and `update` refuse a
  pin naming no flow (`the bead pins flow ghost: not found`); a pin that
  stops resolving later, say a repo flow removed from the base, refuses the
  bead's moves (see above).
- **Store pointer**: the type's pointer, else the rig's (`yardr rig flow`).
- **Repo**: `type/<type>.flow`, else `default.flow`, on the base branch.
- **None**: any stage is allowed.

The store wins as a whole: a rig pointer in the store overrides the repo's
type files too. A name, pinned or pointed at, is looked up among the store's
named flows, then in the rig's repository (`.yardr/flows/<name>.flow`).
Journals and memories are never governed.

`yardr bead show` and `yardr prime --bead` say which flow governs a bead
and where it came from:

```
  flow code (store)                       a store pointer for the rig
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
  example and in the starter flow, where the mayor shapes it before advancing
  it to `new`); a bead no flow governs starts in `new`.
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
  `yardr bead advance --to`, for humans and the mayor. `advance --to` still
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

## Type flows

A type's flow governs that type's beads in the rig; every other type
follows the rig's flow. `yardr flow show <rig>` lists the rig's flow first,
then one per type, and warns about non-terminal stages no route sends
anywhere: beads there wait for a human.

Type `epic` has its own flow in rig yardr, `yardr.epic` (below), for
integrating an epic's branch and landing it on main. Its children follow the
rig's flow like any bead; what an epic changes for them is only their base,
see [pipeline.md](pipeline.md#epics).

## Terminal stages

A line `<stage> terminal` marks a stage that ends the bead: advancing into it
closes the bead with the stage name as its reason, in one write. A terminal
stage has no outgoing rules and needs no route. `yardr bead close` still
works without one, for beads that end outside the flow (the reference
refinery falls back to it when the flow has no `merged`, see
[pipeline.md](pipeline.md#4-approved-the-refinery-lands-it-on-main)).

## Worked example: rig yardr

This is the rig flow yardr reports in `yardr flow show yardr` (as of
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

The rig's flow is the repository's
[`.yardr/flows/default.flow`](../.yardr/flows/default.flow), read from main;
no store pointer overrides it. It governs every bead that is not an epic
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
| backlog | `backlog` | manual (Christopher and the mayor, `yardmaster`, who shapes beads here) |
| new | `yardr-builders` | herdr, Claude Code, limit 5, `prompt=roles/builder.md` |
| review | `yardr-reviewers` | herdr, Codex, limit 3, `prompt=roles/reviewer.md` |
| approved | `yardr-refinery` | exec, limit 1, [`contrib/refinery/merge-local`](../contrib/refinery/merge-local) |
| decide | `decisions` | manual (Christopher and the mayor) |

The refinery lands an approved bead and advances it to `merged`, which
closes it; a red gate sends it back to `new`. The flow has no `merge`
stage, so a rebase that conflicts goes back to `new` as well: merge-local
falls back to the `failed` outcome when the flow refuses `merge`. A flow
with a `merge` stage and a merger group resolves conflicts without a
builder ([pipeline.md](pipeline.md#5-merge-a-merger-resolves-conflicts)).

Before the rename the rig was `yard`, and its flows lived in the store as
the named flows `yard` and `yard.decision` (migrated from the rig and type
flows they were), the second a type flow for decision beads (`decide ->
decided`, `decided terminal`). Rig yardr has neither: a decision is a bead
in `decide`, closed with the answer as its reason.

Epics follow `yardr.epic`, which the repository carries as
[`.yardr/flows/yardr.epic.flow`](../.yardr/flows/yardr.epic.flow) (a named
repo flow, read from main; `yardr rig flow yardr --type epic yardr.epic`
points the type at it, which `flow show` prints as `flow yardr.epic (store,
type epic, from repo@2c2a589)`):

```
# yardr.epic.flow
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

Rig yardr has the flow but, as of 2026-10-03, only the rig's stage routes:
an epic in `backlog`, `review`, `approved` or `decide` goes to the same
group as any bead, and `open`, `merge`, `in-pr` and `land` have no route
(`flow show` warns for each: beads there wait for a human). The groups in
the table that the rig lacks are the ones
[pipeline.md](pipeline.md#setup-2) sets up.

| stage | group | runner |
|---|---|---|
| backlog | `backlog` | manual (the mayor adds children, then advances to open) |
| open | none yet; `yardr-integrators` (route `--type epic`, priority 10) | exec, limit 1, [`contrib/refinery/integrate-epic`](../contrib/refinery/integrate-epic) |
| review | `yardr-reviewers` | as for any bead; the brief says it is an epic and gives the range |
| approved | `yardr-refinery` | merge-local, as for any bead; on the PR path `yardr-pr` (route `--type epic`, priority 10): exec, limit 1, [`contrib/refinery/pr-epic`](../contrib/refinery/pr-epic) with `YARDR_PR=1` |
| in-pr | none yet; `decisions` (route `--type epic`), PR path only | manual: `yardr bead done <epic>` means "merge it" |
| land | none yet; `yardr-pr-merge` (route `--type epic`), PR path only | exec, limit 1, [`contrib/refinery/pr-merge`](../contrib/refinery/pr-merge) with `YARDR_PR=1` |
| merge | none yet; `mergers` | herdr, Claude Code, `prompt=roles/merger.md`, on branch `yard/<epic>`; `done` goes back to `open`, where the integrator gates and pushes it again |
| decide | `decisions` | manual |

An epic waits in `open` while any child is open: each child blocks it
(`dep add -k parent` adds that edge too), and routes only send ready beads.
Where `new` is a bead's way back to its builders, `open` is an epic's: the
fix is a new child, which blocks the epic until it lands.

An epic lands one of two ways from `approved`. Locally, the default (no
`YARDR_PR`, `approved` routed to `yardr-refinery`): merge-local
fast-forwards main and advances the epic straight to `merged` by the bare
edge, which no outcome offers, so an agent reporting `done` in `approved`
always means the PR path. Through a pull request, experimental and off
unless `YARDR_PR=1` is in the cmd of integrate-epic, pr-epic and pr-merge:
pr-epic pushes the reviewed branch and updates the PR, reporting `done` to
`in-pr`; a human there reports `done` to `land`, and pr-merge merges the PR
with a rebase and reports `done` to `merged`. Without the switch pr-epic
and pr-merge hold the epic with a note instead, so nothing reports `done`
in `approved` and `in-pr` is not reached. The details are
in [pipeline.md](pipeline.md#epics).

How beads actually move through these stages is in [pipeline.md](pipeline.md).
