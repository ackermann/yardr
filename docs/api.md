# The socket API: for a program that is not yardr

Every yardr command talks to the yard's server over a unix socket, and so
can a program that is not yardr: a chat window of its own, say, or a window
that shows a person's decisions. This page is what such a program may rely
on: the calls a chat client needs, the calls a decisions client needs,
their JSON, and the events they follow. It is what [`yardr
chat`](chat.md) itself reads and writes, and the queue, the bead and the
writes a decisions client shows, and nothing else.

```sh
curl --unix-socket ~/.yardr/yard.sock -X POST http://yard/call/Users -d '{}'
```

Everything else the socket serves is yardr's own. It changes from one build
to the next with no notice, and a client must not call it. See
[What is stable](#what-is-stable).

The socket is a file on the machine the yard runs on. A program on another
machine, or on a phone, makes the same calls on the yard's listener with a
token of its user: [Over the network](#over-the-network) says what is the
same there and what is not. A Rust or Swift client writes its own few lines
of HTTP against this page; yardr ships no client library.

## The socket

The socket is `$YARDR_HOME/yard.sock` when `YARDR_HOME` is set, and
`~/.yardr/yard.sock` otherwise. It speaks HTTP/1.1; the host of the URL is
ignored (`http://yard/...` here).

Its mode is 0600, in a home yardr makes with mode 0700, so the user who
runs the yard opens it and nobody else. That is all the authentication there is: a
process that can open the socket is trusted as every party of the yard,
and a call that takes an `actor` takes it at its word. A client sends the
name of the user it runs for and no other.

### Who may make a call

The socket knows one thing more of a caller than its `actor`: the process
that opened the connection, which the operating system tells it. From that
process and the processes above it the yard knows whether a call comes from
the pane of a bead's session, from a crew member's pane, from the daemon, or
from none of these (a person's terminal, or a program such as yours). An
exec session's command is the operator's own program, run as its group,
and is served as a person's terminal is. Every
method the socket serves has a class that says which of them may make it,
and a call its class does not let through is refused before the yard looks
at its arguments, with code `error`. If the operating system cannot
identify the peer, only read calls are allowed:

| class | who may make it |
|---|---|
| read | anyone: it changes nothing |
| agent | anyone, a bead's agent too: the work itself (a note, an ask, mail, a bead created or updated, a move its flow allows, a hold, an edge, a source call its grants allow) |
| admin | anyone but a bead's agent: what defines how the yard works, such as groups, routes, flows, crew, depots, settings and packs; the crew, the yardmaster among them, keeps it |
| operator | the operator alone, from no agent's pane, a crew member's included |
| daemon | the daemon alone, as the yard checks it by the daemon's lock: the session records the pane checks read |
| daemon or operator | the daemon, or the operator from no agent's pane: a copy of the store, a prune |

A method a bead's agent has no class for is refused to it: what is not
marked read or agent is not an agent's. The class is a first line, and the
yard's own checks of a call come after it: a call it lets through may still
be refused there, for its `actor` or for what it asks. An agent may update
its bead but not pin the flow the bead goes by. It may hold a bead and add an
edge but not lift a hold or remove a `blocks` edge, which only a person or a
crew member does. Every call this page names is read or agent but those of
[`OperatorCalls`](#over-the-network), which are admin.

It is a line against mistakes, not a wall. Every agent runs as the one OS
user the yard runs as, and the pane is told by the process tree: an agent
that starts a process outside its pane's tree, and calls from there, is
taken for a person at a terminal. The classes stop an agent that calls
the yard as it was given to, not one that sets out to escape.

The server is started by yardr, not by the client. When nothing listens
(the file is missing, or the connection is refused), a yardr command that
reads the yard starts it: `yardr user list`.

`GET /ping` answers `ok` when the server is up.

## A call

```
POST /call/<Method>
```

The body is a JSON object of the method's arguments by name, `{}` for a
method that takes none. A body is always sent. The `Content-Type` of the
request is not read.

- An argument left out is the zero value of its type: `""`, `0`.
- An argument the method does not have is refused, so that a misspelt name
  is an error and not another call.
- yardr itself sends the arguments as a JSON array, in the method's order.
  That form is yardr's own, and only the object is promised here.

The reply has status 200 and is one JSON object:

- `{"result": ...}` when the call answered something. A call asked for a
  part of a list ([`Conversation`](#conversation) with `limit`) says
  `"more": true` beside it when there is more of the list.
- `{}` when it did what it was asked and answers nothing.
- `{"error": {"code": "...", "message": "..."}}` when it was refused. The
  status is 200 here too: the call reached the yard, and the yard said no.

Take `null` where a list is promised as the list with nothing in it.

Every reply of the yard's server carries two headers: `X-Yardr-Revision`,
the build of yardr that serves, and `X-Yardr-Pid`, its process.

### Errors

`code` is one of:

| code | what it says |
|---|---|
| `not-found` | the yard has no such user, crew member, peer |
| `taken`, `at-limit`, `conflict`, `ambiguous` | none of the calls below answers these today |
| `replacing` | the server is about to start a newer build of itself, and did not run the call: see [Builds](#builds) |
| `error` | anything else: an argument that cannot be read, a call the yard refuses |

`message` is a sentence for a person, as yardr prints it. Show it; do not
read it, since its words change.

<!-- errors: made by the test of internal/rpc from what a yard answers, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

What the yard does not have:

```
POST /call/GetUser
{"name":"nobody"}

{"error":{"code":"not-found","message":"user nobody: not found"}}
```

An argument the call does not have (one misspelt would otherwise be a call that means another):

```
POST /call/Mail
{"to":"yardmaster","body":"hello","actor":"jo"}

{"error":{"code":"error","message":"argument \"body\": Mail has none of that name (it takes to, text, actor, files)"}}
```

An argument of another type:

```
POST /call/GetUser
{"name":7}

{"error":{"code":"error","message":"argument \"name\": json: cannot unmarshal number into Go value of type string"}}
```

A method it does not have:

```
POST /call/Sing
{}

{"error":{"code":"error","message":"no such method Sing"}}
```

<!-- /errors -->

## Builds

A yardr command sends the build it is with every call, in the header
`X-Yardr-Revision`. The server compares it with its own. When they differ
and the binary the server was started from has since been replaced by
exactly the caller's build, the server refuses the call with `replacing`,
finishes the calls it is running, and starts the new build in its place.
That is how an install reaches a running yard. In every other case the
caller is served as it is: an older yardr, and a newer one that is not
what is installed, never change the server.

A client that is not yardr sends no `X-Yardr-Revision`, and is served
whatever build the server is. It may say what it is in `X-Yardr-Client`
(`footplate/0.1`); nothing is decided by that header today. A revision
that is no build of yardr is served as well.

So one thing is refused, and to every caller alike: a call that arrives
while the server replaces itself. A client meets it in three ways, and does
the same for each:

- a reply of status 503 with the error `replacing`: the call was not run;
- a connection that is refused, for the moment between the two builds;
- a stream that ends.

Make the call again after 100 ms, for up to 30 seconds, which is what yardr
does. Open the stream again with the last seq you got.

## The stream

```
GET /events?after=<seq>
```

The reply is of type `application/x-ndjson`: one event a line, each a JSON
object. First come the events the yard still has with a seq greater than `after`,
in order and none left out, then every event as it happens. The connection
stays open, and is silent while nothing happens. `after` left out is `0`,
which is the whole log: a client asks `Head` first and follows from there.

A reader that takes nothing for 10 seconds while events wait for it is cut
off. When the stream ends for any reason, open it again with the seq of
the last event you got, and nothing in between is lost.

An event has `seq` (a number that rises), `kind`, `actor` (who did it:
a user, a crew member, `yard`), `at`, `bead` (the bead the event is about,
when it is about one), and `data`, an object whose fields
depend on the kind and which is absent when there are none. The events a
chat client acts on are these; it passes over every other kind. A decisions
client acts on `noted`, and reads a bead it shows again on every event
whose `bead` is that bead.

<!-- events: made by the test of internal/rpc from the stream of a yard, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### Mail to a user or a crew member: `mail`

Mail was sent within the yard, or came from a linked yard for one of this yard. It is for your user when `data.to` is the user's name and `data.to_user` is true; `actor` is then whom the conversation is with. It is your user's own when `actor` is the user's name, whichever client sent it.

```json
{
  "seq": 13,
  "kind": "mail",
  "actor": "yardmaster",
  "data": {
    "files": [
      {
        "bytes": 70,
        "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
        "name": "shed.png",
        "type": "image/png"
      }
    ],
    "text": "The shed is painted. Shall I land it?",
    "to": "jo",
    "to_user": true
  },
  "at": "2026-10-08T09:30:00Z"
}
```

| field | type | |
|---|---|---|
| `seq` | number | the letter's seq |
| `kind` | string | `mail` |
| `actor` | string | the sender |
| `at` | time | when |
| `data.to` | string | the receiver |
| `data.text` | string | the mail |
| `data.to_user` | bool, may be absent | true when the receiver is a user; absent when it is a crew member |
| `data.as` | string, may be absent | on mail that came from a linked yard, whose `actor` is `<name>@<peer>`: the user of this yard the link trusted that name as when it came, as a letter's `as`; absent on any other mail |
| `data.files` | array, may be absent | the files the mail carries, each an image, a page or a file, as a letter's `files`; absent on mail without any |
| `data.files[].hash` | string | the file's name: `GET /blobs/<hash>` answers the bytes |
| `data.files[].type` | string | what the bytes say: `image/png` or `image/jpeg` for an image, `text/html; charset=utf-8` for a page |
| `data.files[].name` | string, may be absent | the file's base name as the sender gave it; absent on a paste and on a letter from before names |
| `data.files[].bytes` | number | how many bytes it is |
| `data.via` | string, may be absent | the [device](#over-the-network) the sender wrote through; absent on mail written on the yard's own machine. Every event a device's call writes as its user carries it, a `noted` and a `mail_read` too |

### Mail to a linked yard: `mail` with `to_peer`

Mail was queued for one of a linked yard: the conversation with `<data.to>@<data.to_peer>`, of the user `actor`. It is `queued` until a `peer_message_sent` or `peer_message_failed` event with that `data.peer` says otherwise; read the conversation again then.

```json
{
  "seq": 121,
  "kind": "mail",
  "actor": "jo",
  "data": {
    "text": "Is the ledger closed?",
    "to": "clerk",
    "to_peer": "north"
  },
  "at": "2026-10-08T09:30:00Z"
}
```

| field | type | |
|---|---|---|
| `seq` | number | the letter's seq |
| `kind` | string | `mail` |
| `actor` | string | the sender |
| `at` | time | when |
| `data.to` | string | the receiver's name in the peer's yard |
| `data.to_peer` | string | the link |
| `data.text` | string | the mail |

### Mail was read: `mail_read`

A user or a crew member acknowledged its mail. Every letter to `data.reader` with a seq up to `data.through` is read: your user's own, when another client acknowledged them, and the letters your user sent a crew member.

```json
{
  "seq": 123,
  "kind": "mail_read",
  "data": {
    "reader": "jo",
    "through": 51
  }
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `mail_read` |
| `data.reader` | string | who read |
| `data.through` | number | through which seq |

### A reaction on mail: `reaction`

A party to a letter put an emoji on it, or took theirs away. Read the conversation with the other party again then: `reactions` on the letter of `data.seq` is what it now holds.

```json
{
  "seq": 14,
  "kind": "reaction",
  "actor": "jo",
  "data": {
    "emoji": "👍",
    "preview": "The shed is painted. Shall I land it?",
    "seq": 13,
    "to": "yardmaster"
  },
  "at": "2026-10-08T09:30:00Z"
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `reaction` |
| `actor` | string | who reacted |
| `at` | time | when |
| `data.seq` | number | the seq of the mail's event |
| `data.emoji` | string | which of 👍 👎 ❤️ 🎉 👀 ✅ it is; `""` when the reaction was taken away |
| `data.to` | string | the other party to the mail, whom this reaction tells in a crew inbox |
| `data.preview` | string | the mail's first words for the crew inbox line, whitespace folded and at most 80 characters |

### A crew member's status: `crew_status`

The daemon saw a crew member's session do something else than before. Any other event whose kind begins `crew_` says that a session started, stopped or failed, or that the list changed, and carries less than what changed: read `Crew` again on it.

```json
{
  "seq": 6,
  "kind": "crew_status",
  "data": {
    "crew": "yardmaster",
    "status": "working",
    "was": ""
  }
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `crew_status` |
| `data.crew` | string | the crew member |
| `data.status` | string | `working`, `idle`, `blocked` or `unknown` |
| `data.was` | string | what it was, `""` for a session the daemon had not looked at |

### A notification: any event with `notified`

An event became a notification for the users `data.notified` names, each with the reason. The event is not the notification's text: read `UserNotifications` again when your user is among them. Nothing else of such an event is for a chat client.

```json
{
  "seq": 10,
  "data": {
    "notified": {
      "jo": "all"
    }
  }
}
```

| field | type | |
|---|---|---|
| `seq` | number | the notification's seq |
| `data.notified` | object | user to reason |

### Notifications were read: `notifications_read`

A user acknowledged their notifications, in this client or another.

```json
{
  "seq": 124,
  "kind": "notifications_read",
  "data": {
    "reader": "jo",
    "through": 11
  }
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `notifications_read` |
| `data.reader` | string | who read |
| `data.through` | number | through which seq |

### A note on a bead: `noted`

A note was added to the bead: a plain note, a question (an ask), or an answer (a pick, words, a secret given, a command approved, put aside, declined), or its asker taking a question back. A question another yard asked arrives as `noted` too, on `bead@peer`. It says no more than that the bead has a new note: read `UserAsks` again on it, and `Bead` again when `bead` is one shown.

```json
{
  "seq": 11,
  "bead": "yd-shed",
  "kind": "noted",
  "actor": "yardmaster",
  "data": {
    "note": 1
  },
  "at": "2026-10-08T09:30:00Z"
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `noted` |
| `bead` | string | the bead the note is on: a handle, or `bead@peer` for a question another yard asked |
| `actor` | string | who wrote it: a name, `name@peer` for a question another yard asked, or `yard` when an answer to one did not land |
| `at` | time | when |
| `data.note` | number, may be absent | the note's seq; absent on a question another yard asked |
| `data.ask` | number, may be absent | the ask's seq on its bead, for a question another yard asked; absent otherwise |

### A bead was closed: `closed`

A bead closed, by an explicit close or by entering a terminal stage of its flow. It says no more than that the bead is done: read `Bead` again when `bead` is one shown.

```json
{
  "seq": 96,
  "bead": "yd-sweep",
  "kind": "closed",
  "actor": "jo",
  "data": {
    "group": "builders",
    "originator": "jo",
    "reason": "done"
  },
  "at": "2026-10-08T09:30:00Z"
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `closed` |
| `bead` | string | the bead that closed: a handle |
| `actor` | string | who closed it |
| `at` | time | when |
| `data.reason` | string | why: the reason the close named, or the terminal stage it entered |
| `data.group` | string, may be absent | the group whose work ended; absent when no group held the bead |
| `data.originator` | string, may be absent | whose bead it was, the user the bead was for; absent when the bead names none |

<!-- /events -->

## A file: `GET /blobs/<hash>`

Mail and a bead's notes carry files: an image, a page or a file. A letter
names each in `files` (the `mail` event in `data.files`, a note in its
own) by its hash, the SHA-256 of its bytes in hex, with its type, size
and name, and this reads one:

```
GET /blobs/<hash>
```

What kind a file is is read from its bytes when it is put: a PNG or a
JPEG is an image, HTML a page, anything else a file. The reply's
`Content-Type` is what the bytes say, the status 200 and the body the
file's bytes. A hash the yard does not hold is status 404, with a line of
text that says so. That is the one answer here that is no JSON.

A file never changes under its hash, so a client that has one need not
ask again. The yard removes a file once no mail or note it still has
names it (`yardr store prune`), which is how a hash from a letter can
come to be 404.

To mail one, call [`PutBlob`](#putblob) with the bytes, then [`Mail`](#mail)
with `{hash, name}` among `files`. To attach one to a note, use
`yardr bead note --file`. The cap on one file is `blob_max_bytes` of
[`Settings`](#settings). To a linked yard it is the same with
[`MailPeer`](#mailpeer), any kind alike, when the link carries `file` out
and the file is within `peer_file_max_bytes`
([peers.md](peers.md#a-file-with-the-mail)). A file of mail from a linked
yard that this yard did not take has `not_taken`, the reason, beside the
sender's `hash`, `type`, `bytes` and `name`: there is no file to ask for
then. In a [`Conversation`](#conversation) with one of a linked yard, a
file of this yard's own mail that the other yard did not take has
`not_taken` too, with that yard's reason.

In the web view a note's page opens from the bead's page, a file downloads
from its link, and a mail's page is named on the talk page without a link
([web.md](web.md#talk)). The view's own `/blobs/<hash>` serves a note's
file to anyone, and on a view that acts a mail's file to its sender or
recipient ([web.md](web.md#one-bead)); `/blobs/<hash>/<name>` serves it
under that name, and the pages link with the name.

The same bytes are the file `<hash>` in `$YARDR_HOME/blobs`, which is how
yardr's own views name a file (`1 image: <path>`, `1 page: <path>`,
`1 file: <path> (name)`). The directory is yardr's own: a client reads a
file here, and never writes there.

## Who you are

`yardr chat` shows the yard to the user whose name is in `$YARDR_ACTOR`,
and when that is not set, to the one named as the OS user is. A client
takes its name the same way, and asks `GetUser` for it: the answer
`not-found` says that the name is no user of the yard, and then there is
nothing to show. The operator adds one with `yardr user add <name>`.

That name is the `name` of the inbox calls and of `UserAsks`, one side
of `Conversation`, and the `actor` of `Mail`, `MailPeer` and the writes.

## Starting

What `yardr chat` reads once, in this order, before it follows the stream:

1. `Head`, for the seq to follow from.
2. `Crew` and `Peers`, for whom there is to talk with.
3. `GetUser`, for how far the user has read.
4. `UserInbox`, for the unread mail, and `UserNotifications`.
5. `GET /events?after=<head>`.
6. `Conversation` with a party, when the user opens it.

`yardr chat <name>` asks `GetCrew` for the name it is given, before any of
this.

From then on it applies events, and asks again only where an event says
less than what changed (`Crew`, `Peers`, `UserNotifications`, and a
conversation with a linked yard).

A client that shows a person's decisions starts the same way, with the
queue for the mail: `Head`, `GetUser`, `UserAsks`, then the stream from
after the head, and `Bead` for each bead it shows. From then on it reads
`UserAsks` again on every `noted` event, `Bead` again on every event whose
`bead` is one shown, and `UserAsks` once more at each put-aside ask's
`return_at`, which is the one return that arrives with no event.

## Over the network

A [device](users.md#devices) is a program of one user that is not on the
yard's machine: a window on a laptop, a saved request on a phone. It makes
the calls of this page on the yard's listener, the address of the setting
`hook_addr` ([configuration.md](configuration.md#settings)), under `/user/`:

```
POST <url>/user/call/<Method>
GET  <url>/user/events?after=<seq>
GET  <url>/user/blobs/<hash>
GET  <url>/user/me
```

with one header more, the token `yardr user device add` printed, or the
one the device was given for a code ([below](#a-token-for-a-code)):

```
Authorization: Bearer yardr-dev-…
```

```sh
curl -H "Authorization: Bearer $(cat ~/.config/footplate/mac.token)" \
  -X POST https://mac.example.net/user/call/UserAsks -d '{}'
```

A call, its reply, its errors, the stream and a file are as this page
says they are on the socket, so a client changes how it connects and
nothing else. What is not the same:

- **Only the calls of this page.** Any other method is answered as one the
  yard does not have (`no such method`), whoever calls: nothing that adds a
  user or a device, moves a bead or changes a setting is on the listener.
  `/ping`, `/wake` and every other path are not there. Three calls more are
  there for a device of the yard's operator and for nobody else,
  [`GiveSecret`, `ApproveAsk` and `ApproveSetup`](#for-the-operator-alone);
  the first is there too for a device of a user who is asked for their
  own address in a source. `ApproveAsk` and `ApproveSetup` are the calls
  on the listener that have the operator's commands run: the command of
  a run ask, which is one of a fixed list ([users.md](users.md#devices)
  has them, row by row), and the apply, account and grants of a setup.
- **Arguments by name only.** The array yardr's own builds send is refused.
- **Every call is made as the device's user.** The `name` of `GetUser`, the
  inbox calls and `UserAsks`, and the `actor` of `Mail`, `MailPeer`,
  `AnswerAsk`, `DeferAsk`, `DeclineAsk`, `TakeBackAsk`, `AddNote`,
  `GiveSecret`, `ApproveAsk` and `ApproveSetup`, may be left out: the yard fills in the user. One that names anyone else is refused with the
  code `error` (`this device calls as jo: "actor" is jo or left out, not
  yardmaster`). One side of `Conversation` is the user: `a` left out is the
  user, and a conversation between two others is refused. So `GetUser`
  answers the device's user and no other; `Users` lists them all.
- **`Settings` answers the promised keys alone**: `operator`,
  `blob_max_bytes` and `files_max`, and the operator's own words to the
  agents, `operator_style` and `operator_note`, for a device of the
  operator alone, and for none in a yard with no operator. No other
  setting is answered on the listener, and a `setting_changed` in the
  stream carries its value for these keys alone: any other setting's
  event comes with the key and without one.
- **`GET /user/me`** answers who the token is, in place of [the name a
  client on the machine takes](#who-you-are):

  ```json
  {"user": "jo", "said_by": "phone", "yard": "south"}
  ```

  `user` is the user every call is made as, `said_by` the device's name,
  `yard` the yard's name.
- **What it writes says how it came.** Every event a device's call writes as
  its user carries `data.via`, the device's name, and so do the letter and
  the note read back (`via` in `Conversation` and in `Bead`'s `notes[]`). A
  view shows it behind the writer: `jo (via phone)`.
- **A request is bounded**: a call's body to 1 MiB, and `PutBlob`'s to the
  yard's cap on a file besides. One over it is answered 413. Over the edge,
  an answer larger than one frame of the link carries is answered 413
  instead, so one large file cannot close the link.
- The two headers of a reply on the socket, the build and the pid, are not
  sent.

### Let in, or not

Without the header, with a token the yard does not know, with the token of a
device that was removed, or when credential resolution times out, every path
under `/user/` is answered 401 `Unauthorized`, `/user/me` too, and a path
that is not served as well: the answer is the same whatever was wrong and
whatever was asked for. It is given before the request's body is read. A
401 is not a fault that passes: a client says "this device is no longer let
in" and stops asking, until it is given a token again.

If the yard cannot check a credential because its store failed, it answers
500 `Internal Server Error`; that is a yard fault, not a token to replace.

A yard with no device at all has no `/user/`: every path there is 404, with
a token or without.

The one request that is let in without a token is `POST /user/pair` while
the operator has a pairing open for a device ([A token for a
code](#a-token-for-a-code)): there a code is what is asked for.

The yard does not slow or refuse by the address a request comes from, and
never answers 429: behind a proxy every caller has the proxy's address, and
a limit for an address would let one caller's wrong tokens shut out every
device beside them. Slowing an address is for what stands in front. The
yard counts what it refuses by the reason alone and logs it in sums, the
event `user_refused` ([users.md](users.md#devices)); the event records the
status too, so a store failure is distinguished from a 401.

### A token for a code

A device gets its token without anyone carrying it when the operator opens
a pairing for it (`yardr user device pair`,
[users.md](users.md#enrolled-by-a-code)): the device is given the yard's
address and a short code, typed or read from a QR code that holds
`<address>#<code>`, and asks once, with no `Authorization` header:

```
POST <url>/user/pair
{"code": "K7QM-2XPD"}
```

The answer is 200 `{"token": "yardr-dev-…", "user": "jo", "device": "phone",
"yard": "south"}`, the one time the token is anywhere: the fields beside it
are what `/user/me` says from then on. A wrong code is 403 `{"refused":
"wrong_code"}`, and the fifth ends the pairing; a body that holds no code
is 400 and no try; 409 `{"refused": "not_added"}` is a code that was right
and a device the yard could not add. The path is there only while a pairing
is open. Before, and once the code was used, ran out or was ended, it is as
every other path here: 401 without a credential, 404 in a yard with no
device. On any of those the client asks the operator for another code.

### The stream of a device

`GET <url>/user/events?after=<seq>` is [the stream](#the-stream), every
event of the yard, one line each. Two things end it that do not end the
socket's:

- **The device was removed**, or its user. The stream is closed at once,
  before the event that says so is sent, and the token is 401 from then on.
  The yard also resolves a stream's token again every minute and closes the
  stream when it no longer resolves.
- **The device holds 16 streams already.** One more is answered 503 with
  `Retry-After`. Close one, or ask again after that many seconds.

A stream that ends for any other reason is opened again after the last seq
read, as on the socket.

A client that reaches the yard through the edge
([edge.md](edge.md#footplate-through-the-link)) gets an empty line while the
stream is idle: the edge writes one whenever 30 seconds pass with nothing
from the yard, since what stands in front of it cuts a response that sends
no bytes for longer. The yard's stream stays silent; the line is the edge's
alone, and no event. A client skips a line that is blank.

### TLS is yours, and the client's rule

yardr does not do TLS: the listener speaks plain HTTP, and a reverse proxy
or a tunnel of yours stands in front of it, as for linked yards
([peers.md](peers.md)). The yard cannot tell from the inside whether one
does, and a token sent in the clear is a token given away. So the rule is
the client's, which knows the address it was given: **a client refuses an
address that is `http://` unless its host is loopback** (`127.0.0.1`, `::1`,
`localhost`), and says why. It keeps the token in a file only its user
reads (mode 0600) or in the system's key store, never in a settings file.

A secret and an approval are the two things the yard holds to this itself,
as far as it can see. [`GiveSecret`, `ApproveAsk` and
`ApproveSetup`](#for-the-operator-alone) are taken only from a request
that reached the listener from the yard's own machine, where the proxy or
tunnel that ends TLS stands (or a client on that machine), and never from
one whose proxy says it came in plain (`X-Forwarded-Proto: http`; the
header is read for a no and never for a yes). Any other is refused with
the code `error`, in words that name the reason and say what may have
crossed a network in the clear: the value, or for an approval the token
that was sent with it. A proxy on another machine that passes requests on
to the yard in plain HTTP is refused so: from there to the yard the value
and the token would be readable. An approval holds no secret of its own;
it is held to the rule so that a token read off a network cannot have a
command of the operator's run from wherever it was read. Every other call
is served wherever it comes from, as before.

A hosted yard takes `GiveSecret` from one peer besides its own machine:
its router, the address the `router_addr` setting names, when the request
says it came to the router over https (`X-Forwarded-Proto: https`). On
that network the router is the only such peer. The header is read for a
yes here, as it is read for a no from the yard's own machine: without it,
or saying anything but https, the giving is refused with the same
sentence. `ApproveAsk` and `ApproveSetup` stay loopback-only: from the
router's peer they are refused as from anywhere else.

Where a proxy stands in front for devices alone, let it pass `/user/` and
nothing more: the listener also serves `/hooks/` and `/peers/`. A header a
proxy adds to say who is calling (`X-User`, `X-Forwarded-For`) is not read:
the token is the only thing that says who a request is.

## The calls

<!-- calls: made by the test of internal/rpc from what a yard answers, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### `Head`

The seq of the newest event, `0` in a yard that has none. Read it before the rest, then follow the stream from after it: what happens while you read comes again from the stream.

```
POST /call/Head
{}

{
  "result": 118
}
```

| field of the result | type | |
|---|---|---|
| `(the result)` | number | the seq |

### `Settings`

The yard's operator, when set, and its caps on a file; to the operator's own devices, also the operator's words to their agents. `yardr chat` reads the operator only to name them when the current name is not a user of the yard; a client can omit this call when it already knows who it is for and sends no file.

```
POST /call/Settings
{}

{
  "result": {
    "blob_max_bytes": "8388608",
    "files_max": "64",
    "operator": "jo",
    "operator_note": "I read diffs, not prose",
    "operator_style": "show"
  }
}
```

| field of the result | type | |
|---|---|---|
| `operator` | string, may be absent | the operator's user name; absent when the yard has not set one |
| `operator_style` | string, may be absent | how the operator asked agents to work with them: `guide`, `show`, `work` or `report`; absent when the operator has not set one, and to a device of any user but the operator |
| `operator_note` | string, may be absent | the one line the operator asked agents to know about them; absent as `operator_style` is |
| `blob_max_bytes` | string, may be absent | the most bytes `PutBlob` takes, a whole number in a string; absent when the yard has not set one, and the cap is then 8388608 (8 MiB) |
| `files_max` | string | how many files one mail or note may carry, a whole number in a string; the yard's own count, so a client needs no copy of the number |

The operator's words are what every session of an agent reads when it starts (`yardr prime`), so the socket tells them to whoever calls, and every other setting with them. Over the network a device is told these keys alone: the words to a device of the operator and to no other, and to none in a yard with no operator. A `setting_changed` in a device's stream carries its `value` for these keys alone; for any other setting the key comes without one.

### `Users`

The users of the yard, by name.

```
POST /call/Users
{}

{
  "result": [
    {
      "name": "jo",
      "cursor": 0,
      "notification_cursor": 0
    }
  ]
}
```

| field of the result | type | |
|---|---|---|
| `[].name` | string | the name mail to the user is addressed to |
| `[].cursor` | number | the user's mail is read through this seq |
| `[].notification_cursor` | number | the same for the user's notifications |

### `GetUser`

One user. It is how a client learns whether the name it runs as is a user of the yard (see [Who you are](#who-you-are)): one that is not is answered with the error `not-found`.

| argument | type | |
|---|---|---|
| `name` | string | the user |

```
POST /call/GetUser
{"name":"jo"}

{
  "result": {
    "name": "jo",
    "cursor": 0,
    "notification_cursor": 0
  }
}
```

| field of the result | type | |
|---|---|---|
| `name` | string | the name |
| `cursor` | number | the user's mail is read through this seq |
| `notification_cursor` | number | the same for the user's notifications |

### `Crew`

The crew, running or not, by name.

```
POST /call/Crew
{}

{
  "result": [
    {
      "name": "yardmaster",
      "enabled": true,
      "model": "opus",
      "kind": "claude",
      "state": "running",
      "status": "working",
      "started_at": "2026-10-08T09:30:00Z"
    }
  ]
}
```

| field of the result | type | |
|---|---|---|
| `[].name` | string | the name mail to the crew member is addressed to |
| `[].enabled` | bool | whether the yard keeps a session of it running |
| `[].state` | string | `stopped`, `running` or `failed` |
| `[].status` | string, may be absent | what a running session does, as the daemon last saw it: `working`, `idle`, `blocked` (it waits at a prompt only a person in its pane can answer) or `unknown`; absent when none runs or the daemon has not looked yet |
| `[].started_at` | time, may be absent | when the running session started |
| `[].model` | string, may be absent | the model its config names; absent when it names none, and the harness's own is then used |
| `[].kind` | string, may be absent | the harness kind its config names (`claude`, `codex`, ...); absent when it names none, and the yard's `default_kind` is then used |

Mail to a crew member that is not running is kept, and read when it next starts.

### `GetCrew`

One crew member, as `Crew` lists it; the error `not-found` for a name the crew does not have.

| argument | type | |
|---|---|---|
| `name` | string | the crew member |

```
POST /call/GetCrew
{"name":"yardmaster"}

{
  "result": {
    "name": "yardmaster",
    "enabled": true,
    "model": "opus",
    "kind": "claude",
    "state": "running",
    "status": "working",
    "started_at": "2026-10-08T09:30:00Z"
  }
}
```

| field of the result | type | |
|---|---|---|
| `name` | string | the name mail to the crew member is addressed to |
| `enabled` | bool | whether the yard keeps a session of it running |
| `state` | string | `stopped`, `running` or `failed` |
| `status` | string, may be absent | what a running session does, as the daemon last saw it: `working`, `idle`, `blocked` (it waits at a prompt only a person in its pane can answer) or `unknown`; absent when none runs or the daemon has not looked yet |
| `started_at` | time, may be absent | when the running session started |
| `model` | string, may be absent | the model its config names; absent when it names none, and the harness's own is then used |
| `kind` | string, may be absent | the harness kind its config names (`claude`, `codex`, ...); absent when it names none, and the yard's `default_kind` is then used |

### `Peers`

The linked yards, by name, each with the roster it last said: whom of that yard this one may see and write to. A client lists each entry as `<name>@<peer>`, which is what `Conversation` and the events call them.

```
POST /call/Peers
{}

{
  "result": [
    {
      "name": "north",
      "send": ["mail"],
      "receive": ["ask", "mail"],
      "trust": {
        "josephine": "jo"
      },
      "said": {
        "roster": [
          {
            "name": "clerk",
            "kind": "crew",
            "role": "Keeps the books",
            "mail": true
          },
          {
            "name": "josephine",
            "kind": "user",
            "mail": true
          }
        ],
        "at": "2026-10-08T09:30:00Z"
      }
    }
  ]
}
```

| field of the result | type | |
|---|---|---|
| `[].name` | string | this yard's name for the link: the `<peer>` of an address |
| `[].send` | array of string | the kinds of message the link carries out; mail can be written to the peer's roster only when `mail` is among them |
| `[].receive` | array of string | the kinds it takes in |
| `[].said` | object, may be absent | what the peer last said of itself; absent for a yard that has not said, which exposes nobody |
| `[].said.at` | time | when this yard heard it |
| `[].said.roster` | array, may be absent | whom the peer exposes; absent when nobody |
| `[].said.roster[].name` | string | the `<name>` of the address |
| `[].said.roster[].kind` | string | `crew` or `user` |
| `[].said.roster[].role` | string, may be absent | one line of what they do, in the peer's words: text from another yard, to be shown as text and nothing else |
| `[].said.roster[].mail` | bool | whether the peer's side of the link takes mail for them |
| `[].trust` | object, may be absent | the names of the peer's roster the operator trusts as a user of this yard (`yardr peer allow north --trust josephine@north=jo`, [peers.md](peers.md#trusting-a-name-as-a-person-of-this-yard)): each key a `<name>` of the roster, its value, a string, the user. Mail from `<name>@<peer>` is that user speaking, and [`Conversation`](#conversation) with the user has it ([chat.md](chat.md#a-name-trusted-as-a-user-of-this-yard)). Absent when the link trusts nobody |

A roster changes with no event of its own, and so does `trust`: read `Peers` again on any event whose kind begins `peer_`. That is `peer_allowed` when the operator gives or takes a grant and `peer_trust_dropped` when the peer's roster has the name no more. A grant also goes with its user, on `user_removed`.

The reply carries the link whole, as `curl` shows it: `reach` ([a user's asks are also sent to the name](peers.md#reach-a-users-asks-are-also-sent-to-the-name)), `key`, the counts and the rest are in it too, but only the table above is promised. The rest is yardr's own, and a client ignores what it does not know.

### `UserInbox`

The mail to a user: the unread, oldest first, then the read, newest first.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `limit` | number | at most this many letters, the unread first; `0` is all |

```
POST /call/UserInbox
{"name":"jo","limit":10}

{
  "result": {
    "mail": [
      {
        "seq": 13,
        "at": "2026-10-08T09:30:00Z",
        "from": "yardmaster",
        "to": "jo",
        "text": "The shed is painted. Shall I land it?",
        "read": false,
        "reactions": [
          {
            "from": "jo",
            "emoji": "👍"
          }
        ],
        "files": [
          {
            "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
            "type": "image/png",
            "bytes": 70,
            "name": "shed.png"
          }
        ]
      },
      {
        "seq": 51,
        "at": "2026-10-08T09:30:00Z",
        "from": "josephine@north",
        "to": "jo",
        "text": "I am at the other machine until four.",
        "read": false,
        "as": "jo"
      }
    ],
    "unread": 2,
    "last": 51,
    "from": ["yardmaster", "josephine@north"]
  }
}
```

| field of the result | type | |
|---|---|---|
| `mail[].seq` | number | the seq of the mail's event: what an acknowledgement goes through, and what `after` is compared with |
| `mail[].at` | time | when it was sent |
| `mail[].from` | string | the sender: a crew member, a user, or `<name>@<peer>` |
| `mail[].to` | string | the receiver, as `from` |
| `mail[].text` | string | the mail |
| `mail[].read` | bool | whether its receiver acknowledged it |
| `mail[].reactions` | array, may be absent | the parties' emoji on the mail, by who put them, as [`React`](#react) put them; absent on mail without any |
| `mail[].reactions[].from` | string | who put it: the sender or the receiver |
| `mail[].reactions[].emoji` | string | which of 👍 👎 ❤️ 🎉 👀 ✅ it is |
| `mail[].as` | string, may be absent | on mail from `<name>@<peer>` only: the user of this yard the link trusted that name as when the mail came, to be shown behind the sender, `josephine@north (as jo)`; absent on any other mail. Whom the links trust now is `trust` of [`Peers`](#peers) |
| `mail[].files` | array, may be absent | the files the mail carries, each an image, a page or a file, in the order it was sent with them; absent on mail without any |
| `mail[].files[].hash` | string | the file's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes |
| `mail[].files[].type` | string | what the bytes say: `image/png` or `image/jpeg` for an image, `text/html; charset=utf-8` for a page |
| `mail[].files[].name` | string, may be absent | the file's base name as the sender gave it; absent on a paste and on a letter from before names |
| `mail[].files[].bytes` | number | how many bytes it is |
| `unread` | number | all the unread, shown or not |
| `last` | number | the seq of the newest unread, `0` when none is |
| `from` | array of string | who the unread are from, in the order they first wrote |

### `PutBlob`

Gives the yard a file, so that mail or a note can carry it. `Mail`, `MailPeer` and `AddNote` name files by the hash this answers, each with a name; `yardr bead note --file` attaches a blob to a note. The bytes go in the JSON of the call, in base64. What kind it is is read from the bytes themselves: a PNG or a JPEG is an image, HTML a page, anything else a file. One over the yard's cap is refused (`blob_max_bytes` of `Settings`). The yard keeps a file once, under the SHA-256 of its bytes: putting the same bytes again answers the same and stores nothing new. A file no mail or note names is removed by `yardr store prune`, though not within an hour of being put, so put it when the mail or note is about to be written, and put it again when that was longer ago.

| argument | type | |
|---|---|---|
| `data` | string | the file's bytes, in base64 (the standard alphabet, padded) |

```
POST /call/PutBlob
{"data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="}

{
  "result": {
    "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
    "type": "image/png",
    "bytes": 70
  }
}
```

| field of the result | type | |
|---|---|---|
| `hash` | string | the file's name: the SHA-256 of its bytes, in hex |
| `type` | string | what the bytes say |
| `bytes` | number | how many bytes it is |

### `Mail`

Sends mail to a crew member or a user of this yard. It answers nothing; the mail is then a `mail` event in the stream. A text over one mail's size is sent as numbered mails, each led by a line `(1/3)` that says which of how many it is, as [`yardr chat`](chat.md#a-long-text) sends a long text; past 16 mails it is refused.

| argument | type | |
|---|---|---|
| `to` | string | the receiver's name |
| `text` | string | the mail; a text over 4096 bytes is sent as numbered parts, up to 16 (about 64 KB). It may be empty when the mail carries a file |
| `actor` | string | who sends it: the user's name |
| `files` | array of object | the files the mail carries, at most 64: each `{hash, name}`, the hash as `PutBlob` answered it with the file's base name. Left out, the mail carries none. One the yard does not hold, or a name no letter may carry, refuses the mail. On a text sent as several mails, the files go with the first alone |

```
POST /call/Mail
{"to":"yardmaster","text":"Thanks. Land it.","actor":"jo"}

{}
```

```
POST /call/Mail
{"to":"yardmaster","text":"Paint the fence like this.","actor":"jo","files":[{"hash":"497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581","name":"fence.png"}]}

{}
```

### `MailPeer`

Queues mail to one of a linked yard, `<to>@<peer>`. It is refused when the link does not carry mail out. The answer is the message as it waits in the outbox for the peer.

| argument | type | |
|---|---|---|
| `to` | string | the receiver's name in the peer's yard |
| `peer` | string | this yard's name for the link |
| `text` | string | the mail |
| `actor` | string | who sends it: the user's name |
| `files` | array of object | the files the mail carries, each `{hash, name}` as for `Mail`; `text` may then be empty. Each crosses to the peer as a message of kind `file` before the mail. Refused, with nothing queued, when the link does not carry `file` out, when the peer's yardr has not said it understands `file`, and when a file is over `peer_file_max_bytes` |

```
POST /call/MailPeer
{"to":"clerk","peer":"north","text":"Is the ledger closed?","actor":"jo"}

{
  "result": {
    "peer": "north",
    "kind": "mail",
    "at": "2026-10-08T09:30:00Z"
  }
}
```

| field of the result | type | |
|---|---|---|
| `peer` | string | the link it waits for |
| `kind` | string | `mail` |
| `at` | time | when it was queued |

Where it stands afterwards is `delivery` in the conversation.

### `React`

Puts the party's reaction on the mail `seq`: one of 👍 👎 ❤️ 🎉 👀 ✅, or `""` to take it away. Only the letter's sender or receiver may react, and only mail its sender wrote takes one. One party holds one reaction: a new one replaces the old one. It answers nothing; the reaction is then a `reaction` event in the stream, and `reactions` on the letter in [`UserInbox`](#userinbox) and [`Conversation`](#conversation).

| argument | type | |
|---|---|---|
| `seq` | number | the seq of the mail's event |
| `emoji` | string | one of 👍 👎 ❤️ 🎉 👀 ✅; `""` takes the party's reaction away |
| `actor` | string | who reacts: the user's name |

```
POST /call/React
{"seq":13,"emoji":"🎉","actor":"jo"}

{}
```

### `Conversation`

The mail between two parties, both directions, oldest first: what `yardr mail --with <name>` prints. A user of this yard has one history, whatever name they wrote under: when a party is a user, the conversation has also the mail between the other party and each name of a linked yard that the link [trusts as that user](peers.md#trusting-a-name-as-a-person-of-this-yard), in the same order and the same pages, so a client joins nothing itself. Asking again with the last seq seen costs what is new. A long conversation is read from its newest end, a page at a time: with `limit` alone first, then with `limit` and, as `before`, the `seq` of the oldest letter of the page before, for as long as the reply says `more`.

| argument | type | |
|---|---|---|
| `a` | string | one party: the user |
| `b` | string | the other: a crew member, a user, or `<name>@<peer>` for one of a linked yard. As `<name>@<peer>` it is the mail with that address alone, trusted or not |
| `after` | number | leave out what is not newer than this seq; `0` for all |
| `limit` | number | at most this many: the newest of what `after` and `before` leave, oldest first as ever; `0` for all |
| `before` | number | leave out what is not older than this seq; `0` for none left out |

```
POST /call/Conversation
{"a":"jo","b":"yardmaster","after":0}

{
  "result": [
    {
      "seq": 13,
      "at": "2026-10-08T09:30:00Z",
      "from": "yardmaster",
      "to": "jo",
      "text": "The shed is painted. Shall I land it?",
      "read": false,
      "reactions": [
        {
          "from": "jo",
          "emoji": "🎉"
        }
      ],
      "files": [
        {
          "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
          "type": "image/png",
          "bytes": 70,
          "name": "shed.png"
        }
      ]
    },
    {
      "seq": 36,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "On the train. Land the shed when it is green.",
      "read": false,
      "via": "phone"
    },
    {
      "seq": 60,
      "at": "2026-10-08T09:30:00Z",
      "from": "josephine@north",
      "to": "yardmaster",
      "text": "And the fence after the shed.",
      "read": false,
      "as": "jo"
    },
    {
      "seq": 119,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Thanks. Land it.",
      "read": false
    },
    {
      "seq": 120,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Paint the fence like this.",
      "read": false,
      "files": [
        {
          "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
          "type": "image/png",
          "bytes": 70,
          "name": "fence.png"
        }
      ]
    }
  ]
}
```

```
POST /call/Conversation
{"a":"jo","b":"yardmaster","limit":2}

{
  "result": [
    {
      "seq": 119,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Thanks. Land it.",
      "read": false
    },
    {
      "seq": 120,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Paint the fence like this.",
      "read": false,
      "files": [
        {
          "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
          "type": "image/png",
          "bytes": 70,
          "name": "fence.png"
        }
      ]
    }
  ],
  "more": true
}
```

```
POST /call/Conversation
{"a":"jo","b":"clerk@north","after":0}

{
  "result": [
    {
      "seq": 121,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "clerk@north",
      "text": "Is the ledger closed?",
      "read": false,
      "delivery": "queued"
    }
  ]
}
```

```
POST /call/Conversation
{"a":"jo","b":"josephine@north","after":0}

{
  "result": [
    {
      "seq": 51,
      "at": "2026-10-08T09:30:00Z",
      "from": "josephine@north",
      "to": "jo",
      "text": "I am at the other machine until four.",
      "read": false,
      "as": "jo"
    }
  ]
}
```

| field of the result | type | |
|---|---|---|
| `[].seq` | number | the seq of the mail's event: what an acknowledgement goes through, and what `after` is compared with |
| `[].at` | time | when it was sent |
| `[].from` | string | the sender: a crew member, a user, or `<name>@<peer>` |
| `[].to` | string | the receiver, as `from` |
| `[].text` | string | the mail |
| `[].read` | bool | whether its receiver acknowledged it |
| `[].reactions` | array, may be absent | the parties' emoji on the mail, by who put them, as [`React`](#react) put them; absent on mail without any |
| `[].reactions[].from` | string | who put it: the sender or the receiver |
| `[].reactions[].emoji` | string | which of 👍 👎 ❤️ 🎉 👀 ✅ it is |
| `[].as` | string, may be absent | on mail from `<name>@<peer>` only: the user of this yard the link trusted that name as when the mail came, to be shown behind the sender, `josephine@north (as jo)`; absent on any other mail. Whom the links trust now is `trust` of [`Peers`](#peers) |
| `[].via` | string, may be absent | the [device](#over-the-network) the mail was written through, to be shown behind the sender, `jo (via phone)`; absent on mail written on the yard's own machine |
| `[].files` | array, may be absent | the files the mail carries, each an image, a page or a file, in the order it was sent with them; absent on mail without any |
| `[].files[].hash` | string | the file's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes |
| `[].files[].type` | string | what the bytes say: `image/png` or `image/jpeg` for an image, `text/html; charset=utf-8` for a page |
| `[].files[].name` | string, may be absent | the file's base name as the sender gave it; absent on a paste and on a letter from before names |
| `[].files[].bytes` | number | how many bytes it is |
| `[].delivery` | string, may be absent | on mail to a linked yard only, whose reading of it this yard never learns of: `queued` while it waits here for the peer, `sent` once the peer has it, `failed` when it was given up |

`more`, beside `result` in the reply and not in it, is `true` when `limit` left out mail older than the oldest letter answered (and newer than `after`, when that is given). It is absent otherwise: on the oldest page, and on every call without `limit`.

A letter of a trusted name says how it went, and is otherwise a letter like any other. What the user wrote under the name has the address as its `from`, and `as` when the link trusted the name as it came: the letter from `josephine@north` above, which jo wrote to the yardmaster from there. Its `read` is its receiver's, as ever. What was written to the name has the address as its `to`, and a `delivery` in place of a reading. Neither is in the user's inbox in this yard: [`UserInbox`](#userinbox) counts and lists none of it, no `mail` event of it is to the user, and [`AckUserInbox`](#ackuserinbox) through its seq marks none of it. So a client counts unread from the inbox and the events, as before, and never from `read` of a conversation.

The letters go by what the link said of the name when the mail was logged: a grant taken back later keeps them in the user's conversation, and what the name writes afterwards, or is written, is the name's alone (`b` as `<name>@<peer>`). Mail from before the grant is in it too, without `as`. Mail of a yard that is linked no more is in neither.

### `AckUserInbox`

Marks the user's mail read through a seq. It answers nothing; when mail was in between it is a `mail_read` event in the stream.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `through` | number | the seq of the newest letter shown |

```
POST /call/AckUserInbox
{"name":"jo","through":51}

{}
```

### `UserNotifications`

What the yard reports to a user, apart from every conversation: the events the user subscribed to, oldest first.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `limit` | number | at most this many; `0` is all the inbox keeps |

```
POST /call/UserNotifications
{"name":"jo","limit":0}

{
  "result": {
    "notifications": [
      {
        "seq": 10,
        "at": "2026-10-08T09:30:00Z",
        "kind": "created",
        "bead": "yd-shed",
        "text": "yd-shed created (all) by yardmaster originator=jo stage=new title=Paint the shed type=task",
        "read": false
      },
      {
        "seq": 11,
        "at": "2026-10-08T09:30:00Z",
        "kind": "noted",
        "bead": "yd-shed",
        "text": "yd-shed noted (all) by yardmaster note=1 on \"Paint the shed\"",
        "read": false
      }
    ],
    "unread": 2,
    "last": 11
  }
}
```

| field of the result | type | |
|---|---|---|
| `notifications[].seq` | number | the seq of the event it tells of |
| `notifications[].at` | time | when that happened |
| `notifications[].kind` | string | the event's kind |
| `notifications[].bead` | string, may be absent | the bead it is about, when it is about one |
| `notifications[].text` | string | the whole of it in one line |
| `notifications[].read` | bool | whether the user acknowledged it |
| `unread` | number | all the unread, shown or not |
| `last` | number | the seq of the newest unread |

### `AckUserNotifications`

Marks the user's notifications read through a seq: a position of its own, which the mail's does not move. It answers nothing; it is a `notifications_read` event in the stream.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `through` | number | the seq of the newest notification shown |

```
POST /call/AckUserNotifications
{"name":"jo","through":11}

{}
```

### `UserAsks`

The person's decisions: every open ask addressed to them, on any bead in any stage and from the linked yards, oldest first, with what they have dealt with beside it. A question another yard asked stands as `bead@peer`, placed by when this yard took it; answered, put aside or declined here it is among the decided by what this yard knows of it, since the answer lands in the other yard. It is not `yardr review`'s queue, which walks beads in manual stages by bead age.

| argument | type | |
|---|---|---|
| `name` | string | the user |

```
POST /call/UserAsks
{"name":"jo"}

{
  "result": {
    "open": [
      {
        "bead": "yd-links",
        "bead_title": "Move the links",
        "topic": "topic:linear-out",
        "ask": 3,
        "question": "Move this yard's links to the declared source now?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z"
      },
      {
        "bead": "yd-links",
        "bead_title": "Move the links",
        "topic": "topic:linear-out",
        "ask": 4,
        "question": "This design before that one?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z"
      },
      {
        "bead": "yd-greet",
        "bead_title": "The greeting",
        "topic": "",
        "ask": 5,
        "question": "What should the tone of the greeting be?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z"
      },
      {
        "bead": "yd-ledger@north",
        "bead_title": "Close the ledger",
        "topic": "topic:books",
        "ask": 9,
        "question": "Close the ledger tonight?",
        "asker": "clerk@north",
        "since": "2026-10-08T09:30:00Z"
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 18,
        "question": "The CRM's token for updates",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "crm",
          "env": ["CRM_UPDATE_TOKEN"],
          "line": "a secret: CRM_UPDATE_TOKEN, for the source crm (https://crm.example.com)"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 19,
        "question": "The API key for the CRM",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "crm",
          "env": ["CRM_TOKEN"],
          "line": "a secret: CRM_TOKEN, for the source crm (https://crm.example.com)"
        }
      },
      {
        "bead": "yd-greet",
        "bead_title": "The greeting",
        "topic": "",
        "ask": 22,
        "question": "Flowers for the newcomer?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z"
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 25,
        "question": "Your id at the phone service",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "phone",
          "env": ["PHONE_USER"],
          "user": "jo",
          "line": "a secret: PHONE_USER, jo's own address in the source phone (https://phone.example.com); jo is told through phone once it is given and yardr user set jo notify=phone is run"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 28,
        "question": "Take more mail from north?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "run": {
          "argv": ["peer", "allow", "north", "--mail-limit", "300"],
          "command": "yardr peer allow north --mail-limit 300",
          "does": ["the link with north (key 6lHks9_z…) takes 300 mails and asks an hour from it. It takes 100 now."],
          "back": "yardr peer allow north --mail-limit 100"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 31,
        "question": "May the CRM be called more often?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "run": {
          "argv": ["source", "set", "crm", "calls_per_hour=240"],
          "command": "yardr source set crm calls_per_hour=240",
          "does": ["the source crm (https://crm.example.com) may be called 240 times an hour by each session or crew member that may call it. It may be called 120 times now."],
          "back": "yardr source set crm calls_per_hour=120",
          "back_by": "only the operator"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 34,
        "question": "The code the second sign-in showed",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "",
          "env": ["INPUT"],
          "login": "claude-example2",
          "line": "a secret: INPUT, for the waiting login claude-example2"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 37,
        "question": "The token it updates with",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "crm",
          "env": ["CRM_UPDATE_TOKEN"],
          "crew": "yardmaster",
          "line": "a secret: CRM_UPDATE_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 40,
        "question": "Your CRM token",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "crm",
          "env": ["CRM_TOKEN"],
          "as": "jo",
          "line": "a secret: CRM_TOKEN, jo's own credential for the source crm (https://crm.example.com), read as jo when a call is made --as jo"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 41,
        "question": "The yard's account for gog",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "gog",
          "env": ["GOG_ACCOUNT"],
          "line": "a secret: GOG_ACCOUNT, for the source gog (https://gog.example.com)"
        },
        "concealed": false
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 44,
        "question": "Set up the courier source?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "setup": {
          "pack": "courier",
          "label": "embedded in yardr doctest",
          "account": ["COURIER_ACCOUNT"],
          "account_file": "/home/jo/.yardr/sources/courier.env",
          "candidates": [
            {
              "source": "courier-existing",
              "file": "/home/jo/.yardr/sources/courier-existing.env",
              "values": {
                "COURIER_ACCOUNT": "jo@example.test"
              }
            }
          ],
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://courier.example.com/api. Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to group builders and crew yardmaster — pick one:"],
          "back": "yardr source rm courier; yardr source revoke courier \u003cwhat\u003e --group builders --crew yardmaster",
          "back_by": "the operator",
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "courier",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "courier",
                  "calls": ["read", "put"]
                }
              ]
            }
          ],
          "to": [
            {
              "group": "builders"
            },
            {
              "crew": "yardmaster"
            }
          ]
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 47,
        "question": "The API key Claude Code will use",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "",
          "env": ["ANTHROPIC_API_KEY"],
          "crew": "yardmaster",
          "harness": "claude",
          "line": "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, for crew member yardmaster; the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
        }
      }
    ],
    "decided": [
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 48,
        "question": "The API key Claude Code needs at first login",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "secret": {
          "source": "",
          "env": ["ANTHROPIC_API_KEY"],
          "crew": "yardmaster",
          "harness": "claude",
          "line": "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, for crew member yardmaster; the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
        },
        "given": ["ANTHROPIC_API_KEY"]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 45,
        "question": "Set up the courier source for the elders too?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "declined",
        "words": "the builders' card covers it",
        "setup": {
          "pack": "courier",
          "label": "embedded in yardr doctest",
          "account": ["COURIER_ACCOUNT"],
          "account_file": "/home/jo/.yardr/sources/courier.env",
          "candidates": [
            {
              "source": "courier-existing",
              "file": "/home/jo/.yardr/sources/courier-existing.env",
              "values": {
                "COURIER_ACCOUNT": "jo@example.test"
              }
            }
          ],
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://courier.example.com/api. Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to group elders and crew yardmaster — pick one:"],
          "back": "yardr source rm courier; yardr source revoke courier \u003cwhat\u003e --group elders --crew yardmaster",
          "back_by": "the operator",
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "courier",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "courier",
                  "calls": ["read", "put"]
                }
              ]
            }
          ],
          "to": [
            {
              "group": "elders"
            },
            {
              "crew": "yardmaster"
            }
          ]
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 42,
        "question": "The yard's other account for gog",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "secret": {
          "source": "gog",
          "env": ["GOG_ACCOUNT"],
          "line": "a secret: GOG_ACCOUNT, for the source gog (https://gog.example.com)"
        },
        "concealed": false,
        "given": ["GOG_ACCOUNT"]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 38,
        "question": "Your CRM token for updates",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "secret": {
          "source": "crm",
          "env": ["CRM_UPDATE_TOKEN"],
          "as": "jo",
          "line": "a secret: CRM_UPDATE_TOKEN, jo's own credential for the source crm (https://crm.example.com), read as jo when a call is made --as jo"
        },
        "given": ["CRM_UPDATE_TOKEN"]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 35,
        "question": "The token it reads with",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "secret": {
          "source": "crm",
          "env": ["CRM_TOKEN"],
          "crew": "yardmaster",
          "line": "a secret: CRM_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
        },
        "given": ["CRM_TOKEN"]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 32,
        "question": "The code the sign-in page showed",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "secret": {
          "source": "",
          "env": ["INPUT"],
          "login": "claude-example1",
          "line": "a secret: INPUT, for the waiting login claude-example1"
        },
        "given": ["INPUT"]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 29,
        "question": "May the CRM be called more often?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "run": {
          "argv": ["source", "set", "crm", "calls_per_hour=120"],
          "command": "yardr source set crm calls_per_hour=120",
          "does": ["the source crm (https://crm.example.com) may be called 120 times an hour by each session or crew member that may call it. It may be called 60 times now."],
          "back": "yardr source set crm calls_per_hour=60",
          "back_by": "only the operator"
        },
        "ran": {
          "argv": ["source", "set", "crm", "calls_per_hour=120"],
          "result": "source crm: 120 calls an hour for each caller, 10 of them updates at most"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 26,
        "question": "Take more mail from north?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "run": {
          "argv": ["peer", "allow", "north", "--mail-limit", "100"],
          "command": "yardr peer allow north --mail-limit 100",
          "does": ["the link with north (key 6lHks9_z…) takes 100 mails and asks an hour from it. It takes 30 now."],
          "back": "yardr peer allow north --mail-limit 30"
        },
        "ran": {
          "argv": ["peer", "allow", "north", "--mail-limit", "100"],
          "result": "peer north: takes 100 mails and asks an hour, and 6400 files"
        }
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 23,
        "question": "Your id at the pager",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "secret": {
          "source": "pager",
          "env": ["PAGER_USER"],
          "user": "jo",
          "line": "a secret: PAGER_USER, jo's own address in the source pager (https://pager.example.com); jo is told through pager once it is given and yardr user set jo notify=pager is run"
        },
        "given": ["PAGER_USER"]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 20,
        "question": "Read the CRM's archive too?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "taken back",
        "words": "the archive is not in the CRM's API",
        "by": "yardmaster"
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 16,
        "question": "The API key for the books",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "secret": {
          "source": "books",
          "env": ["BOOKS_TOKEN"],
          "line": "a secret: BOOKS_TOKEN, for the source books (https://books.example.com)"
        },
        "given": ["BOOKS_TOKEN"]
      },
      {
        "bead": "yd-links",
        "bead_title": "Move the links",
        "topic": "topic:linear-out",
        "ask": 6,
        "question": "Send yardr-3msx and yardr-vgjp to build?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "option": 1,
        "option_label": "Send them",
        "words": "send them tonight"
      },
      {
        "bead": "yd-links",
        "bead_title": "Move the links",
        "topic": "topic:linear-out",
        "ask": 12,
        "question": "The web view cleanup: build?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "put aside",
        "return_at": "2026-10-08T09:30:00Z"
      },
      {
        "bead": "yd-greet",
        "bead_title": "The greeting",
        "topic": "",
        "ask": 10,
        "question": "Who greets the newcomer?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "words": "warmly"
      },
      {
        "bead": "yd-links",
        "bead_title": "Move the links",
        "topic": "topic:linear-out",
        "ask": 8,
        "question": "Close sketch PR #3?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "declined",
        "words": "not mine to take"
      }
    ]
  }
}
```

| field of the result | type | |
|---|---|---|
| `open[].bead` | string | the bead the question stands on: a handle, or `bead@peer` for a question another yard asked |
| `open[].bead_title` | string | the bead's title |
| `open[].topic` | string | the bead's topic: its first `topic:` label, `""` when it has none |
| `open[].ask` | number | the ask's seq on its bead: what an answer names |
| `open[].question` | string | the question in one line, as a person would ask it |
| `open[].asker` | string | who asked: a name, or `name@peer` for a question another yard asked |
| `open[].since` | time | when the ask was written: how long it waits (as sent, for a question another yard asked) |
| `open[].secret` | object, may be absent | what a secret ask asks for, as `Bead` says it; absent on any other ask |
| `open[].secret.source` | string | the declared source the secret is the credential of; with `user`, `crew` or `as`, the source the address or credential is in; empty on a login or harness-key ask |
| `open[].secret.env` | array of string | the names to ask the person a value for; with a harness-key ask, variables in that harness kind's `login_env` |
| `open[].secret.user` | string, may be absent | the user whose own address in the source is asked for; absent on an ask for the source's credential |
| `open[].secret.crew` | string, may be absent | the crew member whose own credential in the source or harness API key is asked for; absent on any other secret ask |
| `open[].secret.as` | string, may be absent | the user whose own credential in the source is asked for; absent on any other secret ask |
| `open[].secret.login` | string, may be absent | the waiting login the value goes to; absent on any other secret ask |
| `open[].secret.harness` | string, may be absent | the harness kind whose API key is asked for; absent on any other secret ask |
| `open[].secret.line` | string | what the card says is asked for, in the yard's words |
| `open[].concealed` | bool, may be absent | whether a secret ask's value is drawn hidden: false where the source marks every asked name as no secret; absent (concealed) on any other ask |
| `open[].run` | object, may be absent | the command a run ask proposes, as `Bead` says it; absent on any other ask |
| `open[].run.argv` | array of string | the command in words, without the leading `yardr` |
| `open[].run.command` | string | the command whole, as it is typed in a terminal. Never shorten it |
| `open[].run.does` | array of string | what the command changes, in the yard's words |
| `open[].run.back` | string | the command that takes the change back |
| `open[].run.back_by` | string, may be absent | who may run it, as `Bead` says it; absent where that is anyone |
| `open[].setup` | object, may be absent | the setup a setup ask proposes, as `Bead` says it; absent on any other ask |
| `open[].setup.pack` | string | the embedded pack to set up, of kind source |
| `open[].setup.label` | string, may be absent | the build the pack is embedded in, as the card shows it |
| `open[].setup.account` | array of string | the names to type unseen beside the preset pick |
| `open[].setup.account_file` | string | the file the value goes to, on the yard's machine |
| `open[].setup.candidates` | array, may be absent | yard sources whose account the card offers to reuse |
| `open[].setup.candidates[].source` | string | the source whose account the card offers |
| `open[].setup.candidates[].file` | string | the file holding the shared account inputs |
| `open[].setup.candidates[].values` | object | the shared account input names and values the file holds |
| `open[].setup.does` | array of string | what approving changes and what is there now, in the yard's words |
| `open[].setup.back` | string | the commands that take the setup back |
| `open[].setup.back_by` | string, may be absent | who may run them, as `Bead` says it; absent where that is anyone |
| `open[].setup.presets` | array | the pack's presets with their grants, in the order the ask's options count them |
| `open[].setup.presets[].name` | string | the preset's name, as the option's label shows it |
| `open[].setup.presets[].grants` | array | what of each source the preset grants |
| `open[].setup.presets[].grants[].source` | string | the source the pack declares |
| `open[].setup.presets[].grants[].calls` | array of string | `read` for every read call, calls by name |
| `open[].setup.to` | array | who the grants are for |
| `open[].setup.to[].group` | string, may be absent | the group, and so each session it starts |
| `open[].setup.to[].crew` | string, may be absent | the crew member |
| `decided[].bead` | string | the bead the question stands on: a handle, or `bead@peer` for a question another yard asked |
| `decided[].bead_title` | string | the bead's title |
| `decided[].topic` | string | the bead's topic: its first `topic:` label, `""` when it has none |
| `decided[].ask` | number | the ask's seq on its bead: what an answer names |
| `decided[].question` | string | the question in one line, as a person would ask it |
| `decided[].asker` | string | who asked: a name, or `name@peer` for a question another yard asked |
| `decided[].since` | time | when the ask was written: how long it waits (as sent, for a question another yard asked) |
| `decided[].secret` | object, may be absent | what a secret ask asks for, as `Bead` says it; absent on any other ask |
| `decided[].secret.source` | string | the declared source the secret is the credential of; with `user`, `crew` or `as`, the source the address or credential is in; empty on a login or harness-key ask |
| `decided[].secret.env` | array of string | the names to ask the person a value for; with a harness-key ask, variables in that harness kind's `login_env` |
| `decided[].secret.user` | string, may be absent | the user whose own address in the source is asked for; absent on an ask for the source's credential |
| `decided[].secret.crew` | string, may be absent | the crew member whose own credential in the source or harness API key is asked for; absent on any other secret ask |
| `decided[].secret.as` | string, may be absent | the user whose own credential in the source is asked for; absent on any other secret ask |
| `decided[].secret.login` | string, may be absent | the waiting login the value goes to; absent on any other secret ask |
| `decided[].secret.harness` | string, may be absent | the harness kind whose API key is asked for; absent on any other secret ask |
| `decided[].secret.line` | string | what the card says is asked for, in the yard's words |
| `decided[].concealed` | bool, may be absent | whether a secret ask's value is drawn hidden: false where the source marks every asked name as no secret; absent (concealed) on any other ask |
| `decided[].run` | object, may be absent | the command a run ask proposes, as `Bead` says it; absent on any other ask |
| `decided[].run.argv` | array of string | the command in words, without the leading `yardr` |
| `decided[].run.command` | string | the command whole, as it is typed in a terminal. Never shorten it |
| `decided[].run.does` | array of string | what the command changes, in the yard's words |
| `decided[].run.back` | string | the command that takes the change back |
| `decided[].run.back_by` | string, may be absent | who may run it, as `Bead` says it; absent where that is anyone |
| `decided[].setup` | object, may be absent | the setup a setup ask proposes, as `Bead` says it; absent on any other ask |
| `decided[].setup.pack` | string | the embedded pack to set up, of kind source |
| `decided[].setup.label` | string, may be absent | the build the pack is embedded in, as the card shows it |
| `decided[].setup.account` | array of string | the names to type unseen beside the preset pick |
| `decided[].setup.account_file` | string | the file the value goes to, on the yard's machine |
| `decided[].setup.candidates` | array, may be absent | yard sources whose account the card offers to reuse |
| `decided[].setup.candidates[].source` | string | the source whose account the card offers |
| `decided[].setup.candidates[].file` | string | the file holding the shared account inputs |
| `decided[].setup.candidates[].values` | object | the shared account input names and values the file holds |
| `decided[].setup.does` | array of string | what approving changes and what is there now, in the yard's words |
| `decided[].setup.back` | string | the commands that take the setup back |
| `decided[].setup.back_by` | string, may be absent | who may run them, as `Bead` says it; absent where that is anyone |
| `decided[].setup.presets` | array | the pack's presets with their grants, in the order the ask's options count them |
| `decided[].setup.presets[].name` | string | the preset's name, as the option's label shows it |
| `decided[].setup.presets[].grants` | array | what of each source the preset grants |
| `decided[].setup.presets[].grants[].source` | string | the source the pack declares |
| `decided[].setup.presets[].grants[].calls` | array of string | `read` for every read call, calls by name |
| `decided[].setup.to` | array | who the grants are for |
| `decided[].setup.to[].group` | string, may be absent | the group, and so each session it starts |
| `decided[].setup.to[].crew` | string, may be absent | the crew member |
| `decided[].given` | array of string, may be absent | the names a secret ask was given values for, or the account names a setup ask was approved with, which reads "gave `<names>`"; absent on any other ask |
| `decided[].ran` | object, may be absent | the command the yard ran when a run ask was approved, which reads "approved, ran: `<command>`"; or the setup a setup ask approved, which reads "approved, being carried out", "approved, ran" or "approved, failed" with the preset and the names given; absent on any other ask and on an ask that was declined or put aside |
| `decided[].ran.argv` | array of string | the command that ran, in words: the one the ask proposed; on a setup ask, `setup` with the pack's name |
| `decided[].ran.result` | string | what the command printed; on a setup ask not yet carried out, `approved, to be carried out`, and afterwards what the carrying out printed |
| `decided[].state` | string | `answered`, `put aside`, `declined` or `taken back`. A later build may add a state: one a client does not know is still not open, and is shown by its word |
| `decided[].option` | number, may be absent | the picked option's number; absent on a words answer, an aside ask, a declined one and one taken back |
| `decided[].option_label` | string, may be absent | the picked option's label; absent with `option` |
| `decided[].words` | string, may be absent | what went with the pick, the words answer, or why the question is declined or taken back; for a question another yard asked that ended there, how it ended; absent when there are none |
| `decided[].by` | string, may be absent | who took the ask back, its asker or the operator: it reads "taken back by `<by>`: `<words>`", and is no answer of the person. Absent on every other state, and on a question another yard took back, whose `words` say where |
| `decided[].return_at` | time, may be absent | when a put-aside ask comes back; absent otherwise |

Read it again on every `noted` event: a question asked, answered, put aside, declined or taken back, and the asker's next note, all arrive as `noted`. A put-aside ask also comes back on its own after 24 hours, with no event: its decided entry carries `return_at`, so read again then. An answer to a question another yard asked that did not land arrives as `noted` by `yard`: the question is open again, or decided with how it ended there, and the person has mail from `yard` that says which.

### `Bead`

One bead whole: body, stage, labels, notes, its asks with their options, recommendation and state, and for each about-bead its id, title and stage. On a `bead@peer` ref, a question another yard asked, the view is synthesized from what that yard carried: its title, stage, topic and labels, its body under one first line saying the bead lives in the other yard and at which stage, no notes, the asks whole with their state as this yard knows it, and the about-beads as carried.

| argument | type | |
|---|---|---|
| `bead` | string | the bead: a handle, or `bead@peer` for a question another yard asked |

```
POST /call/Bead
{"bead":"yd-links"}

{
  "result": {
    "id": "yd-links",
    "title": "Move the links",
    "body": "The links file declares the new source.",
    "stage": "new",
    "topic": "topic:linear-out",
    "labels": ["topic:linear-out"],
    "notes": [
      {
        "seq": 2,
        "kind": "note",
        "author": "yardmaster",
        "text": "the test passed on a copy",
        "at": "2026-10-08T09:30:00Z",
        "files": [
          {
            "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
            "type": "image/png",
            "bytes": 70,
            "name": "shed.png"
          }
        ]
      },
      {
        "seq": 15,
        "kind": "note",
        "author": "jo",
        "text": "read on the train: the test on a copy is enough for me",
        "at": "2026-10-08T09:30:00Z",
        "via": "phone"
      }
    ],
    "asks": [
      {
        "ask": 3,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Move this yard's links to the declared source now?",
        "context": ["The links file declares the new source; 64 links still point at the old one.", "The move was tested on a copy just now and passed."],
        "options": [
          {
            "label": "Move them now",
            "consequence": "all 64 links point at the new source tonight"
          },
          {
            "label": "After Friday",
            "consequence": "keep the old source until the release is out"
          },
          {
            "label": "You decide",
            "consequence": "leave it to yardmaster"
          }
        ],
        "recommend": 1,
        "why": "the test passed",
        "about": [
          {
            "id": "yd-train",
            "title": "Release train",
            "stage": "new"
          }
        ],
        "state": "open"
      },
      {
        "ask": 4,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "This design before that one?",
        "context": ["both designs are shaped."],
        "options": [
          {
            "label": "This first",
            "consequence": "that one waits a day"
          },
          {
            "label": "You decide",
            "consequence": "leave it to yardmaster"
          }
        ],
        "state": "open"
      },
      {
        "ask": 6,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Send yardr-3msx and yardr-vgjp to build?",
        "context": ["both beads are shaped."],
        "options": [
          {
            "label": "Send them",
            "consequence": "both beads move to build tonight"
          },
          {
            "label": "You decide",
            "consequence": "leave it to yardmaster"
          }
        ],
        "state": "answered",
        "option": 1,
        "words": "send them tonight",
        "picks_before": 1
      },
      {
        "ask": 8,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Close sketch PR #3?",
        "context": ["the sketch landed yesterday."],
        "options": [
          {
            "label": "Close it",
            "consequence": "PR #3 closes"
          },
          {
            "label": "You decide",
            "consequence": "leave it to yardmaster"
          }
        ],
        "state": "declined",
        "words": "not mine to take"
      },
      {
        "ask": 12,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The web view cleanup: build?",
        "context": ["the cleanup is shaped."],
        "options": [
          {
            "label": "Build it",
            "consequence": "the cleanup moves to build"
          },
          {
            "label": "You decide",
            "consequence": "leave it to yardmaster"
          }
        ],
        "state": "put aside",
        "return_at": "2026-10-08T09:30:00Z"
      }
    ]
  }
}
```

```
POST /call/Bead
{"bead":"yd-ledger@north"}

{
  "result": {
    "id": "yd-ledger@north",
    "title": "Close the ledger",
    "body": "This bead lives in north's yard, at stage build.\nThe ledger is ready to close.",
    "stage": "build",
    "topic": "topic:books",
    "labels": ["topic:books"],
    "notes": [],
    "asks": [
      {
        "ask": 9,
        "to": "jo",
        "asker": "clerk@north",
        "since": "2026-10-08T09:30:00Z",
        "question": "Close the ledger tonight?",
        "context": ["The bookkeeper signed off."],
        "options": [
          {
            "label": "Close it",
            "consequence": "the ledger closes tonight"
          },
          {
            "label": "You decide",
            "consequence": "leave it to clerk"
          }
        ],
        "recommend": 1,
        "why": "signed off",
        "state": "open"
      }
    ]
  }
}
```

```
POST /call/Bead
{"bead":"yd-crm"}

{
  "result": {
    "id": "yd-crm",
    "title": "Read the CRM",
    "body": "",
    "stage": "new",
    "topic": "",
    "labels": [],
    "notes": [],
    "asks": [
      {
        "ask": 16,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The API key for the books",
        "context": ["Create one at https://books.example.com/account/api (read only)."],
        "options": [],
        "state": "answered",
        "secret": {
          "source": "books",
          "env": ["BOOKS_TOKEN"],
          "line": "a secret: BOOKS_TOKEN, for the source books (https://books.example.com)"
        },
        "given": ["BOOKS_TOKEN"]
      },
      {
        "ask": 18,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The CRM's token for updates",
        "context": ["Create one at https://crm.example.com/account/api (read only)."],
        "options": [],
        "state": "open",
        "secret": {
          "source": "crm",
          "env": ["CRM_UPDATE_TOKEN"],
          "line": "a secret: CRM_UPDATE_TOKEN, for the source crm (https://crm.example.com)"
        }
      },
      {
        "ask": 19,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The API key for the CRM",
        "context": ["Create one at https://crm.example.com/account/api (read only)."],
        "options": [],
        "state": "open",
        "secret": {
          "source": "crm",
          "env": ["CRM_TOKEN"],
          "line": "a secret: CRM_TOKEN, for the source crm (https://crm.example.com)"
        }
      },
      {
        "ask": 20,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Read the CRM's archive too?",
        "context": ["the archive holds the accounts closed before 2024."],
        "options": [
          {
            "label": "Read it",
            "consequence": "the closed accounts are read as well"
          },
          {
            "label": "You decide",
            "consequence": "leave it to yardmaster"
          }
        ],
        "state": "taken back",
        "words": "the archive is not in the CRM's API",
        "by": "yardmaster"
      },
      {
        "ask": 23,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Your id at the pager",
        "context": ["It is on the first page of pager.example.com after login."],
        "options": [],
        "state": "answered",
        "secret": {
          "source": "pager",
          "env": ["PAGER_USER"],
          "user": "jo",
          "line": "a secret: PAGER_USER, jo's own address in the source pager (https://pager.example.com); jo is told through pager once it is given and yardr user set jo notify=pager is run"
        },
        "given": ["PAGER_USER"]
      },
      {
        "ask": 25,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Your id at the phone service",
        "context": ["It is on the first page of phone.example.com after login."],
        "options": [],
        "state": "open",
        "secret": {
          "source": "phone",
          "env": ["PHONE_USER"],
          "user": "jo",
          "line": "a secret: PHONE_USER, jo's own address in the source phone (https://phone.example.com); jo is told through phone once it is given and yardr user set jo notify=phone is run"
        }
      },
      {
        "ask": 26,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Take more mail from north?",
        "context": ["north's yard was refused 4 mails in the last hour."],
        "options": [],
        "state": "answered",
        "run": {
          "argv": ["peer", "allow", "north", "--mail-limit", "100"],
          "command": "yardr peer allow north --mail-limit 100",
          "does": ["the link with north (key 6lHks9_z…) takes 100 mails and asks an hour from it. It takes 30 now."],
          "back": "yardr peer allow north --mail-limit 30"
        },
        "ran": {
          "argv": ["peer", "allow", "north", "--mail-limit", "100"],
          "result": "peer north: takes 100 mails and asks an hour, and 6400 files"
        }
      },
      {
        "ask": 28,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Take more mail from north?",
        "context": ["north's yard was refused 4 mails in the last hour."],
        "options": [],
        "state": "open",
        "run": {
          "argv": ["peer", "allow", "north", "--mail-limit", "300"],
          "command": "yardr peer allow north --mail-limit 300",
          "does": ["the link with north (key 6lHks9_z…) takes 300 mails and asks an hour from it. It takes 100 now."],
          "back": "yardr peer allow north --mail-limit 100"
        }
      },
      {
        "ask": 29,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "May the CRM be called more often?",
        "context": ["The import was refused its 61st call of the hour."],
        "options": [],
        "state": "answered",
        "run": {
          "argv": ["source", "set", "crm", "calls_per_hour=120"],
          "command": "yardr source set crm calls_per_hour=120",
          "does": ["the source crm (https://crm.example.com) may be called 120 times an hour by each session or crew member that may call it. It may be called 60 times now."],
          "back": "yardr source set crm calls_per_hour=60",
          "back_by": "only the operator"
        },
        "ran": {
          "argv": ["source", "set", "crm", "calls_per_hour=120"],
          "result": "source crm: 120 calls an hour for each caller, 10 of them updates at most"
        }
      },
      {
        "ask": 31,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "May the CRM be called more often?",
        "context": ["The import was refused its 61st call of the hour."],
        "options": [],
        "state": "open",
        "run": {
          "argv": ["source", "set", "crm", "calls_per_hour=240"],
          "command": "yardr source set crm calls_per_hour=240",
          "does": ["the source crm (https://crm.example.com) may be called 240 times an hour by each session or crew member that may call it. It may be called 120 times now."],
          "back": "yardr source set crm calls_per_hour=120",
          "back_by": "only the operator"
        }
      },
      {
        "ask": 32,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The code the sign-in page showed",
        "context": ["The login waits for what the sign-in page showed."],
        "options": [],
        "state": "answered",
        "secret": {
          "source": "",
          "env": ["INPUT"],
          "login": "claude-example1",
          "line": "a secret: INPUT, for the waiting login claude-example1"
        },
        "given": ["INPUT"]
      },
      {
        "ask": 34,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The code the second sign-in showed",
        "context": ["The login waits for what the sign-in page showed."],
        "options": [],
        "state": "open",
        "secret": {
          "source": "",
          "env": ["INPUT"],
          "login": "claude-example2",
          "line": "a secret: INPUT, for the waiting login claude-example2"
        }
      },
      {
        "ask": 35,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The token it reads with",
        "context": ["Create one at https://crm.example.com/account/api (read only)."],
        "options": [],
        "state": "answered",
        "secret": {
          "source": "crm",
          "env": ["CRM_TOKEN"],
          "crew": "yardmaster",
          "line": "a secret: CRM_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
        },
        "given": ["CRM_TOKEN"]
      },
      {
        "ask": 37,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The token it updates with",
        "context": ["Create one at https://crm.example.com/account/api (read only)."],
        "options": [],
        "state": "open",
        "secret": {
          "source": "crm",
          "env": ["CRM_UPDATE_TOKEN"],
          "crew": "yardmaster",
          "line": "a secret: CRM_UPDATE_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
        }
      },
      {
        "ask": 38,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Your CRM token for updates",
        "context": ["Create one at https://crm.example.com/account/api (read only)."],
        "options": [],
        "state": "answered",
        "secret": {
          "source": "crm",
          "env": ["CRM_UPDATE_TOKEN"],
          "as": "jo",
          "line": "a secret: CRM_UPDATE_TOKEN, jo's own credential for the source crm (https://crm.example.com), read as jo when a call is made --as jo"
        },
        "given": ["CRM_UPDATE_TOKEN"]
      },
      {
        "ask": 40,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Your CRM token",
        "context": ["Create one at https://crm.example.com/account/api (read only)."],
        "options": [],
        "state": "open",
        "secret": {
          "source": "crm",
          "env": ["CRM_TOKEN"],
          "as": "jo",
          "line": "a secret: CRM_TOKEN, jo's own credential for the source crm (https://crm.example.com), read as jo when a call is made --as jo"
        }
      },
      {
        "ask": 41,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The yard's account for gog",
        "context": ["Create one at https://gog.example.com/account/api (read only)."],
        "options": [],
        "state": "open",
        "secret": {
          "source": "gog",
          "env": ["GOG_ACCOUNT"],
          "line": "a secret: GOG_ACCOUNT, for the source gog (https://gog.example.com)"
        },
        "concealed": false
      },
      {
        "ask": 42,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The yard's other account for gog",
        "context": ["Create one at https://gog.example.com/account/api (read only)."],
        "options": [],
        "state": "answered",
        "secret": {
          "source": "gog",
          "env": ["GOG_ACCOUNT"],
          "line": "a secret: GOG_ACCOUNT, for the source gog (https://gog.example.com)"
        },
        "concealed": false,
        "given": ["GOG_ACCOUNT"]
      },
      {
        "ask": 44,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Set up the courier source?",
        "context": ["The nightly run pages through it."],
        "options": [
          {
            "label": "read",
            "consequence": "may make every read call of the source courier (https://courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source courier (https://courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source. may make the call put (POST /it) of the source courier (https://courier.example.com), which changes something there."
          },
          {
            "label": "You decide",
            "consequence": "the recommended preset, read, is granted"
          }
        ],
        "recommend": 1,
        "why": "reading is the job",
        "state": "open",
        "setup": {
          "pack": "courier",
          "label": "embedded in yardr doctest",
          "account": ["COURIER_ACCOUNT"],
          "account_file": "/home/jo/.yardr/sources/courier.env",
          "candidates": [
            {
              "source": "courier-existing",
              "file": "/home/jo/.yardr/sources/courier-existing.env",
              "values": {
                "COURIER_ACCOUNT": "jo@example.test"
              }
            }
          ],
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://courier.example.com/api. Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to group builders and crew yardmaster — pick one:"],
          "back": "yardr source rm courier; yardr source revoke courier \u003cwhat\u003e --group builders --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "courier",
              "text": "name: courier\nbase: https://courier.example.com/api\ncredential:\n  env: COURIER_TOKEN\n  header: \"Authorization: Bearer {credential}\"\n  mint: [COURIER_ACCOUNT]\n  mint_command: [courier-mint]\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n",
              "files": [
                {
                  "program": "courier-mint",
                  "text": "#!/bin/sh\nexit 1\n",
                  "sha256": "275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc"
                }
              ]
            }
          ],
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "courier",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "courier",
                  "calls": ["read", "put"]
                }
              ]
            }
          ],
          "to": [
            {
              "group": "builders"
            },
            {
              "crew": "yardmaster"
            }
          ]
        }
      },
      {
        "ask": 45,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Set up the courier source for the elders too?",
        "context": ["They read the same pages."],
        "options": [
          {
            "label": "read",
            "consequence": "may make every read call of the source courier (https://courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source courier (https://courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source. may make the call put (POST /it) of the source courier (https://courier.example.com), which changes something there."
          },
          {
            "label": "You decide",
            "consequence": "no preset is recommended, so pick one of the presets above"
          }
        ],
        "state": "declined",
        "words": "the builders' card covers it",
        "setup": {
          "pack": "courier",
          "label": "embedded in yardr doctest",
          "account": ["COURIER_ACCOUNT"],
          "account_file": "/home/jo/.yardr/sources/courier.env",
          "candidates": [
            {
              "source": "courier-existing",
              "file": "/home/jo/.yardr/sources/courier-existing.env",
              "values": {
                "COURIER_ACCOUNT": "jo@example.test"
              }
            }
          ],
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://courier.example.com/api. Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to group elders and crew yardmaster — pick one:"],
          "back": "yardr source rm courier; yardr source revoke courier \u003cwhat\u003e --group elders --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "courier",
              "text": "name: courier\nbase: https://courier.example.com/api\ncredential:\n  env: COURIER_TOKEN\n  header: \"Authorization: Bearer {credential}\"\n  mint: [COURIER_ACCOUNT]\n  mint_command: [courier-mint]\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n",
              "files": [
                {
                  "program": "courier-mint",
                  "text": "#!/bin/sh\nexit 1\n",
                  "sha256": "275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc"
                }
              ]
            }
          ],
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "courier",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "courier",
                  "calls": ["read", "put"]
                }
              ]
            }
          ],
          "to": [
            {
              "group": "elders"
            },
            {
              "crew": "yardmaster"
            }
          ]
        }
      },
      {
        "ask": 47,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The API key Claude Code will use",
        "context": ["Create an API key for Claude Code usage."],
        "options": [],
        "state": "open",
        "secret": {
          "source": "",
          "env": ["ANTHROPIC_API_KEY"],
          "crew": "yardmaster",
          "harness": "claude",
          "line": "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, for crew member yardmaster; the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
        }
      },
      {
        "ask": 48,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "The API key Claude Code needs at first login",
        "context": ["Create an API key for Claude Code usage."],
        "options": [],
        "state": "answered",
        "secret": {
          "source": "",
          "env": ["ANTHROPIC_API_KEY"],
          "crew": "yardmaster",
          "harness": "claude",
          "line": "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, for crew member yardmaster; the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
        },
        "given": ["ANTHROPIC_API_KEY"]
      }
    ]
  }
}
```

| field of the result | type | |
|---|---|---|
| `id` | string | the bead's handle, or `bead@peer` for a question another yard asked |
| `title` | string | the bead's title |
| `body` | string | the bead's body; on a `bead@peer`, one first line saying the bead lives in the other yard and at which stage, then the body as carried |
| `stage` | string | the bead's stage |
| `topic` | string | the bead's topic: its first `topic:` label, `""` when it has none |
| `labels` | array of string | the bead's labels |
| `notes[].seq` | number | the note's seq |
| `notes[].kind` | string | the note's kind: `note`, `journal` or `handoff` |
| `notes[].author` | string | who wrote it |
| `notes[].text` | string | the note |
| `notes[].at` | time | when it was written |
| `notes[].via` | string, may be absent | the [device](#over-the-network) the note was written through; absent on a note written on the yard's own machine |
| `notes[].files` | array, may be absent | the files the note carries, each an image, a page or a file, in the order it was written with them; absent on a note without any |
| `notes[].files[].hash` | string | the file's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes |
| `notes[].files[].type` | string | what the bytes say: `image/png` or `image/jpeg` for an image, `text/html; charset=utf-8` for a page |
| `notes[].files[].name` | string, may be absent | the file's base name as the sender gave it; absent on a paste and on a letter from before names |
| `notes[].files[].bytes` | number | how many bytes it is |
| `asks[].ask` | number | the ask note's seq: what an answer names |
| `asks[].to` | string | who must answer |
| `asks[].asker` | string | who asked: a name, or `name@peer` for a question another yard asked |
| `asks[].since` | time | when the ask was written (as sent, for a question another yard asked) |
| `asks[].question` | string | the question in one line, as a person would ask it |
| `asks[].context` | array of string | what the question is about, two to five lines |
| `asks[].options` | array | the options in order, `you decide` last; none wants words |
| `asks[].options[].label` | string | the option's label |
| `asks[].options[].consequence` | string | what picking it does, in a line |
| `asks[].recommend` | number, may be absent | the recommended option's number; absent when nothing is recommended |
| `asks[].why` | string, may be absent | why the recommended option, in a line; absent with `recommend` |
| `asks[].about` | array, may be absent | the other beads the question is about; absent when there are none |
| `asks[].about[].id` | string | the bead's handle |
| `asks[].about[].title` | string | the bead's title |
| `asks[].about[].stage` | string | the bead's stage |
| `asks[].state` | string | `open`, `answered`, `put aside`, `declined` or `taken back`. One a client does not know is not open: it takes no answer |
| `asks[].option` | number, may be absent | the picked option's number; absent until one is picked |
| `asks[].words` | string, may be absent | what went with the pick, the words answer, or why the question is declined or taken back; absent when there are none |
| `asks[].by` | string, may be absent | who took the ask back, its asker or the operator; absent on every other state |
| `asks[].return_at` | time, may be absent | when a put-aside ask comes back; absent otherwise |
| `asks[].picks_before` | number, may be absent | how many picks there were before the current one, so a client can say "changed"; absent when there were none |
| `asks[].secret` | object, may be absent | what a secret ask asks for; absent on any other ask. Such an ask has no options and takes no words: show it with a field that does not echo, and answer it with [`GiveSecret`](#for-the-operator-alone). `AnswerAsk` refuses a pick and words on it, and `DeclineAsk` words |
| `asks[].secret.source` | string | the declared source the secret is the credential of; with `user`, `crew` or `as`, the source the address or credential is in; empty on a login or harness-key ask |
| `asks[].secret.env` | array of string | the names to ask the person a value for, each a variable of the source's credential; with `user`, the one variable the source reads of a user's file; with `crew`, variables of the source's credential; with `as`, the names a call as that user mints or uses; on a login ask, the one name the input is asked for; on a harness-key ask, variables of the kind's `login_env` |
| `asks[].secret.user` | string, may be absent | the user whose own address in the source is asked for (`yardr bead ask --secret <source> --for-user <user>`): the value goes to a file of that user's, and is theirs to give or the operator's. Absent on an ask for the source's credential. A client needs nothing new for it: the field, the call and `line` are the same |
| `asks[].secret.crew` | string, may be absent | the crew member whose own credential in the source or harness API key is asked for: the value goes to a file of that crew member's and is the operator's to give. A source credential takes effect when the crew member next starts; a harness key restarts sessions of that kind waiting at the login screen when given. Absent on any other secret ask |
| `asks[].secret.as` | string, may be absent | the user whose own credential in the source is asked for (`yardr bead ask --secret <source> --as <user>`): the value goes to a file of that user's, and is theirs to give or the operator's. Absent on any other secret ask. A client needs nothing new for it: the field, the call and `line` are the same |
| `asks[].secret.login` | string, may be absent | the waiting login the value goes to (`yardr bead ask --secret-login <login>`): the reference the login's leader printed in its input need, handed the value and kept nowhere. Absent on any other secret ask. A client needs nothing new for it: the field, the call and `line` are the same |
| `asks[].secret.harness` | string, may be absent | the harness kind whose API key is asked for; absent on any other secret ask |
| `asks[].secret.line` | string | what the card says is asked for, in the yard's words and never the asker's: the names, the source, and where the source's calls go; with `user`, whose address it is and the command that tells them through the source; with `crew`, whose credential it is, that the env_file line is the one written, and when it takes effect; with `as`, whose credential it is and that a call made as them reads it; with `login`, the waiting login the value goes to; with `harness`, whose API key it is and when it takes effect. Show it as it is |
| `asks[].concealed` | bool, may be absent | whether a secret ask's value is drawn hidden: false where the source marks every asked name as no secret, which is shown with a field that echoes; absent (concealed) on any other ask |
| `asks[].given` | array of string, may be absent | the names a secret ask was given values for, or the account names a setup ask was approved with, which reads "gave `<names>`"; absent on any other ask and until it is given |
| `asks[].run` | object, may be absent | the command a run ask proposes: one yardr command that is the operator's to give, which the yard runs when the operator approves the ask. Absent on any other ask. Such an ask has no options and takes no words: `AnswerAsk` refuses a pick and words on it, `DeferAsk` and `DeclineAsk` take it as any ask, and it is approved with [`ApproveAsk`](#for-the-operator-alone), by the operator: at the yard (`yardr bead answer <bead> <ask> --approve`), or from a device of the operator. A command that is a user's own (`subscribe <user> yardr`, `user set <user> tell=<parts>`, `user key <user> <source>`) is approved at the yard by the one it was asked of, that user or the operator. All of `run` is the yard's, made when the ask was written, and none of it the asker's: show it apart from `question` and `context`, which are the asker's words, and draw those as plain text |
| `asks[].run.argv` | array of string | the command in words, without the leading `yardr`, as the yard parsed it |
| `asks[].run.command` | string | the command whole, as it is typed in a terminal and as it would run. Never shorten it: a view that cannot show it whole says where the ask can be read instead |
| `asks[].run.does` | array of string | what the command changes and what is there now, in the yard's words, a sentence each, with what it names as the yard knows it |
| `asks[].run.back` | string | the command that takes the change back |
| `asks[].run.back_by` | string, may be absent | who may run `back`, in words that stand before "can take it back": `only the operator` for a source's limits, `<user> or the operator` for a user's subscription and for what they are told of. Absent where that is anyone, which reads "anyone can take it back" |
| `asks[].ran` | object, may be absent | the command the yard ran when a run ask was approved, which reads "approved, ran: `<command>`" with `result` under it; or the setup a setup ask approved, which reads "approved, being carried out", "approved, ran" or "approved, failed" with the preset and the names given; absent on any other ask and until it is approved. The ask's `state` is then `answered` |
| `asks[].ran.argv` | array of string | the command that ran, in words: the one the ask proposed; on a setup ask, `setup` with the pack's name |
| `asks[].ran.result` | string | what the command printed, made by the yard: a line for each thing it changed, with a line break between them; on a setup ask not yet carried out, `approved, to be carried out`, and afterwards what the carrying out printed |
| `asks[].setup` | object, may be absent | the setup a setup ask proposes: the pack with the yard's sentences for what approving it does, the account to type beside the preset pick or the candidates to take instead, and the presets the ask's options count. Absent on any other ask. `AnswerAsk` refuses a pick and words on it, `DeferAsk` and `DeclineAsk` take it as any ask, and it is approved with [`ApproveSetup`](#for-the-operator-alone), by the operator: at the yard (`yardr bead answer <bead> <ask> <n> --secret`, or `--account <source>` where the card lists one), or from a device of the operator. All of `setup` is the yard's, made when the ask was written, and none of it the asker's: show it apart from `question` and `context`, which are the asker's words, and draw those as plain text |
| `asks[].setup.pack` | string | the embedded pack to set up, of kind source, whose `yardr pack show <pack>` prints the presets numbered |
| `asks[].setup.label` | string, may be absent | the build the pack is embedded in, as the card shows it |
| `asks[].setup.account` | array of string | the names to type unseen beside the preset pick: the mint's inputs where the pack's credential is minted, else every credential name. Where the card lists candidates, the answer names one of them or types another |
| `asks[].setup.account_file` | string | the file a typed value goes to, on the yard's machine |
| `asks[].setup.candidates` | array, may be absent | yard sources whose account the card offers to reuse; absent where none match |
| `asks[].setup.candidates[].source` | string | the source whose account the card offers |
| `asks[].setup.candidates[].file` | string | the file holding the shared account inputs |
| `asks[].setup.candidates[].values` | object | the shared account input names and values the file holds |
| `asks[].setup.does` | array of string | what approving changes and what is there now, in the yard's words, a sentence each, with what it names as the yard knows it |
| `asks[].setup.back` | string | the commands that take the setup back |
| `asks[].setup.back_by` | string, may be absent | who may run `back`, in words that stand before "can take it back". Absent where that is anyone, which reads "anyone can take it back" |
| `asks[].setup.carried` | array | what the pack brings, behind the card's sentences for checking the reading: each source's whole declaration with its command files' bytes, the inventory of the copies it would replace, and the replacement diffs |
| `asks[].setup.presets` | array | the pack's presets with their grants, in the order the ask's options count them |
| `asks[].setup.presets[].name` | string | the preset's name, as the option's label shows it |
| `asks[].setup.presets[].grants` | array | what of each source the preset grants |
| `asks[].setup.presets[].grants[].source` | string | the source the pack declares |
| `asks[].setup.presets[].grants[].calls` | array of string | `read` for every read call, calls by name |
| `asks[].setup.to` | array | who the grants are for |
| `asks[].setup.to[].group` | string, may be absent | the group, and so each session it starts |
| `asks[].setup.to[].crew` | string, may be absent | the crew member |

Read it again on every event whose `bead` is the bead: `noted` and the bead's other events, whose kinds are yardr's own.

### `Arrivals`

The arrivals board: the open beads still travelling, everything but the backlog, up to 15 rows in arrival order, beside the platforms in the order the beads travel. It is what footplate draws: the yard computes the board once so the readers draw without computing. A row is the bead, its title, its platform (stage) and line (depot), the expected arrival as the yard's printed range, and the remark with its tone, and for a held bead who holds it; no via, no badge.

```
POST /call/Arrivals
{}

{
  "result": {
    "rows": [
      {
        "bead": "yd-train",
        "title": "Release train",
        "platform": "new",
        "line": "yard",
        "expected": "—",
        "remark": "",
        "tone": "none"
      },
      {
        "bead": "yd-links",
        "title": "Move the links",
        "platform": "new",
        "line": "",
        "expected": "—",
        "remark": "",
        "tone": "none"
      },
      {
        "bead": "yd-greet",
        "title": "The greeting",
        "platform": "new",
        "line": "drafts",
        "expected": "—",
        "remark": "",
        "tone": "none"
      },
      {
        "bead": "yd-shed",
        "title": "Paint the shed",
        "platform": "new",
        "line": "",
        "expected": "—",
        "remark": "",
        "tone": "none"
      },
      {
        "bead": "yd-crm",
        "title": "Read the CRM",
        "platform": "new",
        "line": "",
        "expected": "—",
        "remark": "",
        "tone": "none"
      },
      {
        "bead": "yd-gate",
        "title": "Mend the gate",
        "platform": "new",
        "line": "yard",
        "expected": "—",
        "remark": "held by jo",
        "tone": "red",
        "held_by": "jo"
      },
      {
        "bead": "yd-fence",
        "title": "Paint the fence",
        "platform": "new",
        "line": "yard",
        "expected": "—",
        "remark": "held by yardmaster",
        "tone": "red",
        "held_by": "yardmaster"
      },
      {
        "bead": "yd-siding",
        "title": "Clear the siding",
        "platform": "new",
        "line": "",
        "expected": "—",
        "remark": "held by yard",
        "tone": "red",
        "held_by": "yard"
      }
    ],
    "platforms": ["new"]
  }
}
```

| field of the result | type | |
|---|---|---|
| `rows[].bead` | string | the bead's handle |
| `rows[].title` | string | the bead's title |
| `rows[].platform` | string | the stage the bead stands at |
| `rows[].line` | string | the depot the bead is worked in, `""` when it has none |
| `rows[].expected` | string | the expected arrival as the yard prints it: the usual range, one span when both ends agree, `—` where the yard serves none, or the bead is blocked by an open bead |
| `rows[].remark` | string | held (`held by <name>` where the yard knows who holds the bead), stalled, blocked by an open bead, waits for a person, late (`late by 40m`: how far past its usual time, in the yard's span format; plain `late` where the yard cannot tell by how much), in that order; `""` when there is none |
| `rows[].tone` | string | `red` for a hold and a stall, `amber` for a block, a wait and a late mark, `none` with no remark |
| `rows[].held_by` | string, may be absent | who holds a held bead, as the remark names them: the person who held it, the agent of the session or the crew member whose pane held it, `yard` where the yard held it itself, `peer:<name>` where the outcome a linked yard sent back held it. Absent where the bead is not held, and where the yard does not know who held it (a hold from before it kept the holder): the remark is then `held` |
| `platforms` | array of string | the stages the rows stand on, in the order the beads travel: group the rows by this list |

The order is the board's: beads with a running session first, then by the low end of the expected arrival in minutes, then by how long the bead has stood at its stage, oldest first, then by id. A low that reads as no span — `under a minute` among it — sorts with the beads the yard serves no ETA for. The remark is the board's too, but for a hold's holder: held (`held by <name>`, or `held` where the yard does not know who held the bead), stalled (the latest ended session came to no good end), blocked by an open bead, waits for a person, late, in that order. A late bead says by how much where the yard can tell: its time in its stage past the usual, in the yard's span format (`late by 1h 5m`), else `late`.

Platforms lists the stages the rows stand on, in the order the beads travel: each depot's flow stage order, as the yard draws it on its flows page. Where depots' flows order the same stages differently, the depot with the most travelling beads sets the order and the others' extra stages stand where their own flows put them after the last stage they share with it, and a stage no flow has stands last, in row order.

Read it again on every event that carries a bead, and on the events without one that move its inputs: `group_set` and `group_removed`, `route_added`, `route_set` and `route_removed`, `flow_set`, `flow_removed`, `flow_pointer_set` and `flow_pointer_cleared`, `depot_updated`, and `setting_changed` whose `key` is `max_sessions` or `operator`. One minute is the ETA's grain: no timed refresh.

### `Flows`

The flows view: the yard's depots as lines, each with its travelling total, its flows, each flow its stages in travel order with the open beads on each and its lamp, and its Elsewhere rows; the beads travelling with no line; and the yard-wide travelling total. It is what footplate's timetable draws: the yard computes the view once so the readers draw without computing. Each bead's row is its Arrivals row, with whether it is worked; each stage's lamp is the panel's: fault where a held, blocked or stranded bead stands on it, turn where only a person's turn does, none otherwise.

```
POST /call/Flows
{}

{
  "result": {
    "travelling": 8,
    "depots": [
      {
        "name": "drafts",
        "kind": "dir",
        "travelling": 1,
        "flows": [],
        "elsewhere": [
          {
            "bead": "yd-greet",
            "title": "The greeting",
            "expected": "—",
            "remark": "",
            "tone": "none",
            "working": true,
            "worker": "herdr greet-new",
            "entered_stage_at": "2026-10-08T09:30:00Z"
          }
        ],
        "error": ".yardr/flows/default.flow: flow line 1: want 'stage -\u003e next on outcome | other' or 'stage terminal', got \"this is no flow\""
      },
      {
        "name": "yard",
        "kind": "git",
        "travelling": 3,
        "flows": [
          {
            "name": "default",
            "type": "",
            "source": "store",
            "stages": [
              {
                "stage": "new",
                "group": "builders",
                "human": false,
                "lamp": "fault",
                "terminal": false,
                "limit": 5,
                "taken": 3,
                "beads": [
                  {
                    "bead": "yd-gate",
                    "title": "Mend the gate",
                    "expected": "—",
                    "remark": "held by jo",
                    "tone": "red",
                    "held_by": "jo",
                    "working": false
                  },
                  {
                    "bead": "yd-train",
                    "title": "Release train",
                    "expected": "—",
                    "remark": "",
                    "tone": "none",
                    "working": true,
                    "worker": "herdr train-new",
                    "entered_stage_at": "2026-10-08T09:30:00Z"
                  }
                ]
              },
              {
                "stage": "shipped",
                "group": "elders",
                "human": true,
                "lamp": "none",
                "terminal": true,
                "limit": 5,
                "taken": 0,
                "beads": []
              }
            ]
          }
        ],
        "elsewhere": [
          {
            "bead": "yd-fence",
            "title": "Paint the fence",
            "expected": "—",
            "remark": "held by yardmaster",
            "tone": "red",
            "held_by": "yardmaster",
            "working": false
          }
        ]
      }
    ],
    "unassigned": [
      {
        "bead": "yd-crm",
        "title": "Read the CRM",
        "expected": "—",
        "remark": "",
        "tone": "none",
        "working": false
      },
      {
        "bead": "yd-siding",
        "title": "Clear the siding",
        "expected": "—",
        "remark": "held by yard",
        "tone": "red",
        "held_by": "yard",
        "working": false
      },
      {
        "bead": "yd-links",
        "title": "Move the links",
        "expected": "—",
        "remark": "",
        "tone": "none",
        "working": true,
        "worker": "herdr links-new",
        "entered_stage_at": "2026-10-08T09:30:00Z"
      },
      {
        "bead": "yd-shed",
        "title": "Paint the shed",
        "expected": "—",
        "remark": "",
        "tone": "none",
        "working": false
      }
    ]
  }
}
```

| field of the result | type | |
|---|---|---|
| `travelling` | number | the yard's open beads outside the backlog: the depots' totals plus the unassigned count, complete, not capped |
| `depots[].name` | string | the depot |
| `depots[].kind` | string | `git` or `dir`, as the depot is |
| `depots[].travelling` | number | the depot's open beads outside the backlog |
| `depots[].flows[].name` | string | the flow |
| `depots[].flows[].type` | string | the bead type it governs, `""` for the depot's own |
| `depots[].flows[].source` | string | where the flow comes from, as `yardr flow show` says it: `store`, `repo@<sha>`, `repo (directory)` |
| `depots[].flows[].stages[].stage` | string | the stage |
| `depots[].flows[].stages[].group` | string | the group a bead of the flow's type would be routed to here, `""` when no route matches |
| `depots[].flows[].stages[].human` | bool | the group is a manual one |
| `depots[].flows[].stages[].lamp` | string | `fault` where a held, blocked or stranded bead stands here, `turn` where only a person's turn does, `none` otherwise |
| `depots[].flows[].stages[].terminal` | bool | the stage closes the bead entering it |
| `depots[].flows[].stages[].limit` | number, may be absent | the routed group's seat limit, `0` pausing the group; absent where no route matches |
| `depots[].flows[].stages[].taken` | number, may be absent | how many of the routed group's seats live sessions hold across the whole yard, the same number on every stage routed to the group; absent where no route matches |
| `depots[].flows[].stages[].beads[].bead` | string | the bead's handle |
| `depots[].flows[].stages[].beads[].title` | string | the bead's title |
| `depots[].flows[].stages[].beads[].expected` | string | the expected arrival as the yard prints it: the usual range, one span when both ends agree, `—` where the yard serves none, or the bead is blocked by an open bead |
| `depots[].flows[].stages[].beads[].remark` | string | held (`held by <name>` where the yard knows who holds the bead), stalled, blocked by an open bead, waits for a person, late (`late by 40m`: how far past its usual time, in the yard's span format; plain `late` where the yard cannot tell by how much), in that order; `""` when there is none |
| `depots[].flows[].stages[].beads[].tone` | string | `red` for a hold and a stall, `amber` for a block, a wait and a late mark, `none` with no remark |
| `depots[].flows[].stages[].beads[].held_by` | string, may be absent | who holds a held bead, as the remark names them: the person who held it, the agent of the session or the crew member whose pane held it, `yard` where the yard held it itself, `peer:<name>` where the outcome a linked yard sent back held it. Absent where the bead is not held, and where the yard does not know who held it (a hold from before it kept the holder): the remark is then `held` |
| `depots[].flows[].stages[].beads[].working` | bool | an agent's or a command's session claimed it; a manual group's claim is a person's turn and no work |
| `depots[].flows[].stages[].beads[].worker` | string, may be absent | who works it, as `yardr session list` shows the claiming session: the runner with the agent's name or pid; absent where no session claimed it, and on a manual group's claim |
| `depots[].flows[].stages[].beads[].entered_stage_at` | time, may be absent | when the bead entered its stage, as an RFC 3339 time; absent where no session claimed it, and where the stay has no clock |
| `depots[].elsewhere[].bead` | string | the bead's handle |
| `depots[].elsewhere[].title` | string | the bead's title |
| `depots[].elsewhere[].expected` | string | the expected arrival as the yard prints it: the usual range, one span when both ends agree, `—` where the yard serves none, or the bead is blocked by an open bead |
| `depots[].elsewhere[].remark` | string | held (`held by <name>` where the yard knows who holds the bead), stalled, blocked by an open bead, waits for a person, late (`late by 40m`: how far past its usual time, in the yard's span format; plain `late` where the yard cannot tell by how much), in that order; `""` when there is none |
| `depots[].elsewhere[].tone` | string | `red` for a hold and a stall, `amber` for a block, a wait and a late mark, `none` with no remark |
| `depots[].elsewhere[].held_by` | string, may be absent | who holds a held bead, as the remark names them: the person who held it, the agent of the session or the crew member whose pane held it, `yard` where the yard held it itself, `peer:<name>` where the outcome a linked yard sent back held it. Absent where the bead is not held, and where the yard does not know who held it (a hold from before it kept the holder): the remark is then `held` |
| `depots[].elsewhere[].working` | bool | an agent's or a command's session claimed it; a manual group's claim is a person's turn and no work |
| `depots[].elsewhere[].worker` | string, may be absent | who works it, as `yardr session list` shows the claiming session: the runner with the agent's name or pid; absent where no session claimed it, and on a manual group's claim |
| `depots[].elsewhere[].entered_stage_at` | time, may be absent | when the bead entered its stage, as an RFC 3339 time; absent where no session claimed it, and where the stay has no clock |
| `depots[].error` | string, may be absent | why the depot's flows could not be read; its beads are then all in Elsewhere; absent when they read |
| `unassigned[].bead` | string | the bead's handle |
| `unassigned[].title` | string | the bead's title |
| `unassigned[].expected` | string | the expected arrival as the yard prints it: the usual range, one span when both ends agree, `—` where the yard serves none, or the bead is blocked by an open bead |
| `unassigned[].remark` | string | held (`held by <name>` where the yard knows who holds the bead), stalled, blocked by an open bead, waits for a person, late (`late by 40m`: how far past its usual time, in the yard's span format; plain `late` where the yard cannot tell by how much), in that order; `""` when there is none |
| `unassigned[].tone` | string | `red` for a hold and a stall, `amber` for a block, a wait and a late mark, `none` with no remark |
| `unassigned[].held_by` | string, may be absent | who holds a held bead, as the remark names them: the person who held it, the agent of the session or the crew member whose pane held it, `yard` where the yard held it itself, `peer:<name>` where the outcome a linked yard sent back held it. Absent where the bead is not held, and where the yard does not know who held it (a hold from before it kept the holder): the remark is then `held` |
| `unassigned[].working` | bool | an agent's or a command's session claimed it; a manual group's claim is a person's turn and no work |
| `unassigned[].worker` | string, may be absent | who works it, as `yardr session list` shows the claiming session: the runner with the agent's name or pid; absent where no session claimed it, and on a manual group's claim |
| `unassigned[].entered_stage_at` | time, may be absent | when the bead entered its stage, as an RFC 3339 time; absent where no session claimed it, and where the stay has no clock |

Every depot stands here, travelling or not, and a removed depot's line leaves with it: `DeleteDepot` refuses while open beads remain. The unassigned rows are the open beads outside the backlog with no depot, the newest change first.

Read it again on every event that carries a bead, and on the events without one that move its inputs: `group_set` and `group_removed`, `route_added`, `route_set` and `route_removed`, `flow_set`, `flow_removed`, `flow_pointer_set` and `flow_pointer_cleared`, `depot_updated` and `depot_removed`, and `setting_changed` whose `key` is `max_sessions` or `operator`. One minute is the ETA's grain: no timed refresh.

### `AnswerAsk`

Answers an ask: picks an option with words alongside, or answers an ask with no options with words. It is what `yardr bead answer` does with an option or `--words`: words alone on an ask with options are refused, and go as a plain note instead. On a `bead@peer`, a question another yard asked, the answer is queued home for that yard instead of written as a note: only the person the question is for may give it, the link must carry `ask` out, and words alone on an ask with options are refused with the address to mail the asker at. It answers nothing; the answer is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `option` | number | the picked option's number, as the bead shows it; `0` with words answers an ask with no options |
| `words` | string | what goes with the pick, or the answer; left out, the pick stands alone |
| `actor` | string | who answers: the user's name |

```
POST /call/AnswerAsk
{"bead":"yd-links","ask":3,"option":1,"words":"move them tonight","actor":"jo"}

{}
```

### `DeferAsk`

Puts the ask aside: it leaves the person's queue for 24 hours or until the asker's next note on the bead, whichever is first. It is what `yardr bead answer` does with `--later`. On a `bead@peer` it is 24 hours by this yard's clock and nothing else, and the put-aside travels home as an answer does. It answers nothing; putting it aside is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `actor` | string | who puts it aside: the user's name |

```
POST /call/DeferAsk
{"bead":"yd-links","ask":4,"actor":"jo"}

{}
```

### `DeclineAsk`

Ends the ask with no option picked: the person will not decide it. It is what `yardr bead answer` does with `--decline`, and reads wherever the ask is shown as "`<user>` declined to decide this: `<words>`". On a `bead@peer` the decline travels home as an answer does, and ends the ask in both yards. It answers nothing; declining is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `words` | string | why they will not decide it |
| `actor` | string | who declines: the user's name |

```
POST /call/DeclineAsk
{"bead":"yd-greet","ask":5,"words":"the greeting can wait","actor":"jo"}

{}
```

### `TakeBackAsk`

Takes an open ask back: its asker asks it no more, and says why. It is what `yardr bead ask --take-back` does. The ask leaves the person's open decisions and stands among the decided as `taken back`, with `by` and the reason as `words`: "taken back by `<by>`: `<words>`", never an answer of the person. It is the asker's to do and the operator's, and is refused for anyone else; refused too on an ask that is answered, declined or taken back already, and on a `bead@peer`, which is taken back in the yard that asked. An ask put aside is taken back as an open one is. `AnswerAsk`, `DeferAsk`, `DeclineAsk` and `GiveSecret` refuse it afterwards, naming who took it back and why. Where the ask also stands in a linked yard it leaves the decisions there as well, with `it was taken back in <peer>'s yard`. It answers nothing; taking it back is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `reason` | string | why it is asked no more, in a line: the person reads it where the question stood |
| `actor` | string | who takes it back: the asker's name, or the operator's |

```
POST /call/TakeBackAsk
{"bead":"yd-greet","ask":22,"reason":"the design settles the greeting","actor":"jo"}

{}
```

### `AddNote`

Puts a plain note on the bead, as `yardr bead note` does. Words about a question with no option chosen go here, and leave the ask open. On a `bead@peer`, a question another yard asked, it is refused: notes stay local, and words for the asker go as mail to `asker@peer`. With `files` the note carries files, as one written with `yardr bead note --file` does. It answers nothing; the note is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `text` | string | the note; it may not be empty when the note carries files |
| `actor` | string | who writes it: the user's name |
| `files` | array of object | the files the note carries, at most 64: each `{hash, name}`, the hash as `PutBlob` answered it with the file's base name. Left out, the note carries none. One the yard does not hold, or a name no letter may carry, refuses the note |

```
POST /call/AddNote
{"bead":"yd-greet","text":"the newcomer arrives Monday","actor":"jo"}

{}
```

```
POST /call/AddNote
{"bead":"yd-greet","text":"the newcomer arrives Monday, in this doorway","actor":"jo","files":[{"hash":"497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581","name":"doorway.png"}]}

{}
```

<!-- /calls -->

## For the operator alone

The calls of this list are as stable as the calls above, and are the
operator's alone to make. In yardr's code the list is `OperatorCalls`, and
it has three calls: `GiveSecret`, which gives the credential a secret ask
asks for, `ApproveAsk`, which approves a run ask so that the yard runs
the command it holds, and `ApproveSetup`, which approves a setup ask with
the preset pick and the account together. Where they are served, and to
whom:

- **On the socket**, to the operator: `actor` is the operator's name, and
  the call does not come from the pane of an agent.
- **On the [listener](#over-the-network)**, to a device of the operator:
  a request whose token is one the yard gave a device that the operator
  added for themselves (`yardr user device add <operator> <device>`, or a
  pairing). `actor` is the operator or left out. The yard knows there who
  calls by the token and not by a word of the caller's; it cannot know
  whether an agent on some machine sent the request, so whoever holds
  that token gives and approves as the operator.
- **One giving is a user's own too.** A secret ask with `secret.user`
  asks for that user's own address in a source that tells people (for
  Pushover the user key), and the value goes to a file of theirs, not to
  a credential of the yard. On the socket it is given by that user or the
  operator, and not from the pane of an agent. On the listener a device
  of that user is served the call for that ask and for no other, as a
  device of the operator is; `actor` is the user or left out. No
  approval is so: a run ask for a user's own subscription or tell is approved by
  that user on the socket, and `ApproveAsk` is not there for a device of
  theirs.
- **To nobody else.** A device of any other user, and a caller the yard
  knows by anything but a device token of its own, is answered as for a
  method the yard does not have (`no such method GiveSecret`, `no such
  method ApproveAsk`). So is every device in a yard with no operator, but
  for an ask for its user's own address. For `ApproveAsk` each of them is
  answered before the request's body is read. For `GiveSecret` a caller
  without a device token is; a device of a user who is not the operator
  is read for the ask it names, and nothing of it is said back. Against
  such an answer a client says "answer it at the yard" and sends its user
  to a terminal there (`ssh -t <host> yardr bead answer <bead> <ask>
  --secret`, or `--approve`).

Over the listener the value travels under the TLS in front of it and under
no other protection, and the yard takes it only where it can see that the
request did not come in plain ([TLS is yours](#tls-is-yours-and-the-clients-rule)).
It never crosses a link to another yard: a secret ask is not sent to a
peer, and is answered at the yard that asked.

What a give through a device writes says how it came, as its other writes
do: the `noted` event of the answer, and `source_set` (for a user's own
address `user_set`) where the file is new, carry `data.via`. So does what
an approval through a device writes: the `noted` event of the answer, and
the event of the change the command made (`peer_allowed`, `source_set`,
`source_granted`, `subscribed`, `user_set`, `user_added`, `user_removed`,
`crew_defined`, `setting_changed`, `peer_renamed`, `originator_changed`,
`timer_removed`), which also carries `approved` with the bead and the ask.

An approval never crosses a link to another yard either: a run ask is
approved in the yard that asked, and `ApproveAsk` refuses a `bead@peer`.
What the yard runs is the command stored on the ask, `asks[].run.command`,
and no argument of the call names or changes it. Show that command whole
and the yard's sentences (`asks[].run.does`) directly above the control
that approves, give that control a key no other card answers with, and
never approve on Return alone.

A value given with `GiveSecret` is the person's secret. Read it into a
field that does not echo, send it in this call and in nothing else, and
keep it nowhere: not in a draft, a notice, a log or an error. Empty the
field when the call returns, whatever it answered.

<!-- local: made by the test of internal/rpc from what a yard answers, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### `GiveSecret`

Answers a secret ask (an ask of `Bead` with `secret`): the values go into the file the source's credential is read from, and the answer on the bead holds the names and never a value. A value is kept in that file and nowhere else: not in the store, an event, a note or the log, and no call reads one back. It is what `yardr bead answer --secret` does. It is the operator's to give: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. An ask with `secret.user` asks for that user's own address in the source instead: the value goes into the user's `env_file`, or with none set into `users/<user>.env` in the yard's home, which is then set; it is that user's to give or the operator's, on the socket and from a device of either, and it does not set the user's `notify`. An ask with `secret.crew` asks for that crew member's own credential in the source instead: the value goes into the crew member's `env_file`, or with none set into `crew/<name>.env` in the yard's home, which is then set; it is the operator's to give, on the socket and from a device of the operator, restarts nothing, and takes effect when the crew member next starts. An ask with `secret.harness` asks for that crew member's harness API key instead: the names come from the harness kind's `login_env`; the values go into the crew member's `env_file`, or with none set into `crew/<name>.env` in the yard's home, which is then set. It is the operator's to give, on the socket and from a device of the operator; it is kept in that file and not in the store, an event, a note or the log. Giving it restarts sessions of that kind waiting at the login screen, and new sessions use the key. An ask with `secret.as` asks for that user's own credential in the source instead: the value goes into the user's `env_file`, or with none set into `users/<user>.env` in the yard's home, which is then set; it is that user's to give or the operator's, on the socket and from a device of either, and the source needs no `notify` key. An ask with `secret.login` asks for the input of the harness login that waits under that reference instead: the value is handed to the waiting login and kept nowhere — not in a file, the store, an event, a note or the log — and the answer holds the names as any giving does; it is the operator's to give, on the socket and from a device of the operator, and is refused when no login waits under the reference. Refused too: an ask that is declined, names that are not exactly `asks[].secret.env`, an empty value, and one with a line break or white space at either end; a refusal never repeats a value. Giving again replaces what was given. The answer is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `values` | object | each name of `asks[].secret.env` to the value the person typed |
| `actor` | string | who gives: the operator's name, or for an ask with `secret.user` or `secret.as` that user's |

```
POST /call/GiveSecret
{"bead":"yd-crm","ask":18,"values":{"CRM_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo"}

{
  "result": {
    "given": ["CRM_UPDATE_TOKEN"],
    "file": "/home/jo/.yardr/sources/crm.env",
    "missing": "env_file /home/jo/.yardr/sources/crm.env does not set CRM_TOKEN"
  }
}
```

```
POST /call/GiveSecret
{"bead":"yd-crm","ask":19,"values":{"CRM_TOKEN":"what-the-person-typed"},"actor":"jo"}

{
  "result": {
    "given": ["CRM_TOKEN"],
    "file": "/home/jo/.yardr/sources/crm.env",
    "credential": "CRM_TOKEN is set in /home/jo/.yardr/sources/crm.env; for an update, CRM_UPDATE_TOKEN is set in /home/jo/.yardr/sources/crm.env"
  }
}
```

```
POST /call/GiveSecret
{"bead":"yd-crm","ask":25,"values":{"PHONE_USER":"what-the-person-typed"},"actor":"jo"}

{
  "result": {
    "given": ["PHONE_USER"],
    "file": "/home/jo/.yardr/users/jo.env",
    "user": "jo",
    "next": "yardr user set jo notify=phone tells jo through it",
    "credential": "PHONE_USER is set in /home/jo/.yardr/users/jo.env"
  }
}
```

```
POST /call/GiveSecret
{"bead":"yd-crm","ask":34,"values":{"INPUT":"what-the-person-typed"},"actor":"jo"}

{
  "result": {
    "given": ["INPUT"],
    "file": "",
    "login": "claude-example2"
  }
}
```

```
POST /call/GiveSecret
{"bead":"yd-crm","ask":37,"values":{"CRM_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo"}

{
  "result": {
    "given": ["CRM_UPDATE_TOKEN"],
    "file": "/home/jo/.yardr/crew/yardmaster.env",
    "crew": "yardmaster",
    "next": "it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)",
    "credential": "CRM_UPDATE_TOKEN is set in /home/jo/.yardr/crew/yardmaster.env"
  }
}
```

```
POST /call/GiveSecret
{"bead":"yd-crm","ask":40,"values":{"CRM_TOKEN":"what-the-person-typed"},"actor":"jo"}

{
  "result": {
    "given": ["CRM_TOKEN"],
    "file": "/home/jo/.yardr/users/jo.env",
    "as": "jo",
    "next": "yardr source check crm --as jo",
    "credential": "CRM_TOKEN is set in /home/jo/.yardr/users/jo.env"
  }
}
```

```
POST /call/GiveSecret
{"bead":"yd-crm","ask":47,"values":{"ANTHROPIC_API_KEY":"what-the-person-typed"},"actor":"jo"}

{
  "result": {
    "given": ["ANTHROPIC_API_KEY"],
    "file": "/home/jo/.yardr/crew/yardmaster.env",
    "crew": "yardmaster",
    "next": "it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)",
    "credential": "ANTHROPIC_API_KEY is set in /home/jo/.yardr/crew/yardmaster.env"
  }
}
```

| field of the result | type | |
|---|---|---|
| `given` | array of string | the names that were written |
| `file` | string | the file they are in, on the yard's machine; empty when the value went to a waiting login instead |
| `login` | string, may be absent | the waiting login the value was handed to; absent for a source's credential |
| `credential` | string, may be absent | where every variable of the source's credential is set now, in words; absent while one is still missing. With `user`, where the address is set; with `crew` or `as`, where the credential is set |
| `missing` | string, may be absent | what the credential still wants, in words; absent when it is whole |
| `user` | string, may be absent | whose own address was given, and whose file `file` is; absent for a source's credential |
| `crew` | string, may be absent | whose own credential or harness key was given, and whose file `file` is; absent for a source's credential and for a user's address |
| `as` | string, may be absent | whose own credential was given, and whose file `file` is; absent for a source's credential and for a user's address |
| `next` | string, may be absent | with `user`: the command that comes next, in a line to show as it is: the one that tells the user through the source, or once that is set the one that sends a test message; with `crew`, including a harness key: when the value takes effect, in a line to show as it is; with `as`: the command that tries it as that user, in a line to show as it is; absent for a source's credential |

The file and the store are two writes. When the yard wrote the file and could not record it, the error's message says so with the names and the file, and the ask is open still: give it again. When the call ends without a reply (the server went away), the value may be written: say so, never "sent", and give it again.

### `ApproveAsk`

Approves a run ask (an ask of `Bead` with `run`), and the yard runs the command the ask holds, as the one who approves: `run.command`, whole, and nothing else. It is what `yardr bead answer --approve` does. In one write the yard checks that it would write the same card now, makes the change under the command's own check, and records the answer, which holds `ran`. A refused approval changes nothing, records nothing and leaves the ask open. It is the operator's to approve: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. A command that is a user's own (`subscribe <user> yardr`, `user set <user> tell=<parts>`, `user key <user> <source>`) is approved by the one it was asked of, that user or the operator, on the socket; a device of that user is not served the call. Refused too: an ask that proposes no command; one that was declined, taken back or approved already, since a command runs once for one ask; a `bead@peer`, since a command is approved in the yard that asked and never over a link; and an ask whose card is from before the yard changed, where what the command names was replaced under its name or what it changes is no longer what `run.does` says. That one is declined, and asked again if it is still wanted. The answer is then a `noted` event in the stream, and the change is the command's own event, which carries `approved` with the bead and the ask.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `actor` | string | who approves: the operator's name, or for a command that is a user's own the name of the one it was asked of |

```
POST /call/ApproveAsk
{"bead":"yd-crm","ask":28,"actor":"jo"}

{
  "result": {
    "bead": "yd-crm",
    "ask": 28,
    "asker": "yardmaster",
    "argv": ["peer", "allow", "north", "--mail-limit", "300"],
    "does": ["the link with north (key 6lHks9_z…) takes 300 mails and asks an hour from it. It takes 100 now."],
    "result": "peer north: takes 300 mails and asks an hour, and 19200 files"
  }
}
```

| field of the result | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask that was approved |
| `asker` | string | who proposed the command |
| `argv` | array of string | the command as it ran, in words, without the leading `yardr`: the one the ask proposed |
| `does` | array of string | what the command changed, in the yard's words, as the card had them |
| `result` | string | what the command printed, made by the yard: a line for each thing it changed, with a line break between them |

The change and the answer are one write: the command ran and the ask is approved, or neither. When the call ends without a reply (the server went away), a client cannot know which: say "the yard did not answer; read the card again", never "approved", and read `Bead` again, where `asks[].ran` is there once the command ran.

### `ApproveSetup`

Approves a setup ask (an ask of `Bead` with `setup`), and the yard then applies the pack, takes the account, and grants the preset picked, as the operator: `setup.pack`, the account `setup.account`, and the preset's grants to `setup.to`. It is what `yardr bead answer <n> --secret` (or `--account <source>`) does. In one write the yard checks that it would write the same card now and records the approval, which holds the preset, the names given or the candidate taken, and `ran`; the server carries it out after the call. Typed values live in its memory alone until the account's write; a picked candidate hands none. A refused approval changes nothing, records nothing and leaves the ask open. It is the operator's to approve: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. Picking `you decide` grants the asker's recommended preset, and is refused where none is recommended. Where the card lists candidates, exactly one of the typed values and the picked candidate settles the account, and where it lists none the typed values do. Refused too: an ask that sets up no pack; one that was declined, taken back or approved already, since a setup runs once for one ask; a `bead@peer`, since a setup is approved in the yard that asked and never over a link; values that are not exactly the account asked for, and a candidate the card lists none of; and an ask whose card is from before the yard changed, where the build, the pack's bytes or presets, a source, the account's owed-ness, a candidate or a grantee moved. That one is declined, and asked again if it is still wanted. The answer is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `option` | number | the preset pick, counting `asks[].options` as shown, `you decide` last |
| `values` | object | each name of `setup.account` to the value the person typed; left out where a candidate is taken |
| `account` | string | the candidate taken instead of typing, one the card lists; left out where the values are typed |
| `actor` | string | who approves: the operator's name |

```
POST /call/ApproveSetup
{"bead":"yd-crm","ask":44,"option":1,"values":{"COURIER_ACCOUNT":"what-the-person-typed"},"actor":"jo"}

{
  "result": {
    "bead": "yd-crm",
    "ask": 44,
    "asker": "yardmaster",
    "pack": "courier",
    "preset": "read",
    "given": ["COURIER_ACCOUNT"],
    "result": "approved, to be carried out"
  }
}
```

| field of the result | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask that was approved |
| `asker` | string | who proposed the setup |
| `pack` | string | the pack set up |
| `preset` | string | the preset picked |
| `given` | array of string | the account names that were given |
| `result` | string | `approved, to be carried out`: the server carries the setup out after the call |

The approval and the answer are one write: the ask is approved, or neither. The carrying out follows after the call: while `ran.result` is `approved, to be carried out` the setup reads as being carried out, and as ran or failed once the server finishes it; read `Bead` again for it. When the call ends without a reply (the server went away), a client cannot know whether the approval landed: say "the yard did not answer; read the card again", never "approved", and read `Bead` again. An open ask is approved again; an approved one is left for the server, whose failure names the account asked again.

<!-- /local -->

## What is stable

Stable, for a client to build on:

- the socket's path and the form of a call, a reply, an error and the
  stream, as above, and `GET /blobs/<hash>`;
- the paths under `/user/` on the listener, the header, `/user/me` and its
  three fields, the 401 and the 404, as [Over the
  network](#over-the-network) says them;
- the methods of [The calls](#the-calls) and of [For the operator
  alone](#for-the-operator-alone), the names of their arguments, and every
  field in their tables, with its name and type;
- the events of [The stream](#the-stream) and the fields in their tables.

A later build may add an argument, which a client of today leaves out, and
may add fields and kinds of event. So a client ignores a field or a kind
it does not know.

Not for a client:

- A field that is in a reply and in no table here. The examples leave them
  out; `curl` shows them. They are yardr's own: a crew member's `config`,
  `handle` and `dir`, a peer's `key`, `url` and counts, a user's
  `subscriptions` and `env_file`, an event's `id`, and whatever else is
  there.
- A method that is not listed. The socket serves all of yardr's, and each
  can change or go with any build.
- The array form of the arguments, `GET /wake`, and the words of an error's
  `message`.

A test holds this page to the server: it makes every call above through a
socket with the JSON shown here, checks each field of the tables for its
type, and fails when the examples are not what came back.
