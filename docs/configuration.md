# Configuration: the repo and the store

yardr reads its configuration from two places:

- **the depot's repository**, under `.yardr/`: what the code must pass. The
  gate, versioned with the code it checks and changed through the depot's
  review. A depot that keeps a flow or roles of its own keeps them here too.
- **the store** (`yard.db`, owned by the yardr server): who works it, where
  and how. Live state, changed with `yardr` commands, no commit needed.

A depot's way of working (its flow, the stage files and the roles of its
groups) comes from a [pack](packs.md): `yardr depot init` imports the yard's
default pack into the store and the yard's home, and writes the gate alone
into the repository ([below](#seeding-a-depot-yardr-depot-init)).

The split was decided in yard-17 (2026-10-02). Both sides are built: flows
in yard-9cza, the merge gate in yard-p2y7.

## What lives where

| | repo `.yardr/` (what the work is) | store (who / where / how) |
|---|---|---|
| flows | a depot's own default and type flows, named flows: `.yardr/flows/` (built); none in a depot set up from a pack | named flows, a pack's among them (`<pack>.<flow>`), and depot and type pointers to them (built): where the flow of a depot set up by `yardr depot init` is |
| stage files | a repository flow's: `.yardr/flows/<name>/<stage>.md`, read from the base (built) | none in the store: a store flow's are files in the yard's home, `~/.yardr/flows/<name>/<stage>.md` (built; [flows.md](flows.md#stage-files-what-a-stage-is-for)) |
| merge gate | `.yardr/check` (built) | a per-depot override, `depot add --check` (built) |
| light gate | `.yardr/check-light` (built) | a per-depot override, `depot add --check-light` (built) |
| which lines of a red gate's output say what failed | `.yardr/check-fail-pattern` (built) | none; a red gate's whole output is a file in the yard's home, `~/.yardr/gates/<depot>/` (built) |
| roles | a depot's own role prompts: `.yardr/roles/<name>.md`, read from the base (built); none in a depot set up from a pack | each group's and crew member's `prompt`: for a depot set up from a pack the absolute path of the pack's role as installed in the home, `~/.yardr/packs/<pack>/roles/<name>.md` (built). `~/.yardr/roles/`, in a yard that has it from an earlier yardr, is the fallback of a relative `prompt` |
| groups | | runner, limit, members, subscriptions (manual), agent kind, model, args, cmd, worktree settings (built) |
| routes | | which group works which stage, by depot, type, label (built) |
| depots | | path, kind (`git` or `dir`), clone, base branch; `depot add` registers, `depot rm` removes (built) |
| crew members | | agent, directory, inbox kinds, rotation, subscriptions, identities (built) |
| settings | | the yard's defaults: `default_kind`, `default_pack`, `operator`, `yardmaster`, `name`, `hook_addr`, and how agents work with the operator: `operator_style`, `operator_note`, and the bounds of its timers: `timer_limit`, `timer_min_every` (built) |

### Why the repo holds what the work is

The gate describes the codebase: what "green" means for this code. It
changes when the code changes, so it belongs next to it:

- **Versioned.** A gate is tied to the code it applies to. Check out an old
  commit and you get the gate that applied then.
- **Reviewed.** Changing `.yardr/check` is a code change like any other; it
  goes through the depot's review and lands on the base branch. Nobody lowers
  the bar without someone seeing it.
- **Portable.** Anyone who runs yardr on the repo gets the same gate,
  without copying store state.

The flow and the roles describe a way of working: which stages a change
passes, who works each, how a builder or a reviewer goes about it. That is
the same for every depot worked that way, so it is a [pack's](packs.md), and
`yardr depot init` writes no flow, stage file or role into a repository. A
copy in each depot was a copy that fell behind the day the wording changed.

- **Where it is reviewed.** A change to a pack's flow or role is reviewed
  where the pack is kept, a repository of its own for a pack of yours, and
  reaches the yard when the operator runs `yardr pack apply`, which shows
  every change before it writes one ([packs.md](packs.md#yardr-pack-apply)).
  The import records the pack's commit. It is no longer a commit on the
  depot's base.
- **A depot that keeps its own flow keeps the review.** Commit
  `.yardr/flows/default.flow` and its stage files on the base and run
  `yardr depot source <depot> flow depot`
  ([below](#switching-one-of-them)): the flow is then the repository's,
  read from the base, and a change to it lands through the depot's review
  like the gate. The same holds for a role: `.yardr/roles/<name>.md` on the
  base and `yardr depot source <depot> role <group> depot`.
- **Never from a bead's branch.** Either way a prompt, a flow and the gate
  are read from the base or from the yard's home, so a bead cannot change
  what judges it.

### Why the store holds who, where and how

Groups, runners, models, agent arguments, members, routes and crew members
describe one installation, not the codebase:

- **Machine-specific.** Paths to scripts and prompts, the herdr setup, which
  agents and models are installed, where worktrees go. A different machine
  needs different values; committing them would be wrong for everyone else.
- **Trust decisions.** Agent arguments decide what an agent may run
  unattended (`--permission-mode auto`, `--dangerously-skip-permissions`).
  That is the operator's call for their machine, not something a repository
  should be able to grant itself by committing a file.
- **Live.** Pausing a group, adding a member, rerouting a stage or switching
  a model happen while beads are in flight, without a commit or a merge.

A role prompt says how a group works, so it is a pack's: the import installs
it under the yard's home and gives the group its absolute path as `prompt`.
The roles of the packs yardr carries name no person, depot or language. A
depot that is worked in a way of its own (its language, its tools, who
decides) keeps a role in its repo, in `.yardr/roles/` next to `.yardr/check`,
and a group names it by role: `--set prompt=roles/builder.md`, or `yardr
depot source <depot> role <group> depot` for a group a pack made.

### Role prompts

A group's `prompt` is read when a session starts:

- an absolute path is read as is;
- a role, `roles/<name>.md` or bare `<name>.md`, is looked up in order:
  1. the depot's `.yardr/roles/<name>.md`;
  2. the depot's `roles/<name>.md`, the older place: still read, with a
     warning (once per file) in the daemon's log to move it;
  3. the yard's home, `~/.yardr/roles/<name>.md` (`--home`, `YARDR_HOME`).
- any other relative path is looked up in the depot's working tree, then in
  the home.

A depot's roles are read like its gate: from the tip of the depot's base branch
(`git show <base>:.yardr/roles/<name>.md`) and from nowhere else in the depot.
Not from the bead's branch or worktree, so a bead cannot rewrite the prompt
of the reviewer that judges it, and not from the checkout's working tree or
whatever branch it has out either: a role the base does not have is not the
depot's, and the home's copy serves until the edit has landed. Only a depot that
is not a git checkout, or whose base does not resolve, is read from its
working tree.

A depot set up by `yardr depot init` needs none of this lookup: its groups'
prompts are absolute paths into `~/.yardr/packs/<pack>/roles/`, which no
commit in the depot changes. Committing `.yardr/roles/<name>.md` there does
not take a group over; `yardr depot sources <depot>` shows the file as
shadowed until `yardr depot source <depot> role <group> depot` makes it the
group's ([below](#switching-one-of-them)). The lookup serves a group set by
hand with `--set prompt=roles/<name>.md`, and yards from before `depot init`
took a pack: there the depot's committed role wins over the home's copy, for
that depot only. `yardr init` no longer writes `~/.yardr/roles/`; a yard that
has the directory keeps it, and it is read as the last place as before. A
prompt found nowhere fails the session start with every path tried in the
error (the depot's as `<base>:.yardr/roles/<name>.md`); three such failures
hold the bead.

Crew members have no depot: a crew member's `prompt` is an absolute path (a
relative one is read from its directory). The yardmaster's is the role of
the default pack's crew member called `yardmaster`, which `yardr init`
installs as `~/.yardr/packs/<pack>/roles/yardmaster.md`.

## Session limits and outages

A stuck bead session should surface, not run forever. Each group can set,
with `yardr group add <name> ... --set key=value`, or on a group that is there
with `yardr group set <name> key=value`:

| key | default | meaning |
|---|---|---|
| `idle` | `30m` | the agent sat idle (herdr status `idle` or `done`) this long: a `session_idle` event, once per idle spell, with the end of its pane as `tail` (the 20 lines `session_stalled` carries) |
| `max_age` | `4h` | the session ran this long: a `session_aged` event, once per session |
| `stall` | `2m` | the agent sat idle this long after its first prompt and did nothing (no note, no commit, no change in its worktree): a `session_stalled` event with the end of its pane, once per session, and the group starts no further session with that model meanwhile ([groups.md](groups.md#models)) |
| `grace` | unset | when set, a session still idle (or still running) this long past its limit is stopped and counted as a failed session; `0` stops it at once. Unset, the defaults below apply |

Durations are Go durations (`45m`, `2h`) or a number of seconds; `off` or
`0` turns `idle`, `max_age` or `stall` off. The defaults apply to runners that report
an agent's status (herdr). For `exec` and `manual` groups only what the group
sets applies, and `idle` never does, since they cannot tell idle from busy.
The idle clock is the daemon's: one that replaces itself starts it again,
while the age is counted from the session's start and reported only once.

A manual group has no session to go idle. Its `wait` is how long a bead may
be in a stage of the group before a `waited` event, once per stay:
[groups.md](groups.md#a-bead-that-waits-long).

**Waiting for its gate is not idle.** An agent that has committed and waits
for `yardr depot check` is idle to herdr for as long as the gate takes, which
is longer than a builders' `idle` of a few minutes. So a `yardr depot check`
run for a bead is known to the yard while it runs, and the daemon raises no
`session_idle` for that bead's session, nor stops it under `grace`: the idle
clock starts again at the daemon's first pass after the gate has ended.
`max_age` and `stall` go on as they do.

| | |
|---|---|
| whose gate | the bead whose worktree `--dir` is (the branch `yard/<bead>` it is on); with no `--dir`, the one whose worktree the command was run from; else `YARDR_BEAD`, the bead the session was started for (a dir depot's bead works in the depot itself). A run for no bead (the yardmaster's, in the depot) is not recorded. |
| where | a file per run in `<home>/gating`, holding the bead, the depot, light or full, since when, and whether it still waits for a [gate slot](#gate-slots-how-many-gates-run-at-once). The run holds a lock on it, which the kernel drops when the process ends, however it ends: a gate that was killed is not a gate that runs. It is in the yard's home, not with the machine's slots, since a bead is one yard's and a light gate takes no slot. |
| who sees it | `yardr session list` has a column `GATE`: `gating 6m (light)`, or `waiting 2m (full)` for a run that waits for a slot (it counts as running here); `--json` has `gate` on the session. `yardr status` names the yard's beads in its `gates` line. |
| what it does not see | an agent that is idle for another reason while no gate runs: it gets its `session_idle` as before, which carries the end of its pane. A harness stopped on an error looks the same to herdr and is told apart by that pane, at the start of the spell: `session_harness_error`, below. An agent that is idle because the yard refused its `yardr bead done` has `move_refused` on its bead ([pipeline.md](pipeline.md#2-new-a-builder-implements-it)). |

**A harness that stopped on an error is not just idle.** A harness that
prints `Selected model is at capacity` and waits for input is idle to herdr.
The daemon reads the end of an idle agent's pane at the start of each idle
spell, whatever `idle` is set to (`off` too), and a known error line of the
group's `kind` raises `session_harness_error` at once, with a `reason`
(`at_capacity`, `not_logged_in`, `rate_limited`, `other`): see
[groups.md](groups.md#a-harness-that-stopped-on-an-error). No key turns it
off; it stops nothing and holds nothing back. One case the daemon first
handles itself: `at_capacity` in answer to a session's first prompt gets the
prompt again, 3 times 5 minutes apart (`session_harness_retry`), and
`session_harness_error` is raised only when that did not help, with `tries`.
And an agent stopped on a usage limit whose window resets within six hours
is told of at once and prompted when it has reset; while a session waits
for that it is not idle to `idle` and `grace`, and `max_age` applies.
A crew member's pane is read the same way while it is idle with its inbox
unread.

**Defaults without a grace.** An idle session is only reported: an agent
waiting on a human (a permission prompt, a question) is idle too, and
stopping it would lose the answer. An aged session is reported at `max_age`
and stopped anyway once it has run twice `max_age` (8h by default): nothing a
bead needs takes that long, so a session still running is stuck. The stop
counts as a failed session, so a bead whose agents keep running into the cap
is held after three like any other repeat failure, and the bead is released
with a note naming the session, the cap and the fix (raise the group's
`max_age`), so the next agent and the yardmaster see why. The `session_aged`
event's `stop_after` says how long after the event the session is stopped,
with or without a grace.

These events, `runtime_down` (below) and `held` (a stuck agent holds its bead
for the yardmaster) are in a new crew member's default `--on`, so the
yardmaster's inbox gets them; a crew member whose `--on` is a list copied
before one of them joined the default needs it added, or the class
`attention` in the list's place, with `yardr crew set <name> --on`, which
replaces `--on` as a whole ([crew.md](crew.md)). `runtime_down` is about no
bead, so a `yardr` subscription does not deliver it.

**Outages are not failures.** When a runner cannot start anything at all
(herdr missing from `PATH`, herdr down, `sh` not found), the session is
ended as `aborted`, which does not count toward the 3 failures that hold a
bead, and the bead waits where it is. The group gets one `runtime_down`
event per outage and is not tried again for 30s, doubling up to 5m while it
stays down; the first start that works logs `runtime_up`. Errors that are
the bead's own (no depot, a bad brief, a worktree that cannot be made) still
count. herdr's own answer that its server is not running or not ready
(`server_not_running`, `server_unavailable`) is an outage too, not a bead
failure. The fetch of a depot's base before a bead branches from it gives up
after 60s and never waits on a credential prompt (`GIT_TERMINAL_PROMPT=0`),
so a dead remote cannot stall the daemon; every other git call a start or a
cleanup makes (`worktree add`, `branch`, `worktree remove`) is cut off after
2 minutes on the same terms.

## Depot kinds: git and dir

A depot is one of two kinds, shown by `yardr depot show` and `depot list`:

- **git** (the default, and every depot registered before kinds existed): a
  repository. Beads work on branches in worktrees, start from the depot's
  base and land on it. `.yardr/` is read from the tip of the base, as the
  rest of this page describes.
- **dir**: a plain directory, a folder of documents, images or invoices.
  There is no repository, no branch and no base: beads are worked in the
  directory itself. `.yardr/` is read from the directory as it is on disk,
  so an edit to a flow, a role or the check applies at once (flows are
  reread when a file under `.yardr/flows/` is added, removed or changes
  mtime or size). Nothing reviews `.yardr/` there before it applies; the
  bar is the operator's, not a landed commit's.

`yardr depot add <name> <path>` detects the kind: if `git -C <path> rev-parse
--git-dir` succeeds the path is a git depot, otherwise a dir depot, and it
prints which it took. `--kind git|dir` says so instead; `--kind dir`
registers even a checkout as a plain directory. A dir depot refuses `--base`
and `--clone`.

| `.yardr/` | git depot | dir depot |
|---|---|---|
| `flows/` | at the tip of the base (`repo@<sha>`) | in the directory (`repo (directory)`) |
| `flows/<name>/<stage>.md` | at the tip of the base, with the flow | in the directory |
| `roles/<name>.md` | at the tip of the base, else the working tree | in the directory |
| `check` | at the tip of the (fetched) base, written out and run | in the directory, run in place |
| no `check` | fails ("no gate") unless `--allow-no-check` | passes ("no gate"): nothing merges |

Trains on a dir depot only group their children: no train branch, and a
child's base (`bead show`, `$YARDR_BASE`) is empty, as is every bead's in
a dir depot.

## Precedence

Where both sides can define something, the most specific source wins:

```
pin on the bead  >  store  >  repo .yardr/  >  none
```

- **Pin on the bead**: the bead names its flow itself (`meta flow=<name>`),
  for the odd bead that needs a different lifecycle.
- **Store**: the operator's override for this installation, e.g. a depot or
  type pointing at a named flow (`yardr depot flow`), or `depot add --check`
  (built).
- **Repo `.yardr/`** (built): the default that ships with the code.
- **None**: no flow means a bead may move to any stage. No check fails the
  gate ("no gate") unless the depot was added with `--allow-no-check`, or is
  a dir depot.

Within one source a type-specific flow wins over the depot's flow; a source
wins as a whole, so a depot pointer in the store overrides the repo's type
files too (see [flows.md](flows.md)). `yardr bead show` and `prime` say
which flow governs a bead and where it came from: `pin`, `store`,
`repo@<sha>`, and the type when a type's flow matched. For the check the
rule is: the depot's `--check`, else `.yardr/check` on the base, else no
gate, which fails unless the depot allows it.

### Where a depot's flows, stage files and roles come from

The store decides per object: the depot's flow pointer, and the `prompt` of
each group and crew member. `yardr depot sources <depot>` shows all of it in one
place, one line per object: the kind, the name, the source that governs,
and what it shadows or what else is available.

```
$ yardr depot sources yardr
KIND   NAME            SOURCE
flow   default         pack starter (store starter.default)                     shadows .yardr/flows/default.flow
stage  new             pack starter (~/.yardr/flows/starter.default/new.md)     shadows .yardr/flows/default/new.md
stage  review          pack starter (~/.yardr/flows/starter.default/review.md)  shadows .yardr/flows/default/review.md
stage  decide          pack starter (~/.yardr/flows/starter.default/decide.md)
role   yardr-builders  depot (.yardr/roles/builder.md@add63e5)                    pack starter has a copy
role   yardr-reviewers pack starter (~/.yardr/packs/starter/roles/reviewer.md)  shadows .yardr/roles/reviewer.md
role   yardmaster      home (~/.yardr/roles/yardmaster.md)
```

- **flow**: each flow governing the depot, as `yardr flow show <depot>` resolves
  them: the depot's own (`default`), then one per type (`type/<type>`). With
  type flows, a stage is named `<type>:<stage>` (`default:<stage>` for the
  depot's own).
- **stage**: the stage file of each non-terminal stage of those flows.
- **role**: the prompt of each agent group a stage or route of the depot leads
  to, where a brief for a bead of the depot reads it, and of each crew member a
  pack imported into the depot names. Script and people groups have no role.

| source | it is |
|---|---|
| `depot (<path>@<sha>)` | a file of the depot's repository, at that commit of the base (no commit: a dir depot's directory, or a working tree) |
| `pack <name>` | the pack's flow in the store, a stage file of that flow in the home, or the pack's installed role |
| `store` | a named flow in the store that no pack imported into the depot brought |
| `home (<path>)` | a file under the yard's home: a role there, or a stage file of a store flow |
| `file (<path>)` | any other path |
| `none` | no flow governs, the stage has no file, the group no prompt; with `: <why>` after it, a prompt that names a file nobody has |

`shadows <file>` names a file of the depot's repository that nothing reads
while the line is as it is: the flow file behind a store pointer (and,
behind the depot's pointer, every type's file, since the store wins as a
whole), the stage file of a flow that does not govern, the depot's own role
behind a group prompted from a pack or the home. Stage files of a shadowed
flow that match no stage of the governing one are listed on the flow's
line. `yardr depot check` names every shadowed file in a note
(`note: .yardr/roles/reviewer.md is shadowed by …`), which never fails the
check. `pack <name> has a copy` and `pack <name> has flow <flow>` say what
the depot could take instead.

`--json` prints the lines as data: `kind`, `name`, `flow`, `type`,
`source`, `pack`, `path`, `commit`, `shadows`, `available`, `error`. The
view changes nothing.

#### Switching one of them

`yardr depot source` switches one line between the depot's repository and the
pack imported into the depot, and prints the line as it is afterwards:

```
$ yardr depot source yardr role yardr-reviewers depot
KIND  NAME             SOURCE
role  yardr-reviewers  depot (.yardr/roles/reviewer.md@add63e5)  pack starter has a copy
note: the import of pack starter chose the pack's roles: the next yardr pack apply sets its role on yardr-reviewers again unless given --depot-roles

$ yardr depot source yardr flow depot
KIND   NAME    SOURCE
flow   default depot (.yardr/flows/default.flow@add63e5)        pack starter has flow starter.default
stage  new     depot (.yardr/flows/default/new.md@add63e5)
stage  review  depot (.yardr/flows/default/review.md@add63e5)
the flow switched whole: its stage files are the ones above now
```

| command | `depot` | `pack` |
|---|---|---|
| `yardr depot source <depot> role <group> depot\|pack` | the group's `prompt` becomes `roles/<name>.md`, which resolves to the depot's `.yardr/roles/<name>.md` on the base | it becomes the pack's installed copy, `<home>/packs/<pack>/roles/<name>.md` |
| `yardr depot source <depot> role <crew member> depot\|pack` | the crew member's `prompt` becomes the absolute path of `.yardr/roles/<name>.md` in the depot's checkout | as for a group |
| `yardr depot source <depot> flow [--type <type>] depot\|pack` | the pointer of the depot, or of the type, is cleared: `.yardr/flows/default.flow`, or `type/<type>.flow`, governs | the pointer is set to the flow the pack's import pointed it at |

- The role is the one the prompt names now, by its file name (`builder.md`
  of `roles/builder.md` or of `~/.yardr/packs/starter/roles/builder.md`). A
  prompt that names no role is refused: set it yourself (`yardr group
  add`, `yardr crew add`).
- A crew member is the yard's and reads its prompt as a path, never by a
  lookup in a depot. Its depot's role is therefore the file in the depot's
  checkout as it is on disk, not at the base commit, and the line has no
  commit. A running crew member keeps its session; the output names the
  restart.
- A flow switches whole: the stage files come from where the flow does, the
  depot's from beside its flow file and the pack's from the yard's home. No
  single stage file can be switched.
- Refused, changing nothing, when the chosen source does not exist: the depot
  has no such role or flow file on its base (in its directory, for a dir
  depot), no pack is imported into the depot, or none has the role or a flow
  for the depot or the type. A type's flow cannot be the depot's file while the
  depot itself is pointed at a flow, since that pointer stands in front of
  every flow file: switch the depot's flow first.
- A flow change is refused as `yardr depot flow` is, naming the beads, while
  an open bead sits in a stage the new flow lacks
  ([flows.md](flows.md#named-flows-and-pointers)).
- Switching to where the object already comes from says `unchanged`.
  `--json` prints `{"depot", "changed", "lines", "notes"}`, the lines as
  `depot sources --json` has them.
- The pack's record still holds the import's choice of roles
  ([packs.md](packs.md#how-a-pack-maps-to-the-yard)), so the next `yardr
  pack apply` sets every role by it again. The output has a note when that
  undoes the switch; give that import `--depot-roles` or `--pack-roles` to
  make the choice the record's. An import also points the depot at the pack's
  flow again.

`yardr depot flow` points a depot at any named flow, not only a pack's, and
`yardr pack apply --depot-roles` or `--pack-roles` chooses every role at once.

## Repo config comes from the base branch

This section is about git depots; a dir depot has no base and reads `.yardr/`
from its directory (see [Depot kinds](#depot-kinds-git-and-dir)).

yardr reads `.yardr/` from the tip of the depot's base branch
(`git show <base>:.yardr/...`), never from the bead's branch or worktree
(built). A bead therefore cannot lower its own bar: a builder who edits
`.yardr/check` on `yard/<bead>` changes nothing for that bead. The edit takes effect only once it has been reviewed
and landed on the base, like any other code, and then applies to every bead
after it. The same holds for the flow and the roles of a depot that keeps
its own under `.yardr/`. A depot set up from a pack has neither there: its
flow is in the store and its roles in the yard's home, where no branch
reaches them, and they change when the operator applies the pack again
([why](#why-the-repo-holds-what-the-work-is)). yardr resolves the base on every check and re-reads the files
when its commit moves.

For the gate, "the base" is the remote's copy when there is one. Refs are
shared by every worktree of a depot, so a bead could `git update-ref` the
local `main` to a commit whose `.yardr/check` passes. When the base has a
remote (`--base origin/main`, or a local branch whose upstream, or else
`origin`, has it), `yardr depot check` fetches that branch into its
remote-tracking ref first (forced, bounded to 60s, never prompting) and
reads `.yardr/check` from `origin/main`, not from the local branch. A fetch
that fails leaves `origin/main` as last fetched; only a depot with no remote,
or whose remote lacks the branch, reads the local base. The result says
which was used (`--json`: `from`, e.g. `origin/main (fetched)`, `main (no
remote)`). A gate change therefore applies once it is on the remote: with
merge-local that is when `YARDR_PUSH` has pushed it.

### The merge gate: `.yardr/check`

`.yardr/check` is a script; exit 0 means green. yardr writes the base's copy
to a temporary file under the depot's git directory and runs it in the
directory being checked (a script without `#!` runs with `sh`; the depot's
name is in `$YARDR_DEPOT`). A store override, `yardr depot add <depot> <path>
--check '<cmd>'`, runs with `sh -c` instead. Re-running `depot add` without
`--check` drops the override. Every `depot add` logs a `depot_updated` event
with the depot's check, `check_light` and `allow_no_check`, so an override never changes
unseen, and `yardr depot show <depot>` prints it.

A depot with neither fails its gate with "no gate": a missing file must not
read as a passing one. `depot add ... --allow-no-check` opts a depot out, for a
repo that has nothing to check.

A dir depot's `.yardr/check` is read from its directory and run where it is
(as is when executable with `#!`, otherwise with `sh`), in `--dir`, which
defaults to the depot's directory. A dir depot without one passes with "no
gate": nothing of it merges anywhere, so there is no bar to lower.

```
yardr depot check yardr                                     # in the depot
yardr depot check yardr --dir ~/.yardr/worktrees/yardr/yardr-k3f9   # in a bead's worktree
```

prints the tail of the output (`--tail N`, default 40; a red gate whose
tail is empty says so, or how many lines it hid) and pass or FAIL; `--json`
gives the full output, the exit code and the source (`store`, `repo@<sha>`,
`dir` for a dir depot's own file, or empty for a depot with no gate). It exits 0 on a green gate, 1 on a red one
(no gate and a timeout included), and 2 when the gate could not run at all:
an unknown depot, a base that does not resolve, git failing, no
[gate slot](#gate-slots-how-many-gates-run-at-once) within `--slot-wait`. A check ended by
a signal from outside (its process killed, or its own exit status 128+N, as
a shell reports a killed command) exits 128+N, which the assembly runs once
more instead of sending the bead back. A red gate's whole output is kept
in a file that the FAIL line names, and its lines that say what failed are
printed before the tail when the depot has a pattern for them
([below](#a-red-gates-output-the-file-and-yardrcheck-fail-pattern)). It also fails,
naming them, while open beads of the depot sit in a stage their flow lacks
(with a green gate that is exit 2, since no branch can fix it; see
[flows.md](flows.md#a-base-change-that-drops-a-stage)). The check is
killed, with every process it started, after `--timeout` (default 30m), and
each git call that finds it is bounded (30s; the fetch, 60s).
Builders run it before handing on, and the assembly runs it too:
[`contrib/assembly/merge-local`](../contrib/assembly/merge-local) runs
`yardr depot check` on exactly what will land, after any rebase, and sends a
red gate back to `new` (see [pipeline.md](pipeline.md#4-approved-the-assembly-lands-it-on-main)).

yardr's own CI (`.github/workflows/ci.yml`) runs the same script, on pushes
to main and on pull requests. CI runs on ubuntu only; macOS is covered by
the assembly's gate on the yard's own Mac at every landing (decided 3 Oct,
see yardr-msjd).

The gate runs the branch's own code, tests included: it is fixed by the
base, but what it runs is not. A branch that deletes or weakens its tests
passes vacuously. That is the accepted model for now; the reviewer is the
one who catches it (see [pipeline.md](pipeline.md#what-the-gate-does-not-catch)).

### A red gate's output: the file and `.yardr/check-fail-pattern`

The tail of a long run is mostly what came after the failure: with `go test
./...` the failing test's lines are followed by the `ok` of every package
after it. So `yardr depot check` does two more things for a gate that did not
pass (red, timed out, ended by a signal; the light gate's too), whoever ran
it: the assembly, a builder, a reviewer.

**It keeps the whole output**, in
`<home>/gates/<depot>/<dir>-<UTC time>.log` (`.light.log` for the light gate),
mode 0600, where `<dir>` is the last element of the directory the check ran
in: a bead's worktree is named after the bead, so the file is too. The FAIL
line ends with it:

```
check yardr (repo@3cb4b79 from main (no remote)) in /…/yardr-k3f9: FAIL (exit 1); whole output: /…/.yardr/gates/yardr/yardr-k3f9-20261005T141500Z.log
```

`--json` has it as `log_file`. A gate that passed keeps nothing: nobody is
sent back to read it. Every run that keeps a file removes the depot's kept
outputs older than 14 days and, of those for the same `<dir>`, all but the
newest 5; the count is per directory, so a builder running a red gate over
and over does not push out the file another bead's note names. A file that
cannot be written does not change the verdict: the line says why instead.

**It prints the lines that say what failed, before the tail**, when the depot
says how to tell them. yardr knows no language; the depot gives
`.yardr/check-fail-pattern`: regular expressions (Go's syntax), one per
line, a line of output matching when any of them does. Blank lines and lines
starting with `#` are skipped, space around a pattern is dropped, and a line
that is not a regular expression is named in a note and skipped. Every
matching line is printed with the 2 lines before it and the 8 after, each
line once (stretches that touch are one, and a matching line already shown
as a match opens no stretch of its own), 150 lines at most, with a count of
the matching lines left out. Each stretch is headed by the number of its
first line in the output, `-- line 812`, which is where to look in the file.
Then come `the last N lines:` and the tail. A depot without the file gets the
tail alone, as before, and the kept file.

The pattern is read where the repository's gate is: at the tip of the base
for a git depot (so it counts once it has landed), in the directory for a dir
depot. It has no store override: under `--check` it is still the repository's,
at the base as it resolves locally. It serves both gates, and it only
chooses what is shown first: it cannot make a check pass or fail.

Depot yardr's [`.yardr/check-fail-pattern`](../.yardr/check-fail-pattern)
names what go prints for a failure: `--- FAIL`, `^FAIL`, `panic:`, `DATA
RACE`, the gate's own `gofmt needed`, and the `file.go:line:col:` of vet and
the compiler.

### Gate slots: how many gates run at once

A full gate is heavy (yardr's own takes about five of ten cores for five
minutes), and the groups' limits do not bound how many run together: five
builders and three reviewers can be eight gates, each several times slower,
with the tests that time something failing. So `yardr depot check` takes one
of the machine's **gate slots** before the check starts and gives it back
when the check ends, however it ends. With none free it waits, and says so
on stderr:

```
yardr: 2 gates are running on this machine; waiting for a slot
yardr: got a slot after waiting 3m12s
```

| what | how |
|---|---|
| how many | the yard's `gate_slots` setting, default 2; `0` is no limit. `YARDR_GATE_SLOTS`, when set, is used instead of the setting (one run told otherwise, a script, a test). |
| whose | the machine's, for every yard of the same user: a builder, a reviewer, the assembly, the integrator and a person at a terminal all take from the same slots, and so does another yard, and an isolated yard ([contrib/isolated-yard](../contrib/isolated-yard/README.md)). |
| where | lock files `slot-1` to `slot-N` in `~/.local/state/yardr/gates`, with `~` the home the user database names, not `$HOME`: a yard run with another `HOME`, `XDG_STATE_HOME` or an emptied environment finds the same directory. `YARDR_GATE_DIR` names another one (tests). A user the database does not know gets `/tmp/yardr-gates-<uid>`. |
| never leaks | a slot is a `flock` held by the `yardr depot check` process, and by nothing it starts. The kernel drops it when that process ends: a pass, a failure, the timeout, a signal, `kill -9`. The files stay; they are a few bytes saying who held each last. |
| the timeout | `--timeout` starts when the check starts. The wait has its own bound, `--slot-wait` (default 30m); past it the check could not run and `depot check` exits 2, which the assembly holds on. It is never a red gate. |
| who goes first | `--first` (or `YARDR_GATE_FIRST=1`, which the assembly scripts set): the runs already waiting, and one that only arrives, stand back for it. Among the others no order is kept; each looks five times a second. |
| the light gate | takes no slot: it is meant to be cheap. `yardr depot add --light-slot` says a depot's light gate takes one. The full gate run under `--light` for want of a light one takes one. |
| a gate inside a gate | a check under a slot has `YARDR_GATE_SLOT=1` in its environment, and a `yardr depot check` started with it takes none: a gate whose tests run gates would otherwise wait on itself. |
| who sees it | `yardr status` has a line `gates  2 running, 1 waiting (limit 2)` while any gate of the machine runs or waits and the yard has a limit, followed by the yard's own beads with a gate, light ones too: `; of this yard: yardr-k3f9 gating 6m (full), yardr-2abc waiting 1m (full)` ([whose they are](#session-limits-and-outages)). `depot check --json` has `slot_waited_seconds`. |

Yards with different limits share the lower slots: a run with limit N takes
one of the first N, so the highest limit among them is what holds for the
machine. Give them the same.

The assembly goes first because its gate is the one every approved bead
waits behind, one at a time, while the gates of builders and reviewers come
many at once: without it the merge queue would stand in line eight to one.
A builder's wait holds up one bead.

The slot is not held by the check's own processes on purpose. A `depot check`
killed with `kill -9` leaves its check running until that ends by itself,
and for that time the machine carries a gate the slots do not count; held
by the check too, a test that left a process behind would keep a slot for
good.

### The light gate: `.yardr/check-light`

A depot may have a second, lighter gate beside `.yardr/check`, for where the
full gate still stands before the base moves. Builders and reviewers check
their work with it, and the full gate runs once, on what lands
([pipeline.md](pipeline.md#one-full-gate-per-bead)); a wagon of a train
([pipeline.md](pipeline.md#wagons)) also lands under it, on its train's
branch, where the full gate runs on the whole train.
`yardr depot check <depot> --light [--dir <worktree>]` runs it.

It is found and run exactly as the full gate is: `.yardr/check-light` at the
tip of the base (the fetched remote's when the base has one), never from the
branch or the worktree; the store override is
`yardr depot add <depot> <path> --check-light '<cmd>'`, logged with the depot's
other settings and shown by `depot show`; the same `--timeout`, the same exit
statuses (0, 1, 2, 128+N for a check ended by a signal). A dir depot's is
read from its directory.

A depot **without** a light gate runs its full gate under `--light`, and the
first line of the output says so (`depot r has no light gate (no
--check-light, no .yardr/check-light on main (no remote)): running the full
gate`; `light_fallback` in `--json`). `--light` never passes for want of a
file, and a depot with no gate at all still fails with "no gate". The light
gate comes from where the full one does: a depot whose gate is a store
override (`--check`) has its light gate in the store too, and a
`.yardr/check-light` in its repository is not read.

Both gates get `YARDR_CHECK_BASE` in their environment: the commit the
repository's gate was read at, which is the tip of the depot's base as
fetched. A check can so look at what the branch changed, from where it left
the base: `git diff --name-only "$(git merge-base "$YARDR_CHECK_BASE" HEAD)"`.
Under a store override it is the depot's base as it resolves locally (nothing
is fetched for an override); it is unset when that does not resolve, and for
a dir depot, which has no base. A check that needs it must fail without it:
depot yardr's does.

The light gate of a **wagon of a train** is compared with the train's branch
instead: `YARDR_CHECK_BASE` is the tip of `yard/<train>`. From the depot's base
the range would hold the train's earlier wagons, so after one wagon touched a
package nearly everything imports, every later wagon's light gate would test
nearly everything again. `depot check --light` finds the wagon by itself: when
`--dir` is on branch `yard/<bead>`, and that bead is a wagon of the depot and a
child of a train, it reads the train's branch as `--dir`'s repository has it.
So a builder's run in its worktree compares with the train's branch as it is
there, and merge-local's with the branch it has just rebased the wagon onto,
with no flag for either to forget. Only what the gate is compared with
changes: the gate itself is still read from the depot's base, and the train's
full gate runs everything before the base moves. Everything else is compared
with the depot's base as before: the full gate always (the one run under
`--light` for want of a light gate too), and the light gate of a bead with
no train, of a child of a train that is not a wagon (it lands under the full
gate), of a train whose branch is not there, and of a run in the depot itself
or on a branch that is no bead's.

A light gate's output starts with what it is compared with:
`light gate, changes since yard/<train> at <sha>`, or
`light gate, changes since origin/main at <sha>` (`base_ref` and `base` in
`--json`).

Depot yardr's [`.yardr/check-light`](../.yardr/check-light) runs `gofmt -l`,
`go vet ./...` and `go build ./...` on everything, then `go test -race` on
the packages that changed and on every package that imports one of them,
however far up: a change breaks its importers as easily as itself. One
`go list -test ./...` gives both, since each package's `Deps` is already
transitive and counts its test files' imports: a package is tested when it
changed or one of its `Deps` did. A package changed when a `.go` file in its
directory did, or a file it embeds (`go:embed`: a role, merge-local), which
is compiled in like one. A changed `_test.go` file runs its own package
only, as nobody imports it. Every package is tested when `go.mod` or
`go.sum` changed.

A file a test reads by path (`os.ReadFile("../../packs/starter/pack.yaml")`)
is in nothing `go list` prints, so the script starts with a table of them: a
path, then the packages whose tests read it, or `-` when none does.

```text
packs/starter/                 internal/pack internal/cli
internal/hooks/testdata/       internal/hooks internal/dispatch
docs/                          -
```

A path ending in `/` is everything under it; any other is that one file.
Every row a changed file matches adds its packages' own tests (not their
importers), and the output says so: `light gate:
packs/starter/roles/builder.md changed: read by the tests of internal/pack
internal/cli`. A changed file that is not `.go`, not embedded and in no row
tests every package: `light gate: contrib/x.txt is read by no known
package: testing everything`. So a missing row costs time, and a new test
that reads a file by path gets a row; a `-` row (docs, the depot's own roles,
CI's workflows) is a promise that no test reads the path. A row naming a
directory that is no package fails the gate. A package may be `.`, the
root: its test reads every `.go` file of the repository for the name of a
role's file (`main_test.go`), so the rows for `contrib/`, whose programs
the binary does not import, name it. `YARDR_CHECK_PLAN=1` makes the
script say what it would test and stop, which is how
`internal/rigcheck`'s test of the table runs it.

What it costs follows from the import graph. A change in a leaf package
takes well under a minute; a change in a package nearly everything imports
(`internal/store`, `internal/config`) tests nearly everything, which is
right, and is then close to the full gate without its second run of three
suites over the socket. In this repository `internal/cli` imports almost
every package, so most changes to non-test Go code run its tests. The table
adds little to that: a change under `packs/starter/` already runs
`internal/cli` (it imports the embedded packs), and its row adds
`internal/pack`, about 6 seconds with the race detector.

## Briefs are not config

The herdr runner writes each agent's brief outside the tracked tree, under
the worktree's git directory (`git rev-parse --git-path yardr`, i.e.
`<repo>/.git/worktrees/<bead>/yardr/brief-<session>.md`), and points the
agent at it by absolute path. Nothing generated per session sits next to
committed files, so `.yardr/` holds only config. (Worktrees from before
this kept a `.yard/` that ignores itself; it is harmless.)

A dir depot has no git directory to hide a brief in, so its briefs go to
`<dir>/.yardr/briefs/brief-<session>.md`, a folder that ignores itself
(for a dir depot inside a checkout); each is removed once its bead closes.

## yardr's own state

Everything yardr keeps for itself lives in one directory, the yard's
**home**: what `--home <dir>` names, else `YARDR_HOME`, else `~/.yardr`:

| path | what |
|---|---|
| `yard.db` | the store (bbolt); back it up with `yardr store backup <path>` while it serves (one read transaction, so the copy is the store as of one commit); restore by stopping the daemon and the server and copying the backup over it |
| `yard.sock` | the server's unix socket |
| `server.pid`, `server.log` | the running server |
| `hooks.state` | what the running server takes webhooks on, or why it does not, which `yardr doctor` and the pack plan read ([below](#settings)); gone when the server stops |
| `peer.key` | the yard's private key, by which the yards it is linked with know it ([peers.md](peers.md#the-key)); made by the first `yardr peer key`, `peer invite` or `peer pair`, mode 0600, and refused when it is more open |
| `daemon.lock` | held by the running daemon, with its pid in it: one daemon per yard |
| `daemon.log` | what a daemon started in the background prints (the onboarding's "Start the yard now?", `yardr init --start`); one in a pane of its own prints there instead |
| `web.log` | what a web view no terminal runs prints: one started with `yardr web start`, or the login service's ([web.md](web.md#keeping-it-running)) |
| `webviews/` | a record per running web view, held by it while it serves, which `yardr status`, `yardr doctor` and `yardr web stop` read ([web.md](web.md#where-it-runs)) |
| `bdp.log`, `otel.log` | the same for `yardr bdp serve` and `yardr otel export` when no terminal runs them ([below](#long-running-commands)) |
| `bdpservers/`, `otelexports/` | a record per running BDP server and telemetry export, as `webviews/` holds the views', which their `stop` and `status` read |
| `otel-export.json` | what `yardr otel export` has sent, where the next run continues ([otel.md](otel.md#what-it-remembers)) |
| `logs/sessions/yard-<session>.log` | what the command of an exec group's session printed, for a group that sets no `log` ([groups.md](groups.md#runners)); nothing prunes them |
| `worktrees/<depot>/<bead>` | herdr bead worktrees |
| `depots/<name>` | depots registered with `depot add --clone`; `depot rm` leaves the clone here |
| `packs/<pack>/` | a pack's roles and scripts as an import installed them, filled with the yard's names ([packs.md](packs.md#how-a-pack-maps-to-the-yard)): what the groups and crew members of a depot set up from the pack read. `yardr init` installs the yardmaster's role here, from the default pack, before any depot |
| `flows/<pack>.<flow>/` | the stage files of a pack's flow, installed the same way |
| `roles/` | only in a yard from before `yardr init` took the yardmaster's role from a pack: the generic role prompts that init installed (yardmaster, builder, merger, reviewer). Still the last place a group's relative `prompt` is looked up, yours to edit, and no longer written or refreshed; `.shipped.json` there is no longer read |
| `linear.secret`, `github.secret`, `crew/<name>.env`, `groups/<group>.env` | credential files, if you keep them here as the examples do ([crew.md](crew.md#outbound-credentials)): yours, yardr creates none of them and reads only the paths a hook, crew member or group names |

`--home <dir>` is a flag of every command, and runs it on the yard in that
directory, so a second yard takes no variable:

```
yardr --home /tmp/y1                          # in a terminal, on a home with no yard: the onboarding form
yardr --home /tmp/y1 init --harness claude    # the scripted way
yardr --home /tmp/y1 bead list
```

The flag wins over `YARDR_HOME`, and the variable over the default. Keep
the path short: unix socket paths are limited to about 100 bytes, and yardr
refuses a home whose `yard.sock` would be longer, before it creates
anything there. A relative home, from the flag or the variable, is made
absolute once, when the command starts, and every process the command
starts gets it as `YARDR_HOME`: the server, a daemon, the agents and the
gate, which run elsewhere, are in the yard it named and not in one relative
to their own directory. So is a login service installed with the flag
([below](#login-services)).

Where yardr prints a command for a yard that is not in the default home
(what to run next in the setup, a fix in `yardr doctor`), the command
carries `--home <dir>`; for the default home it carries neither the flag
nor the variable.

Two yards on one machine share nothing but what they are given in common:
give each its own `hook_addr` ([settings](#settings)) and its own web
address ([web.md](web.md)). Their herdr agents are told apart by the yard's
`name`.

The server is started by the first command that connects to a yard with no
server, and serves every command after it. It gets none of that command's
environment or working directory: the first command is as often an
agent's, in a pane holding the credentials its `env_file` set and in a
worktree, as the operator's. It starts with `PATH`, `HOME`, the settings
the server reads (`YARDR_HOOK_ADDR`, `YARDR_EVENT_RETENTION`,
`YARDR_HOOK_PAYLOAD_RETENTION`) and
`YARDR_HOME` set to the home it serves, and runs in that home. So
`YARDR_HOOK_ADDR` reaches the server only from the environment of the
command that happened to start it; the `hook_addr` setting is the yard's
own and needs no such luck. What the yard starts for work does not inherit
those three settings: a session (an exec group's command, an agent's pane)
and a gate's command (`yardr depot check`) are started without them, so a
server started from one, by a test or a trial, does not listen on the
yard's webhook address. The daemon
is not started this way: it runs in a pane of its own with that pane's
environment, or in the background, started by the onboarding or
`yardr init --start` ([below](#starting-the-yard)), with the environment of
the command that started it.

Neither outlives its home. A server and a daemon are detached from whatever
started them, so a home removed under them (a temp directory of a test, a
yard deleted with `rm -rf` before its processes were stopped) used to leave
a process no command could reach. Each now looks once a minute whether the
home directory is still the one it started in, and whether the file it made
there is still its own: for the server `yard.sock`, for the daemon
`daemon.lock`. A home that is gone, a directory of the same name made in its
place, a socket or lock that is missing or another process's: it logs one
line (`the yardr server's home is no longer its own; stopping`, with the
reason) and stops, with exit status 0, so a login service does not start it
again. A server that stops this way removes nothing that is not its own: a
socket another server put at that path stays, and `server.pid` is removed
only while it still names this server. So a throwaway home needs no
cleanup beyond removing it, and the processes are gone within the minute;
`yardr --home <dir> daemon stop` and `server stop` before the `rm` are
still the way to have them gone at once.

`yardr depot rm <name>` takes a depot out of the store with the routes that
match it (`--depot <name>`) and its flow pointers, logging one `depot_removed`
event. It is refused, naming them, while the depot has open beads or live
sessions; closed beads keep the depot's name as history. Nothing on disk
changes: the clone, its worktrees under `worktrees/<depot>/` and its `.yardr/`
stay where they are for you to remove.

### Creating a yard: the onboarding and `yardr init`

A home is set up once its store has the `default_kind` setting, which init
writes last, or once it has any depot, group, crew member or bead: a yard from
before the onboarding is in use, and onboarding it would refuse its agents'
commands. Opening such a store writes `default_kind` for it, from the first
herdr group's kind, else the first herdr crew member's, else `claude`, with the
event `settings_migrated` (the `operator` and `yardmaster` settings stay
unset; installed role prompts keep the name they have). Opening a store whose
`operator` setting names nobody's user adds that user ([users.md](users.md)),
unless a crew member has the name. Only an empty store onboards. The store file
alone is no marker: the server creates `yard.db` on the first command that
connects, whatever it was, so a home can hold an empty store nobody set up.
Any yardr command on a home that is not set up, bare `yardr` included (help
otherwise; `--help` is always help), runs the onboarding when it has a
terminal: the default harness (every kind herdr starts, marked installed when
its executable is on PATH, the first installed one preselected), the
yardmaster's name (default `yardmaster`), the directory its shell lives in
(default the home's parent, so `~` for `~/.yardr`), and an optional model, and
your name for the role prompts, then "How should agents work with you?" and
"Anything agents should know about you?" ([below](#how-agents-work-with-you)),
then "Create this yard?" and "Start the yard now?"
([below](#starting-the-yard)). Before the first page, when `git` or `herdr` is
not on PATH, it names each and what it is needed for (herdr starts every
agent, the yardmaster first; a depot is a git repository) and asks whether to go
on: the answer starts at stop when herdr is missing, and stopping creates
nothing. Without a terminal it prints how to initialize and exits 2.
`yardr server`, `yardr init`, `yardr daemon`, `yardr settings` and
`yardr help` never ask.

`yardr init [--harness <kind>] [--yardmaster <name>] [--dir <path>] [--model <m>] [--operator <name>] [--style <choice>] [--about <text>] [--start [--wait <duration>]]`
does the same without the form, for scripts and tests; `--harness` is required
to create a yard, and nothing is started unless `--start` says so. Either way
yardr:

1. installs the yardmaster's role: the one the yard's default pack gives its
   crew member called `yardmaster`. The default pack is the `default_pack`
   setting, and `starter`, the pack in the binary, on a new yard. That one
   file goes to `<home>/packs/<pack>/roles/`, where an import of the pack
   installs its roles, so the first depot set up from the pack finds it
   there. Where the role says `{{operator}}`, the person the yardmaster
   brings decisions to, init writes the operator's name (the form's "Your
   name", `--operator`, default the login name, or `operator` where there is
   none), and where it says `{{yardmaster}}`, the yardmaster's. The other
   roles of the pack are installed when a depot is set up from it
   ([below](#seeding-a-depot-yardr-depot-init)). A default pack with no crew
   member called `yardmaster` gets the yard none: init says so, and step 3
   adds nothing. `<home>/roles/`, which init wrote before a yard had a
   default pack, is not written;
2. sets `operator` to the operator's name and `yardmaster` to the
   yardmaster's, and `operator_style` and `operator_note` when they were said
   (the form's answers, `--style`, `--about`);
3. adds the yardmaster crew member: `kind=<harness>`,
   `prompt=<home>/packs/<pack>/roles/yardmaster.md`, the model if given, the harness's
   unattended flags as `args`, rotating every 6h. A crew member of that name
   already there is kept as it is, so init runs again on a yard without harm;
4. sets `default_kind` to the harness, which marks the yard set up.

Init again on a yard that is set up changes only what its flags say: the
`default_kind`, `operator` and `yardmaster` settings stand in for `--harness`,
`--operator` and `--yardmaster` when those are not given, rather than being
reset to the flags' defaults (the login name, `yardmaster`); `operator_style`
and `operator_note` are kept the same way, changed only by `--style` and
`--about`. The summary lists what was kept
(`kept default_kind claude, operator Jo, yardmaster yardmaster, as set before`).
A crew member of the yardmaster's name is kept as it is, and init then reads
no pack and writes no role: an installed role is brought to a newer pack's
by `yardr pack apply`, which shows the change first, and a yard's
`<home>/roles/` from an earlier yardr is left as it is
([upgrading](#upgrading)). What a run that failed leaves, and how the
next one finishes it, is [below](#a-run-that-did-not-finish).

#### How agents work with you

After "Your name" the form asks "How should agents work with you?", each
choice with its meaning on the page, starting at "Show me":

| choice | `operator_style` | what it tells an agent |
|---|---|---|
| Guide me | `guide` | Explain each step and why, in plain words, before taking it. Show no raw commands unless asked: say what a command does instead. Ask before anything done on the operator's behalf. |
| Show me | `show` | Explain briefly. Show the command being run. Ask when there is a decision to make. |
| Work with me | `work` | Be terse. Assume the operator reads code and git. Decide small things and say what was decided. |
| Just report | `report` | Decide what is the agent's to decide. Lead with the result. Come to the operator only for decisions that are theirs. |

and then, optionally, "Anything agents should know about you?": one line.
`yardr init --style <choice> --about <text>` says the same without the form;
a style that is none of the four is refused with their names, before
anything is created. `yardr init` without `--style` stores no style: only
the form has a default.

The answers are the settings `operator_style` and `operator_note`
([Settings](#settings)). They are read when a session starts and are not
written into the role files: `yardr prime --bead <id>` prints them after
the bead, as

```
OPERATOR Jo:
  Jo asked agents to work with them in the style `work` ("Work with me"). In what you write to Jo and what you bring to them: Be terse. ...
  What Jo wants agents to know: I review every merge myself
```

and `yardr prime --crew <name>` after the role, under `## The operator:
Jo` (`--json`: `operator`, with `name`, `style`, `label`, `tells`, `note`).
So `yardr settings set operator_style report` reaches every session that
starts after it, with no init and no edit of a role a depot committed; a
session that is running keeps what it read. The instructions are about what
an agent writes to the operator and brings to them, not about how it does
its own work in a pipeline.

A yard without either setting, which is every yard from before them, primes
as it did: no section, and no style assumed. With only the note set the
section has the note and no style. `yardr setup` uses the style in one way:
with `guide` its pages print no command lines ([setup.md](setup.md#the-pages)).

The summary ends with the way to the yardmaster
([starting the yard](#starting-the-yard)), whose role begins with a first-run
checklist. The yardmaster reads `yardr doctor` ([doctor.md](doctor.md)), whose
setup steps say what is done, and goes on from the first one that is not, with
the operator, by mail in `yardr chat` when the onboarding took them there
([chat.md](chat.md#from-the-onboarding)) and in its pane otherwise: the
repository and its base branch (`yardr depot add`), `yardr depot init`
([below](#seeding-a-depot-yardr-depot-init)), which sets the depot up from
the yard's default pack (its groups that build, review and land, and the
routes for `new`, `review` and `approved`), and a first bead to try the
pipeline. `yardr init --help` lists the same as commands, for scripts.

The unattended flags keep an agent nobody watches from stopping at a
permission prompt: claude `--permission-mode auto`, codex
`--dangerously-bypass-approvals-and-sandbox`, kimi `--yolo`; any other kind
gets none and may ask (`yardr group add --help` lists them too).

#### Starting the yard

The daemon starts the yardmaster. There are four ways to a running daemon:

- the form's last question, "Start the yard now?", yes by default;
- `yardr init --start`, the same without a terminal, on a new yard or one
  that is set up (init again changes nothing its flags do not name);
- `yardr daemon` in a terminal pane of its own, where you watch what it
  does. The form's "No" and plain `yardr init` leave it at that and print
  the command;
- `yardr daemon install`, which has the system start it now and at every
  login ([below](#login-services)). The other three do not come back after
  a reboot.

The first two start `yardr daemon` detached, in the home, logging to
`<home>/daemon.log`, unless a daemon holds `<home>/daemon.lock` already: a
running daemon is left alone. `yardr daemon start` is that start alone,
without the wait for the yardmaster ([below](#long-running-commands)), and
says so when a daemon runs already. Unlike the server, this daemon keeps the
environment of the command that started it (herdr's socket, the PATH an exec
group's command runs with, as a daemon in a pane has its pane's), without
what names the starter (`YARDR_ACTOR`, `_BEAD`, `_SESSION`, `_STAGE`,
`_GROUP`, `_CREW`) and with `YARDR_HOME` set to the home.

`yardr daemon stop` ends the daemon of the home, however it was started: it
signals the pid in `<home>/daemon.lock` and waits (`--wait`, default 30s)
until the lock is free, since the daemon finishes the pass it is in. The
sessions it started carry on in herdr; nothing is dispatched until a daemon
runs again. `yardr daemon status` says whether one runs, with its pid, and
whether the login service is installed and loaded.

Before they start the daemon, in a terminal, they ask "Check that <harness>
answers?", with the yardmaster's harness: "Check" (the answer it starts at) or
"Skip". Check makes one real call to the harness outside a pane, the one
`yardr doctor --try` makes ([doctor.md](doctor.md)); it costs a model call on
your account with the harness, which is why it asks. A harness that answers
gets one line with its answer, and the start goes on. One that does not is
named before any pane exists: how the call ended, the last lines the harness
printed and, where those tell, what that usually means (it is not logged in;
it does not know a flag yardr calls it with). Then:

- "Try again", the answer it starts at: set it right in another terminal
  (log the harness in, say) and choose it; the call is made again;
- "Go on anyway": the start goes on with the harness as it is;
- "Stop here" (and ctrl+c, at either question): nothing is started. The yard
  stays, and it prints how to come back: `yardr init --start`, or bare
  `yardr` in a terminal, which asks again.

Skip makes no call. Nothing is asked without a terminal, with `--json` and
in an agent's session, for a kind yardr knows no one-shot call for (doctor's
"not tried"), for a harness that is not on PATH, for a yardmaster that runs
already or is stopped, and when a daemon runs already, since that one starts
the yardmaster whatever would be answered. `yardr setup` does not ask: it starts
no yardmaster.

They then wait for the yardmaster, up to `--wait` (default 2m; the form always
waits that long), with a line that counts on a terminal and one line on
stderr otherwise. A session that exists is not enough: a harness can be on
PATH and sit dead in a running session (a binary behind a system dialog
nobody sees, a CLI at "Not logged in"), and herdr reads any process of the
harness's name as an idle agent. The yardmaster counts as up once its first
prompt was delivered and it answered: herdr reports the agent working, or
the pane shows output below the prompt.

When the yardmaster is up, yardr prints where it is, as `herdr agent list` and
`yardr crew show <yardmaster> --json` name it: the agent
(`<yard>-<yardmaster>`), the workspace's label (`<yard> <yardmaster>`) and id,
the tab and the pane; and how to get there: `herdr agent focus <agent>` brings
its pane to the front in herdr, `herdr agent attach <agent>` opens it from any
other terminal. The form then asks how to go on with the rest of the setup:
"Talk to the yardmaster" or "Keep going here" ([setup.md](setup.md)). The
first opens `yardr chat` on the yardmaster in the same terminal
([chat.md](chat.md#from-the-onboarding)); the second is `yardr setup`, in the
same run. `yardr init --start` in a terminal does not ask that and opens the
chat.

The yardmaster `yardr init` adds has `greet=<operator>` in its config. The first
prompt of a crew member with that key, in its first session only, asks it to
greet that user by mail (`yardr mail <operator> "…"`) and to say what it
needs for the next setup step, before it points at the prime as every
crew member's first prompt does. The chat shows mail and nothing of a pane, so
this is how the yardmaster's first word gets there. A later session (a rotation,
a restart) is asked for no greeting.

No chat opens without a terminal, with `--json`, in an agent's own session
(`YARDR_SESSION` or `YARDR_CREW` is set) and with `--no-chat`: yardr then
ends where the yardmaster's place is printed, and the lines after it name
`yardr chat`. Nor does one open when who runs the command is no user of the
yard (the operator was given another name than the login name): the yardmaster's
mail goes to the operator, so yardr says who the chat is for and how to open
it as them (`--actor <operator>`), and the form's "Talk to the yardmaster" then
brings the yardmaster's pane to the front as `--no-chat` does: herdr focuses a
pane for any process that asks over its socket, and in a terminal outside
herdr yardr adds that herdr has to be opened to see it.

When the yardmaster is not up, yardr says why the wait ended (the time is up;
the daemon gave up after five failed starts; a start failed with herdr or the
harness not on PATH, which no waiting brings; the daemon ended; the yardmaster
is disabled), what is known (which program is missing, the reason of the last
failed start, the last lines of the yardmaster's pane, where the daemon logs)
and how to go on. `yardr init --start` then exits 1. The form opens no chat,
since nobody is there to answer in it: it prints what
[`yardr doctor`](doctor.md) says of the yard (its `FAULT` lines and the
yardmaster's own line, and for a session that runs without an answer the note
`<yardmaster>'s session runs, and it has not answered its first prompt`), and
goes on to the command it was started for. `--json` carries the same under
`start`: `daemon_started`, `daemon_log`, `running`, `yardmaster` (`agent`,
`workspace`, `workspace_id`, `tab`, `pane`), `pane`, `chat` (the command for
the conversation with the yardmaster, once it is up), `problem`, `known`,
`next`.

A yardmaster whose session runs and that has not answered its first prompt may
have a harness that waits at something yardr does not answer: a login, a
dialog, a question. Where a chat would open (a terminal, no `--json`, no
`--no-chat`, not an agent's session), yardr then says why the wait ended,
shows the last lines of the yardmaster's pane and asks "Open the yardmaster's
pane" or "Not now", before anything else of the above. "Open the yardmaster's
pane" runs `herdr agent attach <agent>` in the same terminal, after a line
that says how to come back: `ctrl+b q`, herdr's keys for leaving a pane it
attached, which stop nothing. Back from the pane, yardr waits for the
yardmaster again, for as long as the start did. A yardmaster that is up now is
one the start found up: the chat opens, or the form goes on. One that is still
not up is said so, with its pane as it is now, and the question is asked
again. "Not now" (or ctrl+c) ends as a start that asks nothing does, with what
is known and how to go on. In a pane of herdr there is nothing to attach:
herdr shows the yardmaster's pane in place of yardr's, yardr says how to come
back to its own (`ctrl+b g`, herdr's list of panes) and waits there meanwhile.
"Talk to the yardmaster" in [`yardr setup`](setup.md) asks the same of a
yardmaster it finds that way. yardr types nothing into the pane: it answers
Claude Code's question about trusting the folder when the session starts
([groups.md](groups.md)), and nothing else a harness asks. A yardmaster
without a session (a start that failed, a program that is missing) has no
pane, and nothing is asked.

Once the yardmaster is up, the form asks three more questions before the rest of
the setup, each starting at **no**: "Start the daemon at login?", which runs
`yardr daemon install`, "Start the web view now?", followed on a yes by
"And at login?": yes runs `yardr web install`, no `yardr web start`, and
"Link with another yard?", which opens the form of `yardr peer pair`
([peers.md](peers.md#the-form)) and is asked of the operator, on a yard
with no peer. Each
command is shown before it runs and prints what it did; the web view's
prints its address. One that fails says so and the form goes on: the yard
runs without either. On a system yardr writes no service for, only the web
view is offered, for now. A question is left out when the yard has what it
asks for: the daemon's service is installed, the web view's is, or something
answers on the view's address. `yardr init --start` asks nothing; the
commands are the same.

#### Long-running commands

Four commands run until they are stopped: `yardr daemon`, `yardr web serve`,
`yardr bdp serve` and `yardr otel export`. Each runs in a terminal under
that name, and each has the same verbs beside it, under `yardr daemon`,
`yardr web`, `yardr bdp` and `yardr otel`:

| verb | what it does |
|---|---|
| `start` | starts the command detached from the terminal, logging to `<home>/daemon.log`, `web.log`, `bdp.log` or `otel.log`, waits until it is up (it listens, or holds its file in the home) and prints its pid. It takes the flags of the command that a background one can use, and hands them on: `--interval` and `--max-attempts`; `--addr` (on loopback, for the web view); `--addr` and `--scope`; `--endpoint`, `--traces-endpoint`, `--logs-endpoint`, `--header`, `--after` and `--include-text` |
| `stop` | signals every one of the kind that runs on the yard, however it was started, and waits (`--wait`, default 30s) until it has ended |
| `status` | says which run, with pid and address, and whether the login service is installed and loaded |
| `install`, `uninstall` | the login service ([below](#login-services)), with the same flags as `start` |

A running one is found by a file it holds a lock on for as long as it runs:
`<home>/daemon.lock` for the daemon, of which a yard has one, and a record
of its own under `<home>/webviews/`, `bdpservers/` or `otelexports/` for the
others, of which it may have several. A lock nobody holds is one that
ended, whatever its pid has become since. `yardr daemon start` and
`yardr otel start` refuse while one of the kind runs (a second export would
send every event again); a web view or a BDP server is refused when
something listens on its address already.

A started web view or BDP server gets the server's environment (`PATH`,
`HOME`, the home) and nothing else of the shell; a started export gets the
`OTEL_EXPORTER_OTLP_*` variables too, and is refused when neither they nor
a flag name a collector. A started daemon keeps its shell's environment, as
[above](#starting-the-yard). A background export keeps its place in
`<home>/otel-export.json`: `--state` and `--once` are for a terminal.

#### Login services

`yardr daemon install` makes the daemon a service of your login session, and
`yardr web install` the web view ([web.md](web.md#keeping-it-running));
`yardr bdp install` and `yardr otel install` do the same for the BDP server
and the telemetry export, with `bdp` or `otel` in the names below:

| system | what is written | where |
|---|---|---|
| macOS | a launchd agent, `dev.yardr.daemon.<home>.plist` (`dev.yardr.web.…`) | `~/Library/LaunchAgents/` |
| Linux | a systemd user unit, `yardr-daemon-<home>.service` (`yardr-web-…`) | `$XDG_CONFIG_HOME/systemd/user/`, default `~/.config/systemd/user/` |
| any other | nothing: the command says it is not supported and how to run it by hand (`yardr daemon` in a terminal, `yardr daemon start` in the background) | |

`<home>` is the home's directory name and the first 8 hex digits of the
sha256 of its path (`yardr-3f9a1c0e`, say, for `~/.yardr`), so every yard
has services of its own and two yards never share one.

The file holds:

- the yardr that ran the install, by its path, with `daemon` (or `web serve
  --addr <addr>`, `bdp serve --addr <addr>`, `otel export`) and the flags
  the install was given. A yardr that moves needs the install again;
- its whole environment besides what the manager sets: `YARDR_HOME` (the
  home the install ran for, `--home` included), `HOME`,
  and the `PATH` of the shell the install ran in. herdr, git and every
  harness have to be on that `PATH`; the install names the ones that are
  not. Of the rest of that shell it takes the settings the server and the
  daemon read (`YARDR_HOOK_ADDR` and the three `YARDR_*_RETENTION`) and
  where herdr is when that is not the default (`HERDR_SOCKET_PATH`,
  `XDG_CONFIG_HOME`), and nothing else: no credential, no identity. A
  daemon in a pane has its pane's whole environment; what an exec group's
  command or a `git fetch` took from there has to come from somewhere else
  for the service (a group's `env_file`, a credential helper). The
  export's file alone holds more: the `OTEL_EXPORTER_OTLP_*` variables of
  that shell, the collector's headers among them, so it is written mode
  0600;
- the home as working directory and `<home>/daemon.log` (`web.log`) for
  what it prints;
- that it starts when loaded and at every login, and is started again when
  it ends with a failure, after 10 seconds at the earliest. An end without
  one is left alone: after `yardr daemon stop` the daemon stays stopped
  until the next login, or until you start one;
- that only the service's own process is the manager's to stop
  (`AbandonProcessGroup`, `KillMode=process`): the store's server, which the
  daemon may have started, and what an exec group runs are not ended with
  it.

The install then loads the file: `launchctl bootstrap gui/<uid> <file>`, or
`systemctl --user daemon-reload`, `enable` and `restart`. A daemon that runs
already is stopped first, as `yardr daemon stop` does, because a yard has
one daemon and the service's would end on the lock the other holds; the
sessions carry on, and the service's daemon takes them over from the store.
An install over an installed service unloads it, writes it again and loads
it: that is how a changed `PATH` or a moved yardr reaches it.

`yardr daemon uninstall` (`yardr web uninstall`) unloads the service
(`launchctl bootout gui/<uid>/<label>`, `systemctl --user disable --now`),
which stops what the service ran, and deletes the file. Nothing else was
written, and nothing else is removed. A daemon started any other way is left
running; with none, start one as [above](#starting-the-yard).

`--file-only`, on install and uninstall, writes or deletes the file and
leaves the manager alone, and prints the manager's command for the rest. On
macOS a file in `~/Library/LaunchAgents` is loaded at the next login anyway;
a systemd unit that is not enabled is not started at all.

**After a reboot.** Nothing of the yard runs: no server, no daemon, and the
panes of the sessions went with herdr. When you log in, the manager starts
the daemon. The daemon's first call starts the server, and its first pass
finds the yardmaster and the sessions it had without their panes and starts them
again. That needs herdr, as any start does; while herdr cannot be reached
the daemon logs it once and tries again every pass. With the web view
installed too, the manager starts it at the same login, at the address it
was installed with. A systemd user unit runs while you are logged in:
`loginctl enable-linger <you>` makes that from boot to shutdown. A launchd
agent belongs to the login session and has no such switch.

**macOS: folders the service cannot read.** macOS keeps `~/Desktop`,
`~/Documents` and `~/Downloads` from a program until it is granted access
(System Settings, Privacy & Security). A daemon started in a terminal comes
under the terminal's grant. One started by launchd is a program of its own
and comes under none, so work in a depot under one of those folders can fail
with `operation not permitted`. `yardr daemon install` names the depots that
are there, and [`yardr doctor`](doctor.md#optional) does while the service
is installed. Keep depots elsewhere, or grant yardr access: Full Disk Access
(add the yardr binary the service names), or Files and Folders, once macOS
has asked for the folder. What an upgrade does to that grant is
[below](#upgrading). This is as reported from one Mac (Linear YRD-3):
yardr's tests load nothing into launchd, so it is not reproduced by them.

#### A run that did not finish

The onboarding and `yardr init` do their steps in one order: the yardmaster's
role, the `operator` and `yardmaster` settings, the yardmaster crew member, `default_kind`,
and then, when asked to, the daemon and the wait for the yardmaster. A run
that fails leaves what the steps before the failure made. The next run
finishes from that: it does not start again from nothing, and it says what it
found.

| the run stopped | what the home holds | the next run |
|---|---|---|
| after the role | the yardmaster's prompt, `packs/<pack>/roles/yardmaster.md` | `yardr init --harness <kind>` again, or `yardr` in a terminal for the form. The prompt the failed run wrote is written again with the names this run is given |
| after `operator`, or after `yardmaster` | the prompt and the settings stored so far | the same. The stored settings stand in for `--operator` and `--yardmaster` when they are not given, and the form's "Yardmaster" starts at what is stored. Its "Your name" starts at who the terminal acts as and takes no other name ([users.md](users.md#who-is-acting)) |
| after the yardmaster was added | all but `default_kind`. With a crew member the home counts as a yard, so no form runs | `yardr init`, with no flag: the harness is the yardmaster's. Or `yardr` in a terminal, below |
| the daemon could not be started, or the yardmaster did not come up | a yard that is set up and does not run | `yardr init --start`, which starts the daemon unless one runs and waits again. Or `yardr` in a terminal, below |

Init then prints `yard in <home> finished: part of it was there`, with a
`found` line (the prompt, settings and crew member that were there) and an
`added` line (what this run added); `--json` has the same as `found` and
`added`, which are absent on a new home and on a yard that was set up. A
setting stored with `yardr settings set` before any init is found the same
way. The role prompt is written to a temporary file and renamed, so a run
that is killed leaves the prompt that was there or none, never half of one.

**Bare `yardr` in a terminal** takes the onboarding up where a yard has no depot
yet and any of this holds: `default_kind` is not set, the yardmaster crew member
is missing, no daemon runs, or the yardmaster is not up (its session is not
running, or runs and has not answered its first prompt). It prints what is
there, in the lines [`yardr doctor`](doctor.md#the-yard) prints for the yard,
and asks "Finish the setup now?". Yes runs `yardr init --start` (which adds
only what is missing, starts the daemon unless one runs, and waits for the
yardmaster) and then goes on as the form does: the two services, and "Talk to
the yardmaster" or "Keep going here". The summary of the yard is left out when
nothing had to be added. No, or ctrl+c, changes nothing and prints the
command. It goes by the yard as it is, not by a record of the earlier run, so
a yard made with `yardr init` and never started is taken up the same way.

On any other yard bare `yardr` is help, as before (with a last line,
`Mail for <user>: N unread from <names>`, when the user who runs it has unread
mail, [users.md](users.md)): one with a depot (its setup is `yardr setup`'s, or
the yardmaster's), one that runs, one whose yardmaster was stopped with
`yardr crew stop` while its daemon runs. It is help too without a
terminal, with `--json`, and in an agent's session. When the form's own start
ends with a yardmaster that is not up, its last line says to run `yardr` in
the terminal again once that is set right.

#### Starting over

There is no command that removes a yard. Starting over is the steps below,
each typed by you with the path or the name in it: yardr never removes a
home or a repository. Take them in this order; the first five need the yard
they stop, so the home goes last.

| what the onboarding and the setup can have made | where | to see it | to remove it |
|---|---|---|---|
| the login services | `~/Library/LaunchAgents/dev.yardr.{daemon,web}.<home>.plist`, or `~/.config/systemd/user/yardr-{daemon,web}-<home>.service` | `yardr doctor`, under Optional; `yardr daemon status` | `yardr web uninstall`, `yardr daemon uninstall`. Each unloads its service, which stops what it ran, and deletes the file |
| a web view started with `yardr web start` | a process; it logs to `<home>/web.log` | `yardr status`, under `web view` | `yardr web stop` |
| the yardmaster's session, and any other crew member's | a herdr workspace labelled `<yard> <crew member>`, agent `<yard>-<crew member>` | `yardr crew list`, `herdr workspace list` | `yardr crew stop <name>`: the daemon closes the session's tab, and its workspace with it, on its next pass (30s at most). It needs the daemon running |
| sessions working beads | a herdr workspace per bead | `yardr session list`, `herdr workspace list` | `herdr workspace close <id>` for each that is left. Look at the bead's worktree first (the home, below) |
| the daemon | a process holding `<home>/daemon.lock` | `yardr daemon status` | `yardr daemon stop` |
| the server | a process; `<home>/server.pid`, `yard.sock` | `yardr server status` | `yardr server stop`. Any yardr command after it starts a server again, so run none before the home is gone. A server or daemon still running when the home is removed stops by itself within a minute ([above](#yardrs-own-state)) |
| the home | `~/.yardr`, or what `YARDR_HOME` names: the store, the roles, the logs, `packs/` and `flows/` (an imported pack's roles, scripts and stage files; the assembly's `merge-local` is one of them), `worktrees/`, `depots/` | `ls -a <home>` | `rm -rf <home>`, typed by you with the path. Read the next paragraph first |
| a depot's `.yardr` files | in the repository, committed on its base branch: `.yardr/roles/`, `.yardr/check`, `.yardr/flows/` | `git -C <repo> ls-files .yardr` | keep them: a new yard that adds the repository as a depot reads them as they are. To start the depot's configuration over too: `git -C <repo> rm -r .yardr`, and commit |

**Before the home is removed**, look at two directories in it, because they
hold work and not only yardr's state. `<home>/worktrees/<depot>/<bead>` are
the worktrees of beads, on branches `yard/<bead>` of the depot's repository:
the commits on those branches stay in the repository, and what is not
committed in a worktree goes with the home. `<home>/depots/<name>` are
repositories themselves, cloned by `yardr depot add --clone`: removing the home
removes those clones, with every branch that was never pushed. Move out what
you want to keep. Credential files you kept in the home
(`linear.secret`, `crew/<name>.env`) go with it too.

After the home is gone, each depot's repository still lists the worktrees that
were in it: `git -C <repo> worktree prune` clears them (and the briefs yardr
kept in the repository's git directory beside them), and
`git -C <repo> branch --list 'yard/*'` shows the bead branches that are left,
for you to delete or keep. Nothing else in a repository is yardr's.

Then `yardr`, or `yardr init --harness <kind>`, creates a yard in the empty
home as on the first day. Tried in an isolated yard: after these steps no
process of the yard was left, `yardr doctor` reported no yard, and
`yardr init --harness claude --start` brought a new yardmaster up.

Starting over is seldom needed for what the onboarding asked: the harness,
your name and the yardmaster's are `yardr settings set`, the yardmaster's
directory and model are `yardr crew set <yardmaster> --dir … model=…`, a
role prompt comes back as this build's when its file is deleted and
`yardr init` runs, and a setup that failed is finished by the next run
([above](#a-run-that-did-not-finish)).

### Seeding a depot: `yardr depot init`

`yardr depot init <depot> [--pack <pack>]` sets a registered depot up from a
[pack](packs.md): the yard's default pack, or the one `--pack` names.

| what | where |
|---|---|
| the pack's roles, scripts and stage files; the depot's groups, routes and flow pointer | the yard's home and store, exactly as `yardr pack apply <pack> --depot <depot>` writes them ([packs.md](packs.md#yardr-pack-apply)) |
| `.yardr/check` | the checkout: the merge gate, executable, with a comment saying so, and only when the depot has no gate (no `.yardr/check` in the checkout, no `--check` in the store). Its command is a guess from the repo: `go.mod` gives `go vet ./... && go test ./...`, a `package.json` with a real `test` script gives `npm test` (`pnpm`/`yarn` by lockfile), anything else `exit 0` with a TODO. It prints the commit that puts that one file on the base, where yardr reads it |

Nothing else goes into the depot: no roles, no flow, no stage files. They are
the pack's, changed in the pack and brought to the yard by `yardr pack
apply` ([why](#why-the-repo-holds-what-the-work-is)).

**Which pack.** Without `--pack` it is the yard's `default_pack`
[setting](#settings). Unset, it is one of the two packs in the binary, by the
depot's kind: `starter` for a git depot, `dir` for a dir depot. A pack
declares the kind of depot it is for (`kind: git` or `kind: dir` in its
pack.yaml, as those two do), and one that declares the other kind than the
depot's is refused with nothing written; a pack without the key fits both
([packs.md](packs.md#yardr-depot-init)). `<pack>` and
the setting name a pack as `yardr pack apply` does
([packs.md](packs.md#naming-a-pack)): a directory, or a name. When the pack
is the setting's, the command says so first (`pack <pack>: the yard's
default_pack setting`, on stderr).

The plan is printed first and the import asks what `pack apply` asks,
answered by the same flags (`--bind`, `--members`, `--crew`, `--flow`,
`--open-threads`, `--calls`, `--depot-roles`). Nothing is written, the gate included,
before a yes in the terminal or `--yes`. A second run changes nothing and
keeps the gate that is there. To apply the pack again after it changed, use
`yardr pack apply`, which never writes a gate.

A depot that already has `.yardr/roles/` keeps the files, and the pack's
roles win: the plan names each one a group or crew member would have read as
`shadows .yardr/roles/<name>.md` before anything is written, and
`yardr depot sources <depot>` shows it afterwards. `--depot-roles` keeps the
depot's own instead. A depot that already has `.yardr/flows/` keeps those
files too, shadowed by the pointer to the pack's flow; `yardr depot source
<depot> flow depot` makes the depot's flow govern again
([switching](#switching-one-of-them)). `depot init` moves a depot only when
someone runs it: a depot set up before it took a pack works as it did until
then ([upgrading](#upgrading)).

A dir depot gets no gate ([dir-depots.md](dir-depots.md#1-register-the-folder-and-set-it-up)):
nothing is written into the directory. `starter` is refused there, since its
flow lands a bead on a base branch through an assembly and nothing merges in
a directory, and `dir` is refused on a git depot, whether `--pack` or the
setting named it.

`--operator`, `--yardmaster` and `--with-yardmaster` are from when `depot
init` wrote role files into the depot. They are accepted and ignored, each
with one line on stderr saying what says the same now:

| flag | instead |
|---|---|
| `--operator <name>` | a pack's roles name the yard's `operator` setting: `yardr settings set operator <name>` |
| `--yardmaster <name>` | `--crew yardmaster=<name>` says which crew member takes the pack's `yardmaster` |
| `--with-yardmaster` | a dir depot's roles are its pack's, installed under the yard's home; none is written into the directory |

```
$ yardr depot add web ~/src/web --base main
$ yardr depot init web --yes \
    --bind builders=claude --bind reviewers=claude --members backlog= --members decisions=jo
…the plan, then:
applied pack starter to depot web: 23 created, 0 changed, 2 unchanged

gate: wrote .yardr/check in /Users/jo/src/web: go vet ./... && go test ./... (a guess from the repo: check it)
Nothing is committed. yardr reads the gate from main:
  git -C /Users/jo/src/web add .yardr/check && git -C /Users/jo/src/web commit -m 'yardr: gate' -- .yardr/check
```

The two unchanged are the yardmaster and its role, which `yardr init` made.
A yard whose yardmaster has another name gives `--crew yardmaster=<name>`.

### Settings

`yardr settings get [<key>]` and `yardr settings set <key> <value>` (an empty
value unsets) keep the yard's own defaults in the store. The store knows
which settings there are and what each takes, so a key that is none of
them, or a value its setting does not take, is refused with the keys there
are, whoever writes it.

`yardr settings get` prints the value the yard goes by and where it is
from: `set`, `default` (the setting is unset), or `environment`
(`YARDR_GATE_SLOTS` and `YARDR_HOOK_ADDR` are used instead of their
settings; the environment is that of the command, and a server goes by the
one it was started in). A setting that is unset and has no default shows
`-`. With a key it prints the value alone, and fails for a setting with no
value; `--json` has where it is from.

| key | meaning |
|---|---|
| `default_kind` | the agent kind a herdr group or crew member gets when `group add` or `crew add` is given no `--set kind`; also the kind their forms start at. One of the kinds herdr starts. Its presence marks the yard set up. |
| `default_pack` | the pack `yardr depot init` sets a depot up from when it is given no `--pack`, and the pack `yardr init` takes the yardmaster's role from: a pack's name (one the yard has imported, or one in the binary) or its directory as an absolute path ([packs.md](packs.md#naming-a-pack)). Unset, `starter`, and `dir` for a dir depot. A relative directory is refused, since the setting is read wherever `depot init` is run, and so is a directory with no `pack.yaml`. Changing it moves nothing: it is read when a depot is set up. |
| `operator` | the person the role prompts name, agents bring decisions to; `yardr init` stores it (`--operator`, the form's "Your name") and an import fills a pack's roles with it. The operator is a user ([users.md](users.md)): setting a name adds the user when there is none, a crew member's name is refused, and only the operator adds and removes other users. |
| `operator_style` | how agents should work with the operator: `guide`, `show`, `work` or `report` ([what each tells an agent](#how-agents-work-with-you)). `yardr init` stores it (`--style`, the form's "How should agents work with you?"); both primes print it as instructions, so a change reaches the next session that starts. Unset, the primes say nothing about it. |
| `operator_note` | one line agents should know about the operator (`--about`, the form's "Anything agents should know about you?"), printed by the primes with the style, or alone. |
| `yardmaster` | the yardmaster crew member's name, whom the builder and reviewer roles tell agents to mail about what blocks them ([crew.md](crew.md#findings-from-builders-and-reviewers)); `yardr init` stores it (`--yardmaster`). Which crew member takes a pack's `yardmaster`, and so whom its roles name, is the import's `--crew yardmaster=<name>`. |
| `hook_addr` | the TCP address the server takes webhooks on, `127.0.0.1:8790` say ([subscriptions.md](subscriptions.md)). The server reads it when it starts, so a change takes its next start: `yardr server stop`, and the next command starts it again; `settings set` says so. `YARDR_HOOK_ADDR`, set where the server starts, is used instead of it. An address that is not `host:port` with a port from 1 to 65535 is refused (`:8790` is every interface). The server writes what it listens on to `hooks.state` in the home, and `yardr doctor` reports it: the address and whether it is the variable's or the setting's. A server that cannot listen there (another yard has the address) keeps serving its yard, logs a `hook_listener_failed` event, and doctor says so with what to change. |
| `gate_slots` | how many full gates (`yardr depot check`) run at once on the machine, counting every yard of the user; the others wait for a [slot](#gate-slots-how-many-gates-run-at-once). A whole number, default 2; `0` is no limit. |
| `name` | the yard's name, which its crew's herdr agents and workspaces carry (`<name>-yardmaster`, labelled `<name> yardmaster`), so two yards on one machine, whose agents herdr lists together, are told apart. Unset, the home's directory name: `yardr` for `~/.yardr`, `y1` for `/tmp/y1`. Changing it renames crew members started from then on. |
| `timer_limit` | how many live timers one crew member or user may have set ([timers.md](timers.md#limits)); a whole number from 1, default 5. |
| `timer_min_every` | the shortest period of a recurring timer (`yardr timer add --every`): a duration such as `30m` or `1d`, default `1h`. |

Environment variables yardr reads:

| variable | meaning |
|---|---|
| `YARDR_HOME` | state directory (above), when `--home` is not given; a relative path is resolved against the command's directory, once |
| `YARDR_ACTOR` | who is acting, for the event log (default: the OS user). A command whose actor is a user's name acts as that user ([users.md](users.md)); nothing checks who is at the keyboard |
| `YARDR_BEAD`, `YARDR_CREW` | defaults for `prime` and crew member commands; set for the sessions yardr starts |
| `YARDR_SESSION` | the session yardr started this process for; every bead move (`bead done`, `advance`, `close`, `hold`, `release`, `reopen`) sends it, and the store refuses to move the session's own bead once the session no longer owns it, and any other bead a live session owns. A call from the session's pane is checked as it even with the variable unset ([pipeline.md](pipeline.md#2-new-a-builder-implements-it)). In a bead's worktree, an environment naming another bead gives way to the worktree's session ([groups.md](groups.md#codex)) |
| `YARDR_HOOK_ADDR` | TCP address the server takes webhooks on, e.g. `127.0.0.1:8790`, read where the server starts; set, it is used instead of the `hook_addr` setting ([above](#settings)). Sessions and gates do not inherit it |
| `YARDR_LINEAR_API`, `YARDR_GITHUB_API` | where `yardr reply`, `yardr thread open` and `yardr thread state` send their calls, read by the command that makes them, and where the daemon sends a hook's issue moves ([links.md](links.md#a-beads-stage-moves-the-issue)), read when it starts (default: Linear's GraphQL endpoint, `https://api.github.com`) |
| `YARDR_EVENT_RETENTION`, `YARDR_HOOK_PAYLOAD_RETENTION`, `YARDR_INBOX_RETENTION` | how long the daemon and `yardr store prune` keep events, ended sessions and webhook bodies, and how long an unread inbox event holds the log back ([below](#retention-keeping-yarddb-bounded)) |
| `YARDR_GATE_SLOTS` | how many full gates run at once on the machine, used instead of the `gate_slots` setting when set; `0` is no limit ([gate slots](#gate-slots-how-many-gates-run-at-once)) |
| `YARDR_GATE_FIRST` | set, `yardr depot check` takes its slot before the other runs waiting for one, as `--first`; the assembly scripts set it |
| `YARDR_GATE_DIR` | where the gate slots are, instead of `~/.local/state/yardr/gates` |
| `YARDR_GATE_SLOT` | set by `yardr depot check` for a check that runs under a slot; a `depot check` started with it takes none |
| `YARDR_PPROF` | measuring: a path; the server serves Go's `net/http/pprof` on a unix socket there (mode 0600) for as long as it runs, read where the server starts. `curl --unix-socket <path> 'http://x/debug/pprof/profile?seconds=60'` is a CPU profile of a minute. Unset, nothing listens |
| `YARDR_TEST_RPC` | tests: run the store suites through the unix-socket client |
| `YARDR_STORE_NOSYNC` | tests: `1` opens `yard.db` without syncing each commit to the disk, which is most of a test run's time on macOS. The test suites set it for their throwaway yards (`0` keeps the sync). Never set it for a yard you keep: its store can be lost when the machine goes down |

Exec commands and herdr bead agents also get `YARDR_SESSION`, `YARDR_STAGE`
and `YARDR_GROUP` (see [groups.md](groups.md)). They get the session's
OpenTelemetry trace context too, `TRACEPARENT` and
`OTEL_RESOURCE_ATTRIBUTES` ([otel.md](otel.md#trace-context-for-the-work)).

### Retention: keeping `yard.db` bounded

The event log, the sessions that have ended and the raw webhook bodies
beside them are pruned by age. The daemon prunes when it starts and every
hour; `yardr store prune` does it on demand (`--events`, `--hook-payloads`
and `--inbox` override the knobs for one run).

| variable | default | what it keeps |
|---|---|---|
| `YARDR_EVENT_RETENTION` | `90d` | events, with their index entries, and ended sessions (`yardr session list --all`, [below](#listings-of-what-the-yard-keeps-the-newest-200)); also what a link with another yard remembers of its messages, though never for less than 7 days (below) |
| `YARDR_HOOK_PAYLOAD_RETENTION` | `30d` | raw webhook bodies (up to 1 MiB each), read only by `yardr hook show`; the `hook` event keeps the envelope routing reads |
| `YARDR_INBOX_RETENTION` | `180d` | how long an event a running crew member has not read holds the log back |

Values are days (`90d`), Go durations (`720h`), or `off` (or `0`) to keep
everything. Whatever its age, an event is kept when:

- it is the newest event (sequences continue from it);
- it is in a *running* crew member's inbox, not yet acknowledged and younger
  than the inbox retention, and so is every event after it; a crew member with
  nothing unread holds nothing back, and neither does one that is stopped or
  disabled, however much it has not read;
- it was logged after the oldest live bead session or running crew member
  session started. A bead waiting for a person holds no events back: a
  manual group's claim starts no agent, so nothing reads back to its start.

An unread inbox event past those bounds goes like any other; the prune
reports how many each crew member lost (`expired`) and logs `inbox_expired` on
the crew member's journal with the count, so the crew member learns what it never
read. An ended session goes with the events of its time once the daemon has
stopped its runner resources and, when it left a worktree, cleaned up after
the bead closed (`workspace_removed`); a live session is never pruned.

The ids of the messages taken from a linked yard, by which one sent twice is
taken once, and the messages given up for it go at the age of events as
well, counted from when the id was taken and the message given up, but
never younger than 7 days however short the retention
([peers.md](peers.md#what-a-link-forgets)). The prune reports how many of
each went per peer (`peers`, also on its `store_pruned` event).

A prune that held events back says why (`held_by`, also on its
`store_pruned` event). Beads, notes and deps are state, not log, and are
never pruned; a bead's own event history shortens with the log. That
history is the only record of what a bead was: an `updated` event carries
the previous value of each field it changed (`was`), the body in full, and
`yardr bead history <id>` and the bead's page read them from the log. So
an old title or body is remembered for the event retention and no longer,
whether the bead is open or closed: an open bead's events are pruned at
their age like any other, unless one of the rules above holds them. A bead
that is edited often costs the store its old bodies for that long, one
body per edit. `YARDR_EVENT_RETENTION=off` keeps every version. What
pruning must not lose lives on the bead: its count of failed attempts in
its stage (reset when it enters a stage or `yardr bead unhold` runs) is kept
there, not read back from the `unheld` event or the ended sessions, so
pruning them does not hold a bead again without new failures. Claims and
the daemon's pass read the live sessions through an index of their own and
do not slow down with the sessions the store still keeps.

### Listings of what the yard keeps: the newest 200

`yardr bead list` and `yardr session list` show the open beads and the live
sessions, however many. With `-a` they include the closed beads and the
ended sessions, which a yard keeps (beads for good, sessions until they are
pruned), so that listing has a limit, as `yardr events` has: there are no
pages and no cursor.

| | what is listed |
|---|---|
| `-a` (`--closed` of `bead list`, `--ended` of `session list`) | the newest 200: beads by when they were made, sessions by when they started. They are shown in the listing's own order (beads by priority, sessions oldest first) |
| `-n <N>`, `--limit <N>` | the newest N instead, with or without `-a` |
| `--all` | every one, the closed or ended too. For a script that counts or exports; it costs what the yard holds (about a second for 60,000 sessions) |
| `--since <age>` | only the beads made, or the sessions started, in that time: `7d`, `12h`, `30m` |
| `--bead <id>` of `session list` | only that bead's sessions; its ended ones too with `-a` |

The filters (`--stage`, `--depot`, `--bead`, `--since` and the others) narrow
first and the limit takes the newest of what they keep. A listing as long as
its limit says so on stderr (`the newest 200 beads; there may be older ones:
-n <N> lists more, --all every one`), and `--json` is the same rows. `--all`
with `-n` is refused.

`-a` was the short form of `--all` and listed everything. A script that
needs every bead or session says `--all`, which lists them as before.

The sessions are read through two indexes the store keeps beside them,
`sessions_by_start` and `sessions_by_bead`, so the newest sessions and one
bead's sessions (`session list -a --bead <id>`, a bead's page in the web
view) cost what they show. A store from before them gets both on its first
open by this build; nothing is to be done, and the schema is unchanged.

bbolt reuses the pages pruning frees but never returns them to the
filesystem. To shrink the file, stop the daemon and the server
(`yardr server stop`) and run `yardr store compact`; it writes a compacted
copy and swaps it in only once it is complete. It is the one store command
that does not start the server, and it refuses while one runs (`the yardr
server is running and holds …`) or anything else holds the file.

The store no longer keeps a snapshot of every bead and dep state: the event
log is their history, and the `versions` bucket older builds wrote is
dropped on open.

### Upgrading

`go install` is enough. The server and the daemon each remember the binary
they were started from and replace themselves (`exec`, same pid) once a
newer build is installed over it. A build is newer when:

- its revision differs from the running one, ignoring `-modified` (a dirty
  build of the running commit, say from a bead worktree, never counts), and
- it was built from a later commit: `vcs.time` from its build info, or for
  `go install …@<pseudo-version>` the time in that version. Two
  `@vX.Y.Z` releases are compared by version instead. A build that says
  neither falls back to the file's mtime against the running binary's.

Any other build installed over the binary (older, the same commit, a
reinstall) is skipped once per install, logged and recorded as a
`replace_skipped` event with the `running` and `candidate` revisions and the
`reason`; stop the server (`yardr server stop`) to run it anyway. Builders
never `go install` from their worktrees; the yardmaster installs after merge.

A binary from a [release archive](install.md) upgrades the same way: unpack
the newer archive and move `yardr` over the old one (`tar` itself also
replaces the file rather than writing into it). A release build carries the
same revision and commit time as a `go install` of its commit, and those,
not the tag `yardr version` prints, decide whether it is newer; so archives
and `go install`s replace each other by commit time.

A new build changes nothing in a yard that is set up: no prompt, group,
route or flow moves until the operator runs `yardr depot init` or `yardr
pack apply`.

- **A yard and its depots from before `depot init` took a pack** work as
  they did. A depot's `.yardr/roles/`, `.yardr/flows/` and stage files on
  its base are read as before, a group whose `prompt` is `roles/<name>.md`
  is looked up as before ([role prompts](#role-prompts)), and `yardr depot
  sources <depot>` prints what it printed.
- **`~/.yardr/roles/`** is no longer written. `yardr init` again leaves
  every file there as it is and refreshes none, edited or not: the
  directory is yours from then on, and stays the last place a relative
  `prompt` is looked up. `.shipped.json` in it is no longer read.
- **Roles installed from a pack** (`~/.yardr/packs/<pack>/`) are files, not
  code: a build with a newer `starter` does not touch them. `yardr pack
  apply starter --depot <depot>` brings them to the build's, showing each as
  `change content: sha256 … -> …` first, and refuses to overwrite one edited
  by hand without `--yes`.
- **Moving an existing depot to a pack** is `yardr depot init <depot>`, when
  you want it: `yardr pack check <pack> --depot <depot>` prints the plan and
  writes nothing. Read it first. The import gives the depot groups of its
  own (`<depot>-builders`), points its routes at them, sets the yardmaster's
  prompt to the pack's role, and names every file of the depot it shadows
  ([seeding a depot](#seeding-a-depot-yardr-depot-init)). The shadowed files
  are then removed from the repository by an ordinary commit, or made the
  source again with `yardr depot source`. A depot shaped by hand can stay
  as it is.
- **Scripts** that ran `yardr depot init <depot>` to write `.yardr/roles/`
  and `.yardr/flows/` into a checkout now get an import instead, which needs
  `--yes` and the answers an import asks without a terminal; `--operator`,
  `--yardmaster` and `--with-yardmaster` are accepted and ignored.

The store is stamped with its schema version on open, and a yardr whose schema
is older than the store's refuses to open it
(`store schema is newer than this yardr knows`) rather than run its migrations
over it. The schema is 17 (the rename of refinery to assembly stores
nothing, so it has no version of its own): version 17 keeps, in a source's
record, how many of a caller's calls an hour may be updates
(`yardr source set <source> updates_per_hour`) and the key of an update that
is still under way (the bucket `source_keys`); version 16 keeps who may call a
source (the bucket `source_grants`) and, in a source's record, how many calls
an hour one caller may make (`yardr source set <source> calls_per_hour`);
version 15 keeps what is still
to be told a user outside the yard (the bucket `pushes`) and, in a user's
record, how the last message ended; version 14 keeps, in a user's
record, the source that tells them outside the yard and the path of the file
their address is read from (`yardr user set`), version 12 stores a rig as a depot,
version 11 stores residents as the crew, version 10 stores the bead types
`epic` and `part` as `train` and `wagon`, version 13 renames the transitional
type to `wagon`, and version 9 renames the setting `mayor` to `yardmaster`
([below](#words-that-changed)); version 8 keeps, in a peer's intake, how long
a bead it sent waits to be released
([peers.md](peers.md#a-held-bead-that-nobody-releases)); version 7 keeps, with
a bead that came from a linked yard, the yards it had passed through, and
version 6 a peer's intake ([peers.md](peers.md#beads-the-receiving-side));
version 5 keeps, in a peer's record, the names it had before a rename and the
event it was added after ([peers.md](peers.md#renaming-a-link)); version 4
gave users subscriptions and a second cursor ([users.md](users.md)); version 3
added the inbox and kind indexes, which every event is written with, so a
store at 2 is reindexed in full the first time a newer build opens it (and a
build without the indexes can no longer append to it unindexed); version 2
keyed beads by UUID.

#### Words that changed

yardr names its things with the yard, and some of them had other names. For
one release an old word is still read wherever a person, a script or a file
of the yard's own may give it, and yardr answers with the new one; what the
store holds under an old word is renamed when the new build first opens it
(`settings_migrated`, `types_migrated`, `crew_migrated` and `depots_migrated`
in the events say what was). The release after drops the old words below, so change scripts
and files now.

**mayor is now yardmaster**: the crew member that coordinates a yard.

| the old word | still read as |
|---|---|
| `yardr init --mayor <name>`, `yardr depot init --mayor <name>` and `--with-mayor` | `--yardmaster`, `--with-yardmaster`; help lists only these. `depot init` accepts and ignores both under either name ([seeding a depot](#seeding-a-depot-yardr-depot-init)) |
| the setting `mayor` (`yardr settings get mayor`, `set mayor <name>`) | the setting `yardmaster`. The stored one is renamed by schema 9 |
| `{{mayor}}` in a role or stage file of a pack whose crew member is `yardmaster`, and in yardr's own templates | `{{yardmaster}}` |
| `--crew mayor=<name>` of `yardr pack check` and `apply`, and the record of an import from when the starter pack's crew member was `mayor` | the pack's crew member `yardmaster`. A pack that still has a crew member `mayor` of its own is read as it stands |
| `mayor.md`, the yardmaster's role file | `yardmaster.md`. A roles directory (`~/.yardr/roles/`, a depot's `.yardr/roles/`) that has `mayor.md` and no `yardmaster.md` keeps its file: `yardr init` leaves it as it is and writes no second one. A prompt that names either file reads the other when the one it names is not there, so a crew member's `prompt=…/mayor.md` keeps working, also after the file is renamed |

What does not change: a crew member's name is the operator's, so a yard whose
crew member is called `mayor` keeps it. Its `yardmaster` setting says `mayor`
(the upgrade sets it where it was unset), and that is whom the roles tell
agents to mail and whom `yardr doctor` reports on. Only a new yard's
crew member is called `yardmaster` by default.

What is printed has the new word at once, with no old spelling beside it:
`yardr doctor`'s line `yardmaster`, the `yardmaster` and `yardmaster_kept`
fields of `yardr init --json` and `yardr doctor
--json` (they were `mayor` and `mayor_kept`), and `yardmaster` in the list
`skipped` of `yardr doctor --json`. `merge-local` and `integrate-train` mail
the yard's `yardmaster` setting when `YARDR_NOTIFY` is unset; a copy
installed before still mails `mayor`.

Nothing on a link to another yard carried the word, so a yard and its
peers can be upgraded in any order. No event kind and no environment
variable carried it either.

**epic is now train, and part is now wagon**: work that spans several beads
and lands as one, and a piece of it that lands on the train's branch under
the light gate ([pipeline.md](pipeline.md#trains)).

| the old word | still read as |
|---|---|
| the bead types `epic` and `part`: `-t epic` and `-t part` of `yardr bead create` and `list`, `--type` of `bead update`, `route add`, `depot flow` and `yardr subscribe --where type=` | `train` and `wagon`. Schema 10 renames the stored ones: beads, routes, a depot's flow pointers, hooks' maps, subscriptions, the records of imported packs |
| `yardr bead list --epic <id>` | `--train <id>`; help lists only that |
| `--where epic=<bead>` of `yardr subscribe` and `unsubscribe`, on the `yardr` and `github` sources | `--where train=<bead>`. Stored subscriptions are renamed |
| `--where kind=epic_branched`, `kind=bad_epic` and `kind=part_without_epic` | `train_branched`, `bad_train` and `wagon_without_train`, the kinds new events carry. Events already in the log keep the kind they were logged with |
| `--map epic:<stage>=<state>` and `part:<stage>=<state>` of `yardr hook add`, and `hears:` under `epic:` or `part:` and `type: epic` in a pack | `train:` and `wagon:` |
| `.yardr/flows/type/epic.flow` and `type/part.flow` in a depot, with their stage files in `type/epic/` and `type/part/` | the flows of the depot's trains and wagons, where the depot has no `type/train.flow` or `type/wagon.flow`. Rename them with `git mv` when it suits |
| a flow named for a type, `<name>.epic` or `<name>.part`, in a pointer (`yardr depot flow`) or a pin (`--meta flow=`) | that flow, or `<name>.train` and `<name>.wagon` when there is none of the old name, and the other way round: a repository can rename `web.epic.flow` to `web.train.flow` before or after the depot is pointed at the new name |
| `YARDR_EPIC` in the environment of an exec group's command | `YARDR_TRAIN`. Both are set. `merge-local` reads either, and `YARDR_TYPE=part` as `wagon` |
| `contrib/refinery/integrate-epic` and `pr-epic` | `integrate-train` and `pr-train`, now in `contrib/assembly/` ([below](#words-that-changed)). The old files run the new ones, so a group's `cmd` that names one keeps working from a checkout |

A yard that ran a build of `main` between the two releases has wagons
stored as `car`, the word they had there for a few hours: schema 13 renames
them in the same places, and nothing reads that word afterwards except the
name of a flow that ends in it (`<name>.car` is found as `<name>.wagon`, and
the other way round).

What does not change: a dependency of kind `parent` makes a child of a
train, and a train's branch is `yard/<bead>`. A flow in the store keeps the
name it has (`<depot>.epic`, say, with its stage files in
`<home>/flows/<depot>.epic/`), as do labels, and a group's `cmd` is the
operator's: copies of the scripts under `<home>/scripts/` are not touched.
Copy `integrate-train` and `pr-train` there and name them in the groups'
`cmd` (`yardr group add <group>` again, with the new `--set cmd=...`). A
copy of `merge-local` installed before reads `YARDR_TYPE=part` only, so
until this build's `contrib/assembly/merge-local` is copied over it a wagon
gets the full gate where it would have had the light one: slower, and
nothing lands that should not. The role and stage files an earlier
`yardr depot init` wrote under a depot's `.yardr/` are the depot's own too and
still say epic and part; the commands they name are read as above, so edit
them when it suits.

What is printed has the new words at once: the `TRAIN` column of
`yardr bead list`, `yardr ready` and `yardr session list`, the field `train`
of their `--json` and of `yardr bead show --json` (it was `epic`), the field `train` of a GitHub hook event and of a
`github_rule` event, and the attribute `yardr.train` of a span's link
([otel.md](otel.md)). Change what reads them. A hook event logged before
the upgrade is still read by the daemon's rules under its field `epic`.

On a link to another yard a bead's type is sent with it. A train and a wagon
are still sent as `epic` and `part` in this release, and a yard takes either
for a task, as it did, so a yard and its peers can be upgraded in any
order. What yardr serves over [BDP](bdp.md) gives a train the reference
type `epic`, as before, and its `issue_type` property says `epic` too; a
wagon's minted type and its `issue_type` say `wagon`.

**resident is now crew**: the ones who are always in the yard are its crew,
and one of them is a crew member ([crew.md](crew.md)).

| the old word | still read as |
|---|---|
| `yardr resident <command>`: `add`, `set`, `show`, `list`, `start`, `stop`, `restart`, `rm`, `journal`, `handoff`, `identity` | `yardr crew <command>`; help lists only that |
| `--resident <name>` of `yardr prime`, `inbox`, `reply`, `thread open`, `thread state`, `bead watch` and `bead unwatch` | `--crew <name>`; help lists only that |
| `--resident <pack's>=<yard's>` and `--bind resident:<name>=…` of `yardr pack check` and `apply` | `--crew <pack's>=<yard's>` and `--bind crew:<name>=…` |
| `YARDR_RESIDENT` in the environment | `YARDR_CREW`. Yardr sets both spellings in a crew member's pane and for its `env_command`, and reads either old-name spelling when neither new-name spelling is set, so a session started before the upgrade works until it ends |
| the event kinds `resident_defined`, `resident_deleted`, `resident_enabled`, `resident_disabled`, `resident_started`, `resident_status`, `resident_stopped`, `resident_start_failed`, `resident_gave_up`, `resident_restart_requested`, `resident_rotation_asked` and `resident_blocked`: in `--where kind=` of `yardr subscribe` and `unsubscribe`, in `--on` of `yardr crew add` and `set`, and in `hears: events:` of a pack | the same kinds with `crew_` for `resident_`, which new events carry. Schema 11 renames the stored ones: a crew member's `--on`, subscriptions, the records of imported packs. Events already in the log keep the kind they were logged with, and the inbox, `yardr chat`, the web view's feed and `yardr doctor` read them as before |
| the label `resident:<name>` of a crew member's journal, in `yardr bead list -l` and `--where label=`, and `--scope resident:<name>` of `yardr remember` and `recall` | `crew:<name>`. Schema 11 renames the stored labels and the scope of the memories |
| `residents:` in a `pack.yaml` | `crew:`. A pack that has both means `crew:` |
| `<home>/residents/`, where these pages had you keep a crew member's `env_file` and keys | `<home>/crew/`. The first start of the new build renames the directory and leaves a link `residents` to it, so a path written in an `env_file` (a GitHub App's key), in a script or in a shell's history still finds its file; an `env_file` of a crew member that was under the old directory is recorded under the new one. A home that has both directories is left as it is, and so are the paths |
| `/residents` of the web view | `/crew`, by a redirect |
| `docs/residents.md` | [crew.md](crew.md); the old page is one line that says so |
| the field `resident` of an event already logged | the field `crew`, where yardr reads an event again (`yardr chat`, the inbox). The [OpenTelemetry export](otel.md) passes an old event's field on under the name it has |

What does not change: a crew member's name, its journal bead
(`<name>-journal`), its herdr agent and workspace, the keys of its
definition (`env_file`, `peers`, `open_threads` and the rest), the settings,
and its inbox, which keeps its place. Files of your own that say resident (a
role or stage file an earlier `yardr init`, `yardr depot init` or pack import
wrote, a script) are yours: the commands and flags they name are read as
above, so edit them when it suits; `yardr init` again refreshes the role
prompts nobody edited.

What is printed has the new word at once, with no old spelling beside it:
the first line of a crew member's prime (`# yardr crew member: <name>`) and
of its first prompt (`You are the yardr crew member "<name>"`), the line
`crew` of `yardr doctor` and the part `crew` of `yardr status`, and in
`--json`: the field `crew` (it was `resident`) of `yardr crew show`,
`journal`, `handoff`, `start`, `stop` and `restart`, of `yardr reply`,
`thread open`, `thread state`, `thread follow` and `unfollow`, and of
`yardr doctor`'s yardmaster; the list `crew` (it was `residents`) of
`yardr status`, of the web view's `/crew`, of `yardr peer rename` and of a
pack's plan and record, with the kind `crew` of a plan's object and its
questions `crew.<name>.<field>`; the count `crew` of `yardr doctor` and the
name `crew` in its `skipped`. New events name the crew member in the field
`crew`. Change what reads them.

A yard that runs while it is upgraded keeps running. A crew member's
session from before has `YARDR_RESIDENT` alone in its environment and a first
prompt in the old words (`yardr prime --resident <name>`): both are read, and
the daemon goes on nudging it. A web view or a `yardr chat` started before
does not replace itself and asks the server for the crew by the names of its
own build, which the server still answers; restart it to get the new words.

On a link to another yard the word was in one place: the reason
`no_such_resident` a yard gives when it refuses mail for a name it does not
have. It is still sent as that in this release, and `no_such_crew`, which the
next release sends, is read already, so a yard and its peers can be upgraded
in any order. Mail addresses (`<name>@<peer>`) and everything else on a link
are as they were.

**rig is now depot**: the repository or plain directory beads are worked
in ([Depot kinds](#depot-kinds-git-and-dir)).

| the old word | still read as |
|---|---|
| `yardr rig <command>`: `add`, `rm`, `list`, `init`, `check`, `flow`, `source`, `sources` and the rest | `yardr depot <command>`; help lists only that |
| `--rig <name>` of `yardr bead create`, `list` and `update`, `route add`, `pack check` and `apply`, and `peer intake` | `--depot <name>`; help lists only that |
| `-r <name>`, the one-letter `--rig` of `yardr bead create`, `list` and `update` and of `route add` | `-d <name>` |
| `--rig-roles` of `yardr pack check` and `apply` | `--depot-roles` |
| `--where rig=<name>` of `yardr subscribe` and `unsubscribe` | `--where depot=<name>`. Schema 12 renames the stored ones: the subscriptions of crew members, users and groups, and those in the records of imported packs |
| the event kinds `rig_updated`, `rig_removed` and `bad_rig`: in `--where kind=`, in `--on` of `yardr crew add` and `set`, and in `hears: events:` of a pack | `depot_updated`, `depot_removed` and `bad_depot`, which new events carry. Schema 12 renames the stored ones. Events already in the log keep the kind they were logged with |
| `YARDR_RIG`, `YARDR_RIG_PATH` and `YARDR_RIG_KIND` in the environment | `YARDR_DEPOT`, `YARDR_DEPOT_PATH` and `YARDR_DEPOT_KIND`. Yardr sets the old names beside the new for an agent's pane, an exec group's command and a gate (`.yardr/check`), so a script or a check that reads `$YARDR_RIG` keeps working |
| `<home>/rigs/`, where `yardr rig add <name> --clone <url>` put its clones | `<home>/depots/`. The first start of the new build renames the directory and leaves a link `rigs` to it, so the worktrees of beads in work, which name their repository by its path, and a path in a script or a shell's history still find it; a depot whose path was under the old directory is recorded under the new one. A home that has both directories is left as it is, and so are the paths |
| `?rig=<name>` of the web view's `/beads` | `?depot=<name>` |
| `docs/dir-rigs.md` | [dir-depots.md](dir-depots.md); the old page is one line that says so |
| the field `rig` of an event already logged, and of the record of a gate or a session that was running through the upgrade | the field `depot`, where yardr reads them again |

What does not change: a depot's name, path, base and gates, the files a
repository keeps under `.yardr/` (`check`, `check-light`, `flows/`,
`roles/`), a bead's worktree (`<home>/worktrees/<depot>/<bead>`) and its
branch, and `--kind dir`, which makes a dir depot. A flow in the store that
was named for its depot keeps the name. Files of your own that say rig (a
role or stage file an earlier `yardr rig init` or pack import wrote, a
script, a `.yardr/check`) are yours: the commands, flags and variables they
name are read as above, so edit them when it suits. The scripts of this
build (`contrib/assembly/`, `contrib/peer/`, a pack's `scripts/`) read the
new variables and call `yardr depot check`, so they need this build; a copy
installed before runs on under it as it is.

What is printed has the new word at once, with no old spelling beside it:
the `DEPOT` column of `yardr bead list` and `yardr ready`, the lines of
`yardr depot add`, `list`, `check` and `sources`, of `yardr doctor` and
`yardr setup`, an agent's brief, and in `--json`: the field `depot` (it was
`rig`) of a bead (`yardr bead show`, `list`, `ready`), a route, a flow
pointer, `yardr depot check`, `init`, `rm`, `source` and `flow`, a peer's
intake, a pack's plan and record (with `depot_roles`, which was
`rig_roles`), and a step of `yardr setup` and `yardr doctor`, whose steps
`rig` and `rig_files` are `depot` and `depot_files`; the list `depots` (it
was `rigs`) of the web view's pages; the source `depot` (it was `rig`) of a
flow, a stage file or a role in `yardr depot sources`. New events name the
depot in the field `depot`, a span of the [OpenTelemetry export](otel.md)
in the attribute `yardr.depot` (it was `yardr.rig`), and a bead served over
[BDP](bdp.md) in the property `depot`. Change what reads them.

A yard that runs while it is upgraded keeps running. A session from before
has `YARDR_RIG` alone in its environment and a brief that says
`yardr rig check`: both are read. A gate that is running through the upgrade
is still shown with its depot. A web view or a `yardr chat` started before
does not replace itself and asks the server for the depots by the names of
its own build, which the server still answers, but it finds no `rig` in a
bead and shows it without one; restart it to get the new words.

Nothing on a link to another yard carried the word: a peer's intake, which
names the depot its beads go to, is this yard's own setting and is not
sent. So a yard and its peers can be upgraded in any order.

**refinery is now assembly**: what lands approved beads, one at a time
([pipeline.md](pipeline.md#4-approved-the-assembly-lands-it-on-main)). It
was never a thing yardr knows: an assembly is an exec group and the script
it runs, so the word was in the starter pack, in `yardr setup`, in the
scripts' directory and in the docs, and nowhere in the store. No schema
version comes with it and nothing is migrated.

| the old word | still read as |
|---|---|
| `contrib/refinery/merge-local`, `integrate-train`, `pr-train` and `pr-merge` in a checkout (and `integrate-epic` and `pr-epic` there) | the scripts of those names in `contrib/assembly/`. The old files run the new ones, so a group's `cmd` that names one keeps working |
| a group `<depot>-refinery`, which an import of the starter pack made when its group was `refinery`, or which a yard set up by hand has under that name | the pack's group `assembly`. The next `yardr pack apply` goes on changing that group and its route, makes no `<depot>-assembly` beside it, and records it as the pack's `assembly`. A yard that has both groups and no record gets the new one; a pack of your own that still has a group `refinery` is read as it stands |
| `--bind refinery=…` and `--members refinery=…` of `yardr pack apply` | the pack's group `assembly`. They answered nothing before and answer nothing now (a script group has no agent and no members), so this is only a command that does not fail |
| a note that starts `refinery: `, as a `merge-local` installed before writes it | `pr-train` takes it for a script's note, like one that starts `assembly: `, when it looks for the review to put on a pull request |

What does not change: a group keeps the name it was given, whoever gave
it. The group `yardr setup` made is still `<depot>/refinery` with its log
in `<home>/logs/refinery.log`, a pack's is still `<depot>-refinery`, and
the yard that builds yardr still lands through `yardr-refinery`; routes,
sessions and the events that name the group as their actor are as they
were. `yardr setup` and `yardr doctor` find what lands approved beads by
its route for `approved`, not by its name, so neither offers a second one.
A first import of the starter pack makes `<depot>-assembly`, and that
import is what `yardr setup` runs. To have
the new name on a yard you already run, add the group under it with the
same `cmd`, point the route for `approved` at it and remove the old one;
nothing needs it.

`<home>/scripts/merge-local` is kept as it is, as ever: a copy installed
before signs its notes and its log lines `refinery:`, and works. This
build's signs `assembly:`; copy it over the old one (or import the pack
again, which installs its own) when it suits. Role and stage files an
earlier `yardr depot init` or pack import wrote say refinery in their
prose and name no command by it.

Nothing on a link to another yard carried the word, no event kind, setting,
flag, command, environment variable or template variable had it, and
nothing that is running is touched, so a yard and its peers can be upgraded
in any order and mid-flight. The next release drops the files under
`contrib/refinery/` and the old name in `yardr pack apply`'s flags and
for a hand-made group; a group an import has recorded as the pack's
`assembly` stays that, under whatever name it has.

How a newer build takes over:

- the server notices when a client of another build calls, or within about
  10 seconds anyway. It refuses that one call (the client retries), stops
  accepting, waits up to 5 seconds for connections to go idle and up to 10
  more for calls still running, closes the store and execs the new build.
  Clients keep retrying for 30 seconds meanwhile, longer than that drain and
  the new build's start (and its migration, if the schema moved), so a
  command issued during a replace is answered by the new build rather than
  failed; a client that finds no answer but a live pid in `server.pid` waits
  as long rather than starting a second server. Event and wake streams are
  ended when the drain starts, and a stream whose reader has stopped taking
  is closed once a write has waited 10 seconds, so it cannot hold the drain
  up either;
- the daemon checks between passes, at least every 10 seconds; it finishes
  the pass it is in, then execs. Sessions live in the store, so the new
  daemon carries on with them. A daemon or server told to stop while a
  replace is pending stops; the new build is not started.

Each replace is logged in `server.log` (or the daemon's output) and as a
`server_replaced` or `daemon_replaced` event with `from` and `to` revisions.

A daemon that runs as a [login service](#login-services) upgrades the same
way, and the service manager has no part in it: an `exec` keeps the pid, so
launchd and systemd see one process that never ended, and start nothing. The
service's file names the binary by its path, which `go install` writes the
new build to, so the next login starts the new build too. When the `exec`
fails, the daemon logs it and runs on as the old build, as it does without
a service. The web view does not replace itself, service
or not: it is a client of the server and keeps running the build it was
started from until it is restarted (`yardr web install` again does that).

On macOS an upgrade can cost the service its access to `~/Desktop`,
`~/Documents` and `~/Downloads`. A yardr from `go install` is signed ad hoc
(`codesign -dv` says `Signature=adhoc`, `flags=…(adhoc,linker-signed)`, no
team): it has no identity but the hash of its code, so a new build is a new
program to the privacy rules, and a grant given to the old one does not
cover it. If a depot under one of those folders fails with `operation not
permitted` after an upgrade, grant the new build again
([above](#login-services)), or keep depots out of those folders, which no
upgrade touches. That the binary is signed ad hoc is checked; that the
grant is lost is the rule for such a binary and was reported from one Mac,
not reproduced.
An older client never makes a server exec anything, and neither does an
older build installed over the binary. If the server cannot
replace itself (the binary is gone, the exec fails, or it is newer than the
client), a call it does not understand fails with
``the yardr server (pid N, built <rev>) differs from this yardr (<rev>); run `yardr server stop` ``;
once it is stopped, the next command starts a server of its own build. A server from before
this mechanism has to be stopped by hand once.
