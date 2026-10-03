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
| merge gate | `.yardr/check` (built) | a per-rig override, `rig add --check` (built) |
| roles | the rig's own role prompts: `.yardr/roles/<name>.md`, read from the base (built) | the generic roles `yardr init` installed in `~/.yardr/roles/`, the fallback; residents' prompts, by absolute path (built) |
| groups | | runner, limit, members, subscriptions (manual), agent kind, model, args, cmd, worktree settings (built) |
| routes | | which group works which stage, by rig, type, label (built) |
| rigs | | path, kind (`git` or `dir`), clone, base branch; `rig add` registers, `rig rm` removes (built) |
| residents | | agent, directory, inbox kinds, rotation, subscriptions, identities (built) |
| settings | | the yard's defaults: `default_kind`, `operator`, `mayor` (built) |

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
  3. the yard's home, `~/.yardr/roles/<name>.md` (`YARDR_HOME`).
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
yardr rig check yard                                      # in the rig
yardr rig check yard --dir ~/.yardr/worktrees/yard/yard-12   # in a bead's worktree
```

prints the tail of the output (`--tail N`, default 40; a red gate whose
tail is empty says so, or how many lines it hid) and pass or FAIL; `--json`
gives the full output, the exit code and the source (`store`, `repo@<sha>`,
`dir` for a dir rig's own file, or empty for a rig with no gate). It exits 0 on a green gate, 1 on a red one
(no gate and a timeout included), and 2 when the gate could not run at all:
an unknown rig, a base that does not resolve, git failing. The check is
killed, with every process it started, after `--timeout` (default 30m), and
each git call that finds it is bounded (30s; the fetch, 60s).
Builders run it before handing on, and the refinery runs it too:
[`contrib/refinery/merge-local`](../contrib/refinery/merge-local) runs
`yardr rig check` on exactly what will land, after any rebase, and sends a
red gate back to `new` (see [pipeline.md](pipeline.md#4-approved-the-refinery-lands-it-on-main)).

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

Everything yardr keeps for itself lives in one directory, **`YARDR_HOME`**
(default `~/.yardr`):

| path | what |
|---|---|
| `yard.db` | the store (bbolt); back it up with `yardr store backup <path>` while it serves (one read transaction, so the copy is the store as of one commit); restore by stopping the daemon and the server and copying the backup over it |
| `yard.sock` | the server's unix socket |
| `server.pid`, `server.log` | the running server |
| `worktrees/<rig>/<bead>` | herdr bead worktrees |
| `rigs/<name>` | rigs registered with `rig add --clone`; `rig rm` leaves the clone here |
| `roles/` | the generic role prompts `yardr init` installed (mayor, builder, merger, reviewer), the fallback for rigs without their own; yours to edit. `.shipped.json` there records each file's hash as init wrote it, so init again can tell an edited prompt (kept) from an untouched one (refreshed) |
| `linear.secret`, `github.secret`, `residents/<name>.env`, `groups/<group>.env` | credential files, if you keep them here as the examples do ([residents.md](residents.md#outbound-credentials)): yours, yardr creates none of them and reads only the paths a hook, resident or group names |

Set `YARDR_HOME` to run a separate yard (tests do, with a short path: unix
socket paths are limited to about 100 bytes, and yardr refuses a home whose
`yard.sock` would be longer). A relative `YARDR_HOME` is made absolute
once, when the command loads its configuration, so the server, the agents
and the gate it starts, which run elsewhere, get the directory it named and
not one relative to their own.

The server is started by the first command that connects to a yard with no
server, and serves every command after it. It gets none of that command's
environment or working directory: the first command is as often an
agent's, in a pane holding the credentials its `env_file` set and in a
worktree, as the operator's. It starts with `PATH`, `HOME`, the settings
the server reads (`YARDR_HOOK_ADDR`, `YARDR_EVENT_RETENTION`,
`YARDR_HOOK_PAYLOAD_RETENTION`, under the old `YARD_` names too) and
`YARDR_HOME` set to the home it serves, and runs in that home. The daemon
is not started this way: it runs in a pane of its own with that pane's
environment.

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
prompts keep the name they have). Only an empty store onboards. The store
file alone is no marker: the server creates `yard.db` on the first command
that connects, whatever it was, so a home can hold an empty store nobody set
up. Any yardr command on a home that is not set
up, bare `yardr` included (help otherwise; `--help` is always help), runs the
onboarding when it has a terminal: the default harness (every kind herdr starts, marked
installed when its executable is on PATH, the first installed one
preselected), the mayor's name (default `mayor`), the directory its shell
lives in (default the home's parent, so `~` for `~/.yardr`), and an optional
model, and your name for the role prompts. Without a terminal it prints how
to initialize and exits 2.
`yardr server`, `yardr init`, `yardr daemon`, `yardr settings` and
`yardr help` never ask.

`yardr init [--harness <kind>] [--mayor <name>] [--dir <path>] [--model <m>]
[--operator <name>]` does the same without the form, for scripts and tests;
`--harness` is required to create a yard. Either way yardr:

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
   form's "Your name", `--operator`, default `$USER`), and where they say
   `{{mayor}}`, the resident builders and reviewers mail about what blocks
   them, the mayor's name. A rig's own roles go in its repo under
   `.yardr/roles/` ([role prompts](#role-prompts));
2. sets `operator` to the operator's name and `mayor` to the mayor's;
3. adds the mayor resident: `kind=<harness>`, `prompt=<home>/roles/mayor.md`,
   the model if given, the harness's unattended flags as `args`, rotating
   every 6h. A resident of that name already there is kept as it is, so
   init runs again on a yard without harm;
4. sets `default_kind` to the harness, which marks the yard set up.

Init again on a yard that is set up changes only what its flags say: the
`default_kind`, `operator` and `mayor` settings stand in for `--harness`,
`--operator` and `--mayor` when those are not given, rather than being
reset to the flags' defaults (the login name, `mayor`), and the summary
lists what was kept (`kept default_kind claude, operator Jo, mayor mayor, as
set before`). A resident of the mayor's name is kept as it is, and so are
the role files already in `<home>/roles/`.

The summary ends with the daemon: it starts the mayor, whose role begins with
a first-run checklist. While `yardr rig list` is empty the mayor sets the
yard up with the operator in its pane: the repository and its base branch
(`yardr rig add`), `yardr rig init` ([below](#seeding-a-rig-yardr-rig-init)),
the builder and reviewer groups on the default harness, how approved beads
land (a refinery or a manual group), routes for `new`, `review` and
`approved`, and a first bead to try the pipeline. `yardr init --help` lists
the same as commands, for scripts.

The unattended flags keep an agent nobody watches from stopping at a
permission prompt: claude `--permission-mode auto`, codex
`--dangerously-bypass-approvals-and-sandbox`, kimi `--yolo`; any other kind
gets none and may ask (`yardr group add --help` lists them too). The daemon
starts the mayor; init does not start a daemon, which runs in a terminal pane
of its own (`yardr daemon`).

### Seeding a rig: `yardr rig init`

`yardr rig init <rig> [--operator <name>] [--mayor <name>] [--with-mayor]` writes a
registered rig's starting configuration into its checkout:

| file | content |
|---|---|
| `.yardr/roles/{mayor,builder,reviewer,merger}.md` | the generic roles, `{{operator}}` filled with `--operator`, default the yard's `operator` setting, else `$USER`; `{{mayor}}` with `--mayor`, default the yard's `mayor` setting, else its one resident, else `mayor` |
| `.yardr/check` | the merge gate, executable, with a comment saying so; its command is a guess from the repo: `go.mod` gives `go vet ./... && go test ./...`, a `package.json` with a real `test` script gives `npm test` (`pnpm`/`yarn` by lockfile), anything else `exit 0` with a TODO |
| `.yardr/flows/default.flow` | the standard stages: backlog, new, review, approved, merged (terminal), decide, with the outcomes `done`, `question`, `changes` and `failed` |

The flow is `default.flow`, not a named one, so it governs the rig without a
pointer (a store pointer, as `yardr rig flow` sets, still wins).

A dir rig (`--kind dir`) gets builder and reviewer roles only (no merger, as nothing merges;
the mayor's with `--with-mayor`), no `.yardr/check` (a dir rig has no gate until one
adds it), and two flows: `default.flow` (backlog, new, review, done, decide)
and `type/epic.flow` (the same with `open` for `new`). They apply at once,
read from the directory; [dir-rigs.md](dir-rigs.md) walks through them.

It never overwrites a file that is there, so a second run changes nothing, and
it commits nothing. In a git rig roles, the gate and flows are read from the
base branch, so they take effect once the operator (or the mayor, with their OK)
commits them there. It prints what it wrote and the commit to make.

### Settings

`yardr settings get [<key>]` and `yardr settings set <key> <value>` (an empty
value unsets) keep the yard's own defaults in the store:

| key | meaning |
|---|---|
| `default_kind` | the agent kind a herdr group or resident gets when `group add` or `resident add` is given no `--set kind`; also the kind their forms start at. One of the kinds herdr starts. Its presence marks the yard set up. |
| `operator` | the person the role prompts name, agents bring decisions to; `yardr init` stores it (`--operator`, the form's "Your name") and `yardr rig init` fills roles with it. |
| `mayor` | the mayor resident's name, whom the builder and reviewer roles tell agents to mail about what blocks them ([residents.md](residents.md#findings-from-builders-and-reviewers)); `yardr init` stores it (`--mayor`) and `yardr rig init` fills roles with it. A yard from before the setting has none: `rig init` then takes its one resident, else `mayor`. |
| `name` | the yard's name, which its residents' herdr agents and workspaces carry (`<name>-mayor`, labelled `<name> mayor`), so two yards on one machine, whose agents herdr lists together, are told apart. Unset, the home's directory name: `yardr` for `~/.yardr`, `y1` for `/tmp/y1`. Changing it renames residents started from then on. |

Environment variables yardr reads:

| variable | meaning |
|---|---|
| `YARDR_HOME` | state directory (above); a relative path is resolved against the command's directory, once |
| `YARDR_ACTOR` | who is acting, for the event log (default: the OS user) |
| `YARDR_BEAD`, `YARDR_RESIDENT` | defaults for `prime` and resident commands; set for the sessions yardr starts |
| `YARDR_SESSION` | the session yardr started this process for; every bead move (`bead done`, `advance`, `close`, `hold`, `release`, `reopen`) sends it, and the store refuses to move the session's own bead once the session no longer owns it, and any other bead a live session owns. A call from the session's pane is checked as it even with the variable unset ([pipeline.md](pipeline.md#2-new-a-builder-implements-it)). In a bead's worktree, an environment naming another bead gives way to the worktree's session ([groups.md](groups.md#codex)) |
| `YARDR_HOOK_ADDR` | TCP address the server takes webhooks on, e.g. `127.0.0.1:8790` |
| `YARDR_EVENT_RETENTION`, `YARDR_HOOK_PAYLOAD_RETENTION`, `YARDR_INBOX_RETENTION` | how long the daemon and `yardr store prune` keep events, ended sessions and webhook bodies, and how long an unread inbox event holds the log back ([below](#retention-keeping-yarddb-bounded)) |
| `YARDR_TEST_RPC` | tests: run the store suites through the unix-socket client |

Exec commands and herdr bead agents also get `YARDR_SESSION`, `YARDR_STAGE`
and `YARDR_GROUP` (see [groups.md](groups.md)).

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
