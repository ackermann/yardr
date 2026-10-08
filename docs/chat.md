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
[`yard`](#the-yard).

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
| `running` | a session runs and the daemon has not said more yet, or its runner cannot tell |

Above the crew stands one row that is no crew member, `yard`
([below](#the-yard)).

**The conversation** with the selected crew member: oldest at the top, the
newest in view, each mail with its time and sender. `pgup` and `pgdn` scroll.

**Where a mail you sent stands** is the line under it. Mails that stand
alike share one line, under the last of them:

| Line | Means |
|---|---|
| `read` | the crew member acknowledged it (`yardr inbox --ack`) |
| `sent; <name> is idle` | it is in the inbox, and the daemon nudges the idle session |
| `sent; <name> is working and reads mail when idle` | mail does not interrupt a turn |
| `<name> waits at a prompt in its pane and cannot read mail (ctrl+g goes there)` | someone has to answer the prompt in its pane first: it is `blocked`, or its first prompt is not delivered yet ([below](#a-crew-member-that-waits-in-its-pane)) |
| `<name> is not running: yardr crew start <name>` | the mail waits in its inbox for its next session |
| `<name> failed to start: yardr crew show <name>` | as above, and that command says why |

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
from a peer, and each part is one.

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
  yardmaster  idle               │ name is in this conversation, and what you write goes to mckean in
> mckean                      1  │ this yard
  yardmaster@airy                │
                                 │ 10-08 14:02  mckean@airy
                                 │   I am at the other machine until four
                                 │
                                 │ 10-08 14:05  ada
                                 │   fine, the review can wait
                                 │   · sent
```

- **One conversation, one number.** It has the mail between you and the user
  in this yard and the mail between you and each name trusted as them, in
  the order written, each under the name it was sent as. The row counts the
  unread of all of it.
- **Enter writes to the user in this yard**, as `yardr mail mckean <text>`
  does: the trusted name is how they spoke, not where they are reached. It
  is in their inbox here, and nothing is queued for the other yard. To write
  to them there, `yardr mail mckean@airy <text>`; that mail shows in this
  conversation too, with its delivery under it.
- **The first line says so**: that this is a person of this yard, which
  names are trusted as them, and where what you write goes. The row has no
  state and no pane, as a person has neither.
- **It goes by the grants as they are now**, not by the mark a mail got when
  it came. A grant given while the view is open moves the name's mail into
  the user's conversation with the next event of the link, and one taken
  back (`--no-trust`, the user removed, the name exposed no more) gives the
  name its row and its mail back. Mail the user wrote you in this yard then
  has no row again, and is counted on the line above the keys.
- `yardr chat mckean@airy` opens the user's row.

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
  prints for a yardmaster in that place. A harness at a login screen is often
  listed `idle`: its runtime cannot tell the two apart, the daemon can,
  because the prompt it sent never showed;
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
| `enter` | sends the line as mail to the selected crew member, [one of a linked yard](#those-of-a-linked-yard) or [user of this yard](#a-name-trusted-as-a-user-of-this-yard), a [long text](#a-long-text) as several; nothing on `yard` |
| `ctrl+j`, `alt+enter` | a line break in the line |
| `tab`, `shift+tab` | the next, the previous row: `yard`, then the crew, then those of the linked yards |
| `pgup`, `pgdn` | scroll the conversation |
| `ctrl+g` | to the selected crew member's pane; nothing on `yard`, and one of a linked yard or a user has none here |
| `ctrl+c` | leave |

A mail holds 4 KB. From three quarters of that the rule above the line
counts the bytes (`3072 of 4096 bytes`).

### An image

Mail can carry images ([users.md](users.md#an-image-in-mail)). The view
shows no picture: under the mail's text it prints, dim, where each is,

```
10-05 13:20  yardmaster
  the gate, painted
  1 image: /home/ada/.yardr/blobs/497790947d46…1581
```

with the whole path, for a terminal or another program to open. Mail that
is an image and no text has that line alone. The line takes no image:
`yardr mail <name> <text> --file <image>` sends one.

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
