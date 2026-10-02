# yardr

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
go install .                            # one static binary, yardr; nothing else to install
alias yd=yardr                          # the short form, if you like it

yardr rig add webapp ~/projects/webapp --base origin/main

# who does the work
yardr group add webapp/builders  --runner herdr --limit 2 \
    --set kind=claude --set prompt=roles/builder.md
yardr group add webapp/reviewers --runner herdr --set kind=codex --set prompt=roles/reviewer.md
yardr group add webapp/merger    --runner exec  --set cmd='scripts/merge-bead.sh'
yardr group add mckean           --runner manual

# which stage goes where
yardr route add --stage new      --rig webapp --group webapp/builders
yardr route add --stage review   --rig webapp --group webapp/reviewers
yardr route add --stage approved --rig webapp --group webapp/merger
yardr route add --stage blocked  --group mckean

yardr daemon &                          # routes and supervises
yardr watch                             # live event stream

yardr bead create "Add login page" --rig webapp -t feature -b "Use the OAuth flow"
```

The yardr server starts on first use, in the background, and owns
`~/.yardr/yard.db` (`yardr server status|stop`; `YARDR_HOME` moves everything).
Back up by copying `yard.db` while the server is stopped.

yardr was called yard; for one release it still reads `YARD_*` variables and
`~/.yard` ([details](docs/configuration.md#the-rename-from-yard)).

`yardr watch` streams every event as it commits. Each line starts with the
event's sequence number; `yardr watch --after <seq>` resumes from there with no
gaps or duplicates, and a running watch rides out a server restart.

## Rigs

A rig is a git repository beads are worked in. Register an existing clone,
or let yardr clone it:

```sh
yardr rig add webapp ~/projects/webapp                       # existing clone
yardr rig add webapp --clone git@github.com:me/webapp.git    # → ~/.yardr/rigs/webapp
yardr rig add legacy ~/src/legacy --base origin/master       # base must resolve
```

Bead branches (`yard/<bead>`) start from the rig's base, fetched before each
new worktree. Give beads a rig with `--rig`; routes can match on it, and
group prompt paths are resolved inside it.

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
internal/cli        cobra commands
```

`go test ./...` runs everything against throwaway store files;
`YARDR_TEST_RPC=1 go test ./...` runs the same suites through the unix-socket
client, so the wire is tested by the same assertions.
