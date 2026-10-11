# A depot's own working rules, added to a role instead of replacing it

Design for bead yardr-me78 (flow `design`, stage `draft`). A depot is a
repository or directory beads are worked in; a role is a prompt file telling
one group (builders, reviewers) how to work; a pack is a way of working as
one directory that a depot is set up from; a brief is the instructions an
agent session starts from. See docs/glossary.md for all of yardr's words.

## 1. The question

Parked from the design yardr-hq65, question 8 (mckean decided on 6 Oct 2026
to leave it out of the packs chain and decide it separately): a generic
role needs nothing copied into a depot, but a depot like yardr has rules of
its own — its language, `contrib/isolated-yard`, never installing from a
worktree, what the light gate misses. Today the only place for them is a
whole role file in `.yardr/roles/`, so yardr's roles are forks of the
starter's and every change to the starter's has to be made twice. How can a
depot add its own standing working rules to the roles it uses without
replacing them?

## 2. What exists today

**Roles, and where they are read from.** The starter pack's roles live at
`packs/starter/roles/` (`builder.md`, `merger.md`, `reviewer.md`,
`yardmaster.md`); the pack's groups point at them
(`packs/starter/pack.yaml`, `groups.<name>` with `agent: roles/<file>`).
Importing a pack installs its roles filled under
`~/.yardr/packs/<pack>/roles/`, and the new groups' `prompt` is that
absolute path (docs/packs.md; docs/configuration.md "What lives where").
A group's prompt is read when a session starts (docs/configuration.md
"Role prompts"; `readPrompt` and `lookPrompt` in
internal/runner/runner.go): an absolute path is read as is; a role
(`roles/<name>.md` or bare `<name>.md`, `roleName`) is looked up in the
depot's `.yardr/roles/<name>.md`, then the deprecated depot-root
`roles/<name>.md`, then `~/.yardr/roles/<name>.md`; any other relative path
comes from the depot's working tree, then the home. A depot's roles are
read from the tip of its base branch (`depotfile.ReadDepotFile` in
internal/depotfile/depotfile.go, via `git cat-file --batch` in
`readAtBase`), never from the bead's branch or worktree, so a bead cannot
rewrite the prompt of the reviewer that judges it; only a depot whose base
does not resolve, or a dir depot, is read from disk. A crew member's prompt
is an absolute path, and crew members have no depot
(docs/configuration.md).

**The brief.** `Brief` in internal/runner/runner.go renders what an agent
session starts from, in this order: the group's prompt, the session header
(actor, bead, group, worktree and branch), the train note if any, the
bead's title and body, "Reporting back", "Sources", the "Flow" map, the
"Stage" file, and "Outcomes" or "Next stages". The stage file is the job's
for a repository flow, or read from the home for a store flow
(`store.HomeStageFile` in internal/store/repoflows.go). herdr starts the
agent with the first prompt `Read <brief> and follow it. Run yardr as
<bin>.` (internal/runner/herdr.go); briefs live outside the tracked tree
(`BriefDir`) or under `.yardr/briefs/` for dir depots. The consequence
that matters: for a depot set up from a pack — pack roles by absolute
path, a store flow, home stage files — no byte of the brief is read from
the depot's repository. The repository contributes the code and the gate
script, which is run, never shown.

**prime.** `yardr prime --bead` shows the bead, its `test:` line
(internal/cli/beads.go), the flow map and the `STAGE` block with the stage
file's body — but not the role. (Both the starter's and yardr's builder
roles say "your brief prints it as `test:`"; strictly it is prime that
prints it, not the brief.)

**Stage files.** docs/flows.md "Stage files: what a stage is for": what
the stage is for, what must be true to leave it, and when each outcome
applies; shown to whoever works a bead there — an agent in its brief, a
person or crew member with prime. One file per flow and stage:
`.yardr/flows/<flow>/<stage>.md` on the base for repository flows,
`~/.yardr/flows/<name>/<stage>.md` for store flows. A flow switches whole
(docs/configuration.md): `yardr depot source <depot> flow depot|pack`
moves the flow and its stage files together; a single stage file cannot be
overridden.

**Packs.** A pack names no person, so role and stage files use
`{{operator}}` and `{{crew member}}` placeholders, filled at import
(`Fill` in internal/pack/pack.go). "The pack's roles win" (docs/packs.md):
an import points groups at the pack's copies and names the depot's files
as shadowed (`depotRole` in internal/pack/plan.go). `yardr depot sources`
shows each flow, stage file and role with its source and what it shadows;
`yardr depot source` switches one role or the whole flow. Note: the
starter pack carries a `merger.md` role though none of its groups uses
one (its train flow routes `merge` to people); in the yardr depot,
`merge` is routed to `mergers`, a herdr group (docs/line.md "Merge: a
merger resolves conflicts").

**The harness's instructions file.** An agent harness reads its own file
from the working tree by its own convention (claude `CLAUDE.md`, codex
`AGENTS.md`). yardr's repository has no such file — no `CLAUDE.md`,
`AGENTS.md` or `MUSE.md` at its root — and yardr never reads or writes
one. herdr starts the agent in the bead's worktree, so whatever the
harness reads there is the bead's branch copy, changeable by the bead
being worked.

**The gate.** The store holds each depot's test command (`depot add
--test`); yardr's is `sh .yardr/test`, whose header describes the plain
run and the whole suite under `YARDR_LANDING=1`. The assembly runs the
base's copy on exactly what lands. Roles name the command, never the
script's content.

**Principles.** README "Principles": Generalization (few concepts, each
carrying many cases), Composability (concepts combine without knowing
each other), Simplicity (the smaller mechanism is chosen; the special
case is left out until use asks for it). And the property the packs
design holds throughout: a prompt and a flow are never read from a
bead's branch (docs/configuration.md "Never from a bead's branch").

**How much of yardr's `.yardr/roles/` is still a fork.** All four roles
differ from the starter's in full: builder 101 changed lines (81 vs 86
lines), reviewer 140, merger 49, yardmaster 113. Changed lines means
added lines plus deleted lines as `git diff --numstat` counts them
(builder 53 added and 48 deleted, reviewer 54 and 86, merger 15 and
34, yardmaster 17 and 96), run as `git diff --numstat --no-index
packs/starter/roles/<f> .yardr/roles/<f>`. The builder and reviewer
keep the starter's shape but rewrite three blocks each: the test section
(now `sh .yardr/test`, gofmt/vet/build, the race selection over changed
packages plus importers and path-readers, the landing suite), the
isolation section (now `contrib/isolated-yard/isolated-yard up/down`
with `D/e`, claude-only stand-ins), and the push/install section (now
the never-`go install`-from-a-worktree ban); names are concrete
(`Christopher`, `yardmaster`, `main`, `--depot yardr`) where the
starter's has placeholders. The merger's delta is smaller (the base
named `main`, the note and outcomes deferred to the stage file, shorter
train and when-not-to sections). The yardmaster's is mostly deletion
(the whole First-run setup, this yard being long set up) plus concrete
names — lifecycle, not depot working rules. The default stage files all
differ too, but slightly: backlog 2 lines, decide 8, new 12, review 20,
approved 42 — the operator's name, the test command concretized, the
assembly described. The yardr depot governs itself from its repository
(`yardr depot sources yardr`: flows, stages and roles all `depot
(…@db24532)`; only the `design` flow is a store flow with home stage
files), and its groups use relative prompts (`prompt=roles/builder.md`)
resolved through the depot lookup. In short: after the packs chain, the
roles are still forks in full — there was no other slot for the depot's
rules — and the stage files are near-copies with small depot deltas.

## 3. The proposal

**The depot file.** A depot may keep one file of its own standing
working rules at `.yardr/depot.md` on its base (the name is Open
question 1): how this depot is worked, for every agent session on its
beads. In yardr's case that is the isolated-yard procedure, the
never-install-from-a-worktree ban, and what the plain test run selects
and misses — the three blocks the builder and reviewer forks both carry
today. The brief appends it directly after the role, as its own section
(see below), so the role stays the pack's and the depot's rules travel
with it. The file is read with `depotfile.ReadDepotFile`, the same read
roles, flows and stage files use: from the base for a git depot, from
the directory as it is for a dir depot. A depot without the file gets
briefs exactly as today: a missing file means no section, never an
error. The section is appended whenever the file exists, even where the
depot also overrides the role — role lookup and the depot file do not
know each other, which is what composability asks. The file is plain
markdown with no placeholder filling: the depot writes its literal
names, as its roles do today. It shadows nothing and no pack provides
one (Open question 2), so `pack check` and `pack apply` do not mention
it; `yardr depot sources` shows it as its own line so the operator sees
what judges see.

**What carries it, and what is new.** Existing: the base read
(`internal/depotfile/depotfile.go`, the function roles already use);
the brief joining (`Brief`, the same append the stage file gets); the
visibility (`yardr depot sources`, the same lines roles and stages
get). New: one path convention, one brief section, one sources line.
No new flag, setting, placeholder, flow, or precedence rule.

**What a person types and sees.** A builder's brief begins:

```text
# Builder

You implement one bead, in its own worktree on branch `yard/<bead>`.
…the pack's role, unchanged…

## Working in yardr

Anything that starts agents is tested in an isolated yard, never
Christopher's herdr session or yard. From your worktree:
`contrib/isolated-yard/isolated-yard up` builds one in a directory D…
…the depot's file, whole…

---

# yardr session 01a12c1f-…
```

and `yardr depot sources yardr` gains one line:

```text
rules  yardr  depot (.yardr/depot.md@db24532)
```

or `rules  yardr  none` where the depot keeps no file. Adopting it is
writing the file, deleting the role forks it replaces, and pointing the
groups at the pack's copies (the third bead below); nothing else in any
yard changes, since no depot has the file until its operator writes one.

**Why a new file rather than the two existing carriers.** Neither fits,
and "Left out" says why in full: the harness's file is read from the
bead's branch, which breaks the one trust property roles and the gate
hold; the stage files are one per stage, so depot-wide rules would be
copied into every one and lost the day the depot follows a pack flow.
The file is the smallest thing that is single-copy, base-read, and
independent of which flow governs.

## 4. Left out

**The harness's file (`CLAUDE.md`, `AGENTS.md`) as the carrier.** It
would need no yardr change at all: write the depot's rules there and
every agent working in the tree reads them. Three reasons against.
First trust: the harness reads the bead's branch copy, so a bead could
rewrite its reviewer's instructions — remove "test in an isolated
yard", soften "be thorough" — which is exactly what "never from a
bead's branch" exists to prevent. The rules that judge (what to run,
where, how thoroughly) must stay base-read; the harness file can only
carry what judging does not depend on. Second naming: the file is per
harness, so a depot with claude and codex groups writes the rules twice
under two names or leaves one harness without them. Third reach: crew
members never work in the depot's tree, and outside contributors' agents
read the same file. It stays a fine place for project notes to humans
and their own agents; it cannot carry the depot's standing orders to the
yard.

**The stage files as the carrier.** Yardr owns its flows already, so it
could move the shared blocks from its roles into its stage files today,
with no code change, and take the pack's roles. Three reasons against.
First duplication: the isolated-yard procedure, the install ban and the
test specifics apply to `new`, `review` and `merge` alike, so they
would be copied into three stage files — the same fork drift at a
smaller scale, edited in three places from day one. Second purpose: a
stage file says what the stage needs and when each outcome applies; a
depot how-to in every one buries that. Third reach: the rules would be
lost the day the depot follows a pack flow (a flow switches whole, and
the home's stage files are the pack's), they never reach crew members,
who see no stage file, and they answer nothing for pack-governed
depots, the growing case after the packs chain.

**Placeholders in the depot file.** No `{{operator}}` filling: filling
happens at pack import for pack files, and the depot file is already
the depot's — it writes literal names, as the depot's roles do today.
A second fill machinery for one file is the kind of new concept the
principles refuse.

**Per-group or per-role depot files.** One file serves every agent
session on the depot's beads. The builder's and reviewer's shared
blocks are the same text today; splitting them per group would fork
them again. A group that needs less simply ignores what does not apply
to it.

**A size cap.** None in the first version. Every line lands in every
agent brief, so bloat has a real context cost, and the pack `setup:`
key's 64 KB precedent exists if briefs grow; name a cap when one does,
not before.

**The merger and yardmaster forks.** They stay as they are. The
merger's 49-line delta is small and partly generic improvements to
propose to the starter's role separately, not depot rules. The
yardmaster is crew, and crew members have no depot: no per-depot file
can reach them, and their fork is lifecycle (setup removal), a separate
question.

**Include and partial-override machinery.** No includes between stage
files, no per-stage overlay on a pack flow, no pack key for this. Each
is a larger change than the file — a precedence rule, a composition
order — for a problem one file solves.

## 5. Open questions

**1. The path.** Options: `.yardr/depot.md` (top-level, beside
`flows/`, `roles/` and the gate files); `.yardr/rules.md` (same place,
a plainer name); `.yardr/roles/depot.md` (beside the roles). The roles
directory pollutes the role namespace — `prompt=roles/depot.md` would
then resolve to it — for no benefit, so it costs a wart the others do
not have. Between the top-level names the cost is only permanence: the
path is a convention every depot learns once. I would take
`.yardr/depot.md`: top-level because it is not a role, and `depot`
because it is the depot's own voice, next to the gate that is also the
depot's own.

**2. Should a pack ship fallback content for it?** Options: no, the
file is the depot's alone and most depots have none (a depot that needs
nothing writes nothing); or a pack key, like `setup:`, whose text Brief
uses when the depot keeps no file. The fallback costs a new pack key
plus fill, precedence and shadows questions — and it contradicts the
shape: a pack describes a way of working, the file describes one depot,
so pack text in the depot's slot belongs in the pack's roles instead.
I would take depot-only.

**3. Should prime show it?** Options: briefs only, prime unchanged; or
prime gains the depot's rules beside its `STAGE` block. The readers of
prime are agents who already have the brief, and people in manual
stages who wrote the file and run no tests — no demonstrated reader
needs it there, and the cost, though small (one more block, one more
read path), buys nothing today. I would take briefs only, and add prime
when a manual stage needs it.

**4. How yardr adopts it: delete the forks or shrink them?** Options:
delete `.yardr/roles/builder.md` and `reviewer.md` and point the groups
at the pack's installed copies; or shrink them to near-copies kept as
depot overrides. Deleting costs care in the move — the groups use
relative prompts, so merely deleting would fall through to the stale
`~/.yardr/roles/` copies, and the prompts must become the pack's
absolute paths — and it means future starter wording reaches yardr
through pack review rather than depot review, the packs design's
accepted trade. Keeping near-copies costs the disease back: every
starter change made twice from day one. I would take deletion.

## 6. The beads it would become

In landing order. Each lands alone; each says what it leaves out.

**1. The brief appends the depot file.** Goal: `Brief` reads
`.yardr/depot.md` (the decided name) from the depot's base and appends
it after the role; a depot without the file gets byte-identical briefs.
Where to look: the prompt block of `Brief` (internal/runner/runner.go),
`depotfile.ReadDepotFile` for the read (missing file means no section;
other errors fail like a missing prompt), the `TestBrief*` pattern in
internal/runner/runner_test.go for tests, docs/configuration.md "Role
prompts" for the docs. Keep the path in one constant the next bead
reuses. Done when: a depot with the file shows the section in every
agent brief, a depot without it shows nothing new, the text comes from
the base and not the branch (a test with differing copies), a dir depot
reads its directory, the docs describe the file, and the gate passes.
Left out: `depot sources` (next bead), prime (Open question 3),
placeholders.

**2. `depot sources` shows the depot file.** Goal: one line per depot
— `rules <depot> depot (.yardr/depot.md@<sha>)`, or `none` — plus the
`--json` field, so the operator sees what judges see. Where to look:
internal/cli/depotsources.go and its test, the reader and path constant
from bead 1, docs/configuration.md's sources section. Done when: the
line shows the base commit for a depot with the file and `none` for
one without, `--json` carries it, the docs describe the line, and the
gate passes. Depends on bead 1. Left out: switching (there is nothing
to switch; the file is additive, and removing it is deleting it).

**3. Yardr adopts it: builder and reviewer back to the pack's.** Goal:
write `.yardr/depot.md` with the isolated-yard procedure, the
install ban and the test specifics, delete
`.yardr/roles/builder.md` and `reviewer.md`, and point the groups at
the pack's installed copies, so the depot's rules live in exactly one
place. Where to look: the two diffs measured in section 2 (builder 101
and reviewer 140 changed lines against `packs/starter/roles/`), the new
docs from beads 1 and 2, and the groups' prompts — they are relative
(`roles/builder.md`) and resolve through the depot lookup, so check
what `yardr depot source <depot> role <group> pack` does for a depot
with no pack import, else `yardr group set` the absolute pack paths;
verify with `yardr depot sources yardr`. Done when: the depot file
holds the three shared blocks once, the two role files are gone, the
groups read the pack's copies, sources shows pack roles plus depot
rules, and the gate passes. Depends on beads 1 and 2. Left out:
merger and yardmaster roles (section 4), stage files (unchanged —
yardr owns train and wagon flows no pack carries).
