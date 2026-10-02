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

yardr was called yard. For one release it still reads each `YARDR_*` variable
under its old name (the R dropped) when the new one is unset, and sets both
for the agents and commands it starts. If `~/.yardr` does not exist but
`~/.yard` does, it keeps using `~/.yard` (the server log says so); move it
once no server, agent or worktree uses it.

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

## Flows: a rig's lifecycle

Routes say who works a stage; a **flow** says where a bead may go next. A rig
without a flow lets a bead move to any stage, exactly as before. A flow file
has one rule per line:

```
# flows/yard.flow
backlog -> new
new -> review | backlog
review -> approved | new
approved -> merged | merge | new
merge -> approved
decide -> backlog
merged terminal
```

```sh
yardr flow set yard -f flows/yard.flow                  # the rig's flow (replaces)
yardr flow set yard --type decision -f flows/decision.flow  # wins for that type
yardr flow show yard                                    # stage, group, open beads, next
yardr flow rm yard [--type decision]
```

With a flow in place:

- `yardr bead advance` refuses a move the flow does not allow and lists the
  allowed next stages (`allowed next: approved, new`).
- Entering a **terminal** stage closes the bead in the same write, with the
  stage as close reason (`closed: merged`).
- `yardr bead create -s <stage>` must name a non-terminal stage of the flow
  (the default `new` too).
- A bead in a stage the flow has no way out of (a stage the flow does not
  know, or a terminal stage after `yardr bead reopen`) may move to any stage
  of the flow, so it is never stuck.
- `yardr prime --bead` prints the allowed next stages; the store API exposes
  them as `NextStages`.

A type-specific flow governs that type's beads in the rig; every other type
follows the rig's flow. Beads without a rig, journals and memories are never
governed. `yardr flow show` warns about non-terminal stages no route sends
anywhere: beads there wait for a human.

## Groups, interactively

`yardr group add` with no name opens a form: pick an existing group to edit or
create one, choose the runner, and for herdr the agent (Claude Code, Codex,
Grok, Kimi, opencode, pi, or any other herdr agent), a model read live from
that agent (press `/` to search long lists), and its permission flags. With a
name, `yardr group add <name> --runner … --set …` stays scriptable.

## Review: beads waiting on you

`yardr review` walks the open beads in stages routed to manual groups (only
groups that list you as a member, if they list members), longest untouched
first. Each bead shows its stage, description, last notes and
`git diff --stat <base>...yard/<bead>` from the rig. Pick where it goes next:
the stages its flow allows (a stage earlier in the flow sends it back and
asks for a note; a terminal stage closes it), or without a flow any stage a
route uses. "Open diff" pages the full diff through `$PAGER`; Skip leaves it.

```sh
yardr review                    # the form
yardr review -g decisions       # only some groups' beads (backlog is manual too)
yardr review list               # the same queue for scripts (--json for actions)
yardr bead advance <id> --to <stage> [--note <text>]
```

## Residents: always-on sessions (a mayor)

A resident is an agent session that is not tied to a bead. The daemon keeps it
running in its directory, nudges it when its inbox has something new and it is
idle, and rotates it through handoffs instead of letting its context degrade.

```sh
yardr resident add                     # interactive: agent, model, permissions, dir, inbox, rotation
yardr resident add mayor --set kind=claude --set 'args=["--permission-mode","auto"]' \
  --set prompt=roles/mayor.md --dir ~/projects --max-age 6h
yardr mail mayor "Deploys are frozen until Friday"
yardr resident show mayor              # state, unread inbox, recent journal
```

- **Inbox:** events of the kinds in `--on` (default: things that went wrong or
  need a decision: `gave_up`, `session_blocked`, `unrouted`, …) plus mail.
  Read with `yardr inbox`, mark handled with `yardr inbox --ack`; the cursor is
  stored, so nothing is lost or repeated across restarts.
- **Hook subscriptions:** webhook deliveries (`yardr hook`) reach a resident only
  through `yardr subscribe <name> --source linear --scope all|mentions|involved
  [--where team=ENG ...]`. `yardr resident identity <name> --source linear --id
  <user id> --key <profile key>` says who it is there, so it can be mentioned
  or assigned, and never hears of its own actions. Once it acts in a thread (a
  Linear issue), it participates there, and `involved` also delivers what
  others do in it; `yardr thread follow|unfollow <name> --source linear <id>`
  and `yardr thread list <name>` manage that by hand. Matching happens when a
  delivery arrives; each delivery records why (all, mentioned, assigned,
  participant) and `yardr inbox` shows it.
- **Journal:** one bead per resident (`<name>-journal`). `yardr resident journal`
  keeps decisions (≤4 KB); `yardr resident handoff` ends the session (≤16 KB) and
  the next session starts from it. At `--max-age` the resident is asked for a
  handoff when idle; after `--handoff-grace` yardr restarts it regardless.
- **Prime:** a new session reads `yardr prime --resident <name>`: its role, the
  latest handoff, journal entries since, its inbox and its memories' titles,
  bounded in size. If the last session ended without a handoff, it gets a
  summary of what happened since instead.
- **Memories:** `yardr remember "title" -b … [--expires 7d] [--scope all]` and
  `yardr recall [query]`: lasting knowledge as beads, listed by title in primes,
  retired automatically when they expire.

Agents are told to run yardr by absolute path: a shell may find an older yardr
first on its PATH, and the prime warns about it.

## Runners

| runner | starts | config |
|---|---|---|
| `manual` | nothing; the bead waits on the group | — |
| `exec` | `sh -c cmd` in the rig, with `YARDR_BEAD`, `YARDR_SESSION`, `YARDR_STAGE`, `YARDR_GROUP`, `YARDR_ACTOR` | `cmd`, `dir`, `log` |
| `herdr` | a herdr worktree workspace on branch `yard/<bead>` at `~/.yardr/worktrees/<rig>/<bead>` (reopened for later stages, one tab per stage), an agent in it, prompted with a brief in `<worktree>/.yard/` | `kind`, `prompt`, `args`, `keep`, `worktrees` |

A session ends when the bead is advanced, closed or released. If the runner
reports the work gone (process exited, herdr agent gone) the session is marked
`died` and the bead is routed again after a backoff; after 3 failures in one
stage the bead is held (`yardr bead unhold` grants fresh attempts).

### herdr setup

- **Folder trust.** Claude Code asks to trust each new project, and every bead's
  worktree is its own git root, so trusting a parent folder does not cover them:
  each builder stops at the trust dialog until someone answers it in its pane.
  yardr keeps such a session (`session_blocked` in `yardr watch`) and sends its
  first prompt once it is answered. Starting without the dialog is yard-16.
- **Decide what agents may run unattended.** Anything outside `args`'
  permissions blocks the agent on a prompt. Pass what the role needs, e.g.
  `--set 'args=["--permission-mode","acceptEdits","--allowedTools","Bash(yardr:*),Bash(git:*)"]'`;
  compound shell commands still prompt unless allowed or auto mode is on.
- **Blocked agents are reported.** When herdr sees an agent waiting on a
  prompt, yardr writes a `session_blocked` event (visible in `yardr watch`);
  answer it in herdr and the session carries on.
- **Worktrees are cleaned up when the bead closes**, not when a stage ends,
  so later stages reuse them. A worktree with uncommitted or untracked changes
  is kept (a `workspace_kept` event says why) and `yard/<bead>` branches are
  never deleted. Set `--set cleanup=false` on a group to keep its worktrees.
  Reopening a bead checks its branch out again.
- The daemon subscribes to herdr's event stream, so an agent that exits is
  noticed within a second; the reconcile timer remains the fallback.

## BDP (Beads Protocol)

`yardr bdp serve` serves the yard over the [BDP](https://github.com/gastownhall/bdp)
Read profile, so any BDP client reads yardr's beads and dependency links over
HTTP:

```sh
yardr bdp serve --addr :8765 --scope https://beads.example/team/
```

- Beads are `<scope>beads/<handle>`; dependencies are Links whose source is
  the waiting bead and target its blocker, as in bd. The handle is the BDP
  id because it never changes and is unique within a yard (BDP ids are
  scoped), and it is what bd and humans use; the UUID is the `uuid`
  property. Listings follow BDP's canonical id order, not creation order.
- Bead properties are bd's JSON row (dependencies, counts, `created_by`, …)
  plus yardr's own fields; a held bead reads as `deferred`. Types use BDP's
  reference vocabulary (`https://work.example/types/task`, `…/blocks`) where
  names match, else `<scope>types/<name>`.
- Every real change mints an opaque revision (a no-op mints none), served
  as the ETag; listings page over one consistent snapshot.
- BDP's own conformance suite passes against it (31/31 runnable Read
  scenarios), and BDP's `bdp bd ready` client computes the same ready set
  from yardr as `yardr ready`. See [conformance/bdp](conformance/bdp).

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
