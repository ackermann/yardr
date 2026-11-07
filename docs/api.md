# The socket API: for a program that is not yardr

Every yardr command talks to the yard's server over a unix socket, and so
can a program that is not yardr: a chat window of its own, say, or a window
that shows a person's decisions. This page is what such a program may rely
on: the calls a chat client needs, the calls a decisions client needs, the
files of a kept hook delivery, their JSON, and the events they follow. It
is what [`yardr chat`](chat.md) itself reads and writes, the queue, the
bead and the writes a decisions client shows, and the files a hook
delivery carries, and nothing else.

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
| operator | an operator alone (a user whose role is `operator`, any of them), from no agent's pane, a crew member's included |
| daemon | the daemon alone, as the yard checks it by the daemon's lock: the session records the pane checks read |
| daemon or operator | the daemon, or an operator from no agent's pane: a copy of the store, a prune |

A method a bead's agent has no class for is refused to it: what is not
marked read or agent is not an agent's. The class is a first line, and the
yard's own checks of a call come after it: a call it lets through may still
be refused there, for its `actor` or for what it asks. An agent may update
its bead but not pin the flow the bead goes by, nor change a bead's type or
depot, which choose it. It may create a bead, but not pin its flow or put it
in a stage past its flow's first that no person looks at first. It may close
its own bead and no other, and a move into a terminal stage closes. It may
hold a bead and add an edge but not lift a hold or remove a `blocks` edge,
which only a person or a crew member does. An advance along the edge
`done-safe` takes is held to the depot's security paths as the outcome is. Every call this page names is read or agent but those of
[`OperatorCalls`](#over-the-network), which are admin, all but
`SourceGrants`, which is read on the socket and the operator's on the
listener, `SessionScreen`, `Teammates`, `TelemetryShow`, `TelemetrySet`,
`DevicePairState` and `DevicePairClose`, whose socket class is operator,
and `UninviteTeammate`, whose socket class
is agent.

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

A field of a reply is named by its path: `mail[].seq` is the `seq` of
each element of the array `mail`, and `given{}.at` the `at` of each value
of the object `given`, whose keys are data rather than names the doc
promises.

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
whose `bead` is that bead; it can say why a bead came to a person from
`rounds_reached` and the `advanced` event with `instead_of` before it.

<!-- events: made by the test of internal/rpc from the stream of a yard, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### Mail to a user or a crew member: `mail`

Mail was sent within the yard, or came from a linked yard for one of this yard. It is for your user when `data.to` is the user's name and `data.to_user` is true; `actor` is then whom the conversation is with. It is your user's own when `actor` is the user's name, whichever client sent it.

```json
{
  "seq": 14,
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
  "seq": 185,
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
  "seq": 187,
  "kind": "mail_read",
  "data": {
    "reader": "jo",
    "through": 52
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
  "seq": 15,
  "kind": "reaction",
  "actor": "jo",
  "data": {
    "emoji": "👍",
    "preview": "The shed is painted. Shall I land it?",
    "seq": 14,
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

The daemon saw a crew member's session do something else than before. Any other event whose kind begins `crew_` says that a session started, stopped or failed, that its login changed (`crew_login`, below), or that the list changed, and carries less than what changed: read `Crew` again on it.

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

### A crew member's login: `crew_login`

A crew member's session sat down at its harness's login screen or left it, or the yard began or stopped leading that login: `data.login_screen` and `data.login_led` are the two facts as they stand now, as `Crew` says them. It is raised once for the change, not for every write the yard makes of the session's record. A session that starts or stops has the event of that, `crew_started` or `crew_stopped`, and this one is not raised besides. Like the others whose kind begins `crew_` it carries less than what changed: read `Crew` again on it. An event with `data.user` is of that user's conversation session, which `Crew` does not list: a client that shows the crew member's own session passes it over.

```json
{
  "seq": 141,
  "kind": "crew_login",
  "data": {
    "crew": "signalman",
    "login_led": false,
    "login_screen": true
  }
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `crew_login` |
| `data.crew` | string | the crew member |
| `data.login_screen` | bool | whether the session now sits at its harness's login screen, as `login_screen` of `Crew` |
| `data.login_led` | bool | whether the yard now leads a login for it, as `login_led` of `Crew` |

### A notification: any event with `notified`

An event became a notification for the users `data.notified` names, each with the reason. The event is not the notification's text: read `UserNotifications` again when your user is among them. Nothing else of such an event is for a chat client.

```json
{
  "seq": 11,
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

### A hook delivery's files arrived: `hook_files`

The yard fetched the files of a kept hook delivery and recorded them: [`DeliveryFiles`](#deliveryfiles) of `data.seq` answers them now, each as the yard holds it or with the reason it did not. The delivery's own event, and the notification of it, come before its files are fetched, so a client that read none then reads again on this, once for the delivery. A delivery whose files are not fetched (none carried, a source with no credential set, a fetch the server's restart cut short) has no such event, and `DeliveryFiles` answers `null` for it. Every device is sent it, as it is sent the delivery. It is no notification: `UserNotifications` has no row for it.

```json
{
  "seq": 149,
  "kind": "hook_files",
  "actor": "hook:telegram",
  "data": {
    "seq": 148
  },
  "at": "2026-10-08T09:30:00Z"
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `hook_files` |
| `actor` | string | the delivery's: `hook:<name>`, the hook that received it |
| `at` | time | when the files were recorded |
| `data.seq` | number | the delivery's seq: what `DeliveryFiles` takes, and the `seq` of its notification |

### Notifications were read: `notifications_read`

A user acknowledged their notifications, in this client or another.

```json
{
  "seq": 188,
  "kind": "notifications_read",
  "data": {
    "reader": "jo",
    "through": 12
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

A note was added to the bead: a plain note, a question (an ask), or an answer (a pick, words, a secret given, a command approved, a depot-push card carried, put aside, declined), or its asker taking a question back. A question another yard asked arrives as `noted` too, on `bead@peer`. A depot-push approval sentence is held from nonoperator devices. The event says no more than that the bead has a new note: read `UserAsks` again on it, and `Bead` again when `bead` is one shown.

```json
{
  "seq": 12,
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
| `data.witness` | string, may be absent | how a strong approval was witnessed: `fresh sign-in` in the hosted web, `terminal` on the socket; absent otherwise, and on an approval through a device, which names the device in `data.via` instead |

### A bead was closed: `closed`

A bead closed, by an explicit close or by entering a terminal stage of its flow. It says no more than that the bead is done: read `Bead` again when `bead` is one shown.

```json
{
  "seq": 97,
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

### A bead was sent on by a round limit: `advanced` with `instead_of`

An outcome of the bead's flow that limits how often it sends the bead back (`max n then <stage>`) was reported with its rounds used, and the bead went to the stage `then` names instead of the one the outcome leads to. It is the bead's `advanced` event, with `data.rounds`, `data.max` and `data.instead_of` beside the `data.from`, `data.to` and `data.outcome` of a move by an outcome; a `rounds_reached` event follows it. Read `Bead` again when `bead` is one shown: its `rounds` for `data.instead_of` is zero again.

```json
{
  "seq": 103,
  "bead": "yd-hedge",
  "kind": "advanced",
  "actor": "yardmaster",
  "data": {
    "from": "critique",
    "instead_of": "draft",
    "max": 1,
    "outcome": "changes",
    "rounds": 1,
    "to": "abandoned"
  },
  "at": "2026-10-08T09:30:00Z"
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `advanced` |
| `bead` | string | the bead that moved: a handle |
| `actor` | string | who reported the outcome |
| `at` | time | when |
| `data.from` | string | the stage the outcome was reported from |
| `data.to` | string | the stage the bead went to: the outcome's `then` |
| `data.outcome` | string | the outcome reported |
| `data.rounds` | number | the rounds counted for `data.instead_of`: the outcome's `data.max` or more, which is more when another outcome into that stage, with a higher limit, raised the count |
| `data.max` | number | the outcome's limit |
| `data.instead_of` | string | the stage the outcome leads to, which the bead did not go back to |

### A bead reached its round limit: `rounds_reached`

The limit of an outcome reported on the bead was used, and the bead went to the stage the limit names instead: the `advanced` event just before it says where. It is of the class `attention`, so a crew member that hears the class, and a subscription to it, has it. A person who sends the bead back to `data.to` has granted it a fresh allowance by doing so.

```json
{
  "seq": 104,
  "bead": "yd-hedge",
  "kind": "rounds_reached",
  "actor": "yard",
  "data": {
    "outcome": "changes",
    "rounds": 1,
    "stage": "critique",
    "then": "abandoned",
    "to": "draft"
  },
  "at": "2026-10-08T09:30:00Z"
}
```

| field | type | |
|---|---|---|
| `seq` | number | the event's seq |
| `kind` | string | `rounds_reached` |
| `bead` | string | the bead: a handle |
| `actor` | string | `yard` |
| `at` | time | when |
| `data.stage` | string | the stage the outcome was reported from |
| `data.outcome` | string | the outcome reported |
| `data.rounds` | number | the rounds counted for `data.to`: the outcome's limit or more |
| `data.to` | string | the stage the outcome leads to, which the bead did not go back to |
| `data.then` | string | the stage the bead went to instead |

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

A kept hook delivery carries files too (a photo sent to a chat service the
yard hears): [`DeliveryFiles`](#deliveryfiles) names them by hash, and
this reads their bytes. They arrive after the delivery does, and the
[`hook_files`](#a-hook-deliverys-files-arrived-hook_files) event says when.
The yard removes them with the delivery, when it leaves the log.

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
  user or a device or moves a bead is on the listener, and of the settings
  only the telemetry switch, which the operator's device reads and changes
  with `TelemetryShow` and `TelemetrySet`.
  `/ping`, `/wake` and every other path are not there. The [calls for the
  operator alone](#for-the-operator-alone) are there besides, for a device
  of an operator of the yard and for nobody else — but for the three that
  open, read and end a pairing (`DevicePairOpen`, `DevicePairState` and
  `DevicePairClose`), which are the socket's alone and are answered as no
  method at all; `GiveSecret` is there too for
  a device of a user, for an ask with `secret.user` or `secret.as` that
  names them: their own address or credential in a source, or with
  `secret.login` the code their own login's sign-in page shows.
  `ApproveAsk` and `ApproveSetup` are the calls on the listener that have
  the operator's commands run: the command of a run ask, which is one of a
  fixed list ([users.md](users.md#devices) has them, row by row), and the
  apply, account and grants of a setup.
- **Arguments by name only.** The array yardr's own builds send is refused.
- **Every call is made as the device's user.** The `name` of `GetUser`, the
  inbox calls and `UserAsks`, and the `actor` of `Mail`, `MailPeer`,
  `AnswerAsk`, `DeferAsk`, `DeclineAsk`, `TakeBackAsk`, `AddNote`,
  `GiveSecret`, `ApproveAsk`, `ApproveSetup` and `TelemetrySet`, may be left out: the yard fills in the user. One that names anyone else is refused with the
  code `error` (`this device calls as jo: "actor" is jo or left out, not
  yardmaster`). One side of `Conversation` is the user: `a` left out is the
  user, and a conversation between two others is refused. So `GetUser`
  answers the device's user and no other; `Users` lists them all.
- **`Settings` answers the promised keys alone**: `operator`,
  `blob_max_bytes` and `files_max`, and the operator's own words to the
  agents, `operator_style` and `operator_note`, and whether the yard is
  hosted, `hosted`, for a device of an operator alone, and for none in a
  yard with no operator. No other setting is answered on the listener. A
  `setting_changed` in the stream follows the same rule: it has its
  `value` only for a key the device is told, and for any other it has the
  `key` and no `value`.
- **`Bead` tells a setup card's account file and candidates, and the file
  an approved setup's account is set in, to a device of an operator
  alone**: the file a typed value goes to (`asks[].setup.account_file`),
  the yard sources whose account the card offers to reuse
  (`asks[].setup.candidates`), each a file of the yard's with the values it
  holds, and the sentence of `asks[].setup.does` that names the file and
  them; and in `asks[].ran.result` of the approved setup the file the
  account is set in, which is the candidate's when the approval took one, in
  its line and in the words of a carrying out that failed. To any other
  device the card is that of a setup with none and naming no file: both
  fields are absent, and the sentence says the account as such a card does;
  the line of the result reads `the account <names> is set`, with no file,
  and a failure reads that it failed and no more. So it is to every device
  of a yard with no operator. The socket tells the whole card and result, as
  it tells the operator's words in `Settings`.
- **The stream tells a device that is no operator's only the data its
  kind lists.** Each kind of event has a row of keys: the names of what the
  event is about, numbers, the words of a fixed vocabulary, and every field
  this page promises of it. The device is sent those keys and no others,
  whether the stream replays the event or sends it as it happens. A kind with
  no row is sent no `data` except `via` and `notified`. A device of an
  operator is sent the whole event, except the `value` of a setting no device
  is told, and the socket sends every event whole. A yard with no operator
  sends the whole event to no device. The user's own device is held the same
  as any other.
- **What the stream holds back is an operator's files and commands, and the
  words of errors that name them.** A `source_set` has `data.env_file`, the
  file a source was pointed at, and `data.mint_command`, the command that
  mints its token. A `user_set` has `data.env_file`, the file a user's
  address is read from. A `hook_blocked` has `data.path`, the file the
  hook's secret is read from, and `data.reason`, the error of reading it,
  which names the file too. A `crew_started` and a `started` have
  `data.handle`, which names the directory the session started in and the
  yardr the agent was told to run. A `push_failed` has `data.error`, which
  names the file of the user's address. A `depot_updated` has `data.path` and
  `data.test`, a `backup_failed` has `data.dir`, a `workspace_kept` and a
  `workspace_removed` have `data.worktree`, and a `move_refused` has
  `data.command` and `data.error`. A `user_removed` has `data.login`, which
  names the groups, the routes and the crew members that went with a removed
  user and the `env_files` of their login that stay on disk; a device that is
  no operator's is sent `groups`, `routes` and `crew` and not `env_files`. A
  session's events have `tail`, `line`, `dialog` and `hint`, which quote its
  pane and name commands to run there.
  A device that is no operator's is sent the `source`, `user`, `hook`,
  `crew`, `depot` or `session` each event is about, and none of those. A
  setup that took a candidate points the source its pack added at the
  candidate's file, and `yardr source set <source> env_file=<path>` points
  any source at one. The event is the same, and so is the hold. A
  `source_set` that sets `data.base` names the selected region and is held
  back for the same reason. What else a `source_set` and a `user_set` carry
  is sent as it is: the `calls_per_hour` and `updates_per_hour` of a source,
  and the `notify`, `tell` and `approved` of a user.
- **`Flows` says that a depot's flows could not be read, and not why, to a
  device that is no operator's.** `depots[].error` is then `the flows
  could not be read; why is an operator's to read`. A device of an operator
  is told the error of the read, which names the directory of the depot's
  repository. The depot, its beads and the rest of the board are the same.
- **`GiveSecret` says which names were written, and not where, to a device
  that is no operator's.** Such a device has the call for its own
  address or credential in a source, and for the code of its own login, only.
  Its answer has no `file`, `credential`, `missing` or `source`. The first
  three name the file the value went to, and `source` is the source's whole
  record, which holds the file it reads its credential from and the command
  that mints its token. The code of a login goes to no file and the answer
  names the login (`login`) and the names given (`given`), which are told.
  A value the file cannot hold (empty, with a line break, or with white space
  at an end) is refused for such a device with the words `the value for
  <name> is empty` and the like, where a device of an operator is refused
  with the file's name before them. A device of an operator is sent the file,
  as the socket is.
- **A call that fails on the yard's own file says that it failed, and not
  on which file, to a device that is no operator's.** `AnswerAsk` whose
  answer would move the bead but the depot's repository cannot be read,
  `PutBlob` whose bytes the disk would not take, and `GiveSecret` whose
  value the file would not take are answered `the yard could not do that;
  why is an operator's to read`, and the full words are in the yard's log,
  for the operator. A refusal of what was sent reads whole: a value the
  file cannot hold is refused with the variable, as above, and a file over
  the yard's cap is refused with its size. A file the yard holds but cannot
  read is likewise a 500 without the file. A device of an operator is told
  the error, which names the file, as the socket is.
- **`Peers` has no `url` and no `error` for a device that is no
  operator's.** The address of a link, and the words of the last send that
  failed, which carry it, are an operator's. The names, what each link
  carries and the roster are the same.
- **`DeliveryFiles` says that a file was not taken, and not why, to a device
  that is no operator's.** `[].not_taken` is then `the file was not
  taken; why is an operator's to read`, where a device of an operator is told
  the reason, which is the error of the fetch where it failed and names the
  program of the source and the directory of the yard. The name, the size and
  the type the source gave are the same.
- **A depot pack's card is whole or a summary.** `run.pack.digest` is the
  pack's digest, as `yardr pack check` prints it. `run.pack.summary` is true
  on a card too large to read whole: `run.does` then names the pack by that
  digest and each file by its hash, and the texts stand behind it.
  `run.pack.files` ties them together, each entry `{name, line}`: `name` is
  the object of kind `file` in `run.pack.plan.objects` that holds the text,
  the diff and the hashes, and `line` is the zero-based index in `run.does`
  of that file's line, or `-1` for a file folded into the card's closing count. A
  client opens a file from `files` and never parses `does`. A whole card
  has no `summary` and no `files`.
- **`Bead` and `UserAsks` tell a run ask as its asker typed it, to a device
  that is no operator's.** The `run` card has `argv`, `command` and
  `description`, and an approved ask's `ran` has `argv` and `failed`; a
  pending member report's `pending_reason` is told to the operator alone. The
  yard's own words are held. `run.does` says what the command changes and
  what is there now (a setting's stored value, the file of a user's address,
  a hook's secret file), and is `[]`. `run.back` is the command that takes it
  back, and is `""`. `ran.result` is what the command printed, and is `""`.
  `run.back_by`, `run.carried` (the declarations and command files a source
  brings), `run.pack.plan` (the paths and the diffs of the files a pack
  writes), `run.pack.mappings` (the people, grants and sources that plan
  was shaped by), and `run.pack.digest`, `run.pack.summary` and
  `run.pack.files` (a summary card's shape, below) are absent. Where the
  typed words themselves carry a value or a
  path, they are the operator's alone: on a run ask that sets a setting, adds
  a source, or applies a pack with a depot or mappings named, `run.argv` is `[]` and `run.command` is `""` to such a
  device, and an approved ask's `ran.argv` is `[]`, the question and the
  command's help line only, and `weight[].line` is the yard's line for the
  class alone, with nothing of what this card does. The sentence of an approval of such a row names
  the command whole, so its `noted` event carries no `answer` to such a
  device, and its notification reads as the event without its sentence. A
  device of an operator is sent all of it, as the socket is.
- **`Bead` and `UserAsks` send a depot-push card and its `ran` only to a
  device of the current operator.** A device of any other user, including a
  user who was the operator when the ask was filed, receives neither field;
  the socket tells the whole card. The `noted` event for its approval and the
  notification made from that event hold the approval sentence from
  nonoperator devices.
- **A notification's `text` is made from what its person is told of the
  event.** An operator's `UserNotifications` has the words of each event as
  the stream sends it to an operator. Any other user's has the words as the
  stream sends it to a device that is no operator's, so a
  `workspace_kept` reads `session=s1` and not the worktree. A notification
  is written when its event is logged. Who is an operator is read then, and
  in a yard with none everyone gets the held words. Rows from earlier
  builds were remade on upgrade: a yard row of a user who is no operator
  is remade from what its person is told (schema 31, and again by schemas
  33 and 34 as the hold learned more old sentences), an operator's rows
  and hook rows (Source is the hook's) are left whole, and a row whose
  event was pruned is remade bare, from its bead, kind, reason and title
  alone.
- **`Users`, `GetUser`, `Crew` and `GetCrew` tell the files and commands
  of the operator's configuration to a device of an operator alone**: a
  user's record has `env_file`, the file their address is read from, and
  `last_push.error`, the words of a send that found that file unfit, which
  name it. A crew member's record has `dir`, the directory it runs in, and a
  `config` that names files and commands (`env_file`, `env_command`,
  `env_for_<user>`, `prompt`, `args`). The handles of its sessions name the
  directory too, and the yardr the agent was told to run (`handle`,
  `starting` and `left`, in the member and in each of its
  `conversations`). Any other device is sent the records without these,
  and so is the user's own device. A yard with no operator sends them to
  no device. Of `config` a device is sent `kind`, `model`, `provider`,
  `trust`, `greet`, `for`, `open_threads`, `set_state` and `mail_peers`,
  and no other key. The rest of each record is sent as it is, and the
  socket tells the whole record.
- **The stream carries the device's own mail alone**: a `mail`, `mail_read`
  or `reaction` event is sent only to a device of its sender or its
  receiver (a read receipt to its reader alone), however far back the
  stream replays; anything between two others is left out. A `hook`
  delivery goes out to every device, as a bead's events do, and so does
  the [`hook_files`](#a-hook-deliverys-files-arrived-hook_files) that says
  its files are recorded.
- **A mail's file goes to its parties alone**: `GET /user/blobs/<hash>`
  serves a note's file and a kept hook delivery's to any device, and a
  mail's file to a device of its sender or recipient, as the web view
  serves them, and anything else is 404. A delivery's files are named by
  [`DeliveryFiles`](#deliveryfiles), which every device may call: they are
  the yard's, as the delivery is, and no one user's. A file the device's
  own user put within the hour that no mail or note names yet is served to
  the device: its mail or note is on its way.
- **`GET /user/me`** answers who the token is, in place of [the name a
  client on the machine takes](#who-you-are):

  ```json
  {"user": "jo", "said_by": "phone", "yard": "south", "yardmaster": "yardmaster"}
  ```

  `user` is the user every call is made as, `said_by` the device's name,
  `yard` the yard's name, `yardmaster` the name of the yard's coordinator.
  A client opens its Ask conversation with that name; where the answer has
  no such field, from a yard older than the field, it asks `yardmaster`.
- **What it writes says how it came.** Every event a device's call writes as
  its user carries `data.via`, the device's name, and so do the letter and
  the note read back (`via` in `Conversation` and in `Bead`'s `notes[]`). A
  view shows it behind the writer: `jo (via phone)`. Down the link from the
  edge the mark says that too: `data.via` is `phone via edge`, shown as
  `jo (via phone via edge)`.
- **A request is bounded**: a call's body to 1 MiB, and `PutBlob`'s to the
  yard's cap on a file besides. One over it is answered 413. Over the edge,
  an answer larger than one frame of the link carries is answered 413
  instead, so one large file cannot close the link.
- The two headers of a reply on the socket, the build and the pid, are not
  sent.

### Let in, or not

Without the header, with a token the yard does not know, or with the token
of a device that was removed, every path under `/user/` is answered 401
`Unauthorized`, `/user/me` too, and a path that is not served as well: the
answer is the same whatever was wrong and whatever was asked for. It is
given before the request's body is read. A 401 is not a fault that passes:
a client says "this device is no longer let in" and stops asking, until it
is given a token again.

If looking the credential up does not finish in time, the yard answers 503
`Service Unavailable` with `Retry-After`: the token may be fine, so the
client keeps asking. If the yard cannot check a credential because its
store failed, it answers 500 `Internal Server Error`; that is a yard
fault, not a token to replace.

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
status too, so a store failure and a lookup that timed out are
distinguished from a 401.

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

The try limit is intended: five wrong codes end the open pairing, a code
cannot be guessed in five tries, and `yardr user device add` is unaffected,
so the operator either opens another pairing or adds the device directly.

A redemption the edge forwards carries one header more, `X-Edge-Presenter`,
the address of the signed-in account the edge resolved. The yard maps it to
a user through `web_jwt_users`. A mapped presenter may redeem only the
pairing opened for that user. A presenter mapped to another user, or an
unmapped presenter for a pairing whose user is mapped, is answered as a
wrong code is, 403 `{"refused": "wrong_code"}`, and counted with them. If
neither is mapped, the code redeems as without the header; the code is still
required. The edge sets the header and a device does not; the yard reads it
on this call and no other, and without it the code alone redeems
([users.md](users.md#enrolled-by-a-code)).

### The stream of a device

`GET <url>/user/events?after=<seq>` is [the stream](#the-stream), every
event of the yard but the mail of others and without what a device is not
told ([above](#over-the-network)), one line each. Two things end it that
do not end the socket's:

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

A secret, an approval, the grant list, the teammates list, a removal
filing, a session's screen and the telemetry switch are what the yard holds
to this itself, as far as it can see.
[`GiveSecret`, `ApproveAsk`, `ApproveSetup`, `SourceGrants`, `Teammates`,
`UninviteTeammate`, `SessionScreen`, `TelemetryShow` and
`TelemetrySet`](#for-the-operator-alone) are taken only from a request
that reached the listener from the yard's own machine, where the proxy or
tunnel that ends TLS stands (or a client on that machine) — the two
approvals, `SessionScreen` and the teammates calls also down the link from
the edge, and `GiveSecret` there for every secret on a hosted yard and for
the code of a harness login alone on any other, `ApproveSetup` with the
account typed being a secret there too
([edge.md](edge.md#footplate-through-the-link)) — and never from one whose
proxy says it came in plain (`X-Forwarded-Proto: http`; the header is read
for a no and never for a yes). Any other is refused with
the code `error`, in words that name the reason and say what may have
crossed a network in the clear: `GiveSecret` carries its value, and every
call carries the operator's device token. A reply may carry the grant list,
a session's screen (`SessionScreen`), the teammates, a removal filing or the
telemetry preview. A proxy on another machine that passes requests on to the
yard in plain HTTP is refused so: from there to the yard the value and the
token would be readable. An approval holds no secret of its own; it is held
to the rule so that a token read off a network cannot have a command of the
operator's run from wherever it was read. The grant list holds none either
and approves nothing; it is held to the rule because the operator's device
token goes with the request. A session's screen can show what an agent
printed, a secret among it, so `SessionScreen` is held to the rule for what
the screen carries, and crosses the link as an approval does. The teammates
list and a removal filing hold none either and approve nothing; they are held
to the rule for the same reason, and cross the link as an approval does. The
telemetry preview and switch follow the grant list's rule: neither crosses
the yard link, the preview may be read from the router's peer over https, and
the switch cannot be changed there, as approvals cannot. The hosted browser
control uses the separate `web` link and the JWT-authenticated view, acting
as the mapped yard user. Every
other call is served wherever it comes from, as before. The three that
open, read and end a pairing, `DevicePairOpen`, `DevicePairState` and
`DevicePairClose`, are the socket's alone: the listener answers each as a
method the yard does not have, to a device of the operator too, so the
rule above has no request of theirs to hold.

Down the link from the edge `GiveSecret` is taken from a device of the
operator, and which asks it is taken for depends on the yard. A hosted yard,
whose `hosted` setting is `true` (the provisioner writes it when it makes the
yard, [configuration.md](configuration.md#settings)), takes it for every
secret ask: the service runs the edge and the machine the yard lives on, and
the person has no shell there to answer at. Any other yard takes it for one
ask: the daemon's ask for the code a harness login waits for
([crew.md](crew.md#the-yard-leads-a-harnesss-login)). It is a secret ask with
`secret.login` set and `secret.env` of `["INPUT"]`, and with `secret.source`
empty and none of `secret.hook`, `secret.harness`, `secret.user`,
`secret.crew` and `secret.as` beside it. The code is used once, within
minutes, and the yard hands it to the waiting login in its memory and keeps
it nowhere. To know which ask a call names, such a yard reads its body, so a
giving down its link has its body read before it is refused, and nothing of
what is found is said back. Every other secret down its link, an API key and
a user's own login code (`secret.as`) among them, is refused with the code
`error`, in words that say a secret never crosses the edge and send the
person to the yard's own machine. On either yard a device of a user who is
not the operator is refused its own giving down the link in the same words,
and a giving that is taken is held to the approvals' rule there: when the
edge says the device reached it in plain HTTP, it is refused as they are.

`ApproveSetup` carries a secret when it types the account (`values`), and is
held to the same rule down the link. A hosted yard takes it whole. Any other
yard takes it without `values`, a candidate taken (`account`) or the account
kept (neither), which hold no secret, and refuses it with `values` in the
words of every other secret there, which send the person to the yard's own
machine (`yardr bead answer <bead> <ask> <n> --secret`). It reads the body to
tell, as it does for a giving, and a body it cannot read as the arguments of
the call is refused so too. A client does not offer to type a setup's account
down the link of a yard that is not hosted: it says "answer it at the yard".
The refusal comes after the request has crossed, so it cannot keep the edge
from having read what was sent; its words say so, as they do for a giving.

A hosted yard takes `GiveSecret`, `SourceGrants`, `SessionScreen`,
`Teammates`, `UninviteTeammate` and `TelemetryShow` from one peer besides
its own machine: its router, the address the `router_addr` setting names,
when the request says it came to the router over https
(`X-Forwarded-Proto: https`). On that network the router is the only such
peer. The header is read for a yes here, as it is read for a no from the
yard's own machine: without it, or saying anything but https, the call is
refused as from any other machine. From the router's peer `ApproveAsk`,
`ApproveSetup` and `TelemetrySet` are refused as from anywhere else: down
the link from the edge the approvals are taken, from the router never, and
the switch is changed from neither.

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
  "result": 182
}
```

| field of the result | type | |
|---|---|---|
| `(the result)` | number | the seq |

### `Settings`

The yard's operator setting, when set, and its caps on a file; to the devices of an operator, also the operator's words to their agents and whether the yard is a hosted one. `yardr chat` reads the operator only to name them when the current name is not a user of the yard; a client can omit this call when it already knows who it is for and sends no file.

```
POST /call/Settings
{}

{
  "result": {
    "blob_max_bytes": "8388608",
    "files_max": "64",
    "hosted": "false",
    "operator": "jo",
    "operator_note": "I read diffs, not prose",
    "operator_style": "show"
  }
}
```

| field of the result | type | |
|---|---|---|
| `operator` | string, may be absent | the user name of the operator an ask with no `--to` goes to (one of the users whose role is `operator`); absent when the yard has not set one |
| `operator_style` | string, may be absent | how the operator asked agents to work with them: `guide`, `show`, `work` or `report`; absent when the operator has not set one, and to a device of any user who is not an operator |
| `operator_note` | string, may be absent | the one line the operator asked agents to know about them; absent as `operator_style` is |
| `hosted` | string, may be absent | `true` where the yard is a hosted one, which the hosting service made and runs (its provisioner writes it when it makes the yard, [configuration.md](configuration.md#settings)); `false` or absent where its operator runs the yard. Absent too to a device of any user who is not an operator, whatever the yard is. Only `true` says the yard is hosted |
| `blob_max_bytes` | string, may be absent | the most bytes `PutBlob` takes, a whole number in a string; absent when the yard has not set one, and the cap is then 8388608 (8 MiB) |
| `files_max` | string | how many files one mail or note may carry, a whole number in a string; the yard's own count, so a client needs no copy of the number |

The operator's words are what every session of an agent reads when it starts (`yardr prime`), so the socket tells them to whoever calls, and every other setting with them. Over the network a device is told these keys alone: the words and `hosted` to a device of an operator and to no other, and to none in a yard with no operator. A `setting_changed` in a device's stream has its `value` only for a key the device is told; for any other it has the `key` and no `value`.

`hosted` is how a client down the link from the edge learns which yard it has reached: a hosted yard takes `GiveSecret` there for every secret, and any other yard for a harness login's code alone ([TLS is yours](#tls-is-yours-and-the-clients-rule)).

### `Users`

The users of the yard, by name and role.

```
POST /call/Users
{}

{
  "result": [
    {
      "name": "hal",
      "cursor": 176,
      "notification_cursor": 176
    },
    {
      "name": "jo",
      "role": "operator",
      "cursor": 0,
      "notification_cursor": 0
    },
    {
      "name": "sam",
      "cursor": 175,
      "notification_cursor": 175
    }
  ]
}
```

| field of the result | type | |
|---|---|---|
| `[].name` | string | the name mail to the user is addressed to |
| `[].role` | string, may be absent | the user's role: `operator` or `member`; absent reads as `member` |
| `[].cursor` | number | the user's mail is read through this seq |
| `[].notification_cursor` | number | the same for the user's notifications |

The reply carries each user whole, as `curl` shows it, but only the table above is promised. The rest is yardr's own, and a client ignores what it does not know. Over the network a device is sent the records without two fields of the operator's configuration: `env_file`, the file the user's address is read from, and `last_push.error`, the words of a send that found that file unfit, which name it. That holds for the user's own device too. A device of an operator is sent both ([over the network](#over-the-network)).

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
    "role": "operator",
    "cursor": 0,
    "notification_cursor": 0
  }
}
```

| field of the result | type | |
|---|---|---|
| `name` | string | the name |
| `role` | string, may be absent | the user's role: `operator` or `member`; absent reads as `member` |
| `cursor` | number | the user's mail is read through this seq |
| `notification_cursor` | number | the same for the user's notifications |

The record is the one [`Users`](#users) lists, and it is sent as `Users` sends it: over the network without `env_file` and `last_push.error`, unless the device is an operator's. A device that is no operator's gets its own record without them too.

### `Crew`

The crew, running or not, by name.

```
POST /call/Crew
{}

{
  "result": [
    {
      "name": "signalman",
      "enabled": true,
      "model": "sonnet",
      "kind": "claude",
      "state": "running",
      "status": "idle",
      "login_screen": true,
      "login_led": true,
      "started_at": "2026-10-08T09:30:00Z"
    },
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
| `[].login_screen` | bool, may be absent | `true` while the running session sits at its harness's login screen, where it reads no mail until the harness is logged in; `status` may read `idle` or `blocked` there, and a client says "not logged in" instead, as `yardr chat` does. Absent while it does not, and while no session runs |
| `[].login_led` | bool, may be absent | `true` while the yard leads a login for the session: it asked the operator how to log in, and the operator answers at their asks ([`UserAsks`](#userasks)) until the login is made and the session starts again. Absent otherwise. Only the session the yard asked for says it: another session of the same harness at its screen is logged in by the same login, and says only `login_screen` |

Mail to a crew member that is not running is kept, and read when it next starts. A session at its harness's login screen reads none either: `login_screen` says it, and `login_led` that the yard leads the login. Both change with no change to the crew member's definition: read `Crew` again on a `crew_login` event, as on any other whose kind begins `crew_`.

The reply carries each crew member whole, as `curl` shows it, but only the table above is promised. `model` and `kind` are what a client reads of the config. The rest is yardr's own, and a client ignores what it does not know. Over the network a device is sent the records without the operator's configuration: `dir`, the directory the crew member runs in; every key of `config` but `kind`, `model`, `provider`, `trust`, `greet`, `for`, `open_threads`, `set_state` and `mail_peers`, which leaves out the files and commands it names; and the handles of its sessions (`handle`, `starting` and `left`, in the member and in each of its `conversations`), which name that directory too. A device of an operator is sent all of it ([over the network](#over-the-network)).

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

```
POST /call/GetCrew
{"name":"signalman"}

{
  "result": {
    "name": "signalman",
    "enabled": true,
    "model": "sonnet",
    "kind": "claude",
    "state": "running",
    "status": "idle",
    "login_screen": true,
    "login_led": true,
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
| `login_screen` | bool, may be absent | `true` while the running session sits at its harness's login screen, where it reads no mail until the harness is logged in; `status` may read `idle` or `blocked` there, and a client says "not logged in" instead, as `yardr chat` does. Absent while it does not, and while no session runs |
| `login_led` | bool, may be absent | `true` while the yard leads a login for the session: it asked the operator how to log in, and the operator answers at their asks ([`UserAsks`](#userasks)) until the login is made and the session starts again. Absent otherwise. Only the session the yard asked for says it: another session of the same harness at its screen is logged in by the same login, and says only `login_screen` |

The record is the one [`Crew`](#crew) lists, and it is sent as `Crew` sends it: over the network without `dir`, the files and commands of `config` and the handles of its sessions, unless the device is an operator's.

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

The mail to a user: the unread, oldest first, then the read, newest first. Only the user reads their inbox, and an operator reads any user's.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `limit` | number | at most this many letters, the unread first; `0` is all |
| `actor` | string | who reads: the user's name, or an operator's; left out, the user |

```
POST /call/UserInbox
{"name":"jo","limit":10,"actor":"jo"}

{
  "result": {
    "mail": [
      {
        "seq": 14,
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
        "seq": 52,
        "at": "2026-10-08T09:30:00Z",
        "from": "josephine@north",
        "to": "jo",
        "text": "I am at the other machine until four.",
        "read": false,
        "as": "jo"
      }
    ],
    "unread": 2,
    "last": 52,
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
{"seq":14,"emoji":"🎉","actor":"jo"}

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
      "seq": 14,
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
      "seq": 37,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "On the train. Land the shed when it is green.",
      "read": false,
      "via": "phone"
    },
    {
      "seq": 61,
      "at": "2026-10-08T09:30:00Z",
      "from": "josephine@north",
      "to": "yardmaster",
      "text": "And the fence after the shed.",
      "read": false,
      "as": "jo"
    },
    {
      "seq": 183,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Thanks. Land it.",
      "read": false
    },
    {
      "seq": 184,
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
      "seq": 183,
      "at": "2026-10-08T09:30:00Z",
      "from": "jo",
      "to": "yardmaster",
      "text": "Thanks. Land it.",
      "read": false
    },
    {
      "seq": 184,
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
      "seq": 185,
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
      "seq": 52,
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

Marks the user's mail read through a seq. It answers nothing; when mail was in between it is a `mail_read` event in the stream. Only the user acknowledges their inbox, and an operator any user's.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `through` | number | the seq of the newest letter shown |
| `actor` | string | who acknowledges: the user's name, or an operator's; left out, the user |

```
POST /call/AckUserInbox
{"name":"jo","through":52,"actor":"jo"}

{}
```

### `UserNotifications`

What the yard reports to a user, apart from every conversation: the events the user subscribed to, oldest first. Only the user reads their notifications, and an operator reads any user's.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `limit` | number | at most this many; `0` is all the inbox keeps |
| `actor` | string | who reads: the user's name, or an operator's; left out, the user |

```
POST /call/UserNotifications
{"name":"jo","limit":0,"actor":"jo"}

{
  "result": {
    "notifications": [
      {
        "seq": 11,
        "at": "2026-10-08T09:30:00Z",
        "kind": "created",
        "bead": "yd-shed",
        "text": "yd-shed created (all) by yardmaster originator=jo stage=new title=Paint the shed type=task",
        "read": false
      },
      {
        "seq": 12,
        "at": "2026-10-08T09:30:00Z",
        "kind": "noted",
        "bead": "yd-shed",
        "text": "yd-shed noted (all) by yardmaster note=1 on \"Paint the shed\"",
        "read": false
      }
    ],
    "unread": 2,
    "last": 12
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

Marks the user's notifications read through a seq: a position of its own, which the mail's does not move. It answers nothing; it is a `notifications_read` event in the stream. Only the user acknowledges their notifications, and an operator any user's.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `through` | number | the seq of the newest notification shown |
| `actor` | string | who acknowledges: the user's name, or an operator's; left out, the user |

```
POST /call/AckUserNotifications
{"name":"jo","through":12,"actor":"jo"}

{}
```

### `UserAsks`

The person's decisions: every open ask addressed to them, on beads the viewer may see in any stage and of any type, the journal of a crew member among them (where the yard asks the operator how to log its harness in), and from the linked yards, oldest first, with what they have dealt with beside it. Only the user reads their queue, and an operator reads any user's. A question another yard asked stands as `bead@peer`, placed by when this yard took it; answered, put aside or declined here it is among the decided by what this yard knows of it, since the answer lands in the other yard. It is not `yardr review`'s queue, which walks beads in manual stages by bead age. A device's viewer is always its user.

| argument | type | |
|---|---|---|
| `name` | string | the user |
| `actor` | string | who reads: the user's name, or an operator's; left out, the user |
| `viewer` | string | who reads the queue; a device's viewer is its user; left out, the yard itself |

```
POST /call/UserAsks
{"name":"jo","actor":"jo","viewer":"jo"}

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
        "bead": "yd-links",
        "bead_title": "Move the links",
        "topic": "topic:linear-out",
        "ask": 12,
        "question": "The web view cleanup: build?",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_UPDATE_TOKEN, for the source crm (https://crm.example.com)"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_TOKEN, for the source crm (https://crm.example.com)"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: PHONE_USER, jo's own address in the source phone (https://phone.example.com); jo is told through phone once it is given and yardr user set jo notify=phone is run"
          }
        ]
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
        },
        "weight": [
          {
            "class": "trust",
            "line": "widens who the yard listens to: the link with north takes 300 mails and asks an hour from it, 100 now"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: INPUT, for the waiting login claude-example2"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_UPDATE_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_TOKEN, jo's own credential for the source crm (https://crm.example.com), read as jo when a call is made --as jo"
          },
          {
            "class": "as-user",
            "line": "lets calls or words count as a person's: a call of the source crm made --as jo reads it as jo's own"
          }
        ]
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
        "concealed": false,
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: GOG_ACCOUNT, for the source gog (https://gog.example.com)"
          }
        ]
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
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://eu.courier.example.com/api (base eu). Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to group builders and crew yardmaster — pick one:"],
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
          ],
          "base": "eu"
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account COURIER_ACCOUNT of pack courier"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group builders and crew yardmaster call put of courier (an update)"
          },
          {
            "class": "code",
            "line": "brings a program the yard will run: the command file courier-mint of courier (new)"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 45,
        "question": "Set up the courier source for the night crew?",
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
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://eu.courier.example.com/api (base eu). Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to crew yardmaster — pick one:"],
          "back": "yardr source rm courier; yardr source revoke courier \u003cwhat\u003e --crew yardmaster",
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
              "crew": "yardmaster"
            }
          ],
          "base": "eu"
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account COURIER_ACCOUNT of pack courier"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets crew yardmaster call put of courier (an update)"
          },
          {
            "class": "code",
            "line": "brings a program the yard will run: the command file courier-mint of courier (new)"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 46,
        "question": "Update the ledger source?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "setup": {
          "pack": "ledger",
          "label": "embedded in yardr doctest",
          "account": ["LEDGER_TOKEN", "LEDGER_UPDATE_TOKEN"],
          "account_file": "/home/jo/.yardr/sources/ledger.env",
          "given": true,
          "does": ["pack ledger (embedded in yardr doctest), of kind source, into the yard: it replaces 1 source and copies no command files, and changes nothing else. It makes no hook and sets no credential's file.", "source ledger: its calls go to https://ledger.example.com/v2. Its credential is the variable LEDGER_TOKEN (the name, never the value), with LEDGER_UPDATE_TOKEN for updates, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no mint.", "it would replace the source ledger the yard has under that name.", "source ledger base: \"https://ledger.example.com/api\" -\u003e \"https://ledger.example.com/v2\".", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command.", "the account LEDGER_TOKEN and LEDGER_UPDATE_TOKEN: given, in /home/jo/.yardr/sources/ledger.env. Type another unseen to change it, or approve keeping it with a bare preset pick (written to /home/jo/.yardr/sources/ledger.env, named in the answer, never the value).", "grants to group builders and crew yardmaster — pick one:"],
          "back": "yardr source rm ledger; yardr source revoke ledger \u003cwhat\u003e --group builders --crew yardmaster",
          "back_by": "the operator",
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "ledger",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "ledger",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account LEDGER_TOKEN and LEDGER_UPDATE_TOKEN of pack ledger"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group builders and crew yardmaster call put of ledger (an update)"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 47,
        "question": "Update the parcel source?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "setup": {
          "pack": "parcel",
          "label": "embedded in yardr doctest",
          "account": ["PARCEL_TOKEN", "PARCEL_UPDATE_TOKEN"],
          "account_file": "/home/jo/.yardr/sources/parcel.env",
          "missing": ["PARCEL_UPDATE_TOKEN"],
          "does": ["pack parcel (embedded in yardr doctest), of kind source, into the yard: it replaces 1 source and copies no command files, and changes nothing else. It makes no hook and sets no credential's file.", "source parcel: its calls go to https://parcel.example.com/api. Its credential is the variable PARCEL_TOKEN (the name, never the value), with PARCEL_UPDATE_TOKEN for updates, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no mint.", "it would replace the source parcel the yard has under that name.", "source parcel credential: {\"env\":\"PARCEL_TOKEN\",\"header\":\"Authorization: Bearer {credential}\"} -\u003e {\"env\":\"PARCEL_TOKEN\",\"header\":\"Authorization: Bearer {credential}\",\"update_env\":\"PARCEL_UPDATE_TOKEN\"}.", "source parcel calls: [{\"name\":\"ping\",\"kind\":\"read\",\"request\":{\"method\":\"GET\",\"path\":\"/me\"},\"answer\":{\"max\":65536}}] -\u003e [{\"name\":\"ping\",\"kind\":\"read\",\"request\":{\"method\":\"GET\",\"path\":\"/me\"},\"answer\":{\"max\":65536}},{\"name\":\"put\",\"kind\":\"update\",\"request\":{\"method\":\"POST\",\"path\":\"/it\"},\"answer\":{\"max\":65536}}].", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command.", "the account PARCEL_TOKEN and PARCEL_UPDATE_TOKEN: given, in /home/jo/.yardr/sources/parcel.env, but missing PARCEL_UPDATE_TOKEN: type PARCEL_UPDATE_TOKEN unseen (written to /home/jo/.yardr/sources/parcel.env, named in the answer, never the value).", "grants to group builders and crew yardmaster — pick one:"],
          "back": "yardr source rm parcel; yardr source revoke parcel \u003cwhat\u003e --group builders --crew yardmaster",
          "back_by": "the operator",
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "parcel",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "parcel",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account PARCEL_TOKEN and PARCEL_UPDATE_TOKEN of pack parcel"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group builders and crew yardmaster call put of parcel (an update)"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 54,
        "question": "The API key Claude Code will use",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "secret": {
          "source": "",
          "env": ["ANTHROPIC_API_KEY"],
          "crew": "yardmaster",
          "harness": "claude",
          "line": "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start"
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 59,
        "question": "Hook the bot's messages up?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "run": {
          "argv": ["hook", "add", "bot", "--source", "bot"],
          "command": "yardr hook add bot --source bot",
          "does": ["it adds the hook bot: POST /hooks/bot takes deliveries from the source bot (https://bot.example.com).", "what it sends in: deliveries are verified by the header X-Test-Secret holding the hook's secret itself (scheme secret); it sends no delivery id; it sends no timestamp; bodies are unique; the command [bot/normalize] turns a verified body into the envelope, handed no credential; its events may carry the fields chat; the yard registers its webhook with setwebhook (POST /setWebhook), filling url with the public URL and secret_token with the hook's secret.", "the yard makes the signing secret for bot and keeps it in /home/jo/.yardr/hooks/bot.secret (mode 0600); the hook names that file, and the value is in no note, event, answer or card. No secret is typed.", "the approval gives project in the clear, which the yard sends to the register call with the URL and the secret.", "it will call POST /setWebhook at https://bot.example.com/api to register https://yard.example/hooks/bot."],
          "back": "yardr hook rm bot",
          "back_by": "only the operator"
        },
        "weight": [
          {
            "class": "trust",
            "line": "widens who the yard listens to: POST /hooks/bot takes deliveries from outside the yard for the source bot"
          }
        ]
      },
      {
        "bead": "yd-links",
        "bead_title": "Move the links",
        "topic": "topic:linear-out",
        "ask": 60,
        "question": "Turn push off for the yard depot?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "depot_push": {
          "depot": "yard",
          "off": true,
          "does": ["turns push to github.com off for depot yard (origin https://github.com/ada/yard.git).", "takes YARDR_PUSH=origin out of yard-assembly's cmd; the rest of the cmd stands untouched, YARDR_LAND as it was.", "takes the pack's helper line for https://github.com out of depot yard's own git config with git -C yard config --unset credential.https://github.com.helper; no other depot's config changes.", "landings of the depot then land here and push nowhere."],
          "back": "yardr bead ask yd-links \"\u003cquestion\u003e\" --depot-push yard",
          "back_by": "only the operator"
        },
        "weight": [
          {
            "class": "outside",
            "line": "lets the yard change something outside it: landings of depot yard push to its origin no more"
          }
        ]
      }
    ],
    "decided": [
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 65,
        "question": "Invite Rhea to the yard?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "run": {
          "argv": ["user", "invite", "rhea", "--email", "rhea@example.net"],
          "command": "yardr user invite rhea --email rhea@example.net",
          "does": ["adds the user rhea with the role member, and maps rhea@example.net to rhea in web_jwt_users: signed in at the edge as rhea@example.net, they act as rhea.", "reports the invite to the edge for the yard jo/edgeyard (member add): until the report lands the card reads as not yet at the edge, and nobody is admitted meanwhile.", "they set a password from the mail the edge sends to this email, then sign in; nothing the operator holds admits them."],
          "back": "yardr user uninvite rhea",
          "back_by": "only the operator"
        },
        "ran": {
          "argv": ["user", "invite", "rhea", "--email", "rhea@example.net"],
          "result": "approved, to be carried out",
          "pending_reason": "the edge refused this yard: mail limit reached (429)"
        },
        "weight": [
          {
            "class": "trust",
            "line": "widens who the yard listens to: a new user, rhea, whose answers and approvals the yard takes"
          }
        ]
      },
      {
        "bead": "yd-links",
        "bead_title": "Move the links",
        "topic": "topic:linear-out",
        "ask": 63,
        "question": "Can the note wait?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "put aside",
        "return_at": "2026-10-08T09:30:00Z"
      },
      {
        "bead": "yd-greet",
        "bead_title": "The greeting",
        "topic": "",
        "ask": 61,
        "question": "Turn push off for the yard depot?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "ran": {
          "argv": ["depot-push-off", "yard"],
          "result": "approved, to be carried out"
        },
        "depot_push": {
          "depot": "yard",
          "off": true,
          "does": ["turns push to github.com off for depot yard (origin https://github.com/ada/yard.git).", "takes YARDR_PUSH=origin out of yard-assembly's cmd; the rest of the cmd stands untouched, YARDR_LAND as it was.", "takes the pack's helper line for https://github.com out of depot yard's own git config with git -C yard config --unset credential.https://github.com.helper; no other depot's config changes.", "landings of the depot then land here and push nowhere."],
          "back": "yardr bead ask yd-greet \"\u003cquestion\u003e\" --depot-push yard",
          "back_by": "only the operator"
        },
        "weight": [
          {
            "class": "outside",
            "line": "lets the yard change something outside it: landings of depot yard push to its origin no more"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 57,
        "question": "May the builders write notes in the CRM?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "run": {
          "argv": ["source", "grant", "crm", "put", "--group", "builders"],
          "command": "yardr source grant crm put --group builders",
          "does": ["the group builders (herdr, claude), and so each session it starts, may make the call put (POST /it) of the source crm (https://crm.example.com), with the credential the yard holds for it, which changes something there. It is granted read (every read call) of crm now."],
          "back": "yardr source revoke crm put --group builders"
        },
        "ran": {
          "argv": ["source", "grant", "crm", "put", "--group", "builders"],
          "result": "group builders may call crm: put (update), read"
        },
        "weight": [
          {
            "class": "outside",
            "line": "lets the yard change something outside it: group builders may call put of crm (an update)"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 55,
        "question": "The API key Claude Code needs at first login",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "answered",
        "secret": {
          "source": "",
          "env": ["ANTHROPIC_API_KEY"],
          "crew": "yardmaster",
          "harness": "claude",
          "line": "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start"
        },
        "given": ["ANTHROPIC_API_KEY"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 52,
        "question": "Update the parcel source for the elders too?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "declined",
        "words": "the builders' card covers it",
        "setup": {
          "pack": "parcel",
          "label": "embedded in yardr doctest",
          "account": ["PARCEL_TOKEN", "PARCEL_UPDATE_TOKEN"],
          "account_file": "/home/jo/.yardr/sources/parcel.env",
          "missing": ["PARCEL_UPDATE_TOKEN"],
          "does": ["pack parcel (embedded in yardr doctest), of kind source, into the yard: it replaces 1 source and copies no command files, and changes nothing else. It makes no hook and sets no credential's file.", "source parcel: its calls go to https://parcel.example.com/api. Its credential is the variable PARCEL_TOKEN (the name, never the value), with PARCEL_UPDATE_TOKEN for updates, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no mint.", "it would replace the source parcel the yard has under that name.", "source parcel credential: {\"env\":\"PARCEL_TOKEN\",\"header\":\"Authorization: Bearer {credential}\"} -\u003e {\"env\":\"PARCEL_TOKEN\",\"header\":\"Authorization: Bearer {credential}\",\"update_env\":\"PARCEL_UPDATE_TOKEN\"}.", "source parcel calls: [{\"name\":\"ping\",\"kind\":\"read\",\"request\":{\"method\":\"GET\",\"path\":\"/me\"},\"answer\":{\"max\":65536}}] -\u003e [{\"name\":\"ping\",\"kind\":\"read\",\"request\":{\"method\":\"GET\",\"path\":\"/me\"},\"answer\":{\"max\":65536}},{\"name\":\"put\",\"kind\":\"update\",\"request\":{\"method\":\"POST\",\"path\":\"/it\"},\"answer\":{\"max\":65536}}].", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command.", "the account PARCEL_TOKEN and PARCEL_UPDATE_TOKEN: given, in /home/jo/.yardr/sources/parcel.env, but missing PARCEL_UPDATE_TOKEN: type PARCEL_UPDATE_TOKEN unseen (written to /home/jo/.yardr/sources/parcel.env, named in the answer, never the value).", "grants to group elders and crew yardmaster — pick one:"],
          "back": "yardr source rm parcel; yardr source revoke parcel \u003cwhat\u003e --group elders --crew yardmaster",
          "back_by": "the operator",
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "parcel",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "parcel",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account PARCEL_TOKEN and PARCEL_UPDATE_TOKEN of pack parcel"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group elders and crew yardmaster call put of parcel (an update)"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 50,
        "question": "Update the ledger source for the elders too?",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "state": "declined",
        "words": "the builders' card covers it",
        "setup": {
          "pack": "ledger",
          "label": "embedded in yardr doctest",
          "account": ["LEDGER_TOKEN", "LEDGER_UPDATE_TOKEN"],
          "account_file": "/home/jo/.yardr/sources/ledger.env",
          "given": true,
          "does": ["pack ledger (embedded in yardr doctest), of kind source, into the yard: it replaces 1 source and copies no command files, and changes nothing else. It makes no hook and sets no credential's file.", "source ledger: its calls go to https://ledger.example.com/v2. Its credential is the variable LEDGER_TOKEN (the name, never the value), with LEDGER_UPDATE_TOKEN for updates, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no mint.", "it would replace the source ledger the yard has under that name.", "source ledger base: \"https://ledger.example.com/api\" -\u003e \"https://ledger.example.com/v2\".", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command.", "the account LEDGER_TOKEN and LEDGER_UPDATE_TOKEN: given, in /home/jo/.yardr/sources/ledger.env. Type another unseen to change it, or approve keeping it with a bare preset pick (written to /home/jo/.yardr/sources/ledger.env, named in the answer, never the value).", "grants to group elders and crew yardmaster — pick one:"],
          "back": "yardr source rm ledger; yardr source revoke ledger \u003cwhat\u003e --group elders --crew yardmaster",
          "back_by": "the operator",
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "ledger",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "ledger",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account LEDGER_TOKEN and LEDGER_UPDATE_TOKEN of pack ledger"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group elders and crew yardmaster call put of ledger (an update)"
          }
        ]
      },
      {
        "bead": "yd-crm",
        "bead_title": "Read the CRM",
        "topic": "",
        "ask": 48,
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
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://eu.courier.example.com/api (base eu). Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to group elders and crew yardmaster — pick one:"],
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
          ],
          "base": "eu"
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account COURIER_ACCOUNT of pack courier"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group elders and crew yardmaster call put of courier (an update)"
          },
          {
            "class": "code",
            "line": "brings a program the yard will run: the command file courier-mint of courier (new)"
          }
        ]
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
        "given": ["GOG_ACCOUNT"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: GOG_ACCOUNT, for the source gog (https://gog.example.com)"
          }
        ]
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
        "given": ["CRM_UPDATE_TOKEN"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_UPDATE_TOKEN, jo's own credential for the source crm (https://crm.example.com), read as jo when a call is made --as jo"
          },
          {
            "class": "as-user",
            "line": "lets calls or words count as a person's: a call of the source crm made --as jo reads it as jo's own"
          }
        ]
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
        "given": ["CRM_TOKEN"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
          }
        ]
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
        "given": ["INPUT"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: INPUT, for the waiting login claude-example1"
          }
        ]
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
        },
        "weight": [
          {
            "class": "trust",
            "line": "widens who the yard listens to: the link with north takes 100 mails and asks an hour from it, 30 now"
          }
        ]
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
        "given": ["PAGER_USER"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: PAGER_USER, jo's own address in the source pager (https://pager.example.com); jo is told through pager once it is given and yardr user set jo notify=pager is run"
          }
        ]
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
        "given": ["BOOKS_TOKEN"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: BOOKS_TOKEN, for the source books (https://books.example.com)"
          }
        ]
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
| `open[].weight` | array, may be absent | the card's weight as the yard weighed it when it wrote the card: one entry a class, each a way approving it widens what the yard does ([weighty cards](#weighty-cards)). Absent on a routine card |
| `open[].weight[].class` | string | `credential`, `as-user`, `outside`, `code`, `trust` or `reads`. A later build may add a class: draw one a client does not know as weighty, with its line |
| `open[].weight[].line` | string | the yard's line for the class with what this card does, as a terminal card prints it after `WEIGHTY · <class>: ` |
| `open[].secret` | object, may be absent | what a secret ask asks for, as `Bead` says it; absent on any other ask |
| `open[].secret.source` | string | the declared source the secret is the credential of; with `user`, `crew` or `as`, the source the address or credential is in; empty on a login or harness-key ask |
| `open[].secret.env` | array of string | the names to ask the person a value for; with a harness-key ask, variables in that harness kind's `login_env` |
| `open[].secret.user` | string, may be absent | the user whose own address in the source is asked for; absent on an ask for the source's credential |
| `open[].secret.crew` | string, may be absent | the crew member whose own credential in the source is asked for, or on a harness-key ask the crew member at whose login screen it is asked (the key goes to the operator's key file of the kind, which every session on the operator's login reads); absent on any other secret ask |
| `open[].secret.as` | string, may be absent | with `login`, the user whose login that is; otherwise the user whose own credential in the source is asked for; absent on any other secret ask |
| `open[].secret.login` | string, may be absent | the waiting login the value goes to; with `as`, the login is that user's own; absent on any other secret ask |
| `open[].secret.harness` | string, may be absent | the harness kind whose API key is asked for; absent on any other secret ask |
| `open[].secret.line` | string | what the card says is asked for, in the yard's words |
| `open[].concealed` | bool, may be absent | whether a secret ask's value is drawn hidden: false where the source marks every asked name as no secret; absent (concealed) on any other ask |
| `open[].run` | object, may be absent | the command a run ask proposes, as `Bead` says it; absent on any other ask |
| `open[].run.argv` | array of string | the command in words, without the leading `yardr`. On a run ask that sets a setting, adds a source, or applies a pack with a depot or mappings named, a device of the current operator is told them, whoever the ask was put to. Any other device is told `[]`, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. The socket tells the whole card |
| `open[].run.command` | string | the command whole, as it is typed in a terminal. Never shorten it. On a run ask that sets a setting, adds a source, or applies a pack with a depot or mappings named, a device of the current operator is told it, whoever the ask was put to. Any other device is told `""`, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. The socket tells the whole card |
| `open[].run.does` | array of string | what the command changes, in the yard's words |
| `open[].run.back` | string | the command that takes the change back |
| `open[].run.back_by` | string, may be absent | who may run it, as `Bead` says it; absent where that is anyone |
| `open[].setup` | object, may be absent | the setup a setup ask proposes, as `Bead` says it; absent on any other ask |
| `open[].setup.pack` | string | the embedded pack to set up, of kind source |
| `open[].setup.base` | string, may be absent | the selected region base name; absent when calls go to the declaration's own base |
| `open[].setup.label` | string, may be absent | the build the pack is embedded in, as the card shows it |
| `open[].setup.account` | array of string | the names to type unseen beside the preset pick |
| `open[].setup.account_file` | string, may be absent | the file the value goes to, on the yard's machine. A device of the current operator is told it, whoever the ask was put to. Any other device is told the card with the field absent, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. The socket tells the whole card |
| `open[].setup.given` | bool, may be absent | on an update, where the yard already holds the source the pack brings: `true` when its account file already holds every name of `setup.account`, so that a bare preset pick keeps it; absent on a first setup, where nothing stands yet, and where the file lacks a name (`setup.missing`) |
| `open[].setup.missing` | array of string, may be absent | on an update, the names of `setup.account` the account file that stands does not set, where the pack newly needs them (every name where the file is not there): the answer types exactly these, or takes a candidate; absent on a first setup and where the file holds every name |
| `open[].setup.candidates` | array, may be absent | yard sources whose account the card offers to reuse, as `Bead` tells them. A device of the current operator is told them, whoever the ask was put to. Any other device is told the card as one with none, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. The socket tells the whole card |
| `open[].setup.candidates[].source` | string | the source whose account the card offers |
| `open[].setup.candidates[].file` | string | the file holding the shared account inputs; told to a device of the current operator alone |
| `open[].setup.candidates[].values` | object | the shared account input names and values the file holds; told to a device of the current operator alone |
| `open[].setup.does` | array of string | what approving changes and what is there now, in the yard's words. The account sentence, which names the file the value goes to and, where the card lists candidates, each by source, values and file, is told to a device of the current operator alone. Any other device is told the account sentence of a card with none and naming no file |
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
| `open[].depot_push` | object, may be absent | the yard's depot-push card; absent on any other ask, and on a device that is not the current operator's. The socket tells the whole card |
| `open[].depot_push.depot` | string | the depot whose push is changed |
| `open[].depot_push.off` | bool, may be absent | true when the card turns push off; absent when it turns push on |
| `open[].depot_push.does` | array of string | what approving changes and what stands now, in the yard's words |
| `open[].depot_push.back` | string | the filing that takes the change back |
| `open[].depot_push.back_by` | string, may be absent | who may file `back`, in words that stand before `can take it back`; absent where that is anyone |
| `decided[].bead` | string | the bead the question stands on: a handle, or `bead@peer` for a question another yard asked |
| `decided[].bead_title` | string | the bead's title |
| `decided[].topic` | string | the bead's topic: its first `topic:` label, `""` when it has none |
| `decided[].ask` | number | the ask's seq on its bead: what an answer names |
| `decided[].question` | string | the question in one line, as a person would ask it |
| `decided[].asker` | string | who asked: a name, or `name@peer` for a question another yard asked |
| `decided[].since` | time | when the ask was written: how long it waits (as sent, for a question another yard asked) |
| `decided[].weight` | array, may be absent | the card's weight as the yard weighed it when it wrote the card: one entry a class, each a way approving it widens what the yard does ([weighty cards](#weighty-cards)). Absent on a routine card |
| `decided[].weight[].class` | string | `credential`, `as-user`, `outside`, `code`, `trust` or `reads`. A later build may add a class: draw one a client does not know as weighty, with its line |
| `decided[].weight[].line` | string | the yard's line for the class with what this card does, as a terminal card prints it after `WEIGHTY · <class>: ` |
| `decided[].secret` | object, may be absent | what a secret ask asks for, as `Bead` says it; absent on any other ask |
| `decided[].secret.source` | string | the declared source the secret is the credential of; with `user`, `crew` or `as`, the source the address or credential is in; empty on a login or harness-key ask |
| `decided[].secret.env` | array of string | the names to ask the person a value for; with a harness-key ask, variables in that harness kind's `login_env` |
| `decided[].secret.user` | string, may be absent | the user whose own address in the source is asked for; absent on an ask for the source's credential |
| `decided[].secret.crew` | string, may be absent | the crew member whose own credential in the source is asked for, or on a harness-key ask the crew member at whose login screen it is asked (the key goes to the operator's key file of the kind, which every session on the operator's login reads); absent on any other secret ask |
| `decided[].secret.as` | string, may be absent | with `login`, the user whose login that is; otherwise the user whose own credential in the source is asked for; absent on any other secret ask |
| `decided[].secret.login` | string, may be absent | the waiting login the value goes to; with `as`, the login is that user's own; absent on any other secret ask |
| `decided[].secret.harness` | string, may be absent | the harness kind whose API key is asked for; absent on any other secret ask |
| `decided[].secret.line` | string | what the card says is asked for, in the yard's words |
| `decided[].concealed` | bool, may be absent | whether a secret ask's value is drawn hidden: false where the source marks every asked name as no secret; absent (concealed) on any other ask |
| `decided[].run` | object, may be absent | the command a run ask proposes, as `Bead` says it; absent on any other ask |
| `decided[].run.argv` | array of string | the command in words, without the leading `yardr`. On a run ask that sets a setting, adds a source, or applies a pack with a depot or mappings named, a device of the current operator is told them, whoever the ask was put to. Any other device is told `[]`, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. The socket tells the whole card |
| `decided[].run.command` | string | the command whole, as it is typed in a terminal. Never shorten it. On a run ask that sets a setting, adds a source, or applies a pack with a depot or mappings named, a device of the current operator is told it, whoever the ask was put to. Any other device is told `""`, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. The socket tells the whole card |
| `decided[].run.does` | array of string | what the command changes, in the yard's words |
| `decided[].run.back` | string | the command that takes the change back |
| `decided[].run.back_by` | string, may be absent | who may run it, as `Bead` says it; absent where that is anyone |
| `decided[].setup` | object, may be absent | the setup a setup ask proposes, as `Bead` says it; absent on any other ask |
| `decided[].setup.pack` | string | the embedded pack to set up, of kind source |
| `decided[].setup.base` | string, may be absent | the selected region base name; absent when calls go to the declaration's own base |
| `decided[].setup.label` | string, may be absent | the build the pack is embedded in, as the card shows it |
| `decided[].setup.account` | array of string | the names to type unseen beside the preset pick |
| `decided[].setup.account_file` | string, may be absent | the file the value goes to, on the yard's machine. A device of the current operator is told it, whoever the ask was put to. Any other device is told the card with the field absent, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. The socket tells the whole card |
| `decided[].setup.given` | bool, may be absent | on an update, where the yard already holds the source the pack brings: `true` when its account file already holds every name of `setup.account`, so that a bare preset pick keeps it; absent on a first setup, where nothing stands yet, and where the file lacks a name (`setup.missing`) |
| `decided[].setup.missing` | array of string, may be absent | on an update, the names of `setup.account` the account file that stands does not set, where the pack newly needs them (every name where the file is not there): the answer types exactly these, or takes a candidate; absent on a first setup and where the file holds every name |
| `decided[].setup.candidates` | array, may be absent | yard sources whose account the card offers to reuse, as `Bead` tells them. A device of the current operator is told them, whoever the ask was put to. Any other device is told the card as one with none, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. The socket tells the whole card |
| `decided[].setup.candidates[].source` | string | the source whose account the card offers |
| `decided[].setup.candidates[].file` | string | the file holding the shared account inputs; told to a device of the current operator alone |
| `decided[].setup.candidates[].values` | object | the shared account input names and values the file holds; told to a device of the current operator alone |
| `decided[].setup.does` | array of string | what approving changes and what is there now, in the yard's words. The account sentence, which names the file the value goes to and, where the card lists candidates, each by source, values and file, is told to a device of the current operator alone. Any other device is told the account sentence of a card with none and naming no file |
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
| `decided[].depot_push` | object, may be absent | the yard's depot-push card; absent on any other ask, and on a device that is not the current operator's. The socket tells the whole card |
| `decided[].depot_push.depot` | string | the depot whose push is changed |
| `decided[].depot_push.off` | bool, may be absent | true when the card turns push off; absent when it turns push on |
| `decided[].depot_push.does` | array of string | what approving changes and what stands now, in the yard's words |
| `decided[].depot_push.back` | string | the filing that takes the change back |
| `decided[].depot_push.back_by` | string, may be absent | who may file `back`, in words that stand before `can take it back`; absent where that is anyone |
| `decided[].given` | array of string, may be absent | the names a secret ask was given values for, or the account names a setup ask was approved with, which reads "gave `<names>`"; absent on any other ask |
| `decided[].ran` | object, may be absent | the command the yard ran when a run ask was approved, which reads "approved, ran: `<command>`"; the setup a setup ask approved, which reads "approved, being carried out", "approved, ran" or "approved, failed" with the preset and the names given; or the carrying out of a depot-push card, which reads "approved, to be carried out", "approved, ran" or "approved, failed". Absent on any other ask and on an ask that was declined or put aside. A depot-push card and its `ran` are told only to a device of the current operator; other devices are told neither |
| `decided[].ran.argv` | array of string | the command that ran, in words: the one the ask proposed; on a setup ask, `setup` with the pack's name. On a run ask that set a setting or added a source, a device of the current operator is told them, whoever the ask was put to. Any other device is told `[]`, even the device of a user the ask was put to while they were the operator, and so is every device of a yard with no operator. A depot-push card and its `ran` are absent for devices of nonoperators. The socket tells the whole result |
| `decided[].ran.result` | string | what the command printed; on a setup ask not yet carried out or a depot-push ask, `approved, to be carried out`, and afterwards what the carrying out printed. Its line `the account <names> is set in <file>` names the file the account is set in, which is the candidate's when the approval took one, and the words of a carrying out that failed can name it too, and the candidate's source: both are told to a device of the current operator alone, whoever the ask was put to ([over the network](#over-the-network)). A depot-push result is told to a device of the current operator alone too. Any other device is told `the account <names> is set` for that line and, for a failure, that it failed and no more; so is every device of a yard with no operator. The socket tells the whole result |
| `decided[].ran.pending_reason` | string, may be absent | while an approved member invite or removal is still owed after a signed 429 or 503, why the edge refused its report, in the edge's words. It is cleared when the report lands or the carrying out fails, and is told to a device of the current operator alone; other devices are told none |
| `decided[].state` | string | `answered`, `put aside`, `declined` or `taken back`. A later build may add a state: one a client does not know is still not open, and is shown by its word |
| `decided[].option` | number, may be absent | the picked option's number; absent on a words answer, an aside ask, a declined one and one taken back |
| `decided[].option_label` | string, may be absent | the picked option's label; absent with `option` |
| `decided[].words` | string, may be absent | what went with the pick, the words answer, or why the question is declined or taken back; for a question another yard asked that ended there, how it ended; for one put aside there, `put aside in <peer>'s yard until <when>`; absent when there are none |
| `decided[].by` | string, may be absent | who took the ask back, its asker or the operator: it reads "taken back by `<by>`: `<words>`", and is no answer of the person. Absent on every other state, and on a question another yard took back, whose `words` say where |
| `decided[].return_at` | time, may be absent | when a put-aside ask comes back, here or in the yard that asked it; absent otherwise |

Read it again on every `noted` event: a question asked, answered, put aside, declined or taken back, and the asker's next note, all arrive as `noted`. A put-aside ask also comes back on its own after 24 hours, with no event: its decided entry carries `return_at`, so read again then. An answer to a question another yard asked that did not land arrives as `noted` by `yard`: the question is open again, or decided with how it ended there, and the person has mail from `yard` that says which.

### `Bead`

One bead whole: body, stage, labels, notes, its asks with their options, recommendation and state, its stage moves, who works it now, and for each about-bead its id, title and stage. On a `bead@peer` ref, a question another yard asked, the view is synthesized from what that yard carried: its title, stage, topic and labels, its body under one first line saying the bead lives in the other yard and at which stage, no notes, the asks whole with their state as this yard knows it, and the about-beads as carried.

| argument | type | |
|---|---|---|
| `bead` | string | the bead: a handle, or `bead@peer` for a question another yard asked |
| `viewer` | string | who reads it: what they may not see reads as missing; left out, the yard itself, which sees every bead |

```
POST /call/Bead
{"bead":"yd-links","viewer":"jo"}

{
  "result": {
    "id": "yd-links",
    "title": "Move the links",
    "body": "The links file declares the new source.",
    "stage": "review",
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
        "state": "open"
      },
      {
        "ask": 60,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Turn push off for the yard depot?",
        "context": ["The linked depot is ready."],
        "options": [],
        "state": "open",
        "depot_push": {
          "depot": "yard",
          "off": true,
          "does": ["turns push to github.com off for depot yard (origin https://github.com/ada/yard.git).", "takes YARDR_PUSH=origin out of yard-assembly's cmd; the rest of the cmd stands untouched, YARDR_LAND as it was.", "takes the pack's helper line for https://github.com out of depot yard's own git config with git -C yard config --unset credential.https://github.com.helper; no other depot's config changes.", "landings of the depot then land here and push nowhere."],
          "back": "yardr bead ask yd-links \"\u003cquestion\u003e\" --depot-push yard",
          "back_by": "only the operator"
        },
        "weight": [
          {
            "class": "outside",
            "line": "lets the yard change something outside it: landings of depot yard push to its origin no more"
          }
        ]
      },
      {
        "ask": 63,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Can the note wait?",
        "context": ["It can be read tomorrow."],
        "options": [],
        "state": "put aside",
        "return_at": "2026-10-08T09:30:00Z"
      }
    ],
    "rounds": {
      "critique": 1,
      "draft": 0,
      "new": 0,
      "review": 0
    },
    "visited": ["new", "draft", "critique", "review"],
    "moves": [
      {
        "from": "new",
        "to": "draft",
        "at": "2026-10-08T09:30:00Z"
      },
      {
        "from": "draft",
        "to": "critique",
        "at": "2026-10-08T09:30:00Z",
        "outcome": "done"
      },
      {
        "from": "critique",
        "to": "draft",
        "at": "2026-10-08T09:30:00Z",
        "outcome": "changes"
      },
      {
        "from": "draft",
        "to": "critique",
        "at": "2026-10-08T09:30:00Z",
        "outcome": "done"
      },
      {
        "from": "critique",
        "to": "review",
        "at": "2026-10-08T09:30:00Z",
        "outcome": "changes",
        "instead_of": "draft"
      }
    ],
    "now": {
      "session": {
        "group": "builders",
        "kind": "claude",
        "model": "opus",
        "state": "running",
        "started_at": "2026-10-08T09:30:00Z"
      },
      "activity": {
        "kind": "noted",
        "at": "2026-10-08T09:30:00Z"
      }
    }
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
        "given": ["BOOKS_TOKEN"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: BOOKS_TOKEN, for the source books (https://books.example.com)"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_UPDATE_TOKEN, for the source crm (https://crm.example.com)"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_TOKEN, for the source crm (https://crm.example.com)"
          }
        ]
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
        "given": ["PAGER_USER"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: PAGER_USER, jo's own address in the source pager (https://pager.example.com); jo is told through pager once it is given and yardr user set jo notify=pager is run"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: PHONE_USER, jo's own address in the source phone (https://phone.example.com); jo is told through phone once it is given and yardr user set jo notify=phone is run"
          }
        ]
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
        },
        "weight": [
          {
            "class": "trust",
            "line": "widens who the yard listens to: the link with north takes 100 mails and asks an hour from it, 30 now"
          }
        ]
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
        },
        "weight": [
          {
            "class": "trust",
            "line": "widens who the yard listens to: the link with north takes 300 mails and asks an hour from it, 100 now"
          }
        ]
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
        "given": ["INPUT"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: INPUT, for the waiting login claude-example1"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: INPUT, for the waiting login claude-example2"
          }
        ]
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
        "given": ["CRM_TOKEN"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_UPDATE_TOKEN, for crew member yardmaster in the source crm (https://crm.example.com); the env_file line is the one written, and it takes effect when yardmaster next starts (rotation or yardr crew restart yardmaster)"
          }
        ]
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
        "given": ["CRM_UPDATE_TOKEN"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_UPDATE_TOKEN, jo's own credential for the source crm (https://crm.example.com), read as jo when a call is made --as jo"
          },
          {
            "class": "as-user",
            "line": "lets calls or words count as a person's: a call of the source crm made --as jo reads it as jo's own"
          }
        ]
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: CRM_TOKEN, jo's own credential for the source crm (https://crm.example.com), read as jo when a call is made --as jo"
          },
          {
            "class": "as-user",
            "line": "lets calls or words count as a person's: a call of the source crm made --as jo reads it as jo's own"
          }
        ]
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
        "concealed": false,
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: GOG_ACCOUNT, for the source gog (https://gog.example.com)"
          }
        ]
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
        "given": ["GOG_ACCOUNT"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: GOG_ACCOUNT, for the source gog (https://gog.example.com)"
          }
        ]
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
            "consequence": "may make every read call of the source courier (https://eu.courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source courier (https://eu.courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source. may make the call put (POST /it) of the source courier (https://eu.courier.example.com), which changes something there."
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
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://eu.courier.example.com/api (base eu). Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to group builders and crew yardmaster — pick one:"],
          "back": "yardr source rm courier; yardr source revoke courier \u003cwhat\u003e --group builders --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "courier",
              "text": "name: courier\nbase: https://courier.example.com/api\nbases:\n  us: https://courier.example.com/api\n  eu: https://eu.courier.example.com/api\ncredential:\n  env: COURIER_TOKEN\n  header: \"Authorization: Bearer {credential}\"\n  mint: [COURIER_ACCOUNT]\n  mint_command: [courier-mint]\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n",
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
          ],
          "base": "eu"
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account COURIER_ACCOUNT of pack courier"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group builders and crew yardmaster call put of courier (an update)"
          },
          {
            "class": "code",
            "line": "brings a program the yard will run: the command file courier-mint of courier (new)"
          }
        ]
      },
      {
        "ask": 45,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Set up the courier source for the night crew?",
        "context": ["The night run pages through it."],
        "options": [
          {
            "label": "read",
            "consequence": "may make every read call of the source courier (https://eu.courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source courier (https://eu.courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source. may make the call put (POST /it) of the source courier (https://eu.courier.example.com), which changes something there."
          },
          {
            "label": "You decide",
            "consequence": "no preset is recommended, so pick one of the presets above"
          }
        ],
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
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://eu.courier.example.com/api (base eu). Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to crew yardmaster — pick one:"],
          "back": "yardr source rm courier; yardr source revoke courier \u003cwhat\u003e --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "courier",
              "text": "name: courier\nbase: https://courier.example.com/api\nbases:\n  us: https://courier.example.com/api\n  eu: https://eu.courier.example.com/api\ncredential:\n  env: COURIER_TOKEN\n  header: \"Authorization: Bearer {credential}\"\n  mint: [COURIER_ACCOUNT]\n  mint_command: [courier-mint]\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n",
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
              "crew": "yardmaster"
            }
          ],
          "base": "eu"
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account COURIER_ACCOUNT of pack courier"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets crew yardmaster call put of courier (an update)"
          },
          {
            "class": "code",
            "line": "brings a program the yard will run: the command file courier-mint of courier (new)"
          }
        ]
      },
      {
        "ask": 46,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Update the ledger source?",
        "context": ["The build brings the ledger's new base."],
        "options": [
          {
            "label": "read",
            "consequence": "may make every read call of the source ledger (https://ledger.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source (new)."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source ledger (https://ledger.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source (new). may make the call put (POST /it) of the source ledger (https://ledger.example.com), which changes something there (new)."
          },
          {
            "label": "You decide",
            "consequence": "no preset is recommended, so pick one of the presets above"
          }
        ],
        "state": "open",
        "setup": {
          "pack": "ledger",
          "label": "embedded in yardr doctest",
          "account": ["LEDGER_TOKEN", "LEDGER_UPDATE_TOKEN"],
          "account_file": "/home/jo/.yardr/sources/ledger.env",
          "given": true,
          "does": ["pack ledger (embedded in yardr doctest), of kind source, into the yard: it replaces 1 source and copies no command files, and changes nothing else. It makes no hook and sets no credential's file.", "source ledger: its calls go to https://ledger.example.com/v2. Its credential is the variable LEDGER_TOKEN (the name, never the value), with LEDGER_UPDATE_TOKEN for updates, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no mint.", "it would replace the source ledger the yard has under that name.", "source ledger base: \"https://ledger.example.com/api\" -\u003e \"https://ledger.example.com/v2\".", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command.", "the account LEDGER_TOKEN and LEDGER_UPDATE_TOKEN: given, in /home/jo/.yardr/sources/ledger.env. Type another unseen to change it, or approve keeping it with a bare preset pick (written to /home/jo/.yardr/sources/ledger.env, named in the answer, never the value).", "grants to group builders and crew yardmaster — pick one:"],
          "back": "yardr source rm ledger; yardr source revoke ledger \u003cwhat\u003e --group builders --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "ledger",
              "text": "name: ledger\nbase: https://ledger.example.com/v2\ncredential: { env: LEDGER_TOKEN, update_env: LEDGER_UPDATE_TOKEN, header: \"Authorization: Bearer {credential}\" }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n"
            }
          ],
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "ledger",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "ledger",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account LEDGER_TOKEN and LEDGER_UPDATE_TOKEN of pack ledger"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group builders and crew yardmaster call put of ledger (an update)"
          }
        ]
      },
      {
        "ask": 47,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Update the parcel source?",
        "context": ["The build gives the parcel's put call a token of its own."],
        "options": [
          {
            "label": "read",
            "consequence": "may make every read call of the source parcel (https://parcel.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source (new)."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source parcel (https://parcel.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source (new). may make the call put (POST /it) of the source parcel (https://parcel.example.com), which changes something there (new), waiting on the preset all."
          },
          {
            "label": "You decide",
            "consequence": "no preset is recommended, so pick one of the presets above"
          }
        ],
        "state": "open",
        "setup": {
          "pack": "parcel",
          "label": "embedded in yardr doctest",
          "account": ["PARCEL_TOKEN", "PARCEL_UPDATE_TOKEN"],
          "account_file": "/home/jo/.yardr/sources/parcel.env",
          "missing": ["PARCEL_UPDATE_TOKEN"],
          "does": ["pack parcel (embedded in yardr doctest), of kind source, into the yard: it replaces 1 source and copies no command files, and changes nothing else. It makes no hook and sets no credential's file.", "source parcel: its calls go to https://parcel.example.com/api. Its credential is the variable PARCEL_TOKEN (the name, never the value), with PARCEL_UPDATE_TOKEN for updates, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no mint.", "it would replace the source parcel the yard has under that name.", "source parcel credential: {\"env\":\"PARCEL_TOKEN\",\"header\":\"Authorization: Bearer {credential}\"} -\u003e {\"env\":\"PARCEL_TOKEN\",\"header\":\"Authorization: Bearer {credential}\",\"update_env\":\"PARCEL_UPDATE_TOKEN\"}.", "source parcel calls: [{\"name\":\"ping\",\"kind\":\"read\",\"request\":{\"method\":\"GET\",\"path\":\"/me\"},\"answer\":{\"max\":65536}}] -\u003e [{\"name\":\"ping\",\"kind\":\"read\",\"request\":{\"method\":\"GET\",\"path\":\"/me\"},\"answer\":{\"max\":65536}},{\"name\":\"put\",\"kind\":\"update\",\"request\":{\"method\":\"POST\",\"path\":\"/it\"},\"answer\":{\"max\":65536}}].", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command.", "the account PARCEL_TOKEN and PARCEL_UPDATE_TOKEN: given, in /home/jo/.yardr/sources/parcel.env, but missing PARCEL_UPDATE_TOKEN: type PARCEL_UPDATE_TOKEN unseen (written to /home/jo/.yardr/sources/parcel.env, named in the answer, never the value).", "grants to group builders and crew yardmaster — pick one:"],
          "back": "yardr source rm parcel; yardr source revoke parcel \u003cwhat\u003e --group builders --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "parcel",
              "text": "name: parcel\nbase: https://parcel.example.com/api\ncredential: { env: PARCEL_TOKEN, update_env: PARCEL_UPDATE_TOKEN, header: \"Authorization: Bearer {credential}\" }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n"
            }
          ],
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "parcel",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "parcel",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account PARCEL_TOKEN and PARCEL_UPDATE_TOKEN of pack parcel"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group builders and crew yardmaster call put of parcel (an update)"
          }
        ]
      },
      {
        "ask": 48,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Set up the courier source for the elders too?",
        "context": ["They read the same pages."],
        "options": [
          {
            "label": "read",
            "consequence": "may make every read call of the source courier (https://eu.courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source courier (https://eu.courier.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source. may make the call put (POST /it) of the source courier (https://eu.courier.example.com), which changes something there."
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
          "does": ["pack courier (embedded in yardr doctest), of kind source, into the yard: it adds 1 source and copies 1 command file (1 command), and changes nothing else. It makes no hook and sets no credential's file.", "source courier: its calls go to https://eu.courier.example.com/api (base eu). Its credential is the variable COURIER_TOKEN (the name, never the value), minted from COURIER_ACCOUNT by the declaration's own mint command [courier-mint] (command file courier-mint, copied to \u003chome\u003e/sources/courier/courier-mint: sha256 275239824e00e61b0a220e61a41791c7e9b4bd726f8b0c27077a338f8131c9dc, mode 0700), run with the mint's inputs and the operator's local logins where the source's mint_command is unset: one the operator sets wins over it, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no second credential for updates.", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command but its mint command, said with its credential above.", "the account COURIER_ACCOUNT: already given for courier-existing (COURIER_ACCOUNT=jo@example.test, in /home/jo/.yardr/sources/courier-existing.env). Take it with --account courier-existing, or type another unseen (written to /home/jo/.yardr/sources/courier.env, named in the answer, never the value).", "grants to group elders and crew yardmaster — pick one:"],
          "back": "yardr source rm courier; yardr source revoke courier \u003cwhat\u003e --group elders --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "courier",
              "text": "name: courier\nbase: https://courier.example.com/api\nbases:\n  us: https://courier.example.com/api\n  eu: https://eu.courier.example.com/api\ncredential:\n  env: COURIER_TOKEN\n  header: \"Authorization: Bearer {credential}\"\n  mint: [COURIER_ACCOUNT]\n  mint_command: [courier-mint]\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n",
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
          ],
          "base": "eu"
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account COURIER_ACCOUNT of pack courier"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group elders and crew yardmaster call put of courier (an update)"
          },
          {
            "class": "code",
            "line": "brings a program the yard will run: the command file courier-mint of courier (new)"
          }
        ]
      },
      {
        "ask": 50,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Update the ledger source for the elders too?",
        "context": ["They read the same pages."],
        "options": [
          {
            "label": "read",
            "consequence": "may make every read call of the source ledger (https://ledger.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source (new)."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source ledger (https://ledger.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source (new). may make the call put (POST /it) of the source ledger (https://ledger.example.com), which changes something there (new)."
          },
          {
            "label": "You decide",
            "consequence": "no preset is recommended, so pick one of the presets above"
          }
        ],
        "state": "declined",
        "words": "the builders' card covers it",
        "setup": {
          "pack": "ledger",
          "label": "embedded in yardr doctest",
          "account": ["LEDGER_TOKEN", "LEDGER_UPDATE_TOKEN"],
          "account_file": "/home/jo/.yardr/sources/ledger.env",
          "given": true,
          "does": ["pack ledger (embedded in yardr doctest), of kind source, into the yard: it replaces 1 source and copies no command files, and changes nothing else. It makes no hook and sets no credential's file.", "source ledger: its calls go to https://ledger.example.com/v2. Its credential is the variable LEDGER_TOKEN (the name, never the value), with LEDGER_UPDATE_TOKEN for updates, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no mint.", "it would replace the source ledger the yard has under that name.", "source ledger base: \"https://ledger.example.com/api\" -\u003e \"https://ledger.example.com/v2\".", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command.", "the account LEDGER_TOKEN and LEDGER_UPDATE_TOKEN: given, in /home/jo/.yardr/sources/ledger.env. Type another unseen to change it, or approve keeping it with a bare preset pick (written to /home/jo/.yardr/sources/ledger.env, named in the answer, never the value).", "grants to group elders and crew yardmaster — pick one:"],
          "back": "yardr source rm ledger; yardr source revoke ledger \u003cwhat\u003e --group elders --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "ledger",
              "text": "name: ledger\nbase: https://ledger.example.com/v2\ncredential: { env: LEDGER_TOKEN, update_env: LEDGER_UPDATE_TOKEN, header: \"Authorization: Bearer {credential}\" }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n"
            }
          ],
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "ledger",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "ledger",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account LEDGER_TOKEN and LEDGER_UPDATE_TOKEN of pack ledger"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group elders and crew yardmaster call put of ledger (an update)"
          }
        ]
      },
      {
        "ask": 52,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Update the parcel source for the elders too?",
        "context": ["They read the same pages."],
        "options": [
          {
            "label": "read",
            "consequence": "may make every read call of the source parcel (https://parcel.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source (new)."
          },
          {
            "label": "all",
            "consequence": "may make every read call of the source parcel (https://parcel.example.com): today that is ping (GET /me), and it covers any read call the operator later gives the source (new). may make the call put (POST /it) of the source parcel (https://parcel.example.com), which changes something there (new), waiting on the preset all."
          },
          {
            "label": "You decide",
            "consequence": "no preset is recommended, so pick one of the presets above"
          }
        ],
        "state": "declined",
        "words": "the builders' card covers it",
        "setup": {
          "pack": "parcel",
          "label": "embedded in yardr doctest",
          "account": ["PARCEL_TOKEN", "PARCEL_UPDATE_TOKEN"],
          "account_file": "/home/jo/.yardr/sources/parcel.env",
          "missing": ["PARCEL_UPDATE_TOKEN"],
          "does": ["pack parcel (embedded in yardr doctest), of kind source, into the yard: it replaces 1 source and copies no command files, and changes nothing else. It makes no hook and sets no credential's file.", "source parcel: its calls go to https://parcel.example.com/api. Its credential is the variable PARCEL_TOKEN (the name, never the value), with PARCEL_UPDATE_TOKEN for updates, sent in the header \"Authorization: Bearer {credential}\" on every call. It has no mint.", "it would replace the source parcel the yard has under that name.", "source parcel credential: {\"env\":\"PARCEL_TOKEN\",\"header\":\"Authorization: Bearer {credential}\"} -\u003e {\"env\":\"PARCEL_TOKEN\",\"header\":\"Authorization: Bearer {credential}\",\"update_env\":\"PARCEL_UPDATE_TOKEN\"}.", "source parcel calls: [{\"name\":\"ping\",\"kind\":\"read\",\"request\":{\"method\":\"GET\",\"path\":\"/me\"},\"answer\":{\"max\":65536}}] -\u003e [{\"name\":\"ping\",\"kind\":\"read\",\"request\":{\"method\":\"GET\",\"path\":\"/me\"},\"answer\":{\"max\":65536}},{\"name\":\"put\",\"kind\":\"update\",\"request\":{\"method\":\"POST\",\"path\":\"/it\"},\"answer\":{\"max\":65536}}].", "call ping (read): GET /me. It takes no parameter, and sends no query.", "call ping sends no body. Its answer is returned as it comes, at most 65536 bytes.", "call put (update): POST /it. It takes no parameter, and sends no query.", "call put sends no body. Its answer is returned as it comes, at most 65536 bytes.", "The source names no check call and no idempotency header.", "it can tell no user.", "it hears no deliveries.", "it names no limits command.", "it speaks in no threads.", "its issues are no bead's link.", "its identity keys keep their case.", "it sends no hook deliveries into the yard.", "it sends no pull-request or CI events.", "it names no command.", "the account PARCEL_TOKEN and PARCEL_UPDATE_TOKEN: given, in /home/jo/.yardr/sources/parcel.env, but missing PARCEL_UPDATE_TOKEN: type PARCEL_UPDATE_TOKEN unseen (written to /home/jo/.yardr/sources/parcel.env, named in the answer, never the value).", "grants to group elders and crew yardmaster — pick one:"],
          "back": "yardr source rm parcel; yardr source revoke parcel \u003cwhat\u003e --group elders --crew yardmaster",
          "back_by": "the operator",
          "carried": [
            {
              "name": "parcel",
              "text": "name: parcel\nbase: https://parcel.example.com/api\ncredential: { env: PARCEL_TOKEN, update_env: PARCEL_UPDATE_TOKEN, header: \"Authorization: Bearer {credential}\" }\ncalls:\n  ping: { kind: read, request: { method: GET, path: /me } }\n  put: { kind: update, request: { method: POST, path: /it } }\n"
            }
          ],
          "presets": [
            {
              "name": "read",
              "grants": [
                {
                  "source": "parcel",
                  "calls": ["read"]
                }
              ]
            },
            {
              "name": "all",
              "grants": [
                {
                  "source": "parcel",
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
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: the account PARCEL_TOKEN and PARCEL_UPDATE_TOKEN of pack parcel"
          },
          {
            "class": "outside",
            "line": "lets the yard change something outside it: the preset all lets group elders and crew yardmaster call put of parcel (an update)"
          }
        ]
      },
      {
        "ask": 54,
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
          "line": "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start"
        },
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start"
          }
        ]
      },
      {
        "ask": 55,
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
          "line": "a secret: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start"
        },
        "given": ["ANTHROPIC_API_KEY"],
        "weight": [
          {
            "class": "credential",
            "line": "gives a credential or an address to the yard: ANTHROPIC_API_KEY, the API key Claude Code runs with, asked for crew member yardmaster; it is read by every Claude Code session that burns the operator's login (the crew, groups without login_for, the operator's conversations), from its next start"
          }
        ]
      },
      {
        "ask": 57,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "May the builders write notes in the CRM?",
        "context": ["The import leaves a note on each account it touches."],
        "options": [],
        "state": "answered",
        "run": {
          "argv": ["source", "grant", "crm", "put", "--group", "builders"],
          "command": "yardr source grant crm put --group builders",
          "does": ["the group builders (herdr, claude), and so each session it starts, may make the call put (POST /it) of the source crm (https://crm.example.com), with the credential the yard holds for it, which changes something there. It is granted read (every read call) of crm now."],
          "back": "yardr source revoke crm put --group builders"
        },
        "ran": {
          "argv": ["source", "grant", "crm", "put", "--group", "builders"],
          "result": "group builders may call crm: put (update), read"
        },
        "weight": [
          {
            "class": "outside",
            "line": "lets the yard change something outside it: group builders may call put of crm (an update)"
          }
        ]
      },
      {
        "ask": 59,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Hook the bot's messages up?",
        "context": ["The bot's stream needs a hook to land in."],
        "options": [],
        "state": "open",
        "run": {
          "argv": ["hook", "add", "bot", "--source", "bot"],
          "command": "yardr hook add bot --source bot",
          "does": ["it adds the hook bot: POST /hooks/bot takes deliveries from the source bot (https://bot.example.com).", "what it sends in: deliveries are verified by the header X-Test-Secret holding the hook's secret itself (scheme secret); it sends no delivery id; it sends no timestamp; bodies are unique; the command [bot/normalize] turns a verified body into the envelope, handed no credential; its events may carry the fields chat; the yard registers its webhook with setwebhook (POST /setWebhook), filling url with the public URL and secret_token with the hook's secret.", "the yard makes the signing secret for bot and keeps it in /home/jo/.yardr/hooks/bot.secret (mode 0600); the hook names that file, and the value is in no note, event, answer or card. No secret is typed.", "the approval gives project in the clear, which the yard sends to the register call with the URL and the secret.", "it will call POST /setWebhook at https://bot.example.com/api to register https://yard.example/hooks/bot."],
          "back": "yardr hook rm bot",
          "back_by": "only the operator",
          "ask": ["project"]
        },
        "weight": [
          {
            "class": "trust",
            "line": "widens who the yard listens to: POST /hooks/bot takes deliveries from outside the yard for the source bot"
          }
        ]
      },
      {
        "ask": 65,
        "to": "jo",
        "asker": "yardmaster",
        "since": "2026-10-08T09:30:00Z",
        "question": "Invite Rhea to the yard?",
        "context": ["Rhea works with the builders."],
        "options": [],
        "state": "answered",
        "run": {
          "argv": ["user", "invite", "rhea", "--email", "rhea@example.net"],
          "command": "yardr user invite rhea --email rhea@example.net",
          "does": ["adds the user rhea with the role member, and maps rhea@example.net to rhea in web_jwt_users: signed in at the edge as rhea@example.net, they act as rhea.", "reports the invite to the edge for the yard jo/edgeyard (member add): until the report lands the card reads as not yet at the edge, and nobody is admitted meanwhile.", "they set a password from the mail the edge sends to this email, then sign in; nothing the operator holds admits them."],
          "back": "yardr user uninvite rhea",
          "back_by": "only the operator"
        },
        "ran": {
          "argv": ["user", "invite", "rhea", "--email", "rhea@example.net"],
          "result": "approved, to be carried out",
          "pending_reason": "the edge refused this yard: mail limit reached (429)"
        },
        "weight": [
          {
            "class": "trust",
            "line": "widens who the yard listens to: a new user, rhea, whose answers and approvals the yard takes"
          }
        ]
      }
    ],
    "rounds": {
      "new": 0
    },
    "visited": ["new"],
    "now": {
      "activity": {
        "kind": "noted",
        "at": "2026-10-08T09:30:00Z"
      }
    }
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
| `rounds` | object, may be absent | the stages the bead has reached, each with the rounds counted for it: how many times an outcome brought the bead back to a stage it had been in (a reviewer's `changes`, a failed landing), a same-stage outcome included. A stage reached for the first time reads `0`, and a move by hand (`yardr bead advance --to`) adds none. Absent on a bead from before the record, and on a `bead@peer` |
| `visited` | array of string, may be absent | the stages the bead reached, first visit order, at most 64. Kept as history for a bead from before rounds were recorded. Absent on a `bead@peer` |
| `moves` | array, may be absent | the bead's recorded stage moves still in the event log, oldest first, at most 100: the stages each left and entered and when, with the outcome that moved it where one did. Absent when none remain, and on a `bead@peer` |
| `moves[].from` | string | the stage the move left |
| `moves[].to` | string | the stage the move entered |
| `moves[].at` | time | when the move was made |
| `moves[].outcome` | string, may be absent | the outcome that moved the bead, as the flow names it; absent on a move by hand |
| `moves[].instead_of` | string, may be absent | the stage a round limit kept the bead from, which `to` stands in for; absent where no limit sent the bead on |
| `now` | object, may be absent | the live session, if any, and the latest event still in the log. Absent when neither remains, and on a `bead@peer` |
| `now.session` | object, may be absent | the live session working the bead; absent where none is |
| `now.session.group` | string | the group the session works for |
| `now.session.kind` | string | the harness kind the session started with |
| `now.session.model` | string | the model the session started with |
| `now.session.state` | string | the session's state: `starting` or `running` |
| `now.session.started_at` | time | when the session started |
| `now.activity` | object, may be absent | the bead's latest recorded activity: its newest event still in the log, with its bare kind and time and no content. Absent when no event remains |
| `now.activity.kind` | string | the kind of the bead's newest event |
| `now.activity.at` | time | when the bead's newest event was written |
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
| `asks[].options` | array | the options in order, `you decide` last, except on a card the yard puts for a stage, which offers its own options alone (one put before still ends in it); none wants words |
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
| `asks[].words` | string, may be absent | what went with the pick, the words answer, or why the question is declined or taken back; on a question another yard asked, how it ended or stands put aside there; absent when there are none |
| `asks[].by` | string, may be absent | who took the ask back, its asker or the operator; absent on every other state |
| `asks[].return_at` | time, may be absent | when a put-aside ask comes back, here or in the yard that asked it; absent otherwise |
| `asks[].picks_before` | number, may be absent | how many picks there were before the current one, so a client can say "changed"; absent when there were none |
| `asks[].weight` | array, may be absent | the card's weight as the yard weighed it when it wrote the card: one entry a class, each a way approving it widens what the yard does ([weighty cards](#weighty-cards)). Absent on a routine card |
| `asks[].weight[].class` | string | `credential`, `as-user`, `outside`, `code`, `trust` or `reads`. A later build may add a class: draw one a client does not know as weighty, with its line |
| `asks[].weight[].line` | string | the yard's line for the class with what this card does, as a terminal card prints it after `WEIGHTY · <class>: ` |
| `asks[].secret` | object, may be absent | what a secret ask asks for; absent on any other ask. Such an ask has no options and takes no words: show it with a field that does not echo, and answer it with [`GiveSecret`](#for-the-operator-alone). `AnswerAsk` refuses a pick and words on it, and `DeclineAsk` words |
| `asks[].secret.source` | string | the declared source the secret is the credential of; with `user`, `crew` or `as`, the source the address or credential is in; empty on a login or harness-key ask |
| `asks[].secret.env` | array of string | the names to ask the person a value for, each a variable of the source's credential; with `user`, the one variable the source reads of a user's file; with `crew`, variables of the source's credential; with `as`, the names a call as that user mints or uses; on a login ask, the one name the input is asked for; on a harness-key ask, variables of the kind's `login_env` |
| `asks[].secret.user` | string, may be absent | the user whose own address in the source is asked for (`yardr bead ask --secret <source> --for-user <user>`): the value goes to a file of that user's, and is theirs to give or the operator's. Absent on an ask for the source's credential. A client needs nothing new for it: the field, the call and `line` are the same |
| `asks[].secret.crew` | string, may be absent | the crew member whose own credential in the source is asked for, or on a harness-key ask the crew member at whose login screen it is asked; the value is the operator's to give. A source credential goes to a file of that crew member's and takes effect when the crew member next starts; a harness key goes to the operator's key file of the kind, which every session on the operator's login reads, and restarts the sessions of that kind on that login waiting at the login screen when given. Absent on any other secret ask |
| `asks[].secret.as` | string, may be absent | with `login`, the user whose login that is; otherwise the user whose own credential in the source is asked for (`yardr bead ask --secret <source> --as <user>`): the value goes to a file of that user's, and is theirs to give or the operator's. Absent on any other secret ask. A client needs nothing new for it: the field, the call and `line` are the same |
| `asks[].secret.login` | string, may be absent | the waiting login the value goes to (`yardr bead ask --secret-login <login>`): the reference the login's leader printed in its input need, handed the value and kept nowhere; with `as`, the login is that user's own and theirs to give. Absent on any other secret ask. A client needs nothing new for it: the field, the call and `line` are the same |
| `asks[].secret.harness` | string, may be absent | the harness kind whose API key is asked for; absent on any other secret ask |
| `asks[].secret.line` | string | what the card says is asked for, in the yard's words and never the asker's: the names, the source, and where the source's calls go; with `user`, whose address it is and the command that tells them through the source; with `crew`, whose credential it is, that the env_file line is the one written, and when it takes effect; with `as`, whose credential it is and that a call made as them reads it; with `login`, the waiting login the value goes to, and whose it is where `as` names a user; with `harness`, whose API key it is and when it takes effect. Show it as it is |
| `asks[].concealed` | bool, may be absent | whether a secret ask's value is drawn hidden: false where the source marks every asked name as no secret, which is shown with a field that echoes; absent (concealed) on any other ask |
| `asks[].given` | array of string, may be absent | the names a secret ask was given values for, or the account names a setup ask was approved with, which reads "gave `<names>`"; absent on any other ask and until it is given |
| `asks[].depot_push` | object, may be absent | the yard's depot-push card for turning one depot's push to its HTTPS origin on or off. Absent on any other ask, and on a device that is not the current operator's; the socket tells the whole card. It has no options and takes no words: `AnswerAsk` refuses a pick and words on it, and it is approved with [`ApproveAsk`](#for-the-operator-alone), by the operator, at the yard or from a device of the operator. Approval carries the change out after the call; it never runs `git push` |
| `asks[].depot_push.depot` | string | the depot whose push is changed |
| `asks[].depot_push.off` | bool, may be absent | true when the card turns push off; absent when it turns push on |
| `asks[].depot_push.does` | array of string | what approving changes and what stands now, in the yard's words |
| `asks[].depot_push.back` | string | the filing that takes the change back |
| `asks[].depot_push.back_by` | string, may be absent | who may file `back`, in words that stand before `can take it back`; absent where that is anyone |
| `asks[].run` | object, may be absent | the command a run ask proposes: one yardr command that is the operator's to give, which the yard runs when the operator approves the ask. Absent on any other ask. Such an ask has no options and takes no words: `AnswerAsk` refuses a pick and words on it, `DeferAsk` and `DeclineAsk` take it as any ask, and it is approved with [`ApproveAsk`](#for-the-operator-alone), by the operator: at the yard (`yardr bead answer <bead> <ask> --approve`, with `--value <name>=<value>` where the card asks for values in the clear), or from a device of the operator. A strong card ([weighty cards](#weighty-cards)) is approved from a device as any weighty card is; the bead page approves it only on a hosted yard, under a fresh sign-in. A command that is a user's own (`subscribe <user> yardr`, `user set <user> tell=<parts>`, `user key <user> <source>`) is approved at the yard by the one it was asked of, that user or the operator. All of `run` is the yard's, made when the ask was written, and none of it the asker's: show it apart from `question` and `context`, which are the asker's words, and draw those as plain text |
| `asks[].run.argv` | array of string | the command in words, without the leading `yardr`, as the yard parsed it. On a run ask that sets a setting, adds a source, or applies a pack with a depot or mappings named, told to a device of the operator alone ([over the network](#over-the-network)): `[]` on any other device's card, and on every device's of a yard with no operator. The socket tells the whole card |
| `asks[].run.command` | string | the command whole, as it is typed in a terminal and as it would run. Never shorten it: a view that cannot show it whole says where the ask can be read instead. On a run ask that sets a setting, adds a source, or applies a pack with a depot or mappings named, told to a device of the operator alone ([over the network](#over-the-network)): `""` on any other device's card, and on every device's of a yard with no operator. The socket tells the whole card |
| `asks[].run.does` | array of string | what the command changes and what is there now, in the yard's words, a sentence each, with what it names as the yard knows it |
| `asks[].run.back` | string | the command that takes the change back |
| `asks[].run.back_by` | string, may be absent | who may run `back`, in words that stand before "can take it back": `only the operator` for a source's limits, `<user> or the operator` for a user's subscription and for what they are told of. Absent where that is anyone, which reads "anyone can take it back" |
| `asks[].run.ask` | array of string, may be absent | the names the approval gives values for in the clear, one field on the card each, in this order: a hook add the yard registers itself asks for its register call's asked parameters, and the approval carries them to the call. Absent on any other run ask |
| `asks[].ran` | object, may be absent | the command the yard ran when a run ask was approved, which reads "approved, ran: `<command>`" with `result` under it; the setup a setup ask approved, which reads "approved, being carried out", "approved, ran" or "approved, failed" with the preset and the names given; or the carrying out of a depot-push card, which reads "approved, to be carried out", "approved, ran" or "approved, failed". Absent on any other ask and until it is approved. A depot-push card and its `ran` are absent for devices of nonoperators; the socket tells the whole card |
| `asks[].ran.argv` | array of string | the command that ran, in words: the one the ask proposed; on a setup ask, `setup` with the pack's name. On a run ask that set a setting or added a source, told to a device of the operator alone ([over the network](#over-the-network)): `[]` on any other device's card, and on every device's of a yard with no operator. A depot-push card and its `ran` are absent for devices of nonoperators. The socket tells the whole card |
| `asks[].ran.result` | string | what the command printed, made by the yard: a line for each thing it changed, with a line break between them; on a setup ask or depot-push ask not yet carried out, `approved, to be carried out`, and afterwards what the carrying out printed. Its line `the account <names> is set in <file>` names the file the account is set in, which is the candidate's when the approval took one, and the words of a carrying out that failed can name it too, and the candidate's source: both are told to a device of the operator alone ([over the network](#over-the-network)). A depot-push result is told to a device of the operator alone too; any other device is told `the account <names> is set` for that line and, for a failure, that it failed and no more; so is every device of a yard with no operator. The socket tells the whole result |
| `asks[].ran.pending_reason` | string, may be absent | while an approved member invite or removal is still owed after a signed 429 or 503, why the edge refused its report, in the edge's words. It is cleared when the report lands or the carrying out fails, and is told to a device of the current operator alone; other devices are told none |
| `asks[].setup` | object, may be absent | the setup a setup ask proposes: the pack with the yard's sentences for what approving it does, the account to type beside the preset pick or the candidates to take instead (on an update, the account that already stands), and the presets the ask's options count. Absent on any other ask. `AnswerAsk` refuses a pick and words on it, `DeferAsk` and `DeclineAsk` take it as any ask, and it is approved with [`ApproveSetup`](#for-the-operator-alone), by the operator: at the yard (`yardr bead answer <bead> <ask> <n> --secret`, `--account <source>` where the card lists a candidate, or a bare `<n>` where the card is `given`), or from a device of the operator, but for the account typed down the link from the edge, which a yard that is not hosted refuses ([TLS is yours](#tls-is-yours-and-the-clients-rule)). A strong card ([weighty cards](#weighty-cards)) is approved from a device as any weighty card is; the bead page approves it only on a hosted yard, under a fresh sign-in. All of `setup` is the yard's, made when the ask was written, and none of it the asker's: show it apart from `question` and `context`, which are the asker's words, and draw those as plain text |
| `asks[].setup.pack` | string | the embedded pack to set up, of kind source, whose `yardr pack show <pack>` prints the presets numbered |
| `asks[].setup.base` | string, may be absent | the selected region base name; absent when calls go to the declarations' own bases |
| `asks[].setup.label` | string, may be absent | the build the pack is embedded in, as the card shows it |
| `asks[].setup.account` | array of string | the names the account is made of: the mint's inputs where the pack's credential is minted, else every credential name. A first setup types every one beside the preset pick; an update says by `given` and `missing` which of them the account file that stands holds. Where the card lists candidates, the answer names one of them instead |
| `asks[].setup.account_file` | string, may be absent | the file a typed value goes to, on the yard's machine; on an update, the account file that stands. Told to a device of the operator alone ([over the network](#over-the-network)): absent on any other device's card, and on every device's of a yard with no operator. The socket tells the whole card |
| `asks[].setup.given` | bool, may be absent | on an update, where the yard already holds the source the pack brings: `true` when its account file already holds every name of `setup.account`, so that a bare preset pick keeps it, typing every name changes it, and a candidate may be taken instead; absent on a first setup, where nothing stands yet, and where the file lacks a name (`setup.missing`) |
| `asks[].setup.missing` | array of string, may be absent | on an update, the names of `setup.account` the account file that stands does not set, where the pack newly needs them (every name where the file is not there): the answer types exactly these, and the names that stand stay, or takes a candidate; absent on a first setup and where the file holds every name |
| `asks[].setup.candidates` | array, may be absent | yard sources whose account the card offers to reuse; absent where none match. Told to a device of the operator alone ([over the network](#over-the-network)): any other device is told the card as one with none, and so is every device of a yard with no operator. The socket tells the whole card |
| `asks[].setup.candidates[].source` | string | the source whose account the card offers |
| `asks[].setup.candidates[].file` | string | the file holding the shared account inputs; told to a device of the operator alone |
| `asks[].setup.candidates[].values` | object | the shared account input names and values the file holds; told to a device of the operator alone |
| `asks[].setup.does` | array of string | what approving changes and what is there now, in the yard's words, a sentence each, with what it names as the yard knows it. The account sentence, which names the file the value goes to and, where the card lists candidates, each by source, values and file, is told to a device of the operator alone: any other device is told the account sentence of a card with none and naming no file |
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

| argument | type | |
|---|---|---|
| `viewer` | string | who reads the board: it holds no row they may not see; left out, the yard itself, which sees every bead |

```
POST /call/Arrivals
{"viewer":"jo"}

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
        "bead": "yd-greet",
        "title": "The greeting",
        "platform": "new",
        "line": "drafts",
        "expected": "—",
        "remark": "",
        "tone": "none"
      },
      {
        "bead": "yd-links",
        "title": "Move the links",
        "platform": "review",
        "line": "",
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
    "platforms": ["new", "review"]
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

The flows view: the yard's depots as lines, each with its travelling total, its flows, each flow its stages in travel order with the open beads on each and its lamp, each flow its landings of the trailing 24 hours, and its Elsewhere rows; the beads travelling with no line; and the yard-wide travelling total. A device's board holds only beads its user may see and the totals count those beads; the yard itself sees every bead. It is what footplate's timetable draws: the yard computes the view once so the readers draw without computing. Each bead's row is its Arrivals row, with whether it is worked; each stage's lamp is the panel's: none while a bead is worked with no fault of its own, fault where a session waits on a prompt or a bead lacks what its route needs, turn where only a held bead or a person's turn does, none otherwise. A flow's landings are the beads that entered merged in the trailing 24 hours, newest first, at most 30: what landed under its landed station.

| argument | type | |
|---|---|---|
| `viewer` | string | who reads the board: it holds no row they may not see; a device's viewer is its user; left out, the yard itself |

```
POST /call/Flows
{"viewer":"jo"}

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
            "group": "builders",
            "kind": "claude",
            "model": "opus",
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
                "lamp": "none",
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
                    "group": "builders",
                    "kind": "claude",
                    "model": "opus",
                    "entered_stage_at": "2026-10-08T09:30:00Z"
                  }
                ]
              },
              {
                "stage": "shipped",
                "group": "elders",
                "human": true,
                "lamp": "none",
                "terminal": false,
                "limit": 5,
                "taken": 0,
                "beads": []
              },
              {
                "stage": "merged",
                "group": "",
                "human": false,
                "lamp": "none",
                "terminal": true,
                "beads": []
              }
            ],
            "landings": [
              {
                "bead": "yd-points",
                "title": "Oil the points",
                "landed_at": "2026-10-08T09:30:00Z"
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
        "bead": "yd-links",
        "title": "Move the links",
        "expected": "—",
        "remark": "",
        "tone": "none",
        "working": true,
        "worker": "herdr links-new",
        "group": "builders",
        "kind": "claude",
        "model": "opus",
        "entered_stage_at": "2026-10-08T09:30:00Z"
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
            "group": "builders",
            "kind": "claude",
            "model": "opus",
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
                "lamp": "none",
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
                    "group": "builders",
                    "kind": "claude",
                    "model": "opus",
                    "entered_stage_at": "2026-10-08T09:30:00Z"
                  }
                ]
              },
              {
                "stage": "shipped",
                "group": "elders",
                "human": true,
                "lamp": "none",
                "terminal": false,
                "limit": 5,
                "taken": 0,
                "beads": []
              },
              {
                "stage": "merged",
                "group": "",
                "human": false,
                "lamp": "none",
                "terminal": true,
                "beads": []
              }
            ],
            "landings": [
              {
                "bead": "yd-points",
                "title": "Oil the points",
                "landed_at": "2026-10-08T09:30:00Z"
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
        "bead": "yd-links",
        "title": "Move the links",
        "expected": "—",
        "remark": "",
        "tone": "none",
        "working": true,
        "worker": "herdr links-new",
        "group": "builders",
        "kind": "claude",
        "model": "opus",
        "entered_stage_at": "2026-10-08T09:30:00Z"
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
| `depots[].flows[].stages[].lamp` | string | `none` while a bead is worked here with no fault of its own, `fault` where a session waits on a prompt or a bead lacks what its route needs, `turn` where only a held bead or a person's turn does, `none` otherwise |
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
| `depots[].flows[].stages[].beads[].group` | string, may be absent | the group working it, as the claiming session recorded it at claim; absent wherever no worker can be named, including where no session claimed it and on a manual group's claim |
| `depots[].flows[].stages[].beads[].kind` | string, may be absent | the harness kind the claiming session started with (`claude`, `codex`, ...), as it recorded it at claim, served unchanged; absent wherever no worker can be named, on a manual group's claim, and where the session recorded none (an exec or peer claim, or a session from before kinds were recorded) |
| `depots[].flows[].stages[].beads[].model` | string, may be absent | the model the claiming session started with, as it recorded it at claim, served unchanged; absent wherever no worker can be named, on a manual group's claim, and where the session recorded none |
| `depots[].flows[].stages[].beads[].entered_stage_at` | time, may be absent | when the bead entered its stage, as an RFC 3339 time; absent where no session claimed it, and where the stay has no clock |
| `depots[].flows[].landings[].bead` | string | the landed bead's handle |
| `depots[].flows[].landings[].title` | string | the landed bead's title, as it stands now |
| `depots[].flows[].landings[].landed_at` | time | when the bead entered merged, as an RFC 3339 time |
| `depots[].elsewhere[].bead` | string | the bead's handle |
| `depots[].elsewhere[].title` | string | the bead's title |
| `depots[].elsewhere[].expected` | string | the expected arrival as the yard prints it: the usual range, one span when both ends agree, `—` where the yard serves none, or the bead is blocked by an open bead |
| `depots[].elsewhere[].remark` | string | held (`held by <name>` where the yard knows who holds the bead), stalled, blocked by an open bead, waits for a person, late (`late by 40m`: how far past its usual time, in the yard's span format; plain `late` where the yard cannot tell by how much), in that order; `""` when there is none |
| `depots[].elsewhere[].tone` | string | `red` for a hold and a stall, `amber` for a block, a wait and a late mark, `none` with no remark |
| `depots[].elsewhere[].held_by` | string, may be absent | who holds a held bead, as the remark names them: the person who held it, the agent of the session or the crew member whose pane held it, `yard` where the yard held it itself, `peer:<name>` where the outcome a linked yard sent back held it. Absent where the bead is not held, and where the yard does not know who held it (a hold from before it kept the holder): the remark is then `held` |
| `depots[].elsewhere[].working` | bool | an agent's or a command's session claimed it; a manual group's claim is a person's turn and no work |
| `depots[].elsewhere[].worker` | string, may be absent | who works it, as `yardr session list` shows the claiming session: the runner with the agent's name or pid; absent where no session claimed it, and on a manual group's claim |
| `depots[].elsewhere[].group` | string, may be absent | the group working it, as the claiming session recorded it at claim; absent wherever no worker can be named, including where no session claimed it and on a manual group's claim |
| `depots[].elsewhere[].kind` | string, may be absent | the harness kind the claiming session started with (`claude`, `codex`, ...), as it recorded it at claim, served unchanged; absent wherever no worker can be named, on a manual group's claim, and where the session recorded none (an exec or peer claim, or a session from before kinds were recorded) |
| `depots[].elsewhere[].model` | string, may be absent | the model the claiming session started with, as it recorded it at claim, served unchanged; absent wherever no worker can be named, on a manual group's claim, and where the session recorded none |
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
| `unassigned[].group` | string, may be absent | the group working it, as the claiming session recorded it at claim; absent wherever no worker can be named, including where no session claimed it and on a manual group's claim |
| `unassigned[].kind` | string, may be absent | the harness kind the claiming session started with (`claude`, `codex`, ...), as it recorded it at claim, served unchanged; absent wherever no worker can be named, on a manual group's claim, and where the session recorded none (an exec or peer claim, or a session from before kinds were recorded) |
| `unassigned[].model` | string, may be absent | the model the claiming session started with, as it recorded it at claim, served unchanged; absent wherever no worker can be named, on a manual group's claim, and where the session recorded none |
| `unassigned[].entered_stage_at` | time, may be absent | when the bead entered its stage, as an RFC 3339 time; absent where no session claimed it, and where the stay has no clock |

Every depot stands here, travelling or not, and a removed depot's line leaves with it: `DeleteDepot` refuses while open beads remain. The unassigned rows are the open beads outside the backlog with no depot, the newest change first.

Read it again on every event that carries a bead, and on the events without one that move its inputs: `group_set` and `group_removed`, `route_added`, `route_set` and `route_removed`, `flow_set`, `flow_removed`, `flow_pointer_set` and `flow_pointer_cleared`, `depot_updated` and `depot_removed`, and `setting_changed` whose `key` is `max_sessions` or `operator`. One minute is the ETA's grain: no timed refresh.

### `AnswerAsk`

Answers an ask: picks an option with words alongside, or answers an ask with no options with words. It is what `yardr bead answer` does with an option or `--words`: words alone on an ask with options are refused, and go as a plain note instead. On a `bead@peer`, a question another yard asked, the answer is queued home for that yard instead of written as a note: only the person the question is for may give it, the link must carry `ask` out, and words alone on an ask with options are refused with the address to mail the asker at. A pick on a mapping ask (an ask of `Bead` with `identity`) that writes a user's identity, a user's pick or someone new's, is a weighty approval: the call is refused unless it says `weighed` ([weighty cards](#weighty-cards)), and the ask stays open. Nobody and you decide write no identity; let in once writes none either, but it answers its ask, delivers what the card held as `outside` and closes the card, so the sender's next message offers a fresh card, and no other answer asks for the word. The call returns no data; its answer is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `option` | number | the picked option's number, as the bead shows it; `0` with words answers an ask with no options |
| `words` | string | what goes with the pick, or the answer; left out, the pick stands alone |
| `actor` | string | who answers: the user's name |
| `weighed` | bool | `true` for a pick that writes a user's identity on a mapping ask, once the person has seen the card's weight and passed the client's gate; left out, `false`, and every other answer takes it so |

```
POST /call/AnswerAsk
{"bead":"yd-links","ask":3,"option":1,"words":"move them tonight","actor":"jo"}

{}
```

### `DeferAsk`

Puts the ask aside: it leaves the person's queue for 24 hours or until the asker's next note on the bead, whichever is first. It is what `yardr bead answer` does with `--later`. A plain ask, and a run, secret, setup or depot-push ask, is put aside by its addressee or an operator and refused for anyone else, naming the addressee. On a `bead@peer` it is 24 hours by this yard's clock and nothing else, and the put-aside travels home as an answer does. It answers nothing; putting it aside is then a `noted` event in the stream.

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

Ends the ask with no option picked: the person will not decide it. It is what `yardr bead answer` does with `--decline`, and reads wherever the ask is shown as "`<user>` declined to decide this: `<words>`". A plain ask, and a run, secret, setup or depot-push ask, is declined by its addressee or an operator and refused for anyone else, naming the addressee. On a `bead@peer` the decline travels home as an answer does, and ends the ask in both yards. It answers nothing; declining is then a `noted` event in the stream.

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

Puts a plain note on the bead, as `yardr bead note` does. Words about a question with no option chosen go here, and leave the ask open. On a `bead@peer`, a question another yard asked, it is refused: notes stay local, and words for the asker go as mail to `asker@peer`. With `files` the note carries files, as one written with `yardr bead note --file` does. A note with no words, empty or only white space, is refused, with files or without. It answers nothing; the note is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `text` | string | the note; it may not be empty or only white space |
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

### `DeliveryFiles`

The files a kept hook delivery carried, as the yard fetched them: a photo, a document, a voice note a chat service's message names. The source names them by its own refs and the yard fetches the bytes after it has kept and answered the delivery, so a client that reads this at the delivery's notification may read none: it reads again on the delivery's [`hook_files`](#a-hook-deliverys-files-arrived-hook_files) event. A file the yard holds is read with `GET /blobs/<hash>`; one it did not take (over `blob_max_bytes`, or its fetch failed) has `not_taken`, the reason, and no bytes to ask for. A delivery whose files were not fetched (none carried, a source with no credential set, a fetch the server's restart cut short, which is not tried again) answers `null`, a list with nothing in it, and so does a `seq` that is no delivery's or whose delivery has left the log. The files are the yard's, not one user's: a device of any user reads them, as it is sent the delivery.

| argument | type | |
|---|---|---|
| `seq` | number | the delivery's: the `seq` of its `hook` event, which is the `seq` of its notification where `kind` is `hook` |

```
POST /call/DeliveryFiles
{"seq":148}

{
  "result": [
    {
      "hash": "497790947d4666760ce38f3c00e852c71fdb66cae849bae8e9ede352719e1581",
      "type": "image/png",
      "bytes": 70
    },
    {
      "hash": "",
      "type": "",
      "bytes": 1048576,
      "name": "shed-plans.pdf",
      "not_taken": "document of 1048576 bytes, over the yard's limit of 8192"
    }
  ]
}
```

| field of the result | type | |
|---|---|---|
| `[].hash` | string | the file's name: the SHA-256 of its bytes, in hex. `GET /blobs/<hash>` answers the bytes. `""` on a file not taken |
| `[].type` | string | what the bytes say: `image/png` or `image/jpeg` for an image, `text/html; charset=utf-8` for a page. `""` on a file not taken |
| `[].bytes` | number | how many bytes it is; on a file not taken, the size the source gave, `0` where it gave none |
| `[].name` | string, may be absent | the file's base name as the source gave it; absent where it gave none, as for a photo |
| `[].not_taken` | string, may be absent | why the yard did not take the file, in its words; absent on a file the yard holds |

The list is in the order the source named the files. A file is recorded once the fetch of all the delivery's files has ended, so the list is whole when it is there at all.

<!-- /calls -->

## For the operator alone

The calls of this list are as stable as the calls above, and are an
operator's alone to make. An operator is a user whose role is `operator`
(`yardr user set <user> role=operator`, [users.md](users.md#the-record)):
there may be several, and each of them makes these calls. The `operator`
setting names one of them, the one an ask with no `--to` goes to; any
operator approves or answers it. In yardr's code the list is `OperatorCalls`, and
it has twelve calls: `GiveSecret`, which gives the credential a secret ask
asks for, `ApproveAsk`, which approves a run ask so that the yard runs
the command it holds, carrying the approval's values to a hook's register
call where the card asks for any, `ApproveSetup`, which approves a setup ask with
the preset pick and the account together: typed, taken from a candidate, or
kept as it stands, `SourceGrants`, which reads every grant of the
yard's sources, the whole of who may call what, `SessionScreen`,
which reads what a running session's pane shows, its visible screen with
the ANSI styling, `Teammates`, which reads the yard's teammates, one row
per mapped address with its user and devices, and `UninviteTeammate`,
which files a teammate's removal as a run ask on a new bead; `TelemetryShow`,
which previews the switch and next request; `TelemetrySet`, which turns
the switch on or off; and `DevicePairOpen`, which opens a pairing for one
device of one user, `DevicePairState`, which reads the open pairing back,
and `DevicePairClose`, which ends it. Each of the first three
takes `weighed`, the client's word that its person saw the card's weight,
and the yard refuses a weighty card without it ([weighty
cards](#weighty-cards)). Where they are served, and to whom:

- **On the socket**, the class table above applies; a call with `actor`
  is held to the store's caller on a pane. `UninviteTeammate` is filed as
  its caller, and `Teammates` is withheld from agent and crew panes.
- **On the [listener](#over-the-network)**, to a device of an operator:
  a request whose token is one the yard gave a device of that operator
  (`yardr user device add <operator> <device>`, or a pairing). The role is
  read at each call, so a device of a user made a member is answered as
  below from then on. `actor` is the operator or left out. The yard knows there who
  calls by the token and not by a word of the caller's; it cannot know
  whether an agent on some machine sent the request, so whoever holds
  that token gives and approves as the operator.
- **Three are the socket's alone.** `DevicePairOpen`, `DevicePairState`
  and `DevicePairClose` open, read and end a device pairing for a window
  on the yard's own machine. A device token never opens a pairing, so on
  the listener each is answered as a method the yard does not have, to a
  device of the operator too, and down the yard link from the edge alike.
- **A giving can be a user's own too.** A secret ask with `secret.user`
  asks for that user's own address in a source that tells people (for
  Pushover the user key), and one with `secret.as` for their own
  credential in a source, which a call made as them reads; the value goes
  to a file of theirs, not to a credential of the yard. On the socket it
  is given by that user or the operator, and not from the pane of an
  agent. On the listener a device of that user is served the call for such
  an ask and for no other, as a device of the operator is; `actor` is the
  user or left out. No approval is so: a run ask for a user's own
  subscription or tell is approved by that user on the socket, and
  `ApproveAsk` is not there for a device of theirs.
- **To nobody else.** A device of any other user, and a caller the yard
  knows by anything but a device token of its own, is answered as for a
  method the yard does not have (`no such method GiveSecret`, `no such
  method ApproveAsk`, `no such method SourceGrants`). So is every device
  in a yard with no operator, but for an ask with `secret.user` or
  `secret.as` that names its user. For `ApproveAsk` and
  `SourceGrants` each of them is answered before the request's body is
  read. For `GiveSecret` a caller
  without a device token is; a device of a user who is not the operator
  is read for the ask it names, and nothing of it is said back; so is
  a giving of a device of the operator down the link from the edge, unless
  the yard is hosted
  ([TLS is yours](#tls-is-yours-and-the-clients-rule)), as is an
  `ApproveSetup` of the operator's device that types the account. Against
  such an answer a client says "answer it at the yard" and sends its user
  to a terminal there (`ssh -t <host> yardr bead answer <bead> <ask>
  --secret`, `<n> --secret` for a setup, or `--approve`).

Over the listener the value travels under the TLS in front of it and under
no other protection, and the yard takes it only where it can see that the
request did not come in plain ([TLS is yours](#tls-is-yours-and-the-clients-rule)).
`SourceGrants` carries no secret and approves nothing, but the operator's
device token goes with the request, so the listener takes it only over
https or from the yard's own machine, like the other calls of this list.
It never crosses a link to another yard: a secret ask is not sent to a
peer, and is answered at the yard that asked.

What a give through a device writes says how it came, as its other writes
do: the `noted` event of the answer, and `source_set` (for a user's own
address or credential `user_set`) where the file is new, carry `data.via`.
So does what an approval through a device writes: the `noted` event of the
answer, and the event of the change the command made (`peer_added`,
`peer_allowed`, `peer_url_set`, `peer_intake`, `peer_identity_set`,
`peer_removed`, `peer_renamed`, `source_added`, `source_set`,
`source_granted`, `hook_added`, `identity_set`, `sender_unignored`,
`subscribed`, `user_set`, `user_added`, `user_removed`, `crew_defined`,
`crew_restart_requested`, `depot_updated`, `setting_changed`,
`originator_changed`, `timer_removed`), which also carries `approved` with
the bead and the ask. A `source_set` that points a source at a file or sets
its mint command names it, a `user_set` that points a user at a file names
it, and a `hook_blocked` names the file of the hook's secret: the stream
tells these to a device of the operator alone, and to any other device the
event names the source, the user or the hook and not them ([over the
network](#over-the-network)).

An approval never crosses a link to another yard either: a run ask is
approved in the yard that asked, and `ApproveAsk` refuses a `bead@peer`.
What the yard runs is the command stored on the ask, `asks[].run.command`,
and no argument of the call names or changes it. Show that command whole
and the yard's sentences (`asks[].run.does`) directly above the control
that approves, give that control a key no other card answers with, and
never approve on Return alone. Where the card asks for values in the
clear (`asks[].run.ask`), draw one field a name, shown as typed: the
approval sends them as `values`, and the yard sends them to the register
call with the URL and the secret, and keeps none. A name the card asks
for nothing of, and a missing one, refuse the approval.

A value given with `GiveSecret` is the person's secret. Read it into a
field that does not echo, send it in this call and in nothing else, and
keep it nowhere: not in a draft, a notice, a log or an error. Empty the
field when the call returns, whatever it answered.

### Weighty cards

A card the person approves rather than answers in words (a run ask, a
setup ask, a secret ask, a mapping ask) may be *weighty*: approving it
widens what reaches outside the yard, whose word or credential counts as a
person's, or what runs inside the yard. The yard weighs every card in the
write that stores it, from the card it wrote and its own records, never
from the asker's words, and puts the weight on the ask as `weight`
(`open[].weight` and `decided[].weight` of [`UserAsks`](#userasks),
`asks[].weight` of [`Bead`](#bead)): one entry a class, each with the
yard's line for it and what this card does. A routine card has none.

| class | the yard's line | the cards that carry it |
|---|---|---|
| `credential` | gives a credential or an address to the yard | every secret ask; `user key`; a setup ask, which takes the account |
| `as-user` | lets calls or words count as a person's | `source grant ... --as <user>`; a secret ask with `secret.as`; `user identity`; a mapping ask, whose picks write a user's identity |
| `outside` | lets the yard change something outside it | `source grant` of an update call, or of a preset holding one; `source set` of `calls_per_hour` or `updates_per_hour` that raises the updates a caller can make in an hour, the lower of the two; a setup ask whose presets grant an update call; `pack apply --depot` whose mappings grant an update call or let a crew member open threads |
| `code` | brings a program the yard will run | a `source add` that replaces a source, whatever it changes; `source add` and `pack apply` whose command files are new or differ from the copies that stand; a setup ask whose pack brings such files; a depot pack's first install whose plan makes files, groups or crew; `pack apply --depot` with a `--bind`, or whose mappings grant a call that sends a file; `crew add`, which starts a new agent on the role its card shows whole |
| `trust` | widens who the yard listens to | `user add`; `peer allow` adding a kind, raising the mail limit, or exposing, trusting or sending on (`--expose`, `--trust`, `--reach`); `peer add`; `peer set`; `crew set peers=` adding a peer; `hook add`; `pack apply --depot` with `--members` |
| `reads` | widens what a standing grant of read reaches | `source add` or `pack apply`, or a setup ask, that replaces a declaration with a new or changed read call while someone holds a grant of `read` of it |

A card that narrows (a revoke, a lower limit, `user rm`, `timer rm`, a
kind taken off a link) is never weighty. A later build may add a class: a
client draws one it does not know as weighty, with its line. Show the
lines first on the card, above everything the asker wrote, as `yardr bead
ask` prints them: `WEIGHTY · <class>: <line>`.

An approval (`ApproveAsk`, `ApproveSetup`, `GiveSecret`, a pick on a
mapping ask with `AnswerAsk`) weighs the card again. Where the weight is
not the one the card showed, or the card was written before cards were
weighed and weighs as weighty now, nothing runs and the call is refused
with the stale refusal: the ask stays open, to be declined and asked
again.

A weighty card is approved only by a call that says its person saw the
weight. Each of those four calls takes `weighed`, a bool that is `false`
when it is left out. `AnswerAsk` takes it for a pick on a mapping ask that
writes a user's identity, a user's pick or someone new's; a pick of
nobody or you decide writes none, and neither does let in once, which
answers and closes its card without writing one, and no other answer
asks for it. Without
`weighed` a weighty card is refused, before anything is written or run,
with the code `error` and these words, in which the classes are the
card's:

```
ask 412 is weighty (outside, as-user): approve it where its weight is shown; a client sends "weighed": true once its person has passed its gate
```

That refusal is no stale one: the card is the one that was shown, so the
ask stays open, to be approved where its weight is shown. A routine card
is approved without `weighed` as it always was, and a client from before
the argument, which leaves it out, makes the same calls it did.

A card the yard weighs `trust` or `code` is *strong*. A strong card is
approved from a device as any weighty card is: `ApproveAsk` and
`ApproveSetup` take it on the listener from a device of the operator,
with `weighed`, and the answer's event records `via` and no `witness`.
What a strong card brings (a program the yard runs, a link, a user or an
endpoint the yard listens to) stands after the device is removed
([users.md](users.md#devices) says what that costs). The bead page asks
more: on a hosted yard it approves a strong card under a fresh sign-in.
Approving starts the gateway re-authentication unless the session's last
authentication was a strong-card re-authentication by the operator inside
the gateway's window, and the answer's event records `witness: fresh
sign-in`. On the socket a strong card is approved as any weighty card is,
and records `witness: terminal`. A self-hosted yard's bead page approves
no strong card, and names the terminal command.

The word is the client's. The yard knows who calls by the operator's
token or shell, and whoever holds either can send `weighed`, so the yard
checks what a client says and verifies nothing about the person. What the
check buys is that a client which has never heard of weight (an older
window, a request saved before this, a script written before it) cannot
approve a weighty card by accident, and that every client which does send
the word was written knowing it promises a gate. The gate is the
client's, and it sends `weighed: true` after the gate and never as a
default:

- **`yardr bead answer`** at a terminal prints the card's `WEIGHTY` lines
  and asks for the ask's number typed back, before it reads a value, and
  sends `weighed` on a match and stops on anything else. Without a
  terminal (standard input is not one) it declines a weighty approval,
  and its words name footplate and a terminal. No flag gets round that,
  since a gate that a script walks through is not a gate: a person
  approves a weighty card in footplate or in a terminal. The terminal is
  the yard's own machine and its shell is the operator's, so this is
  friction against approving on reflex and no authentication.
- **`yardr chat`**'s field for a secret asks for the ask's number typed
  back the same way, and then for the value.
- **`yardr web`** keeps the terminal's gate for a card that is weighty but
  not strong: the card's `WEIGHTY` lines, then a field for the ask's
  number typed back, and `weighed` on a match alone. It takes no secret,
  since a view may be plain HTTP, and approves a strong card only on a
  hosted yard, under a fresh sign-in.
- **A client with no gate** sends none, and so cannot approve a weighty
  card: it says `this card is weighty, and this window cannot approve it.
  Approve it in footplate, or in a terminal on the yard's machine:` with
  the `yardr bead answer` line of the card, and leaves the card open.
  Nothing is declined for the person.

A weighty card is approved alone, by a call that names it, and never in a
batch. No call approves several cards today. A call that does, if one is
ever added, refuses each weighty ask in it by name and approves the rest.

<!-- local: made by the test of internal/rpc from what a yard answers, not by hand; YARDR_UPDATE_GOLDEN=1 go test ./internal/rpc -run TestAPIDoc writes it again -->

### `GiveSecret`

Answers a secret ask (an ask of `Bead` with `secret`): the values go into the file the source's credential is read from, and the answer on the bead holds the names and never a value. A value is kept in that file and nowhere else: not in the store, an event, a note or the log, and no call reads one back. It is what `yardr bead answer --secret` does. It is the operator's to give: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. An ask with `secret.user` asks for that user's own address in the source instead: the value goes into the user's `env_file`, or with none set into `users/<user>.env` in the yard's home, which is then set; it is that user's to give or the operator's, on the socket and from a device of either, and it does not set the user's `notify`. An ask with `secret.crew` asks for that crew member's own credential in the source instead: the value goes into the crew member's `env_file`, or with none set into `crew/<name>.env` in the yard's home, which is then set; it is the operator's to give, on the socket and from a device of the operator, restarts nothing, and takes effect when the crew member next starts. An ask with `secret.harness` asks for that harness kind's API key instead: the names come from the kind's `login_env`; the values go into the operator's key file of the kind, `harness/<kind>/<operator>.env` in the yard's home, which every session of the kind on the operator's login reads as it starts (the crew, groups without `login_for`, the operator's conversations), and no crew member's `env_file` is set. It is the operator's to give, on the socket and from a device of the operator; it is kept in that file and not in the store, an event, a note or the log. Giving it restarts the sessions of that kind on the operator's login waiting at the login screen, and new sessions use the key. An ask with `secret.as` asks for that user's own credential in the source instead: the value goes into the user's `env_file`, or with none set into `users/<user>.env` in the yard's home, which is then set; it is that user's to give or the operator's, on the socket and from a device of either, and the source needs no `notify` key. An ask with `secret.login` asks for the input of the harness login that waits under that reference instead: the value is handed to the waiting login and kept nowhere — not in a file, the store, an event, a note or the log — and the answer holds the names as any giving does; it is the operator's to give, on the socket and from a device of the operator, and is refused when no login waits under the reference. With `secret.as` beside it the login is that user's own (the sign-in `yardr user add --login` leads for them): the code its sign-in page shows is theirs to give, or the operator's for them, on the socket and from a device of either. A wait is the login of one user, recorded when its leader registers it (the user whose login the group it is led for carries, the operator's for a login led with no group), so a giving is refused, and the login left waiting, when the ask is for another user's login: `secret.as`, else the user it is asked of. Refused too: an ask that is declined or taken back, names that are not exactly `asks[].secret.env`, an empty value, and one with a line break or white space at either end; a refusal never repeats a value. Giving again replaces what was given. Every secret ask is weighty ([weighty cards](#weighty-cards)): the call is refused unless it says `weighed`, before anything is written, and the ask stays open. The answer is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `values` | object | each name of `asks[].secret.env` to the value the person typed |
| `actor` | string | who gives: the operator's name, or for an ask with `secret.user` or `secret.as` (a login's too) that user's |
| `weighed` | bool | `true` once the person has seen the card's weight and passed the client's gate; left out, `false`, and a weighty card, which every secret ask is, is refused |

```
POST /call/GiveSecret
{"bead":"yd-crm","ask":18,"values":{"CRM_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo","weighed":true}

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
{"bead":"yd-crm","ask":19,"values":{"CRM_TOKEN":"what-the-person-typed"},"actor":"jo","weighed":true}

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
{"bead":"yd-crm","ask":25,"values":{"PHONE_USER":"what-the-person-typed"},"actor":"jo","weighed":true}

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
{"bead":"yd-crm","ask":34,"values":{"INPUT":"what-the-person-typed"},"actor":"jo","weighed":true}

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
{"bead":"yd-crm","ask":37,"values":{"CRM_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo","weighed":true}

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
{"bead":"yd-crm","ask":40,"values":{"CRM_TOKEN":"what-the-person-typed"},"actor":"jo","weighed":true}

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
{"bead":"yd-crm","ask":54,"values":{"ANTHROPIC_API_KEY":"what-the-person-typed"},"actor":"jo","weighed":true}

{
  "result": {
    "given": ["ANTHROPIC_API_KEY"],
    "file": "/home/jo/.yardr/harness/claude/jo.env",
    "user": "jo",
    "harness": "claude",
    "next": "every Claude Code session that burns jo's login reads it from its next start",
    "credential": "ANTHROPIC_API_KEY is set in /home/jo/.yardr/harness/claude/jo.env"
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
| `user` | string, may be absent | whose own address was given, and whose file `file` is; with `harness`, whose login's key file `file` is; absent for a source's credential |
| `crew` | string, may be absent | whose own credential was given, and whose file `file` is; absent for a source's credential, a user's address and a harness key |
| `harness` | string, may be absent | the harness kind whose API key was given; absent for any other secret |
| `as` | string, may be absent | whose own credential was given, and whose file `file` is; absent for a source's credential and for a user's address |
| `next` | string, may be absent | with `user`: the command that comes next, in a line to show as it is: the one that tells the user through the source, or once that is set the one that sends a test message; with `crew` or `harness`: when the value takes effect, in a line to show as it is; with `as`: the command that tries it as that user, in a line to show as it is; absent for a source's credential |

The file and the store are two writes. When the yard wrote the file and could not record it, the error's message says so with the names and the file, and the ask is open still: give it again. When the call ends without a reply (the server went away), the value may be written: say so, never "sent", and give it again.

### `ApproveAsk`

Approves a run ask (an ask of `Bead` with `run`), and the yard runs the command the ask holds, as the one who approves: `run.command`, whole, and nothing else. It is what `yardr bead answer --approve` does, with `--value <name>=<value>` where the card asks for values in the clear (`run.ask`): a hook add the yard registers itself carries them to its register call with the URL and the secret, and keeps none; where the declaration names a check, a retry first lists what stands and adopts a webhook already aimed at the hook. In one write the yard checks that it would write the same card now, makes the change under the command's own check, and records the answer, which holds `ran`. A refused approval changes nothing, records nothing and leaves the ask open. It is the operator's to approve: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. A command that is a user's own (`subscribe <user> yardr`, `user set <user> tell=<parts>`, `user key <user> <source>`) is approved by the one it was asked of, that user or the operator, on the socket; a device of that user is not served the call. Refused too: an ask that proposes no command; one that was declined, taken back or approved already, since a command runs once for one ask; a `bead@peer`, since a command is approved in the yard that asked and never over a link; values the card asks for nothing of, and a missing one where it asks for any; and an ask whose card is from before the yard changed, where what the command names was replaced under its name or what it changes is no longer what `run.does` says. That one is declined, and asked again if it is still wanted. A weighty card ([weighty cards](#weighty-cards)) is refused unless the call says `weighed`; that refusal changes nothing and leaves the ask open, as it is. A routine card asks for no such word. A strong card — one the yard weighs `trust` or `code` ([weighty cards](#weighty-cards)) — is approved from a device as any weighty card is, with `weighed`: the command runs, and the answer's event carries `via` and no `data.witness`. On a hosted yard the bead page approves it only under a fresh sign-in, which the answer's event records (`data.witness`); a stale witness, or another user's, refuses with the ask open. The answer is then a `noted` event in the stream, and the change is the command's own event, which carries `approved` with the bead and the ask.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `actor` | string | who approves: the operator's name, or for a command that is a user's own the name of the one it was asked of |
| `weighed` | bool | `true` once the person has seen the card's weight and passed the client's gate; left out, `false`, and a weighty card is refused. A routine card takes it so |
| `values` | object | each name of `run.ask` to the value the person gave in the clear; left out where the card asks for none |

```
POST /call/ApproveAsk
{"bead":"yd-crm","ask":28,"actor":"jo","weighed":true}

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

The change and the answer are one write: the command ran and the ask is approved, or neither. For a depot-push ask, the approval records `ran` as pending; the server then checks the card, source and token again before writing the assembly command and the depot's local Git config. It verifies both writes, never runs `git push`, and updates `ran` to say whether it ran or failed. A depot-push card and its answer are sent to the current operator alone on the listener, and its approval sentence is held from nonoperator devices in the event stream. When the call ends without a reply (the server went away), a client cannot know which: say "the yard did not answer; read the card again", never "approved", and read `Bead` again, where `asks[].ran` is there once the command ran.

### `ApproveSetup`

Approves a setup ask (an ask of `Bead` with `setup`), and the yard then applies the pack, takes the account, and grants the preset picked, as the operator: `setup.pack`, the account `setup.account`, and the preset's grants to `setup.to`. It is what `yardr bead answer <n> --secret` (or `--account <source>`, or a bare `<n>` where the card is `given`) does. The request settles the account in one of three ways: it types it, takes a candidate the card lists, or keeps the account that stands. What the card says (`setup.given`, `setup.missing`, `setup.candidates`) decides which are open to it, and the paragraph under the reply's fields says how. The examples are, in order, a first setup with the account typed, a first setup with a candidate taken, an update that keeps the account that stands, and an update that types the names it is missing. In one write the yard checks that it would write the same card now and records the approval, which holds the preset, the names given, the candidate taken or the account kept, and `ran`; the server carries it out after the call. Typed values live in its memory alone until the account's write; a picked candidate hands none. Where the pack's source names a check call, the typed account is tried with it before the write: a refused check refuses the approval with the check's words. A refused approval changes nothing, records nothing and leaves the ask open. It is the operator's to approve: on the socket it is refused for anyone else and for a call that comes from the pane of an agent, whatever `actor` says, and on the listener it is there for a device of the operator and for no other caller. Down the link from the edge the account typed (`values`) is a secret, and only a hosted yard takes it: any other yard refuses it in the words that send the person to the yard's own machine (`yardr bead answer <bead> <ask> <n> --secret`), and takes a candidate taken and the account kept ([TLS is yours](#tls-is-yours-and-the-clients-rule)). Picking `you decide` grants the asker's recommended preset, and is refused where none is recommended. Refused too: an ask that sets up no pack; one that was declined, taken back or approved already, since a setup runs once for one ask; a `bead@peer`, since a setup is approved in the yard that asked and never over a link; values and a candidate together; values that are not exactly the names the card asks for, and a candidate the card lists none of; and an ask whose card is from before the yard changed, where the pack's bytes or presets, a source, the account's owed-ness, a candidate or a grantee moved. That one is declined, and asked again if it is still wanted. A newer build alone is no such change. A setup takes the account, so its card is always weighty ([weighty cards](#weighty-cards)): the call is refused unless it says `weighed`, with nothing changed and the ask open. A setup whose pack brings a program the yard will run is a strong card, and a device approves a strong card as it does any weighty one, with `weighed`: the answer's event carries `via` and no `data.witness`. On a hosted yard the bead page approves it only under a fresh sign-in, which the answer's event records (`data.witness`); a stale witness, or another user's, refuses with the ask open. The answer is then a `noted` event in the stream.

| argument | type | |
|---|---|---|
| `bead` | string | the bead |
| `ask` | number | the ask note's seq |
| `option` | number | the preset pick, counting `asks[].options` as shown, `you decide` last |
| `values` | object | each name the person typed, with its value: every name of `setup.account` on a first setup, the names of `setup.missing` alone on an update that has any, and on an update that is `given` every name of `setup.account`, to change the account that stands; left out where a candidate is taken or the account is kept, and refused down the link from the edge by a yard that is not hosted |
| `account` | string | the candidate taken instead of typing, one the card lists (`setup.candidates[].source`); left out where the values are typed or the account is kept |
| `actor` | string | who approves: the operator's name |
| `weighed` | bool | `true` once the person has seen the card's weight and passed the client's gate; left out, `false`, and a weighty card, which every setup is, is refused |

```
POST /call/ApproveSetup
{"bead":"yd-crm","ask":44,"option":1,"values":{"COURIER_ACCOUNT":"what-the-person-typed"},"actor":"jo","weighed":true}

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

```
POST /call/ApproveSetup
{"bead":"yd-crm","ask":45,"option":1,"account":"courier-existing","actor":"jo","weighed":true}

{
  "result": {
    "bead": "yd-crm",
    "ask": 45,
    "asker": "yardmaster",
    "pack": "courier",
    "preset": "read",
    "account": "courier-existing",
    "result": "approved, to be carried out"
  }
}
```

```
POST /call/ApproveSetup
{"bead":"yd-crm","ask":46,"option":1,"actor":"jo","weighed":true}

{
  "result": {
    "bead": "yd-crm",
    "ask": 46,
    "asker": "yardmaster",
    "pack": "ledger",
    "preset": "read",
    "keep": true,
    "result": "approved, to be carried out"
  }
}
```

```
POST /call/ApproveSetup
{"bead":"yd-crm","ask":47,"option":1,"values":{"PARCEL_UPDATE_TOKEN":"what-the-person-typed"},"actor":"jo","weighed":true}

{
  "result": {
    "bead": "yd-crm",
    "ask": 47,
    "asker": "yardmaster",
    "pack": "parcel",
    "preset": "read",
    "given": ["PARCEL_UPDATE_TOKEN"],
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
| `given` | array of string, may be absent | the account names that were typed; absent when a candidate was taken or the account was kept |
| `account` | string, may be absent | the candidate taken: its `source` in `setup.candidates`; absent when the account was typed or kept |
| `keep` | bool, may be absent | `true` when the account that stands was kept with a bare preset pick; absent when it was typed or a candidate was taken |
| `result` | string | `approved, to be carried out`: the server carries the setup out after the call |

What the card holds decides the account. On a first setup, neither `setup.given` nor `setup.missing`, nothing stands yet: the request types every name of `setup.account` in `values`, or names a candidate in `account`. On an update the account file that stands is `setup.account_file`. When it holds every name (`setup.given`), the request sends neither `values` nor `account` to keep it, which the reply says as `keep`, or types every name of `setup.account` to change it, or names a candidate. When it lacks some (`setup.missing`), the request types exactly those names and the others stay where they are, or names a candidate. The reply has one of `given` (the names typed), `account` (the candidate taken) and `keep`.

The approval and the answer are one write: the ask is approved, or neither. The carrying out follows after the call: while `ran.result` is `approved, to be carried out` the setup reads as being carried out, and as ran or failed once the server finishes it; read `Bead` again for it. When the call ends without a reply (the server went away), a client cannot know whether the approval landed: say "the yard did not answer; read the card again", never "approved", and read `Bead` again. An open ask is approved again; an approved one is left for the server, whose failure names the account asked again.

### `SourceGrants`

Every grant of the yard's sources: who may make which calls of each, and as whom, with when, by whom and on which card each call was given. It is what `yardr source grants` prints. It changes nothing. A record is one source, one holder (a group or a crew member) and one user called as, `as`, absent for a plain grant: a holder's plain grant and its grant as a user are two records. `calls` holds `read`, the grant of every read call of the source, and the names of calls granted one by one, an update call always so. `kinds` maps each entry of `calls` to its kind, `read` or `update`, as the source's file declares it, for a client that has not the file: `read` is `read`, and a call granted by name has the kind its declaration gives it, so a read call granted by name is not taken for an update. A call whose kind the yard cannot tell has no entry, because its source is gone, its file is one this yard does not read, or the file no longer has the call: take it for an update, as a client does that does not read `kinds`. `given` maps each entry of `calls` to that call's own grant: `at` and `by`, and where it was approved on a card `bead`, `ask` and `asker`, who asked it, absent where the ask's note is gone. A call granted before the yard kept `given`, whose history was pruned by then, has no entry: show it as not kept, and never with the record's `at` and `by`, which are when the record last gained a call and who gave it, for no call in particular. It is the operator's to read: on the socket it is a read, served as every read of the store is to a caller on the yard's machine, and on the listener it is there for a device of the operator and for no other caller, since the whole grant graph is theirs. It changes nothing, but the operator's device token goes with the request, so the listener takes it only over https or from the yard's own machine, like the other calls of this list.

| argument | type | |
|---|---|---|
| `source` | string | the source whose grants are read; empty or left out for every source's |

```
POST /call/SourceGrants
{}

{
  "result": [
    {
      "source": "crm",
      "crew": "yardmaster",
      "as": "jo",
      "calls": ["read"],
      "kinds": {
        "read": "read"
      },
      "at": "2026-10-08T09:30:00Z",
      "by": "jo",
      "given": {
        "read": {
          "at": "2026-10-08T09:30:00Z",
          "by": "jo"
        }
      }
    },
    {
      "source": "crm",
      "group": "builders",
      "calls": ["put", "read"],
      "kinds": {
        "put": "update",
        "read": "read"
      },
      "at": "2026-10-08T09:30:00Z",
      "by": "jo",
      "given": {
        "put": {
          "at": "2026-10-08T09:30:00Z",
          "by": "jo",
          "bead": "yd-crm",
          "ask": 57,
          "asker": "yardmaster"
        },
        "read": {
          "at": "2026-10-08T09:30:00Z",
          "by": "jo"
        }
      }
    }
  ]
}
```

| field of the result | type | |
|---|---|---|
| `[].source` | string | the source |
| `[].group` | string, may be absent | the group that holds it, each session it starts; absent for a crew member's |
| `[].crew` | string, may be absent | the crew member that holds it; absent for a group's |
| `[].as` | string, may be absent | the user whose credential a call under it is made with; absent for a plain grant |
| `[].calls` | array of string | `read` and the names of calls granted, sorted |
| `[].kinds` | object, may be absent | each entry of `calls` to its kind, `read` or `update`, as the source's file declares it, keyed by the entry; a call with no key is one whose kind the yard cannot tell (its source is gone, its file is one this yard does not read, or the file no longer has the call): take it for an update. A record with none has no `kinds` |
| `[].at` | time | when the record last gained a call: the record's, for no call in particular |
| `[].by` | string | who gave the last call added to the record; it is the record's, for no call in particular |
| `[].given` | object, may be absent | each entry of `calls` to its own grant, keyed by the entry; a call with no key was granted before its history was kept, and a record with none has no `given` |
| `[].given{}.at` | time | when the call was granted |
| `[].given{}.by` | string | who granted it |
| `[].given{}.bead` | string, may be absent | the bead of the card it was approved on; absent for a grant given at a terminal |
| `[].given{}.ask` | number, may be absent | the ask it was approved on; absent for a grant at a terminal |
| `[].given{}.asker` | string, may be absent | who asked it; absent for a grant at a terminal, and where the ask's note is gone |

### `SessionScreen`

Reads what a running session's pane shows: its visible screen with the terminal's ANSI styling, so the operator sees whether something hangs (a dialog, a login screen, a spinner). `session` is a session id, the herdr agent name of a live session (starting or running), or a crew member's name, in that order; the agent name is the name the board shows, and a crew member's is its own session's screen. The read is passive: it moves nothing in the pane, on the alternate screen or off it. The reply is the screen alone: no width or height rides with it, and a client that draws it measures them itself. It is the operator's to read: on the socket the operator's alone, and on the listener there for a device of the operator and for no other caller. A screen can show what an agent printed, a secret among it, so the listener takes it only over https or from the yard's own machine, like the other calls of this list, and down the link from the edge from a paired device of the operator, as an approval ([edge.md](edge.md#footplate-through-the-link)).

| argument | type | |
|---|---|---|
| `session` | string | a session id, a live session's herdr agent name, or a crew member's name for its own session's screen |

```
POST /call/SessionScreen
{"session":"yardmaster"}

{
  "result": {
    "screen": "\u001b[1mYardr yardmaster\u001b[0m\n❯ "
  }
}
```

| field of the result | type | |
|---|---|---|
| `screen` | string | the pane's visible screen, with the terminal's ANSI styling |

### `Teammates`

The yard's teammates: one row per address of the standing `web_jwt_users` mapping, each with its yard user name, role, email, who invited them and when, and their devices with last use. A row with devices is paired; a row without is waiting. Whether anyone signed in over the edge's web door is not told: nothing at the yard records it. It is the operator's to read: on the socket it is an operator-class call, refused from a bead agent's or crew member's pane, and on the listener it is there for a device of the operator and for no other caller, since the mapping is theirs. It changes nothing, but the operator's device token goes with the request, so the listener takes it only over https or from the yard's own machine, like the other calls of this list, and down the link from the edge from a paired device of the operator, as an approval ([edge.md](edge.md#footplate-through-the-link)).

```
POST /call/Teammates
{}

{
  "result": [
    {
      "name": "hal",
      "role": "member",
      "email": "hal@example.net",
      "invited_by": "jo",
      "invited_at": "2026-10-08T09:30:00Z",
      "devices": []
    },
    {
      "name": "sam",
      "role": "member",
      "email": "sam@example.net",
      "invited_by": "jo",
      "invited_at": "2026-10-08T09:30:00Z",
      "devices": [
        {
          "name": "tablet",
          "last_used": "2026-10-08T09:30:00Z"
        }
      ]
    }
  ]
}
```

| field of the result | type | |
|---|---|---|
| `[].name` | string | the teammate's yard user name |
| `[].role` | string | their role: `operator` or `member` |
| `[].email` | string | the mapped address they sign in with at the edge |
| `[].invited_by` | string | who invited them |
| `[].invited_at` | time | when they were invited |
| `[].devices` | array | their devices, by name; empty while none is paired |
| `[].devices[].name` | string | the device's name |
| `[].devices[].last_used` | time, may be absent | when its token was last resolved; absent on a device never used |

A mapped address with no user record stands in no row.

### `UninviteTeammate`

Files the removal of a teammate: the run ask `user uninvite <user>` on a new bead, addressed to the yard's operator. It files the ask and no more: approval, the edge-first removal and the retry are the card's built path, and approving from the same device uses `ApproveAsk`. It refuses a user the yard has not, one no mapping names, and the caller's own user. On the socket it files as its actor; on the listener it is there for a device of the operator and for no other caller, and the actor is the device's user or left out. The operator's device token goes with the request, so the listener takes it only over https or from the yard's own machine, like the other calls of this list, and down the link from the edge from a paired device of the operator, as an approval ([edge.md](edge.md#footplate-through-the-link)).

| argument | type | |
|---|---|---|
| `user` | string | the teammate whose removal is filed |
| `actor` | string | who files: the operator's name on the socket; a device calls as its user |

```
POST /call/UninviteTeammate
{"user":"sam","actor":"jo"}

{
  "result": {
    "bead": "yd-uninvite",
    "ask": 85
  }
}
```

| field of the result | type | |
|---|---|---|
| `bead` | string | the new bead the ask stands on |
| `ask` | number | the ask's seq on its bead |

The bead and the ask are one write: the removal is filed on a new bead, or neither. When the call ends without a reply (the server went away), a client cannot know which: say "the yard did not answer; read the queue again", never "filed", and read `UserAsks` again.

### `DevicePairOpen`

Opens a pairing for one device of one user, the opening `yardr user device pair <user> <device>` makes: the reply's code is good for 10 minutes and for one device, and 5 wrong codes end the pairing. A yard has one such pairing open at a time: opening while one lives, for a user the yard has not, or for a device the user has, is refused. It is the operator's to open: on the socket its Admin class refuses a bead's agent's pane, and the store's ActAddDevice gate refuses anyone but the operator, whatever `actor` says. A device token never opens a pairing: on the listener the call is answered as a method the yard does not have, to a device of the operator too, and down the yard link from the edge alike.

| argument | type | |
|---|---|---|
| `user` | string | the user the pairing is for |
| `name` | string | the device's name |
| `actor` | string | who opens: the operator's name |

```
POST /call/DevicePairOpen
{"user":"sam","name":"laptop","actor":"jo"}

{
  "result": {
    "user": "sam",
    "device": "laptop",
    "state": "open",
    "expires": "2026-10-08T09:30:00Z",
    "code": "K7QM-2XPD"
  }
}
```

| field of the result | type | |
|---|---|---|
| `user` | string | the user the pairing is for |
| `device` | string | the device's name |
| `state` | string | `open` |
| `code` | string | the code the device calls with, as `yardr user device pair` shows it |
| `expires` | time | when the code runs out |

When the call ends without a reply (the server went away), a client cannot know whether the pairing opened: say "the yard did not answer; read the pairing again", never "open", and read `DevicePairState` again.

### `DevicePairState`

Reads the yard's open device pairing back, so a window reopened shows the same pairing again: the device, the code as it was shown when it opened, when it runs out, and the QR string a phone scans in place of typing. The QR string is `<address>#<code>#<yard>`, the yard's `edge` address with its handle/name there; with no edge setting the yard names no address and `qr` is empty, and the code is given where it is asked for. With no pairing open the error is `not-found`. It is the operator's to read: on the socket its Operator class refuses agent and crew panes. A device token never opens a pairing: on the listener the call is answered as a method the yard does not have, to a device of the operator too, and down the yard link from the edge alike.

```
POST /call/DevicePairState
{}

{
  "result": {
    "user": "sam",
    "device": "laptop",
    "state": "open",
    "expires": "2026-10-08T09:30:00Z",
    "code": "K7QM-2XPD",
    "qr": "https://edge.example.com#K7QM-2XPD#jo/edgeyard"
  }
}
```

| field of the result | type | |
|---|---|---|
| `user` | string | the user the pairing is for |
| `device` | string | the device's name |
| `state` | string | `open` |
| `code` | string | the code the device calls with, as it was shown when it opened |
| `expires` | time | when the code runs out |
| `qr` | string | what a QR code of the pairing holds: `<address>#<code>#<yard>`; empty while the yard is connected to no edge |

### `DevicePairClose`

Ends the yard's open device pairing, as `yardr user device pair --end` does: nothing is added, and the code is good for nothing then. With no pairing open the error is `not-found`. It is the operator's to end: on the socket its Operator class refuses agent and crew panes, and the store's ActEndDevicePairing gate refuses anyone but the operator, whatever `actor` says. A device token never opens a pairing: on the listener the call is answered as a method the yard does not have, to a device of the operator too, and down the yard link from the edge alike.

| argument | type | |
|---|---|---|
| `actor` | string | who ends it: the operator's name |

```
POST /call/DevicePairClose
{"actor":"jo"}

{
  "result": {
    "user": "sam",
    "device": "laptop",
    "state": "ended",
    "expires": "2026-10-08T09:30:00Z"
  }
}
```

| field of the result | type | |
|---|---|---|
| `user` | string | the user the pairing was for |
| `device` | string | the device's name |
| `state` | string | `ended` |
| `expires` | time | when the code would have run out |

### `TelemetryShow`

Reads what `yardr telemetry show` prints: the switch, the install id, and the exact next request the daemon would send from the current state, without sending it or advancing anything. The request is the sending-day runtime snapshot with the eligible completed source days in the batch; `source_days` holds those days, oldest first, and is absent while none are eligible — a grant counts from tomorrow on, so a yard that turned on today has none yet. The request carries the shared project's public, write-only key as `api_key`, which the example does not print. It is the operator's to read: on the socket its Operator class keeps it from agent and crew panes; on the listener it is there for a device of the operator and for no other caller, since the id groups the yard's events across time. It changes nothing, but the id and the operator's device token go with the reply and the request, so the listener takes it only over https or from the yard's own machine, like the other calls of this list. As a device call it is never served down the yard link from the edge; the hosted browser uses the separate `web` link.

```
POST /call/TelemetryShow
{}

{
  "result": {
    "install_id": "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
    "telemetry": "on",
    "since": "2026-10-08",
    "sending_day": "2026-10-08",
    "hosted": false,
    "request": {
      "batch": [
        {
          "event": "yardr_daily",
          "distinct_id": "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
          "timestamp": "2026-10-08T09:30:00Z",
          "uuid": "0e3c8b2a-1432-4a51-9f39-1bc7a1b1d001",
          "properties": {
            "$geoip_disable": true,
            "$process_person_profile": true,
            "os": "darwin",
            "telemetry_age_days": 0,
            "version": "development"
          }
        },
        {
          "event": "yardr_crew_day",
          "distinct_id": "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
          "timestamp": "2026-10-08T09:30:00Z",
          "uuid": "451d5fc1-4a12-4c20-9d7b-1f9df88ca004",
          "properties": {
            "$geoip_disable": true,
            "$process_person_profile": true,
            "hosted": false,
            "kind": "claude",
            "model": "opus",
            "running_crew_sessions": 1
          }
        },
        {
          "event": "yardr_crew_day",
          "distinct_id": "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
          "timestamp": "2026-10-08T09:30:00Z",
          "uuid": "f481958d-ea90-4a3c-8ed7-0a1ff35af004",
          "properties": {
            "$geoip_disable": true,
            "$process_person_profile": true,
            "hosted": false,
            "kind": "claude",
            "model": "sonnet",
            "running_crew_sessions": 1
          }
        }
      ]
    }
  }
}
```

| field of the result | type | |
|---|---|---|
| `install_id` | string, may be absent | the yard's random install id, as PostHog sees it; absent while none is minted. This id is pseudonymous, not anonymous: anyone the operator gives it to can find this yard's events |
| `telemetry` | string | `on` or `off` |
| `since` | string, may be absent | the UTC date of the latest consent grant; absent while none was given |
| `sending_day` | string | the UTC day the batch is built for |
| `hosted` | bool | whether the yard is a hosted one, which sends no daily snapshot |
| `request` | object or null | the exact next request; null when there is no batch to send, and `no_batch` says why |
| `request.batch[].event` | string | `yardr_daily`, `yardr_harness_day`, `yardr_crew_day` or `yardr_failure_day` |
| `request.batch[].distinct_id` | string | the install id again, as PostHog's `distinct_id` |
| `request.batch[].timestamp` | time | the event's day marker: midnight UTC on the sending day for a snapshot, noon UTC on the source day for a count, or the consent grant where it was given that day |
| `request.batch[].uuid` | string | the event's stable id, derived from the install id, the event, the day and its dimensions: rebuilding the same event keeps it, so a retried batch deduplicates |
| `request.batch[].properties` | object | the event's allow-listed properties: counts and fixed names only, never a bead, an error's words or an actor's name |
| `request.batch[].properties.$process_person_profile` | bool | always `true`: the id-only person profile the deletion path removes |
| `request.batch[].properties.$geoip_disable` | bool | always `true`: the project adds no GeoIP location to the event |
| `request.batch[].properties.version` | string, may be absent | a daily's release tag, or `development` for any build without one; on a daily alone |
| `request.batch[].properties.os` | string, may be absent | a daily's `darwin`, `linux`, `windows` or `other`; on a daily alone |
| `request.batch[].properties.telemetry_age_days` | number, may be absent | a daily's whole UTC days from first consent through the sending day; on a daily alone |
| `request.batch[].properties.kind` | string, may be absent | a harness, crew or failure day's harness kind, or the failure kind `gave_up`, `held` or `landing_failed`; on those alone |
| `request.batch[].properties.model` | string, may be absent | a harness or crew day's model id, `other` for one the harness file does not offer, `default` for the harness's own; on those alone |
| `request.batch[].properties.running_crew_sessions` | number, may be absent | a crew day's live sessions of the pair; on a crew day alone |
| `request.batch[].properties.hosted` | bool, may be absent | whether the yard is a hosted one, from the setting on the sending day; on a harness, crew or failure day alone |

When there is no batch `request` is null and `no_batch` says why: `off` until telemetry is enabled, `attempted` until the next UTC day, `empty` where a hosted yard has nothing to send, `no-id` where the setting stands on with no install id. A harness day carries `sessions_claimed` beside `kind`, `model` and `hosted`, and a failure day `count` beside its `kind` and `hosted`; the example's batch holds a daily and two crew days, since the yard turned on today.

### `TelemetrySet`

Turns telemetry on or off, as `yardr telemetry on` and `off` do: through the same functions, so consent bookkeeping (`eligible_from`, grant and withdrawal times) is the same however the switch is turned. On mints the install id first when none exists, records the grant, and counts only whole days from tomorrow on, so activity from before consent is never included; repeating on leaves `eligible_from` alone. Off records the withdrawal and stops new batches from starting, and keeps the id, so on again is the same yard. It is the operator's to change: on the socket its Operator class refuses agent and crew panes, and the store's ActChangeSetting gate refuses anyone but the operator, whatever `actor` says; on the listener it is there for a device of the operator and for no other caller. This device call changes consent only over https or from the yard's own machine, like the other calls of this list; it is refused down the yard link from the edge and from the router's peer. The hosted browser control is served through the separate `web` link after the view verifies the JWT and maps it to an operator.

| argument | type | |
|---|---|---|
| `on` | bool | `true` turns on, `false` turns off; left out reads as `false`, so only an explicit `true` turns on |
| `actor` | string | who changes: the operator's name |

```
POST /call/TelemetrySet
{"on":false,"actor":"jo"}

{
  "result": {
    "telemetry": "off",
    "changed": true,
    "install_id": "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
    "since": "2026-10-08",
    "eligible_from": "2026-10-09",
    "endpoint": "https://eu.i.posthog.com/batch/"
  }
}
```

```
POST /call/TelemetrySet
{"on":true,"actor":"jo"}

{
  "result": {
    "telemetry": "on",
    "changed": true,
    "install_id": "3f6d1c2e-9a4b-4e7a-b5c1-2d8f0a6e4b90",
    "since": "2026-10-08",
    "eligible_from": "2026-10-09",
    "endpoint": "https://eu.i.posthog.com/batch/"
  }
}
```

| field of the result | type | |
|---|---|---|
| `telemetry` | string | `on` or `off`, as it stands now |
| `changed` | bool | whether the switch moved |
| `install_id` | string, may be absent | the yard's install id; absent while none is minted |
| `since` | string, may be absent | the UTC date of the latest consent grant; absent while none was given |
| `eligible_from` | string, may be absent | the first full UTC day the latest grant counts from; absent while none was given |
| `endpoint` | string | where the daemon posts the daily batch |

Deleting `<home>/telemetry.json` forgets the install id, a separate deliberate act; the next on mints a new one. To have the yard's data deleted, the operator sends the project the install id.

<!-- /local -->

## What is stable

Stable, for a client to build on:

- the socket's path and the form of a call, a reply, an error and the
  stream, as above, and `GET /blobs/<hash>`;
- the paths under `/user/` on the listener, the header, `/user/me` and its
  four fields, the 401 and the 404, as [Over the
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
