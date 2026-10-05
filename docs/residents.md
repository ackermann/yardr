# Residents: always-on sessions (a mayor)

A resident is an agent session that is not tied to a bead. The daemon keeps it
running in its directory, nudges it when its inbox has something new and it is
idle, and rotates it through handoffs instead of letting its context degrade.

```sh
yardr resident add                     # interactive: agent, model, permissions, dir, inbox, rotation
yardr resident add mayor --set kind=claude --set 'args=["--permission-mode","auto"]' \
  --set prompt=$HOME/projects/webapp/.yardr/roles/mayor.md --dir ~/projects --max-age 6h
yardr mail mayor "Deploys are frozen until Friday"
yardr resident show mayor              # state, unread inbox, recent journal
```

A resident has no rig, so its `prompt` is an absolute path (a relative one is
read from its `--dir`): a rig's own `.yardr/roles/mayor.md`, or the generic
`~/.yardr/roles/mayor.md` that `yardr init` installs and points the mayor at.

In herdr a resident's agent is `<yard>-<name>` (`yardr-mayor` for a yard in
`~/.yardr`, `y1-mayor` for one in `/tmp/y1`) and its workspace is labelled
`<yard> <name>`: herdr lists every yard on the machine's agents together and
refuses a second of one name, so two yards' mayors cannot both be `mayor`.
The yard is its `name` setting, else the home's directory name
([configuration.md](configuration.md#settings)).

**One session per resident.** Before it starts a resident, yardr asks herdr
for its agents. If one of them already is that resident, nothing is started:
no workspace, no second agent. An agent is the resident when it is not the
session yardr itself recorded and either its name is `<yard>-<name>`, or its
name is the resident's bare name (`mayor`) and it runs in the resident's
directory, which is what a pane you started by hand before defining the
resident looks like. Two sessions under one name would each act as the
resident without knowing of the other: one gets the nudges, you talk to the
other.

The resident stays `stopped` and a `resident_blocked` event on its journal
names the agent and its pane, once for as long as it is the same agent:

```sh
yardr resident list                  # mayor  stopped  …
yardr events --bead mayor-journal    # … resident_blocked yard  agent=mayor pane=w1:p1 reason=agent "mayor" in pane w1:p1 already is this resident; end it and the resident starts
```

To clear it, end that agent: close its pane in herdr (`herdr pane close
w1:p1`), or rename it (`herdr agent rename`) if you want to keep it as
something else. yardr looks again on every pass and starts the resident once
the agent is gone; this is not counted as a failed start, so it never gives
up. yardr does not adopt the pane it found and does not end it for you.

**A start that fails.** The daemon waits before it tries again, longer each
time (30s, 1m, 2m, 4m), and gives up after five failed starts in a row: the
resident is `failed`, a `resident_gave_up` event is on its journal (in every
default `--on`, so the mayor hears of another resident's) and nothing starts
it until `yardr resident start <name>`, which also clears the count. Each
failed start is a `resident_start_failed` event with the reason.

A session that is gone within 60 seconds of its start is a failed start too,
with the reason `the session ended 4s after its start (…)`: a harness that
exits at once (a broken install, a bad flag in `args`, a login that expired)
would otherwise be started again on every pass, a new workspace every few
seconds. Run the resident's command by hand in its directory to see what it
says. A session that lives longer, still running or ended since, clears the
count. An ending that was asked for never counts, however soon: a handoff, a
rotation, `yardr resident restart` or `stop`.

```sh
yardr resident list          # mayor  stopped (2 failed starts, next try in 48s)  …
yardr resident show mayor    # 2 failed starts, next try in 48s, the last with: <reason>
yardr doctor                 # FAULT mayor mayor is not running: the daemon gave up after 5 tries, the last with: <reason>
```

When the resident that gave up is the mayor itself, nobody is told: `yardr
doctor` and bare `yardr` say it.

- **Inbox:** mail, plus what its subscriptions deliver. Read with `yardr
  inbox`, mark handled with `yardr inbox --ack`; the cursor is stored, so
  nothing is lost or repeated across restarts.
- **Subscriptions:** `--on` kinds (default: things that went wrong or need a
  decision: `gave_up`, `session_blocked`, `session_stalled`, `session_model_mismatch`, `unrouted`, `stranded`, `held`, …; `yardr
  resident add --help` lists them), webhook deliveries
  (`yardr subscribe <name> linear --scope involved`), and yardr's own bead
  events (`yardr subscribe <name> yardr --done`, `yardr bead watch <id>`).
  Each delivery records why it reached the resident, and `yardr inbox` shows
  it. See [subscriptions.md](subscriptions.md).
- **Journal:** one bead per resident (`<name>-journal`). `yardr resident journal`
  keeps decisions (≤4 KB); `yardr resident handoff` ends the session (≤16 KB) and
  the next session starts from it. At `--max-age` the resident is asked for a
  handoff when idle; after `--handoff-grace` yardr restarts it regardless.
- **Prime:** a new session reads `yardr prime --resident <name>`: its role, how
  the operator wants agents to work with them when the yard says
  ([configuration.md](configuration.md#how-agents-work-with-you)), the
  latest handoff, journal entries since, its inbox and its memories' titles,
  bounded in size. If the last session ended without a handoff, it gets a
  summary of what happened since instead.
- **Memories:** `yardr remember "title" -b … [--expires 7d] [--scope all]` and
  `yardr recall [query]`: lasting knowledge as beads, listed by title in primes,
  retired automatically when they expire.

Agents are told to run yardr by absolute path: a shell may find an older yardr
first on its PATH, and the prime warns about it.

## Findings from builders and reviewers

What an agent finds outside its bead reaches the mayor on two channels, as
the builder and reviewer roles say ("Something outside your bead"). A finding
that does not block the agent (a bug in another package, a stale doc, a flaky
test) arrives as a bead in stage `backlog`, for the mayor to shape or close
like any other. One that blocks it (a red gate on the base, a missing tool)
arrives as that bead plus mail from the agent, `<bead id>: <one line>`, and
the agent's own bead is held with a note naming the blocker when it cannot
finish; the mayor orders the two with `yardr dep add <blocker> <bead>` and
unholds. The roles mail the yard's `mayor` setting ([configuration.md](configuration.md#settings)),
so a mayor added under another name is still the one addressed.

A hold reaches the mayor because `held` is among a new resident's default
`--on` kinds. A resident made before `held` joined them (or before any kind
the default gained since) does not hear it until its list is replaced:
`yardr resident add` with the name writes the whole definition, so repeat
its other flags and give `--on` the full list, e.g.

```sh
yardr resident show mayor --json          # its config, dir, max_age and on today
yardr resident add mayor --set kind=claude --set prompt=$HOME/.yardr/roles/mayor.md \
  --dir ~/projects --max-age 6h \
  --on gave_up,session_blocked,unrouted,stranded,no_runner,workspace_kept,resident_gave_up,runtime_down,session_idle,session_aged,session_stalled,session_model_mismatch,held
```

Its journal, inbox position and runtime state are kept across the replace.

## Outbound credentials

What a resident uses to act outside the yard (the mayor's Linear developer
token, a GitHub token) goes in an env file of its own, never in
the store: BDP serves the store, backups copy it and every builder can read
it. That is the one rule. The examples keep such files in the yard's home
(`--home` or `YARDR_HOME`, `~/.yardr` unless set), next to everything else of the yard:
a file beside `yard.db` is not in it, and neither BDP, `yardr store backup`
nor pruning touches anything in the home but the store file. Any other path
works as well. Inbound is separate: webhooks arrive through one shared hook and its
secret file (`yardr hook add linear --source linear --secret-file …`); outbound is per
resident.

```sh
mkdir -p ~/.yardr/residents
cat > ~/.yardr/residents/mayor.env <<'END'
# Linear, as the mayor: its app's developer token (or a personal API key)
LINEAR_API_KEY=lin_oauth_…
# GitHub, as the mayor (else `gh auth token` in its pane is used)
GITHUB_TOKEN=github_pat_…
END
chmod 600 ~/.yardr/residents/mayor.env
yardr resident add mayor --set env_file=~/.yardr/residents/mayor.env …
yardr resident show mayor     # env_file …/mayor.env (LINEAR_API_KEY)
```

- Lines are `KEY=VALUE`; blank lines and `#` comments are skipped. Nothing is
  expanded: the value is the rest of the line as written. `~` in the
  `env_file` path is the daemon user's home; a variable is not expanded
  there, so a yard with its own `YARDR_HOME` writes the path out.
- Each entry joins the pane environment next to `YARDR_RESIDENT`,
  `YARDR_ACTOR` and `YARDR_HOME`. A key yardr sets itself is ignored, so a
  credentials file cannot change who the agent is.
- The environment reaches herdr over its API socket, in the request that
  opens the pane, never as arguments of a `herdr` command: a command line
  shows in `ps` to every user of the machine, the socket only to its owner.
  The same goes for a group's `env_file` and a bead agent's tab.
- The file must be a regular file that only its owner can read or write
  (mode `0600` or stricter). Missing, not a regular file or open to others,
  the resident does not start: the start fails with the reason (backed off
  as any failed start) and a `resident_blocked` event in its journal says
  so, once per reason.
- The file is read at each start, so a new token takes effect on the next
  restart (`yardr resident restart mayor`). Values are never logged, put on
  the session's handle or shown; `yardr resident show` lists the key names.

## Speaking in sources

A resident answers what reached its inbox where it came from, as itself
(and may [open a thread](#opening-a-thread) when the operator allowed it):

```sh
yardr inbox                                   #   4211 10-03 09:14  hook (mentioned) linear comment create by Jane on "Retries": @mayor can you look?
yardr thread show 4211                        # the whole thread, read from Linear: ids, parents, authors, times, bodies
yardr reply 4211 "Filed as yardr-k3f9; I'll post here when it lands."
yardr reply 4211 --to <message id> - < answer.md   # under another conversation of the thread
```

`<seq>` is the number an inbox line starts with; only `hook` events from a
source with a replier (`linear`, `github`) can be answered. In Linear the
reply goes under the conversation of the delivered comment (Linear threads
one level deep, so under its root), or a new comment for an issue
delivery; in GitHub it is a comment on the issue or pull request.

yardr only types. The call is made by the CLI in the resident's pane with
the credential its `env_file` put there (`LINEAR_API_KEY`, `GITHUB_TOKEN`,
else `gh auth token`); the server is asked only for the event, and never
sees, stores or sends a credential. After posting, the resident takes part
in the thread at once (`yardr thread list`, how `replied`) and a `replied`
event (source, thread, url, the comment's id as `message`, the delivery's
`seq`) lands on its journal; the webhook's echo of the reply is its own
action and does not reach its inbox.

**Who may speak as a resident.** `yardr reply --resident <name>` is taken
from the resident's own session (the pane yardr started it in names it in
`YARDR_RESIDENT` and `YARDR_ACTOR`) or from the operator (the yard's
`operator` setting, else the user running the command, which is what a
shell with no `YARDR_ACTOR` acts as). A bead's agent, or another resident,
is refused: its credential, or the operator's `gh` login, would otherwise
speak in the resident's name. Every agent still runs as one OS user, so
this is a line, not a wall; it moves when yardr hosts more than one person.

**A reply is posted once.** Before anything is sent, the reply is on record
in the store with a key chosen for it (a UUID), under the delivery, the
conversation and the text. The key goes with the post: in Linear as the
comment's own id, which Linear refuses twice; on GitHub as an HTML comment
at the end of the body, which renders as nothing. Run the same `yardr
reply` again, after a timeout or a crash, and yardr answers from the
record when the post is known to have landed, else asks the source for the
comment that key made and records it, and only posts when there is none.
Another text, or another `--to`, is another reply. A call to a source is
bounded at two minutes, well past a slow day at Linear, so a post that
landed slowly is not reported as failed and made again.

### Opening a thread

A resident can also speak first: open a thread of its own, when its
definition allows it.

```sh
yardr thread open linear --in YRD "Web view: epic" -b - < epic.md      # an issue in the team (key or name)
yardr thread open github --in acme/webapp "Retries drop the last attempt" -b "Seen in yardr-k3f9."
```

The source is `linear` or `github`; `--in` is the Linear team (its key, or
its name when one team has it) or the GitHub repository (`owner/repo`); `-b`
is the body (`-` reads stdin). Text only, as a reply. It prints the URL of
the issue it made.

The rules are those of `yardr reply`. The CLI in the resident's pane makes
the call with the credential from its `env_file`, so the issue's author is
the resident's own user in the source; the server keeps the record and
never sees the credential. Only the resident's own session or the operator
may speak as it (`--resident`).

**Who may open threads.** No resident, unless its definition says so:

```sh
yardr resident add mayor --set open_threads=linear,github …   # the rest of the definition, as add replaces it
yardr resident show mayor     # may open threads in github, linear
```

`open_threads` names the sources, comma-separated (or as a JSON list).
Without it `yardr thread open` refuses and says what to set. Only the
operator (the yard's `operator` setting, else the user running the command)
may add a source to it: `yardr resident add` from a resident's session, or a
bead agent's, is refused when it would let a resident (itself or another)
open threads where it could not before. Saving a definition that keeps the
setting as it is, or narrows it, is anyone's, like the rest of a
definition; left out, the setting is gone. The store refuses the opening
itself for a resident that is not allowed, whoever asks; who may change the
setting is checked by the CLI, the same line, not wall, as who may speak.

A pack can ask for it: `open_threads: [linear]` under a resident in
`pack.yaml`. That sets nothing by itself. `yardr pack apply` asks the
operator, `Let mayor open threads in linear?` with no as the default (or
takes `--open-threads mayor=linear`), only for a source the yard has a hook
and the resident a credential for, and only when the operator is the one
applying. A yes adds the source to the setting, keeps what is there, and is
logged as `open_threads_granted`
([packs.md](packs.md#yardr-pack-apply)).

**A thread is opened once.** Like a reply, the thread is on record with a
key (a UUID) before anything is sent, under the source, the place, the
title and the body. In Linear the key is the issue's own id, which Linear
refuses twice; on GitHub it is an HTML comment at the end of the body. Run
the same command again after a timeout and yardr answers from the record,
else looks for the issue the key made (Linear by id; GitHub among the
repository's issues touched since the first attempt) and only opens one
when there is none. Another title, body or place is another thread.

**The thread is the resident's.** It takes part at once (`yardr thread
list`, how `opened`), before the source's webhook has said anything, and a
`thread_opened` event (source, `in`, thread, title, url) lands on its
journal. With an `involved` subscription what others say there reaches its
inbox as `participant`, no mention needed, and `yardr reply <seq>` answers
it. The thread's id is the one deliveries carry: the issue's id in Linear,
`<owner>/<repo>#<number>` in GitHub.

### Setting a thread's status

A resident can move the issue a thread is to started, review or done, when
its definition allows it.

```sh
yardr thread state 4211 started                           # the thread of hook delivery 4211, as yardr reply names it
yardr thread state 539068e2-ae88-4d09-bd75-22eb4a59612f review   # a thread it takes part in, by id
yardr thread state --source github acme/webapp#12 done    # by source and id, as yardr thread follow names it
```

The three words are yardr's; each source has its own statuses:

| | `started` | `review` | `done` |
|---|---|---|---|
| `linear` | the team's first status of type `started` | its first status of type `started` whose name contains "review", in any case; a team without one gets `started`, and the output says so | its first status of type `completed` |
| `github` | reopens a closed issue; an open one is left as it is | refused: an issue is open or closed, so the words it has are `started` and `done` | closes the issue as completed |

First means lowest position, the order the team gives its statuses. So in
Linear `started` on an issue in review moves it back to the first started
status. On GitHub a closed issue is done whatever it was closed as (the
reason someone gave is not rewritten), and a pull request is refused: the
same call would close it unmerged.

**Nothing to do.** The issue is read first. One already in the status asked
for gets no call that changes it, the output says it was there
(`… was already Done; nothing set`), and nothing is logged.

**Who may set a status.** No resident, unless its definition says so:

```sh
yardr resident add mayor --set set_state=linear,github …   # the rest of the definition, as add replaces it
yardr resident show mayor     # may set the status of threads in github, linear
```

`set_state` is its own setting with the rules of `open_threads` above:
it names the sources, only the operator may add a source to it (`yardr
resident add` from a resident's session or a bead agent's is refused when it
would allow more than before), keeping or narrowing it is anyone's, and
left out it is gone. Being allowed to open threads is no leave to set their
status, nor the other way. The store refuses the change itself for a
resident that is not allowed, whoever asks. A pack cannot ask for it.

**A hook acting as the resident.** `set_state` is also what lets a hook
with a map move a tracked issue as the resident when a bead changes stage
([links.md](links.md#a-beads-stage-moves-the-issue)). The daemon makes that
call, with the credential in the resident's `env_file` and nothing else: its
own environment and `gh` login are the operator's. There the issue is only
moved forward, and the resident need not take part in the thread.

**Where.** Only in a thread the resident takes part in (`yardr thread
list`): one it acted in, answered in, opened, or follows. Anywhere else the
command refuses and names `yardr thread follow`, which makes it a
participant. Only the resident's own session or the operator may speak as
it (`--resident`), and the call is made with the resident's own credential,
as a reply is.

**What is recorded.** A `thread_state` event on the resident's journal:
the resident, the source, the thread, `state` (the word asked), `status`
(the source's name of the status set), `was`, `instead` (`started`, when
review had no status of its own) and, as the event's actor, who ran the
command. `--json` prints the same fields, with `actor` and `changed`.

**No echo.** The source tells of the change by webhook, and that delivery
does not reach the resident's own inbox: its actor is the resident, by its
identity in the source (`yardr resident identity`), and a resident never
hears of what it did. Without an identity there yardr cannot tell the
delivery from someone else's, so the command warns when it changes
something for a resident that has none.

`YARDR_LINEAR_API` and `YARDR_GITHUB_API`, in the environment of the
command, send the outbound calls (replies and status changes too) to
another endpoint: a stand-in when trying yardr out, or a GitHub Enterprise
API (`https://<host>/api/v3`).

What to say, and whether to, is the resident's role: see the generic
[roles/mayor.md](../roles/mayor.md) that `yardr init` installs. Setting a source up end to end is in
[subscriptions.md](subscriptions.md#setting-up-linear).
