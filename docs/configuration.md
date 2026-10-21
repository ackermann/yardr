# Configuration: the repo and the store

yardr reads its configuration from two places:

- **the depot's repository**, under `.yardr/`: a flow, stage files and roles
  of the depot's own, for a depot that keeps them, versioned with the code
  and changed through the depot's review.
- **the store** (`yard.db`, owned by the yardr server): who works it, where
  and how, and what tests it. Live state, changed with `yardr` commands, no
  commit needed.

A depot's way of working (its flow, the stage files and the roles of its
groups) comes from a [pack](packs.md): `yardr depot init` imports the yard's
default pack into the store and the yard's home, and writes nothing into the
repository ([below](#seeding-a-depot-yardr-depot-init)).

## What lives where

| | repo `.yardr/` (what the work is) | store (who / where / how) |
|---|---|---|
| flows | a depot's own default and type flows, named flows: `.yardr/flows/`; none in a depot set up from a pack | named flows, a pack's among them (`<pack>.<flow>`), and depot and type pointers to them: where the flow of a depot set up by `yardr depot init` is |
| stage files | a repository flow's: `.yardr/flows/<name>/<stage>.md`, read from the base | none in the store: a store flow's are files in the yard's home, `~/.yardr/flows/<name>/<stage>.md` ([flows.md](flows.md#stage-files-what-a-stage-is-for)) |
| tests | the test script, when the depot's test command names one by its path | the depot's test command, `depot add --test` ([Tests](#tests)) |
| security paths | `.yardr/security_paths`, read from the base: the paths whose change a reviewer may not call `done-safe` ([flows.md](flows.md#done-safe-and-the-security-paths)) | |
| roles | a depot's own role prompts: `.yardr/roles/<name>.md`, read from the base; none in a depot set up from a pack | each group's and crew member's `prompt`: for a depot set up from a pack the absolute path of the pack's role as installed in the home, `~/.yardr/packs/<pack>/roles/<name>.md`. `~/.yardr/roles/`, in a yard that has it from an earlier yardr, is the fallback of a relative `prompt` |
| groups | | runner, limit, members, subscriptions (manual), agent kind, model, args, cmd, worktree settings |
| routes | | which group works which stage, by depot, type, label |
| depots | | path, kind (`git` or `dir`), clone, base branch, test command; `depot add` registers, `depot rm` removes |
| crew members | | agent, directory, inbox kinds, rotation, subscriptions, identities |
| settings | | the yard's defaults ([Settings](#settings)) |

### Why the repo holds what the work is

A flow or a role a depot keeps under `.yardr/` is part of its repository:

- **Versioned.** It is tied to the code it applies to. Check out an old
  commit and you get the flow that applied then.
- **Reviewed.** Changing it is a code change like any other; it goes through
  the depot's review and lands on the base branch.
- **Portable.** Anyone who runs yardr on the repo gets the same flow,
  without copying store state.

Most depots keep none.
The flow and the roles describe a way of working: which stages a change
passes, who works each, how a builder or a reviewer goes about it. That is
the same for every depot worked that way, so it is a [pack's](packs.md), and
`yardr depot init` writes no flow, stage file or role into a repository. A
copy in each depot was a copy that fell behind the day the wording changed.

- **Where it is reviewed.** A change to a pack's flow or role is reviewed
  where the pack is kept, a repository of its own for a pack of yours, and
  reaches the yard when the operator runs `yardr pack apply`, which shows
  every change before it writes one ([packs.md](packs.md#yardr-pack-apply)).
  The import records the pack's commit.
- **A depot that keeps its own flow keeps the review.** Commit
  `.yardr/flows/default.flow` and its stage files on the base and run
  `yardr depot source <depot> flow depot`
  ([below](#switching-one-of-them)): the flow is then the repository's,
  read from the base, and a change to it lands through the depot's review.
  The same holds for a role: `.yardr/roles/<name>.md` on the
  base and `yardr depot source <depot> role <group> depot`.
- **Never from a bead's branch.** Either way a prompt and a flow are read
  from the base or from the yard's home, so a bead cannot change what
  judges it.

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

A depot's roles are read from the tip of the depot's base branch
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
that depot only. A prompt found nowhere fails the session start with every
path tried in the error (the depot's as `<base>:.yardr/roles/<name>.md`); three such failures
hold the bead.

A depot may also keep its own standing working rules at `.yardr/depot.md`:
how this depot is worked, for every agent session on its beads. The file is
appended to the brief after the role, as its own section (`## Working in
<depot>`), whatever the role is — the pack's, the depot's own, or the home's —
so the role stays generic and the depot's rules travel with it. It is read
from the tip of the depot's base like a role (a dir depot's from its
directory as it is): never from a bead's branch, so a bead cannot rewrite
what its agents are told. A depot without the file gets briefs as if it were
not there, and a file that cannot be read fails the session start as a
missing prompt does. The file is plain markdown with no placeholder filling:
the depot writes its literal names.

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
| `still` | `10m` | the agent herdr calls `working` showed a pane whose end stood unchanged this long: it counts as idle, whatever the pane shows, and `session_idle` fires at the `idle` limit with the end of its pane ([groups.md](groups.md#a-harness-that-stopped-on-an-error)) |
| `grace` | unset | when set, a session still idle (or still running) this long past its limit is stopped and counted as a failed session; `0` stops it at once. Unset, the defaults below apply |

Durations are Go durations (`45m`, `2h`) or a number of seconds; `off` or
`0` turns `idle`, `max_age`, `stall` or `still` off. The defaults apply to runners that report
an agent's status (herdr). For `exec` and `manual` groups only what the group
sets applies, and `idle` and `still` never do, since they cannot tell idle from busy.
The idle clock is the daemon's: one that replaces itself starts it again,
while the age is counted from the session's start and reported only once.

A manual group has no session to go idle. Its `wait` is how long a bead may
be in a stage of the group before a `waited` event, once per stay:
[groups.md](groups.md#a-bead-that-waits-long).

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

**An agent that waits for its own background work is not idle either.** A
session whose pane says, when it reaches `idle`, that a command its agent
started in the background still runs (the `background` lines of its kind's
harness file; Claude Code's `1 shell still running`) raises no
`session_idle`, and its idle spell starts again from that look, so `grace`
does not end it. `max_age` applies. See
[groups.md](groups.md#a-command-runs-in-the-background).

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
so a dead remote cannot stall the daemon. Where the base is a local branch
with an upstream, the branch itself is fast-forwarded to what was fetched,
without a checkout; a base checked out in a worktree is left as it is, as
is one holding commits its upstream lacks, and one that diverged from its
upstream is left with a `base_diverged` event on the bead, of the class
`attention`. Every other git call a start or a
cleanup makes (`worktree add`, `branch`, `worktree remove`) is cut off after
2 minutes on the same terms.

## Registering a depot

A depot is a git repository or a plain directory beads are worked in. For a
git repository, register an existing clone, or let yardr clone it:

```sh
yardr depot add webapp ~/projects/webapp                       # existing clone
yardr depot add webapp --clone git@github.com:me/webapp.git    # → ~/.yardr/depots/webapp
yardr depot add legacy ~/src/legacy --base origin/master       # base must resolve
yardr depot rm legacy                                          # store only; the clone stays
```

Bead branches (`yard/<bead>`) start from the depot's base, brought up to its
remote before each new worktree: a local base is fast-forwarded to its
upstream, and one that diverged from it is left, with a `base_diverged`
event. The worktree of a bead that tracks a GitHub pull request stands
on the pull request's head instead ([links](links.md)). Give beads a depot with `--depot`; routes can match on it. A
depot set up by `yardr depot init` gets groups whose roles are its pack's,
installed in the yard's home; a group set by hand with
`prompt=roles/<name>.md` reads the depot's `.yardr/roles/<name>.md` from its
base ([Role prompts](#role-prompts)). Depot names, like bead types and
dependency kinds, are lowercase letters, digits, `.`, `_` and `-`. What
`depot rm` removes, and when it is refused, is
[below](#yardrs-own-state).

A depot can say what tests it: `yardr depot add webapp ~/projects/webapp
--test 'make test'` ([Tests](#tests)).

A depot can also be a plain directory, with no git (`--kind dir`): the next
section says how the two kinds differ, and [dir-depots.md](dir-depots.md)
has a worked example.

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
  type pointing at a named flow (`yardr depot flow`).
- **Repo `.yardr/`**: the default that ships with the code.
- **None**: no flow means a bead may move to any stage.

Within one source a type-specific flow wins over the depot's flow; a source
wins as a whole, so a depot pointer in the store overrides the repo's type
files too (see [flows.md](flows.md)). `yardr bead show` and `prime` say
which flow governs a bead and where it came from: `pin`, `store`,
`repo@<sha>`, and the type when a type's flow matched.

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
rules  yardr           depot (.yardr/depot.md@add63e5)
```

- **flow**: each flow governing the depot, as `yardr flow show <depot>` resolves
  them: the depot's own (`default`), then one per type (`type/<type>`). With
  type flows, a stage is named `<type>:<stage>` (`default:<stage>` for the
  depot's own).
- **stage**: the stage file of each non-terminal stage of those flows.
- **role**: the prompt of each agent group a stage or route of the depot leads
  to, where a brief for a bead of the depot reads it, and of each crew member a
  pack imported into the depot names. Script and people groups have no role.
- **rules**: the depot's own standing working rules (`.yardr/depot.md`),
  appended to every agent brief after the role
  ([above](#role-prompts)), or `none` where the depot keeps no file. It
  shadows nothing and no pack provides one.

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
line. `pack <name> has a copy` and `pack <name> has flow <flow>` say what
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
- There is nothing to switch about rules: the file is additive, and removing
  it is deleting it.

`yardr depot flow` points a depot at any named flow, not only a pack's, and
`yardr pack apply --depot-roles` or `--pack-roles` chooses every role at once.

## Repo config comes from the base branch

This section is about git depots; a dir depot has no base and reads `.yardr/`
from its directory (see [Depot kinds](#depot-kinds-git-and-dir)).

yardr reads `.yardr/` from the tip of the depot's base branch
(`git show <base>:.yardr/...`), never from the bead's branch or worktree.
A bead therefore cannot change what judges it: a builder who edits
`.yardr/roles/reviewer.md` on `yard/<bead>` changes nothing for that bead.
The edit takes effect only once it has been reviewed and landed on the
base, like any other code, and then applies to every bead after it. That
holds for the flow and the roles of a depot that keeps its own under
`.yardr/` ([why](#why-the-repo-holds-what-the-work-is)), and for
`.yardr/security_paths`, the list that holds a reviewer's `done-safe` to the
paths it names: a bead that empties the list on its own branch is held to the
base's. yardr resolves the base whenever it reads them and re-reads the files
when its commit moves.

## Tests

A depot can say what tests it, in one field of the store: its test command.

```sh
yardr depot add webapp ~/projects/webapp --test 'make test'    # a command
yardr depot add webapp ~/projects/webapp --test scripts/test   # a script of the repository's, by its path
yardr depot show webapp                                        # prints it as test:
```

It is a command for `sh -c`, or the path of a script in the repository.
yardr never runs it. It hands it to whoever does:

- `yardr prime --bead` prints it under the bead's depot, so an agent that is
  to run the tests before it hands on need not guess the command;
- an `exec` group's command gets it as `YARDR_TEST`, unset for a depot with
  none ([groups.md](groups.md)).

`depot add` replaces the depot's record, so adding the depot again without
`--test` drops it. `yardr depot init` sets it for a git depot that has none,
as a guess from the repository
([below](#seeding-a-depot-yardr-depot-init)).
Only the operator may change an existing depot's path, base or test command;
anyone may add one or re-add it with those values unchanged.

**What the starter pack does with it.** Its builder and reviewer roles say
to run it in the worktree before handing on. Its merge script, `merge-local`,
runs it on the rebased commit before the base moves, with `YARDR_LANDING=1`
in the environment (unset for a wagon landing on its train's branch) and
for at most `YARDR_TEST_TIMEOUT` seconds (1800), besides what it waited for
the race lock. Exit 0 lands the bead.
Exit 2, the command saying it could not run, holds it. Any other exit sends
it back to its builder with the end of the output in a note. A depot with no
test command lands untested, and the note says so. Where the field is the
path of a file the base branch has, the script runs the base's copy of that
file, with `sh`, in the worktree: a bead cannot change the script that
judges it, and a change to the script counts from the next bead on.
Anything else it runs as a command, as the branch has it
([line.md](line.md#4-approved-the-assembly-lands-it-on-main)).

That is the pack's choice, not yardr's: another pack may test in a stage of
its own before a plain merge ([flows.md](flows.md)), or nowhere.

**How many run at once.** The heavy run is the landing's, and a group's
`limit` bounds it. Route the landing stage of every depot to one script
group, and that group's limit is how many landings, and so how many whole
suites, run at once across those depots
([groups.md](groups.md)). The runs builders and reviewers make in their
worktrees are bounded only by how many such sessions there are, so keep
what the test command does when it is run plainly cheap, and leave the whole
suite to `YARDR_LANDING`. Two yards on one machine are not bounded together.
Depot yardr's own gate serialises its race leg per machine: one `go test
-race` run at a time, under a lock file in the yard's home, and a gate that
has to wait says so (`test: waited 312 s for the race lock`). The deadline of
a landing, and of a train's integration, does not count that wait:
`merge-local` and `integrate-train` credit the waited seconds, so one late in
the queue is killed only when its own run outlasts `YARDR_TEST_TIMEOUT`. Race suites
run side by side slow each other past Go's per-package timeout while one
alone takes a third of the time, so the queue costs less wall time than the
contention. Waiters take the lock in arrival order, on tickets in
`~/.yardr/race.queue/` (a dead waiter's ticket is skipped and removed, never
blocking); a waiting gate says its place and the holder, at once and then
each minute (`test: race lock: 3rd in line, held by yardr-cayh for 6 m`),
and `sh .yardr/test --race-queue` shows the holder and the queue, oldest
first, without waiting for or leaving the lock held.

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
| `yard.db` | the store (bbolt); back it up with `yardr store backup <path>`, a path in `backups/` or in `backup_dir`, while it serves (one read transaction, so the copy is the store as of one commit), or have the daemon keep dated copies ([Copies of the store](#copies-of-the-store), which says how to restore one) |
| `backups/` | the dated copies of the store the daemon keeps once `backup_every` is set, unless `backup_dir` names another directory |
| `yard.sock` | the server's unix socket |
| `server.pid`, `server.pid.start`, `server.log` | the running server |
| `hooks.state` | what the running server takes webhooks on, or why it does not, which `yardr doctor` and the pack plan read ([below](#settings)); gone when the server stops |
| `edge.state` | how the running server's link to the edge is, written at each change: up and since when, or down and why, which `yardr status`, `yardr doctor` and `yardr edge status` read; gone when the server stops or the yard disconnects |
| `peer.key` | the yard's private key, by which the yards it is linked with know it ([peers.md](peers.md#the-key)); made by the first `yardr peer key`, `peer invite` or `peer pair`, mode 0600, and refused when it is more open |
| `daemon.lock` | held by the running daemon, with its pid and start time in it: one daemon per yard |
| `daemon.log` | what a daemon started in the background prints (the onboarding's "Start the yard now?", `yardr init --start`); one in a pane of its own prints there instead |
| `web.log` | what a web view no terminal runs prints: one started with `yardr web start`, or the login service's ([web.md](web.md#keeping-it-running)) |
| `webviews/` | a record per running web view, held by it while it serves, which `yardr status`, `yardr doctor`, `yardr web stop` and `yardr web open` read; the record of a view that acts holds its token, in a file of mode 0600 ([web.md](web.md#where-it-runs)) |
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

The yard lives in that one file, `yard.db`, owned by one process, the yardr
server, which every other yardr process reaches over a unix socket. Every
change is committed together with its event; events carry a sequence
number, so subscribers get every change pushed in order and resume from where
they stopped. Nothing polls the store for changes: the dispatcher wakes on
each committed write, re-reads state and acts idempotently, so a missed
wake-up only costs a delay (a pass every 30s is the safety net) and a restart
is just another pass.

`yardr watch` streams every event as it commits. Each line starts with the
event's sequence number; `yardr watch --after <seq>` resumes from there with no
gaps or duplicates, and a running watch rides out a server restart. With
`--json`, one event per line.

The server starts on first use, in the background (further down);
`yardr server status` says whether it runs and `yardr server stop` ends it.

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
scripts, which run elsewhere, are in the yard it named and not in one relative
to their own directory. So is a login service installed with the flag
([below](#login-services)).

Where yardr prints a command for a yard that is not in the default home
(what to run next in the setup, a fix in `yardr doctor`), the command
carries `--home <dir>`; for the default home it carries neither the flag
nor the variable.

Two yards on one machine share nothing but what they are given in common:
give each its own `hook_addr`, `web_addr`, `web_pages_addr` and `bdp_addr`
([settings](#settings); [web.md](web.md#the-address)); the BDP address is
needed where it serves BDP.
Their herdr agents are told apart by the yard's `name`, and each can be given
a herdr session of its own with `herdr_session`.

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
is started without them, so a
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
   adds nothing;
2. sets `operator` to the operator's name and `yardmaster` to the
   yardmaster's, `hearer` to the yardmaster's too unless one is stored
   already, and `operator_style` and `operator_note` when they were said
   (the form's answers, `--style`, `--about`);
3. adds the yardmaster crew member: `kind=<harness>`,
   `prompt=<home>/packs/<pack>/roles/yardmaster.md`, the model if given, the harness's
   unattended flags as `args`, rotating every 24h. A crew member of that name
   already there is kept as it is, so init runs again on a yard without harm;
4. sets `default_kind` to the harness, which marks the yard set up.

Init again on a yard that is set up changes only what its flags say: the
`default_kind`, `operator` and `yardmaster` settings stand in for `--harness`,
`--operator` and `--yardmaster` when those are not given, rather than being
reset to the flags' defaults (the login name, `yardmaster`); `operator_style`
and `operator_note` are kept the same way, changed only by `--style` and
`--about`, and `hearer` is kept as stored, which no flag names. The summary lists what was kept
(`kept default_kind claude, operator Jo, yardmaster yardmaster, as set before`).
A flag that changes a setting there is taken from the operator alone, as
`yardr settings set` is ([Settings](#settings)).
A crew member of the yardmaster's name is kept as it is, and init then reads
no pack and writes no role: an installed role is brought to a newer pack's
by `yardr pack apply`, which shows the change first
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
its own work on the line.

A yard without either setting, which is every yard from before them, primes
as it did: no section, and no style assumed. With only the note set the
section has the note and no style. `yardr setup` uses the style in one way:
with `guide` its pages print no command lines ([setup.md](setup.md#the-pages)).

The summary ends with the way to the yardmaster
([starting the yard](#starting-the-yard)), whose first job is the rest of
the setup, with the operator ([setup.md](setup.md)). `yardr init --help`
lists it as commands, for scripts.

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
signals the pid in `<home>/daemon.lock` — first verifying it is still the
daemon's, never a later process with its pid — and waits (`--wait`, default
30s) until the lock is free, since the daemon finishes the pass it is in. The
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
and how to go on. Of a session that runs without an answer, and of a start
that failed with every program there, what is known has one more line: what
one real call to the harness outside a pane came to, made once after the
failure, with a terminal or without; how to go on says what that usually
means, where it tells. `yardr init --start` then exits 1. The form opens no chat,
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

#### Starting and stopping a yard

`yardr up` starts everything a yard needs that is not running, and
`yardr down` stops the yard. Both are safe to repeat, both print one line a
part, and both take `--json`.

```sh
yardr up       # herdr's server, the yard's server, the daemon
yardr down     # the daemon, the yard's agents, the web view, the server
```

Neither is another way to start or stop a part: `up` is `herdr server`, the
server every command starts, and `yardr daemon start`; `down` is
`yardr daemon stop`, what the daemon does to a session it ends, and
`yardr server stop`. `yardr up --login` installs the login services too,
and `yardr down --login` removes them ([below](#yardr-up---login)).
`yardr uninstall` is `yardr down`, then the removal of every login service
yardr wrote for the yard, then a list of what it left on the machine
([install.md](install.md#removing-yardr)).

**`yardr up`** takes the parts in this order:

| part | what up does |
|---|---|
| herdr's server | one that answers is left as it is. Else up starts herdr's headless server, `herdr server`, detached, logging to `<home>/herdr-server.log`: the server of the herdr session the yard's `herdr_session` setting names, else of the shell's. It gets no `YARDR_*` variable, since one server serves every yard |
| the server | started as any command starts it |
| the daemon | `yardr daemon start`. It starts the crew, the yardmaster first, and dispatches the beads that wait. It is not started while herdr's server does not answer: every start of an agent would fail and count against it |
| the web view, the BDP server | only when the yard's `web_addr` or `bdp_addr` setting names its address: as `yardr web start` and `yardr bdp start` do. The telemetry export, which has no setting, is never started |

herdr is looked for on PATH, then with your login shell, as `yardr doctor`
looks for a program: a window starts yardr without what your profile adds to
PATH. When only the login shell finds herdr, what up starts gets that
shell's PATH after its own, and the line says so. When neither finds it, the
line says where it looked.

A part that could not be started has `NOT`, what kept it, and the one thing
to do. The exit status is 0 when herdr's server, the server and the daemon
run, 1 when one does not, 2 on a home with no yard.

**`yardr down`** stops, in this order: the daemon, so that nothing is
dispatched or started under it; every agent this yard started, a crew
member or a bead's session, and no other pane in herdr; the web view, the
BDP server and the telemetry export that run, which answer with an error
only once the server is gone; the server. It never stops herdr's server:
other yards and your own panes are in it.

- A bead whose session is stopped keeps its worktree and its branch, is
  back in its stage's queue, and has no failed attempt counted: the session
  ends as `aborted`, as one does that herdr could not start, so stopping a
  yard never holds a bead (`--max-attempts`). What the agent had not
  committed is in the worktree; the turn it was in is lost. After `yardr up`
  the daemon dispatches the bead again, into the same worktree.
- A crew member is asked for its handoff (`yardr crew handoff`) and stopped
  once it wrote one, or after `--handoff-wait` (default 2m; `0` asks for
  none). It stays enabled, so the daemon of the next `up` starts it, from
  that handoff.
- A manual group's claim is a person's turn and a peer group's a wait on
  another yard: neither is an agent, and both stay.

Before it stops an agent, down says what it will stop. The agents in the
middle of something come first, each by its bead or its crew name:
`working` (in a turn), `blocked` (at a question in its pane) or `unknown`
(it could not be asked). The idle ones are a count.

| who runs it | with an agent in the middle of something | with idle agents only |
|---|---|---|
| a person at a terminal | asks, starting at no | says how many and stops them |
| a script, stdin closed | refuses, stops nothing, exit 1 | says how many and stops them |
| either, with `--yes` | stops them | stops them |
| a program, `--json` without `--yes` | stops nothing: prints the agents with `"confirm": true` | the same: a program puts the question itself and calls again with `--yes` |

Idle agents alone are not asked about because nothing of theirs is lost.
With no agent at all `--json` stops the yard without `--yes`.

`yardr up --json` prints `home`, `up` and `parts`; `yardr down --json`
prints `home`, `down`, `confirm`, `agents`, `working`, `idle` and `parts`.
With `--login` both print `login` too ([below](#yardr-up---login)).
A part is `part` (`herdr`, `server`, `daemon`, `web`, `bdp`, `otel`),
`running` as it is after the command, `started` or `stopped` when this run
did it, `pid`, `url`, `detail`, and `error` with `do` for what could not be
done. An agent is `kind` (`bead` or `crew`), `name` (the bead or the crew
member), `stage`, `group`, `agent` (herdr's name for it), `state`,
`stopped`, `handoff`, and `error` with `do`. `yardr status --json` says what
runs without changing anything ([doctor.md](doctor.md#yardr-status)).

#### Long-running commands

Four commands run until they are stopped: `yardr daemon`, `yardr web serve`,
`yardr bdp serve` and `yardr otel export`. Each runs in a terminal under
that name, and each has the same verbs beside it, under `yardr daemon`,
`yardr web`, `yardr bdp` and `yardr otel`:

| verb | what it does |
|---|---|
| `start` | starts the command detached from the terminal, logging to `<home>/daemon.log`, `web.log`, `bdp.log` or `otel.log`, waits until it is up (it listens, or holds its file in the home) and prints its pid. It takes the flags of the command that a background one can use, and hands them on: `--interval` and `--max-attempts`; `--addr` and `--host` (on loopback, for the web view); `--addr`, `--scope` and `--host` (on loopback, for the BDP server); `--endpoint`, `--traces-endpoint`, `--logs-endpoint`, `--header`, `--after` and `--include-text` |
| `stop` | signals every one of the kind that runs on the yard, however it was started, and waits (`--wait`, default 30s) until it has ended |
| `status` | says which run, with pid and address, and whether the login service is installed and loaded |
| `install`, `uninstall` | the login service ([below](#login-services)), with the same flags as `start` |

The web view and the BDP server listen on an address, and the command, its
`start` and its `install` all take it in the same order: `--addr` when it is
given, else `YARDR_WEB_ADDR` or `YARDR_BDP_ADDR` when set in that command's
environment, else the yard's setting (`web_addr`, `bdp_addr`;
[settings](#settings)), else the default (`127.0.0.1:8791`,
`127.0.0.1:8765`). A yard that sets them needs no flag at any start.

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
  the install was given. The address is always written, whether it came
  from `--addr`, `YARDR_WEB_ADDR` or `YARDR_BDP_ADDR`, from the yard's
  `web_addr` or `bdp_addr`
  ([settings](#settings)) or from the default, so the file says where the
  service listens. It is read when the install runs and not again: a
  service that is installed keeps the address in its file when the setting
  changes, until `yardr web install` (`yardr bdp install`) writes it again,
  and `settings set` says so. A yardr that moves needs the install again;
- its whole environment besides what the manager sets: `YARDR_HOME` (the
  home the install ran for, `--home` included), `HOME`,
  and the `PATH` of the shell the install ran in. herdr, git and every
  harness have to be on that `PATH`; the install names the ones that are
  not. Of the rest of that shell it takes the settings the server and the
  daemon read (`YARDR_HOOK_ADDR`, `YARDR_WEB_ADDR`, `YARDR_WEB_AS`,
  `YARDR_BDP_ADDR` and the three `YARDR_*_RETENTION`) and
  where herdr is when that is not the default (`HERDR_SOCKET_PATH`,
  `XDG_CONFIG_HOME`), and nothing else: no credential, no identity. A
  yard whose [`herdr_session`](#settings) is set needs no
  `HERDR_SOCKET_PATH` there: the daemon reads the setting from the yard
  and reaches that session from a service installed in any shell. A
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

##### `yardr up --login`

`yardr up --login` is `yardr up` that stays: it installs the login service
of every part it starts, and a part that does not run is started by its
service. `yardr down --login` stops the yard and removes them.

| part | `up --login` | `down --login` |
|---|---|---|
| herdr's server | writes `dev.yardr.herdr.plist` or `yardr-herdr.service`, a service of the user and of no yard: `herdr server` by the path your login shell finds it at, with that shell's `PATH` (and `XDG_CONFIG_HOME` when set), your home as working directory, restarted on a failure only. With a server running, the file is written and the server left as it is: launchd loads the file at the next login, systemd is told `enable`. With none, the service is loaded and starts it. One of yardr's that is there is `found`, and nothing is written | removes the file and tells systemd `disable`; never unloads it, so the server and its panes run on. Left, and said, while another yard's daemon service is in the directory: that yard starts at login and needs it |
| the daemon | `yardr daemon install`, unless its service is installed and loaded. A daemon that runs is stopped for the service's | `yardr daemon uninstall` |
| the web view, the BDP server | `yardr web install`, `yardr bdp install` with the address of the yard's `web_addr`, `bdp_addr`: only for a yard that sets it, and unless the service is installed. One that runs detached is stopped for the service's | `yardr web uninstall`, `yardr bdp uninstall`, for a yard that sets the address. On a yard that does not, a service that is installed is one `yardr web install` wrote: left, and said |

A service for herdr's server that yardr did not write is the person's: any
`.plist` or `.service` in the user's service directory or the machine's
that runs `herdr server`, and a file under yardr's own name without yardr's
first line. `up --login` reports it as `left` with the files as `foreign`
and writes none; `down --login` leaves it
([install.md](install.md#keep-it-running-after-a-reboot)).

The `PATH` written into the services is the login shell's, with what the
shell `up --login` ran in has beyond it after: a window starts yardr
without what a profile adds. Without a login shell to ask (`SHELL` unset) it
is the PATH of the shell it ran in.

`login` in `--json` is a list, one for each service: `part` (`herdr`,
`daemon`, `web`, `bdp`), `manager`, `name`, `file`, `result`, `loaded`,
`foreign`, `detail`, `warnings` (what `yardr daemon install` warns of) and
`error` with `do`. `result` is `installed`, `found` or `left` after `up`,
and `removed`, `absent` or `left` after `down`. A service that could not be
installed or removed makes the exit status 1. `down --login` removes
nothing while it stopped nothing: after `--json` without `--yes` on a yard
with agents, or when the daemon could not be stopped. On a system yardr
writes no service for, `--login` is refused before anything is started or
stopped.

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
[below](#upgrading).

#### A run that did not finish

The onboarding and `yardr init` do their steps in one order: the yardmaster's
role, the `operator`, `yardmaster` and `hearer` settings, the yardmaster crew member, `default_kind`,
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
| a depot's `.yardr` files | in the repository, committed on its base branch: `.yardr/roles/`, `.yardr/flows/` | `git -C <repo> ls-files .yardr` | keep them: a new yard that adds the repository as a depot reads them as they are. To start the depot's configuration over too: `git -C <repo> rm -r .yardr`, and commit |

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
home as on the first day.

Starting over is seldom needed for what the onboarding asked: the harness,
your name and the yardmaster's are `yardr settings set` (as the operator), the yardmaster's
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
| the depot's [test command](#tests) | the store, for a git depot that has none, when the checkout says how it is tested: a guess. A test script of the repository's own is named by its path (`.yardr/test`, `scripts/test`); else `go.mod` gives `go vet ./... && go test ./...`, a `package.json` with a real `test` script `npm test` (`pnpm`/`yarn` by lockfile), `Cargo.toml` `cargo test`, a Makefile with a `test` target `make test`. A repository that says none of that gets no test command |

Nothing goes into the depot: no roles, no flow, no stage files, no script. They are
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
answered by the same flags (`--group`, `--bind`, `--members`, `--crew`,
`--flow`, `--open-threads`, `--calls`, `--depot-roles`). Nothing is written, and the
test command is not set, before a yes in the terminal or `--yes`. A second
run changes nothing and keeps the test command the depot has. To apply the
pack again after it changed, use `yardr pack apply`, which never sets one.

A depot that already has `.yardr/roles/` keeps the files, and the pack's
roles win: the plan names each one a group or crew member would have read as
`shadows .yardr/roles/<name>.md` before anything is written, and
`yardr depot sources <depot>` shows it afterwards. `--depot-roles` keeps the
depot's own instead. A depot that already has `.yardr/flows/` keeps those
files too, shadowed by the pointer to the pack's flow; `yardr depot source
<depot> flow depot` makes the depot's flow govern again
([switching](#switching-one-of-them)).

A dir depot gets no guess ([dir-depots.md](dir-depots.md#1-register-the-folder-and-set-it-up)):
nothing in a folder of documents says how it is tested, and nothing is
written into the directory. `starter` is refused there, since its
flow lands a bead on a base branch through an assembly and nothing merges in
a directory, and `dir` is refused on a git depot, whether `--pack` or the
setting named it.

```
$ yardr depot add web ~/src/web --base main
$ yardr depot init web --yes \
    --bind builders=claude --bind reviewers=claude --members backlog= --members decisions=jo
…the plan, then:
applied pack starter to depot web: 23 created, 0 changed, 2 unchanged

test: depot web is tested with go vet ./... && go test ./... (a guess from /Users/jo/src/web: check it with yardr depot show web)
To change it:
  yardr depot add web /Users/jo/src/web --base main --test '<command>'
```

The two unchanged are the yardmaster and its role, which `yardr init` made.
A yard whose yardmaster has another name gives `--crew yardmaster=<name>`.

### Copies of the store

Everything the yard knows is in one file, `yard.db` in its home. A yard
keeps dated copies of it by itself once it is told how often:

```sh
yardr settings set backup_every 1d            # a copy a day; unset or 0: never
yardr settings set backup_keep 14             # default 7
yardr settings set backup_dir /Volumes/nas/yard   # default <home>/backups
```

A copy is written in one of two directories and nowhere else: the one
`backup_dir` names, and `<home>/backups`, where `yardr upgrade` keeps its
copy whatever the setting says. `yardr store backup <path>` refuses a path
in any other directory, a directory below them included: the copy replaces
whatever is at the path.

The daemon makes the copy on the pass on which the newest one is older than
`backup_every`, the way `yardr store backup` does: through the running
server, in one read transaction, so nothing stops and the copy is the store
as of one commit. It is named by the date and time it was made, in UTC,
`yard-20261013T142233Z.db`, and readable by you alone. When the last copy
was made is read from those names, so a daemon that did not run for a while
makes the overdue copy on its first pass. No copy is made while no daemon
runs.

After a copy the daemon removes its own older ones beyond `backup_keep`.
Its own are the files named exactly like that; a file with any other name
in the directory, such as a copy you made by hand
(`yard.db.before-the-move`), is never removed or changed.

The directory itself is made when it is not there, and what it is in is
not: a directory on a volume that is not mounted is a failure, not a
directory made on the disk beneath it. To have the copies on another
machine, name a directory you mount or sync.

A copy that cannot be made (the disk is full, the directory is gone), or
older copies that cannot be removed, is one `backup_failed` event with the
directory and the reason. It is of the class `attention`, so the yardmaster
hears of it ([subscriptions.md](subscriptions.md#the-class-attention)),
once: the daemon tries again after 30 seconds, then twice as long each
time, up to an hour, and says nothing more until a copy has been made and
a later one fails. `yardr status` has a `backups` line: how often, how many
and where, and when the last copy was made, or that none is kept.

The copies hold the store alone. The other files of the home (the yard's
key, `.env` files, worktrees) are not in them.

**Restore** a copy by stopping the yard and putting the file in the store's
place:

```sh
yardr down                                   # nothing may hold yard.db
cp ~/.yardr/yard.db ~/.yardr/yard.db.before-restore
cp ~/.yardr/backups/yard-20261013T142233Z.db ~/.yardr/yard.db
yardr up
```

The yard is then as it was when the copy was made: what happened since is
not in it.

### Settings

`yardr settings get [<key>]` and `yardr settings set <key> <value>` (an empty
value unsets) keep the yard's own defaults in the store. The store knows
which settings there are and what each takes, so a key that is none of
them, or a value its setting does not take, is refused with the keys there
are, whoever writes it.

**Settings are the operator's to change.** They say who the operator is,
where the server takes webhooks and what every later session is told, so
`yardr settings set` is taken only from the operator: the command's actor
(`YARDR_ACTOR`, `--actor`, else the login name) is the `operator` setting.
Anyone else is refused with `only the operator (<name>) may change a
setting; this is <actor>`, nothing is stored and no `setting_changed` event
is written. That holds for every setting, `operator` among them: the
operator names the next one. No crew member and no bead's agent sets one,
and there is no grant that lets one. Reading (`yardr settings get`) is
anyone's. A crew member that wants a setting changed proposes it with a
run ask ([chat.md](chat.md#questions-for-you)), for the settings the list
names; `operator`, `edge` and the settings of where the yard reads and serves
from stay the operator's to type. `hook_public_url` binds nothing and is a run
ask's: its card shows the whole URL each hook is registered at. Like the other things only the operator does
(adding a user or a peer), this is a line against mistakes, not a wall:
nothing checks who is at the keyboard, and an agent is known by its pane's
process tree, which one that sets out to can leave
([api.md](api.md#who-may-make-a-call)).

A yard that names no operator yet has nobody to hold a setting to. It
takes one from anyone who is no agent of its own: no crew member, and no
call from the pane of a bead's session or of a crew member. That is how
`yardr init` names the first operator, and how a setting is stored before
any init. `yardr init` names the operator in its first setting and writes
the rest of a new yard's as that operator, also when `--operator` is not
the name the terminal acts as. If setup is cut short after that first
setting, a later `init` must act as the stored operator to finish it. On a
yard that is set up, init changes a setting as `settings set` does, as the
operator alone; a setting that already has the value init
would write is left untouched, so init again changes nothing from any
terminal.

`yardr settings get` prints the value the yard goes by and where it is
from: `set`, `default` (the setting is unset), or `environment`
(`YARDR_HOOK_ADDR` is used instead of its setting; the environment is that
of the command, and a server goes by the one it was started in). A setting that is unset and has no default shows
`-`. With a key it prints the value alone, and fails for a setting with no
value; `--json` has where it is from.

| key | meaning |
|---|---|
| `default_kind` | the agent kind a herdr group or crew member gets when `group add` or `crew add` is given no `--set kind`; also the kind their forms start at. One of the kinds herdr starts. Its presence marks the yard set up. |
| `default_pack` | the pack `yardr depot init` sets a depot up from when it is given no `--pack`, and the pack `yardr init` takes the yardmaster's role from: a pack's name (one the yard has imported, or one in the binary) or its directory as an absolute path ([packs.md](packs.md#naming-a-pack)). Unset, `starter`, and `dir` for a dir depot. A relative directory is refused, since the setting is read wherever `depot init` is run, and so is a directory with no `pack.yaml`. Changing it moves nothing: it is read when a depot is set up. |
| `hearer` | the crew member an approved claim subscribes to the claimed address beside writing it, who then hears that chat ([integrations.md](integrations.md)); `yardr init` stores the yardmaster's name unless one is stored already. Unset, or naming no crew member, an approval writes the address alone. A crew member may propose it as a run ask. |
| `operator` | the person the role prompts name, agents bring decisions to; `yardr init` stores it (`--operator`, the form's "Your name") and an import fills a pack's roles with it. The operator is a user ([users.md](users.md)): setting a name adds the user when there is none, a crew member's name is refused, and only the operator adds and removes other users. |
| `operator_style` | how agents should work with the operator: `guide`, `show`, `work` or `report` ([what each tells an agent](#how-agents-work-with-you)). `yardr init` stores it (`--style`, the form's "How should agents work with you?"); both primes print it as instructions, so a change reaches the next session that starts. Unset, the primes say nothing about it. |
| `operator_note` | one line agents should know about the operator (`--about`, the form's "Anything agents should know about you?"), printed by the primes with the style, or alone. |
| `yardmaster` | the yardmaster crew member's name, whom the builder and reviewer roles tell agents to mail about what blocks them ([crew.md](crew.md#findings-from-builders-and-reviewers)); `yardr init` stores it (`--yardmaster`). Which crew member takes a pack's `yardmaster`, and so whom its roles name, is the import's `--crew yardmaster=<name>`. They may shape the yard as the operator may: restart, enable and remove crew members (`yardr crew restart`, `yardr crew start`, `yardr crew rm`), and remove groups, flows and routes (`yardr group rm`, `yardr flow rm`, `yardr route rm`); anyone else is refused in words naming both. The yardmaster cannot remove itself; only the operator removes the yardmaster. |
| `hook_addr` | the TCP address the server takes webhooks on, `127.0.0.1:8790` say ([subscriptions.md](subscriptions.md)). The server reads it when it starts, so a change takes its next start: `yardr server stop`, and the next command starts it again; `settings set` says so. `YARDR_HOOK_ADDR`, set where the server starts, is used instead of it. An address that is not `host:port` with a port from 1 to 65535 is refused (`:8790` is every interface). The server writes what it listens on to `hooks.state` in the home, and `yardr doctor` reports it: the address and whether it is the variable's or the setting's. A server that cannot listen there (another yard has the address) keeps serving its yard, logs a `hook_listener_failed` event, and doctor says so with what to change. |
| `hook_public_url` | the public address hooks are reached at, such as `https://yard.example/hooks`: the URL of hook `<name>` is it plus `/<name>`. The operator gives it, on a card or in a terminal; no proxy or tunnel address is worked out. A hook whose source names a register call with scheme `secret` is registered at that URL when its card is approved (the yard makes its secret and calls the service); no card for one is written while it is unset, and the refusal names this setting. An absolute `https` address (plain `http` to this machine alone, for a stand-in), with no user, query or fragment, is taken. |
| `edge` | the edge this yard is connected to, one JSON value that `yardr edge connect` writes and `yardr edge disconnect` clears. `url` is the edge's address, its origin and nothing after it: `https`, or plain `http` to this machine alone for a stand-in. `key` is the edge's public key, which the yard pinned when it connected and never fetches again, so a different certificate, address or key served later moves nothing. `handle` is what the edge calls this yard, and its hooks are reached under it. `hook_public_url` is the `hook_public_url` that connecting replaced, which disconnecting puts back (absent when there was none). Connecting also sets `hook_public_url` to `<url>/hooks/<handle>`, so hooks print and register the right URL. A yard connects to one edge at a time. While it is set, the running server keeps the edge link: it dials the edge at once (no restart: it follows the setting), checks the edge's challenge against the pinned key, and serves what comes down the link with the listener's own handlers, with or without `hook_addr`, though never `/peers/`; a dropped link is dialled again after 1 second, doubling to 60, with jitter. It logs `edge_link_up` and `edge_link_down` (with the reason: `closed`, `unanswered pings`, `refused key`, `refused`, `unreachable`, `stopped`) and writes `edge.state`, which `yardr status`, `yardr doctor` and `yardr edge status` show. Only the operator sets this setting; a run ask cannot. |
| `router_addr` | the address of the yard's router on the user's network, `172.18.0.2` say, written by the provisioner of a hosted yard: `GiveSecret` is taken from that peer when it says the request came to it over https (`X-Forwarded-Proto: https`), and from the yard's own machine as before ([api.md](api.md#tls-is-yours-and-the-clients-rule)). An IP address, and nothing else, is taken: a name or a `host:port` is refused, since it is compared with the address the request came from. Read on every call, so a change moves nothing that runs; unset, a secret is taken from the yard's own machine alone. `ApproveAsk` stays loopback-only. |
| `web_addr` | the address the web view listens on when `yardr web serve`, `yardr web start` or `yardr web install` is given no `--addr` and `YARDR_WEB_ADDR` is unset ([web.md](web.md#the-address)); default `127.0.0.1:8791`. The flag wins over the variable, which wins over this setting. It is held to what `--addr` takes without `--unsafe`: a loopback address with a port (`127.0.0.1`, `::1` or `localhost`), and any other is refused with the flag's sentence. Nothing that runs is moved: a view that runs keeps its address until it is stopped and started again, and a login service keeps the one in its file until `yardr web install` is run again ([login services](#login-services)); `settings set` says so, with the file's address when a service is installed. |
| `web_pages_addr` | the address the web view's sandbox origin listens on when `yardr web serve`, `yardr web start` or `yardr web install` is given no `--pages-addr` and `YARDR_WEB_PAGES_ADDR` is unset ([web.md](web.md#the-address)); default `127.0.0.1:8789`. The flag wins over the variable, which wins over this setting. It is held to what `--pages-addr` takes without `--unsafe`: a loopback address with a port, and any other is refused with the flag's sentence. Nothing that runs is moved: a view that runs keeps its address until it is stopped and started again, and a login service keeps the one in its file until `yardr web install` is run again ([login services](#login-services)). |
| `web_pages_host` | the public name run links use for the web view's sandbox origin behind a proxy of yours, when `yardr web serve`, `yardr web start` or `yardr web install` is given no `--pages-host` and `YARDR_WEB_PAGES_HOST` is unset ([web.md](web.md#behind-a-proxy)); unset, run links use the host the view was reached by. The flag wins over the variable, which wins over this setting. A wildcard, an empty name and a name with a scheme or a port are refused, as `--pages-host` refuses them. |
| `bdp_addr` | the same for the BDP server (`yardr bdp serve`, `start` and `install`); default `127.0.0.1:8765`. The flag wins over `YARDR_BDP_ADDR`, which wins over this setting. It is held to what `--addr` takes without `--unsafe`: a loopback address with a port (`127.0.0.1`, `::1` or `localhost`), and any other is refused with the flag's sentence ([bdp.md](bdp.md#loopback-only)). |
| `web_as` | the user the web view acts as when `yardr web serve`, `yardr web start` or `yardr web install` is given no `--as` and `YARDR_WEB_AS` is unset ([web.md](web.md#acting-as-a-user)); unset, the view reads only. The flag wins over the variable, which wins over this setting. Whoever it is must be a user of the yard: with any other name the view refuses to start. Nothing that runs is moved: a view that runs keeps its user until it is stopped and started again. A login service keeps the `YARDR_WEB_AS` from its install environment, if set; otherwise its view reads the setting at every start, since `--as` is written into its file only when it is given. |
| `web_jwt_issuer` | the issuer a gateway's token must carry (its `iss` claim), such as `https://team.cloudflareaccess.com`; setting it turns the web view's JWT mode on: every request but `/health` must then carry a token the gateway signed ([web.md](web.md#behind-an-auth-gateway)). An absolute `http` or `https` URL, and nothing else, is taken. Nothing that runs is moved: a view that runs keeps its mode until it is stopped and started again. |
| `web_jwt_audience` | the audience a gateway's token must carry (its `aud` claim), such as an Access application's AUD tag; required in JWT mode, so a token minted for another application is refused. One word. Read when the view starts, with the rest of JWT mode. |
| `web_jwt_jwks` | the URL the keys that verify a gateway's tokens are fetched from, such as `https://team.cloudflareaccess.com/cdn-cgi/access/certs`; required in JWT mode. An absolute URL; plain `http` is allowed only for test keys on loopback or for the hosted yard's router at the exact host `router`; other hosts require `https`. The view fetches the keys when it starts and refuses to start while they cannot be fetched. |
| `web_jwt_header` | the header carrying the gateway's token; default `Cf-Access-Jwt-Assertion`. A `Bearer` prefix is stripped on `Authorization` only. Read when the view starts, with the rest of JWT mode. |
| `web_jwt_users` | whose token acts as whom in JWT mode: `email=user` pairs separated by commas, each user a user of the yard, matched without regard to case. A token whose email maps to no user is refused with 403, and the view refuses to start when no user of the yard has a name in the table. Read when the view starts, with the rest of JWT mode. |
| `max_sessions` | the most sessions that run on this machine at once, over all groups: what the machine carries, beside each group's `limit` ([groups.md](groups.md#how-many-sessions-the-machine-carries)). A whole number; unset, nothing is capped, and `0` pauses the yard: nothing new starts until it is raised ([groups.md](groups.md#pausing-the-yard)). The daemon reads it on every pass, so a change holds on the next one, with no restart. |
| `backup_every` | how often the daemon makes a dated copy of the store ([Copies of the store](#copies-of-the-store)): a duration such as `1d` or `6h`. Unset or `0`, never, which is how a yard starts. The daemon reads it on every pass, so a change holds on the next one, with no restart. |
| `backup_keep` | how many dated copies the daemon keeps; a whole number from 1, default 7. It removes its own older copies beyond that, and no other file. |
| `backup_dir` | the directory the dated copies are kept in, as an absolute path; unset, `backups` in the yard's home. |
| `herdr_session` | the herdr session the yard's agents run in, by its name in `herdr session list` (letters, digits, `.`, `_`, `-`; `default` is herdr's first). Set, every call the yard makes to herdr goes to that session, from the daemon, from any `yardr` command and from a login service, whatever shell each was started from: the yard works out the session's socket (`<herdr's config directory>/sessions/<name>/herdr.sock`, `herdr.sock` for `default`) and uses it for herdr's API and, as `HERDR_SOCKET_PATH`, for the `herdr` program, which takes the socket before its own `HERDR_SESSION`. Unset, each process uses the session of the environment it was started in, as herdr itself would: `HERDR_SOCKET_PATH`, else `HERDR_SESSION`, else herdr's default session. That is the shell's and not the yard's: a daemon started from a pane of one session and a command run in another, or a login service installed from a shell with no herdr variables, then look in different sessions, and the one that finds none of the yard's panes takes its crew and sessions for gone. Name the session on any machine where herdr has more than one. The daemon reads the setting before every call to herdr, so a change needs no restart; a web view or an open `yardr chat` follows within seconds. It is changed only while nothing of the yard runs in a pane: `settings set` refuses while a crew member runs or is being started, or a bead's herdr session is live, and lists them, because their panes are in the old session and are not moved. Stop the daemon and the crew and let the sessions end first. One change moves no pane and is taken while they run: from unset to the session their panes are in, which is how a yard that already runs names its session (`yardr status` shows it). The yard goes by what it recorded for each pane when it made it, the session that call went to, never by the shell `settings set` is run from; a pane in another session, one started by a build that recorded none, and a bead's session that has no pane yet are refused and listed as before. yardr starts and creates no herdr session: one herdr does not have, or one that is stopped, is reported as herdr's server not running, with the session's name (`herdr session attach <name>` starts it). `yardr status` and `yardr doctor` say which session and socket are in use, and whether it is the setting's or the shell's. |
| `name` | the yard's name, which its crew's herdr agents and workspaces carry (`<name>-yardmaster`, labelled `<name> yardmaster`), so two yards on one machine, whose agents herdr lists together, are told apart. Unset, the home's directory name: `yardr` for `~/.yardr`, `y1` for `/tmp/y1`. Changing it renames crew members started from then on. |
| `timer_limit` | how many live timers one crew member or user may have set ([timers.md](timers.md#limits)); a whole number from 1, default 5. |
| `timer_min_every` | the shortest period of a recurring timer (`yardr timer add --every`): a duration such as `30m` or `1d`, default `1h`. |
| `blob_max_bytes` | the most bytes one file in mail may have ([users.md](users.md#a-file-in-mail)); a whole number from 1, default 8388608 (8 MiB). A larger one is refused when it is put, by `yardr mail --file` and by `PutBlob` on the socket; files the yard already holds stay. |
| `files_max` | how many files one mail or note may carry ([users.md](users.md#a-file-in-mail)); the yard answers its own count, 64, and it is not set. |
| `peer_file_max_bytes` | the most bytes one file sent to a linked yard, or taken from one, may have ([peers.md](peers.md#a-file-with-the-mail)); a whole number from 1 to 33554432 (32 MiB). Unset, it is `blob_max_bytes`, and never more than 32 MiB. A file over it is refused before its mail is queued here, and refused with its size when it comes from a peer. |
| `upgrade_from` | the depot whose landed build the yard installs itself after the assembly lands on it (`yardr upgrade`), with the scripts it already runs from `<home>/scripts` ([upgrade.md](upgrade.md#the-scripts)); unset, nothing is installed. Setting it is refused while the operator names no notify source, and names how to set one (`yardr pack apply pushover` or `yardr pack apply telegram`, with `--notify`): with none, a failed install could tell nobody outside the yard. Setting it subscribes the operator to the install's failure kinds (`install_failed`, `rolled_back`) in the same write, and while it is set unsubscribing from either kind is refused: remove the setting first ([subscriptions.md](subscriptions.md#users-as-subscribers)). It names a depot of the yard: setting it to one the yard has not got is refused, and the named depot is not removed until the setting is cleared. For one run without it, `yardr upgrade --from <depot>` names the depot instead, and the caller takes a failure through the exit status and the terminal. |
| `upgrade_restore` | whether a failed `yardr upgrade` restores the pre-install store copies (`true`) or leaves refused stores down for a person to restore (`false`); default `true`. The setting governs the whole run, every covered yard with it; `YARDR_UPGRADE_RESTORE` and `yardr upgrade --no-restore` override it, the flag for one run ([upgrade.md](upgrade.md#rollback)). |
| `quiet_landings` | how many code landings on a depot's base pass before the daemon owes the whole suite a run on a quiet machine ([testing.md](testing.md)); a whole number, default 10 (about two hours at the 60-140 landings a day of 14-16 Oct 2026). `0` turns the quiet run off. The daemon reads it on every event it judges, so a change holds from the next one, with no restart. |
| `disk_keep_gb` | the gigabytes of free space on GOCACHE's volume below which a landing keeps the disk before its tests run ([setup.md](setup.md#the-machines-disk)): it rotates GOCACHE at a fresh sibling directory, removes a rotated directory once it has stood aside for 30 minutes, and clears go-build directories in GOTMPDIR older than 3 hours. A whole number, default 30. `0` turns the keeping off. The assembly reads it on every landing, so a change holds from the next one. |

Environment variables yardr reads:

| variable | meaning |
|---|---|
| `YARDR_HOME` | state directory (above), when `--home` is not given; a relative path is resolved against the command's directory, once |
| `YARDR_ACTOR` | who is acting, for the event log (default: the OS user). A command whose actor is a user's name acts as that user ([users.md](users.md)); nothing checks who is at the keyboard |
| `YARDR_BEAD`, `YARDR_CREW` | defaults for `prime` and crew member commands; set for the sessions yardr starts |
| `YARDR_SESSION` | the session yardr started this process for; every bead move (`bead done`, `advance`, `close`, `hold`, `release`, `reopen`) sends it, and the store refuses to move the session's own bead once the session no longer owns it, and any other bead a live session owns. A call from the session's pane is checked as it even with the variable unset ([line.md](line.md#2-new-a-builder-implements-it)). In a bead's worktree, an environment naming another bead gives way to the worktree's session ([groups.md](groups.md#codex)) |
| `YARDR_HOOK_ADDR` | TCP address the server takes webhooks on, e.g. `127.0.0.1:8790`, read where the server starts; set, it is used instead of the `hook_addr` setting ([above](#settings)). Sessions do not inherit it |
| `YARDR_UPGRADE_RESTORE` | `true` or `false`, read by `yardr upgrade`; set, it is used instead of the `upgrade_restore` setting ([upgrade.md](upgrade.md#rollback)) |
| `YARDR_LINEAR_API`, `YARDR_GITHUB_API` | where `yardr reply`, `yardr thread open` and `yardr thread state` send their calls, read by the command that makes them, and where the daemon sends a hook's issue moves ([links.md](links.md#a-beads-stage-moves-the-issue)), read when it starts (default: Linear's GraphQL endpoint, `https://api.github.com`) |
| `YARDR_EVENT_RETENTION`, `YARDR_HOOK_PAYLOAD_RETENTION`, `YARDR_INBOX_RETENTION` | how long the daemon and `yardr store prune` keep events, ended sessions and webhook bodies, and how long an unread inbox event holds the log back ([below](#retention-keeping-yarddb-bounded)) |
| `YARDR_PPROF` | measuring: a path; the server serves Go's `net/http/pprof` on a unix socket there (mode 0600) for as long as it runs, read where the server starts. `curl --unix-socket <path> 'http://x/debug/pprof/profile?seconds=60'` is a CPU profile of a minute. Unset, nothing listens |
| `YARDR_TEST_RPC` | tests: run the store suites through the unix-socket client |
| `YARDR_STORE_NOSYNC` | tests: `1` opens `yard.db` without syncing each commit to the disk, which is most of a test run's time on macOS. The test suites set it for their throwaway yards (`0` keeps the sync). Never set it for a yard you keep: its store can be lost when the machine goes down |

Exec commands and herdr bead agents also get `YARDR_SESSION`, `YARDR_STAGE`,
`YARDR_GROUP`, `YARDR_KIND`, `YARDR_MODEL` and `YARDR_GIT_SYSTEM_CLEAN` (see [groups.md](groups.md)). They get the session's
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

The files mail carries are beside `yard.db`, in `blobs/` in the home
([users.md](users.md#a-file-in-mail)), and no knob keeps them: a file
goes with the prune once no mail left in the log names it, so it lives as
long as the last letter that carries it. One put less than an hour ago
stays whether or not mail names it yet. `yardr store prune` says how many
went, and the `store_pruned` event counts them (`blobs`).

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
  `--yes` and the answers an import asks without a terminal. `--operator`,
  `--yardmaster` and the flag that asked for a yardmaster's role file are
  gone: the operator a pack's roles name is the yard's `operator` setting,
  `--crew yardmaster=<name>` says which crew member takes the pack's
  `yardmaster`, and a dir depot's roles are its pack's.

The store is stamped with its schema version on open, and a yardr whose schema
is older than the store's refuses to open it
(`store schema is newer than this yardr knows`) rather than run its migrations
over it.

#### The check yardr ran itself

Until schema 19 yardr ran a depot's check before a bead landed: `yardr depot
check`, with `.yardr/check` and `.yardr/check-light` read from the base. It
no longer does. A depot has a [test command](#tests), and the script of the
landing stage runs it. Nothing of the old is still read:

| gone | instead |
|---|---|
| `yardr depot check` (and `--light`, `--first`, `--slot-wait`) | run the depot's test command: `yardr depot show <depot>` prints it, and a script group has it as `YARDR_TEST` |
| `--check`, `--check-light`, `--allow-no-check` and `--light-slot` of `yardr depot add` | `--test`. The first start of this build drops the four fields from each depot, and gives a depot that had `--check` and no test command that command as its test command; the event `depots_migrated` says what each depot held |
| `.yardr/check`, `.yardr/check-light` and `.yardr/check-fail-pattern` in a repository | nothing reads them. Keep the script and name it: `--test .yardr/check`. One script can do for both runs by reading `YARDR_LANDING` |
| the setting `gate_slots`, `YARDR_GATE_SLOTS`, `YARDR_GATE_FIRST`, `YARDR_GATE_DIR`, `YARDR_GATE_SLOT` | the `limit` of the group that lands ([Tests](#tests)). The setting is removed from the store |
| `YARDR_CHECK_BASE` in a check's environment | `YARDR_BASE`, which a session and a script group have |
| the `GATE` column of `yardr session list`, `gate` in its `--json` and in the web view's `/sessions`, the `gates` line of `yardr status` | nothing: yardr does not know that a test runs. An agent that waits at its prompt for tests that take longer than its group's `idle` gets a `session_idle`, unless its harness shows that they run in the background ([above](#session-limits-and-outages)); raise the limit |
| `~/.yardr/gates/`, `~/.yardr/gating/` and `~/.local/state/yardr/gates/` | not written any more; remove them |

A copy of `merge-local` or `integrate-train` from before calls `yardr depot
check` and fails. Apply the starter pack again (`yardr pack apply starter
--depot <depot>`), and copy this build's `contrib/assembly/` scripts over
any under `<home>/scripts/`. A role or stage file of your own that says
`yardr depot check` names a command that is gone: edit it.

#### Words yardr had before

A build up to v0.6.0 still read the words yardr had before (mayor for
yardmaster, epic and part for train and wagon, resident for crew, rig for
depot, refinery for assembly) in commands, flags, settings, filters,
`YARDR_` variables, pack files and under `contrib/refinery/`; this build
reads only the new ones, and still renames what a store from before them
holds when it first opens it.

A depot's pointer at a flow named `<name>.epic`, `<name>.part`, `type/epic`
or `type/part` is pointed at `<name>.train`, `<name>.wagon`, `type/train` or
`type/wagon` when the store opens, and a `flows_migrated` event says what it
was. A bead that is not closed and pins such a name (`flow=<name>.epic`) has
its pin rewritten the same way, in the same event. A pointer or a pin at a
flow the store itself holds under the old name is left: it names that flow.
A flow file in a repository still called
`<name>.epic.flow` or `<name>.part.flow` is not found any more: rename it to
`<name>.train.flow` or `<name>.wagon.flow`.

#### How a newer build takes over

- the server notices when a client of another build calls, or within about
  10 seconds anyway. It refuses that one call (the client retries), stops
  accepting, waits up to 5 seconds for connections to go idle and up to 10
  more for calls still running, closes the store and execs the new build.
  Clients keep retrying for 30 seconds meanwhile, longer than that drain and
  the new build's start (and its migration, if the schema moved), so a
  command issued during a replace is answered by the new build rather than
  failed; a client that finds no answer but the server's pid still live in
  `server.pid` waits as long rather than starting a second server. Event and
  wake streams are
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
a service. The web view does not replace itself: it is a client of the
server and keeps running the build it was started from until it is
restarted. `yardr upgrade` restarts the service on the build it installs;
a `go install` by hand leaves it until the service is restarted.

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
