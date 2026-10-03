# Packs

A pack is one yard's way of working as a directory: the flows a bead passes
through, the groups that work their stages, the role prompts and scripts
those groups run, and what an always-on resident hears. A yard imports a
pack into a rig; the pack names no agent product, model, argument, rig,
person, secret or identity, which are the yard's and stay in it.

What is built: `yardr pack check`, which reads a pack and shows what
importing it would change. Importing itself is **planned** (*planned,
yardr-48xe*), as is fetching a pack from a git URL; until then a pack is a
directory on this machine.

The pack yardr ships is [`packs/starter`](../packs/starter): the flow `yardr
rig init` writes, the roles yardr embeds and the reference refinery, as one
pack.

```
packs/starter/
  pack.yaml         flows, groups, residents
  roles/*.md        role prompts
  scripts/*         what a script group runs
```

## pack.yaml

```yaml
name: starter

flows:
  default:
    stages:
      backlog:  { group: backlog,   manual: [new, decide] }
      new:      { group: builders,  outcomes: { done: review, question: decide }, manual: [backlog] }
      review:   { group: reviewers, outcomes: { done: approved, changes: new, question: decide } }
      approved: { group: refinery,  outcomes: { done: merged, failed: new, question: decide } }
      decide:   { group: decisions, manual: [backlog, new] }
      merged:   { end: true }

groups:
  builders:  { agent: roles/builder.md,  limit: 5 }
  reviewers: { agent: roles/reviewer.md, limit: 3 }
  refinery:  { run: scripts/merge-local, limit: 1 }
  backlog:   { people: true }
  decisions: { people: true }

residents:
  mayor:
    role: roles/mayor.md
    hears:
      events: [gave_up, session_blocked, unrouted, held, session_idle, session_stalled]
      stages: [decide]
      done: true
    subscribe:
      linear: involved
```

| key | meaning |
|---|---|
| `name` | the pack's name: letters, digits, `.`, `_`, `-`. It names the directory its files are copied to and goes in front of its flows' names |
| `flows.<flow>.stages` | the flow's stages, **in file order**: the first is where a bead starts |
| `flows.<flow>.type` | optional: the flow is for beads of this type, beside the rig's flow (an epic flow beside the task flow). A type follows one flow |
| `…stages.<stage>.group` | who works the stage: a key of `groups`. A stage without one is routed to nobody |
| `…stages.<stage>.outcomes` | outcome reported (`yardr bead done --outcome`) -> next stage, in file order |
| `…stages.<stage>.manual` | stages the bead may be moved to by hand (`yardr bead advance --to`); no outcome leads there |
| `…stages.<stage>.end` | `true`: entering the stage closes the bead. Such a stage has no group, outcomes or manual moves, and a flow has at least one |
| `groups.<group>` | exactly one of `agent: <role file>`, `run: <script>`, `people: true` |
| `groups.<group>.limit` | optional: how many beads the group works at once |
| `residents.<name>.role` | the resident's role file |
| `residents.<name>.hears.events` | event kinds from the yard itself |
| `residents.<name>.hears.stages` | a bead entering one of these stages, which a flow of the pack must have |
| `residents.<name>.hears.done` | `true`: a bead becoming done |
| `residents.<name>.subscribe.<source>` | an outside source (`github`, `linear`, `token`) and how much of it: `involved`, `mentions` or `all` |

A stage's outcomes and manual moves are the flow's transitions, outcomes
first ([flows.md](flows.md#outcomes-agents-say-how-the-work-ended)): `new`
above is `new -> review on done | decide on question | backlog`. The store's
rule for a stage without outcomes holds here too: with exactly one way out
(`manual: [new]`), `yardr bead done` takes it.

**Paths.** A role or script is always relative to the pack's directory, so
a pack works wherever it is cloned. An absolute path, a path leaving the
directory (`..`) and a file that is not a regular file there (a link) are
errors, and a script must be executable.

### What a pack is refused for

`yardr pack check` reads the whole file and names every mistake with its
file, line and key, then exits 1:

```
packs/mine/pack.yaml:6: flows.default.stages.review.on: YAML reads the bare key on as a boolean, not a name: rename it, or quote it ("on")
packs/mine/pack.yaml:7: flows.default.stages.decide.group: no group deciders is declared under groups
```

- an unknown key, a key given twice, a value of the wrong kind;
- a key or a name YAML reads as a boolean: bare `on`, `off`, `yes`, `no`,
  `y`, `n`, `true`, `false`. One YAML reader takes `on:` for a key called
  "on" and the next for `true`, so a pack may not depend on it (it is why
  the keys are `outcomes` and `hears`). Quoted, such a word is a name;
- a boolean that is not `true` or `false` (`end: yes`);
- a stage naming a group that is not declared, or an outcome or manual move
  to a stage the flow lacks;
- a flow without an `end` stage; an `end` stage with a group or a way out;
- a group that is not exactly one of agent, script, people;
- a role or script that is missing, absolute, outside the pack, or a script
  that is not executable;
- `hears.stages` naming a stage no flow of the pack has;
- a `subscribe` source this yardr has no adapter for, or an unknown scope.

## yardr pack check

```sh
yardr pack check <dir> --rig <rig> [--resident <pack's>=<yard's>]… [--flow <flow>] [--json]
```

It prints what an import into the rig would do, object by object, and
writes nothing, neither to the store nor under the home:

| it says | meaning |
|---|---|
| `create` | the yard has no object of that name |
| `change`, then `field: old -> new` per field | it has one that differs in what the pack carries |
| `unchanged` | it has one and nothing would change |
| `not in the pack` | the yard has it, the pack does not: an import leaves it alone (nothing is pruned) |

then what an import would ask, which flow would govern the rig afterwards,
what each subscribed source has and lacks, and notes. `--json` prints the
same plan as data ([below](#the-plan-as-data)). It exits 1 on an invalid
pack and on a plan an import would refuse.

## How a pack maps to the yard

Everything is matched by name, so importing twice changes nothing the
second time.

**Files.** The roles and scripts the pack's groups and residents name are
copied to `<home>/packs/<pack>/…` (`~/.yardr/packs/starter/roles/builder.md`)
and referred to by that absolute path. A group's prompt is then no file a
bead's branch can change, as with the home's own roles
([configuration.md](configuration.md#role-prompts)), and a rig's
`.yardr/roles/` is not consulted for it. Files of the pack that nothing in
`pack.yaml` names are not copied.

**Groups.** An agent or script group is the rig's, named `<rig>-<group>`
(`yardr-builders`); a people group keeps its name and is the yard's, shared
by every rig that imports a pack naming it.

| pack | group in the yard | the pack sets | the yard keeps |
|---|---|---|---|
| `agent: <role>` | runner `herdr` | `prompt` (the copied role), `limit` | `kind`, `model`, `args`, `env_file`, `idle` and every other setting |
| `run: <script>` | runner `exec` | the command in `cmd` (the copied script), `limit` | the assignments before the command and the arguments after it (`YARDR_PUSH=origin … merge-local`), `log`, `dir` and the rest |
| `people: true` | runner `manual` | `limit`, when the pack gives one | `members`, subscriptions |

A group the yard lacks needs what a pack cannot say: the agent kind and
model, or the members. These are the import's questions.

**Flows.** A flow is stored as the named flow `<pack>.<flow>`
(`starter.default`), and the rig is pointed at it (`yardr rig flow`); a
flow with `type:` becomes that type's pointer. Of several flows without a
type the rig follows one: `--flow`, else the first; the others are not
imported.

A pointer in the store wins over the flow files in the rig's repository
([flows.md](flows.md#which-flow-governs-a-bead)), and it wins as a whole: a
rig whose base branch carries `.yardr/flows/default.flow` (what `yardr rig
init` writes) is governed by the pack's flow once the import has pointed it
there, and its `type/<type>.flow` files stop governing too. The files stay
and govern again when the pointer is cleared (`yardr rig flow <rig>
--clear`). The plan says which flow would govern the rig's beads and each
type's afterwards, and which does now. A type pointer the pack has no flow
for is left alone.

The store refuses a pointer or a flow that would leave an open bead in a
stage its new flow lacks ([flows.md](flows.md#named-flows-and-pointers)).
The plan makes the same check for every flow of the pack, over the beads
that would follow it by a pointer or a pin, and marks the import
**refused**, naming the beads: move or pin them first.

**Routes.** Each stage with a group gets the one route `rig, stage ->
group` (with the type, for a type's flow). A route the rig has for the
stage is that route: unchanged when it leads to the same group, else
replaced. `yardr route add` mints a new id per call, so a replacement is
an add and a delete of every route the rig had for the stage; two routes
for one rig and stage never remain. A stage with no group clears the
matching rig, stage and type routes, so its beads wait for a hand. The
route's priority is kept when a stage has a group. Routes with a label, and
routes for stages the pack lacks, are left alone.

**Residents.** A resident of the pack is taken by a resident of the yard:
`--resident mayor=yardmaster`. Without the flag it is the yard's resident
of the same name, else a question. The pack sets three things and nothing
else (identity, `env_file`, args, model, directory and rotation are the
yard's):

| pack | resident |
|---|---|
| `role` | its `prompt`, the copied file |
| `hears.events` | its `--on` kinds: the list is replaced |
| `hears.stages` | one subscription per stage: `yardr all rig=<rig> stage=<stage>` |
| `hears.done` | `yardr all rig=<rig> state=done` |
| `subscribe.<source>: <scope>` | `<source> <scope>`; a subscription to the source without filters has its scope changed |

Its other subscriptions are left alone
([subscriptions.md](subscriptions.md)).

**Sources.** For each source a resident subscribes to, the plan says what
the yard has and lacks to hear it and answer in it:

| need | what is looked at |
|---|---|
| `hook` | a hook with that source (`yardr hook add`) |
| `secret file` | the hook's secret file exists |
| `identity` | the resident's identity in the source (`yardr resident identity`) |
| `credential` | the name of the reply credential (`LINEAR_API_KEY`, `GITHUB_TOKEN`) is set in the resident's `env_file`; only names are read |
| `listener` | `YARDR_HOOK_ADDR`. Only the server knows whether it listens, so this is `ok` when the variable is set where the command runs and `unknown` otherwise |

## The plan as data

`--json` prints the plan an import will execute:

```json
{
  "pack": "starter", "dir": "/…/packs/starter", "rig": "yardr",
  "install": "/Users/me/.yardr/packs/starter",
  "objects": [
    {"kind": "group", "name": "yardr-builders", "action": "change",
     "changes": [{"field": "prompt", "old": "roles/builder.md", "new": "/Users/me/.yardr/packs/starter/roles/builder.md"}],
     "group": {"name": "yardr-builders", "runner": "herdr", "config": {"kind": "claude", "prompt": "/Users/me/…"}, "limit": 5}}
  ],
  "questions": [{"id": "group.web-builders.kind", "object": "group web-builders", "field": "kind", "ask": "…", "default": "claude"}],
  "governs": [{"flow": "starter.default", "source": "store", "now": "default (repo@77a3a8f)", "in_pack": true}],
  "sources": [{"source": "linear", "resident": "yardmaster", "needs": [{"what": "hook", "status": "ok", "detail": "…"}]}],
  "notes": [], "refused": []
}
```

Objects come in the order an import does them: files, groups, flows,
pointers, routes, residents, subscriptions. `action` is `create`, `change`,
`unchanged` or `not_in_pack`. Each object carries what the import would
write, under its kind:

| kind | carries |
|---|---|
| `file` | `from`, `to`, `mode`, `sha256` |
| `group` | the whole group as it would be stored, what the yard keeps included |
| `flow` | the whole flow, its stages in order |
| `pointer` | `rig`, `type`, `flow` |
| `route` | `stage`, `rig`, `type`, `group`, `priority`, and `delete`: the ids of the routes it replaces |
| `resident` | `pack` (its name in the pack), `resident` (in the yard), `prompt`, `on` |
| `subscription` | `resident`, `add`, and `remove` when a scope changes |

An object's `asks` are the ids of the questions whose answers it still
needs. A plan with anything under `refused` must not be applied.
