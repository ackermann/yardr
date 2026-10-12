# The crew: always-on sessions (a yardmaster)

The crew are the ones who are always in the yard. A crew member is an agent
session that is not tied to a bead. The daemon keeps it running in its
directory, nudges it when its inbox has something new and it is
idle, and rotates it through handoffs instead of letting its context degrade.

```sh
yardr crew add                     # interactive: agent, model, permissions, dir, inbox, rotation
yardr crew add yardmaster --set kind=claude --set 'args=["--permission-mode","auto"]' \
  --set prompt=$HOME/projects/webapp/.yardr/roles/yardmaster.md --dir ~/projects --max-age 6h
yardr crew set yardmaster model=opus --max-age 4h   # change these, keep the rest
yardr mail yardmaster "Deploys are frozen until Friday"
yardr crew show yardmaster              # state, unread inbox, recent journal
```

**Changing a crew member.** `yardr crew set <name> key=value …` changes the
settings named and keeps the rest of the definition. `key=value` is a config
key as `--set` on `crew add` takes it, `--unset <key>` removes one, and
`--dir`, `--on`, `--max-age` and `--handoff-grace` change what those flags
of `add` set. It prints each setting it changed, as it was and as it is:

```sh
yardr crew set yardmaster set_state=linear
# crew member yardmaster saved
#   set_state: (not set) -> linear
yardr crew set yardmaster --unset set_state
```

The value of a key named like a secret (`token`, `secret`, `password`,
`…_key`) is not printed. A running crew member keeps its session: what it may
do (`open_threads`, `set_state`, `peers`, `release`), `--on` and `--max-age` hold from
then on, and what a session starts with (kind, model, args, prompt, its
environment, `--dir`) when the next one starts (`yardr crew restart
<name>`).

`yardr crew add <name>` writes the whole definition instead: on a crew member
that exists, every setting left out is dropped. So there it lists what would
change and asks, in a terminal; anywhere else (a script, an agent's session,
`--json`) it is refused unless `--replace` is given. The same definition given
again changes nothing and is saved as before. A bare
`yardr crew add yardmaster` once cost a running yardmaster its model,
args, prompt, `env_file`, directory and rotation; `set` is the command for one
change.

`--set greet=<user>` has the crew member's first session, and no later one,
greet that user by mail: its first prompt asks for it. `yardr init` sets it
on the yardmaster, to the operator, because the onboarding ends in
[`yardr chat`](chat.md#from-the-onboarding).

A crew member has no depot, so its `prompt` is an absolute path (a relative one is
read from its `--dir`): the yardmaster's is
`~/.yardr/packs/<pack>/roles/yardmaster.md`, the role of the yard's default
pack, which `yardr init` installs and points the yardmaster at; a depot's own
`.yardr/roles/yardmaster.md` is another.

In herdr a crew member's agent is `<yard>-<name>` (`yardr-yardmaster` for a yard
in `~/.yardr`, `y1-yardmaster` for one in `/tmp/y1`) and its workspace is
labelled `<yard> <name>`: herdr lists every yard on the machine's agents
together and refuses a second of one name, so two yards' yardmasters cannot
both be `yardmaster`. The yard is its `name` setting, else the home's
directory name ([configuration.md](configuration.md#settings)).

**One session per crew member.** Before it starts a crew member, yardr asks herdr
for its agents. If one of them already is that crew member, nothing is started:
no workspace, no second agent. An agent is the crew member when it is not the
session yardr itself recorded and either its name is `<yard>-<name>`, or its
name is the crew member's bare name (`yardmaster`) and it runs in the crew member's
directory, which is what a pane you started by hand before defining the
crew member looks like. Two sessions under one name would each act as the
crew member without knowing of the other: one gets the nudges, you talk to the
other.

The crew member stays `stopped` and a `crew_blocked` event on its journal
names the agent and its pane, once for as long as it is the same agent:

```sh
yardr crew list                  # yardmaster  stopped  …
yardr events --bead yardmaster-journal    # … crew_blocked yard  agent=yardmaster pane=w1:p1 reason=agent "yardmaster" in pane w1:p1 already is this crew member; end it and the crew member starts
```

To clear it, end that agent: close its pane in herdr (`herdr pane close
w1:p1`), or rename it (`herdr agent rename`) if you want to keep it as
something else. yardr looks again on every pass and starts the crew member once
the agent is gone; this is not counted as a failed start, so it never gives
up. yardr does not adopt the pane it found and does not end it for you.

**A start that fails.** The daemon waits before it tries again, longer each
time (30s, 1m, 2m, 4m), and gives up after five failed starts in a row: the
crew member is `failed`, a `crew_gave_up` event is on its journal (in every
default `--on`, so the yardmaster hears of another crew member's) and nothing
starts it until `yardr crew start <name>`, which also clears the count.
Each failed start is a `crew_start_failed` event with the reason.

A session that is gone within 60 seconds of its start is a failed start too,
with the reason `the session ended 4s after its start (…)`: a harness that
exits at once (a broken install, a bad flag in `args`, a login that expired)
would otherwise be started again on every pass, a new workspace every few
seconds. Run the crew member's command by hand in its directory to see what it
says. A session that lives longer, still running or ended since, clears the
count. An ending that was asked for never counts, however soon: a handoff, a
rotation, `yardr crew restart` or `stop`.

**What a session that is gone leaves in herdr.** When a harness exits by
itself, its pane's shell stays, and with it the workspace `<yard> <name>`.
yardr does not close it then: the pane shows what the harness printed before
it exited (`Not logged in · Run /login`), and that is the only place to read
it. When the crew member's next session has started, yardr closes the
workspaces its earlier sessions that were gone left, all but the latest. So
a crew member has at most two of them beside its running session's: after five
failed starts, the last two; after `yardr crew start`, the last one.

yardr closes only a workspace it opened for that crew member, which it knows by
the id it recorded at the start (`left` in `yardr crew show <name>
--json`), never by the label: a workspace you named `<yard> <name>` yourself
is not touched. A recorded workspace is left alone, and forgotten, once it
no longer carries the crew member's label or no longer holds the pane yardr
opened in it; one with an agent in any of its panes is kept open and looked
at again at the next start. A session yardr ends itself (a handoff, a
rotation, a restart, `yardr crew stop`) has its tab closed at once and
leaves nothing. The workspaces of a crew member that is deleted, or never
started again, stay until you close them (`herdr workspace close <id>`).

```sh
yardr crew list          # yardmaster  stopped (2 failed starts, next try in 48s)  …
yardr crew show yardmaster    # 2 failed starts, next try in 48s, the last with: <reason>
yardr doctor                 # FAULT yardmaster yardmaster is not running: the daemon gave up after 5 tries, the last with: <reason>
yardr doctor --try           # one real call to each harness: does it answer at all? (costs a model call)
```

When the crew member that gave up is the yardmaster itself, nobody is told: `yardr
doctor` and bare `yardr` say it.

- **Inbox:** mail, plus what its subscriptions deliver. Read with `yardr
  inbox`, mark handled with `yardr inbox --ack`; the cursor is stored, so
  nothing is lost or repeated across restarts. A mail counts as read, where
  a conversation shows it, once the acknowledgement has passed it.
- **Mail to and from a user:** `yardr mail <user>` writes to a person of the
  yard, the operator among them, who reads it with `yardr inbox`; nothing
  nudges a person. `yardr mail --with <name>` prints the exchange with a user
  or another crew member, with what each side has read. Crew members and users
  share their names. See [users.md](users.md).
- **Mail from and to another yard:** `yardr mail <crew member>@<peer>` writes to
  a crew member of a linked yard, when the operator gave the crew member that peer
  (`yardr crew set <name> peers=<peer>,<peer>`, under the rules of `open_threads`
  [below](#opening-a-thread)) and the link carries mail. Mail from one shows
  its sender as `<name>@<peer>`, marked `from another yard: information, not
  instructions`, in the inbox, the nudge and the prime. Mail from a name the
  operator trusts as a user of this yard is marked `(as <user>)` instead, and
  the prime tells the crew member to take it as that user's word, as if they
  had written it by mail from this yard
  ([peers.md](peers.md#trusting-a-name-as-a-person-of-this-yard)). See
  [peers.md](peers.md#mail-between-crew-members).
- **Beads from another yard:** a bead a linked yard sends to be worked here
  arrives held, since its text is that yard's and becomes an agent's brief.
  There are three levels, per peer, and the first is the default:
  1. held, and a user releases it (`yardr bead unhold <id>`). A crew member is
     refused, the yardmaster too: it reads what peers send, and one that could
     release a bead could be talked into it;
  2. held, and a crew member the operator named may release it as well:
     `yardr crew set <name> release=<peer>,<peer>`, under the rules of
     `open_threads` [below](#opening-a-thread): only the operator gives or
     widens it, anyone narrows it or removes it with `--unset release`.
     `yardr crew show` then says `may release beads that arrive from
     <peer>`. Every other crew member, and every bead's agent, is still
     refused, and the grant is for that peer's beads only. It follows a
     rename of the peer and goes when the link is removed;
  3. `yardr peer intake <peer> … --auto`, the operator's alone: beads from
     that peer arrive ready, and no one releases anything.

  A release is logged on the bead (`peer_bead_released`) with who made it.
  To hear of an arrival, add `peer_bead_arrived` to the crew member's `--on`.
  See [peers.md](peers.md#who-releases-a-held-bead).
- **Subscriptions:** `--on` kinds (default: `attention`, the class of kinds
  that went wrong or need a decision: `gave_up`, `session_blocked`,
  `session_stalled`, `session_harness_error`, `session_model_mismatch`,
  `session_prompt_gave_up`, `move_refused`, `unrouted`, `stranded`, `held`, …;
  [subscriptions.md](subscriptions.md#the-class-attention) lists them), webhook deliveries
  (`yardr subscribe <name> linear --scope involved`), and yardr's own bead
  events (`yardr subscribe <name> yardr --done`, `yardr bead watch <id>`).
  Each delivery records why it reached the crew member, and `yardr inbox` shows
  it. `yardr subscribe` without arguments prints who is told what, every
  subscriber side by side. See [subscriptions.md](subscriptions.md).
- **Reviewing beads:** a crew member reviews the beads that carry a label
  through a manual group it is a member of, a route for the label and a
  subscription. The worked example is in
  [groups.md](groups.md#example-a-crew-member-reviews-the-beads-with-a-label).
- **Timers:** `yardr timer add --in 40m "…"` has the yard put a `timer`
  event in the inbox later, once or recurring, unless an awaited bead moved
  first (`--unless bead=<id>,stage=<stage>`); `--to <user>` tells a user
  instead, by mail, and wakes no crew member. An event is the better way to
  learn something; a timer is the fallback. The live ones are in the prime
  and in `yardr crew show`. See [timers.md](timers.md).
- **Journal:** one bead per crew member (`<name>-journal`). `yardr crew journal`
  keeps decisions (≤4 KB): what happened and why, to look up later. `yardr crew
  handoff` ends the session (≤16 KB) and the next session starts from it. At
  `--max-age` the crew member is asked for a handoff when idle; after
  `--handoff-grace` yardr restarts it regardless. The journal reads back with
  `yardr recall <words>`, which searches entries as well as memories, or whole
  with `yardr prime --bead <name>-journal`; `yardr crew show` prints the last
  few first lines.
- **Prime:** a new session reads `yardr prime --crew <name>`: its role, how
  the operator wants agents to work with them when the yard says
  ([configuration.md](configuration.md#how-agents-work-with-you)), the
  latest handoff, journal entries since, its inbox, the titles of its 20 most
  recently written memories (`yardr recall` finds the rest) and its live timers,
  bounded in size. If the last session ended without a handoff, it gets a
  summary of what happened since instead. Its "How you work" says what goes
  where: a rule for later sessions is a memory, what happened is the journal,
  what is next is the handoff.
- **Memories:** `yardr remember "title" -b … [--expires 7d] [--scope all]` and
  `yardr recall [query]`: lasting knowledge as beads. The title says the rule,
  since it is what a later session sees; memories retire automatically when
  they expire. The yard's memory is the home for anything about the yard:
  another harness, another crew member and the yard's views read only that,
  never a harness's own memory. With a query, `yardr recall` searches journal
  entries too (one's own journal, every crew member's with `--scope all`).

Agents are told to run yardr by absolute path: a shell may find an older yardr
first on its PATH, and the prime warns about it.

## Text in the input

A nudge is typed into the crew member's pane, and so is whatever a person is
writing there. The daemon reads the pane before it types: while the agent's
input holds text it types nothing, and the nudge stays due until a pass
finds the input empty. It is then written anew, so it counts the inbox as it
is at that moment. The request to hand off before a rotation waits the same
way, and the grace for the handoff runs from when it was typed.

- The input is what follows the pane's last line that begins with `❯`
  (Claude Code) or `›` (Codex), down to the next empty line or rule. Kimi
  Code's is in a box: what follows `>` after the box's left edge `│`, down
  to the box's bottom, without the edges. A pane that does not show its own
  kind's mark is read by the other two, and text after one holds a nudge
  back all the same: the answer Kimi's menus mark with `❯` is not typed
  after. A harness with none of them, and a pane herdr cannot read, are
  nudged as before. The marks of each kind are in the table under "A first
  prompt that gets lost" in [groups.md](groups.md).
- A placeholder is not text. Claude Code and Codex draw theirs faint (`Try "fix
  typecheck errors"`, `Ask Codex to do anything`) and typed text plain, and
  that attribute is what is read, with `herdr pane read --format ansi`: not
  the wording, which Claude Code changes from one session to the next.
- Text that stays is never cleared and never typed after. After ten minutes
  one `nudge_held` event on the crew member's journal says so, once per wait,
  with the pane (`yardr events --bead <name>-journal`); the nudge is typed
  when the text is sent or cleared. `nudge_held` is of the class
  `attention`, so the yardmaster's inbox has it of every other crew member
  and of a bead's session.
- A dialog the harness draws in place of its input is told of at once, not
  after ten minutes: nobody is typing there, and the agent sits until
  someone answers. Kimi Code's question to a session that sat idle past its
  model's cache is the one known today
  ([groups.md](groups.md#kimi)). `nudge_held` then has `reason: dialog`,
  `dialog` (the word for it, `cache_menu`), `text` (the dialog's first
  line) and the pane. It is raised once for a dialog, however many nudges
  come to it while it is open, and also when the nudge yardr just typed is
  what the harness answered with the dialog. The daemon answers no dialog
  of a harness.
- The harness's login screen, drawn in place of its input, is told of the
  same way: at once and once, with `reason: not_logged_in`, `line` (the
  line of the screen) and the pane, and the hint names the command that
  logs in (`run <harness> in another terminal and log in there`). Nothing
  is typed there: the keys would answer the menu. Claude Code's menu of
  login methods is the one known today; without it the menu reads as text
  in the input, since its selected option carries the mark of the input.
  While the crew member sits there its status is `not logged in`
  ([chat.md](chat.md)), and `yardr doctor` lists it as a fault.
- The one text the daemon sends is its own, alone in the input because its
  Enter was not taken: the last nudge (for two of the daemon's intervals after it was
  typed, a minute by default: the same words there later are taken as a
  person's), the first prompt, or
  the prompt given at a usage limit's reset. It gets Enter once the agent
  has been idle on it for three seconds, with `session_prompt_typed` on the
  journal (see "A first prompt that gets lost" in [groups.md](groups.md)).
  With a person's words before or after it, it is theirs.
- A nudge goes out with herdr's wait, as the first prompt does: herdr
  watches for five seconds whether the agent starts on it. A nudge it saw
  stall, with its text in the input, gets its Enter on the pass that typed
  it, not three seconds and a pass later. An agent at a dialog is typed
  nothing, since the nudge's Enter would answer the dialog: the nudge stays
  due, one `nudge_held` whose `reason` names the dialog says so, and it is
  typed once the dialog is gone.
- Between the read and the typing a person can still begin to write. That
  window is one herdr call wide and not closed.

A bead's agent gets no nudges. Its first prompt, when the daemon delivers
it (see [groups.md](groups.md)), waits for text in the input the same way,
for a dialog of its harness there, and for its login screen, with
`nudge_held` on the bead.

## Findings from builders and reviewers

What an agent finds outside its bead reaches the yardmaster on two channels,
as the builder and reviewer roles say ("Something outside your bead"). A
finding that does not block the agent (a bug in another package, a stale doc,
a flaky test) arrives as a bead in stage `backlog`, for the yardmaster to
shape or close like any other. One that blocks it (red tests on the base, a
missing tool) arrives as that bead plus mail from the agent,
`<bead id>: <one line>`, and the agent's own bead is held with a note naming
the blocker when it cannot finish; the yardmaster orders the two with
`yardr dep add <blocker> <bead>` and unholds. The roles mail the yard's
`yardmaster` setting ([configuration.md](configuration.md#settings)), so a
yardmaster added under another name is still the one addressed.

A hold reaches the yardmaster because `held` is of the class `attention`,
which is a new crew member's default `--on`. The class is stored by its
name and looked up when the inbox is read, so a kind that joins it in a
later release reaches every crew member that hears the class
([subscriptions.md](subscriptions.md#the-class-attention)).

A crew member made before the class has a copy of the default list as it
was that day, and does not hear a kind that joined since (`held` once was
one) until its `--on` is replaced. yardr leaves such a list alone: a list
someone shortened on purpose looks the same. `yardr doctor` has a note for
each such crew member, with the kinds of the class it does not hear and the
command that names the class in their place. `--on` replaces the list, so
the command keeps the other kinds the crew member has; `yardr crew set`
keeps the rest of the definition. `yardr subscribe` without arguments shows
what was left out: under its matrix of every subscriber's `--on` kinds and
subscriptions, "reaches no one" lists the kinds of the class that are in
nobody's `--on` and in no `yardr all kind=<k>` subscription
([subscriptions.md](subscriptions.md#who-is-told-what)).

```sh
yardr doctor                               # note crew: who has a copied list, and the command
yardr subscribe                            # who is told what, and the kinds of the class that reach no one
yardr crew show yardmaster --json          # its on today
yardr crew set yardmaster --on attention
```

A kind outside the class is added beside it. `hook_rejected`, a
webhook delivery the listener refused
([subscriptions.md](subscriptions.md#a-refused-delivery)), is one:
`--on attention,hook_rejected` for the crew member that looks after the
hooks. It is about no bead, so `--on` carries it and a `yardr subscribe
<name> yardr --where kind=hook_rejected` does not.

Its journal, inbox position and runtime state are kept across a change of
its definition, by `set` or by `add`.

## Outbound credentials

What a crew member uses to act outside the yard (the yardmaster's Linear developer
token, a GitHub token) goes in an env file of its own, never in
the store: BDP serves the store, backups copy it and every builder can read
it. That is the one rule. The examples keep such files in the yard's home
(`--home` or `YARDR_HOME`, `~/.yardr` unless set), next to everything else of the yard:
a file beside `yard.db` is not in it, and neither BDP, `yardr store backup`
nor pruning touches anything in the home but the store file. Any other path
works as well. Inbound is separate: webhooks arrive through one shared hook and its
secret file (`yardr hook add linear --source linear --secret-file …`); outbound is per
crew member.

```sh
mkdir -p ~/.yardr/crew
cat > ~/.yardr/crew/yardmaster.env <<'END'
# Linear, as the yardmaster: its app's developer token (or a personal API key)
LINEAR_API_KEY=lin_oauth_…
# GitHub, as the yardmaster: a personal token of its account, or the two lines
# of a GitHub App (below) in its place
GITHUB_TOKEN=github_pat_…
END
chmod 600 ~/.yardr/crew/yardmaster.env
yardr crew set yardmaster env_file=~/.yardr/crew/yardmaster.env
yardr crew show yardmaster     # env_file …/yardmaster.env (LINEAR_API_KEY)
```

- Lines are `KEY=VALUE`; blank lines and `#` comments are skipped. Nothing is
  expanded: the value is the rest of the line as written. `~` in the
  `env_file` path is the daemon user's home; a variable is not expanded
  there, so a yard with its own `YARDR_HOME` writes the path out.
- Each entry joins the pane environment next to `YARDR_CREW`,
  `YARDR_ACTOR` and `YARDR_HOME`. A key yardr sets itself is ignored, so a
  credentials file cannot change who the agent is.
- The environment reaches herdr over its API socket, in the request that
  opens the pane, never as arguments of a `herdr` command: a command line
  shows in `ps` to every user of the machine, the socket only to its owner.
  The same goes for a group's `env_file` and a bead agent's tab.
- The file must be a regular file that only its owner can read or write
  (mode `0600` or stricter). Missing, not a regular file or open to others,
  the crew member does not start: the start fails with the reason (backed off
  as any failed start) and a `crew_blocked` event in its journal says
  so, once per reason.
- The file is read at each start, so a new token takes effect on the next
  restart (`yardr crew restart yardmaster`). Values are never logged, put on
  the session's handle or shown; `yardr crew show` lists the key names.

### Secrets from a provider

The values need not be in a file of yardr's at all. A crew member may name a
provider: a command yardr runs to get its environment, so a secrets tool
holds the values and yardr knows none of them by name.

```sh
yardr crew set yardmaster env_command="varlock load --format env"
yardr crew show yardmaster     # env_command varlock load --format env (LINEAR_API_KEY)
```

The contract is one line: **the command prints `KEY=VALUE` lines on its
stdout and exits 0.** What it prints is read exactly as an `env_file` is
(the same reader): blank lines and `#` comments are skipped, nothing is
expanded or unquoted, a key yardr sets itself is ignored.

- The command is run without a shell. A string is split at white space and
  nothing in it is expanded (no quotes, `$VAR`, pipes or `~` in an
  argument); a word with a space in it takes the list form,
  `--set 'env_command=["op","inject","-i","my template.env"]'`. Anything
  more goes in a script. A leading `~/` of the program itself is the
  daemon user's home; a bare name is looked up on the daemon's `PATH`.
- It runs in the crew member's directory (the yard's home for a crew member
  without one), with the daemon's environment plus `YARDR_CREW` (who is
  asked for) and `YARDR_HOME`, and nothing on stdin. It has 10 seconds: a
  tool that waits for a password or a fingerprint is a failure, so the tool
  must be able to answer unattended.
- It is asked again every time the environment is needed, and nothing is
  kept in between: at each start of the crew member, for each call the daemon
  makes as the crew member (an [issue's status](links.md)), and by `yardr
  crew show` and a pack's plan, which run it to list the key names.
- With an `env_file` too, both are read, and **the command's value wins**
  for a key in both.
- If the command is not found, exits non-zero, does not answer in time or
  prints a line that is not `KEY=VALUE`, the crew member does not start, as
  with a refused `env_file`: a failed start, backed off, and a
  `crew_blocked` event naming the command, how it ended and the last
  lines of its **stderr**. Its stdout is never shown, and a bad line is
  named by its number. So a provider should not print a secret on stderr.
- The command line is stored in the yard and shown by `yardr crew
  show`: it is not a place for a secret. Give the tool its own token
  through the daemon's environment or the tool's own login.
- `yardr doctor` notes a crew member whose `env_command` names a program it
  does not find. It does not run it.

**varlock** ([CLI reference](https://varlock.dev/reference/cli/load-and-run/):
`varlock load`, "`--format env`: dotenv lines … with raw values"). In a
directory with the crew member's `.env.schema`:

```sh
yardr crew set yardmaster --dir ~/projects env_command="varlock load --format env"
```

**1Password** ([`op inject`](https://www.1password.dev/cli/reference/commands/inject/):
it fills the `{{ op://vault/item/field }}` references of a template and,
without `--out-file`, writes the result to stdout). The template holds
names and references, no values:

```sh
cat > ~/.yardr/crew/yardmaster.env.tpl <<'END'
LINEAR_API_KEY={{ op://yard/linear-yardmaster/credential }}
GITHUB_TOKEN={{ op://yard/github-yardmaster/token }}
END
yardr crew set yardmaster 'env_command=["op","inject","-i","/Users/jo/.yardr/crew/yardmaster.env.tpl"]'
```

Both commands are taken from the tools' documentation as linked; neither
was run against a real varlock or a real 1Password vault for this page.
Check yours with `yardr crew show yardmaster`, which lists the names it got
or says why it got none. If a tool quotes its values, the quotes are part
of the value yardr passes on: have it print them raw. Not checked at all:
sops, the macOS keychain, any other tool. For those, and for anything that
needs a shell, the provider is a script of yours that keeps the contract:

```sh
#!/bin/sh
# ~/.yardr/crew/yardmaster-env: prints the yardmaster's environment
printf 'LINEAR_API_KEY=%s\n' "$(your-secrets-tool get linear-yardmaster)"
```

What this does and does not do. The values are no longer plain text under
`~/.yardr`: yardr holds them in memory only, for the request that opens the
pane or for one call, and writes them to no file, event or log. But the
agent still has them in its environment, as with an `env_file`, and can
read and print them. And the provider is a command anyone running as the
same user can run: another agent on the machine gets the same answer by
running it. A provider moves the secrets out of yardr's files; it does not
keep them from the agents.

### As a GitHub App

With a personal token a crew member's comments are the token's owner's; in a
yard one person runs, that is the person. A crew member that speaks as a GitHub
App comments as the App's bot, `<slug>[bot]`, and needs no account of its
own. Create one private GitHub App per yard, owned by the yard's operator
and installed only on that operator's account. A GitHub App has one webhook
URL and private key: a shared public App would send every installer's events
to one machine. App names are global; if the name you want is taken, try
`<something>-yardr` or `yardr-<owner>`.

1. **Create the App** (GitHub, your account's **Settings → Developer
   settings → GitHub Apps → New GitHub App**), installable *only on this
   account*. Its webhook URL is the hook's (`https://<your host>/hooks/gh`)
   and its webhook secret the contents of the hook's `--secret-file`. The
   permissions and events to tick are in
   [subscriptions.md](subscriptions.md#as-a-github-app).
2. **The private key**: on the App's page, *Generate a private key*. GitHub
   downloads a `.pem`; keep it where the env file is, for its owner alone:

   ```sh
   mv ~/Downloads/yardr.*.private-key.pem ~/.yardr/crew/yardr-app.pem
   chmod 600 ~/.yardr/crew/yardr-app.pem
   ```

3. **Install it** (the App's page, *Install App*) on the repositories the
   crew member answers in.
4. **Two lines in the crew member's `env_file`**, in place of `GITHUB_TOKEN`:

   ```sh
   GITHUB_APP_ID=1234567            # the App ID, or its Client ID (Iv23li…)
   GITHUB_APP_PRIVATE_KEY_FILE=~/.yardr/crew/yardr-app.pem
   ```

   GitHub takes either id as the issuer of the App's token and recommends
   the client id; both are on the App's page. The key's path is absolute or
   starts with `~/`: the daemon and the pane read the line from different
   directories. The key file is held to the rule the env file is: a regular
   file of mode `0600` or stricter.
5. **The identity**, the bot's login, so that its own comments stay out of
   its inbox and `@<slug>` reaches it:

   ```sh
   yardr crew identity yardmaster --source github --key 'yardr[bot]'
   yardr crew restart yardmaster
   yardr crew show yardmaster     # github credential: GitHub App (GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY_FILE)
   ```

For each call yardr signs a token as the App (a JWT, RS256, good for nine
minutes), asks GitHub which installation covers the thread's repository,
and is given an installation token for that repository, which lives an
hour. The token is kept in the memory of the process that asked (the
daemon, or one `yardr reply`) until five minutes before it expires, and is
never written to the store, an event or a log. Where the App is not
installed the call is refused with the repository's name and "install the
App there".

When both the App's lines and `GITHUB_TOKEN` are set, the App answers. One
of the two lines without the other is an error, not a reason to use the
token. With neither, a crew member is refused: it does not fall back to `gh
auth token`, which is the login of whoever runs the yard. A person at their
own shell (the operator's `yardr reply --crew yardmaster`) still speaks as
`gh` has them logged in.

The assembly's scripts and the daemon's reads of pull requests and CI call
`gh` and so act as its login, not as the App: with the permissions above the
App cannot push or merge.

## Speaking in sources

A crew member answers what reached its inbox where it came from, as itself
(and may [open a thread](#opening-a-thread) when the operator allowed it):

```sh
yardr inbox                                   #   4211 10-03 09:14  hook (mentioned) linear comment create by Jane on "Retries": @yardmaster can you look?
yardr thread show 4211                        # the whole thread, read from Linear: ids, parents, authors, times, bodies
yardr reply 4211 "Filed as yardr-k3f9; I'll post here when it lands."
yardr reply 4211 --to <message id> - < answer.md   # under another conversation of the thread
yardr reply 13974 "Noted; I'll post here when it lands."   # mail to you, answered by mail to its sender
```

`<seq>` is the number an inbox line starts with. A `hook` event from a
source with a replier (`linear`, `github`) is answered in its source: in
Linear the reply goes under the conversation of the delivered comment
(Linear threads one level deep, so under its root), or a new comment for
an issue delivery; in GitHub it is a comment on the issue or pull request.
A `mail` event to the crew member is answered by mail to its sender: mail
from this yard (a user or a crew member) as `yardr mail <sender>` would
send it; mail from `<name>@<peer>` as `yardr mail <name>@<peer>` would (it
is queued for the peer, which the crew member must be allowed, and a
refusal reads as that command's). Anything else is refused. `--to` is for
hook deliveries only; each run of a mail reply sends one mail.

yardr only types. The call is made by the CLI in the crew member's pane with
the credential its `env_file` put there (`LINEAR_API_KEY`; for GitHub
[an App's two lines](#as-a-github-app), else `GITHUB_TOKEN`); the server is
asked only for the event, and never
sees, stores or sends a credential. After posting, the crew member takes part
in the thread at once (`yardr thread list`, how `replied`) and a `replied`
event (source, thread, url, the comment's id as `message`, the delivery's
`seq`) lands on its journal; the webhook's echo of the reply is its own
action and does not reach its inbox.

**Who may speak as a crew member.** `yardr reply --crew <name>` is taken
from the crew member's own session (the pane yardr started it in names it in
`YARDR_CREW` and `YARDR_ACTOR`) or from the operator (the yard's
`operator` setting, else the user running the command, which is what a
shell with no `YARDR_ACTOR` acts as). A bead's agent, or another crew member,
is refused: its credential, or the operator's `gh` login, would otherwise
speak in the crew member's name. Every agent still runs as one OS user, so
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

A crew member can also speak first: open a thread of its own, when its
definition allows it.

```sh
yardr thread open linear --in YRD "Web view: train" -b - < train.md      # an issue in the team (key or name)
yardr thread open github --in acme/webapp "Retries drop the last attempt" -b "Seen in yardr-k3f9."
```

The source is `linear` or `github`; `--in` is the Linear team (its key, or
its name when one team has it) or the GitHub repository (`owner/repo`); `-b`
is the body (`-` reads stdin). Text only, as a reply. It prints the URL of
the issue it made.

The rules are those of `yardr reply`. The CLI in the crew member's pane makes
the call with the credential from its `env_file`, so the issue's author is
the crew member's own user in the source; the server keeps the record and
never sees the credential. Only the crew member's own session or the operator
may speak as it (`--crew`).

**Who may open threads.** No crew member, unless its definition says so:

```sh
yardr crew set yardmaster open_threads=linear,github   # the rest of its definition stays
yardr crew show yardmaster     # may open threads in github, linear
```

`open_threads` names the sources, comma-separated (or as a JSON list).
Without it `yardr thread open` refuses and says what to set. Only the
operator (the yard's `operator` setting, else the user running the command)
may add a source to it: `yardr crew set` (or `add`) from a crew member's
session, or a bead agent's, is refused when it would let a crew member (itself
or another) open threads where it could not before. Keeping the setting as
it is, narrowing it or removing it (`--unset open_threads`) is anyone's,
like the rest of a definition; left out of a `crew add --replace`, the
setting is gone. The store refuses the opening
itself for a crew member that is not allowed, whoever asks; who may change the
setting is checked by the CLI, the same line, not wall, as who may speak.

A pack can ask for it: `open_threads: [linear]` under a crew member in
`pack.yaml`. That sets nothing by itself. `yardr pack apply` asks the
operator, `Let yardmaster open threads in linear?` with no as the default (or
takes `--open-threads yardmaster=linear`), only for a source the yard has a hook
and the crew member a credential for, and only when the operator is the one
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

**The thread is the crew member's.** It takes part at once (`yardr thread
list`, how `opened`), before the source's webhook has said anything, and a
`thread_opened` event (source, `in`, thread, title, url) lands on its
journal. With an `involved` subscription what others say there reaches its
inbox as `participant`, no mention needed, and `yardr reply <seq>` answers
it. The thread's id is the one deliveries carry: the issue's id in Linear,
`<owner>/<repo>#<number>` in GitHub.

### Setting a thread's status

A crew member can move the issue a thread is to started, review or done, when
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

GitHub takes a fourth word, `label:<name>`, since open and closed say
little: it adds the label to the issue, or to a pull request, and yardr
never removes one.

```sh
yardr thread state --source github acme/webapp#12 "label:in review"
```

**Nothing to do.** The issue is read first. One already in the status asked
for gets no call that changes it, the output says it was there
(`… was already Done; nothing set`), and nothing is logged.

**Who may set a status.** No crew member, unless its definition says so:

```sh
yardr crew set yardmaster set_state=linear,github   # the rest of its definition stays
yardr crew show yardmaster     # may set the status of threads in github, linear
```

`set_state` is its own setting with the rules of `open_threads` above:
it names the sources, only the operator may add a source to it (`yardr
crew set`, or `add`, from a crew member's session or a bead agent's is
refused when it would allow more than before), keeping, narrowing or
removing it is anyone's, and left out of a `crew add --replace` it is
gone. Being allowed to open threads is no leave to set their
status, nor the other way. The store refuses the change itself for a
crew member that is not allowed, whoever asks. A pack cannot ask for it.

**A hook acting as the crew member.** `set_state` is also what lets a hook
with a map move a tracked issue as the crew member when a bead changes stage
([links.md](links.md#a-beads-stage-moves-the-issue)). The daemon makes that
call, with the credential in the crew member's `env_file` and nothing else: its
own environment and `gh` login are the operator's. There the issue is only
moved forward, and the crew member need not take part in the thread.

**Where.** Only in a thread the crew member takes part in (`yardr thread
list`): one it acted in, answered in, opened, or follows. Anywhere else the
command refuses and names `yardr thread follow`, which makes it a
participant. Only the crew member's own session or the operator may speak as
it (`--crew`), and the call is made with the crew member's own credential,
as a reply is.

**What is recorded.** A `thread_state` event on the crew member's journal:
the crew member, the source, the thread, `state` (the word asked), `status`
(the source's name of the status set), `was`, `instead` (`started`, when
review had no status of its own) and, as the event's actor, who ran the
command. `--json` prints the same fields, with `actor` and `changed`.

**No echo.** The source tells of the change by webhook, and that delivery
does not reach the crew member's own inbox: its actor is the crew member, by its
identity in the source (`yardr crew identity`), and a crew member never
hears of what it did. Without an identity there yardr cannot tell the
delivery from someone else's, so the command warns when it changes
something for a crew member that has none.

`YARDR_LINEAR_API` and `YARDR_GITHUB_API`, in the environment of the
command, send the outbound calls (replies and status changes too) to
another endpoint: a stand-in when trying yardr out, or a GitHub Enterprise
API (`https://<host>/api/v3`).

What to say, and whether to, is the crew member's role: see the starter
pack's [yardmaster.md](../packs/starter/roles/yardmaster.md), which `yardr
init` installs.
Setting a source up end to end is in
[subscriptions.md](subscriptions.md#setting-up-linear).
