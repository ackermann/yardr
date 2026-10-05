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
go install .        # 1. one static binary, yardr; it needs git and herdr on PATH
                    #    (or a release archive, without Go: see Install below)
yardr               # 2. the onboarding: answer the form; it starts the yard
                    #    and prints where the mayor is
                    # 3. talk to the mayor in its pane, or keep going in the
                    #    terminal: either sets up the rest (yardr setup)
```

### Install

`go install .` from a checkout is one way. The other is a release archive,
which needs no Go: `yardr_<version>_<os>_<arch>.tar.gz` for darwin and linux,
arm64 and amd64, with a `checksums.txt`. The archives need a login while the
repository is private, so download with `gh`:

```sh
gh release download v1.2.3 --repo McKean/yardr \
  --pattern 'yardr_1.2.3_darwin_arm64.tar.gz' --pattern checksums.txt
shasum -a 256 --ignore-missing -c checksums.txt   # must say OK
tar -xzf yardr_1.2.3_darwin_arm64.tar.gz yardr && mv yardr ~/.local/bin/
```

Either way yardr still needs git, herdr and an agent harness (claude, codex,
…) on PATH; `yardr doctor` says which are missing and where to get them.
[docs/install.md](docs/install.md) has the details, and what macOS does with
a downloaded binary that is not notarised.

The onboarding asks for the agent harness yardr starts by default (each herdr
kind, marked installed or not), the mayor's name (the always-on resident that
coordinates the yard), the directory its shell lives in, your name, and how
agents should work with you: "Guide me", "Show me" (the default), "Work with
me" or "Just report", with a line of anything they should know about you.
Every agent reads that answer when its session starts
([configuration.md](docs/configuration.md#how-agents-work-with-you)). It
installs generic role prompts in `~/.yardr/roles/` (mayor, builder, merger,
reviewer, naming you as the one who decides), stores the harness as the
yard's `default_kind` and your name as its `operator`, and adds the mayor.
Before its first page it says so when git or herdr is not on PATH, what
each is needed for and where to get it; you stop there or go on.

Its last question is "Start the yard now?" (yes by default). Yes starts the
daemon in the background, logging to `~/.yardr/daemon.log`, and waits up to
two minutes, counting, for the mayor: its session running and its first
output in its pane. Then it prints where the mayor is, as herdr names it
(agent, workspace, tab, pane), how to get there (`herdr agent focus <agent>`
in herdr, `herdr agent attach <agent>` from any other terminal), and asks how
to go on with the rest of the setup: "Talk to the mayor", which brings its
pane to the front in herdr, or "Keep going here", a short run of forms. Before
that it asks two things, both no by default: whether to start the daemon at
login (`yardr daemon install`: a launchd agent on macOS, a systemd user unit
on Linux), and whether to start the web view now, and at login
(`yardr web start`, `yardr web install`). When the
mayor does not come up it says what is known instead (herdr or the harness
not on PATH, the last failed start, the last lines of the mayor's pane, where
the daemon logs) and how to go on. No leaves the start to you: `yardr daemon`
in a pane of its own, where you watch what it does, or `yardr init --start`,
which starts and waits the same way without a terminal and, on a running
yard, tells you where the mayor is.

A setup that failed halfway is finished by the next run: `yardr` in a
terminal, or `yardr init`, says what the earlier run left and adds what is
missing, and on a yard that is set up and does not run it starts the daemon
and waits for the mayor again. To remove a yard and start over, see
[docs/configuration.md](docs/configuration.md#starting-over).

The mayor's first job is the setup, as a conversation with you: it asks for
the repository and its base branch, registers the rig, seeds it with
`yardr rig init` (starter roles, gate, flow and stage files under `.yardr/`, which you
commit on the base branch), creates the groups and routes, and offers a first
bead to try the pipeline. It asks before anything outward, such as a commit.

"Keep going here" is the same setup as forms, and `yardr setup` runs them at
any later time ([docs/setup.md](docs/setup.md)). It takes the first step that
is not done, whoever did the ones before, and each page ends in the command
you would type, shown before it runs:

1. the repository and its base branch (`yardr rig add`), then `yardr rig init`,
   which shows what it wrote and the gate it guessed, and asks before it
   commits `.yardr/` on the base branch;
2. who builds and who reviews: the form of `yardr group add`, starting at a
   group on the default harness with the role's prompt;
3. how approved beads land: a refinery (yardr's `merge-local`, which it
   installs in `~/.yardr/scripts/`) or people who merge by hand;
4. the routes for `new`, `review` and `approved`, which follow from the
   groups: shown, confirmed;
5. a first small bead, created and advanced to `new`, and how to watch it.

After each page it asks again: keep going, talk to the mayor, or stop here.
The mayor and the forms read one list of steps, the one `yardr doctor`
prints, so you can change over after any step. A cancelled page has run no
command.

`yardr doctor` says at any time what the yard has and what it lacks: git,
herdr and each harness in use (on PATH, and answering its version flag),
whether the server, the daemon and the mayor run, and the setup steps, each
done or todo, with the command for the next one. It is read-only, runs on a
home with no yard too, and exits 1 when something the yard needs is missing
([docs/doctor.md](docs/doctor.md)). The mayor reads it to know where the
setup stands.

A home counts as set up once its store has `default_kind`; a store the server
created on some earlier command, with nothing in it, still onboards. Without a
terminal, a command on a home with no yard says so and exits 2. To try the
onboarding without touching your yard, give it a home of its own; keep it
short, since the server's unix socket lives in it (a home too long for one is
refused):

```sh
yardr --home /tmp/y1                          # the form
yardr --home /tmp/y1 init --harness claude    # the scripted path; add --start to start the yard too
```

`--home <dir>` goes on every command for that yard (`yardr --home /tmp/y1
bead list`), and what yardr prints for it carries the flag. Several yards on
one machine are several homes
([docs/configuration.md](docs/configuration.md#yardrs-own-state)).

Scripts skip the forms and the mayor's conversation (`yardr init --help`):

```sh
yardr init --harness claude [--mayor mayor] [--dir ~/projects] [--model opus] [--operator Jo] [--style show] [--about "..."]
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

yardr daemon &                          # routes and supervises; starts the mayor (or: yardr init --start)
yardr watch                             # live event stream

yardr bead create "Add login page" --rig webapp -t feature -b "Use the OAuth flow"
# it starts in the flow's first stage, backlog; the mayor shapes it and advances it to new
```

The yardr server starts on first use, in the background, and owns
`~/.yardr/yard.db` (`yardr server status|stop`; `--home <dir>`, or `YARDR_HOME`, moves everything).
Back up with `yardr store backup <path>`, which copies it while the server
keeps serving. To upgrade, `go install` again, or put a newer release's
binary where the old one is: the server and daemon replace themselves with
the new build ([details](docs/configuration.md#upgrading)).

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
- **[Users](docs/users.md):** `yardr user`, people with an inbox of their own: residents
  mail a user, the user reads it with `yardr inbox` and a whole exchange with
  `yardr mail --with mayor`.
- **[Chat](docs/chat.md):** `yardr chat`, the residents and your conversation with each
  in one terminal view: what each is doing, where what you sent stands, a line to type.
- **[Subscriptions](docs/subscriptions.md):** what reaches a resident's inbox:
  hook sources, yardr's own bead events (`--done`, `yardr bead watch`), groups.
- **[Links](docs/links.md):** `yardr link`, the outside issue (Linear, GitHub) a bead
  tracks or relates to, and the hook's map that moves it when the bead changes stage.
- **[Peers](docs/peers.md):** `yardr peer`, linking two yards by their keys: signed
  messages both ways, pushed or pulled, proved with a ping, and mail between
  their residents (`yardr mail mayor@north "…"`).
- **[BDP](docs/bdp.md):** serving the yard over the Beads Protocol.
- **[Web view](docs/web.md):** `yardr web serve`, the yard in a browser, read-only.

## Layout

```
internal/store      the bbolt store: records, indexes, revisions, event log and push hub
internal/dispatch   routing, claims, liveness, teardown (the only control loop)
internal/runner     manual, exec, herdr
internal/bdp        the BDP Read server (protocol rules, selector, paging)
internal/web        the read-only web view (pages as HTML or JSON, embedded assets)
internal/rpc        the store API over the server's unix socket
internal/server     the store-owning server process; started on first use
internal/version    build revisions; notices a new binary installed over a running one
internal/cli        cobra commands
contrib/refinery    merge-local, a reference refinery script for an exec group
contrib/isolated-yard  a throwaway yard and herdr server for tests that start agents (see its README)
```

`go test ./...` runs everything against throwaway store files;
`YARDR_TEST_RPC=1 go test ./...` runs the same suites through the unix-socket
client, so the wire is tested by the same assertions.
