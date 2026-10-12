# Users: people with an inbox

A user is a person as a party of the yard: a name and an inbox, as a
[crew member](crew.md) has. Crew members write to a user through yardr, and
the user reads it, and the whole exchange with a crew member, without going to
the crew member's pane. The yard itself tells a user of the events they
subscribed to, as notifications, in a part of the inbox apart from the mail.
A user who names a source for it is also told outside the yard, on a phone,
of what lands in that inbox. A user's [device](#devices) is a program of
theirs that calls the yard from outside it, let in by a token.

```sh
yardr user list                          # the users, who is the operator, unread mail, where each is told; then whom the linked yards expose
yardr user add ada                       # the operator adds a user
yardr user show ada
yardr user rm ada                        # the mail stays in the log
yardr user set ada notify=pushover env_file=~/.yardr/users/ada.env   # told on a phone, through a source
yardr user device add ada phone          # the operator: a device of ada's, and its token, shown once
yardr user device pair ada phone --qr    # or a code the device calls with, and is given its token for
yardr user device list                   # every device, when added, when and from where last used
yardr user device rm ada phone           # the operator or ada: the token is refused from now on

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
from ([below](#told-outside-the-yard)), and their
[devices](#devices). The record is where more about a
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
operator first. Removing a user removes their devices, and the event
`user_removed` names them (`devices`).

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
then on what lands in their inbox is sent through it: to a phone, through
the `telegram` or `pushover` pack ([pushover.md](pushover.md) sets Pushover
up from nothing).

```sh
yardr user set ada notify=pushover env_file=~/.yardr/users/ada.env
yardr source check pushover --user ada      # one real test message
yardr user show ada                         # notify, the env file, and how the last message ended
yardr user set ada notify=                  # nowhere; what was still to be told is dropped
```

`yardr user list` shows where each user is told. `notify` is the source.
`env_file` is a file of `KEY=VALUE` lines that sets
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
([subscriptions.md](subscriptions.md#users-as-subscribers)). And every
**ask for the user** (`yardr bead ask ... --to ada`), which is in no inbox
but in their queue of decisions: a question is what waits for a person, so
it is told without a subscription. The yard that puts the ask in the
person's queue tells it, for its own asks and for those
[another yard asked](peers.md#questions-for-a-person-of-another-yard).
Nothing else: to be told of a kind of event, subscribe to it; not to be,
do not.

**What leaves the machine.** A fixed line that says who and what kind, and
the first words of the text: the
message goes to the service's servers and onto a lock screen.

| what | title | text |
|---|---|---|
| mail | `mail from yardmaster` | the first 200 characters |
| a fired timer | `timer from mckean` | the first 200 characters (`timer: …`) |
| a notification of a bead | `web-hk7z advanced` | its line as the inbox has it: `web-hk7z advanced (all) by yardmaster from=new to=decide on "Which port for the hook listener"` |
| a notification of a hook delivery | `github hook` | its line, without the link it ends with |
| an ask for the user | `yardmaster asks`; `yardmaster@north asks` for another yard's | the first 200 characters of the question; not its context, its options, its bead or the bead's title |
| several | `5 new in your yardr inbox`; `5 new in yardr` with an ask among them | each kind once, with how many: `mail from yardmaster (4), web-hk7z advanced` |

No link is sent. The source cuts a title or a text to what its call takes
(a parameter's `max`).

**When.** The daemon sends, so it must be running. A thing is told once it
has been in the inbox for 20 seconds and is still unread: what was
acknowledged by then, by `yardr chat` showing it or `yardr inbox --ack`, is
not told. An ask is told once it has been in the queue for 20 seconds and is
still open: one that was answered, declined or put aside by then is not
told, and reading the inbox does not answer it. It is told once, and not
again when one put aside comes back. Where a subscription made a
notification of the same note, the two are one item, the ask. Everything
due for one user goes as **one message**, and a
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

## Devices

A device is one program of one user on one machine or phone that calls the
yard from outside it: a window that is not on the yard's machine. The yard
gives it a token, and a call that carries the token is made as that user.
A yard needs nothing else for it, no identity service and no login.

**Where it calls.** On the yard's listener, the address of the setting
`hook_addr` ([configuration.md](configuration.md#settings)), under
`/user/`, with the token in `Authorization: Bearer <token>`. The paths, the
answers and what a client must do about TLS are in
[api.md](api.md#over-the-network). A yard without `hook_addr` listens
nowhere, and a token lets nothing in; a yard without a device answers 404
under `/user/`.

```text
$ yardr user device add mckean phone
device phone of mckean. Its token, shown once:

  yardr-dev-7Kq2…<43 characters>

Give it to the device with this yard's address, the one the device will reach it at.
It calls as mckean until: yardr user device rm mckean phone

$ yardr user device list
USER    DEVICE  ADDED        LAST USED
mckean  airy    10-12 19:05  never
mckean  phone   10-12 19:02  10-12 19:40 from 100.64.0.7

$ yardr user device rm mckean phone
device phone of mckean removed: its token is refused from now on, and the streams it held are closed
```

**The token** is 32 random bytes after `yardr-dev-`, printed once, by `device
add` (with `--json` the field `token`), or given once to the device itself,
which called with a code ([below](#enrolled-by-a-code)). The store keeps its SHA-256 beside
the device's name, its user, who added it and when, and when and from which
address it was last used; it never keeps the token, and no command, event
or log has it. A token that is lost is not read again: remove the device
and add it anew. The yard does not know the address it is reached at from
outside, since a proxy or a tunnel of yours may stand in front, so `device
add --url <address>` only prints the address under the token, to hand both
over together.

**The name** is yours to choose (`phone`, `laptop`), of the characters a
user's name has, and one of a kind among that user's devices: a second
`phone` of one user is refused, and two users may each have one. `yardr user
device list <user>` lists one user's.

**A token does not end by itself.** It is refused once its device is
removed, or its user (`yardr user rm`). `LAST USED` is when a call last
carried it and the address that call came from, written at most once a
minute while the address stays the same; behind a proxy that is the proxy's
address. `yardr doctor` notes a device that has not been used for 30 days,
or was never used 30 days after it was added, with the command that removes
it ([doctor.md](doctor.md)); nothing is removed for you.

**Who may.** Only the operator adds a device, with `device add` or by
opening a pairing for it, under the check `yardr user
add` uses, and never from the pane of an agent, whoever the command says it
is, as for [`user set notify=`](#told-outside-the-yard): whoever adds a
device decides who may speak as a person. The user themselves does not add
one. The operator and the user whose it is remove one. Like every such
check in yardr it is a line, not a wall ([Who is acting](#who-is-acting)).

**What a device reads and writes.** The 21 calls of
[api.md](api.md#the-calls), the event stream and the images of mail, and
nothing else the yard's socket serves: no command of the operator's, no
move of a bead and no setting is on the listener, whoever calls. A device
of the operator has one call more, and no other device has it: it gives
the credential a [secret ask](chat.md#questions-for-you) asks for
([api.md](api.md#for-the-operator-alone)), typed where the operator sits
and written by the yard into the source's file. No call reads a credential
back, for any device.

It writes as its user and as nobody else: mail to a crew member, a user or
a name a linked yard exposes, an answer to an ask (a pick, words, put aside,
declined), a note on a bead, and the acknowledgement of its user's own mail
and notifications. A call that names another user or a crew member as its
writer, or another user's inbox, is refused. The yardmaster does what the
operator asks by mail, so a device of the operator can have work shaped,
built and landed; it cannot run what is [refused from an agent's
pane](#who-is-acting) either, since no such command is among the calls.

It reads what any user of the yard can read at the yard's terminal: every
bead with its notes, the crew, the users, and through the stream the mail
of everyone in the yard. That is true of a device of any user, so `device
add` for a user who is not the operator says it. It is also what a lost
token gives away until its device is removed.

**A stolen device of the operator can replace a source's credential, and
cannot read one.** Until `yardr user device rm`, whoever holds its token
can answer an open secret ask with a value of their own, and the source's
next call is made with it: to the host the operator declared, with a key
that is not the operator's. They cannot ask for a secret (an ask is an
agent's or the terminal's to write), cannot name a file or a source that
no open ask names, and read no credential by any call. Each such giving is
an event with `via <device>` and the names, never the value. A device of
any other user, and a credential that anything but the yard's own record
of a device vouches for, cannot do it at all. The value crosses the
network under the TLS in front of the listener and nothing more
([api.md](api.md#tls-is-yours-and-the-clients-rule)); nothing secret
crosses a link to another yard.

**A write says how it came.** What a device writes carries the device's
name: the event has `via` in its data, and wherever the writer is shown the
mark stands behind the name, for a person and for an agent alike:

```
    52 10-12 19:41  mail from mckean (via phone): Land yardr-1a2b when the tests are green
```

in `yardr inbox` (a crew member's and a user's), in `yardr mail --with` and
`yardr chat`, in the notes and answers of `yardr prime --bead` and `yardr
review` (`[2026-10-12 19:42 mckean (via phone)]`, `mckean (via phone)
picked 1`), and on a bead's page in the web view. A client reads it as
`via` ([api.md](api.md#over-the-network)). It is a record of the way in.
Nothing is decided by it: mail `via` a device is its user's word as mail
from the terminal is.

**A device that is lost.** `yardr user device rm <user> <device>`, at the
yard's terminal or by asking the operator. The token is refused at the next
request, and the event streams the device held are closed at once. The yard
also resolves the token of an open stream again every minute, and closes
the stream when it no longer resolves.

**What is refused** is answered 401 before the request's body is read, and
is counted by its reason alone: `no_credential` (no `Authorization: Bearer`
header, or more than one), `unknown_credential` (a token the yard does not
know, a removed device's among them), and `resolve_timeout`. If the store
fails while checking a credential, the yard answers 500: that is a yard
fault, not a token to replace. Nothing is kept for the address a request
came from or for the token it carried, and no request is slowed or answered
429, so wrong tokens from an address cost a device at that address nothing.
The refusals are logged in sums, as [a hook's](subscriptions.md#a-refused-delivery)
are: the event `user_refused`, about no bead, with `reason`, `status`,
`count` (how many since the last such event for that reason) and `last`, at
most one for a reason in ten minutes.

**Where they are listed.** `yardr user device list`; `yardr user show
<name>`, one line a device (`device: phone, added 10-12 19:02, last used
10-12 19:40 from 100.64.0.7`); and `yardr status`, in its part `devices`.
The events are `device_added` and `device_removed`, about no bead, each
with `user` and `device`; a pairing's are [below](#enrolled-by-a-code).

From Go the commands are `store.API`'s `AddDevice`, `Devices`,
`RemoveDevice`, `DevicePairOpen` and `DevicePairClose`. Resolving a token to its device and user is the server's
own (`(*store.Store).ResolveDevice`) and is no call on the socket; the
handler of the listener is `rpc.UserServer`.

### Enrolled by a code

`device add` prints a token for you to carry to the device. `device pair`
has the device fetch it: the operator opens a pairing for one device of one
user, the yard shows a short code, and the device that calls the yard with
the code is answered with its token. Nobody types or pastes the token, and
no command prints it. It is the pairing by which [two yards are
linked](peers.md#pairing), with what a device has no use for left out.

```text
$ yardr user device pair mckean phone --url https://mac.example.net --qr
Pairing is open for 10 minutes, until 14:32:10, for the device phone of mckean. Give the device this yard's address and the code:

  address  https://mac.example.net
  code     K7QM-2XPD

  <the QR code>

The code is good once: the device is answered with its token, which is shown nowhere else. 5 wrong codes end the pairing, and so does: yardr user device pair --end
It then calls as mckean until: yardr user device rm mckean phone
```

- **The code** is one of a pairing of yards: eight characters, good for 10
  minutes and for one device. A yard has one device pairing open at a time,
  beside the one it may have open with another yard; `yardr user device pair
  --end` ends it. A wrong code is counted, and the fifth ends the pairing.
  The code is in the server's memory until it is used, and nowhere else: not
  in the store, not in an event, not in a log. A server that stops takes an
  open pairing with it. The command does not wait for the device: `yardr
  user device list` has it once the code was used.
- **`--url`** is the address the device reaches this yard at, given to the
  device with the code. Without it, it is the listener's own address, which
  is right on one machine or one network; a listener on every address
  (`:8790`) needs it. A yard without a listener opens no pairing.
- **The device is added when the code is used**, as by the operator who
  opened the pairing, and is then a device as `device add` makes one: the
  store keeps the token's SHA-256, `device list` and `device rm` know it.
  What `device add` refuses is refused when the pairing is opened: a user
  the yard has not, a second device of the name. If either becomes true
  while the pairing is open, the code is used and nothing is added.
- **`--json`** prints `{"pairing", "user", "device", "code", "address",
  "expires", "qr"}`, where `qr` is what the QR code holds, and draws
  nothing.

**What the QR code holds.** With `--qr` the command also draws a QR code in
the terminal, for a phone to read in place of the address and the code
being typed. It holds one line of text and nothing else:

```text
<address>#<code>        https://mac.example.net#K7QM-2XPD
```

`<address>` is the yard's address as the command prints it: `http://` or
`https://`, a host, perhaps a port and a path, no query, no `#` of its own
and no slash at its end. `<code>` is the code as it is printed, with its
dash. A client splits the text at its last `#`. The QR code is of error
correction level M.

**What a device does with them.** One request, without a credential:

```text
POST <address>/user/pair
{"code": "K7QM-2XPD"}
```

```sh
curl -X POST https://mac.example.net/user/pair -d '{"code": "K7QM-2XPD"}'
```

The code is read as it is typed: the dash and spaces may be left out, small
letters are capitals, and `I` and `L` are read as `1`, `O` as `0`.

| answer | what it says |
|---|---|
| 200 `{"token", "user", "device", "yard"}` | the device's token, the one time it is anywhere, and what [`/user/me`](api.md#over-the-network) then says of it: the user it calls as, its own name, the yard's name. The answer carries `Cache-Control: no-store`. Keep the token where the device keeps a secret |
| 403 `{"refused": "wrong_code"}` | not the code. It is counted, and the fifth ends the pairing |
| 409 `{"refused": "not_added"}` | the code was right and is used, and the device could not be added. The operator opens another pairing |
| 400 | no code at all: a body that is not `{"code": ...}`, or a code that is not eight of a code's characters. It is no try |
| 404, or 401 | no pairing is open: none was opened, the code was used, it ran out, five wrong codes or the operator ended it. The path is there only while a pairing is open, and otherwise is answered as [any path under `/user/`](api.md#let-in-or-not): 404 in a yard without a device, 401 without a credential. The operator opens another pairing |

An answer that does not arrive leaves a device nobody has the token of:
remove it (`yardr user device rm`) and pair again.

**What it rests on.** The code is all a device needs: whoever has it within
its ten minutes, and reaches the listener, gets the token. Show the code,
and the QR code, to the device and to nobody else. There is no check for two
people to compare, as a pairing of yards has. That check keeps someone
between two yards from putting a key of their own in place of a yard's, and
a device has no key: what it is given is a bearer token, which whoever
reads the connection reads on every call after this one. So a device rests
on the connection, for the code as for the token: `https` off the yard's
machine ([api.md](api.md#over-the-network)). The command says so when the
address is plain `http` and not this machine's. Five tries at a code of 40
bits are what someone who can reach the listener without the code has; they
can end a pairing with them, and no more.

The events, about no bead, each with `pairing`, `user` and `device`:
`device_pair_opened` (with `expires`), and `device_pair_refused` for a
pairing that ended with no device, with `reason` (`wrong_code`, `timed_out`,
`closed`, `not_added`) and `wrong_codes` when there were any. The
`device_added` of a device enrolled so names its `pairing`.

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
