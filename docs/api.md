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
| `data.via` | string, may be absent | the [device](#over-the-network) the sender wrote through; absent on mail written on the yard's own machine. Every event a device's call writes as its user carries it, a `noted` and a `mail_read` too |

### Mail to a linked yard: `mail` with `to_peer`

Mail was queued for one of a linked yard: the conversation with `<data.to>@<data.to_peer>`, of the user `actor`. It is `queued` until a `peer_message_sent` or `peer_message_failed` event with that `data.peer` says otherwise; read the conversation again then.

```json
{
  "seq": 47,
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
  "seq": 48,
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
  "seq": 49,
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

A note was added to the bead: a plain note, a question (an ask), or an answer (a pick, words, a secret given, put aside, declined). A question another yard asked arrives as `noted` too, on `bead@peer`. It says no more than that the bead has a new note: read `UserAsks` again on it, and `Bead` again when `bead` is one shown.

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

A call, its reply, its errors, the stream and an image are as this page
says they are on the socket, so a client changes how it connects and
nothing else. What is not the same:

- **Only the calls of this page.** Any other method is answered as one the
  yard does not have (`no such method`), whoever calls: nothing that adds a
  user or a device, moves a bead or changes a setting is on the listener.
  `/ping`, `/wake` and every other path are not there. One call more is
  there for a device of the yard's operator and for nobody else:
  [`GiveSecret`](#for-the-operator-alone).
- **Arguments by name only.** The array yardr's own builds send is refused.
- **Every call is made as the device's user.** The `name` of `GetUser`, the
  inbox calls and `UserAsks`, and the `actor` of `Mail`, `MailPeer`,
  `AnswerAsk`, `DeferAsk`, `DeclineAsk`, `AddNote` and `GiveSecret`, may be
  left out: the yard fills in the user. One that names anyone else is refused with the
  code `error` (`this device calls as jo: "actor" is jo or left out, not
  yardmaster`). One side of `Conversation` is the user: `a` left out is the
  user, and a conversation between two others is refused. So `GetUser`
  answers the device's user and no other; `Users` lists them all.
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
  yard's cap on an image besides. One over it is answered 413.
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

### TLS is yours, and the client's rule

yardr does not do TLS: the listener speaks plain HTTP, and a reverse proxy
or a tunnel of yours stands in front of it, as for linked yards
([peers.md](peers.md)). The yard cannot tell from the inside whether one
does, and a token sent in the clear is a token given away. So the rule is
the client's, which knows the address it was given: **a client refuses an
address that is `http://` unless its host is loopback** (`127.0.0.1`, `::1`,
`localhost`), and says why. It keeps the token in a file only its user
reads (mode 0600) or in the system's key store, never in a settings file.

A secret is the one thing the yard holds to this itself, as far as it can
see. [`GiveSecret`](#for-the-operator-alone) is taken only from a request
that reached the listener from the yard's own machine, where the proxy or
tunnel that ends TLS stands (or a client on that machine), and never from
one whose proxy says it came in plain (`X-Forwarded-Proto: http`; the
header is read for a no and never for a yes). Any other is refused with
the code `error`, in words that name the reason and say that what was sent
may have crossed a network in the clear. A proxy on another machine that
passes requests on to the yard in plain HTTP is refused so: from there to
the yard the value would be readable. Every other call is served wherever
it comes from, as before.

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
  "result": 44
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
      "receive": ["ask", "mail"],
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
      "seq": 35,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "On the train. Land the shed when it is green.",
      "read": false,
      "via": "phone"
    },
    {
      "seq": 45,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Thanks. Land it.",
      "read": false
    },
    {
      "seq": 46,
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
      "seq": 47,
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
| `[].via` | string, may be absent | the [device](#over-the-network) the mail was written through, to be shown behind the sender, `jo (via phone)`; absent on mail written on the yard's own machine |
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
      }
    ],
    "decided": [
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
| `open[].secret.source` | string | the declared source the secret is the credential of |
| `open[].secret.env` | array of string | the names to ask the person a value for |
| `open[].secret.line` | string | what the card says is asked for, in the yard's words |
| `decided[].bead` | string | the bead the question stands on: a handle, or `bead@peer` for a question another yard asked |
| `decided[].bead_title` | string | the bead's title |
| `decided[].topic` | string | the bead's topic: its first `topic:` label, `""` when it has none |
| `decided[].ask` | number | the ask's seq on its bead: what an answer names |
| `decided[].question` | string | the question in one line, as a person would ask it |
| `decided[].asker` | string | who asked: a name, or `name@peer` for a question another yard asked |
| `decided[].since` | time | when the ask was written: how long it waits (as sent, for a question another yard asked) |
| `decided[].secret` | object, may be absent | what a secret ask asks for, as `Bead` says it; absent on any other ask |
| `decided[].secret.source` | string | the declared source the secret is the credential of |
| `decided[].secret.env` | array of string | the names to ask the person a value for |
| `decided[].secret.line` | string | what the card says is asked for, in the yard's words |
| `decided[].given` | array of string, may be absent | the names a secret ask was given values for, which reads "gave `<names>`"; absent on any other ask |
| `decided[].state` | string | `answered`, `put aside` or `declined` |
| `decided[].option` | number, may be absent | the picked option's number; absent on a words answer, an aside ask and a declined one |
| `decided[].option_label` | string, may be absent | the picked option's label; absent with `option` |
| `decided[].words` | string, may be absent | what went with the pick, the words answer, or why the question is declined; for a question another yard asked that ended there, how it ended; absent when there are none |
| `decided[].return_at` | time, may be absent | when a put-aside ask comes back; absent otherwise |

Read it again on every `noted` event: a question asked, answered, put aside or declined, and the asker's next note, all arrive as `noted`. A put-aside ask also comes back on its own after 24 hours, with no event: its decided entry carries `return_at`, so read again then. An answer to a question another yard asked that did not land arrives as `noted` by `yard`: the question is open again, or decided with how it ended there, and the person has mail from `yard` that says which.

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
        "at": "2026-10-08T09:30:00Z"
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
| `asks[].state` | string | `open`, `answered`, `put aside` or `declined` |
| `asks[].option` | number, may be absent | the picked option's number; absent until one is picked |
| `asks[].words` | string, may be absent | what went with the pick, the words answer, or why the question is declined; absent when there are none |
| `asks[].return_at` | time, may be absent | when a put-aside ask comes back; absent otherwise |
| `asks[].picks_before` | number, may be absent | how many picks there were before the current one, so a client can say "changed"; absent when there were none |
| `asks[].secret` | object, may be absent | what a secret ask asks for; absent on any other ask. Such an ask has no options and takes no words: show it with a field that does not echo, and answer it with [`GiveSecret`](#for-the-operator-alone). `AnswerAsk` refuses a pick and words on it, and `DeclineAsk` words |
| `asks[].secret.source` | string | the declared source the secret is the credential of |
| `asks[].secret.env` | array of string | the names to ask the person a value for, each a variable of the source's credential |
| `asks[].secret.line` | string | what the card says is asked for, in the yard's words and never the asker's: the names, the source, and where the source's calls go. Show it as it is |
| `asks[].given` | array of string, may be absent | the names a secret ask was given values for, which reads "gave `<names>`"; absent on any other ask and until it is given |

Read it again on every event whose `bead` is the bead: `noted` and the bead's other events, whose kinds are yardr's own.

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

### `AddNote`

Puts a plain note on the bead, as `yardr bead note` does. Words about a question with no option chosen go here, and leave the ask open. On a `bead@peer`, a question another yard asked, it is refused: notes stay local, and words for the asker go as mail to `asker@peer`. It answers nothing; the note is then a `noted` event in the stream.

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

## For the operator alone

The calls of this list are as stable as the calls above, and are the
operator's alone to make. In yardr's code the list is `OperatorCalls`, and
it has one call, `GiveSecret`. Where it is served, and to whom:

- **On the socket**, to the operator: `actor` is the operator's name, and
  the call does not come from the pane of an agent.
- **On the [listener](#over-the-network)**, to a device of the operator:
  a request whose token is one the yard gave a device that the operator
  added for themselves (`yardr user device add <operator> <device>`, or a
  pairing). `actor` is the operator or left out. The yard knows there who
  calls by the token and not by a word of the caller's; it cannot know
  whether an agent on some machine sent the request, so whoever holds
  that token gives as the operator.
- **To nobody else.** A device of any other user, and a caller the yard
  knows by anything but a device token of its own, is answered as for a
  method the yard does not have (`no such method GiveSecret`), before the
  request's body is read. So is every device in a yard with no operator.
  Against such an answer a client says "answer it at the yard" and sends
  its user to a terminal there (`ssh -t <host> yardr bead answer <bead>
  <ask> --secret`).

Over the listener the value travels under the TLS in front of it and under
no other protection, and the yard takes it only where it can see that the
request did not come in plain ([TLS is yours](#tls-is-yours-and-the-clients-rule)).
It never crosses a link to another yard: a secret ask is not sent to a
peer, and is answered at the yard that asked.

What a give through a device writes says how it came, as its other writes
do: the `noted` event of the answer, and `source_set` where the file is
new, carry `data.via`.

A value given with `GiveSecret` is the person's secret. Read it into a
field that does not echo, send it in this call and in nothing else, and
keep it nowhere: not in a draft, a notice, a log or an error. Empty the
field when the call returns, whatever it answered.

<!-- local: made by the test of internal/rpc from what a yard answers, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### `GiveSecret`

Answers a secret ask (an ask of `Bead` with `secret`): the values go into the file the source's credential is read from, and the answer on the bead holds the names and never a value. A value is kept in that file and nowhere else: not in the store, an event, a note or the log, and no call reads one back. It is what `yardr bead answer --secret` does. It is the operator's to give: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. Refused too: an ask that is declined, names that are not exactly `asks[].secret.env`, an empty value, and one with a line break or white space at either end; a refusal never repeats a value. Giving again replaces what was given. The answer is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `values` | object | each name of `asks[].secret.env` to the value the person typed |
| `actor` | string | who gives: the operator's name |

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

| field of the result | type | |
|---|---|---|
| `given` | array of string | the names that were written |
| `file` | string | the file they are in, on the yard's machine |
| `credential` | string, may be absent | where every variable of the source's credential is set now, in words; absent while one is still missing |
| `missing` | string, may be absent | what the credential still wants, in words; absent when it is whole |

The file and the store are two writes. When the yard wrote the file and could not record it, the error's message says so with the names and the file, and the ask is open still: give it again. When the call ends without a reply (the server went away), the value may be written: say so, never "sent", and give it again.

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
