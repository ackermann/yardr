# yardr

[![ci](https://github.com/McKean/yardr/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/McKean/yardr/actions/workflows/ci.yml)

Route beads of work to whoever works them.

A **bead** is a durable unit of work. **Routes** send open, unblocked beads in
a **stage** to a **group**; a group's **runner** starts the work (an agent in
herdr, a shell command, or nobody — a human). Whoever works the bead advances
it to the next stage or closes it, and the next route takes over. Roles are
not built in: "builder" or "reviewer" is just a group plus a prompt file.

The yard lives in one file (`~/.yardr/yard.db`, bbolt) owned by one process,
the yardr server, which every other yardr process reaches over a unix socket.
Every change is committed together with its event; events carry a sequence
number, so subscribers get every change pushed in order and resume from where
they stopped. Nothing polls: the dispatcher wakes on each committed write,
re-reads state and acts idempotently, so a missed wake-up only costs a delay
and a restart is just another pass.

## Quick start

```sh
go install .        # 1. one static binary, yardr; nothing else to install
yardr               # 2. the onboarding: answer the form
yardr daemon        # 3. in a pane of its own; it starts the mayor
                    # 4. talk to the mayor in its pane: it sets up the rest
```

The onboarding asks for the agent harness yardr starts by default (each herdr
kind, marked installed or not), the mayor's name (the always-on resident that
coordinates the yard), the directory its shell lives in, and your name. It
installs generic role prompts in `~/.yardr/roles/` (mayor, builder, merger,
reviewer, naming you as the one who decides), stores the harness as the
yard's `default_kind` and your name as its `operator`, and adds the mayor.

The mayor's first job is the setup, as a conversation with you: it asks for
the repository and its base branch, registers the rig, seeds it with
`yardr rig init` (starter roles, gate, flow and stage files under `.yardr/`, which you
commit on the base branch), creates the groups and routes, and offers a first
bead to try the pipeline. It asks before anything outward, such as a commit.

A home counts as set up once its store has `default_kind`; a store the server
created on some earlier command, with nothing in it, still onboards. Without a
terminal, a command on a home with no yard says so and exits 2. To try the
onboarding without touching your yard, give it a home of its own; keep it
short, since the server's unix socket lives in it (a home too long for one is
refused):

```sh
YARDR_HOME=/tmp/y1 yardr                          # the form
YARDR_HOME=/tmp/y1 yardr init --harness claude    # the scripted path
```

Scripts skip the form and the mayor's conversation (`yardr init --help`):

```sh
yardr init --harness claude [--mayor mayor] [--dir ~/projects] [--model opus] [--operator Jo]
yardr rig add webapp ~/projects/webapp --base origin/main
yardr rig init webapp                   # .yardr/roles, .yardr/check, .yardr/flows/default.flow and its stage files; commit them

# role prompts: prompt=roles/<name>.md reads the rig's own .yardr/roles/<name>.md
# (as on its base branch), else the generic ~/.yardr/roles/<name>.md

# who does the work (--set kind defaults to the yard's default_kind)
yardr group add webapp/builders  --runner herdr --limit 2 --set prompt=roles/builder.md
yardr group add webapp/reviewers --runner herdr --set kind=codex --set prompt=roles/reviewer.md
yardr group add webapp/merger    --runner exec  --set cmd='scripts/merge-bead.sh'
yardr group add mckean           --runner manual

# which stage goes where
yardr route add --stage new      --rig webapp --group webapp/builders
yardr route add --stage review   --rig webapp --group webapp/reviewers
yardr route add --stage approved --rig webapp --group webapp/merger
yardr route add --stage blocked  --group mckean

yardr daemon &                          # routes and supervises; starts the mayor
yardr watch                             # live event stream

yardr bead create "Add login page" --rig webapp -t feature -b "Use the OAuth flow"
# it starts in the flow's first stage, backlog; the mayor shapes it and advances it to new
```

The yardr server starts on first use, in the background, and owns
`~/.yardr/yard.db` (`yardr server status|stop`; `YARDR_HOME` moves everything).
Back up with `yardr store backup <path>`, which copies it while the server
keeps serving. To upgrade, `go install` again: the server and daemon replace
themselves with the new build ([details](docs/configuration.md#upgrading)).

yardr was called yard; for one release it still reads `YARD_*` variables and
`~/.yard` ([details](docs/configuration.md#the-rename-from-yard)).

`yardr watch` streams every event as it commits. Each line starts with the
event's sequence number; `yardr watch --after <seq>` resumes from there with no
gaps or duplicates, and a running watch rides out a server restart.

Work that spans several beads is an epic: `yardr bead create -t epic`, then
`yardr dep add <epic> <child> -k parent` for each child. Children branch from
and land on the epic's branch, and the epic itself is integrated, reviewed as a
whole and landed on main through its own flow ([details](docs/pipeline.md#epics)).

## Rigs

A rig is a git repository beads are worked in. Register an existing clone,
or let yardr clone it:

```sh
yardr rig add webapp ~/projects/webapp                       # existing clone
yardr rig add webapp --clone git@github.com:me/webapp.git    # → ~/.yardr/rigs/webapp
yardr rig add legacy ~/src/legacy --base origin/master       # base must resolve
yardr rig rm legacy                                          # store only; the clone stays
```

Bead branches (`yard/<bead>`) start from the rig's base, fetched before each
new worktree. Give beads a rig with `--rig`; routes can match on it, and
a group's `prompt=roles/<name>.md` reads the rig's `.yardr/roles/<name>.md`
from its base, then `~/.yardr/roles/<name>.md`
([details](docs/configuration.md#role-prompts)). Rig names, like
bead types and dependency kinds, are lowercase letters, digits, `.`, `_` and
`-`. `rig rm` removes a rig with its routes and flow pointers, and is refused
while the rig has open beads or live sessions; it touches nothing on disk.

A rig can also be a plain directory, a folder of documents or invoices with no
git (`--kind dir`): see [docs/dir-rigs.md](docs/dir-rigs.md) for a worked example.

## Working a bead (humans and agents alike)

```sh
yardr prime --bead webapp-3                  # bead, edges, notes
yardr bead note webapp-3 "tests green: ..."  # evidence
yardr bead advance webapp-3 --to review      # hand to the next stage
yardr bead advance webapp-3 --to new --note "rename the flag"  # note and move in one write
yardr bead close webapp-3 --reason "merged"
yardr bead hold webapp-3                     # stop routing; a human looks
```

Every command takes `--json`. Set `YARDR_ACTOR` so the event log shows who
acted.

### Bead ids

Every bead has two names. Its **handle** (`webapp-k3f9`: the rig, or
`--prefix`, then random base32) is what yardr prints and what branches
(`yard/<handle>`), worktrees and herdr labels are built from; it is chosen
once and never changes. Its **UUID** (a UUIDv7) is its identity: the store
is keyed by it, so ids need no central counter. `--json` shows both
(`id` is the handle, `uuid` the UUID); `--id` on create picks the handle.

Commands accept the UUID, the handle, or a unique prefix of the handle
with at least 4 characters after the `-` (`webapp-k3f`... is too short; an
ambiguous prefix lists its candidates).

A UUIDv7 starts with its creation time, so the store's key order is
creation order, and listings use it. Handles are random and carry no
order: never sort by handle.

A store from before UUIDs is migrated once when the server opens it, after
copying it to `yard.db.pre-uuid-<time>`: each bead keeps its old id
(`webapp-3`) as its handle, so existing branches and worktrees stay valid,
and gets a UUID built from its creation time.

## A short tour

The rest of the documentation is in [docs/](docs/README.md):

- **[Configuration](docs/configuration.md):** what a repo's `.yardr/` holds
  (what the work is: flows, the check gate, roles) and what the store holds
  (who, where and how: groups, runners, routes, residents), which wins, and
  where yardr keeps its own state.
- **[Flows](docs/flows.md):** bead lifecycles. Named flows rigs and types
  point at, flows in the repo's `.yardr/flows/` read from the base branch,
  which stages a bead may move to next, and terminal stages that close it.
  Edges may carry outcomes (`new -> review on done | decide on question`):
  agents finish with `yardr bead done <id> [--outcome <o>]` and the flow
  picks the stage; edges without one are left to `bead advance --to`.
- **[The pipeline](docs/pipeline.md):** how a bead moves in practice, from
  backlog through builders, review and the refinery to main, using the rig
  yardr is built with.
- **[Groups and runners](docs/groups.md):** `manual`, `exec` and `herdr`
  runners, the interactive `group add`, and setting up herdr agents.
- **[Review](docs/review.md):** `yardr review`, a form for beads waiting on you.
- **[Residents](docs/residents.md):** always-on sessions such as a mayor, with
  inbox, webhook subscriptions, journal, handoffs and memories.
- **[Subscriptions](docs/subscriptions.md):** what reaches a resident's inbox:
  hook sources, yardr's own bead events (`--done`, `yardr bead watch`), groups.
- **[BDP](docs/bdp.md):** serving the yard over the Beads Protocol.

## Layout

```
internal/store      the bbolt store: records, indexes, revisions, event log and push hub
internal/dispatch   routing, claims, liveness, teardown (the only control loop)
internal/runner     manual, exec, herdr
internal/bdp        the BDP Read server (protocol rules, selector, paging)
internal/rpc        the store API over the server's unix socket
internal/server     the store-owning server process; started on first use
internal/version    build revisions; notices a new binary installed over a running one
internal/cli        cobra commands
contrib/refinery    merge-local, a reference refinery script for an exec group
contrib/isolated-yard  a throwaway yard and herdr server for tests that start agents
```

`go test ./...` runs everything against throwaway store files;
`YARDR_TEST_RPC=1 go test ./...` runs the same suites through the unix-socket
client, so the wire is tested by the same assertions.
