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

- **Inbox:** mail, plus what its subscriptions deliver. Read with `yardr
  inbox`, mark handled with `yardr inbox --ack`; the cursor is stored, so
  nothing is lost or repeated across restarts.
- **Subscriptions:** `--on` kinds (default: things that went wrong or need a
  decision: `gave_up`, `session_blocked`, `session_stalled`, `session_model_mismatch`, `unrouted`, `held`, …; `yardr
  resident add --help` lists them), webhook deliveries
  (`yardr subscribe <name> linear --scope involved`), and yardr's own bead
  events (`yardr subscribe <name> yardr --done`, `yardr bead watch <id>`).
  Each delivery records why it reached the resident, and `yardr inbox` shows
  it. See [subscriptions.md](subscriptions.md).
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
  --on gave_up,session_blocked,unrouted,no_runner,workspace_kept,resident_gave_up,runtime_down,session_idle,session_aged,session_stalled,session_model_mismatch,held
```

Its journal, inbox position and runtime state are kept across the replace.

## Outbound credentials

What a resident uses to act outside the yard (the mayor's Linear developer
token, a GitHub token) goes in an env file of its own, never in
the store: BDP serves the store, backups copy it and every builder can read
it. That is the one rule. The examples keep such files in the yard's home
(`YARDR_HOME`, `~/.yardr` unless set), next to everything else of the yard:
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

A resident answers what reached its inbox where it came from, as itself:

```sh
yardr inbox                                   #   4211 10-03 09:14  hook (mentioned) linear comment create by Jane on "Retries": @mayor can you look?
yardr thread show 4211                        # the whole thread, read from Linear: ids, parents, authors, times, bodies
yardr reply 4211 "Filed as yard-k3f9; I'll post here when it lands."
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

What to say, and whether to, is the resident's role: see the generic
[roles/mayor.md](../roles/mayor.md) that `yardr init` installs. Setting a source up end to end is in
[subscriptions.md](subscriptions.md#setting-up-linear).
