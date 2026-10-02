# Configuration: the repo and the store

yardr reads its configuration from two places:

- **the rig's repository**, under `.yardr/`: what the work is. Versioned
  with the code it describes and changed through review.
- **the store** (`yard.db`, owned by the yardr server): who works it, where
  and how. Live state, changed with `yardr` commands, no commit needed.

The split was decided in yard-17 (2026-10-02). Parts of the repo side are not
built yet; each is marked **planned** with the bead that builds it. Flows
are built on both sides (yard-9cza).

## What lives where

| | repo `.yardr/` (what the work is) | store (who / where / how) |
|---|---|---|
| flows | the rig's default and type flows, named flows: `.yardr/flows/` (built) | named flows, and rig and type pointers to them (built) |
| merge gate | `.yardr/check` (*planned, yard-p2y7*) | a per-rig override, `rig add --check` (*planned, yard-p2y7*) |
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
  (*planned, yard-p2y7*).
- **Repo `.yardr/`** (built for flows; *planned, yard-p2y7* for the
  check): the default that ships with the code.
- **None**: no flow means a bead may move to any stage; no check means
  nothing gates the merge.

Within one source a type-specific flow wins over the rig's flow; a source
wins as a whole, so a rig pointer in the store overrides the repo's type
files too (see [flows.md](flows.md)). `yardr bead show` and `prime` say
which flow governs a bead and where it came from: `pin`, `store`,
`repo@<sha>`, and the type when a type's flow matched.

## Repo config comes from the base branch

yardr reads `.yardr/` from the tip of the rig's base branch
(`git show <base>:.yardr/...`), never from the bead's branch or worktree
(built for flows; *planned, yard-p2y7* for the check). A bead therefore
cannot lower its own bar: a builder who edits `.yardr/check` or a flow on `yard/<bead>` changes
nothing for that bead. The edit takes effect only once it has been reviewed
and landed on the base, like any other code, and then applies to every bead
after it. yardr resolves the base on every check and re-reads the files
when its commit moves.

## Briefs are not config

The herdr runner writes each agent's brief into its worktree, today under
`<worktree>/.yard/`. Once `.yardr/` holds committed config, briefs move out
of the tracked tree, under the worktree's git directory (*planned,
yard-p2y7*), so nothing generated per session sits next to committed files.

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

### The rename from yard

yardr was called yard. For one release:

- each `YARDR_*` variable is still read under its old name (`YARD_*`) when
  the new one is unset, and yardr sets both names for the agents and
  commands it starts;
- if `~/.yardr` does not exist but `~/.yard` does, yardr keeps using
  `~/.yard` (the server log says so). Move it once no server, agent or
  worktree uses it; the file names inside are unchanged, so it is a plain
  `mv`.
