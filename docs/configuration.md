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
| roles | codebase role prompts, e.g. this repo's `roles/builder.md` (built: any repo path, named by a group's `prompt`) | prompts outside any repo: relative paths not in the rig resolve in `~/.yardr`, or an absolute path (built) |
| groups | | runner, limit, members, subscriptions (manual), agent kind, model, args, cmd, worktree settings (built) |
| routes | | which group works which stage, by rig, type, label (built) |
| rigs | | path, clone, base branch (built) |
| residents | | agent, directory, inbox kinds, rotation, subscriptions, identities (built) |

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

A role prompt can live on either side: a prompt about the codebase belongs
in the repo, and a group names it by its path inside the rig
(`--set prompt=roles/builder.md`); a prompt that is the operator's own (rig
yard's reviewer prompt in `~/.yardr/roles/`) stays out of the repo.

### Role prompts

A group's `prompt` is read when a session starts:

- an absolute path is read as is;
- a relative path is looked up in the bead's rig first, then in the yard's
  home (`YARDR_HOME`, default `~/.yardr`). `prompt=roles/reviewer.md` reads
  the rig's `roles/reviewer.md` when the repo has one, and
  `~/.yardr/roles/reviewer.md` otherwise.

So a fresh rig works from the operator's prompts in `~/.yardr/roles/` until
it commits its own, and committing one overrides the home copy for that rig
only. A prompt found in neither place fails the session start with both paths
in the error; three such failures hold the bead.

## Session limits and outages

A stuck bead session should surface, not run forever. Each group can set,
with `yardr group add <name> ... --set key=value`:

| key | default | meaning |
|---|---|---|
| `idle` | `30m` | the agent sat idle (herdr status `idle` or `done`) this long: a `session_idle` event, once per idle spell |
| `max_age` | `4h` | the session ran this long: a `session_aged` event, once per session |
| `grace` | unset | when set, a session still idle (or still running) this long past its limit is stopped and counted as a failed session; `0` stops it at once. Unset, sessions are only reported |

Durations are Go durations (`45m`, `2h`) or a number of seconds; `off` or
`0` turns `idle` or `max_age` off. The defaults apply to runners that report
an agent's status (herdr). For `exec` and `manual` groups only what the group
sets applies, and `idle` never does, since they cannot tell idle from busy.
The idle clock is the daemon's: one that replaces itself starts it again,
while the age is counted from the session's start and reported only once.

These events and `runtime_down` (below) are in a new resident's default
`--on`, so the mayor's inbox gets them; a resident made before them needs
them added to its `--on` (`runtime_down` is about no bead, so a `yardr`
subscription does not deliver it).

**Outages are not failures.** When a runner cannot start anything at all
(herdr missing from `PATH`, herdr down, `sh` not found), the session is
ended as `aborted`, which does not count toward the 3 failures that hold a
bead, and the bead waits where it is. The group gets one `runtime_down`
event per outage and is not tried again for 30s, doubling up to 5m while it
stays down; the first start that works logs `runtime_up`. Errors that are
the bead's own (no rig, a bad brief, a worktree that cannot be made) still
count. The fetch of a rig's base before a bead branches from it gives up
after 60s and never waits on a credential prompt (`GIT_TERMINAL_PROMPT=0`),
so a dead remote cannot stall the daemon.

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
  gate ("no gate") unless the rig was added with `--allow-no-check`.

Within one source a type-specific flow wins over the rig's flow; a source
wins as a whole, so a rig pointer in the store overrides the repo's type
files too (see [flows.md](flows.md)). `yardr bead show` and `prime` say
which flow governs a bead and where it came from: `pin`, `store`,
`repo@<sha>`, and the type when a type's flow matched. For the check the
rule is: the rig's `--check`, else `.yardr/check` on the base, else no
gate, which fails unless the rig allows it.

## Repo config comes from the base branch

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

```
yardr rig check yard                                      # in the rig
yardr rig check yard --dir ~/.yardr/worktrees/yard/yard-12   # in a bead's worktree
```

prints the tail of the output (`--tail N`, default 40; a red gate whose
tail is empty says so, or how many lines it hid) and pass or FAIL; `--json`
gives the full output, the exit code and the source (`store`, `repo@<sha>`,
or empty for a rig with no gate). It exits 0 on a green gate, 1 on a red one
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

## yardr's own state

Everything yardr keeps for itself lives in one directory, **`YARDR_HOME`**
(default `~/.yardr`):

| path | what |
|---|---|
| `yard.db` | the store (bbolt); back it up with `yardr store backup <path>` while it serves (one read transaction, so the copy is the store as of one commit); restore by stopping the daemon and the server and copying the backup over it |
| `yard.sock` | the server's unix socket |
| `server.pid`, `server.log` | the running server |
| `worktrees/<rig>/<bead>` | herdr bead worktrees |
| `rigs/<name>` | rigs registered with `rig add --clone` |

Set `YARDR_HOME` to run a separate yard (tests do, with a short path: unix
socket paths are limited to about 100 bytes).

Environment variables yardr reads:

| variable | meaning |
|---|---|
| `YARDR_HOME` | state directory (above) |
| `YARDR_ACTOR` | who is acting, for the event log (default: the OS user) |
| `YARDR_BEAD`, `YARDR_RESIDENT` | defaults for `prime` and resident commands; set for the sessions yardr starts |
| `YARDR_SESSION` | the session yardr started this process for; `bead done`, `advance` and `close` refuse to move a bead that session no longer owns ([pipeline.md](pipeline.md#2-new-a-builder-implements-it)) |
| `YARDR_HOOK_ADDR` | TCP address the server takes webhooks on, e.g. `127.0.0.1:8790` |
| `YARDR_EVENT_RETENTION`, `YARDR_HOOK_PAYLOAD_RETENTION` | how long the daemon and `yardr store prune` keep events and webhook bodies ([below](#retention-keeping-yarddb-bounded)) |
| `YARDR_TEST_RPC` | tests: run the store suites through the unix-socket client |

Exec commands and herdr bead agents also get `YARDR_SESSION`, `YARDR_STAGE`
and `YARDR_GROUP` (see [groups.md](groups.md)).

### Retention: keeping `yard.db` bounded

The event log and the raw webhook bodies beside it are pruned by age. The
daemon prunes when it starts and every hour; `yardr store prune` does it on
demand (`--events` and `--hook-payloads` override the knobs for one run).

| variable | default | what it keeps |
|---|---|---|
| `YARDR_EVENT_RETENTION` | `90d` | events, with their index entries |
| `YARDR_HOOK_PAYLOAD_RETENTION` | `30d` | raw webhook bodies (up to 1 MiB each), read only by `yardr hook show`; the `hook` event keeps the envelope routing reads |

Values are days (`90d`), Go durations (`720h`), or `off` (or `0`) to keep
everything. Whatever its age, an event is kept when:

- it is the newest event (sequences continue from it);
- it is in a resident's inbox and not yet acknowledged, and so is every
  event after it; a resident with nothing unread holds nothing back;
- it was logged after the oldest live bead session or running resident
  session started.

A prune that held events back says why (`held_by`, also on its
`store_pruned` event). Beads, notes and deps are state, not log, and are
never pruned; a bead's own event history shortens with the log.

bbolt reuses the pages pruning frees but never returns them to the
filesystem. To shrink the file, stop the daemon and the server
(`yardr server stop`) and run `yardr store compact`; it writes a compacted
copy and swaps it in only once it is complete, and refuses while anything
holds the store.

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

The store is stamped with its schema version on open, and a yardr whose
schema is older than the store's refuses to open it (`store schema is newer
than this yardr knows`) rather than run its migrations over it.

How a newer build takes over:

- the server notices when a client of another build calls, or within about
  10 seconds anyway. It refuses that one call (the client retries), stops
  accepting, answers the calls in flight, closes the store and execs the
  new build. Clients retry for a few seconds meanwhile, and one that finds
  no answer but a live pid in `server.pid` waits rather than starting a
  second server;
- the daemon checks between passes, at least every 10 seconds; it finishes
  the pass it is in, then execs. Sessions live in the store, so the new
  daemon carries on with them.

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
