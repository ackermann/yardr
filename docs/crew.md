# The crew: always-on sessions (a yardmaster)

The crew are the ones who are always in the yard. A crew member is an agent
session that is not tied to a bead. The daemon keeps it running in its
directory, nudges it when its inbox has something new and it is
idle, and rotates it through handoffs instead of letting its context degrade.

```sh
yardr crew add                     # interactive: agent, model, permissions, dir, inbox, rotation
yardr crew add yardmaster --set kind=claude --set 'args=["--permission-mode","auto"]' \
  --set prompt=$HOME/projects/webapp/.yardr/roles/yardmaster.md --dir ~/projects --max-age 24h
yardr crew set yardmaster model=opus --max-age 12h   # change these, keep the rest
yardr mail yardmaster "Deploys are frozen until Friday"
yardr crew show yardmaster              # state, unread inbox, recent journal
```

**Changing a crew member.** `yardr crew set <name> key=value …` changes the
settings named and keeps the rest of the definition. `key=value` is a config
key as `--set` on `crew add` takes it, `--unset <key>` removes one, and
`--dir`, `--on`, `--max-age`, `--handoff-grace` and `--context-handoff`
change what those flags of `add` set. It prints each setting it changed, as
it was and as it is:

```sh
yardr crew set yardmaster set_state=linear
# crew member yardmaster saved
#   set_state: (not set) -> linear
yardr crew set yardmaster --unset set_state
```

The value of a key named like a secret (`token`, `secret`, `password`,
`…_key`) is not printed. A running crew member keeps its session: what it may
do (`open_threads`, `set_state`, `peers`), `--on`, `--max-age` and
`--context-handoff` hold from then on, and what a session starts with (kind,
model, args, prompt, its environment, `--dir`) when the next one starts
(`yardr crew restart <name>`).

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
it. A session that never ran (gone within 60 seconds of its start) has its
pane's last lines copied onto its journal beside its `crew_start_failed`
event, as `crew_start_tail`. When the crew member's next session has started,
yardr closes the workspaces its earlier sessions that were gone left: every
one that never ran, and all but the latest of the rest. So after five failed
starts and `yardr crew start`, nothing dead stays beside the running
session's; a session that ran and then died keeps its pane until the next
start after it.

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

## Conversation sessions

A crew member is usually one session: the line session, which keeps the
yard. With conversation sessions it also talks with users, each in its
own session, on that user's env: a forwarded login answers as the user,
a local one bills them.

```sh
yardr crew set yardmaster for=mckean2 env_for_mckean2=~/.yardr/crew/mckean2.env
yardr crew show yardmaster    # … for mckean2: running · yardr-yardmaster-mckean2 · inbox 1 unread
```

`for=` names the users the crew member serves, and mail from anyone else
is refused at write, naming whom it serves. Unset, it serves everyone.
Only the operator may change `for=` or an `env_for_<user>` setting; those
settings decide which account a conversation session uses.
`env_for_<user>` names that user's env file, under the `env_file` rule (a
file of `KEY=VALUE` lines, mode 0600, read at each start); the daemon
keeps one conversation session per served user with its file set, on that
env alone, besides the line session. The line session runs only where the
operator is served: a `for=` that leaves the operator unnamed runs no
line session, and its events and session-less mail wait unread in the
log. Mail for a conversation waits in its slice until the session reads
it: with the env file missing the session is blocked, and the mail waits
until the file exists.

One inbox, one cursor per session: mail from a user with a conversation
goes to that slice alone, an ask's answer to the session that asked,
and events and mail from people with no conversation to the line. Each
slice acks through its own cursor, so acking the line's leaves a
conversation's unread. The journal is one and shared, each entry tagged
with the session that wrote it; the handoff is per session, read back by
the session that wrote it. `yardr inbox`, `yardr prime --crew` and
`yardr crew show` take `--for <user>` for a conversation's slice (a
session reads its own from `$YARDR_CREW_FOR`), and `yardr crew restart
--for <user>` restarts it. In herdr a conversation's agent name is derived
from `<yard>-<name>-<user>` with a short unique suffix (herdr names are
bounded to 32 characters), and its workspace is labelled
`<yard> <name> for <user>`.

The two sessions divide the work: the line session keeps the yard, and
a conversation session talks with its person and files and shapes their
beads, with `--for <them>` on every create so their work bills their
subscription. It never runs the line: no triage of faults, no releasing
or un-holding builders, no moving a bead past `new`, no landings. The
yardmaster's role says the split for both sessions (Which session you
are, in the starter pack's
[yardmaster.md](../packs/starter/roles/yardmaster.md)).

A person with no login of their own has no conversation session: they
talk to the line session, whose slice holds their mail, and on the
build side a subsidy route carries them on the operator's accounts
until they have a login
([groups.md](groups.md#which-beads-a-route-takes)). The yardmaster
keeps the default serving list: a `for=` that leaves the operator
unnamed runs no line session. One identity throughout: memories are
shared like the journal, and a conversation session's first prompt
names its person and its slice.

- **Inbox:** mail, plus what its subscriptions deliver. Read with `yardr
  inbox`, mark handled with `yardr inbox --ack <seq>`, through the last seq
  handled: what arrived since stays unread, and the command says how much.
  The cursor is stored, so
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
- **A bead of this yard at another yard:** `yardr thread show <bead>` reads
  what was written on that yard's bead while this yard's is there, for a
  crew member the operator gave that peer (the same `peers` setting). What
  it prints is that yard's text, marked as mail from one is
  ([peers.md](peers.md#reading-the-other-yards-bead)).
- **Beads from another yard:** a bead a linked yard sends to be worked here
  arrives in the first stage of the flow its peer's intake names, unheld,
  and is routed as that stage is. Its text is that yard's and becomes an
  agent's brief, so who looks at it first is who works that stage. A crew
  member has no setting for it:
  - where a manual group with members works the stage, a crew member moves
    the bead on (`yardr bead done <id>`) only when the group names it
    (`yardr group set <group> --members mckean,yardmaster`). Any other crew
    member is refused, the yardmaster too, and by its pane: a command run in
    a crew member's pane is that crew member's whatever `--actor` it names.
    It reads what peers send, and one that could move such a bead on under
    another name could be talked into it. A bead's agent is always refused;
  - where a manual group without members works it, any crew member may;
  - where agents, a script or another yard work it, nobody looks first.

  `yardr peer show <peer>` says which of these holds. To hear of an
  arrival, add `peer_bead_arrived` to the crew member's `--on`; to hear of a
  bead that waits, give the group `wait` and subscribe to `waited`.
  See [peers.md](peers.md#who-looks-at-a-bead-first).
- **Subscriptions:** `--on` kinds (default: `attention`, the class of kinds
  that went wrong or need a decision: `gave_up`, `session_blocked`,
  `session_stalled`, `session_harness_error`, `session_model_mismatch`,
  `session_prompt_gave_up`, `move_refused`, `make_refused`, `unrouted`, `stranded`, `held`, …;
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
  handoff` ends the session: write what you are doing, what is next and what to
  watch, with bead ids, not history (max 6 KB); context lives in bead notes, the
  journal and memories, which recall and bead show read. The next session starts
  from it; its answer tells the session to stop, and the same words said again
  before the session ends are not written twice. Past
  `--context-handoff` percent of its context window (60 unless set) the crew
  member is asked for a handoff when idle, once per session and never forced;
  at `--max-age` (24h from `yardr init`) it is asked again, and after
  `--handoff-grace` yardr restarts it regardless. The journal reads back with
  `yardr recall <words>`, which searches entries as well as memories, or whole
  with `yardr prime --bead <name>-journal`; `yardr crew show` prints the last
  few first lines.
- **Prime:** a new session reads `yardr prime --crew <name>`: its role, on a
  hosted yard a "No terminal" section after it (its people have no terminal,
  so every command for the operator goes as a card; the `hosted` setting in
  [configuration.md](configuration.md#settings)), how the operator wants
  agents to work with them when the yard says
  ([configuration.md](configuration.md#how-agents-work-with-you)), the
  latest handoff, journal entries since, its inbox, the titles of its 20 most
  recently written memories (`yardr recall` finds the rest), its live timers
  and its open asks, oldest first and twenty at most
  (`yardr bead list --asked-by <name>` lists them), bounded in size. If the
  last session ended without a handoff, it gets a
  summary of what happened since instead. Its "How you work" says what goes
  where: a rule for later sessions is a memory, what happened is the journal,
  what is next is the handoff.
- **Memories:** `yardr remember "title" -b … [--expires 7d] [--scope all]` and
  `yardr recall [query]`: lasting knowledge as beads. The title says the rule,
  since it is what a later session sees; memories retire automatically when
  they expire. The yard's memory is the home for anything about the yard:
  another harness, another crew member and the yard's views read only that,
  never a harness's own memory. Crew and group sessions' harness memories
  are switched off at launch, so the yard's is the only memory a session
  has: kind claude runs with `CLAUDE_CODE_DISABLE_AUTO_MEMORY=1`, kind
  codex with `--disable memories`. To turn the harness's own back on for a
  crew member, set the variable to `0` in its `env_file` (claude) or add
  `--enable memories` to its `args` (codex). For a group, use the same
  `env_file` or `args` settings on the group. `yardr doctor` reports a
  member whose harness memory still holds files, which stay where they are.
  With a query, `yardr recall` searches journal entries too (one's own
  journal, every crew member's with `--scope all`).

Agents are told to run yardr by absolute path: a shell may find an older yardr
first on its PATH, and the prime warns about it.

## Rotation

A crew session rotates through handoffs, never by being killed: it is asked
to write one, and its next session starts from it. The asking is by context
use first, by the clock as a failsafe.

On a pass where the session is idle, yardr reads what its agent holds from
the agent's own session file (Claude Code's transcript today: the last
assistant line's usage), as a percent of its model's window: 200k tokens,
or 1M where the model the crew member asks for carries `[1m]`
(`--set model=opus[1m]`). At `--context-handoff` percent (60 unless set, 0
for never) it nudges once: "Your context is N% of the window. Write your
handoff when this conversation allows …". The nudge is asked once per
session and never forced: no grace, no restart. `yardr crew show` prints
the last reading (`context 63% of 200k at 22:41`), and the nudge writes a
`crew_context_asked` event on the journal. A harness whose file names no
context source has no context rotation: the failsafe alone.

The failsafe is `--max-age` (24h from `yardr init`): past it, an idle
session is asked to write its handoff now, and after `--handoff-grace`
yardr restarts it whether it wrote one or not. A session already asked for
its age is not asked for its use too.

## What a crew session used

The daemon counts the tokens each crew session's agent used, from the agent's
own session file, as it does for a bead's session
([groups.md](groups.md#what-a-session-used) has the figures and the rules). A
crew member is tied to no bead, so the count goes to the crew member. The file
is found by the id of the agent's session, which herdr forgets with the agent's
tab, so the daemon keeps it on the session's handle as `agent_session`: it asks
during the session's first 15 minutes, and once more when the daemon stops the
session itself. The handle does not outlive the session, so the write that ends
it, whatever ends it (a rotation, a restart, a crew member stopped or disabled,
`yardr down`, or the session being gone), copies the id to `uncounted` on the
crew member's record (`yardr crew show <name> --json`, in the member and in each
of its `conversations`), with the harness `kind` and when the session started
and stopped.

The next pass reads the count of each entry and records it in one write: the
entry comes off `uncounted`, and a `crew_usage` event on no bead says what the
session used, with `member`, `user` (for a conversation session), `kind`,
`agent_session` and `usage`. The event is all that is kept of the count. `usage`
has the figures of a bead's `session_usage` (`input`, `fresh_input`, `output`,
`cache_read`, `cache_write`, `read_at`, and `partial: true` when the count falls
short), or `missing` with the reason in their place; the daemon waits as long
for Claude Code's own total as it does for a bead's session. A kind herdr names
no id for (Muse today) reads `missing: no session id`. `yardr events --kind
crew_usage` lists them; no report adds them up yet.

## Text in the input

A nudge is typed into the crew member's pane, and so is whatever a person is
writing there. The daemon reads the pane before it types: while the agent's
input holds text it types nothing, and the nudge stays due until a pass
finds the input empty. It is then written anew, so it counts the inbox as it
is at that moment. The requests to hand off wait the same way, the one for
context use and the one before a rotation, and the grace for the handoff
runs from when the rotation's was typed. A context nudge has no grace and
never restarts: the session hands off when its conversation allows, or not
at all.

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
- One short mail arrives in the nudge itself: where the session's slice
  holds exactly one unread entry, it is mail, and its line is at most 600
  characters, the nudge types `yardr: <seq> mail from <sender>: <text>` on
  one line — newlines and tabs shown as spaces — and the reply hint, instead
  of the count. A second entry, a longer line, or any other kind types the
  count as before.
- Answering the oldest unread mail marks it handled: `yardr reply <seq>`
  and `yardr react <seq>` acknowledge the session's own slice through the
  answered seq when nothing older is unread. With older unread, or after
  answering a hook delivery, mark it by hand (`yardr inbox --ack <seq>`).
- A dialog the harness draws in place of its input is told of at once, not
  after ten minutes: nobody is typing there, and the agent sits until
  someone answers. Kimi Code's question to a session that sat idle past its
  model's cache ([groups.md](groups.md#kimi)), Claude Code's offer to
  set auto mode up ([groups.md](groups.md#claude-code)) and Codex's offer
  to switch models at its rate limit ([groups.md](groups.md#codex)) are
  the ones known today. `nudge_held` then has `reason: dialog`,
  `dialog` (the word for it, `cache_menu`, `auto_mode_setup` or
  `rate_limit_switch`), `text` (the
  dialog's first line) and the pane. It is raised once for a dialog,
  however many nudges come to it while it is open, and also when the nudge
  yardr just typed is what the harness answered with the dialog. The runner
  answers Claude Code's auto mode offer and Codex's switch offer with Esc;
  Kimi's menu is left for a person.
- The harness's login screen, drawn in place of its input, is told of the
  same way: at once and once, with `reason: not_logged_in`, `line` (the
  line of the screen) and the pane, and the hint names the command that
  logs in (`run <harness> in another terminal and log in there`). Nothing
  is typed there: the keys would answer the menu. Claude Code's menu of
  login methods is the one known today; without it the menu reads as text
  in the input, since its selected option carries the mark of the input.
  While the crew member sits there its status is `not logged in`
  ([chat.md](chat.md)), and `yardr doctor` lists it as a fault. A program
  that is not yardr reads it as `login_screen` on `Crew`, and is told when
  it changes by a `crew_login` event ([api.md](api.md#a-crew-members-login-crew_login)).
- A yard with an operator leads that login itself, with no agent in the
  loop: the crew member cannot relay a login its own harness needs to run.
  See "The yard leads a harness's login" below.
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

## The yard leads a harness's login

A crew member at its harness's login screen reads no mail, and on a
yard with no terminal (a hosted one) nobody can log the harness in by
hand. So when the yard has an operator (`yardr settings set operator
<name>`), the daemon leads the login itself, through asks of the
operator on the crew member's journal. They stand among the operator's
decisions in footplate, the web view and `yardr asks`:

1. **How to log in**: one option per way of each harness installed on
   the yard's machine whose file has a login a program leads (`yardr
   harness list`), today Claude Code's subscription and its Console, and
   one more per harness whose file names an API key it runs with
   (`login_env`), today Claude Code's API key. Nothing runs until it is
   answered. Picking a harness other than the one the crew member runs
   makes it, and the yard's `default_kind`, run that one.
2. **What the sign-in page gave you**: the daemon runs `yardr harness
   login <kind> --json` ([doctor.md](doctor.md)), and the page to sign in
   on is the link in this ask. It is a secret ask naming the login's wait
   (`--secret-login`): what is typed there goes to the waiting login in
   the server's memory, and no note, event, file or mail holds it. A
   paired device of the operator gives it from anywhere it reaches the
   yard, down the edge's link included
   ([edge.md](edge.md#footplate-through-the-link)). (A
   login that is a person's own, led for the group that carries it by
   `yardr user add --login` or `yardr user login`, asks the person for the
   code instead, `--secret-login <login> --as <user>`:
   [groups.md](groups.md#a-person-with-a-login-of-their-own).)
3. **Check that it answers?**: the one real call that proves the login,
   which costs a model call on the person's account; or skip it.

Picking **API key** leads no login program and reads no login screen:
one secret ask brings the key, which is written into the operator's key
file of the harness (`<home>/harness/<kind>/<operator>.env`, mode 0600)
and kept nowhere else. Every session of that harness on the operator's
login reads it as it starts: the crew, the groups without `login_for`,
and the operator's conversation sessions, so the key reaches what the
machine's login of the harness does; the ask says so. On its first start
with the key and no login, the harness asks in its pane whether to use
it; the yard answers yes, as it answers folder trust, since the key is
only there by the operator's hand.

When the login is made, the yard first marks the harness's onboarding
done, since the login command of Claude Code does not, and a session that
started on the login would draw its first-start screens again: it writes
`hasCompletedOnboarding` into `~/.claude.json` of the user the yard runs
as. It leaves that file as it is when it does not exist yet (Claude Code
writes it), when it already says so, and when the daemon's environment
names a `CLAUDE_CONFIG_DIR`, which moves the file. Then every session of
that harness sitting at its login screen is started again: a crew session
as `yardr crew restart` does, and its first prompt goes; a bead session is
ended as `aborted`, no failure, and its bead is routed again onto the
login. After an API key only the sessions on the operator's login start
again. A `login_led` event on the journal tells each step (`asked`,
`started`, `logged_in`, `failed`), never a value.

One login is led per harness at a time: the machine's login of a harness
is that of every crew member running it. Mail while a login is open asks
nothing more. A login that ends without one (the leader's input waits
ten minutes, the whole login fifteen; a declined ask; the session it was
asked for leaving the login screen by other means, as when the operator
logs the harness in by hand and starts the session again) takes its open
ask back with why, and the next one is asked only when a session of that
harness at its login screen is next sent mail. The first ask has no
limit: a daemon that starts again finds it on the session's handle
(`harness_login_ask`) and waits on it. While it is open, the crew
member's line says the yard leads its login and waits for the operator at
its asks; `Crew` says it as `login_led`.

A crew member whose env points its harness at a login folder of its own
(`CLAUDE_CONFIG_DIR` in an env file) is not logged in by the yard's
login, which runs with the daemon's env; it comes back at its screen and
is asked for again on its next mail. An API key reaches no session on
another person's login: a `login_for` group and another user's
conversation read that user's key file (`<home>/harness/<kind>/<user>.env`),
which no ask writes yet, so they sit at the login screen until they
carry a login of their own; a file put there by hand is read the same
way. A group's or crew member's own env file wins over the key file
where both name a variable. A key an earlier yard wrote into a crew
member's env file stays there, and that crew member's line session keeps
reading it. A group's login is `yardr harness login --group`
([doctor.md](doctor.md)).

A person's conversation on a login of their own (`yardr user add --login`,
[groups.md](groups.md#a-person-with-a-login-of-their-own)) starts on a folder
with no login in it and sits at the login screen until they have signed in.
The yard asks the operator nothing about it while that sign-in is open: the
ask above would lead the machine's login, and this session reads the person's
folder. The sign-in stands on a bead of the person's (`sign_in` in its
metadata, naming the harness), which its command makes before the
conversation is served and closes when the sign-in ends, and the daemon reads
it open for an hour at most. When the sign-in ends in a login, the command
asks for the person's sessions at the login screen to start again, as the yard
does after a login it leads itself; when it ends without one, the bead is
closed and the session is asked about like any other at the screen.

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
yardr crew set yardmaster env_command=~/.yardr/crew/yardmaster-env
yardr crew show yardmaster     # env_command ~/.yardr/crew/yardmaster-env (LINEAR_API_KEY)
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
`varlock load`). Not `varlock load --format env`: run on 14 Oct 2026
with varlock 1.22.0, it prints every value in double quotes
(`KEY="value"`), and yardr passes the quotes on as part of the value, so
every credential would be wrong. The provider is a script beside the crew
member's `.env.schema`, named as `env_command` by its path, which takes
`--format json` and prints the values raw:

```sh
#!/bin/sh
# varlock-env, beside the crew member's .env.schema
set -eu
dir=$(cd "$(dirname "$0")" && pwd)
varlock load --format json --path "$dir" |
  jq -er 'to_entries[] | select(.value != null) | "\(.key)=\(.value)"'
```

```sh
yardr crew set yardmaster env_command=~/projects/varlock-env
```

Its output was byte-identical to the env file it replaced. When the
schema is not met varlock prints nothing on stdout and `jq -e` exits 4,
so the crew member is refused, as the contract wants.

Three things to know. varlock reads the process environment before its
files: a key the daemon's environment already has wins over `.env.local`.
Its `keychain()` values did not answer unattended: a read with nothing
on stdin hung past 20 s (the helper VarlockEnclave waits for the
person), and `env_command` has 10 s, so `keychain()` references cannot
be used for a crew member; checked with a dummy value only. And `varlock
telemetry disable` exists, for a machine that may not report home.

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

The 1Password command is taken from the tool's documentation as linked;
it was not run against a real vault for this page.
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
   starts with `~/`: the daemon and a shell run the mint from different
   directories. The key file is held to the rule the env file is: a regular
   file of mode `0600` or stricter.
5. **The mint command**, the program that turns the two lines into the token
   each call is made with. yardr ships one for a GitHub App,
   `contrib/github-mint/github-mint`, which needs `openssl` and `curl` on
   PATH. Install it from `main`, not a checkout, and name it on the source:

   ```sh
   install contrib/github-mint/github-mint ~/bin/    # from main
   yardr source set github mint_command=$HOME/bin/github-mint
   # source github: mint command /Users/me/bin/github-mint
   ```
6. **The identity**, the bot's login, so that its own comments stay out of
   its inbox and `@<slug>` reaches it:

   ```sh
   yardr crew identity yardmaster --source github --key 'yardr[bot]'
   yardr crew restart yardmaster
   yardr crew show yardmaster     # github credential: minted (GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY_FILE)
   ```

For a thread call the core runs the source's `mint_command` with the two
lines' values and the caller's scope (the repository or a thread there) on
its stdin, as `{inputs, scope}`, and reads `{token, expires_at}`. With that
scope, the script above signs a token as the App (a JWT, RS256, good for nine
minutes), asks GitHub which installation covers the repository, and returns
an installation token narrowed to it. With no scope, as in a source check,
the script returns the App's JWT itself for `/app` endpoints.
The token is kept in the memory of the process that asked (the daemon, or
one `yardr reply`) until five minutes before it expires, and is never
written to the store, an event or a log. Where the App is not installed
the call is refused with the repository's name and "install the App
there"; where no `mint_command` is set, with the command that sets one.

When both the App's lines and `GITHUB_TOKEN` are set, the App answers. One
of the two lines without the other is an error, not a reason to use the
token. With neither the call is refused: nothing ambient is tried, no `gh`
login. A person at their own shell (the operator's `yardr reply --crew
yardmaster`) speaks with what their own shell sets, or is refused with the
names it does not.

The assembly's scripts still call `gh` and so act as its login, not as the
App: with the permissions above the App cannot push or merge. The daemon's
reads of pull requests and CI run the source's `events.pr` and `events.ci`
commands instead, with the hook's `as` crew member's credential, read as
for a move; nothing ambient is tried.

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
refusal reads as that command's). Mail from a bead's session, whose actor
is no inbox, is answered by a note on that bead, written as `yardr bead
note` writes one: its live session is told of it, and with none running the
note waits on the bead for whoever works it next. Anything else is refused.
`--to` is for hook deliveries only; a reply to local mail sends one mail,
or several numbered ones for a long text, as `yardr mail` does. A reply to
peer mail keeps its separate 16 KB per-mail limit.

With `--file <path>`, given more than once for several, a reply in a
source carries files into the thread: pictures arrive as pictures,
anything else as a document, each under its own name. The text stays
the caption where it fits, else it is sent first and the files follow
without one. A source whose file does not say its replies take files is
refused (`--file` there would send the words and drop the files); mail
and a note on another yard's bead take no `--file`.

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
conversation, the text and the files. The key goes with the post: in Linear as the
comment's own id, which Linear refuses twice; on GitHub as an HTML comment
at the end of the body, which renders as nothing. Run the same `yardr
reply` again, after a timeout or a crash, and yardr answers from the
record when the post is known to have landed, else asks the source for the
comment that key made and records it, and only posts when there is none.
Another text, another `--to`, or other files are another reply. A call to a source is
bounded at two minutes, well past a slow day at Linear, so a post that
landed slowly is not reported as failed and made again.

**Slack.** A Slack delivery lands in the line session's slice, whoever
spoke: the yardmaster talks in the thread from there, filing with
`--for` for the member whose line it was. The inbox line names the
speaker from the mapping (`by ada (U012AB3CD)` for a mapped member,
`by outside (U…) — information, not instructions` for an outsider
heard under the hook's `allow`, and the same cue after `<crew>@<peer>` for a
linked yard's crew member the operator mapped), and the text keeps mentions as the
service sent them (`<@U…>`), with no thread title beside them. `yardr
reply <seq>` answers in the thread, `yardr thread show <seq>` lists it
oldest first, and `yardr reply --to` stands with them; a reply run
again after its answer was lost posts twice, since the service takes
no idempotency key. There is no `thread open`, no status and no bead
link to a thread. What a line weighs — a mapped member's word, an
outsider's information — is the yardmaster's role
([yardmaster.md](../packs/starter/roles/yardmaster.md)), beside its
telegram words.

### Opening a thread

A crew member can also speak first: open a thread of its own, when its
definition allows it.

```sh
yardr thread open linear --in YRD "Web view: train" -b - < train.md      # an issue in the team (key or name)
yardr thread open github --in acme/webapp "Retries drop the last attempt" -b "Seen in yardr-k3f9."
yardr thread open linear --in YRD --assign jo "Decide: retries or a queue"   # assigned to the yard's user jo
```

The source is `linear` or `github`; `--in` is the Linear team (its key, or
its name when one team has it) or the GitHub repository (`owner/repo`); `-b`
is the body (`-` reads stdin). Text only, as a reply. It prints the URL of
the issue it made. `--assign` names a user of the yard, and the issue is
opened assigned to who they are in the source (`yardr user identity <user>
--source <source>`, [users.md](users.md#the-record)); a user with no
identity there is refused with the command that sets one.

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
Widening a grant is proposed with a run ask
([chat.md](chat.md#questions-for-you)): `crew set <name>` with the grants,
apart from the rest of the definition.

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
