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
merge -> approved
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
pointing at it, at once. `flow rm` names what still uses the flow (`rig
yard`, `rig yard type decision`, `bead yard-x` for an open bead pinning it)
and removes nothing until they point elsewhere.

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
the files when its commit moves (it caches them by commit). A rig whose path
is not a checkout, or whose base does not resolve, has no repo flows.

A file that does not parse is shown by `yardr flow list` with its error;
a bead it would govern cannot be created or moved until the base is fixed,
rather than silently running without its flow.

## Which flow governs a bead

```
pin on the bead  >  store pointer  >  repo .yardr/flows  >  none
```

- **Pin**: the bead names its flow in its metadata
  (`yardr bead update <id> --meta flow=<name>`, or `bead create --meta`).
  The pin governs even a bead without a rig. A pin naming no flow refuses
  the bead's moves.
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
- `yardr bead create -s <stage>` must name a non-terminal stage of the flow
  (the default `new` too).
- A bead in a stage the flow has no way out of (a stage the flow does not
  know, or a terminal stage after `yardr bead reopen`) may move to any stage
  of the flow, so it is never stuck.
- `yardr prime --bead` prints the allowed next stages; the store API exposes
  them as `NextStages`. Agents' briefs list them with the exact
  `yardr bead advance` command for each.

## Type flows

A type's flow governs that type's beads in the rig; every other type
follows the rig's flow. `yardr flow show <rig>` lists the rig's flow first,
then one per type, and warns about non-terminal stages no route sends
anywhere: beads there wait for a human.

## Terminal stages

A line `<stage> terminal` marks a stage that ends the bead: advancing into it
closes the bead with the stage name as its reason, in one write. A terminal
stage has no outgoing rules and needs no route. `yardr bead close` still
works without one, for beads that end outside the flow (the interim
refinery closes beads this way, see [pipeline.md](pipeline.md)).

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
approved -> new
```

| stage | group | runner |
|---|---|---|
| backlog | `backlog` | manual (humans; the mayor shapes beads here) |
| new | `yard-builders` | herdr, Claude Code, `prompt=roles/builder.md` |
| review | `yard-codex-review` | herdr, Codex, reviewer prompt in `~/.yardr/roles/` |
| approved | `yard-refinery` | exec, limit 1, the interim merge script |
| decide | `decisions` | manual (Christopher) |

`approved` has only `new` as a way on: the interim refinery either sends a
bead back to the builders or closes it with `bead close` once merged. A
`merged` terminal stage arrives with the real refinery (yard-23). Once the
mergers are wired ([pipeline.md](pipeline.md#5-merge-a-merger-resolves-conflicts)),
a conflict goes to `merge` instead: `approved -> new | merge` and
`merge -> approved | new | decide`.

Decision beads follow their own type flow: a human decides, and entering
`decided` closes the bead.

```
# yard.decision.flow
decide -> decided
decided terminal
```

How beads actually move through these stages is in [pipeline.md](pipeline.md).

## Planned

- **Outcomes and `yardr bead done`** (*planned, yard-qexg*). Agents report
  how their work ended and the flow picks the stage, instead of agents naming
  stages. An edge may carry an outcome:

  ```
  new      -> review on done | decide on question
  review   -> approved on done | new on changes | decide on question
  approved -> new on failed
  ```

  `yardr bead done <id> [--outcome <o>]` resolves the edge for the bead's
  stage; a stage with one outgoing edge gets `done` implicitly; an edge
  without `on` is reachable only by an explicit `advance --to` (humans, the
  mayor). Briefs and `yardr review` offer outcomes instead of stage names.
