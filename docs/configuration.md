# Configuration: the repo and the store

yardr reads its configuration from two places:

- **the rig's repository**, under `.yardr/`: what the work is. Versioned
  with the code it describes and changed through review.
- **the store** (`yard.db`, owned by the yardr server): who works it, where
  and how. Live state, changed with `yardr` commands, no commit needed.

The split was decided in yard-17 (2026-10-02). Both sides are built: flows
in yard-9cza, the merge gate in yard-p2y7.

## What lives where

| | repo `.yardr/` (what the work is) | store (who / where / how) |
|---|---|---|
| flows | the rig's default and type flows, named flows: `.yardr/flows/` (built) | named flows, and rig and type pointers to them (built) |
| stage files | a repository flow's: `.yardr/flows/<name>/<stage>.md`, read from the base (built) | none in the store: a store flow's are files in the yard's home, `~/.yardr/flows/<name>/<stage>.md` (built; [flows.md](flows.md#stage-files-what-a-stage-is-for)) |
| merge gate | `.yardr/check` (built) | a per-rig override, `rig add --check` (built) |
| roles | the rig's own role prompts: `.yardr/roles/<name>.md`, read from the base (built) | the generic roles `yardr init` installed in `~/.yardr/roles/`, the fallback; residents' prompts, by absolute path (built) |
| groups | | runner, limit, members, subscriptions (manual), agent kind, model, args, cmd, worktree settings (built) |
| routes | | which group works which stage, by rig, type, label (built) |
| rigs | | path, kind (`git` or `dir`), clone, base branch; `rig add` registers, `rig rm` removes (built) |
| residents | | agent, directory, inbox kinds, rotation, subscriptions, identities (built) |
| settings | | the yard's defaults: `default_kind`, `operator`, `mayor`, `name`, `hook_addr`, and how agents work with the operator: `operator_style`, `operator_note`, and the bounds of its timers: `timer_limit`, `timer_min_every` (built) |

### Why the repo holds what the work is

A flow, a check and a role prompt describe the codebase: which stages a change
passes, what "green" means for this code, how to work in it. They change
when the code changes, so they belong next to it:

- **Versioned.** A flow or a gate is tied to the code it applies to. Check
  out an old commit and you get the gate that applied then.
- **Reviewed.** Changing `.yardr/` is a code change like any other; it goes
  through the rig's review and lands on the base branch. Nobody lowers a
  bar without someone seeing it.
- **Portable.** Anyone who runs yardr on the repo gets the same lifecycle
  and the same gate, without copying store state.

### Why the store holds who, where and how

Groups, runners, models, agent arguments, members, routes and residents
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

A role prompt says how a rig is worked (its language, its tools, its gate,
who decides), so a rig's own roles live in its repo, in `.yardr/roles/` next
to `.yardr/flows/` and `.yardr/check`, and a group names them by role:
`--set prompt=roles/builder.md`. The roles built into yardr (the repo's
`roles/`, which `yardr init` installs in `~/.yardr/roles/`) are generic
starting points that name no person, rig or language; a rig without its own
works from them.

### Role prompts

A group's `prompt` is read when a session starts:

- an absolute path is read as is;
- a role, `roles/<name>.md` or bare `<name>.md`, is looked up in order:
  1. the rig's `.yardr/roles/<name>.md`;
  2. the rig's `roles/<name>.md`, the older place: still read, with a
     warning (once per file) in the daemon's log to move it;
  3. the yard's home, `~/.yardr/roles/<name>.md` (`--home`, `YARDR_HOME`).
- any other relative path is looked up in the rig's working tree, then in
  the home.

A rig's roles are read like its gate: from the tip of the rig's base branch
(`git show <base>:.yardr/roles/<name>.md`) and from nowhere else in the rig.
Not from the bead's branch or worktree, so a bead cannot rewrite the prompt
of the reviewer that judges it, and not from the checkout's working tree or
whatever branch it has out either: a role the base does not have is not the
rig's, and the home's copy serves until the edit has landed. Only a rig that
is not a git checkout, or whose base does not resolve, is read from its
working tree.

So a fresh rig works from the generic roles in `~/.yardr/roles/` until it
commits its own, and committing one overrides the home copy for that rig
only. A prompt found nowhere fails the session start with every path tried in
the error (the rig's as `<base>:.yardr/roles/<name>.md`); three such failures
hold the bead.

Residents have no rig: give a resident's `prompt` as an absolute path (a
relative one is read from its directory), e.g. a rig's
`/path/to/repo/.yardr/roles/mayor.md`.

## Session limits and outages

A stuck bead session should surface, not run forever. Each group can set,
with `yardr group add <name> ... --set key=value`:

| key | default | meaning |
|---|---|---|
| `idle` | `30m` | the agent sat idle (herdr status `idle` or `done`) this long: a `session_idle` event, once per idle spell |
| `max_age` | `4h` | the session ran this long: a `session_aged` event, once per session |
| `stall` | `2m` | the agent sat idle this long after its first prompt and did nothing (no note, no commit, no change in its worktree): a `session_stalled` event with the end of its pane, once per session, and the group starts no further session with that model meanwhile ([groups.md](groups.md#models)) |
| `grace` | unset | when set, a session still idle (or still running) this long past its limit is stopped and counted as a failed session; `0` stops it at once. Unset, the defaults below apply |

Durations are Go durations (`45m`, `2h`) or a number of seconds; `off` or
`0` turns `idle`, `max_age` or `stall` off. The defaults apply to runners that report
an agent's status (herdr). For `exec` and `manual` groups only what the group
sets applies, and `idle` never does, since they cannot tell idle from busy.
The idle clock is the daemon's: one that replaces itself starts it again,
while the age is counted from the session's start and reported only once.

**Defaults without a grace.** An idle session is only reported: an agent
waiting on a human (a permission prompt, a question) is idle too, and
stopping it would lose the answer. An aged session is reported at `max_age`
and stopped anyway once it has run twice `max_age` (8h by default): nothing a
bead needs takes that long, so a session still running is stuck. The stop
counts as a failed session, so a bead whose agents keep running into the cap
is held after three like any other repeat failure, and the bead is released
with a note naming the session, the cap and the fix (raise the group's
`max_age`), so the next agent and the mayor see why. The `session_aged`
event's `stop_after` says how long after the event the session is stopped,
with or without a grace.

These events, `runtime_down` (below) and `held` (a stuck agent holds its
bead for the mayor) are in a new resident's default `--on`, so the mayor's
inbox gets them; a resident made before one of them joined the default needs
it added to its `--on`, which `yardr resident add` replaces as a whole
([residents.md](residents.md)). `runtime_down` is about no bead, so a `yardr`
subscription does not deliver it.

**Outages are not failures.** When a runner cannot start anything at all
(herdr missing from `PATH`, herdr down, `sh` not found), the session is
ended as `aborted`, which does not count toward the 3 failures that hold a
bead, and the bead waits where it is. The group gets one `runtime_down`
event per outage and is not tried again for 30s, doubling up to 5m while it
stays down; the first start that works logs `runtime_up`. Errors that are
the bead's own (no rig, a bad brief, a worktree that cannot be made) still
count. herdr's own answer that its server is not running or not ready
(`server_not_running`, `server_unavailable`) is an outage too, not a bead
failure. The fetch of a rig's base before a bead branches from it gives up
after 60s and never waits on a credential prompt (`GIT_TERMINAL_PROMPT=0`),
so a dead remote cannot stall the daemon; every other git call a start or a
cleanup makes (`worktree add`, `branch`, `worktree remove`) is cut off after
2 minutes on the same terms.

## Rig kinds: git and dir

A rig is one of two kinds, shown by `yardr rig show` and `rig list`:

- **git** (the default, and every rig registered before kinds existed): a
  repository. Beads work on branches in worktrees, start from the rig's
  base and land on it. `.yardr/` is read from the tip of the base, as the
  rest of this page describes.
- **dir**: a plain directory, a folder of documents, images or invoices.
  There is no repository, no branch and no base: beads are worked in the
  directory itself. `.yardr/` is read from the directory as it is on disk,
  so an edit to a flow, a role or the check applies at once (flows are
  reread when a file under `.yardr/flows/` is added, removed or changes
  mtime or size). Nothing reviews `.yardr/` there before it applies; the
  bar is the operator's, not a landed commit's.

`yardr rig add <name> <path>` detects the kind: if `git -C <path> rev-parse
--git-dir` succeeds the path is a git rig, otherwise a dir rig, and it
prints which it took. `--kind git|dir` says so instead; `--kind dir`
registers even a checkout as a plain directory. A dir rig refuses `--base`
and `--clone`.

| `.yardr/` | git rig | dir rig |
|---|---|---|
| `flows/` | at the tip of the base (`repo@<sha>`) | in the directory (`repo (directory)`) |
| `flows/<name>/<stage>.md` | at the tip of the base, with the flow | in the directory |
| `roles/<name>.md` | at the tip of the base, else the working tree | in the directory |
| `check` | at the tip of the (fetched) base, written out and run | in the directory, run in place |
| no `check` | fails ("no gate") unless `--allow-no-check` | passes ("no gate"): nothing merges |

Epics on a dir rig only group their children: no epic branch, and a
child's base (`bead show`, `$YARDR_BASE`) is empty, as is every bead's in
a dir rig.

## Precedence

Where both sides can define something, the most specific source wins:

```
pin on the bead  >  store  >  repo .yardr/  >  none
```

- **Pin on the bead**: the bead names its flow itself (`meta flow=<name>`),
  for the odd bead that needs a different lifecycle.
- **Store**: the operator's override for this installation, e.g. a rig or
  type pointing at a named flow (`yardr rig flow`), or `rig add --check`
  (built).
- **Repo `.yardr/`** (built): the default that ships with the code.
- **None**: no flow means a bead may move to any stage. No check fails the
  gate ("no gate") unless the rig was added with `--allow-no-check`, or is
  a dir rig.

Within one source a type-specific flow wins over the rig's flow; a source
wins as a whole, so a rig pointer in the store overrides the repo's type
files too (see [flows.md](flows.md)). `yardr bead show` and `prime` say
which flow governs a bead and where it came from: `pin`, `store`,
`repo@<sha>`, and the type when a type's flow matched. For the check the
rule is: the rig's `--check`, else `.yardr/check` on the base, else no
gate, which fails unless the rig allows it.

### Where a rig's flows, stage files and roles come from

The store decides per object: the rig's flow pointer, and the `prompt` of
each group and resident. `yardr rig sources <rig>` shows all of it in one
place, one line per object: the kind, the name, the source that governs,
and what it shadows or what else is available.

```
$ yardr rig sources yardr
KIND   NAME            SOURCE
flow   default         pack starter (store starter.default)                     shadows .yardr/flows/default.flow
stage  new             pack starter (~/.yardr/flows/starter.default/new.md)     shadows .yardr/flows/default/new.md
stage  review          pack starter (~/.yardr/flows/starter.default/review.md)  shadows .yardr/flows/default/review.md
stage  decide          pack starter (~/.yardr/flows/starter.default/decide.md)
role   yardr-builders  rig (.yardr/roles/builder.md@add63e5)                    pack starter has a copy
role   yardr-reviewers pack starter (~/.yardr/packs/starter/roles/reviewer.md)  shadows .yardr/roles/reviewer.md
role   yardmaster      home (~/.yardr/roles/mayor.md)
```

- **flow**: each flow governing the rig, as `yardr flow show <rig>` resolves
  them: the rig's own (`default`), then one per type (`type/<type>`). With
  type flows, a stage is named `<type>:<stage>` (`default:<stage>` for the
  rig's own).
- **stage**: the stage file of each non-terminal stage of those flows.
- **role**: the prompt of each agent group a stage or route of the rig leads
  to, where a brief for a bead of the rig reads it, and of each resident a
  pack imported into the rig names. Script and people groups have no role.

| source | it is |
|---|---|
| `rig (<path>@<sha>)` | a file of the rig's repository, at that commit of the base (no commit: a dir rig's directory, or a working tree) |
| `pack <name>` | the pack's flow in the store, a stage file of that flow in the home, or the pack's installed role |
| `store` | a named flow in the store that no pack imported into the rig brought |
| `home (<path>)` | a file under the yard's home: a role there, or a stage file of a store flow |
| `file (<path>)` | any other path |
| `none` | no flow governs, the stage has no file, the group no prompt; with `: <why>` after it, a prompt that names a file nobody has |

`shadows <file>` names a file of the rig's repository that nothing reads
while the line is as it is: the flow file behind a store pointer (and,
behind the rig's pointer, every type's file, since the store wins as a
whole), the stage file of a flow that does not govern, the rig's own role
behind a group prompted from a pack or the home. Stage files of a shadowed
flow that match no stage of the governing one are listed on the flow's
line. `yardr rig check` names every shadowed file in a note
(`note: .yardr/roles/reviewer.md is shadowed by …`), which never fails the
check. `pack <name> has a copy` and `pack <name> has flow <flow>` say what
the rig could take instead.

`--json` prints the lines as data: `kind`, `name`, `flow`, `type`,
`source`, `pack`, `path`, `commit`, `shadows`, `available`, `error`. The
view changes nothing.

#### Switching one of them

`yardr rig source` switches one line between the rig's repository and the
pack imported into the rig, and prints the line as it is afterwards:

```
$ yardr rig source yardr role yardr-reviewers rig
KIND  NAME             SOURCE
role  yardr-reviewers  rig (.yardr/roles/reviewer.md@add63e5)  pack starter has a copy
note: the import of pack starter chose the pack's roles: the next yardr pack apply sets its role on yardr-reviewers again unless given --rig-roles

$ yardr rig source yardr flow rig
KIND   NAME    SOURCE
flow   default rig (.yardr/flows/default.flow@add63e5)        pack starter has flow starter.default
stage  new     rig (.yardr/flows/default/new.md@add63e5)
stage  review  rig (.yardr/flows/default/review.md@add63e5)
the flow switched whole: its stage files are the ones above now
```

| command | `rig` | `pack` |
|---|---|---|
| `yardr rig source <rig> role <group> rig\|pack` | the group's `prompt` becomes `roles/<name>.md`, which resolves to the rig's `.yardr/roles/<name>.md` on the base | it becomes the pack's installed copy, `<home>/packs/<pack>/roles/<name>.md` |
| `yardr rig source <rig> role <resident> rig\|pack` | the resident's `prompt` becomes the absolute path of `.yardr/roles/<name>.md` in the rig's checkout | as for a group |
| `yardr rig source <rig> flow [--type <type>] rig\|pack` | the pointer of the rig, or of the type, is cleared: `.yardr/flows/default.flow`, or `type/<type>.flow`, governs | the pointer is set to the flow the pack's import pointed it at |

- The role is the one the prompt names now, by its file name (`builder.md`
  of `roles/builder.md` or of `~/.yardr/packs/starter/roles/builder.md`). A
  prompt that names no role is refused: set it yourself (`yardr group
  add`, `yardr resident add`).
- A resident is the yard's and reads its prompt as a path, never by a
  lookup in a rig. Its rig's role is therefore the file in the rig's
  checkout as it is on disk, not at the base commit, and the line has no
  commit. A running resident keeps its session; the output names the
  restart.
- A flow switches whole: the stage files come from where the flow does, the
  rig's from beside its flow file and the pack's from the yard's home. No
  single stage file can be switched.
- Refused, changing nothing, when the chosen source does not exist: the rig
  has no such role or flow file on its base (in its directory, for a dir
  rig), no pack is imported into the rig, or none has the role or a flow
  for the rig or the type. A type's flow cannot be the rig's file while the
  rig itself is pointed at a flow, since that pointer stands in front of
  every flow file: switch the rig's flow first.
- A flow change is refused as `yardr rig flow` is, naming the beads, while
  an open bead sits in a stage the new flow lacks
  ([flows.md](flows.md#named-flows-and-pointers)).
- Switching to where the object already comes from says `unchanged`.
  `--json` prints `{"rig", "changed", "lines", "notes"}`, the lines as
  `rig sources --json` has them.
- The pack's record still holds the import's choice of roles
  ([packs.md](packs.md#how-a-pack-maps-to-the-yard)), so the next `yardr
  pack apply` sets every role by it again. The output has a note when that
  undoes the switch; give that import `--rig-roles` or `--pack-roles` to
  make the choice the record's. An import also points the rig at the pack's
  flow again.

`yardr rig flow` points a rig at any named flow, not only a pack's, and
`yardr pack apply --rig-roles` or `--pack-roles` chooses every role at once.

## Repo config comes from the base branch

This section is about git rigs; a dir rig has no base and reads `.yardr/`
from its directory (see [Rig kinds](#rig-kinds-git-and-dir)).

yardr reads `.yardr/` from the tip of the rig's base branch
(`git show <base>:.yardr/...`), never from the bead's branch or worktree
(built). A bead therefore cannot lower its own bar: a builder who edits
`.yardr/check` or a flow on `yard/<bead>` changes nothing for that bead. The edit takes effect only once it has been reviewed
and landed on the base, like any other code, and then applies to every bead
after it. yardr resolves the base on every check and re-reads the files
when its commit moves.

For the gate, "the base" is the remote's copy when there is one. Refs are
shared by every worktree of a rig, so a bead could `git update-ref` the
local `main` to a commit whose `.yardr/check` passes. When the base has a
remote (`--base origin/main`, or a local branch whose upstream, or else
`origin`, has it), `yardr rig check` fetches that branch into its
remote-tracking ref first (forced, bounded to 60s, never prompting) and
reads `.yardr/check` from `origin/main`, not from the local branch. A fetch
that fails leaves `origin/main` as last fetched; only a rig with no remote,
or whose remote lacks the branch, reads the local base. The result says
which was used (`--json`: `from`, e.g. `origin/main (fetched)`, `main (no
remote)`). A gate change therefore applies once it is on the remote: with
merge-local that is when `YARDR_PUSH` has pushed it.

### The merge gate: `.yardr/check`

`.yardr/check` is a script; exit 0 means green. yardr writes the base's copy
to a temporary file under the rig's git directory and runs it in the
directory being checked (a script without `#!` runs with `sh`; the rig's
name is in `$YARDR_RIG`). A store override, `yardr rig add <rig> <path>
--check '<cmd>'`, runs with `sh -c` instead. Re-running `rig add` without
`--check` drops the override. Every `rig add` logs a `rig_updated` event
with the rig's check and `allow_no_check`, so an override never changes
unseen, and `yardr rig show <rig>` prints it.

A rig with neither fails its gate with "no gate": a missing file must not
read as a passing one. `rig add ... --allow-no-check` opts a rig out, for a
repo that has nothing to check.

A dir rig's `.yardr/check` is read from its directory and run where it is
(as is when executable with `#!`, otherwise with `sh`), in `--dir`, which
defaults to the rig's directory. A dir rig without one passes with "no
gate": nothing of it merges anywhere, so there is no bar to lower.

```
yardr rig check yardr                                     # in the rig
yardr rig check yardr --dir ~/.yardr/worktrees/yardr/yardr-k3f9   # in a bead's worktree
```

prints the tail of the output (`--tail N`, default 40; a red gate whose
tail is empty says so, or how many lines it hid) and pass or FAIL; `--json`
gives the full output, the exit code and the source (`store`, `repo@<sha>`,
`dir` for a dir rig's own file, or empty for a rig with no gate). It exits 0 on a green gate, 1 on a red one
(no gate and a timeout included), and 2 when the gate could not run at all:
an unknown rig, a base that does not resolve, git failing. A check ended by
a signal from outside (its process killed, or its own exit status 128+N, as
a shell reports a killed command) exits 128+N, which the refinery runs once
more instead of sending the bead back. It also fails,
naming them, while open beads of the rig sit in a stage their flow lacks
(with a green gate that is exit 2, since no branch can fix it; see
[flows.md](flows.md#a-base-change-that-drops-a-stage)). The check is
killed, with every process it started, after `--timeout` (default 30m), and
each git call that finds it is bounded (30s; the fetch, 60s).
Builders run it before handing on, and the refinery runs it too:
[`contrib/refinery/merge-local`](../contrib/refinery/merge-local) runs
`yardr rig check` on exactly what will land, after any rebase, and sends a
red gate back to `new` (see [pipeline.md](pipeline.md#4-approved-the-refinery-lands-it-on-main)).

yardr's own CI (`.github/workflows/ci.yml`) runs the same script, on pushes
to main and on pull requests. CI runs on ubuntu only; macOS is covered by
the refinery's gate on the yard's own Mac at every landing (decided 3 Oct,
see yardr-msjd).

The gate runs the branch's own code, tests included: it is fixed by the
base, but what it runs is not. A branch that deletes or weakens its tests
passes vacuously. That is the accepted model for now; the reviewer is the
one who catches it (see [pipeline.md](pipeline.md#what-the-gate-does-not-catch)).

## Briefs are not config

The herdr runner writes each agent's brief outside the tracked tree, under
the worktree's git directory (`git rev-parse --git-path yardr`, i.e.
`<repo>/.git/worktrees/<bead>/yardr/brief-<session>.md`), and points the
agent at it by absolute path. Nothing generated per session sits next to
committed files, so `.yardr/` holds only config. (Worktrees from before
this kept a `.yard/` that ignores itself; it is harmless.)

A dir rig has no git directory to hide a brief in, so its briefs go to
`<dir>/.yardr/briefs/brief-<session>.md`, a folder that ignores itself
(for a dir rig inside a checkout); each is removed once its bead closes.

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
| `webviews/` | a record per running web view, held by it while it serves, which `yardr status` and `yardr doctor` read ([web.md](web.md#where-it-runs)) |
| `worktrees/<rig>/<bead>` | herdr bead worktrees |
| `rigs/<name>` | rigs registered with `rig add --clone`; `rig rm` leaves the clone here |
| `roles/` | the generic role prompts `yardr init` installed (mayor, builder, merger, reviewer), the fallback for rigs without their own; yours to edit. `.shipped.json` there records each file's hash as init wrote it, so init again can tell an edited prompt (kept) from an untouched one (refreshed) |
| `linear.secret`, `github.secret`, `residents/<name>.env`, `groups/<group>.env` | credential files, if you keep them here as the examples do ([residents.md](residents.md#outbound-credentials)): yours, yardr creates none of them and reads only the paths a hook, resident or group names |

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
`YARDR_HOOK_PAYLOAD_RETENTION`, under the old `YARD_` names too) and
`YARDR_HOME` set to the home it serves, and runs in that home. So
`YARDR_HOOK_ADDR` reaches the server only from the environment of the
command that happened to start it; the `hook_addr` setting is the yard's
own and needs no such luck. The daemon
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

`yardr rig rm <name>` takes a rig out of the store with the routes that
match it (`--rig <name>`) and its flow pointers, logging one `rig_removed`
event. It is refused, naming them, while the rig has open beads or live
sessions; closed beads keep the rig's name as history. Nothing on disk
changes: the clone, its worktrees under `worktrees/<rig>/` and its `.yardr/`
stay where they are for you to remove.

### Creating a yard: the onboarding and `yardr init`

A home is set up once its store has the `default_kind` setting, which init
writes last, or once it has any rig, group, resident or bead: a yard from
before the onboarding is in use, and onboarding it would refuse its agents'
commands. Opening such a store writes `default_kind` for it, from the first
herdr group's kind, else the first herdr resident's, else `claude`, with the
event `settings_migrated` (the `operator` and `mayor` settings stay unset; installed role
prompts keep the name they have). Opening a store whose `operator` setting
names nobody's user adds that user ([users.md](users.md)), unless a
resident has the name. Only an empty store onboards. The store
file alone is no marker: the server creates `yard.db` on the first command
that connects, whatever it was, so a home can hold an empty store nobody set
up. Any yardr command on a home that is not set
up, bare `yardr` included (help otherwise; `--help` is always help), runs the
onboarding when it has a terminal: the default harness (every kind herdr starts, marked
installed when its executable is on PATH, the first installed one
preselected), the mayor's name (default `mayor`), the directory its shell
lives in (default the home's parent, so `~` for `~/.yardr`), and an optional
model, and your name for the role prompts, then "How should agents work
with you?" and "Anything agents should know about you?"
([below](#how-agents-work-with-you)), then "Create this yard?" and
"Start the yard now?" ([below](#starting-the-yard)). Before the first page,
when `git` or `herdr` is not on PATH, it names each and what it is needed for
(herdr starts every agent, the mayor first; a rig is a git repository) and
asks whether to go on: the answer starts at stop when herdr is missing, and
stopping creates nothing. Without a terminal it prints how
to initialize and exits 2.
`yardr server`, `yardr init`, `yardr daemon`, `yardr settings` and
`yardr help` never ask.

`yardr init [--harness <kind>] [--mayor <name>] [--dir <path>] [--model <m>]
[--operator <name>] [--style <choice>] [--about <text>]
[--start [--wait <duration>]]` does the same without the
form, for scripts and tests; `--harness` is required to create a yard, and
nothing is started unless `--start` says so. Either way yardr:

1. writes the default role prompts, built into the binary from this repo's
   `roles/`, to `<home>/roles/`. A file already there is kept when it was
   edited (the mayor edits its own copy) and replaced with this build's when
   it still reads as init wrote it: `<home>/roles/.shipped.json` holds the
   hash of each file as written, so init again after an upgrade refreshes
   the untouched templates and the summary says which were refreshed and
   which kept (`--json`: `written`, `refreshed`, `current`, `kept`). A file
   from before the manifest counts as edited unless it already matches this
   build's, which is when it starts being tracked; delete one to get this
   build's copy. They are generic: where they say `{{operator}}`, the
   person agents bring decisions to, init writes the operator's name (the
   form's "Your name", `--operator`, default the login name, or `operator`
   where there is none), and where they say
   `{{mayor}}`, the resident builders and reviewers mail about what blocks
   them, the mayor's name. A rig's own roles go in its repo under
   `.yardr/roles/` ([role prompts](#role-prompts));
2. sets `operator` to the operator's name and `mayor` to the mayor's, and
   `operator_style` and `operator_note` when they were said (the form's
   answers, `--style`, `--about`);
3. adds the mayor resident: `kind=<harness>`, `prompt=<home>/roles/mayor.md`,
   the model if given, the harness's unattended flags as `args`, rotating
   every 6h. A resident of that name already there is kept as it is, so
   init runs again on a yard without harm;
4. sets `default_kind` to the harness, which marks the yard set up.

Init again on a yard that is set up changes only what its flags say: the
`default_kind`, `operator` and `mayor` settings stand in for `--harness`,
`--operator` and `--mayor` when those are not given, rather than being
reset to the flags' defaults (the login name, `mayor`); `operator_style` and
`operator_note` are kept the same way, changed only by `--style` and
`--about`. The summary
lists what was kept (`kept default_kind claude, operator Jo, mayor mayor, as
set before`). A resident of the mayor's name is kept as it is, and so are
the role files already in `<home>/roles/`. What a run that failed leaves,
and how the next one finishes it, is [below](#a-run-that-did-not-finish).

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

and `yardr prime --resident <name>` after the role, under `## The operator:
Jo` (`--json`: `operator`, with `name`, `style`, `label`, `tells`, `note`).
So `yardr settings set operator_style report` reaches every session that
starts after it, with no init and no edit of a role a rig committed; a
session that is running keeps what it read. The instructions are about what
an agent writes to the operator and brings to them, not about how it does
its own work in a pipeline.

A yard without either setting, which is every yard from before them, primes
as it did: no section, and no style assumed. With only the note set the
section has the note and no style. `yardr setup` uses the style in one way:
with `guide` its pages print no command lines ([setup.md](setup.md#the-pages)).

The summary ends with the way to the mayor ([starting the
yard](#starting-the-yard)), whose role begins with
a first-run checklist. The mayor reads `yardr doctor` ([doctor.md](doctor.md)),
whose setup steps say what is done, and goes on from the first one that is
not, with the operator, by mail in `yardr chat` when the onboarding took them
there ([chat.md](chat.md#from-the-onboarding)) and in its pane otherwise: the repository and its base branch
(`yardr rig add`), `yardr rig init` ([below](#seeding-a-rig-yardr-rig-init)),
the builder and reviewer groups on the default harness, how approved beads
land (a refinery or a manual group), routes for `new`, `review` and
`approved`, and a first bead to try the pipeline. `yardr init --help` lists
the same as commands, for scripts.

The unattended flags keep an agent nobody watches from stopping at a
permission prompt: claude `--permission-mode auto`, codex
`--dangerously-bypass-approvals-and-sandbox`, kimi `--yolo`; any other kind
gets none and may ask (`yardr group add --help` lists them too).

#### Starting the yard

The daemon starts the mayor. There are four ways to a running daemon:

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
running daemon is left alone. Unlike the server, this daemon keeps the
environment of the command that started it (herdr's socket, the PATH an exec
group's command runs with, as a daemon in a pane has its pane's), without
what names the starter (`YARDR_ACTOR`, `_BEAD`, `_SESSION`, `_STAGE`,
`_GROUP`, `_RESIDENT`) and with `YARDR_HOME` set to the home.

`yardr daemon stop` ends the daemon of the home, however it was started: it
signals the pid in `<home>/daemon.lock` and waits (`--wait`, default 30s)
until the lock is free, since the daemon finishes the pass it is in. The
sessions it started carry on in herdr; nothing is dispatched until a daemon
runs again. `yardr daemon status` says whether one runs, with its pid, and
whether the login service is installed and loaded.

Before they start the daemon, in a terminal, they ask "Check that <harness>
answers?", with the mayor's harness: "Check" (the answer it starts at) or
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
"not tried"), for a harness that is not on PATH, for a mayor that runs
already or is stopped, and when a daemon runs already, since that one starts
the mayor whatever would be answered. `yardr setup` does not ask: it starts
no mayor.

They then wait for the mayor, up to `--wait` (default 2m; the form always
waits that long), with a line that counts on a terminal and one line on
stderr otherwise. A session that exists is not enough: a harness can be on
PATH and sit dead in a running session (a binary behind a system dialog
nobody sees, a CLI at "Not logged in"), and herdr reads any process of the
harness's name as an idle agent. The mayor counts as up once its first
prompt was delivered and it answered: herdr reports the agent working, or
the pane shows output below the prompt.

When the mayor is up, yardr prints where it is, as `herdr agent list` and
`yardr resident show <mayor> --json` name it: the agent (`<yard>-<mayor>`),
the workspace's label (`<yard> <mayor>`) and id, the tab and the pane; and
how to get there: `herdr agent focus <agent>` brings its pane to the front
in herdr, `herdr agent attach <agent>` opens it from any other terminal.
The form then asks how to go on with the rest of the setup: "Talk to the
mayor" or "Keep going here" ([setup.md](setup.md)). The first opens
`yardr chat` on the mayor in the same terminal
([chat.md](chat.md#from-the-onboarding)); the second is `yardr setup`, in the
same run. `yardr init --start` in a terminal does not ask that and opens the chat.

The mayor `yardr init` adds has `greet=<operator>` in its config. The first
prompt of a resident with that key, in its first session only, asks it to
greet that user by mail (`yardr mail <operator> "…"`) and to say what it
needs for the next setup step, before it points at the prime as every
resident's first prompt does. The chat shows mail and nothing of a pane, so
this is how the mayor's first word gets there. A later session (a rotation,
a restart) is asked for no greeting.

No chat opens without a terminal, with `--json`, in an agent's own session
(`YARDR_SESSION` or `YARDR_RESIDENT` is set) and with `--no-chat`: yardr then
ends where the mayor's place is printed, and the lines after it name
`yardr chat`. Nor does one open when who runs the command is no user of the
yard (the operator was given another name than the login name): the mayor's
mail goes to the operator, so yardr says who the chat is for and how to open
it as them (`--actor <operator>`), and the form's "Talk to the mayor" then
brings the mayor's pane to the front as `--no-chat` does: herdr focuses a
pane for any process that asks over its socket, and in a terminal outside
herdr yardr adds that herdr has to be opened to see it.

When the mayor is not up, yardr says why the wait ended (the time is up; the
daemon gave up after five failed starts; a start failed with herdr or the
harness not on PATH, which no waiting brings; the daemon ended; the mayor is
disabled), what is known (which program is missing, the reason of the last
failed start, the last lines of the mayor's pane, where the daemon logs) and
how to go on. `yardr init --start` then exits 1. The form opens no chat,
since nobody is there to answer in it: it prints what
[`yardr doctor`](doctor.md) says of the yard (its `FAULT` lines and the
mayor's own line, and for a session that runs without an answer the note
`<mayor>'s session runs, and it has not answered its first prompt`), and
goes on to the command it was started for. `--json` carries the same under
`start`: `daemon_started`, `daemon_log`, `running`, `mayor` (`agent`,
`workspace`, `workspace_id`, `tab`, `pane`), `pane`, `chat` (the command for
the conversation with the mayor, once it is up), `problem`, `known`, `next`.

A mayor whose session runs and that has not answered its first prompt may
have a harness that waits at something yardr does not answer: a login, a
dialog, a question. Where a chat would open (a terminal, no `--json`, no
`--no-chat`, not an agent's session), yardr then says why the wait ended,
shows the last lines of the mayor's pane and asks "Open the mayor's pane" or
"Not now", before anything else of the above. "Open the mayor's pane" runs
`herdr agent attach <agent>` in the same terminal, after a line that says
how to come back: `ctrl+b q`, herdr's keys for leaving a pane it attached,
which stop nothing. Back from the pane, yardr waits for the mayor again, for
as long as the start did. A mayor that is up now is one the start found up:
the chat opens, or the form goes on. One that is still not up is said so,
with its pane as it is now, and the question is asked again. "Not now" (or
ctrl+c) ends as a start that asks nothing does, with what is known and how
to go on. In a pane of herdr there is nothing to attach: herdr shows the
mayor's pane in place of yardr's, yardr says how to come back to its own
(`ctrl+b g`, herdr's list of panes) and waits there meanwhile. "Talk to the
mayor" in [`yardr setup`](setup.md) asks the same of a mayor it finds that
way. yardr types nothing into the pane: it answers Claude Code's question
about trusting the folder when the session starts
([groups.md](groups.md)), and nothing else a harness asks. A mayor without a
session (a start that failed, a program that is missing) has no pane, and
nothing is asked.

Once the mayor is up, the form asks two more questions before the rest of
the setup, each starting at **no**: "Start the daemon at login?", which runs
`yardr daemon install`, and "Start the web view now?", followed on a yes by
"And at login?": yes runs `yardr web install`, no `yardr web start`. Each
command is shown before it runs and prints what it did; the web view's
prints its address. One that fails says so and the form goes on: the yard
runs without either. On a system yardr writes no service for, only the web
view is offered, for now. A question is left out when the yard has what it
asks for: the daemon's service is installed, the web view's is, or something
answers on the view's address. `yardr init --start` asks nothing; the
commands are the same.

#### Login services

`yardr daemon install` makes the daemon a service of your login session, and
`yardr web install` the web view ([web.md](web.md#keeping-it-running)):

| system | what is written | where |
|---|---|---|
| macOS | a launchd agent, `dev.yardr.daemon.<home>.plist` (`dev.yardr.web.…`) | `~/Library/LaunchAgents/` |
| Linux | a systemd user unit, `yardr-daemon-<home>.service` (`yardr-web-…`) | `$XDG_CONFIG_HOME/systemd/user/`, default `~/.config/systemd/user/` |
| any other | nothing: the command says it is not supported and how to run the daemon by hand (`yardr daemon` in a terminal, `yardr init --start`) | |

`<home>` is the home's directory name and the first 8 hex digits of the
sha256 of its path (`yardr-3f9a1c0e`, say, for `~/.yardr`), so every yard
has services of its own and two yards never share one.

The file holds:

- the yardr that ran the install, by its path, with `daemon` (or `web serve
  --addr <addr>`). A yardr that moves needs the install again;
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
  for the service (a group's `env_file`, a credential helper);
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
finds the mayor and the sessions it had without their panes and starts them
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
and comes under none, so work in a rig under one of those folders can fail
with `operation not permitted`. `yardr daemon install` names the rigs that
are there, and [`yardr doctor`](doctor.md#optional) does while the service
is installed. Keep rigs elsewhere, or grant yardr access: Full Disk Access
(add the yardr binary the service names), or Files and Folders, once macOS
has asked for the folder. What an upgrade does to that grant is
[below](#upgrading). This is as reported from one Mac (Linear YRD-3):
yardr's tests load nothing into launchd, so it is not reproduced by them.

#### A run that did not finish

The onboarding and `yardr init` do their steps in one order: the roles, the
`operator` and `mayor` settings, the mayor resident, `default_kind`, and then,
when asked to, the daemon and the wait for the mayor. A run that fails leaves
what the steps before the failure made. The next run finishes from that: it
does not start again from nothing, and it says what it found.

| the run stopped | what the home holds | the next run |
|---|---|---|
| among the roles | some of the prompts, each whole and recorded in `.shipped.json` as written | `yardr init --harness <kind>` again, or `yardr` in a terminal for the form. Prompts the failed run wrote are refreshed to the names this run is given; none counts as edited by hand |
| after the roles | the four prompts | the same |
| after `operator`, or after `mayor` | the prompts and the settings stored so far | the same. The stored settings stand in for `--operator` and `--mayor` when they are not given, and are what the form's "Mayor" and "Your name" start at |
| after the mayor was added | all but `default_kind`. With a resident the home counts as a yard, so no form runs | `yardr init`, with no flag: the harness is the mayor's. Or `yardr` in a terminal, below |
| the daemon could not be started, or the mayor did not come up | a yard that is set up and does not run | `yardr init --start`, which starts the daemon unless one runs and waits again. Or `yardr` in a terminal, below |

Init then prints `yard in <home> finished: part of it was there`, with a
`found` line (the prompts, settings and resident that were there) and an
`added` line (what this run added); `--json` has the same as `found` and
`added`, which are absent on a new home and on a yard that was set up. A
setting stored with `yardr settings set` before any init is found the same
way. Each role prompt is written to a temporary file and renamed, so a run
that is killed leaves the prompt that was there or none, never half of one;
`.shipped.json` is saved after every prompt. One case is left: a run killed
between a prompt and the manifest's save leaves one prompt unrecorded, and a
next run given another operator or mayor keeps that file as edited. Delete it
to get this build's copy.

**Bare `yardr` in a terminal** takes the onboarding up where a yard has no
rig yet and any of this holds: `default_kind` is not set, the mayor resident
is missing, no daemon runs, or the mayor is not up (its session is not
running, or runs and has not answered its first prompt). It prints what is
there, in the lines [`yardr doctor`](doctor.md#the-yard) prints for the yard,
and asks "Finish the setup now?". Yes runs `yardr init --start` (which adds
only what is missing, starts the daemon unless one runs, and waits for the
mayor) and then goes on as the form does: the two services, and "Talk to the
mayor" or "Keep going here". The summary of the yard is left out when
nothing had to be added. No, or ctrl+c, changes nothing and prints the
command. It goes by the yard as it is, not by a record of the earlier run,
so a yard made with `yardr init` and never started is taken up the same way.

On any other yard bare `yardr` is help, as before (with a last line, `Mail for <user>: N unread from <names>`, when the user who runs it has unread mail, [users.md](users.md)): one with a rig (its setup
is `yardr setup`'s, or the mayor's), one that runs, one whose mayor was
stopped with `yardr resident stop` while its daemon runs. It is help too
without a terminal, with `--json`, and in an agent's session. When the form's
own start ends with a mayor that is not up, its last line says to run `yardr`
in the terminal again once that is set right.

#### Starting over

There is no command that removes a yard. Starting over is the steps below,
each typed by you with the path or the name in it: yardr never removes a
home or a repository. Take them in this order; the first five need the yard
they stop, so the home goes last.

| what the onboarding and the setup can have made | where | to see it | to remove it |
|---|---|---|---|
| the login services | `~/Library/LaunchAgents/dev.yardr.{daemon,web}.<home>.plist`, or `~/.config/systemd/user/yardr-{daemon,web}-<home>.service` | `yardr doctor`, under Optional; `yardr daemon status` | `yardr web uninstall`, `yardr daemon uninstall`. Each unloads its service, which stops what it ran, and deletes the file |
| a web view started with `yardr web start` | a process; it logs to `<home>/web.log` | `yardr status`, under `web view` | `kill <pid>` |
| the mayor's session, and any other resident's | a herdr workspace labelled `<yard> <resident>`, agent `<yard>-<resident>` | `yardr resident list`, `herdr workspace list` | `yardr resident stop <name>`: the daemon closes the session's tab, and its workspace with it, on its next pass (30s at most). It needs the daemon running |
| sessions working beads | a herdr workspace per bead | `yardr session list`, `herdr workspace list` | `herdr workspace close <id>` for each that is left. Look at the bead's worktree first (the home, below) |
| the daemon | a process holding `<home>/daemon.lock` | `yardr daemon status` | `yardr daemon stop` |
| the server | a process; `<home>/server.pid`, `yard.sock` | `yardr server status` | `yardr server stop`. Any yardr command after it starts a server again, so run none before the home is gone. A server or daemon still running when the home is removed stops by itself within a minute ([above](#yardrs-own-state)) |
| the home | `~/.yardr`, or what `YARDR_HOME` names: the store, the roles, the logs, `scripts/merge-local` and `logs/` (a refinery's, from `yardr setup`), `worktrees/`, `rigs/` | `ls -a <home>` | `rm -rf <home>`, typed by you with the path. Read the next paragraph first |
| a rig's `.yardr` files | in the repository, committed on its base branch: `.yardr/roles/`, `.yardr/check`, `.yardr/flows/` | `git -C <repo> ls-files .yardr` | keep them: a new yard that adds the repository as a rig reads them as they are. To start the rig's configuration over too: `git -C <repo> rm -r .yardr`, and commit |

**Before the home is removed**, look at two directories in it, because they
hold work and not only yardr's state. `<home>/worktrees/<rig>/<bead>` are
the worktrees of beads, on branches `yard/<bead>` of the rig's repository:
the commits on those branches stay in the repository, and what is not
committed in a worktree goes with the home. `<home>/rigs/<name>` are
repositories themselves, cloned by `yardr rig add --clone`: removing the home
removes those clones, with every branch that was never pushed. Move out what
you want to keep. Credential files you kept in the home
(`linear.secret`, `residents/<name>.env`) go with it too.

After the home is gone, each rig's repository still lists the worktrees that
were in it: `git -C <repo> worktree prune` clears them (and the briefs yardr
kept in the repository's git directory beside them), and
`git -C <repo> branch --list 'yard/*'` shows the bead branches that are left,
for you to delete or keep. Nothing else in a repository is yardr's.

Then `yardr`, or `yardr init --harness <kind>`, creates a yard in the empty
home as on the first day. Tried in an isolated yard: after these steps no
process of the yard was left, `yardr doctor` reported no yard, and
`yardr init --harness claude --start` brought a new mayor up.

Starting over is seldom needed for what the onboarding asked: the harness,
your name and the mayor's are `yardr settings set`, the mayor's directory
and model are `yardr resident add <mayor>` again, a role prompt comes back as
this build's when its file is deleted and `yardr init` runs, and a setup that
failed is finished by the next run ([above](#a-run-that-did-not-finish)).

### Seeding a rig: `yardr rig init`

`yardr rig init <rig> [--operator <name>] [--mayor <name>] [--with-mayor]` writes a
registered rig's starting configuration into its checkout:

| file | content |
|---|---|
| `.yardr/roles/{mayor,builder,reviewer,merger}.md` | the generic roles, `{{operator}}` filled with `--operator`, default the yard's `operator` setting, else the login name, else `operator`; `{{mayor}}` with `--mayor`, default the yard's `mayor` setting, else its one resident, else `mayor` |
| `.yardr/check` | the merge gate, executable, with a comment saying so; its command is a guess from the repo: `go.mod` gives `go vet ./... && go test ./...`, a `package.json` with a real `test` script gives `npm test` (`pnpm`/`yarn` by lockfile), anything else `exit 0` with a TODO |
| `.yardr/flows/default.flow` | the standard stages: backlog, new, review, approved, merged (terminal), decide, with the outcomes `done`, `question`, `changes` and `failed` |
| `.yardr/flows/default/<stage>.md` | a starter [stage file](flows.md#stage-files-what-a-stage-is-for) for each stage but `merged`: what the stage is for, what must be true to leave it, when each outcome applies; the placeholders filled as in the roles |

The flow is `default.flow`, not a named one, so it governs the rig without a
pointer (a store pointer, as `yardr rig flow` sets, still wins).

A dir rig (`--kind dir`) gets builder and reviewer roles only (no merger, as nothing merges;
the mayor's with `--with-mayor`), no `.yardr/check` (a dir rig has no gate until one
adds it), and two flows: `default.flow` (backlog, new, review, done, decide)
and `type/epic.flow` (the same with `open` for `new`), each with a stage file
for every stage but `done`. They apply at once,
read from the directory; [dir-rigs.md](dir-rigs.md) walks through them.

It never overwrites a file that is there, so a second run changes nothing, and
it commits nothing. Stage files are written only beside a flow it writes: a
flow that was there is the rig's own, and a starter file for a stage it lacks
would fail `yardr rig check`. A freshly seeded rig's check names no stage
without a stage file. In a git rig roles, the gate, flows and stage files are read from the
base branch, so they take effect once the operator (or the mayor, with their OK)
commits them there. It prints what it wrote and the commit to make.

### Settings

`yardr settings get [<key>]` and `yardr settings set <key> <value>` (an empty
value unsets) keep the yard's own defaults in the store:

| key | meaning |
|---|---|
| `default_kind` | the agent kind a herdr group or resident gets when `group add` or `resident add` is given no `--set kind`; also the kind their forms start at. One of the kinds herdr starts. Its presence marks the yard set up. |
| `operator` | the person the role prompts name, agents bring decisions to; `yardr init` stores it (`--operator`, the form's "Your name") and `yardr rig init` fills roles with it. The operator is a user ([users.md](users.md)): setting a name adds the user when there is none, a resident's name is refused, and only the operator adds and removes other users. |
| `operator_style` | how agents should work with the operator: `guide`, `show`, `work` or `report` ([what each tells an agent](#how-agents-work-with-you)). `yardr init` stores it (`--style`, the form's "How should agents work with you?"); both primes print it as instructions, so a change reaches the next session that starts. Unset, the primes say nothing about it. |
| `operator_note` | one line agents should know about the operator (`--about`, the form's "Anything agents should know about you?"), printed by the primes with the style, or alone. |
| `mayor` | the mayor resident's name, whom the builder and reviewer roles tell agents to mail about what blocks them ([residents.md](residents.md#findings-from-builders-and-reviewers)); `yardr init` stores it (`--mayor`) and `yardr rig init` fills roles with it. A yard from before the setting has none: `rig init` then takes its one resident, else `mayor`. |
| `hook_addr` | the TCP address the server takes webhooks on, `127.0.0.1:8790` say ([subscriptions.md](subscriptions.md)). The server reads it when it starts, so a change takes its next start: `yardr server stop`, and the next command starts it again; `settings set` says so. `YARDR_HOOK_ADDR`, set where the server starts, is used instead of it. An address that is not `host:port` with a port from 1 to 65535 is refused (`:8790` is every interface). The server writes what it listens on to `hooks.state` in the home, and `yardr doctor` reports it: the address and whether it is the variable's or the setting's. A server that cannot listen there (another yard has the address) keeps serving its yard, logs a `hook_listener_failed` event, and doctor says so with what to change. |
| `name` | the yard's name, which its residents' herdr agents and workspaces carry (`<name>-mayor`, labelled `<name> mayor`), so two yards on one machine, whose agents herdr lists together, are told apart. Unset, the home's directory name: `yardr` for `~/.yardr`, `y1` for `/tmp/y1`. Changing it renames residents started from then on. |
| `timer_limit` | how many live timers one resident or user may have set ([timers.md](timers.md#limits)); a whole number from 1, default 5. |
| `timer_min_every` | the shortest period of a recurring timer (`yardr timer add --every`): a duration such as `30m` or `1d`, default `1h`. |

Environment variables yardr reads:

| variable | meaning |
|---|---|
| `YARDR_HOME` | state directory (above), when `--home` is not given; a relative path is resolved against the command's directory, once |
| `YARDR_ACTOR` | who is acting, for the event log (default: the OS user). A command whose actor is a user's name acts as that user ([users.md](users.md)); nothing checks who is at the keyboard |
| `YARDR_BEAD`, `YARDR_RESIDENT` | defaults for `prime` and resident commands; set for the sessions yardr starts |
| `YARDR_SESSION` | the session yardr started this process for; every bead move (`bead done`, `advance`, `close`, `hold`, `release`, `reopen`) sends it, and the store refuses to move the session's own bead once the session no longer owns it, and any other bead a live session owns. A call from the session's pane is checked as it even with the variable unset ([pipeline.md](pipeline.md#2-new-a-builder-implements-it)). In a bead's worktree, an environment naming another bead gives way to the worktree's session ([groups.md](groups.md#codex)) |
| `YARDR_HOOK_ADDR` | TCP address the server takes webhooks on, e.g. `127.0.0.1:8790`, read where the server starts; set, it is used instead of the `hook_addr` setting ([above](#settings)) |
| `YARDR_LINEAR_API`, `YARDR_GITHUB_API` | where `yardr reply`, `yardr thread open` and `yardr thread state` send their calls, read by the command that makes them, and where the daemon sends a hook's issue moves ([links.md](links.md#a-beads-stage-moves-the-issue)), read when it starts (default: Linear's GraphQL endpoint, `https://api.github.com`) |
| `YARDR_EVENT_RETENTION`, `YARDR_HOOK_PAYLOAD_RETENTION`, `YARDR_INBOX_RETENTION` | how long the daemon and `yardr store prune` keep events, ended sessions and webhook bodies, and how long an unread inbox event holds the log back ([below](#retention-keeping-yarddb-bounded)) |
| `YARDR_TEST_RPC` | tests: run the store suites through the unix-socket client |

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
| `YARDR_EVENT_RETENTION` | `90d` | events, with their index entries, and ended sessions (`yardr session list --all`) |
| `YARDR_HOOK_PAYLOAD_RETENTION` | `30d` | raw webhook bodies (up to 1 MiB each), read only by `yardr hook show`; the `hook` event keeps the envelope routing reads |
| `YARDR_INBOX_RETENTION` | `180d` | how long an event a running resident has not read holds the log back |

Values are days (`90d`), Go durations (`720h`), or `off` (or `0`) to keep
everything. Whatever its age, an event is kept when:

- it is the newest event (sequences continue from it);
- it is in a *running* resident's inbox, not yet acknowledged and younger
  than the inbox retention, and so is every event after it; a resident with
  nothing unread holds nothing back, and neither does one that is stopped or
  disabled, however much it has not read;
- it was logged after the oldest live bead session or running resident
  session started.

An unread inbox event past those bounds goes like any other; the prune
reports how many each resident lost (`expired`) and logs `inbox_expired` on
the resident's journal with the count, so the resident learns what it never
read. An ended session goes with the events of its time once the daemon has
stopped its runner resources and, when it left a worktree, cleaned up after
the bead closed (`workspace_removed`); a live session is never pruned.

A prune that held events back says why (`held_by`, also on its
`store_pruned` event). Beads, notes and deps are state, not log, and are
never pruned; a bead's own event history shortens with the log. What
pruning must not lose lives on the bead: its count of failed attempts in
its stage (reset when it enters a stage or `yardr bead unhold` runs) is kept
there, not read back from the `unheld` event or the ended sessions, so
pruning them does not hold a bead again without new failures. Claims and
the daemon's pass read the live sessions through an index of their own and
do not slow down with the sessions the store still keeps.

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
never `go install` from their worktrees; the mayor installs after merge.

A binary from a [release archive](install.md) upgrades the same way: unpack
the newer archive and move `yardr` over the old one (`tar` itself also
replaces the file rather than writing into it). A release build carries the
same revision and commit time as a `go install` of its commit, and those,
not the tag `yardr version` prints, decide whether it is newer; so archives
and `go install`s replace each other by commit time.

The generic role prompts in `~/.yardr/roles/` are files, not code: a new
build does not touch them. Run `yardr init` again (with the same `--harness`,
`--operator` and `--mayor`, since init stores the last two) to refresh the
ones nobody edited to the new build's templates; edited ones are kept, and
the summary says which ([above](#creating-a-yard-the-onboarding-and-yardr-init)).

The store is stamped with its schema version on open, and a yardr whose
schema is older than the store's refuses to open it (`store schema is newer
than this yardr knows`) rather than run its migrations over it. The schema
is 3: version 3 added the inbox and kind indexes, which every event is
written with, so a store at 2 is reindexed in full the first time a newer
build opens it (and a build without the indexes can no longer append to it
unindexed); version 2 keyed beads by UUID.

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
cover it. If a rig under one of those folders fails with `operation not
permitted` after an upgrade, grant the new build again
([above](#login-services)), or keep rigs out of those folders, which no
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

### The rename from yard

yardr was called yard. For one release:

- each `YARDR_*` variable is still read under its old name (`YARD_*`) when
  the new one is unset, and yardr sets both names for the agents and
  commands it starts;
- if `~/.yardr` does not exist but `~/.yard` does, yardr keeps using
  `~/.yard` (the server log says so). Move it once no server, agent or
  worktree uses it; the file names inside are unchanged, so it is a plain
  `mv`.
