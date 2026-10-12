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

Mail was sent within the yard. It is for your user when `data.to` is the user's name and `data.to_user` is true; `actor` is then whom the conversation is with. It is your user's own when `actor` is the user's name, whichever client sent it.

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
| `data.files` | array, may be absent | the images the mail carries, as a letter's `files`; absent on mail without any |
| `data.files[].hash` | string | the image's name: `GET /blobs/<hash>` answers the bytes |
| `data.files[].type` | string | `image/png` or `image/jpeg` |
| `data.files[].bytes` | number | how many bytes it is |

### Mail to a linked yard: `mail` with `to_peer`

Mail was queued for one of a linked yard: the conversation with `<data.to>@<data.to_peer>`, of the user `actor`. It is `queued` until a `peer_message_sent` or `peer_message_failed` event with that `data.peer` says otherwise; read the conversation again then.

```json
{
  "seq": 34,
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
  "seq": 35,
  "kind": "mail_read",
  "data": {
    "reader": "jo",
    "through": 13
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
  "seq": 36,
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

A note was added to the bead: a plain note, a question (an ask), or an answer (a pick, words, put aside, declined). It says no more than that the bead has a new note: read `UserAsks` again on it, and `Bead` again when `bead` is one shown.

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
| `bead` | string | the bead the note is on |
| `actor` | string | who wrote it |
| `at` | time | when |
| `data.note` | number | the note's seq |

<!-- /events -->

## An image: `GET /blobs/<hash>`

Mail can carry images. A letter names each in `files` (the `mail` event in
`data.files`) by its hash, the SHA-256 of its bytes in hex, with its type
and size, and this reads one:

```
GET /blobs/<hash>
```

The reply has status 200, the `Content-Type` the letter said (`image/png`
or `image/jpeg`) and the image's bytes as its body. A hash the yard does
not hold is status 404, with a line of text that says so. That is the one
answer here that is no JSON.

An image never changes under its hash, so a client that has one need not
ask again. The yard removes an image once no mail it still has names it
(`yardr store prune`), which is how a hash from a letter can come to be 404.

Sending one is two calls: [`PutBlob`](#putblob) with the bytes, then
[`Mail`](#mail) with the hash it answered among `files`. The cap on one
image is `blob_max_bytes` of [`Settings`](#settings). To a linked yard it is
the same with [`MailPeer`](#mailpeer), when the link carries `file` out and
the image is within `peer_file_max_bytes`
([peers.md](peers.md#an-image-with-the-mail)). A file of mail from a linked
yard that this yard did not take has `not_taken`, the reason, beside the
sender's `hash`, `type` and `bytes`: there is no image to ask for then.
In a [`Conversation`](#conversation) with one of a linked yard, a file of
this yard's own mail that the other yard did not take has `not_taken` too,
with that yard's reason.

The same bytes are the file `<hash>` in `$YARDR_HOME/blobs`, which is how
yardr's own views name an image (`1 image: <path>`). The directory is
yardr's own: a client reads an image here, and never writes there.

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

## The calls

<!-- calls: made by the test of internal/rpc from what a yard answers, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### `Head`

The seq of the newest event, `0` in a yard that has none. Read it before the rest, then follow the stream from after it: what happens while you read comes again from the stream.

```
POST /call/Head
{}

{
  "result": 31
}
```

| field of the result | type | |
|---|---|---|
| `(the result)` | number | the seq |

### `Settings`

The yard's operator, when set, and its cap on an image. `yardr chat` reads the operator only to name them when the current name is not a user of the yard; a client can omit this call when it already knows who it is for and sends no image.

```
POST /call/Settings
{}

{
  "result": {
    "blob_max_bytes": "8388608",
    "operator": "jo"
  }
}
```

| field of the result | type | |
|---|---|---|
| `operator` | string, may be absent | the operator's user name; absent when the yard has not set one |
| `blob_max_bytes` | string, may be absent | the most bytes `PutBlob` takes, a whole number in a string; absent when the yard has not set one, and the cap is then 8388608 (8 MiB) |

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
        "seq": 13,
        "at": "2026-10-08T09:30:00Z",
        "from": "yardmaster",
        "to": "jo",
        "text": "The shed is painted. Shall I land it?",
        "read": false,
        "files": [
          {
            "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
            "type": "image/png",
            "bytes": 70
          }
        ]
      }
    ],
    "unread": 1,
    "last": 13,
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
| `mail[].files` | array, may be absent | the images the mail carries, in the order it was sent with them; absent on mail without any |
| `mail[].files[].hash` | string | the image's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes |
| `mail[].files[].type` | string | `image/png` or `image/jpeg` |
| `mail[].files[].bytes` | number | how many bytes it is |
| `unread` | number | all the unread, shown or not |
| `last` | number | the seq of the newest unread, `0` when none is |
| `from` | array of string | who the unread are from, in the order they first wrote |

### `PutBlob`

Gives the yard an image, so that mail can carry it: `Mail` then names it by the hash this answers. The bytes go in the JSON of the call, in base64. Only a PNG or a JPEG is taken, and which it is is read from the bytes themselves; anything else is refused, and so is one over the yard's cap (`blob_max_bytes` of `Settings`). The yard keeps an image once, under the SHA-256 of its bytes: putting the same bytes again answers the same and stores nothing new. An image no mail names is removed by `yardr store prune`, though not within an hour of being put, so put it when the mail is about to be sent, and put it again when that was longer ago.

| argument | type | |
|---|---|---|
| `data` | string | the image's bytes, in base64 (the standard alphabet, padded) |

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
| `hash` | string | the image's name: the SHA-256 of its bytes, in hex |
| `type` | string | `image/png` or `image/jpeg`, as the bytes say |
| `bytes` | number | how many bytes it is |

### `Mail`

Sends mail to a crew member or a user of this yard. It answers nothing; the mail is then a `mail` event in the stream.

| argument | type | |
|---|---|---|
| `to` | string | the receiver's name |
| `text` | string | the mail, 1 to 4096 bytes; it may be empty when the mail carries an image |
| `actor` | string | who sends it: the user's name |
| `files` | array of string | the images the mail carries, at most 8: the hash of each, as `PutBlob` answered it. Left out, the mail carries none. One the yard does not hold refuses the mail |

```
POST /call/Mail
{"to":"yardmaster","text":"Thanks. Land it.","actor":"jo"}

{}
```

```
POST /call/Mail
{"to":"yardmaster","text":"Paint the fence like this.","actor":"jo","files":["497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581"]}

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
| `files` | array of string | the hashes of the images the mail carries, as for `Mail`; `text` may then be empty. Each crosses to the peer as a message of kind `file` before the mail. Refused, with nothing queued, when the link does not carry `file` out, when the peer's yardr has not said it understands `file`, and when an image is over `peer_file_max_bytes` |

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
      "seq": 13,
      "at": "2026-10-08T09:30:00Z",
      "from": "yardmaster",
      "to": "jo",
      "text": "The shed is painted. Shall I land it?",
      "read": false,
      "files": [
        {
          "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
          "type": "image/png",
          "bytes": 70
        }
      ]
    },
    {
      "seq": 32,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Thanks. Land it.",
      "read": false
    },
    {
      "seq": 33,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Paint the fence like this.",
      "read": false,
      "files": [
        {
          "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
          "type": "image/png",
          "bytes": 70
        }
      ]
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
      "seq": 34,
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
| `[].files` | array, may be absent | the images the mail carries, in the order it was sent with them; absent on mail without any |
| `[].files[].hash` | string | the image's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes |
| `[].files[].type` | string | `image/png` or `image/jpeg` |
| `[].files[].bytes` | number | how many bytes it is |
| `[].delivery` | string, may be absent | on mail to a linked yard only, whose reading of it this yard never learns of: `queued` while it waits here for the peer, `sent` once the peer has it, `failed` when it was given up |

### `AckUserInbox`

Marks the user's mail read through a seq. It answers nothing; when mail was in between it is a `mail_read` event in the stream.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `through` | number | the seq of the newest letter shown |

```
POST /call/AckUserInbox
{"name":"jo","through":13}

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
        "text": "yd-shed created (all) by yardmaster stage=new title=Paint the shed type=task",
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

The person's decisions: every open ask addressed to them, on any bead in any stage, oldest ask first, with what they have dealt with beside it. It is not `yardr review`'s queue, which walks beads in manual stages by bead age.

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
      }
    ],
    "decided": [
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
| `open[].bead` | string | the bead the question stands on |
| `open[].bead_title` | string | the bead's title |
| `open[].topic` | string | the bead's topic: its first `topic:` label, `""` when it has none |
| `open[].ask` | number | the ask note's seq: what an answer names |
| `open[].question` | string | the question in one line, as a person would ask it |
| `open[].asker` | string | who asked |
| `open[].since` | time | when the ask was written: how long it waits |
| `decided[].bead` | string | the bead the question stands on |
| `decided[].bead_title` | string | the bead's title |
| `decided[].topic` | string | the bead's topic: its first `topic:` label, `""` when it has none |
| `decided[].ask` | number | the ask note's seq: what an answer names |
| `decided[].question` | string | the question in one line, as a person would ask it |
| `decided[].asker` | string | who asked |
| `decided[].since` | time | when the ask was written: how long it waits |
| `decided[].state` | string | `answered`, `put aside` or `declined` |
| `decided[].option` | number, may be absent | the picked option's number; absent on a words answer, an aside ask and a declined one |
| `decided[].option_label` | string, may be absent | the picked option's label; absent with `option` |
| `decided[].words` | string, may be absent | what went with the pick, the words answer, or why the question is declined; absent when there are none |
| `decided[].return_at` | time, may be absent | when a put-aside ask comes back; absent otherwise |

Read it again on every `noted` event: a question asked, answered, put aside or declined, and the asker's next note, all arrive as `noted`. A put-aside ask also comes back on its own after 24 hours, with no event: its decided entry carries `return_at`, so read again then.

### `Bead`

One bead whole: body, stage, labels, notes, its asks with their options, recommendation and state, and for each about-bead its id, title and stage.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |

```
POST /call/Bead
{"bead":"yd-links"}

{
  "result": {
    "id": "yd-links",
    "title": "Move the links",
    "body": "The links file declares the new source.",
    "stage": "new",
    "labels": ["topic:linear-out"],
    "notes": [
      {
        "seq": 2,
        "kind": "note",
        "author": "yardmaster",
        "text": "the test passed on a copy",
        "at": "2026-10-08T09:30:00Z"
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

| field of the result | type | |
|---|---|---|
| `id` | string | the bead's handle |
| `title` | string | the bead's title |
| `body` | string | the bead's body |
| `stage` | string | the bead's stage |
| `labels` | array of string | the bead's labels |
| `notes[].seq` | number | the note's seq |
| `notes[].kind` | string | the note's kind: `note`, `journal` or `handoff` |
| `notes[].author` | string | who wrote it |
| `notes[].text` | string | the note |
| `notes[].at` | time | when it was written |
| `asks[].ask` | number | the ask note's seq: what an answer names |
| `asks[].to` | string | who must answer |
| `asks[].asker` | string | who asked |
| `asks[].since` | time | when the ask was written |
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
| `asks[].state` | string | `open`, `answered`, `put aside` or `declined` |
| `asks[].option` | number, may be absent | the picked option's number; absent until one is picked |
| `asks[].words` | string, may be absent | what went with the pick, the words answer, or why the question is declined; absent when there are none |
| `asks[].return_at` | time, may be absent | when a put-aside ask comes back; absent otherwise |
| `asks[].picks_before` | number, may be absent | how many picks there were before the current one, so a client can say "changed"; absent when there were none |

Read it again on every event whose `bead` is the bead: `noted` and the bead's other events, whose kinds are yardr's own.

### `AnswerAsk`

Answers an ask: picks an option with words alongside, or answers an ask with no options with words. It is what `yardr bead answer` does with an option or `--words`: words alone on an ask with options are refused, and go as a plain note instead. It answers nothing; the answer is then a `noted` event in the stream.

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

Puts the ask aside: it leaves the person's queue for 24 hours or until the asker's next note on the bead, whichever is first. It is what `yardr bead answer` does with `--later`. It answers nothing; putting it aside is then a `noted` event in the stream.

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

Ends the ask with no option picked: the person will not decide it. It is what `yardr bead answer` does with `--decline`, and reads wherever the ask is shown as "`<user>` declined to decide this: `<words>`". It answers nothing; declining is then a `noted` event in the stream.

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

### `AddNote`

Puts a plain note on the bead, as `yardr bead note` does. Words about a question with no option chosen go here, and leave the ask open. It answers nothing; the note is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `text` | string | the note |
| `actor` | string | who writes it: the user's name |

```
POST /call/AddNote
{"bead":"yd-greet","text":"the newcomer arrives Monday","actor":"jo"}

{}
```

<!-- /calls -->

## What is stable

Stable, for a client to build on:

- the socket's path and the form of a call, a reply, an error and the
  stream, as above, and `GET /blobs/<hash>`;
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
