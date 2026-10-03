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

yardr rig flow yard code                      # rig yard's beads follow code
yardr rig flow webapp code                    # and webapp's
yardr rig flow yard --type decision decide    # yard's decisions follow decide
yardr rig flow yard --type decision --clear   # back to the rig's flow
yardr rig list                                # shows each rig's flow and its source

yardr flow show yard                          # a rig: every flow governing it, per stage
```

Changing a named flow with `flow set` changes it for every rig and type
pointing at it, and every bead pinning it, at once. `flow rm` names what still uses the flow (`rig
yard`, `rig yard type decision`, `bead yard-x` for an open bead pinning it)
and removes nothing until they point elsewhere.

`rig flow`, setting or clearing a pointer, checks every open bead the
change would hand to another flow: the rig's beads, or the type's, that
are not pinned and, for the rig's pointer, whose type has no pointer of its
own. A bead sitting in a stage the new flow lacks is named and the change
refused, so a configuration command never moves a bead or leaves one in a
stage nobody is routed to:

```
flow code lacks the stage of open beads in rig yard: yard-x (triage),
yard-y (triage); advance them to a stage the flow has, or pin them to the
flow they follow now (yardr bead update <id> --meta flow=<name>), then
point the rig again
```

Clearing a pointer checks against whatever would govern next (the rig's
pointer, the repository's `type/<type>.flow` or `default.flow`), and
names that flow per bead (`yard-x (triage, not in flow default)`). A rig
whose flows would come from a checkout git cannot read is refused with
git's error.

`flow set`, replacing a named flow, makes the same check for every open
bead that follows the name: those a rig's or type's pointer at it reaches
(as above), and those pinning it. A bead in a stage the new version lacks
is named with what leads it to the flow, and the stored flow is kept:

```
the new flow code lacks the stage of open beads in rig yard: yard-x
(triage); in rig webapp type bug: webapp-y (triage); pinning it: yard-z
(triage); advance them to a stage it has, or pin them to another flow
(yardr bead update <id> --meta flow=<name>), then set the flow again
```

A flow in a rig's repository has no such check, since no command changes
it: when the base branch moves to a version of the file without a stage,
an open bead in that stage stays there, and rejoins the flow at its first
stage (see below).

### Stage order

A flow's stages are ordered as its rules first name them, and the order
matters: the **first stage** is where a bead in a stage the flow does not
know may rejoin it, and `yardr flow show` lists stages in it. Such a bead
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
  flow yard.decision (store, type decision)
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

Type `epic` has its own flow in rig yard, `yardr.epic` (below), for
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

## Worked example: rig yard

These are the flows yardr itself is built with (`yardr flow show yard`, as of
2026-10-02). They live in the store as the named flows `yard` and
`yard.decision` (migrated from the rig and type flows they were), with rig
yard and its type `decision` pointing at them.

The rig's flow, for code beads (tasks, features, bugs):

```
# yard.flow
backlog -> new | decide
new -> review | backlog | decide
decide -> backlog | new
review -> approved | new | decide
approved -> merged | merge | new
merge -> review | new | decide
merged terminal
```

| stage | group | runner |
|---|---|---|
| backlog | `backlog` | manual (humans; the mayor shapes beads here) |
| new | `yard-builders` | herdr, Claude Code, `prompt=roles/builder.md` |
| review | `yard-codex-review` | herdr, Codex, `prompt=roles/reviewer.md` |
| approved | `yard-refinery` | exec, limit 1, [`contrib/refinery/merge-local`](../contrib/refinery/merge-local) |
| merge | `mergers` | herdr, Claude Code (Sonnet), limit 1, `prompt=roles/merger.md` |
| decide | `decisions` | manual (Christopher) |

The refinery lands an approved bead and advances it to `merged`, which
closes it; a red gate sends it back to `new`, a conflict to `merge`. The
merger sends a resolved rebase to `review`, where the reviewer checks only
its conflict resolution before returning it to `approved`
([pipeline.md](pipeline.md#5-merge-a-merger-resolves-conflicts)).

Decision beads follow their own type flow: a human decides, and entering
`decided` closes the bead.

```
# yard.decision.flow
decide -> decided
decided terminal
```

Epics follow `yardr.epic`, which the repository carries as
[`.yardr/flows/yardr.epic.flow`](../.yardr/flows/yardr.epic.flow) (a named
repo flow, read from main; `yardr rig flow yardr --type epic yardr.epic`
points the type at it):

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

| stage | group | runner |
|---|---|---|
| backlog | `backlog` | manual (the mayor adds children, then advances to open) |
| open | `yard-integrators` (route `--type epic`, priority 10) | exec, limit 1, [`contrib/refinery/integrate-epic`](../contrib/refinery/integrate-epic) |
| review | `yard-codex-review` | as for any bead; the brief says it is an epic and gives the range |
| approved | `yard-pr` (route `--type epic`, priority 10) | exec, limit 1, [`contrib/refinery/pr-epic`](../contrib/refinery/pr-epic) with `YARDR_PR=1`; by default, `yard-refinery` and merge-local as for any bead |
| in-pr | `decisions` (route `--type epic`), PR path only | manual: `yardr bead done <epic>` means "merge it" |
| land | `yard-pr-merge` (route `--type epic`), PR path only | exec, limit 1, [`contrib/refinery/pr-merge`](../contrib/refinery/pr-merge) with `YARDR_PR=1` |
| merge | `mergers` | as for any bead, on branch `yard/<epic>`; `done` goes back to `open`, where the integrator gates and pushes it again |
| decide | `decisions` | manual |

An epic waits in `open` while any child is open: each child blocks it
(`dep add -k parent` adds that edge too), and routes only send ready beads.
Where `new` is a bead's way back to its builders, `open` is an epic's: the
fix is a new child, which blocks the epic until it lands.

An epic lands one of two ways from `approved`. Locally, the default (no
`YARDR_PR`, `approved` routed to `yard-refinery`): merge-local
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
