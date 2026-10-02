# Flows: a rig's lifecycle

Routes say who works a stage; a **flow** says where a bead may go next. A rig
without a flow lets a bead move to any stage, exactly as before. A flow file
has one rule per line:

```
# flows/yard.flow
backlog -> new
new -> review | backlog
review -> approved | new
approved -> merged | merge | new
merge -> approved
decide -> backlog
merged terminal
```

```sh
yardr flow set yard -f flows/yard.flow                  # the rig's flow (replaces)
yardr flow set yard --type decision -f flows/decision.flow  # wins for that type
yardr flow show yard                                    # stage, group, open beads, next
yardr flow rm yard [--type decision]
```

Flows are kept in the store today, one per rig plus one per bead type in the
rig. Where flows will also come from, and which source wins, is in
[configuration.md](configuration.md).

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

A type-specific flow governs that type's beads in the rig; every other type
follows the rig's flow. Beads without a rig, journals and memories are never
governed. `yardr flow show` warns about non-terminal stages no route sends
anywhere: beads there wait for a human.

## Terminal stages

A line `<stage> terminal` marks a stage that ends the bead: advancing into it
closes the bead with the stage name as its reason, in one write. A terminal
stage has no outgoing rules and needs no route. `yardr bead close` still
works without one, for beads that end outside the flow (the interim
refinery closes beads this way, see [pipeline.md](pipeline.md)).

## Worked example: rig yard

These are the flows yardr itself is built with (`yardr flow show yard`, as of
2026-10-02).

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
`merged` terminal stage arrives with the real refinery (yard-23).

Decision beads follow their own type flow: a human decides, and entering
`decided` closes the bead.

```
# decision.flow
decide -> decided
decided terminal
```

How beads actually move through these stages is in [pipeline.md](pipeline.md).

## Planned

- **Named flows** (*planned, yard-9cza*). Flows stored once under a name
  (`yardr flow set <name> -f file`, `flow list`, `flow rm` refused while
  something points at it), and rigs and types point at them
  (`yardr rig flow <rig> [--type T] <flow>`), so several rigs share one flow
  instead of copying it. Today's rig flows become named flows `<rig>` and
  `<rig>.<type>` with pointers, so behaviour does not change.
- **Repo flows** (*planned, yard-9cza*). Flow files in the rig's repository
  under `.yardr/`, read from the rig's base branch, never the bead's branch.
  They are the rig's default; the store and a pin on the bead override them.
  See [configuration.md](configuration.md).
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
