# yardr chat: talking with the residents

`yardr chat` is one view of the yard's [residents](residents.md) and your
conversation with each, in a terminal. It is the same for every harness a
resident runs, and you do not have to find the resident's pane to say
something to it.

```sh
yardr chat             # opens on the mayor
yardr chat refinery    # opens on the resident named
```

It shows the mail a [user](users.md) and a resident have written each other,
and what you type is mail: `yardr chat` adds no other channel. What it shows
is what `yardr mail --with <resident>` prints, and Enter does what
`yardr mail <resident> <text>` does.

## Who it is for

A user of the yard: the operator, and whoever the operator added with
`yardr user add`. The view acts as the user the command acts as (`--actor`,
else `$YARDR_ACTOR`, else the login name). Anyone else is told to ask the
operator for `yardr user add <name>`.

It needs a terminal. Without one (a script, a pipe, `--json`) it does not
start and names what does the same there: `yardr mail <resident> <text>`
sends, `yardr mail --with <resident> [--after <seq>] [--json]` reads the
conversation.

It shows residents only: no agents working a bead (their channel is the
bead) and no residents of other yards.

## The view

At 80 columns:

```
yardr chat · ada
[mayor blocked]  refinery idle (1)
────────────────────────────────────────────────────────────────────────────────
10-05 13:20  ada
  deploys are frozen until Friday
  · read

10-05 13:20  mayor
  noted. Until when on Friday?

10-05 13:20  ada
  until the release is tagged
  · mayor waits at a prompt in its pane and cannot read mail (ctrl+g goes there)

────────────────────────────────────────────────────────────────────────────────
> Write to mayor
>
>

enter send  ^j new line  tab next resident  pgup/pgdn scroll  ^g pane  ^c quit
```

From 100 columns the residents are a column on the left, one a line, with
`>` before the selected one; narrower, they fold into the one line on top,
the selected one in brackets.

The colours are those of the onboarding's forms, for a light and a dark
terminal, and say nothing the words and marks do not; with `NO_COLOR` set,
or in a terminal without colour, the view is plain text.

**The residents.** Each with its state and, when there is any, the number of
mails from it you have not seen:

| State | Means |
|---|---|
| `not running` | no session runs: it was stopped, or the daemon has not started it yet |
| `working` | its session is in a turn |
| `idle` | its session waits for a prompt; the daemon nudges it about new mail |
| `blocked` | its session waits at a prompt only a person in its pane can answer (a permission, a question) |
| `failed to start` | its last start failed; `yardr resident show <name>` says why |
| `running` | a session runs and the daemon has not said more yet, or its runner cannot tell |

**The conversation** with the selected resident: oldest at the top, the
newest in view, each mail with its time and sender. `pgup` and `pgdn` scroll.

**Where a mail you sent stands** is the line under it. Mails that stand
alike share one line, under the last of them:

| Line | Means |
|---|---|
| `read` | the resident acknowledged it (`yardr inbox --ack`) |
| `sent; <name> is idle` | it is in the inbox, and the daemon nudges the idle session |
| `sent; <name> is working and reads mail when idle` | mail does not interrupt a turn |
| `<name> waits at a prompt in its pane and cannot read mail (ctrl+g goes there)` | someone has to answer the prompt in its pane first |
| `<name> is not running: yardr resident start <name>` | the mail waits in its inbox for its next session |
| `<name> failed to start: yardr resident show <name>` | as above, and that command says why |

**Timers.** A mail whose text begins `timer:` was sent by the yard for a
timer the resident set for you (`yardr timer add --to <you>`,
[timers.md](timers.md)): the resident wrote the reason when it set the
timer, and was not woken when it fired.

A resident answers when it is idle and has read its inbox, so an answer takes
as long as its turn does. What it is working on in the meantime is in its
pane, not here.

**Reading.** Opening a conversation marks your unread mail in it as read, and
so does mail that arrives in the open one. A user's inbox has one position
for all senders ([users.md](users.md#a-conversation)), so mail of a
conversation you have not opened holds back what came after it: nothing is
marked read that the view did not show. Mail from someone who is no resident
(another user) is not shown; the line above the keys counts it and names
`yardr inbox`.

## Keys

| Key | Does |
|---|---|
| `enter` | sends the line as mail to the selected resident |
| `ctrl+j`, `alt+enter` | a line break in the line |
| `tab`, `shift+tab` | the next, the previous resident |
| `pgup`, `pgdn` | scroll the conversation |
| `ctrl+g` | to the selected resident's pane |
| `ctrl+c` | leave |

A mail holds 4 KB. From three quarters of that the rule above the line
counts the bytes (`3072 of 4096 bytes`), and the line takes nothing past the
limit, so a mail is never refused for its size after it was typed.

`ctrl+g` brings the resident's pane to the front when `yardr chat` runs in a
pane of herdr (`herdr agent focus <agent>`); the view goes on in its own
pane. Anywhere else it names the command that opens the pane from that
terminal: `herdr agent attach <agent>`.

Leaving the view stops nothing: the residents are the daemon's to keep
running, and mail goes on arriving in your inbox (`yardr inbox`).

## Live

New mail, mail being read and a resident's state appear without a key
press. They come from the yard's event stream, the one `yardr watch` prints:
the view reads the residents and a conversation once and applies events from
then on, and asks nothing in a loop. When the yardr server goes away (it
replaces itself after an install) the title says so, and the view subscribes
again after the last event it got, so nothing in between is lost.

Two kinds of event exist for this, and anything else that follows the
stream can use them:

- `resident_status`, with `resident`, `status` (`working`, `idle`, `blocked`
  or `unknown`) and `was`: the daemon saw the session do something else
  than on its pass before. The status is also on the resident
  (`yardr resident show <name> --json`, `status`). The daemon looks on every
  pass: after every change to the store, and every 30 seconds otherwise, so
  a session that starts or ends a turn with nothing else happening in the
  yard shows up to that much later.
- `mail_read`, with `reader` and `through`: a resident or a user
  acknowledged its inbox through that seq, and mail was among what it
  acknowledged.

Neither is on a bead, and the web view's feed leaves them out, but for a
resident that stops at a prompt or leaves it.
