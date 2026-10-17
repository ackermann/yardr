# yardr chat: talking with the crew

`yardr chat` is one view of the yard's [crew members](crew.md) and your
conversation with each, in a terminal. It is the same for every harness a
crew member runs, and you do not have to find the crew member's pane to say
something to it.

```sh
yardr chat                    # opens on the yardmaster
yardr chat assembly           # opens on the crew member named
yardr chat yardmaster@north   # opens on one a linked yard exposes
```

It shows the mail a [user](users.md) and a crew member have written each other,
and what you type is mail: `yardr chat` adds no other channel. What it shows
is what `yardr mail --with <crew member>` prints, and Enter does what
`yardr mail <crew member> <text>` does. What the yard itself reports to you,
your notifications, is in no conversation: it has a row of its own,
[`yard`](#the-yard). Under the mail with a crew member, the view also shows
that crew member's questions for you ([below](#questions-for-you));
`mail --with` shows mail alone.

## From the onboarding

The onboarding ends here. Once the form has started the yard and the yardmaster
is up, "Talk to the yardmaster" (the answer its last question starts at) opens
this view on the yardmaster, in the terminal the form ran in.
`yardr init --start` in a terminal does the same without the question, and
so does "Talk to the yardmaster" after a page of [`yardr setup`](setup.md).

The yardmaster that `yardr init` adds is asked, in the first prompt of its first
session, to greet the operator by mail and to say what it needs for the next
setup step ([configuration.md](configuration.md#starting-the-yard)). So its
first message is there when the view opens, or arrives while you watch:
until there is mail between you, the conversation says

```
yardmaster is starting: it reads its role first, and its first message appears here.
You can write to it already.
```

What you type goes to the yardmaster as mail, and it answers by mail: its role
says the setup conversation stays by mail when it began there. What the
yardmaster does in between is in its pane (`ctrl+g`).

Leaving the view prints one line and stops nothing:

```
You left the chat; yardmaster keeps running. To come back: yardr chat
```

It does not open from the onboarding:

- without a terminal, with `--json`, in an agent's own session, and with
  `--no-chat` (`yardr init --start --no-chat`, `yardr setup --no-chat`):
  yardr then prints where the yardmaster's pane is, as it did before there was a
  chat, and names `yardr chat`;
- when the yardmaster did not come up (not logged in, a harness that exits):
  nobody would answer, so the form prints what [`yardr doctor`](doctor.md)
  says of the yard instead, and how the onboarding is taken up again;
- when who runs the command is no user of the yard, which is the case when
  `yardr init --operator` named another than your login name (the form's
  "Your name" takes only who its terminal acts as): the yardmaster
  writes to the operator, so yardr says so and how to open the chat as them
  (`yardr --actor <operator> chat`).

## Who it is for

A user of the yard: the operator, and whoever the operator added with
`yardr user add`. The view acts as the user the command acts as (`--actor`,
else `$YARDR_ACTOR`, else the login name). Anyone else is told to ask the
operator for `yardr user add <name>`.

It needs a terminal. Without one (a script, a pipe, `--json`) it does not
start and names what does the same there: `yardr mail <crew member> <text>`
sends, `yardr mail --with <crew member> [--after <seq>] [--json]` reads the
conversation.

It shows the crew, the `yard` row and [those the linked yards expose to
this one](#those-of-a-linked-yard): no agents working a bead (their channel
is the bead), and nobody of another yard that its yard does not expose.
Another user of this yard has a row only when a name of a linked yard is
[trusted as them](#a-name-trusted-as-a-user-of-this-yard).

## The view

At 80 columns:

```
yardr chat · ada
yard (2)  [yardmaster blocked]  assembly idle (1)
────────────────────────────────────────────────────────────────────────────────
10-05 13:20  ada
  deploys are frozen until Friday
  · read

10-05 13:20  yardmaster
  noted. Until when on Friday?

10-05 13:20  ada
  until the release is tagged
  · yardmaster waits at a prompt in its pane and cannot read mail (ctrl+g goes there)

! yardmaster waits at a prompt in its pane: claude may wait at a login, a question or
  a system dialog
  │    git push origin main
  │  Do you want to proceed?
  │  ❯ 1. Yes
  │    2. No, and tell Claude what to do differently
  │  Esc to cancel · Enter to confirm
  ctrl+g names the command that opens its pane; answer there and come back
────────────────────────────────────────────────────────────────────────────────
> Write to yardmaster
>
>

enter send  ^j new line  tab next member  pgup/pgdn scroll  ^g pane  ^c quit
```

From 100 columns the rows (`yard`, then the crew) are a column on the
left, one a line, with `>` before the selected one and the unread number at
the column's right edge; narrower, they fold into the one line on top, the
selected one in brackets and the number in parentheses.
If an existing crew member is named `yard`, the system row is labelled
`yard-notify` so the two rows stay distinct.

The colours are those of the onboarding's forms, for a light and a dark
terminal, and say nothing the words and marks do not; with `NO_COLOR` set,
or in a terminal without colour, the view is plain text.

**The crew.** Each with its state and, when there is any, the number of
mails from it you have not seen:

| State | Means |
|---|---|
| `not running` | no session runs: it was stopped, or the daemon has not started it yet |
| `working` | its session is in a turn |
| `idle` | its session waits for a prompt; the daemon nudges it about new mail |
| `blocked` | its session waits at a prompt only a person in its pane can answer (a permission, a question) |
| `failed to start` | its last start failed; `yardr crew show <name>` says why |
| `not logged in` | its session runs and its pane shows its harness's login screen; the notice above the input names the command that logs in |
| `running` | a session runs and the daemon has not said more yet, or its runner cannot tell |

Above the crew stands one row that is no crew member, `yard`
([below](#the-yard)).

**The conversation** with the selected crew member: oldest at the top, the
newest in view, each mail with its time and sender. `pgup` and `pgdn` scroll.

**A mail's text** is drawn as a small part of Markdown, and is otherwise the
text as written, a line of it staying a line:

| Written | Drawn |
|---|---|
| a block between lines of ` ``` ` or `~~~` | its lines as they are, indented two columns and in the colour of code, without the fences and the language's name. A line longer than the pane is broken where the row ends and goes on behind `↪`; it is never wrapped as prose. A block nobody closed runs to the mail's end |
| `` `code` `` in a line | the code in that colour, without the backticks |
| `- item`, `* item` | `• item`, the rows after the first under the text |
| `1. item`, `1) item` | as written, the rows after the first under the text |
| `# Heading`, to `######` | one bold line, of the text's own size |
| `**bold**`, `__bold__` | bold |
| `*italic*`, `_italic_` | italic |

A mark counts only where it stands as one. A heading's `#` begins the line
and has a space after it. `**`, `*` and `_` stand at the edges of words,
with their partner on the same line, so `my_var_name`, `__init__.py`,
`2 * 3`, `*.go` and `src/**/x.go` are what they were. A line beginning `+ `
is no item: a line of a diff begins so. Nothing is drawn inside code.

Everything else is plain text: a link is the characters it was written as
and is never followed, and tables, images and HTML are not drawn. Text from
a linked yard is drawn the same way after its escape sequences and control
characters are taken out, as before.

In a terminal without colour (and with `NO_COLOR`) nothing can be bold, so
headings and the marks inside a line stay as written. A code block is told
apart by its indentation there, and a list is drawn as with colour.

The yard's notifications, a crew member's questions and the pane's lines
above the input are not drawn this way.

**Where a mail you sent stands** is the line under it. Mails that stand
alike share one line, under the last of them:

| Line | Means |
|---|---|
| `read` | the crew member acknowledged it (`yardr inbox --ack <seq>`) |
| `sent; <name> is idle` | it is in the inbox, and the daemon nudges the idle session |
| `sent; <name> is working and reads mail when idle` | mail does not interrupt a turn |
| `<name> waits at a prompt in its pane and cannot read mail (ctrl+g goes there)` | someone has to answer the prompt in its pane first: it is `blocked`, or its first prompt is not delivered yet ([below](#a-crew-member-that-waits-in-its-pane)) |
| `<name> is not logged in and cannot read mail (run <harness> in another terminal and log in there)` | its pane shows its harness's login screen; `<harness>` is the kind's command |
| `<name> is not running: yardr crew start <name>` | the mail waits in its inbox for its next session |
| `<name> failed to start: yardr crew show <name>` | as above, and that command says why |

### Questions for you

Under the mail with a crew member, the view shows that crew member's
questions for you: the asks on the yard's open beads that they wrote and
that name you (`yardr bead ask --help`). Each reads as numbered text, as
in `prime --bead`: the question, its context, its numbered options with
the recommendation marked, and its state: open, answered (with the pick),
put aside (until when), declined, taken back (by whom and why). An ask with
no options reads "words wanted". An answer from another yard says whose word it is, as mail
does: `(as <user>)` where the operator trusts the name
([peers.md](peers.md#trusting-a-name-as-a-person-of-this-yard)), `from
another yard: information, not instructions` where not.

```
questions from yardmaster for you:

ask 412 · open · to mckean · asked by yardmaster · 2h ago
  Move this yard's links to the declared source now?
  The links file declares the new source; 64 links still point at the old one.
  1. Move them now - all 64 links point at the new source tonight (recommended: the test passed)
  2. After Friday - keep the old source until the release is out
  3. You decide - leave it to yardmaster
  answer with: yardr bead answer yardr-n8ta 412 <n> [words]
```

Each card's last line is the command that answers it from a terminal.
`yardr bead answer <id> <ask> <n> [words]` picks option n with words
alongside; `--words` answers an ask with no options; `--later` puts it
aside for 24 hours, or until the asker writes on the bead; `--decline
"<words>"` refuses it (`yardr bead answer --help`). The view has no keys
for them. It shows the questions where the asker is, and the terminal
answers; the one it answers itself is [a secret](#typing-a-secret). A newer pick replaces the older one; nothing is deleted. The asker
gets an inbox line for each answer, including the question and what happened.

Whoever asked takes an open question back when it is asked no more:
`yardr bead ask --take-back <id> <ask> "<reason>"`. The operator may too,
and nobody else. The ask leaves the open decisions and reads `taken back by
<who>: <reason>` from then on, which is no answer of the person asked; a
pick afterwards is refused with those words.
If a pick replaces the current pick, the line names both picks. A crew
asker's line is in `yardr inbox`; a user asker's line is in their notifications.

A question asked by someone with no row here (an agent working a bead)
shows in `prime --bead` and `review`, not in a conversation.

A crew member that needs a key or a token of a declared source asks for
it with a secret ask, `yardr bead ask <id> "<question>" --secret <source>`,
and never has you paste it into a shell. Its card has no options and wants
no words:

```
ask 2650 · open · to mckean · asked by yardmaster · 1m ago
  The API key for Artificial Analysis
  Create one at https://artificialanalysis.ai/account/api (free plan, read only).
  a secret: ARTIFICIALANALYSIS_API_KEY, for the source artificialanalysis (https://artificialanalysis.ai)
  it goes to a file only the yard's server reads; no agent is shown it
  ctrl+o types it here · or in a terminal: yardr bead answer yardr-k3vd 2650 --secret
```

The line `a secret: …` is the yard's, from the source's declaration: the
variable, the source, and where the source's calls go. Whatever the
question and its context say, what you type becomes that variable of that
source and nothing else. Answer it here with `ctrl+o`
([below](#typing-a-secret)), or in a terminal on the yard's machine (from
another machine, `ssh -t <host> yardr bead answer <id> <ask> --secret`, or
in a window that is paired to this yard as a device of the operator,
[users.md](users.md#devices)):

```
$ yardr bead answer yardr-k3vd 2650 --secret
ARTIFICIALANALYSIS_API_KEY for the source artificialanalysis (nothing is shown as you type):
mckean gave ARTIFICIALANALYSIS_API_KEY · it is set in ~/.yardr/sources/artificialanalysis.env
```

The yard's server writes the value into the file the source's credential
is read from, as [`yardr source key`](integrations.md) does, and the answer
on the bead says who gave which names. The value is in that file and
nowhere else: not in a note, an event, mail, a brief or the asker's pane,
and the asker's inbox line reads `mckean gave ARTIFICIALANALYSIS_API_KEY
for the source artificialanalysis (ask 2650 on yardr-k3vd)`. A script
gives one as `NAME=value` lines on the command's standard input; the value
is never an argument. It is yours as the operator to give, and only you
can be asked. One kind of secret ask is for any user: `--secret <source>
--for-user <user>` asks for that user's own address in a source that tells
people (for Pushover the user key). Its line reads `a secret:
PUSHOVER_USER, ada's own address in the source pushover (…); ada is told
through pushover once it is given and yardr user set ada notify=pushover
is run`, it is that user's to answer or the operator's, the same way, and
the value goes to a file of the user's
([users.md](users.md#told-outside-the-yard)). One more kind is for a crew
member: `--secret <source> --for-crew <crew member>` asks for that crew
member's own credential in the source, which a reply answers with. Its
line reads `a secret: <names>, for crew member <name> in the source
<source> (…); the env_file line is the one written, and it takes effect
when <name> next starts (rotation or yardr crew restart <name>)`, it is
the operator's to answer, the value goes to the crew member's env_file,
and nothing restarts it: its next session starts with the new value. One
more kind is for a waiting login: `--secret-login <login>` asks for the
input of the harness login
that waits under that reference, the one its leader's input need names. Its
line reads `a secret: INPUT, for the waiting login <login>`, the value is
handed to the login and kept nowhere, to no note and no file, and a giving
with no login waiting under it is refused naming that. One more kind is a
hook's signing secret: `--secret-hook <hook> --source <source>` asks for the
value the service signs its webhooks with. Its line reads `a secret: the
signing secret for the hook <hook> (POST /hooks/<hook>), for the source
<source> (…)`, the yard's server writes it to `<home>/hooks/<hook>.secret`
(mode 0600), which the hook then names, and it is refused for a source the
yard does not declare, one whose declaration has no hook key, and a hook the
yard has already. A hook is set up by two asks on one bead, this one first:
give the secret here, then approve the hook's own run ask below. Before
approving the hook, giving again replaces the secret file; once the hook
exists, changing its secret stays in a terminal. If the yard says the value is
written and could not be recorded, give it again. Words, a pick, and a
decline's words are refused on such an ask and kept nowhere, so the value
cannot land in a note by mistake: `--later` puts it aside as any ask, and
`--decline ""` declines it. Nothing of a secret ask goes to a linked yard.
No agent is shown the value without going looking for it, which is a
line, not a wall ([integrations.md](integrations.md)).

A crew member that needs a command only you may give does not mail you a
line to paste into a terminal either. It proposes the command with a run
ask, `yardr bead ask <id> "<question>" --run "<command>"`, and you approve
or decline it. The command is one of a fixed list:

| the command | what it is for | who takes it back |
|---|---|---|
| `peer allow <peer> --mail-limit <n>` | a link that should take more mail in an hour | anyone, with a lower number |
| `peer allow <peer> --send <kinds>`, `--receive <kinds>` | what a link may carry each way | anyone where kinds were given, only the operator where they were taken, with the kinds that stood, which the card names |
| `source set <source> calls_per_hour=<n>`, `updates_per_hour=<n>` | how many calls of a source one caller makes in an hour, and how many of them are updates | only the operator: every setting of a source is yours |
| `source grant <source> read\|<call> --group <group>\|--crew <crew member>` | a group's sessions or a crew member may make the source's read calls, or one call by its name; several `--group` and `--crew` are one command and one change | anyone, with `yardr source revoke` |
| `source add <file>` | a new source the file declares: the ask carries the declaration and each command file it names, and the card shows the whole declaration; a commands-free add runs at approval, one naming commands is carried out by the server after it | only the operator, with `yardr source rm <name>` |
| `pack apply <name>` | a pack yardr embeds, with no flag: of kind source, or of a depot kind re-applied to the one depot that holds it where its plan asks nothing; the card is written from the server's own binary, shows each declared source whole and, for a depot pack, each object and changed file's diff, and is carried out by the server after approval | only the operator: for a source, with `yardr source rm <name>`; a depot import has no one command back, and `yardr pack check <name> --depot <depot>` shows where it stands |
| `hook add <name> --source <declared source>` | a new webhook endpoint `POST /hooks/<name>` for the declared source, verified with its signing secret given on the hook's own secret card into the file the yard owns for it; the card names that file, which the hook then names | only the operator, with `yardr hook rm <name>` |
| `subscribe <user> yardr [--done] [--where <key>=<value>]` | a user is told of the yard's own events in their inbox | that user or the operator, with `yardr unsubscribe` |
| `user set <user> tell=<parts>` | which parts a source tells a user of outside the yard, on their phone: a list of `notifications`, `mail` and `asks`, or nothing after `tell=` for all three | that user or the operator, with the `tell=` that stood, which the card names |
| `user add <name>` | a user added: they read mail from now on, and are told nothing from before | only the operator, with `user rm` |
| `user rm <name>` | a user removed: their subscriptions, devices and telling outside the yard go, and the mail stays in the log | only the operator, with `user add`, which starts them fresh |
| `crew set <name> open_threads=<sources> \| set_state=<sources> \| peers=<peers>` | what a crew member may newly do outside the yard | anyone where grants were given, only the operator where they were taken, with the grants that stood, which the card names |
| `crew identity <name> --source <source> [--id <id>] [--key <key>]` | who the crew member is in the source: mentions and assignments of the id or key there are then addressed to it | only the operator, with the identity that stood, which the card names, or `--rm` where none did |
| `settings set <key> <value>` | a setting changed | only the operator, with the value that stood, which the card names |
| `peer rename <old> <new>` | a link renamed: its key, address and what it carries stay the same | only the operator, with the name that stood |
| `bead update <bead> --originator <user>` | whose a bead is for | only the operator, with the originator that stood; a bead that was for no one can only be for another after |
| `timer rm <id>` | another's timer removed, and what it would have told | whoever can add it back; the card says who, or that no one can |

Anything else that is the operator's is still typed by you: a replacement
by `source add`, a hook's replacement and its `--map` and `--as`, a source's `env_file` and `mint_command`, `peer allow` with `--trust`,
`--reach`, `--expose`, or `--send`/`--receive` of a kind other than mail
or ping, a user's `notify` and `env_file`, `group add` with `--runner exec`
or `cmd=`, `group set` with `cmd=`, the `operator` setting and the settings
of where the yard reads and serves from, and every other command.
`hook_public_url` is the exception among the addresses: it binds nothing,
only says where the yard is reached from outside, so a crew member may
propose it and its card shows the whole URL each hook is registered at.
A command that mixes a flag of the list with one that is not is refused
whole, and the limit and the kinds of `peer allow` are two asks: one
command carries one or the other. The subscription and the `tell` rows
are not the operator's alone: a subscription and what a user's phone is
told of ([users.md](users.md#told-outside-the-yard)) are the user's own,
or the operator's for them, so such an ask is put to one of the two and
the one asked approves it.

```
ask 2710 · open · to mckean · asked by yardmaster · 1m ago
  Take more mail from airy?
  yardmaster says: airy's yard was refused 4 mails in the last hour: the link takes 30.
  yardmaster says: You write from airy all day, so most of its mail is yours.
  when you approve, the yard runs this as mckean, and nothing else:
      yardr peer allow airy --mail-limit 300
  what the command does: Set which messages a peer link carries and its mail limit
  the yard says: the link with airy (it calls itself yardr, key 6lHks9_z…) takes 300 mails and asks an hour from it. It takes 30 now.
  anyone can take it back: yardr peer allow airy --mail-limit 30
  approve with: yardr bead answer yardr-k3vd 2710 --approve
```

The card says whose each line is. The question and the lines that begin
`<asker> says:` are the asker's words. The command and its one-line help
description, the line `the yard says:` and the command that takes the
change back are the yard's: it parsed the command when the ask was written,
and wrote the sentence from its own records, with what the command names as the yard knows it: a peer
by its name, what it calls itself and the start of its key; a source by
its name and the host its calls go to; a call by its name, method and
path; a group or a crew member by its name and runner. The last line says
who can take the change back, which is not always anyone. Read those, whatever the question
says. The command is shown whole and is exactly what runs: the yard runs
no shell line, only the one yardr command it stored, as you, through the
same check the command has when you type it.

```
$ yardr bead answer yardr-k3vd 2710 --approve
ask 2710, asked by yardmaster: yardr peer allow airy --mail-limit 300
the link with airy (it calls itself yardr, key 6lHks9_z…) takes 300 mails and asks an hour from it. It takes 30 now.
mckean approved it, and the yard ran it:
peer airy: takes 300 mails and asks an hour, and 19200 files
```

Approving is yours as the operator and nobody else's: in a terminal on the
yard's machine (from another machine, `ssh -t <host> yardr bead answer <id>
<ask> --approve`), or from a [device](users.md#devices) of yours, where
the card then reads `mckean (via <device>) approved, ran:` and the
change's own event names the device. It is refused from an agent's pane
whatever name the call gives, so a crew member cannot approve what it
asked, and it does not
cross a link: a run ask is for the operator of the yard that asks, and no
copy of it goes to a linked yard. A command runs once for one ask. The
answer on the bead holds the command as it ran and what it printed, the
change's own event (`yardr events`) names the ask it was approved on, and
the asker's inbox line reads `mckean approved ask 2710 on yardr-k3vd, and
the yard ran: yardr peer allow airy --mail-limit 300`.

When the yard changed between the asking and your approval, nothing runs
and the yard says what changed: `ask 2710 says the link with airy takes 30
mails an hour now, and it takes 100: the card is from before that changed.
Nothing ran. Decline it; it is asked again if it is still wanted.` The
same holds when the link was removed and made again under its name: the
card would read the same, and the approval is held to the link's key
whole. It is held as firmly to a source, by its declaration's text and
where its credential comes from (`env_file`, `mint_command`), and for a
grant to each group by its runner and config and to each crew member by
its runner, config and directory: `ask 2714 was written from another
declaration of the source crm: it was replaced since (source_added, 12 Oct
23:10, by mckean). Nothing ran.` A user asked removed is held to when
they were added and by whom, a crew member's grants to its definition, a
setting to the value that stood, a renamed link to its key whole, and a
bead's originator to whose the bead was for. What is gone since is refused
by the card itself: a user added for one asked added, a timer gone for one
asked removed. The yard does not judge which change
matters, so a replaced file whose calls read the same, and a group given
another prompt, make the ask stale. What the card does not cover leaves
the approval good: the source's other limit, another grantee's grant, a
group's limit, whether a crew member runs. A pick and words are refused on a run ask and approve nothing;
`--later` puts it aside and `--decline "<words>"` ends it, as for any ask.

#### Typing a secret

`ctrl+o` in a conversation that shows a secret ask puts a field in the
place of the line:

```
──────────────────────────────────── a secret: what you type is not shown ──
ARTIFICIALANALYSIS_API_KEY for artificialanalysis  ••••••••••••••••••••••••
enter sends it to the yard · esc leaves, and nothing is sent
```

The field shows a dot for each character and nothing else: the terminal,
and a pane that shows this view, are sent dots. Paste works. While it is
open every key but `enter`, `esc`, `pgup`, `pgdn` and `ctrl+c` is the
field's: `tab` selects nobody, and nothing typed reaches the line for mail.
An ask for two names asks for one after the other (`PAIR_TOKEN (1 of 2) for
pair`) and sends them together. `esc` closes the field and sends nothing.

`enter` gives what you typed to the yard's server over its socket
(`GiveSecret`, [api.md](api.md)) and closes the field, whatever the yard
answers. Blanks around the value are left out, so a key pasted with its
line break is the key. Above the line the view then says how it ended,
until the next key:

| The view says | It means |
|---|---|
| `mckean gave ARTIFICIALANALYSIS_API_KEY · it is set in ~/.yardr/sources/artificialanalysis.env · yardr source check artificialanalysis` | the yard wrote it and recorded it; the command tries the key against the service |
| a refusal, in the yard's words | nothing was written: the ask was declined meanwhile, you are not the operator, the source was replaced since the ask |
| `<NAME> is written to <file>, but the yard could not record it: … Give it again.` | the file holds the value and the ask is still open: `ctrl+o` again, with the same value |
| `the yard did not answer. The value may be written. Give it again.` | the connection to the server broke during the call, and the view cannot know on which side of the write: `ctrl+o` again, with the same value |

None of them holds the value, and the view never says "sent" of a call the
yard did not answer. `ctrl+o` answers the oldest secret ask of the
conversation that still waits for its value; where all were given, it gives
the newest again, which replaces a mistyped key. Where the conversation
shows no secret ask it says `nothing to give: <name> asks you for no
secret`. It is the only key the view has for an ask: a pick, words, Later
and Decline are the terminal's.

A question that is about no bead is asked with `yardr bead ask --new
"<question>"`: it makes a bead titled with the question, under no flow
unless `--depot` places it, and the ask on it. The bead is of the type
`question`, which nothing routes, and the asker closes it when the answer
is on it.

### Those of a linked yard

After the crew the list has whom the linked yards expose to this one
([peers.md](peers.md#the-roster-whom-a-peer-may-see-and-write-to)): a row for
each name of the roster a peer last said, `<name>@<peer>`, the peer being
this yard's name for the link. They are the rows `yardr user list` prints
under the users. A peer that exposes nobody adds none.

```
yardr chat · ada
  yard                           │ a crew member of the linked yard north: Shapes the beads
  yardmaster  idle               │
  assembly    working            │ 10-08 14:02  ada
  mckean@north                   │   is your webapp depot on Go 1.27?
> yardmaster@north            1  │   · delivered to north, which does not say when it is read
                                 │
                                 │ 10-08 14:09  yardmaster@north
                                 │   it is, since Monday
```

Such a row is a conversation as a crew member's is, and Enter there does what
`yardr mail <name>@<peer> <text>` does: the mail is queued for the other
yard under your name, and the daemon delivers it. You need no setting of
your own for it, only the link's `send mail`; a crew member needs its
`peers` setting as well, a person does not.

It has no state and no pane (`ctrl+g` says so), since this yard knows
neither. The first line of its conversation says who that is there, a crew
member or a person, with the line their yard gives of what they do. Under
it, when something keeps the mail or its answer from arriving, one line
says what, with the operator's command on a line of its own:

| Line | Means |
|---|---|
| `this yard's link sends no mail to <peer>. The operator allows it:` | Enter is refused until `yardr peer allow <peer> --send mail` |
| `<peer> takes no mail from this yard yet, so what you write is refused there` | their link has no `--receive mail` for this yard: theirs to change |
| `<name> cannot answer you: this yard's link does not let <peer> write to you. The operator allows it:` | the answer needs `yardr peer allow <peer> --receive mail --expose <you>` here |

Where a mail you sent there stands is its delivery, since the other yard
does not say what was read:

| Line | Means |
|---|---|
| `queued for <peer>: the daemon delivers it` | it waits in this yard's outbox (`yardr peer show <peer>` lists it) |
| `delivered to <peer>, which does not say when it is read` | the other yard has it |
| `not delivered: yardr inbox says why` | the other yard refused it, or it was given up after 24 tries: mail from `yard` in your inbox has the reason |

The answer shows in the same conversation, counted on the row until you open
it, like a crew member's. Its text is another yard's and reaches the
terminal as letters only, as a pane's lines do. Mail from someone of a
linked yard who is not on its roster has no row: the line above the keys
counts it with the other mail read in `yardr inbox`.

A [long text](#a-long-text) is split as for a crew member, at 4 KB a mail,
though one mail to another yard holds 16 KB: a yard takes 30 mails an hour
from a peer unless [its operator set another
limit](peers.md#how-much-mail-a-link-takes), and each part is one.

#### A name trusted as a user of this yard

A name the operator [trusts as a user of this
yard](peers.md#trusting-a-name-as-a-person-of-this-yard)
(`yardr peer allow airy --trust mckean@airy=mckean`) has no row of its own.
It is how that user spoke, so its mail is in the conversation with the user:
where `mckean@airy` would stand the list has `mckean`, once, however many
names of however many yards are trusted as them.

```
yardr chat · ada
  yard                           │ a person of this yard, also trusted as mckean@airy: mail from that
  yardmaster  idle               │ name is in this conversation, and what you write goes to
> mckean                         │ mckean@airy, who wrote last
  yardmaster@airy                │
                                 │ 10-08 14:02  mckean@airy
                                 │   I am at the other machine until four
                                 │
                                 │ 10-08 14:05  ada to mckean@airy
                                 │   fine, the review can wait
                                 │   · queued for airy: the daemon delivers it
────────────────────────────────────────────────────────────────────────────────────────────────────
> Write to mckean@airy
```

- **One conversation, one number.** It has the mail between you and the user
  in this yard and the mail between you and each name trusted as them, in
  the order written, each under the name it was sent as. The row counts the
  unread of all of it.
- **Enter answers whoever wrote last.** What you type goes to where the
  newest mail in the conversation that is not your own came from. When that
  is a trusted name, it goes to that name at its yard, as `yardr mail
  mckean@airy <text>` and `yardr reply <seq>` on that mail do: queued for
  the peer, shown under `ada to mckean@airy` with its
  [delivery](#those-of-a-linked-yard) beneath. When it is the user in this
  yard, or nobody has written yet, it goes to the user here, as `yardr mail
  mckean <text>` does, and shows as `· sent`.
- **One mail, to one place.** An answer is never copied: not to the user in
  this yard as well, and not to the other names trusted as them. To write
  to the place the view does not name, `yardr mail mckean <text>` or `yardr
  mail mckean@airy <text>`; that mail shows in this conversation too.
- **A refusal is shown, and nothing is written instead.** When the link
  carries no mail out, the view says so in the store's words (`not sent to
  mckean@airy: peer airy: the link does not carry mail out: ...`) and keeps
  the line. It does not fall back to the user in this yard.
- **The first line and the empty input say where, before you type**: that
  this is a person of this yard, which names are trusted as them, and whom
  your next mail goes to (`mckean@airy, who wrote last`, or `mckean in this
  yard`). Both change with the mail that arrives. While the conversation
  loads, the first line says it is being read to learn where your answer
  goes, and the input says `Reading mckean`; enter sends nothing and keeps
  the line until it shows. The row has no
  state and no pane, as a person has neither.
- **Which rows there are goes by the grants as they are now**, not by the
  mark a mail got when it came. A grant given while the view is open moves
  the name's mail into the user's conversation with the next event of the link, and one taken
  back (`--no-trust`, the user removed, the name exposed no more) gives the
  name its row and its mail back. Mail the user wrote you in this yard then
  has no row again, and is counted on the line above the keys.
- `yardr chat mckean@airy` opens the user's row.
- **A name trusted as you is you, in every row.** When the yard trusts
  `ada@airy` as you, what you wrote a crew member from airy is in that crew
  member's row under `ada@airy`, and what it wrote back there under
  `yardmaster to ada@airy`, in order with the mail between the two of you
  here. None of it is unread for you, and what you
  type in that row goes to the crew member in this yard, as ever.

The view joins nothing itself: a conversation is what the yard answers for
it ([users.md](users.md#a-conversation), "One history for one person"), so
`yardr mail --with` and a client of [the API](api.md#conversation) have the
same letters.

This is the one way a user of this yard gets a row: without such a grant
their mail is read in `yardr inbox`, as before.

The roster is read when the view opens and again with every event of a link
(`peer_*`): a peer says it without an event of its own, so a name it
exposes newly can show up to the next such event later, or when the view is
opened again. One it exposes no more leaves the list then, and the first
row is selected if that one was.

### A crew member that waits in its pane

A harness can stop at something only a person at its pane can answer: a
login, a permission, a question, a dialog of the system. Mail does not reach
it there. While such a crew member is selected, a notice stands above the line
you type in:

- one line of what yardr knows. For a `blocked` crew member: `<name> waits at a
  prompt in its pane: <harness> may wait at a login, a question or a system
  dialog`. For one whose session runs and whose first prompt the daemon has
  not been able to deliver, whatever state it is listed with: `<harness> was
  started in the pane but is not ready for its first prompt: it may wait at
  a login, a question or a system dialog`, the sentence `yardr init --start`
  prints for a yardmaster in that place. A harness at its login screen is
  listed `not logged in`: its runtime often cannot tell the screen from
  `idle`, the daemon reads the screen itself. Its line is `<name> is not
  logged in: run <harness> in another terminal and log in there; then
  answer the login in its pane or start it fresh (`yardr crew restart
  <name>`)`;
- up to six of the last lines of its pane that say something, behind `│`:
  usually the question itself. They are read every 5 seconds while the
  notice shows. A pane that cannot be read (no herdr on this machine, the
  pane gone) leaves the notice without them;
- the key: `ctrl+g opens its pane; answer there and come back` in herdr,
  `ctrl+g names the command that opens its pane; …` anywhere else.

The notice is no mail and is kept nowhere. It is gone when the crew member
goes on, and when another crew member is selected. What you send meanwhile is
sent, and waits in the crew member's inbox; the line under it says so in the
notice's words.

A pane's lines are shown as text and nothing else: escape sequences and
control characters in them are removed, and nothing in a pane is acted on
by the view or by your terminal.

The notice goes when the daemon has seen the crew member go on, not when you
answered: up to 30 seconds later ([Live](#live)).

### The yard

The row `yard`, above the crew, is the yard reporting: the
notifications of the events you subscribed to
([subscriptions.md](subscriptions.md#users-as-subscribers)), the part
`yardr inbox --notifications` prints. It has no state, since nothing runs
behind it, and shows the number of notifications you have not read, when
there is any.

Selecting it shows the notifications in place of a conversation: oldest at
the top and the newest last, plain. Each is laid out as a mail is: a line
with its time and, where a sender's name would stand, the bead's id, then
the yard's text under it, less the id it would begin with. At 80 columns,
with one unread mail from the yardmaster:

```
yardr chat · jo
[yard]  ada idle  yardmaster idle (1)
────────────────────────────────────────────────────────────────────────────────
10-06 01:00  web-f1qx
  closed (done) by yardmaster reason=merged 5e6f7a8 on "Cache the avatar lookups"

10-06 01:00  web-wh4c
  closed (done) by yardmaster reason=merged 1234567 on "Narrow one"

10-06 01:02  web-hk7z
  advanced (all) by yardmaster from=new to=decide on "Which port for the hook
  listener"

10-06 01:02  web-3cps
  closed (done) by yardmaster reason=merged 1a2b3c4 on "Chat: a row for the yard"
────────────────────────────────────────────────────────────────────────────────

tab next member  pgup/pgdn scroll  ^c quit
```

- **There is no line to type in.** Nobody sent a notification, so there is
  nobody to answer. `enter` and `ctrl+g` do nothing here, and a key that
  would type says `nobody to answer here: the yard reports, tab goes to a
  crew member`. To talk about one, select a crew member and write to it.
- **A notification about a bead has the bead's id alone after its time**,
  whole on its line, so it can be copied into `yardr bead show <id>`. The
  view does not go there itself. A hook delivery's has its source there.
- **Unread notifications light up no crew member.** A crew member's number counts
  the mail from it and nothing else, whoever the notification says acted.
- The list is what the inbox keeps: the newest 500, the oldest dropped.

Without a subscription the row is there and says how it fills:

```
No notifications. The yard reports here what you subscribe to,
for one when beads land:
  yardr subscribe jo yardr --done
```

**Timers.** A mail whose text begins `timer:` was sent by the yard for a
timer the crew member set for you (`yardr timer add --to <you>`,
[timers.md](timers.md)): the crew member wrote the reason when it set the
timer, and was not woken when it fired.

A crew member answers when it is idle and has read its inbox, so an answer takes
as long as its turn does. What it is working on in the meantime is in its
pane, not here.

**Reading.** Opening a conversation marks your unread mail in it as read, and
so does mail that arrives in the open one. A user's mail has one position
for all senders ([users.md](users.md#a-conversation)), so mail of a
conversation you have not opened holds back what came after it: nothing is
marked read that the view did not show. Mail from someone who has no row
(another user, unless a name of a linked yard is [trusted as
them](#a-name-trusted-as-a-user-of-this-yard)) is not shown; the line above
the keys counts it and names `yardr inbox`.

The notifications have a position of their own
([users.md](users.md#the-inbox)): selecting `yard` marks them read and
marks no mail read, and reading a conversation leaves the number on `yard`
as it was.

## Keys

| Key | Does |
|---|---|
| `enter` | sends the line as mail to the selected crew member, [one of a linked yard](#those-of-a-linked-yard) or, in the row of a user of this yard, [whoever wrote last](#a-name-trusted-as-a-user-of-this-yard), a [long text](#a-long-text) as several; nothing on `yard` |
| `ctrl+j`, `alt+enter` | a line break in the line |
| `tab`, `shift+tab` | the next, the previous row: `yard`, then the crew, then those of the linked yards |
| `pgup`, `pgdn` | scroll the conversation |
| `ctrl+g` | to the selected crew member's pane; nothing on `yard`, and one of a linked yard or a user has none here |
| `ctrl+o` | a field to [type a secret](#typing-a-secret) in, for a secret ask the conversation shows; says so where it shows none |
| `esc` | in that field: leave it, and nothing is sent |
| `ctrl+c` | leave |

A mail holds 4 KB. From three quarters of that the rule above the line
counts the bytes (`3072 of 4096 bytes`).

### A file

Mail can carry files ([users.md](users.md#a-file-in-mail)). The view
shows no preview: under the mail's text it prints, dim, where each is,
a file with its name,

```
10-05 13:20  yardmaster
  the yard, painted
  ◆ 1 image: /home/ada/.yardr/blobs/497790947d46…1581 (yard.png)
10-05 13:21  yardmaster
  the report
  ◆ 1 file: /home/ada/.yardr/blobs/8f3a…c41d (report.pdf)
```

with the whole path, for a terminal or another program to open. Mail that
is a file and no text has that line alone. The line takes no file:
`yardr mail <name> <text> --file <path>` sends one.

`ctrl+g` brings the crew member's pane to the front when `yardr chat` runs in a
pane of herdr (`herdr agent focus <agent>`); the view goes on in its own
pane. Anywhere else it names the command that opens the pane from that
terminal: `herdr agent attach <agent>`.

Leaving the view stops nothing: the crew are the daemon's to keep
running, and mail goes on arriving in your inbox (`yardr inbox`).

## A long text

A text longer than one mail is sent as several. Type or paste it and send it as
any other: past one mail's size the rule says how many it comes to
(`9310 bytes, sent as 3 mails`), and Enter sends them one after the other.
Each begins with a line that numbers it:

```
(1/3)
The first paragraphs of the text …
```

They are ordinary mails. The conversation and `yardr mail --with` show three,
the crew member's `yardr inbox` lists three in the order sent, and the numbers
tell it that they are one text and whether more follow. The line under the
last says where they stand.

A text is split where it reads best: at the last paragraph break (an empty
line) that leaves the mail at least half full, else at the last line break,
else at the last space, and a line that has none of these and is longer than
a mail is cut at the limit, between two characters. The break itself is not
sent.

If one of the mails is not taken, those after it are not sent: the line
above the keys says how many went (`1 of 3 mails went to yardmaster, the rest is
in the line: …`), and the text from that mail on is in the line again. Sent
from there it is a text of its own, numbered from 1.

A text is sent as at most 16 mails (about 64 KB); the line takes nothing
past that. More than that is a file, and the mail names it.

Only `yardr chat` splits. `yardr mail <crew member> <text|->` sends one mail and
refuses a text over 4 KB, from stdin too.

## Live

New mail, mail being read and a crew member's state appear without a key
press. They come from the yard's event stream, the one `yardr watch` prints:
the view reads the crew and a conversation once and applies events from
then on, and asks nothing of the yard in a loop. (The pane of a selected
crew member that [waits in it](#a-crew-member-that-waits-in-its-pane) is not the
yard's: herdr is asked for it every 5 seconds, while that notice shows.) When the yardr server goes away (it
replaces itself after an install) the title says so, and the view subscribes
again after the last event it got, so nothing in between is lost.

Two kinds of event exist for this, and anything else that follows the
stream can use them:

- `crew_status`, with `crew`, `status` (`working`, `idle`, `blocked`
  or `unknown`) and `was`: the daemon saw the session do something else
  than on its pass before. The status is also on the crew member
  (`yardr crew show <name> --json`, `status`). The daemon looks on every
  pass: after every change to the store, and every 30 seconds otherwise, so
  a session that starts or ends a turn with nothing else happening in the
  yard shows up to that much later.
- `mail_read`, with `reader` and `through`: a crew member or a user
  acknowledged its inbox through that seq, and mail was among what it
  acknowledged.

The `yard` row follows two more things in the stream. An event that became
a notification names whom it told in its data, `notified` (user to reason),
and the view reads your notifications again when you are among them.
`notifications_read`, with `reader` and `through`, says a user acknowledged
their notifications through that seq.

Neither of the two above is on a bead, and the web view's feed leaves them out, but for a
crew member that stops at a prompt or leaves it.

Mail to and from a linked yard is `mail` in the stream too (`to_peer`,
`from_peer`). On `peer_message_sent` and `peer_message_failed` the view
reads the open conversations with that yard again, for where your mail
stands, and on every `peer_*` event the linked yards, their rosters and whom
they trust as whom.

The view also reads the crew again on `session_prompted` and the other
`session_prompt_*` events of a crew member: a first prompt that arrived changes
no status, and is what ends the notice of a crew member that was not ready for
it.
