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
| roles | codebase role prompts, e.g. this repo's `roles/builder.md` (built: any repo path, named by a group's `prompt`) | prompts outside any repo, by absolute path (built) |
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
yard's reviewer prompt in `~/.yardr/roles/`) is named by absolute path and
stays out of the repo.

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
- **None**: no flow means a bead may move to any stage; no check means
  nothing gates the merge.

Within one source a type-specific flow wins over the rig's flow; a source
wins as a whole, so a rig pointer in the store overrides the repo's type
files too (see [flows.md](flows.md)). `yardr bead show` and `prime` say
which flow governs a bead and where it came from: `pin`, `store`,
`repo@<sha>`, and the type when a type's flow matched. For the check the
rule is: the rig's `--check`, else `.yardr/check` on the base, else none.

## Repo config comes from the base branch

yardr reads `.yardr/` from the tip of the rig's base branch
(`git show <base>:.yardr/...`), never from the bead's branch or worktree
(built). A bead therefore cannot lower its own bar: a builder who edits
`.yardr/check` or a flow on `yard/<bead>` changes nothing for that bead. The edit takes effect only once it has been reviewed
and landed on the base, like any other code, and then applies to every bead
after it. yardr resolves the base on every check and re-reads the files
when its commit moves.

### The merge gate: `.yardr/check`

`.yardr/check` is a script; exit 0 means green. yardr writes the base's copy
to a temporary file under the rig's git directory and runs it in the
directory being checked (a script without `#!` runs with `sh`; the rig's
name is in `$YARDR_RIG`). A store override, `yardr rig add <rig> <path>
--check '<cmd>'`, runs with `sh -c` instead. Re-running `rig add` without
`--check` drops the override.

```
yardr rig check yard                                      # in the rig
yardr rig check yard --dir ~/.yardr/worktrees/yard/yard-12   # in a bead's worktree
```

prints the tail of the output (`--tail N`, default 40) and pass or FAIL,
and exits 1 on a failing gate; `--json` gives the full output, the exit code
and the source (`store`, `repo@<sha>`, or empty for a rig with no gate,
which passes). Builders run it before handing on, and the refinery runs it
on what will land.

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
| `yard.db` | the store (bbolt); back it up by copying it while the server is stopped |
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
| `YARDR_HOOK_ADDR` | TCP address the server takes webhooks on, e.g. `127.0.0.1:8790` |
| `YARDR_TEST_RPC` | tests: run the store suites through the unix-socket client |

Exec commands also get `YARDR_SESSION`, `YARDR_STAGE` and `YARDR_GROUP`
(see [groups.md](groups.md)).

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
