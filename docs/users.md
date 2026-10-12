# Users: people with an inbox

A user is a person as a party of the yard: a name and an inbox, as a
[crew member](crew.md) has. Crew members write to a user through yardr, and
the user reads it, and the whole exchange with a crew member, without going to
the crew member's pane. The yard itself tells a user of the events they
subscribed to, as notifications, in a part of the inbox apart from the mail.
A user who names a source for it is also told outside the yard, on a phone,
of what lands in that inbox.

```sh
yardr user list                          # the users, who is the operator, unread mail; then whom the linked yards expose
yardr user add ada                       # the operator adds a user
yardr user show ada
yardr user rm ada                        # the mail stays in the log
yardr user set ada notify=pushover env_file=~/.yardr/users/ada.env   # told on a phone, through a source

yardr mail mckean "The assembly is red on main: yardr-1a2b"   # a crew member's session, or a user
yardr inbox                              # as a user: Mail and Notifications, each with its unread
yardr inbox --mail --ack                 # one part, and mark what it shows as read
yardr subscribe ada yardr --done         # ada is told of every bead that becomes done
yardr mail --with yardmaster                  # the exchange with the yardmaster, both directions
yardr mail --with yardmaster --after 412 --json
```

## The record

A user has a name, an inbox and the subscriptions that fill its
notifications ([subscriptions.md](subscriptions.md#users-as-subscribers));
`yardr user show <name>` lists them. It also has the source that tells
the user outside the yard and the path of the file their address is read
from ([below](#told-outside-the-yard)). The record is where more about a
person goes later: who they are in an outside source (as `yardr crew
identity` says for a crew member) and a user who comes from another yard.
Neither is built.

**The operator is a user.** `yardr init` and the onboarding add the user
named by the `operator` setting, and `yardr settings set operator <name>`
adds the user of a new name. A yard from before users gets its operator's
user when its server next starts (event `user_added` by `yardr`).

**Only the operator adds and removes users**, under the check `yardr
crew add` uses for what a crew member may do: the command's actor is the
`operator` setting. The operator's own user is not removed: name another
operator first.

**One name, one party.** Crew members and users share their names, because
mail to a name reaches one inbox: `user add` refuses a crew member's name,
`crew add` a user's, and `settings set operator` a crew member's. A yard
that already had a crew member named as its operator keeps the crew member and
gets no user; mail to the name is the crew member's, and `yardr doctor` notes
it with what to rename.

## Who is acting

A command acts as a user when its actor is a user's name: `--actor`, else
`$YARDR_ACTOR`, else the login name, as for every command. yardr does not
authenticate anyone: whoever can run yardr on the yard's machine can name
any actor.

When the actor is the login name (neither `--actor` nor `$YARDR_ACTOR`
given) and it is neither a user nor a crew member, a command that acts as
one (`inbox`, `mail`, `reply`, `chat`) refuses in one line: who acts and
where that came from, the yard's users, and both ways to act as one:

```
$ USER=kloni yardr inbox
yardr: kloni (from $USER) is neither a user nor a crew member of this yard; its users are konrad. Act as one: --actor konrad or export YARDR_ACTOR=konrad
```

The onboarding form's "Your name" is therefore who its terminal acts as: it
starts at that name and takes no other, since the operator under a name
nobody acts as would leave the person who made the yard no user of it. To
be the operator under another name, set `YARDR_ACTOR` to it before `yardr`.

## Mail

`yardr mail <user> <text|->` puts a message of up to 4 KB in the user's
inbox. A crew member's session sends it, and so does a user. **An agent working
a bead is refused**: what it has to say to a person goes on its bead
(`yardr bead note`), and a question ends its stage with the `question`
outcome. That is a rule of the `mail` command, not of the user's record.

A crew member answers mail with `yardr reply <seq> <text|->`, the number
its inbox line starts with: the reply is mail to that mail's sender.
`yardr mail` is for writing first.

### An image in mail

`--file <path>` sends an image with the mail, a PNG or a JPEG, and is given
once for each image, up to 8. The text may then be left out:

```
$ yardr mail yardmaster "The yard, painted" --file yard.png
mail sent to yardmaster  1 image: /home/mckean/.yardr/blobs/497790947d46…1581
```

The yard keeps the image in its home, in `blobs/`, in a file named by the
SHA-256 of its bytes: sent twice, it is there once, and it is never
changed. Wherever the mail is read, its line ends with `1 image: <path>`
(`2 images: <path> <path>`): `yardr inbox` for a user and for a crew member,
`yardr mail --with`, the nudge a crew member gets for its inbox, and
[`yardr chat`](chat.md#an-image), which prints it under the text. A crew
member reads the picture by opening that file, as it reads any other; yardr
shows none itself. In `--json` the letter has `files`, a list of
`{hash, type, bytes}`.

What a file is is read from its bytes, not its name: anything but a PNG or
a JPEG is refused, and so is one over the yard's limit, 8 MiB unless
`yardr settings set blob_max_bytes <bytes>` says otherwise. Nothing is
stored then, and the mail is not sent.

**To a linked yard** an image goes with `yardr mail <name>@<peer> --file`
when the link carries the kind `file` (`yardr peer allow <peer> --send
file`), and one comes in when this yard's link takes it (`--receive file`).
Where the other yard does not take it the mail still arrives, with `1 image:
not taken: <reason>` in place of the path
([peers.md](peers.md#an-image-with-the-mail)).

An image is removed by `yardr store prune`, and by the daemon's hourly
prune, once no mail the yard still has names it
([configuration.md](configuration.md#retention-keeping-yarddb-bounded)).
Nothing else deletes one.

**Nothing nudges a person inside the yard.** A user who named a source is
told on their phone ([below](#told-outside-the-yard)); for everyone else,
and as well, `yardr doctor` (the `mail` line) and bare
`yardr` (a last line under its help) say `N unread from <names>` to the
user who runs them. That count is of mail alone; bare `yardr` adds `and N
notifications` after it when there are unread ones:

```
Mail for jo: 1 unread from yardmaster, and 2 notifications (yardr inbox).
```

Without unread mail bare `yardr` prints no such line, whatever the
notifications: nobody is talking, and a person who never acknowledges them
would read it under every command. `yardr inbox`, `yardr user show <name>`
and the `yard` row of [`yardr chat`](chat.md#the-yard) count them.

A crew member can have the yard tell a user something later, unless a bead
moved first: `yardr timer add --to <user>` ([timers.md](timers.md)). It
arrives as mail from that crew member whose text begins `timer:`, and the
crew member is not woken for it. A user sets timers for themselves the same
way; `yardr user show` lists the live ones.

**To and from a linked yard.** A user writes to a crew member or a person
of a linked yard with `yardr mail <name>@<peer> <text|->` (up to 16 KB), and
from [`yardr chat`](chat.md#those-of-a-linked-yard), where each of them has a
row. The link's `send mail` is the grant (`yardr peer allow <peer> --send
mail`, the operator's): a user has no `peers` setting, that one is a crew
member's. The mail is queued and the daemon delivers it; what the other yard
does not take comes back to the user's inbox as mail from `yard` that says
why. An agent working a bead is refused here too.

Whom a user can write to is the roster each linked yard last said
([peers.md](peers.md#the-roster-whom-a-peer-may-see-and-write-to)), and
`yardr user list` prints it under this yard's users:

```text
NAME  ROLE      UNREAD
ada   -         0
jo    operator  1

OF A LINKED YARD  IS      MAIL  ROLE
mckean@north      person  yes   operator
yardmaster@north  crew    yes   Yardmaster: shapes the beads
```

`MAIL` is whether mail from this yard reaches them: `no (yardr peer allow
north --send mail)` when this yard's link sends none, `no (north takes none
from this yard)` when theirs takes none. A peer that exposes nobody adds no
rows, and a yard none of whose peers do prints its users alone. It is `user
list` that has them and not `crew list`: that one is the table of this
yard's sessions (state, pane, runner), none of which a roster says, and
this is the list of whom one writes to. `--json` stays the list of this
yard's users; the roster is in `yardr peer list --json` (`said.roster`).

The answer of someone there lands in the user's inbox as mail from
`<name>@<peer>` once this yard's link takes mail from that yard and exposes
the user to it (`yardr peer allow <peer> --receive mail --expose <user>`),
and is one conversation with what the user wrote: `yardr mail --with
<name>@<peer>`, and the row in `yardr chat`
([peers.md](peers.md#mail-between-crew-members)).

## The inbox

A user's inbox has two parts, and `yardr inbox`, run as a user with neither
`--crew` nor `$YARDR_CREW` naming a crew member, shows both, each
under its heading with its unread count:

- **Mail**: what a crew member or another user wrote to this user, and to
  nobody else. Somebody is talking, and can be answered.
- **Notifications**: what the yard reports of the events the user
  subscribed to
  ([subscriptions.md](subscriptions.md#users-as-subscribers)), one line
  each. Nobody sent them, so there is nobody to answer.

Each part lists its unread, oldest first, then the latest read, the newest
of them first, marked `(read)`; `-n` bounds how many in each. `--mail` and
`--notifications` show one part.

```
Mail: nothing unread; the latest read:
    50 10-06 01:02  mail from yardmaster (read): One question on web-hk7z when you have a minute.
    39 10-06 01:00  mail from yardmaster (read): Shall I add a reviewer group?

Notifications: 2 unread
    51 10-06 01:02  web-hk7z advanced (all) by yardmaster from=new to=decide on "Which port for the hook listener"
    53 10-06 01:02  web-3cps closed (done) by yardmaster reason=merged 1a2b3c4 on "Chat: a row for the yard"
    43 10-06 01:00  (read) web-wh4c closed (done) by yardmaster reason=merged 1234567 on "Narrow one"
```

A part with nothing in it is `Mail: none` or `Notifications: none`. The
first column is the seq, the same in both parts: it orders a mail and a
notification against each other.

**Each part is acknowledged apart.** `--ack` marks the unread it shows as
read: with `--mail` the mail, with `--notifications` the notifications,
with neither both, since both were shown. Acknowledging the mail leaves the
notifications unread, and the other way round: a user has two positions,
one a part. So a stream of notifications nobody reads does not hold back
what counts as read mail, and reading through the notifications marks no
mail read.

A yard from before notifications kept one position for the whole inbox.
Its store is migrated when the server next opens it (schema 4): what a
user had acknowledged is acknowledged in both parts.

The notifications are bounded: the newest 500 are kept and the oldest
dropped, read or not. Mail is not.

## Told outside the yard

A user names a source that can tell a person
([integrations.md](integrations.md#tell-a-user-outside-the-yard)), and from
then on what lands in their inbox is sent through it: to a phone, with
Pushover as the source ([pushover.md](pushover.md) sets that up from
nothing).

```sh
yardr user set ada notify=pushover env_file=~/.yardr/users/ada.env
yardr source check pushover --user ada      # one real test message
yardr user show ada                         # notify, the env file, and how the last message ended
yardr user set ada notify=                  # nowhere; what was still to be told is dropped
```

`notify` is the source. `env_file` is a file of `KEY=VALUE` lines that sets
the variable the source's file names for a person's address
(`notify.address.env`; for Pushover `PUSHOVER_USER`, the person's own user
key). It is held to the rule of every env file: a regular file of mode 0600
or stricter, read at each send. The store keeps the path, never what is in
it, and a file that is open to others or does not set the name is refused
when it is set. The source's own credential is the yard's and in another
file (`yardr source set <source> env_file=…`): the application's token is
one thing, a person's address another.

**Who may.** The user themselves and the operator. It is refused from the
pane of an agent, whoever the command says it is, and from a crew member:
whoever sets these two decides where a person's inbox is sent. Like
every such check in yardr it is a line, not a wall: every agent runs as the
one OS user ([Who is acting](#who-is-acting)).

**What is told.** Mail to the user, their own too, so also a timer they
set for themselves; and the notifications of their subscriptions, which
never include what the user did themselves
([subscriptions.md](subscriptions.md#users-as-subscribers)). Nothing else,
and nothing that is not in the inbox: to be told of a kind of event,
subscribe to it; not to be, do not.

**What leaves the machine.** A fixed line that says who and what kind, and
the first words of the text: the
message goes to the service's servers and onto a lock screen.

| in the inbox | title | text |
|---|---|---|
| mail | `mail from yardmaster` | the first 200 characters |
| a fired timer | `timer from mckean` | the first 200 characters (`timer: …`) |
| a notification of a bead | `web-hk7z advanced` | its line as the inbox has it: `web-hk7z advanced (all) by yardmaster from=new to=decide on "Which port for the hook listener"` |
| a notification of a hook delivery | `github hook` | its line, without the link it ends with |
| several | `5 new in your yardr inbox` | each kind once, with how many: `mail from yardmaster (4), web-hk7z advanced` |

No link is sent. The source cuts a title or a text to what its call takes
(a parameter's `max`).

**When.** The daemon sends, so it must be running. A thing is told once it
has been in the inbox for 20 seconds and is still unread: what was
acknowledged by then, by `yardr chat` showing it or `yardr inbox --ack`, is
not told. Everything due for one user goes as **one message**, and a
message waits for a burst to end: it goes once nothing new came for 20
seconds, at the latest 60 seconds after its oldest item. At most **20
messages an hour** for one user; the twentieth says it is the last of the
hour, and what is over waits and goes as one when the hour has room.

**When the source fails.** The inbox is filled first, in the same write as
the mail or notification, and never waits for a source: with the source
down, misconfigured or removed, `yardr inbox` and `yardr chat` show
everything at once. A send that gets no answer, a 5xx, a 408 or a 429 is
tried again after 30 seconds, then with doubling waits, for up to **8
tries** and until the thing is **6 hours** old; then it is given up. Any
other 4xx is final at once, and so is a user or a source that is not set
up (a missing env file, one open to others, a name it does not set). Giving
up is the event `push_failed`, about no bead: the user, the source, the
reason (`not_set_up`, `refused`, `gave_up`, `expired`), the status, how
many of the inbox it covered. The address, the token, the title and the
text are in no event and no log, and nothing of a request is. A message
that was taken leaves no event.

**How it stands.** `yardr user show <name>` has `last told: <when> through
<source>, taken (status 200, 2 of the inbox in one message)`, or `failed
(…); next try <time>` while the daemon still tries, or `not sent: <reason>
(…)`. `yardr doctor` notes a user whose last message was not taken
([doctor.md](doctor.md)). A crew member hears of `push_failed` with `--on
push_failed`; a user cannot subscribe to it, so a failed message never
queues another.

None of the numbers above is a setting, and there is one source per user.

## A conversation

`yardr mail --with <name>` sends nothing. It prints the mail between who
runs it and that crew member or user, both directions, oldest first:

```
   412 10-05 14:02  read    mckean: deploys are frozen until Friday
   415 10-05 14:03  read    yardmaster: noted. Until when on Friday?
   431 10-05 16:40  unread  mckean: until the release is tagged
```

The columns are the mail's seq, when it was sent, whether its receiver has
read it, the sender and the text, which ends with `1 image: <path>` on mail
that carries [one](#an-image-in-mail). A crew member's session reads the same
exchange with the user's name: `yardr mail --with mckean`.

`--after <seq>` prints only what is newer, and `--json` prints a list of
`{seq, at, from, to, text, read}`: a view asks again with the last seq it
has.

`yardr mail --with <name>@<peer>` is the same for a crew member or a person
of a linked yard ([peers.md](peers.md#mail-between-crew-members)); what was sent
there says `queued`, `sent` or `failed` in place of read.

**Read** means acknowledged by the receiver, with `yardr inbox --ack`: a
user's own, for the mail part of [the inbox](#the-inbox), or a crew member's,
whose acknowledgement covers its whole inbox through the last event shown.
Mail to a party that has since been removed counts as unread.
An acknowledgement that passes mail is an event, `mail_read` (with `reader`
and `through`), so a view that follows the stream learns of it. One that
passes a user's notifications is `notifications_read`, with the same two
fields.

[`yardr chat`](chat.md) is this exchange as a view in the terminal, with a
line to type.

From Go the same reads are `store.API`'s `Conversation`, `UserInbox`,
`AckUserInbox`, `Users` and `GetUser`; the notifications are
`UserNotifications` and `AckUserNotifications`.

## Members of groups

A manual group's `--members` are names, taken as they are given, whether or
not a user or crew member has them ([groups.md](groups.md)). `yardr doctor`
notes a member that is neither, since nobody of that name can be mailed;
nothing is refused.
