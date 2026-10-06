# Packs

A pack is one yard's way of working as a directory: the flows a bead passes
through and what each of their stages is for, the groups that work the
stages, the role prompts and scripts those groups run, and what an always-on
crew member hears. A yard imports a
pack into a depot; the pack names no agent product, model, argument, depot,
person, secret or identity, which are the yard's and stay in it.

`yardr pack check` reads a pack and shows what importing it would change;
`yardr pack apply` imports it; `yardr pack list` shows what the yard
imported. Fetching a pack from a git URL is **planned**; until then a pack
is a directory on this machine.

The pack yardr ships is [`packs/starter`](../packs/starter): the flow `yardr
depot init` writes with its stage files, the roles yardr embeds and the
reference refinery, as one pack.

```
packs/starter/
  pack.yaml         flows, groups, crew members
  roles/*.md        role prompts
  scripts/*         what a script group runs
  flows/<flow>/*.md stage files
```

## pack.yaml

```yaml
name: starter

flows:
  default:
    stages:
      backlog:  { group: backlog,   file: flows/default/backlog.md,  manual: [new, decide] }
      new:      { group: builders,  file: flows/default/new.md,      outcomes: { done: review, question: decide }, manual: [backlog] }
      review:   { group: reviewers, file: flows/default/review.md,   outcomes: { done: approved, changes: new, question: decide } }
      approved: { group: refinery,  file: flows/default/approved.md, outcomes: { done: merged, failed: new, question: decide } }
      decide:   { group: decisions, file: flows/default/decide.md,   manual: [backlog, new] }
      merged:   { end: true }

groups:
  builders:  { agent: roles/builder.md,  limit: 5 }
  reviewers: { agent: roles/reviewer.md, limit: 3 }
  refinery:  { run: scripts/merge-local, limit: 1 }
  backlog:   { people: true }
  decisions: { people: true }

crew:
  yardmaster:
    role: roles/yardmaster.md
    hears:
      events: [gave_up, session_blocked, unrouted, stranded, no_runner, workspace_kept, crew_gave_up, runtime_down, session_idle, session_aged, session_stalled, session_harness_error, session_model_mismatch, session_prompt_gave_up, move_refused, held]
      stages: [decide]
      done: true
    subscribe:
      linear: involved
    open_threads: [linear]

sources:
  linear:
    as: yardmaster
    hears:
      task: { new: started, review: review, merged: done }
```

| key | meaning |
|---|---|
| `name` | the pack's name: letters, digits, `.`, `_`, `-`. It names the directory its files are copied to and goes in front of its flows' names |
| `flows.<flow>.stages` | the flow's stages, **in file order**: the first is where a bead starts |
| `flows.<flow>.type` | optional: the flow is for beads of this type, beside the depot's flow (a train flow beside the task flow). A type follows one flow |
| `…stages.<stage>.group` | who works the stage: a key of `groups`. A stage without one is routed to nobody |
| `…stages.<stage>.file` | optional: the stage's stage file in the pack, markdown saying what the stage is for, what must be true to leave it and when each outcome applies ([flows.md](flows.md)). Whoever works a bead in the stage is shown it. A stage without the key has none |
| `…stages.<stage>.outcomes` | outcome reported (`yardr bead done --outcome`) -> next stage, in file order |
| `…stages.<stage>.manual` | stages the bead may be moved to by hand (`yardr bead advance --to`); no outcome leads there |
| `…stages.<stage>.end` | `true`: entering the stage closes the bead. Such a stage has no group, stage file, outcomes or manual moves, and a flow has at least one |
| `groups.<group>` | exactly one of `agent: <role file>`, `run: <script>`, `people: true` |
| `groups.<group>.limit` | optional: how many beads the group works at once |
| `crew members.<name>.role` | the crew member's role file |
| `crew members.<name>.hears.events` | event kinds from the yard itself |
| `crew members.<name>.hears.stages` | a bead entering one of these stages, which a flow of the pack must have |
| `crew members.<name>.hears.done` | `true`: a bead becoming done |
| `crew members.<name>.subscribe.<source>` | an outside source (`github`, `linear`, `token`) and how much of it: `involved`, `mentions` or `all` |
| `crew members.<name>.open_threads` | optional: the sources (`github`, `linear`) the crew member should be able to open a thread in unasked (`yardr thread open`). The pack only asks: the import puts it to the yard's operator ([below](#yardr-pack-apply)) |
| `sources.<source>.as` | the crew member of the pack an issue's status is set as, in that source (`github`, `linear`): a key of `crew` |
| `sources.<source>.hears.<type>.<stage>` | the stage map: when a bead of that type enters that stage, the issue of the source it tracks goes to `started`, `review` or `done`. A stage left out changes nothing. The import sets it on the yard's hook of the source ([Stage maps](#how-a-pack-maps-to-the-yard)) |

A stage's outcomes and manual moves are the flow's transitions, outcomes
first ([flows.md](flows.md#outcomes-agents-say-how-the-work-ended)): `new`
above is `new -> review on done | decide on question | backlog`. The store's
rule for a stage without outcomes holds here too: with exactly one way out
(`manual: [new]`), `yardr bead done` takes it.

**Paths.** A role, script or stage file is always relative to the pack's
directory, so a pack works wherever it is cloned. An absolute path, a path
leaving the directory (`..`) and a file that is not a regular file there (a
link) are errors, a script must be executable, and a stage file must have
something in it. Where a stage file is in the pack is the pack's choice, and
two stages may name the same one; the starter keeps them as
`flows/<flow>/<stage>.md`, the layout a depot's repository and the yard's home
use.

**Placeholders.** A pack names no person and no crew member of a yard, so its
role and stage files say who they mean in double braces, and the import
puts the yard's names there:

| placeholder | filled with |
|---|---|
| `{{operator}}` | the yard's operator: its `operator` setting, the name `yardr init` and `yardr depot init` put in the roles they write |
| `{{<crew member>}}`, for a key of `crew` | the yard's crew member that takes it: `{{yardmaster}}` reads `boss` under `--crew yardmaster=boss`. A pack whose crew member is called `lead` writes `{{lead}}` |

So the starter's builder is told to run ``yardr mail {{yardmaster}} "…"`` and an
agent prompted from the installed copy reads `yardr mail boss "…"`.
Nothing else is filled: any other `{{…}}` in a role or stage file the pack
names, `{{ operator }}` with spaces included, is a mistake of the pack that
`pack check` names by file and line. `{{operator}}` is the operator also in
a pack with a crew member called `operator`. A script is copied as it is, so
braces in it are its own.

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
- a flow without an `end` stage; an `end` stage with a group, a stage file or
  a way out;
- a group that is not exactly one of agent, script, people;
- a role, script or stage file that is missing, absolute or outside the
  pack, a script that is not executable, or a stage file that is empty;
- a placeholder in a role or stage file that an import does not fill
  ([Placeholders](#packyaml)), named by that file and its line:
  `packs/mine/roles/builder.md:44: placeholder {{lead}}: an import fills
  {{operator}}, {{yardmaster}} (the operator and the pack's crew) and
  nothing else`;
- `hears.stages` naming a stage no flow of the pack has;
- a `subscribe` source this yardr has no adapter for, or an unknown scope;
- an `open_threads` source yardr cannot open threads in (`token`), with the
  ones it can;
- under `sources`: a source this yardr has no adapter for, or one it cannot
  set a status in (`token`); a source without `as` or without `hears`; `as`
  naming no crew member of the pack; a bead type no flow of the pack is for; a
  stage that flow lacks; a word that is not `started`, `review` or `done`,
  or one the source has no status for (GitHub knows open and closed, so
  `review` is refused there as `yardr hook add --map` refuses it).

Which flow a type's stages are held to: the flow with that `type:`, else a
flow a depot could follow, since a depot's flow governs every type that has
none of its own. A pack with several such flows may name a stage any of
them has.

## yardr pack check

```sh
yardr pack check <dir> --depot <depot> [--crew <pack's>=<yard's>]… [--flow <flow>] [--depot-roles] [--json]
```

It prints what an import into the depot would do, object by object, and
writes nothing, neither to the store nor under the home:

| it says | meaning |
|---|---|
| `create` | the yard has no object of that name |
| `change`, then `field: old -> new` per field | it has one that differs in what the pack carries |
| `unchanged` | it has one and nothing would change |
| `not in the pack` | the yard has it, the pack does not, and no import of this pack brought it: an import leaves it alone |
| `no longer in it` | an earlier import of this pack into the depot brought it and the pack has dropped it since: left alone unless the import is told to prune ([Pruning](#pruning)) |
| `shadows …` under an object | a file of the depot that is not used while the object is as the pack sets it: the depot's own `.yardr/roles/builder.md`, under a group prompted from the pack's copy |
| `kept: …` under an object | something the pack carries and the import leaves as the yard has it, with the reason: a prompt, where the depot has its own role and the import was given `--depot-roles` |
| `edited by hand since the last import: …` under a file | the installed copy is neither what the last import installed nor what the pack has now: `pack apply` overwrites it only with `--yes` ([below](#yardr-pack-apply)) |

then what an import would ask, which flow would govern the depot afterwards,
what each subscribed source has and lacks, what each crew member would be let
open threads in ([Opening threads](#how-a-pack-maps-to-the-yard)), and notes. `--json` prints the
same plan as data ([below](#the-plan-as-data)). It exits 1 on an invalid
pack and on a plan an import would refuse.

## How a pack maps to the yard

Everything is matched by name, so importing twice changes nothing the
second time.

**Files.** The roles and scripts the pack's groups and crew members name are
copied to `<home>/packs/<pack>/…` (`~/.yardr/packs/starter/roles/builder.md`)
and referred to by that absolute path. A group's prompt is then no file a
bead's branch can change, as with the home's own roles
([configuration.md](configuration.md#role-prompts)). Files of the pack that
nothing in `pack.yaml` names are not copied.

A role is installed with its placeholders filled ([above](#packyaml)), so
the copy differs from the pack's file where that names the operator or a
crew member; a script is copied byte for byte. The plan says what it would
fill in (`in its roles and stage files {{operator}} would read Jo,
{{yardmaster}} would read boss`), and compares, hashes and records the
filled text: a fresh import shows no change. The depots a pack is imported
into share its copies, so when another crew member takes one of the pack's
(`--crew yardmaster=boss`), the files naming it show as `change` and are
written again.

**Stage files.** A flow of the pack is a flow in the store, and a store
flow's stage files are read from the yard's home. So the file a stage names
is copied to `<home>/flows/<pack>.<flow>/<stage>.md`
(`~/.yardr/flows/starter.default/review.md`), under the stage's name
whatever the file is called in the pack. The plan lists each with the
pack's other files, as `flows/default/review.md -> <where it goes>`; only
the flows the import uses have theirs copied. They are filled, recorded,
checked for hand edits and pruned as roles are. Every depot following the
flow reads the same copies. A stage file in that directory the pack does
not carry (one written by hand for a stage the pack names no file for)
shows as `not in the pack`, by its path under the home
(`flows/starter.default/<stage>.md`), and is left alone. The stage files in
a depot's repository are not touched; like its flow files, they are not read
while the depot's pointer is at the pack's flow.

**The pack's roles win.** An import gives the depot the pack's way of working
whole, its roles as its flow. Every agent group and crew member the pack names
is pointed at the pack's copied role, also where the depot has
`.yardr/roles/<name>.md` on its base branch (in its directory, for a dir
depot) and the pack's role is `roles/<name>.md`. The depot's file stays where it
is and is read by nothing; the plan shows the `change` of `prompt` and names
the file under the object:

```
  change           web-builders
                     shadows .yardr/roles/builder.md
                     prompt: roles/builder.md -> /Users/me/.yardr/packs/starter/roles/builder.md
```

`--depot-roles` is the opposite choice. A group whose depot has its own role
keeps its prompt as it is; a new group, and one the pack's copy was set on
before, gets `roles/<name>.md`, which yardr resolves to the depot's file; and
the plan says `kept: prompt roles/builder.md: the depot has its own
.yardr/roles/builder.md`. A crew member that has a prompt of its own keeps it
the same way. Groups and crew members whose role the depot lacks get the pack's
copy either way, and every copy is installed all the same.

`yardr depot sources <depot>` shows the outcome for a depot as it is now: one line
per flow, stage file and role, with where it comes from and the depot's file
it shadows ([configuration.md](configuration.md#where-a-depots-flows-stage-files-and-roles-come-from)).

`yardr depot source <depot> role <group|crew> depot|pack` switches one of
them afterwards, without a new import: it sets the prompt to the depot's role
or to the pack's copy, prints the line as `depot sources` then has it, and
refuses when the depot has no such role or no pack of the depot installed one
([configuration.md](configuration.md#switching-one-of-them)).

The record remembers which of the two was chosen (`roles` in `yardr pack
list`), and an import without the flag repeats it, for every role: one
switched the other way by `yardr depot source` is set back, and that command
says so when it applies. `--pack-roles` chooses
the pack's roles again after an import with `--depot-roles`; it is the default
otherwise, and says so in one line.

**Groups.** An agent or script group is the depot's, named `<depot>-<group>`
(`yardr-builders`); a people group keeps its name and is the yard's, shared
by every depot that imports a pack naming it.

| pack | group in the yard | the pack sets | the yard keeps |
|---|---|---|---|
| `agent: <role>` | runner `herdr` | `prompt` (the copied role), `limit` | `kind`, `model`, `args`, `env_file`, `idle` and every other setting |
| `run: <script>` | runner `exec` | the command in `cmd` (the copied script), `limit` | the assignments before the command and the arguments after it (`YARDR_PUSH=origin … merge-local`), `log`, `dir` and the rest |
| `people: true` | runner `manual` | `limit`, when the pack gives one | `members`, subscriptions |

A group the yard lacks needs what a pack cannot say: the agent kind, model
and permission arguments, or the members. These are the import's questions.

**Flows.** A flow is stored as the named flow `<pack>.<flow>`
(`starter.default`), and the depot is pointed at it (`yardr depot flow`); a
flow with `type:` becomes that type's pointer. Of several flows without a
type the depot follows one: `--flow`, else the first; the others are not
imported.

A pointer in the store wins over the flow files in the depot's repository
([flows.md](flows.md#which-flow-governs-a-bead)), and it wins as a whole: a
depot whose base branch carries `.yardr/flows/default.flow` (what `yardr depot
init` writes) is governed by the pack's flow once the import has pointed it
there, and its `type/<type>.flow` files stop governing too. The files stay
and govern again when the pointer is cleared (`yardr depot flow <depot>
--clear`). `yardr depot sources <depot>` names each of them, and the stage files
beside them, as shadowed. `yardr depot source <depot> flow [--type <type>]
depot|pack` moves the depot between the two: `depot` clears the pointer, `pack`
points it at the pack's flow again, and either way the flow switches whole,
its stage files with it. The plan says which flow would govern the depot's beads and each
type's afterwards, and which does now. A type pointer the pack has no flow
for is left alone.

The store refuses a pointer or a flow that would leave an open bead in a
stage its new flow lacks ([flows.md](flows.md#named-flows-and-pointers)).
The plan makes the same check for every flow of the pack, over the beads
that would follow it by a pointer or a pin, and marks the import
**refused**, naming the beads: move or pin them first.

**Routes.** Each stage with a group gets the one route `depot, stage ->
group` (with the type, for a type's flow). A route the depot has for the
stage is that route: unchanged when it leads to the same group, else
replaced. `yardr route add` mints a new id per call, so a replacement is
an add and a delete of every route the depot had for the stage; two routes
for one depot and stage never remain. A stage with no group clears the
matching depot, stage and type routes, so its beads wait for a hand. The
route's priority is kept when a stage has a group. Routes with a label, and
routes for stages the pack lacks, are left alone.

**Crew members.** A crew member of the pack is taken by a crew member of the yard:
`--crew yardmaster=boss`. Without the flag it is the yard's crew member
of the same name, else whoever took it at the last import, else a question.
The pack sets three things and nothing
else (identity, `env_file`, args, model, directory and rotation are the
yard's):

| pack | crew member |
|---|---|
| `role` | its `prompt`, the copied file |
| `hears.events` | its `--on` kinds: the list is replaced, so a pack carries every kind its crew member should hear |
| `hears.stages` | one subscription per stage: `yardr all depot=<depot> stage=<stage>` |
| `hears.done` | `yardr all depot=<depot> state=done` |
| `subscribe.<source>: <scope>` | `<source> <scope>`; a subscription to the source without filters has its scope changed |

Its other subscriptions are left alone
([subscriptions.md](subscriptions.md)).

**Opening threads.** `open_threads` is the one key of a crew member the pack
does not set: it asks. Speaking first in a source is the operator's to grant
([crew.md](crew.md#opening-a-thread)), so the plan sorts the
sources the pack names, per crew member, and the import asks the operator
about what is left:

```
open threads, for crew member boss
  the pack asks for linear, github
  asks      linear: the operator is asked, and the answer without one is no
  the pack wants github, this yard lacks hook and credential: nothing is asked or granted
    missing  hook         no hook receives github: yardr hook add github --source github --secret-file <file>
    missing  credential   GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY_FILE, or GITHUB_TOKEN, in crew member boss's env_file, to answer in github: …
```

| the plan says | meaning |
|---|---|
| `has` | the yard's crew member may open threads there already: no question |
| `asks` | the import asks the operator |
| `the pack wants <source>, this yard lacks <hook\|credential>` | the yard has no hook for the source, or the crew member's `env_file` does not name its credential, read as the Sources table below reads them. The import asks nothing and grants nothing for it, by flag either, and records no answer: an import after the yard has both asks then. A crew member the import creates has no `env_file` yet, so its sources are all here the first time |
| `declined` | an earlier import was told no: not asked again; `--open-threads` still grants it |
| `grants`, `declines` | in the plan `pack apply` prints: what `--open-threads` answered |

The import only adds to the crew member's `open_threads`. A pack without the
key, a no, and a source nobody was asked about leave the setting as the
yard has it.

**Stage maps.** `sources.<source>` says which status an issue goes to when
the bead tracking it ([links.md](links.md)) enters a stage, and as which
crew member. Which stage means which status is opinion about the flow, so the
pack carries it; the import puts it where the yard keeps it, on the yard's
hook of that source, as `yardr hook add --as <crew member> --map
<type>:<stage>=<state>` would:

| pack | hook |
|---|---|
| `as: yardmaster` | acts as the yard's crew member that takes `yardmaster` (`--crew yardmaster=boss`) |
| `hears.task.review: review` | the map line `task:review=review` |

The plan says, per source, what it would do and what the yard has for it:

```
stage maps
  create           hook linear as boss

stage map of source linear, as crew member boss
  the pack says task:merged=done task:new=started task:review=review
  would be set on hook linear, which has none
  ok       hook         hook linear
  ok       identity     crew member boss has an identity in linear
  missing  set state    crew member boss may not set a status in linear, so no issue is moved. The import does not grant it: the operator allows it with yardr crew set boss set_state=linear
```

| the plan says | meaning |
|---|---|
| `would be set on hook <name>, which has none` | the hook gets the crew member and the map (`create`) |
| `would replace what hook <name> has: as <crew member>, <map>` | the hook has another map or acts as another crew member: it gets the pack's, whole, and the object's `as:` and `map:` lines show old and new (`change`) |
| `hook <name> has it` | nothing to do (`unchanged`) |
| `nothing would be set` | the yard has no hook of the source, or the crew member no identity in it. The import goes through without it; the next one sets the map once the yard has what is `missing` |
| `not set on hook <name>: …` | in the plan `pack apply` prints: this run may not have a hook act as that crew member ([below](#yardr-pack-apply)) |

| need | what is looked at |
|---|---|
| `hook` | a hook with that source. **The import never makes one**: it has no secret to give it. With several hooks of one source the map goes on the first by name, and a note says so |
| `identity` | the crew member's identity in the source. A hook acts only as a crew member that has one, which keeps the source's delivery of the change out of that crew member's inbox; a crew member the import itself creates has none yet |
| `set state` | whether the crew member's definition lets it set a status in the source (`set_state`, [crew.md](crew.md)). **The import never grants it**: with the map set and the grant missing, no issue is moved, and the line names the command the operator runs |

**Sources.** For each source a crew member subscribes to, the plan says what
the yard has and lacks to hear it and answer in it:

| need | what is looked at |
|---|---|
| `hook` | a hook with that source (`yardr hook add`) |
| `secret file` | the hook's secret file exists |
| `identity` | the crew member's identity in the source (`yardr crew identity`) |
| `credential` | the name of the reply credential is set in the crew member's `env_file`: `LINEAR_API_KEY`; for GitHub a GitHub App's pair (`GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY_FILE`) or `GITHUB_TOKEN`. Only names are read, and for an App its key file, which must be a private key only its owner can read ([crew.md](crew.md#as-a-github-app)) |
| `listener` | what the yard's running server takes webhooks on, as it wrote it when it started: `ok` with the address and whether it is the `hook_addr` setting's or `YARDR_HOOK_ADDR`'s, `missing` when the server has no address or could not listen on its own (with the setting to change). `unknown` only when no server has said: a build from before `hook_addr` |

## yardr pack apply

```sh
yardr pack apply <dir> --depot <depot> [--yes] [--prune]
    [--bind <group>=<kind>[:<model>][,<preset>]]… [--members <group>=a,b]…
    [--crew <pack's>=<yard's>]… [--open-threads <pack's>=<source>,…|none]…
    [--flow <flow>] [--depot-roles] [--json]
```

It makes the plan `pack check` shows, prints it with what the flags answer
filled in, asks for what the pack cannot say, and sets the yard to the
pack's state. An object the yard has
under the same name is overwritten in what the pack carries and keeps the
rest; nothing is added a second time.

**What it asks.** In a terminal it asks; each question has a flag that
answers it instead:

| question | flag |
|---|---|
| the agent and model of a group the yard lacks, picked from the list the installed agent gives, as in the group form | `--bind <group>=<kind>[:<model>]`; no model is the agent's own default |
| what that agent may do without asking, picked from the permission presets the group form shows | `,<preset>` at the end of its `--bind`; see below for what it gets without one |
| the members of a people group the yard lacks | `--members <group>=a,b`; `--members <group>=` for anyone |
| which crew member of the yard takes a crew member of the pack | `--crew <pack's>=<yard's>` |
| the agent, model and permissions of a crew member the yard lacks | `--bind crew:<pack's>=<kind>[:<model>][,<preset>]` |
| which flow the depot follows, when the pack has several without a type | `--flow <name>` |
| whether a crew member may open threads in the sources the pack asks for | `--open-threads <pack's>=linear,github`, or `=none`; see below for what happens without it |

`<group>` is the pack's name for the group (`builders`, not
`web-builders`). A flag for a group or crew member the yard already has
answers nothing, since its binding is the yard's and is kept; that is not
an error, so the same command can be run again. A crew member the import
creates works in the yard's home, as one made by `yardr crew add` does.

**One answer to `--crew` per yard.** The filled copies of a pack's
roles and stage files are installed once per pack
(`<home>/packs/<pack>/roles/…`, `<home>/flows/<pack>.<flow>/…`), and every
depot that imports the pack reads those shared files ([above](#how-a-pack-maps-to-the-yard)).
A second depot importing the pack with another `--crew <pack's>=<yard's>`
rewrites them for the first depot too, so its agents read the second depot's
crew member in their roles and stage files. Import a pack with the same
crew members in every depot of a yard.

Without a terminal, or with `--json`, nothing is asked: a question without
its flag is an error that names the flag, never a default, with two
exceptions: the permission arguments, and whether a crew member may open
threads, both below. `--yes` applies without the
confirmation, and without a terminal it is required whenever there is
something to write.

**A new agent gets permission arguments.** A pack names none, and an agent
without them stops at every permission prompt, with nobody watching. So an
agent group or crew member the import creates is given some:

- In a terminal the import asks, after agent and model, with the
  permission list of the group form (`yardr group add`), the kind's
  unattended arguments preselected.
- Without a terminal, or with `--yes`, it gets the kind's unattended
  arguments, the ones `yardr init` gives the yardmaster: claude
  `--permission-mode auto`, codex
  `--dangerously-bypass-approvals-and-sandbox`, kimi `--yolo`.
- `--bind` may name another preset after a comma, by the label the group
  form shows, in lower case with `-` for a space: claude has `auto`,
  `accept-edits` and `bypass`, codex `approve-for-me` and `bypass`; `none`
  is no arguments, for any kind. `--bind builders=claude:opus,bypass`,
  `--bind reviewers=codex,approve-for-me`. A preset the kind lacks is an
  error that lists the ones it has. It is a comma, not a third colon,
  because a model's name may have colons of its own.
- A kind yardr knows no unattended arguments for (pi, or any kind outside
  the presets) gets none, as does `none`: the output then says, for each
  such group and crew member, that its agent stops at permission prompts
  until it is given arguments ([groups.md](groups.md#herdr-setup)).

The plan `pack apply` prints shows, under each group and crew member it would
create, the agent, model and arguments the flags give it, and lists under
"an import would ask" only the questions still open. The arguments of a
group or crew member the yard already has are the yard's and are never
touched, whatever `--bind` says.

**Opening threads is asked of the operator.** For each crew member with a
source under `asks` in the plan:

- In a terminal the import asks once per crew member, after its other
  questions and the confirmation: `Let boss open threads in linear?`
  The default is no, and the one answer is for every source in the
  question.
- Without a terminal, with `--json` or with `--yes`, nothing is asked and
  the flag answers: `--open-threads yardmaster=linear,github` lets the crew member
  that takes the pack's `yardmaster` open threads in those and says no to the
  other sources in the question; `--open-threads yardmaster=none` says no to
  all. A no changes nothing in the yard, and the output says it is on
  record. Without the flag nothing is granted, the rest is imported, and the
  output names the flag. The safe answer exists here, and it is no, which
  is why this is no error. Without a terminal an answer by flag needs
  `--yes`, like any other change.
- The flag names sources the pack asks for; any other is an error. One the
  crew member has already answers nothing, so the same command can be run
  again. One the yard lacks a hook or a credential for grants nothing, and
  the output says what is missing.
- Only the operator grants, by the check `yardr crew add` makes: the
  yard's `operator` setting, else the user running the command. Run by
  anyone else (a crew member's session, a bead's agent) the import does
  everything else, grants nothing whatever the flag says, records no
  answer, and says who may grant it; the operator's next apply is asked.
- A yes is logged as `open_threads_granted` on the crew member's journal, with
  the pack, the crew member, the sources that were new to it and the actor.
  The crew member needs no restart: `yardr thread open` reads the setting each
  time.

**Stage maps.** For each source under `sources` the import sets the map,
and the crew member it is acted on as, on the yard's hook of that source, and
leaves the hook's name, source and secret file. A hook that has another map
gets the pack's.

- No hook of the source, or a crew member without an identity in it: the
  import goes through, sets nothing for that source and says what to add
  (`the stage map of linear was not set: no hook receives linear, and the
  import makes none: yardr hook add linear --source linear --secret-file
  <file>, then apply the pack again`). The next apply sets it.
- It grants nothing. While the crew member lacks `set_state` for the source the
  output ends with a `note:` naming the command the operator grants it with
  (`yardr crew set <crew member> set_state=linear`), on every
  apply until it is granted.
- A hook that acts as a crew member speaks as it. So, as `yardr hook add --as`
  does, the import sets the map only when run by the operator or from that
  crew member's own session. Run by anyone else it does everything else, sets
  no map, records none and prints the refusal; `--json` has it under
  `maps_refused`, by source.
- What it set is printed as `hook linear now moves tracked linear issues as
  boss: …` and logged as the store logs any `hook_added`.

**Twice is once.** Applying a pack that is already applied writes no object
and logs one event, `pack_unchanged`. A hook that has the pack's map and
acts as the crew member that takes the pack's is left as it is. It needs no flag and no `--yes`: the
record says which crew member took each of the pack's, which flow the depot
follows and whose roles were chosen. It also keeps each answer about
opening threads, by crew member and source, yes or no: the same pack asks
nothing the second time, and one that asks for a further source later asks
about that source alone. A source nobody could be asked about (the yard
lacked it, nobody with a terminal or a flag was there, the operator was not
the one applying) has no answer and is asked about next time. So is a yes
whose setting the operator has since taken away by hand: the import never
gives it back unasked. An import whose only news is an answer writes the
record and logs `pack_applied`.

**A copy edited by hand is not overwritten silently.** The record holds the
content of each file as the import installed it, placeholders filled. A copy under
`<home>/packs/<pack>/`, or a stage file under `<home>/flows/<pack>.<flow>/`,
that is neither that nor what the pack has now was edited in the yard; the plan marks it, and the import stops before it writes
anything, in a terminal too:

```
nothing was changed: /Users/me/.yardr/packs/starter/roles/builder.md was edited by hand since the last import, and the import would overwrite it: the edit belongs in the pack's source (/Users/me/yardr/packs/starter/roles/builder.md); --yes overwrites it
```

Make the edit in the pack and apply again, or give `--yes` to drop it. The
depots a pack is imported into share its copies, so a copy that is as the
import into any of them left it counts as not edited. A record written
before yardr kept the content says nothing, and such a copy is overwritten
as before.

**All or nothing, as far as the store allows.** Before the first write the
import checks that the plan is not refused, that every question has a usable
answer, that the pack's files are the ones the plan was made from, and that
no group would be given a runner the store refuses. A failure there changes
nothing. The store has no transaction over several objects, so a write that
fails after that stops the import where it is: the objects written before it
stay, no record is written, and since everything is matched by name,
applying again finishes the import and repeats nothing. A route is added
before the ones it replaces are deleted, so a stage is never left without a
route; if a delete fails, the next import finds two routes for the stage and
replaces both by one.

**Refused** while open beads sit in a stage the pack's flow lacks: the plan
is printed, the message names the beads, nothing is changed, and the exit
code is 1. Advance or pin them, then apply again.

**Running crew members are not restarted.** A crew member that is running and
whose role, `--on` kinds or subscriptions the import changes keeps its
session; the plan notes it and the output names it with the command
(`yardr crew restart <name>`). The new role is read when its next
session starts.

### The record

The yard records each import, one record per pack and depot, replaced by the
next import that changes something:

```
$ yardr pack list
starter in depot web  2026-10-03 18:40 by mckean
  source     /Users/me/yardr/packs/starter at 8562e43c1f0a
  flow       default (stored as starter.default)
  roles      the pack's
  groups     backlog -> backlog, builders -> web-builders, decisions -> decisions, refinery -> web-refinery, reviewers -> web-reviewers
  crew       yardmaster -> boss
  objects    9 files, 5 groups, 1 flows, 1 flow pointers, 5 routes, 3 subscriptions
```

| field (`--json`) | meaning |
|---|---|
| `pack`, `depot` | what was imported into which depot |
| `source` | the directory it was read from |
| `commit`, `dirty` | that directory's git commit when it is in a repository, and whether it held changes the commit does not |
| `at`, `by` | when, and the actor who applied it |
| `flow` | the pack's flow the depot follows |
| `depot_roles` | `true` when the import was given `--depot-roles`; absent when the pack's roles were set |
| `groups`, `crew` | pack name -> yard name |
| `objects` | the files, groups, flows, pointers, routes, subscriptions and stage maps that are the pack's; a file with the `sha256` of its content as installed, placeholders filled; a stage map (`kind: map`) with the hook's `name`, the `crew` it acts as and the `map` as the import set it |
| `open_threads` | the answers about opening threads: yard's crew member -> source -> `true` or `false`; absent when nothing was answered. `yardr pack list` prints them as `may open   boss: github no, linear yes` |

An import that changes nothing leaves the record as it is, its commit and
time included: it is the record of the last import that did something. The
import is logged as `pack_applied` when the record was written and
`pack_unchanged` when not, with pack, depot, source and commit. Removing a depot
removes its records.

A crew member is in the mapping and not among the objects: it is the yard's.

### Pruning

An object the last import brought and the pack no longer carries (a group
it dropped, the route of a stage it lost, a role file nothing names any
more, the stage file of a stage that lost its `file`, a subscription to a
stage a crew member no longer hears) shows in the
plan as `no longer in it`. An import leaves it in the yard and lists it;
`--prune` removes it. Only what the record names is ever pruned: a group
or route made by hand stays `not in the pack`, and so does everything in a
yard no pack was imported into.

- A group is removed as `yardr group rm` does: with the routes that lead to
  it, releasing the beads its live sessions hold.
- A people group is the yard's, shared by its depots: one that another import
  names, or that a route outside the depot leads to, is **held** and stays
  even with `--prune`. So does a flow another depot follows. The plan says
  why.
- A crew member is never removed. One that no longer takes a crew member of the
  pack keeps its role and `--on` kinds as the import left them; its
  subscriptions that came from the pack are pruned like any other object.
- A stage map of a source the pack no longer has under `sources` shows as
  `no longer in it` while the hook still acts as that crew member with the map
  the import set; `--prune` clears the hook's crew member and map and keeps the
  hook. A map changed by hand since (`yardr hook add`) is the yard's, and
  is neither listed nor pruned.
- A file is removed only under `<home>/packs/<pack>/`, or as a stage file
  of one of the pack's flows, `<home>/flows/<pack>.<flow>/<stage>.md`. A
  dropped stage file is named by that path under the home.
- A flow's stage files stay while the flow does: with a flow that is held,
  and with one the store refused to remove.
- The stage file of a stage the flow lost is named after no stage of the
  flow any more, which `yardr depot check` reports as a stray file in the
  yard's home ([flows.md](flows.md#stage-files-what-a-stage-is-for)) until
  it is pruned.

Pruning happens after the import, before the record. A removal the store
refuses (a flow an open bead still pins) does not undo the rest: what could
be removed is, the record keeps the object so the next `--prune` tries
again, and the command exits 1 naming it.

## The plan as data

`pack check --json` prints the plan an import executes (`pack apply --json`
prints `{"plan": …, "answers": …, "open_threads": …, "maps_refused": …, "result": …}`, the
answers by question id, an `args` answer being the arguments the new agent
got, space-separated; `open_threads` has `answers`, crew member -> source ->
yes or no as given by flag or form, and `notes`, what was not granted and
which flag or who would grant it; `result.granted` lists each crew member with
the sources it was let open threads in; `maps_refused` is source -> why this
run set no stage map for it, and `result.mapped` the maps it set, each with
`hook`, `source`, `as` and `map`):

```json
{
  "pack": "starter", "dir": "/…/packs/starter", "depot": "yardr",
  "flow": "default", "depot_roles": false, "groups": {"builders": "yardr-builders"}, "crew": {"yardmaster": "boss"},
  "fill": {"operator": "Jo", "yardmaster": "boss"},
  "install": "/Users/me/.yardr/packs/starter", "stage_files": "/Users/me/.yardr/flows",
  "objects": [
    {"kind": "group", "name": "yardr-builders", "action": "change", "shadows": [".yardr/roles/builder.md"],
     "changes": [{"field": "prompt", "old": "roles/builder.md", "new": "/Users/me/.yardr/packs/starter/roles/builder.md"}],
     "group": {"name": "yardr-builders", "runner": "herdr", "config": {"kind": "claude", "prompt": "/Users/me/…"}, "limit": 5}}
  ],
  "questions": [{"id": "group.web-builders.kind", "object": "group web-builders", "field": "kind", "ask": "…", "default": "claude"}],
  "governs": [{"flow": "starter.default", "source": "store", "now": "default (repo@77a3a8f)", "in_pack": true}],
  "sources": [{"source": "linear", "crew": "boss", "needs": [{"what": "hook", "status": "ok", "detail": "…"}]}],
  "open_threads": [{"pack": "yardmaster", "crew": "boss", "wants": ["linear", "github"], "has": [], "asks": ["linear"], "declined": [],
    "lacks": [{"source": "github", "missing": [{"what": "hook", "status": "missing", "detail": "…"}]}]}],
  "maps": [{"source": "linear", "pack": "yardmaster", "as": "boss", "map": {"task:new": "started", "task:review": "review", "task:merged": "done"},
    "hook": "linear", "action": "create", "needs": [{"what": "set_state", "status": "missing", "detail": "…"}]}],
  "notes": [], "refused": []
}
```

`install` is where roles and scripts are copied, `stage_files` the
directory under which each flow's stage files are
(`<stage_files>/<pack>.<flow>/<stage>.md`). `fill` is what the import puts
for each placeholder, by its name.

Objects come in the order an import does them: files (roles and scripts,
then stage files), groups, flows,
pointers, routes, crew members, subscriptions, stage maps; dropped objects come last.
`action` is `create`, `change`, `unchanged`, `not_in_pack` or `dropped`.
Each object carries what the import would write (for a dropped one, what a
prune removes), under its kind:

| kind | carries |
|---|---|
| `file` | `from`, `to`, `mode`, `sha256` (of the content as installed), and `fill: true` on a role or stage file, whose placeholders are filled; a stage file also `flow`, the store flow it belongs to, and `stage` |
| `group` | the whole group as it would be stored, what the yard keeps included |
| `flow` | the whole flow, its stages in order |
| `pointer` | `depot`, `type`, `flow` |
| `route` | `stage`, `depot`, `type`, `group`, `priority`, and `delete`: the ids of the routes it replaces |
| `crew` | `pack` (its name in the pack), `crew` (in the yard), `prompt`, `on`, and `restart` when it is running and the import changes its role or what it hears |
| `subscription` | `crew`, `add`, and `remove` when a scope changes |
| `map` | `hook`, `source`, `as` and `map`: what the hook is set to. Only a map the import can set is an object; `maps` has every source |

An object's `asks` are the ids of the questions whose answers it still
needs, its `keeps` what the import leaves as the yard has it, and its
`shadows` the depot's files the pack's role stands in front of. A file has
`edited: true` when its installed copy was edited by hand. `depot_roles` says
whose roles the plan sets. A dropped
object has `owned`, the record's entry for it, and `held` when not even a
prune removes it. The question `pack.flow` belongs to no object: it is
asked when the pack has several flows a depot could follow. A plan with
anything under `refused` must not be applied.

`maps` has one entry per source under the pack's `sources`: the `map` keyed
`<type>:<stage>` as a hook keeps it, `pack` and `as` (the crew member by the
pack's name and the yard's), `hook` (absent when the yard has none),
`action` (`create`, `change` with `was`, the hook's `as` and `map` before,
`unchanged`, or `not_set` when the import sets nothing), and `needs`: `hook`,
`identity` and `set_state`, each `ok` or `missing` with the command in
`detail`.

`open_threads` has one entry per crew member the pack asks it for. Each source
under `wants` is in exactly one of `has`, `lacks`, `declined` and `asks`;
`recorded` holds the last import's answers. What `asks` lists is not among
`questions`: without an answer it is no, not an error.
