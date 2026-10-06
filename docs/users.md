# Users: people with an inbox

A user is a person as a party of the yard: a name and an inbox, as a
[resident](residents.md) has. Residents write to a user through yardr, and
the user reads it, and the whole exchange with a resident, without going to
the resident's pane. The yard itself tells a user of the events they
subscribed to, as notifications, in a part of the inbox apart from the mail.

```sh
yardr user list                          # the users, who is the operator, unread mail
yardr user add ada                       # the operator adds a user
yardr user show ada
yardr user rm ada                        # the mail stays in the log

yardr mail mckean "The refinery is red on main: yardr-1a2b"   # a resident's session, or a user
yardr inbox                              # as a user: Mail and Notifications, each with its unread
yardr inbox --mail --ack                 # one part, and mark what it shows as read
yardr subscribe ada yardr --done         # ada is told of every bead that becomes done
yardr mail --with mayor                  # the exchange with the mayor, both directions
yardr mail --with mayor --after 412 --json
```

## The record

A user has a name, an inbox and the subscriptions that fill its
notifications ([subscriptions.md](subscriptions.md#users-as-subscribers));
`yardr user show <name>` lists them. The record is where more about a person
goes later: who they are in an outside source (as `yardr resident identity`
says for a resident) and a user who comes from another yard. Neither is
built.

**The operator is a user.** `yardr init` and the onboarding add the user
named by the `operator` setting, and `yardr settings set operator <name>`
adds the user of a new name. A yard from before users gets its operator's
user when its server next starts (event `user_added` by `yardr`).

**Only the operator adds and removes users**, under the check `yardr
resident add` uses for what a resident may do: the command's actor is the
`operator` setting. The operator's own user is not removed: name another
operator first.

**One name, one party.** Residents and users share their names, because
mail to a name reaches one inbox: `user add` refuses a resident's name,
`resident add` a user's, and `settings set operator` a resident's. A yard
that already had a resident named as its operator keeps the resident and
gets no user; mail to the name is the resident's, and `yardr doctor` notes
it with what to rename.

## Who is acting

A command acts as a user when its actor is a user's name: `--actor`, else
`$YARDR_ACTOR`, else the login name, as for every command. yardr does not
authenticate anyone: whoever can run yardr on the yard's machine can name
any actor.

## Mail

`yardr mail <user> <text|->` puts a message of up to 4 KB in the user's
inbox. A resident's session sends it, and so does a user. **An agent working
a bead is refused**: what it has to say to a person goes on its bead
(`yardr bead note`), and a question ends its stage with the `question`
outcome. That is a rule of the `mail` command, not of the user's record.

**Nothing nudges a person.** `yardr doctor` (the `mail` line) and bare
`yardr` (a last line under its help) say `N unread from <names>` to the
user who runs them. That count is of mail alone; bare `yardr` adds `and N
notifications` after it when there are unread ones:

```
Mail for jo: 1 unread from mayor, and 2 notifications (yardr inbox).
```

Without unread mail bare `yardr` prints no such line, whatever the
notifications: nobody is talking, and a person who never acknowledges them
would read it under every command. `yardr inbox`, `yardr user show <name>`
and the `yard` row of [`yardr chat`](chat.md#the-yard) count them.

A resident can have the yard tell a user something later, unless a bead
moved first: `yardr timer add --to <user>` ([timers.md](timers.md)). It
arrives as mail from that resident whose text begins `timer:`, and the
resident is not woken for it. A user sets timers for themselves the same
way; `yardr user show` lists the live ones.

Mail from a resident of another yard to a user is not built: a peer's mail
is taken for residents only ([peers.md](peers.md#mail-between-residents)).

## The inbox

A user's inbox has two parts, and `yardr inbox`, run as a user with neither
`--resident` nor `$YARDR_RESIDENT` naming a resident, shows both, each
under its heading with its unread count:

- **Mail**: what a resident or another user wrote to this user, and to
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
    50 10-06 01:02  mail from mayor (read): One question on web-hk7z when you have a minute.
    39 10-06 01:00  mail from mayor (read): Shall I add a reviewer group?

Notifications: 2 unread
    51 10-06 01:02  web-hk7z advanced (all) by mayor from=new to=decide on "Which port for the hook listener"
    53 10-06 01:02  web-3cps closed (done) by mayor reason=merged 1a2b3c4 on "Chat: a row for the yard"
    43 10-06 01:00  (read) web-wh4c closed (done) by mayor reason=merged 1234567 on "Narrow one"
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

## A conversation

`yardr mail --with <name>` sends nothing. It prints the mail between who
runs it and that resident or user, both directions, oldest first:

```
   412 10-05 14:02  read    mckean: deploys are frozen until Friday
   415 10-05 14:03  read    mayor: noted. Until when on Friday?
   431 10-05 16:40  unread  mckean: until the release is tagged
```

The columns are the mail's seq, when it was sent, whether its receiver has
read it, the sender and the text. A resident's session reads the same
exchange with the user's name: `yardr mail --with mckean`.

`--after <seq>` prints only what is newer, and `--json` prints a list of
`{seq, at, from, to, text, read}`: a view asks again with the last seq it
has.

`yardr mail --with <resident>@<peer>` is the same for a resident of a
linked yard ([peers.md](peers.md#mail-between-residents)); what was sent
there says `queued`, `sent` or `failed` in place of read.

**Read** means acknowledged by the receiver, with `yardr inbox --ack`: a
user's own, for the mail part of [the inbox](#the-inbox), or a resident's,
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
not a user or resident has them ([groups.md](groups.md)). `yardr doctor`
notes a member that is neither, since nobody of that name can be mailed;
nothing is refused.
