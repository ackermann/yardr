# The socket API: for a program that is not yardr

Every yardr command talks to the yard's server over a unix socket, and so
can a program that is not yardr: a chat window of its own, say. This page
is what such a program may rely on: the calls a chat client needs, their
JSON, and the events it follows. It is what [`yardr chat`](chat.md) itself
reads and writes, and nothing else.

```sh
curl --unix-socket ~/.yardr/yard.sock -X POST http://yard/call/Users -d '{}'
```

Everything else the socket serves is yardr's own. It changes from one build
to the next with no notice, and a client must not call it. See
[What is stable](#what-is-stable).

This is no network API: the socket is a file on the machine the yard runs
on. A Rust or Swift client writes its own few lines of HTTP against this
page; yardr ships no client library.

## The socket

The socket is `$YARDR_HOME/yard.sock` when `YARDR_HOME` is set, and
`~/.yardr/yard.sock` otherwise. It speaks HTTP/1.1; the host of the URL is
ignored (`http://yard/...` here).

Its mode is 0600, in a home yardr makes with mode 0700, so the user who
runs the yard opens it and nobody else. That is all the authentication there is: a
process that can open the socket is trusted as every party of the yard,
and a call that takes an `actor` takes it at its word. A client sends the
name of the user it runs for and no other.

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

- `{"result": ...}` when the call answered something.
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

{"error":{"code":"error","message":"argument \"body\": Mail has none of that name (it takes to, text, actor)"}}
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
a user, a crew member, `yard`), `at`, and `data`, an object whose fields
depend on the kind and which is absent when there are none. The events a
chat client acts on are these; it passes over every other kind.

<!-- events: made by the test of internal/rpc from the stream of a yard, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### Mail to a user or a crew member: `mail`

Mail was sent within the yard. It is for your user when `data.to` is the user's name and `data.to_user` is true; `actor` is then whom the conversation is with. It is your user's own when `actor` is the user's name, whichever client sent it.

```json
{
  "seq": 12,
  "kind": "mail",
  "actor": "yardmaster",
  "data": {
    "text": "The gate is painted. Shall I land it?",
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

### Mail to a linked yard: `mail` with `to_peer`

Mail was queued for one of a linked yard: the conversation with `<data.to>@<data.to_peer>`, of the user `actor`. It is `queued` until a `peer_message_sent` or `peer_message_failed` event with that `data.peer` says otherwise; read the conversation again then.

```json
{
  "seq": 14,
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
  "seq": 15,
  "kind": "mail_read",
  "data": {
    "reader": "jo",
    "through": 12
  }
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `mail_read` |
| `data.reader` | string | who read |
| `data.through` | number | through which seq |

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
  "seq": 16,
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

<!-- /events -->

## Who you are

`yardr chat` shows the yard to the user whose name is in `$YARDR_ACTOR`,
and when that is not set, to the one named as the OS user is. A client
takes its name the same way, and asks `GetUser` for it: the answer
`not-found` says that the name is no user of the yard, and then there is
nothing to show. The operator adds one with `yardr user add <name>`.

That name is the `name` of the inbox calls, one side of `Conversation`,
and the `actor` of `Mail` and `MailPeer`.

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

## The calls

<!-- calls: made by the test of internal/rpc from what a yard answers, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### `Head`

The seq of the newest event, `0` in a yard that has none. Read it before the rest, then follow the stream from after it: what happens while you read comes again from the stream.

```
POST /call/Head
{}

{
  "result": 12
}
```

| field of the result | type | |
|---|---|---|
| `(the result)` | number | the seq |

### `Settings`

The yard's operator, when set. `yardr chat` reads it only to name the operator when the current name is not a user of the yard; a client can omit this call when it already knows who it is for.

```
POST /call/Settings
{}

{
  "result": {
    "operator": "jo"
  }
}
```

| field of the result | type | |
|---|---|---|
| `operator` | string, may be absent | the operator's user name; absent when the yard has not set one |

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
      "receive": ["mail"],
      "said": {
        "roster": [
          {
            "name": "clerk",
            "kind": "crew",
            "role": "Keeps the books",
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

A roster changes with no event of its own: read `Peers` again on any event whose kind begins `peer_`.

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
        "seq": 12,
        "at": "2026-10-08T09:30:00Z",
        "from": "yardmaster",
        "to": "jo",
        "text": "The gate is painted. Shall I land it?",
        "read": false
      }
    ],
    "unread": 1,
    "last": 12,
    "from": ["yardmaster"]
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
| `unread` | number | all the unread, shown or not |
| `last` | number | the seq of the newest unread, `0` when none is |
| `from` | array of string | who the unread are from, in the order they first wrote |

### `Mail`

Sends mail to a crew member or a user of this yard. It answers nothing; the mail is then a `mail` event in the stream.

| argument | type | |
|---|---|---|
| `to` | string | the receiver's name |
| `text` | string | the mail, 1 to 4096 bytes |
| `actor` | string | who sends it: the user's name |

```
POST /call/Mail
{"to":"yardmaster","text":"Thanks. Land it.","actor":"jo"}

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

### `Conversation`

The mail between two parties, both directions, oldest first: what `yardr mail --with <name>` prints. Asking again with the last seq seen costs what is new.

| argument | type | |
|---|---|---|
| `a` | string | one party: the user |
| `b` | string | the other: a crew member, a user, or `<name>@<peer>` for one of a linked yard |
| `after` | number | leave out what is not newer than this seq; `0` for all |

```
POST /call/Conversation
{"a":"jo","b":"yardmaster","after":0}

{
  "result": [
    {
      "seq": 12,
      "at": "2026-10-08T09:30:00Z",
      "from": "yardmaster",
      "to": "jo",
      "text": "The gate is painted. Shall I land it?",
      "read": false
    },
    {
      "seq": 13,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Thanks. Land it.",
      "read": false
    }
  ]
}
```

```
POST /call/Conversation
{"a":"jo","b":"clerk@north","after":0}

{
  "result": [
    {
      "seq": 14,
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

| field of the result | type | |
|---|---|---|
| `[].seq` | number | the seq of the mail's event: what an acknowledgement goes through, and what `after` is compared with |
| `[].at` | time | when it was sent |
| `[].from` | string | the sender: a crew member, a user, or `<name>@<peer>` |
| `[].to` | string | the receiver, as `from` |
| `[].text` | string | the mail |
| `[].read` | bool | whether its receiver acknowledged it |
| `[].delivery` | string, may be absent | on mail to a linked yard only, whose reading of it this yard never learns of: `queued` while it waits here for the peer, `sent` once the peer has it, `failed` when it was given up |

### `AckUserInbox`

Marks the user's mail read through a seq. It answers nothing; when mail was in between it is a `mail_read` event in the stream.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `through` | number | the seq of the newest letter shown |

```
POST /call/AckUserInbox
{"name":"jo","through":12}

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
        "bead": "yd-gate",
        "text": "yd-gate created (all) by yardmaster stage=new title=Paint the gate type=task",
        "read": false
      },
      {
        "seq": 11,
        "at": "2026-10-08T09:30:00Z",
        "kind": "noted",
        "bead": "yd-gate",
        "text": "yd-gate noted (all) by yardmaster note=1 on \"Paint the gate\"",
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

<!-- /calls -->

## What is stable

Stable, for a client to build on:

- the socket's path and the form of a call, a reply, an error and the
  stream, as above;
- the methods of [The calls](#the-calls), the names of their arguments,
  and every field in their tables, with its name and type;
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
