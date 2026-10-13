# yardr pack export: a yard's way of working written out as a pack

Design yardr-c0d0, proposed 2026-10-13. A proposal as it was decided on, not a description of what the code does today; the decision is on the bead.

A yard is one installation of yardr: its store, its crew and its depots. A depot is a git repository or a plain directory beads are worked in; a bead is one durable piece of work. A pack is a way of working as one directory: flows, groups, roles, crew members. A flow is the stages a bead passes through; a stage is one step of a flow; a route says which group works a stage; a group is who works it (a pool of agents, a script, people, or another yard); a runner is how a group's work starts (`herdr`, `exec`, `manual`, `peer`). The crew are the yard's always-on agents, tied to no bead. A hook is a webhook endpoint that turns an outside system's deliveries into events; a source is that outside system. The operator is the person who runs the yard. See docs/glossary.md for all of yardr's words.

## 1. The question

mckean, 13 Oct 2026: a teacher runs a yard that corrects essays, and mckean wants him to share his pack, perhaps for the website: "is there a built in export?" There is none: `yardr pack` has apply, check, list and show, which all read a pack into a yard, and nothing writes a yard's flows, stage files, roles and groups back out as a pack directory. The same wish came up the same day from the other side: copying a yard's basics to a second yard (design yardr-8cgx, merged, not built). The question is what `yardr pack export <depot|--yard> <dir>`, the inverse of "How a pack maps to the yard" in docs/packs.md, should write: what a person changed in their yard after `depot init` comes out as pack.yaml, flows and roles that `yardr pack check` accepts and `yardr pack apply` on a fresh yard turns into the same way of working, with nothing of the teacher's students in it.

## 2. What exists today

### 2.1. A pack, and the four verbs that read it

A pack is one directory: `pack.yaml` plus `roles/`, `scripts/` and `flows/` (docs/packs.md; the shipped example is `packs/starter/`). `pack.yaml` holds `name`, `kind` (`git`, `dir`, or `source` for an integration), `flows` (stages in file order, each with group, stage file, outcomes, manual moves, or `end: true`), `groups` (exactly one of `agent:`, `run:`, `people: true`, plus `limit`), `crew` (role, `hears`, `subscribe`, `open_threads`), and `sources` (stage maps, `says` and `makes` tables, declared sources). A pack names no machine thing: no agent product, model, argument, depot path, person, secret or identity, which are the yard's and stay in it. Role and stage files name the operator and the pack's crew through placeholders, `{{operator}}` and `{{<crew member>}}`, which the import fills; any other `{{…}}` is a pack error named by file and line.

`internal/pack` reads a pack (`pack.go`), plans an import without writing anything (`plan.go`, `Build`), and applies it (`apply.go`). `internal/cli/pack.go` serves the four verbs: `check` (validate, then print what an import would change), `apply` (ask what the pack cannot say, then set the yard to the pack's state), `show` (the pack's setup notes) and `list` (the imports on record). Nothing writes a pack. `yardr flow draw <pack>` reads a pack's flows with no yard at all, which is the closest the tree comes to treating a pack as output.

### 2.2. How a pack maps to the yard, which export must invert

Each half of the mapping (docs/packs.md "How a pack maps to the yard") is a line the export must read back:

- **Files.** Roles and scripts are copied to `<home>/packs/<pack>/…` and referred to by absolute path; roles with placeholders filled, scripts byte for byte. Stage files go to `<home>/flows/<pack>.<flow>/<stage>.md`.
- **Groups.** An agent or script group becomes the depot's own, `<depot>-<group>` (runner `herdr` with `prompt` and `limit` set, runner `exec` with the copied script in `cmd` and `limit` set); a people group keeps its name and is the yard's, shared by every depot. What the pack cannot say (agent kind, model, arguments, members) the import asks for. `--group <pack's>=<yard's>` lets a group of the yard take the pack's group as it is, recorded in the mapping, never as an object.
- **Flows.** A flow is stored as `<pack>.<flow>` and the depot is pointed at it (`yardr depot flow`); a pointer wins over the flow files in the depot's repository as a whole. Type flows become per-type pointers.
- **Routes.** Each stage with a group gets the one route `depot, stage -> group` (with the type, for a type's flow). The store's `Route` (internal/store/routing.go) can carry more: labels and bead priority, which an import leaves alone, and `When` conditions with `Else` groups, which it replaces together with the plain route (internal/pack/plan.go `routes()` matches every depot-scoped route that is `!ByBead()`). A yard-wide route, added without `--depot` and stored with no depot, is left alone too, and `MatchRoute` applies it to any depot's beads.
- **Crew.** A crew member of the pack is taken by one of the yard (`--crew`); the pack sets its `prompt`, its `--on` kinds (replaced whole), and one subscription per heard stage or done state. `open_threads`, `calls` and `expose_to_peers` are never set, only asked: the answers live in the import record and in the yard's settings and grants.
- **Hooks and sources.** Stage maps, `says` and `makes` go on the yard's hook of the source; the import makes no hook and grants nothing (`set_state` stays the operator's). Declared sources are added as `yardr source add` would add them, keeping their `env_file`; secrets and files of the yard are never in the declaration.
- **The record.** `store.PackImport` (internal/store/packs.go), written by `RecordPack`, holds per pack and depot the source directory and its git commit, the flow the depot follows, the group and crew mappings, every object with the sha256 of installed files as filled, and the answers. It is what `pack check` compares against, including "edited by hand since the last import".

### 2.3. The yard-side state an export reads

Everything the mapping writes is readable back: named flows and pointers (`Flows`, `FlowPointers`, `DepotFlows`, which resolves what governs a depot however it is governed); groups (`Groups`: runner, config, limit, members); routes (`Routes`); crew (`GetCrew`: prompt in config, `on`, subscriptions, identities, `open_threads` and `set_state` in config); hooks (`GetHook`: the crew member it acts as, map, `says`, `makes`); declared sources and their grants (`SourceGrants`); settings (`Settings`).

Two existing readers do most of the resolving export needs. `yardr depot sources <depot>` (internal/cli/depotsources.go) already answers, one line per object, where each of a depot's flows, stage files, roles and rules comes from (its repository, a pack's copy, the home, a file elsewhere, or nothing), the way a brief reads them, including what each shadows. `yardr depot flow`, `yardr group show`, `yardr crew show`, `yardr hook` and `yardr source show` print the rest. No new reader is needed for any of it; only the inverse of placeholder filling (section 3.3) has no existing mechanism.

### 2.4. What may not go in

The pack rule above already forbids persons, secrets, identities, models and arguments. Beyond it: no bead, note, mail, inbox, journal or depot file ever goes in (the teacher's yard holds students' essays); no user, device, peer or hook secret; no setting: settings live in the store's `settings` bucket today (internal/store/settings.go, the `KnownSettings` table), and no `settings:` key goes in packs. Design yardr-8cgx, merged but not yet built, decided "file only" as the accepted future direction: nine start-up values would move to `<home>/config.yaml` while the live knobs stay in the store. Export leaves that answer unchanged and carries no setting in any version. No depot test command (`depot init` guesses it again at setup); no depot rules file (packs provide none: `depotsources.go` says so outright). The import record's `at`, `by` and commit never enter the pack either, which is what makes a round trip comparable at all.

### 2.5. The isolated yard, where a round trip is tested

`contrib/isolated-yard/isolated-yard up` builds an empty yard in a directory with a stand-in agent, prints the proof it is isolated, and `down` stops it by pid and removes it. Export, apply and export again run there, never in the operator's yard.

## 3. The proposal

What follows is the whole-pack version for one depot. Open question 1 takes one depot or the whole yard; open question 2 takes the whole pack or the difference from the pack the yard came from. If both directions are as picked below, this is the version to build.

### 3.1. The command

```sh
yardr pack export --depot <depot> [--name <pack>] [--crew <yard's>=<pack's>]… <dir>
```

It reads the depot's way of working and writes a pack directory in the starter's layout (`pack.yaml`, `roles/`, `scripts/`, `flows/<flow>/*.md`), then validates what it wrote with the pack reader, the same errors `pack check` names, and prints the verdict. It writes nothing to the yard, asks nothing, and needs no `--yes`. The directory must not exist or must be empty, else the command refuses; a pack is hand-editable after it is written, and an export must never silently merge with hand edits.

`--name` defaults to the depot's name and must satisfy the pack name rule (letters, digits, `.`, `_`, `-`); `kind` is the depot's kind, since the pack was read from that kind of depot. `--crew` renames a crew member of the yard to its pack name (default: the same name) and adds one the automatic rule below would leave out.

A depot with no governing flow, or no depot of the name, is refused with the sentence the other verbs use. So is a pack name the rule refuses.

### 3.2. What goes in, part by part

**Flows.** Every flow the depot's pointers name: its own and each type's, as `DepotFlows` resolves them, however governed (a store pointer, the repository's `.flow` files, or the home's). Order, outcomes and manual moves come from the stored flow (`Stages` in order, `Outcomes`, and the transitions no outcome names); terminal stages become `end: true`. Each is written under a pack-local key, the inverse of `FlowName` (`<pack>.<flow>`, internal/pack/plan.go): the import record's `Flow` name when the depot's import of that pack points it at the governing store flow (the record keeps the pack's name for the flow, internal/pack/apply.go); else the reported name with a leading `<pack>.` removed when a pack imported into this depot recorded that store flow, which covers a type's flow through its pointer object; else the reported name whole, which the pack name rule already allows, dots included. Two flows resolving to one key refuse the export, naming both. On re-apply the import stores `<new pack>.<key>` and records the key, so the second export strips to the same key: the round trip holds, including for a pack-backed flow, which the round-trip fixture exercises (section 3.4).

**Stage files.** The effective file of each stage of each exported flow, read the way a brief reads it, copied to `flows/<flow>/<stage>.md` with placeholders restored (section 3.3). A stage with no file gets no `file` key. A prompt-style error (a file nobody has) refuses the export, as it is a yard to fix, not a pack to write.

**Routes.** Routes are followed, not carried: an import rebuilds them. A plain route is exactly one thing: depot and stage (and type), with no labels, no bead priority, and no `When` condition or `Else` groups (`!ByBead() && !Quotaed()` in internal/store/routing.go). The routes of a stage are its depot-scoped routes for its type together with the yard-wide routes matching its stage and type, since a yard-wide route routes this depot's beads today. A stage is routed to a group only when those together are exactly one plain route and it is depot-scoped. Every other case exports with no group, so its beads wait for a hand until the importer routes them: no route at all, several, a labelled or bead-priority route (which an import leaves alone), a quotaed route with a condition or further groups (which an import replaces: applying the pack where such a route exists deletes it, and the plan shows the change), or any yard-wide route, plain or not (which an import leaves alone: `routes()` matches depot-scoped routes only, so no pack form carries one). Every route for an exported stage that is not the one plain route, depot-scoped or yard-wide, is listed as left out with why, a yard-wide one with all the groups it names, never silently dropped; a group reached only through such routes is not exported either, and is listed with them. On a fresh yard no yard-wide route exists, so the round trip still holds.

**Groups.** For each group the exported routes lead to: an agent group becomes `{ agent: roles/<name>.md, limit }`; a script group becomes `{ run: scripts/<name>, limit }` with the script copied byte for byte; a people group becomes `{ people: true }`, with `limit` only when it is not 1. The pack name is the yard name minus the `<depot>-` prefix where it has one, else the yard name; two groups mapping to one pack name refuse the export, naming both.

- Left out of a group is every config key the pack cannot set (internal/store/configkeys.go), each reported per group in the output whenever it is set to a non-default value, so nothing omitted is silent: of an agent group, `kind`, `model`, `provider`, `args`, `env_file`, `trust`, `label` and `worktrees` (the importer answers `--bind` for kind, model and arguments); of a script group, `dir`, `log` and `worktrees`, plus the assignments before the script's command and the arguments after it (the output warns per script that the pack carries the script alone); of a people group, `members` (the importer answers `--members`), `notify` and `wait`; of any group, `idle`, `max_age`, `grace`, `stall`, `keep` and `cleanup`, which change session lifetime and what a closed bead leaves behind. `calls` stays in only as `calls:` asks (section 3.2, crew), since the import re-asks the operator.
- The script is the first command word, past any `VAR=…` assignments, that names an existing regular file; where none does, the export refuses and names the command. Hand-made commands that build their script at run time cannot be carried.
- A group whose runner is `peer` refuses the export: it names another yard, and no pack form holds it. Skipping it would write a stage that works nothing and say nothing.

**Roles.** Each exported agent group and crew member's prompt, resolved as `depot sources` resolves it (a pack's copy, the home's role, the depot's repository file at its base, or a file elsewhere), copied to `roles/<name>.md` with placeholders restored. Two prompts resolving to one file name with different content refuse the export, naming both.

**Crew.** A crew member is exported when it hears the depot: any subscription scoped to it (`depot=<depot>`, a stage or a done state), or named by `--crew`. Each exports its role, `hears.events` as its full `on` list, `hears.stages` and `hears.done` derived from its depot-scoped subscriptions, `subscribe` from its unfiltered source subscriptions, `open_threads` from what it may open (`MayOpen`) as asks, and `calls` from its grants of declared sources as asks. A grant thus becomes a question again: the importer re-answers, which is the safe direction. Left out: identities, `env_file`, `env_command`, arguments, model, provider, trust, directory, rotation, `greet`, the `set_state` grant (never from a pack), `mail_peers` (`expose_to_peers` stays for the importer to answer), and every subscription scoped to another depot. Each is reported per crew member in the output whenever it is set to a non-default value. The `on` list is yard-wide and goes whole; the output says so per crew member, so the author knows the pack carries a choice made for the whole yard.

**Sources on hooks.** Of each source, the first hook by name is the one read, since it is the one the import sets (internal/pack/plan.go, `of[0]`); the map, `says` and `makes` of every other hook of that source are left out and listed. Of that hook's lines, where they touch an exported flow: `hears` lines restricted to the type-and-stage pairs the exported flows govern (the rest dropped and listed); `as` only where it names an exported crew member, else the map dropped and listed; `says` lines kept when every stage they name is exported, lines naming no stage kept whole; `makes` lines kept when the bead type's flow is exported and the line names no depot or names this one (exported without the `depot` key, so it applies to the importing depot).

**Declared sources.** A declared source is exported, declaration copied to `sources/<name>.yaml`, when an exported group or crew member asks calls of it. The credential's values and `env_file` stay in the yard; a declaration carries their names by construction. A declaration using command keys (`hook`, `thread`, `link`) cannot go in a depot pack (`DeclarationKeys` refuses it) and is left out and listed; it belongs to an integration pack, which is open question 1's `--yard`.

**Settings.** None. Export leaves yardr-8cgx's answer unchanged: settings are what makes one yard not another, and packs carry a way of working shared across yards. Once 8cgx is built, copying a yard's basics to a second yard is two moves, each whole for its kind: export the pack, and copy `<home>/config.yaml` (then change the ports). Until then the second yard's settings are re-set by hand, which is today's only mechanism. No `settings:` key, no settings file beside the pack.

### 3.3. Placeholders are restored by whole-word replacement

Filling has no recorded inverse, so export inverts it by text: in every copied role and stage file, whole-word occurrences of the yard's `operator` setting become `{{operator}}`, and of each exported crew member's yard name become its `{{<pack name>}}`. Every substitution is listed by file and line for the author to review, because a word that merely equals a name is rewritten too; the round trip still holds (both exports rewrite the same way), but the meaning can shift, and the listing is where the author catches it. A file that already contains `{{…}}` refuses the export with file and line: it would be a pack error no import fills, for the author to fix by hand.

### 3.4. The output, and the self-check

The command prints what went in and, per object, what was left out and why, in the style of `pack check`'s `kept:` lines, so nothing is silently lost. It ends by validating the written directory with the pack reader and printing the verdict; a failure there is an export bug, exits 1, and names it. Since check-against-a-yard needs a yard, the deeper proof is the round trip, which the beads run in the isolated yard in two legs, because the import deliberately creates some things and never others.

The first leg is the empty yard, and it covers exactly what an import can create: flows, stage files, groups, roles, scripts, crew entries and declared sources. Export D1 from the depot; apply D1 to an empty isolated yard with `--yes` and a flag answering every ask the fixture raises (`--bind`, `--members`, `--crew`, each granting everything asked: without a terminal a missing answer is an error, never a default); export D2 from the new depot; `diff -r` shows nothing except the asks below. The test grants everything because a no is the importer's answer, not the pack's: D2 would otherwise drop the ask, correctly but confusingly. `open_threads` and `calls` are out of this leg's comparison: on an empty yard the import asks neither (no hook and credential for the source, no `env_file` on it) and `--yes` grants nothing without `--open-threads` and `--calls` (internal/cli/pack.go), so D2's crew entries carry no granted threads or calls while D1's do. That difference is the operator's ungiven answer, not pack content, and the second leg proves the asks survive where they can be granted. A depot exercising hook lines keeps those lines out of this leg's comparison too: the import makes no hook and grants nothing (internal/pack/plan.go `maps()`: with no hook of the source the plan records it as missing and sets nothing), so on an empty yard D2 cannot contain D1's hook lines, by design.

The second leg seeds what the import needs but never makes, then round-trips the hook lines and the granted asks: in a fresh isolated yard, add the source, a hook of it, and the crew identity the map acts as; apply D1 with `--yes` and the same answering flags; then set what the asks need but no pack carries (the source's `env_file` with fake values, the taking crew member's reply credential the same way: a declaration names only the variables) and apply D1 again, this time with `--open-threads` and `--calls` granting every ask (the first import asked nothing, and what nobody was asked about a later import asks once its hook, credential and `env_file` exist); export D2; the `sources:` entries and the crew's `open_threads` and `calls` show no difference. Map lines need the hook and the identity (without either the plan sets nothing); `says` and `makes` lines need the hook alone, since no crew member acts when an event moves a bead or makes one. The `set_state` grant stays the operator's and is out of the comparison either way: the import never grants it, so no export carries it.

Where the round trip cannot hold exactly, the design says so: the `VAR=…` assignments and trailing arguments of script commands (warned per script); stages left unrouted for want of one plain route; the operator's re-answers above; and the author's hand edits after export, which are the point of writing a pack down.

## 4. Left out

- **`--yard`.** The whole yard at once: every depot's flows and groups merged by the `--group` rules, every crew member, hooks whole, integration declarations with their command files, and the merge questions (two depots' yardmasters, two packs' people groups). Open question 1; the depot version is the mechanism it would reuse.
- **The difference from the pack the yard came from.** Open question 2; the proposal writes the whole.
- **A `settings:` key in packs**, again and finally: yardr-8cgx decided "file only" as the future direction (merged, not built), and export does not reopen it (section 3.2).
- **Depot rules.** The depot's standing rules file has no pack key and no pack provides one; carrying it would invent one. Listed per export until a later design says otherwise.
- **Setup notes.** `setup:` and its file are the author's words about what a setup takes; the author writes them after export. Nothing generates them.
- **Beads, notes, mail, inboxes, journals, depot files, the depot test command, users, devices, peers, hook secrets.** Never in any version; most are listed nowhere, since silence is the rule for other people's work.
- **Peer groups, members of people groups, `expose_to_peers`.** Another yard, persons, and per-link answers have no pack form that names no one; the first refuses, the rest stay for the importer to answer.
- **A prune-style diff of what changed since the last export.** Export writes a directory, not a record; `diff -r` between two exports is the tool, and it already exists.

## 5. Open questions

### 5.1. Does the first version export one depot, or the whole yard?

- **One depot only (`--depot`).** One command, one pack, no merge questions: the teacher's yard exports its essay depot and shares it. Costs: a yard with several depots exports several packs by hand, and integrations (a `kind: source` pack's declarations with command files) wait for the later bead.
- **The whole yard (`--yard`) instead.** One pack of everything, which is what "copy my yard" wants directly. Costs much more: merging every depot's flows and groups, every crew member, hooks whole, and the questions merging asks (two depots' yardmasters, shared people groups), all before anything ships.
- **Both at once.** Costs the most: the second before the first has been used.

Take one depot only. It answers the teacher today with the mechanism the yard version would reuse, and `--yard` becomes one later bead with real exports behind it.

### 5.2. The whole pack, or the difference from the pack the yard came from?

- **The whole.** Self-contained: it applies to a fresh yard with no base to find, `pack check` reads it as any pack, and a pack is small text so duplicating the base costs nothing. Costs: a yard set up from the starter pack and lightly changed exports the starter's files again, and later fixes to the starter do not reach the copy.
- **The difference.** The import records the source pack's commit, so export could write only what changed since it. Costs much more: the recorded directory may be gone, dirty, or a build whose embedded pack differs; and the difference needs a form `check` and `apply` understand (a `base:` key, patch semantics), a new mechanism where a whole pack uses the one that exists.

Take the whole. Packs are shared text, not versioned overlays; the import already shows, per object, what applying one changes.

### 5.3. Which crew members go in: found by subscription, or named by flag?

- **Found, with flags to adjust (section 3.2).** A crew member that hears the depot goes in; `--crew` renames or adds. Costs: the `on` list and unfiltered subscriptions are yard-wide, so the pack carries choices made for the whole yard (the output says so); a crew member that hears the depot only incidentally goes in until the author removes it by hand.
- **Flags only.** Nothing goes in unnamed: `export` without `--crew` writes no crew, and roles naming a crew member refuse until it is named. Costs: every export of a yard with a yardmaster needs the flag, and forgetting it refuses late, after the author expected a pack.

Take found, with flags. The subscription is the existing record of who hears the depot, and the output lists each crew member with why, so the author reviews rather than remembers.

## 6. The beads it would become

In the order they land. None waits on any bead outside this list. Each is tested in the isolated yard (`contrib/isolated-yard`), never the operator's.

### 6.1. `pack export --depot`: flows, stages, groups, roles, scripts

Goal: one depot's flow, stage files, groups, roles and scripts come out as a directory `pack check` accepts and `pack apply` on a fresh yard turns into the same way of working.

Where to look: internal/cli/pack.go (the new verb beside check/apply/show/list), internal/pack (the reader for the self-check), internal/cli/depotsources.go (provenance reading: where each flow, stage file and role comes from), internal/store (DepotFlows, Groups, Routes), internal/depotfile and internal/runner (prompt resolution).

Done when: in an isolated yard, a depot whose roles and stage files name no crew member, then changed by hand (a role edited in the home, a limit changed, a group and route added), exports to a directory that `pack check` accepts; applying it with `--yes` and flags answering every ask to a fresh isolated yard reproduces the governing flow, the routes and the prompts; exporting again shows no difference; a yard-wide route matching an exported stage and a quotaed depot route each leave their stage unrouted and are listed with why; a role or stage file naming a crew member of the yard refuses, naming file and line (crew arrives in 6.2, which is also where the starter-based round trip lands: the starter's builder and reviewer roles already name `{{yardmaster}}`, so this bead's narrower fixture keeps the refusal rule consistent); a `peer` group on a route, a group-name collision, and a script command naming no file refuse with the reason; `go test` of the touched packages passes.

### 6.2. Crew members that hear the depot

Goal: the exported pack carries the crew the depot's files name and hear it, with their roles and what they hear, and the asks an import puts to the operator.

Where to look: bead 6.1's export; internal/store (crew, subscriptions, SourceGrants; `MayOpen` in threads.go); docs/packs.md "Crew members", "Opening threads", "Calls of a declared source".

Done when: a depot set up from the starter pack, then changed by hand (a role edited in the home, a limit changed), exports to a directory that `pack check` accepts; a yardmaster hearing the depot (stage and done subscriptions, a source subscription, `open_threads`, a call grant) exports as a `crew:` entry whose role has its placeholders restored, and the pack-backed flow exports under its pack-local key; bead 6.1's refusal for crew-naming files is lifted for exported crew and stays for the rest; applying with `--yes` and flags answering every ask, then exporting again, shows no difference except the `open_threads` and `calls` asks, which no empty yard grants and the seeded leg of 6.4 covers; `go test` of the touched packages passes. Depends on 6.1.

### 6.3. Sources: maps, says, makes, and the declared sources asked for

Goal: what the depot's beads mean in outside sources, and what those sources' events do to them, comes out as `sources:`, with the declarations the pack's calls need.

Where to look: beads 6.1 and 6.2; internal/store (hooks: map, says, makes); internal/source (declarations) and `DeclarationKeys` in internal/pack/pack.go; docs/packs.md "Stage maps", "What a source's events say", "What a source's events make", "Declared sources".

Done when: a hook with a map, `says` and `makes` lines touching the depot's flows exports restricted to the exported stages and types, read from the first hook by name of the source with `as` naming an exported crew member, and a declared source the pack's calls ask for exports its declaration; lines and sources that do not fit are dropped and each is listed with why; a declaration with command keys is left out and listed; the hook lines round-trip in an isolated yard seeded with the source, a hook of it and the crew identity (section 3.4's second leg) with no difference; the empty-yard leg covers the rest; `go test` of the touched packages passes. Depends on 6.1 and 6.2.

### 6.4. The round-trip suite and the docs

Goal: the two-leg round trip of section 3.4 is one repeatable check, and docs/packs.md says what export writes and refuses.

Where to look: beads 6.1–6.3; contrib/isolated-yard; docs/packs.md.

Done when: one script or test runs both legs of the round trip of section 3.4 over a depot exercising every part (hand-set flow, a pack-backed flow, edited roles, added group, hearing crew member, hook lines, a declared source): the empty-yard leg for what the import creates (minus the asks no empty yard grants), the seeded leg for the hook lines and the granted `open_threads` and `calls`; it fails on any difference `diff -r` finds that the design does not excuse; docs/packs.md gains the export section (the command, the part-by-part table, the refusals, the round trip); `go test` of the touched packages passes. Depends on 6.1, 6.2 and 6.3.

A later bead, after decide answers open question 1, carries `--yard`: every depot's flows and groups merged, every crew member, hooks whole, and integration declarations with their command files. It reuses beads 6.1–6.3 part by part and adds only the merge.
