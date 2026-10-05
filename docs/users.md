# Users: people with an inbox

A user is a person as a party of the yard: a name and an inbox, as a
[resident](residents.md) has. Residents write to a user through yardr, and
the user reads it, and the whole exchange with a resident, without going to
the resident's pane.

```sh
yardr user list                          # the users, who is the operator, unread mail
yardr user add ada                       # the operator adds a user
yardr user show ada
yardr user rm ada                        # the mail stays in the log

yardr mail mckean "The refinery is red on main: yardr-1a2b"   # a resident's session, or a user
yardr inbox                              # as a user: your unread mail, then the latest read
yardr inbox --ack                        # mark what is shown as read
yardr mail --with mayor                  # the exchange with the mayor, both directions
yardr mail --with mayor --after 412 --json
```

## The record

A user has a name and an inbox. The record is where more about a person
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

`yardr inbox`, run as a user with neither `--resident` nor
`$YARDR_RESIDENT` naming a resident, lists the mail to that user and to
nobody else: the unread, oldest first, then the latest read, marked
`(read)`; `-n` bounds how many. `--ack` marks the unread it shows as read.

**Nothing nudges a person.** `yardr doctor` (the `mail` line) and bare
`yardr` (a last line under its help) say `N unread from <names>` to the
user who runs them.

Mail from a resident of another yard to a user is not built: a peer's mail
is taken for residents only ([peers.md](peers.md#mail-between-residents)).

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

**Read** means acknowledged by the receiver, with `yardr inbox --ack`: a
user's own, or a resident's, whose acknowledgement covers its whole inbox
through the last event shown. Mail to a party that has since been removed
counts as unread.

From Go the same reads are `store.API`'s `Conversation`, `UserInbox`,
`AckUserInbox`, `Users` and `GetUser`.

## Members of groups

A manual group's `--members` are names, taken as they are given, whether or
not a user or resident has them ([groups.md](groups.md)). `yardr doctor`
notes a member that is neither, since nobody of that name can be mailed;
nothing is refused.
