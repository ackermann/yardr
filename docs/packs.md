# Packs

A pack is one yard's way of working as a directory: the flows a bead passes
through and what each of their stages is for, the groups that work the
stages, the role prompts and scripts those groups run, and what an always-on
crew member hears. A yard imports a
pack into a depot; the pack names no agent product, model, argument, depot,
person, secret or identity, which are the yard's and stay in it.

`yardr pack check` reads a pack and shows what importing it would change;
`yardr pack apply` imports it; `yardr pack list` shows what the yard
imported; `yardr pack export` writes a depot's way of working out as a pack
([below](#yardr-pack-export)). `yardr depot init` sets a new depot up from the yard's
[default pack](#the-default-pack). `yardr flow draw <pack>` draws a pack's
flow with the groups it puts at each stage, with no yard
([flows.md](flows.md#drawing-a-flow)). A pack is a directory on this
machine, one yardr carries in its binary, or the pack's directory on a web
server, fetched when the command runs.

The pack yardr ships is [`packs/starter`](../packs/starter): a flow from
backlog to merged with its stage files, the roles of a builder, a reviewer,
a security reviewer and the yardmaster, and the reference assembly, as one
pack. It is in the
binary, so `starter` names it with no checkout at hand
([Naming a pack](#naming-a-pack)), and it is the default pack of a yard that
names none.

It is written for a git depot. For a depot that is a plain directory yardr
carries a second pack, [`packs/dir`](../packs/dir), named `dir`: two flows
(the depot's, and its trains') with their stage files, a builders and a reviewers group whose
roles are written for a directory, the starter's two people groups for
`backlog` and `decide`, no group that lands anything and no crew
([dir-depots.md](dir-depots.md#1-register-the-folder-and-set-it-up)).

A third, [`packs/contributors`](../packs/contributors), named
`contributors`, is for what comes to a git depot's repository from outside:
an issue or a pull request someone opens there is a bead, a maintainer looks
first, and a pull request is reviewed by an agent that only reads. It is
imported beside the pack the depot follows
([The contributors pack](#the-contributors-pack)).

Beside the depot's pack too, [`packs/design`](../packs/design), named
`design`, is the lane a question of direction takes in a git depot: a bead
of type `design` is a document an agent drafts, another argues against and
a person decides on, before anything is built
([The design pack](#the-design-pack)).

A fifth, [`packs/linear`](../packs/linear), named `linear`, is for no
depot: it is Linear itself as an integration, imported into the yard. It
declares the source `linear`, with the commands that verify and
read Linear's webhooks and that reply, open a thread and set a status in
one ([An integration pack](#an-integration-pack-kind-source)).

A sixth, [`packs/github`](../packs/github), named `github`, is for no
depot either: it is GitHub itself as an integration, imported into the
yard. It declares the source `github`, with the commands that verify and
read GitHub's webhooks and that reply, open a thread and set a status in
one, and that read a pull request and its CI back
([An integration pack](#an-integration-pack-kind-source)).

Two more, [`packs/telegram`](../packs/telegram) and
[`packs/pushover`](../packs/pushover), named `telegram` and `pushover`,
are for no depot either: each is one service as an integration, a
declaration with the `notify` key that tells a user outside the yard.
Pushover has one command, which sends the picture a tell carries;
telegram also hears the operator's chat back, with a `hook` key, `thread`
commands and a `hear` key. One apply and
two commands that each take one secret, typed unseen, set telling up
whole: `yardr pack apply telegram --notify <user>=telegram`, then `yardr
source key telegram` and `yardr user key <user> telegram`. (An apply
with files you keep yourself still does it in one: `--source-env
telegram=<file> --notify <user>=telegram,<file>`.)

One more, [`packs/slack`](../packs/slack), named `slack`, is for no
depot either: it is Slack itself as an integration, imported into the
yard. It declares the source `slack`, with the commands that verify and
read the Events API deliveries the setup subscribes to and that answer
in their threads, and the app manifest the operator creates the Slack
app from ([An integration pack](#an-integration-pack-kind-source)).

One more, [`packs/webhook`](../packs/webhook), named `webhook`, is for no
depot either, and for no one service. It is any service that POSTs to a URL
or takes one, which covers Zapier, Make and n8n in both directions. It
declares the source `webhook`, with a `hook` key that hears any POST at a
hook path, checked by a shared secret in the `X-Yardr-Secret` header, and
one update call, `send`, that POSTs a JSON object to a URL. That URL is the
credential (`credential.url: true`,
[integrations.md](integrations.md#a-declared-source)), so the declaration
names no base ([An integration pack](#an-integration-pack-kind-source)).

And [`packs/website`](../packs/website), named `website`, is the starter's
line with a deploy stage, for a depot that is a website: a landed change is
deployed to the site's project on the user's own Void platform, and the bead
closes when the site is live. It is imported instead of the starter, with
setup notes for the person whose website it is.

| pack | for | flows | a depot's default |
|---|---|---|---|
| `starter` | a git depot's own work | `default`, and `train` for trains | of a git depot |
| `dir` | a dir depot's own work | `default`, and `train` for trains | of a dir depot |
| `contributors` | issues and pull requests from outside, in a git depot | `issue` and `pr`, each for its type | of none: `yardr pack apply contributors --depot <depot>` |
| `design` | designs in a git depot: a document drafted, critiqued and decided before anything is built | `design`, for its type | of none: `yardr pack apply design --depot <depot>` |
| `website` | a git depot's website, deployed to its own Void platform after landing | `default`, past the deploy to `live` | of none: `yardr pack apply website --depot <depot>` |
| `linear` | Linear itself, in the yard | none: `kind: source` | of none: `yardr pack apply linear` |
| `github` | GitHub itself, in the yard | none: `kind: source` | of none: `yardr pack apply github` |
| `google-mail` | Gmail itself, in the yard: mail read, drafts written, nothing sent | none: `kind: source` | of none: `yardr pack apply google-mail` |
| `google-drive` | Drive itself, in the yard: files listed and read, folders and empty Docs and Slides made | none: `kind: source` | of none: `yardr pack apply google-drive` |
| `google-docs` | Docs and Slides in the yard: documents and decks read and appended to, documents replaced in place | none: `kind: source` | of none: `yardr pack apply google-docs` |
| `google-calendar` | Calendar itself, in the yard: events listed, read, created and edited without invitations | none: `kind: source` | of none: `yardr pack apply google-calendar` |
| `telegram` | telling a user through Telegram, and hearing the operator's chat back, in the yard | none: `kind: source` | of none: `yardr pack apply telegram` |
| `pushover` | telling a user through Pushover, in the yard | none: `kind: source` | of none: `yardr pack apply pushover` |
| `slack` | mentions and direct messages in a Slack workspace, answered in their threads, in the yard | none: `kind: source` | of none: `yardr pack apply slack` |
| `posthog` | PostHog itself, in the yard: HogQL queries, error tracking issues, session recordings and Replay Vision observations read, a new error issue heard | none: `kind: source` | of none: `yardr pack apply posthog` |
| `webhook` | any service that POSTs to a URL or takes one (Zapier, Make, n8n), in the yard: its POSTs heard, a JSON object sent to its URL | none: `kind: source` | of none: `yardr pack apply webhook` |

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
kind: git

flows:
  default:
    stages:
      backlog:  { group: backlog,   file: flows/default/backlog.md,  manual: [new, decide] }
      new:      { group: builders,  file: flows/default/new.md,      outcomes: { done: review, question: decide }, manual: [backlog] }
      review:   { group: reviewers, file: flows/default/review.md,   outcomes: { done: review-security, done-safe: approved, changes: { to: new, max: 3, then: decide }, question: decide } }
      review-security: { group: security-reviewers, file: flows/default/review-security.md, outcomes: { done: approved, changes: { to: new, max: 3, then: decide }, question: decide } }
      approved: { group: assembly,  file: flows/default/approved.md, outcomes: { done: merged, failed: { to: new, max: 3, then: decide }, question: decide } }
      decide:   { group: decisions, file: flows/default/decide.md,   manual: [backlog, new] }
      merged:   { end: true }

groups:
  builders:  { agent: roles/builder.md,  limit: 5 }
  reviewers: { agent: roles/reviewer.md, limit: 3 }
  security-reviewers: { agent: roles/review-security.md, limit: 2, binds_as: reviewers }
  assembly:  { run: scripts/merge-local, limit: 1, env: { YARDR_LAND: squash } }
  backlog:   { people: true }
  decisions: { people: true }

crew:
  yardmaster:
    role: roles/yardmaster.md
    hears:
      events: [attention]
      stages: [decide]
      done: true
    subscribe:
      linear: involved
    open_threads: [linear]

sources:
  linear:
    as: yardmaster
    hears:
      task: { new: started, review: review, review-security: review, merged: done }
```

| key | meaning |
|---|---|
| `name` | the pack's name: letters, digits, `.`, `_`, `-`. It names the directory its files are copied to and goes in front of its flows' names |
| `kind` | optional: the kind of depot the pack is for, `git` (a repository: beads are built on branches and landed on a base) or `dir` (a plain directory: nothing is committed and nothing merges), or `source`: the pack is an integration, imported into the yard itself and into no depot ([An integration pack](#an-integration-pack-kind-source)). `yardr depot init` refuses the pack for a depot of the other kind ([below](#yardr-depot-init)). A pack without the key fits either depot |
| `setup` | optional, for a pack of any kind: the pack's setup notes, its author's words about what a setup takes — where in the service a webhook is made and which events it needs, what an identity wants, how to test. A file of the pack, markdown (`setup: SETUP.md`), at most 64 KB, with something in it. `pack check` prints it after the plan, `pack apply` after what it did (or names `pack show <pack>` when nothing changed), `pack show <pack>` prints it later, and a crew member's prime names it by that command, so the one who helps with a setup reads it first. The notes are marked as the pack's, not checked by yardr. Text only: the pack still carries no secret, sets no identity and grants nothing |
| `flows.<flow>.stages` | the flow's stages, **in file order**: the first is where a bead starts |
| `flows.<flow>.type` | optional: the flow is for beads of this type, beside the depot's flow (a train flow beside the task flow). A type follows one flow |
| `…stages.<stage>.group` | who works the stage: a key of `groups`. A stage without one is routed to nobody |
| `…stages.<stage>.file` | optional: the stage's stage file in the pack, markdown saying what the stage is for, what must be true to leave it and when each outcome applies ([flows.md](flows.md)). Whoever works a bead in the stage is shown it. A stage without the key has none |
| `…stages.<stage>.outcomes` | outcome reported (`yardr bead done --outcome`) -> next stage, in file order. An outcome that sends a bead back may be a mapping, `{ to: <stage>, max: <rounds>, then: <stage> }`, that limits the rounds back to `to` (see "A limit on rounds" below) |
| `…stages.<stage>.manual` | stages the bead may be moved to by hand (`yardr bead advance --to`); no outcome leads there |
| `…stages.<stage>.end` | `true`: entering the stage closes the bead. Such a stage has no group, stage file, outcomes or manual moves, and a flow has at least one |
| `groups.<group>` | exactly one of `agent: <role file>`, `run: <script>`, `people: true` |
| `groups.<group>.limit` | optional: how many beads the group works at once |
| `groups.<group>.env` | optional, for a group with `run:` alone: the options of its script, a map of variable names to strings (`env: { YARDR_LAND: squash }`: the starter's assembly lands a bead by squash from it). The import writes them before the script in the `cmd` of a group it **creates**, names sorted and values quoted as the script's path is (`YARDR_LAND=squash /…/merge-local`); a group the yard has keeps its own `cmd` whatever `env` says, since after the group is made its assignments are the operator's ([Groups](#how-a-pack-maps-to-the-yard)). A name is what the shell takes for a variable's (letters, digits and `_`, no digit first), a value a string of printable characters on one line, not empty (quote a number, `"1"`, and the words YAML reads as booleans). An empty map says nothing; beside `agent:` or `people:` the key is an error. `pack check` shows the assignments a new group's `cmd` starts with, `pack show` prints a pack's `env`, and `pack export` writes none ([below](#yardr-pack-export)) |
| `groups.<group>.binds_as` | optional, for a group with `agent:`: another agent group of the pack, declared before this one, whose agent, model and permission arguments this group gets, so that the import asks nothing for it. The starter's `security-reviewers` bind as `reviewers`: the second review is run by the agent the first is, unless the import is given `--bind security-reviewers=<kind>[:<model>]`. A group the yard has already keeps what it has, as any does ([below](#yardr-pack-apply)) |
| `groups.<group>.calls.<source>` | optional, for a group with `agent:` or `run:`: the calls of a declared source the group's sessions should be able to make with `yardr call`: `read` for every read call, and calls by name, which is the one way an update is asked for (`calls: { crm: [read, note] }`). The pack only asks: the import puts it to the yard's operator ([Calls of a declared source](#how-a-pack-maps-to-the-yard)) |
| `crew.<name>.role` | the crew member's role file |
| `crew.<name>.hears.events` | event kinds from the yard itself |
| `crew.<name>.hears.stages` | a bead entering one of these stages, which a flow of the pack must have |
| `crew.<name>.hears.done` | `true`: a bead becoming done |
| `crew.<name>.subscribe.<source>` | an outside source and how much of it: `involved`, `mentions` or `all`. Any name: the source is the yard's to have, from an integration pack. While it has none the import skips the subscription and says the source once, as optional, and the next apply sets it ([below](#how-a-pack-maps-to-the-yard)). yardr's own two are always there: `yardr`, the yard's bead events, and `peer`, what the yards it is linked with say of its beads there ([subscriptions.md](subscriptions.md#the-peer-source)). The subscription is set with no filter and needs nothing of the yard |
| `crew.<name>.open_threads` | optional: the sources the crew member should be able to open a thread in unasked (`yardr thread open`). Any name: a source yardr has built in but cannot open threads in (`token`) is refused, and one the yard has nothing to open with is not asked about until it has it. The pack only asks: the import puts it to the yard's operator ([below](#yardr-pack-apply)) |
| `crew.<name>.expose_to_peers` | optional, `true`: the yards this one is linked with should see the crew member and be able to write to it ([peers.md](peers.md#the-roster-whom-a-peer-may-see-and-write-to)). The pack only asks, and names no peer and no person: the import puts "Expose <name> to <peer>?" to the yard's operator, once per peer ([below](#how-a-pack-maps-to-the-yard)) |
| `crew.<name>.calls.<source>` | optional: the same for a crew member (`calls: { crm: [customer] }`) |
| `sources.<source>.as` | the crew member of the pack an issue's status is set as, in that source: a key of `crew` |
| `sources.<source>.hears.<type>.<stage>` | the stage map, for any name: when a bead of that type enters that stage, the issue of the source it tracks goes to `started`, `review` or `done`, or, for a source whose threads take labels, gets the label of `label:<name>` (`review: "label:in review"`), which an issue and a pull request both take and yardr never removes ([links.md](links.md#a-beads-stage-moves-the-issue)). A stage left out changes nothing. The import sets it on the yard's hook of the source once the yard has the source; until then it skips the lines and says so ([Stage maps](#how-a-pack-maps-to-the-yard)) |
| `sources.<source>.says` | what the source's events say of the bead the event is about, for any name: the one that tracks its thread, else the train of its branch. A list of lines, the first an event matches counting. `where` is the events the line is for, as a subscription's `--where` is typed (`"event=pull_request action=closed merged=true"`: every word must hold, a key given twice holds either value), over the source's keys and `stage` and `type`, the stage the bead is in and its type, for a line that holds in some stages or for some types only (`type=train` holds for a train, `type=pr` for a pr bead); the event's own type is named with `event` there. `outcome` is what such an event reports for the bead, which its flow routes; `note`, optional, begins the note of the move. A line is refused when its `where` parses wrong, when it names a key a source yardr has built in has not (for any other name the yard's source is asked when the plan is made), and when no flow of the pack has an edge for the outcome in the stage the line names (in any stage, when it names none): a line with `type` is held to the flows beads of that type follow alone, any other to every flow, since the bead may be of any type, and the refusal names each flow and what it lacks for the stage. A source may have `says` alone, without `as` and `hears`. The yard has one hook of a source and its table holds the lines of every pack that carries the source, so a pack whose lines meet events another pack's may meet names the bead's `type` on each (the starter's `type=train`, the contributors' `type=pr` and `type=issue`). The import sets the lines on the yard's hook of the source, once the yard has the source, among the lines it has ([What a source's events say](#how-a-pack-maps-to-the-yard)) |
| `sources.<source>.makes` | what the source's events make, for any name: a bead for a thread no open bead tracks. A list of lines, the first an event matches counting. `where` is as in `says`, over the source's keys (for github `association` among them: whom the repository knows) and without `stage` and without `type`, which the line's bead already says; the event's own type is named with `event` there. `bead` is the bead such an event makes, its `type`, its `stage` and, optionally, the `depot` of the yard it is made in. A line is refused when its `where` parses wrong, when it names a key a source yardr has built in has not, when it names `type` (redundant with the bead the line makes), and when the flow of the pack that beads of the type follow has no such stage a bead can be made in. A source may have `makes` without `as` and `hears`. The import sets it on the yard's hook of the source, once the yard has the source ([What a source's events make](#how-a-pack-maps-to-the-yard)) |
| `sources.<name>.declaration` | for a name yardr has not built in: the file in the pack that declares a service yardr has no Go for, as `yardr source add` takes it. The import adds it to the yard's declared sources ([Declared sources](#how-a-pack-maps-to-the-yard)). Such a source has this key and no other; in a pack of kind `source` its declaration may also name the commands the yard runs (`hook`, `thread`, `link`) |
| `presets.<preset>.about` | optional, in a pack of kind `source`: one line saying what the preset grants, printed by `pack show` beside the preset's name |
| `presets.<preset>.grants.<source>` | in a pack of kind `source`: the calls of a source the pack declares that the preset grants, in the words `yardr source grant` takes: `read` for every read call, an update call by its name, a single read call by its name where a preset wants one without the rest (`grants: { crm: [read, note] }`). `pack check` refuses a source the pack does not declare, a call the declaration does not name, an empty preset, and presets on a pack of another kind; `pack show` prints the presets numbered in file order. The pack only suggests: nothing is granted until the operator grants it, and a terminal `pack apply` ignores presets ([An integration pack](#an-integration-pack-kind-source)) |

A stage's outcomes and manual moves are the flow's transitions, outcomes
first ([flows.md](flows.md#outcomes-agents-say-how-the-work-ended)): `new`
above is `new -> review on done | decide on question | backlog`. The store's
rule for a stage without outcomes holds here too: with exactly one way out
(`manual: [new]`), `yardr bead done` takes it.

**A limit on rounds.** An outcome that sends a bead back to a stage it has
been in can limit how many times it does that, and name the stage the bead goes
to after that. Write it as a mapping in place of the stage:

```yaml
review: { group: reviewers, outcomes: { done: approved, changes: { to: new, max: 3, then: decide }, question: decide } }
```

`to` is the stage the outcome leads to. `max` is how many rounds back to it the
outcome allows, a whole number of 1 or more. `then` is the stage the bead goes
to once those rounds are used. A flow file says the same as
`new on changes max 3 then decide`, and
[flows.md](flows.md#a-limit-on-rounds) explains how rounds are counted and what
happens at the limit. A limit needs both `max` and `then`, and the mapping takes
no key besides these and `to`.

`then` must be a stage of the flow, as `to` must, and it must not be `to`,
which would send the bead where it was going anyway. Any other stage will do,
whether people work it (`decide`) or it closes the bead. Which suits the flow
is the author's call. `done` takes no limit, because the arrival estimate
follows `done` and counts no rounds. The stage a `then` names counts as one of
the stage's moves, as `to` does, so `manual` does not need to list it. An
outcome with no limit stays a plain stage name.

`yardr pack check` shows a limit that was added, changed or dropped as a change
to that stage in the flow, and `yardr pack export` writes the mapping back.

The packs that ship with yardr carry limits. The starter's default flow limits
its three ways back to `new` (`changes` from `review` and from `review-security`,
`failed` from `approved`) to 3 rounds, then `decide`, and the website pack's
default flow, which is the starter's with a deploy stage, has the same three;
the design pack limits `changes` from `critique` to 2 rounds and `failed` from
`approved` to 3, both back to `draft`, then `decide`. The starter's train flow
has none. Importing one of these packs again gives a yard its limits.

**Paths.** A role, script or stage file is always relative to the pack's
directory, so a pack works wherever it is cloned. An absolute path, a path
leaving the directory (`..`) and a file that is not a regular file there (a
link) are errors, and a stage file must have something in it. A script is
installed executable because a `run:` group names it, whatever its mode in
the pack: an embedded pack's files have no mode. Where a stage file is in the pack is the pack's choice, and
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
- a `kind` that is none of `git`, `dir` and `source`;
- a key or a name YAML reads as a boolean: bare `on`, `off`, `yes`, `no`,
  `y`, `n`, `true`, `false`. One YAML reader takes `on:` for a key called
  "on" and the next for `true`, so a pack may not depend on it (it is why
  the keys are `outcomes` and `hears`). Quoted, such a word is a name;
- a boolean that is not `true` or `false` (`end: yes`);
- a stage naming a group that is not declared, or an outcome, a limit's `then`
  or a manual move to a stage the flow lacks;
- a limit that cannot be kept: `max` without `then`, `then` without `max`, a
  `max` that is not a whole number of rounds of 1 or more, a `then` equal to
  `to`, or a limit on `done`; an outcome written as a mapping with no `to`; any
  key in that mapping besides `to`, `max` and `then`;
- a flow without an `end` stage; an `end` stage with a group, a stage file or
  a way out;
- a group that is not exactly one of agent, script, people;
- a `binds_as` on a group that is no agent group, or naming a group that the
  pack lacks, that is no agent group, or that is the group itself or comes
  after it in the file;
- a role, script or stage file that is missing, absolute or outside the
  pack, or a stage file that is empty;
- a placeholder in a role or stage file that an import does not fill
  ([Placeholders](#packyaml)), named by that file and its line:
  `packs/mine/roles/builder.md:44: placeholder {{lead}}: an import fills
  {{operator}}, {{yardmaster}} (the operator and the pack's crew) and
  nothing else`;
- `hears.stages` naming a stage no flow of the pack has;
- a `subscribe` scope that is none of `involved`, `mentions`, `all`;
- an `open_threads` source yardr has built in but cannot open threads in
  (`token`), with the ones it can;
- under `calls`: a source yardr has built in (`peer`, `token`,
  `yardr`), whose calls are not declared; a call the pack's own declaration
  of the source lacks, with the ones it has; a source with an empty list; a
  people group, since a person calls as who they are and never as a group;
- in a pack of kind `source`: `flows`, `groups` or `crew`, which it carries
  none of; no `sources`, or an empty one; a source with any key beside
  `declaration`;
- under `sources`: a source yardr has built in that it cannot set a status
  in (`token`); `hears` without `as`; `as` with neither `hears` nor a
  table; neither a map nor a table; `as` naming no crew member of the pack;
  a bead type no flow of the pack is for; a stage that flow lacks; a word
  that is not `started`, `review`, `done` or `label:<name>`. Every name is
  read, and held to the yard's source when the plan is made: a word the
  source has no status for is refused there, as `yardr hook add --map`
  refuses it ([Stage maps](#how-a-pack-maps-to-the-yard)).

Which flow a type's stages are held to: the flow with that `type:`, else a
flow a depot could follow, since a depot's flow governs every type that has
none of its own. A pack with several such flows may name a stage any of
them has.

## Naming a pack

`yardr pack check` and `yardr pack apply` take the pack as their argument,
`yardr depot init` as its `--pack`, and the `default_pack` setting as its
value (a directory there is absolute). It is one of:

| written | is |
|---|---|
| with a path separator: `packs/starter`, `./mine`, `~/packs/team` (also `.` and `..`) | the pack's directory |
| a URL ending in /: `https://yardr.dev/packs/starter/` | the pack's directory on a web server: pack.yaml and the files it names, fetched when the command runs |
| a bare word: `starter`, `team` | a name: of a pack this yard has imported (`yardr pack list`), read again from where that import recorded it; else of a pack in the binary (`starter`, `dir`, `contributors`, `design`, `linear`, `github`, `google-mail`, `google-drive`, `google-docs`, `google-calendar`, `telegram`, `pushover`, `slack`, `posthog`, `webhook`, `website`) |

Each time yardr reads a file of a URL pack, it makes one GET; planning and
applying can read the same file separately. Plain `http://` is refused unless
it goes to this machine (`localhost`, `127.0.0.1`, `[::1]`), for a stand-in. A
redirect is followed within the pack's own host and refused to another host,
or from https down to http; a file of more than 1 MiB is refused. The files
`yardr pack apply` installs are the bytes it hashed in its check, never a
second download of them: a server that answers differently once the import
is under way changes nothing that is installed.

**Pinning a pack.** A pack applied with `--yes` is read by no one, and a
server may have changed what it serves since someone did. `yardr pack check`
ends its plan with the pack's digest, `digest sha256:<hex>`: one sha256 over
`pack.yaml` and every file it names (the roles and scripts, the stage files
of all its flows, the setup notes, the declarations and the programs of their
commands), each as its path in the pack, a NUL, its length, a NUL and its
bytes, in path order. One byte changed in any of them, or a file renamed, is
another digest; a file the pack does not name is not part of it. It is the
pack's and not the plan's, so it is the same for any depot and any answers,
and for the pack read from a directory or from a URL. `yardr pack apply
<pack> --sha256 <digest>`, and `yardr depot init --pack <pack> --sha256
<digest>`, refuse a pack whose digest is another, naming both, before they
ask anything or write anything. So does `yardr init --sha256 <digest>` for
the yard's default pack, before it installs the yardmaster's role from it.
The digest may be in upper case and may keep the `sha256:` that `pack check`
prints; anything else is a flag error. It is the digest of the bytes the
plan was made from, which a pack keeps from the first read of each file, and
so of the bytes the import installs, and of the role `yardr init` installs.
The pin holds for the one command: the record of an import does not keep it,
and a later `pack apply <name>` takes none unless it is given one.

A bare word is never tried as a directory, so what `starter` means does not
depend on where the command is run: in a directory that holds `starter/` it
is still the name, and `./starter` is the directory. An imported pack comes
before an embedded one of its name; when it was imported into several depots
the import into `--depot` says where it is, else the latest. If the recorded
directory is gone the command stops and names it: give the directory the
pack is in now. An import from a URL records the URL, and the next use of
the name reads the pack from it again. A word that names no pack is an error listing the packs the
yard has imported and the ones yardr embeds.

## yardr pack check

```sh
yardr pack check <pack> [--depot <depot>] [--crew <pack's>=<yard's>]… [--group <pack's>=<yard's>]…
    [--flow <flow>] [--depot-roles] [--json]
```

Its first line names the pack, where it was read from, the kind it declares
and where it goes (`pack starter (/…/packs/starter), kind git, into depot
web`; `of no kind` for a pack without the key; `pack linear (embedded in
yardr …), kind source, into the yard`). A pack of kind `source` is checked
without `--depot`, which is refused for it, and any other with it. Then it
prints what an import would do, object by object, and writes nothing,
neither to the store nor under the home:

| it says | meaning |
|---|---|
| `create` | the yard has no object of that name |
| `change`, then `field: old -> new` per field | it has one that differs in what the pack carries |
| `unchanged` | it has one and nothing would change |
| `not in the pack` | the yard has it, the pack does not, and no import of this pack brought it: an import leaves it alone |
| `no longer in it` | an earlier import of this pack into the depot brought it and the pack has dropped it since: left alone unless the import is told to prune ([Pruning](#pruning)) |
| `takes` | a group the yard has takes a group of the pack, as it is (`--group`): nothing of it would be changed, and it is not recorded as the pack's ([Groups](#how-a-pack-maps-to-the-yard)) |
| `shadows …` under an object | a file of the depot that is not used while the object is as the pack sets it: the depot's own `.yardr/roles/builder.md`, under a group prompted from the pack's copy |
| `kept: …` under an object | something the pack carries and the import leaves as the yard has it, with the reason: a prompt, where the depot has its own role and the import was given `--depot-roles`; the role, script or limit of a group that takes the pack's, where it is not the pack's |
| `edited by hand since the last import: …` under a file | the installed copy is neither what the last import installed nor what the pack has now: `pack apply` overwrites it only with `--yes` ([below](#yardr-pack-apply)) |

then what an import would ask, which flow would govern the depot afterwards,
what each subscribed source has and lacks, which sources the yard has
nothing of, once each as optional, what each crew member would be let
open threads in ([Opening threads](#how-a-pack-maps-to-the-yard)), which peers each crew member the pack asks
it for is exposed to ([Exposing a crew member to peers](#how-a-pack-maps-to-the-yard)), and notes. The plan ends
with the pack's digest (`digest sha256:<hex>`), which `pack apply --sha256` takes
([Pinning a pack](#naming-a-pack)). After the plan
come the pack's setup notes (`setup`), when it has any: its author's words
about what a setup takes, marked as the pack's, not checked by yardr. `--json` prints the
same plan as data ([below](#the-plan-as-data)). It exits 1 on an invalid
pack and on a plan an import would refuse.

A pack given as a directory is read and checked before the yard is asked
anything, so its mistakes are named, with exit 1, on a home that has no yard
yet. A valid one stops there, since the plan is made against a yard: `no
yard in <home> yet`, with how to create one, and exit 2 (in a terminal, the
onboarding). A pack given by name is the yard's to resolve and needs the
yard from the start.

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
| `run: <script>` | runner `exec` | the command in `cmd` (the copied script), `limit`; in the `cmd` of a group it creates, the assignments of `env` before the script | the assignments before the command and the arguments after it (`YARDR_PUSH=origin … merge-local`), always: `env` is never set on a group the yard has. `log`, `dir` and the rest |
| `people: true` | runner `manual` | `limit`, when the pack gives one | `members`, subscriptions |

A group the yard lacks needs what a pack cannot say: the agent kind, model
and permission arguments, or the members. These are the import's questions.

**A script's options.** A script group's `env` is the pack's say in how its
script runs, for a group the import makes. The starter's assembly has
`env: { YARDR_LAND: squash }`, so a depot set up from it lands each bead as
one commit ([line.md](line.md#landing-by-squash)), where a script run with
nothing before it lands the branch's commits as they are. The plan shows what
a new group's `cmd` starts with:

```
groups
  create           web-assembly
                     cmd sets YARDR_LAND=squash before the script
```

The group the import made is the operator's from then on, so a yard that
has it is never changed by `env`: not when the pack's `env` changes, and not
for a yard that set the group up before the pack had one. Its plan says
what its `cmd` lacks, once, as it does for a role or a limit it keeps, and
says nothing when the `cmd` has the pack's value already:

```
groups
  unchanged        web-assembly
                     kept: cmd /Users/me/.yardr/packs/starter/scripts/merge-local: the pack's env YARDR_LAND=squash is set on a new group only
```

A yard opts in with `yardr group set <group> 'cmd=YARDR_LAND=squash <the rest
of the cmd>'` (`yardr group show <group>` has the cmd as it stands), and the
line goes. The same holds for a group that takes the pack's (below). The
import keeps no record of which assignments the pack wrote, so it never
removes one either.

**A group the yard has may take the pack's.** `--group <pack's
group>=<yard's group>` (repeatable) says that the pack's group is that group
of the yard, as it is, and no `<depot>-<group>` is made: `--group
builders=yardr-builders`, `--group maintainers=decisions`. A yard whose
depots share their agents imports a pack for a second depot this way, and a
second pack for a depot, without a copy of every agent group, each with its
own limit, per depot. The group must be of the pack's group's kind: an agent
group takes an agent group, a script group a script group, people people.
A group the flag names is the yard's whatever its name: where the yard has
a group of that name that is not the pack's own, the import takes it as it
is, even under the name the pack's group would otherwise be given.

The plan says `takes` where it would say `create`, the routes of the pack's
stages lead to the yard's group, and what the pack asks for its group (the
calls of a declared source) is asked for that group. Nothing of the group
is changed: its kind, model, args, limit, members, command and role stay.
Where the pack's role, script, limit or env is not the group's, the plan says so
in a line, and it stays a line:

```
groups
  takes            yardr-builders (the yard's, for the pack's builders: nothing of it would be changed)
                     kept: prompt /Users/me/.yardr/packs/starter/roles/builder.md: the group is the yard's, and the pack's role roles/builder.md is not set on it
                     kept: limit 4: the group is the yard's, and the pack's limit 3 is not set on it
```

The pack's role is installed all the same and read by no group; `yardr group
set` is how the yard's group is given another role or limit. The group is
in the record's mapping and not among its objects, as a crew member is, so
no `--prune` removes it.

Without the flag the pack's group is taken by whichever group took it at the
last import, while the yard has that group and it is of the same kind; else
it is the depot's own. An import that would make a group asks first whether
one of the yard's takes it, when the yard has a group of that kind (agents
for an agent group, people for people; never for a script group, whose
command is the pack's): in the terminal as a list, the new group first, and
in `pack check` under "an import would ask". Where the yard has no group
of that name of its own, the import's own name for the group answers that
none does (`--group builders=web-builders`), which also ends an earlier
`--group`: the depot gets its own group, and the yard's is left as it is.

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

**Routes.** The planner asks, for each stage, what the depot's effective
routes do with a plain bead of that stage (and the flow's type) — its own
routes plus every yard default no own route of the same key sets aside.
Where a plain yard default (one group, no condition) already works the
stage for the group the pack binds, the plan writes no route and says
`stage <stage> of flow <flow>: worked by yard default <id> to <group>; no
route written`: a fresh depot inherits the yard's defaults for the stages
every depot shares, and re-applying a pack resurrects no copy a migration
removed. Where the winner leads elsewhere, or no route wins, the plan
writes the depot's own route `depot, stage -> group` (with the type, for
a type's flow) as it always has, and that own route sets the default aside
for this depot alone. A route the depot has for the stage is that route:
unchanged when it leads to the same group, else replaced. `yardr route
add` mints a new id per call, so a replacement is an add and a delete of
every route the depot had for the stage; two routes for one depot and
stage never remain. A stage with no group clears the matching depot,
stage and type routes, so its beads wait for a hand. The route's priority
is kept when a stage has a group. Routes that ask something of the single
bead (a label, a bead priority or a named originator), routes for stages
the pack lacks, and a stage whose effective winner carries a quota
condition or further groups, are left alone, of either scope, each listed
as not in the pack with its scope; a pack writes no route over a condition
it cannot say. An import never writes or removes a yard default: where
fitting the pack would have to change or remove one — a stage the pack
routes to nobody that a default works — the plan is refused and says so,
for a hand to remove the default. Export follows the same effective
winner: a stage worked plainly through a yard default is exported with
that default's group, the route itself not left out.

**Crew members.** A crew member of the pack is taken by a crew member of the yard:
`--crew yardmaster=boss`. Without the flag it is the yard's crew member
of the same name, else whoever took it at the last import, else a question.
The pack sets three things and nothing
else (identity, `env_file`, args, model, directory and rotation are the
yard's):

| pack | crew member |
|---|---|
| `role` | its `prompt`, the copied file |
| `hears.events` | its `--on` kinds: the list is replaced, so a pack carries every kind its crew member should hear. `attention` is the class of kinds that need someone, as the yardr that runs has it ([subscriptions.md](subscriptions.md#the-class-attention)), and the starter pack names it in place of a list. A crew member with a list of kinds from an earlier import gets `attention` in its place at the next one, shown in the plan as a change of `on` |
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
| `the pack wants <source>, this yard lacks <hook\|credential>` | the yard has no hook for the source, or the crew member's `env_file` does not name its credential, read as the Sources table below reads them. The import asks nothing and grants nothing for it, by flag either, and records no answer: an import after the yard has both asks then. A crew member the import creates has no `env_file` yet, so those the yard has something of are all here the first time. One the yard has nothing of is not said here at all: the plan says it once, as optional, with the command that sets it up |
| `declined` | an earlier import was told no: not asked again; `--open-threads` still grants it |
| `grants`, `declines` | in the plan `pack apply` prints: what `--open-threads` answered |

The import only adds to the crew member's `open_threads`. A pack without the
key, a no, and a source nobody was asked about leave the setting as the
yard has it.

**Exposing a crew member to peers.** `expose_to_peers: true` is a key the
pack does not set either: it asks. Whom of a yard another yard may see and
write to is the operator's to say, per link
([peers.md](peers.md#the-roster-whom-a-peer-may-see-and-write-to)), and a
pack knows neither the yard's peers nor its people. So the plan sorts the
peers the yard has, per crew member that asks, and the import asks the
operator about what is left, a peer at a time:

```
an import would ask
  the operator: expose boss to north, south (each as yardr peer allow <peer> --expose boss)? [no]

expose to peers, for crew member boss
  has       east: it is exposed there already, and nothing is asked
  asks      north, south: the operator is asked, once per peer, and the answer without one is no
  declined  west: an earlier import was told no (yardr peer allow <peer> --expose boss exposes it)
```

- In a terminal the import asks last, after the confirmation:
  `Expose boss to north?`, then `Expose boss to south?`. The default is no.
  A yes is what `yardr peer allow north --expose boss` is: the peer is shown
  the crew member, and its mail reaches it where the link takes mail. It is
  logged as `peer_allowed`, as any.
- Without a terminal, with `--json` or with `--yes`, nothing is asked,
  nobody is exposed and no answer is recorded; the rest is imported, and the
  output names the command. There is no flag for it: `yardr peer allow
  <peer> --expose <name>` is the one.
- Only the operator exposes. Run by anyone else the import does everything
  else, exposes nobody, records no answer, and says so; the operator's next
  apply is asked.
- Either answer is recorded, by the peer and not by the name the yard has
  for it, so it is asked once: a renamed peer is not asked about again, and
  another yard added under a name that was told no is. A yes taken back
  (`--no-expose`) is asked again by the next import. A peer linked later is
  asked about by the next import, and a yard with no peer is asked nothing
  (`this yard is linked with no other: nothing is asked`).
- The import only exposes. A pack without the key, or one that drops it,
  leaves every link as it is.

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
| `hears.task.review: "label:in review"` (GitHub) | the map line `task:review=label:in review` |

The plan says, per source, what it would do and what the yard has for it:

```
stage maps
  create           hook linear as boss

stage map of source linear, as crew member boss
  the pack says task:merged=done task:new=started task:review-security=review task:review=review
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
| `skipped: …` | the pack's lines do not fit the source the yard has: a status it has none of, or a key its events carry none of. The import goes through without them; the next one sets them once they fit |
| `optional <source>`, under `sources the import goes through without` | the yard has no source of the name and no hook of it: no map section is printed for it, and this line carries the command that sets it up |
| `not set on hook <name>: …` | in the plan `pack apply` prints: this run may not have a hook act as that crew member ([below](#yardr-pack-apply)) |

| need | what is looked at |
|---|---|
| `hook` | a hook with that source. **The import never makes one**: it has no secret to give it. With several hooks of one source the map goes on the first by name, and a note says so |
| `identity` | the crew member's identity in the source. A hook acts only as a crew member that has one, which keeps the source's delivery of the change out of that crew member's inbox; a crew member the import itself creates has none yet |
| `set state` | whether the crew member's definition lets it set a status in the source (`set_state`, [crew.md](crew.md)). **The import never grants it**: with the map set and the grant missing, no issue is moved, and the line names the command the operator runs |

A pack names a source it does not carry: `hears`, `says` and `makes` are
read for any name, and the source itself is the yard's to have, from an
integration pack ([An integration pack](#an-integration-pack-kind-source)).
With a source of the name in the yard, the lines are set
as above, `hears` held to the statuses it has and `where` to the keys its
events carry; without one the source is said once, as optional, with the
command that sets it up, and the next apply sets the lines once it is
there. `crew.<name>.subscribe` and `open_threads` follow the same rule: no
subscription is made and nothing is asked about a source the yard has
nothing to hear or open with, and neither the Sources table nor the
`lacks` lines name it.

**Declared sources.** `sources.<name>.declaration` names a file of the pack
that declares a service outside the yard: its address, the credential it
takes and the requests the yard may make of it (`yardr source --help`;
[pushover.md](pushover.md) has a whole one). The import adds it to the
yard's declared sources under the pack's name for it, as `yardr source add
<file>` would:

```yaml
sources:
  pushover:
    declaration: sources/pushover.yaml
```

```
declared sources
  create           pushover

declared source pushover, from sources/pushover.yaml
  its calls go to https://api.pushover.net/1: message (update)
  would be added to the yard's sources
  missing  env file     no env_file is set for it, and a pack neither carries nor names one: yardr source key pushover gives PUSHOVER_TOKEN, or yardr source set pushover env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> "<question>" --secret pushover --context "<where to get it>"
  missing  notify       it can tell a user outside the yard and no user names it. The import names none: yardr user key <user> pushover gives their PUSHOVER_USER, then yardr user set <user> notify=pushover
```

- **The file is read as `yardr source add` reads it**, and everything that
  refuses there makes the pack invalid, named by the declaration's file,
  line and key. The name in the file is the pack's key.
- **A declaration in a pack for a depot has the keys `name`, `about`,
  `base`, `credential`, `check`, `idempotency`, `calls` and `notify`, and
  no other.** A key a later yardr reads in a declaration is not thereby one
  a pack may bring: a pack for a depot brings no command the yard runs. A
  pack of kind `source` may also bring `hook`, `thread` and `link`
  ([An integration pack](#an-integration-pack-kind-source)); `limits` stays
  refused in every pack: what a subscription still carries is the yard's to
  ask, not a pack's to say.
- **The commands' files are the pack's to carry.** In a pack of kind
  `source`, each file a command names by a relative program stands beside
  the declaration, and the import copies it under
  `<home>/sources/<name>/`, as `yardr source add` copies it. The plan names
  each command, its file and the file's sha256, and a second apply shows a
  changed file as a change (`file linear/thread: sha256 … -> sha256 …`). A
  command with an absolute program is refused: every program a pack runs
  with a credential is one the yard copies and hashes, so the operator's
  own absolute program stays a loose declaration's. A prune removes the
  files with the source.
- **A declaration the yard has under that name is replaced**, and keeps its
  `env_file`. The object's lines show what changes: a `base`, the
  credential's name (and the one of its updates, `update_env`) or header,
  the `idempotency` header, each call that is new, gone or different
  (`call message: (update) POST /messages.json -> (update) POST /messages`).
  Read them: a source is where a credential of the yard is sent, and a new
  `base` sends it somewhere else.
- **A pack carries no secret and names no file of the yard.** Where a call
  of the source is granted, the source's credential is still to be set
  after the import, and no user is told through it until one names it;
  `needs` gives both commands, and `ok` with the file or the users once
  they are there. `--source-env <source>=<file>` and `--notify
  <user>=<source>[,<envfile>]` set both as the import runs, as `yardr
  source set` and `yardr user set` do, and are refused as there; both are
  repeatable, the last wins for one source or user, and neither is asked
  for. Setting a source's file is the operator's, and telling a user
  theirs or the operator's: run by anyone else the import does the rest
  and sets neither. Where no call is granted and a crew member holds the
  key, the plan says nothing of the source's file: the server makes no
  call of it, and only `yardr source check <name>` reads the source's own
  file.
- **Adding a source is the operator's**, as with `yardr source add`. Run by
  anyone else the import does the rest, adds no source, says so, and
  records none; the operator's next apply adds it.
- **A new read call reaches whoever holds `read`.** A grant of `read` covers
  the read calls a later file adds
  ([integrations.md](integrations.md#a-call-by-a-session-or-a-crew-member)),
  so a declaration that replaces the yard's and adds or changes a read call
  widens what those grantees reach, with nobody asked. The plan says who and
  which calls, as `yardr source add` does:
  `through their grant of read, group web-builders would then call: orders
  (nobody is asked: read covers the read calls a later file adds; yardr
  source revoke crm read takes it back)`. In the plan as data it is the
  declared source's `reaches`: `calls` and `who`.

**Calls of a declared source.** `calls` under a group or a crew member is,
like `open_threads`, a key the pack does not set: it asks. Which calls a
group's sessions or a crew member may make of a source is the operator's to
grant (`yardr source grant`), so the plan sorts what the pack names, per
group or crew member and source, and the import asks the operator about
what is left:

```yaml
groups:
  builders: { agent: roles/builder.md, calls: { crm: [read] } }
  support:  { run: scripts/note, calls: { crm: [note] } }
crew:
  yardmaster:
    role: roles/yardmaster.md
    calls: { crm: [customer, note] }
sources:
  crm:
    declaration: sources/crm.yaml
```

```
calls of source crm, for group web-builders
  the pack asks for read
  asks      read: the operator is asked, and the answer without one is no

calls of source crm, for group web-support
  the pack asks for note (update)
  lacks     note (update): nothing is asked or granted
    missing  env file     no env_file is set for it, and a pack neither carries nor names one: yardr source key crm gives CRM_TOKEN and CRM_UPDATE_TOKEN, or yardr source set crm env_file=<file> names a file the operator keeps themselves; an agent asks from its pane: yardr bead ask <bead> "<question>" --secret crm --context "<where to get it>", then apply the pack again
```

| the plan says | meaning |
|---|---|
| `has` | the group or crew member is granted it already, by that word or, for a read call named, through `read`: no question |
| `asks` | the import asks the operator |
| `lacks` | nothing is asked or granted, by flag either, and no answer is recorded; the lines under it say why, each `missing` with the command. `env file`: the source declares a credential and the yard has no file set that it is read from, or the file does not set the name: nobody can make a call yet, so there is nothing to judge. `source`: the pack asks for calls of a source it does not declare and the yard has none of that name. `call`: the yard's source of that name has no such call. An import after the yard has what is missing asks then |
| `declined` | an earlier import was told no: not asked again; `--calls` still grants it |
| `grants`, `declines` | in the plan `pack apply` prints: what `--calls` answered |

A pack imported into a yard that has not the source yet therefore asks
nothing the first time: the import adds the source, the operator sets its
`env_file` (`yardr source set`), and the next `pack apply` asks. A pack may
ask for calls of a source it does not declare, one the yard has from another
pack or from `yardr source add`; the calls of a source yardr has built in
are not declared and cannot be asked for. The import only adds grants: a
pack without the key, a no, and a call nobody was asked about leave the
yard's grants as they are, and so does dropping the key from the pack
(`yardr source revoke` takes a grant back).

**Sources.** For each source a crew member subscribes to, the plan says what
the yard has and lacks to hear it and answer in it. A source the yard has
nothing of is not said here at all: the plan says it once, as optional,
with the command that sets it up. Nor are `yardr` and `peer`, yardr's own
sources, which no hook receives and nobody answers in:

| need | what is looked at |
|---|---|
| `hook` | a hook with that source (`yardr hook add`) |
| `secret file` | the hook's secret file exists |
| `identity` | the crew member's identity in the source (`yardr crew identity`) |
| `credential` | the names of the reply credential are set in the crew member's `env_file`: `LINEAR_API_KEY`; for GitHub a GitHub App's pair (`GITHUB_APP_ID` and `GITHUB_APP_PRIVATE_KEY_FILE`) or `GITHUB_TOKEN`. Only the names are read ([crew.md](crew.md#as-a-github-app)) |
| `listener` | what the yard's running server takes webhooks on, as it wrote it when it started: `ok` with the address and whether it is the `hook_addr` setting's or `YARDR_HOOK_ADDR`'s, `missing` when the server has no address or could not listen on its own (with the setting to change). `unknown` only when no server has said: a build from before `hook_addr` |

## yardr pack apply

```sh
yardr pack apply <pack> [--depot <depot>] [--yes] [--sha256 <digest>] [--prune]
    [--bind <group>=<kind>[:<model>][,<preset>]]… [--members <group>=a,b]…
    [--crew <pack's>=<yard's>]… [--group <pack's>=<yard's>]…
    [--open-threads <pack's>=<source>,…|none]…
    [--calls <group>|crew:<pack's>=<source>:<call>,…|none]…
    [--flow <flow>] [--depot-roles] [--json]
```

A pack of kind `source` is applied without `--depot`, which is refused for
it, into the yard itself ([An integration
pack](#an-integration-pack-kind-source)); any other with `--depot`, into
the depot.

It makes the plan `pack check` shows, prints it with what the flags answer
filled in, asks for what the pack cannot say, and sets the yard to the
pack's state. An object the yard has
under the same name is overwritten in what the pack carries and keeps the
rest; nothing is added a second time. With `--sha256 <digest>` it first
refuses, writing nothing, any pack but the one with that digest
([Pinning a pack](#naming-a-pack)); a script that applies a pack from a URL with
`--yes` gives the digest it read in `pack check`.

**What it asks.** In a terminal it asks; each question has a flag that
answers it instead:

| question | flag |
|---|---|
| whether a group the yard has takes a group of the pack the yard lacks, when it has one of that kind | `--group <pack's>=<yard's>`; see below for what happens without it |
| the agent and model of a group the yard lacks, picked from the list the installed agent gives, as in the group form | `--bind <group>=<kind>[:<model>]`; no model is the agent's own default |
| what that agent may do without asking, picked from the permission presets the group form shows | `,<preset>` at the end of its `--bind`; see below for what it gets without one |
| the members of a people group the yard lacks | `--members <group>=a,b`; `--members <group>=` for anyone |
| which crew member of the yard takes a crew member of the pack | `--crew <pack's>=<yard's>` |
| the agent, model and permissions of a crew member the yard lacks | `--bind crew:<pack's>=<kind>[:<model>][,<preset>]` |
| which flow the depot follows, when the pack has several without a type | `--flow <name>` |
| whether a crew member may open threads in the sources the pack asks for | `--open-threads <pack's>=linear,github`, or `=none`; see below for what happens without it |
| whether a group's sessions or a crew member may make the calls of a declared source the pack asks for | `--calls <group>=<source>:read,<call>`, `--calls crew:<pack's>=<source>:<call>`, or `<source>:none`; see below for what happens without it |

`<group>` is the pack's name for the group (`builders`, not
`web-builders`). A flag for a group or crew member the yard already has
answers nothing, since its binding is the yard's and is kept; that is not
an error, so the same command can be run again. A crew member the import
creates works in the yard's home, as one made by `yardr crew add` does.

Two flags are never asked for, in a terminal or without one: `--source-env
<source>=<file>` and `--notify <user>=<source>[,<envfile>]`, which set a
declared source's file and the user told through it as the import runs
([Declared sources](#how-a-pack-maps-to-the-yard)). Nothing is set but
what they give; without them the plan and the output give the `source set`
and `user set` commands that are owed.

**One answer to `--crew` per yard.** The filled copies of a pack's
roles and stage files are installed once per pack
(`<home>/packs/<pack>/roles/…`, `<home>/flows/<pack>.<flow>/…`), and every
depot that imports the pack reads those shared files ([above](#how-a-pack-maps-to-the-yard)).
A second depot importing the pack with another `--crew <pack's>=<yard's>`
rewrites them for the first depot too, so its agents read the second depot's
crew member in their roles and stage files. Import a pack with the same
crew members in every depot of a yard.

Without a terminal, or with `--json`, nothing is asked: a question without
its flag is an error that names every missing flag at once, one to a line,
before anything else is printed, never a default, with three
exceptions: the permission arguments and whether a crew member may open
threads, both below, and which group of the yard takes a new group. That
one is asked in a terminal alone, and not with `--yes` or of a group a
`--bind` or `--members` names: without `--group` the group is the depot's
own ([Groups](#how-a-pack-maps-to-the-yard)), so a command that set a depot
up before the question does so still. `--yes` applies without the
confirmation, and without a terminal it is required whenever there is
something to write.

Both refusals end with exit status 2 and have written nothing: the command
had something to ask and no terminal to ask it in, and the same command with
what it named goes through. Any other error is 1, or 3 for a depot or pack
that is not there. With `--json` the missing flags are a list,
`{"error": "depot init cannot ask without a terminal", "missing": ["--bind
builders=<kind>[:<model>]", "--members backlog=<users>"]}`: each is
`--<flag> <name>=<what is wanted>`, so a caller keeps the flag and the name
and puts the answer after the `=`
([setup.md](setup.md#for-a-program-that-drives-the-first-run)).

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
  `accept-edits`, `bypass` and `read-only`, codex `approve-for-me`, `bypass`
  and `read-only`; `none` is no arguments, for any kind. `read-only` is the
  agent's own sandbox, for a group that reads what a stranger wrote
  ([The contributors pack](#the-contributors-pack)). `--bind builders=claude:opus,bypass`,
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

**A group that binds as another asks nothing.** Where a group of the pack
says `binds_as: <group>`, the import gives a new one the agent kind, model and
arguments that other group has: those the flags and answers bind it with when
the import makes it too, or those the group of the yard that is or takes it
has now (`--group` included). So a pack can add a group to a flow, as the
starter added `security-reviewers`, without every earlier import command
gaining a flag for it, or a terminal a form. `--bind <group>=…` names the group
and binds it on its own instead, and the plan then asks about it as about any
new agent group. Under "groups" the plan says `agent, model and arguments as
group <name>`. Only a new group is bound so: one the yard has keeps what it
has, and where the group it binds as is no agent group in the yard (a people
group of that name, say) the new group is asked about as before.

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

**Calls of a declared source are asked of the operator**, under the rules
of opening threads above. For each group or crew member with a call under
`asks` in the plan:

- In a terminal the import asks once per group or crew member and source,
  after its other questions and the confirmation: `Let group web-builders
  call read of the source crm?`, an update marked as one (`note (update)`).
  The default is no, and the one answer is for every call in the question.
- Without a terminal, with `--json` or with `--yes`, nothing is asked and
  the flag answers: `--calls builders=crm:read` grants the pack's `builders`
  that and says no to the other calls of `crm` in its question;
  `--calls crew:yardmaster=crm:customer` is the same for a crew member;
  `--calls support=crm:none` says no to all. The flag is repeated per group
  or crew member and source. Without it nothing is granted, the rest is
  imported, and the output names the flag.
- The flag names calls the pack asks for; any other is an error. One that
  is granted already answers nothing. One under `lacks` grants nothing, and
  the output says what is missing.
- Only the operator grants. The store refuses a grant from anyone else and
  from the pane of an agent whatever `--actor` says (`yardr source grant`);
  the import checks first, so run by anyone else it does everything else,
  grants nothing whatever the flag says, records no answer, and says who
  may grant it.
- A yes is a grant as `yardr source grant` gives it, logged as
  `source_granted` and shown by `yardr source show <source>` (`may be called
  by group web-builders (through read)`), `yardr group list` and `yardr crew
  show`. A session started afterwards has the call in its brief; one that
  runs already may make it at once.

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

**What a source's events say.** A source's `says` goes on the same hook, as
`yardr hook add --says` would put it ([links.md](links.md#the-sources-events-move-the-bead)),
in the pack's order. The outcome is reported as the hook's `as` crew
member when it has one, so a table whose outcomes cross a stage of people
wants the pack's `as`; a source with `says` and no `as` asks for no
identity and no `set_state`, and anyone who may apply the pack sets it.
What the hook has besides (the crew member it acts as, its map) stays
unless the pack carries those too, and then it is replaced whole.

The table is the hook's, and every pack that carries the source sets its
lines in it: the starter's for the pull request of a train, the
contributors' for the pull requests and issues from outside. So an import
sets the pack's lines among the hook's and replaces none of the others:

- It takes off the lines the pack set at its last import into the depot and
  no longer gives, and no other. The plan names each as a drop; nothing
  asks for `--yes`.
- The pack's lines the hook has stay in the places they stand, put in the
  pack's order, since the first line an event matches counts. A line the
  pack gains goes before the pack line that follows it, where the pack wrote
  it, and one that comes after its last line the hook has goes last, after
  the rest.
- Every other line stays where it is: another pack's, found by the
  records of their imports, and the operator's, set by hand with `yardr hook
  add`. The plan never offers one as a drop. A line that another pack's last
  import lists as well stays when this pack lets go of it.

`yardr pack check` prints each line it would add and each of the pack's own
it would drop. Two packs on one hook tell their lines apart by the type of
the bead, which a line names with `type` (`type=train` in the starter's,
`type=pr` and `type=issue` in the contributors'): a pull request closed
unmerged asks a person about the train that tracks it and drops a
contributor's pull request, and no event meets the lines of both. Without a
hook of the source the import sets nothing for it and names the command, as
for a map. A pack that drops the block has its own lines removed by
`--prune`, those the hook still has, while the crew member and the map stay
for as long as another pack acts on the hook as one.

**What a source's events make.** A source's `makes` goes on that hook too,
as `yardr hook add --makes` would put it
([integrations.md](integrations.md#github-where-it-differs)), in the pack's
order: `- { where: "event=issues action=opened", bead: { type: issue, stage:
triage } }` makes an issue bead in `triage` for an issue someone opens in
the depot's repository, which tracks it, so that the lines of `says` find it
afterwards. Like `says` it needs the hook alone, is set among the lines the hook
has (the pack's own from its last import that it no longer gives are taken
off, the others stay, and a plan shows what it would add and drop), and its
own lines are removed by `--prune` once the pack drops it. A line that names
no `depot` makes the bead in the depot whose `origin` remote is the event's
repository; a pack that is imported into several depots of one repository
must name it.

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
  groups     assembly -> web-assembly, backlog -> backlog, builders -> web-builders, decisions -> decisions, reviewers -> web-reviewers, security-reviewers -> web-security-reviewers
  crew       yardmaster -> boss
  objects    11 files, 6 groups, 2 flows, 2 flow pointers, 15 routes, 3 subscriptions
```

| field (`--json`) | meaning |
|---|---|
| `pack`, `depot` | what was imported into which depot; `depot` is absent for a pack of kind `source`, which `yardr pack list` shows as imported into the yard |
| `source` | the directory it was read from; for a pack in the binary, `embedded in yardr <build>`, the build as `yardr version` names it |
| `commit`, `dirty` | that directory's git commit when it is in a repository, and whether it held changes the commit does not; absent for an embedded pack, whose source names the build |
| `at`, `by` | when, and the actor who applied it |
| `flow` | the pack's flow the depot follows |
| `setup` | `true` when the pack carried setup notes; absent when it had none. A crew member's prime names them as `yardr pack show <pack>` |
| `depot_roles` | `true` when the import was given `--depot-roles`; absent when the pack's roles were set |
| `groups`, `crew` | pack name -> yard name |
| `objects` | the files, groups, flows, pointers, routes, subscriptions, stage maps and declared sources that are the pack's; a file with the `sha256` of its content as installed, placeholders filled; a stage map (`kind: map`) with the hook's `name`, the `crew` it acts as and the `map` as the import set it; a declared source (`kind: source`) with its `name` and the `sha256` of the declaration as the import added it |
| `open_threads` | the answers about opening threads: yard's crew member -> source -> `true` or `false`; absent when nothing was answered. `yardr pack list` prints them as `may open   boss: github no, linear yes` |
| `expose_to_peers` | the answers about exposing a crew member to peers: yard's crew member -> the peer's public key -> `true` or `false`; absent when nothing was answered. `yardr pack check` says them by the peer's name, as `has` and `declined` |
| `calls` | the answers about the calls of declared sources: the yard's group or crew member (`group web-builders`, `crew member boss`) -> `<source>:<read or a call's name>` -> `true` or `false`; absent when nothing was answered. `yardr pack list` prints them as `may call   group web-builders: crm:read yes`. A yes whose grant was revoked since is forgotten, so the next import asks |

An import that changes nothing leaves the record as it is, its commit and
time included: it is the record of the last import that did something. The
import is logged as `pack_applied` when the record was written and
`pack_unchanged` when not, with pack, depot, source and commit. Removing a depot
removes its records.

A crew member is in the mapping and not among the objects: it is the yard's.
So is a group of the yard that took a group of the pack (`--group`): the
next import takes it again by that, and a prune never removes it.

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
- A declared source the pack no longer has under `sources` shows as `no
  longer in it` while the yard has the declaration the import added;
  `--prune` removes the source with its commands' files, as `yardr source
  rm` does (the file its credential is in stays). It is
  **held** while a user is told through it (`yardr user set <user> notify=`
  ends that) or another import names it. One whose declaration was added by
  hand since (`yardr source add`) is the yard's, and is neither listed nor
  pruned.
- A file is removed only under `<home>/packs/<pack>/`, or as a stage file
  of one of the pack's flows, `<home>/flows/<pack>.<flow>/<stage>.md`. A
  dropped stage file is named by that path under the home.
- A flow's stage files stay while the flow does: with a flow that is held,
  and with one the store refused to remove.
- The stage file of a stage the flow lost is named after no stage of the
  flow any more, which `yardr flow show <depot>` warns of as a stray file in
  the yard's home ([flows.md](flows.md#stage-files-what-a-stage-is-for)) until
  it is pruned.

Pruning happens after the import, before the record. A removal the store
refuses (a flow an open bead still pins) does not undo the rest: what could
be removed is, the record keeps the object so the next `--prune` tries
again, and the command exits 1 naming it.

## yardr pack export

`yardr pack export --depot <depot> [--name <pack>] <dir>` is the import the
other way round: it writes the way of working of one depot to `<dir>` as a
pack, to share or to import into another yard. `<dir>` must be new or
empty. Nothing in the yard is changed and nothing is asked. `--name` is what
the pack calls itself, the depot's name without it.

It writes `pack.yaml` with the depot's kind, the flows that govern the depot
(its own and each bead type's, however the depot came by them), the stage
file of each stage under `flows/`, the groups that work the stages, each
agent group's role under `roles/` and each script group's script under
`scripts/`. An outcome with a limit on rounds is written as the mapping
`{ to, max, then }` and any other as its stage alone. A group gets the yard's
name less the depot's prefix (`web-builders` is `builders`), which an import
puts back. A group the depot's import bound as another is written with
`binds_as` again while the yard still runs both with the same agent kind,
model and arguments; one the depot changed since, or bound as a group the
pack does not carry, is written as its own agent group and said under
`left out`.

In the role and stage files the operator's name becomes `{{operator}}`
again wherever it stands as a whole word. The command prints each such line:
a word that only equals the name is rewritten too.

It never writes a bead, a note, a message or an inbox, a setting, a file of
the depot other than its flows and roles under `.yardr/`, the depot's test
command, an agent product, a model, an argument, an environment file or a
secret, or the members of a people group. This version writes no crew
member, source definition or hook. A group's calls of a source are written
as requests only; the permission grant itself is never exported.

What the depot has and the pack does not is printed under `left out`, each
with why:

- A group's agent, model and arguments, a people group's members, and every
  other setting of a group. An import asks for the first four. Calls a group
  was granted are requests only, which an import asks about again when its
  yard has the source and a credential for it.
- A stage whose beads are not worked by one plain route in the depot's
  effective routes (its own, plus every yard default no own route of the
  same key sets aside; depot and stage, no condition, no further group).
  Of the routes that match a stage, the first in the order dispatch reads
  them is what works it. It is followed when it is plain, whether it is the
  depot's own or a yard default the pack follows. A stage whose winner has
  a condition or further groups is written with no group. So is a stage
  where a label, bead priority or type filter lets a route win for only
  some of its beads before a route that matches the whole flow, or where
  no such whole-flow route exists. Each route that is not followed is
  listed with its groups, and a group only such routes lead to is not
  written.
- A crew member's name in a role or stage file, by file and line. The name
  stays as plain text, and the pack brings no crew member of that name.
- The variables before and the arguments after a script in a script group's
  command, and the depot's rules file (`.yardr/depot.md`). The pack has an
  `env` for a script's options, but an export writes none: they are this
  yard's choices (where it pushes, how it lands), and a pack that carried
  them would make them the default of every yard that imports it, which an
  import of a group the yard has would not restore anyway.

It refuses, and writes nothing, for a group on one of the depot's routes
that another yard works (runner `peer`), two groups or two flows that would
get one name in the pack, a script group whose command does not start with
a script's full path, an agent group whose prompt is missing or cannot be
read, and a role or stage file that already holds a `{{placeholder}}`.

What was written is read back as `yardr pack check` reads a pack. Read the
role and stage files through before you share the pack: they are copied as
the yard has them.

## yardr depot init

`yardr depot init <depot> [--pack <pack>]` is the import above for a depot
that is new to the yard, with the one thing a pack cannot carry: what tests
the depot. It sets the depot's test command in the store, guessed from the
repository, when a git depot has none and the checkout says how it is
tested ([configuration.md](configuration.md#seeding-a-depot-yardr-depot-init)),
then imports the pack as `yardr pack apply <pack>
--depot <depot>` does, with that command's plan, questions and flags
(`--group`, `--bind`, `--members`, `--crew`, `--flow`, `--open-threads`,
`--calls`, `--depot-roles`, `--yes`; not `--prune`). The test command is part of what
is confirmed: nothing is set before a yes in the terminal or `--yes`. It
writes no role, flow, stage file or script into the depot.

A dir depot is set up the same way and gets no guess: nothing is written
into the directory.
A pack declares the kind of depot it is for (`kind: git` or `kind: dir` in
its pack.yaml), and `depot init` refuses one for a depot of the other kind
before anything is shown or written. The packs in the binary each
declare theirs, and for `starter` and `dir` the refusal names the other,
which fits: `starter` on a dir depot (its flow lands a bead on a base branch
through an assembly, and nothing merges in a directory), and `dir` on a git
depot (its roles commit nothing and its flow closes a bead at review). For a
pack of your own it says which kind the pack declares. A pack without the
key fits both kinds and is yours to judge. A pack of kind `source` is
refused for any depot: it is imported into the yard, not into one (`yardr
pack apply <pack>`). `yardr pack apply` does not check the kind.
[configuration.md](configuration.md#seeding-a-depot-yardr-depot-init) has
the details.

### The default pack

Without `--pack`, `depot init` takes the yard's default pack: the
`default_pack` setting, a pack's name or its directory as an absolute path.

```sh
yardr settings set default_pack ~/packs/research   # every depot set up from now on
yardr settings set default_pack ""                 # unset: yardr's own again
```

Unset, the default is one of the two packs in the binary, by the depot's
kind: `starter`, which declares `kind: git`, for a git depot, and `dir`,
which declares `kind: dir`, for a dir depot. A setting applies to both
kinds: a pack that declares a kind is refused for a depot of the other, so
a yard with both kinds of depot and `default_pack` set names the pack for
the other kind with `--pack`, or sets a pack without `kind`, which fits
both. Changing the setting moves no depot: it is
read when a depot is set up, and a depot stays on the pack it was set up
from until `yardr pack apply` or `depot init` is run on it.

`yardr init` reads the same setting for the one crew member a yard has
before its first depot. It takes the role of the default pack's crew member
called `yardmaster`, installs that file under `<home>/packs/<pack>/roles/`
filled as an import fills it, and makes it the yardmaster's prompt; the
first import of the pack finds the file as it would write it. A default pack
with no crew member of that name gets the yard no yardmaster. Init reads no
pack on a yard that has its yardmaster
([configuration.md](configuration.md#creating-a-yard-the-onboarding-and-yardr-init)).

### Two packs in one yard

A depot's groups, routes and flow are its own, named for it
(`web-builders`, `notes-builders`), so a second depot set up from another
pack leaves the first untouched: its groups go on reading
`<home>/packs/<first>/`. A yard that wants one set of agents for both names
the first depot's groups for the second (`--group builders=web-builders`):
they take the second pack's groups as they are, with the first pack's roles
([Groups](#how-a-pack-maps-to-the-yard)). A crew member is the yard's, and there is one
yardmaster. When both packs have a crew member `yardmaster`, each import
sets the yardmaster's prompt to its own pack's role, so the last import
wins, and the plan shows it before anything is written:

```
$ yardr depot init notes --pack ~/packs/research
…
crew
  change           yardmaster
                     prompt: /Users/jo/.yardr/packs/starter/roles/yardmaster.md -> /Users/jo/.yardr/packs/research/roles/yardmaster.md
```

To keep the yardmaster on the first pack's role, the second pack names no
crew, or the import maps its member to another crew member of the yard
(`--crew yardmaster=<other>`). A running crew member keeps its session when
its role changes; the output names the restart.

## The contributors pack

[`packs/contributors`](../packs/contributors) is a public repository's
issues and pull requests as pack data. It has two flows, each for one type
of bead, and none for the depot, so it is imported into a depot that
already follows a pack for its own work:

```sh
yardr pack apply contributors --depot web \
  --bind pr-reviewers=codex,read-only \
  --members maintainers=jo --members outside=
```

| flow | stages |
|---|---|
| `issue` | `triage` (maintainers) -> `backlog` -> `new` -> `review` -> `approved` -> `merged`, or `closed`: from `backlog` on, the starter's stages and groups |
| `pr` | `triage` (maintainers) -> `review` (pr-reviewers) -> `ciwait` -> `contributor` (outside) -> `approved` (maintainers) -> `merged`, or `closed` |

What moves a bead through them is the pack's `sources.github`, in three
verbs. The stages, the label and who is trusted are the pack's lines;
yardr knows the verbs alone:

| verb | direction | in this pack |
|---|---|---|
| `makes` | source -> yard: an event makes a bead for a thread no open bead tracks | an issue opened makes an `issue` bead in `triage`; a pull request opened or marked ready a `pr` bead in `triage`, or in `review` when its author is an owner or a member of the repository (`association=`) or a bot (`sender=bot`: a kind, never a login, so every bot) |
| `says` | source -> bead: an event reports an outcome to the bead that tracks its thread | the label `accepted` says `accepted`; new commits on a pull request say `pushed` in `contributor`, and in `review` and `ciwait` `pushed` when a bot pushed and `changes` when a person did; a merge says `merged`, a close `dropped`, in `triage`, `review`, `ciwait`, `contributor` and `approved`. No event of GitHub's CI says anything: the pull request's CI is the yard's own |
| `hears` | bead -> source: a stage of the bead sets a status on the thread | an issue is closed when its bead reaches `merged` or `closed`, a pull request when its bead reaches `closed` |

- **Triage is the pack's advice, and no rule of yardr.** Its stage file
  says a maintainer checks whether the person and the code are legit
  before anything of a pull request is run anywhere. Which stage a bead
  starts in is a line of `makes`: a trusted author's pull request starts in
  `review`, and a copy of the pack may trust more authors, or all.
- **`merge-ok` is the reviewer's to give for one kind of pull request.** A
  bump of Go modules from a bot (title opening `build(deps)`, diff changing
  `go.mod` or `go.sum` and nothing else) gets the label from the reviewer
  when it reports `done`; it says the yard may merge the pull request from
  `approved`. For any other pull request it is a maintainer's to add.
- **`ciwait` is the yard's own CI, worked by the script group `pr-ci`.** A
  reviewed pull request waits there while `pr-ci` runs the depot's test
  command on its head, the one every landing runs; green is `done` to
  `approved` with the commit recorded as the bead's `ci_head`, red or a
  person's new commits meanwhile `changes` to `contributor`, a bot's new
  commits meanwhile `pushed` back to `review`. The reviewer runs nothing,
  which is why the wait is a stage of its own and not the end of `review`,
  and a fork's code runs on the yard's machine only after a reviewer has
  read it. The head tested is the one the reviewer read: it records the
  commit as the bead's `reviewed_head` before it reports `done`, and `pr-ci`
  holds a bead whose pull request has another head when it starts, or none
  recorded, instead of running unread code. The script works as the yard's
  GitHub App, whose id and key file are the group's `env_file`, so a yard
  makes the group first and maps it
  (`--group pr-ci=pr-ci`, [line.md](line.md#the-yard-as-the-ci-of-a-pull-request)).
- **A bot's push starts the read again.** A bot's rebase is the only push
  it makes, so a bead sent to `contributor` by it would wait there for one
  that does not come. A push by a bot (`sender=bot`) while the pull request
  is read or waits on CI says `pushed`, which leads from `review` and from
  `ciwait` back to `review`: from `review` the bead stays, as a new stay,
  and a new session reads the new head; from `ciwait` it goes back to be
  read, and waits for CI on the new head after. A person's push says
  `changes`, as before, and waits in `contributor` for the next commits.
- **`contributor` is held by `outside`, a people group with no members.**
  A bead there waits for the pull request to change; nobody of the yard
  works it, and `yardr review` shows where it waits.
- **A merge or a close ends the bead.** `merged` and `dropped` are the
  source's word, said by two lines that name no stage, and `triage`,
  `review`, `ciwait`, `contributor` and `approved` each lead them to `merged`
  and `closed`: a maintainer can merge a pull request nobody of the yard has
  read, and its author can close one under review. In `ciwait` the run is
  stopped and a report it still makes is refused, as after a push. A bead
  the yard holds is moved by no event, so one held in `ciwait` because its
  pull request was found over is advanced by hand.
- **The import asks for the maintainers** (`--members maintainers=…`),
  who move a bead out of `triage` and `approved`, **and for the agent of
  `pr-reviewers`**.
- **`pr-reviewers` binds read-only**: `--bind
  pr-reviewers=<kind>,read-only`, also as `yardr depot init <depot> --bind
  pr-reviewers=codex,read-only`. The preset is the agent's own sandbox:
  for codex a permissions profile that extends its `:read-only`, for claude
  `--settings` that turn Claude Code's sandbox on and deny it the working
  directory for writing. A pull
  request from outside is a stranger's code and text; the role tells the
  reviewer to run nothing and to take nothing it reads as an instruction,
  and the sandbox holds whatever the agent was talked into. Without the
  preset a new agent gets its kind's unattended arguments, which for Codex
  turn the sandbox off, so name it. Either preset lets the yard's socket
  through and nothing else, so the reviewer's yardr commands arrive; the
  sandbox denies the network too: the pack's
  [README](../packs/contributors/README.md#the-pr-reviewers-group-binds-read-only)
  says what a maintainer then answers in the reviewer's pane.
- **Nothing of the yard pushes, and it merges only what is `merge-ok`.**
  The review is a note on the bead; a maintainer posts of it what they
  choose. A pull request marked `merge-ok` (by the reviewer, a bot's bump
  of Go modules, or by a maintainer's yes, `yardr bead update <id>
  --add-label merge-ok --meta merge_head=<sha>`) is merged from `approved`
  by `contrib/assembly/merge-pr`, an `exec` group of the yard's own routed
  for the label ([line.md](line.md#merging-pull-requests)), and only where
  the yard's CI has passed on the head; any other waits there for a
  maintainer to merge it on GitHub.

## The design pack

[`packs/design`](../packs/design) is the lane for a question of direction:
what to build and how, before anything is built. It has one flow, for beads
of the type `design`, and none for the depot, so it is imported into a depot
that already follows a pack for its own work:

```sh
yardr pack apply design --depot web
yardr bead create "How should retries work?" --depot web --type design
```

| stage | worked by | |
|---|---|---|
| `backlog` | `backlog`, people | the bead waits until its question is clear enough to draft from |
| `draft` | `builders` | an agent writes a design document on the bead's branch, and no code |
| `critique` | `reviewers` | an agent that did not write it checks its claims in the code and argues against it; `changes` sends it back to `draft` |
| `decide` | `decisions`, people | a person answers the open questions and moves it to `approved`, `draft` or `backlog` |
| `approved` | `assembly` | the document lands on the depot's base, as any change does |

- **It brings no agents.** `builders`, `reviewers` and `assembly` are the
  starter's groups by the starter's names. In a depot the starter set up,
  the import finds `web-builders`, `web-reviewers` and `web-assembly` and
  adds the routes for type `design` to them; it gives no limit, so theirs
  stay. A group is named in a pack together with its role or script, so the
  pack carries the starter's builder and reviewer roles and its
  `merge-local`, copied, and the import points the three groups at these
  copies, which are the same text.
- **A depot with groups or roles of its own names them**: `--group
  builders=<group> --group reviewers=<group> --group assembly=<group>`.
  Those groups take the pack's as they are, and nothing of them is changed
  ([Groups](#how-a-pack-maps-to-the-yard)).
- **What a design asks of an agent is in the stage files**, not in a role:
  a builder in `draft` is shown what the document holds, a reviewer in
  `critique` what to check and when to stop sending it back.
- **It names the crew member `yardmaster` and carries no role for it**: the
  copied roles mail it. The yardmaster keeps its prompt, and hears what the
  starter has it hear.

## An integration pack (kind: source)

A pack of kind `source` is an integration: a service outside the yard that
yardr has no Go for, as one pack. It has a name, the kind, setup notes and
sources, each with a declaration and nothing else; it has no flows, groups
or crew, and is imported into the yard itself, with no `--depot`:

```yaml
# packs/linear/pack.yaml
name: linear
kind: source
setup: SETUP.md

sources:
  linear:
    declaration: linear.yaml
```

```sh
yardr pack check linear
yardr pack apply linear
yardr pack show linear
```

`pack show` prints the pack's setup notes later: Linear's say where its
webhook is made and which events it needs, what the crew member's identity
wants, and how to test the wiring. Telegram's and Pushover's say what to
create at the service, how each secret is given to the yard (`yardr source
key`, `yardr user key`, or a card a crew member asks with) and how to
test: each is one service with the `notify` key that tells a user, so one
apply with `--notify` and the two keys set it up whole, and `yardr user
list` shows the user told through it. The webhook pack's
setup notes say how to make one hook for each sender, how Zapier, Make and
n8n each post to it with the secret in a header, and where each service
shows the URL the yard sends to.

An integration pack may also suggest grants. `presets`, beside `sources`,
names each preset with an optional one line of `about` and the `grants`,
per source the pack declares, in the words `yardr source grant` takes:

```yaml
presets:
  read:
    about: Read mail, threads and drafts; write nothing
    grants:
      google-mail: [read]
  drafts:
    about: Read mail, and write drafts the person sends
    grants:
      google-mail: [read, draft]
```

`pack check` refuses a source the pack does not declare, a call the
declaration does not name, an empty preset, and presets on a pack of
another kind, each with the file and line; `pack show` prints them
numbered in file order, after the setup notes. A preset suggests and
grants nothing: the operator grants it with `yardr source grant
<source> --preset <name>`, in a terminal or as a run ask, and a terminal
`pack apply` asks no grants at import, as before. The four google packs
each carry three: read for the reads, one for the reads and some
updates, and everything for all the pack lists.

A new source pack starts from the template, `packs/template`: a skeleton
pack for a fictional service, with every key a declaration may bring,
command stubs showing each stdin and stdout shape, and setup notes. Copy
the directory, rename the service, and drop the keys the service does
not need; [integrations.md](integrations.md#adding-a-service) walks the
rest, with the envelope and check contracts. A test holds the skeleton
to the reader: it copies the template, renames it, and applies the copy
in a scratch yard, so the skeleton cannot drift from what the reader
takes.

Its declaration may name the commands the yard runs (`hook`, `thread`,
`link`), which a pack for a depot may not; `limits` stays refused, as
there. Each file a command names by a relative program stands beside the
declaration in the pack, and the import copies it under
`<home>/sources/<name>/`, as `yardr source add` copies it. The plan names
each command, its file and the file's sha256; a second apply shows a
changed file as a change. The source's own credential file is still to be
set where a call of it is granted (`yardr source set <name>
env_file=<file>`); where none is and a crew member holds the key, the plan
and the output say nothing of it: only `yardr source check <name>` reads
the source's own file. Adding the source is the operator's: run by anyone
else the import adds none. `yardr pack list` shows the import into the
yard.

**Proposing an embedded pack.** A crew member may propose applying a
pack yardr embeds, with a run ask (`yardr bead ask <id> "<question>"
--run "pack apply <name>"`, [chat.md](chat.md#questions-for-you)), for
the operator to approve on a card: an embedded pack of kind `source`
(the packs table above names them), or a pack of a depot kind
re-applied to the one depot that holds it where its plan asks nothing.
A card is read whole, so a pack whose card would run past 40 lines is
refused, and the operator applies it in a terminal. Google is split into
packs (`google-mail`, `google-drive`, `google-docs` and `google-calendar`)
so that each applies by one card, and so does the re-apply of
`google-docs` over an earlier import.
The card is written by the yard's server from its own binary: it shows
each declared source whole as a `source add` card does, what a source it
would replace changes to with each command file's diff, and for a depot
pack each object with each changed file's diff. The approval is held to
the build, the pack's bytes and the yard's sources as carded, and is
carried out by the server after it. A pack that is a directory, a URL or
an earlier import's record, any flag of `pack apply`, a first install of
a depot pack, and a plan that asks, is refused to a terminal, where
`yardr pack check <name>` shows the plan.

**A pack updated is one card again.** A setup ask on a pack already
applied (`yardr bead ask <id> "<question>" --setup <pack>`,
[chat.md](chat.md#setting-up-a-packs-sources)) is the same setup card
again, showing the difference: each source's declaration diff old against
new and each command file's old and new sha256 with its unified diff, as
the replacement row shows them; the account line reading given with its
file where that file holds every name the declaration needs, kept with a
bare preset pick, typed another to change it, or a candidate's file
taken; where the declaration newly needs a name the file does not set,
the card names the missing names and the answer types them; and each
preset rendered against what the grantee holds, calls held marked held
and calls the preset would add marked new, a new read call under a
standing read grant reading covered with no grant needed and a new update
call waiting on its preset. Picking a preset grants what it names past
what is held, and revokes nothing.

What a stage means in such a source is not the integration pack's to say:
that is the flow pack's, the one the depot follows. The seam between the
two packs is the source's name:

| the integration pack says | the flow pack says |
|---|---|
| how the yard works there: the declaration, the commands, their files | what each stage of its beads means there: `sources.<name>.hears` |
| nothing of the flow: no flows, groups or crew | nothing of the commands: what to hear, before the yard has the source, is skipped |

So the yard is set up in two imports: the integration pack first, then the
flow pack, whose lines are set once the yard has the source. In the other
order the flow pack's import goes through and says the source once, as
optional, with the command that sets it up, and the next apply sets the
lines. The same holds for `crew.<name>.subscribe` and `open_threads`:
nothing is heard or asked about a source the yard has nothing for, until
it has it.

## The plan as data

`pack check --json` prints the plan an import executes (`pack apply --json`
prints `{"plan": …, "answers": …, "open_threads": …, "calls": …, "expose": …, "maps_refused": …, "sources_refused": …, "result": …, "sets": …}`, the
answers by question id, an `args` answer being the arguments the new agent
got, space-separated; `open_threads` has `answers`, crew member -> source ->
yes or no as given by flag or form, and `notes`, what was not granted and
which flag or who would grant it; `result.granted` lists each crew member with
the sources it was let open threads in; `calls` has the same two for the
calls of declared sources, `answers` being grantee -> `<source>:<call>` ->
yes or no, and `result.calls` lists each `group` or `crew` with the `source`
and the `calls` it was granted; `expose` has `answers`, crew member -> peer
-> yes or no as the form gave them, and `notes`, who was not exposed and
which command or who would, and `result.exposed` lists each `crew` with the
`peers` it was exposed to; `maps_refused` is source -> why this
run set no stage map for it, and `result.mapped` the maps it set, each with
`hook`, `source`, `as` and `map`; `sources_refused` is declared source ->
why this run did not add it, and `result.declared` the names of those it
added or replaced; `sets` is what `--source-env` and `--notify` set for
the declared sources, `set` and `skip` each holding source -> the lines
said):

```json
{
  "pack": "starter", "dir": "/…/packs/starter", "digest": "9f2c…", "kind": "git", "depot": "yardr",
  "flow": "default", "depot_roles": false, "groups": {"builders": "yardr-builders"}, "crew": {"yardmaster": "boss"},
  "fill": {"operator": "Jo", "yardmaster": "boss"},
  "home": "/Users/me/.yardr",
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
  "maps": [{"source": "linear", "pack": "yardmaster", "as": "boss", "map": {"task:new": "started", "task:review": "review", "task:review-security": "review", "task:merged": "done"},
    "hook": "linear", "action": "create", "needs": [{"what": "set_state", "status": "missing", "detail": "…"}]}],
  "optional": [{"source": "github", "setup": "yardr hook add github --source github --secret-file <file>", "detail": "…"}],
  "notes": [], "refused": []
}
```

`kind` is the kind of depot the pack declares, left out for a pack that
declares none. `digest` is the pack's digest in hex, without the `sha256:`
that `pack check` prints before it. `install` is where roles and scripts are copied, `stage_files` the
directory under which each flow's stage files are
(`<stage_files>/<pack>.<flow>/<stage>.md`). `fill` is what the import puts
for each placeholder, by its name. `setup` and `setup_file` are the pack's
setup notes and the file of the pack they were read from, absent for a pack
without any.

Objects come in the order an import does them: files (roles and scripts,
then stage files), groups, flows,
pointers, routes, crew members, subscriptions, stage maps, declared sources; dropped objects come last.
`action` is `create`, `change`, `unchanged`, `not_in_pack`, `dropped` or
`takes`: a group of the yard that takes one of the pack's, which carries the
group as it is and stays, and under `keeps` what of the pack's is not set
on it.
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
| `map` | `hook`, `source`, `as`, `map`, `says` and `makes`: what the hook is set to. Only a map the import can set is an object; `maps` has every source |
| `source` | `name`, `from` (the declaration's file in the pack) and `declaration`, its text: what is added; `decl_dir` (the declaration's directory in the pack) and `commands`, each command's `key`, `program` and the `sha256` of the pack's file, when it brings command files. `declared` has, per source, `file`, `base`, `calls`, `commands`, `action` and `needs` |

An object's `asks` are the ids of the questions whose answers it still
needs, its `binds` the yard's group a new group that binds as another
(`binds_as`) takes its agent, model and arguments from, with no question of
its own, its `keeps` what the import leaves as the yard has it, its `env` the
assignments a new script group's `cmd` starts with (from the pack's `env`; a
group the yard has gets none, and `keeps` says what of it its `cmd` lacks),
and its `shadows` the depot's files the pack's role stands in front of. A file has
`edited: true` when its installed copy was edited by hand. `depot_roles` says
whose roles the plan sets. A dropped
object has `owned`, the record's entry for it, and `held` when not even a
prune removes it. The question `pack.flow` belongs to no object: it is
asked when the pack has several flows a depot could follow. A plan with
anything under `refused` must not be applied.

`maps` has one entry per source under the pack's `sources`: the `map` keyed
`<type>:<stage>` as a hook keeps it, `pack` and `as` (the crew member by the
pack's name and the yard's), `says` and `makes` (the pack's tables), `hook`
(absent when the yard has none), `action` (`create`, `change` with `was`,
the hook's `as`, `map`, `says` and `makes` before, `unchanged`, `not_set`
when the import sets nothing for want of a hook or an identity, or `skipped`
with `skipped`, why: the yard has no source of the name, or the lines do not
fit the one it has), `drops` (the hook's `says` and `makes` lines the import
takes off, each with `table` and `line`: the pack's own from its last import
that it no longer gives, and no other; the `map` of the plan's object and
`result.mapped` carry them as `drop_says` and `drop_makes`), and `needs`: `hook`,
`identity` and `set_state`, each `ok` or `missing` with the command in
`detail`.

`optional` names the sources the pack could use that the yard has nothing
of: no source of the name and no hook of it. Each has `source`, `setup`
(the command that sets it up: the pack apply that brings it, or the hook
add that receives it; absent when no command does) and `detail`, the
line's words. The text says each once, under `sources the import goes
through without`, instead of a missing need on every step that could use
it; the steps still carry their needs as data.

`open_threads` has one entry per crew member the pack asks it for. Each source
under `wants` is in exactly one of `has`, `lacks`, `declined` and `asks`;
`recorded` holds the last import's answers. What `asks` lists is not among
`questions`: without an answer it is no, not an error.

`expose` has one entry per crew member the pack asks it for
(`expose_to_peers`): `pack` (the pack's name for it), `crew` (the yard's),
and the yard's peers by name, each in exactly one of `has`, `declined` and
`asks`; `keys` is each peer's public key, which an answer is recorded by,
and `recorded` holds the last import's answers. What `asks` lists is not
among `questions`: without an answer it is no, not an error.

`calls` has one entry per group or crew member and declared source the pack
asks calls of: `pack` (the pack's name for it), `group` or `crew` (the
yard's), `source`, and `wants`, each of which is in exactly one of `has`,
`lacks`, `declined` and `asks`; `updates` are those of `wants` that are
update calls, `missing` why anything is under `lacks`, and `recorded` the
last import's answers.
